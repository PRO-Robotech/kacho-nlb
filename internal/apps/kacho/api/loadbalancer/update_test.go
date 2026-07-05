// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func TestUpdate_HappyPath_PatchName(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, &fakeZoneClient{}, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		Name:                  "edge-v2",
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"name"}},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, "edge-v2", string(repo.lbs[lbID].Name))
}

// Update(labels) re-emits the FGA-register mirror-feed intent so kacho-iam keeps
// its resource_mirror current under label-change reconcile.
func TestUpdate_LabelsMask_EmitsMirrorIntent(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, &fakeZoneClient{}, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		Labels:                map[string]string{"tier": "critical"},
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"labels"}},
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)

	require.Len(t, repo.fga, 1, "Update(labels) emits one fga.register mirror intent")
	require.Equal(t, domain.FGAEventRegister, repo.fga[0].EventType)
	require.Equal(t, map[string]string{"tier": "critical"}, repo.fga[0].Intent.Labels)
	require.Equal(t, "prj-a", repo.fga[0].Intent.ParentProjectID)
}

// A non-labels Update is a mirror no-op.
func TestUpdate_NonLabelsMask_NoMirrorIntent(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, &fakeZoneClient{}, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		Name:                  "edge-v2",
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"name"}},
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Empty(t, repo.fga, "non-labels Update emits no mirror intent")
}

// session_affinity is mutable via update_mask.
func TestUpdate_SessionAffinityMask(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, &fakeZoneClient{}, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		SessionAffinity:       lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"session_affinity"}},
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Equal(t, domain.SessionAffinityClientIPOnly, repo.lbs[lbID].SessionAffinity)
}

// 8.1-26: disabled_announce_zones — drain then re-enable (REGIONAL); ZONAL/all-zones reject.
func TestUpdate_DisabledAnnounceZones(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.lbs[lbID].Type = domain.LBTypeInternal
	repo.lbs[lbID].RegionID = "region-1"
	repo.lbs[lbID].PlacementType = domain.PlacementRegional
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, &fakeZoneClient{}, slog.Default())

	// drain region-1-b
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DisabledAnnounceZones: []string{"region-1-b"},
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"disabled_announce_zones"}},
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Equal(t, []string{"region-1-b"}, repo.lbs[lbID].DisabledAnnounceZones)

	// re-enable (empty)
	op2, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"disabled_announce_zones"}},
	})
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op2.ID).Error)
	require.Empty(t, repo.lbs[lbID].DisabledAnnounceZones)

	// drain all zones → reject
	_, err = uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DisabledAnnounceZones: []string{"region-1-a", "region-1-b"},
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"disabled_announce_zones"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "disabled_announce_zones must not cover all zones of the region", status.Convert(err).Message())
}

// 8.1-26 (ZONAL branch): drain on a ZONAL LB → reject.
func TestUpdate_DisabledAnnounceZones_ZonalReject(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.lbs[lbID].Type = domain.LBTypeInternal
	repo.lbs[lbID].PlacementType = domain.PlacementZonal
	uc := NewUpdateLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeZoneClient{}, slog.Default())
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DisabledAnnounceZones: []string{"region-1-a"},
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"disabled_announce_zones"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "disabled_announce_zones is only valid for REGIONAL load balancer", status.Convert(err).Message())
}

// 8.1-25: immutable fields in update_mask → InvalidArgument.
func TestUpdate_ImmutableFields(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"type", "placement_type", "region_id", "project_id", "v4_source", "v6_source", "v4_address_id", "v6_address_id"} {
		t.Run(field, func(t *testing.T) {
			repo := newFakeRepo()
			lbID := seedLB(t, repo, "prj-a", "edge")
			uc := NewUpdateLoadBalancerUseCase(repo, newFakeOpsRepo(), nil, nil)
			_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
				NetworkLoadBalancerId: lbID,
				UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{field}},
			})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, status.Convert(err).Message(), "immutable")
		})
	}
}

func TestUpdate_UnknownField(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewUpdateLoadBalancerUseCase(repo, newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"frobnicate"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestUpdate_EmptyMask_FullPatch(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateLoadBalancerUseCase(repo, opsRepo, &fakeZoneClient{}, nil)
	op, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		Name:                  "renamed",
		Labels:                map[string]string{"k": "v"},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.LbName("renamed"), repo.lbs[lbID].Name)
}

func TestUpdate_NotFound(t *testing.T) {
	t.Parallel()
	uc := NewUpdateLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: "nlb-nope",
		UpdateMask:            &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		Name:                  "edge",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestUpdate_EmptyID(t *testing.T) {
	t.Parallel()
	uc := NewUpdateLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateNetworkLoadBalancerRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// Cover apply func — full patch sets all mutable fields from req.
func TestApplyUpdateMask_FullPatch(t *testing.T) {
	t.Parallel()
	cur := domain.LoadBalancer{
		Name: "old", Description: "old", DeletionProtection: false,
		Labels: domain.LabelsFromMap(map[string]string{"k": "old"}),
	}
	req := &lbv1.UpdateNetworkLoadBalancerRequest{
		Name: "new", Description: "new", DeletionProtection: true,
		Labels: map[string]string{"k": "new"},
	}
	out := applyUpdateMask(cur, req, nil)
	require.Equal(t, domain.LbName("new"), out.Name)
	require.True(t, out.DeletionProtection)
}
