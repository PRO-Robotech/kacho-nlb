// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/loadbalancer"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	"github.com/PRO-Robotech/kacho-nlb/internal/migrations"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// gooseMu serialises goose's package-level globals (SetBaseFS / SetDialect / Up),
// which are not goroutine-safe; parallel integration tests each apply migrations.
var gooseMu sync.Mutex

// setupDB поднимает изолированный Postgres контейнер и применяет миграцию.
// Зеркало pg/setup_integration_test.go (внутренний helper).
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

// newOpsRepo создаёт реальную operations-таблицу repo на тестовом пуле.
func newOpsRepo(t *testing.T, pool *pgxpool.Pool) operations.Repo {
	t.Helper()
	return operations.NewRepo(pool, "kacho_nlb")
}

// pollOpDone — детерминированно ждёт op.Done в реальной БД (60s).
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

func makeHandler(t *testing.T, repo *kachopg.Repository, opsRepo operations.Repo) *loadbalancer.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// vpc/geo недоступны в testcontainers-стенде — VIP-аллокация заглушается stub'ом,
	// возвращающим уникальный адрес на вызов (DB-сторона саги — реальная). Subnet-
	// client / address-reader / zone-client — nil (placement / link / drain precheck
	// пропускаются без vpc/geo; эти сьюты гоняют subnet-auto семейства).
	return loadbalancer.NewHandler(repo, opsRepo, nil, nil, nil, nil, nil, nil, &stubAddressClient{}, nil, logger)
}

// stubAddressClient — заглушка vpc InternalAddressClient для integration-стенда
// (без реального vpc). AllocateInternalIP/IPv6 возвращают уникальный адрес/id на
// вызов; AttachExisting эхо-адрес; release — no-op.
type stubAddressClient struct{ seq int64 }

func (s *stubAddressClient) AllocateInternalIP(_ context.Context, _ vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	n := atomic.AddInt64(&s.seq, 1)
	return &vpcclient.AllocateResponse{
		AddressID: fmt.Sprintf("adr%017d", n),
		Value:     fmt.Sprintf("100.64.0.%d", n),
	}, nil
}

func (s *stubAddressClient) AllocateInternalIPv6(_ context.Context, _ vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	n := atomic.AddInt64(&s.seq, 1)
	return &vpcclient.AllocateResponse{
		AddressID: fmt.Sprintf("adr%017d", n),
		Value:     fmt.Sprintf("fd00::%d", n),
	}, nil
}

func (s *stubAddressClient) AllocateExternalIP(_ context.Context, _ vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	n := atomic.AddInt64(&s.seq, 1)
	return &vpcclient.AllocateResponse{AddressID: fmt.Sprintf("adr%017d", n), Value: fmt.Sprintf("203.0.113.%d", n)}, nil
}

func (s *stubAddressClient) AllocateExternalIPv6(_ context.Context, _ vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	n := atomic.AddInt64(&s.seq, 1)
	return &vpcclient.AllocateResponse{AddressID: fmt.Sprintf("adr%017d", n), Value: fmt.Sprintf("2001:db8::%d", n)}, nil
}

func (s *stubAddressClient) AttachExisting(_ context.Context, req vpcclient.AttachExistingRequest) (*vpcclient.AllocateResponse, error) {
	n := atomic.AddInt64(&s.seq, 1)
	return &vpcclient.AllocateResponse{AddressID: req.AddressID, Value: fmt.Sprintf("100.64.9.%d", n)}, nil
}

func (s *stubAddressClient) FreeIP(context.Context, string) error { return nil }
func (s *stubAddressClient) ClearReference(context.Context, string) error {
	return nil
}

// internalAutoReq — INTERNAL REGIONAL Create-request (subnet-auto v4) для e2e.
func internalAutoReq(projectID, name string) *lbv1.CreateNetworkLoadBalancerRequest {
	return &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: projectID, RegionId: "ru-central1", Name: name,
		Type:          lbv1.NetworkLoadBalancer_INTERNAL,
		PlacementType: lbv1.NetworkLoadBalancer_REGIONAL,
		V4Source:      &lbv1.VipSource{Source: &lbv1.VipSource_SubnetId{SubnetId: "sub-1"}},
	}
}

