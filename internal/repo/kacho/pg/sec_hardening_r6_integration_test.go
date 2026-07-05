// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// sec_hardening_r6_integration_test.go — 6th-audit contract-safe hardening.
//
//   - DATA #2: Listener.Insert must source project_id/region_id atomically from
//     the parent LB under `FOR NO KEY UPDATE OF lb`, serializing with
//     LoadBalancer.MoveProject so a listener can never persist a stale
//     (pre-move) project_id whose parent LB has moved to another project.
//   - TEST #6: the (load_balancer_id, port, protocol) UNIQUE invariant needs a
//     concurrent-goroutine exactly-one-winner race test (data-integrity #10,
//     checklist item 5), not only the single-threaded duplicate-insert case.
package pg_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestListenerCreate_MoveFirst_ProjectConsistent — the dangerous interleaving
// (audit DATA #2): LoadBalancer.MoveProject executes and holds its (uncommitted)
// FOR NO KEY UPDATE row lock FIRST, then a concurrent Listener.Insert starts.
// With the locking-read INSERT...SELECT guard, the insert must block on the LB
// row, then EvalPlanQual re-reads the committed post-move project — the listener
// persists with the LB's FINAL project_id, never the stale pre-move one.
func TestListenerCreate_MoveFirst_ProjectConsistent(t *testing.T) {
	dsn := setupTestDB(t)
	repo, cleanup := newRepo(t, dsn)
	defer cleanup()
	observer := newObserverPool(t, dsn)
	ctx := context.Background()

	const srcPrj = "prj0LSMV1234567890lll"
	const dstPrj = "prj0LSMV2234567890lll"
	lb := newLB(srcPrj, "lsmvrace-lb") // no attached TGs → Move is allowed
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	// TX-move: execute the guarded UPDATE + listener cascade (row lock held,
	// uncommitted).
	wMove, err := repo.Writer(ctx)
	require.NoError(t, err)
	_, moveErr := wMove.LoadBalancers().MoveProject(ctx, string(lb.ID), dstPrj)

	// TX-insert runs concurrently: the listener is built with the STALE
	// (pre-move) project_id — exactly the value a sync-phase snapshot carries.
	// The fix ignores it and sources project_id from the locked LB row.
	stale := newListener(lb.ID, srcPrj, "lsmvrace-lst", 8080)
	insCh := make(chan error, 1)
	go func() {
		wIns, ierr := repo.Writer(ctx)
		if ierr != nil {
			insCh <- ierr
			return
		}
		defer wIns.Abort()
		_, e := wIns.Listeners().Insert(ctx, stale)
		if e == nil {
			e = wIns.Commit()
		}
		insCh <- e
	}()

	// Deterministically wait until the insert goroutine is blocked on the LB row
	// lock wMove holds — proves the two TX overlap before we release the lock (no
	// fixed sleep). Without the FOR NO KEY UPDATE guard the plain INSERT takes only
	// FK KEY SHARE (no conflict), never blocks, and this times out — RED.
	waitForLockWaiter(t, ctx, observer, 10*time.Second)
	if moveErr == nil {
		require.NoError(t, wMove.Commit())
	} else {
		wMove.Abort()
	}
	require.NoError(t, <-insCh)

	// Invariant: the persisted listener's project_id equals its LB's final project.
	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	gotLB, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Equal(t, dstPrj, string(gotLB.ProjectID), "precondition: LB moved to dst project")
	gotLst, err := rd.Listeners().Get(ctx, string(stale.ID))
	require.NoError(t, err)
	assert.Equal(t, string(gotLB.ProjectID), string(gotLst.ProjectID),
		"listener project_id must equal its LB final project — no stale denorm (moveErr=%v)", moveErr)
	assert.Equal(t, string(gotLB.RegionID), string(gotLst.RegionID),
		"listener region_id must also mirror the LB row")
}

// TestListenerInsert_ConcurrentUniquePortProto_ExactlyOneWinner — concurrent
// race test for the (load_balancer_id, port, protocol) UNIQUE invariant (audit
// TEST #6, data-integrity #10 checklist item 5). Two writer-TX opened before a
// start barrier both Insert a listener with identical (lb, port=80, TCP): exactly
// one commits, the other gets ErrAlreadyExists, final row count == 1.
func TestListenerInsert_ConcurrentUniquePortProto_ExactlyOneWinner(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj0LSUC1234567890lll", "lsuc-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	const n = 2
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(n)
	done.Add(n)

	var mu sync.Mutex
	winners, conflicts := 0, 0
	var others []error

	record := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			winners++
		case errors.Is(err, kacho.ErrAlreadyExists):
			conflicts++
		default:
			others = append(others, err)
		}
	}

	for i := 0; i < n; i++ {
		lst := newListener(lb.ID, string(lb.ProjectID), "lsuc-lst", 80) // same port+proto
		go func() {
			defer done.Done()
			w, err := repo.Writer(ctx)
			if err != nil {
				ready.Done()
				record(err)
				return
			}
			committed := false
			defer func() {
				if !committed {
					w.Abort()
				}
			}()
			ready.Done()
			<-start // all writer-TX open (BeginTx eager) before any touches the table

			if _, err := w.Listeners().Insert(ctx, lst); err != nil {
				record(err)
				return
			}
			if err := w.Commit(); err != nil {
				record(err)
				return
			}
			committed = true
			record(nil)
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()

	require.Empty(t, others, "unexpected non-conflict errors: %v", others)
	assert.Equal(t, 1, winners, "exactly one Insert must win the (lb,port,proto) UNIQUE race")
	assert.Equal(t, 1, conflicts, "the other Insert must lose with ErrAlreadyExists")

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	page, _, err := rd.Listeners().ListByLB(ctx, string(lb.ID), kacho.Pagination{})
	require.NoError(t, err)
	assert.Len(t, page, 1, "exactly one listener row must persist")
}
