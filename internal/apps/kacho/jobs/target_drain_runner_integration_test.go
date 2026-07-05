// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package jobs — integration tests for TargetDrainRunner.
//
// Покрывают двухфазный drain:
//
//   - Expired DRAINING target → DELETE, outbox UPDATED emit (per TG).
//   - Not-yet-expired DRAINING (drain_started_at + delay > now) → preserved.
//   - ACTIVE targets — никогда не trogались.
//   - Multiple expired targets одного TG в одном tick → ровно одна
//     `nlb_target_group:<tg_id> UPDATED` outbox-row (DISTINCT).
//   - Пустая очередь → no-op (no outbox row).
//   - TG.deregistration_delay_seconds=0 → drain на следующем tick'е сразу
//     после mark'а.
//   - Run exits cleanly при ctx cancel (no error, no leak).
//
// Inside-out от drainOnce(ctx) → Run(ctx, interval). Использует testcontainers
// (postgres:16-alpine) + goose с embedded migrations FS (как cmd/migrator).
package jobs

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-nlb/internal/migrations"
)

// setupTestDB поднимает postgres:16-alpine через testcontainers и применяет
// embedded migrations FS из internal/migrations. Возвращает DSN с
// search_path=kacho_nlb,public.
func setupTestDB(t testing.TB) string {
	t.Helper()
	ctx := context.Background()

	pgc, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("kacho_nlb_test"),
		postgres.WithUsername("nlb"),
		postgres.WithPassword("nlb"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })

	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(db, "."))

	return appendSearchPath(dsn)
}

func appendSearchPath(dsn string) string {
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

// newRunner — конструктор для тестов с logger discarded (no stdout noise).
func newRunner(t testing.TB, pool *pgxpool.Pool, interval time.Duration) *TargetDrainRunner {
	t.Helper()
	// Использовать standard slog handler с io.Discard — observability.NewSlogger
	// пишет в os.Stdout что зашумляет вывод тестов. Достаточно «тихого» logger'а.
	logger := observability.NewSlogger(discardWriter{})
	return NewTargetDrainRunner(pool, logger, interval)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// insertTargetGroup — helper: создаёт TG с заданным deregistration_delay_seconds.
func insertTargetGroup(t testing.TB, ctx context.Context, pool *pgxpool.Pool, delaySec int) (id, projectID string) {
	t.Helper()
	id = ids.NewID(ids.PrefixTargetGroup)
	projectID = "proj-" + ids.NewUID()[:8]
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_nlb.target_groups
			(id, project_id, region_id, deregistration_delay_seconds, status)
		VALUES ($1, $2, 'ru-central1', $3, 'ACTIVE')
	`, id, projectID, delaySec)
	require.NoError(t, err)
	return id, projectID
}

// insertActiveTarget — helper: target с status='ACTIVE'.
func insertActiveTarget(t testing.TB, ctx context.Context, pool *pgxpool.Pool, tgID, instanceID string) string {
	t.Helper()
	id := ids.NewUID()
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_nlb.targets
			(id, target_group_id, instance_id, weight, status)
		VALUES ($1, $2, $3, 100, 'ACTIVE')
	`, id, tgID, instanceID)
	require.NoError(t, err)
	return id
}

// insertDrainingTarget — helper: target с status='DRAINING' и заданным
// drain_started_at (relative-to-now).
func insertDrainingTarget(t testing.TB, ctx context.Context, pool *pgxpool.Pool, tgID, instanceID string, drainAge time.Duration) string {
	t.Helper()
	id := ids.NewUID()
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_nlb.targets
			(id, target_group_id, instance_id, weight, status, drain_started_at)
		VALUES ($1, $2, $3, 100, 'DRAINING', now() - $4::interval)
	`, id, tgID, instanceID, drainAge.String())
	require.NoError(t, err)
	return id
}

func countTargets(t testing.TB, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM kacho_nlb.targets`).Scan(&n))
	return n
}

