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

// newCreateUCSG — Create UC c подменяемым SecurityGroupClient (project/region/
// network — OK-фейки; network отдаёт "enp-1").
func newCreateUCSG(repo *fakeRepo, opsRepo *fakeOpsRepo, sgc SecurityGroupClient) *CreateLoadBalancerUseCase {
	return NewCreateLoadBalancerUseCase(repo, opsRepo,
		&fakeProjectClient{}, &fakeRegionClient{}, &fakeNetworkClient{}, sgc, &fakeSubnetClient{}, &fakeAddressReader{}, &fakeAddressClient{}, slog.Default())
}

// internalSGReq — INTERNAL Create-request (auto v4) на enp-1 с заданным набором SG.
func internalSGReq(name string, sgs ...string) *lbv1.CreateNetworkLoadBalancerRequest {
	return &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1",
		Name: name, Type: lbv1.NetworkLoadBalancer_INTERNAL,
		NetworkId:        "enp-1",
		AddressSpec:      autoV4Spec(lbTestSubnetV4),
		SecurityGroupIds: sgs,
	}
}

// seedInternalLBWithSG — INTERNAL LB на network enp-1 с заданным набором SG.
func seedInternalLBWithSG(t *testing.T, repo *fakeRepo, projectID, name string, sgs ...string) string {
	t.Helper()
	id := seedLB(t, repo, projectID, name)
	lb := repo.lbs[id]
	lb.Type = domain.LBTypeInternal
	lb.NetworkID = "enp-1"
	lb.SecurityGroupIDs = domain.SecurityGroupIDsFromStrings(sgs)
	return id
}

// TestCreateLoadBalancer_SecurityGroupNotFound — SG отсутствует в vpc → sync
// InvalidArgument "security group <id> not found"; LB не создаётся.
func TestCreateLoadBalancer_SecurityGroupNotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	sgc := &fakeSecurityGroupClient{getFunc: func(_ context.Context, sgID string) (*vpcclient.SecurityGroup, error) {
		return nil, fmt.Errorf("%w: SecurityGroup %s not found", domain.ErrInvalidArg, sgID)
	}}
	uc := newCreateUCSG(repo, newFakeOpsRepo(), sgc)
	_, err := uc.Execute(context.Background(), internalSGReq("internal-lb", "sgp-missing"))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "security group sgp-missing not found", status.Convert(err).Message())
	require.Empty(t, repo.lbs, "LB must not be persisted")
}

// TestCreateLoadBalancer_SecurityGroupOtherNetwork — SG принадлежит другой сети →
// sync InvalidArgument verbatim "security group <sg> does not belong to network <net>".
func TestCreateLoadBalancer_SecurityGroupOtherNetwork(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	sgc := &fakeSecurityGroupClient{getFunc: func(_ context.Context, sgID string) (*vpcclient.SecurityGroup, error) {
		return &vpcclient.SecurityGroup{ID: sgID, ProjectID: "prj-a", NetworkID: "enp-2", Name: "other-sg"}, nil
	}}
	uc := newCreateUCSG(repo, newFakeOpsRepo(), sgc)
	_, err := uc.Execute(context.Background(), internalSGReq("internal-lb", "sgp-other"))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "security group sgp-other does not belong to network enp-1",
		status.Convert(err).Message())
	require.Empty(t, repo.lbs, "LB must not be persisted")
}

// TestCreateLoadBalancer_SecurityGroupVPCUnavailable — vpc недоступен на валидации
// SG → sync Unavailable (fail-closed для мутации).
func TestCreateLoadBalancer_SecurityGroupVPCUnavailable(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	sgc := &fakeSecurityGroupClient{getFunc: func(_ context.Context, _ string) (*vpcclient.SecurityGroup, error) {
		return nil, fmt.Errorf("%w: dial", domain.ErrUnavailable)
	}}
	uc := newCreateUCSG(repo, newFakeOpsRepo(), sgc)
	_, err := uc.Execute(context.Background(), internalSGReq("internal-lb", "sgp-1"))
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Empty(t, repo.lbs, "LB must not be persisted")
}

// TestCreateLoadBalancer_SecurityGroupHappyPath — INTERNAL + валидные SG той же
// сети → Operation done, набор персистится.
func TestCreateLoadBalancer_SecurityGroupHappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	uc := newCreateUCSG(repo, opsRepo, &fakeSecurityGroupClient{})
	op, err := uc.Execute(context.Background(), internalSGReq("internal-lb", "sgp-1", "sgp-2"))
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	got := onlyLB(t, repo)
	require.Equal(t, []domain.SecurityGroupID{"sgp-1", "sgp-2"}, got.SecurityGroupIDs)
}

// TestUpdateLoadBalancer_SecurityGroupReplace — Update заменяет набор SG целиком
// (mutable, full-replace через update_mask).
func TestUpdateLoadBalancer_SecurityGroupReplace(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedInternalLBWithSG(t, repo, "prj-a", "internal-lb", "sgp-1")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, &fakeSecurityGroupClient{}, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"security_group_ids"}},
		SecurityGroupIds:      []string{"sgp-1", "sgp-2"},
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Equal(t, []domain.SecurityGroupID{"sgp-1", "sgp-2"}, repo.lbs[lbID].SecurityGroupIDs)
}

// TestUpdateLoadBalancer_SecurityGroupOtherNetworkRejected — Update с SG чужой
// сети → InvalidArgument; прежний набор SG сохранён.
func TestUpdateLoadBalancer_SecurityGroupOtherNetworkRejected(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedInternalLBWithSG(t, repo, "prj-a", "internal-lb", "sgp-1")
	sgc := &fakeSecurityGroupClient{getFunc: func(_ context.Context, sgID string) (*vpcclient.SecurityGroup, error) {
		return &vpcclient.SecurityGroup{ID: sgID, ProjectID: "prj-a", NetworkID: "enp-2", Name: "other-sg"}, nil
	}}
	uc := NewUpdateLoadBalancerUseCase(repo, newFakeOpsRepo(), sgc, slog.Default())
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"security_group_ids"}},
		SecurityGroupIds:      []string{"sgp-other"},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "security group sgp-other does not belong to network enp-1",
		status.Convert(err).Message())
	require.Equal(t, []domain.SecurityGroupID{"sgp-1"}, repo.lbs[lbID].SecurityGroupIDs,
		"prior SG set must be kept")
}

// TestUpdateLoadBalancer_SecurityGroupEmptyClears — пустой набор разрешён (снятие
// всех SG).
func TestUpdateLoadBalancer_SecurityGroupEmptyClears(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedInternalLBWithSG(t, repo, "prj-a", "internal-lb", "sgp-1")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, &fakeSecurityGroupClient{}, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"security_group_ids"}},
		SecurityGroupIds:      nil,
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Empty(t, repo.lbs[lbID].SecurityGroupIDs)
}
