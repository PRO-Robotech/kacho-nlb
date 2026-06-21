package targetgroup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
)

// RBAC sub-phase D (§11, LST-1..6) per-object filtered List — TargetGroup.
// Acceptance D-41 (LST-2 byName) / D-44 (LST-5 no-leak) / D-47 (fail-closed).
// Ссылается на ещё-не-существующий internal/authzfilter + расширенный
// NewListTargetGroupsUseCase(repo, filter) → RED до GREEN.

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

// LST-2 byName: List отдаёт ровно перечисленные id; остальные отсутствуют.
func TestListTargetGroupsFilter_OnlyAccessible(t *testing.T) {
	repo := newFakeRepo()
	a := makeTG("prj-a", "tg-a1")
	b := makeTG("prj-a", "tg-a2")
	c := makeTG("prj-a", "tg-a3") // НЕ в гранте
	repo.seedTG(a)
	repo.seedTG(b)
	repo.seedTG(c)

	flt := &fakeListFilter{allowed: map[string][]string{
		"lb_target_group": {string(a.ID), string(b.ID)},
	}}
	uc := NewListTargetGroupsUseCase(repo, flt)

	resp, err := uc.Execute(ctxWithUser("usr_alice"),
		&lbv1.ListTargetGroupsRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetTargetGroups(), 2)
	got := map[string]bool{}
	for _, tg := range resp.GetTargetGroups() {
		got[tg.GetId()] = true
	}
	assert.True(t, got[string(a.ID)])
	assert.True(t, got[string(b.ID)])
	assert.False(t, got[string(c.ID)])

	// read==enforce: правильный тип + list-action (viewer relation server-side).
	assert.Equal(t, "user:usr_alice", flt.gotSubj)
	assert.Equal(t, "lb_target_group", flt.gotType)
	assert.Equal(t, "loadbalancer.targetGroups.list", flt.gotAct)
}

// LST-5 no-leak: пустой грант → пустой List.
func TestListTargetGroupsFilter_EmptyGrantEmptyList(t *testing.T) {
	repo := newFakeRepo()
	repo.seedTG(makeTG("prj-a", "tg-secret"))

	flt := &fakeListFilter{allowed: map[string][]string{}}
	uc := NewListTargetGroupsUseCase(repo, flt)

	resp, err := uc.Execute(ctxWithUser("usr_bob"),
		&lbv1.ListTargetGroupsRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	assert.Empty(t, resp.GetTargetGroups())
}

// D-47 fail-closed: ListObjects error → Unavailable.
func TestListTargetGroupsFilter_FailClosed(t *testing.T) {
	repo := newFakeRepo()
	repo.seedTG(makeTG("prj-a", "tg-a1"))

	flt := &fakeListFilter{err: status.Error(codes.Unavailable, "iam down")}
	uc := NewListTargetGroupsUseCase(repo, flt)

	_, err := uc.Execute(ctxWithUser("usr_alice"),
		&lbv1.ListTargetGroupsRequest{ProjectId: "prj-a"})
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

// nil-filter → passthrough (dev / disabled).
func TestListTargetGroupsFilter_NilFilterPassthrough(t *testing.T) {
	repo := newFakeRepo()
	repo.seedTG(makeTG("prj-a", "tg-a1"))
	repo.seedTG(makeTG("prj-a", "tg-a2"))

	uc := NewListTargetGroupsUseCase(repo, nil)
	resp, err := uc.Execute(ctxWithUser("usr_alice"),
		&lbv1.ListTargetGroupsRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetTargetGroups(), 2)
}

// bypass → all project-scoped.
func TestListTargetGroupsFilter_BypassReturnsAll(t *testing.T) {
	repo := newFakeRepo()
	repo.seedTG(makeTG("prj-a", "tg-a1"))
	repo.seedTG(makeTG("prj-a", "tg-a2"))

	flt := &fakeListFilter{bypass: true}
	uc := NewListTargetGroupsUseCase(repo, flt)
	resp, err := uc.Execute(ctxWithUser("usr_admin"),
		&lbv1.ListTargetGroupsRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetTargetGroups(), 2)
}
