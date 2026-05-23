package compute

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	computepb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"
)

// startFakeCompute поднимает gRPC server in-memory (TCP loopback :0).
// nil-fake — services не регистрируется.
func startFakeCompute(
	t *testing.T,
	regions computepb.RegionServiceServer,
	zones computepb.ZoneServiceServer,
	instances computepb.InstanceServiceServer,
) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	if regions != nil {
		computepb.RegisterRegionServiceServer(srv, regions)
	}
	if zones != nil {
		computepb.RegisterZoneServiceServer(srv, zones)
	}
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
