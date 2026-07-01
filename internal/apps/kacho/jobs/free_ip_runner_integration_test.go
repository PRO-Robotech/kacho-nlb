// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// free_ip_runner_integration_test.go — integration tests для FreeIPRunner
// (durable-handle reconciler LoadBalancer'ов). VIP консолидирован на LoadBalancer
// (anycast active-active), поэтому reconciler сканирует load_balancers (не
// listeners) и освобождает VIP per-family. Покрывают:
//
//   - застрявший DELETING-LB → reconcile: FreeIP по address_id_v4 + DELETE строки
//   - outbox DELETED + fga-unregister;
//   - идемпотентность: уже освобождённый VIP → строка всё равно удаляется;
//   - linked-ветка: vip_origin='linked' → ClearReference (НЕ FreeIP);
//   - dualstack: раздельный release v4 (auto owned → two-step) и v6 (linked → ClearReference);
//   - create-orphan ('CREATING' с известным address_id) → FreeIP + DELETE без
//     outbox/fga (LB никогда не анонсировался);
//   - auto-only known-gap: 'CREATING' без address_id → DELETE без release;
//   - age-порог: свежий in-flight 'CREATING' НЕ трогается;
//   - multi-replica: ровно один release+DELETE (FOR UPDATE SKIP LOCKED);
//   - Run(ctx): tick освобождает stuck-строку, ctx cancel → чистый выход.
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
func (f *fakeReleaser) SetReference(context.Context, string, vpcclient.AddressOwner, bool) error {
	return nil
}
func (f *fakeReleaser) AttachExisting(context.Context, vpcclient.AttachExistingRequest) (*vpcclient.AllocateResponse, error) {
	return &vpcclient.AllocateResponse{}, nil
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

// insertStuckLB — durable-handle LB в нетерминальном статусе с заданным возрастом
// (updated_at = now() - age) и per-family binding. address_v4/v6 оставляем пустыми
// (status-aware CHECK пропускает), reconcile ключуется по address_id_v4/v6.
func insertStuckLB(t testing.TB, ctx context.Context, pool *pgxpool.Pool,
	status domain.LBStatus, originV4, addrIDV4, originV6, addrIDV6 string, age time.Duration) (id, projectID string) {
	t.Helper()
	id = ids.NewID(ids.PrefixLoadBalancer)
	projectID = "prj01" + ids.NewUID()[:15]
	_, err := pool.Exec(ctx, `
		INSERT INTO kacho_nlb.load_balancers
			(id, project_id, region_id, type, status, placement_type,
			 address_id_v4, vip_origin_v4, address_id_v6, vip_origin_v6,
			 created_at, updated_at)
		VALUES ($1, $2, 'region-1', 'INTERNAL', $3, 'REGIONAL',
		        $4, $5, $6, $7, now() - $8::interval, now() - $8::interval)
	`, id, projectID, string(status), addrIDV4, originV4, addrIDV6, originV6, age.String())
	require.NoError(t, err)
	return id, projectID
}

func countLoadBalancers(t testing.TB, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM kacho_nlb.load_balancers`).Scan(&n))
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

// TestFreeIP_ReconcileStuckDeleting — застрявший DELETING auto-LB → reconcile
// освобождает VIP (FreeIP по address_id_v4), удаляет строку, эмитит DELETED +
// fga-unregister.
func TestFreeIP_ReconcileStuckDeleting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const addrID = "adr0000000STUCKDEL01"
	lbID, _ := insertStuckLB(t, ctx, pool, domain.LBStatusDeleting, "auto", addrID, "", "", 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly one stuck LB reconciled")
	assert.Equal(t, []string{addrID}, rel.frees(), "FreeIP called once by address_id_v4")
	assert.Equal(t, []string{addrID}, rel.clears(), "owned auto → ClearReference before FreeIP (two-step)")
	assert.Equal(t, 0, countLoadBalancers(t, ctx, pool), "durable handle deleted")
	assert.Equal(t, 1, countOutboxFor(t, ctx, pool, "nlb_load_balancer", lbID, "DELETED"))
	assert.Equal(t, 1, countFGAUnregister(t, ctx, pool, lbID))

	// Повторный тик по уже удалённой строке — no-op.
	n2, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "second tick is a no-op")
}

// TestFreeIP_IdempotentAlreadyFreed — VIP уже освобождён (release no-op): строка
// всё равно удаляется, ошибки нет.
func TestFreeIP_IdempotentAlreadyFreed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	insertStuckLB(t, ctx, pool, domain.LBStatusDeleting, "auto", "adr000000ALREADYGONE", "", "", 10*time.Minute)

	rel := &fakeReleaser{} // freeErr=nil моделирует idempotent NotFound
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, countLoadBalancers(t, ctx, pool), "row removed despite VIP already gone")
}

// TestFreeIP_LinkedClearReference — vip_origin='linked' → ClearReference (НЕ FreeIP);
// tenant-owned Address не удаляется.
func TestFreeIP_LinkedClearReference(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const addrID = "adr000000LINKSTUCK01"
	insertStuckLB(t, ctx, pool, domain.LBStatusDeleting, "linked", addrID, "", "", 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{addrID}, rel.clears(), "ClearReference called by address_id_v4")
	assert.Empty(t, rel.frees(), "FreeIP must NOT be called for linked (anti data-loss)")
	assert.Equal(t, 0, countLoadBalancers(t, ctx, pool))
}

// TestFreeIP_DualstackSeparateRelease — dualstack orphan: v4 (auto owned →
// two-step ClearReference→FreeIP) и v6 (linked → ClearReference) освобождаются
// РАЗДЕЛЬНО, каждый по своему дискриминатору.
func TestFreeIP_DualstackSeparateRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const addrV4 = "adr0000000DUALV40001"
	const addrV6 = "adr0000000DUALV60001"
	insertStuckLB(t, ctx, pool, domain.LBStatusDeleting, "auto", addrV4, "linked", addrV6, 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{addrV4}, rel.frees(), "v4 auto → FreeIP (after clear)")
	assert.Equal(t, []string{addrV4, addrV6}, rel.clears(), "v4 owned two-step clear + v6 linked clear")
	assert.Equal(t, 0, countLoadBalancers(t, ctx, pool))
}

// TestFreeIP_CreateOrphanReconciled — create-path durable-handle orphan
// ('CREATING' с известным address_id) → FreeIP + DELETE, но БЕЗ outbox/fga
// (LB никогда не достиг терминального статуса и не анонсировался).
func TestFreeIP_CreateOrphanReconciled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	const addrID = "adr0000CREATEORPHAN1"
	lbID, _ := insertStuckLB(t, ctx, pool, domain.LBStatusCreating, "auto", addrID, "", "", 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{addrID}, rel.frees(), "FreeIP frees the orphan VIP")
	assert.Equal(t, 0, countLoadBalancers(t, ctx, pool), "durable handle deleted")
	assert.Equal(t, 0, countOutboxFor(t, ctx, pool, "nlb_load_balancer", lbID, "DELETED"))
	assert.Equal(t, 0, countFGAUnregister(t, ctx, pool, lbID))
}

// TestFreeIP_CreateOrphanEmptyAddress_DeletedNoRelease — auto-only known-gap:
// 'CREATING' без address_id (краш в окне «alloc-ответ ↔ persist») → reconcile
// удаляет handle БЕЗ release (нечем ключевать FreeIP).
func TestFreeIP_CreateOrphanEmptyAddress_DeletedNoRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	ctx := context.Background()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()

	insertStuckLB(t, ctx, pool, domain.LBStatusCreating, "", "", "", "", 10*time.Minute)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, time.Minute)

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Empty(t, rel.frees(), "no address_id → nothing to free")
	assert.Empty(t, rel.clears())
	assert.Equal(t, 0, countLoadBalancers(t, ctx, pool), "handle deleted")
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

	insertStuckLB(t, ctx, pool, domain.LBStatusCreating, "auto", "adr0000FRESHINFLIGHT", "", "", 0)

	rel := &fakeReleaser{}
	r := newFreeIPRunner(t, pool, rel, 5*time.Minute) // порог 5m >> age 0

	n, err := r.reconcileOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "fresh in-flight CREATING must be skipped")
	assert.Empty(t, rel.frees())
	assert.Equal(t, 1, countLoadBalancers(t, ctx, pool), "fresh row untouched")
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

	const addrID = "adr000MULTIREPLICA01"
	insertStuckLB(t, ctx, pool, domain.LBStatusDeleting, "auto", addrID, "", "", 10*time.Minute)

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
	assert.Equal(t, 0, countLoadBalancers(t, ctx, pool), "row deleted exactly once")
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

	insertStuckLB(t, ctx, pool, domain.LBStatusDeleting, "auto", "adr00000RUNSTUCK0001", "", "", 10*time.Minute)

	rel := &fakeReleaser{}
	r := NewFreeIPRunner(pool, rel, observability.NewSlogger(discardWriter{}), 100*time.Millisecond, time.Minute)
	runErr := make(chan error, 1)
	go func() { runErr <- r.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if countLoadBalancers(t, ctx, pool) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.Equal(t, 0, countLoadBalancers(t, ctx, pool), "stuck LB reconciled within 3s")

	cancel()
	select {
	case e := <-runErr:
		assert.NoError(t, e, "Run must return nil on ctx cancel")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s after ctx cancel")
	}
}
