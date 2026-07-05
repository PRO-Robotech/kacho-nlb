// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// AttachTargetGroupUseCase — idempotent INSERT into attached_target_groups
// pivot. Acceptance: replaces old full-replace `attached_target_groups`
// semantics with explicit Attach/Detach RPCs.
//
// Sync prechecks:
//   - LB exists (Get) и `TG.region_id == LB.region_id` (same-region constraint).
//   - TG exists (Get).
//   - caller holds `viewer` on the TargetGroup object (handler-side Check —
//     audit SEC r3 #3): the per-RPC interceptor gates only the LB (v_update);
//     without a TG-object Check a custom role granting v_update directly on one
//     LB (without project-editor) could wire in a same-project TG the caller
//     holds no grant on. The standard FGA cascade (project-editor ⇒ viewer on
//     same-project TGs) already implies this, so the Check is a no-op for
//     ordinary bindings; it only bites narrowly-scoped custom grants (CWE-863).
//
// Worker: Writer-TX → Attach (ON CONFLICT DO NOTHING) + outbox UPDATED → Commit.
// Trigger lb_status_recompute (DB-side) переводит LB.status INACTIVE→ACTIVE если
// добавился первый pair при наличии listener.
//
// Acceptance:.
type AttachTargetGroupUseCase struct {
	repo        Repo
	opsRepo     operations.Repo
	checkClient CheckClient
	logger      *slog.Logger
}

// NewAttachTargetGroupUseCase конструктор. checkClient авторизует caller'а на
// target-group object (`viewer on lb_target_group:<tg>`); nil → TG-authz
// пропускается (dev/unwired; breakglass также обходит source-check interceptor'а).
func NewAttachTargetGroupUseCase(repo Repo, opsRepo operations.Repo, checkClient CheckClient, logger *slog.Logger) *AttachTargetGroupUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &AttachTargetGroupUseCase{repo: repo, opsRepo: opsRepo, checkClient: checkClient, logger: logger}
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

	// Target-group authorization (audit SEC r3 #3 / CWE-863): interceptor gated
	// the LB only; the caller must ALSO hold `viewer` on the TG object it is
	// wiring in, else a narrow custom v_update grant on the LB could attach a TG
	// the caller has no authorization over.
	if err := u.authorizeTargetGroup(ctx, tgID); err != nil {
		return nil, err
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

// authorizeTargetGroup авторизует caller'а на TG object (`viewer on
// lb_target_group:<tg>`). nil checkClient или system/empty subject
// (breakglass/dev — source-check тоже обойдён interceptor'ом) → пропуск.
func (u *AttachTargetGroupUseCase) authorizeTargetGroup(ctx context.Context, tgID string) error {
	if u.checkClient == nil {
		return nil
	}
	p := operations.PrincipalFromContext(ctx)
	subject := domain.FGASubjectFromPrincipal(p.Type, p.ID)
	if subject == "" {
		return nil
	}
	allowed, err := u.checkClient.Check(ctx, subject, domain.FGARelationViewer,
		domain.FGAObjectRef(domain.FGAObjectTypeTargetGroup, tgID))
	if err != nil {
		return attachTGCheckErr(err, tgID)
	}
	if !allowed {
		return status.Errorf(codes.PermissionDenied,
			"caller is not authorized (viewer) on target group %s", tgID)
	}
	return nil
}

// attachTGCheckErr маппит ошибку TG-authz Check'а в gRPC-status (fail-closed).
// no-path → PermissionDenied; iam недоступен → Unavailable; bad args →
// InvalidArgument; прочее → Internal.
func attachTGCheckErr(err error, tgID string) error {
	switch {
	case errors.Is(err, authz.ErrNoPath):
		return status.Errorf(codes.PermissionDenied,
			"caller is not authorized (viewer) on target group %s", tgID)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, "authorization check unavailable")
	case errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "authorization check: %v", err)
	}
	return status.Error(codes.Internal, "authorization check failed")
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
		kachorepo.OutboxResourceLoadBalancer, lbID, projectID,
		kachorepo.OutboxActionUpdated, map[string]any{
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
