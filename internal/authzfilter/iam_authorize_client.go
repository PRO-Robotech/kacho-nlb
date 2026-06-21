package authzfilter

import (
	"context"

	"google.golang.org/grpc"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// NewIAMAuthorizeClient оборачивает gRPC conn в AuthorizeClient. conn указывает на
// kacho-iam, где живёт AuthorizeService (RegisterAuthorizeServiceServer на public
// listener; ListObjects — публичный read-RPC). См. cmd composition root.
func NewIAMAuthorizeClient(conn grpc.ClientConnInterface) AuthorizeClient {
	return &grpcAuthorizeClient{cli: iamv1.NewAuthorizeServiceClient(conn)}
}

type grpcAuthorizeClient struct {
	cli iamv1.AuthorizeServiceClient
}

// ListObjects пробрасывает request в kacho-iam AuthorizeService.
//
// W1.4 (KAC-178 follow-up, зеркало compute/iam_authorize_client + nlb
// check_client): outgoing ctx обёрнут auth.PropagateOutgoing, чтобы iam-side
// grpcsrv.UnaryPrincipalExtract увидел РЕАЛЬНОГО caller'а (а не SystemPrincipal()
// = user:bootstrap). Без wrap'а iam authzguard видит "system:bootstrap" и отбивает
// ListObjects → nlb list-filter возвращал бы 403/Unavailable для всех user'ов.
func (g *grpcAuthorizeClient) ListObjects(ctx context.Context, req *iamv1.ListObjectsRequest, opts ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	return g.cli.ListObjects(auth.PropagateOutgoing(ctx), req, opts...)
}
