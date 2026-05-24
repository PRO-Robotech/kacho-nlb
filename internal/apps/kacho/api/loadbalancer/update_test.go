package loadbalancer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func TestUpdate_HappyPath_PatchName(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		Name:                  "edge-v2",
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"name"}},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, "edge-v2", string(repo.lbs[lbID].Name))
}

func TestUpdate_ImmutableType(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewUpdateLoadBalancerUseCase(repo, newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"type"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestUpdate_ImmutableProjectID(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewUpdateLoadBalancerUseCase(repo, newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"project_id"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestUpdate_UnknownField(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewUpdateLoadBalancerUseCase(repo, newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"frobnicate"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestUpdate_EmptyMask_FullPatch(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, nil)
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		Name:                  "renamed",
		Labels:                map[string]string{"k": "v"},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.LbName("renamed"), repo.lbs[lbID].Name)
}

func TestUpdate_NotFound(t *testing.T) {
	t.Parallel()
	uc := NewUpdateLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: "nlb-nope",
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		Name:                  "edge",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestUpdate_EmptyID(t *testing.T) {
	t.Parallel()
	uc := NewUpdateLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// Cover apply func — full patch sets all mutable fields from req.
func TestApplyUpdateMask_FullPatch(t *testing.T) {
	t.Parallel()
	cur := domain.LoadBalancer{
		Name: "old", Description: "old", DeletionProtection: false,
		Labels: domain.LabelsFromMap(map[string]string{"k": "old"}),
	}
	req := &lbv1.UpdateNetworkLoadBalancerRequest{
		Name: "new", Description: "new", DeletionProtection: true,
		Labels: map[string]string{"k": "new"},
	}
	out := applyUpdateMask(cur, req, nil)
	require.Equal(t, domain.LbName("new"), out.Name)
	require.True(t, out.DeletionProtection)
}
