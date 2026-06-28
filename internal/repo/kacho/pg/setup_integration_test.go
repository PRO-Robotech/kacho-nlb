// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/migrations"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// setupTestDB поднимает изолированный Postgres контейнер с применённой
// миграцией 0001_initial.sql. Возвращает DSN с search_path=kacho_nlb,public.
func setupTestDB(t testing.TB) string {
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
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(db, "."))

	return appendSearchPathOptions(dsn)
}

// appendSearchPathOptions добавляет libpq `options=-c search_path=kacho_nlb,public`
// (mirror config.baseDSN поведения).
func appendSearchPathOptions(dsn string) string {
	const optionsParam = "options=-c%20search_path%3Dkacho_nlb%2Cpublic"
	if strings.Contains(dsn, "options=") || strings.Contains(dsn, "options%3D") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + optionsParam
}

// testContext — общий test fixture: pool + repo. Возвращает обоих, чтобы
// тесты могли использовать pool для raw-SQL (CHECK-constraint violations,
// которые нельзя триггернуть через типизированный repo-layer).
type testContext struct {
	Pool *pgxpool.Pool
	Repo *kachopg.Repository
}

// newTestCtx создаёт изолированный test-context (свежий Postgres-контейнер).
// Pool/Repo живут до Cleanup(t).
func newTestCtx(t testing.TB) *testContext {
	t.Helper()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return &testContext{Pool: pool, Repo: kachopg.New(pool, nil)}
}

// newRepo — short helper для тестов, которым не нужен raw-pool доступ.
func newRepo(t testing.TB, dsn string) (*kachopg.Repository, func()) {
	t.Helper()
	pool, err := coredb.NewPool(context.Background(), dsn)
	require.NoError(t, err)
	return kachopg.New(pool, nil), func() { pool.Close() }
}

// newLB строит свежий domain.LoadBalancer для тестов.
func newLB(projectID, name string) *domain.LoadBalancer {
	return &domain.LoadBalancer{
		ID:              domain.ResourceID(ids.NewID(ids.PrefixLoadBalancer)),
		ProjectID:       domain.ProjectID(projectID),
		RegionID:        "ru-central1",
		Name:            domain.LbName(name),
		Description:     "test lb",
		Labels:          domain.LabelsFromMap(map[string]string{"test": "1"}),
		Type:            domain.LBTypeExternal,
		Status:          domain.LBStatusInactive,
		SessionAffinity: domain.SessionAffinity5Tuple,
	}
}

// newListener строит свежий domain.Listener.
func newListener(lbID domain.ResourceID, projectID, name string, port int32) *domain.Listener {
	return &domain.Listener{
		ID:               domain.ResourceID(ids.NewID(ids.PrefixListener)),
		LoadBalancerID:   lbID,
		ProjectID:        domain.ProjectID(projectID),
		RegionID:         "ru-central1",
		Name:             domain.LbName(name),
		Description:      "",
		Labels:           domain.LbLabels{},
		Protocol:         domain.ProtoTCP,
		Port:             domain.LbPort(port),
		TargetPort:       domain.LbPort(8080),
		IPVersion:        domain.IPVersionV4,
		AllocatedAddress: "203.0.113.10",
		Status:           domain.ListenerStatusActive,
	}
}

// newTG строит свежий domain.TargetGroup с safe-defaults (без targets).
func newTG(projectID, name string) *domain.TargetGroup {
	return &domain.TargetGroup{
		ID:                         domain.ResourceID(ids.NewID(ids.PrefixTargetGroup)),
		ProjectID:                  domain.ProjectID(projectID),
		RegionID:                   "ru-central1",
		Name:                       domain.LbName(name),
		Description:                "",
		Labels:                     domain.LbLabels{},
		DeregistrationDelaySeconds: 300,
		SlowStartSeconds:           0,
		Status:                     domain.TargetGroupStatusActive,
	}
}

// commitWriter — helper: открыть writer, выполнить fn, commit.
func commitWriter(t testing.TB, repo kacho.Repository, fn func(w kacho.RepositoryWriter)) {
	t.Helper()
	ctx := context.Background()
	w, err := repo.Writer(ctx)
	require.NoError(t, err)
	defer w.Abort()
	fn(w)
	require.NoError(t, w.Commit())
}