// ---- Tests -----------------------------------------------------------------

func TestIntegration_CreateLoadBalancer_EndToEnd(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)

	op, err := h.Create(context.Background(), internalAutoReq("prj-acme-test", "edge-public"))
	require.NoError(t, err)
	require.False(t, op.GetDone())
	require.NotEmpty(t, op.GetId())

	final := pollOpDone(t, opsRepo, op.GetId())
	require.Nilf(t, final.Error, "operation error: %v", final.Error)
	require.NotNil(t, final.Response)

	// Inspect outbox: exactly one CREATED row.
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
	require.Contains(t, events, "nlb_load_balancer:CREATED")
}

func TestIntegration_DeleteLoadBalancer_BlocksOnListener(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)

	// Insert LB via repo directly.
	w, err := repo.Writer(context.Background())
	require.NoError(t, err)
	lb := &domain.LoadBalancer{
		ID:        domain.ResourceID(ids.NewID(ids.PrefixLoadBalancer)),
		ProjectID: "prj-x", RegionID: "ru-central1",
		Name: "edge", Type: domain.LBTypeExternal, Status: domain.LBStatusInactive,
		SessionAffinity: domain.SessionAffinity5Tuple,
	}
	_, err = w.LoadBalancers().Insert(context.Background(), lb)
	require.NoError(t, err)
	require.NoError(t, w.Commit())
	// Insert listener (via raw SQL — no listener handler yet). Must run after LB
	// TX is committed because the pool sees a different snapshot.
	_, err = pool.Exec(context.Background(), `
		INSERT INTO kacho_nlb.listeners (id, project_id, load_balancer_id, region_id, name,
			description, labels, protocol, port, target_port, ip_version,
			address_id, allocated_address, subnet_id, proxy_protocol_v2, default_target_group_id, status)
		VALUES ($1, $2, $3, $4, 'lst-1', '', '{}', 'TCP', 8080, 80, 'IPV4',
		        '', '203.0.113.1', '', false, '', 'ACTIVE')`,
		ids.NewID(ids.PrefixListener), "prj-x", string(lb.ID), "ru-central1",
	)
	require.NoError(t, err)

	h := makeHandler(t, repo, opsRepo)
	_, err = h.Delete(context.Background(), &lbv1.DeleteNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: string(lb.ID),
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "listener")
}

func TestIntegration_AttachTargetGroup_RegionMismatch(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)

	w, err := repo.Writer(context.Background())
	require.NoError(t, err)
	lb := &domain.LoadBalancer{
		ID:        domain.ResourceID(ids.NewID(ids.PrefixLoadBalancer)),
		ProjectID: "prj-z", RegionID: "ru-central1",
		Name: "edge", Type: domain.LBTypeExternal, Status: domain.LBStatusInactive,
		SessionAffinity: domain.SessionAffinity5Tuple,
	}
	_, err = w.LoadBalancers().Insert(context.Background(), lb)
	require.NoError(t, err)
	tg := &domain.TargetGroup{
		ID:        domain.ResourceID(ids.NewID(ids.PrefixTargetGroup)),
		ProjectID: "prj-z", RegionID: "ru-central2", Name: "tg-1",
		DeregistrationDelaySeconds: 300,
		Status:                     domain.TargetGroupStatusActive,
		HealthCheck: domain.HealthCheck{
			Name: "hc", Interval: domain.DefaultHealthInterval, Timeout: domain.DefaultHealthTimeout,
			UnhealthyThreshold: domain.DefaultUnhealthyThreshold, HealthyThreshold: domain.DefaultHealthyThreshold,
			TCP: &domain.HealthCheckTCP{Port: 80},
		},
	}
	_, err = w.TargetGroups().Insert(context.Background(), tg)
	require.NoError(t, err)
	require.NoError(t, w.Commit())

	_, err = h.AttachTargetGroup(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: string(lb.ID),
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: string(tg.ID)},
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "region mismatch")
}

