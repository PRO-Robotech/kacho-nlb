package check_test

import (
	"fmt"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/check"
)

// permissionRegex — design §6.11 + §6.5 + §6.2:
//
//	^loadbalancer\.[a-z]+\.[a-z][A-Za-z]+$
//
// Group 1 = resource (lowercase + digits ok, no separator); group 2 = verb
// (starts с lowercase letter, может содержать camelCase для длинных глаголов
// типа `attachTargetGroup`, `getTargetStates`, `listOperations`).
var permissionRegex = regexp.MustCompile(`^loadbalancer\.[a-z][a-zA-Z0-9]*\.[a-z][a-zA-Z]+$`)

// allPublicServiceDescs — все gRPC-сервисы NLB, **которые регистрируются на
// public listener'е** (см. `cmd/kacho-loadbalancer/main.go`). Internal-сервисы
// (`InternalResourceLifecycleService`) не подлежат per-RPC FGA Check —
// они отдельно защищаются NetworkPolicy / mTLS (acceptance §AZD-025).
//
// Источник истины: proto-generated `<Service>_ServiceDesc` variables.
var allPublicServiceDescs = []grpc.ServiceDesc{
	lbv1.NetworkLoadBalancerService_ServiceDesc,
	lbv1.ListenerService_ServiceDesc,
	lbv1.TargetGroupService_ServiceDesc,
	operationpb.OperationService_ServiceDesc,
}

// fullMethodName собирает gRPC FullMethod из ServiceDesc.ServiceName + method name:
// "/<package>.<Service>/<Method>".
func fullMethodName(sd grpc.ServiceDesc, methodName string) string {
	return "/" + sd.ServiceName + "/" + methodName
}

