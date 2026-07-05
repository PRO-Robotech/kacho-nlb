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

// MoveLoadBalancerUseCase — change project_id (cross-project) keeping region.
// Sync prechecks:
//   - same-project — InvalidArgument "destination project is the same as source";
//   - destination project exists (peer ProjectClient.Get);
//   - LB has no attached target groups (FGA / data-model constraint: cross-project
//     TG attach запрещён — Move заблокирован если есть).
//
// Worker: Writer-TX → repo.MoveProject (UPDATE LB + cascade UPDATE listeners) +
// outbox MOVED + FGA-register(dst project) + FGA-unregister(src project) → Commit
// (Вариант A: project-rewrite = register new-project tuple + unregister
// old-project tuple, both in the same writer-tx as MoveProject — no dual-write).
//
// Acceptance:.
type MoveLoadBalancerUseCase struct {
	repo          Repo
	opsRepo       operations.Repo
	projectClient ProjectClient
	checkClient   CheckClient
	logger        *slog.Logger
}

// NewMoveLoadBalancerUseCase конструктор. checkClient авторизует caller'а на
// destination project (`editor on project:<dst>`); nil → dst-authz пропускается
// (dev/unwired).
func NewMoveLoadBalancerUseCase(
	repo Repo, opsRepo operations.Repo,
	pc ProjectClient, checkClient CheckClient, logger *slog.Logger,
) *MoveLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &MoveLoadBalancerUseCase{
		repo: repo, opsRepo: opsRepo,
		projectClient: pc, checkClient: checkClient, logger: logger,
	}
}

// Execute — sync prechecks + ops insert + spawn worker.
func (u *MoveLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.MoveNetworkLoadBalancerRequest,
) (*operations.Operation, error) {
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(id); err != nil {
		return nil, err
	}
	dst := req.GetDestinationProjectId()
	if dst == "" {
		return nil, errInvalidArg("destination_project_id", "required")
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := rd.LoadBalancers().Get(ctx, id)
	if err != nil {
		_ = rd.Close()
		return nil, mapDomainErr(err)
	}
	if string(cur.ProjectID) == dst {
		_ = rd.Close()
		return nil, status.Error(codes.InvalidArgument,
			"destination project is the same as source")
	}
	hasTG, err := rd.LoadBalancers().HasAttachedTargetGroups(ctx, id)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if hasTG {
		return nil, status.Error(codes.FailedPrecondition,
			"NetworkLoadBalancer has attached target group(s); detach before Move")
	}

	// Peer-check destination project.
	if u.projectClient != nil {
		if _, err := u.projectClient.Get(ctx, dst); err != nil {
			return nil, peerErrToStatus(err, "project", dst)
		}
	}

	// Destination-project authorization (audit SEC-high #2 / CWE-862/863): the
	// per-RPC interceptor authorizes the caller on the SOURCE LB only; the caller
	// must ALSO hold `editor` on the destination project, else it could inject
	// the LB into a victim's project. This is a handler-side Check by design (an
	// RPCEntry has a single object extractor and cannot check the destination).
	if err := u.authorizeDestination(ctx, dst); err != nil {
		return nil, err
	}

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Move NetworkLoadBalancer %s → %s", id, dst),
		&lbv1.MoveNetworkLoadBalancerMetadata{
			NetworkLoadBalancerId: id,
			DestinationProjectId:  dst,
		},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}
	srcProject := string(cur.ProjectID)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doMove(workerCtx, id, srcProject, dst)
	})
	return &op, nil
}

// authorizeDestination авторизует caller'а на destination project
// (`editor on project:<dst>`). nil checkClient или system/empty subject
// (breakglass/dev — source-check тоже обойдён interceptor'ом) → пропуск.
func (u *MoveLoadBalancerUseCase) authorizeDestination(ctx context.Context, dst string) error {
	if u.checkClient == nil {
		return nil
	}
	p := operations.PrincipalFromContext(ctx)
	subject := domain.FGASubjectFromPrincipal(p.Type, p.ID)
	if subject == "" {
		return nil
	}
	allowed, err := u.checkClient.Check(ctx, subject, domain.FGARelationEditor,
		domain.FGAObjectRef(domain.FGAObjectTypeProject, dst))
	if err != nil {
		return moveDestCheckErr(err, dst)
	}
	if !allowed {
		return status.Errorf(codes.PermissionDenied,
			"caller is not authorized (editor) on destination project %s", dst)
	}
	return nil
}

// moveDestCheckErr маппит ошибку destination-authz Check'а в gRPC-status
// (fail-closed). no-path (нет grant'а) → PermissionDenied; iam недоступен →
// Unavailable; bad args → InvalidArgument; прочее → Internal.
func moveDestCheckErr(err error, dst string) error {
	switch {
	case errors.Is(err, authz.ErrNoPath):
		return status.Errorf(codes.PermissionDenied,
			"caller is not authorized (editor) on destination project %s", dst)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, "authorization check unavailable")
	case errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "authorization check: %v", err)
	}
	return status.Error(codes.Internal, "authorization check failed")
}

// doMove — worker: Writer-TX → MoveProject + outbox MOVED → Commit → FGA rewrite.
func (u *MoveLoadBalancerUseCase) doMove(ctx context.Context, id, srcProject, dstProject string) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	moved, err := w.LoadBalancers().MoveProject(ctx, id, dstProject)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachorepo.OutboxResourceLoadBalancer, string(moved.ID), string(moved.ProjectID),
		kachorepo.OutboxActionMoved, lbMovedPayload(string(moved.ID), srcProject, dstProject),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// Also emit UPDATED for downstream watchers that don't subscribe to MOVED.
	if err := w.Outbox().Emit(ctx,
		kachorepo.OutboxResourceLoadBalancer, string(moved.ID), string(moved.ProjectID),
		kachorepo.OutboxActionUpdated, lbOutboxPayload(moved),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// project-rewrite as register(dst) + unregister(src) in the SAME tx.
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
		lbUnregisterIntent(id, dstProject)); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventUnregister,
		lbUnregisterIntent(id, srcProject)); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	pb, err := lbRecordToProto(moved)
	if err != nil {
		return nil, err
	}
	out, err := anypb.New(pb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return out, nil
}
