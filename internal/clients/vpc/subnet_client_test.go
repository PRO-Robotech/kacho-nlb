// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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

	vpcpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeSubnetService — in-memory SubnetServiceServer.
type fakeSubnetService struct {
	vpcpb.UnimplementedSubnetServiceServer

	resp *vpcpb.Subnet
	err  error
}

func (f *fakeSubnetService) Get(_ context.Context, _ *vpcpb.GetSubnetRequest) (*vpcpb.Subnet, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestSubnetClient_Get_HappyPath(t *testing.T) {
	conn := startFakeVPC(t, &fakeSubnetService{resp: &vpcpb.Subnet{
		Id:           "e9b-1",
		ProjectId:    "prj-1",
		NetworkId:    "enp-1",
		ZoneId:       "ru-central1-a",
		V4CidrBlocks: []string{"10.128.0.0/24"},
		V6CidrBlocks: nil,
	}}, nil, nil, nil, nil)
	c := NewSubnetClient(conn)
	require.NotNil(t, c)

	got, err := c.Get(ctxBackground(), "e9b-1")
	require.NoError(t, err)
	assert.Equal(t, "e9b-1", got.ID)
	assert.Equal(t, "prj-1", got.ProjectID)
	assert.Equal(t, "enp-1", got.NetworkID)
	assert.Equal(t, "ru-central1-a", got.ZoneID)
	assert.Equal(t, []string{"10.128.0.0/24"}, got.V4CIDRBlocks)
	assert.Empty(t, got.V6CIDRBlocks)
	assert.Empty(t, got.RegionID, "RegionID — denormalised mirror, заполняется consumer'ом")
}

func TestSubnetClient_Get_NotFoundMapsToInvalidArg(t *testing.T) {
	conn := startFakeVPC(t, &fakeSubnetService{err: status.Error(codes.NotFound, "no subnet")},
		nil, nil, nil, nil)
	c := NewSubnetClient(conn)
	_, err := c.Get(ctxBackground(), "e9b-nx")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestSubnetClient_Get_PermissionDenied(t *testing.T) {
	conn := startFakeVPC(t, &fakeSubnetService{err: status.Error(codes.PermissionDenied, "scope")},
		nil, nil, nil, nil)
	c := NewSubnetClient(conn)
	_, err := c.Get(ctxBackground(), "e9b-other")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
	assert.NotContains(t, err.Error(), "permission")
}

func TestSubnetClient_Get_Unavailable(t *testing.T) {
	conn := startFakeVPC(t, &fakeSubnetService{err: status.Error(codes.Unavailable, "vpc down")},
		nil, nil, nil, nil)
	c := NewSubnetClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Get(ctx, "e9b-1")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestSubnetClient_Get_EmptyID(t *testing.T) {
	c := NewSubnetClient(startFakeVPC(t, &fakeSubnetService{}, nil, nil, nil, nil))
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestSubnetClient_NilConn(t *testing.T) { assert.Nil(t, NewSubnetClient(nil)) }
