// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestLB_SecurityGroupIDs_InternalPersists — INTERNAL-LB сохраняет набор
// security_group_ids и читает его обратно (round-trip text[]-колонки).
func TestLB_SecurityGroupIDs_InternalPersists(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01SGID1234567890ll", "internal-sg-lb")
	lb.Type = domain.LBTypeInternal
	lb.NetworkID = "enp01NETWORK1234567x"
	lb.SecurityGroupIDs = []domain.SecurityGroupID{"sgp01AAAAAA1234567xx", "sgp01BBBBBB1234567xx"}

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		assert.Equal(t, lb.SecurityGroupIDs, rec.SecurityGroupIDs)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Equal(t, []domain.SecurityGroupID{"sgp01AAAAAA1234567xx", "sgp01BBBBBB1234567xx"}, got.SecurityGroupIDs)
}

// TestLB_SecurityGroupIDs_EmptyIsNil — INTERNAL-LB без SG читается как nil-набор
// (пустой text[] '{}' → nil, паритет proto-семантики).
func TestLB_SecurityGroupIDs_EmptyIsNil(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01SGEMPTY12345678", "internal-nosg")
	lb.Type = domain.LBTypeInternal
	lb.NetworkID = "enp01NETWORK1234567x"

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Empty(t, got.SecurityGroupIDs)
}

// TestLB_SecurityGroupIDs_CheckRejectsExternalWithSG — DB CHECK
// (load_balancers_sg_internal_check) отвергает EXTERNAL с непустым набором SG
// (23514 → ErrInvalidArg) — defense-in-depth поверх sync-валидации.
func TestLB_SecurityGroupIDs_CheckRejectsExternalWithSG(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01SGEXT12345678ll", "edge-bad-sg")
	lb.Type = domain.LBTypeExternal // EXTERNAL не имеет сети → SG невалидны
	lb.SecurityGroupIDs = []domain.SecurityGroupID{"sgp01AAAAAA1234567xx"}

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().Insert(ctx, lb)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrInvalidArg), "want ErrInvalidArg, got %v", err)
}

// TestLB_SecurityGroupIDs_ConcurrentReplace_NoTornState — два конкурентных
// full-replace набора SG → итоговое состояние равно ровно одному из входных
// наборов целиком (без перемешивания). Атомарность single-statement UPDATE
// text[]-колонки + row-lock сериализуют writer'ов.
func TestLB_SecurityGroupIDs_ConcurrentReplace_NoTornState(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01SGCONC123456789", "internal-sg-conc")
	lb.Type = domain.LBTypeInternal
	lb.NetworkID = "enp01NETWORK1234567x"
	lb.SecurityGroupIDs = []domain.SecurityGroupID{"sgp01INIT00000000xxx"}
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	setA := []domain.SecurityGroupID{"sgp01AAAAAA0000001xx", "sgp01AAAAAA0000002xx"}
	setB := []domain.SecurityGroupID{"sgp01BBBBBB0000001xx", "sgp01BBBBBB0000002xx", "sgp01BBBBBB0000003xx"}

	errc := make(chan error, 2)
	var wg sync.WaitGroup
	replace := func(set []domain.SecurityGroupID) {
		defer wg.Done()
		upd := *lb
		upd.SecurityGroupIDs = set
		w, err := repo.Writer(ctx)
		if err != nil {
			errc <- err
			return
		}
		defer w.Abort()
		if _, err := w.LoadBalancers().Update(ctx, &upd); err != nil {
			errc <- err
			return
		}
		errc <- w.Commit()
	}
	wg.Add(2)
	go replace(setA)
	go replace(setB)
	wg.Wait()
	close(errc)
	for err := range errc {
		require.NoError(t, err)
	}

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	if !domain.SecurityGroupIDsEqual(got.SecurityGroupIDs, setA) &&
		!domain.SecurityGroupIDsEqual(got.SecurityGroupIDs, setB) {
		t.Fatalf("torn state: got %v; want exactly setA=%v or setB=%v", got.SecurityGroupIDs, setA, setB)
	}
}
