// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// Move OK (no attached LB).
func TestMove_Happy(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-src", "movable")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewMoveTargetGroupUseCase(repo, opsRepo, &fakeProjectClient{}, nil, nil)

	op, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-dst",
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	events := repo.outboxEvents()
	// MOVED + UPDATED
	require.Len(t, events, 2)
	assert.Equal(t, kachorepo.OutboxActionMoved, events[0].Action)
	assert.Equal(t, kachorepo.OutboxActionUpdated, events[1].Action)

	// project-rewrite = register(dst) + unregister(src) intents in writer-tx.
	require.Len(t, repo.fga, 2)
	assert.Equal(t, domain.FGAEventRegister, repo.fga[0].EventType)
	assert.Equal(t, "project:prj-dst", repo.fga[0].Intent.Tuples[0].SubjectID)
	assert.Equal(t, domain.FGAEventUnregister, repo.fga[1].EventType)
	assert.Equal(t, "project:prj-src", repo.fga[1].Intent.Tuples[0].SubjectID)
}

// Same-project destination → InvalidArgument с фиксированным текстом.
func TestMove_SameProject_InvalidArg(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-x", "same-proj")
	repo.seedTG(tg)
	uc := NewMoveTargetGroupUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, nil, nil)

	_, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-x",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "destination project is the same as source")
}

// attached to LB → FailedPrecondition с фиксированным текстом.
func TestMove_HasAttachedLB(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-y", "attached")
	repo.seedTG(tg)
	repo.seedAttached("nlb-1", string(tg.ID))
	uc := NewMoveTargetGroupUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, nil, nil)

	_, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-z",
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(),
		"is attached to 1 load balancer(s); detach before moving")
}

// Destination project peer NotFound → InvalidArgument with с фиксированным текстом.
func TestMove_DestProjectNotFound(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-src", "peer-nf")
	repo.seedTG(tg)
	uc := NewMoveTargetGroupUseCase(repo, newFakeOpsRepo(),
		&fakeProjectClient{getFunc: func(_ context.Context, id string) (*iam.Project, error) {
			return nil, projectNotFound(id)
		}}, nil, nil)

	_, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-doesnt-exist",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "Project prj-doesnt-exist not found")
}

func TestMove_MissingFields(t *testing.T) {
	uc := NewMoveTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), &fakeProjectClient{}, nil, nil)
	for _, tc := range []struct {
		name string
		req  *lbv1.MoveTargetGroupRequest
	}{
		{"no id", &lbv1.MoveTargetGroupRequest{DestinationProjectId: "p"}},
		{"no dst", &lbv1.MoveTargetGroupRequest{TargetGroupId: "tgr-x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := uc.Execute(context.Background(), tc.req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestMove_NotFound(t *testing.T) {
	uc := NewMoveTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), &fakeProjectClient{}, nil, nil)
	_, err := uc.Execute(context.Background(), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        "tgr-missing",
		DestinationProjectId: "prj-dst",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// SECURITY (audit SEC-high #2 / CWE-862/863): the caller must be authorized on
// the DESTINATION project (editor on project:<dst>). A caller with editor on the
// source TG but NO grant on the destination must be denied, else it injects its
// TG into a victim's project. Deny → PermissionDenied and the TG must NOT move.
func TestMove_DeniesUnauthorizedDestination(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-src", "movable")
	repo.seedTG(tg)
	chk := &fakeCheckClient{allowed: false}
	uc := NewMoveTargetGroupUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, chk, nil)

	_, err := uc.Execute(ctxWithUser("usr_attacker"), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-victim",
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Equal(t, domain.ProjectID("prj-src"), repo.tgs[string(tg.ID)].ProjectID,
		"TG must not be re-parented when dst authz is denied")
	require.Equal(t, 1, chk.calls)
	require.Equal(t, "user:usr_attacker", chk.gotSubject)
	require.Equal(t, domain.FGARelationEditor, chk.gotRelation)
	require.Equal(t, "project:prj-victim", chk.gotObject)
}

// Authorized (editor) on the destination → Move proceeds.
func TestMove_AllowsAuthorizedDestination(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-src", "movable")
	repo.seedTG(tg)
	chk := &fakeCheckClient{allowed: true}
	opsRepo := newFakeOpsRepo()
	uc := NewMoveTargetGroupUseCase(repo, opsRepo, &fakeProjectClient{}, chk, nil)

	op, err := uc.Execute(ctxWithUser("usr_owner"), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-dst",
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.Equal(t, domain.ProjectID("prj-dst"), repo.tgs[string(tg.ID)].ProjectID)
	require.Equal(t, 1, chk.calls)
}

// IAM unavailable during the dst-authz check → fail-closed Unavailable.
func TestMove_DestCheckUnavailableFailsClosed(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-src", "movable")
	repo.seedTG(tg)
	// CheckClient contract: transport-unavailable surfaces as domain.ErrUnavailable.
	chk := &fakeCheckClient{err: domain.ErrUnavailable}
	uc := NewMoveTargetGroupUseCase(repo, newFakeOpsRepo(), &fakeProjectClient{}, chk, nil)

	_, err := uc.Execute(ctxWithUser("usr_owner"), &lbv1.MoveTargetGroupRequest{
		TargetGroupId:        string(tg.ID),
		DestinationProjectId: "prj-dst",
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Equal(t, domain.ProjectID("prj-src"), repo.tgs[string(tg.ID)].ProjectID)
}

// TestTgMovedPayload_OldProjectReachesConsumer — regression for outbox
// payload-key drift (5th audit, HIGH). MOVED producer emits `old_project_id`
// (canonical key the Subscribe consumer parses into
// ResourceLifecycleEvent.OldProjectId), not the legacy `src_project_id`.
func TestTgMovedPayload_OldProjectReachesConsumer(t *testing.T) {
	m := tgMovedPayload("nlb-tg-1", "prj-src", "prj-dst")
	require.NotContains(t, m, "src_project_id", "legacy key must not be emitted")

	raw, err := json.Marshal(m)
	require.NoError(t, err)
	parsed, err := kachorepo.ParseLifecyclePayload(raw)
	require.NoError(t, err)
	require.Equal(t, "prj-src", parsed.OldProjectID,
		"consumer must recover source project from MOVED payload")
	require.Equal(t, "prj-dst", parsed.NewProjectID)
}
