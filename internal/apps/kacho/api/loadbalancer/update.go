// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// UpdateLoadBalancerUseCase — UpdateMask discipline + async update.
// Mutable: name / description / labels / deletion_protection /
// session_affinity / cross_zone_enabled.
// Immutable: type / region_id / project_id / network_id (in mask → InvalidArgument).
// allow_zonal_shift (proto field) — пока не хранится в domain (reserved для
// будущего toggle); если попало в mask — silent-accept без эффекта.
type UpdateLoadBalancerUseCase struct {
	repo                Repo
	opsRepo             operations.Repo
	securityGroupClient SecurityGroupClient
	logger              *slog.Logger
}

// NewUpdateLoadBalancerUseCase конструктор.
func NewUpdateLoadBalancerUseCase(repo Repo, opsRepo operations.Repo, sgc SecurityGroupClient, logger *slog.Logger) *UpdateLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &UpdateLoadBalancerUseCase{repo: repo, opsRepo: opsRepo, securityGroupClient: sgc, logger: logger}
}

// knownUpdateFields — допустимый whitelist для update_mask. Поле, отсутствующее
// здесь, в mask → InvalidArgument "unknown field".
var knownUpdateFields = map[string]bool{
	"name":                true,
	"description":         true,
	"labels":              true,
	"deletion_protection": true,
	"session_affinity":    true,
	"cross_zone_enabled":  true,
	"security_group_ids":  true,
	"allow_zonal_shift":   true, // silent-accept (no domain effect — reserved).
}

// immutableUpdateFields — hard-immutable; в mask → InvalidArgument с фиксированным текстом.
var immutableUpdateFields = map[string]string{
	"type":       "type is immutable after NetworkLoadBalancer.Create",
	"region_id":  "region_id is immutable after NetworkLoadBalancer.Create",
	"project_id": "project_id is immutable; use NetworkLoadBalancerService.Move",
	"network_id": "network_id is immutable after NetworkLoadBalancer.Create",
}

// Execute — sync mask validation + read existing → apply diff → ops insert →
// spawn worker.
func (u *UpdateLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.UpdateNetworkLoadBalancerRequest,
) (*operations.Operation, error) {
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(id); err != nil {
		return nil, err
	}

	mask := req.GetUpdateMask().GetPaths()
	for _, p := range mask {
		if msg, ok := immutableUpdateFields[p]; ok {
			return nil, status.Errorf(codes.InvalidArgument, "%s", msg)
		}
		if !knownUpdateFields[p] {
			return nil, status.Errorf(codes.InvalidArgument, "unknown update_mask field: %s", p)
		}
	}

	// Read current state.
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := rd.LoadBalancers().Get(ctx, id)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}

	// Apply mask (mask-empty → full PATCH applying all mutable fields from req,
	// silent-ignoring immutable).
	updated := applyUpdateMask(cur.LoadBalancer, req, mask)
	if err := updated.Validate(); err != nil {
		return nil, mapDomainErr(err)
	}

	// Sync-precheck security_group_ids (только когда mask их трогает — мутация,
	// не затрагивающая SG, не перевалидирует набор и не падает на dangling-SG).
	// network_id immutable → берётся из текущего состояния (updated.NetworkID ==
	// cur.NetworkID). not-found/чужая сеть → InvalidArgument; vpc недоступен →
	// Unavailable. Прежний набор SG при отказе сохраняется (мутация не доходит до
	// writer-TX).
	if securityGroupIDsInMask(mask) {
		if err := validateSecurityGroups(ctx, u.securityGroupClient, string(updated.NetworkID), updated.SecurityGroupIDs); err != nil {
			return nil, err
		}
	}

	// Operation row.
	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Update NetworkLoadBalancer %s", id),
		&lbv1.UpdateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: id},
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
	emitMirror := labelsInMask(mask)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doUpdate(workerCtx, updated, emitMirror)
	})

	return &op, nil
}

// labelsInMask reports whether the Update touches labels: explicit "labels" in
// the mask, or an empty mask (full-object PATCH reapplies all mutable fields).
func labelsInMask(mask []string) bool {
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

// doUpdate — worker: open Writer → Update + outbox UPDATED (+ FGA-register intent
// when labels changed) → Commit. The mirror-feed intent is written in the
// SAME writer-tx as the resource UPDATE (no dual-write); the emitter stamps a
// monotonic source_version so IAM applies the mirror last-source-state-wins.
func (u *UpdateLoadBalancerUseCase) doUpdate(ctx context.Context, lb domain.LoadBalancer, emitMirror bool) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	updated, err := w.LoadBalancers().Update(ctx, &lb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, string(updated.ID), string(updated.ProjectID),
		kachopg.OutboxActionUpdated, lbOutboxPayload(updated),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	if emitMirror {
		if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
			lbMirrorIntent(updated)); err != nil {
			return nil, mapDomainErr(err)
		}
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	pb, err := lbRecordToProto(updated)
	if err != nil {
		return nil, err
	}
	out, err := anypb.New(pb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return out, nil
}

// applyUpdateMask — наложить mask на текущий LB. Empty mask → full PATCH:
// mutable полностью перезаписываются из req; immutable silent-ignored
// (по конвенции Kachō).
func applyUpdateMask(
	cur domain.LoadBalancer, req *lbv1.UpdateNetworkLoadBalancerRequest, mask []string,
) domain.LoadBalancer {
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
	if apply("deletion_protection") {
		out.DeletionProtection = req.GetDeletionProtection()
	}
	if apply("session_affinity") {
		// out-of-domain → невалидная domain-строка, которую отвергает
		// updated.Validate каноничным field-сообщением.
		out.SessionAffinity = domainSessionAffinity(req.GetSessionAffinity())
	}
	if apply("cross_zone_enabled") {
		out.CrossZoneEnabled = req.GetCrossZoneEnabled()
	}
	if apply("security_group_ids") {
		// full-replace набора (set-семантика, dedup); пустой → снятие всех SG.
		out.SecurityGroupIDs = domain.SecurityGroupIDsFromStrings(req.GetSecurityGroupIds())
	}
	// allow_zonal_shift — silent-accept (no-op в domain).
	return out
}
