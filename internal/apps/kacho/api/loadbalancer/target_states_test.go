package loadbalancer

import (
	"context"
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

func TestGetTargetStates_RequiresIDs(t *testing.T) {
	t.Parallel()
	uc := NewGetTargetStatesUseCase(newFakeRepo())
	_, err := uc.Execute(context.Background(), &lbv1.GetTargetStatesRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetTargetStates_LBNotFound(t *testing.T) {
	t.Parallel()
	uc := NewGetTargetStatesUseCase(newFakeRepo())
	_, err := uc.Execute(context.Background(), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: "nlb-x",
		TargetGroupId:         "tgr-y",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestGetTargetStates_HappyPath_EmptyTargets(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	uc := NewGetTargetStatesUseCase(repo)
	resp, err := uc.Execute(context.Background(), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.NoError(t, err)
	require.Empty(t, resp.GetTargetStates(), "fake.ListTargets returns nil → empty states")
}

// computeTargetState — directly test deterministic ramp matrix.

func TestComputeTargetState_LBStoppedInactive(t *testing.T) {
	t.Parallel()
	hc := domain.HealthCheck{Interval: domain.DefaultHealthInterval, HealthyThreshold: 2}
	tg := &kachorepo.TargetRecord{
		ID: ids.NewID(ids.PrefixTargetGroup), Status: "ACTIVE",
		CreatedAt: time.Now().Add(-1 * time.Hour),
		Target:    domain.Target{ExternalIP: &domain.TargetExternalIP{Address: "8.8.8.8"}},
	}
	got := computeTargetState(domain.LBStatusStopped, hc, tg, time.Now())
	require.Equal(t, lbv1.TargetState_INACTIVE, got.GetStatus())
}

func TestComputeTargetState_TargetDraining(t *testing.T) {
	t.Parallel()
	hc := domain.HealthCheck{Interval: domain.DefaultHealthInterval, HealthyThreshold: 2}
	tg := &kachorepo.TargetRecord{
		Status:    "DRAINING",
		CreatedAt: time.Now().Add(-1 * time.Hour),
		Target:    domain.Target{ExternalIP: &domain.TargetExternalIP{Address: "8.8.8.8"}},
	}
	got := computeTargetState(domain.LBStatusActive, hc, tg, time.Now())
	require.Equal(t, lbv1.TargetState_DRAINING, got.GetStatus())
}

func TestComputeTargetState_InitialRamp(t *testing.T) {
	t.Parallel()
	hc := domain.HealthCheck{
		Interval:         domain.LbDuration(2 * time.Second),
		HealthyThreshold: 3,
	}
	now := time.Now()
	tg := &kachorepo.TargetRecord{
		Status:    "ACTIVE",
		CreatedAt: now.Add(-1 * time.Second), // age=1s, ramp=6s → INITIAL
		Target:    domain.Target{ExternalIP: &domain.TargetExternalIP{Address: "8.8.8.8"}},
	}
	got := computeTargetState(domain.LBStatusActive, hc, tg, now)
	require.Equal(t, lbv1.TargetState_INITIAL, got.GetStatus())
}

func TestComputeTargetState_RampElapsedHealthy(t *testing.T) {
	t.Parallel()
	hc := domain.HealthCheck{
		Interval:         domain.LbDuration(1 * time.Second),
		HealthyThreshold: 2,
	}
	now := time.Now()
	tg := &kachorepo.TargetRecord{
		Status:    "ACTIVE",
		CreatedAt: now.Add(-1 * time.Hour), // ramp long over
		Target:    domain.Target{ExternalIP: &domain.TargetExternalIP{Address: "8.8.8.8"}},
	}
	got := computeTargetState(domain.LBStatusActive, hc, tg, now)
	require.Equal(t, lbv1.TargetState_HEALTHY, got.GetStatus())
}

func TestComputeTargetState_AddressOfTarget(t *testing.T) {
	t.Parallel()
	require.Equal(t, "10.0.0.5", addressOfTarget(domain.Target{
		IPRef: &domain.TargetIPRef{SubnetID: "sub-x", Address: "10.0.0.5"},
	}))
	require.Equal(t, "1.1.1.1", addressOfTarget(domain.Target{
		ExternalIP: &domain.TargetExternalIP{Address: "1.1.1.1"},
	}))
}
