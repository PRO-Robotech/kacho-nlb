// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// TestGetTargetGroup_MalformedID — id с неизвестным prefix → sync
// InvalidArgument "invalid target group id '<X>'", НЕ NotFound
// (api-conventions malformed-id discipline). Well-formed-но-нет → NotFound.
func TestGetTargetGroup_MalformedID(t *testing.T) {
	t.Parallel()
	uc := NewGetTargetGroupUseCase(newFakeRepo())
	_, err := uc.Execute(context.Background(), &lbv1.GetTargetGroupRequest{
		TargetGroupId: "bogus-id",
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "invalid target group id")
}
