package listener

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestHandler_RoutesEachRPC — smoke-tests that Handler thin-transport correctly
// delegates each ListenerServiceServer method to its UseCase. We don't re-test
// business semantics here (covered exhaustively in create/update/delete/read tests);
// just verify request → handler → use-case wiring + proto-response marshalling.
func TestHandler_RoutesEachRPC(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lb := newRecordLB(t, "prj01HANDLERROUTING1", "ru-central1", domain.LBTypeExternal, "h-lb")
	repo.seedLB(lb)
	listener := &kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID:               domain.ResourceID(ids.NewID(ids.PrefixListener)),
			LoadBalancerID:   lb.ID,
			ProjectID:        lb.ProjectID,
			RegionID:         lb.RegionID,
			Name:             domain.LbName("handler-routed"),
			Labels:           domain.LbLabels{},
			Protocol:         domain.ProtoTCP,
			Port:             8080,
			TargetPort:       80,
			IPVersion:        domain.IPVersionV4,
			AllocatedAddress: "203.0.113.99",
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	repo.seedListener(listener)

	ops := newFakeOpsRepo()
	internalAddrs := newFakeInternalAddressClient()
	h := NewHandler(repo, ops, newFakeAddressClient(), internalAddrs, newFakeSubnetClient(), newFakeHierarchyWriter(), slog.Default())

	t.Run("Get", func(t *testing.T) {
		t.Parallel()
		got, err := h.Get(context.Background(), &lbv1.GetListenerRequest{ListenerId: string(listener.ID)})
		require.NoError(t, err)
		require.Equal(t, string(listener.ID), got.Id)
	})

	t.Run("List", func(t *testing.T) {
		t.Parallel()
		resp, err := h.List(context.Background(), &lbv1.ListListenersRequest{
			ProjectId:      string(lb.ProjectID),
			LoadBalancerId: string(lb.ID),
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(resp.Listeners), 1)
	})

	t.Run("Create", func(t *testing.T) {
		t.Parallel()
		op, err := h.Create(context.Background(), &lbv1.CreateListenerRequest{
			LoadBalancerId: string(lb.ID),
			Name:           "h-created",
			Protocol:       lbv1.Listener_TCP,
			Port:           81,
			TargetPort:     8081,
			IpVersion:      lbv1.IpVersion_IPV4,
			AddressSpec:    autoSpec(""),
		})
		require.NoError(t, err)
		require.NotEmpty(t, op.Id)
		awaitOpDone(t, ops, op.Id, time.Second)
	})

	t.Run("Update", func(t *testing.T) {
		t.Parallel()
		op, err := h.Update(context.Background(), &lbv1.UpdateListenerRequest{
			ListenerId: string(listener.ID),
			UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"description"}},
			Description: "handler-set",
		})
		require.NoError(t, err)
		awaitOpDone(t, ops, op.Id, time.Second)
	})

	t.Run("Delete", func(t *testing.T) {
		t.Parallel()
		// Seed an extra listener to delete in isolation from above flow.
		toDelete := *listener
		toDelete.ID = domain.ResourceID(ids.NewID(ids.PrefixListener))
		toDelete.Port = 7000
		toDelete.AddressID = listener.AddressID
		repo.seedListener(&toDelete)
		op, err := h.Delete(context.Background(), &lbv1.DeleteListenerRequest{
			ListenerId: string(toDelete.ID),
		})
		require.NoError(t, err)
		awaitOpDone(t, ops, op.Id, time.Second)
	})

	t.Run("ListOperations", func(t *testing.T) {
		t.Parallel()
		resp, err := h.ListOperations(context.Background(), &lbv1.ListListenerOperationsRequest{
			ListenerId: string(listener.ID),
		})
		require.NoError(t, err)
		_ = resp // operations may not be present in this thin smoke test
	})

	t.Run("Get_EmptyID_InvalidArgument", func(t *testing.T) {
		t.Parallel()
		_, err := h.Get(context.Background(), &lbv1.GetListenerRequest{ListenerId: ""})
		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}