func countOutboxForTG(t testing.TB, ctx context.Context, pool *pgxpool.Pool, tgID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM kacho_nlb.nlb_outbox
		 WHERE resource_type='nlb_target_group'
		   AND resource_id=$1
		   AND action='UPDATED'
	`, tgID).Scan(&n))
	return n
}

// =============================================================================
// drainOnce — single tick, direct call.
// =============================================================================

// TestDrainOnce_ExpiredOnly — /012: только expired DRAINING удаляются;
// non-expired DRAINING + ACTIVE остаются. Outbox emit ровно один (DISTINCT).
func TestDrainOnce_ExpiredOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tgID, _ := insertTargetGroup(t, ctx, pool, 2) // delay=2s
	idExpired := insertDrainingTarget(t, ctx, pool, tgID, "i-expired", 10*time.Second)
	idNotYet := insertDrainingTarget(t, ctx, pool, tgID, "i-fresh", 0) // drain_age=0 < 2s delay
	idActive := insertActiveTarget(t, ctx, pool, tgID, "i-active")

	r := newRunner(t, pool, 1*time.Second)
	deleted, tgs, err := r.drainOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted, "exactly 1 expired DRAINING target deleted")
	assert.Equal(t, 1, tgs, "exactly 1 TG had a drained target → 1 outbox row")

	assert.Equal(t, 2, countTargets(t, ctx, pool), "non-expired DRAINING + ACTIVE remain")
	assert.Equal(t, 1, countOutboxForTG(t, ctx, pool, tgID), "exactly 1 UPDATED outbox row for TG")

	// Проверяем, какие именно остались.
	rows, err := pool.Query(ctx, `SELECT id FROM kacho_nlb.targets ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var remaining []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		remaining = append(remaining, id)
	}
	assert.NotContains(t, remaining, idExpired)
	assert.Contains(t, remaining, idNotYet)
	assert.Contains(t, remaining, idActive)
}

// TestDrainOnce_NoExpired_NoOp — empty drain queue → no DELETE, no outbox.
func TestDrainOnce_NoExpired_NoOp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tgID, _ := insertTargetGroup(t, ctx, pool, 30)
	insertActiveTarget(t, ctx, pool, tgID, "i-1")
	insertDrainingTarget(t, ctx, pool, tgID, "i-2", 1*time.Second) // drain_age=1s < 30s delay

	r := newRunner(t, pool, 1*time.Second)
	deleted, tgs, err := r.drainOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
	assert.Equal(t, 0, tgs)
	assert.Equal(t, 2, countTargets(t, ctx, pool))
	assert.Equal(t, 0, countOutboxForTG(t, ctx, pool, tgID))
}

// TestDrainOnce_MultipleSameTG_OneOutboxRow — 3 expired DRAINING одной TG →
// 3 DELETE'а + DISTINCT'нутый outbox: ровно 1 UPDATED row на TG.
func TestDrainOnce_MultipleSameTG_OneOutboxRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tgID, _ := insertTargetGroup(t, ctx, pool, 1)
	insertDrainingTarget(t, ctx, pool, tgID, "i-a", 10*time.Second)
	insertDrainingTarget(t, ctx, pool, tgID, "i-b", 10*time.Second)
	insertDrainingTarget(t, ctx, pool, tgID, "i-c", 10*time.Second)

	r := newRunner(t, pool, 1*time.Second)
	deleted, tgs, err := r.drainOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)
	assert.Equal(t, 1, tgs, "3 targets in same TG → DISTINCT → 1 outbox row")
	assert.Equal(t, 0, countTargets(t, ctx, pool))
	assert.Equal(t, 1, countOutboxForTG(t, ctx, pool, tgID))
}

// TestDrainOnce_MultipleTGs — 2 TG, у каждой свой expired target → 2 outbox
// rows (по одной на TG). DISTINCT работает per-TG, не глобально.
func TestDrainOnce_MultipleTGs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tg1, _ := insertTargetGroup(t, ctx, pool, 1)
	tg2, _ := insertTargetGroup(t, ctx, pool, 1)
	insertDrainingTarget(t, ctx, pool, tg1, "i-1", 10*time.Second)
	insertDrainingTarget(t, ctx, pool, tg2, "i-2", 10*time.Second)

	r := newRunner(t, pool, 1*time.Second)
	deleted, tgs, err := r.drainOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)
	assert.Equal(t, 2, tgs)
	assert.Equal(t, 1, countOutboxForTG(t, ctx, pool, tg1))
	assert.Equal(t, 1, countOutboxForTG(t, ctx, pool, tg2))
}

// TestDrainOnce_ZeroDelay_ImmediateDrain — TG.deregistration_delay_seconds=0 →
// drain на следующем tick'е сразу после mark'а (drain_age=1ms > 0s).
func TestDrainOnce_ZeroDelay_ImmediateDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tgID, _ := insertTargetGroup(t, ctx, pool, 0)
	// drain_age=1ms — больше 0s → expired.
	insertDrainingTarget(t, ctx, pool, tgID, "i-zero", 100*time.Millisecond)

	r := newRunner(t, pool, 1*time.Second)
	deleted, tgs, err := r.drainOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	assert.Equal(t, 1, tgs)
	assert.Equal(t, 0, countTargets(t, ctx, pool))
}

