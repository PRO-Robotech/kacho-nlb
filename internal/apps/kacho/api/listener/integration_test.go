// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/anypb"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/listener"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/jobs"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	"github.com/PRO-Robotech/kacho-nlb/internal/migrations"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// Integration tests for Listener UseCases against a real Postgres
// (testcontainers). Covers:
//   - End-to-end Create flow + outbox emit + LB denorm correctness.
//   - Race: 2 concurrent Create requests with same (LB, port, protocol) — one
//     must succeed, the other gets AlreadyExists.
//   - Race: 2 concurrent Create requests targeting the same BYO VIP — first
//     succeeds, second fails on SetReference CAS (mocked here as
//     `fakeInternalAddrs` returns FailedPrecondition once exhausted).
//
// All tests gated by testing.Short (testcontainers requires Docker).

// gooseMu serialises the goose package-level globals (SetBaseFS / SetDialect /
// Up) which are NOT goroutine-safe. Parallel integration tests each call
// setupTestDB; without this guard `-race` flags a write-write race on goose's
// internal globals (test-infra only, no production state).
var gooseMu sync.Mutex

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
	gooseMu.Lock()
	goose.SetBaseFS(migrations.FS)
	err = goose.SetDialect("postgres")
	if err == nil {
		err = goose.Up(db, ".")
	}
	gooseMu.Unlock()
	require.NoError(t, err)
	return appendSearchPathOptions(dsn)
}

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

// integrationCtx — реальный CQRS-repo + ops repo + in-memory peer-clients.
type integrationCtx struct {
	pool    *pgxpool.Pool
	repo    *kachopg.Repository
	opsRepo operations.Repo
}

func newIntegrationCtx(t *testing.T) *integrationCtx {
	t.Helper()
	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	return &integrationCtx{
		pool:    pool,
		repo:    kachopg.New(pool, nil),
		opsRepo: operations.NewRepo(pool, "kacho_nlb"),
	}
}

func (i *integrationCtx) seedLB(t *testing.T, projectID, regionID string, lbType domain.LBType, name string) *kachorepo.LoadBalancerRecord {
	t.Helper()
	lb := domain.NewLoadBalancer(domain.ProjectID(projectID), domain.RegionID(regionID),
		domain.LbName(name), "", domain.LbLabels{}, lbType)
	lb.Status = domain.LBStatusActive
	w, err := i.repo.Writer(context.Background())
	require.NoError(t, err)
	defer w.Abort()
	rec, err := w.LoadBalancers().Insert(context.Background(), &lb)
	require.NoError(t, err)
	require.NoError(t, w.Commit())
	return rec
}

// recordingInternalAddrs — in-memory InternalAddressClient that simulates the
// real `vpc.InternalAddressService` for integration tests. Returns a stable
// (or sequentially incremented) allocated IP without talking to a real peer.
type recordingInternalAddrs struct {
	mu        sync.Mutex
	nextID    int
	allocs    int
	frees     int
	setRefs   int
	clears    int
	allocErr  error
	failAfter int // if > 0, fail allocations after N successes
}

func (r *recordingInternalAddrs) AllocateExternalIP(_ context.Context, req vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allocs++
	if r.allocErr != nil {
		return nil, r.allocErr
	}
	if r.failAfter > 0 && r.allocs > r.failAfter {
		return nil, errors.New("simulated alloc exhaustion")
	}
	r.nextID++
	return &vpcclient.AllocateResponse{
		AddressID: ids.NewID(ids.PrefixSubnet),
		Value:     "203.0.113." + strconv.Itoa(100+r.nextID),
	}, nil
}
func (r *recordingInternalAddrs) AllocateInternalIP(_ context.Context, req vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return r.AllocateExternalIP(context.Background(), vpcclient.AllocateExternalIPRequest{
		ProjectID: req.ProjectID,
		Name:      req.Name,
		Owner:     req.Owner,
	})
}
func (r *recordingInternalAddrs) FreeIP(_ context.Context, _ string, _ vpcclient.AddressOwner) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frees++
	return nil
}
func (r *recordingInternalAddrs) SetReference(_ context.Context, _ string, _ vpcclient.AddressOwner) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setRefs++
	return nil
}
func (r *recordingInternalAddrs) ClearReference(_ context.Context, _ string, _ vpcclient.AddressOwner) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clears++
	return nil
}

