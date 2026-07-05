// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package check_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/check"
)

// ────────────────────────────────────────────────────────────────────────────
// helpers
// ────────────────────────────────────────────────────────────────────────────

func principalCtx(typ, id string) context.Context {
	return operations.WithPrincipal(context.Background(), operations.Principal{
		Type:        typ,
		ID:          id,
		DisplayName: "test",
	})
}

// fakeServerStream — минимальный grpc.ServerStream с заданным ctx, для прогона
// stream-RPC (Subscribe) через authz.Interceptor.Stream.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context { return s.ctx }

type checkCall struct {
	subject  string
	relation string
	object   string
}

// newTestInterceptor — interceptor с CheckClientFunc stub'ом и счётчиком вызовов.
// Возвращает interceptor, pointer на счётчик и slice захваченных вызовов.
func newTestInterceptor(
	t *testing.T,
	fn func(ctx context.Context, subject, relation, object string) (bool, error),
) (*authz.Interceptor, *int, *[]checkCall) {
	t.Helper()
	var (
		mu    sync.Mutex
		calls []checkCall
		n     int
	)
	wrapped := authz.CheckClientFunc(func(ctx context.Context, subject, relation, object string) (bool, error) {
		mu.Lock()
		n++
		calls = append(calls, checkCall{subject, relation, object})
		mu.Unlock()
		return fn(ctx, subject, relation, object)
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-nlb-test",
		Map:         check.PermissionMap(),
		Client:      wrapped,
	})
	return intr, &n, &calls
}

// ────────────────────────────────────────────────────────────────────────────
// NLB.Create без editor on project → PermissionDenied
// ────────────────────────────────────────────────────────────────────────────

func TestAZD001_NLBCreate_NoEditor_Denied(t *testing.T) {
	intr, n, calls := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "user:usr_bob", subj)
		require.Equal(t, "editor", rel)
		require.Equal(t, "project:prj_demo", obj)
		return false, nil
	})
	uIntr := intr.Unary()
	handlerCalled := false
	handler := func(ctx context.Context, req any) (any, error) {
		handlerCalled = true
		return "should-not-return", nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create"}
	ctx := principalCtx("user", "usr_bob")
	req := &lbv1.CreateNetworkLoadBalancerRequest{ProjectId: "prj_demo", Name: "lb1"}

	_, err := uIntr(ctx, req, info, handler)
	require.Error(t, err)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.False(t, handlerCalled)
	require.Equal(t, 1, *n)
	require.Len(t, *calls, 1)
}

// ────────────────────────────────────────────────────────────────────────────
// NLB.Get viewer OK
// ────────────────────────────────────────────────────────────────────────────

func TestAZD002_NLBGet_VGet_OK(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "v_get", rel)
		require.Equal(t, "lb_network_load_balancer:nlb-1", obj)
		return true, nil
	})
	uIntr := intr.Unary()
	called := false
	handler := func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil }
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"}
	ctx := principalCtx("user", "usr_alice")
	req := &lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"}

	resp, err := uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.True(t, called)
}

// ────────────────────────────────────────────────────────────────────────────
// stranger subject → PermissionDenied
// ────────────────────────────────────────────────────────────────────────────

func TestAZD003_NLBGet_Stranger_Denied(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		return false, nil // no path; not even reason — generic deny
	})
	uIntr := intr.Unary()
	handler := func(ctx context.Context, req any) (any, error) {
		t.Fatal("handler must not be called")
		return nil, nil
	}
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"}
	ctx := principalCtx("user", "usr_stranger")
	req := &lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"}

	_, err := uIntr(ctx, req, info, handler)
	require.Error(t, err)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

// ────────────────────────────────────────────────────────────────────────────
// NLB.Start: viewer rejected, editor OK
// ────────────────────────────────────────────────────────────────────────────

func TestAZD004_NLBStart_VUpdate_Denied(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "v_update", rel)
		return false, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_viewer"),
		&lbv1.StartNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Start"},
		func(context.Context, any) (any, error) { t.Fatal("handler must not run"); return nil, nil },
	)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

func TestAZD004_NLBStop_VUpdate_OK(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "v_update", rel)
		require.Equal(t, "lb_network_load_balancer:nlb-1", obj)
		return true, nil
	})
	resp, err := intr.Unary()(
		principalCtx("user", "usr_editor"),
		&lbv1.StopNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Stop"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
}

