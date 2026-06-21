package loadbalancer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
)

// RBAC sub-phase D (§11, LST-1..6) per-object filtered List — kacho-nlb consumer.
// Acceptance D-40..D-47 (docs/specs/rbac-rules-model-2026-acceptance.md):
//   - LST-2 byName / LST-3 global: List отдаёт только доступные объекты (union
//     армов) — пересечение repo-rows с FGA ListObjects(subject,"...","lb_*").
//   - LST-5 no-leak: объект вне грантов отсутствует в List И Get→NotFound.
//   - read==enforce: List-видимость = Check-allow (одна tuple-база, relation viewer).
//   - D-47 fail-closed: IAM недоступен → Unavailable (НЕ нефильтрованный список).
//
// Эти тесты ссылаются на ещё-не-существующий internal/authzfilter и на
// расширенный конструктор NewListLoadBalancersUseCase(repo, filter) →
// компилятор/прогон падают (RED) до GREEN-реализации под-фазы D.

// fakeListFilter — in-memory authzfilter.Filter для unit-тестов.
//
//   - bypass=true → Decision{BypassAll:true} (admin / nil-filter / wildcard-grant).
//   - err!=nil    → возвращается как есть (fail-closed Unavailable у use-case).
//   - allowed     → per (resourceType) explicit allow-list; nil-map → пусто.
type fakeListFilter struct {
	bypass  bool
	err     error
	allowed map[string][]string // resourceType → ids
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

// ctxWithUser возвращает ctx с user-principal (FGA subject "user:<id>").
func ctxWithUser(id string) context.Context {
	return operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: id})
}

// LST-3 global: subject с list-грантом видит все доступные LB; чужие отсутствуют.
func TestListLoadBalancersFilter_OnlyAccessible(t *testing.T) {
	repo := newFakeRepo()
	a := seedLB(t, repo, "prj-a", "lb-a1")
	b := seedLB(t, repo, "prj-a", "lb-a2")
	_ = seedLB(t, repo, "prj-a", "lb-a3") // НЕ в гранте → не должен попасть в List

	flt := &fakeListFilter{allowed: map[string][]string{
		"lb_network_load_balancer": {a, b},
	}}
	uc := NewListLoadBalancersUseCase(repo, flt)

	resp, err := uc.Execute(ctxWithUser("usr_alice"),
		&lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetNetworkLoadBalancers(), 2)
	got := map[string]bool{}
	for _, lb := range resp.GetNetworkLoadBalancers() {
		got[lb.GetId()] = true
	}
	assert.True(t, got[a])
	assert.True(t, got[b])

	// read==enforce: фильтр спрошен с relation viewer-action на правильном типе.
	assert.Equal(t, "user:usr_alice", flt.gotSubj)
	assert.Equal(t, "lb_network_load_balancer", flt.gotType)
	assert.Equal(t, "loadbalancer.networkLoadBalancers.list", flt.gotAct)
}

// LST-5 no-leak: пустой грант → пустой List (НЕ ошибка, НЕ leak).
func TestListLoadBalancersFilter_EmptyGrantEmptyList(t *testing.T) {
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "lb-secret")

	flt := &fakeListFilter{allowed: map[string][]string{}} // нет грантов
	uc := NewListLoadBalancersUseCase(repo, flt)

	resp, err := uc.Execute(ctxWithUser("usr_bob"),
		&lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	assert.Empty(t, resp.GetNetworkLoadBalancers())
	assert.Empty(t, resp.GetNextPageToken())
}

// D-47 fail-closed: IAM ListObjects error → Unavailable (НЕ нефильтрованный список).
func TestListLoadBalancersFilter_FailClosed(t *testing.T) {
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "lb-a1")

	flt := &fakeListFilter{err: status.Error(codes.Unavailable, "iam down")}
	uc := NewListLoadBalancersUseCase(repo, flt)

	_, err := uc.Execute(ctxWithUser("usr_alice"),
		&lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}

// bypass: BypassAll (admin / wildcard) → нефильтрованный project-scoped список.
func TestListLoadBalancersFilter_BypassReturnsAll(t *testing.T) {
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "lb-a1")
	seedLB(t, repo, "prj-a", "lb-a2")

	flt := &fakeListFilter{bypass: true}
	uc := NewListLoadBalancersUseCase(repo, flt)

	resp, err := uc.Execute(ctxWithUser("usr_admin"),
		&lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetNetworkLoadBalancers(), 2)
}

// nil-filter (list-filter disabled / dev) → нефильтрованный passthrough.
func TestListLoadBalancersFilter_NilFilterPassthrough(t *testing.T) {
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "lb-a1")
	seedLB(t, repo, "prj-a", "lb-a2")

	uc := NewListLoadBalancersUseCase(repo, nil)
	resp, err := uc.Execute(ctxWithUser("usr_alice"),
		&lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetNetworkLoadBalancers(), 2)
}

// system-principal (нет user-identity) → bypass (фоновые / dev вызовы).
func TestListLoadBalancersFilter_SystemSubjectPassthrough(t *testing.T) {
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "lb-a1")

	flt := &fakeListFilter{allowed: map[string][]string{}} // даже с пустым грантом — system bypass
	uc := NewListLoadBalancersUseCase(repo, flt)

	// background ctx → SystemPrincipal → subject "" → bypass без ListObjects.
	resp, err := uc.Execute(context.Background(),
		&lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetNetworkLoadBalancers(), 1)
}

// errFromFilter — guard: фильтр возвращает не-status ошибку → всё равно Unavailable.
func TestListLoadBalancersFilter_GenericErrIsUnavailable(t *testing.T) {
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "lb-a1")

	flt := &fakeListFilter{err: errors.New("boom")}
	uc := NewListLoadBalancersUseCase(repo, flt)

	_, err := uc.Execute(ctxWithUser("usr_alice"),
		&lbv1.ListNetworkLoadBalancersRequest{ProjectId: "prj-a"})
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
}
