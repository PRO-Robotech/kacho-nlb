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

// TestMarkDeleting_Guards — атомарный guarded-переход в DELETING: защищённый LB /
// LB с листенером → FailedPrecondition (VIP не трогается ДО этого guard'а в
// use-case'е); чистый LB → status=DELETING (идемпотентно при повторе);
// отсутствующий id → NotFound. Первый шаг durable-handle Delete-саги
// (sec-hardening r8b, DATA #1).
func TestMarkDeleting_Guards(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	t.Run("protected → FailedPrecondition", func(t *testing.T) {
		lb := newLB("prj0MDGUARD000000001", "md-protected")
		lb.DeletionProtection = true
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			_, err := w.LoadBalancers().Insert(ctx, lb)
			require.NoError(t, err)
		})
		w, err := repo.Writer(ctx)
		require.NoError(t, err)
		defer w.Abort()
		_, err = w.LoadBalancers().MarkDeleting(ctx, string(lb.ID))
		require.Error(t, err)
		assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition),
			"protected LB mark → FailedPrecondition, got %v", err)
	})

	t.Run("clean → DELETING (idempotent)", func(t *testing.T) {
		lb := newLB("prj0MDGUARD000000002", "md-clean")
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			_, err := w.LoadBalancers().Insert(ctx, lb)
			require.NoError(t, err)
		})
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			rec, err := w.LoadBalancers().MarkDeleting(ctx, string(lb.ID))
			require.NoError(t, err)
			assert.Equal(t, domain.LBStatusDeleting, rec.Status)
		})
		// Повторный mark на уже-DELETING строке — идемпотентный no-op (retry Delete).
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			rec, err := w.LoadBalancers().MarkDeleting(ctx, string(lb.ID))
			require.NoError(t, err)
			assert.Equal(t, domain.LBStatusDeleting, rec.Status)
		})
	})

	t.Run("with listener → FailedPrecondition", func(t *testing.T) {
		lb := newLB("prj0MDGUARD000000003", "md-listener")
		commitWriter(t, repo, func(w kacho.RepositoryWriter) {
			_, err := w.LoadBalancers().Insert(ctx, lb)
			require.NoError(t, err)
			_, err = w.Listeners().Insert(ctx, newListener(lb.ID, string(lb.ProjectID), "md-l1", 80))
			require.NoError(t, err)
		})
		w, err := repo.Writer(ctx)
		require.NoError(t, err)
		defer w.Abort()
		_, err = w.LoadBalancers().MarkDeleting(ctx, string(lb.ID))
		require.Error(t, err)
		assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition),
			"LB with listener mark → FailedPrecondition, got %v", err)
	})

	t.Run("missing → NotFound", func(t *testing.T) {
		w, err := repo.Writer(ctx)
		require.NoError(t, err)
		defer w.Abort()
		_, err = w.LoadBalancers().MarkDeleting(ctx, "nlbMISSINGMARKDEL001")
		require.Error(t, err)
		assert.True(t, errors.Is(err, kacho.ErrNotFound),
			"missing LB mark → NotFound, got %v", err)
	})
}

// TestListenerInsert_RejectsDeletingParent — DB-level guard: листенер нельзя
// вставить в LB со status=DELETING (окно между mark-DELETING и финальным DELETE
// закрыто с обеих сторон, иначе ребёнок расклинил бы Delete + free_ip_runner на
// FK-RESTRICT). 0 rows → FailedPrecondition.
func TestListenerInsert_RejectsDeletingParent(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj0LSTDELPARENT0001", "lst-del-parent")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.LoadBalancers().MarkDeleting(ctx, string(lb.ID))
		require.NoError(t, err)
		require.Equal(t, domain.LBStatusDeleting, rec.Status)
	})

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, ierr := w.Listeners().Insert(ctx, newListener(lb.ID, string(lb.ProjectID), "into-deleting", 80))
	require.Error(t, ierr)
	assert.True(t, errors.Is(ierr, kacho.ErrFailedPrecondition),
		"listener insert into DELETING LB → FailedPrecondition, got %v", ierr)
}

