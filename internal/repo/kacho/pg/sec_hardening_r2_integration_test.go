// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// instTarget — helper: target с уникальным instance-id (для cap-тестов).
func instTarget(idx int) domain.Target {
	return domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID(fmt.Sprintf("inst%07d", idx))),
		Weight:     100,
	}
}

// --- Finding #1: Move ↔ Attach TOCTOU (cross-project attach) -----------------

// TestMoveProject_BlockedByAttachedTG_Atomic — MoveProject должен атомарно
// отказывать, если у LB есть приаттаченный TG (иначе attached_target_groups
// свяжет LB проекта B с TG проекта A). DB-level guard (`WHERE NOT EXISTS attach`).
func TestMoveProject_BlockedByAttachedTG_Atomic(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01MOVE1234567890ll", "move-lb")
	tg := newTG("prj01MOVE1234567890ll", "move-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		_, _, err = w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0)
		require.NoError(t, err)
	})

	// Move to a different project must be refused while a TG is attached.
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().MoveProject(ctx, string(lb.ID), "prj02OTHER234567890ll")
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition),
		"attached LB move must be FailedPrecondition, got %v", err)

	// Project unchanged.
	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Equal(t, domain.ProjectID("prj01MOVE1234567890ll"), got.ProjectID,
		"project must be unchanged after refused move")
}

// TestMoveProject_Allowed_NoAttach — без attach'ей move проходит (regression).
func TestMoveProject_Allowed_NoAttach(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01MOVEOK234567890l", "move-ok-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		moved, err := w.LoadBalancers().MoveProject(ctx, string(lb.ID), "prj02MOVEOK234567890l")
		require.NoError(t, err)
		assert.Equal(t, domain.ProjectID("prj02MOVEOK234567890l"), moved.ProjectID)
	})
}

// TestMoveProject_NotFound — несуществующий LB → NotFound (не FailedPrecondition).
func TestMoveProject_NotFound(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.LoadBalancers().MoveProject(ctx, "nlbMISSING1234567890", "prj02OTHER234567890ll")
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrNotFound), "missing LB → NotFound, got %v", err)
}

// TestAttach_CrossProject_Rejected — repo-level guard: attach LB(projA)↔TG(projB)
// отклоняется на DB-уровне (conditional INSERT ... SELECT re-check project/region).
func TestAttach_CrossProject_Rejected(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj0AXPROJ234567890ll", "xp-lb")
	tg := newTG("prj0BXPROJ234567890ll", "xp-tg") // different project, same region
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, inserted, err := w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0)
	require.Error(t, err)
	assert.False(t, inserted)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition),
		"cross-project attach must be FailedPrecondition, got %v", err)
}

// TestAttach_SameProject_Idempotent — same-project attach + идемпотентный re-attach.
func TestAttach_SameProject_Idempotent(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj0SAMEPR34567890lll", "sp-lb")
	tg := newTG("prj0SAMEPR34567890lll", "sp-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, inserted, err := w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0)
		require.NoError(t, err)
		assert.True(t, inserted)
	})
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, inserted, err := w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0)
		require.NoError(t, err, "re-attach must be idempotent")
		assert.False(t, inserted, "no new row on idempotent re-attach")
	})
}

// TestMoveAttach_Race — конкурентные Move и Attach не должны создать
// cross-project attach: ровно одна из операций «побеждает», а результат
// консистентен (либо moved+no-attach, либо attached+not-moved).
func TestMoveAttach_Race(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	const srcPrj = "prj0RACE1234567890lll"
	const dstPrj = "prj0RACE2234567890lll"
	lb := newLB(srcPrj, "race-lb")
	tg := newTG(srcPrj, "race-tg")
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
		if _, err := w.LoadBalancers().MoveProject(ctx, string(lb.ID), dstPrj); err == nil {
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

	// Invariant: any attached TG must share the LB's (post-op) project.
	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	gotLB, err := rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	attached, err := rd.AttachedTargetGroups().ListByLB(ctx, string(lb.ID))
	require.NoError(t, err)
	if len(attached) > 0 {
		gotTG, err := rd.TargetGroups().Get(ctx, string(tg.ID))
		require.NoError(t, err)
		assert.Equal(t, gotLB.ProjectID, gotTG.ProjectID,
			"attached TG must share the LB project — no cross-project attach allowed")
		assert.Equal(t, domain.ProjectID(srcPrj), gotLB.ProjectID,
			"if attach won, LB must NOT have moved")
	}
}

// --- Finding #2/#4: cumulative per-group target cap -------------------------

// TestAddTargets_CumulativeCap — серия AddTargets не должна пробить
// MaxTargetsPerGroup (=100); превышающий вызов → FailedPrecondition, count не растёт.
func TestAddTargets_CumulativeCap(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj0CAP01234567890lll", "cap-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	mkBatch := func(from, n int) []domain.Target {
		out := make([]domain.Target, 0, n)
		for i := from; i < from+n; i++ {
			out = append(out, instTarget(i))
		}
		return out
	}

	// 60 → ok.
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), mkBatch(0, 60))
		require.NoError(t, err)
		assert.Equal(t, 60, n)
	})
	// +60 → would be 120 > 100 → FailedPrecondition, nothing inserted.
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), mkBatch(60, 60))
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "cap breach → FailedPrecondition, got %v", err)
	assert.Equal(t, 0, n)
	w.Abort()

	// +40 → exactly 100 → ok.
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), mkBatch(60, 40))
		require.NoError(t, err)
		assert.Equal(t, 40, n)
	})
	// +1 → 101 > 100 → FailedPrecondition.
	w2, err := repo.Writer(ctx)
	require.NoError(t, err)
	_, err = w2.TargetGroups().AddTargets(ctx, string(tg.ID), mkBatch(100, 1))
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "over-cap → FailedPrecondition, got %v", err)
	w2.Abort()

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	targets, err := rd.TargetGroups().ListTargets(ctx, string(tg.ID))
	require.NoError(t, err)
	assert.Len(t, targets, 100, "group must be capped at 100")
}

