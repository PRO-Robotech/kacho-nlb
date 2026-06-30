// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// lbUnregisterIntent builds the FGA-unregister-intent (project-hierarchy)
// for a deleted LoadBalancer. The creator tuple is left for IAM-side GC
// (unregister project-hierarchy/parent-link).
func lbUnregisterIntent(id, projectID string) domain.FGARegisterIntent {
	return domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: id,
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, id, projectID),
		},
	}
}

// DeleteLoadBalancerUseCase — sync precheck + async delete.
//
// Sync prechecks:
//   - lb.DeletionProtection=true → FailedPrecondition (фиксированный текст);
//   - HasListeners > 0           → FailedPrecondition "has N listener(s); delete first";
//   - HasAttachedTargetGroups>0  → FailedPrecondition "has attached target group(s); detach first".
//
// Worker: Writer-TX → Delete (FK 23503 backstop → ErrFailedPrecondition) +
// outbox-emit DELETED → Commit. Response = google.protobuf.Empty.
type DeleteLoadBalancerUseCase struct {
	repo          Repo
	opsRepo       operations.Repo
	anycastClient AnycastAddressClient
	logger        *slog.Logger
}

// NewDeleteLoadBalancerUseCase конструктор.
func NewDeleteLoadBalancerUseCase(repo Repo, opsRepo operations.Repo, ac AnycastAddressClient, logger *slog.Logger) *DeleteLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeleteLoadBalancerUseCase{repo: repo, opsRepo: opsRepo, anycastClient: ac, logger: logger}
}

// Execute — sync prechecks + ops insert + spawn worker.
func (u *DeleteLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.DeleteNetworkLoadBalancerRequest,
) (*operations.Operation, error) {
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(id); err != nil {
		return nil, err
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
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}

	snap := vipBinding{
		addressIDV4: string(cur.AddressIDV4),
		addressIDV6: string(cur.AddressIDV6),
		originV4:    cur.VipOriginV4,
		originV6:    cur.VipOriginV6,
	}
	projectID := string(cur.ProjectID)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doDelete(workerCtx, id, projectID, snap)
	})

	return &op, nil
}

// vipBinding — per-family VIP-привязка LB (release-вход для Delete).
type vipBinding struct {
	addressIDV4 string
	addressIDV6 string
	originV4    domain.VipOrigin
	originV6    domain.VipOrigin
}

// doDelete — worker: per-family release VIP (auto → FreeIP, byo → ClearReference;
// идемпотентно по address_id) → Writer-TX: Delete + outbox DELETED + fga-unregister
// → Commit. release-сбой (vpc Unavailable) → fail-closed error, строка остаётся
// (повторный Delete идемпотентен — release по address_id трактует NotFound как успех).
// FK 23503 backstop → ErrFailedPrecondition (листенер появился между sync-check и delete).
func (u *DeleteLoadBalancerUseCase) doDelete(ctx context.Context, id, projectID string, vip vipBinding) (*anypb.Any, error) {
	// Per-family release VIP до удаления строки (раздельно v4/v6).
	if err := u.releaseVIP(ctx, id, vip.addressIDV4, vip.originV4); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := u.releaseVIP(ctx, id, vip.addressIDV6, vip.originV6); err != nil {
		return nil, mapDomainErr(err)
	}

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
	// FGA-unregister-intent (project-hierarchy) in the SAME tx as the
	// Delete — drainer applies UnregisterResource to remove the owner-tuple.
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventUnregister,
		lbUnregisterIntent(id, projectID)); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	out, err := anypb.New(&emptypb.Empty{})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return out, nil
}

// releaseVIP — освобождает VIP одного семейства по address_id: auto → FreeIP
// (Address удаляется), byo → ClearReference (Address tenant'а уцелел). Пустой
// addressID → no-op (семейство без VIP). Идемпотентно (NotFound → успех).
func (u *DeleteLoadBalancerUseCase) releaseVIP(ctx context.Context, lbID, addressID string, origin domain.VipOrigin) error {
	if addressID == "" {
		return nil
	}
	if u.anycastClient == nil {
		return status.Error(codes.Unavailable, "vpc anycast address client not configured")
	}
	owner := lbAddressOwner(lbID)
	if origin == domain.VipOriginBYO {
		return u.anycastClient.ClearReference(ctx, addressID, owner)
	}
	return u.anycastClient.FreeIP(ctx, addressID, owner)
}
