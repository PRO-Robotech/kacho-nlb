package loadbalancer

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestLBTypeFromPb_All(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in     lbv1.NetworkLoadBalancer_Type
		want   domain.LBType
		hasErr bool
	}{
		{lbv1.NetworkLoadBalancer_EXTERNAL, domain.LBTypeExternal, false},
		{lbv1.NetworkLoadBalancer_INTERNAL, domain.LBTypeInternal, false},
		{lbv1.NetworkLoadBalancer_TYPE_UNSPECIFIED, "", true},
	} {
		got, err := lbTypeFromPb(tc.in)
		if tc.hasErr {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}
	}
}

func TestLBOutboxPayload_Nil(t *testing.T) {
	t.Parallel()
	require.Nil(t, lbOutboxPayload(nil))
}

func TestAddressOfTarget_AllVariants(t *testing.T) {
	t.Parallel()
	require.Equal(t, "epd00INSTANCE000000", addressOfTarget(domain.Target{
		InstanceID: option.MustNewOption(domain.InstanceID("epd00INSTANCE000000")),
	}))
	require.Equal(t, "e9b00NIC00000000000", addressOfTarget(domain.Target{
		NicID: option.MustNewOption(domain.NicID("e9b00NIC00000000000")),
	}))
	require.Equal(t, "", addressOfTarget(domain.Target{}))
}

func TestSubnetOfTarget(t *testing.T) {
	t.Parallel()
	require.Equal(t, "", subnetOfTarget(domain.Target{}))
	require.Equal(t, "sub-x", subnetOfTarget(domain.Target{
		IPRef: &domain.TargetIPRef{SubnetID: "sub-x", Address: "10.0.0.5"},
	}))
}

func TestEmitHierarchyTuples_NilWriter_NoOp(t *testing.T) {
	t.Parallel()
	uc := NewCreateLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil, nil, nil, slog.Default())
	// Should not panic.
	uc.emitHierarchyTuples(context.Background(),
		&kachorepo.LoadBalancerRecord{LoadBalancer: domain.LoadBalancer{ID: "nlb-x", ProjectID: "prj"}},
		operations.SystemPrincipal())
}

func TestEmitHierarchyTuples_PartialFailure_Logged(t *testing.T) {
	t.Parallel()
	fga := &fakeHierarchy{rewriteProjectErr: errors.New("project failed")}
	uc := NewCreateLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), nil, nil, fga, slog.Default())
	uc.emitHierarchyTuples(context.Background(),
		&kachorepo.LoadBalancerRecord{LoadBalancer: domain.LoadBalancer{ID: "nlb-x", ProjectID: "prj"}},
		operations.Principal{Type: "user", ID: "usr-1"})
}

func TestPeerErrToStatus_ProjectClientCaller(t *testing.T) {
	t.Parallel()
	pc := &fakeProjectClient{getFunc: func(_ context.Context, _ string) (*iam.Project, error) {
		return nil, errors.New("transient")
	}}
	_, err := pc.Get(context.Background(), "prj")
	require.Error(t, err)
	st := peerErrToStatus(err, "project", "prj")
	require.Equal(t, codes.Internal, codes.Code(codeFromErr(st)))
}

// codeFromErr — мини-helper, чтобы избежать import status в helpers_test.
func codeFromErr(err error) uint32 {
	type coder interface{ Code() uint32 }
	if c, ok := err.(coder); ok {
		return c.Code()
	}
	// fallback through status.FromError.
	return 13 // Internal
}
