// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// UpdateTargetGroupUseCase — UpdateMask discipline + async update
// .
//
// Mutable: name / description / labels / health_check / deregistration_delay_seconds /
// slow_start_seconds. Immutable: project_id / region_id (mask → InvalidArgument).
// Targets — отдельная семантика через AddTargets/RemoveTargets; mask=["targets"]
// → InvalidArgument с фиксированным текстом.
type UpdateTargetGroupUseCase struct {
	repo    Repo
	opsRepo OpsRepo
	logger  *slog.Logger
}

// NewUpdateTargetGroupUseCase конструктор.
func NewUpdateTargetGroupUseCase(repo Repo, opsRepo OpsRepo, logger *slog.Logger) *UpdateTargetGroupUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &UpdateTargetGroupUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// knownUpdateFieldsTG — whitelist update_mask fields.
var knownUpdateFieldsTG = map[string]bool{
	"name":                         true,
	"description":                  true,
	"labels":                       true,
	"health_check":                 true,
	"deregistration_delay_seconds": true,
	"slow_start_seconds":           true,
}

// immutableUpdateFieldsTG — hard-immutable, с фиксированным текстом error text.
var immutableUpdateFieldsTG = map[string]string{
	"project_id": "project_id is immutable; use TargetGroupService.Move",
	"region_id":  "region_id is immutable after TargetGroup.Create",
}

// Execute — sync mask validation + read existing → apply diff → ops insert + worker.
func (u *UpdateTargetGroupUseCase) Execute(
	ctx context.Context, req *lbv1.UpdateTargetGroupRequest,
) (*operations.Operation, error) {
	id := req.GetTargetGroupId()
	if id == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}
	if err := validateTargetGroupID(id); err != nil {
		return nil, err
	}
	mask := req.GetUpdateMask().GetPaths()
	for _, p := range mask {
		// targets via mask запрещён — отдельный фиксированный текст.
		if p == "targets" {
			return nil, status.Error(codes.InvalidArgument,
				"targets must be modified via AddTargets / RemoveTargets")
		}
		if msg, ok := immutableUpdateFieldsTG[p]; ok {
			return nil, status.Errorf(codes.InvalidArgument, "%s", msg)
		}
		if !knownUpdateFieldsTG[p] {
			return nil, status.Errorf(codes.InvalidArgument, "unknown update_mask field: %s", p)
		}
	}

	// Read current state.
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := rd.TargetGroups().Get(ctx, id)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}

	updated := applyUpdateMaskTG(cur.TargetGroup, req, mask)
	if err := updated.Validate(); err != nil {
		return nil, mapDomainErr(err)
	}

	// Operation row.
	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Update TargetGroup %s", id),
		&lbv1.UpdateTargetGroupMetadata{TargetGroupId: id},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}

	// (parity with compute): re-emit the FGA-register intent
	// (carrying the new labels) ONLY when labels change — labels in mask, or empty
	// mask (full PATCH always reapplies labels). A non-labels Update is a mirror
	// no-op (skip the intent to avoid a useless RegisterResource round-trip).
	emitMirror := labelsInMaskTG(mask)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doUpdate(workerCtx, updated, emitMirror)
	})
	return &op, nil
}

// labelsInMaskTG reports whether the Update touches labels: explicit "labels" in
// the mask, or an empty mask (full-object PATCH reapplies all mutable fields).
func labelsInMaskTG(mask []string) bool {
	if len(mask) == 0 {
		return true
	}
	for _, p := range mask {
		if p == "labels" {
			return true
		}
	}
	return false
}

// doUpdate — worker: Writer-TX → Update + outbox UPDATED (+ FGA-register intent
// when labels changed) → Commit. The mirror-feed intent is written in the
// SAME writer-tx as the resource UPDATE (no dual-write); the emitter stamps a
// monotonic source_version so IAM applies the mirror last-source-state-wins.
func (u *UpdateTargetGroupUseCase) doUpdate(ctx context.Context, tg domain.TargetGroup, emitMirror bool) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	updated, err := w.TargetGroups().Update(ctx, &tg)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachorepo.OutboxResourceTargetGroup, string(updated.ID), string(updated.ProjectID),
		kachorepo.OutboxActionUpdated, tgOutboxPayload(updated),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	if emitMirror {
		if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
			tgMirrorIntent(updated)); err != nil {
			return nil, mapDomainErr(err)
		}
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}
	return marshalTargetGroup(updated)
}

// applyUpdateMaskTG — наложить mask на текущий TG. Empty mask → full PATCH:
// mutable полностью перезаписываются из req; immutable silent-ignored
// (по конвенции Kachō; explicit immutable field в mask уже отлавливается выше).
func applyUpdateMaskTG(
	cur domain.TargetGroup, req *lbv1.UpdateTargetGroupRequest, mask []string,
) domain.TargetGroup {
	apply := func(field string) bool {
		if len(mask) == 0 {
			return true
		}
		for _, p := range mask {
			if p == field {
				return true
			}
		}
		return false
	}
	out := cur
	if apply("name") {
		out.Name = domain.LbName(req.GetName())
	}
	if apply("description") {
		out.Description = domain.LbDescription(req.GetDescription())
	}
	if apply("labels") {
		out.Labels = domain.LabelsFromMap(req.GetLabels())
	}
	if apply("health_check") && req.GetHealthCheck() != nil {
		out.HealthCheck = healthCheckFromPb(req.GetHealthCheck())
	}
	if apply("deregistration_delay_seconds") {
		out.DeregistrationDelaySeconds = req.GetDeregistrationDelaySeconds()
	}
	if apply("slow_start_seconds") {
		out.SlowStartSeconds = req.GetSlowStartSeconds()
	}
	return out
}
