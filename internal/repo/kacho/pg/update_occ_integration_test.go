// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// update_occ_integration_test.go — optimistic-concurrency (xmin) coverage for the
// mutable-field Update of LoadBalancer / Listener / TargetGroup (guards against
// lost update on concurrent partial-mask Update).
//
// Before the fix writer.Update issued `UPDATE ... WHERE id=$1` and rewrote ALL
// mutable columns from a stale read-modify-write snapshot, so two concurrent
// Updates with DISJOINT masks silently reverted each other's non-masked fields
// (e.g. an Update that renames reverts a concurrently-set deletion_protection).
// Update now carries `WHERE id=$1 AND xmin::text=$exp`: a writer whose snapshot
// is stale (row changed since its Get) affects 0 rows → FailedPrecondition and
// must re-read+retry, so no field is lost.
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

// TestLB_Update_OCC_NoLostUpdate — the security-relevant scenario: a rename must
// NOT revert a concurrently-enabled deletion_protection. Deterministic: two
// "clients" share ONE read snapshot (xmin); the second, applying its change on
// the stale snapshot, is rejected and retried correctly → both changes survive.
func TestLB_Update_OCC_NoLostUpdate(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01OCCLB000000001l", "occ-lb")
	lb.DeletionProtection = false
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	// Both clients read the SAME snapshot (classic read-modify-write).
	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	snap, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	_ = rd.Close()
	require.NotEmpty(t, snap.Xmin, "Get must surface an xmin OCC token")
	sharedXmin := snap.Xmin

	// Client A: enable deletion_protection (mask=[deletion_protection]).
	aObj := snap.LoadBalancer
	aObj.DeletionProtection = true
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, uerr := w.LoadBalancers().Update(ctx, &aObj, sharedXmin)
		require.NoError(t, uerr, "first writer with a valid xmin must succeed")
	})

	// Client B: rename, built on the STALE snapshot (protection still false) with
	// the now-stale sharedXmin. Must be rejected — else it clobbers A's protection.
	bObj := snap.LoadBalancer
	bObj.Name = domain.LbName("occ-lb-renamed")
	wB, err := repo.Writer(ctx)
	require.NoError(t, err)
	_, bErr := wB.LoadBalancers().Update(ctx, &bObj, sharedXmin)
	wB.Abort()
	require.Error(t, bErr)
	require.True(t, errors.Is(bErr, kacho.ErrFailedPrecondition),
		"stale-xmin Update must be FailedPrecondition (OCC guard), got %v", bErr)

	// Retry B correctly: re-read fresh state, re-apply rename, Update fresh xmin.
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		cur, gerr := w.LoadBalancers().Get(ctx, string(lb.ID))
		require.NoError(t, gerr)
		fresh := cur.LoadBalancer
		fresh.Name = domain.LbName("occ-lb-renamed")
		_, uerr := w.LoadBalancers().Update(ctx, &fresh, cur.Xmin)
		require.NoError(t, uerr)
	})

	// No lost update: BOTH protection (A) and rename (B) survive.
	rd2, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd2.Close() }()
	final, err := rd2.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.True(t, final.DeletionProtection, "A's deletion_protection must NOT be reverted by B's rename")
	assert.Equal(t, domain.LbName("occ-lb-renamed"), final.Name, "B's rename must be applied")
}

// TestLB_Update_OCC_ConcurrentExactlyOneWins — real goroutine race: two Updates
// sharing one xmin snapshot; exactly one commits, the other is OCC-rejected (never
// a silent double-success that would lose a field). Errors are collected and
// asserted on the MAIN goroutine (never require.* inside a child goroutine).
func TestLB_Update_OCC_ConcurrentExactlyOneWins(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01OCCLB000000002l", "occ-conc")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	snap, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	_ = rd.Close()
	sharedXmin := snap.Xmin

	mutate := []func(*domain.LoadBalancer){
		func(o *domain.LoadBalancer) { o.DeletionProtection = true },
		func(o *domain.LoadBalancer) { o.Name = domain.LbName("occ-conc-renamed") },
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			obj := snap.LoadBalancer
			mutate[idx](&obj)
			w, werr := repo.Writer(ctx)
			if werr != nil {
				errs[idx] = werr
				return
			}
			if _, uerr := w.LoadBalancers().Update(ctx, &obj, sharedXmin); uerr != nil {
				w.Abort()
				errs[idx] = uerr
				return
			}
			errs[idx] = w.Commit()
		}(i)
	}
	wg.Wait()

	nSuccess, nConflict := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			nSuccess++
		case errors.Is(e, kacho.ErrFailedPrecondition):
			nConflict++
		default:
			t.Fatalf("unexpected Update error: %v", e)
		}
	}
	assert.Equal(t, 1, nSuccess, "exactly one concurrent Update commits")
	assert.Equal(t, 1, nConflict, "the other is OCC-rejected (no silent lost update)")
}

// TestListener_Update_OCC_StaleXminConflict — listener mutable-field Update honours
// the xmin CAS: a second write from a stale snapshot → FailedPrecondition.
func TestListener_Update_OCC_StaleXminConflict(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01OCCLST00000001l", "occ-lst-lb")
	lst := newListener(lb.ID, "prj01OCCLST00000001l", "occ-lst", 443)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, lst)
		require.NoError(t, err)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	snap, err := rd.Listeners().Get(ctx, string(lst.ID))
	require.NoError(t, err)
	_ = rd.Close()
	sharedXmin := snap.Xmin

	// First writer (valid xmin) commits — bumps the row's xmin.
	first := snap.Listener
	first.Description = "occ-v1"
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, uerr := w.Listeners().Update(ctx, &first, sharedXmin)
		require.NoError(t, uerr)
	})

	// Second writer with the now-stale xmin → FailedPrecondition.
	second := snap.Listener
	second.Description = "occ-v2"
	wB, err := repo.Writer(ctx)
	require.NoError(t, err)
	_, bErr := wB.Listeners().Update(ctx, &second, sharedXmin)
	wB.Abort()
	require.Error(t, bErr)
	assert.True(t, errors.Is(bErr, kacho.ErrFailedPrecondition),
		"stale-xmin listener Update must be FailedPrecondition, got %v", bErr)
}

