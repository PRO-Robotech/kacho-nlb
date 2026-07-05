// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/H-BF/corlib/pkg/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// UpdateUseCase — async Update Listener.
//
// Sync (handler-thread):
//  1. listener_id required.
//  2. repo.Reader Listener.Get (NotFound иначе).
//  3. update_mask discipline (единая для всех ресурсов, api-conventions):
//     - empty mask → full-object PATCH: применяются все mutable-поля из тела;
//     immutable из тела silently игнорируются (parity с loadbalancer/targetgroup).
//     - unknown field → InvalidArgument "field '<X>' is not recognised in update_mask".
//     - immutable field (load_balancer_id / protocol / port / target_port /
//     project_id) → InvalidArgument
//     по конвенции Kachō `"<field> is immutable after Listener.Create"`.
//  4. Validate per-mask field (name regex, labels schema, etc).
//  5. default_target_group_id same-region precheck  — async-soft
//     либо sync; здесь делаем sync через kacho-nlb local TG.Get (same-DB
//     query); cross-region → FailedPrecondition фиксированный текст.
//  6. opsRepo.CreateWithPrincipal + operations.Run.
//
// Async worker:
//  1. Listener.SetStatusCAS(ACTIVE → UPDATING) — атомарный transient guard.
//  2. repo.Writer.Listeners.Update + outbox emit `nlb_listener:<id> UPDATED`.
//  3. SetStatusCAS(UPDATING → ACTIVE).
//  4. ops.MarkDone(response=Listener).
type UpdateUseCase struct {
	repo    RepoFactory
	opsRepo OperationsRepo
	logger  *slog.Logger
}

// NewUpdateUseCase — конструктор.
func NewUpdateUseCase(repo RepoFactory, opsRepo OperationsRepo, logger *slog.Logger) *UpdateUseCase {
	return &UpdateUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// Mutable update_mask paths (single source of truth).
var listenerMutableMaskPaths = map[string]struct{}{
	"name":                    {},
	"description":             {},
	"labels":                  {},
	"default_target_group_id": {},
	"proxy_protocol_v2":       {},
}

// Immutable update_mask paths (in mask → InvalidArgument with фиксированный текст).
// VIP консолидирован на LoadBalancer: address_id/ip_version/subnet_id/region_id
// сняты с листенера (proto reserved), поэтому в immutable-списке их больше нет —
// неизвестный путь → "field '<x>' is not recognised in update_mask".
var listenerImmutableMaskPaths = map[string]struct{}{
	"load_balancer_id": {},
	"protocol":         {},
	"port":             {},
	"target_port":      {},
	"project_id":       {},
}

// Run — sync validate + spawn worker. Errors mapped to gRPC codes inline.
func (u *UpdateUseCase) Run(ctx context.Context, req *lbv1.UpdateListenerRequest) (*operations.Operation, error) {
	id := req.GetListenerId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "listener_id required")
	}
	if err := validateListenerID(id); err != nil {
		return nil, err
	}

	mask := req.GetUpdateMask().GetPaths()
	if err := validateListenerMask(mask); err != nil {
		return nil, err
	}

	// Load current row (verifies existence; needed for same-region precheck +
	// merge for partial Update).
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := rd.Listeners().Get(ctx, id)
	if err != nil {
		_ = rd.Close()
		return nil, mapDomainErr(err)
	}

	// Apply mask-driven mutations on a copy of current domain entity. Empty mask →
	// full-object PATCH: apply пропускает все mutable-поля (parity с
	// loadbalancer.applyUpdateMask / targetgroup.applyUpdateMaskTG).
	next := cur.Listener
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
	tgRegionCheckNeeded := false
	tgIDToCheck := ""
	if apply("name") {
		n := domain.LbName(req.GetName())
		if err := n.Validate(); err != nil {
			_ = rd.Close()
			return nil, err
		}
		next.Name = n
	}
	if apply("description") {
		d := domain.LbDescription(req.GetDescription())
		if err := d.Validate(); err != nil {
			_ = rd.Close()
			return nil, err
		}
		next.Description = d
	}
	if apply("labels") {
		lbls := domain.LabelsFromMap(req.GetLabels())
		if err := domain.ValidateLabels(lbls); err != nil {
			_ = rd.Close()
			return nil, err
		}
		next.Labels = lbls
	}
	if apply("default_target_group_id") {
		tg := req.GetDefaultTargetGroupId()
		if tg == "" {
			next.DefaultTargetGroupID = option.ValueOf[domain.ResourceID]{}
		} else {
			next.DefaultTargetGroupID = option.MustNewOption(domain.ResourceID(tg))
			tgIDToCheck = tg
			tgRegionCheckNeeded = true
		}
	}
	if apply("proxy_protocol_v2") {
		next.ProxyProtocolV2 = req.GetProxyProtocolV2()
	}

	// Same-region precheck for default_target_group_id.
	if tgRegionCheckNeeded {
		tg, terr := rd.TargetGroups().Get(ctx, tgIDToCheck)
		_ = rd.Close()
		if terr != nil {
			return nil, mapDomainErr(terr)
		}
		if tg.RegionID != cur.RegionID {
			return nil, status.Errorf(codes.FailedPrecondition,
				"default target group region %s does not match listener region %s",
				tg.RegionID, cur.RegionID)
		}
	} else {
		_ = rd.Close()
	}

	// Re-validate the merged domain entity (defence in depth — partial fields
	// already validated above; this catches cross-field invariants).
	if err := next.Validate(); err != nil {
		return nil, err
	}

	// Create Operation row.
	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Update listener %s", string(next.Name)),
		&lbv1.UpdateListenerMetadata{ListenerId: id},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}

	// (parity with LB/TG update.go labelsInMask):
	// re-emit the FGA-register mirror-feed (carrying the NEW labels) ONLY when
	// labels change — labels in mask, or empty mask (full PATCH always reapplies
	// labels). A non-labels Update is a mirror no-op (skip the intent). Full
	// label removal → mirror.upsert with empty labels (NOT Unregister) — the
	// listener still lives; this stales label selectors without dropping the
	// resource registration.
	emitMirror := listenerLabelsInMask(mask)

	// Snapshot inputs into worker closure to avoid handler-ctx capture.
	snap := next
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doUpdate(workerCtx, snap, emitMirror)
	})
	return &op, nil
}

