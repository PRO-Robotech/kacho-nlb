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

	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// AttachTargetGroupUseCase — idempotent INSERT into attached_target_groups
// pivot. Acceptance: replaces old full-replace `attached_target_groups`
// semantics with explicit Attach/Detach RPCs.
//
// Sync prechecks:
//   - LB exists (Get) и `TG.region_id == LB.region_id` (same-region constraint).
//   - TG exists (Get).
//
// Worker: Writer-TX → Attach (ON CONFLICT DO NOTHING) + outbox UPDATED → Commit.
// Trigger lb_status_recompute (DB-side) переводит LB.status INACTIVE→ACTIVE если
// добавился первый pair при наличии listener.
//
// Acceptance:.
type AttachTargetGroupUseCase struct {
	repo    Repo
	opsRepo operations.Repo
	logger  *slog.Logger
}

// NewAttachTargetGroupUseCase конструктор.
func NewAttachTargetGroupUseCase(repo Repo, opsRepo operations.Repo, logger *slog.Logger) *AttachTargetGroupUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &AttachTargetGroupUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// Execute — sync prechecks + ops insert + spawn worker.
func (u *AttachTargetGroupUseCase) Execute(
	ctx context.Context, req *lbv1.AttachNetworkLoadBalancerTargetGroupRequest,
) (*operations.Operation, error) {
	lbID := req.GetNetworkLoadBalancerId()
	if lbID == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(lbID); err != nil {
		return nil, err
	}
	attached := req.GetAttachedTargetGroup()
	if attached == nil || attached.GetTargetGroupId() == "" {
		return nil, errInvalidArg("attached_target_group.target_group_id", "required")
	}
	tgID := attached.GetTargetGroupId()
	if err := validateTargetGroupRefID(tgID); err != nil {
		return nil, err
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	lb, err := rd.LoadBalancers().Get(ctx, lbID)
	if err != nil {
		_ = rd.Close()
		return nil, mapDomainErr(err)
	}
	tg, err := rd.TargetGroups().Get(ctx, tgID)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if string(lb.RegionID) != string(tg.RegionID) {
		return nil, status.Errorf(codes.FailedPrecondition,
			"region mismatch: NetworkLoadBalancer is in region %s, TargetGroup is in region %s",
			lb.RegionID, tg.RegionID)
	}
	if string(lb.ProjectID) != string(tg.ProjectID) {
		return nil, status.Errorf(codes.FailedPrecondition,
			"project mismatch: NetworkLoadBalancer is in project %s, TargetGroup is in project %s",
			lb.ProjectID, tg.ProjectID)
	}

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Attach TargetGroup %s → NetworkLoadBalancer %s", tgID, lbID),
		&lbv1.AttachNetworkLoadBalancerTargetGroupMetadata{
			NetworkLoadBalancerId: lbID,
			TargetGroupId:         tgID,
		},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}

	projectID := string(lb.ProjectID)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doAttach(workerCtx, lbID, tgID, projectID)
	})
	return &op, nil
}

// doAttach — worker: Attach with ON CONFLICT DO NOTHING + outbox + commit.
func (u *AttachTargetGroupUseCase) doAttach(ctx context.Context, lbID, tgID, projectID string) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	rec, attached, err := w.AttachedTargetGroups().Attach(ctx, lbID, tgID, 0)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	_ = rec
	_ = attached
	// Always emit UPDATED even on idempotent no-op (downstream may sync attach
	// state). The trigger `attached_tg_lb_status_recompute_trg` recomputes
	// lb.status on real INSERT.
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, lbID, projectID,
		kachopg.OutboxActionUpdated, map[string]any{
			"id":              lbID,
			"target_group_id": tgID,
			"action":          "attach",
		},
	); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	// Reload LB to reflect trigger-driven status changes (INACTIVE→ACTIVE).
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()
	lb, err := rd.LoadBalancers().Get(ctx, lbID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	pb, err := lbRecordToProto(lb)
	if err != nil {
		return nil, err
	}
	out, err := anypb.New(pb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return out, nil
}