// TestTargetGroup_Update_OCC_StaleXminConflict — target-group mutable-field Update
// honours the xmin CAS.
func TestTargetGroup_Update_OCC_StaleXminConflict(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj01OCCTG000000001l", "occ-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	snap, err := rd.TargetGroups().Get(ctx, string(tg.ID))
	require.NoError(t, err)
	_ = rd.Close()
	sharedXmin := snap.Xmin

	first := snap.TargetGroup
	first.DeregistrationDelaySeconds = 111
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, uerr := w.TargetGroups().Update(ctx, &first, sharedXmin)
		require.NoError(t, uerr)
	})

	second := snap.TargetGroup
	second.DeregistrationDelaySeconds = 222
	wB, err := repo.Writer(ctx)
	require.NoError(t, err)
	_, bErr := wB.TargetGroups().Update(ctx, &second, sharedXmin)
	wB.Abort()
	require.Error(t, bErr)
	assert.True(t, errors.Is(bErr, kacho.ErrFailedPrecondition),
		"stale-xmin target-group Update must be FailedPrecondition, got %v", bErr)
}

// TestListener_Update_OCC_ConcurrentExactlyOneWins — real goroutine race for the
// listener xmin CAS (parity with TestLB_Update_OCC_ConcurrentExactlyOneWins): two
// Updates share one xmin snapshot; exactly one commits, the other is OCC-rejected
// (never a silent double-success that would lose a field). The xmin CAS SQL is the
// same shape across resources, but each contested write path
// carries its own concurrent-goroutine test so a listener-specific regression
// (e.g. widening writer.Update into a read-then-write) is caught GREEN→RED here.
// Errors are collected and asserted on the MAIN goroutine (never require.* inside a
// child goroutine).
func TestListener_Update_OCC_ConcurrentExactlyOneWins(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01OCCLST00000002l", "occ-lst-conc-lb")
	lst := newListener(lb.ID, "prj01OCCLST00000002l", "occ-lst-conc", 443)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, lst)
		require.NoError(t, err)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	snap, err := rd.Listeners().Get(ctx, string(lst.ID))
	require.NoError(t, err)
	_ = rd.Close()
	sharedXmin := snap.Xmin

	mutate := []func(*domain.Listener){
		func(o *domain.Listener) { o.Description = "occ-lst-a" },
		func(o *domain.Listener) { o.Name = domain.LbName("occ-lst-conc-renamed") },
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			obj := snap.Listener
			mutate[idx](&obj)
			w, werr := repo.Writer(ctx)
			if werr != nil {
				errs[idx] = werr
				return
			}
			if _, uerr := w.Listeners().Update(ctx, &obj, sharedXmin); uerr != nil {
				w.Abort()
				errs[idx] = uerr
				return
			}
			errs[idx] = w.Commit()
		}(i)
	}
	wg.Wait()

	nSuccess, nConflict := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			nSuccess++
		case errors.Is(e, kacho.ErrFailedPrecondition):
			nConflict++
		default:
			t.Fatalf("unexpected listener Update error: %v", e)
		}
	}
	assert.Equal(t, 1, nSuccess, "exactly one concurrent listener Update commits")
	assert.Equal(t, 1, nConflict, "the other is OCC-rejected (no silent lost update)")
}

// TestTargetGroup_Update_OCC_ConcurrentExactlyOneWins — real goroutine race for the
// target-group xmin CAS (parity with TestLB_Update_OCC_ConcurrentExactlyOneWins):
// two Updates share one xmin snapshot; exactly one commits, the other is
// OCC-rejected. A per-path concurrent test is required even though the CAS
// SQL is shared — a regression in targetGroupWriter.Update alone must not ship GREEN.
// Errors are collected and asserted on the MAIN goroutine.
func TestTargetGroup_Update_OCC_ConcurrentExactlyOneWins(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj01OCCTG000000002l", "occ-tg-conc")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	snap, err := rd.TargetGroups().Get(ctx, string(tg.ID))
	require.NoError(t, err)
	_ = rd.Close()
	sharedXmin := snap.Xmin

	mutate := []func(*domain.TargetGroup){
		func(o *domain.TargetGroup) { o.DeregistrationDelaySeconds = 111 },
		func(o *domain.TargetGroup) { o.Name = domain.LbName("occ-tg-conc-renamed") },
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			obj := snap.TargetGroup
			mutate[idx](&obj)
			w, werr := repo.Writer(ctx)
			if werr != nil {
				errs[idx] = werr
				return
			}
			if _, uerr := w.TargetGroups().Update(ctx, &obj, sharedXmin); uerr != nil {
				w.Abort()
				errs[idx] = uerr
				return
			}
			errs[idx] = w.Commit()
		}(i)
	}
	wg.Wait()

	nSuccess, nConflict := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			nSuccess++
		case errors.Is(e, kacho.ErrFailedPrecondition):
			nConflict++
		default:
			t.Fatalf("unexpected target-group Update error: %v", e)
		}
	}
	assert.Equal(t, 1, nSuccess, "exactly one concurrent target-group Update commits")
	assert.Equal(t, 1, nConflict, "the other is OCC-rejected (no silent lost update)")
}