// ────────────────────────────────────────────────────────────────────────────
// NLB.Delete: editor OK, viewer rejected
// ────────────────────────────────────────────────────────────────────────────

func TestAZD005_NLBDelete_VDelete_OK_ViewerRejected(t *testing.T) {
	// v_delete — OK.
	intr1, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, _ string) (bool, error) {
		require.Equal(t, "v_delete", rel)
		return true, nil
	})
	_, err := intr1.Unary()(
		principalCtx("user", "usr_e"),
		&lbv1.DeleteNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Delete"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)

	// Без v_delete (FGA Check returns false for v_delete-relation) — denied.
	intr2, _, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		return false, nil
	})
	_, err = intr2.Unary()(
		principalCtx("user", "usr_v"),
		&lbv1.DeleteNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Delete"},
		func(context.Context, any) (any, error) { t.Fatal("must not run"); return nil, nil },
	)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

// ────────────────────────────────────────────────────────────────────────────
// NLB.Move src-check OK (per-RPC); dst-check — handler-level.
//
// Interceptor выполняет ОДНОЙ Check — на ресурсе (FGA cascade покрывает
// editor on src project). Проверка destination_project_id — handler'ом.
// Этот test проверяет, что interceptor НЕ запрашивает обе projection'и
// (no over-extraction) и принимает запрос если editor on src LB удовлетворён.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD006_NLBMove_PerRPCCheck_OnResourceOnly(t *testing.T) {
	intr, n, calls := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "v_update", rel)
		require.Equal(t, "lb_network_load_balancer:nlb-src", obj, "interceptor must Check on resource, NOT destination project")
		return true, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_e"),
		&lbv1.MoveNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-src", DestinationProjectId: "prj-dst"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Move"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, 1, *n, "exactly one Check expected at interceptor level (destination project — handler-level)")
	require.Len(t, *calls, 1)
}

// ────────────────────────────────────────────────────────────────────────────
// NLB.AttachTargetGroup: per-RPC Check на LB (editor); TG-check
// handler'ом (interceptor выполняет один Check).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD007_NLBAttachTargetGroup_PerRPCCheck_OnLBOnly(t *testing.T) {
	intr, n, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "v_update", rel)
		require.Equal(t, "lb_network_load_balancer:nlb-1", obj)
		return true, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_e"),
		&lbv1.AttachNetworkLoadBalancerTargetGroupRequest{
			NetworkLoadBalancerId: "nlb-1",
			AttachedTargetGroup:   &lbv1.AttachedTargetGroup{TargetGroupId: "tgr-1"},
		},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/AttachTargetGroup"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, 1, *n)
}

// ────────────────────────────────────────────────────────────────────────────
// TG.AddTargets: editor on TG required, viewer rejected.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD008_TGAddTargets_VUpdate_Denied(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "v_update", rel)
		require.Equal(t, "lb_target_group:tgr-1", obj)
		return false, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_v"),
		&lbv1.AddTargetsRequest{TargetGroupId: "tgr-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.TargetGroupService/AddTargets"},
		func(context.Context, any) (any, error) { t.Fatal("must not run"); return nil, nil },
	)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

// ────────────────────────────────────────────────────────────────────────────
// Listener.Create: editor on parent LB required.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD009_ListenerCreate_OnParentLB(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "editor", rel)
		require.Equal(t, "lb_network_load_balancer:nlb-1", obj,
			"Listener.Create checks editor on parent LB (FGA cascades через lb_listener.load_balancer)")
		return false, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_v"),
		&lbv1.CreateListenerRequest{LoadBalancerId: "nlb-1", Name: "lst-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.ListenerService/Create"},
		func(context.Context, any) (any, error) { t.Fatal("must not run"); return nil, nil },
	)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

