// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// RBAC  per-object filtered List — Listener.
// Acceptance. Ссылается на ещё-не-существующий internal/authzfilter
// + расширенный NewListUseCase(repo, filter) → RED до GREEN.

type fakeListFilter struct {
	bypass  bool
	err     error
	allowed map[string][]string
	gotSubj string
	gotType string
	gotAct  string
}

func (f *fakeListFilter) ListAllowedIDs(_ context.Context, subject, resourceType, action string) (authzfilter.Decision, error) {
	f.gotSubj, f.gotType, f.gotAct = subject, resourceType, action
	if f.err != nil {
		return authzfilter.Decision{}, f.err
	}
	if f.bypass {
		return authzfilter.Decision{BypassAll: true}, nil
	}
	ids := f.allowed[resourceType]
	if len(ids) == 0 {
		return authzfilter.Decision{Empty: true}, nil
	}
	return authzfilter.Decision{AllowedIDs: ids}, nil
}

func ctxWithUser(id string) context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: id})
}

func seedListenerLF(t *testing.T, repo *fakeRepo, projectID, lbID, name string) string {
	t.Helper()
	id := ids.NewID(ids.PrefixListener)
	repo.seedListener(&kachorepo.ListenerRecord{
		Listener: domain.Listener{
			ID:               domain.ResourceID(id),
			LoadBalancerID:   domain.ResourceID(lbID),
			ProjectID:        domain.ProjectID(projectID),
			RegionID:         "ru-central1",
			Name:             domain.LbName(name),
			Labels:           domain.LbLabels{},
			Protocol:         domain.ProtoTCP,
			Port:             80,
			TargetPort:       80,
			IPVersion:        domain.IPVersionV4,
			AllocatedAddress: "203.0.113.10",
			Status:           domain.ListenerStatusActive,
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	return id
}

// union analog: List отдаёт только доступные listener'ы.
func TestListListenersFilter_OnlyAccessible(t *testing.T) {
	repo := newFakeRepo()
	a := seedListenerLF(t, repo, "prj-a", "nlb_lb1", "l-a1")
	b := seedListenerLF(t, repo, "prj-a", "nlb_lb1", "l-a2")
	_ = seedListenerLF(t, repo, "prj-a", "nlb_lb1", "l-a3") // НЕ в гранте

	flt := &fakeListFilter{allowed: map[string][]string{
		"lb_listener": {a, b},
	}}
	uc := NewListUseCase(repo, flt)

	resp, err := uc.Run(ctxWithUser("usr_alice"),
		&lbv1.ListListenersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetListeners(), 2)
	got := map[string]bool{}
	for _, l := range resp.GetListeners() {
		got[l.GetId()] = true
	}
	assert.True(t, got[a])
	assert.True(t, got[b])

	assert.Equal(t, "user:usr_alice", flt.gotSubj)
	assert.Equal(t, "lb_listener", flt.gotType)
	assert.Equal(t, "loadbalancer.listeners.list", flt.gotAct)
}

// no-leak: пустой грант → пустой List.
func TestListListenersFilter_EmptyGrantEmptyList(t *testing.T) {
	repo := newFakeRepo()
	seedListenerLF(t, repo, "prj-a", "nlb_lb1", "l-secret")

	flt := &fakeListFilter{allowed: map[string][]string{}}
	uc := NewListUseCase(repo, flt)

	resp, err := uc.Run(ctxWithUser("usr_bob"),
		&lbv1.ListListenersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	assert.Empty(t, resp.GetListeners())
}

// fail-closed → Unavailable.
func TestListListenersFilter_FailClosed(t *testing.T) {
	repo := newFakeRepo()
	seedListenerLF(t, repo, "prj-a", "nlb_lb1", "l-a1")

	flt := &fakeListFilter{err: status.Error(codes.Unavailable, "iam down")}
	uc := NewListUseCase(repo, flt)

	_, err := uc.Run(ctxWithUser("usr_alice"),
		&lbv1.ListListenersRequest{ProjectId: "prj-a"})
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

// nil-filter → passthrough.
func TestListListenersFilter_NilFilterPassthrough(t *testing.T) {
	repo := newFakeRepo()
	seedListenerLF(t, repo, "prj-a", "nlb_lb1", "l-a1")
	seedListenerLF(t, repo, "prj-a", "nlb_lb1", "l-a2")

	uc := NewListUseCase(repo, nil)
	resp, err := uc.Run(ctxWithUser("usr_alice"),
		&lbv1.ListListenersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetListeners(), 2)
}
