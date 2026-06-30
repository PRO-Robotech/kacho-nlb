// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

func TestDelete_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	opsRepo := newFakeOpsRepo()
	uc := NewDeleteLoadBalancerUseCase(repo, opsRepo, &fakeAnycastClient{}, slog.Default())
	op, err := uc.Execute(context.Background(), &lbv1.DeleteNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.NotContains(t, repo.lbs, lbID)
	// outbox emitted DELETED.
	evts := repo.outboxEvents()
	require.Len(t, evts, 1)
	require.Equal(t, "DELETED", evts[0].Action)
}

func TestDelete_DeletionProtection(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	repo.lbs[lbID].DeletionProtection = true
	uc := NewDeleteLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeAnycastClient{}, nil)
	_, err := uc.Execute(context.Background(), &lbv1.DeleteNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "deletion_protection")
}

func TestDelete_HasListeners(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	// Seed listeners в lists map.
	repo.lists[lbID] = []*kachorepo.ListenerRecord{
		{Listener: domain.Listener{
			ID:             domain.ResourceID(ids.NewID(ids.PrefixListener)),
			LoadBalancerID: domain.ResourceID(lbID),
		}},
	}
	uc := NewDeleteLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeAnycastClient{}, nil)
	_, err := uc.Execute(context.Background(), &lbv1.DeleteNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "listener")
}

func TestDelete_HasAttachedTG(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	lbID := seedLB(t, repo, "prj-a", "edge")
	tgID := "tgr-fake"
	repo.pivot[lbID+"/"+tgID] = &kachorepo.AttachedTargetGroupRecord{
		LoadBalancerID: lbID, TargetGroupID: tgID,
	}
	uc := NewDeleteLoadBalancerUseCase(repo, newFakeOpsRepo(), &fakeAnycastClient{}, nil)
	_, err := uc.Execute(context.Background(), &lbv1.DeleteNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: lbID,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "attached target group")
}

func TestDelete_NotFound(t *testing.T) {
	t.Parallel()
	uc := NewDeleteLoadBalancerUseCase(newFakeRepo(), newFakeOpsRepo(), &fakeAnycastClient{}, nil)
	_, err := uc.Execute(context.Background(), &lbv1.DeleteNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: "nlb-x",
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}
