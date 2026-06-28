// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestAttached_AttachIdempotent — /: ON CONFLICT DO NOTHING.
// Первая Attach вставляет row (returned: attached=true); вторая — no-op (false).
func TestAttached_AttachIdempotent(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01ATCC1234567890ll", "att-lb")
	tg := newTG(string(lb.ProjectID), "att-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, attached, err := w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 50)
		require.NoError(t, err)
		assert.True(t, attached, "first attach inserts row")
		assert.Equal(t, string(lb.ID), rec.LoadBalancerID)
		assert.Equal(t, int32(50), rec.Priority)
	})

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, attached, err := w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 99)
		require.NoError(t, err)
		assert.False(t, attached, "second attach returns existing (idempotent)")
		assert.Equal(t, int32(50), rec.Priority, "priority NOT overwritten on idempotent re-attach")
	})
}

// TestAttached_Detach_Idempotent — 0 affected → no error.
func TestAttached_Detach_Idempotent(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		err := w.AttachedTargetGroups().Detach(ctx, "nlb01NX111111111111x", "tgr01NX111111111111x")
		require.NoError(t, err, "detach non-existent pair must be no-op")
	})
}

// TestAttached_FKViolation_BothSides — INSERT с non-existent LB или TG →
// FK 23503 → ErrFailedPrecondition.
func TestAttached_FKViolation_BothSides(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	w, _ := repo.Writer(ctx)
	defer w.Abort()
	_, _, err := w.AttachedTargetGroups().Attach(ctx,
		"nlb01NOEXIST123456ll", "tgr01NOEXIST123456ll", 100)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "FK 23503 → ErrFailedPrecondition, got %v", err)
}

// TestAttached_DeleteLB_RESTRICT_AttachedTG — нельзя удалить LB с attached_tg.
func TestAttached_DeleteLB_RESTRICT(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01ATDR1234567890ll", "del-att-lb")
	tg := newTG(string(lb.ProjectID), "del-att-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		_, _, err = w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 100)
		require.NoError(t, err)
	})

	w, _ := repo.Writer(ctx)
	defer w.Abort()
	err := w.LoadBalancers().Delete(ctx, string(lb.ID))
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "FK RESTRICT on attached_target_groups, got %v", err)
}

// TestAttached_DeleteTG_RESTRICT — нельзя удалить TG, который attached.
func TestAttached_DeleteTG_RESTRICT(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01ATTG1234567890ll", "trg-lb")
	tg := newTG(string(lb.ProjectID), "trg-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		_, _, err = w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg.ID), 100)
		require.NoError(t, err)
	})

	w, _ := repo.Writer(ctx)
	defer w.Abort()
	err := w.TargetGroups().Delete(ctx, string(tg.ID))
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "got %v", err)
}

// TestAttached_ListByLB_ListByTG — bidirectional queries.
func TestAttached_ListByLB_ListByTG(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newLB("prj01ATBI1234567890ll", "bi-lb")
	tg1 := newTG(string(lb.ProjectID), "bi-tg-1")
	tg2 := newTG(string(lb.ProjectID), "bi-tg-2")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg1)
		require.NoError(t, err)
		_, err = w.TargetGroups().Insert(ctx, tg2)
		require.NoError(t, err)
		_, _, err = w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg1.ID), 10)
		require.NoError(t, err)
		_, _, err = w.AttachedTargetGroups().Attach(ctx, string(lb.ID), string(tg2.ID), 20)
		require.NoError(t, err)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	byLB, err := rd.AttachedTargetGroups().ListByLB(ctx, string(lb.ID))
	require.NoError(t, err)
	assert.Len(t, byLB, 2)

	byTG, err := rd.AttachedTargetGroups().ListByTG(ctx, string(tg1.ID))
	require.NoError(t, err)
	assert.Len(t, byTG, 1)
}