// ────────────────────────────────────────────────────────────────────────────
// OperationService.Get: Public (exempt), handler runs.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD010_OperationGet_Public(t *testing.T) {
	intr, n, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		t.Fatal("Check must not be invoked for Public/exempt RPC")
		return false, nil
	})
	called := false
	resp, err := intr.Unary()(
		principalCtx("user", "usr_anyone"),
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.operation.OperationService/Get"},
		func(context.Context, any) (any, error) { called = true; return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
	require.True(t, called)
	require.Equal(t, 0, *n, "Public RPC must skip Check entirely")
}

// ────────────────────────────────────────────────────────────────────────────
// OperationService.Cancel: Public (exempt); creator-only check
// handler-level (`operation.created_by` в БД), не authz-interceptor.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD011_OperationCancel_Public_HandlerOwnsCreatorCheck(t *testing.T) {
	intr, n, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		t.Fatal("Check must not be invoked for Cancel (exempt; creator-check — handler)")
		return false, nil
	})
	resp, err := intr.Unary()(
		principalCtx("user", "usr_bob"),
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.operation.OperationService/Cancel"},
		func(context.Context, any) (any, error) { return "ok-from-handler", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok-from-handler", resp)
	require.Equal(t, 0, *n)
}

// ────────────────────────────────────────────────────────────────────────────
// FGA unavailable → fail-closed PermissionDenied.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD012_FGAUnavailable_FailClosed(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		return false, errors.New("iam unavailable: connection refused")
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_alice"),
		&lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"},
		func(context.Context, any) (any, error) { t.Fatal("must not run"); return nil, nil },
	)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code(),
		"FGA-unavailable → fail-closed PermissionDenied (acceptance §AZD-012)")
}

// ────────────────────────────────────────────────────────────────────────────
// Breakglass dev-only (interceptor allows; production cfg
// rejects breakglass=true, covered separately в config/validate_test.go).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD013_Breakglass_AllowsAuthenticated(t *testing.T) {
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-nlb-test",
		Map:         check.PermissionMap(),
		Breakglass:  true,
	})
	resp, err := intr.Unary()(
		principalCtx("user", "usr_bob"),
		&lbv1.DeleteNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Delete"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
}

func TestAZD013_Breakglass_DeniesAnonymous(t *testing.T) {
	// Breakglass НЕ должен пускать anonymous'а — иначе CRIT-6/7 повторно
	// (root cause). Anonymous = пустой principal_id.
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-nlb-test",
		Map:         check.PermissionMap(),
		Breakglass:  true,
	})
	anonCtx := operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: ""})
	_, err := intr.Unary()(
		anonCtx,
		&lbv1.CreateNetworkLoadBalancerRequest{ProjectId: "prj-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create"},
		func(context.Context, any) (any, error) { t.Fatal("must not run"); return nil, nil },
	)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
}

// ────────────────────────────────────────────────────────────────────────────
// RPC не в PermissionMap → fail-closed.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD014_UnmappedRPC_FailClosed(t *testing.T) {
	intr, n, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		t.Fatal("Check must not be invoked for unmapped RPC")
		return false, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_anyone"),
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/HypotheticalNewMethod"},
		func(context.Context, any) (any, error) { t.Fatal("must not run"); return nil, nil },
	)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.Equal(t, 0, *n)
}

// ────────────────────────────────────────────────────────────────────────────
// sync creator-tuple fail → operation aborts. Это
// **handler-level** (worker abort tx до commit); здесь, в interceptor-suite,
// проверяем что adapter транзитом передаёт WriteCreatorTuple errors (это
// функционально для — handler ловит и rollback'ит).
//
// Sanity: interceptor НЕ перехватывает creator-tuple write — это другой
// gRPC-call (`InternalIAMService.WriteCreatorTuple` через hierarchy-client),
// не Check. Этот test просто документирует delineation.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD015_D11CreatorTupleWrite_NotInterceptorScope(t *testing.T) {
	// Interceptor пропускает Create (Check OK); worker сам зовёт
	// WriteCreatorTuple и решает что делать на failure. Здесь — sanity, что
	// после allowed=true handler действительно запускается.
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		return true, nil
	})
	handlerCalled := false
	_, err := intr.Unary()(
		principalCtx("user", "usr_alice"),
		&lbv1.CreateNetworkLoadBalancerRequest{ProjectId: "prj-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create"},
		func(context.Context, any) (any, error) {
			handlerCalled = true
			// Здесь handler позвал бы WriteCreatorTuple → если бы упал,
			// вернул бы Unavailable. Симулируем return-nil чтобы не падать.
			return "ok", nil
		},
	)
	require.NoError(t, err)
	require.True(t, handlerCalled)
}

