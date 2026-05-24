package targetgroup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// GWT-TGR-018 — Update mutable fields via mask.
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

// GWT-TGR-019 — immutable project_id / region_id → InvalidArgument verbatim.
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

// GWT-TGR-020 — targets via mask → InvalidArgument verbatim.
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
