package loadbalancer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ---- Start ------------------------------------------------------------------

func TestStart_FromStopped_NoChildren_GoesInactive(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.lbs[lbID].Status = domain.LBStatusStopped
	opsRepo := newFakeOpsRepo()
	uc := NewStartLoadBalancerUseCase(repo, opsRepo, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.StartNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.LBStatusInactive, repo.lbs[lbID].Status)
}

func TestStart_FromInactive_WithChildren_GoesActive(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := ids.NewID(ids.PrefixTargetGroup)
	repo.lbs[lbID].Status = domain.LBStatusInactive
	repo.lists[lbID] = []*kachorepo.ListenerRecord{
		{Listener: domain.Listener{ID: domain.ResourceID(ids.NewID(ids.PrefixListener)), LoadBalancerID: domain.ResourceID(lbID)}},
	}
	repo.pivot[lbID+"/"+tgID] = &kachorepo.AttachedTargetGroupRecord{
		LoadBalancerID: lbID, TargetGroupID: tgID,
	}
	opsRepo := newFakeOpsRepo()
	uc := NewStartLoadBalancerUseCase(repo, opsRepo, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.StartNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.LBStatusActive, repo.lbs[lbID].Status)
}

func TestStart_WrongStatus(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.lbs[lbID].Status = domain.LBStatusActive
	uc := NewStartLoadBalancerUseCase(repo, newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.StartNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestStart_NotFound(t *testing.T) {
	t.Parallel()
	uc := NewStartLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.StartNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: "nlb-x",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// ---- Stop ------------------------------------------------------------------

func TestStop_FromActive_GoesStopped(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.lbs[lbID].Status = domain.LBStatusActive
	opsRepo := newFakeOpsRepo()
	uc := NewStopLoadBalancerUseCase(repo, opsRepo, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.StopNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.LBStatusStopped, repo.lbs[lbID].Status)
}

func TestStop_FromInactive_GoesStopped(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.lbs[lbID].Status = domain.LBStatusInactive
	opsRepo := newFakeOpsRepo()
	uc := NewStopLoadBalancerUseCase(repo, opsRepo, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.StopNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
}

func TestStop_WrongStatus(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.lbs[lbID].Status = domain.LBStatusStopped
	uc := NewStopLoadBalancerUseCase(repo, newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.StopNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
