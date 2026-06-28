// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package compute

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	computepb "github.com/PRO-Robotech/kacho-compute/proto/gen/go/kacho/cloud/compute/v1"
)

// startFakeCompute поднимает gRPC server in-memory (TCP loopback :0).
// nil-fake — service не регистрируется. После выноса Geography в kacho-geo
// (kacho-geo) kacho-nlb зовёт у compute только InstanceService.
func startFakeCompute(
	t *testing.T,
	instances computepb.InstanceServiceServer,
) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	if instances != nil {
		computepb.RegisterInstanceServiceServer(srv, instances)
	}
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

func ctxBackground() context.Context { return context.Background() }
