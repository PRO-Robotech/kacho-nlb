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
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// MoveLoadBalancerUseCase — change project_id (cross-project) keeping region.
// Sync prechecks (design §4.7):
//   - same-project — InvalidArgument "destination project is the same as source";
//   - destination project exists (peer ProjectClient.Get);
//   - LB has no attached target groups (FGA / data-model constraint: cross-project
//     TG attach запрещён — Move заблокирован если есть).
//
// Worker: Writer-TX → repo.MoveProject (UPDATE LB + cascade UPDATE listeners) +
// outbox MOVED + FGA-register(dst project) + FGA-unregister(src project) → Commit
// (SEC-D Вариант A: project-rewrite = register new-project tuple + unregister
// old-project tuple, both in the same writer-tx as MoveProject — no dual-write).
//
// Acceptance: GWT-NLB-026..GWT-NLB-031.
type MoveLoadBalancerUseCase struct {
	repo          Repo
	opsRepo       operations.Repo
	projectClient ProjectClient
	logger        *slog.Logger
}

// NewMoveLoadBalancerUseCase конструктор.
func NewMoveLoadBalancerUseCase(
	repo Repo, opsRepo operations.Repo,
	pc ProjectClient, logger *slog.Logger,
) *MoveLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &MoveLoadBalancerUseCase{
		repo: repo, opsRepo: opsRepo,
		projectClient: pc, logger: logger,
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

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Move NetworkLoadBalancer %s → %s", id, dst),
		&lbv1.MoveNetworkLoadBalancerMetadata{
			NetworkLoadBalancerId: id,
			DestinationProjectId:  dst,
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
		kachopg.OutboxResourceLoadBalancer, string(moved.ID), string(moved.ProjectID),
		kachopg.OutboxActionMoved, map[string]any{
			"id":             string(moved.ID),
			"src_project_id": srcProject,
			"dst_project_id": dstProject,
		},
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// Also emit UPDATED for downstream watchers that don't subscribe to MOVED.
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, string(moved.ID), string(moved.ProjectID),
		kachopg.OutboxActionUpdated, lbOutboxPayload(moved),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// SEC-D: project-rewrite as register(dst) + unregister(src) in the SAME tx.
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
		return nil, status.Errorf(codes.Internal, "marshal response: %v", err)
	}
	return out, nil
}
