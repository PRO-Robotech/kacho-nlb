package loadbalancer

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// ListOperationsUseCase — per-resource history через operations.ListFilter с
// фильтром по `resource_id` (NetworkLoadBalancerId). Sync read.
//
// Acceptance: GWT-NLB-043..GWT-NLB-046.
type ListOperationsUseCase struct {
	opsRepo operations.Repo
}

// NewListOperationsUseCase конструктор.
func NewListOperationsUseCase(opsRepo operations.Repo) *ListOperationsUseCase {
	return &ListOperationsUseCase{opsRepo: opsRepo}
}

// Execute — wrap opsRepo.List + per-row operationToProto.
func (u *ListOperationsUseCase) Execute(
	ctx context.Context, req *lbv1.ListNetworkLoadBalancerOperationsRequest,
) (*lbv1.ListNetworkLoadBalancerOperationsResponse, error) {
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	ops, next, err := u.opsRepo.List(ctx, operations.ListFilter{
		ResourceID: id,
		PageSize:   req.GetPageSize(),
		PageToken:  req.GetPageToken(),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list operations: %v", err)
	}
	resp := &lbv1.ListNetworkLoadBalancerOperationsResponse{NextPageToken: next}
	for i := range ops {
		resp.Operations = append(resp.Operations, operationToProto(&ops[i]))
	}
	return resp, nil
}
