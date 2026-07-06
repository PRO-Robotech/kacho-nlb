// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// defaultTGNotAttachedMsg — дословный contract-текст композитного FK
// listeners_default_tg_attached_fk (часть API-контракта, меняется только осознанно).
const defaultTGNotAttachedMsg = "default target group is not attached to this load balancer"

// seedLBTGListener — общий setup: LB + TG (в той же region) + листенер на LB.
// Возвращает три domain-объекта (TG ещё НЕ приаттаджен).
func seedLBTGListener(t testing.TB, repo kacho.Repository, projectID string) (*domain.LoadBalancer, *domain.TargetGroup, *domain.Listener) {
	t.Helper()
	ctx := context.Background()
	lb := newLB(projectID, "")
	tg := newTG(projectID, "")
	lst := newListener(lb.ID, projectID, "", 443)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		_, err = w.Listeners().Insert(ctx, lst)
		require.NoError(t, err)
	})
	return lb, tg, lst
}

func attach(t testing.TB, repo kacho.Repository, lbID, tgID string) {
	t.Helper()
	ctx := context.Background()
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, _, err := w.AttachedTargetGroups().Attach(ctx, lbID, tgID, 100)
		require.NoError(t, err)
	})
}

func setDefaultTG(t testing.TB, repo kacho.Repository, lst *domain.Listener, tgID domain.ResourceID) {
	t.Helper()
	ctx := context.Background()
	lst.DefaultTargetGroupID = option.MustNewOption(tgID)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := updateListenerOCC(ctx, w, lst)
		require.NoError(t, err)
	})
}

// updateListenerOCC — Get current xmin (OCC snapshot) then Update, mirroring the
// use-case read-modify-write flow now that listenerWriter.Update enforces the
// `WHERE xmin::text=$exp` CAS. Returns a plain error (no *testing.T) so it is safe
// to call from concurrency-test goroutines.
func updateListenerOCC(ctx context.Context, w kacho.RepositoryWriter, l *domain.Listener) (*kacho.ListenerRecord, error) {
	cur, err := w.Listeners().Get(ctx, string(l.ID))
	if err != nil {
		return nil, err
	}
	return w.Listeners().Update(ctx, l, cur.Xmin)
}

// TestListener_GWT_6_0_01_SetDefaultOnAttachedTG_Happy — установка default на
// приаттаченный TG проходит; пустой default-routing допустим без FK.
func TestListener_GWT_6_0_01_SetDefaultOnAttachedTG_Happy(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb, tg, lst := seedLBTGListener(t, repo, "prj01DTHAPPY00000001")

	// Пустой default — допустим без какого-либо attached TG (FK не проверяется).
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		lst.DefaultTargetGroupID = option.ValueOf[domain.ResourceID]{}
		_, err := updateListenerOCC(ctx, w, lst)
		require.NoError(t, err, "empty default_target_group_id must be allowed (no FK on empty)")
	})

	attach(t, repo, string(lb.ID), string(tg.ID))

	lst.DefaultTargetGroupID = option.MustNewOption(tg.ID)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := updateListenerOCC(ctx, w, lst)
		require.NoError(t, err, "default on attached TG must succeed")
		got, ok := rec.DefaultTargetGroupID.Maybe()
		require.True(t, ok)
		assert.Equal(t, tg.ID, got)
	})

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	got, err := rd.Listeners().Get(ctx, string(lst.ID))
	require.NoError(t, err)
	val, ok := got.DefaultTargetGroupID.Maybe()
	require.True(t, ok)
	assert.Equal(t, tg.ID, val)
}

// TestListener_GWT_6_0_02_SetDefaultOnUnattachedTG_FailedPrecondition — установка
// default на TG, не приаттаченный к этому LB, отвергается композитным FK
// (23503 → FailedPrecondition + точный текст контракта).
func TestListener_GWT_6_0_02_SetDefaultOnUnattachedTG_FailedPrecondition(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	_, tg, lst := seedLBTGListener(t, repo, "prj01DTUNATT0000001")

	// TG существует, но НЕ приаттаджен к LB.
	lst.DefaultTargetGroupID = option.MustNewOption(tg.ID)
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = updateListenerOCC(ctx, w, lst)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "composite FK 23503 → ErrFailedPrecondition, got %v", err)
	assert.Contains(t, err.Error(), defaultTGNotAttachedMsg, "verbatim contract text required")
}

// TestListener_GWT_6_0_02_SetDefaultOnNonexistentTG_FailedPrecondition —
// well-formed-но-несуществующий TG даёт тот же FAILED_PRECONDITION (композитный FK
// закрывает и существование, и attachment одной конструкцией).
func TestListener_GWT_6_0_02_SetDefaultOnNonexistentTG_FailedPrecondition(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	_, _, lst := seedLBTGListener(t, repo, "prj01DTNOEXIST00001")

	lst.DefaultTargetGroupID = option.MustNewOption(domain.ResourceID("tgr00000000000000xx"))
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = updateListenerOCC(ctx, w, lst)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "got %v", err)
	assert.Contains(t, err.Error(), defaultTGNotAttachedMsg)
}

