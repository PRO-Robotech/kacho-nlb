// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// VIP консолидирован на LoadBalancer: Listener.Create — чистый INSERT строки
// (FK на LB), без acquireVIP-саги и без обращения к vpc. Поэтому use-case больше
// не получает address/subnet-клиентов. Тесты покрывают: happy-path (ACTIVE +
// outbox + FGA, без VIP-аллокации), наследование vestigial ip_version от LB,
// валидацию формы и uniqueness, LB precond'ы.

const testTimeout = 2 * time.Second

// newCreateUC — use-case без address-клиентов (VIP не аллоцируется).
func newCreateUC(repo *fakeRepo, ops *fakeOpsRepo) *CreateUseCase {
	return NewCreateUseCase(repo, ops, slog.Default())
}

// seedParentLB — INACTIVE INTERNAL LB с anycast-VIP (родитель листенера).
func seedParentLB(t *testing.T, repo *fakeRepo, families ...domain.IPVersion) *kachorepo.LoadBalancerRecord {
	t.Helper()
	lb := newRecordLB(t, "prj01TESTPROJ0000001", "ru-central1", domain.LBTypeInternal, "test-lb")
	lb.Status = domain.LBStatusInactive
	if len(families) == 0 {
		families = []domain.IPVersion{domain.IPVersionV4}
	}
	lb.IPFamilies = families
	lb.AddressV4 = "10.0.0.5"
	repo.seedLB(lb)
	return lb
}

func newRecordLB(t *testing.T, projectID domain.ProjectID, regionID domain.RegionID, lbType domain.LBType, name string) *kachorepo.LoadBalancerRecord {
	t.Helper()
	lb := domain.NewLoadBalancer(projectID, regionID, domain.LbName(name), "", domain.LbLabels{}, lbType)
	lb.Status = domain.LBStatusActive
	lb.ID = domain.ResourceID(ids.NewID(ids.PrefixLoadBalancer))
	return &kachorepo.LoadBalancerRecord{
		LoadBalancer: lb,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
}

func listenerByLB(repo *fakeRepo, lbID string) []*kachorepo.ListenerRecord {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	var out []*kachorepo.ListenerRecord
	for _, l := range repo.listeners {
		if string(l.LoadBalancerID) == lbID {
			c := *l
			out = append(out, &c)
		}
	}
	return out
}

// TestCreateListener_HappyPath_NoVIP — Create на VIP-only LB: листенер ACTIVE,
// без собственных address-полей (наследует VIP от LB), outbox CREATED + LB
// UPDATED, FGA register-intent (creator + parent-link), без VIP-аллокации.
func TestCreateListener_HappyPath_NoVIP(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ops := newFakeOpsRepo()
	lb := seedParentLB(t, repo)
	uc := newCreateUC(repo, ops)

	op, err := uc.Run(contextWithSubject("user:test-actor"), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID),
		Name:           "https",
		Protocol:       lbv1.Listener_TCP,
		Port:           443,
		TargetPort:     8080,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, ops, op.ID, testTimeout)
	require.Nil(t, final.Error)

	got := listenerByLB(repo, string(lb.ID))
	require.Len(t, got, 1)
	require.Equal(t, domain.ListenerStatusActive, got[0].Status)
	require.Equal(t, domain.LbPort(443), got[0].Port)
	require.Equal(t, domain.LbPort(8080), got[0].TargetPort)
	// Листенер не аллоцировал собственный VIP.
	_, hasAddr := got[0].AddressID.Maybe()
	require.False(t, hasAddr, "listener carries no own address binding")
	require.Empty(t, string(got[0].AllocatedAddress))

	// Outbox: listener CREATED + lb UPDATED(listener_created).
	evts := repo.pendingOutbox()
	var sawListenerCreated, sawLBUpdated bool
	for _, e := range evts {
		if e.ResourceType == "nlb_listener" && e.Action == "CREATED" {
			sawListenerCreated = true
		}
		if e.ResourceType == "nlb_load_balancer" && e.Action == "UPDATED" {
			sawLBUpdated = true
		}
	}
	require.True(t, sawListenerCreated, "listener CREATED emitted")
	require.True(t, sawLBUpdated, "lb UPDATED emitted")

	// FGA register-intent: creator + parent-link.
	fga := repo.committedFGA()
	require.Len(t, fga, 1)
	require.Equal(t, domain.FGAEventRegister, fga[0].EventType)
	require.Equal(t, "Listener", fga[0].Intent.Kind)
}

