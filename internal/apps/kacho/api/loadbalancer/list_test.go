// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

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

// TestListLoadBalancers_InvalidFilter — после унификации name=-парсера на
// shared.ParseNameFilter (kacho-corelib/filter.Parse) malformed / unknown-field
// filter — InvalidArgument (раньше loadbalancer молча игнорировал такой фильтр и
// возвращал ВСЕ project-rows; reconciled к строгой канонической семантике —
// см. shared/namefilter_test.go для полного контракта парсера).
func TestListLoadBalancers_InvalidFilter(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "edge")
	uc := NewListLoadBalancersUseCase(repo, nil)
	for _, bad := range []string{`name=edge`, `other="foo"`, `garbage`} {
		_, err := uc.Execute(context.Background(), &lbv1.ListNetworkLoadBalancersRequest{
			ProjectId: "prj-a", Filter: bad,
		})
		require.Equalf(t, codes.InvalidArgument, status.Code(err), "filter %q", bad)
	}
}
