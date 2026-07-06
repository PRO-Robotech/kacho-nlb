// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestTG_CRUD_WithHealthCheck(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj01TGCC1234567890ll", "crud-tg")
	tg.HealthCheck = domain.HealthCheck{
		Name:               "hc-tcp",
		Interval:           domain.LbDuration(2 * time.Second),
		Timeout:            domain.LbDuration(1 * time.Second),
		UnhealthyThreshold: 2,
		HealthyThreshold:   2,
		TCP:                &domain.HealthCheckTCP{Port: 80},
	}

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		assert.Equal(t, tg.ID, rec.ID)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	got, err := rd.TargetGroups().Get(ctx, string(tg.ID))
	require.NoError(t, err)
	// HealthCheck roundtrip через JSONB.
	require.NotNil(t, got.HealthCheck.TCP)
	assert.Equal(t, domain.LbPort(80), got.HealthCheck.TCP.Port)
	assert.Equal(t, domain.LbDuration(2*time.Second), got.HealthCheck.Interval)
}

func TestTG_AddTargets_Idempotent(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj01TGIT1234567890ll", "idem-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	t1 := domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd0INST1")),
		Weight:     100,
	}
	t2 := domain.Target{
		NicID:  option.MustNewOption(domain.NicID("e9b0NIC1")),
		Weight: 50,
	}

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), []domain.Target{t1, t2})
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	})

	// Re-add same targets — partial UNIQUE catches; ON CONFLICT DO NOTHING; 0 inserted.
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), []domain.Target{t1, t2})
		require.NoError(t, err, "re-add same identity must be idempotent (GWT-TGT-005)")
		assert.Equal(t, 0, n, "no new rows")
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	targets, err := rd.TargetGroups().ListTargets(ctx, string(tg.ID))
	require.NoError(t, err)
	assert.Len(t, targets, 2, "still 2 unique targets after idempotent re-add")
}

func TestTG_AddTargets_IPRef_And_ExternalIP(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj01TGIP1234567890ll", "ip-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	t1 := domain.Target{
		IPRef:  &domain.TargetIPRef{SubnetID: "e9b0SUB1", Address: "10.0.0.5"},
		Weight: 100,
	}
	t2 := domain.Target{
		ExternalIP: &domain.TargetExternalIP{
			Address: "203.0.113.99",
			ZoneID:  option.MustNewOption(domain.ZoneID("ru-central1-a")),
		},
		Weight: 50,
	}

	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), []domain.Target{t1, t2})
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	})

	rd, _ := repo.Reader(ctx)
	defer func() { _ = rd.Close() }()
	tgGot, err := rd.TargetGroups().Get(ctx, string(tg.ID))
	require.NoError(t, err)
	require.Len(t, tgGot.Targets, 2)
	// Roundtrip identity fields.
	var hasIPRef, hasExtIP bool
	for _, target := range tgGot.Targets {
		if target.IPRef != nil {
			hasIPRef = true
			assert.Equal(t, domain.IPAddress("10.0.0.5"), target.IPRef.Address)
		}
		if target.ExternalIP != nil {
			hasExtIP = true
			assert.Equal(t, domain.IPAddress("203.0.113.99"), target.ExternalIP.Address)
			z, ok := target.ExternalIP.ZoneID.Maybe()
			require.True(t, ok)
			assert.Equal(t, domain.ZoneID("ru-central1-a"), z)
		}
	}
	assert.True(t, hasIPRef && hasExtIP, "both identities preserved")
}

// TestTG_Target_4WayOneOfCheck — defense-in-depth: попытка обойти service-layer
// и вставить target с двумя identity (instance_id + external_ip) → CHECK 23514.
// Делается raw SQL'ом (минуя AddTargets, который не позволяет такое сконструировать).
func TestTG_Target_4WayOneOfCheck(t *testing.T) {
	tc := newTestCtx(t)
	repo := tc.Repo
	pool := tc.Pool
	ctx := context.Background()
	tg := newTG("prj01TG4W1234567890ll", "oneof-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_nlb.targets (id, target_group_id, instance_id, external_ip_address, weight)
         VALUES ('tgt0BAD000000000000', $1, 'epd-i1', '203.0.113.10', 100)`,
		string(tg.ID),
	)
	require.Error(t, err)
	// pgconn.PgError SQLSTATE 23514 expected → not nil, contains "targets_identity_exactly_one"
	assert.Contains(t, err.Error(), "targets_identity_exactly_one", "want CHECK constraint violation, got %v", err)
}

// TestTG_DrainConsistency_Trigger — status=ACTIVE требует
// drain_started_at IS NULL; status=DRAINING — NOT NULL.
func TestTG_DrainConsistency_Trigger(t *testing.T) {
	tc := newTestCtx(t)
	repo := tc.Repo
	pool := tc.Pool
	ctx := context.Background()

	tg := newTG("prj01TGDC1234567890ll", "dc-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
	})

	// Попытка вставить ACTIVE с drain_started_at — CHECK fail.
	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_nlb.targets (id, target_group_id, instance_id, weight, status, drain_started_at)
         VALUES ('tgt0BAD200000000000', $1, 'epd-i', 100, 'ACTIVE', now())`,
		string(tg.ID),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "targets_drain_consistency")

	// Попытка вставить DRAINING без drain_started_at — CHECK fail.
	_, err = pool.Exec(ctx,
		`INSERT INTO kacho_nlb.targets (id, target_group_id, instance_id, weight, status, drain_started_at)
         VALUES ('tgt0BAD300000000000', $1, 'epd-j', 100, 'DRAINING', NULL)`,
		string(tg.ID),
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "targets_drain_consistency")
}

