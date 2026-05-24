package listener

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestDeleteListener_GWT_LST_022_AutoAlloc_FreeIP — auto-alloc Listener:
// FreeIP called for the address; outbox emits DELETED + LB UPDATED.
func TestDeleteListener_GWT_LST_022_AutoAlloc_FreeIP(t *testing.T) {
	t.Parallel()
	suite := newDeleteSuite(t)
	const autoAddrID = "e9bAUTOALLOCADDR001"
	suite.listener.AddressID = option.MustNewOption(domain.AddressID(autoAddrID))
	suite.repo.seedListener(suite.listener)
	// Auto-alloc heuristic relies on Address.Name == "nlb-listener-<short-id>"
	suite.addresses.seed(&vpcclient.Address{
		ID:        autoAddrID,
		ProjectID: string(suite.listener.ProjectID),
		Name:      "nlb-listener-" + domain.TruncateID(suite.listener.ID),
		Value:     string(suite.listener.AllocatedAddress),
		Family:    vpcclient.AddressFamilyIPv4,
		UsedBy: &vpcclient.AddressOwner{
			Kind: addressOwnerKindNLBListener,
			ID:   string(suite.listener.ID),
		},
	})

	op, err := suite.uc.Run(context.Background(), &lbv1.DeleteListenerRequest{
		ListenerId: string(suite.listener.ID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error, "Operation must succeed; got %v", done.Error)

	require.Len(t, suite.internalAddrs.freeCalls, 1, "FreeIP must be called for auto-alloc")
	require.Equal(t, autoAddrID, suite.internalAddrs.freeCalls[0])
	require.Empty(t, suite.internalAddrs.clearCalls, "ClearReference must NOT be called for auto-alloc")

	events := suite.repo.pendingOutbox()
	// 1× listener UPDATED (status=DELETING marker) + 1× listener DELETED + 1× LB UPDATED.
	require.Len(t, events, 3)
	require.Equal(t, outboxActionUpdated, events[0].Action)
	require.Equal(t, outboxResourceTypeListener, events[0].ResourceType)
	require.Equal(t, outboxActionDeleted, events[1].Action)
	require.Equal(t, outboxResourceTypeListener, events[1].ResourceType)
	require.Equal(t, outboxActionUpdated, events[2].Action)
	require.Equal(t, outboxResourceTypeLoadBalancer, events[2].ResourceType)

	require.Empty(t, suite.repo.listeners, "listener row must be DELETE'd")
}

// TestDeleteListener_GWT_LST_023_BYO_ClearReference — BYO Listener: ClearReference
// called (NOT FreeIP — tenant Address must remain).
func TestDeleteListener_GWT_LST_023_BYO_ClearReference(t *testing.T) {
	t.Parallel()
	suite := newDeleteSuite(t)
	const byoAddrID = "e9bBYOADDR000000001"
	suite.listener.AddressID = option.MustNewOption(domain.AddressID(byoAddrID))
	suite.repo.seedListener(suite.listener)
	// BYO Address has tenant-chosen name, NOT matching `nlb-listener-` prefix.
	suite.addresses.seed(&vpcclient.Address{
		ID:        byoAddrID,
		ProjectID: string(suite.listener.ProjectID),
		Name:      "my-precious-vip",
		Value:     string(suite.listener.AllocatedAddress),
		Family:    vpcclient.AddressFamilyIPv4,
		UsedBy: &vpcclient.AddressOwner{
			Kind: addressOwnerKindNLBListener,
			ID:   string(suite.listener.ID),
		},
	})

	op, err := suite.uc.Run(context.Background(), &lbv1.DeleteListenerRequest{
		ListenerId: string(suite.listener.ID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error)

	require.Len(t, suite.internalAddrs.clearCalls, 1, "ClearReference must be called for BYO")
	require.Equal(t, byoAddrID, suite.internalAddrs.clearCalls[0])
	require.Empty(t, suite.internalAddrs.freeCalls, "FreeIP must NOT be called for BYO")
	require.Empty(t, suite.repo.listeners)
}

// TestDeleteListener_GWT_LST_024_FreeIPFails_FAILED_Outbox — vpc unavailable:
// listener row остаётся в DELETING + outbox FAILED event.
func TestDeleteListener_GWT_LST_024_FreeIPFails(t *testing.T) {
	t.Parallel()
	suite := newDeleteSuite(t)
	const autoAddrID = "e9bAUTOFAILFREE001"
	suite.listener.AddressID = option.MustNewOption(domain.AddressID(autoAddrID))
	suite.repo.seedListener(suite.listener)
	suite.addresses.seed(&vpcclient.Address{
		ID:        autoAddrID,
		ProjectID: string(suite.listener.ProjectID),
		Name:      "nlb-listener-" + domain.TruncateID(suite.listener.ID),
		Value:     string(suite.listener.AllocatedAddress),
		Family:    vpcclient.AddressFamilyIPv4,
	})
	suite.internalAddrs.freeErr = fmt.Errorf("%w: vpc backend down", domain.ErrUnavailable)

	op, err := suite.uc.Run(context.Background(), &lbv1.DeleteListenerRequest{
		ListenerId: string(suite.listener.ID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.Unavailable), done.Error.Code)

	// Row still present in DELETING status.
	row, exists := suite.repo.listeners[string(suite.listener.ID)]
	require.True(t, exists, "listener row must remain for retry")
	require.Equal(t, domain.ListenerStatusDeleting, row.Status)

	// Outbox contains the FAILED marker.
	hasFailed := false
	for _, e := range suite.repo.pendingOutbox() {
		if e.Action == outboxActionFailed {
			hasFailed = true
			break
		}
	}
	require.True(t, hasFailed, "outbox must contain FAILED event for retry job")
}

// TestDeleteListener_GWT_LST_025_TriggerRecomputeOnDelete — implicit: outbox
// contains nlb_load_balancer:<id> UPDATED so consumers re-compute LB.status.
// (The trigger itself lives in DB; in unit tests we only assert outbox semantics.)
func TestDeleteListener_GWT_LST_025_LBUpdatedOutbox(t *testing.T) {
	t.Parallel()
	suite := newDeleteSuite(t)
	suite.listener.AddressID = option.ValueOf[domain.AddressID]{} // no address: skip VIP release branch
	suite.repo.seedListener(suite.listener)
	op, err := suite.uc.Run(context.Background(), &lbv1.DeleteListenerRequest{
		ListenerId: string(suite.listener.ID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error)

	events := suite.repo.pendingOutbox()
	hasLBUpd := false
	for _, e := range events {
		if e.ResourceType == outboxResourceTypeLoadBalancer && e.Action == outboxActionUpdated {
			hasLBUpd = true
			break
		}
	}
	require.True(t, hasLBUpd, "nlb_load_balancer:<id> UPDATED must be emitted on Listener.Delete")
}

// TestDeleteListener_NotFound — listener_id doesn't exist → sync NotFound.
func TestDeleteListener_NotFound(t *testing.T) {
	t.Parallel()
	suite := newDeleteSuite(t)
	_, err := suite.uc.Run(context.Background(), &lbv1.DeleteListenerRequest{
		ListenerId: "lstNOTEXISTS0000001",
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestDeleteListener_EmptyID — sync InvalidArgument.
func TestDeleteListener_EmptyID(t *testing.T) {
	t.Parallel()
	suite := newDeleteSuite(t)
	_, err := suite.uc.Run(context.Background(), &lbv1.DeleteListenerRequest{ListenerId: ""})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ---- shared helpers ----

type deleteSuite struct {
	t             *testing.T
	repo          *fakeRepo
	ops           *fakeOpsRepo
	addresses     *fakeAddressClient
	internalAddrs *fakeInternalAddressClient
	listener      *kachorepo.ListenerRecord
	uc            *DeleteUseCase
}

func newDeleteSuite(t *testing.T) *deleteSuite {
	t.Helper()
	repo := newFakeRepo()
	lb := newRecordLB(t, "prj01TESTPROJ0000001", "ru-central1", domain.LBTypeExternal, "parent-lb")
	repo.seedLB(lb)
	listener := &kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID:               domain.ResourceID(ids.NewID(ids.PrefixListener)),
			ProjectID:        lb.ProjectID,
			LoadBalancerID:   lb.ID,
			RegionID:         lb.RegionID,
			Name:             domain.LbName("doomed"),
			Labels:           domain.LbLabels{},
			Protocol:         domain.ProtoTCP,
			Port:             443,
			TargetPort:       8443,
			IPVersion:        domain.IPVersionV4,
			AllocatedAddress: "203.0.113.7",
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	repo.seedListener(listener)
	addresses := newFakeAddressClient()
	internalAddrs := newFakeInternalAddressClient()
	ops := newFakeOpsRepo()
	uc := NewDeleteUseCase(repo, ops, addresses, internalAddrs, slog.Default())
	return &deleteSuite{
		t:             t,
		repo:          repo,
		ops:           ops,
		addresses:     addresses,
		internalAddrs: internalAddrs,
		listener:      listener,
		uc:            uc,
	}
}

// _ — sentinel for errors import (used indirectly elsewhere).
var _ = errors.Is
