// Package loadbalancer — gRPC handler + use-cases для NetworkLoadBalancerService.
//
// evgeniy §2.B: handler — тонкий transport (parse-request → call use-case →
// format-response), бизнес-логика живёт в use-case'ах (per-RPC файлы:
// create.go / update.go / delete.go / start.go / stop.go / move.go /
// attach_target_group.go / detach_target_group.go / get_target_states.go /
// list_operations.go).
//
// Каждый use-case принимает domain-тип (или ResourceID), репозиторий через
// CQRS Repository interface (Reader/Writer split), peer-clients через
// port-интерфейсы (ports.go), opsRepo для async LRO.
//
// Composition root — `cmd/kacho-loadbalancer/main.go`: pgxpool → kachopg.New →
// peer-clients → NewHandler(...) → publicSrv.Register.
package loadbalancer

import (
	"context"
	"log/slog"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
)

// Handler реализует lbv1.NetworkLoadBalancerServiceServer.
//
// Handler владеет per-RPC use-case'ами; sample конструктор `NewHandler`
// собирает все use-case'ы из общего набора зависимостей (Repo / opsRepo /
// peers / logger). Декомпозиция per-RPC файл — для тестируемости и
// читаемости (evgeniy §2.B / §7).
type Handler struct {
	lbv1.UnimplementedNetworkLoadBalancerServiceServer

	get             *GetLoadBalancerUseCase
	list            *ListLoadBalancersUseCase
	create          *CreateLoadBalancerUseCase
	update          *UpdateLoadBalancerUseCase
	deleteUC        *DeleteLoadBalancerUseCase
	start           *StartLoadBalancerUseCase
	stop            *StopLoadBalancerUseCase
	move            *MoveLoadBalancerUseCase
	attachTG        *AttachTargetGroupUseCase
	detachTG        *DetachTargetGroupUseCase
	getTargetStates *GetTargetStatesUseCase
	listOps         *ListOperationsUseCase
}

// NewHandler собирает Handler с одним набором common-зависимостей. Composition
// root вызывает это один раз и регистрирует на publicSrv.
//
// peerProject / peerRegion могут быть nil (dev-mode без peer-сервисов) —
// use-case'ы при отсутствующем peer'е fail-close с Unavailable / "<peer> not
// configured". FGA owner-tuple-регистрация — через SEC-D transactional-outbox
// (register-drainer → IAM), не через peer-client здесь.
func NewHandler(
	repo Repo,
	opsRepo operations.Repo,
	peerProject ProjectClient,
	peerRegion RegionClient,
	logger *slog.Logger,
) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		get:             NewGetLoadBalancerUseCase(repo),
		list:            NewListLoadBalancersUseCase(repo),
		create:          NewCreateLoadBalancerUseCase(repo, opsRepo, peerProject, peerRegion, logger),
		update:          NewUpdateLoadBalancerUseCase(repo, opsRepo, logger),
		deleteUC:        NewDeleteLoadBalancerUseCase(repo, opsRepo, logger),
		start:           NewStartLoadBalancerUseCase(repo, opsRepo, logger),
		stop:            NewStopLoadBalancerUseCase(repo, opsRepo, logger),
		move:            NewMoveLoadBalancerUseCase(repo, opsRepo, peerProject, logger),
		attachTG:        NewAttachTargetGroupUseCase(repo, opsRepo, logger),
		detachTG:        NewDetachTargetGroupUseCase(repo, opsRepo, logger),
		getTargetStates: NewGetTargetStatesUseCase(repo),
		listOps:         NewListOperationsUseCase(opsRepo),
	}
}

// ---- 4 read RPCs (sync) ----------------------------------------------------

func (h *Handler) Get(ctx context.Context, req *lbv1.GetNetworkLoadBalancerRequest) (*lbv1.NetworkLoadBalancer, error) {
	return h.get.Execute(ctx, req)
}

func (h *Handler) List(ctx context.Context, req *lbv1.ListNetworkLoadBalancersRequest) (*lbv1.ListNetworkLoadBalancersResponse, error) {
	return h.list.Execute(ctx, req)
}

func (h *Handler) GetTargetStates(ctx context.Context, req *lbv1.GetTargetStatesRequest) (*lbv1.GetTargetStatesResponse, error) {
	return h.getTargetStates.Execute(ctx, req)
}

func (h *Handler) ListOperations(ctx context.Context, req *lbv1.ListNetworkLoadBalancerOperationsRequest) (*lbv1.ListNetworkLoadBalancerOperationsResponse, error) {
	return h.listOps.Execute(ctx, req)
}

// ---- 8 mutating RPCs (async; return Operation) -----------------------------

func (h *Handler) Create(ctx context.Context, req *lbv1.CreateNetworkLoadBalancerRequest) (*operationpb.Operation, error) {
	op, err := h.create.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) Update(ctx context.Context, req *lbv1.UpdateNetworkLoadBalancerRequest) (*operationpb.Operation, error) {
	op, err := h.update.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) Delete(ctx context.Context, req *lbv1.DeleteNetworkLoadBalancerRequest) (*operationpb.Operation, error) {
	op, err := h.deleteUC.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) Start(ctx context.Context, req *lbv1.StartNetworkLoadBalancerRequest) (*operationpb.Operation, error) {
	op, err := h.start.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) Stop(ctx context.Context, req *lbv1.StopNetworkLoadBalancerRequest) (*operationpb.Operation, error) {
	op, err := h.stop.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) Move(ctx context.Context, req *lbv1.MoveNetworkLoadBalancerRequest) (*operationpb.Operation, error) {
	op, err := h.move.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) AttachTargetGroup(ctx context.Context, req *lbv1.AttachNetworkLoadBalancerTargetGroupRequest) (*operationpb.Operation, error) {
	op, err := h.attachTG.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) DetachTargetGroup(ctx context.Context, req *lbv1.DetachNetworkLoadBalancerTargetGroupRequest) (*operationpb.Operation, error) {
	op, err := h.detachTG.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}
