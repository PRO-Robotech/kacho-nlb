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

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// StartLoadBalancerUseCase — STOPPED → STARTING → (ACTIVE | INACTIVE).
// Precondition (sync): status ∈ {STOPPED, INACTIVE}.
//
// Acceptance:.
type StartLoadBalancerUseCase struct {
	repo    Repo
	opsRepo operations.Repo
	logger  *slog.Logger
}

// NewStartLoadBalancerUseCase конструктор.
func NewStartLoadBalancerUseCase(repo Repo, opsRepo operations.Repo, logger *slog.Logger) *StartLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &StartLoadBalancerUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// Execute — read LB sync (precondition status check) + ops insert + spawn worker.
func (u *StartLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.StartNetworkLoadBalancerRequest,
) (*operations.Operation, error) {
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(id); err != nil {
		return nil, err
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
	if cur.Status != domain.LBStatusStopped && cur.Status != domain.LBStatusInactive {
		return nil, status.Errorf(codes.FailedPrecondition,
			"NetworkLoadBalancer %s cannot be started in status %s (expected STOPPED or INACTIVE)",
			id, cur.Status)
	}

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Start NetworkLoadBalancer %s", id),
		&lbv1.StartNetworkLoadBalancerMetadata{NetworkLoadBalancerId: id},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}
	expected := cur.Status
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doStart(workerCtx, id, expected)
	})
	return &op, nil
}

// doStart — atomic CAS STOPPED/INACTIVE → STARTING, then resolve to ACTIVE or
// INACTIVE based on listener/TG presence (trigger lb_status_recompute may also
// move things; here we drive it explicitly to keep semantics deterministic).
func (u *StartLoadBalancerUseCase) doStart(
	ctx context.Context, id string, expected domain.LBStatus,
) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	// шаг 1: CAS expected → STARTING. If CAS-miss (race) → FailedPrecondition.
	if _, err := w.LoadBalancers().SetStatusCAS(ctx, id, expected, domain.LBStatusStarting); err != nil {
		if errors.Is(err, kachorepo.ErrFailedPrecondition) {
			return nil, status.Errorf(codes.FailedPrecondition,
				"NetworkLoadBalancer %s status changed concurrently; retry Start", id)
		}
		return nil, mapDomainErr(err)
	}

	// шаг 2: resolve target status based on children presence.
	hasListeners, err := w.LoadBalancers().HasListeners(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	hasTG, err := w.LoadBalancers().HasAttachedTargetGroups(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	target := domain.LBStatusInactive
	if hasListeners && hasTG {
		target = domain.LBStatusActive
	}
	final, err := w.LoadBalancers().SetStatusCAS(ctx, id, domain.LBStatusStarting, target)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, string(final.ID), string(final.ProjectID),
		kachopg.OutboxActionUpdated, lbOutboxPayload(final),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	pb, err := lbRecordToProto(final)
	if err != nil {
		return nil, err
	}
	out, err := anypb.New(pb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return out, nil
}
