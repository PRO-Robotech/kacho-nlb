// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
	"errors"
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
)

// DeleteUseCase — async Delete Listener.
//
// Sync (handler-thread):
//  1. listener_id required.
//  2. Listener.Get — NotFound иначе.
//  3. opsRepo.CreateWithPrincipal + operations.Run.
//
// Async worker:
//  1. Listener.SetStatusCAS(<current> → DELETING) — атомарный transient marker;
//     parallel UPDATE/DELETE losses race fast.
//  2. Free VIP branch:
//     - auto-alloc (BYO=false): vpc.InternalAddressService.FreeIP(address_id).
//     - BYO       (BYO=true): vpc.InternalAddressService.ClearReference(address_id).
//     Failure → outbox `nlb_listener:<id> FAILED` + ops.MarkDone(error UNAVAILABLE);
//     listener row остаётся в `status='DELETING'` — background `free_ip_runner`
//     реконсилирует её (release-by-address + delete + finalize) на следующем тике.
//  3. repo.Writer.Listeners.Delete + 2× outbox emit (`nlb_listener:<id> DELETED`
//     + `nlb_load_balancer:<lb_id> UPDATED`).
//  4. ops.MarkDone(response=Empty).
//
// BYO vs auto-alloc detection:
// release-ветка выбирается дискриминатором `listeners.vip_origin`, который
// проставляется на Create (auto-alloc → 'auto', переданный tenant'ом address_id
// → 'byo'). Имя Address для решения НЕ используется: tenant волен назвать свой
// статический адрес как угодно (в т.ч. совпав с auto-паттерном), а ошибочный
// выбор FreeIP по имени удалил бы чужой адрес (data-loss). Источник истины —
// колонка, прочитанная вместе с листенером.
type DeleteUseCase struct {
	repo          RepoFactory
	opsRepo       OperationsRepo
	internalAddrs InternalAddressClient
	logger        *slog.Logger
}

// NewDeleteUseCase — конструктор.
func NewDeleteUseCase(
	repo RepoFactory,
	opsRepo OperationsRepo,
	internalAddrs InternalAddressClient,
	logger *slog.Logger,
) *DeleteUseCase {
	return &DeleteUseCase{
		repo:          repo,
		opsRepo:       opsRepo,
		internalAddrs: internalAddrs,
		logger:        logger,
	}
}

// Run — sync validate + spawn worker.
func (u *DeleteUseCase) Run(ctx context.Context, req *lbv1.DeleteListenerRequest) (*operations.Operation, error) {
	id := req.GetListenerId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "listener_id required")
	}
	if err := validateListenerID(id); err != nil {
		return nil, err
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	cur, err := rd.Listeners().Get(ctx, id)
	if err != nil {
		_ = rd.Close()
		return nil, mapDomainErr(err)
	}
	_ = rd.Close()

	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Delete listener %s", string(cur.Name)),
		&lbv1.DeleteListenerMetadata{
			ListenerId:     string(cur.ID),
			LoadBalancerId: string(cur.LoadBalancerID),
		},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}

	snap := *cur
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doDelete(workerCtx, &snap)
	})
	return &op, nil
}