// TestListener_GWT_6_0_03_DetachDefaultTG_BlockedByRESTRICT — detach TG, который
// является default листенера, блокируется FK ON DELETE RESTRICT; после очистки
// default повторный detach проходит.
func TestListener_GWT_6_0_03_DetachDefaultTG_BlockedByRESTRICT(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb, tg, lst := seedLBTGListener(t, repo, "prj01DTDETACH000001")
	attach(t, repo, string(lb.ID), string(tg.ID))
	setDefaultTG(t, repo, lst, tg.ID)

	// Detach заблокирован — TG является default. Scoped writer с defer-Abort,
	// чтобы FailNow не оставил открытую TX (иначе pool.Close в cleanup зависнет).
	func() {
		w, err := repo.Writer(ctx)
		require.NoError(t, err)
		defer w.Abort()
		derr := w.AttachedTargetGroups().Detach(ctx, string(lb.ID), string(tg.ID))
		require.Error(t, derr, "detach of default TG must be blocked by FK RESTRICT")
		assert.True(t, errors.Is(derr, kacho.ErrFailedPrecondition), "got %v", derr)
	}()

	// pivot-строка цела.
	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	_, err = rd.AttachedTargetGroups().Get(ctx, string(lb.ID), string(tg.ID))
	require.NoError(t, err, "pivot row must survive blocked detach")
	_ = rd.Close()

	// Очистить default → detach проходит.
	lst.DefaultTargetGroupID = option.ValueOf[domain.ResourceID]{}
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := updateListenerOCC(ctx, w, lst)
		require.NoError(t, err)
	})
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		err := w.AttachedTargetGroups().Detach(ctx, string(lb.ID), string(tg.ID))
		require.NoError(t, err, "detach must succeed after default cleared")
	})

	rd2, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd2.Close() }()
	_, err = rd2.AttachedTargetGroups().Get(ctx, string(lb.ID), string(tg.ID))
	assert.True(t, errors.Is(err, kacho.ErrNotFound), "pivot row must be gone after detach")
}

// TestListener_GWT_6_0_04_ConcurrentDetachVsClearDefault_NoTornState — конкурентный
// detach default-TG vs очистка default; композитный FK гарантирует отсутствие
// torn-state «default ссылается на отсутствующую pivot-строку».
func TestListener_GWT_6_0_04_ConcurrentDetachVsClearDefault_NoTornState(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb, tg, lst := seedLBTGListener(t, repo, "prj01DTRACE00000001")
	attach(t, repo, string(lb.ID), string(tg.ID))
	setDefaultTG(t, repo, lst, tg.ID)

	var wg sync.WaitGroup
	wg.Add(2)

	// C1: detach (может быть заблокирован FK RESTRICT, пока default не очищен).
	go func() {
		defer wg.Done()
		w, err := repo.Writer(ctx)
		if err != nil {
			return
		}
		defer w.Abort()
		if derr := w.AttachedTargetGroups().Detach(ctx, string(lb.ID), string(tg.ID)); derr != nil {
			return
		}
		_ = w.Commit()
	}()

	// C2: очистить default, затем detach.
	go func() {
		defer wg.Done()
		cleared := *lst
		cleared.DefaultTargetGroupID = option.ValueOf[domain.ResourceID]{}
		w1, err := repo.Writer(ctx)
		if err != nil {
			return
		}
		if _, uerr := updateListenerOCC(ctx, w1, &cleared); uerr != nil {
			w1.Abort()
			return
		}
		if cerr := w1.Commit(); cerr != nil {
			return
		}
		w2, err := repo.Writer(ctx)
		if err != nil {
			return
		}
		defer w2.Abort()
		if derr := w2.AttachedTargetGroups().Detach(ctx, string(lb.ID), string(tg.ID)); derr == nil {
			_ = w2.Commit()
		}
	}()

	wg.Wait()

	// Инвариант: НИКОГДА «default == tg И pivot отсутствует».
	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()

	gotLst, err := rd.Listeners().Get(ctx, string(lst.ID))
	require.NoError(t, err)
	defVal, defSet := gotLst.DefaultTargetGroupID.Maybe()
	defaultIsTG := defSet && defVal == tg.ID

	_, pivotErr := rd.AttachedTargetGroups().Get(ctx, string(lb.ID), string(tg.ID))
	pivotPresent := pivotErr == nil
	if !pivotPresent {
		require.True(t, errors.Is(pivotErr, kacho.ErrNotFound), "unexpected pivot read error: %v", pivotErr)
	}

	assert.False(t, defaultIsTG && !pivotPresent,
		"torn state: listener default references a detached pivot row")
}

// TestListener_GWT_6_0_04_ConcurrentAttachVsSetDefault_NoTornState — конкурентный
// attach TG vs установка его же как default; FK гарантирует, что default не может
// быть установлен без существующей pivot-строки.
func TestListener_GWT_6_0_04_ConcurrentAttachVsSetDefault_NoTornState(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb, tg, lst := seedLBTGListener(t, repo, "prj01DTRACE00000002")

	var wg sync.WaitGroup
	wg.Add(2)

	// C1: attach.
	go func() {
		defer wg.Done()
		w, err := repo.Writer(ctx)
		if err != nil {
			return
		}
		defer w.Abort()
		if _, _, aerr := w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 100); aerr != nil {
			return
		}
		_ = w.Commit()
	}()

	// C2: установить default = tg (успех только если pivot уже виден).
	go func() {
		defer wg.Done()
		withDefault := *lst
		withDefault.DefaultTargetGroupID = option.MustNewOption(tg.ID)
		w, err := repo.Writer(ctx)
		if err != nil {
			return
		}
		defer w.Abort()
		if _, uerr := updateListenerOCC(ctx, w, &withDefault); uerr != nil {
			return
		}
		_ = w.Commit()
	}()

	wg.Wait()

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()

	gotLst, err := rd.Listeners().Get(ctx, string(lst.ID))
	require.NoError(t, err)
	defVal, defSet := gotLst.DefaultTargetGroupID.Maybe()
	defaultIsTG := defSet && defVal == tg.ID

	_, pivotErr := rd.AttachedTargetGroups().Get(ctx, string(lb.ID), string(tg.ID))
	pivotPresent := pivotErr == nil

	// default установлен ⇒ pivot обязан существовать (FK).
	assert.False(t, defaultIsTG && !pivotPresent,
		"torn state: default set without an attached pivot row")
}
