// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// TestMapDomainErr_AllSentinels — each domain-sentinel maps to the right
// gRPC code; non-sentinel maps to Internal; pre-formed gRPC status passes through.
func TestMapDomainErr_AllSentinels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"nil → nil", nil, codes.OK},
		{"NotFound", fmt.Errorf("%w: Listener xyz not found", domain.ErrNotFound), codes.NotFound},
		{"AlreadyExists", fmt.Errorf("%w: dup", domain.ErrAlreadyExists), codes.AlreadyExists},
		{"FailedPrecondition", fmt.Errorf("%w: bad state", domain.ErrFailedPrecondition), codes.FailedPrecondition},
		{"InvalidArg", fmt.Errorf("%w: bad input", domain.ErrInvalidArg), codes.InvalidArgument},
		{"Unavailable", fmt.Errorf("%w: peer down", domain.ErrUnavailable), codes.Unavailable},
		{"Internal", fmt.Errorf("%w: db crashed", domain.ErrInternal), codes.Internal},
		{"unknown raw error → Internal", errors.New("mystery"), codes.Internal},
		{"pre-formed gRPC status passes through", grpcstatus.Error(codes.PermissionDenied, "no access"), codes.PermissionDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapDomainErr(tc.err)
			if tc.err == nil {
				require.NoError(t, got)
				return
			}
			require.Equal(t, tc.want, grpcstatus.Code(got))
		})
	}
}

// TestStripSentinel — фиксированный текст after sentinel prefix.
func TestStripSentinel(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Listener xyz not found", stripSentinel("not found: Listener xyz not found", "not found"))
	require.Equal(t, "raw text", stripSentinel("raw text", "not found"))
	require.Equal(t, "not found", stripSentinel("not found", "not found"))
}

// TestOperationToProto_FillsPrincipal — principal mapping correctness.
func TestOperationToProto_FillsPrincipal(t *testing.T) {
	t.Parallel()
	op := &operations.Operation{
		ID:          "nlb-op-1",
		Description: "test",
		CreatedAt:   time.Now().UTC(),
		ModifiedAt:  time.Now().UTC(),
		Done:        true,
		Principal: operations.Principal{
			Type:        "user",
			ID:          "usr-123",
			DisplayName: "Alice",
		},
		Error: &status.Status{Code: int32(codes.InvalidArgument), Message: "boom"},
	}
	pb := operationToProto(op)
	require.Equal(t, "user", pb.PrincipalType)
	require.Equal(t, "usr-123", pb.PrincipalId)
	require.Equal(t, "Alice", pb.PrincipalDisplayName)
	require.NotNil(t, pb.GetError())

	// Nil op → nil.
	require.Nil(t, operationToProto(nil))

	// Success response path.
	op.Error = nil
	op.Response = nil // no response
	pb = operationToProto(op)
	require.Equal(t, true, pb.Done)
	require.Nil(t, pb.GetError())
	require.Nil(t, pb.GetResponse())
}

// TestListenerPayloadMap_NilGuard — nil input returns nil.
func TestListenerPayloadMap_NilGuard(t *testing.T) {
	t.Parallel()
	require.Nil(t, listenerPayloadMap(nil))
}

// TestListenerRecordToPb_NilGuard — nil → Internal.
func TestListenerRecordToPb_NilGuard(t *testing.T) {
	t.Parallel()
	_, err := listenerRecordToPb(nil)
	require.Error(t, err)
	require.Equal(t, codes.Internal, grpcstatus.Code(err))
}

// TestLoggerOrDiscard_Default — nil logger → slog.Default.
func TestLoggerOrDiscard_Default(t *testing.T) {
	t.Parallel()
	require.Same(t, slog.Default(), loggerOrDiscard(nil))
	custom := slog.New(slog.Default().Handler())
	require.Same(t, custom, loggerOrDiscard(custom))
}

// TestFamilyForIPVersion — covers both branches.
func TestFamilyForIPVersion(t *testing.T) {
	t.Parallel()
	require.Equal(t, vpcclient.AddressFamilyIPv4, familyForIPVersion(domain.IPVersionV4))
	require.Equal(t, vpcclient.AddressFamilyIPv6, familyForIPVersion(domain.IPVersionV6))
	// Unknown → defaults to v4 (safe-by-default).
	require.Equal(t, vpcclient.AddressFamilyIPv4, familyForIPVersion(domain.IPVersion("WEIRD")))
}

// TestOwnerMatches — same kind+id matches; difference does not.
func TestOwnerMatches(t *testing.T) {
	t.Parallel()
	a := vpcclient.AddressOwner{Kind: "nlb_listener", ID: "lst1"}
	require.True(t, ownerMatches(a, a))
	require.False(t, ownerMatches(a, vpcclient.AddressOwner{Kind: "nlb_listener", ID: "lst2"}))
	require.False(t, ownerMatches(a, vpcclient.AddressOwner{Kind: "compute_instance", ID: "lst1"}))
}
