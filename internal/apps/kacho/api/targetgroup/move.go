// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

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

// MoveTargetGroupUseCase — cross-project move.
//
// Sync prechecks:
//   - same-project ("destination project is the same as source") → InvalidArgument;
//   - HasAttachedLB > 0 → FailedPrecondition с фиксированным текстом
//     `"TargetGroup is attached to N load balancer(s); detach before moving"`;
//   - destination project exists (peer ProjectClient.Get) — InvalidArgument если NotFound.
//
// Worker:
//   - Writer-TX → MoveProject (UPDATE project_id) + outbox MOVED + outbox
//     UPDATED + FGA-register(dst project) + FGA-unregister(src project) → Commit
//     (Вариант A: project-rewrite in the SAME tx as MoveProject).
//
// Destination-project authorization (`editor on project:<dst>`) — handler-side
// Check via checkClient (audit SEC-high #2): the per-RPC interceptor authorizes
// only the source TG, so the caller's grant on the destination is verified here.
type MoveTargetGroupUseCase struct {
	repo          Repo
	opsRepo       OpsRepo
	projectClient ProjectClient
	checkClient   CheckClient
	logger        *slog.Logger
}

// NewMoveTargetGroupUseCase конструктор. checkClient авторизует caller'а на
// destination project (`editor on project:<dst>`); nil → dst-authz пропускается
// (dev/unwired).
func NewMoveTargetGroupUseCase(
	repo Repo, opsRepo OpsRepo,
	pc ProjectClient, checkClient CheckClient, logger *slog.Logger,
) *MoveTargetGroupUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &MoveTargetGroupUseCase{
		repo: repo, opsRepo: opsRepo,
		projectClient: pc, checkClient: checkClient, logger: logger,
	}
}

// Execute — sync prechecks + ops insert + spawn worker.
func (u *MoveTargetGroupUseCase) Execute(
	ctx context.Context, req *lbv1.MoveTargetGroupRequest,
) (*operations.Operation, error) {
	id := req.GetTargetGroupId()
	if id == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}
	if err := validateTargetGroupID(id); err != nil {
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
	cur, err := rd.TargetGroups().Get(ctx, id)
	if err != nil {
		_ = rd.Close()
		return nil, mapDomainErr(err)
	}
	if string(cur.ProjectID) == dst {
		_ = rd.Close()
		return nil, status.Error(codes.InvalidArgument,
			"destination project is the same as source")
	}
	hasLB, err := rd.TargetGroups().HasAttachedLB(ctx, id)
	if err != nil {
		_ = rd.Close()
		return nil, mapDomainErr(err)
	}
	if hasLB {
		atgs, lerr := rd.AttachedTargetGroups().ListByTG(ctx, id)
		_ = rd.Close()
		if lerr != nil {
			return nil, mapDomainErr(lerr)
		}
		return nil, status.Errorf(codes.FailedPrecondition,
			"TargetGroup is attached to %d load balancer(s); detach before moving", len(atgs))
	}
	_ = rd.Close()

	// Peer-check destination project.
	if u.projectClient != nil {
		if _, err := u.projectClient.Get(ctx, dst); err != nil {
			return nil, peerErrToStatus(err, "project", dst)
		}
	}

	// Destination-project authorization (audit SEC-high #2 / CWE-862/863): the
	// per-RPC interceptor authorizes the caller on the SOURCE TG only; the caller
	// must ALSO hold `editor` on the destination project, else it could inject
	// the TG into a victim's project.
	if err := u.authorizeDestination(ctx, dst); err != nil {
		return nil, err
	}

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Move TargetGroup %s -> %s", id, dst),
		&lbv1.MoveTargetGroupMetadata{
			TargetGroupId:        id,
			DestinationProjectId: dst,
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
func (u *MoveTargetGroupUseCase) authorizeDestination(ctx context.Context, dst string) error {
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

// doMove — worker: Writer-TX → MoveProject + outbox MOVED + UPDATED → Commit → FGA rewrite.
func (u *MoveTargetGroupUseCase) doMove(ctx context.Context, id, srcProject, dstProject string) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	moved, err := w.TargetGroups().MoveProject(ctx, id, dstProject)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachorepo.OutboxResourceTargetGroup, string(moved.ID), string(moved.ProjectID),
		kachorepo.OutboxActionMoved, tgMovedPayload(string(moved.ID), srcProject, dstProject),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// UPDATED — для downstream watchers, не подписанных на MOVED.
	if err := w.Outbox().Emit(ctx,
		kachorepo.OutboxResourceTargetGroup, string(moved.ID), string(moved.ProjectID),
		kachorepo.OutboxActionUpdated, tgOutboxPayload(moved),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// project-rewrite as register(dst) + unregister(src) in the SAME tx.
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
		tgUnregisterIntent(id, dstProject)); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventUnregister,
		tgUnregisterIntent(id, srcProject)); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}
	return marshalTargetGroup(moved)
}
