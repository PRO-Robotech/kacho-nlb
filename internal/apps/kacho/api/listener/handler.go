// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
	"log/slog"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
)

// Handler реализует kacho.cloud.loadbalancer.v1.ListenerServiceServer.
//
// Thin transport: parse req → call UseCase → format proto resp.
// No business logic, no validation beyond `id is required` (the latter is
// purely defensive — handler-level early-exit; UseCase repeats the check).
//
// Per-resource FGA Check выполняется до handler'а — interceptor в api-gateway;
// handler сам Check не зовёт.
type Handler struct {
	lbv1.UnimplementedListenerServiceServer

	get            *GetUseCase
	list           *ListUseCase
	create         *CreateUseCase
	update         *UpdateUseCase
	deleteUC       *DeleteUseCase
	listOperations *ListOperationsUseCase
}

// NewHandler — composition-root constructor.
//
// Все adapters передаются через port-интерфейсы (`internal/clients/*`,
// `internal/repo/kacho.Repository`). nil-зависимость logger допускается — Create/
// Delete UseCase это переживают (см. helpers.loggerOrDiscard). FGA owner/parent-
// link tuple-регистрация — через outbox (FGARegisterOutbox в writer-tx).
func NewHandler(
	repo RepoFactory,
	opsRepo OperationsRepo,
	internalAddrs InternalAddressClient,
	listFilter authzfilter.Filter,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		get:            NewGetUseCase(repo),
		list:           NewListUseCase(repo, listFilter),
		create:         NewCreateUseCase(repo, opsRepo, logger),
		update:         NewUpdateUseCase(repo, opsRepo, logger),
		deleteUC:       NewDeleteUseCase(repo, opsRepo, internalAddrs, logger),
		listOperations: NewListOperationsUseCase(opsRepo),
	}
}

// Get — sync read.
func (h *Handler) Get(ctx context.Context, req *lbv1.GetListenerRequest) (*lbv1.Listener, error) {
	return h.get.Run(ctx, req)
}

// List — sync paginated list filtered by load_balancer_id.
func (h *Handler) List(ctx context.Context, req *lbv1.ListListenersRequest) (*lbv1.ListListenersResponse, error) {
	return h.list.Run(ctx, req)
}

// Create — async; VIP allocation in worker.
func (h *Handler) Create(ctx context.Context, req *lbv1.CreateListenerRequest) (*operationpb.Operation, error) {
	op, err := h.create.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Update — async; mutable mask only.
func (h *Handler) Update(ctx context.Context, req *lbv1.UpdateListenerRequest) (*operationpb.Operation, error) {
	op, err := h.update.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// Delete — async; releases VIP + emits 2× outbox events.
func (h *Handler) Delete(ctx context.Context, req *lbv1.DeleteListenerRequest) (*operationpb.Operation, error) {
	op, err := h.deleteUC.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	return operationToProto(op), nil
}

// ListOperations — sync per-resource history.
func (h *Handler) ListOperations(ctx context.Context, req *lbv1.ListListenerOperationsRequest) (*lbv1.ListListenerOperationsResponse, error) {
	return h.listOperations.Run(ctx, req)
}
