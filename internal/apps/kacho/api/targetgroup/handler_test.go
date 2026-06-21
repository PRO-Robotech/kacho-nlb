package targetgroup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// TestHandler_RoutesAllRPCs — sanity-check: handler does dispatch to underlying
// use-case'ы для каждого RPC method'а. Use-case'ы возвращают expected errors
// (invalid arg / not found) — handler не должен глотать или менять status code.
func TestHandler_RoutesAllRPCs(t *testing.T) {
	h := NewHandler(
		newFakeRepo(), newFakeOpsRepo(),
		&fakeProjectClient{}, &fakeRegionClient{},
		&fakeInstanceClient{}, &fakeNICClient{}, &fakeSubnetClient{},
		nil, nil,
	)

	t.Run("Get empty id → InvalidArgument", func(t *testing.T) {
		_, err := h.Get(context.Background(), &lbv1.GetTargetGroupRequest{})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("List empty project_id → InvalidArgument", func(t *testing.T) {
		_, err := h.List(context.Background(), &lbv1.ListTargetGroupsRequest{})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("Create missing project → InvalidArgument", func(t *testing.T) {
		_, err := h.Create(context.Background(), &lbv1.CreateTargetGroupRequest{})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("Update empty id → InvalidArgument", func(t *testing.T) {
		_, err := h.Update(context.Background(), &lbv1.UpdateTargetGroupRequest{})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("Delete empty id → InvalidArgument", func(t *testing.T) {
		_, err := h.Delete(context.Background(), &lbv1.DeleteTargetGroupRequest{})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("Move missing dst → InvalidArgument", func(t *testing.T) {
		_, err := h.Move(context.Background(), &lbv1.MoveTargetGroupRequest{
			TargetGroupId: "tgr-x",
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("AddTargets empty list → InvalidArgument", func(t *testing.T) {
		_, err := h.AddTargets(context.Background(), &lbv1.AddTargetsRequest{
			TargetGroupId: "tgr-x",
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("RemoveTargets empty list → InvalidArgument", func(t *testing.T) {
		_, err := h.RemoveTargets(context.Background(), &lbv1.RemoveTargetsRequest{
			TargetGroupId: "tgr-x",
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})

	t.Run("ListOperations empty id → InvalidArgument", func(t *testing.T) {
		_, err := h.ListOperations(context.Background(), &lbv1.ListTargetGroupOperationsRequest{})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}

// nil-deps tolerance: handler с nil-зависимостями всё равно создаётся.
func TestHandler_TolerantToNilPeers(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "nil-peers")
	repo.seedTG(tg)
	h := NewHandler(repo, newFakeOpsRepo(), nil, nil, nil, nil, nil, nil, nil)

	// Get all-sync path работает без peer'ов.
	resp, err := h.Get(context.Background(), &lbv1.GetTargetGroupRequest{
		TargetGroupId: string(tg.ID),
	})
	require.NoError(t, err)
	assert.Equal(t, string(tg.ID), resp.GetId())
}
