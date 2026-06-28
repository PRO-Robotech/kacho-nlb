// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// (nlb-side): Update(labels) re-emits the FGA-register
// mirror-feed intent (carrying the new labels + parent) in the writer-tx so
// kacho-iam keeps its resource_mirror current under label-change reconcile.
func TestUpdate_LabelsMask_EmitsMirrorIntent(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "tg-mirror")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateTargetGroupUseCase(repo, opsRepo, nil)

	op, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{
		TargetGroupId: string(tg.ID),
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"labels"}},
		Labels:        map[string]string{"tier": "critical"},
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	require.Len(t, repo.fga, 1, "Update(labels) emits one fga.register mirror intent")
	ev := repo.fga[0]
	assert.Equal(t, domain.FGAEventRegister, ev.EventType)
	assert.Equal(t, map[string]string{"tier": "critical"}, ev.Intent.Labels, "new labels in intent")
	assert.Equal(t, "prj-acme", ev.Intent.ParentProjectID)
}

// (compute parity): a non-labels Update is a mirror no-op —
// no FGA-register intent (avoids a useless RegisterResource round-trip).
func TestUpdate_NonLabelsMask_NoMirrorIntent(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "tg-nomirror")
	repo.seedTG(tg)
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateTargetGroupUseCase(repo, opsRepo, nil)

	op, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{
		TargetGroupId:              string(tg.ID),
		UpdateMask:                 &fieldmaskpb.FieldMask{Paths: []string{"deregistration_delay_seconds"}},
		DeregistrationDelaySeconds: 600,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	require.Empty(t, repo.fga, "non-labels Update emits no mirror intent")
}

// Update mutable fields via mask.
func TestUpdate_MutableFields(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "to-update")
	repo.seedTG(tg)
	// sanity-check the seed is valid.
	opsRepo := newFakeOpsRepo()
	uc := NewUpdateTargetGroupUseCase(repo, opsRepo, nil)

	op, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{
		TargetGroupId:              string(tg.ID),
		UpdateMask:                 &fieldmaskpb.FieldMask{Paths: []string{"deregistration_delay_seconds"}},
		DeregistrationDelaySeconds: 600,
	})
	require.NoErrorf(t, err, "err details=%s", fieldViolationsText(err))
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	// Outbox UPDATED.
	events := repo.outboxEvents()
	require.Len(t, events, 1)
	assert.Equal(t, kachopg.OutboxActionUpdated, events[0].Action)
}

// immutable project_id / region_id → InvalidArgument с фиксированным текстом.
func TestUpdate_Immutable_RegionID(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "imm-region")
	repo.seedTG(tg)
	uc := NewUpdateTargetGroupUseCase(repo, newFakeOpsRepo(), nil)

	_, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{
		TargetGroupId: string(tg.ID),
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"region_id"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "region_id is immutable after TargetGroup.Create")
}

func TestUpdate_Immutable_ProjectID(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "imm-proj")
	repo.seedTG(tg)
	uc := NewUpdateTargetGroupUseCase(repo, newFakeOpsRepo(), nil)

	_, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{
		TargetGroupId: string(tg.ID),
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"project_id"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "project_id is immutable")
	require.Contains(t, status.Convert(err).Message(), "TargetGroupService.Move")
}

// targets via mask → InvalidArgument с фиксированным текстом.
func TestUpdate_Targets_ForbiddenViaMask(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "imm-targets")
	repo.seedTG(tg)
	uc := NewUpdateTargetGroupUseCase(repo, newFakeOpsRepo(), nil)

	_, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{
		TargetGroupId: string(tg.ID),
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"targets"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "targets must be modified via AddTargets / RemoveTargets")
}

// Unknown mask field.
func TestUpdate_UnknownMaskField(t *testing.T) {
	repo := newFakeRepo()
	tg := makeTG("prj-acme", "unk")
	repo.seedTG(tg)
	uc := NewUpdateTargetGroupUseCase(repo, newFakeOpsRepo(), nil)

	_, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{
		TargetGroupId: string(tg.ID),
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"foo_bar_baz"}},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "unknown update_mask field: foo_bar_baz")
}

// Empty id → InvalidArgument.
func TestUpdate_EmptyID(t *testing.T) {
	uc := NewUpdateTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TG not found → NotFound.
func TestUpdate_NotFound(t *testing.T) {
	uc := NewUpdateTargetGroupUseCase(newFakeRepo(), newFakeOpsRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.UpdateTargetGroupRequest{
		TargetGroupId: "tgr-missing",
		UpdateMask:    &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		Name:          "new-name",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}
