// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Integration-тест per-family fan-out compensation на реальном Postgres
// (testcontainers): dualstack-Create, где v6-acquire падает после того, как v4
// уже аллоцирован и persisted в CREATING-handle. Проверяет, что compensation
// освобождает КАЖДЫЙ непустой address_id (здесь только v4 → FreeIP) и СНИМАЕТ
// durable-handle из реальной БД — LB не остаётся с половиной VIP. Unit-версия
// (in-memory fake repo) живёт в create_test.go; здесь — настоящие CAS-attach v4 и
// DELETE-compensation против DB.
package loadbalancer_test

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/loadbalancer"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
)

// fanoutCompensationStub — vpc InternalAddressClient, моделирующий отказ v6-acquire
// в середине fan-out: AllocateInternalIP(v4) → успех (фиксированный address_id),
// AllocateInternalIPv6(v6) → FAILED_PRECONDITION. Записывает FreeIP/ClearReference —
// чтобы тест проверил release уже аллоцированного v4 в compensation.
type fanoutCompensationStub struct {
	v4AddressID string

	mu      sync.Mutex
	freeIPs []string
	clears  []string
}

func (s *fanoutCompensationStub) AllocateInternalIP(_ context.Context, _ vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return &vpcclient.AllocateResponse{AddressID: s.v4AddressID, Value: "100.64.0.7"}, nil
}

func (s *fanoutCompensationStub) AllocateInternalIPv6(_ context.Context, _ vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	// v6-подсеть исчерпана — FAILED_PRECONDITION, валит второй side-effect.
	return nil, status.Error(codes.FailedPrecondition, "subnet exhausted")
}

func (s *fanoutCompensationStub) AllocateExternalIP(_ context.Context, _ vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return nil, status.Error(codes.Unavailable, "AllocateExternalIP not expected in fan-out compensation test")
}

func (s *fanoutCompensationStub) AllocateExternalIPv6(_ context.Context, _ vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return nil, status.Error(codes.Unavailable, "AllocateExternalIPv6 not expected in fan-out compensation test")
}

// AttachExisting — link в этом сценарии не используется; вызов означал бы баг.
func (s *fanoutCompensationStub) AttachExisting(_ context.Context, _ vpcclient.AttachExistingRequest) (*vpcclient.AllocateResponse, error) {
	return nil, status.Error(codes.Unavailable, "AttachExisting not expected in fan-out compensation test")
}

func (s *fanoutCompensationStub) FreeIP(_ context.Context, addressID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeIPs = append(s.freeIPs, addressID)
	return nil
}

func (s *fanoutCompensationStub) ClearReference(_ context.Context, addressID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clears = append(s.clears, addressID)
	return nil
}

func (s *fanoutCompensationStub) freedIPs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.freeIPs))
	copy(out, s.freeIPs)
	return out
}

func (s *fanoutCompensationStub) clearedRefs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.clears))
	copy(out, s.clears)
	return out
}

// TestIntegration_CreateLoadBalancer_FanoutCompensationOnV6Fail — dualstack Create,
// где v6-acquire падает: worker уже сделал acquire(v4)+persist(v4), затем
// acquire(v6) → FAILED_PRECONDITION. Compensation освобождает v4 (FreeIP по
// address_id_v4) и удаляет durable-handle; Operation done с error; реальная
// LB-строка не остаётся в БД.
func TestIntegration_CreateLoadBalancer_FanoutCompensationOnV6Fail(t *testing.T) {
	t.Parallel()
	pool, repo := setupDB(t)
	opsRepo := newOpsRepo(t, pool)

	stub := &fanoutCompensationStub{v4AddressID: "adr00000000000FANV4X"}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	// peer-clients project/region/zone/subnet/address — nil (sync-prechecks и
	// worker peer-check пропускаются); единственный side-effect — VIP fan-out.
	h := loadbalancer.NewHandler(repo, opsRepo, nil, nil, nil, nil, nil, nil, stub, nil, logger)

	req := &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-fanout", RegionId: "region-1", Name: "edge-ds-fail",
		Type:          lbv1.NetworkLoadBalancer_INTERNAL,
		PlacementType: lbv1.NetworkLoadBalancer_REGIONAL,
		V4Source:      &lbv1.VipSource{Source: &lbv1.VipSource_SubnetId{SubnetId: "sub-1"}},
		V6Source:      &lbv1.VipSource{Source: &lbv1.VipSource_SubnetId{SubnetId: "sub-6"}},
	}

	op, err := h.Create(context.Background(), req)
	require.NoError(t, err)
	require.False(t, op.GetDone())

	final := pollOpDone(t, opsRepo, op.GetId())
	require.NotNil(t, final.Error, "v6-acquire failure must surface as Operation error")
	require.Equal(t, int32(codes.FailedPrecondition), final.Error.GetCode())
	require.Equal(t, "could not allocate load balancer address", final.Error.GetMessage())

	// v4 уже persisted (owned auto) → освобождён two-step compensation'ом:
	// ClearReference(v4) → FreeIP(v4); v6 не аллоцировался → не освобождается.
	require.Equal(t, []string{"adr00000000000FANV4X"}, stub.freedIPs(), "v4 freed by compensation")
	require.Equal(t, []string{"adr00000000000FANV4X"}, stub.clearedRefs(), "owned v4 ref cleared before free (two-step)")

	// Durable-handle снят — реальная строка удалена; LB не остаётся с половиной VIP.
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT count(*) FROM kacho_nlb.load_balancers`).Scan(&n))
	require.Equal(t, 0, n, "durable handle removed by compensation")
}
