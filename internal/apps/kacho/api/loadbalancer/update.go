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
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// UpdateLoadBalancerUseCase — UpdateMask discipline + async update.
// Mutable: name / description / labels / deletion_protection / session_affinity /
// disabled_announce_zones (REGIONAL only). Immutable: type / placement_type /
// v4_source / v6_source (→ bound address) / region_id / project_id.
type UpdateLoadBalancerUseCase struct {
	repo       Repo
	opsRepo    operations.Repo
	zoneClient ZoneClient
	logger     *slog.Logger
}

// NewUpdateLoadBalancerUseCase конструктор.
func NewUpdateLoadBalancerUseCase(repo Repo, opsRepo operations.Repo, zc ZoneClient, logger *slog.Logger) *UpdateLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &UpdateLoadBalancerUseCase{repo: repo, opsRepo: opsRepo, zoneClient: zc, logger: logger}
}

// knownUpdateFields — whitelist для update_mask. Поле вне списка → InvalidArgument.
var knownUpdateFields = map[string]bool{
	"name":                    true,
	"description":             true,
	"labels":                  true,
	"deletion_protection":     true,
	"session_affinity":        true,
	"disabled_announce_zones": true,
}

// immutableUpdateFields — hard-immutable; в mask → InvalidArgument.
var immutableUpdateFields = map[string]string{
	"type":           "type is immutable after NetworkLoadBalancer.Create",
	"placement_type": "placement_type is immutable after NetworkLoadBalancer.Create",
	"region_id":      "region_id is immutable after NetworkLoadBalancer.Create",
	"project_id":     "project_id is immutable; use NetworkLoadBalancerService.Move",
	"v4_source":      "v4_source is immutable after NetworkLoadBalancer.Create",
	"v6_source":      "v6_source is immutable after NetworkLoadBalancer.Create",
	"v4_address_id":  "v4_address_id is immutable after NetworkLoadBalancer.Create",
	"v6_address_id":  "v6_address_id is immutable after NetworkLoadBalancer.Create",
}

// Execute — sync mask validation + read existing → apply diff → ops insert → worker.
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

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := rd.LoadBalancers().Get(ctx, id)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}

	updated := applyUpdateMask(cur.LoadBalancer, req, mask)
	if err := updated.Validate(); err != nil {
		return nil, mapDomainErr(err)
	}

	// disabled_announce_zones — перевалидируется только когда mask её трогает
	// (REGIONAL-only + зоны ∈ регион + не все зоны, теми же правилами, что Create).
	if disabledAnnounceZonesInMask(mask) {
		if err := checkDisabledAnnounceZones(ctx, u.zoneClient,
			updated.PlacementType, string(updated.RegionID), updated.DisabledAnnounceZones); err != nil {
			return nil, err
		}
	}

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

	emitMirror := labelsInMask(mask)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doUpdate(workerCtx, updated, emitMirror)
	})

	return &op, nil
}

// labelsInMask — Update трогает labels: явный "labels" в mask либо пустой mask.
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

// disabledAnnounceZonesInMask — Update трогает disabled_announce_zones: явный путь
// в mask либо пустой mask (full-object PATCH переприменяет все mutable-поля).
func disabledAnnounceZonesInMask(mask []string) bool {
	if len(mask) == 0 {
		return true
	}
	for _, p := range mask {
		if p == "disabled_announce_zones" {
			return true
		}
	}
	return false
}

// doUpdate — worker: Writer → Update + outbox UPDATED (+ FGA-register при labels) → Commit.
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
// mutable полностью перезаписываются из req; immutable silent-ignored.
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
		out.SessionAffinity = domainSessionAffinity(req.GetSessionAffinity())
	}
	if apply("disabled_announce_zones") {
		out.DisabledAnnounceZones = normalizeZones(req.GetDisabledAnnounceZones())
	}
	return out
}
