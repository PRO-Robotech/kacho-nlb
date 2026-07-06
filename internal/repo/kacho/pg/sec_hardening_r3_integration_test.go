// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// waitForLockWaiter deterministically blocks until at least one backend is
// waiting on a lock (pg_stat_activity.wait_event_type='Lock'), proving the
// concurrent goroutine has actually reached and is blocked on the contended row
// lock. Replaces a fixed `time.Sleep(600ms)` barrier that could pass vacuously
// under CI/host load if the goroutine had not yet issued its locking query
// before the main TX committed and released the lock — the intended race window
// then never opened and a genuine lost-update/cross-project-attach regression
// slipped through green (CWE-367). `observer` must be a pool
// distinct from the transactions under test. Fails the test if no waiter appears
// within deadline.
func waitForLockWaiter(t *testing.T, ctx context.Context, observer *pgxpool.Pool, deadline time.Duration) {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		var n int
		err := observer.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity
			  WHERE wait_event_type = 'Lock' AND state = 'active'`).Scan(&n)
		require.NoError(t, err)
		if n >= 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout: no backend became blocked on a lock within %v — the race window never opened", deadline)
}

// newObserverPool opens a standalone pool (separate from the tx-under-test pool)
// for pg_stat_activity introspection in the deterministic lock-wait helper.
func newObserverPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	p, err := coredb.NewPool(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

// =============================================================================
// TargetGroup.MoveProject TOCTOU — cross-project attach.
//
// The model invariant "an attached TargetGroup must share the LoadBalancer's
// project" is enforced by the Attach INSERT...SELECT project/region JOIN. But an
// UNGUARDED TG.MoveProject can interleave with a concurrent Attach: Move updates
// tg.project (uncommitted) while Attach's plain (non-locking) join still reads
// the stale committed project → both commit → cross-project attach that no
// FK/CHECK rejects. The fix is two-sided and DB-level:
//   - TG.MoveProject: atomic CAS guard `WHERE NOT EXISTS(attached_target_groups
//     WHERE target_group_id=$1)` (closes the attach-committed-first ordering);
//   - Attach INSERT...SELECT: `FOR NO KEY UPDATE OF lb, tg` locking read so a
//     concurrent Move blocks it and EvalPlanQual re-evaluates the project JOIN
//     against the freshly-committed project (closes the move-first ordering).
// =============================================================================

// TestTGMoveProject_BlockedByAttachedTG_Atomic — TG.MoveProject must atomically
// refuse while the TG is attached to any LB (mirror of the LB-side guard).
func TestTGMoveProject_BlockedByAttachedTG_Atomic(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const srcPrj = "prj0TGMVBLK234567890l"
	lb := newLB(srcPrj, "tgmvblk-lb")
	tg := newTG(srcPrj, "tgmvblk-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		_, _, err = w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0)
		require.NoError(t, err)
	})

	// Move to a different project must be refused while the TG is attached.
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.TargetGroups().MoveProject(ctx, string(tg.ID), "prj0TGMVOTHER67890lll")
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition),
		"attached TG move must be FailedPrecondition, got %v", err)

	// Project unchanged.
	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	got, err := rd.TargetGroups().Get(ctx, string(tg.ID))
	require.NoError(t, err)
	assert.Equal(t, domain.ProjectID(srcPrj), got.ProjectID,
		"project must be unchanged after refused move")
}

// TestTGMoveProject_Allowed_NoAttach — without any attach, move proceeds.
func TestTGMoveProject_Allowed_NoAttach(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj0TGMVOK234567890ll", "tgmvok-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		moved, err := w.TargetGroups().MoveProject(ctx, string(tg.ID), "prj0TGMVOK2234567890l")
		require.NoError(t, err)
		assert.Equal(t, domain.ProjectID("prj0TGMVOK2234567890l"), moved.ProjectID)
	})
}

