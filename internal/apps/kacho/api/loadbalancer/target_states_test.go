// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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
	uc := NewGetTargetStatesUseCase(newFakeRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.GetTargetStatesRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetTargetStates_LBNotFound(t *testing.T) {
	t.Parallel()
	uc := NewGetTargetStatesUseCase(newFakeRepo(), nil)
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
	uc := NewGetTargetStatesUseCase(repo, nil)
	resp, err := uc.Execute(context.Background(), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.NoError(t, err)
	require.Empty(t, resp.GetTargetStates(), "fake.ListTargets returns nil → empty states")
}

// TestGetTargetStates_DeniesCrossProjectTargetGroup — CWE-863/639 guard
// (round-7 audit, BOLA finding): the per-RPC interceptor's StaticExtractor
// scopes its FGA Check to the LB object only (`GetNetworkLoadBalancerId`),
// so a caller authorized on their own LB (project prj-a) must NOT be able to
// read another project's TargetGroup (prj-b) by passing its id in
// target_group_id — refused (mirrors TestAttach_ProjectMismatch) before any
// target data (instance/NIC ids, addresses, subnet ids) is returned, even
// with checkClient nil (dev/unwired — the same-project invariant is
// unconditional, not just an authz nicety).
func TestGetTargetStates_DeniesCrossProjectTargetGroup(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-b", "ru-central1", "tg")
	uc := NewGetTargetStatesUseCase(repo, nil)

	resp, err := uc.Execute(context.Background(), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.Nil(t, resp)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// TestGetTargetStates_CrossProjectUnauthorized_NoProjectOracle — object-scoped
// authz (checkTargetGroupViewer) must run BEFORE the project-mismatch branch:
// an unauthorized caller passing a victim TG from another project must get a
// generic PermissionDenied that reveals nothing, NOT the FailedPrecondition
// "project mismatch: ... TargetGroup is in project prj-b" oracle (which both
// confirms the TG exists and leaks its owning project). Locks the observable
// message, not only the gRPC code (security.md #3 object-scoped authz +
// existence-hiding).
func TestGetTargetStates_CrossProjectUnauthorized_NoProjectOracle(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-b", "ru-central1", "tg") // victim TG in another project
	chk := &fakeCheckClient{allowed: false}               // caller not authorized on the TG
	uc := NewGetTargetStatesUseCase(repo, chk)

	_, err := uc.Execute(ctxWithUser("usr_attacker"), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.NotContains(t, status.Convert(err).Message(), "prj-b",
		"must not leak the victim TG's owning project id")
	require.Equal(t, 1, chk.calls, "authz Check must run before the project-mismatch branch")
}

// TestGetTargetStates_DeniesUnauthorizedSameProjectTargetGroup — same-project
// TG is not automatically viewable: a narrowly-scoped custom grant on the LB
// (without project-editor ⇒ TG-viewer cascade) must still be refused.
func TestGetTargetStates_DeniesUnauthorizedSameProjectTargetGroup(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	chk := &fakeCheckClient{allowed: false}
	uc := NewGetTargetStatesUseCase(repo, chk)

	_, err := uc.Execute(ctxWithUser("usr_attacker"), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, 1, chk.calls)
	require.Equal(t, "user:usr_attacker", chk.gotSubject)
	require.Equal(t, domain.FGARelationViewer, chk.gotRelation)
	require.Equal(t, "lb_target_group:"+tgID, chk.gotObject)
}

// TestGetTargetStates_AllowsAuthorizedTargetGroup — a same-project caller
// holding viewer on the TG passes the handler-side gate and gets the states.
func TestGetTargetStates_AllowsAuthorizedTargetGroup(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	chk := &fakeCheckClient{allowed: true}
	uc := NewGetTargetStatesUseCase(repo, chk)

	resp, err := uc.Execute(ctxWithUser("usr_owner"), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, 1, chk.calls)
}

// TestGetTargetStates_CheckUnavailableFailsClosed — IAM unavailable during
// the TG-authz check → fail-closed Unavailable, never a silent allow.
func TestGetTargetStates_CheckUnavailableFailsClosed(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := seedTG(t, repo, "prj-a", "ru-central1", "tg")
	chk := &fakeCheckClient{err: domain.ErrUnavailable}
	uc := NewGetTargetStatesUseCase(repo, chk)

	_, err := uc.Execute(ctxWithUser("usr_owner"), &lbv1.GetTargetStatesRequest{
		NetworkLoadBalancerId: lbID, TargetGroupId: tgID,
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
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
