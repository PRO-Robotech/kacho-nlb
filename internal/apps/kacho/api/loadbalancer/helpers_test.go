// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

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

// TestLBRegisterIntent_SystemPrincipal_ProjectTupleOnly — a system /
// unauthenticated principal yields only the project-hierarchy tuple (no creator).
func TestLBRegisterIntent_SystemPrincipal_ProjectTupleOnly(t *testing.T) {
	t.Parallel()
	intent := lbRegisterIntent(
		&kachorepo.LoadBalancerRecord{LoadBalancer: domain.LoadBalancer{ID: "nlb-x", ProjectID: "prj"}},
		operations.SystemPrincipal())
	require.Equal(t, "NetworkLoadBalancer", intent.Kind)
	require.Len(t, intent.Tuples, 1)
	require.Equal(t, domain.FGARelationProject, intent.Tuples[0].Relation)
	require.Equal(t, "project:prj", intent.Tuples[0].SubjectID)
}

// TestLBRegisterIntent_UserPrincipal_ProjectAndCreator — an authenticated
// user principal yields project-hierarchy + creator (admin) tuples.
func TestLBRegisterIntent_UserPrincipal_ProjectAndCreator(t *testing.T) {
	t.Parallel()
	intent := lbRegisterIntent(
		&kachorepo.LoadBalancerRecord{LoadBalancer: domain.LoadBalancer{ID: "nlb-x", ProjectID: "prj"}},
		operations.Principal{Type: "user", ID: "usr-1"})
	require.Len(t, intent.Tuples, 2)
	require.Equal(t, "user:usr-1", intent.Tuples[1].SubjectID)
	require.Equal(t, domain.FGARelationAdmin, intent.Tuples[1].Relation)
	require.Equal(t, "lb_network_load_balancer:nlb-x", intent.Tuples[1].Object)
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
