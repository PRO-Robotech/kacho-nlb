// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestMapDomainErr_AllSentinels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		err      error
		wantCode codes.Code
	}{
		{"nil", nil, codes.OK},
		{"not_found", fmt.Errorf("%w: NetworkLoadBalancer nlb-x not found", domain.ErrNotFound), codes.NotFound},
		{"already_exists", fmt.Errorf("%w: duplicate", domain.ErrAlreadyExists), codes.AlreadyExists},
		{"failed_precondition", fmt.Errorf("%w: bad state", domain.ErrFailedPrecondition), codes.FailedPrecondition},
		{"invalid_arg", fmt.Errorf("%w: bad input", domain.ErrInvalidArg), codes.InvalidArgument},
		{"unavailable", fmt.Errorf("%w: peer down", domain.ErrUnavailable), codes.Unavailable},
		{"internal", fmt.Errorf("%w: pgx exploded", domain.ErrInternal), codes.Internal},
		{"unknown", errors.New("random"), codes.Internal},
		{"already_status", status.Error(codes.AlreadyExists, "test"), codes.AlreadyExists},
		{"repo_not_found_alias", fmt.Errorf("%w: x", kachorepo.ErrNotFound), codes.NotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapDomainErr(tc.err)
			if tc.err == nil {
				require.NoError(t, got)
				return
			}
			require.Equal(t, tc.wantCode, status.Code(got))
		})
	}
}

func TestStripSentinel(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("%w: NetworkLoadBalancer nlb-x not found", domain.ErrNotFound)
	require.Equal(t, "NetworkLoadBalancer nlb-x not found", stripSentinel(err, "fallback"))
	require.Equal(t, "fallback", stripSentinel(nil, "fallback"))
}

func TestOperationToProto(t *testing.T) {
	t.Parallel()
	op := &operations.Operation{
		ID: "op-123", Description: "test", CreatedAt: time.Now(), Done: true,
		Principal: operations.Principal{Type: "user", ID: "usr-x", DisplayName: "Alice"},
	}
	pb := operationToProto(op)
	require.Equal(t, "op-123", pb.GetId())
	require.True(t, pb.GetDone())
	require.Equal(t, "user", pb.GetPrincipalType())
	require.Equal(t, "usr-x", pb.GetPrincipalId())
}

func TestPeerErrToStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want codes.Code
	}{
		{fmt.Errorf("%w: not found", domain.ErrNotFound), codes.InvalidArgument},
		{fmt.Errorf("%w: invalid", domain.ErrInvalidArg), codes.InvalidArgument},
		{fmt.Errorf("%w: precondition", domain.ErrFailedPrecondition), codes.FailedPrecondition},
		{fmt.Errorf("%w: unavail", domain.ErrUnavailable), codes.Unavailable},
		{errors.New("other"), codes.Internal},
	}
	for i, tc := range cases {
		got := peerErrToStatus(tc.err, "project", "prj-x")
		require.Equal(t, tc.want, status.Code(got), "case %d", i)
	}
	require.NoError(t, peerErrToStatus(nil, "x", "y"))
}
