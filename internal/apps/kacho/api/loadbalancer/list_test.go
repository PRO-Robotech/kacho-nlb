package loadbalancer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func seedLB(t *testing.T, repo *fakeRepo, projectID, name string) string {
	t.Helper()
	id := ids.NewID(ids.PrefixLoadBalancer)
	repo.lbs[id] = &kachorepo.LoadBalancerRecord{
		LoadBalancer: domain.LoadBalancer{
			ID: domain.ResourceID(id), ProjectID: domain.ProjectID(projectID),
			RegionID: "ru-central1", Name: domain.LbName(name),
			Type: domain.LBTypeExternal, Status: domain.LBStatusInactive,
			SessionAffinity: domain.SessionAffinity5Tuple,
		},
	}
	return id
}

func TestListLoadBalancers_RequiresProjectID(t *testing.T) {
	t.Parallel()
	uc := NewListLoadBalancersUseCase(newFakeRepo(), nil)
	_, err := uc.Execute(context.Background(), &lbv1.ListNetworkLoadBalancersRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestListLoadBalancers_FiltersByProject(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "lb-a1")
	seedLB(t, repo, "prj-a", "lb-a2")
	seedLB(t, repo, "prj-b", "lb-b1")
	uc := NewListLoadBalancersUseCase(repo, nil)
	resp, err := uc.Execute(context.Background(), &lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetNetworkLoadBalancers(), 2)
}

func TestListLoadBalancers_FilterName(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "edge")
	seedLB(t, repo, "prj-a", "api")
	uc := NewListLoadBalancersUseCase(repo, nil)
	resp, err := uc.Execute(context.Background(), &lbv1.ListNetworkLoadBalancersRequest{
		ProjectId: "prj-a", Filter: `name="edge"`,
	})
	require.NoError(t, err)
	require.Len(t, resp.GetNetworkLoadBalancers(), 1)
	require.Equal(t, "edge", resp.GetNetworkLoadBalancers()[0].GetName())
}

func TestParseFilterName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`name="edge"`:  "edge",
		`name=edge`:    "edge",
		`name="api-1"`: "api-1",
		``:             "",
		`other=foo`:    "",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			require.Equal(t, want, parseFilterName(in))
		})
	}
}
