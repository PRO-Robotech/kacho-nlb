// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// free_ip_runner_integration_test.go — integration tests для FreeIPRunner
// (durable-handle reconciler). Покрывают:
//
//   - застрявший DELETING-листенер (release-неудача Delete) → reconcile: FreeIP
//     по address_id + DELETE строки + outbox DELETED/LB UPDATED + fga-unregister;
//   - идемпотентность: уже освобождённый VIP (release no-op) → строка всё равно
//     удаляется; повторный тик по удалённой строке — no-op;
//   - BYO-ветка: vip_origin='byo' → ClearReference (НЕ FreeIP);
//   - create-orphan ('CREATING' durable-handle с известным address_id) →
//     reconcile: FreeIP + DELETE, но БЕЗ outbox/fga (никогда не анонсировался);
//   - auto-only known-gap: 'CREATING' с пустым address_id → DELETE без release;
//   - age-порог: свежий in-flight 'CREATING' НЕ трогается;
//   - multi-replica: 2 реплики тикают одновременно → ровно одна делает
//     release+DELETE (FOR UPDATE SKIP LOCKED);
//   - Run(ctx): tick освобождает stuck-строку, ctx cancel → чистый выход.
//
// Использует testcontainers (postgres:16-alpine) + goose embedded migrations
// (общий setupTestDB из target_drain_runner_integration_test.go).
package jobs

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/jackc/pgx/v5/pgxpool"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeReleaser — in-memory vpcclient.InternalAddressClient: считает FreeIP /
// ClearReference (release-ветка reconciler'а). Alloc/SetReference не нужны.
type fakeReleaser struct {
	mu          sync.Mutex
	freeCalls   []string
	clearCalls  []string
	freeErr     error
	clearErr    error
	onFirstFree func() // coordination-hook (multi-replica): держит lock пока B тикает
	firstFired  bool
}

func (f *fakeReleaser) AllocateExternalIP(context.Context, vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return &vpcclient.AllocateResponse{}, nil
}
func (f *fakeReleaser) AllocateInternalIP(context.Context, vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return &vpcclient.AllocateResponse{}, nil
}
func (f *fakeReleaser) AllocateExternalIPv6(context.Context, vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return &vpcclient.AllocateResponse{}, nil
}
func (f *fakeReleaser) AllocateInternalIPv6(context.Context, vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return &vpcclient.AllocateResponse{}, nil
}
func (f *fakeReleaser) SetReference(context.Context, string, vpcclient.AddressOwner) error {
	return nil
}

func (f *fakeReleaser) FreeIP(_ context.Context, addressID string, _ vpcclient.AddressOwner) error {
	f.mu.Lock()
	hook := f.onFirstFree
	if hook != nil && !f.firstFired {
		f.firstFired = true
	} else {
		hook = nil
	}
	f.freeCalls = append(f.freeCalls, addressID)
	err := f.freeErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

func (f *fakeReleaser) ClearReference(_ context.Context, addressID string, _ vpcclient.AddressOwner) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearCalls = append(f.clearCalls, addressID)
	return f.clearErr
}

func (f *fakeReleaser) frees() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.freeCalls))
	copy(out, f.freeCalls)
	return out
}

func (f *fakeReleaser) clears() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.clearCalls))
	copy(out, f.clearCalls)
	return out
}

// newFreeIPRunner — конструктор для тестов с тихим logger'ом.
func newFreeIPRunner(t testing.TB, pool *pgxpool.Pool, addrs vpcclient.InternalAddressClient, age time.Duration) *FreeIPRunner {
	t.Helper()
	logger := observability.NewSlogger(discardWriter{})
	return NewFreeIPRunner(pool, addrs, logger, time.Second, age)
}

// insertReconcileLB — LB-родитель (FK listeners.load_balancer_id).
func insertReconcileLB(t testing.TB, ctx context.Context, pool *pgxpool.Pool) (id, projectID string) {
	t.Helper()
	id = ids.NewID(ids.PrefixLoadBalancer)
	projectID = "prj01" + ids.NewUID()[:15]
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_nlb.load_balancers (id, project_id, region_id, type, status)
		VALUES ($1, $2, 'region-1', 'EXTERNAL', 'ACTIVE')
	`, id, projectID)
	require.NoError(t, err)
	return id, projectID
}

// insertStuckListener — листенер в нетерминальном статусе с заданным возрастом
// (updated_at = now() - age). Возвращает listener id.
func insertStuckListener(t testing.TB, ctx context.Context, pool *pgxpool.Pool,
	lbID, projectID, name string, port int32,
	status domain.ListenerStatus, origin domain.VipOrigin, addressID, alloc string, age time.Duration) string {
	t.Helper()
	id := ids.NewID(ids.PrefixListener)
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_nlb.listeners
			(id, load_balancer_id, project_id, region_id, name, protocol, port, target_port,
			 ip_version, address_id, allocated_address, status, vip_origin, created_at, updated_at)
		VALUES ($1, $2, $3, 'region-1', $4, 'TCP', $5, $6, 'IPV4', $7, $8, $9, $10,
		        now() - $11::interval, now() - $11::interval)
	`, id, lbID, projectID, name, port, port, addressID, alloc, string(status), string(origin), age.String())
	require.NoError(t, err)
	return id
}

