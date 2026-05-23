package compute

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	computepb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeInstanceService — in-memory InstanceServiceServer.
type fakeInstanceService struct {
	computepb.UnimplementedInstanceServiceServer

	resp *computepb.Instance
	err  error
}

func (f *fakeInstanceService) Get(_ context.Context, _ *computepb.GetInstanceRequest) (*computepb.Instance, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestInstanceClient_Get_HappyPath(t *testing.T) {
	inst := &computepb.Instance{
		Id:        "epd-1",
		ProjectId: "prj-1",
		ZoneId:    "ru-central1-a",
		Name:      "vm-1",
		Status:    computepb.Instance_RUNNING,
		NetworkInterfaces: []*computepb.NetworkInterface{
			{
				Index:    "0",
				SubnetId: "e9b-1",
				PrimaryV4Address: &computepb.PrimaryAddress{
					Address: "10.128.0.42",
				},
			},
		},
	}
	conn := startFakeCompute(t, nil, nil, &fakeInstanceService{resp: inst})
	c := NewInstanceClient(conn)

	got, err := c.Get(ctxBackground(), "epd-1")
	require.NoError(t, err)
	assert.Equal(t, "epd-1", got.ID)
	assert.Equal(t, "prj-1", got.ProjectID)
	assert.Equal(t, "ru-central1-a", got.ZoneID)
	assert.Equal(t, "vm-1", got.Name)
	assert.Equal(t, "10.128.0.42", got.PrimaryNICAddress)
	assert.Equal(t, "RUNNING", got.Status)
}

func TestInstanceClient_Get_NoNICReturnsEmptyAddress(t *testing.T) {
	inst := &computepb.Instance{Id: "epd-2", ProjectId: "p", Status: computepb.Instance_PROVISIONING}
	conn := startFakeCompute(t, nil, nil, &fakeInstanceService{resp: inst})
	c := NewInstanceClient(conn)

	got, err := c.Get(ctxBackground(), "epd-2")
	require.NoError(t, err)
	assert.Empty(t, got.PrimaryNICAddress)
}

func TestInstanceClient_Get_NotFoundMapsToInvalidArg(t *testing.T) {
	conn := startFakeCompute(t, nil, nil, &fakeInstanceService{err: status.Error(codes.NotFound, "no instance")})
	c := NewInstanceClient(conn)
	_, err := c.Get(ctxBackground(), "epd-missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg), "expected ErrInvalidArg: %v", err)
}

func TestInstanceClient_Get_PermissionDeniedMapsToInvalidArg(t *testing.T) {
	conn := startFakeCompute(t, nil, nil, &fakeInstanceService{err: status.Error(codes.PermissionDenied, "denied")})
	c := NewInstanceClient(conn)
	_, err := c.Get(ctxBackground(), "epd-other")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
	assert.NotContains(t, err.Error(), "permission")
}

func TestInstanceClient_Get_FailedPrecondition(t *testing.T) {
	conn := startFakeCompute(t, nil, nil,
		&fakeInstanceService{err: status.Error(codes.FailedPrecondition, "DELETING")})
	c := NewInstanceClient(conn)
	_, err := c.Get(ctxBackground(), "epd-x")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrFailedPrecondition))
}

func TestInstanceClient_Get_Unavailable(t *testing.T) {
	conn := startFakeCompute(t, nil, nil,
		&fakeInstanceService{err: status.Error(codes.Unavailable, "down")})
	c := NewInstanceClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()

	_, err := c.Get(ctx, "epd-x")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestInstanceClient_Get_EmptyID(t *testing.T) {
	c := NewInstanceClient(startFakeCompute(t, nil, nil, &fakeInstanceService{}))
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestInstanceClient_NilConn(t *testing.T) {
	assert.Nil(t, NewInstanceClient(nil))
}
