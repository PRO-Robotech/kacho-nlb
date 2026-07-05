// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup_test

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/targetgroup"
	// dto/type2pb init регистрирует TargetGroup transfer.
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	"github.com/PRO-Robotech/kacho-nlb/internal/migrations"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// gooseMu serialises goose's package-level globals (SetBaseFS / SetDialect / Up),
// which are not goroutine-safe; parallel integration tests each apply migrations.
var gooseMu sync.Mutex

// setupDB поднимает изолированный Postgres + накатывает migrations.
func setupDB(t *testing.T) (*pgxpool.Pool, *kachopg.Repository) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test (testing.Short)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pgc, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("kacho_nlb_test"),
		postgres.WithUsername("nlb"),
		postgres.WithPassword("nlb"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgc.Terminate(context.Background()) })

	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	gooseMu.Lock()
	goose.SetBaseFS(migrations.FS)
	err = goose.SetDialect("postgres")
	if err == nil {
		err = goose.Up(db, ".")
	}
	gooseMu.Unlock()
	require.NoError(t, err)

	if !strings.Contains(dsn, "options=") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		dsn = dsn + sep + "options=-c%20search_path%3Dkacho_nlb%2Cpublic"
	}

	pool, err := coredb.NewPool(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return pool, kachopg.New(pool, nil)
}

func newOpsRepo(t *testing.T, pool *pgxpool.Pool) operations.Repo {
	t.Helper()
	return operations.NewRepo(pool, "kacho_nlb")
}

func pollOpDone(t *testing.T, opsRepo operations.Repo, opID string) *operations.Operation {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		op, err := opsRepo.Get(context.Background(), opID)
		if err == nil && op.Done {
			return op
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("operation %s did not finish within 60s", opID)
	return nil
}

func mkHandler(t *testing.T, repo *kachopg.Repository, opsRepo operations.Repo) *targetgroup.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// nil peer-clients — Create/AddTargets/Move skip peer-validate (acceptable
	// для integration сценариев DB happy-paths).
	return targetgroup.NewHandler(repo, opsRepo, nil, nil, nil, nil, nil, nil, logger)
}

// ---- Integration tests -----------------------------------------------------

func TestIntegration_CreateTargetGroup_EndToEnd(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := mkHandler(t, repo, opsRepo)

	op, err := h.Create(context.Background(), &lbv1.CreateTargetGroupRequest{
		ProjectId: "prj-integ-create",
		RegionId:  "ru-central1",
		Name:      "tg-int-1",
		Labels:    map[string]string{"env": "prod"},
		HealthCheck: &lbv1.HealthCheck{
			Name:               "hc-http",
			Interval:           durationpb.New(2 * time.Second),
			Timeout:            durationpb.New(1 * time.Second),
			UnhealthyThreshold: 2,
			HealthyThreshold:   2,
			Options: &lbv1.HealthCheck_HttpOptions_{
				HttpOptions: &lbv1.HealthCheck_HttpOptions{Port: 8080, Path: "/healthz"},
			},
		},
		DeregistrationDelaySeconds: 300,
	})
	require.NoError(t, err)
	require.False(t, op.GetDone())

	final := pollOpDone(t, opsRepo, op.GetId())
	require.Nilf(t, final.Error, "operation error: %v", final.Error)
	require.NotNil(t, final.Response)

	// Outbox row present.
	rows, err := pool.Query(context.Background(),
		`SELECT resource_type, action FROM kacho_nlb.nlb_outbox ORDER BY sequence_no ASC`)
	require.NoError(t, err)
	defer rows.Close()
	var events []string
	for rows.Next() {
		var rt, action string
		require.NoError(t, rows.Scan(&rt, &action))
		events = append(events, rt+":"+action)
	}
	require.Contains(t, events, "nlb_target_group:CREATED")
}

