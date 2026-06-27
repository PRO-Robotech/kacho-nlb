package loadbalancer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestGetLoadBalancerUseCase_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := ids.NewID(ids.PrefixLoadBalancer)
	repo.lbs[lbID] = &kachorepo.LoadBalancerRecord{
		LoadBalancer: domain.LoadBalancer{
			ID: domain.ResourceID(lbID), ProjectID: "prj-abc",
			RegionID: "ru-central1", Name: "edge", Type: domain.LBTypeExternal,
			Status: domain.LBStatusInactive, SessionAffinity: domain.SessionAffinity5Tuple,
		},
	}
	uc := NewGetLoadBalancerUseCase(repo)
	out, err := uc.Execute(context.Background(), &lbv1.GetNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.NoError(t, err)
	require.Equal(t, lbID, out.GetId())
	require.Equal(t, "edge", out.GetName())
	require.Equal(t, lbv1.NetworkLoadBalancer_INACTIVE, out.GetStatus())
}

func TestGetLoadBalancerUseCase_NotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := NewGetLoadBalancerUseCase(repo)
	_, err := uc.Execute(context.Background(), &lbv1.GetNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: "nlb-missing",
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetLoadBalancerUseCase_EmptyID(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := NewGetLoadBalancerUseCase(repo)
	_, err := uc.Execute(context.Background(), &lbv1.GetNetworkLoadBalancerRequest{})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