// TestCreateListener_InheritsVestigialIPVersion — vestigial ip_version листенера
// берётся из первого семейства родительского LB (колонка снята с proto, остаётся
// в DB до поздней миграции).
func TestCreateListener_InheritsVestigialIPVersion(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ops := newFakeOpsRepo()
	lb := seedParentLB(t, repo, domain.IPVersionV6)
	uc := newCreateUC(repo, ops)

	op, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID),
		Name:           "v6port",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, ops, op.ID, testTimeout).Error)
	got := listenerByLB(repo, string(lb.ID))
	require.Len(t, got, 1)
	require.Equal(t, domain.IPVersionV6, got[0].IPVersion)
}

func TestCreateListener_LoadBalancerIDRequired(t *testing.T) {
	t.Parallel()
	uc := newCreateUC(newFakeRepo(), newFakeOpsRepo())
	_, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		Name: "x", Protocol: lbv1.Listener_TCP, Port: 80, TargetPort: 80,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateListener_NameRegexInvalid(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lb := seedParentLB(t, repo)
	uc := newCreateUC(repo, newFakeOpsRepo())
	_, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID), Name: "Bad Name!",
		Protocol: lbv1.Listener_TCP, Port: 443, TargetPort: 8080,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateListener_PortOutOfRange(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lb := seedParentLB(t, repo)
	uc := newCreateUC(repo, newFakeOpsRepo())
	_, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID), Name: "p",
		Protocol: lbv1.Listener_TCP, Port: 70000, TargetPort: 8080,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateListener_UnsupportedProtocol(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lb := seedParentLB(t, repo)
	uc := newCreateUC(repo, newFakeOpsRepo())
	_, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID), Name: "p",
		Protocol: lbv1.Listener_PROTOCOL_UNSPECIFIED, Port: 443, TargetPort: 8080,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestCreateListener_DuplicatePortProto — конфликт (load_balancer_id, port,
// protocol) → Operation done с error ALREADY_EXISTS.
func TestCreateListener_DuplicatePortProto(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ops := newFakeOpsRepo()
	lb := seedParentLB(t, repo)
	repo.seedListener(&kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID: "lst01EXISTING0000001", LoadBalancerID: lb.ID, ProjectID: lb.ProjectID,
			RegionID: lb.RegionID, Name: "existing", Protocol: domain.ProtoTCP,
			Port: 443, TargetPort: 8080, IPVersion: domain.IPVersionV4,
			Status: domain.ListenerStatusActive, VipOrigin: domain.VipOriginAuto,
		},
	})
	uc := newCreateUC(repo, ops)
	op, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID), Name: "dup",
		Protocol: lbv1.Listener_TCP, Port: 443, TargetPort: 9090,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, ops, op.ID, testTimeout)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.AlreadyExists), final.Error.GetCode())
}

func TestCreateListener_LBNotFound(t *testing.T) {
	t.Parallel()
	uc := newCreateUC(newFakeRepo(), newFakeOpsRepo())
	_, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: "nlb00000000000000miss", Name: "x",
		Protocol: lbv1.Listener_TCP, Port: 80, TargetPort: 80,
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestCreateListener_LBDeleting(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lb := seedParentLB(t, repo)
	lb.Status = domain.LBStatusDeleting
	uc := newCreateUC(repo, newFakeOpsRepo())
	_, err := uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb.ID), Name: "x",
		Protocol: lbv1.Listener_TCP, Port: 80, TargetPort: 80,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, err.Error(), "being deleted")
}