// TestDrift_EveryRPCMapped — каждый зарегистрированный публичный RPC покрыт
// PermissionMap (либо как RPCEntry либо как Public: true). Любая «новая»
// RPC, добавленная в proto, без соответствующей регистрации здесь — fail CI.
//
// Этот тест — acceptance §AZD-014 (drift-test catches unmapped RPC).
func TestDrift_EveryRPCMapped(t *testing.T) {
	m := check.PermissionMap()

	var missing []string
	for _, sd := range allPublicServiceDescs {
		for _, mi := range sd.Methods {
			fm := fullMethodName(sd, mi.MethodName)
			if _, ok := m[fm]; !ok {
				missing = append(missing, fm)
			}
		}
		for _, si := range sd.Streams {
			fm := fullMethodName(sd, si.StreamName)
			if _, ok := m[fm]; !ok {
				missing = append(missing, fm)
			}
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"PermissionMap drift: %d RPC(s) not registered in permission_map.go: %v\n"+
			"Add either an RPCEntry (Relation/Permission/Extract) or `{Public: true}` for exempt RPCs.",
		len(missing), missing)
}

// TestDrift_PermissionUnique — все Permission строки в PermissionMap уникальны
// (нет двух RPC, делящих один permission). Design §6.11 п.3.
func TestDrift_PermissionUnique(t *testing.T) {
	m := check.PermissionMap()
	seen := make(map[string]string, len(m))
	for fm, e := range m {
		if e.Permission == "" {
			continue // Public RPC — допускается пустой Permission.
		}
		if prev, dup := seen[e.Permission]; dup {
			t.Errorf("Permission %q used by both %q and %q (must be unique)",
				e.Permission, prev, fm)
		}
		seen[e.Permission] = fm
	}
}

// TestDrift_PermissionRegex — все Permission соответствуют regex
// `^loadbalancer\.[a-z]+\.[a-z][A-Za-z]+$`. Design §6.11 п.4.
func TestDrift_PermissionRegex(t *testing.T) {
	for fm, e := range check.PermissionMap() {
		if e.Permission == "" {
			continue // Public — skip.
		}
		require.Regexpf(t, permissionRegex, e.Permission,
			"RPC %q: Permission %q does not match %s",
			fm, e.Permission, permissionRegex.String())
	}
}

// TestDrift_PermissionNonEmpty — каждый РЕАЛЬНЫЙ (не-Public) RPCEntry имеет
// non-empty Permission (для будущего fine-grained Check / для каталога iam).
// Design §6.11 п.2.
func TestDrift_PermissionNonEmpty(t *testing.T) {
	for fm, e := range check.PermissionMap() {
		if e.Public {
			continue
		}
		require.NotEmptyf(t, e.Permission,
			"RPC %q is not Public but has empty Permission — fill from catalog `loadbalancer.<resource>.<verb>`", fm)
	}
}

// TestDrift_PublicRPCsHaveNoRelation — Public RPC не должны иметь Extract /
// Relation (защита от случайной частичной заливки — иначе interceptor
// проигнорирует Relation, что вызовёт путаницу при ревью).
func TestDrift_PublicRPCsHaveNoRelation(t *testing.T) {
	for fm, e := range check.PermissionMap() {
		if !e.Public {
			continue
		}
		require.Empty(t, e.Relation,
			"RPC %q marked Public — Relation must be empty (got %q)", fm, e.Relation)
		require.Nil(t, e.Extract,
			"RPC %q marked Public — Extract must be nil", fm)
		require.Empty(t, e.Permission,
			"RPC %q marked Public — Permission must be empty (exempt из per-RPC Check)", fm)
	}
}

// TestDrift_ExtractNonNilForNonPublic — у не-Public RPC должен быть Extract.
// Без него interceptor упадёт на nil-deref в runtime.
func TestDrift_ExtractNonNilForNonPublic(t *testing.T) {
	for fm, e := range check.PermissionMap() {
		if e.Public {
			continue
		}
		require.NotNilf(t, e.Extract, "RPC %q: Extract must be non-nil", fm)
		require.NotEmptyf(t, e.Relation, "RPC %q: Relation must be non-empty (viewer/editor/owner)", fm)
	}
}

// TestDrift_CatalogCompleteness — Catalog() возвращает ровно 30 уникальных
// permission строк (design §6.2). Acceptance §AZD-019.
func TestDrift_CatalogCompleteness(t *testing.T) {
	cat := check.Catalog()
	uniq := make(map[string]struct{}, len(cat))
	for _, p := range cat {
		uniq[p] = struct{}{}
	}
	require.Lenf(t, uniq, 30,
		"Catalog must have 30 unique permission strings (design §6.2 §AZD-019); got %d: %v",
		len(uniq), sortedKeys(uniq))
	require.Equal(t, len(cat), len(uniq), "Catalog() contains duplicates")
}

// TestDrift_CatalogRegex — все 30 строк каталога соответствуют regex
// (включая catalog-only `loadbalancer.operations.{get,cancel,list}`).
func TestDrift_CatalogRegex(t *testing.T) {
	for _, p := range check.Catalog() {
		require.Regexpf(t, permissionRegex, p,
			"Catalog permission %q does not match %s", p, permissionRegex.String())
	}
}

// TestDrift_RPCMethodCount — sanity-check: общее число RPCEntries в map'е
// равно сумме методов всех публичных gRPC-сервисов. Если refactor proto
// добавил/удалил RPC, оба теста (этот + EveryRPCMapped) ловят drift.
//
// Ожидание (design §3 / §6.5):
//
//	12 NLB + 6 Listener + 9 TG + 2 Operation = 29 entries.
func TestDrift_RPCMethodCount(t *testing.T) {
	got := len(check.PermissionMap())

	var expected int
	for _, sd := range allPublicServiceDescs {
		expected += len(sd.Methods) + len(sd.Streams)
	}
	require.Equalf(t, expected, got,
		"PermissionMap has %d entries; sum of %d ServiceDesc methods = %d. "+
			"Either drift in proto-generated RPC set or PermissionMap, recheck.",
		got, len(allPublicServiceDescs), expected)
}

// TestExtract_AllRPCEntries — table-driven: для каждого non-Public RPCEntry
// прогоняем Extract с подходящим типизированным request и проверяем что:
//   - возвращённый objectType непустой;
//   - возвращённый objectID == ожидаемый "extracted-id";
//   - err == nil.
//
// Это исчерпывающе покрывает все 27 Extract closure'ов в PermissionMap →
// PermissionMap coverage 39% → ≥90%.
func TestExtract_AllRPCEntries(t *testing.T) {
	m := check.PermissionMap()

	type tc struct {
		fm       string
		req      any
		wantType string
		wantID   string
	}
	const id = "x-id"
	cases := []tc{
		// NLB
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get",
			&lbv1.GetNetworkLoadBalancerRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/List",
			&lbv1.ListNetworkLoadBalancersRequest{ProjectId: id},
			"project", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create",
			&lbv1.CreateNetworkLoadBalancerRequest{ProjectId: id},
			"project", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Update",
			&lbv1.UpdateNetworkLoadBalancerRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Delete",
			&lbv1.DeleteNetworkLoadBalancerRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Start",
			&lbv1.StartNetworkLoadBalancerRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Stop",
			&lbv1.StopNetworkLoadBalancerRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Move",
			&lbv1.MoveNetworkLoadBalancerRequest{NetworkLoadBalancerId: id, DestinationProjectId: "p2"},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/AttachTargetGroup",
			&lbv1.AttachNetworkLoadBalancerTargetGroupRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/DetachTargetGroup",
			&lbv1.DetachNetworkLoadBalancerTargetGroupRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/GetTargetStates",
			&lbv1.GetTargetStatesRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/ListOperations",
			&lbv1.ListNetworkLoadBalancerOperationsRequest{NetworkLoadBalancerId: id},
			"lb_network_load_balancer", id},
		// Listener
		{"/kacho.cloud.loadbalancer.v1.ListenerService/Get",
			&lbv1.GetListenerRequest{ListenerId: id},
			"lb_listener", id},
		{"/kacho.cloud.loadbalancer.v1.ListenerService/List",
			&lbv1.ListListenersRequest{ProjectId: id},
			"project", id},
		{"/kacho.cloud.loadbalancer.v1.ListenerService/Create",
			&lbv1.CreateListenerRequest{LoadBalancerId: id},
			"lb_network_load_balancer", id},
		{"/kacho.cloud.loadbalancer.v1.ListenerService/Update",
			&lbv1.UpdateListenerRequest{ListenerId: id},
			"lb_listener", id},
		{"/kacho.cloud.loadbalancer.v1.ListenerService/Delete",
			&lbv1.DeleteListenerRequest{ListenerId: id},
			"lb_listener", id},
		{"/kacho.cloud.loadbalancer.v1.ListenerService/ListOperations",
			&lbv1.ListListenerOperationsRequest{ListenerId: id},
			"lb_listener", id},
		// TG
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/Get",
			&lbv1.GetTargetGroupRequest{TargetGroupId: id},
			"lb_target_group", id},
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/List",
			&lbv1.ListTargetGroupsRequest{ProjectId: id},
			"project", id},
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/Create",
			&lbv1.CreateTargetGroupRequest{ProjectId: id},
			"project", id},
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/Update",
			&lbv1.UpdateTargetGroupRequest{TargetGroupId: id},
			"lb_target_group", id},
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/Delete",
			&lbv1.DeleteTargetGroupRequest{TargetGroupId: id},
			"lb_target_group", id},
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/Move",
			&lbv1.MoveTargetGroupRequest{TargetGroupId: id, DestinationProjectId: "p2"},
			"lb_target_group", id},
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/AddTargets",
			&lbv1.AddTargetsRequest{TargetGroupId: id},
			"lb_target_group", id},
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/RemoveTargets",
			&lbv1.RemoveTargetsRequest{TargetGroupId: id},
			"lb_target_group", id},
		{"/kacho.cloud.loadbalancer.v1.TargetGroupService/ListOperations",
			&lbv1.ListTargetGroupOperationsRequest{TargetGroupId: id},
			"lb_target_group", id},
	}
	require.Lenf(t, cases, 27, "must cover all 27 non-Public RPCs in PermissionMap (got %d)", len(cases))

	for _, c := range cases {
		t.Run(c.fm, func(t *testing.T) {
			e, ok := m[c.fm]
			require.Truef(t, ok, "PermissionMap missing entry %q", c.fm)
			require.False(t, e.Public, "case %q is Public — should not be in this table", c.fm)
			require.NotNil(t, e.Extract)
			gotType, gotID, err := e.Extract(c.req)
			require.NoError(t, err)
			require.Equal(t, c.wantType, gotType)
			require.Equal(t, c.wantID, gotID)
		})
	}
}

// sortedKeys — helper для error-сообщений.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Sanity — чтобы fmt всегда был "использован" в этой test-сборке (drift_test
// помогает рендерить помощь — оставляем placeholder для будущих error
// сообщений).
var _ = fmt.Sprintf