func countListeners(t testing.TB, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM kacho_nlb.listeners`).Scan(&n))
	return n
}

func countOutboxFor(t testing.TB, ctx context.Context, pool *pgxpool.Pool, resourceType, resourceID, action string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM kacho_nlb.nlb_outbox
		 WHERE resource_type=$1 AND resource_id=$2 AND action=$3
	`, resourceType, resourceID, action).Scan(&n))
	return n
}

func countFGAUnregister(t testing.TB, ctx context.Context, pool *pgxpool.Pool, resourceID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT count(*) FROM kacho_nlb.fga_register_outbox
		 WHERE event_type='fga.unregister' AND resource_id=$1
	`, resourceID).Scan(&n))
	return n
}

// =============================================================================
// reconcileOnce — single tick, direct call.
// =============================================================================

// TestFreeIP_ReconcileStuckDeleting — застрявший DELETING auto-листенер →
// reconcile освобождает VIP (FreeIP по address_id), удаляет строку, эмитит
// DELETED + LB UPDATED + fga-unregister.
func TestFreeIP_ReconcileStuckDeleting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	lbID, projectID := insertReconcileLB(t, ctx, pool)
	const addrID = "e9bSTUCKDELETE0001"
	lstID := insertStuckListener(t, ctx, pool, lbID, projectID, "stuck-del", 80,
		domain.ListenerStatusDeleting, domain.VipOriginAuto, addrID, "203.0.113.10", 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly one stuck listener reconciled")
	assert.Equal(t, []string{addrID}, rel.frees(), "FreeIP called once by address_id")
	assert.Empty(t, rel.clears(), "ClearReference must NOT be called for auto")
	assert.Equal(t, 0, countListeners(t, ctx, pool), "durable handle deleted")
	assert.Equal(t, 1, countOutboxFor(t, ctx, pool, "nlb_listener", lstID, "DELETED"))
	// LB UPDATED: explicit reconcile-finalize emit + lb_status_recompute trigger
	// (ACTIVE→INACTIVE when the last listener disappears) — same as the normal
	// Delete path. Consumers are idempotent; assert at least the explicit one.
	assert.GreaterOrEqual(t, countOutboxFor(t, ctx, pool, "nlb_load_balancer", lbID, "UPDATED"), 1)
	assert.Equal(t, 1, countFGAUnregister(t, ctx, pool, lstID))

	// Повторный тик по уже удалённой строке — no-op.
	n2, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "second tick is a no-op")
}

// TestFreeIP_IdempotentAlreadyFreed — VIP уже освобождён (release no-op):
// строка всё равно удаляется, ошибки нет.
func TestFreeIP_IdempotentAlreadyFreed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	lbID, projectID := insertReconcileLB(t, ctx, pool)
	lstID := insertStuckListener(t, ctx, pool, lbID, projectID, "already-freed", 80,
		domain.ListenerStatusDeleting, domain.VipOriginAuto, "e9bALREADYGONE001", "203.0.113.11", 10*time.Minute)

	// freeErr=nil моделирует idempotent NotFound (client мапит NotFound→nil).
	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, countListeners(t, ctx, pool), "row removed despite VIP already gone")
	_ = lstID
}

// TestFreeIP_BYOClearReference — vip_origin='byo' → ClearReference (НЕ FreeIP);
// статический Address tenant'а не удаляется.
func TestFreeIP_BYOClearReference(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	lbID, projectID := insertReconcileLB(t, ctx, pool)
	const addrID = "e9bBYOSTUCK000001"
	insertStuckListener(t, ctx, pool, lbID, projectID, "stuck-byo", 80,
		domain.ListenerStatusDeleting, domain.VipOriginBYO, addrID, "198.51.100.7", 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{addrID}, rel.clears(), "ClearReference called by address_id")
	assert.Empty(t, rel.frees(), "FreeIP must NOT be called for BYO (anti data-loss)")
	assert.Equal(t, 0, countListeners(t, ctx, pool))
}

// TestFreeIP_CreateOrphanReconciled — create-path durable-handle orphan
// ('CREATING' с известным address_id) → reconcile: FreeIP + DELETE, но БЕЗ
// outbox/fga (листенер никогда не достиг ACTIVE и не анонсировался).
func TestFreeIP_CreateOrphanReconciled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	lbID, projectID := insertReconcileLB(t, ctx, pool)
	const addrID = "e9bCREATEORPHAN01"
	lstID := insertStuckListener(t, ctx, pool, lbID, projectID, "orphan", 80,
		domain.ListenerStatusCreating, domain.VipOriginAuto, addrID, "203.0.113.12", 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{addrID}, rel.frees(), "FreeIP frees the orphan VIP")
	assert.Equal(t, 0, countListeners(t, ctx, pool), "durable handle deleted")
	// CREATING-сирота никогда не анонсировался → НЕ эмитим DELETED/unregister.
	assert.Equal(t, 0, countOutboxFor(t, ctx, pool, "nlb_listener", lstID, "DELETED"))
	assert.Equal(t, 0, countFGAUnregister(t, ctx, pool, lstID))
}

// TestFreeIP_CreateOrphanEmptyAddress_DeletedNoRelease — auto-only known-gap:
// 'CREATING' с пустым address_id (краш в окне «alloc-ответ ↔ persist») →
// reconcile удаляет handle БЕЗ release (нечем ключевать FreeIP).
func TestFreeIP_CreateOrphanEmptyAddress_DeletedNoRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	lbID, projectID := insertReconcileLB(t, ctx, pool)
	insertStuckListener(t, ctx, pool, lbID, projectID, "orphan-noaddr", 80,
		domain.ListenerStatusCreating, domain.VipOriginAuto, "", "", 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, rel.frees(), "no address_id → nothing to free")
	assert.Empty(t, rel.clears())
	assert.Equal(t, 0, countListeners(t, ctx, pool), "handle deleted")
}

// TestFreeIP_AgeThresholdSkipsFresh — свежий in-flight 'CREATING' (updated_at
// ~now) НЕ трогается, пока легитимный worker дорабатывает.
func TestFreeIP_AgeThresholdSkipsFresh(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	lbID, projectID := insertReconcileLB(t, ctx, pool)
	insertStuckListener(t, ctx, pool, lbID, projectID, "fresh", 80,
		domain.ListenerStatusCreating, domain.VipOriginAuto, "e9bFRESHINFLIGHT1", "203.0.113.13", 0)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, 5*time.Minute) // порог 5m >> age 0

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "fresh in-flight CREATING must be skipped")
	assert.Empty(t, rel.frees())
	assert.Equal(t, 1, countListeners(t, ctx, pool), "fresh row untouched")
}

// TestFreeIP_MultiReplica — две реплики тикают одновременно по одной stuck-строке:
// FOR UPDATE SKIP LOCKED гарантирует ровно один release+DELETE.
func TestFreeIP_MultiReplica(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	lbID, projectID := insertReconcileLB(t, ctx, pool)
	const addrID = "e9bMULTIREPLICA01"
	insertStuckListener(t, ctx, pool, lbID, projectID, "contended", 80,
		domain.ListenerStatusDeleting, domain.VipOriginAuto, addrID, "203.0.113.14", 10*time.Minute)

	// onFirstFree держит row-lock пока вторая реплика тикает → SKIP LOCKED путь.
	rel := &fakeReleaser{onFirstFree: func() { time.Sleep(200 * time.Millisecond) }}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	var wg sync.WaitGroup
	results := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			n, e := r.reconcileOnce(ctx)
			require.NoError(t, e)
			results[idx] = n
		}(i)
	}
	wg.Wait()

	assert.Equal(t, 1, results[0]+results[1], "exactly one replica reconciled the row")
	assert.Len(t, rel.frees(), 1, "FreeIP called exactly once (no double free)")
	assert.Equal(t, 0, countListeners(t, ctx, pool), "row deleted exactly once")
}

// =============================================================================
// Run — full loop with ctx cancel.
// =============================================================================

// TestFreeIP_RunTickAndCancel — Run реконсилит stuck-строку в течение тика и
// чисто выходит на ctx cancel.
func TestFreeIP_RunTickAndCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	lbID, projectID := insertReconcileLB(t, ctx, pool)
	insertStuckListener(t, ctx, pool, lbID, projectID, "run-stuck", 80,
		domain.ListenerStatusDeleting, domain.VipOriginAuto, "e9bRUNSTUCK000001", "203.0.113.15", 10*time.Minute)

	rel := &fakeReleaser{}
	r := NewFreeIPRunner(pool, rel, observability.NewSlogger(discardWriter{}), 100*time.Millisecond, time.Minute)
	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countListeners(t, ctx, pool) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, 0, countListeners(t, ctx, pool), "stuck listener reconciled within 3s")

	cancel()
	select {
	case e := <-runErr:
		assert.NoError(t, e, "Run must return nil on ctx cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s after ctx cancel")
	}
}
