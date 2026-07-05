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

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func seedTG(t *testing.T, repo *fakeRepo, projectID, region, name string) string {
	t.Helper()
	id := ids.NewID(ids.PrefixTargetGroup)
	repo.tgs[id] = &kachorepo.TargetGroupRecord{
		TargetGroup: domain.TargetGroup{
			ID: domain.ResourceID(id), ProjectID: domain.ProjectID(projectID),
			RegionID: domain.RegionID(region), Name: domain.LbName(name),
			Status: domain.TargetGroupStatusActive,
		},
	}
	return id
}

func TestAttach_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	opsRepo := newFakeOpsRepo()
	uc := NewAttachTargetGroupUseCase(repo, opsRepo, nil, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Contains(t, repo.pivot, lbID+"/"+tgID)
}

func TestAttach_RegionMismatch(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central2", "tg") // different region
	uc := NewAttachTargetGroupUseCase(repo, newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "region mismatch")
}

func TestAttach_ProjectMismatch(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-b", "ru-central1", "tg")
	uc := NewAttachTargetGroupUseCase(repo, newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestAttach_Idempotent(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	// Pre-attach the same pair.
	repo.pivot[lbID+"/"+tgID] = &kachorepo.AttachedTargetGroupRecord{
		LoadBalancerID: lbID, TargetGroupID: tgID, Priority: 0,
	}
	opsRepo := newFakeOpsRepo()
	uc := NewAttachTargetGroupUseCase(repo, opsRepo, nil, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
}

func TestAttach_MissingTG(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewAttachTargetGroupUseCase(repo, newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: "tgr-missing"},
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestAttach_EmptyTG(t *testing.T) {
	t.Parallel()
	uc := NewAttachTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: "nlb-x",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestAttach_DeniesUnauthorizedTargetGroup — a caller that passed the LB
// interceptor (v_update on the LB) but lacks `viewer` on the TargetGroup must be
// refused with PermissionDenied, and no attach may happen (audit SEC r3 #3).
func TestAttach_DeniesUnauthorizedTargetGroup(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	chk := &fakeCheckClient{allowed: false} // caller lacks viewer on the TG
	uc := NewAttachTargetGroupUseCase(repo, newFakeOpsRepo(), chk, slog.Default())

	_, err := uc.Execute(ctxWithUser("usr_attacker"), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.NotContains(t, repo.pivot, lbID+"/"+tgID, "unauthorized TG must not be attached")
	// Check was against the TG object with the viewer relation.
	require.Equal(t, 1, chk.calls)
	require.Equal(t, "user:usr_attacker", chk.gotSubject)
	require.Equal(t, domain.FGARelationViewer, chk.gotRelation)
	require.Equal(t, "lb_target_group:"+tgID, chk.gotObject)
}

// TestAttach_AllowsAuthorizedTargetGroup — a caller with viewer on the TG passes
// the handler-side gate and the attach proceeds.
func TestAttach_AllowsAuthorizedTargetGroup(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	chk := &fakeCheckClient{allowed: true}
	opsRepo := newFakeOpsRepo()
	uc := NewAttachTargetGroupUseCase(repo, opsRepo, chk, slog.Default())

	op, err := uc.Execute(ctxWithUser("usr_owner"), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Contains(t, repo.pivot, lbID+"/"+tgID)
	require.Equal(t, 1, chk.calls)
}

// TestAttach_TGCheckUnavailableFailsClosed — IAM unavailable during the TG-authz
// check → fail-closed Unavailable (never a silent allow).
func TestAttach_TGCheckUnavailableFailsClosed(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	chk := &fakeCheckClient{err: domain.ErrUnavailable}
	uc := NewAttachTargetGroupUseCase(repo, newFakeOpsRepo(), chk, slog.Default())

	_, err := uc.Execute(ctxWithUser("usr_owner"), &lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: tgID},
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, repo.pivot, lbID+"/"+tgID)
}

// ---- Detach ---------------------------------------------------------------

func TestDetach_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := "tgr-fake"
	repo.pivot[lbID+"/"+tgID] = &kachorepo.AttachedTargetGroupRecord{
		LoadBalancerID: lbID, TargetGroupID: tgID,
	}
	opsRepo := newFakeOpsRepo()
	uc := NewDetachTargetGroupUseCase(repo, opsRepo, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.DetachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		TargetGroupId:         tgID,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.NotContains(t, repo.pivot, lbID+"/"+tgID)
}

func TestDetach_Idempotent(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewDetachTargetGroupUseCase(repo, opsRepo, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.DetachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: lbID,
		TargetGroupId:         "tgr-not-attached",
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error, "detach of nothing — idempotent no-op")
}

func TestDetach_MissingLB(t *testing.T) {
	t.Parallel()
	uc := NewDetachTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.DetachNetworkLoadBalancerTargetGroupRequest{
		NetworkLoadBalancerId: "nlb-x", TargetGroupId: "tgr-y",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}
