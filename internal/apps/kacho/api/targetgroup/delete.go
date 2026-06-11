package targetgroup

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// DeleteTargetGroupUseCase — sync precheck + async delete
// (acceptance GWT-TGR-021..024).
//
// Sync prechecks (design §3.4):
//   - HasAttachedLB > 0 → FailedPrecondition verbatim
//     `"TargetGroup is attached to N load balancer(s); detach first"`.
//   - ListTargets count > 0 → FailedPrecondition verbatim
//     `"TargetGroup has N target(s); remove them first via RemoveTargets"`.
//
// Worker (TOCTOU backstop):
//   - Writer-TX → Delete (FK 23503 от child rows → ErrFailedPrecondition) +
//     outbox DELETED → Commit.
//   - GWT-TGR-024: concurrent AddTargets между sync precheck и worker DELETE —
//     SQL 23503 ловится mapPgErr → FailedPrecondition.
type DeleteTargetGroupUseCase struct {
	repo    Repo
	opsRepo OpsRepo
	logger  *slog.Logger
}

// NewDeleteTargetGroupUseCase конструктор.
func NewDeleteTargetGroupUseCase(repo Repo, opsRepo OpsRepo, logger *slog.Logger) *DeleteTargetGroupUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeleteTargetGroupUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// Execute — sync prechecks + ops insert + spawn worker.
func (u *DeleteTargetGroupUseCase) Execute(
	ctx context.Context, req *lbv1.DeleteTargetGroupRequest,
) (*operations.Operation, error) {
	id := req.GetTargetGroupId()
	if id == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}

	// Sync prechecks via reader-TX.
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	cur, err := rd.TargetGroups().Get(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}

	hasLB, err := rd.TargetGroups().HasAttachedLB(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if hasLB {
		// Точный count: ListByTG в AttachedTargetGroups.
		atgs, lerr := rd.AttachedTargetGroups().ListByTG(ctx, id)
		if lerr != nil {
			return nil, mapDomainErr(lerr)
		}
		return nil, status.Errorf(codes.FailedPrecondition,
			"TargetGroup is attached to %d load balancer(s); detach first", len(atgs))
	}

	targets, err := rd.TargetGroups().ListTargets(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if len(targets) > 0 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"TargetGroup has %d target(s); remove them first via RemoveTargets", len(targets))
	}

	// Operation row.
	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Delete TargetGroup %s", id),
		&lbv1.DeleteTargetGroupMetadata{TargetGroupId: id},
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build operation: %v", err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, status.Errorf(codes.Internal, "operation persist: %v", err)
	}

	projectID := string(cur.ProjectID)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doDelete(workerCtx, id, projectID)
	})
	return &op, nil
}

// doDelete — worker: Writer-TX → Delete + outbox DELETED → Commit.
// FK 23503 (concurrent AddTargets or attach) → ErrFailedPrecondition fallback.
func (u *DeleteTargetGroupUseCase) doDelete(ctx context.Context, id, projectID string) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	if err := w.TargetGroups().Delete(ctx, id); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceTargetGroup, id, projectID,
		kachopg.OutboxActionDeleted, map[string]any{"id": id, "project_id": projectID},
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// SEC-D: FGA-unregister-intent (project-hierarchy) in the SAME tx as Delete.
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventUnregister,
		tgUnregisterIntent(id, projectID)); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	return anypb.New(&emptypb.Empty{})
}
