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

// TestListener_CRUD — базовый CRUD + Get/List через ListByLB.
func TestListener_CRUD(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01LSTC1234567890ll", "parent-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	l := newListener(lb.ID, string(lb.ProjectID), "lst-a", 8080)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.Listeners().Insert(ctx, l)
		require.NoError(t, err)
		assert.Equal(t, l.ID, rec.ID)
		assert.Equal(t, lb.ID, rec.LoadBalancerID)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	page, _, err := rd.Listeners().ListByLB(ctx, string(lb.ID), kacho.Pagination{})
	require.NoError(t, err)
	require.Len(t, page, 1)
	assert.Equal(t, l.ID, page[0].ID)
}

// TestListener_UniquePortProto — UNIQUE (lb_id, port, protocol).
func TestListener_UniquePortProto(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01LSTU1234567890ll", "uni-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, newListener(lb.ID, string(lb.ProjectID), "lst-1", 80))
		require.NoError(t, err)
	})

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	dup := newListener(lb.ID, string(lb.ProjectID), "lst-2", 80) // same port+proto, different name
	dup.AllocatedAddress = "203.0.113.20"
	_, err = w.Listeners().Insert(ctx, dup)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrAlreadyExists), "got %v", err)
}

// TestListener_RegionVipUnique_RaceTest — partial UNIQUE
// (region_id, allocated_address, port, protocol) WHERE status<>'DELETING'.
// Race-сценарий: 2 goroutines пытаются insert'нуть один и тот же VIP/port.
// Ровно одна успешна.
func TestListener_RegionVipUnique_RaceTest(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb1 := newLB("prj01LSTR1234567890ll", "race-lb-1")
	lb2 := newLB("prj01LSTR1234567890ll", "race-lb-2")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb1)
		require.NoError(t, err)
		_, err = w.LoadBalancers().Insert(ctx, lb2)
		require.NoError(t, err)
	})

	var wg sync.WaitGroup
	var successes, conflicts int
	var mu sync.Mutex
	insert := func(parent domain.ResourceID, name string) {
		defer wg.Done()
		l := newListener(parent, string(lb1.ProjectID), name, 80)
		l.AllocatedAddress = "203.0.113.42" // SAME VIP
		w, err := repo.Writer(ctx)
		if err != nil {
			return
		}
		defer w.Abort()
		if _, err := w.Listeners().Insert(ctx, l); err != nil {
			mu.Lock()
			if errors.Is(err, kacho.ErrAlreadyExists) {
				conflicts++
			}
			mu.Unlock()
			return
		}
		if err := w.Commit(); err != nil {
			mu.Lock()
			if errors.Is(err, kacho.ErrAlreadyExists) {
				conflicts++
			}
			mu.Unlock()
			return
		}
		mu.Lock()
		successes++
		mu.Unlock()
	}

	wg.Add(2)
	go insert(lb1.ID, "race-lst-1")
	go insert(lb2.ID, "race-lst-2")
	wg.Wait()

	assert.Equal(t, 1, successes, "exactly one VIP-allocation succeeds")
	assert.Equal(t, 1, conflicts, "the other gets ErrAlreadyExists")
}

// TestListener_PortOutOfRange — CHECK port BETWEEN 1 AND 65535.
func TestListener_PortOutOfRange(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01LSTP1234567890ll", "port-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	l := newListener(lb.ID, string(lb.ProjectID), "bad-port", 0) // 0 not in 1..65535
	_, err = w.Listeners().Insert(ctx, l)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrInvalidArg), "got %v", err)
}

// TestListener_SetStatusCAS — atomic CAS transitions.
func TestListener_SetStatusCAS(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01LSTS1234567890ll", "cas-lb")
	l := newListener(lb.ID, string(lb.ProjectID), "cas-lst", 9090)
	l.Status = domain.ListenerStatusCreating
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, l)
		require.NoError(t, err)
	})

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.Listeners().SetStatusCAS(ctx, string(l.ID),
			domain.ListenerStatusCreating, domain.ListenerStatusActive)
		require.NoError(t, err)
		assert.Equal(t, domain.ListenerStatusActive, rec.Status)
	})

	// CAS-miss.
	w, _ := repo.Writer(ctx)
	defer w.Abort()
	_, err := w.Listeners().SetStatusCAS(ctx, string(l.ID),
		domain.ListenerStatusCreating, domain.ListenerStatusDeleting)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition))
}

// TestListener_Delete — successful delete + outbox emit.
func TestListener_Delete(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01LSTD1234567890ll", "del-lb")
	l := newListener(lb.ID, string(lb.ProjectID), "del-lst", 9876)

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, l)
		require.NoError(t, err)
	})

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		err := w.Listeners().Delete(ctx, string(l.ID))
		require.NoError(t, err)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	_, err := rd.Listeners().Get(ctx, string(l.ID))
	assert.True(t, errors.Is(err, kacho.ErrNotFound))
}

// TestListener_MoveProject_Cascade — каскад от LB.MoveProject обновляет
// project_id у всех Listener'ов в той же TX.
func TestListener_MoveProject_Cascade(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const srcProject = "prj01MVSS1234567890ll"
	const dstProject = "prj01MVDD1234567890ll"

	lb := newLB(srcProject, "move-lb")
	l := newListener(lb.ID, srcProject, "move-lst", 5000)

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, l)
		require.NoError(t, err)
	})

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().MoveProject(ctx, string(lb.ID), dstProject)
		require.NoError(t, err)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	lst, err := rd.Listeners().Get(ctx, string(l.ID))
	require.NoError(t, err)
	assert.Equal(t, domain.ProjectID(dstProject), lst.ProjectID,
		"caskaded UPDATE moved listener project_id")
}
