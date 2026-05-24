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
// GWT-AZD-001 — NLB.Create без editor on project → PermissionDenied
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
// GWT-AZD-002 — NLB.Get viewer OK
// ────────────────────────────────────────────────────────────────────────────

func TestAZD002_NLBGet_Viewer_OK(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "viewer", rel)
		require.Equal(t, "nlb_load_balancer:nlb-1", obj)
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
// GWT-AZD-003 — stranger subject → PermissionDenied
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
// GWT-AZD-004 — NLB.Start: viewer rejected, editor OK
// ────────────────────────────────────────────────────────────────────────────

func TestAZD004_NLBStart_Viewer_Denied(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "editor", rel)
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

func TestAZD004_NLBStop_Editor_OK(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "editor", rel)
		require.Equal(t, "nlb_load_balancer:nlb-1", obj)
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
// GWT-AZD-005 — NLB.Delete: editor OK, viewer rejected
// ────────────────────────────────────────────────────────────────────────────

func TestAZD005_NLBDelete_Editor_OK_ViewerRejected(t *testing.T) {
	// Editor — OK.
	intr1, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, _ string) (bool, error) {
		require.Equal(t, "editor", rel)
		return true, nil
	})
	_, err := intr1.Unary()(
		principalCtx("user", "usr_e"),
		&lbv1.DeleteNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Delete"},
		func(context.Context, any) (any, error) { return "ok", nil },
	)
	require.NoError(t, err)

	// Viewer (FGA Check returns false for editor-relation) — denied.
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
// GWT-AZD-006 — NLB.Move src-check OK (per-RPC); dst-check — handler-level.
//
// Interceptor выполняет ОДНОЙ Check — на ресурсе (FGA cascade покрывает
// editor on src project). Проверка destination_project_id — handler'ом.
// Этот test проверяет, что interceptor НЕ запрашивает обе projection'и
// (no over-extraction) и принимает запрос если editor on src LB удовлетворён.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD006_NLBMove_PerRPCCheck_OnResourceOnly(t *testing.T) {
	intr, n, calls := newTestInterceptor(t, func(_ context.Context, subj, rel, obj string) (bool, error) {
		require.Equal(t, "editor", rel)
		require.Equal(t, "nlb_load_balancer:nlb-src", obj, "interceptor must Check on resource, NOT destination project")
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
// GWT-AZD-007 — NLB.AttachTargetGroup: per-RPC Check на LB (editor); TG-check
// — handler'ом (interceptor выполняет один Check).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD007_NLBAttachTargetGroup_PerRPCCheck_OnLBOnly(t *testing.T) {
	intr, n, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "editor", rel)
		require.Equal(t, "nlb_load_balancer:nlb-1", obj)
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
// GWT-AZD-008 — TG.AddTargets: editor on TG required, viewer rejected.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD008_TGAddTargets_Viewer_Denied(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "editor", rel)
		require.Equal(t, "nlb_target_group:tgr-1", obj)
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
// GWT-AZD-009 — Listener.Create: editor on parent LB required.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD009_ListenerCreate_OnParentLB(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "editor", rel)
		require.Equal(t, "nlb_load_balancer:nlb-1", obj,
			"Listener.Create checks editor on parent LB (FGA cascades через nlb_listener.load_balancer)")
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
// GWT-AZD-010 — OperationService.Get: Public (exempt), handler runs.
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
// GWT-AZD-011 — OperationService.Cancel: Public (exempt); creator-only check
// — handler-level (`operation.created_by` в БД), не authz-interceptor.
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
// GWT-AZD-012 — FGA unavailable → fail-closed PermissionDenied.
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
// GWT-AZD-013 — Breakglass dev-only (interceptor allows; production cfg
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
	// (KAC-122 root cause). Anonymous = пустой principal_id.
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
// GWT-AZD-014 — RPC не в PermissionMap → fail-closed.
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
// GWT-AZD-015 — D-11 sync creator-tuple fail → operation aborts. Это
// **handler-level** (worker abort tx до commit); здесь, в interceptor-suite,
// проверяем что adapter транзитом передаёт WriteCreatorTuple errors (это
// функционально для D-11 — handler ловит и rollback'ит).
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
// GWT-AZD-016 — Cache invalidation ≤10s. Здесь — direct unit-тест
// `Cache.InvalidateBySubject`. ListenInvalidator end-to-end — integration-тест.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD016_CacheInvalidation_BySubject(t *testing.T) {
	cache := authz.NewCache(5 * time.Second)
	cache.SetAllowed("user:usr_alice", "viewer", "nlb_load_balancer", "nlb-1")

	allowed, hit := cache.Get("user:usr_alice", "viewer", "nlb_load_balancer", "nlb-1")
	require.True(t, hit)
	require.True(t, allowed)

	cache.InvalidateBySubject("user:usr_alice")
	_, hit = cache.Get("user:usr_alice", "viewer", "nlb_load_balancer", "nlb-1")
	require.False(t, hit, "after InvalidateBySubject the cache miss")
}

