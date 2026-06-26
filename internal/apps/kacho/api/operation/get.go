package operation

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
)

// GetUseCase — sync read одной операции.
//
// evgeniy §2.B: use-case владеет бизнес-логикой (валидация input + маппинг
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
// Mapping (verbatim kacho-vpc/kacho-compute pattern):
//
//	req.OperationId == "" → InvalidArgument "operation_id required"
//	repo.ErrNotFound      → NotFound        "operation <id> not found"
//	other repo err        → Internal        "operation get failed" (no pgx leak)
func (u *GetUseCase) Run(ctx context.Context, req *operationpb.GetOperationRequest) (*operationpb.Operation, error) {
	if req.GetOperationId() == "" {
		return nil, status.Error(codes.InvalidArgument, "operation_id required")
	}
	op, err := u.repo.Get(ctx, req.GetOperationId())
	if err != nil {
		if errors.Is(err, operations.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "operation %s not found", req.GetOperationId())
		}
		// Generic Internal без leak'а pgx-detail (см. kacho-vpc R9 M1).
		return nil, status.Error(codes.Internal, "operation get failed")
	}
	return operationToProto(op), nil
}
