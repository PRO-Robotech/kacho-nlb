// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// ListOperationsUseCase — per-resource history через operations.ListFilter с
// фильтром по `resource_id` (NetworkLoadBalancerId). Sync read.
//
// Acceptance:.
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
	if err := validateLoadBalancerID(id); err != nil {
		return nil, err
	}
	ops, next, err := u.opsRepo.List(ctx, operations.ListFilter{
		ResourceID: id,
		PageSize:   req.GetPageSize(),
		PageToken:  req.GetPageToken(),
	})
	if err != nil {
		return nil, mapDomainErr(err)
	}
	resp := &lbv1.ListNetworkLoadBalancerOperationsResponse{NextPageToken: next}
	for i := range ops {
		resp.Operations = append(resp.Operations, operationToProto(&ops[i]))
	}
	return resp, nil
}