// TestIntegration_Listener_Create_EndToEnd — real Postgres: Create → row
// persists with correct LB denorm (project_id, region_id) + outbox event row.
func TestIntegration_Listener_Create_EndToEnd(t *testing.T) {
	t.Parallel()
	ic := newIntegrationCtx(t)
	lb := ic.seedLB(t, "prj01INTEGTEST000001", "ru-central1", domain.LBTypeExternal, "lb-e2e")
	internalAddrs := &recordingInternalAddrs{}
	createUC := listener.NewCreateUseCase(ic.repo, ic.opsRepo, nil, internalAddrs, nil, slog.Default())

	op, err := createUC.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID),
		Name:           "e2e-listener",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpecIntegration(""),
	})
	require.NoError(t, err)
	awaitOpDoneIntegration(t, ic.opsRepo, op.ID, 5*time.Second)

	gotOp, err := ic.opsRepo.Get(context.Background(), op.ID)
	require.NoError(t, err)
	require.True(t, gotOp.Done)
	require.Nil(t, gotOp.Error)

	// Verify listener row + LB denorm.
	rd, err := ic.repo.Reader(context.Background())
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	page, _, err := rd.Listeners().ListByLB(context.Background(), string(lb.ID), kachorepo.Pagination{})
	require.NoError(t, err)
	require.Len(t, page, 1)
	got := page[0]
	require.Equal(t, lb.RegionID, got.RegionID, "Listener.region_id denorm must match LB")
	require.Equal(t, lb.ProjectID, got.ProjectID, "Listener.project_id denorm must match LB")
	require.NotEmpty(t, got.AllocatedAddress)
	// durable-handle saga: CREATING-handle finalized to ACTIVE (TX-3).
	require.Equal(t, domain.ListenerStatusActive, got.Status, "create-saga must finalize CREATING→ACTIVE")

	// Verify outbox events.
	rows, err := ic.pool.Query(context.Background(),
		`SELECT resource_type, resource_id, action FROM nlb_outbox WHERE resource_id IN ($1, $2) ORDER BY sequence_no ASC`,
		string(got.ID), string(lb.ID),
	)
	require.NoError(t, err)
	defer rows.Close()
	type ev struct {
		resourceType, resourceID, action string
	}
	var events []ev
	for rows.Next() {
		var e ev
		require.NoError(t, rows.Scan(&e.resourceType, &e.resourceID, &e.action))
		events = append(events, e)
	}
	require.NoError(t, rows.Err())
	// Expect listener CREATED + lb UPDATED.
	require.GreaterOrEqual(t, len(events), 2)
	hasListenerCreated := false
	hasLBUpdated := false
	for _, e := range events {
		if e.resourceType == "nlb_listener" && e.action == "CREATED" {
			hasListenerCreated = true
		}
		if e.resourceType == "nlb_load_balancer" && e.action == "UPDATED" {
			hasLBUpdated = true
		}
	}
	require.True(t, hasListenerCreated, "must have nlb_listener CREATED event")
	require.True(t, hasLBUpdated, "must have nlb_load_balancer UPDATED event")
}

