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
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// MoveTargetGroupUseCase — cross-project move (acceptance GWT-TGR-025..027).
//
// Sync prechecks:
//   - same-project ("destination project is the same as source") → InvalidArgument;
//   - HasAttachedLB > 0 → FailedPrecondition verbatim
//     `"TargetGroup is attached to N load balancer(s); detach before moving"`;
//   - destination project exists (peer ProjectClient.Get) — InvalidArgument если NotFound.
//
// Worker:
//   - Writer-TX → MoveProject (UPDATE project_id) + outbox MOVED + outbox
//     UPDATED + FGA-register(dst project) + FGA-unregister(src project) → Commit
//     (SEC-D Вариант A: project-rewrite in the SAME tx as MoveProject).
//
// GWT-TGR-027 (scope editor на dst project) — реализуется api-gateway authz-
// interceptor'ом (KAC-127 Phase 4); use-case остаётся unaware.
type MoveTargetGroupUseCase struct {
	repo          Repo
	opsRepo       OpsRepo
	projectClient ProjectClient
	logger        *slog.Logger
}

// NewMoveTargetGroupUseCase конструктор.
func NewMoveTargetGroupUseCase(
	repo Repo, opsRepo OpsRepo,
	pc ProjectClient, logger *slog.Logger,
) *MoveTargetGroupUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &MoveTargetGroupUseCase{
		repo: repo, opsRepo: opsRepo,
		projectClient: pc, logger: logger,
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

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Move TargetGroup %s -> %s", id, dst),
		&lbv1.MoveTargetGroupMetadata{
			TargetGroupId:        id,
			DestinationProjectId: dst,
		},
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build operation: %v", err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, status.Errorf(codes.Internal, "operation persist: %v", err)
	}
	srcProject := string(cur.ProjectID)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doMove(workerCtx, id, srcProject, dst)
	})
	return &op, nil
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
		kachopg.OutboxResourceTargetGroup, string(moved.ID), string(moved.ProjectID),
		kachopg.OutboxActionMoved, map[string]any{
			"id":             string(moved.ID),
			"src_project_id": srcProject,
			"dst_project_id": dstProject,
		},
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// UPDATED — для downstream watchers, не подписанных на MOVED.
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceTargetGroup, string(moved.ID), string(moved.ProjectID),
		kachopg.OutboxActionUpdated, tgOutboxPayload(moved),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// SEC-D: project-rewrite as register(dst) + unregister(src) in the SAME tx.
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
