// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package geo

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	geopb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/geo/v1"
)

// startFakeGeo поднимает gRPC server in-memory (TCP loopback :0).
// nil-fake — service не регистрируется. zones — variadic, чтобы не трогать
// существующие call-site'ы (region-only тесты продолжают звать startFakeGeo(t, regions)).
func startFakeGeo(t *testing.T, regions geopb.RegionServiceServer, zones ...geopb.ZoneServiceServer) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	if regions != nil {
		geopb.RegisterRegionServiceServer(srv, regions)
	}
	if len(zones) > 0 && zones[0] != nil {
		geopb.RegisterZoneServiceServer(srv, zones[0])
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
