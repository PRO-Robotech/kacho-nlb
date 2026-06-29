// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// newCreateUCNet — Create UC c подменяемым NetworkClient (project/region — OK-фейки).
func newCreateUCNet(repo *fakeRepo, opsRepo *fakeOpsRepo, nc NetworkClient) *CreateLoadBalancerUseCase {
	return NewCreateLoadBalancerUseCase(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{}, nc, &fakeSecurityGroupClient{}, slog.Default())
}

// TestCreateLoadBalancer_InternalRequiresNetworkID — INTERNAL без network_id
// отвергается sync (cross-field инвариант, design §0 #7).
func TestCreateLoadBalancer_InternalRequiresNetworkID(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := newCreateUCNet(repo, newFakeOpsRepo(), &fakeNetworkClient{})
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: "internal-lb", Type: lbv1.NetworkLoadBalancer_INTERNAL,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, lbFieldViolations(err),
		"network_id: network_id is required for INTERNAL load balancer")
	require.Empty(t, repo.lbs, "LB must not be persisted")
}

// TestCreateLoadBalancer_ExternalRejectsNetworkID — EXTERNAL с network_id
// отвергается sync (публичный VIP не из сети).
func TestCreateLoadBalancer_ExternalRejectsNetworkID(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := newCreateUCNet(repo, newFakeOpsRepo(), &fakeNetworkClient{})
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: "edge", Type: lbv1.NetworkLoadBalancer_EXTERNAL,
		NetworkId: "enp-1",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, lbFieldViolations(err),
		"network_id: network_id is only valid for INTERNAL load balancer")
	require.Empty(t, repo.lbs, "LB must not be persisted")
}

// TestCreateLoadBalancer_InternalNetworkNotFound — network_id отсутствует в vpc
// → sync InvalidArgument "network <id> not found" (cross-domain, fail-closed).
func TestCreateLoadBalancer_InternalNetworkNotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	nc := &fakeNetworkClient{getFunc: func(_ context.Context, networkID string) (*vpcclient.Network, error) {
		return nil, fmt.Errorf("%w: Network %s not found", domain.ErrInvalidArg, networkID)
	}}
	uc := newCreateUCNet(repo, newFakeOpsRepo(), nc)
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: "internal-lb", Type: lbv1.NetworkLoadBalancer_INTERNAL,
		NetworkId: "enp-missing",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "network enp-missing not found", status.Convert(err).Message())
	require.Empty(t, repo.lbs, "LB must not be persisted")
}

// TestCreateLoadBalancer_InternalVPCUnavailable — vpc недоступен на валидации
// network → sync Unavailable (fail-closed для мутации).
func TestCreateLoadBalancer_InternalVPCUnavailable(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	nc := &fakeNetworkClient{getFunc: func(_ context.Context, _ string) (*vpcclient.Network, error) {
		return nil, fmt.Errorf("%w: dial", domain.ErrUnavailable)
	}}
	uc := newCreateUCNet(repo, newFakeOpsRepo(), nc)
	_, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: "internal-lb", Type: lbv1.NetworkLoadBalancer_INTERNAL,
		NetworkId: "enp-1",
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Empty(t, repo.lbs, "LB must not be persisted")
}

// TestCreateLoadBalancer_InternalHappyPath — INTERNAL + валидный network_id →
// Operation done, network_id персистится.
func TestCreateLoadBalancer_InternalHappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	uc := newCreateUCNet(repo, opsRepo, &fakeNetworkClient{})
	op, err := uc.Execute(context.Background(), &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: "internal-lb", Type: lbv1.NetworkLoadBalancer_INTERNAL,
		NetworkId: "enp-1",
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	got := onlyLB(t, repo)
	require.Equal(t, domain.LBTypeInternal, got.Type)
	require.Equal(t, domain.NetworkID("enp-1"), got.NetworkID)
}

// TestUpdateLoadBalancer_NetworkIDImmutable — network_id в update_mask →
// InvalidArgument с verbatim immutable-текстом.
func TestUpdateLoadBalancer_NetworkIDImmutable(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewUpdateLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeSecurityGroupClient{}, slog.Default())
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"network_id"}},
		NetworkId:             "enp-2",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "network_id is immutable after NetworkLoadBalancer.Create",
		status.Convert(err).Message())
}
