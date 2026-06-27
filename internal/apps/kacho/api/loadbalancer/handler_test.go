package loadbalancer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// TestHandler_DispatchesAll — Handler — тонкая обёртка над use-case'ами.
// Тест проверяет, что каждый RPC handler-метода действительно вызывает
// соответствующий use-case (а не panic'ит / возвращает unimplemented).
func TestHandler_DispatchesAll(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg-1")
	opsRepo := newFakeOpsRepo()
	h := NewHandler(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{}, nil, slog.Default())

	ctx := context.Background()

	// Get
	got, err := h.Get(ctx, &lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: lbID})
	require.NoError(t, err)
	require.Equal(t, "edge", got.GetName())

	// List
	resp, err := h.List(ctx, &lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetNetworkLoadBalancers())

	// Create
	op, err := h.Create(ctx, &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1", Name: "edge-2",
		Type: lbv1.NetworkLoadBalancer_EXTERNAL,
	})
	require.NoError(t, err)
	require.NotEmpty(t, op.GetId())
	awaitOpDone(t, opsRepo, op.GetId())

	// Update
	op2, err := h.Update(ctx, &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID, Name: "edge-renamed",
	})
	require.NoError(t, err)
	awaitOpDone(t, opsRepo, op2.GetId())

	// Stop → Start (precondition: ACTIVE).
	repo.lbs[lbID].Status = domain.LBStatusActive
	opStop, err := h.Stop(ctx, &lbv1.StopNetworkLoadBalancerRequest{NetworkLoadBalancerId: lbID})
	require.NoError(t, err)
	awaitOpDone(t, opsRepo, opStop.GetId())
	opStart, err := h.Start(ctx, &lbv1.StartNetworkLoadBalancerRequest{NetworkLoadBalancerId: lbID})
	require.NoError(t, err)
	awaitOpDone(t, opsRepo, opStart.GetId())

	// AttachTG
	opAttach, err := h.AttachTargetGroup(ctx, &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.NoError(t, err)
	awaitOpDone(t, opsRepo, opAttach.GetId())

	// DetachTG
	opDetach, err := h.DetachTargetGroup(ctx, &lbv1.DetachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.NoError(t, err)
	awaitOpDone(t, opsRepo, opDetach.GetId())

	// GetTargetStates
	_, err = h.GetTargetStates(ctx, &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.NoError(t, err)

	// ListOperations
	_, err = h.ListOperations(ctx, &lbv1.ListNetworkLoadBalancerOperationsRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.NoError(t, err)

	// Move (need destination project; tg already detached)
	opMove, err := h.Move(ctx, &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID, DestinationProjectId: "prj-b",
	})
	require.NoError(t, err)
	awaitOpDone(t, opsRepo, opMove.GetId())

	// Delete (ensure no listeners/TG)
	opDel, err := h.Delete(ctx, &lbv1.DeleteNetworkLoadBalancerRequest{NetworkLoadBalancerId: lbID})
	require.NoError(t, err)
	awaitOpDone(t, opsRepo, opDel.GetId())
}

func TestHandler_NewHandler_NilLogger_OK(t *testing.T) {
	t.Parallel()
	h := NewHandler(newFakeRepo(), newFakeOpsRepo(), nil, nil, nil, nil)
	require.NotNil(t, h)
}

func TestHandler_Get_PropagatesErr(t *testing.T) {
	t.Parallel()
	h := NewHandler(newFakeRepo(), newFakeOpsRepo(), nil, nil, nil, slog.Default())
	_, err := h.Get(context.Background(), &lbv1.GetNetworkLoadBalancerRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
