package listener

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestGetListener_GWT_LST_016_HappyPath — Get returns full Listener proto.
func TestGetListener_GWT_LST_016_HappyPath(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	uc := NewGetUseCase(suite.repo)
	got, err := uc.Run(context.Background(), &lbv1.GetListenerRequest{
		ListenerId: string(suite.listener.ID),
	})
	require.NoError(t, err)
	require.Equal(t, string(suite.listener.ID), got.Id)
	require.Equal(t, string(suite.listener.LoadBalancerID), got.LoadBalancerId)
	require.Equal(t, string(suite.listener.ProjectID), got.ProjectId)
	require.Equal(t, int64(suite.listener.Port), got.Port)
}

// TestGetListener_NotFound — verbatim YC text "Listener <id> not found".
func TestGetListener_NotFound(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	uc := NewGetUseCase(suite.repo)
	_, err := uc.Run(context.Background(), &lbv1.GetListenerRequest{
		ListenerId: "lstMISSING000000001",
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
	require.Contains(t, err.Error(), "Listener lstMISSING000000001 not found")
}

// TestGetListener_EmptyID — InvalidArgument.
func TestGetListener_EmptyID(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	uc := NewGetUseCase(suite.repo)
	_, err := uc.Run(context.Background(), &lbv1.GetListenerRequest{ListenerId: ""})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestListListeners_GWT_LST_017_FilterByLB — List scoped to lb_id.
func TestListListeners_GWT_LST_017_FilterByLB(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	// Seed an extra listener on a different LB; must NOT be returned.
	otherLB := newRecordLB(t, suite.listener.ProjectID, suite.listener.RegionID, domain.LBTypeExternal, "other-lb")
	suite.repo.seedLB(otherLB)
	suite.repo.seedListener(&kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID:               domain.ResourceID(ids.NewID(ids.PrefixListener)),
			LoadBalancerID:   otherLB.ID,
			ProjectID:        suite.listener.ProjectID,
			RegionID:         suite.listener.RegionID,
			Name:             domain.LbName("other-listener"),
			Labels:           domain.LbLabels{},
			Protocol:         domain.ProtoTCP,
			Port:             80,
			TargetPort:       8080,
			IPVersion:        domain.IPVersionV4,
			AllocatedAddress: "203.0.113.99",
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})

	uc := NewListUseCase(suite.repo, nil)
	resp, err := uc.Run(context.Background(), &lbv1.ListListenersRequest{
		ProjectId:      string(suite.listener.ProjectID),
		LoadBalancerId: string(suite.listener.LoadBalancerID),
	})
	require.NoError(t, err)
	require.Len(t, resp.Listeners, 1, "load_balancer_id filter restricts to the parent LB's listeners")
	require.Equal(t, string(suite.listener.ID), resp.Listeners[0].Id)
}

// TestListListeners_ByProject_KAC229 — project-scoped List returns ALL listeners
// in the project across every load balancer (no load_balancer_id filter).
func TestListListeners_ByProject_KAC229(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	otherLB := newRecordLB(t, suite.listener.ProjectID, suite.listener.RegionID, domain.LBTypeExternal, "other-lb-p")
	suite.repo.seedLB(otherLB)
	suite.repo.seedListener(&kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID:               domain.ResourceID(ids.NewID(ids.PrefixListener)),
			LoadBalancerID:   otherLB.ID,
			ProjectID:        suite.listener.ProjectID,
			RegionID:         suite.listener.RegionID,
			Name:             domain.LbName("other-listener-p"),
			Labels:           domain.LbLabels{},
			Protocol:         domain.ProtoTCP,
			Port:             81,
			TargetPort:       8081,
			IPVersion:        domain.IPVersionV4,
			AllocatedAddress: "203.0.113.98",
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	uc := NewListUseCase(suite.repo, nil)
	resp, err := uc.Run(context.Background(), &lbv1.ListListenersRequest{
		ProjectId: string(suite.listener.ProjectID),
	})
	require.NoError(t, err)
	require.Len(t, resp.Listeners, 2, "project-scoped List returns listeners across all LBs in the project")
}

// TestListListeners_EmptyProjectID — project_id is required (KAC-229).
func TestListListeners_EmptyProjectID(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	uc := NewListUseCase(suite.repo, nil)
	_, err := uc.Run(context.Background(), &lbv1.ListListenersRequest{ProjectId: ""})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestListListeners_FilterName — name=… filter.
func TestListListeners_FilterName(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	suite.repo.seedListener(&kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID:               domain.ResourceID(ids.NewID(ids.PrefixListener)),
			LoadBalancerID:   suite.listener.LoadBalancerID,
			ProjectID:        suite.listener.ProjectID,
			RegionID:         suite.listener.RegionID,
			Name:             domain.LbName("named-second"),
			Labels:           domain.LbLabels{},
			Protocol:         domain.ProtoTCP,
			Port:             81,
			TargetPort:       8081,
			IPVersion:        domain.IPVersionV4,
			AllocatedAddress: "203.0.113.81",
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	uc := NewListUseCase(suite.repo, nil)
	resp, err := uc.Run(context.Background(), &lbv1.ListListenersRequest{
		ProjectId:      string(suite.listener.ProjectID),
		LoadBalancerId: string(suite.listener.LoadBalancerID),
		Filter:         `name="named-second"`,
	})
	require.NoError(t, err)
	require.Len(t, resp.Listeners, 1)
	require.Equal(t, "named-second", resp.Listeners[0].Name)
}

// TestListListeners_InvalidFilter — unrecognised filter syntax.
func TestListListeners_InvalidFilter(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	uc := NewListUseCase(suite.repo, nil)
	_, err := uc.Run(context.Background(), &lbv1.ListListenersRequest{
		ProjectId:      string(suite.listener.ProjectID),
		LoadBalancerId: string(suite.listener.LoadBalancerID),
		Filter:         "garbage",
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestListOperations_GWT_LST_026_PerListenerHistory — filter by listener_id.
func TestListOperations_GWT_LST_026_PerListenerHistory(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)

	// Seed 2 operations for our listener + 1 for an unrelated listener.
	listenerID := string(suite.listener.ID)
	for i := 0; i < 2; i++ {
		op, err := operations.New(
			ids.PrefixOperationNLB,
			"Test op",
			&lbv1.CreateListenerMetadata{ListenerId: listenerID, LoadBalancerId: string(suite.listener.LoadBalancerID)},
		)
		require.NoError(t, err)
		require.NoError(t, suite.ops.Create(context.Background(), op))
	}
	op3, err := operations.New(
		ids.PrefixOperationNLB,
		"Unrelated op",
		&lbv1.CreateListenerMetadata{ListenerId: "lstUNRELATED0000001"},
	)
	require.NoError(t, err)
	require.NoError(t, suite.ops.Create(context.Background(), op3))

	uc := NewListOperationsUseCase(suite.ops)
	resp, err := uc.Run(context.Background(), &lbv1.ListListenerOperationsRequest{
		ListenerId: listenerID,
	})
	require.NoError(t, err)
	require.Len(t, resp.Operations, 2)
	for _, op := range resp.Operations {
		md, err := op.Metadata.UnmarshalNew()
		require.NoError(t, err)
		metaListenerID := md.(*lbv1.CreateListenerMetadata).GetListenerId()
		require.Equal(t, listenerID, metaListenerID)
	}
}

// TestListOperations_EmptyListenerID — InvalidArgument.
func TestListOperations_EmptyListenerID(t *testing.T) {
	t.Parallel()
	suite := newReadSuite(t)
	uc := NewListOperationsUseCase(suite.ops)
	_, err := uc.Run(context.Background(), &lbv1.ListListenerOperationsRequest{ListenerId: ""})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ---- read suite ----

type readSuite struct {
	t        *testing.T
	repo     *fakeRepo
	ops      *fakeOpsRepo
	listener *kachorepo.ListenerRecord
}

func newReadSuite(t *testing.T) *readSuite {
	t.Helper()
	repo := newFakeRepo()
	lb := newRecordLB(t, "prj01TESTPROJ0000001", "ru-central1", domain.LBTypeExternal, "read-lb")
	repo.seedLB(lb)
	listener := &kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID:               domain.ResourceID(ids.NewID(ids.PrefixListener)),
			LoadBalancerID:   lb.ID,
			ProjectID:        lb.ProjectID,
			RegionID:         lb.RegionID,
			Name:             domain.LbName("read-listener"),
			Labels:           domain.LbLabels{},
			Protocol:         domain.ProtoTCP,
			Port:             80,
			TargetPort:       8080,
			IPVersion:        domain.IPVersionV4,
			AllocatedAddress: "203.0.113.5",
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	repo.seedListener(listener)
	return &readSuite{t: t, repo: repo, ops: newFakeOpsRepo(), listener: listener}
}

// _ — silence unused anypb import (used implicitly via Operation.Response).
var _ = anypb.Any{}
