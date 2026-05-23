package vpc

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
	vpcpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"
)

// startFakeVPC поднимает gRPC server in-memory (TCP loopback :0).
// nil-fake — services не регистрируется.
func startFakeVPC(
	t *testing.T,
	subnets vpcpb.SubnetServiceServer,
	nics vpcpb.NetworkInterfaceServiceServer,
	addrs vpcpb.AddressServiceServer,
	internalAddrs vpcpb.InternalAddressServiceServer,
	ops operationpb.OperationServiceServer,
) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	if subnets != nil {
		vpcpb.RegisterSubnetServiceServer(srv, subnets)
	}
	if nics != nil {
		vpcpb.RegisterNetworkInterfaceServiceServer(srv, nics)
	}
	if addrs != nil {
		vpcpb.RegisterAddressServiceServer(srv, addrs)
	}
	if internalAddrs != nil {
		vpcpb.RegisterInternalAddressServiceServer(srv, internalAddrs)
	}
	if ops != nil {
		operationpb.RegisterOperationServiceServer(srv, ops)
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