// TestAddTargets_CumulativeCap_Concurrent — конкурентные AddTargets не должны
// суммарно пробить cap (FOR UPDATE на parent сериализует).
func TestAddTargets_CumulativeCap_Concurrent(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj0CAPCONC34567890ll", "cap-conc-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	mkBatch := func(from, n int) []domain.Target {
		out := make([]domain.Target, 0, n)
		for i := from; i < from+n; i++ {
			out = append(out, instTarget(i))
		}
		return out
	}

	// Two goroutines each try to add 70 distinct targets. Cap is 100 → at most
	// one full batch can win; the total must never exceed 100.
	var wg sync.WaitGroup
	wg.Add(2)
	for g := 0; g < 2; g++ {
		g := g
		go func() {
			defer wg.Done()
			w, err := repo.Writer(ctx)
			if err != nil {
				return
			}
			defer w.Abort()
			if _, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), mkBatch(g*1000, 70)); err == nil {
				_ = w.Commit()
			}
		}()
	}
	wg.Wait()

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	targets, err := rd.TargetGroups().ListTargets(ctx, string(tg.ID))
	require.NoError(t, err)
	assert.LessOrEqual(t, len(targets), 100, "concurrent AddTargets must never exceed cap")
}

// --- Finding #5: deletion_protection atomic guard ---------------------------

// TestDeleteIfUnprotected_Guard — защищённый LB не удаляется атомарным guard'ом;
// снятие защиты открывает удаление; отсутствующий id → NotFound.
func TestDeleteIfUnprotected_Guard(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj0DELPRO34567890lll", "del-pro-lb")
	lb.DeletionProtection = true
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	// Protected → guard blocks.
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	err = w.LoadBalancers().DeleteIfUnprotected(ctx, string(lb.ID))
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition),
		"protected LB delete → FailedPrecondition, got %v", err)
	w.Abort()

	// Row still there.
	rd, _ := repo.Reader(ctx)
	_, err = rd.LoadBalancers().Get(ctx, string(lb.ID))
	require.NoError(t, err)
	_ = rd.Close()

	// Missing id → NotFound.
	w2, err := repo.Writer(ctx)
	require.NoError(t, err)
	err = w2.LoadBalancers().DeleteIfUnprotected(ctx, "nlbMISSING1234567890")
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrNotFound), "missing LB → NotFound, got %v", err)
	w2.Abort()

	// Clear protection → delete succeeds.
	lb.DeletionProtection = false
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Update(ctx, lb)
		require.NoError(t, err)
	})
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		require.NoError(t, w.LoadBalancers().DeleteIfUnprotected(ctx, string(lb.ID)))
	})
	rd2, _ := repo.Reader(ctx)
	defer func() { _ = rd2.Close() }()
	_, err = rd2.LoadBalancers().Get(ctx, string(lb.ID))
	assert.True(t, errors.Is(err, kacho.ErrNotFound), "LB must be gone after unprotected delete")
}

// --- Finding #6: 23505 constraint-specific messages -------------------------

// TestUnique_PortProto_Message — коллизия (lb, port, protocol) отдаёт сообщение
// про port/protocol, а НЕ про «name already exists».
func TestUnique_PortProto_Message(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj0PPMSG34567890llll", "ppmsg-lb")
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
	dup.AllocatedAddress = "203.0.113.55"
	_, err = w.Listeners().Insert(ctx, dup)
	require.Error(t, err)
	require.True(t, errors.Is(err, kacho.ErrAlreadyExists), "got %v", err)
	assert.Contains(t, strings.ToLower(err.Error()), "port",
		"message must name the port/protocol conflict, not 'name': %v", err)
	assert.NotContains(t, strings.ToLower(err.Error()), "with name already exists",
		"must not mislabel port/protocol collision as name conflict: %v", err)
}