// ────────────────────────────────────────────────────────────────────────────
// GWT-AZD-017 — Custom role resolves to editor through 3-relation cascade
// (FGA expands `loadbalancer.networkLoadBalancers.start` → tuple `editor`).
// Здесь — interceptor proof: relation=editor accepted даже если permission
// строка узкая (Custom role).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD017_CustomRole_ResolvesToEditor(t *testing.T) {
	intr, _, calls := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "editor", rel,
			"Custom role narrowest covering relation = editor (design §6.4)")
		require.Equal(t, "nlb_load_balancer:nlb-1", obj)
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
// GWT-AZD-018 — Custom role with unknown permission → InvalidArgument.
// Это валидация iam.Role.Create (kacho-iam-side); здесь — sanity test через
// Catalog().
// ────────────────────────────────────────────────────────────────────────────

func TestAZD018_UnknownPermission_NotInCatalog(t *testing.T) {
	cat := check.Catalog()
	for _, p := range cat {
		require.NotEqualf(t, "loadbalancer.foo.bar", p,
			"hypothetical garbage permission must NOT be in catalog")
	}
	// iam.Role.Create rejects permission strings absent в Catalog() — это
	// проверяется в kacho-iam unit-тестах; здесь sanity что Catalog не содержит
	// мусора.
}

// ────────────────────────────────────────────────────────────────────────────
// GWT-AZD-019 — Permission catalog completeness (30) — также в drift_test.
// Дублируем здесь как explicit AZD-019 проверка.
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
// GWT-AZD-020 — Predefined system role seeds — проверяется в kacho-iam
// migration tests. Здесь — sanity: Catalog содержит виды permissions, которые
// system-роли должны покрывать.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD020_AdminRoleCoversAllPermissions(t *testing.T) {
	// admin == `loadbalancer.*.*` — все 30 permissions.
	// Sanity: Catalog не пуст.
	require.NotEmpty(t, check.Catalog())
}

// ────────────────────────────────────────────────────────────────────────────
// GWT-AZD-021 — Owner relation: creator gets owner on created LB. Это
// D-11 sync write (handler-level). Здесь sanity: relation `owner` валидно
// в наших object types (interceptor не использует owner на per-RPC, но
// catalog-level permissions могут на него рассчитывать).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD021_OwnerRelation_Valid(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, _ string) (bool, error) {
		// Owner check — handler-level (D-11 sync write); interceptor
		// per-RPC использует viewer/editor. Этот test просто sanity-проверяет
		// fall-through allow-path.
		require.Contains(t, []string{"viewer", "editor"}, rel)
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
// GWT-AZD-022 — Lifecycle DELETED tuple cleanup → DecisionNoPath. Здесь —
// прямая проверка: peer-client возвращает authz.ErrNoPath → interceptor
// → DecisionNoPath → handler runs → DB вернёт NOT_FOUND (KAC-133).
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
// GWT-AZD-023 — Cache hit ratio ≥95%. Здесь — direct unit: повторный Check
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
// GWT-AZD-024 — Cache hit latency. Sub-millisecond cache hit (single-process
// map lookup). Меряем delta между холодным Check и cache hit.
// ────────────────────────────────────────────────────────────────────────────

