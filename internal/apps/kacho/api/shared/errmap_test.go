// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package shared

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

func TestMapDomainErr_AllSentinels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil", nil, codes.OK},
		{"NotFound", fmt.Errorf("%w: LB x not found", domain.ErrNotFound), codes.NotFound},
		{"AlreadyExists", fmt.Errorf("%w: dup", domain.ErrAlreadyExists), codes.AlreadyExists},
		{"FailedPrecondition", fmt.Errorf("%w: bad state", domain.ErrFailedPrecondition), codes.FailedPrecondition},
		{"InvalidArg", fmt.Errorf("%w: bad input", domain.ErrInvalidArg), codes.InvalidArgument},
		{"Unavailable", fmt.Errorf("%w: peer down", domain.ErrUnavailable), codes.Unavailable},
		{"Internal", fmt.Errorf("%w: db crashed", domain.ErrInternal), codes.Internal},
		{"unknown raw error", errors.New("mystery"), codes.Internal},
		{"pre-formed status passes through", grpcstatus.Error(codes.PermissionDenied, "no access"), codes.PermissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := MapDomainErr(tc.err)
			if tc.err == nil {
				require.NoError(t, got)
				return
			}
			require.Equal(t, tc.want, grpcstatus.Code(got))
		})
	}
}

// TestMapDomainErr_UnknownStatusNotPassedThrough — a status with codes.Unknown
// must NOT be passed through (это была расхождение в loadbalancer-копии): такой
// error попадает в sentinel-switch → Internal без leak'а, одинаково для всех
// ресурсов.
func TestMapDomainErr_UnknownStatusNotPassedThrough(t *testing.T) {
	t.Parallel()
	unknown := grpcstatus.Error(codes.Unknown, "leaky text")
	got := MapDomainErr(unknown)
	require.Equal(t, codes.Internal, grpcstatus.Code(got))
	require.Equal(t, "internal error", grpcstatus.Convert(got).Message())
}

func TestStripSentinel(t *testing.T) {
	t.Parallel()
	require.Equal(t, "LB nlb-x not found",
		StripSentinel(fmt.Errorf("%w: LB nlb-x not found", domain.ErrNotFound), "fallback"))
	require.Equal(t, "fallback", StripSentinel(nil, "fallback"))
	require.Equal(t, "raw text", StripSentinel(errors.New("raw text"), "fallback"))
}