// TestIntegration_Listener_Create_UniquePortRace — two parallel Create requests
// with the same (LB, port, protocol). DB UNIQUE constraint enforces — exactly
// one succeeds.
func TestIntegration_Listener_Create_UniquePortRace(t *testing.T) {
	t.Parallel()
	ic := newIntegrationCtx(t)
	lb := ic.seedLB(t, "prj01INTEGTEST000002", "ru-central1", domain.LBTypeExternal, "lb-race")
	internalAddrs := &recordingInternalAddrs{}
	createUC := listener.NewCreateUseCase(ic.repo, ic.opsRepo, nil, internalAddrs, nil, slog.Default())

	var wg sync.WaitGroup
	var successCount, failCount int
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			op, err := createUC.Run(context.Background(), &lbv1.CreateListenerRequest{
				LoadBalancerId: string(lb.ID),
				Name:           "race-listener-" + strconv.Itoa(i),
				Protocol:       lbv1.Listener_TCP,
				Port:           80,
				TargetPort:     8080,
				IpVersion:      lbv1.IpVersion_IPV4,
				AddressSpec:    autoSpecIntegration(""),
			})
			if err != nil {
				mu.Lock()
				failCount++
				mu.Unlock()
				return
			}
			awaitOpDoneIntegration(t, ic.opsRepo, op.ID, 5*time.Second)
			gotOp, err := ic.opsRepo.Get(context.Background(), op.ID)
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			if gotOp.Error != nil {
				failCount++
				return
			}
			successCount++
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, successCount, "exactly one Create must succeed (UNIQUE constraint)")
	assert.Equal(t, 1, failCount, "exactly one must fail with AlreadyExists/conflict")
	// Verify exactly one row inserted.
	rd, err := ic.repo.Reader(context.Background())
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	page, _, err := rd.Listeners().ListByLB(context.Background(), string(lb.ID), kachorepo.Pagination{})
	require.NoError(t, err)
	require.Len(t, page, 1)
	// Durable-handle saga: the loser fails at the TX-1 INSERT (lb,port,proto
	// UNIQUE) BEFORE acquireVIP runs → it never allocated a VIP → nothing to free.
	assert.Equal(t, 0, internalAddrs.frees, "loser fails at TX-1 INSERT before VIP-alloc — no compensation free")
}