// integration: Delete TG blocked when attached to LB
// (real FK precheck via HasAttachedLB query).
func TestIntegration_DeleteTG_BlocksOnAttached(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)

	// Insert LB + TG + pivot row via raw SQL (no handlers).
	lbID := ids.NewID(ids.PrefixLoadBalancer)
	tgID := ids.NewID(ids.PrefixTargetGroup)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_nlb.load_balancers (id, project_id, region_id, name, description, labels,
			type, status, session_affinity, cross_zone_enabled, deletion_protection)
		VALUES ($1, 'prj-x', 'ru-central1', 'lb-int', '', '{}', 'EXTERNAL', 'ACTIVE',
		        'FIVE_TUPLE', true, false)`, lbID,
	)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO kacho_nlb.target_groups (id, project_id, region_id, name, description, labels,
			health_check, deregistration_delay_seconds, slow_start_seconds, status)
		VALUES ($1, 'prj-x', 'ru-central1', 'tg-int', '', '{}',
		        '{"name":"hc","interval":"2s","timeout":"1s","unhealthy_threshold":2,"healthy_threshold":2,"tcp":{"port":80}}'::jsonb,
		        300, 0, 'ACTIVE')`, tgID,
	)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO kacho_nlb.attached_target_groups (load_balancer_id, target_group_id, priority)
		VALUES ($1, $2, 0)`, lbID, tgID,
	)
	require.NoError(t, err)

	h := mkHandler(t, repo, opsRepo)
	_, err = h.Delete(ctx, &lbv1.DeleteTargetGroupRequest{TargetGroupId: tgID})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, status.Convert(err).Message(), "is attached to 1 load balancer(s)")
}

// TestIntegration_AddRemoveTargets_Lifecycle — фаза B parity integration: full
// Add/Remove/Drain lifecycle через real Postgres.
func TestIntegration_AddRemoveTargets_Lifecycle(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := mkHandler(t, repo, opsRepo)
	ctx := context.Background()

	// 1. Create TG.
	createOp, err := h.Create(ctx, &lbv1.CreateTargetGroupRequest{
		ProjectId: "prj-life", RegionId: "ru-central1", Name: "life-tg",
		HealthCheck: &lbv1.HealthCheck{
			Name: "hc-tcp", Interval: durationpb.New(2 * time.Second),
			Timeout: durationpb.New(1 * time.Second), UnhealthyThreshold: 2, HealthyThreshold: 2,
			Options: &lbv1.HealthCheck_TcpOptions_{TcpOptions: &lbv1.HealthCheck_TcpOptions{Port: 80}},
		},
		DeregistrationDelaySeconds: 300,
	})
	require.NoError(t, err)
	createFinal := pollOpDone(t, opsRepo, createOp.GetId())
	require.Nil(t, createFinal.Error)

	// Resolve TG.ID via List.
	listResp, err := h.List(ctx, &lbv1.ListTargetGroupsRequest{ProjectId: "prj-life"})
	require.NoError(t, err)
	require.Len(t, listResp.GetTargetGroups(), 1)
	tgID := listResp.GetTargetGroups()[0].GetId()

	// 2. AddTargets — 2 unique external_ip identities (peer-validate not needed:
	// external_ip path skips compute/vpc peer lookups; bogon-check done in
	// domain Validate).
	addOp, err := h.AddTargets(ctx, &lbv1.AddTargetsRequest{
		TargetGroupId: tgID,
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_ExternalIp{ExternalIp: &lbv1.Target_ExternalIP{
				Address: "203.0.113.10",
			}}, Weight: 100},
			{Identity: &lbv1.Target_ExternalIp{ExternalIp: &lbv1.Target_ExternalIP{
				Address: "203.0.113.20",
			}}, Weight: 50},
		},
	})
	require.NoError(t, err)
	addFinal := pollOpDone(t, opsRepo, addOp.GetId())
	require.Nilf(t, addFinal.Error, "add op error: %v", addFinal.Error)

	// 3. Re-add same → idempotent (ON CONFLICT DO NOTHING on partial UNIQUE
	// per external_ip_address).
	reAddOp, err := h.AddTargets(ctx, &lbv1.AddTargetsRequest{
		TargetGroupId: tgID,
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_ExternalIp{ExternalIp: &lbv1.Target_ExternalIP{
				Address: "203.0.113.10",
			}}, Weight: 100},
		},
	})
	require.NoError(t, err)
	reAddFinal := pollOpDone(t, opsRepo, reAddOp.GetId())
	require.Nil(t, reAddFinal.Error)

	// 4. Get TG inspects 2 targets (idempotent re-add не добавил третий).
	getResp, err := h.Get(ctx, &lbv1.GetTargetGroupRequest{TargetGroupId: tgID})
	require.NoError(t, err)
	require.Len(t, getResp.GetTargets(), 2, "duplicate identity should be no-op")

	// 5. RemoveTargets фаза A: mark one external_ip target as DRAINING.
	rmOp, err := h.RemoveTargets(ctx, &lbv1.RemoveTargetsRequest{
		TargetGroupId: tgID,
		Targets: []*lbv1.Target{
			{Identity: &lbv1.Target_ExternalIp{ExternalIp: &lbv1.Target_ExternalIP{
				Address: "203.0.113.10",
			}}, Weight: 100},
		},
	})
	require.NoError(t, err)
	rmFinal := pollOpDone(t, opsRepo, rmOp.GetId())
	require.Nil(t, rmFinal.Error)

	// 6. Verify SQL state: 1 row DRAINING + drain_started_at != NULL; 1 row ACTIVE.
	var drainingCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM kacho_nlb.targets
		  WHERE target_group_id = $1 AND status='DRAINING' AND drain_started_at IS NOT NULL`,
		tgID).Scan(&drainingCount)
	require.NoError(t, err)
	assert.Equal(t, 1, drainingCount)

	var activeCount int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM kacho_nlb.targets WHERE target_group_id = $1 AND status='ACTIVE'`,
		tgID).Scan(&activeCount)
	require.NoError(t, err)
	assert.Equal(t, 1, activeCount)
}

// integration: фаза B Delete expired only — using a single TG
// with a DRAINING target whose drain_started_at is in the past.
func TestIntegration_DrainPhaseB_DeletesExpired(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	ctx := context.Background()
	_ = repo

	// Setup TG + DRAINING target with drain_started_at = now - 1h.
	tgID := ids.NewID(ids.PrefixTargetGroup)
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_nlb.target_groups (id, project_id, region_id, name, description, labels,
			health_check, deregistration_delay_seconds, slow_start_seconds, status)
		VALUES ($1, 'prj-d', 'ru-central1', 'drain-tg', '', '{}',
		        '{"name":"hc","interval":"2s","timeout":"1s","unhealthy_threshold":2,"healthy_threshold":2,"tcp":{"port":80}}'::jsonb,
		        60, 0, 'ACTIVE')`, tgID,
	)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO kacho_nlb.targets (id, target_group_id, instance_id, weight, status, drain_started_at)
		VALUES ($1, $2, 'epd-drained', 100, 'DRAINING', now() - interval '1 hour')`,
		ids.NewID("tgt"), tgID,
	)
	require.NoError(t, err)

	// Invoke pg-side DeleteTargetsDrained directly (фаза B runner runs same SQL).
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	deleted, err := w.TargetGroups().DeleteTargetsDrained(ctx, tgID, int32(60))
	require.NoError(t, err)
	require.NoError(t, w.Commit())
	assert.Equal(t, 1, deleted)

	// Verify rows gone.
	var remaining int
	err = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM kacho_nlb.targets WHERE target_group_id = $1`, tgID).Scan(&remaining)
	require.NoError(t, err)
	assert.Equal(t, 0, remaining)
}