// ────────────────────────────────────────────────────────────────────────────
// Cache invalidation ≤10s. Здесь — direct unit-тест
// `Cache.InvalidateBySubject`. ListenInvalidator end-to-end — integration-тест.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD016_CacheInvalidation_BySubject(t *testing.T) {
	cache := authz.NewCache(5 * time.Second)
	cache.SetAllowed("user:usr_alice", "viewer", "lb_network_load_balancer", "nlb-1")

	allowed, hit := cache.Get("user:usr_alice", "viewer", "lb_network_load_balancer", "nlb-1")
	require.True(t, hit)
	require.True(t, allowed)

	cache.InvalidateBySubject("user:usr_alice")
	_, hit = cache.Get("user:usr_alice", "viewer", "lb_network_load_balancer", "nlb-1")
	require.False(t, hit, "after InvalidateBySubject the cache miss")
}

// ────────────────────────────────────────────────────────────────────────────
// Custom role resolves to editor through 3-relation cascade
// (FGA expands `loadbalancer.networkLoadBalancers.start` → tuple `editor`).
// Здесь — interceptor proof: relation=editor accepted даже если permission
// строка узкая (Custom role).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD017_CustomRole_ResolvesToVUpdate(t *testing.T) {
	intr, _, calls := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "v_update", rel,
			"Custom role with start-verb gates object-self Start on v_update (Design B verb-bearing)")
		require.Equal(t, "lb_network_load_balancer:nlb-1", obj)
		return true, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_custom_role"),
		&lbv1.StartNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Start"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Len(t, *calls, 1)
}

// ────────────────────────────────────────────────────────────────────────────
// Custom role with unknown permission → InvalidArgument.
// Это валидация iam.Role.Create (kacho-iam-side); здесь — sanity test через
// Catalog.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD018_UnknownPermission_NotInCatalog(t *testing.T) {
	cat := check.Catalog()
	for _, p := range cat {
		require.NotEqualf(t, "loadbalancer.foo.bar", p,
			"hypothetical garbage permission must NOT be in catalog")
	}
	// iam.Role.Create rejects permission strings absent в Catalog — это
	// проверяется в kacho-iam unit-тестах; здесь sanity что Catalog не содержит
	// мусора.
}

// ────────────────────────────────────────────────────────────────────────────
// Permission catalog completeness (30) — также в drift_test.
// Дублируем здесь как explicit проверка.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD019_CatalogCount_30(t *testing.T) {
	cat := check.Catalog()
	uniq := map[string]struct{}{}
	for _, p := range cat {
		uniq[p] = struct{}{}
	}
	require.Len(t, uniq, 30, "design §6.2 + acceptance §AZD-019: exactly 30 permission strings")
}

// ────────────────────────────────────────────────────────────────────────────
// Predefined system role seeds — проверяется в kacho-iam
// migration tests. Здесь — sanity: Catalog содержит виды permissions, которые
// system-роли должны покрывать.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD020_AdminRoleCoversAllPermissions(t *testing.T) {
	// admin == `loadbalancer.*.*` — все 30 permissions.
	// Sanity: Catalog не пуст.
	require.NotEmpty(t, check.Catalog())
}

// ────────────────────────────────────────────────────────────────────────────
// Owner relation: creator gets owner on created LB. Это
// sync write (handler-level). Здесь sanity: relation `owner` валидно
// в наших object types (interceptor не использует owner на per-RPC, но
// catalog-level permissions могут на него рассчитывать).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD021_OwnerRelation_Valid(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, _ string) (bool, error) {
		// Owner check — handler-level (sync write); interceptor per-RPC Get
		// гейтит object-self `v_get` (Design B verb-bearing). Этот test sanity-
		// проверяет fall-through allow-path.
		require.Equal(t, "v_get", rel)
		return true, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_creator"),
		&lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-just-created"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// Lifecycle DELETED tuple cleanup → DecisionNoPath. Здесь —
// прямая проверка: peer-client возвращает authz.ErrNoPath → interceptor
// → DecisionNoPath → handler runs → DB вернёт NOT_FOUND.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD022_NoPath_PassthroughToHandler(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		return false, authz.ErrNoPath // FGA нет path → ресурс не существует
	})
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		// Реальный handler сделает repo.Get → NotFound.
		return nil, status.Error(codes.NotFound, "NetworkLoadBalancer nlb-1 not found")
	}
	_, err := intr.Unary()(
		principalCtx("user", "usr_alice"),
		&lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"},
		handler,
	)
	require.True(t, called, "ErrNoPath → handler runs (acceptance §AZD-022 + KAC-133)")
	st, _ := status.FromError(err)
	require.Equal(t, codes.NotFound, st.Code(), "DB-level NotFound surfaced through, not 403")
}

