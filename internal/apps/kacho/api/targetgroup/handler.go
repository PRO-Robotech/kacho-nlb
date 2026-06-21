// Package targetgroup — gRPC handler + use-cases для TargetGroupService.
//
// 9 RPCs (design §3.4, acceptance §5 + §6):
//   - Get / List / ListOperations    — sync read.
//   - Create / Update / Delete / Move — async (Operation envelope).
//   - AddTargets / RemoveTargets      — async (Operation envelope).
//
// Архитектура (workspace CLAUDE.md «Чистая архитектура»):
//   - handler.go — thin transport: parse → use-case → format-response.
//   - <verb>.go  — per-RPC use-case (бизнес-логика, port-deps только).
//   - ports.go   — port-интерфейсы для composition root.
//   - mapping.go — domain↔proto / sentinel→gRPC code transfer.
//   - helpers.go — общие mapping-утилы (HC, target, outbox-payload).
//
// Composition root — `cmd/kacho-loadbalancer/main.go`: pgxpool → kachopg.New →
// peer-clients → NewHandler(...) → publicSrv.Register.
package targetgroup

import (
	"context"
	"log/slog"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
)

// Handler реализует lbv1.TargetGroupServiceServer. Per-RPC файлы в этом же
// пакете (создан тонкий transport — handler делегирует в use-case'ы).
type Handler struct {
	lbv1.UnimplementedTargetGroupServiceServer

	get           *GetTargetGroupUseCase
	list          *ListTargetGroupsUseCase
	create        *CreateTargetGroupUseCase
	update        *UpdateTargetGroupUseCase
	deleteUC      *DeleteTargetGroupUseCase
	move          *MoveTargetGroupUseCase
	addTargets    *AddTargetsUseCase
	removeTargets *RemoveTargetsUseCase
	listOps       *ListOperationsUseCase
}

// NewHandler собирает Handler из общего набора зависимостей.
//
// peer*-аргументы могут быть nil (dev-mode без peer-сервисов) — use-case'ы
// fail-close с Unavailable / "<peer> not configured" на runtime'е.
func NewHandler(
	repo Repo,
	opsRepo OpsRepo,
	peerProject ProjectClient,
	peerRegion RegionClient,
	peerInstance InstanceClient,
	peerNIC NetworkInterfaceClient,
	peerSubnet SubnetClient,
	listFilter authzfilter.Filter,
	logger *slog.Logger,
) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		get:           NewGetTargetGroupUseCase(repo),
		list:          NewListTargetGroupsUseCase(repo, listFilter),
		create:        NewCreateTargetGroupUseCase(repo, opsRepo, peerProject, peerRegion, logger),
		update:        NewUpdateTargetGroupUseCase(repo, opsRepo, logger),
		deleteUC:      NewDeleteTargetGroupUseCase(repo, opsRepo, logger),
		move:          NewMoveTargetGroupUseCase(repo, opsRepo, peerProject, logger),
		addTargets:    NewAddTargetsUseCase(repo, opsRepo, peerInstance, peerNIC, peerSubnet, logger),
		removeTargets: NewRemoveTargetsUseCase(repo, opsRepo, logger),
		listOps:       NewListOperationsUseCase(opsRepo),
	}
}

// ---- 3 sync read RPCs ------------------------------------------------------

func (h *Handler) Get(ctx context.Context, req *lbv1.GetTargetGroupRequest) (*lbv1.TargetGroup, error) {
	return h.get.Execute(ctx, req)
}

func (h *Handler) List(ctx context.Context, req *lbv1.ListTargetGroupsRequest) (*lbv1.ListTargetGroupsResponse, error) {
	return h.list.Execute(ctx, req)
}

func (h *Handler) ListOperations(ctx context.Context, req *lbv1.ListTargetGroupOperationsRequest) (*lbv1.ListTargetGroupOperationsResponse, error) {
	return h.listOps.Execute(ctx, req)
}

// ---- 6 async mutating RPCs (return Operation) ------------------------------

func (h *Handler) Create(ctx context.Context, req *lbv1.CreateTargetGroupRequest) (*operationpb.Operation, error) {
	op, err := h.create.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) Update(ctx context.Context, req *lbv1.UpdateTargetGroupRequest) (*operationpb.Operation, error) {
	op, err := h.update.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) Delete(ctx context.Context, req *lbv1.DeleteTargetGroupRequest) (*operationpb.Operation, error) {
	op, err := h.deleteUC.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) Move(ctx context.Context, req *lbv1.MoveTargetGroupRequest) (*operationpb.Operation, error) {
	op, err := h.move.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) AddTargets(ctx context.Context, req *lbv1.AddTargetsRequest) (*operationpb.Operation, error) {
	op, err := h.addTargets.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

func (h *Handler) RemoveTargets(ctx context.Context, req *lbv1.RemoveTargetsRequest) (*operationpb.Operation, error) {
	op, err := h.removeTargets.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}
