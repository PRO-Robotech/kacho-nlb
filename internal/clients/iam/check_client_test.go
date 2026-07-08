// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package iam

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/authz"
	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeInternalIAMForCheck — in-memory InternalIAMServiceServer для Check тестов.
type fakeInternalIAMForCheck struct {
	iampb.UnimplementedInternalIAMServiceServer

	resp *iampb.CheckResponse
	err  error
}

func (f *fakeInternalIAMForCheck) Check(_ context.Context, _ *iampb.CheckRequest) (*iampb.CheckResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestCheckClient_Allowed(t *testing.T) {
	fake := &fakeInternalIAMForCheck{resp: &iampb.CheckResponse{Allowed: true}}
	conn := startFakeIAM(t, nil, fake)
	c := NewCheckClient(conn)

	allowed, err := c.Check(ctxBackground(), "user:u1", "viewer", "nlb_listener:lst-1")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestCheckClient_DeniedNoReason(t *testing.T) {
	fake := &fakeInternalIAMForCheck{resp: &iampb.CheckResponse{Allowed: false, Reason: "explicit deny"}}
	conn := startFakeIAM(t, nil, fake)
	c := NewCheckClient(conn)

	allowed, err := c.Check(ctxBackground(), "user:u1", "viewer", "nlb_listener:lst-1")
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestCheckClient_NoPath(t *testing.T) {
	fake := &fakeInternalIAMForCheck{resp: &iampb.CheckResponse{Allowed: false, Reason: "no path to resource"}}
	conn := startFakeIAM(t, nil, fake)
	c := NewCheckClient(conn)

	allowed, err := c.Check(ctxBackground(), "user:u1", "viewer", "nlb_listener:lst-nx")
	require.Error(t, err)
	assert.True(t, errors.Is(err, authz.ErrNoPath), "expected authz.ErrNoPath: %v", err)
	assert.False(t, allowed)
}

func TestCheckClient_NoPath_AlternateMarker(t *testing.T) {
	// kacho-iam may emit "no_path" instead of "no path"
	fake := &fakeInternalIAMForCheck{resp: &iampb.CheckResponse{Allowed: false, Reason: "no_path: tuple missing"}}
	conn := startFakeIAM(t, nil, fake)
	c := NewCheckClient(conn)

	_, err := c.Check(ctxBackground(), "user:u1", "viewer", "nlb_listener:lst-nx")
	require.Error(t, err)
	assert.True(t, errors.Is(err, authz.ErrNoPath))
}

func TestCheckClient_InvalidArgument(t *testing.T) {
	fake := &fakeInternalIAMForCheck{err: status.Error(codes.InvalidArgument, "bad relation")}
	conn := startFakeIAM(t, nil, fake)
	c := NewCheckClient(conn)

	_, err := c.Check(ctxBackground(), "user:u1", "bad-rel", "obj")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestCheckClient_Unavailable(t *testing.T) {
	fake := &fakeInternalIAMForCheck{err: status.Error(codes.Unavailable, "fga down")}
	conn := startFakeIAM(t, nil, fake)
	c := NewCheckClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Check(ctx, "user:u1", "viewer", "obj")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

// blockingInternalIAMForCheck — fake InternalIAMServiceServer that never
// returns from Check until explicitly released (simulates a hung/stalled
// iam-side FGA handler that still ACKs keepalives at the transport level).
type blockingInternalIAMForCheck struct {
	iampb.UnimplementedInternalIAMServiceServer
	release chan struct{}
}

func (f *blockingInternalIAMForCheck) Check(_ context.Context, _ *iampb.CheckRequest) (*iampb.CheckResponse, error) {
	<-f.release
	return &iampb.CheckResponse{Allowed: true}, nil
}

// TestCheckClient_HangingPeer_BoundsToConfiguredTimeout — regression for the
// missing per-call deadline (round-5 audit finding 1): a stalled iam/FGA peer
// must not park the calling goroutine forever. Check is called with a
// deadline-less caller ctx (context.Background(), mirroring the raw ctx used
// by handler-side direct Check calls in attach_target_group.go/move.go,
// which run outside the authz interceptor's own CheckTimeout-bounded ctx) —
// the client itself must bound the call to ~its configured per-call timeout
// and fail closed (DeadlineExceeded -> domain.ErrUnavailable), not hang.
func TestCheckClient_HangingPeer_BoundsToConfiguredTimeout(t *testing.T) {
	fake := &blockingInternalIAMForCheck{release: make(chan struct{})}
	conn := startFakeIAM(t, nil, fake)

	const configuredTimeout = 100 * time.Millisecond
	c := NewCheckClientWithTimeout(conn, configuredTimeout)

	start := time.Now()
	_, err := c.Check(context.Background(), "user:u1", "viewer", "obj")
	elapsed := time.Since(start)
	// Release the still-in-flight fake handler goroutine synchronously (NOT
	// via t.Cleanup: cleanups registered by startFakeIAM's GracefulStop run
	// LIFO-*after* one registered here would, and GracefulStop blocks on the
	// in-flight handler — closing release only in a later Cleanup would
	// deadlock the test's own teardown).
	close(fake.release)

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrUnavailable),
		"expected fail-closed domain.ErrUnavailable on peer hang; got %v", err)
	assert.Less(t, elapsed, 2*time.Second,
		"Check must bound to the configured per-call timeout (~%s), not hang on an unresponsive peer; took %s",
		configuredTimeout, elapsed)
}

func TestCheckClient_EmptyArgs(t *testing.T) {
	c := NewCheckClientFromStub(&fakeInternalIAMStub{})
	for _, tc := range []struct{ name, subj, rel, obj string }{
		{"empty subject", "", "viewer", "obj"},
		{"empty relation", "user:u", "", "obj"},
		{"empty object", "user:u", "viewer", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Check(ctxBackground(), tc.subj, tc.rel, tc.obj)
			require.Error(t, err)
			assert.True(t, errors.Is(err, domain.ErrInvalidArg))
		})
	}
}

func TestCheckClient_NilConn(t *testing.T) {
	assert.Nil(t, NewCheckClient(nil))
}

type fakeInternalIAMStub struct{ iampb.InternalIAMServiceClient }