// TestTG_DrainLifecycle — фаза A mark DRAINING + фаза B DELETE.
// Использует «прошлое» drain_started_at чтобы фаза B сразу подобрал target.
func TestTG_DrainLifecycle(t *testing.T) {
	tc := newTestCtx(t)
	repo := tc.Repo
	pool := tc.Pool
	ctx := context.Background()

	tg := newTG("prj01TGDR1234567890ll", "drain-tg")
	t1 := domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd0DRAIN1")),
		Weight:     100,
	}
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), []domain.Target{t1})
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})

	// Узнаём id внесённого target'а.
	rd, _ := repo.Reader(ctx)
	targets, err := rd.TargetGroups().ListTargets(ctx, string(tg.ID))
	require.NoError(t, err)
	_ = rd.Close()
	require.Len(t, targets, 1)
	targetID := targets[0].ID

	// фаза A: mark DRAINING.
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().RemoveTargetsMarkDraining(ctx, string(tg.ID), []string{targetID})
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})

	// Подкрутим drain_started_at в прошлое (имитация expired delay).
	_, err = pool.Exec(ctx,
		`UPDATE kacho_nlb.targets SET drain_started_at = now() - interval '1 hour' WHERE id = $1`,
		targetID,
	)
	require.NoError(t, err)

	// фаза B: DeleteTargetsDrained с delay=60s удалит наш target.
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().DeleteTargetsDrained(ctx, string(tg.ID), 60)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})

	rd2, _ := repo.Reader(ctx)
	defer func() { _ = rd2.Close() }()
	targetsAfter, err := rd2.TargetGroups().ListTargets(ctx, string(tg.ID))
	require.NoError(t, err)
	assert.Empty(t, targetsAfter)
}

// TestTG_AddTargets_ReactivatesDrainingTarget — re-add of a target that is
// mid-removal (status='DRAINING') must reactivate it (status='ACTIVE',
// drain_started_at=NULL, new weight) in place, NOT be swallowed by ON CONFLICT
// DO NOTHING and then hard-deleted by the phase-B drain runner (finding DATA #2,
// CWE-362 — within-service state changed by conflicting paths must be an atomic
// CAS, not a check-then-noop).
func TestTG_AddTargets_ReactivatesDrainingTarget(t *testing.T) {
	tc := newTestCtx(t)
	repo := tc.Repo
	pool := tc.Pool
	ctx := context.Background()

	tg := newTG("prj01TGRA1234567890ll", "reactivate-tg")
	t1 := domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd0REACT1")),
		Weight:     100,
	}
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), []domain.Target{t1})
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})

	rd, _ := repo.Reader(ctx)
	targets, err := rd.TargetGroups().ListTargets(ctx, string(tg.ID))
	require.NoError(t, err)
	_ = rd.Close()
	require.Len(t, targets, 1)
	targetID := targets[0].ID

	// Phase A: mark DRAINING (mid-removal window).
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().RemoveTargetsMarkDraining(ctx, string(tg.ID), []string{targetID})
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})

	// Re-add the SAME identity while DRAINING → must reactivate (counted as 1),
	// applying the re-added weight.
	reAdd := domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd0REACT1")),
		Weight:     42,
	}
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().AddTargets(ctx, string(tg.ID), []domain.Target{reAdd})
		require.NoError(t, err)
		require.Equal(t, 1, n, "re-add of a DRAINING target must reactivate it, not be a swallowed no-op")
	})

	// Row is back ACTIVE with drain cleared and new weight — reactivated in place.
	var st string
	var drainStarted *time.Time
	var weight int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT status, drain_started_at, weight FROM kacho_nlb.targets WHERE target_group_id=$1`,
		string(tg.ID)).Scan(&st, &drainStarted, &weight))
	assert.Equal(t, "ACTIVE", st)
	assert.Nil(t, drainStarted, "drain_started_at cleared on reactivation")
	assert.Equal(t, 42, weight, "reactivation applies the re-added weight")

	var cnt int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM kacho_nlb.targets WHERE target_group_id=$1`, string(tg.ID)).Scan(&cnt))
	assert.Equal(t, 1, cnt, "single target row (reactivated, not duplicated)")

	// Phase-B drain must NOT delete the now-ACTIVE target even at zero delay.
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		n, err := w.TargetGroups().DeleteTargetsDrained(ctx, string(tg.ID), 0)
		require.NoError(t, err)
		require.Equal(t, 0, n, "reactivated (ACTIVE) target is not drain-deleted")
	})
}

// TestTG_Delete_FK_RESTRICT — targets есть → нельзя удалить TG.
func TestTG_Delete_FK_RESTRICT_Targets(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj01TGDF1234567890ll", "del-tg")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.TargetGroups().Insert(ctx, tg)
		require.NoError(t, err)
		_, err = w.TargetGroups().AddTargets(ctx, string(tg.ID), []domain.Target{
			{InstanceID: option.MustNewOption(domain.InstanceID("epd0F1")), Weight: 100},
		})
		require.NoError(t, err)
	})

	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	err = w.TargetGroups().Delete(ctx, string(tg.ID))
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrFailedPrecondition), "got %v", err)
}

// TestTG_DereDelayOutOfRange — CHECK deregistration_delay 0..3600.
func TestTG_DereDelayOutOfRange(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	tg := newTG("prj01TGDS1234567890ll", "dr-tg")
	tg.DeregistrationDelaySeconds = 9999 // out of range
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	_, err = w.TargetGroups().Insert(ctx, tg)
	require.Error(t, err)
	assert.True(t, errors.Is(err, kacho.ErrInvalidArg))
}
