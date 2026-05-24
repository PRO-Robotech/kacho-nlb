package loadbalancer

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

	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// DeleteLoadBalancerUseCase — sync precheck + async delete (acceptance
// GWT-NLB-015..GWT-NLB-019).
//
// Sync prechecks (design §5.6):
//   - lb.DeletionProtection=true → FailedPrecondition (verbatim text);
//   - HasListeners > 0           → FailedPrecondition "has N listener(s); delete first";
//   - HasAttachedTargetGroups>0  → FailedPrecondition "has attached target group(s); detach first".
//
// Worker: Writer-TX → Delete (FK 23503 backstop → ErrFailedPrecondition) +
// outbox-emit DELETED → Commit. Response = google.protobuf.Empty.
type DeleteLoadBalancerUseCase struct {
	repo    Repo
	opsRepo operations.Repo
	logger  *slog.Logger
}

// NewDeleteLoadBalancerUseCase конструктор.
func NewDeleteLoadBalancerUseCase(repo Repo, opsRepo operations.Repo, logger *slog.Logger) *DeleteLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeleteLoadBalancerUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// Execute — sync prechecks + ops insert + spawn worker.
func (u *DeleteLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.DeleteNetworkLoadBalancerRequest,
) (*operations.Operation, error) {
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}

	// Sync prechecks (read reader-TX).
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	cur, err := rd.LoadBalancers().Get(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if cur.DeletionProtection {
		return nil, status.Errorf(codes.FailedPrecondition,
			"NetworkLoadBalancer %s has deletion_protection enabled", id)
	}
	hasListeners, err := rd.LoadBalancers().HasListeners(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if hasListeners {
		// Count via List (page size 1) → just say "has listener(s)".
		listeners, _, err := rd.Listeners().ListByLB(ctx, id, kachorepo.Pagination{PageSize: 1000})
		if err != nil {
			return nil, mapDomainErr(err)
		}
		return nil, status.Errorf(codes.FailedPrecondition,
			"NetworkLoadBalancer %s has %d listener(s); delete first", id, len(listeners))
	}
	hasTG, err := rd.LoadBalancers().HasAttachedTargetGroups(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if hasTG {
		return nil, status.Errorf(codes.FailedPrecondition,
			"NetworkLoadBalancer %s has attached target group(s); detach first", id)
	}

	// Operation row.
	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Delete NetworkLoadBalancer %s", id),
		&lbv1.DeleteNetworkLoadBalancerMetadata{NetworkLoadBalancerId: id},
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

// doDelete — worker: open Writer → Delete + outbox DELETED → Commit. FK 23503
// backstop → ErrFailedPrecondition (TOCTOU: листенер появился между sync-check
// и worker-delete).
func (u *DeleteLoadBalancerUseCase) doDelete(ctx context.Context, id, projectID string) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	if err := w.LoadBalancers().Delete(ctx, id); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, id, projectID,
		kachopg.OutboxActionDeleted, map[string]any{"id": id, "project_id": projectID},
	); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	out, err := anypb.New(&emptypb.Empty{})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal response: %v", err)
	}
	return out, nil
}