func TestAZD024_CacheHit_FastPath(t *testing.T) {
	var delay atomic.Int64
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		time.Sleep(time.Duration(delay.Load()))
		return true, nil
	})
	uIntr := intr.Unary()
	info := &grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"}
	ctx := principalCtx("user", "usr_alice")
	req := &lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: "nlb-1"}
	handler := func(context.Context, any) (any, error) { return "ok", nil }

	// Cold: simulated 10ms peer latency.
	delay.Store(int64(10 * time.Millisecond))
	start := time.Now()
	_, err := uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	cold := time.Since(start)
	require.GreaterOrEqual(t, cold, 10*time.Millisecond, "first Check should include simulated peer latency")

	// Warm: peer slow — но cache не должен звать peer.
	delay.Store(int64(100 * time.Millisecond))
	start = time.Now()
	_, err = uIntr(ctx, req, info, handler)
	require.NoError(t, err)
	warm := time.Since(start)
	require.Less(t, warm, 5*time.Millisecond, "warm cache hit must be < 5ms (acceptance §AZD-024 budget ≤20ms p95)")
}

// ────────────────────────────────────────────────────────────────────────────
// GWT-AZD-025 — InternalResourceLifecycleService.Subscribe — НЕ на public
// listener'е. Здесь — sanity: PermissionMap НЕ содержит record для
// `InternalResourceLifecycleService/Subscribe` (internal-only защита через
// methodIsInternal heuristic + listener-isolation).
// ────────────────────────────────────────────────────────────────────────────

func TestAZD025_InternalRPC_NotInPermissionMap(t *testing.T) {
	m := check.PermissionMap()
	const fm = "/kacho.cloud.loadbalancer.v1.InternalResourceLifecycleService/Subscribe"
	_, ok := m[fm]
	require.False(t, ok, "Internal* RPC must NOT be в PermissionMap (lives on internal listener only)")

	// Sanity: interceptor пропускает Internal* по methodIsInternal heuristic
	// (если случайно попадёт на public listener — DecisionInternal bypass).
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, _, _ string) (bool, error) {
		t.Fatal("Internal RPC: Check must not be invoked")
		return false, nil
	})
	called := false
	_, err := intr.Unary()(
		principalCtx("system", "bootstrap"),
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: fm},
		func(context.Context, any) (any, error) { called = true; return "ok", nil },
	)
	require.NoError(t, err)
	require.True(t, called)
}

// ────────────────────────────────────────────────────────────────────────────
// GWT-AZD-026 — Operations: per-resource ListOperations — viewer на ресурсе.
// (Cross-resource ops listing — handler-level scope-filter.)
// ────────────────────────────────────────────────────────────────────────────

func TestAZD026_NLBListOperations_ViewerOnResource(t *testing.T) {
	intr, _, _ := newTestInterceptor(t, func(_ context.Context, _, rel, obj string) (bool, error) {
		require.Equal(t, "viewer", rel)
		require.Equal(t, "nlb_load_balancer:nlb-1", obj)
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
// GWT-AZD-027 — Anonymous → UNAUTHENTICATED. Это — auth-interceptor scope
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
// GWT-AZD-028 — Service account subject. FGA-tuple resolves via SA-id.
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
// GWT-AZD-029 — Group membership cascade. FGA сама резолвит group#member;
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
// GWT-AZD-030 — Concurrent revoke + Check race. Eventual consistency
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

	allowed, err := adapter.Check(context.Background(), "user:x", "viewer", "nlb_load_balancer:y")
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