// TestTGMoveProject_NotFound — missing TG → NotFound (not FailedPrecondition).
func TestTGMoveProject_NotFound(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.TargetGroups().MoveProject(ctx, "tgrMISSING1234567890", "prj0TGMVX2234567890ll")
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrNotFound), "missing TG → NotFound, got %v", err)
}

// TestTGMoveAttach_MoveFirst_NoCrossProject — the dangerous interleaving:
// TG.Move executes and holds its (uncommitted) row lock FIRST, then a concurrent
// Attach starts. With the two-sided DB guard, Attach must block on the tg row,
// then re-evaluate the project JOIN against the committed post-move project and
// refuse — never producing a cross-project attach.
func TestTGMoveAttach_MoveFirst_NoCrossProject(t *testing.T) {
	dsn := setupTestDB(t)
	repo, cleanup := newRepo(t, dsn)
	defer cleanup()
	observer := newObserverPool(t, dsn)
	ctx := context.Background()

	const srcPrj = "prj0TGMV1234567890lll"
	const dstPrj = "prj0TGMV2234567890lll"
	lb := newLB(srcPrj, "tgmvrace-lb")
	tg := newTG(srcPrj, "tgmvrace-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	// TX-move: execute the guarded UPDATE (row lock held, uncommitted).
	wMove, err := repo.Writer(ctx)
	require.NoError(t, err)
	_, moveErr := wMove.TargetGroups().MoveProject(ctx, string(tg.ID), dstPrj)

	// TX-attach runs concurrently and must block on the tg row wMove holds.
	attachCh := make(chan error, 1)
	go func() {
		wAttach, aerr := repo.Writer(ctx)
		if aerr != nil {
			attachCh <- aerr
			return
		}
		defer wAttach.Abort()
		_, _, e := wAttach.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0)
		if e == nil {
			e = wAttach.Commit()
		}
		attachCh <- e
	}()

	// Deterministically wait until the attach goroutine has actually reached and
	// blocked on the tg row lock wMove holds — proving the two TX overlap before
	// we release the lock (replaces a fixed 600ms sleep).
	waitForLockWaiter(t, ctx, observer, 10*time.Second)
	if moveErr == nil {
		require.NoError(t, wMove.Commit())
	} else {
		wMove.Abort()
	}
	attachErr := <-attachCh

	// Invariant: any surviving attach row must share the LB's post-op project.
	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	gotTG, err := rd.TargetGroups().Get(ctx, string(tg.ID))
	require.NoError(t, err)
	gotLB, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	attached, err := rd.AttachedTargetGroups().ListByLB(ctx, string(lb.ID))
	require.NoError(t, err)
	if len(attached) > 0 {
		assert.Equal(t, gotLB.ProjectID, gotTG.ProjectID,
			"attached TG must share the LB project — no cross-project attach (attachErr=%v)", attachErr)
	}
}

// TestTGMoveAttach_Race_Concurrent — probabilistic variant: concurrent Move and
// Attach must never yield a cross-project attach, whichever wins.
func TestTGMoveAttach_Race_Concurrent(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const srcPrj = "prj0TGRC1234567890lll"
	const dstPrj = "prj0TGRC2234567890lll"
	lb := newLB(srcPrj, "tgrc-lb")
	tg := newTG(srcPrj, "tgrc-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w, err := repo.Writer(ctx)
		if err != nil {
			return
		}
		defer w.Abort()
		if _, err := w.TargetGroups().MoveProject(ctx, string(tg.ID), dstPrj); err == nil {
			_ = w.Commit()
		}
	}()
	go func() {
		defer wg.Done()
		w, err := repo.Writer(ctx)
		if err != nil {
			return
		}
		defer w.Abort()
		if _, _, err := w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0); err == nil {
			_ = w.Commit()
		}
	}()
	wg.Wait()

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	gotTG, err := rd.TargetGroups().Get(ctx, string(tg.ID))
	require.NoError(t, err)
	attached, err := rd.AttachedTargetGroups().ListByLB(ctx, string(lb.ID))
	require.NoError(t, err)
	if len(attached) > 0 {
		assert.Equal(t, domain.ProjectID(srcPrj), gotTG.ProjectID,
			"if attach won, TG must NOT have moved — attached TG shares LB (src) project")
	}
}

