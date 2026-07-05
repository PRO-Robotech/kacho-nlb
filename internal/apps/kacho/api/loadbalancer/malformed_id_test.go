// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// TestGetLoadBalancer_MalformedID — id с неизвестным prefix → sync
// InvalidArgument "invalid network load balancer id '<X>'", НЕ NotFound
// (api-conventions malformed-id discipline). Well-formed-но-нет → NotFound.
func TestGetLoadBalancer_MalformedID(t *testing.T) {
	t.Parallel()
	uc := NewGetLoadBalancerUseCase(newFakeRepo())
	_, err := uc.Execute(context.Background(), &lbv1.GetNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: "bogus-id",
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "invalid network load balancer id")
}