func TestIntegration_AttachTargetGroup_HappyPath_AndStatusRecompute(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)

	w, err := repo.Writer(context.Background())
	require.NoError(t, err)
	lbID := ids.NewID(ids.PrefixLoadBalancer)
	lb := &domain.LoadBalancer{
		ID: domain.ResourceID(lbID), ProjectID: "prj-acme",
		RegionID: "ru-central1", Name: "edge",
		Type: domain.LBTypeExternal, Status: domain.LBStatusInactive,
		SessionAffinity: domain.SessionAffinity5Tuple,
	}
	_, err = w.LoadBalancers().Insert(context.Background(), lb)
	require.NoError(t, err)
	tgID := ids.NewID(ids.PrefixTargetGroup)
	tg := &domain.TargetGroup{
		ID: domain.ResourceID(tgID), ProjectID: "prj-acme", RegionID: "ru-central1",
		Name: "tg-1", DeregistrationDelaySeconds: 300,
		Status: domain.TargetGroupStatusActive,
		HealthCheck: domain.HealthCheck{
			Name: "hc", Interval: domain.DefaultHealthInterval, Timeout: domain.DefaultHealthTimeout,
			UnhealthyThreshold: domain.DefaultUnhealthyThreshold, HealthyThreshold: domain.DefaultHealthyThreshold,
			TCP: &domain.HealthCheckTCP{Port: 80},
		},
	}
	_, err = w.TargetGroups().Insert(context.Background(), tg)
	require.NoError(t, err)
	require.NoError(t, w.Commit())
	// Insert listener (raw SQL) after LB committed so trigger lb_status_recompute fires.
	_, err = pool.Exec(context.Background(), `
		INSERT INTO kacho_nlb.listeners (id, project_id, load_balancer_id, region_id, name,
			description, labels, protocol, port, target_port, ip_version,
			address_id, allocated_address, subnet_id, proxy_protocol_v2, default_target_group_id, status)
		VALUES ($1, $2, $3, $4, 'lst-1', '', '{}', 'TCP', 8080, 80, 'IPV4',
		        '', '203.0.113.5', '', false, '', 'ACTIVE')`,
		ids.NewID(ids.PrefixListener), "prj-acme", lbID, "ru-central1",
	)
	require.NoError(t, err)

	op, err := h.AttachTargetGroup(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.NoError(t, err)
	final := pollOpDone(t, opsRepo, op.GetId())
	require.Nilf(t, final.Error, "op err: %v", final.Error)

	// Verify pivot row inserted.
	rd, err := repo.Reader(context.Background())
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	rec, err := rd.AttachedTargetGroups().Get(context.Background(), lbID, tgID)
	require.NoError(t, err)
	require.Equal(t, lbID, rec.LoadBalancerID)

	// Trigger lb_status_recompute should have switched status to ACTIVE.
	lbRec, err := rd.LoadBalancers().Get(context.Background(), lbID)
	require.NoError(t, err)
	require.Equal(t, domain.LBStatusActive, lbRec.Status, "trigger should have moved INACTIVE → ACTIVE")
}

