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

	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeProjectService — in-memory ProjectServiceServer.
type fakeProjectService struct {
	iampb.UnimplementedProjectServiceServer

	getResp *iampb.Project
	getErr  error
	lastReq *iampb.GetProjectRequest
}

func (f *fakeProjectService) Get(_ context.Context, req *iampb.GetProjectRequest) (*iampb.Project, error) {
	f.lastReq = req
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getResp, nil
}

func TestProjectClient_Get_HappyPath(t *testing.T) {
	fake := &fakeProjectService{getResp: &iampb.Project{
		Id:        "prj-abc",
		Name:      "acme-prod",
		AccountId: "acc-1",
	}}
	conn := startFakeIAM(t, fake, nil)
	c := NewProjectClient(conn)
	require.NotNil(t, c)

	ctx, cancel := context.WithTimeout(ctxBackground(), 3*time.Second)
	defer cancel()
	got, err := c.Get(ctx, "prj-abc")
	require.NoError(t, err)
	assert.Equal(t, "prj-abc", got.ID)
	assert.Equal(t, "acme-prod", got.Name)
	assert.Equal(t, "acc-1", got.AccountID)
	assert.Equal(t, "ACTIVE", got.Status)
	assert.Equal(t, "prj-abc", fake.lastReq.GetProjectId())
}

func TestProjectClient_Get_NotFound(t *testing.T) {
	fake := &fakeProjectService{getErr: status.Error(codes.NotFound, "no such project")}
	conn := startFakeIAM(t, fake, nil)
	c := NewProjectClient(conn)
	_, err := c.Get(ctxBackground(), "prj-missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrNotFound), "expected ErrNotFound: %v", err)
}

func TestProjectClient_Get_PermissionDenied(t *testing.T) {
	fake := &fakeProjectService{getErr: status.Error(codes.PermissionDenied, "scope mismatch")}
	conn := startFakeIAM(t, fake, nil)
	c := NewProjectClient(conn)
	_, err := c.Get(ctxBackground(), "prj-other-account")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrFailedPrecondition), "expected ErrFailedPrecondition: %v", err)
	// must NOT leak permission denied
	assert.NotContains(t, err.Error(), "permission")
}

func TestProjectClient_Get_InvalidArgument(t *testing.T) {
	fake := &fakeProjectService{getErr: status.Error(codes.InvalidArgument, "malformed id")}
	conn := startFakeIAM(t, fake, nil)
	c := NewProjectClient(conn)
	_, err := c.Get(ctxBackground(), "prj-bad")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg), "expected ErrInvalidArg: %v", err)
}

func TestProjectClient_Get_UnavailableMapsToDomain(t *testing.T) {
	// retry.OnUnavailable будет крутить ~30s; для теста используем context-deadline,
	// чтобы быстро завершить. shouldRetry в corelib retry уважает ctx.DeadlineExceeded.
	fake := &fakeProjectService{getErr: status.Error(codes.Unavailable, "peer down")}
	conn := startFakeIAM(t, fake, nil)
	c := NewProjectClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Get(ctx, "prj-abc")
	require.Error(t, err)
	// Любой из путей: либо domain.ErrUnavailable (если retry дошёл до bail), либо
	// ctx.DeadlineExceeded (если ctx cancel'нул в середине backoff). Оба
	// семантически = unavailable; принимаем оба.
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestProjectClient_Get_EmptyID(t *testing.T) {
	c := NewProjectClientFromStub(&fakeProjectServiceStub{})
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestProjectClient_NilConn(t *testing.T) {
	c := NewProjectClient(nil)
	assert.Nil(t, c)
}

// fakeProjectServiceStub — stub соответствующий iampb.ProjectServiceClient
// (для конструктора FromStub). Используется в тестах sync-валидации без
// раскручивания gRPC-server'а.
type fakeProjectServiceStub struct{ iampb.ProjectServiceClient }

// blockingProjectService — fake ProjectServiceServer that never returns from
// Get until explicitly released (simulates a hung/stalled kacho-iam peer).
type blockingProjectService struct {
	iampb.UnimplementedProjectServiceServer
	release chan struct{}
}

func (f *blockingProjectService) Get(_ context.Context, _ *iampb.GetProjectRequest) (*iampb.Project, error) {
	<-f.release
	return &iampb.Project{Id: "prj-abc"}, nil
}

// TestProjectClient_Get_HangingPeer_BoundsToConfiguredTimeout — regression for
// the missing per-call deadline (round-6 audit finding 2, sibling client
// check_client.go DefaultCheckTimeout): a stalled kacho-iam peer must not park
// the calling goroutine forever. Get is called with a deadline-less caller
// ctx (context.Background()) — the client itself must bound the call to ~its
// configured per-call timeout and fail closed (DeadlineExceeded ->
// domain.ErrUnavailable), not hang.
func TestProjectClient_Get_HangingPeer_BoundsToConfiguredTimeout(t *testing.T) {
	fake := &blockingProjectService{release: make(chan struct{})}
	conn := startFakeIAM(t, fake, nil)

	const configuredTimeout = 100 * time.Millisecond
	c := NewProjectClientWithTimeout(conn, configuredTimeout)

	start := time.Now()
	_, err := c.Get(context.Background(), "prj-abc")
	elapsed := time.Since(start)
	// Release the still-in-flight fake handler goroutine synchronously (NOT
	// via t.Cleanup: startFakeIAM's own GracefulStop cleanup runs LIFO and
	// would deadlock waiting on this still-blocked handler otherwise).
	close(fake.release)

	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrUnavailable),
		"expected fail-closed domain.ErrUnavailable on peer hang; got %v", err)
	assert.Less(t, elapsed, 2*time.Second,
		"Get must bound to the configured per-call timeout (~%s), not hang on an unresponsive peer; took %s",
		configuredTimeout, elapsed)
}
