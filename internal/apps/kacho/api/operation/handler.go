// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package operation — gRPC handler для kacho.cloud.operation.OperationService.
//
// Scope: thin transport wrapper над `kacho-corelib/operations.Repo`.
// Proto-сервис (`kacho-proto/proto/kacho/cloud/operation/operation_service.proto`)
// exposes ровно 2 RPC:
//   - Get(GetOperationRequest)    → Operation
//   - Cancel(CancelOperationRequest) → Operation
//
// List-операций НЕТ на этом сервисе — per-resource history exposed через
// `<Resource>Service.ListOperations` (на NLB: `NetworkLoadBalancerService.ListOperations`,
// `ListenerService.ListOperations`, `TargetGroupService.ListOperations`); см. design
// ListOperations RPC'и.
//
// Authz: оба RPC помечены `(kacho.iam.authz.v1.permission) = "<exempt>"` в proto —
// per-RPC FGA Check interceptor их пропускает (op-id опакен, поллится creator'ом
// сразу после Create — в этом окне FGA-tuple ещё может быть не виден). Защита от
// cross-tenant доступа энфорсится на уровне use-case: Get/Cancel сверяют принципала
// caller'а (`operations.PrincipalFromContext`) с создателем операции через
// ownership-scoped repo (GetOwned/CancelOwned, предикат в SQL WHERE). Чужой op-id →
// existence-hiding NotFound.
//
// Принципал-поля (`principal_type` / `principal_id` / `principal_display_name`)
// заполняются в proto-ответе через operationToProto.
package operation

import (
	"context"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
)

// Handler реализует operationpb.OperationServiceServer.
type Handler struct {
	operationpb.UnimplementedOperationServiceServer

	get    *GetUseCase
	cancel *CancelUseCase
}

// NewHandler собирает Handler с use-case'ами поверх переданного `operations.Repo`.
// `cmd/kacho-loadbalancer/main.go` (composition root) — единственное место создания.
func NewHandler(repo operations.Repo) *Handler {
	return &Handler{
		get:    NewGetUseCase(repo),
		cancel: NewCancelUseCase(repo),
	}
}

// Get возвращает Operation по id (sync read). Ошибки:
//
//	InvalidArgument — пустой operation_id.
//	NotFound        — операция не существует.
//	Internal        — нераспознанная ошибка (без leak'а pgx-detail).
func (h *Handler) Get(ctx context.Context, req *operationpb.GetOperationRequest) (*operationpb.Operation, error) {
	return h.get.Run(ctx, req)
}

// Cancel переводит in-flight операцию в done=true со статусом CANCELLED. Ошибки:
//
//	InvalidArgument    — пустой operation_id.
//	NotFound           — операция не существует.
//	FailedPrecondition — операция уже done=true (с фиксированным текстом: "operation is already completed").
//	Internal           — нераспознанная ошибка.
//
// Cancel НЕ идемпотентна: повторный Cancel уже-отменённой op → FailedPrecondition
// . Это сознательный design choice (соответствует с фиксированным текстом
// Kachō и проверенному паттерну vpc/compute).
func (h *Handler) Cancel(ctx context.Context, req *operationpb.CancelOperationRequest) (*operationpb.Operation, error) {
	return h.cancel.Run(ctx, req)
}