// doDelete — worker flow.
func (u *DeleteUseCase) doDelete(ctx context.Context, cur *kachorepo.ListenerRecord) (*anypb.Any, error) {
	listenerID := string(cur.ID)
	lbID := string(cur.LoadBalancerID)
	projectID := string(cur.ProjectID)
	regionID := string(cur.RegionID)
	addressID := ""
	if v, ok := cur.AddressID.Maybe(); ok {
		addressID = string(v)
	}

	// Step 1: mark DELETING (atomic CAS — protects against parallel writers).
	// We accept any non-DELETING current status. Mutex (CAS-style) writes
	// `status='DELETING'` если был ACTIVE / CREATING / UPDATING; если уже
	// DELETING — повторный Delete-worker идемпотентно проходит дальше.
	if cur.Status != domain.ListenerStatusDeleting {
		w, err := u.repo.Writer(ctx)
		if err != nil {
			return nil, mapDomainErr(err)
		}
		committed := false
		defer func() {
			if !committed {
				w.Abort()
			}
		}()
		_, err = w.Listeners().SetStatusCAS(ctx, listenerID, cur.Status, domain.ListenerStatusDeleting)
		if err != nil {
			// CAS-miss (e.g. parallel writer already moved to DELETING) →
			// proceed anyway (read current status); else propagate.
			if !errors.Is(err, domain.ErrFailedPrecondition) {
				return nil, mapDomainErr(err)
			}
		}
		if err := w.Outbox().Emit(ctx,
			outboxResourceTypeListener, listenerID, projectID,
			outboxActionUpdated, listenerPayloadMap(cur),
		); err != nil {
			return nil, mapDomainErr(fmt.Errorf("%w: outbox emit listener UPDATED: %v", domain.ErrInternal, err))
		}
		if err := w.Commit(); err != nil {
			return nil, mapDomainErr(err)
		}
		committed = true
	}

	// Step 2: release VIP. Release-ветка выбирается дискриминатором vip_origin
	// (auto → FreeIP, byo → ClearReference), прочитанным вместе с листенером —
	// без обращения к vpc за именем Address (anti data-loss).
	if addressID != "" {
		byo := cur.VipOrigin == domain.VipOriginBYO
		if err := u.releaseVIP(ctx, addressID, listenerID, byo); err != nil {
			return nil, u.markFailedAndReturn(ctx, listenerID, projectID, err)
		}
	}

	// Step 3: DELETE listener row + 2× outbox emit + Commit atomically.
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	committed := false
	defer func() {
		if !committed {
			w.Abort()
		}
	}()
	if err := w.Listeners().Delete(ctx, listenerID); err != nil {
		// ErrNotFound — idempotent (двойной Delete): продолжаем, emit DELETED
		// для consumers (idempotency).
		if !errors.Is(err, domain.ErrNotFound) {
			return nil, mapDomainErr(err)
		}
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeListener, listenerID, projectID,
		outboxActionDeleted, listenerPayloadMap(cur),
	); err != nil {
		return nil, mapDomainErr(fmt.Errorf("%w: outbox emit listener DELETED: %v", domain.ErrInternal, err))
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeLoadBalancer, lbID, projectID,
		outboxActionUpdated, lbUpdatedPayloadMap(lbID, projectID, regionID, "listener_deleted"),
	); err != nil {
		return nil, mapDomainErr(fmt.Errorf("%w: outbox emit lb UPDATED: %v", domain.ErrInternal, err))
	}
	// FGA-unregister-intent (parent-link) in the SAME tx as the Delete —
	// register-drainer removes the parent-link tuple via IAM.UnregisterResource.
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventUnregister,
		listenerUnregisterIntent(listenerID, lbID)); err != nil {
		return nil, mapDomainErr(fmt.Errorf("%w: fga unregister-intent emit: %v", domain.ErrInternal, err))
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}
	committed = true

	any, err := anypb.New(&emptypb.Empty{})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return any, nil
}

// releaseVIP — branch:
//
//	byo == true  → ClearReference (Address остаётся у tenant'а).
//	byo == false → FreeIP (kacho-vpc delete Address целиком).
//
// Failure мапится в gRPC через mapDomainErr. NotFound → idempotent ok.
func (u *DeleteUseCase) releaseVIP(ctx context.Context, addressID, listenerID string, byo bool) error {
	if u.internalAddrs == nil {
		return status.Error(codes.Unavailable, "vpc internal-address client not configured")
	}
	owner := addressOwner(listenerID)
	if byo {
		return u.internalAddrs.ClearReference(ctx, addressID, owner)
	}
	return u.internalAddrs.FreeIP(ctx, addressID, owner)
}

// markFailedAndReturn — best-effort outbox emit `nlb_listener:<id> FAILED`
// + return wrapped error для ops.MarkError. listener row остаётся в DELETING
// state — её реконсилирует background `free_ip_runner` на следующем тике.
func (u *DeleteUseCase) markFailedAndReturn(ctx context.Context, listenerID, projectID string, original error) error {
	w, err := u.repo.Writer(ctx)
	if err == nil {
		_ = w.Outbox().Emit(ctx,
			outboxResourceTypeListener, listenerID, projectID,
			outboxActionFailed, map[string]any{
				"id":         listenerID,
				"project_id": projectID,
				"reason":     "release_vip_failed",
				"error":      original.Error(),
			},
		)
		_ = w.Commit()
	}
	loggerOrDiscard(u.logger).Warn("listener.Delete release VIP failed; listener kept in DELETING",
		"listener_id", listenerID, "err", original)
	return mapDomainErr(original)
}

// _ — sentinel-use: подавление unused-import предупреждения в редких
// перекомпонованных билдах. errors импортируется в логике выше.
var _ = errors.Is
