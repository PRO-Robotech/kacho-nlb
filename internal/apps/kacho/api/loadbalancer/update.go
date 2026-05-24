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

// UpdateLoadBalancerUseCase — UpdateMask discipline + async update.
// Mutable: name / description / labels / deletion_protection.
// Immutable: type / region_id / project_id (in mask → InvalidArgument).
// allow_zonal_shift (proto field) — пока не хранится в domain (reserved для
// будущего toggle); если попало в mask — silent-accept без эффекта.
//
// Acceptance: GWT-NLB-011..GWT-NLB-014.
type UpdateLoadBalancerUseCase struct {
	repo    Repo
	opsRepo operations.Repo
	logger  *slog.Logger
}

// NewUpdateLoadBalancerUseCase конструктор.
func NewUpdateLoadBalancerUseCase(repo Repo, opsRepo operations.Repo, logger *slog.Logger) *UpdateLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &UpdateLoadBalancerUseCase{repo: repo, opsRepo: opsRepo, logger: logger}
}

// knownUpdateFields — допустимый whitelist для update_mask. Поле, отсутствующее
// здесь, в mask → InvalidArgument "unknown field".
var knownUpdateFields = map[string]bool{
	"name":                true,
	"description":         true,
	"labels":              true,
	"deletion_protection": true,
	"allow_zonal_shift":   true, // silent-accept (no domain effect — reserved).
}

// immutableUpdateFields — hard-immutable; в mask → InvalidArgument verbatim.
var immutableUpdateFields = map[string]string{
	"type":       "type is immutable after NetworkLoadBalancer.Create",
	"region_id":  "region_id is immutable after NetworkLoadBalancer.Create",
	"project_id": "project_id is immutable; use NetworkLoadBalancerService.Move",
}

// Execute — sync mask validation + read existing → apply diff → ops insert →
// spawn worker.
func (u *UpdateLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.UpdateNetworkLoadBalancerRequest,
) (*operations.Operation, error) {
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}

	mask := req.GetUpdateMask().GetPaths()
	for _, p := range mask {
		if msg, ok := immutableUpdateFields[p]; ok {
			return nil, status.Errorf(codes.InvalidArgument, "%s", msg)
		}
		if !knownUpdateFields[p] {
			return nil, status.Errorf(codes.InvalidArgument, "unknown update_mask field: %s", p)
		}
	}

	// Read current state.
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := rd.LoadBalancers().Get(ctx, id)
	_ = rd.Close()
	if err != nil {
		return nil, mapDomainErr(err)
	}

	// Apply mask (mask-empty → full PATCH applying all mutable fields from req,
	// silent-ignoring immutable).
	updated := applyUpdateMask(cur.LoadBalancer, req, mask)
	if err := updated.Validate(); err != nil {
		return nil, mapDomainErr(err)
	}

	// Operation row.
	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Update NetworkLoadBalancer %s", id),
		&lbv1.UpdateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: id},
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "build operation: %v", err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, status.Errorf(codes.Internal, "operation persist: %v", err)
	}

	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doUpdate(workerCtx, updated)
	})

	return &op, nil
}

// doUpdate — worker: open Writer → Update + outbox UPDATED → Commit.
func (u *UpdateLoadBalancerUseCase) doUpdate(ctx context.Context, lb domain.LoadBalancer) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	updated, err := w.LoadBalancers().Update(ctx, &lb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, string(updated.ID), string(updated.ProjectID),
		kachopg.OutboxActionUpdated, lbOutboxPayload(updated),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	pb, err := lbRecordToProto(updated)
	if err != nil {
		return nil, err
	}
	out, err := anypb.New(pb)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal response: %v", err)
	}
	return out, nil
}

// applyUpdateMask — наложить mask на текущий LB. Empty mask → full PATCH:
// mutable полностью перезаписываются из req; immutable silent-ignored
// (verbatim YC).
func applyUpdateMask(
	cur domain.LoadBalancer, req *lbv1.UpdateNetworkLoadBalancerRequest, mask []string,
) domain.LoadBalancer {
	apply := func(field string) bool {
		if len(mask) == 0 {
			return true
		}
		for _, p := range mask {
			if p == field {
				return true
			}
		}
		return false
	}
	out := cur
	if apply("name") {
		out.Name = domain.LbName(req.GetName())
	}
	if apply("description") {
		out.Description = domain.LbDescription(req.GetDescription())
	}
	if apply("labels") {
		out.Labels = domain.LabelsFromMap(req.GetLabels())
	}
	if apply("deletion_protection") {
		out.DeletionProtection = req.GetDeletionProtection()
	}
	// allow_zonal_shift — silent-accept (no-op в domain).
	return out
}
