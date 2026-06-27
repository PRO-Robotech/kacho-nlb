package iam

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	iampb "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"
)

// startFakeIAM поднимает gRPC server in-memory (TCP loopback :0) — простой
// аналог bufconn, не требует bufconn pkg. Регистрирует переданные fake-server'ы;
// nil-fake — services не регистрируется.
func startFakeIAM(
	t *testing.T,
	project iampb.ProjectServiceServer,
	internal iampb.InternalIAMServiceServer,
) *grpc.ClientConn {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	if project != nil {
		iampb.RegisterProjectServiceServer(srv, project)
	}
	if internal != nil {
		iampb.RegisterInternalIAMServiceServer(srv, internal)
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

// ctxBackground — синоним для контекста без cancel/deadline в коротких тестах.
func ctxBackground() context.Context { return context.Background() }