// TestAttach_RejectsDeletingParent — DB-level guard: TG нельзя приаттачить к LB со
// status=DELETING (симметрично листенеру — attached_target_groups тоже FK-RESTRICT
// на load_balancers). 0 rows → FailedPrecondition.
func TestAttach_RejectsDeletingParent(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj0ATTDELPARENT0001", "att-del-lb")
	tg := newTG("prj0ATTDELPARENT0001", "att-del-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().MarkDeleting(ctx, string(lb.ID))
		require.NoError(t, err)
	})

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, _, aerr := w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 0)
	require.Error(t, aerr)
	assert.True(t, errors.Is(aerr, kacho.ErrFailedPrecondition),
		"attach TG to DELETING LB → FailedPrecondition, got %v", aerr)
}

// TestMarkDeleting_vs_ListenerInsert_Race — start-барьерная гонка mark-DELETING vs
// Listener.Insert на одной LB-строке. Row-lock (обе стороны берут FOR NO KEY
// UPDATE на LB) сериализует их: ровно одна сторона коммитит, вторая получает
// FailedPrecondition. НИКОГДА обе — DELETING-строка с листенером расклинила бы
// финальный Delete и free_ip_runner на FK-RESTRICT (TOCTOU-регресс).
func TestMarkDeleting_vs_ListenerInsert_Race(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj0MDRACE0000000001", "md-race")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(2)
	done.Add(2)

	var (
		mu          sync.Mutex
		markOK      bool
		markErr     error
		listenerOK  bool
		listenerErr error
	)

	// G1: mark DELETING.
	go func() {
		defer done.Done()
		w, err := repo.Writer(ctx)
		if err != nil {
			ready.Done()
			return
		}
		committed := false
		defer func() {
			if !committed {
				w.Abort()
			}
		}()
		ready.Done()
		<-start
		if _, err := w.LoadBalancers().MarkDeleting(ctx, string(lb.ID)); err != nil {
			mu.Lock()
			markErr = err
			mu.Unlock()
			return
		}
		if err := w.Commit(); err != nil {
			mu.Lock()
			markErr = err
			mu.Unlock()
			return
		}
		committed = true
		mu.Lock()
		markOK = true
		mu.Unlock()
	}()

	// G2: insert listener.
	go func() {
		defer done.Done()
		w, err := repo.Writer(ctx)
		if err != nil {
			ready.Done()
			return
		}
		committed := false
		defer func() {
			if !committed {
				w.Abort()
			}
		}()
		ready.Done()
		<-start
		if _, err := w.Listeners().Insert(ctx, newListener(lb.ID, string(lb.ProjectID), "race-l", 80)); err != nil {
			mu.Lock()
			listenerErr = err
			mu.Unlock()
			return
		}
		if err := w.Commit(); err != nil {
			mu.Lock()
			listenerErr = err
			mu.Unlock()
			return
		}
		committed = true
		mu.Lock()
		listenerOK = true
		mu.Unlock()
	}()

	ready.Wait()
	close(start)
	done.Wait()

	require.False(t, markOK && listenerOK,
		"mark-DELETING и listener-insert НЕ должны оба закоммититься (TOCTOU)")
	require.True(t, markOK || listenerOK, "хотя бы одна сторона обязана закоммититься")
	if markOK {
		require.Error(t, listenerErr)
		assert.True(t, errors.Is(listenerErr, kacho.ErrFailedPrecondition),
			"проигравший listener-insert → FailedPrecondition, got %v", listenerErr)
	} else {
		require.Error(t, markErr)
		assert.True(t, errors.Is(markErr, kacho.ErrFailedPrecondition),
			"проигравший mark → FailedPrecondition, got %v", markErr)
	}
}