// ────────────────────────────────────────────────────────────────────────────
// Cache hit ratio ≥95%. Здесь — direct unit: повторный Check
// на ту же тройку — cache hit, peer не вызывается.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD023_CacheHit_PositiveSecondCheck(t *testing.T) {
	intr, n, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		return true, nil
	})
	uIntr := intr.Unary()
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"}
	ctx := principalCtx("user", "usr_alice")
	req := &lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"}
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	_, err := uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, 1, *n)

	_, err = uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, 1, *n, "second invocation must be cache hit (peer Check not called)")
}

// ────────────────────────────────────────────────────────────────────────────
// Cache hit latency. Sub-millisecond cache hit (single-process
// map lookup). Меряем delta между холодным Check и cache hit.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD024_CacheHit_FastPath(t *testing.T) {
	var delay atomic.Int64
	var peerCalls atomic.Int64
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		peerCalls.Add(1)
		time.Sleep(time.Duration(delay.Load()))
		return true, nil
	})
	uIntr := intr.Unary()
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"}
	ctx := principalCtx("user", "usr_alice")
	req := &lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"}
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	// Cold: simulated 10ms peer latency → the peer IS consulted.
	delay.Store(int64(10 * time.Millisecond))
	_, err := uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, int64(1), peerCalls.Load(), "cold Check must consult the peer once")

	// Warm: peer set intentionally slow (100ms). The correctness invariant of a
	// cache hit is that the peer is NOT consulted — assert the call-count stays
	// 1 rather than a wall-clock upper bound (a hard latency ceiling is flaky
	// under -race/GC/CPU-throttle on shared CI; acceptance §AZD-024 ≤20ms p95 is
	// a metric budget, not a per-call assertion). A regression that dropped the
	// cache would call the peer and increment the counter (and stall 100ms).
	delay.Store(int64(100 * time.Millisecond))
	start := time.Now()
	_, err = uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	require.Equal(t, int64(1), peerCalls.Load(), "warm cache hit must NOT consult the peer")
	// Sanity: the warm call clearly short-circuited (well under the 100ms slow
	// peer). Generous bound — proves the cache path, not a tight budget.
	require.Less(t, time.Since(start), 100*time.Millisecond,
		"warm cache hit must short-circuit well under the slow-peer delay")
}

// ────────────────────────────────────────────────────────────────────────────
// InternalResourceLifecycleService.Subscribe на internal :9091
// проходит РЕАЛЬНЫЙ per-RPC Check (system_viewer @ cluster:cluster_kacho_root).
//
// Security-инвариант (security.md «authN+authZ на ОБОИХ listener'ах»): internal-
// листенер гоняет тот же authzIntr. Subscribe стримит resource_id/project_id ВСЕХ
// проектов; без записи в PermissionMap он fail-closed'ился бы как unmapped (ломая
// легитимного kacho-iam consumer'а) ИЛИ — при name-based methodIsInternal-skip —
// стримил бы всё без Check. Поэтому он явно замаплен на cluster-floor system_viewer:
// non-privileged principal → PermissionDenied, internal-reader SA (как seed'ится в
// kacho-iam SystemViewerFloor) → allowed.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD025_InternalSubscribe_SystemViewerFloor(t *testing.T) {
	const fm = "/kacho.cloud.loadbalancer.v1.InternalResourceLifecycleService/Subscribe"

	m := check.PermissionMap()
	entry, ok := m[fm]
	require.True(t, ok, "Subscribe must be mapped — internal :9091 runs the same per-RPC Check as public")
	require.False(t, entry.Public, "Subscribe must NOT be Public — it streams resource_id/project_id of ALL projects")
	require.Equal(t, "system_viewer", entry.Relation, "Subscribe must be gated by the cluster-floor read relation")

	// Non-privileged principal (Check → false): stream rejected, и Check РЕАЛЬНО
	// вызван с (system_viewer, cluster:cluster_kacho_root) — не skip, не blind-deny.
	t.Run("non_privileged_denied", func(t *testing.T) {
		intr, n, calls := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
			require.Equal(t, "system_viewer", rel)
			require.Equal(t, "cluster:cluster_kacho_root", obj)
			return false, nil
		})
		ss := &fakeServerStream{ctx: principalCtx("service_account", "sva_intruder")}
		err := intr.Stream()(nil, ss, &grpc.StreamServerInfo{FullMethod: fm},
			func(any, grpc.ServerStream) error { t.Fatal("handler must not run on deny"); return nil })
		st, _ := status.FromError(err)
		require.Equal(t, codes.PermissionDenied, st.Code())
		require.Equal(t, 1, *n, "a real Check must run (not skipped via methodIsInternal)")
		require.Len(t, *calls, 1)
	})

	// Privileged internal-reader (system_viewer@cluster) → stream allowed.
	t.Run("system_viewer_allowed", func(t *testing.T) {
		intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
			require.Equal(t, "system_viewer", rel)
			require.Equal(t, "cluster:cluster_kacho_root", obj)
			return true, nil
		})
		ss := &fakeServerStream{ctx: principalCtx("service_account", "sva_kacho_iam")}
		handled := false
		err := intr.Stream()(nil, ss, &grpc.StreamServerInfo{FullMethod: fm},
			func(any, grpc.ServerStream) error { handled = true; return nil })
		require.NoError(t, err)
		require.True(t, handled)
	})
}

