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

// fakeNetworkService — in-memory NetworkServiceServer.
type fakeNetworkService struct {
	vpcpb.UnimplementedNetworkServiceServer

	resp *vpcpb.Network
	err  error
}

func (f *fakeNetworkService) Get(_ context.Context, _ *vpcpb.GetNetworkRequest) (*vpcpb.Network, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

// startFakeVPCNetwork поднимает gRPC server с NetworkService (TCP loopback :0).
func startFakeVPCNetwork(t *testing.T, svc vpcpb.NetworkServiceServer) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	vpcpb.RegisterNetworkServiceServer(srv, svc)
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

func TestNetworkClient_Get_HappyPath(t *testing.T) {
	conn := startFakeVPCNetwork(t, &fakeNetworkService{resp: &vpcpb.Network{
		Id:        "enp-1",
		ProjectId: "prj-1",
		Name:      "prod-net",
	}})
	c := NewNetworkClient(conn)
	require.NotNil(t, c)

	got, err := c.Get(ctxBackground(), "enp-1")
	require.NoError(t, err)
	assert.Equal(t, "enp-1", got.ID)
	assert.Equal(t, "prj-1", got.ProjectID)
	assert.Equal(t, "prod-net", got.Name)
}

func TestNetworkClient_Get_NotFoundMapsToInvalidArg(t *testing.T) {
	conn := startFakeVPCNetwork(t, &fakeNetworkService{err: status.Error(codes.NotFound, "no network")})
	c := NewNetworkClient(conn)
	_, err := c.Get(ctxBackground(), "enp-nx")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestNetworkClient_Get_PermissionDenied(t *testing.T) {
	conn := startFakeVPCNetwork(t, &fakeNetworkService{err: status.Error(codes.PermissionDenied, "scope")})
	c := NewNetworkClient(conn)
	_, err := c.Get(ctxBackground(), "enp-other")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
	assert.NotContains(t, err.Error(), "permission")
}

func TestNetworkClient_Get_Unavailable(t *testing.T) {
	conn := startFakeVPCNetwork(t, &fakeNetworkService{err: status.Error(codes.Unavailable, "vpc down")})
	c := NewNetworkClient(conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Get(ctx, "enp-1")
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}

func TestNetworkClient_Get_EmptyID(t *testing.T) {
	conn := startFakeVPCNetwork(t, &fakeNetworkService{})
	c := NewNetworkClient(conn)
	_, err := c.Get(ctxBackground(), "")
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestNetworkClient_NilConn(t *testing.T) { assert.Nil(t, NewNetworkClient(nil)) }
