// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// GetLoadBalancerUseCase — sync read одного NetworkLoadBalancer.
// Маппинг:
//
//	req.NetworkLoadBalancerId == "" → InvalidArgument "network_load_balancer_id required"
//	repo ErrNotFound                → NotFound (текст ошибки по конвенции Kachō из mapPgErr)
//	repo internal                   → Internal (no leak)
type GetLoadBalancerUseCase struct {
	repo Repo
}

// NewGetLoadBalancerUseCase конструктор.
func NewGetLoadBalancerUseCase(repo Repo) *GetLoadBalancerUseCase {
	return &GetLoadBalancerUseCase{repo: repo}
}

// Execute — sync flow: open reader → repo.Get → DTO transfer → return.
func (u *GetLoadBalancerUseCase) Execute(ctx context.Context, req *lbv1.GetNetworkLoadBalancerRequest) (*lbv1.NetworkLoadBalancer, error) {
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(id); err != nil {
		return nil, err
	}
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	rec, err := rd.LoadBalancers().Get(ctx, id)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return lbRecordToProto(rec)
}
