// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"encoding/json"
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

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
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

// StripSentinel behaviour is now unit-tested centrally in
// internal/apps/kacho/api/shared/errmap_test.go (single source of truth after
// the mapper de-duplication, audit ARCH-medium). mapDomainErr coverage above
// still verifies listener делегирует в shared корректно.

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

// TestListenerPayloadMap_ParentResourceIDReachesConsumer — regression for the
// outbox payload-key drift: listenerPayloadMap must emit the parent LB under the
// canonical `parent_resource_id` key that the Subscribe consumer parses, so
// ResourceLifecycleEvent.ParentResourceId is populated for kacho-iam FGA-sync
// (previously it emitted `load_balancer_id`, which no consumer reads → parent
// always empty). Producer helper → shared parser (the SAME parser the consumer
// uses) round-trip proves both sides agree on the key name.
func TestListenerPayloadMap_ParentResourceIDReachesConsumer(t *testing.T) {
	t.Parallel()
	rec := &kachorepo.ListenerRecord{}
	rec.ID = domain.ResourceID("nlb-listener-1")
	rec.LoadBalancerID = domain.ResourceID("nlb-1")
	rec.ProjectID = domain.ProjectID("prj-b")

	m := listenerPayloadMap(rec)
	// legacy key must be gone.
	require.NotContains(t, m, "load_balancer_id", "legacy key must not be emitted")

	raw, err := json.Marshal(m)
	require.NoError(t, err)
	parsed, err := kachorepo.ParseLifecyclePayload(raw)
	require.NoError(t, err)
	require.Equal(t, "nlb-1", parsed.ParentResourceID,
		"consumer must recover parent LB id from listener payload")
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
