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
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// DeleteUseCase — async Delete Listener (acceptance GWT-LST-022..LST-025).
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
//     - BYO       (BYO=true) : vpc.InternalAddressService.ClearReference(address_id).
//     Failure → outbox `nlb_listener:<id> FAILED` + ops.MarkDone(error UNAVAILABLE);
//     listener row остаётся в `status='DELETING'` для retry by `free_ip_runner`
//     (Wave 9 follow-up). Verbatim acceptance GWT-LST-024.
//  3. repo.Writer.Listeners.Delete + 2× outbox emit (`nlb_listener:<id> DELETED`
//     + `nlb_load_balancer:<lb_id> UPDATED`).
//  4. ops.MarkDone(response=Empty).
//
// BYO vs auto-alloc detection (design §4.2 / acceptance LST-022/LST-023):
// текущая schema хранит `address_id` для обоих вариантов (BYO — original id;
// auto — id новосозданного Address из AllocateExternalIP/AllocateInternalIP).
// Чтобы различить branch — храним ещё одну метку. На уровне domain.Listener
// её нет, поэтому используем эвристику: если AllocateExternalIP/InternalIP
// в Create wrote отдельный Address — он живёт в kacho-vpc с
// `used_by={nlb_listener, listener_id}`. На Delete мы спрашиваем
// vpc.AddressService.Get(address_id) → если `created_for == listener_id`
// (соблюдается atomic alloc+SetReference в InternalAddressService), значит
// auto-alloc и можно FreeIP; иначе BYO и достаточно ClearReference.
//
// Pragmatic shortcut: heuristic ниже — Address.Name префикс `nlb-listener-<short-id>`
// (детерминированный builder в acquireVIP). Это надёжный признак auto-alloc
// branch; альтернативно можно расширить domain.Listener дополнительной flag-
// колонкой `address_byo bool` — отложено в KAC-152 follow-up (требует миграции
// schema; acceptance работает и с эвристикой). См. GWT-LST-022/-023.
type DeleteUseCase struct {
	repo          RepoFactory
	opsRepo       OperationsRepo
	addresses     AddressClient
	internalAddrs InternalAddressClient
	logger        *slog.Logger
}

// NewDeleteUseCase — конструктор.
func NewDeleteUseCase(
	repo RepoFactory,
	opsRepo OperationsRepo,
	addresses AddressClient,
	internalAddrs InternalAddressClient,
	logger *slog.Logger,
) *DeleteUseCase {
	return &DeleteUseCase{
		repo:          repo,
		opsRepo:       opsRepo,
		addresses:     addresses,
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
		return nil, status.Errorf(codes.Internal, "operations.New: %v", err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, status.Errorf(codes.Internal, "ops.Create: %v", err)
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

	// Step 2: release VIP. Branch (BYO vs auto-alloc) detection — heuristic
	// on Address.Name prefix (set in acquireVIP).
	if addressID != "" {
		byo, derr := u.detectBYO(ctx, addressID, listenerID)
		if derr != nil {
			return nil, u.markFailedAndReturn(ctx, listenerID, projectID, derr)
		}
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
		// для consumers (acceptance LST-022 idempotency).
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
	// SEC-D: FGA-unregister-intent (parent-link) in the SAME tx as the Delete —
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
		return nil, status.Errorf(codes.Internal, "anypb.New Empty: %v", err)
	}
	return any, nil
}

// detectBYO — heuristic: BYO Address chose by tenant обычно не следует
// шаблону `nlb-listener-<short-id>`; auto-alloc Address всегда имеет это имя
// (см. acquireVIP). При недоступности vpc.AddressService адаптера — считаем
// branch=auto-alloc (FreeIP idempotent если address уже удалён → NotFound = ok).
//
// Returned err имеет sentinel-обёртку (domain.Err*) и мапится через
// mapDomainErr.
func (u *DeleteUseCase) detectBYO(ctx context.Context, addressID, listenerID string) (bool, error) {
	if u.addresses == nil {
		return false, nil
	}
	addr, err := u.addresses.Get(ctx, addressID)
	if err != nil {
		// NotFound → address уже удалён (предыдущий Delete partial); считаем
		// BYO=false (FreeIP вернёт NotFound idempotent).
		if errors.Is(err, domain.ErrInvalidArg) || errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	// Heuristic: auto-alloc Address всегда имеет name = `nlb-listener-<8chars>`
	// (см. CreateUseCase.acquireVIP). Если name пуст или другой формат — BYO.
	const autoPrefix = "nlb-listener-"
	if len(addr.Name) > len(autoPrefix) && addr.Name[:len(autoPrefix)] == autoPrefix {
		return false, nil
	}
	// Name пустое (vpc.AddressService.Get не вернул) → safer to treat as BYO
	// (ClearReference вместо FreeIP).
	_ = listenerID
	return true, nil
}

// releaseVIP — branch:
//
//	byo == true  → ClearReference (Address остаётся у tenant'а; LST-023).
//	byo == false → FreeIP (kacho-vpc delete Address целиком; LST-022).
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
// state — retry'ит background `free_ip_runner` (Wave 9 follow-up, GWT-LST-024).
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
