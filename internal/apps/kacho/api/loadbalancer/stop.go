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
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// StopLoadBalancerUseCase — ACTIVE/INACTIVE → STOPPING → STOPPED.
// Precondition (sync): status ∈ {ACTIVE, INACTIVE}.
//
// Acceptance:.
type StopLoadBalancerUseCase struct {
	repo    Repo
	opsRepo operations.Repo
	logger  *slog.Logger
}

// NewStopLoadBalancerUseCase конструктор.
func NewStopLoadBalancerUseCase(repo Repo, opsRepo operations.Repo, logger *slog.Logger) *StopLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &StopLoadBalancerUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// Execute — read LB (precondition check) + ops insert + spawn worker.
func (u *StopLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.StopNetworkLoadBalancerRequest,
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
	if cur.Status != domain.LBStatusActive && cur.Status != domain.LBStatusInactive {
		return nil, status.Errorf(codes.FailedPrecondition,
			"NetworkLoadBalancer %s cannot be stopped in status %s (expected ACTIVE or INACTIVE)",
			id, cur.Status)
	}

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Stop NetworkLoadBalancer %s", id),
		&lbv1.StopNetworkLoadBalancerMetadata{NetworkLoadBalancerId: id},
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
		return u.doStop(workerCtx, id, expected)
	})
	return &op, nil
}

// doStop — CAS expected → STOPPING → STOPPED in one TX.
func (u *StopLoadBalancerUseCase) doStop(
	ctx context.Context, id string, expected domain.LBStatus,
) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	if _, err := w.LoadBalancers().SetStatusCAS(ctx, id, expected, domain.LBStatusStopping); err != nil {
		if errors.Is(err, kachorepo.ErrFailedPrecondition) {
			return nil, status.Errorf(codes.FailedPrecondition,
				"NetworkLoadBalancer %s status changed concurrently; retry Stop", id)
		}
		return nil, mapDomainErr(err)
	}
	final, err := w.LoadBalancers().SetStatusCAS(ctx, id, domain.LBStatusStopping, domain.LBStatusStopped)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachorepo.OutboxResourceLoadBalancer, string(final.ID), string(final.ProjectID),
		kachorepo.OutboxActionUpdated, lbOutboxPayload(final),
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
