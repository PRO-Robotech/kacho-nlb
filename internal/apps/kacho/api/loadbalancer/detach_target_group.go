// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// DetachTargetGroupUseCase — idempotent DELETE from attached_target_groups
// pivot. Acceptance:.
//
// 0 affected rows → no-op (idempotent). Trigger lb_status_recompute переводит
// LB.status ACTIVE→INACTIVE если убрали последний pair / последний listener.
type DetachTargetGroupUseCase struct {
	repo    Repo
	opsRepo operations.Repo
	logger  *slog.Logger
}

// NewDetachTargetGroupUseCase конструктор.
func NewDetachTargetGroupUseCase(repo Repo, opsRepo operations.Repo, logger *slog.Logger) *DetachTargetGroupUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &DetachTargetGroupUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// Execute — sync read (для project_id snapshot) + ops insert + spawn worker.
func (u *DetachTargetGroupUseCase) Execute(
	ctx context.Context, req *lbv1.DetachNetworkLoadBalancerTargetGroupRequest,
) (*operations.Operation, error) {
	lbID := req.GetNetworkLoadBalancerId()
	if lbID == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(lbID); err != nil {
		return nil, err
	}
	tgID := req.GetTargetGroupId()
	if tgID == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}
	if err := validateTargetGroupRefID(tgID); err != nil {
		return nil, err
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	lb, err := rd.LoadBalancers().Get(ctx, lbID)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Detach TargetGroup %s from NetworkLoadBalancer %s", tgID, lbID),
		&lbv1.DetachNetworkLoadBalancerTargetGroupMetadata{
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
		return u.doDetach(workerCtx, lbID, tgID, projectID)
	})
	return &op, nil
}

// doDetach — worker: Detach (idempotent) + outbox + commit.
func (u *DetachTargetGroupUseCase) doDetach(ctx context.Context, lbID, tgID, projectID string) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	if err := w.AttachedTargetGroups().Detach(ctx, lbID, tgID); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, lbID, projectID,
		kachopg.OutboxActionUpdated, map[string]any{
			"id":              lbID,
			"target_group_id": tgID,
			"action":          "detach",
		},
	); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	// Reload LB after trigger-driven status change.
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
