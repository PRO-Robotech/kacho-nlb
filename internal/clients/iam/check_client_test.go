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
