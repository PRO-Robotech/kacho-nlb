package vpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	vpcpb "github.com/PRO-Robotech/kacho-vpc/proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeNICService — in-memory NetworkInterfaceServiceServer.
type fakeNICService struct {
	vpcpb.UnimplementedNetworkInterfaceServiceServer

	resp *vpcpb.NetworkInterface
	err  error
}

func (f *fakeNICService) Get(_ context.Context, _ *vpcpb.GetNetworkInterfaceRequest) (*vpcpb.NetworkInterface, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestNetworkInterfaceClient_Get_HappyPath(t *testing.T) {
	nic := &vpcpb.NetworkInterface{
		Id:           "e9b-nic-1",
		ProjectId:    "prj-1",
		SubnetId:     "e9b-1",
		V4AddressIds: []string{"e9b-ip-1"},
		Status:       vpcpb.NetworkInterface_ACTIVE,
	}
	conn := startFakeVPC(t, nil, &fakeNICService{resp: nic}, nil, nil, nil)
	c := NewNetworkInterfaceClient(conn)

	got, err := c.Get(ctxBackground(), "e9b-nic-1")
	require.NoError(t, err)
	assert.Equal(t, "e9b-nic-1", got.ID)
	assert.Equal(t, "prj-1", got.ProjectID)
	assert.Equal(t, "e9b-1", got.SubnetID)
	assert.Equal(t, "e9b-ip-1", got.PrimaryV4Address)
	assert.Equal(t, "ACTIVE", got.Status)
}

func TestNetworkInterfaceClient_Get_NoV4Addresses(t *testing.T) {
	nic := &vpcpb.NetworkInterface{Id: "e9b-nic-2", Status: vpcpb.NetworkInterface_PROVISIONING}
	conn := startFakeVPC(t, nil, &fakeNICService{resp: nic}, nil, nil, nil)
	c := NewNetworkInterfaceClient(conn)
	got, err := c.Get(ctxBackground(), "e9b-nic-2")
	require.NoError(t, err)
	assert.Empty(t, got.PrimaryV4Address)
}

func TestNetworkInterfaceClient_Get_NotFound(t *testing.T) {
	conn := startFakeVPC(t, nil, &fakeNICService{err: status.Error(codes.NotFound, "no nic")},
		nil, nil, nil)
	c := NewNetworkInterfaceClient(conn)
	_, err := c.Get(ctxBackground(), "e9b-nx")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestNetworkInterfaceClient_Get_PermissionDenied(t *testing.T) {
	conn := startFakeVPC(t, nil, &fakeNICService{err: status.Error(codes.PermissionDenied, "scope")},
		nil, nil, nil)
	c := NewNetworkInterfaceClient(conn)
	_, err := c.Get(ctxBackground(), "e9b-other")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
	assert.NotContains(t, err.Error(), "permission")
}

func TestNetworkInterfaceClient_Get_FailedPrecondition(t *testing.T) {
	conn := startFakeVPC(t, nil, &fakeNICService{err: status.Error(codes.FailedPrecondition, "DELETING")},
		nil, nil, nil)
	c := NewNetworkInterfaceClient(conn)
	_, err := c.Get(ctxBackground(), "e9b-deleting")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrFailedPrecondition))
}

func TestNetworkInterfaceClient_Get_Unavailable(t *testing.T) {
	conn := startFakeVPC(t, nil, &fakeNICService{err: status.Error(codes.Unavailable, "down")},
		nil, nil, nil)
	c := NewNetworkInterfaceClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Get(ctx, "e9b-nic-1")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestNetworkInterfaceClient_Get_EmptyID(t *testing.T) {
	c := NewNetworkInterfaceClient(startFakeVPC(t, nil, &fakeNICService{}, nil, nil, nil))
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestNetworkInterfaceClient_NilConn(t *testing.T) { assert.Nil(t, NewNetworkInterfaceClient(nil)) }