func TestIntegration_AttachTargetGroup_Concurrent_OnlyOneInsert(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)

	w, err := repo.Writer(context.Background())
	require.NoError(t, err)
	lbID := ids.NewID(ids.PrefixLoadBalancer)
	tgID := ids.NewID(ids.PrefixTargetGroup)
	_, err = w.LoadBalancers().Insert(context.Background(), &domain.LoadBalancer{
		ID: domain.ResourceID(lbID), ProjectID: "prj-c", RegionID: "ru-central1",
		Name: "edge", Type: domain.LBTypeExternal, Status: domain.LBStatusInactive,
		SessionAffinity: domain.SessionAffinity5Tuple,
	})
	require.NoError(t, err)
	_, err = w.TargetGroups().Insert(context.Background(), &domain.TargetGroup{
		ID: domain.ResourceID(tgID), ProjectID: "prj-c", RegionID: "ru-central1",
		Name: "tg-1", DeregistrationDelaySeconds: 300, Status: domain.TargetGroupStatusActive,
		HealthCheck: domain.HealthCheck{
			Name: "hc", Interval: domain.DefaultHealthInterval, Timeout: domain.DefaultHealthTimeout,
			UnhealthyThreshold: domain.DefaultUnhealthyThreshold, HealthyThreshold: domain.DefaultHealthyThreshold,
			TCP: &domain.HealthCheckTCP{Port: 80},
		},
	})
	require.NoError(t, err)
	require.NoError(t, w.Commit())

	// Fire 5 concurrent Attach RPCs for the same (lb, tg) pair.
	const N = 5
	ops := make([]string, N)
	for i := 0; i < N; i++ {
		op, err := h.AttachTargetGroup(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
			NetworkLoadBalancerId: lbID,
			AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
		})
		require.NoError(t, err)
		ops[i] = op.GetId()
	}
	for _, id := range ops {
		final := pollOpDone(t, opsRepo, id)
		require.Nil(t, final.Error)
	}
	// Verify exactly one pivot row.
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM kacho_nlb.attached_target_groups WHERE load_balancer_id=$1 AND target_group_id=$2`,
		lbID, tgID).Scan(&n))
	require.Equal(t, 1, n)
}

func TestIntegration_Move_Blocked_AttachedTG(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)

	w, err := repo.Writer(context.Background())
	require.NoError(t, err)
	lbID := ids.NewID(ids.PrefixLoadBalancer)
	tgID := ids.NewID(ids.PrefixTargetGroup)
	_, err = w.LoadBalancers().Insert(context.Background(), &domain.LoadBalancer{
		ID: domain.ResourceID(lbID), ProjectID: "prj-src", RegionID: "ru-central1",
		Name: "edge", Type: domain.LBTypeExternal, Status: domain.LBStatusInactive,
		SessionAffinity: domain.SessionAffinity5Tuple,
	})
	require.NoError(t, err)
	_, err = w.TargetGroups().Insert(context.Background(), &domain.TargetGroup{
		ID: domain.ResourceID(tgID), ProjectID: "prj-src", RegionID: "ru-central1",
		Name: "tg-1", DeregistrationDelaySeconds: 300, Status: domain.TargetGroupStatusActive,
		HealthCheck: domain.HealthCheck{
			Name: "hc", Interval: domain.DefaultHealthInterval, Timeout: domain.DefaultHealthTimeout,
			UnhealthyThreshold: domain.DefaultUnhealthyThreshold, HealthyThreshold: domain.DefaultHealthyThreshold,
			TCP: &domain.HealthCheckTCP{Port: 80},
		},
	})
	require.NoError(t, err)
	_, _, err = w.AttachedTargetGroups().Attach(context.Background(), lbID, tgID, 0)
	require.NoError(t, err)
	require.NoError(t, w.Commit())

	_, err = h.Move(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-dst",
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestIntegration_GetTargetStates_HappyPath(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)
	_ = pool

	w, err := repo.Writer(context.Background())
	require.NoError(t, err)
	lbID := ids.NewID(ids.PrefixLoadBalancer)
	tgID := ids.NewID(ids.PrefixTargetGroup)
	_, err = w.LoadBalancers().Insert(context.Background(), &domain.LoadBalancer{
		ID: domain.ResourceID(lbID), ProjectID: "prj-q", RegionID: "ru-central1",
		Name: "edge", Type: domain.LBTypeExternal, Status: domain.LBStatusActive,
		SessionAffinity: domain.SessionAffinity5Tuple,
	})
	require.NoError(t, err)
	_, err = w.TargetGroups().Insert(context.Background(), &domain.TargetGroup{
		ID: domain.ResourceID(tgID), ProjectID: "prj-q", RegionID: "ru-central1",
		Name: "tg-1", DeregistrationDelaySeconds: 300, Status: domain.TargetGroupStatusActive,
		HealthCheck: domain.HealthCheck{
			Name: "hc", Interval: domain.DefaultHealthInterval, Timeout: domain.DefaultHealthTimeout,
			UnhealthyThreshold: 2, HealthyThreshold: 2,
			TCP: &domain.HealthCheckTCP{Port: 80},
		},
		Targets: []domain.Target{
			{ExternalIP: &domain.TargetExternalIP{Address: "1.1.1.1"}, Weight: 100},
		},
	})
	require.NoError(t, err)
	require.NoError(t, w.Commit())

	resp, err := h.GetTargetStates(context.Background(), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetTargetStates(), 1)
}

func TestIntegration_ListOperations_FilterByResourceID(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)

	op, err := h.Create(context.Background(), internalAutoReq("prj-ops", "edge"))
	require.NoError(t, err)
	final := pollOpDone(t, opsRepo, op.GetId())
	require.Nilf(t, final.Error, "op err: %v", final.Error)

	// Find LB id from outbox payload.
	var lbID string
	row := pool.QueryRow(context.Background(),
		`SELECT resource_id FROM kacho_nlb.nlb_outbox WHERE action='CREATED' LIMIT 1`)
	require.NoError(t, row.Scan(&lbID))
	require.NotEmpty(t, lbID)

	resp, err := h.ListOperations(context.Background(), &lbv1.ListNetworkLoadBalancerOperationsRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetOperations())
}

func TestIntegration_Update_PathUpdatesPersisted(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)
	_ = pool

	w, err := repo.Writer(context.Background())
	require.NoError(t, err)
	lbID := ids.NewID(ids.PrefixLoadBalancer)
	_, err = w.LoadBalancers().Insert(context.Background(), &domain.LoadBalancer{
		ID: domain.ResourceID(lbID), ProjectID: "prj-u", RegionID: "ru-central1",
		Name: "edge-old", Description: "old", Type: domain.LBTypeExternal,
		Status: domain.LBStatusInactive, SessionAffinity: domain.SessionAffinity5Tuple,
	})
	require.NoError(t, err)
	require.NoError(t, w.Commit())

	op, err := h.Update(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		Name:                  "edge-new",
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"name"}},
	})
	require.NoError(t, err)
	final := pollOpDone(t, opsRepo, op.GetId())
	require.Nil(t, final.Error)

	rd, err := repo.Reader(context.Background())
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	got, err := rd.LoadBalancers().Get(context.Background(), lbID)
	require.NoError(t, err)
	require.Equal(t, domain.LbName("edge-new"), got.Name)
}

// TestIntegration_SessionAffinity_RoundTrip — Create persists an explicit
// session_affinity (CLIENT_IP_ONLY, accepted by the DB CHECK); Update flips it
// back via update_mask.
func TestIntegration_SessionAffinity_RoundTrip(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)
	h := makeHandler(t, repo, opsRepo)
	ctx := context.Background()

	saReq := internalAutoReq("prj-sa", "edge-sa")
	saReq.SessionAffinity = lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY
	op, err := h.Create(ctx, saReq)
	require.NoError(t, err)
	final := pollOpDone(t, opsRepo, op.GetId())
	require.Nilf(t, final.Error, "create error: %v", final.Error)

	rd, err := repo.Reader(ctx)
	require.NoError(t, err)
	lbs, _, err := rd.LoadBalancers().List(ctx, kachorepo.LoadBalancerFilter{ProjectID: "prj-sa"}, kachorepo.Pagination{})
	require.NoError(t, err)
	_ = rd.Close()
	require.Len(t, lbs, 1)
	lbID := string(lbs[0].ID)
	require.Equal(t, domain.SessionAffinityClientIPOnly, lbs[0].SessionAffinity)

	// Update flips session_affinity back via mask.
	opU, err := h.Update(ctx, &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		SessionAffinity:       lbv1.NetworkLoadBalancer_FIVE_TUPLE,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"session_affinity"}},
	})
	require.NoError(t, err)
	require.Nil(t, pollOpDone(t, opsRepo, opU.GetId()).Error)
	rd3, err := repo.Reader(ctx)
	require.NoError(t, err)
	got, err := rd3.LoadBalancers().Get(ctx, lbID)
	require.NoError(t, err)
	_ = rd3.Close()
	require.Equal(t, domain.SessionAffinity5Tuple, got.SessionAffinity)
}

// ---- Compile guard ----

var _ kachorepo.Repository = (*kachopg.Repository)(nil)
