// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// ListOperationsUseCase — sync per-Listener Operations history.
// Tonkii wrapper над `kacho-corelib/operations.Repo.List`
// с фильтром `resource_id == <listener_id>` (extractResourceID per
// CreateListenerMetadata.listener_id / UpdateListenerMetadata.listener_id /
// DeleteListenerMetadata.listener_id).
type ListOperationsUseCase struct {
	opsRepo OperationsRepo
}

// NewListOperationsUseCase — конструктор.
func NewListOperationsUseCase(opsRepo OperationsRepo) *ListOperationsUseCase {
	return &ListOperationsUseCase{opsRepo: opsRepo}
}

// Run выполняет ListOperations.
//
// Mapping:
//
//	req.ListenerId == "" → InvalidArgument "listener_id required"
//	other repo err       → Internal "operation list failed" (no leak)
func (u *ListOperationsUseCase) Run(ctx context.Context, req *lbv1.ListListenerOperationsRequest) (*lbv1.ListListenerOperationsResponse, error) {
	id := req.GetListenerId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "listener_id required")
	}
	if err := validateListenerID(id); err != nil {
		return nil, err
	}
	ops, next, err := u.opsRepo.List(ctx, operations.ListFilter{
		ResourceID: id,
		PageSize:   req.GetPageSize(),
		PageToken:  req.GetPageToken(),
	})
	if err != nil {
		return nil, status.Error(codes.Internal, "operation list failed")
	}
	resp := &lbv1.ListListenerOperationsResponse{NextPageToken: next}
	for i := range ops {
		resp.Operations = append(resp.Operations, operationToProto(&ops[i]))
	}
	return resp, nil
}
