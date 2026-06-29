// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestLB_NetworkID_InternalPersists — INTERNAL-LB сохраняет network_id и читает
// его обратно (round-trip колонки network_id).
func TestLB_NetworkID_InternalPersists(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01NETID123456789l", "internal-lb")
	lb.Type = domain.LBTypeInternal
	lb.NetworkID = "enp01NETWORK1234567x"

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		assert.Equal(t, domain.NetworkID("enp01NETWORK1234567x"), rec.NetworkID)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Equal(t, domain.LBTypeInternal, got.Type)
	assert.Equal(t, domain.NetworkID("enp01NETWORK1234567x"), got.NetworkID)
}

// TestLB_NetworkID_CheckRejectsExternalWithNetwork — DB CHECK
// (load_balancers_network_id_scheme_check) отвергает EXTERNAL с network_id
// (23514 → ErrInvalidArg) — defense-in-depth поверх sync-валидации.
func TestLB_NetworkID_CheckRejectsExternalWithNetwork(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01NETEXT123456789", "edge-bad")
	lb.Type = domain.LBTypeExternal
	lb.NetworkID = "enp01NETWORK1234567x" // EXTERNAL не должен нести network_id

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().Insert(ctx, lb)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrInvalidArg), "want ErrInvalidArg, got %v", err)
}

// TestLB_NetworkID_CheckRejectsInternalWithoutNetwork — DB CHECK отвергает
// INTERNAL без network_id (23514 → ErrInvalidArg).
func TestLB_NetworkID_CheckRejectsInternalWithoutNetwork(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01NETINT123456789", "internal-bad")
	lb.Type = domain.LBTypeInternal
	lb.NetworkID = "" // INTERNAL обязан нести network_id

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().Insert(ctx, lb)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrInvalidArg), "want ErrInvalidArg, got %v", err)
}
