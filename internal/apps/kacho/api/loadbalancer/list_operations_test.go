package loadbalancer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

func TestListOperations_RequiresID(t *testing.T) {
	t.Parallel()
	uc := NewListOperationsUseCase(newFakeOpsRepo())
	_, err := uc.Execute(context.Background(), &lbv1.ListNetworkLoadBalancerOperationsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestListOperations_HappyPath(t *testing.T) {
	t.Parallel()
	opsRepo := newFakeOpsRepo()
	// seed two operations.
	op1 := operations.Operation{
		ID:        ids.NewID(ids.PrefixOperationNLB),
		Description: "Create lb a",
	}
	op2 := operations.Operation{
		ID:        ids.NewID(ids.PrefixOperationNLB),
		Description: "Update lb a",
	}
	meta1, _ := anypb.New(&lbv1.CreateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: "nlb-a"})
	op1.Metadata = meta1
	meta2, _ := anypb.New(&lbv1.UpdateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: "nlb-a"})
	op2.Metadata = meta2
	require.NoError(t, opsRepo.Create(context.Background(), op1))
	require.NoError(t, opsRepo.Create(context.Background(), op2))

	uc := NewListOperationsUseCase(opsRepo)
	resp, err := uc.Execute(context.Background(), &lbv1.ListNetworkLoadBalancerOperationsRequest{
		NetworkLoadBalancerId: "nlb-a",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetOperations())
}
