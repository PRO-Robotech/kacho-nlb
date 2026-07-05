// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package announce

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// GetAnnounceStateUseCase — sync read наблюдаемой per-zone announce-state одного
// LB (Internal-проекция). Маппинг:
//
//	network_load_balancer_id == ""     → InvalidArgument "required"
//	malformed id                       → InvalidArgument (corevalidate)
//	LB отсутствует                     → NotFound "NetworkLoadBalancer <id> not found"
//	store internal                     → Internal (no leak)
type GetAnnounceStateUseCase struct {
	store Store
}

// NewGetAnnounceStateUseCase конструктор.
func NewGetAnnounceStateUseCase(store Store) *GetAnnounceStateUseCase {
	return &GetAnnounceStateUseCase{store: store}
}

// Execute — sync flow: validate id → store.LoadState → DTO → return.
func (u *GetAnnounceStateUseCase) Execute(
	ctx context.Context, req *lbv1.GetLoadBalancerAnnounceStateRequest,
) (*lbv1.LoadBalancerAnnounceState, error) {
	if u.store == nil {
		return nil, status.Error(codes.Unavailable, "announce store not configured")
	}
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(id); err != nil {
		return nil, mapErr(err)
	}

	rec, found, err := u.store.LoadState(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	if !found {
		return nil, status.Errorf(codes.NotFound, "NetworkLoadBalancer %s not found", id)
	}
	return stateToProto(rec), nil
}
