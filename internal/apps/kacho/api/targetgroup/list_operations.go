package targetgroup

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// ListOperationsUseCase — per-resource Operation history filtered by
// resource_id == target_group_id (acceptance GWT-TGR-028). Sync read.
type ListOperationsUseCase struct {
	opsRepo OpsRepo
}

// NewListOperationsUseCase конструктор.
func NewListOperationsUseCase(opsRepo OpsRepo) *ListOperationsUseCase {
	return &ListOperationsUseCase{opsRepo: opsRepo}
}

// Execute — opsRepo.List + per-row operationToProto.
func (u *ListOperationsUseCase) Execute(
	ctx context.Context, req *lbv1.ListTargetGroupOperationsRequest,
) (*lbv1.ListTargetGroupOperationsResponse, error) {
	id := req.GetTargetGroupId()
	if id == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}
	ops, next, err := u.opsRepo.List(ctx, operations.ListFilter{
		ResourceID: id,
		PageSize:   req.GetPageSize(),
		PageToken:  req.GetPageToken(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list operations: %v", err)
	}
	resp := &lbv1.ListTargetGroupOperationsResponse{NextPageToken: next}
	for i := range ops {
		resp.Operations = append(resp.Operations, operationToProto(&ops[i]))
	}
	return resp, nil
}
