// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operation

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
)

// CancelUseCase — переводит in-flight операцию в done=true со статусом CANCELLED.
//
// Семантика (owner-scope):
//
//	in-flight (владелец)  → done=true, error.code=CANCELLED — возвращается свежий Operation;
//	already done          → FailedPrecondition "operation <id> already completed";
//	чужой / not found      → NotFound          "operation <id> not found" (existence-hiding).
//
// Owner-scope: без проверки владельца любой аутентифицированный caller, узнав
// чужой op-id, отменил бы чужую in-flight мутацию. Поэтому первым шагом идёт
// ownership-gate через `GetOwned` (предикат по principal-ключу создателя в SQL
// WHERE) — чужой/несуществующий id → одинаковый NotFound (existence-hiding) ДО
// какой-либо мутации, так что чужую операцию нельзя ни прочитать, ни тронуть.
//
// Сама отмена — атомарный single-statement CAS `repo.Cancel`
// (`UPDATE... WHERE id=$1 AND done=false`): ровно один из конкурирующих Cancel'ов
// выигрывает, остальные видят done=true → ErrAlreadyDone → FailedPrecondition.
// Cancel НЕ идемпотентен (повторный Cancel уже-завершённой op → FailedPrecondition)
// — поэтому используется именно non-idempotent repo.Cancel,
// а не idempotent-on-CANCELLED corelib CancelOwned.
//
// Worker, реально выполняющий тяжёлую работу, периодически проверяет op.Done через
// `operations.Repo.Get` и aborter'ится при cancel-flag — это ответственность
// worker'а, не Cancel-handler'а.
type CancelUseCase struct {
	repo operations.Repo
}

// NewCancelUseCase конструктор.
func NewCancelUseCase(repo operations.Repo) *CancelUseCase {
	return &CancelUseCase{repo: repo}
}

// Run выполняет use-case.
func (u *CancelUseCase) Run(ctx context.Context, req *operationpb.CancelOperationRequest) (*operationpb.Operation, error) {
	if req.GetOperationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id required")
	}
	// pgRepo обязан реализовывать OwnedOperationRepo; иначе fail-closed Internal
	// (не silent-bypass owner-предиката).
	owned, ok := operations.AsOwned(u.repo)
	if !ok {
		return nil, status.Error(codes.Internal, "operation cancel failed")
	}
	// Ownership-gate: чужой/несуществующий op-id → NotFound (existence-hiding) до
	// любой мутации. Ownership неизменна, поэтому read-gate перед атомарным CAS не
	// создаёт TOCTOU на состояние (саму отмену выигрывает single-statement Cancel).
	owner := operations.OwnerFromPrincipal(operations.PrincipalFromContext(ctx))
	if _, err := owned.GetOwned(ctx, req.GetOperationId(), owner); err != nil {
		if errors.Is(err, operations.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "operation %s not found", req.GetOperationId())
		}
		return nil, status.Error(codes.Internal, "operation cancel failed")
	}

	if err := u.repo.Cancel(ctx, req.GetOperationId()); err != nil {
		if errors.Is(err, operations.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "operation %s not found", req.GetOperationId())
		}
		if errors.Is(err, operations.ErrAlreadyDone) {
			return nil, status.Errorf(codes.FailedPrecondition,
				"operation %s already completed", req.GetOperationId())
		}
		return nil, status.Error(codes.Internal, "operation cancel failed")
	}
	// Re-read актуального состояния (с error.code=CANCELLED заполненным).
	op, err := u.repo.Get(ctx, req.GetOperationId())
	if err != nil {
		if errors.Is(err, operations.ErrNotFound) {
			// race: op исчез между Cancel и Get — отдадим NotFound.
			return nil, status.Errorf(codes.NotFound, "operation %s not found", req.GetOperationId())
		}
		return nil, status.Error(codes.Internal, "operation reload after cancel failed")
	}
	return operationToProto(op), nil
}
