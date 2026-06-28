// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// TestGetListener_MalformedID — id с неизвестным 3-char prefix должен дать sync
// InvalidArgument "invalid listener id '<X>'", НЕ NotFound (api-conventions
// malformed-id discipline). Well-formed-но-нет → NotFound (см. read_test.go).
func TestGetListener_MalformedID(t *testing.T) {
	t.Parallel()
	uc := NewGetUseCase(newFakeRepo())
	_, err := uc.Run(context.Background(), &lbv1.GetListenerRequest{ListenerId: "bogus-id"})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "invalid listener id")
}

// TestUpdateListener_MalformedID — malformed id на мутации тоже даёт sync
// InvalidArgument (первым стейтментом), не NotFound.
func TestUpdateListener_MalformedID(t *testing.T) {
	t.Parallel()
	suite := newUpdateSuite(t)
	_, err := suite.uc.Run(context.Background(), &lbv1.UpdateListenerRequest{
		ListenerId: "bogus-id",
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}},
		Name:       "x",
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "invalid listener id")
}