// listenerLabelsInMask reports whether the Update touches labels: explicit
// "labels" in the mask, or an empty mask (full-object PATCH reapplies all mutable
// fields). Parity with loadbalancer.labelsInMask / targetgroup.labelsInMaskTG.
func listenerLabelsInMask(mask []string) bool {
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

// validateListenerMask — verifies every path is one of mutable; rejects
// immutable + unknown.
func validateListenerMask(paths []string) error {
	for _, p := range paths {
		if _, ok := listenerImmutableMaskPaths[p]; ok {
			return status.Errorf(codes.InvalidArgument,
				"%s is immutable after Listener.Create", p)
		}
		if _, ok := listenerMutableMaskPaths[p]; ok {
			continue
		}
		return status.Errorf(codes.InvalidArgument,
			"field '%s' is not recognised in update_mask", p)
	}
	return nil
}

// doUpdate — worker-side flow. When emitMirror is true (labels changed),
// the FGA-register mirror-feed intent is written in the SAME
// writer-tx as the resource UPDATE (no dual-write); the emitter
// stamps a monotonic source_version so IAM applies the mirror last-source-wins.
func (u *UpdateUseCase) doUpdate(ctx context.Context, next domain.Listener, emitMirror bool) (*anypb.Any, error) {
	// Transient UPDATING status guard. CAS handles concurrent Delete (status
	// already DELETING → FailedPrecondition; client sees фиксированный текст).
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		w.Abort()
	}()

	// We don't lock status to UPDATING in DB transition (one-tx Update is
	// simpler + atomic). UPDATING is a transient projection of in-flight
	// Operation — caller polls Operation.done, sees done=true with the new
	// row. This mirrors kacho-vpc Network.Update flow.
	updated, err := w.Listeners().Update(ctx, &next)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeListener, string(updated.ID), string(updated.ProjectID),
		outboxActionUpdated, listenerPayloadMap(updated),
	); err != nil {
		return nil, mapDomainErr(fmt.Errorf("%w: outbox emit listener UPDATED: %v", domain.ErrInternal, err))
	}
	// refresh the IAM resource_mirror with the current
	// labels in the SAME writer-tx (gated, upsert-not-unregister,
	// atomic). Label removal → upsert with empty labels, which stales the γ
	// label selector while keeping the listener registered.
	if emitMirror {
		if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
			listenerMirrorIntent(updated)); err != nil {
			return nil, mapDomainErr(fmt.Errorf("%w: fga register-intent emit: %v", domain.ErrInternal, err))
		}
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}
	committed = true
	return marshalListener(updated)
}

// listenerMirrorIntent builds the mirror-feed register-intent for an
// UPDATED Listener: the parent-link tuple (re-register is idempotent in IAM)
// carrying the refreshed labels + parent-project so kacho-iam updates its
// resource_mirror. No creator tuple — Update never re-assigns ownership; this is a
// pure labels-refresh feed (parity with lbMirrorIntent / tgMirrorIntent). Empty
// labels (full removal) is a valid upsert payload — it stales the label selector
// without unregistering the listener. source_version is stamped by the
// outbox emitter from the DB clock inside the writer-tx.
func listenerMirrorIntent(l *kachorepo.ListenerRecord) domain.FGARegisterIntent {
	id := string(l.ID)
	return domain.FGARegisterIntent{
		Kind:       "Listener",
		ResourceID: id,
		Tuples: []domain.FGATuple{
			domain.FGAParentLinkTuple(
				domain.FGAObjectTypeLoadBalancer, string(l.LoadBalancerID),
				domain.FGARelationLoadBalancer,
				domain.FGAObjectTypeListener, id,
			),
		},
		Labels:          domain.LabelsToMap(l.Labels),
		ParentProjectID: string(l.ProjectID),
	}
}