// TestDrainOnce_PerTGDelay — разные TG с разными delay → expiry рассчитывается
// per-TG (через tg.deregistration_delay_seconds). target TG-короткой-delay
// дренируется; target TG-длинной-delay — нет.
func TestDrainOnce_PerTGDelay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tgShort, _ := insertTargetGroup(t, ctx, pool, 1)                  // 1s delay
	tgLong, _ := insertTargetGroup(t, ctx, pool, 3600)                // 1h delay
	insertDrainingTarget(t, ctx, pool, tgShort, "i-s", 5*time.Second) // expired (5>1)
	insertDrainingTarget(t, ctx, pool, tgLong, "i-l", 5*time.Second)  // not (5<3600)

	r := newRunner(t, pool, 1*time.Second)
	deleted, tgs, err := r.drainOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	assert.Equal(t, 1, tgs)
	assert.Equal(t, 1, countOutboxForTG(t, ctx, pool, tgShort))
	assert.Equal(t, 0, countOutboxForTG(t, ctx, pool, tgLong))
}

// TestDrainOnce_MultiReplica — N реплик тикают drainOnce одновременно по одной
// expired DRAINING строке. Single-statement `DELETE ... RETURNING` + outbox
// INSERT CTE полагается на row-level lock Postgres: ровно одна транзакция
// удаляет строку и эмитит outbox row, остальные видят её уже удалённой и
// возвращают 0. Аналог TestFreeIP_MultiReplica (тот — через FOR UPDATE SKIP
// LOCKED; здесь — через DELETE-атомарность). Гейт против будущего рефактора,
// расщепляющего drainSQL на SELECT-then-DELETE (потеря exactly-once).
func TestDrainOnce_MultiReplica(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tgID, _ := insertTargetGroup(t, ctx, pool, 1) // delay=1s
	insertDrainingTarget(t, ctx, pool, tgID, "i-contested", 10*time.Second)

	r := newRunner(t, pool, 1*time.Second)

	const replicas = 8
	start := make(chan struct{}) // барьер — все goroutine стартуют одновременно
	var wg sync.WaitGroup
	deletedTotals := make([]int64, replicas)
	tgTotals := make([]int, replicas)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			deleted, tgs, e := r.drainOnce(ctx)
			require.NoError(t, e)
			deletedTotals[idx] = deleted
			tgTotals[idx] = tgs
		}(i)
	}
	close(start)
	wg.Wait()

	var sumDeleted int64
	var sumTGs int
	for i := 0; i < replicas; i++ {
		sumDeleted += deletedTotals[i]
		sumTGs += tgTotals[i]
	}

	assert.Equal(t, int64(1), sumDeleted, "exactly one replica deleted the contested target")
	assert.Equal(t, 1, sumTGs, "exactly one replica emitted the TG outbox row")
	assert.Equal(t, 0, countTargets(t, ctx, pool), "target deleted exactly once")
	assert.Equal(t, 1, countOutboxForTG(t, ctx, pool, tgID), "exactly one UPDATED outbox row (no duplicate drain_complete)")
}

// =============================================================================
// Run — full loop with ctx cancel.
// =============================================================================

// TestRun_TickAndCancel — runner запускается с interval=100ms,
// expired target → DELETE'aется в течение первого tick'а;
// ctx cancel завершает Run без error.
func TestRun_TickAndCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	tgID, _ := insertTargetGroup(t, ctx, pool, 1)
	insertDrainingTarget(t, ctx, pool, tgID, "i-expired", 10*time.Second)
	insertActiveTarget(t, ctx, pool, tgID, "i-active")

	r := newRunner(t, pool, 100*time.Millisecond)
	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx) }()

	// Poll: ждём, пока drain произойдёт (поллим targets count раз в 50ms,
	// дедлайн 3s — гораздо больше interval'а, надёжно).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countTargets(t, ctx, pool) == 1 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, 1, countTargets(t, ctx, pool), "expired DRAINING must be drained within 3s")
	assert.Equal(t, 1, countOutboxForTG(t, ctx, pool, tgID))

	// Cancel — runner должен выйти чисто, без error.
	cancel()
	select {
	case err := <-runErr:
		assert.NoError(t, err, "Run must return nil on ctx cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s after ctx cancel")
	}
}

// TestRun_TransientErrorContinues — runner НЕ должен exit на transient errors
// (только ctx cancel exits). Симуляция: дропнуть schema → tick fail → cancel.
// Проверяем, что Run всё равно вернул nil (не propagate'ил error).
func TestRun_TransientErrorContinues(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	// Эмулируем «transient»-ошибку: дропаем targets-таблицу
	// → drainOnce схватит SQL error → должен log + continue.
	_, err = pool.Exec(ctx, `DROP TABLE kacho_nlb.targets CASCADE`)
	require.NoError(t, err)

	r := newRunner(t, pool, 50*time.Millisecond)
	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx) }()

	// Дать пару тиков на отлов error.
	time.Sleep(200 * time.Millisecond)

	cancel()
	select {
	case err := <-runErr:
		assert.NoError(t, err, "Run must return nil on ctx cancel even after transient errors")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s after ctx cancel")
	}
}