// =============================================================================
// lb_status_recompute trigger must not clobber a concurrent
// explicit status transition. The trigger's final write must be a CAS against
// the status snapshot it validated its guard on.
// =============================================================================

// TestLBStatusRecompute_PreservesConcurrentStop — LB is ACTIVE. A detach fires
// lb_status_recompute (which would recompute to INACTIVE) concurrently with an
// explicit ACTIVE→STOPPING transition. The explicit STOPPING must survive: the
// recompute UPDATE is status-guarded and must not overwrite it.
func TestLBStatusRecompute_PreservesConcurrentStop(t *testing.T) {
	dsn := setupTestDB(t)
	repo, cleanup := newRepo(t, dsn)
	defer cleanup()
	observer := newObserverPool(t, dsn)
	ctx := context.Background()

	const prj = "prj0RECOMP234567890ll"
	lb := newLB(prj, "recomp-lb")
	tg := newTG(prj, "recomp-tg")
	lst := newListener(lb.ID, prj, "recomp-lst", 80)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, lst)
		require.NoError(t, err)
		// Attach → recompute (listener + attach present) drives INACTIVE→ACTIVE.
		_, _, err = w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0)
		require.NoError(t, err)
	})

	// Precondition: LB is ACTIVE (recompute fired on attach).
	rd0, _ := repo.Reader(ctx)
	got0, err := rd0.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	_ = rd0.Close()
	require.Equal(t, domain.LBStatusActive, got0.Status, "setup must leave LB ACTIVE")

	// TX-status: explicit ACTIVE→STOPPING (holds the lb row lock, uncommitted).
	wStat, err := repo.Writer(ctx)
	require.NoError(t, err)
	_, statErr := wStat.LoadBalancers().SetStatusCAS(ctx, string(lb.ID),
		domain.LBStatusActive, domain.LBStatusStopping)
	require.NoError(t, statErr)

	// TX-detach: DELETE attach → fires recompute → its status-guarded UPDATE must
	// block on the lb row, then re-evaluate and NOT clobber the STOPPING commit.
	detachCh := make(chan error, 1)
	go func() {
		wDet, derr := repo.Writer(ctx)
		if derr != nil {
			detachCh <- derr
			return
		}
		defer wDet.Abort()
		e := wDet.AttachedTargetGroups().Detach(ctx, string(lb.ID), string(tg.ID))
		if e == nil {
			e = wDet.Commit()
		}
		detachCh <- e
	}()

	// Deterministically wait until the detach goroutine's recompute UPDATE is
	// actually blocked on the lb row lock wStat holds — proving overlap before we
	// commit STOPPING (replaces a fixed 600ms sleep).
	waitForLockWaiter(t, ctx, observer, 10*time.Second)
	require.NoError(t, wStat.Commit())
	require.NoError(t, <-detachCh)

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Equal(t, domain.LBStatusStopping, got.Status,
		"explicit STOPPING must survive a concurrent recompute (no lost-update clobber)")
}

// NOTE on listener region-VIP uniqueness: the partial UNIQUE
// listeners_region_vip_uniq that existed in migration 0001 was DELIBERATELY
// dropped in migration 0009 ("VIP-уникальность переехала на LoadBalancer"). VIP
// uniqueness is now a LoadBalancer-level invariant enforced by
// load_balancers_region_v4_uniq / _v6_uniq, and is race-tested in
// load_balancer_vip_concurrent_integration_test.go. There is therefore no
// listener-level region-VIP invariant left to test; the corrected/dead comment
// in listener_integration_test.go and docs/architecture/known-divergences.md
// record this by-design move.
