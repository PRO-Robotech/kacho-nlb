package loadbalancer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestMove_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-src", "edge")
	opsRepo := newFakeOpsRepo()
	fga := &fakeHierarchy{}
	uc := NewMoveLoadBalancerUseCase(repo, opsRepo, &fakeProjectClient{}, fga, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-dst",
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.ProjectID("prj-dst"), repo.lbs[lbID].ProjectID)
	require.Len(t, fga.rewriteCalls, 1, "expected FGA rewrite call")
}

func TestMove_SameProject(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewMoveLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-a",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestMove_BlockedIfAttachedTG(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.pivot[lbID+"/tgr-fake"] = &kachorepo.AttachedTargetGroupRecord{
		LoadBalancerID: lbID, TargetGroupID: "tgr-fake",
	}
	uc := NewMoveLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-dst",
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestMove_EmptyDst(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewMoveLoadBalancerUseCase(repo, newFakeOpsRepo(), nil, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestMove_NotFound(t *testing.T) {
	t.Parallel()
	uc := NewMoveLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: "nlb-x",
		DestinationProjectId:  "prj-dst",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}