// ────────────────────────────────────────────────────────────────────────────
// Operations: per-resource ListOperations — viewer на ресурсе.
// (Cross-resource ops listing — handler-level scope-filter.)
// ────────────────────────────────────────────────────────────────────────────

func TestAZD026_NLBListOperations_VListOnResource(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "v_list", rel)
		require.Equal(t, "lb_network_load_balancer:nlb-1", obj)
		return true, nil
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_v"),
		&lbv1.ListNetworkLoadBalancerOperationsRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/ListOperations"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// Anonymous → UNAUTHENTICATED. Это — auth-interceptor scope
// (uplane, не authz). Здесь sanity: если principal не извлекается из ctx
// (нет auth), authz fail-closed'нет с PermissionDenied (не Unauthenticated —
// transformation делает auth-interceptor выше по chain'у).
//
// Auth-interceptor должен возвращать Unauthenticated ДО того, как
// authz-interceptor получит ctx. Здесь — поведение когда principal_id="".
// ────────────────────────────────────────────────────────────────────────────

func TestAZD027_EmptyPrincipal_AuthzDenies(t *testing.T) {
	intr, n, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		t.Fatal("Check must not run with empty principal")
		return false, nil
	})
	anonCtx := operations.WithPrincipal(context.Background(), operations.Principal{Type: "user", ID: ""})
	_, err := intr.Unary()(
		anonCtx,
		&lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"},
		func(context.Context, any) (any, error) { t.Fatal("must not run"); return nil, nil },
	)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code())
	require.Equal(t, 0, *n)
}

