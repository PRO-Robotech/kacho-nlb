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
// §3.5 + per-resource ListOperations RPC'и.
//
// Authz: оба RPC помечены `(kacho.iam.authz.v1.permission) = "<exempt>"` в proto —
// FGA Check interceptor пропускает их (любой авторизованный subject может Get/Cancel
// своих операций). Owner-scope для Cancel будет добавлен следом (см. GWT-AZD-011)
// — пока это handler-уровень за scope этого PR.
//
// Реализация копирует проверенный паттерн `kacho-vpc/internal/handler/operation_handler.go`
// и `kacho-compute/internal/handler/operation_handler.go`, с одним отличием — proto
// принципал-поля (`principal_type` / `principal_id` / `principal_display_name`, KAC-105)
// заполняются в ответе (vpc/compute mapping этот шаг исторически пропускал).
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
//	FailedPrecondition — операция уже done=true (verbatim: "operation is already completed").
//	Internal           — нераспознанная ошибка.
//
// Cancel НЕ идемпотентна: повторный Cancel уже-отменённой op → FailedPrecondition
// (acceptance GWT-OP-006). Это сознательный design choice (соответствует verbatim
// YC и проверенному паттерну vpc/compute).
func (h *Handler) Cancel(ctx context.Context, req *operationpb.CancelOperationRequest) (*operationpb.Operation, error) {
	return h.cancel.Run(ctx, req)
}
