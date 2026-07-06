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
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
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
	addressClient InternalAddressClient
	logger        *slog.Logger
}

// NewDeleteLoadBalancerUseCase конструктор.
func NewDeleteLoadBalancerUseCase(repo Repo, opsRepo operations.Repo, ac InternalAddressClient, logger *slog.Logger) *DeleteLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &DeleteLoadBalancerUseCase{repo: repo, opsRepo: opsRepo, addressClient: ac, logger: logger}
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
		return nil, status.Error(codes.FailedPrecondition,
			"load balancer has deletion protection enabled")
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

	projectID := string(cur.ProjectID)
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		// Worker-ctx детачнут — восстанавливаем principal, чтобы release-вызовы в vpc
		// (ClearReference/FreeIP) несли identity тенанта (иначе authz_no_principal).
		return u.doDelete(operations.WithPrincipal(workerCtx, principal), id, projectID)
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

// doDelete — durable-handle Delete-сага (mark→release→delete):
//
//  1. MarkDeleting — атомарный guarded-переход в status=DELETING (unprotected +
//     no children) ДО необратимого release VIP. Provал guard'а → fail БЕЗ release
//     (строка цела, VIP не тронут). Пометка DELETING также делает строку видимой
//     free_ip_runner'у.
//  2. Per-family release VIP (auto → ClearReference→FreeIP, byo → ClearReference;
//     идемпотентно по address_id, NotFound=успех).
//  3. Writer-TX: DELETE строки + outbox DELETED + fga-unregister → Commit.
//
// Краш/release-сбой между 1 и 3 оставляет строку в DELETING → free_ip_runner
// доводит release+DELETE (durable-handle self-heal). FK 23503 backstop на
// финальном DELETE недостижим (mark-guard требует отсутствия детей, а
// child-INSERT'ы отвергают DELETING-родителя), но `Delete` его сохраняет как
// defense-in-depth.
func (u *DeleteLoadBalancerUseCase) doDelete(ctx context.Context, id, projectID string) (*anypb.Any, error) {
	// Шаг 1: атомарно пометить DELETING ДО release VIP.
	marked, err := u.markDeleting(ctx, id)
	if err != nil {
		return nil, err
	}
	// VIP-binding — из строки под mark-lock (устойчиво к гонке с AttachVIP).
	vip := vipBinding{
		addressIDV4: string(marked.AddressIDV4),
		addressIDV6: string(marked.AddressIDV6),
		originV4:    marked.VipOriginV4,
		originV6:    marked.VipOriginV6,
	}

	// Шаг 2: per-family release VIP (раздельно v4/v6).
	if err := u.releaseVIP(ctx, vip.addressIDV4, vip.originV4); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := u.releaseVIP(ctx, vip.addressIDV6, vip.originV6); err != nil {
		return nil, mapDomainErr(err)
	}

	// Шаг 3: финальный DELETE + outbox DELETED + fga-unregister (одна TX).
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	if err := w.LoadBalancers().Delete(ctx, id); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachorepo.OutboxResourceLoadBalancer, id, projectID,
		kachorepo.OutboxActionDeleted, map[string]any{"id": id, "project_id": projectID},
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

// markDeleting — отдельная закоммиченная Writer-TX с атомарным MarkDeleting-guard'ом
// (первый шаг Delete). DELETING обязан быть durable ДО release VIP: (а) при провале
// guard'а VIP не трогается, (б) при краше после release строка в DELETING
// самозалечивается free_ip_runner'ом. Конкурентный Update(deletion_protection=true)
// или появившийся ребёнок между sync-precheck и этим guard'ом пресекается на
// DB-уровне (0 rows → FailedPrecondition).
func (u *DeleteLoadBalancerUseCase) markDeleting(ctx context.Context, id string) (*kachorepo.LoadBalancerRecord, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()
	rec, err := w.LoadBalancers().MarkDeleting(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}
	return rec, nil
}

// releaseVIP — освобождает VIP одного семейства по address_id: owned (auto)
// → two-step ClearReference → FreeIP (иначе FreeIP==AddressService.Delete упрётся
// в собственный Delete-guard на owned-референсе); linked → ClearReference без
// Delete (tenant-адрес уцелевает). Пустой addressID → no-op. Идемпотентно
// (NotFound → успех; окно cleared-but-not-deleted добивает free_ip_runner).
func (u *DeleteLoadBalancerUseCase) releaseVIP(ctx context.Context, addressID string, origin domain.VipOrigin) error {
	if addressID == "" {
		return nil
	}
	if u.addressClient == nil {
		return status.Error(codes.Unavailable, "vpc internal address client not configured")
	}
	if origin == domain.VipOriginAuto {
		// owned: снять собственный owned-референс, затем удалить адрес.
		if err := u.addressClient.ClearReference(ctx, addressID); err != nil {
			return err
		}
		return u.addressClient.FreeIP(ctx, addressID)
	}
	// linked (tenant-owned): снять только референс.
	return u.addressClient.ClearReference(ctx, addressID)
}
