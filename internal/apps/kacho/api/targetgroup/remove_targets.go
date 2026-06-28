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
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// RemoveTargetsUseCase — фаза A.
//
// фаза A (этот use-case): UPDATE targets SET status='DRAINING',
// drain_started_at=now WHERE matching identities → ops.MarkDone(true). Latency
// <500ms (один UPDATE + outbox + Commit).
//
// фаза B обрабатывается background-runner'ом `jobs/target_drain_runner.go`
// (запущен из cmd/main.go как параллельная task'а) — DELETE expired rows после
// tg.deregistration_delay_seconds + outbox UPDATED.
//
// Identity-keyed resolution: client passes Target identities (instance_id /
// nic_id / ip_ref / external_ip), а фаза A SQL работает по target.id —
// resolve identity → target.id делается через ListTargets + match.
//
// Idempotency: identity, которой нет в TG, тихо игнорируется.
// Identity, уже DRAINING, тоже тихо игнорируется (RemoveTargetsMarkDraining
// фильтрует WHERE status='ACTIVE').
type RemoveTargetsUseCase struct {
	repo    Repo
	opsRepo OpsRepo
	logger  *slog.Logger
}

// NewRemoveTargetsUseCase конструктор.
func NewRemoveTargetsUseCase(repo Repo, opsRepo OpsRepo, logger *slog.Logger) *RemoveTargetsUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &RemoveTargetsUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// Execute — sync validate + ops insert + spawn worker.
func (u *RemoveTargetsUseCase) Execute(
	ctx context.Context, req *lbv1.RemoveTargetsRequest,
) (*operations.Operation, error) {
	tgID := req.GetTargetGroupId()
	if tgID == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}
	if err := validateTargetGroupID(tgID); err != nil {
		return nil, err
	}
	if len(req.GetTargets()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one target is required")
	}
	if len(req.GetTargets()) > domain.MaxTargetsPerGroup {
		return nil, status.Errorf(codes.InvalidArgument,
			"too many targets in a single RemoveTargets call (max %d)", domain.MaxTargetsPerGroup)
	}
	targets := targetsFromPb(req.GetTargets())
	for i := range targets {
		if err := targets[i].Validate(); err != nil {
			return nil, mapDomainErr(err)
		}
	}

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("RemoveTargets from TargetGroup %s (n=%d)", tgID, len(targets)),
		&lbv1.RemoveTargetsMetadata{TargetGroupId: tgID},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doRemove(workerCtx, tgID, targets)
	})
	return &op, nil
}

// doRemove — worker: TG.Get (status guard) → resolve identities → MarkDraining +
// outbox UPDATED (only if affected>0) → Commit. Re-read TG для response.
func (u *RemoveTargetsUseCase) doRemove(ctx context.Context, tgID string, targets []domain.Target) (*anypb.Any, error) {
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	tg, err := rd.TargetGroups().Get(ctx, tgID)
	if err != nil {
		_ = rd.Close()
		return nil, mapDomainErr(err)
	}
	// Note: DELETING TG может иметь pending RemoveTargets — это no-op (rows
	// будут удалены drain-runner'ом). Не блокируем sentinel'ом FailedPrecondition
	// (semantics RemoveTargets — "ensure target is draining"); идемпотентно.
	existing, err := rd.TargetGroups().ListTargets(ctx, tgID)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}

	targetIDs := resolveTargetIdentities(existing, targets)

	// Open Writer-TX → MarkDraining + (conditional) outbox UPDATED + Commit.
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	affected := 0
	if len(targetIDs) > 0 {
		n, err := w.TargetGroups().RemoveTargetsMarkDraining(ctx, tgID, targetIDs)
		if err != nil {
			return nil, mapDomainErr(err)
		}
		affected = n
	}
	if affected > 0 {
		if err := w.Outbox().Emit(ctx,
			kachopg.OutboxResourceTargetGroup, tgID, string(tg.ProjectID),
			kachopg.OutboxActionUpdated, map[string]any{
				"id":             tgID,
				"project_id":     string(tg.ProjectID),
				"region_id":      string(tg.RegionID),
				"trigger":        "remove_targets_phase_a",
				"draining_count": affected,
			},
		); err != nil {
			return nil, mapDomainErr(err)
		}
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	// Re-read TG (с inline targets, теперь часть в DRAINING).
	rd2, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	updated, err := rd2.TargetGroups().Get(ctx, tgID)
	_ = rd2.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return marshalTargetGroup(updated)
}

// resolveTargetIdentities — для каждого target в req находит matching target.id
// среди existing rows. Не найденная identity — silently skipped (idempotent).
//
// Match-keys per identity-type:
//   - instance_id: equal InstanceID.
//   - nic_id: equal NicID.
//   - ip_ref: equal (subnet_id, address).
//   - external_ip: equal address (zone_id не входит в identity match —
//     это hint, не часть keys).
func resolveTargetIdentities(existing []*kachorepo.TargetRecord, wanted []domain.Target) []string {
	if len(existing) == 0 || len(wanted) == 0 {
		return nil
	}
	var ids []string
	for _, w := range wanted {
		for _, e := range existing {
			if targetIdentityEqual(w, e.Target) {
				ids = append(ids, e.ID)
				break
			}
		}
	}
	return ids
}

// targetIdentityEqual — true if a и b имеют одинаковый identity-tuple
// (без weight + без external_ip.zone_id).
func targetIdentityEqual(a, b domain.Target) bool {
	if av, ok := a.InstanceID.Maybe(); ok {
		if bv, ok2 := b.InstanceID.Maybe(); ok2 {
			return av == bv
		}
		return false
	}
	if av, ok := a.NicID.Maybe(); ok {
		if bv, ok2 := b.NicID.Maybe(); ok2 {
			return av == bv
		}
		return false
	}
	if a.IPRef != nil {
		if b.IPRef == nil {
			return false
		}
		return a.IPRef.SubnetID == b.IPRef.SubnetID && a.IPRef.Address == b.IPRef.Address
	}
	if a.ExternalIP != nil {
		if b.ExternalIP == nil {
			return false
		}
		return a.ExternalIP.Address == b.ExternalIP.Address
	}
	return false
}
