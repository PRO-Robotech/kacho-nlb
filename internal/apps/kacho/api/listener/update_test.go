package listener

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestUpdateListener_GWT_LST_018_MutableFields_HappyPath — name + proxy_protocol_v2
// in mask → applied + outbox UPDATED.
func TestUpdateListener_GWT_LST_018_MutableFields(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	op, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId:      string(suite.listener.ID),
		UpdateMask:      &fieldmaskpb.FieldMask{Paths: []string{"name", "proxy_protocol_v2"}},
		Name:            "https",
		ProxyProtocolV2: true,
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error)

	got := suite.getListener(string(suite.listener.ID))
	require.Equal(t, domain.LbName("https"), got.Name)
	require.True(t, got.ProxyProtocolV2)

	events := suite.repo.pendingOutbox()
	require.Len(t, events, 1)
	require.Equal(t, outboxActionUpdated, events[0].Action)
	require.Equal(t, outboxResourceTypeListener, events[0].ResourceType)
}

// TestUpdateListener_GWT_LST_019_ImmutableLoadBalancerID — immutable in mask
// → InvalidArgument verbatim text.
func TestUpdateListener_GWT_LST_019_ImmutableLoadBalancerID(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	_, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId: string(suite.listener.ID),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"load_balancer_id"}},
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "load_balancer_id is immutable after Listener.Create")
}

// TestUpdateListener_GWT_LST_020_ImmutableFields — all immutable mask paths
// individually rejected.
func TestUpdateListener_GWT_LST_020_ImmutableFields(t *testing.T) {
	t.Parallel()
	immutable := []string{"protocol", "port", "ip_version", "address_id", "address_spec",
		"subnet_id", "region_id", "project_id", "target_port"}
	for _, field := range immutable {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			suite := newUpdateSuite(t)
			_, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
				ListenerId: string(suite.listener.ID),
				UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{field}},
			})
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, err.Error(), field+" is immutable after Listener.Create")
		})
	}
}

// TestUpdateListener_EmptyMask — empty/nil mask → InvalidArgument.
func TestUpdateListener_EmptyMask(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	_, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId: string(suite.listener.ID),
		UpdateMask: nil,
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestUpdateListener_UnknownMaskField — unknown path → InvalidArgument.
func TestUpdateListener_UnknownMaskField(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	_, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId: string(suite.listener.ID),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"made_up_field"}},
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "made_up_field")
}

// TestUpdateListener_NotFound — listener_id doesn't exist → NotFound sync.
func TestUpdateListener_NotFound(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	_, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId: "lstNOTEXISTS000001",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		Name:       "any",
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestUpdateListener_GWT_LST_021_DefaultTGRegionMismatch — TG in another region
// → FailedPrecondition verbatim text.
func TestUpdateListener_GWT_LST_021_DefaultTGRegionMismatch(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	tgID := domain.ResourceID(ids.NewID(ids.PrefixTargetGroup))
	tg := &kachorepo.TargetGroupRecord{
		TargetGroup: domain.TargetGroup{
			ID:        tgID,
			ProjectID: suite.listener.ProjectID,
			RegionID:  "ru-central2", // different region
			Name:      domain.LbName("other-region-tg"),
			Status:    domain.TargetGroupStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	suite.repo.seedTG(tg)

	_, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId:           string(suite.listener.ID),
		UpdateMask:           &fieldmaskpb.FieldMask{Paths: []string{"default_target_group_id"}},
		DefaultTargetGroupId: string(tgID),
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, err.Error(), "default target group region")
	require.Contains(t, err.Error(), "does not match listener region")
}

// TestUpdateListener_DefaultTGSameRegion — same-region TG accepted.
func TestUpdateListener_DefaultTGSameRegion(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	tgID := domain.ResourceID(ids.NewID(ids.PrefixTargetGroup))
	tg := &kachorepo.TargetGroupRecord{
		TargetGroup: domain.TargetGroup{
			ID:        tgID,
			ProjectID: suite.listener.ProjectID,
			RegionID:  suite.listener.RegionID,
			Name:      domain.LbName("same-region-tg"),
			Status:    domain.TargetGroupStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	suite.repo.seedTG(tg)
	op, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId:           string(suite.listener.ID),
		UpdateMask:           &fieldmaskpb.FieldMask{Paths: []string{"default_target_group_id"}},
		DefaultTargetGroupId: string(tgID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error)

	got := suite.getListener(string(suite.listener.ID))
	v, ok := got.DefaultTargetGroupID.Maybe()
	require.True(t, ok)
	require.Equal(t, tgID, v)
}

// TestUpdateListener_ClearDefaultTG — passing empty string in mask → clear.
func TestUpdateListener_ClearDefaultTG(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	// Pre-set default TG.
	suite.listener.DefaultTargetGroupID = option.MustNewOption(domain.ResourceID("tgrPREEXIST00000001"))
	suite.repo.seedListener(suite.listener)

	op, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId:           string(suite.listener.ID),
		UpdateMask:           &fieldmaskpb.FieldMask{Paths: []string{"default_target_group_id"}},
		DefaultTargetGroupId: "",
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error)
	got := suite.getListener(string(suite.listener.ID))
	require.True(t, got.DefaultTargetGroupID.IsNone())
}

// TestUpdateListener_InvalidNameRegex — invalid name → InvalidArgument sync.
func TestUpdateListener_InvalidNameRegex(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	_, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId: string(suite.listener.ID),
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		Name:       "Bad_Name",
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ---- shared helpers ----

type updateSuite struct {
	t        *testing.T
	repo     *fakeRepo
	ops      *fakeOpsRepo
	listener *kachorepo.ListenerRecord
	uc       *UpdateUseCase
}

func newUpdateSuite(t *testing.T) *updateSuite {
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
			Name:             domain.LbName("initial"),
			Description:      "initial",
			Labels:           domain.LbLabels{},
			Protocol:         domain.ProtoTCP,
			Port:             80,
			TargetPort:       8080,
			IPVersion:        domain.IPVersionV4,
			AllocatedAddress: "203.0.113.1",
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	repo.seedListener(listener)
	ops := newFakeOpsRepo()
	uc := NewUpdateUseCase(repo, ops, slog.Default())
	return &updateSuite{t: t, repo: repo, ops: ops, listener: listener, uc: uc}
}

func (s *updateSuite) getListener(id string) *kachorepo.ListenerRecord {
	s.repo.mu.Lock()
	defer s.repo.mu.Unlock()
	c := *s.repo.listeners[id]
	return &c
}
