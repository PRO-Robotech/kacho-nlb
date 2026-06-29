// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package vpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	vpcpb "github.com/PRO-Robotech/kacho-vpc/proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeSecurityGroupService — in-memory SecurityGroupServiceServer.
type fakeSecurityGroupService struct {
	vpcpb.UnimplementedSecurityGroupServiceServer

	resp *vpcpb.SecurityGroup
	err  error
}

func (f *fakeSecurityGroupService) Get(_ context.Context, _ *vpcpb.GetSecurityGroupRequest) (*vpcpb.SecurityGroup, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// startFakeVPCSecurityGroup поднимает gRPC server с SecurityGroupService (TCP loopback :0).
func startFakeVPCSecurityGroup(t *testing.T, svc vpcpb.SecurityGroupServiceServer) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	vpcpb.RegisterSecurityGroupServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestSecurityGroupClient_Get_HappyPath(t *testing.T) {
	conn := startFakeVPCSecurityGroup(t, &fakeSecurityGroupService{resp: &vpcpb.SecurityGroup{
		Id:        "sgp-1",
		ProjectId: "prj-1",
		NetworkId: "enp-1",
		Name:      "web-sg",
	}})
	c := NewSecurityGroupClient(conn)
	require.NotNil(t, c)

	got, err := c.Get(ctxBackground(), "sgp-1")
	require.NoError(t, err)
	assert.Equal(t, "sgp-1", got.ID)
	assert.Equal(t, "prj-1", got.ProjectID)
	assert.Equal(t, "enp-1", got.NetworkID)
	assert.Equal(t, "web-sg", got.Name)
}

func TestSecurityGroupClient_Get_NotFoundMapsToInvalidArg(t *testing.T) {
	conn := startFakeVPCSecurityGroup(t, &fakeSecurityGroupService{err: status.Error(codes.NotFound, "no sg")})
	c := NewSecurityGroupClient(conn)
	_, err := c.Get(ctxBackground(), "sgp-nx")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestSecurityGroupClient_Get_PermissionDenied(t *testing.T) {
	conn := startFakeVPCSecurityGroup(t, &fakeSecurityGroupService{err: status.Error(codes.PermissionDenied, "scope")})
	c := NewSecurityGroupClient(conn)
	_, err := c.Get(ctxBackground(), "sgp-other")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
	assert.NotContains(t, err.Error(), "permission")
}

func TestSecurityGroupClient_Get_Unavailable(t *testing.T) {
	conn := startFakeVPCSecurityGroup(t, &fakeSecurityGroupService{err: status.Error(codes.Unavailable, "vpc down")})
	c := NewSecurityGroupClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Get(ctx, "sgp-1")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestSecurityGroupClient_Get_EmptyID(t *testing.T) {
	conn := startFakeVPCSecurityGroup(t, &fakeSecurityGroupService{})
	c := NewSecurityGroupClient(conn)
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestSecurityGroupClient_NilConn(t *testing.T) { assert.Nil(t, NewSecurityGroupClient(nil)) }
