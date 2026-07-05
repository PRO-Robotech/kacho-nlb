// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestMove_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-src", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewMoveLoadBalancerUseCase(repo, opsRepo, &fakeProjectClient{}, nil, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-dst",
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.ProjectID("prj-dst"), repo.lbs[lbID].ProjectID)
	// project-rewrite = register(dst) + unregister(src) intents in writer-tx.
	require.Len(t, repo.fga, 2, "expected register(dst)+unregister(src) intents")
	require.Equal(t, domain.FGAEventRegister, repo.fga[0].EventType)
	require.Equal(t, "project:prj-dst", repo.fga[0].Intent.Tuples[0].SubjectID)
	require.Equal(t, domain.FGAEventUnregister, repo.fga[1].EventType)
	require.Equal(t, "project:prj-src", repo.fga[1].Intent.Tuples[0].SubjectID)
}

func TestMove_SameProject(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewMoveLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-a",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestMove_BlockedIfAttachedTG(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.pivot[lbID+"/tgr-fake"] = &kachorepo.AttachedTargetGroupRecord{
		LoadBalancerID: lbID, TargetGroupID: "tgr-fake",
	}
	uc := NewMoveLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-dst",
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestMove_EmptyDst(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	uc := NewMoveLoadBalancerUseCase(repo, newFakeOpsRepo(), nil, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestMove_NotFound(t *testing.T) {
	t.Parallel()
	uc := NewMoveLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: "nlb-x",
		DestinationProjectId:  "prj-dst",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// SECURITY (audit SEC-high #2 / CWE-862/863): the caller must be authorized on
// the DESTINATION project (editor on project:<dst>). The per-RPC interceptor only
// checks the source LB; a caller with editor on the source but NO grant on the
// destination must be denied — otherwise it can inject its LB into a victim's
// project. With a check-client that denies, Move → PermissionDenied and the LB
// must NOT move.
func TestMove_DeniesUnauthorizedDestination(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-src", "edge")
	chk := &fakeCheckClient{allowed: false} // caller lacks editor on dst
	uc := NewMoveLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, chk, slog.Default())

	_, err := uc.Execute(ctxWithUser("usr_attacker"), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-victim",
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, domain.ProjectID("prj-src"), repo.lbs[lbID].ProjectID,
		"LB must not be re-parented when dst authz is denied")
	// Check was performed against the destination project with the editor relation.
	require.Equal(t, 1, chk.calls)
	require.Equal(t, "user:usr_attacker", chk.gotSubject)
	require.Equal(t, domain.FGARelationEditor, chk.gotRelation)
	require.Equal(t, "project:prj-victim", chk.gotObject)
}

// A caller authorized (editor) on the destination project passes the dst-authz
// gate and the Move proceeds.
func TestMove_AllowsAuthorizedDestination(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-src", "edge")
	chk := &fakeCheckClient{allowed: true}
	opsRepo := newFakeOpsRepo()
	uc := NewMoveLoadBalancerUseCase(repo, opsRepo, &fakeProjectClient{}, chk, slog.Default())

	op, err := uc.Execute(ctxWithUser("usr_owner"), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-dst",
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.ProjectID("prj-dst"), repo.lbs[lbID].ProjectID)
	require.Equal(t, 1, chk.calls)
}

// IAM unavailable during the dst-authz check → fail-closed Unavailable (never a
// silent allow).
func TestMove_DestCheckUnavailableFailsClosed(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-src", "edge")
	// CheckClient contract: transport-unavailable surfaces as domain.ErrUnavailable.
	chk := &fakeCheckClient{err: domain.ErrUnavailable}
	uc := NewMoveLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, chk, slog.Default())

	_, err := uc.Execute(ctxWithUser("usr_owner"), &lbv1.MoveNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
		DestinationProjectId:  "prj-dst",
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Equal(t, domain.ProjectID("prj-src"), repo.lbs[lbID].ProjectID)
}

// TestLbMovedPayload_OldProjectReachesConsumer — regression for outbox
// payload-key drift (5th audit, HIGH). The MOVED producer must emit the source
// project under the canonical `old_project_id` key that the Subscribe consumer
// parses into ResourceLifecycleEvent.OldProjectId (kacho-iam tears down stale
// owner/hierarchy tuples on the OLD project). Previously it emitted
// `src_project_id`, which no consumer reads → OldProjectId always empty.
// Producer helper → shared parser (the SAME parser the consumer uses) proves both
// sides agree on the key name.
func TestLbMovedPayload_OldProjectReachesConsumer(t *testing.T) {
	t.Parallel()
	m := lbMovedPayload("nlb-1", "prj-src", "prj-dst")
	require.NotContains(t, m, "src_project_id", "legacy key must not be emitted")

	raw, err := json.Marshal(m)
	require.NoError(t, err)
	parsed, err := kachorepo.ParseLifecyclePayload(raw)
	require.NoError(t, err)
	require.Equal(t, "prj-src", parsed.OldProjectID,
		"consumer must recover source project from MOVED payload")
	require.Equal(t, "prj-dst", parsed.NewProjectID)
}