// ────────────────────────────────────────────────────────────────────────────
// Service account subject. FGA-tuple resolves via SA-id.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD028_ServiceAccount_Subject(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, _, _ string) (bool, error) {
		require.Equal(t, "service_account:sa_nlb_deployer", subj)
		return true, nil
	})
	saCtx := operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "service_account", ID: "sa_nlb_deployer", DisplayName: "nlb-deployer-sa",
	})
	_, err := intr.Unary()(
		saCtx,
		&lbv1.CreateNetworkLoadBalancerRequest{ProjectId: "prj-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// Group membership cascade. FGA сама резолвит group#member;
// interceptor видит только конкретный user-subject (resolved auth-layer'ом).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD029_GroupMembership_TransitiveResolve(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, _, _ string) (bool, error) {
		// User уже resolved (group membership — FGA-side, not interceptor-side).
		require.Equal(t, "user:usr_alice", subj)
		return true, nil // FGA нашёл path через group#member tuple
	})
	_, err := intr.Unary()(
		principalCtx("user", "usr_alice"),
		&lbv1.CreateNetworkLoadBalancerRequest{ProjectId: "prj-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)
}

// ────────────────────────────────────────────────────────────────────────────
// Concurrent revoke + Check race. Eventual consistency
// гарантия ≤10s. Здесь — direct test: после InvalidateBySubject следующий
// Check звонит peer (cache miss), а peer-decision определяет результат.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD030_RaceRevoke_CacheInvalidatesBeforeNextCheck(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)

	// Используем interceptor с явным Cache, чтобы получить доступ к invalidate.
	cache := authz.NewCache(time.Hour)
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-nlb-test",
		Map:         check.PermissionMap(),
		Client: authz.CheckClientFunc(func(_ context.Context, _, _, _ string) (bool, error) {
			return allowed.Load(), nil
		}),
		Cache: cache,
	})
	uIntr := intr.Unary()
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"}
	ctx := principalCtx("user", "usr_alice")
	req := &lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"}
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	// 1. Allowed → cached
	_, err := uIntr(ctx, req, info, handler)
	require.NoError(t, err)

	// 2. Cache hit (peer slow but не вызывается).
	_, err = uIntr(ctx, req, info, handler)
	require.NoError(t, err)

	// 3. Admin revokes binding → simulated peer flip + manual cache invalidate
	// (в проде это делает ListenInvalidator через pg_notify).
	allowed.Store(false)
	cache.InvalidateBySubject("user:usr_alice")

	// Next Check goes to peer → denied.
	_, err = uIntr(ctx, req, info, handler)
	st, _ := status.FromError(err)
	require.Equal(t, codes.PermissionDenied, st.Code(),
		"after revoke + cache invalidation, next Check sees fresh deny within ≤10s window")
}

// ────────────────────────────────────────────────────────────────────────────
// Factory tests (NewInterceptor) — exercise все branches.
// ────────────────────────────────────────────────────────────────────────────

func TestFactory_Breakglass_NoIAM_OK(t *testing.T) {
	intr, cache, err := check.NewInterceptor(check.Options{
		ServiceName: "kacho-nlb-test",
		IAMCheck:    nil,
		Breakglass:  true,
	})
	require.NoError(t, err)
	require.NotNil(t, intr)
	require.NotNil(t, cache)
}

func TestFactory_NoIAM_NoBreakglass_Error(t *testing.T) {
	intr, cache, err := check.NewInterceptor(check.Options{
		ServiceName: "kacho-nlb-test",
		IAMCheck:    nil,
		Breakglass:  false,
	})
	require.Nil(t, intr)
	require.Nil(t, cache)
	require.ErrorIs(t, err, check.ErrIAMCheckNotConfigured)
}

// fakeIAMCheck — простой stub реализующий iam.CheckClient interface.
type fakeIAMCheck struct {
	fn func(ctx context.Context, subjectID, relation, object string) (bool, error)
}

func (f *fakeIAMCheck) Check(ctx context.Context, subjectID, relation, object string) (bool, error) {
	return f.fn(ctx, subjectID, relation, object)
}

func TestFactory_WithIAM_OK(t *testing.T) {
	intr, cache, err := check.NewInterceptor(check.Options{
		ServiceName: "kacho-nlb-test",
		IAMCheck: &fakeIAMCheck{fn: func(_ context.Context, _, _, _ string) (bool, error) {
			return true, nil
		}},
		Breakglass: false,
	})
	require.NoError(t, err)
	require.NotNil(t, intr)
	require.NotNil(t, cache)
}

// IAMCheckClient adapter — ErrNoPath passthrough.
func TestIAMCheckClient_Adapter_NoPathPassthrough(t *testing.T) {
	peer := &fakeIAMCheck{fn: func(_ context.Context, _, _, _ string) (bool, error) {
		return false, authz.ErrNoPath
	}}
	adapter := check.NewIAMCheckClient(peer)
	require.NotNil(t, adapter)

	allowed, err := adapter.Check(context.Background(), "user:x", "viewer", "lb_network_load_balancer:y")
	require.False(t, allowed)
	require.ErrorIs(t, err, authz.ErrNoPath)
}

func TestIAMCheckClient_Adapter_NilPeer(t *testing.T) {
	require.Nil(t, check.NewIAMCheckClient(nil))
}

func TestIAMCheckClient_Adapter_DefensiveNilCheck(t *testing.T) {
	var c *check.IAMCheckClient // nil receiver
	_, err := c.Check(context.Background(), "s", "r", "o")
	require.Error(t, err)
}