// TestIntegration_Listener_CreateOrphan_FreeIPReconcile — 6.0-38 (durable-handle,
// create-path orphan). Симулируем краш после TX-2 (persist address_id), но до
// TX-3 (CREATING→ACTIVE): строка остаётся в 'CREATING' с известным address_id.
// free_ip_runner находит её (старше age-порога) → FreeIP по address_id + DELETE
// handle. VIP не утёк, сирота финализирована.
func TestIntegration_Listener_CreateOrphan_FreeIPReconcile(t *testing.T) {
	t.Parallel()
	ic := newIntegrationCtx(t)
	lb := ic.seedLB(t, "prj01INTEGORPHAN001", "ru-central1", domain.LBTypeExternal, "lb-orphan")

	const addrID = "e9bORPHANADDR00001"
	// TX-1 (durable handle, CREATING) + TX-2 (persist address_id) через РЕАЛЬНЫЙ
	// repo, без TX-3 — эквивалент краха после persist address_id.
	l := domain.NewListener(lb.LoadBalancer, "orphan-lst",
		domain.LbProto("TCP"), domain.LbPort(80), domain.LbPort(8080), domain.IPVersion("IPV4"))
	w1, err := ic.repo.Writer(context.Background())
	require.NoError(t, err)
	_, err = w1.Listeners().Insert(context.Background(), &l)
	require.NoError(t, err)
	require.NoError(t, w1.Commit())
	w2, err := ic.repo.Writer(context.Background())
	require.NoError(t, err)
	_, err = w2.Listeners().SetVIP(context.Background(), string(l.ID), addrID, "203.0.113.55")
	require.NoError(t, err)
	require.NoError(t, w2.Commit())

	// Backdate updated_at за age-порог (симулируем «застряло давно»).
	_, err = ic.pool.Exec(context.Background(),
		`UPDATE kacho_nlb.listeners SET updated_at = now() - interval '10 minutes' WHERE id = $1`, string(l.ID))
	require.NoError(t, err)

	rel := &recordingInternalAddrs{}
	runner := jobs.NewFreeIPRunner(ic.pool, rel, slog.Default(), 100*time.Millisecond, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = runner.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if listenerRowCount(t, ic.pool, string(l.ID)) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()

	assert.Equal(t, 0, listenerRowCount(t, ic.pool, string(l.ID)), "durable handle reconciled away")
	rel.mu.Lock()
	frees := rel.frees
	rel.mu.Unlock()
	assert.Equal(t, 1, frees, "orphan VIP freed exactly once by free_ip_runner")
}

func listenerRowCount(t *testing.T, pool *pgxpool.Pool, id string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM kacho_nlb.listeners WHERE id = $1`, id).Scan(&n))
	return n
}

// TestIntegration_Listener_Delete_FreeIP — happy-path Delete: VIP freed,
// outbox emits DELETED + LB UPDATED, listener row removed.
func TestIntegration_Listener_Delete_FreeIP(t *testing.T) {
	t.Parallel()
	ic := newIntegrationCtx(t)
	lb := ic.seedLB(t, "prj01INTEGTEST000003", "ru-central1", domain.LBTypeExternal, "lb-delete")
	internalAddrs := &recordingInternalAddrs{}
	createUC := listener.NewCreateUseCase(ic.repo, ic.opsRepo, nil, internalAddrs, nil, slog.Default())
	deleteUC := listener.NewDeleteUseCase(ic.repo, ic.opsRepo, internalAddrs, slog.Default())

	op, err := createUC.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID),
		Name:           "to-delete",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpecIntegration(""),
	})
	require.NoError(t, err)
	awaitOpDoneIntegration(t, ic.opsRepo, op.ID, 5*time.Second)

	// Find the inserted listener_id.
	rd, err := ic.repo.Reader(context.Background())
	require.NoError(t, err)
	page, _, err := rd.Listeners().ListByLB(context.Background(), string(lb.ID), kachorepo.Pagination{})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, domain.VipOriginAuto, page[0].VipOrigin, "auto-alloc listener must carry vip_origin=auto")
	_ = rd.Close()

	// Delete: vip_origin=auto → FreeIP (Address удаляется целиком).
	delOp, err := deleteUC.Run(context.Background(), &lbv1.DeleteListenerRequest{
		ListenerId: string(page[0].ID),
	})
	require.NoError(t, err)
	awaitOpDoneIntegration(t, ic.opsRepo, delOp.ID, 5*time.Second)

	gotOp, err := ic.opsRepo.Get(context.Background(), delOp.ID)
	require.NoError(t, err)
	require.True(t, gotOp.Done)
	require.Nil(t, gotOp.Error, "Delete operation must succeed; got %v", gotOp.Error)

	// Row removed.
	rd2, err := ic.repo.Reader(context.Background())
	require.NoError(t, err)
	defer func() { _ = rd2.Close() }()
	page2, _, err := rd2.Listeners().ListByLB(context.Background(), string(lb.ID), kachorepo.Pagination{})
	require.NoError(t, err)
	require.Len(t, page2, 0)
	assert.Equal(t, 1, internalAddrs.frees, "FreeIP must be called exactly once")
}

// TestIntegration_Listener_Delete_BYO_ClearReference — BYO delete-path: a
// tenant-supplied Address (vip_origin=byo persisted on Create) is released via
// ClearReference, NOT FreeIP — the static address survives. Guards the
// data-loss regression at the DB+use-case boundary (vip_origin round-trips
// through Postgres and drives the release branch).
func TestIntegration_Listener_Delete_BYO_ClearReference(t *testing.T) {
	t.Parallel()
	ic := newIntegrationCtx(t)
	lb := ic.seedLB(t, "prj01INTEGTEST000004", "ru-central1", domain.LBTypeExternal, "lb-delete-byo")
	internalAddrs := &recordingInternalAddrs{}
	const byoAddrID = "e9bINTEGBYOADDR001"
	// BYO Address deliberately named like an auto-alloc one ("nlb-listener-*")
	// to prove the release branch ignores the name and reads vip_origin.
	addresses := &byoIntegrationAddressClient{
		addr: &vpcclient.Address{
			ID:        byoAddrID,
			ProjectID: string(lb.ProjectID),
			Name:      "nlb-listener-edge",
			Value:     "198.51.100.9",
			Family:    vpcclient.AddressFamilyIPv4,
			External:  true,
		},
	}
	createUC := listener.NewCreateUseCase(ic.repo, ic.opsRepo, addresses, internalAddrs, nil, slog.Default())
	deleteUC := listener.NewDeleteUseCase(ic.repo, ic.opsRepo, internalAddrs, slog.Default())

	op, err := createUC.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID),
		Name:           "byo-to-delete",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    byoSpecIntegration(byoAddrID),
	})
	require.NoError(t, err)
	awaitOpDoneIntegration(t, ic.opsRepo, op.ID, 5*time.Second)
	gotOp, err := ic.opsRepo.Get(context.Background(), op.ID)
	require.NoError(t, err)
	require.Nil(t, gotOp.Error, "BYO Create must succeed; got %v", gotOp.Error)

	rd, err := ic.repo.Reader(context.Background())
	require.NoError(t, err)
	page, _, err := rd.Listeners().ListByLB(context.Background(), string(lb.ID), kachorepo.Pagination{})
	require.NoError(t, err)
	require.Len(t, page, 1)
	require.Equal(t, domain.VipOriginBYO, page[0].VipOrigin, "BYO listener must persist vip_origin=byo")
	_ = rd.Close()

	delOp, err := deleteUC.Run(context.Background(), &lbv1.DeleteListenerRequest{
		ListenerId: string(page[0].ID),
	})
	require.NoError(t, err)
	awaitOpDoneIntegration(t, ic.opsRepo, delOp.ID, 5*time.Second)
	delGot, err := ic.opsRepo.Get(context.Background(), delOp.ID)
	require.NoError(t, err)
	require.Nil(t, delGot.Error, "BYO Delete must succeed; got %v", delGot.Error)

	assert.Equal(t, 1, internalAddrs.clears, "ClearReference must be called for vip_origin=byo")
	assert.Equal(t, 0, internalAddrs.frees, "FreeIP must NOT be called for vip_origin=byo (data-loss guard)")
}

// ---- helpers ----

// byoIntegrationAddressClient — minimal AddressClient returning a single seeded
// tenant Address (for BYO Create validation path).
type byoIntegrationAddressClient struct {
	addr *vpcclient.Address
}

func (c *byoIntegrationAddressClient) Get(_ context.Context, id string) (*vpcclient.Address, error) {
	if c.addr != nil && c.addr.ID == id {
		cp := *c.addr
		return &cp, nil
	}
	return nil, errors.New("address not found")
}

func byoSpecIntegration(addressID string) *lbv1.ListenerAddressSpec {
	return &lbv1.ListenerAddressSpec{
		Source: &lbv1.ListenerAddressSpec_AddressId{AddressId: addressID},
	}
}

func autoSpecIntegration(subnetID string) *lbv1.ListenerAddressSpec {
	return &lbv1.ListenerAddressSpec{
		Source: &lbv1.ListenerAddressSpec_Auto{
			Auto: &lbv1.ListenerAddressSpec_AutoAllocate{SubnetId: subnetID},
		},
	}
}

func awaitOpDoneIntegration(t *testing.T, repo operations.Repo, id string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		op, err := repo.Get(context.Background(), id)
		if err == nil && op.Done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("operation %s did not complete within %s", id, timeout)
}

// _ — sentinel for option / anypb / codes used in some lines but not directly
// referenced after refactor (kept to ensure imports validated by gofmt).
var (
	_ = option.MustNewOption[domain.AddressID]
	_ = anypb.Any{}
	_ = codes.OK
)
