// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operation

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
)

// GetUseCase — sync read одной операции.
//
// use-case владеет бизнес-логикой (валидация input + маппинг
// domain-ошибок в gRPC-codes); handler — тонкий transport.
type GetUseCase struct {
	repo operations.Repo
}

// NewGetUseCase конструктор.
func NewGetUseCase(repo operations.Repo) *GetUseCase {
	return &GetUseCase{repo: repo}
}

// Run выполняет use-case.
//
// Owner-scope: operation_id опакен, но это прямой объект-референс. Без проверки
// владельца любой аутентифицированный caller, узнав чужой id, прочитал бы чужую
// операцию (Operation.response несёт ресурс целиком). Поэтому чтение идёт через
// ownership-scoped repo (GetOwned, предикат в SQL WHERE по principal-ключу
// создателя). Чужой/несуществующий id отдаёт одинаковый NotFound (no-leak:
// «есть-но-не-твоя» неотличимо от «нет такой»).
//
// Mapping:
//
//	req.OperationId == "" → InvalidArgument "operation_id required"
//	ErrNotFound / чужой   → NotFound        "operation <id> not found"
//	other repo err        → Internal        "operation get failed" (no pgx leak)
func (u *GetUseCase) Run(ctx context.Context, req *operationpb.GetOperationRequest) (*operationpb.Operation, error) {
	if req.GetOperationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id required")
	}
	// pgRepo обязан реализовывать OwnedOperationRepo; если не реализует (ошибка
	// wiring'а) — fail-closed Internal, а не silent-bypass owner-предиката.
	owned, ok := operations.AsOwned(u.repo)
	if !ok {
		return nil, status.Error(codes.Internal, "operation get failed")
	}
	owner := operations.OwnerFromPrincipal(operations.PrincipalFromContext(ctx))
	op, err := owned.GetOwned(ctx, req.GetOperationId(), owner)
	if err != nil {
		if errors.Is(err, operations.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "operation %s not found", req.GetOperationId())
		}
		// Generic Internal без leak'а pgx-detail.
		return nil, status.Error(codes.Internal, "operation get failed")
	}
	return operationToProto(op), nil
}
