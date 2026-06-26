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
// Семантика (verbatim kacho-vpc/kacho-compute pattern; acceptance GWT-OP-005/006):
//
//	in-flight → done=true, error.code=CANCELLED — handler возвращает свежий Operation;
//	already done → FailedPrecondition "operation <id> already completed";
//	not found    → NotFound          "operation <id> not found".
//
// Worker, который реально выполняет тяжёлую работу, должен периодически проверять
// op.Done через `operations.Repo.Get` и aborter'иться при cancel-flag — это
// ответственность worker'а, не Cancel-handler'а. Сам handler атомарно flip'ает
// done через repo.Cancel (single-statement UPDATE ... WHERE done=false; safe от race).
//
// Cancel НЕ идемпотентен: повторный Cancel уже-отменённой op → FailedPrecondition
// (acceptance GWT-OP-006). См. handler-level doc для обоснования.
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
