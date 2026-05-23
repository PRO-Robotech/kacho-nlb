package iam

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeInternalIAMForWrite — in-memory InternalIAMServiceServer для
// WriteCreatorTuple тестов; ловит все WriteCreatorTuple запросы.
type fakeInternalIAMForWrite struct {
	iampb.UnimplementedInternalIAMServiceServer

	mu       sync.Mutex
	requests []*iampb.WriteCreatorTupleRequest
	err      error
}

func (f *fakeInternalIAMForWrite) WriteCreatorTuple(
	_ context.Context, req *iampb.WriteCreatorTupleRequest,
) (*iampb.WriteCreatorTupleResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	return &iampb.WriteCreatorTupleResponse{}, nil
}

func (f *fakeInternalIAMForWrite) calls() []*iampb.WriteCreatorTupleRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*iampb.WriteCreatorTupleRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func TestHierarchyWriter_WriteCreatorTuple_HappyPath(t *testing.T) {
	fake := &fakeInternalIAMForWrite{}
	conn := startFakeIAM(t, nil, fake)
	w := NewHierarchyWriter(conn)

	err := w.WriteCreatorTuple(ctxBackground(),
		"user:usr_alice", "admin", "nlb_listener:lst-1")
	require.NoError(t, err)
	calls := fake.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "user:usr_alice", calls[0].SubjectId)
	assert.Equal(t, "admin", calls[0].Relation)
	assert.Equal(t, "nlb_listener:lst-1", calls[0].Object)
}

func TestHierarchyWriter_WriteCreatorTuple_Idempotent(t *testing.T) {
	fake := &fakeInternalIAMForWrite{err: status.Error(codes.AlreadyExists, "tuple exists")}
	conn := startFakeIAM(t, nil, fake)
	w := NewHierarchyWriter(conn)

	err := w.WriteCreatorTuple(ctxBackground(), "user:u", "admin", "obj:x")
	require.NoError(t, err, "AlreadyExists must be idempotent (treated as success)")
}

func TestHierarchyWriter_WriteCreatorTuple_InvalidArgPropagates(t *testing.T) {
	fake := &fakeInternalIAMForWrite{err: status.Error(codes.InvalidArgument, "bad object syntax")}
	conn := startFakeIAM(t, nil, fake)
	w := NewHierarchyWriter(conn)

	err := w.WriteCreatorTuple(ctxBackground(), "user:u", "admin", "obj")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestHierarchyWriter_WriteCreatorTuple_Unavailable(t *testing.T) {
	fake := &fakeInternalIAMForWrite{err: status.Error(codes.Unavailable, "iam down")}
	conn := startFakeIAM(t, nil, fake)
	w := NewHierarchyWriter(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()

	err := w.WriteCreatorTuple(ctx, "user:u", "admin", "obj:x")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestHierarchyWriter_WriteCreatorTuple_EmptyArgs(t *testing.T) {
	w := NewHierarchyWriterFromStub(&fakeInternalIAMStub{})
	for _, tc := range []struct{ name, subj, rel, obj string }{
		{"empty subject", "", "admin", "obj"},
		{"empty relation", "user:u", "", "obj"},
		{"empty object", "user:u", "admin", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := w.WriteCreatorTuple(ctxBackground(), tc.subj, tc.rel, tc.obj)
			require.Error(t, err)
			assert.True(t, errors.Is(err, domain.ErrInvalidArg))
		})
	}
}

func TestHierarchyWriter_RewriteProjectTuple_HappyPath(t *testing.T) {
	fake := &fakeInternalIAMForWrite{}
	conn := startFakeIAM(t, nil, fake)
	w := NewHierarchyWriter(conn)

	err := w.RewriteProjectTuple(ctxBackground(), "nlb_listener", "lst-1", "prj-src", "prj-dst")
	require.NoError(t, err)
	calls := fake.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "project:prj-dst", calls[0].SubjectId)
	assert.Equal(t, "project", calls[0].Relation)
	assert.Equal(t, "nlb_listener:lst-1", calls[0].Object)
}

func TestHierarchyWriter_RewriteProjectTuple_EmptySrc(t *testing.T) {
	fake := &fakeInternalIAMForWrite{}
	conn := startFakeIAM(t, nil, fake)
	w := NewHierarchyWriter(conn)

	// Initial Create-flow: srcProject пустой — допустимо (первичная запись).
	err := w.RewriteProjectTuple(ctxBackground(), "nlb_listener", "lst-1", "", "prj-dst")
	require.NoError(t, err)
}

func TestHierarchyWriter_RewriteProjectTuple_EmptyArgs(t *testing.T) {
	w := NewHierarchyWriterFromStub(&fakeInternalIAMStub{})
	for _, tc := range []struct{ name, objType, objID, dst string }{
		{"empty type", "", "lst-1", "prj"},
		{"empty id", "nlb_listener", "", "prj"},
		{"empty dst", "nlb_listener", "lst-1", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := w.RewriteProjectTuple(ctxBackground(), tc.objType, tc.objID, "", tc.dst)
			require.Error(t, err)
			assert.True(t, errors.Is(err, domain.ErrInvalidArg))
		})
	}
}

func TestHierarchyWriter_NilConn(t *testing.T) {
	assert.Nil(t, NewHierarchyWriter(nil))
}
