// Package check — FGA Check interceptor + permission map для kacho-nlb (KAC-156).
//
// Каждый RPC kacho-nlb проходит через FGA `Check` (kacho-corelib/authz.Interceptor)
// до выполнения handler'а. Маппинг RPC → required (relation, object, permission)
// — статическая таблица `PermissionMap()`, собранная из 4-х под-сервисов
// (NetworkLoadBalancer, Listener, TargetGroup, Operation).
//
// Drift-test (`drift_test.go`) гарантирует, что **каждый** публичный RPC
// зарегистрированных gRPC-сервисов покрыт map'ой (`RPCEntry` либо `Public:true`).
// Несовпадение → CI fail до merge'а.
//
// Source-of-truth permission catalog — `kacho-iam/internal/authzmap/permission_catalog.go`
// (30 строк под namespace `loadbalancer.*`, design §6.2). Эти 30 имён
// валидируются iam'ом против каталога при создании custom roles. Все 30 строк
// перечислены в `Catalog()` ниже; 27 из них привязаны к конкретным RPC через
// PermissionMap, ещё 3 (`loadbalancer.operations.{get,cancel,list}`) — это
// "catalog-only" permissions: OperationService.Get/Cancel помечены
// `(kacho.iam.authz.v1.permission) = "<exempt>"` в proto-аннотации (Public,
// без per-RPC Check), а `loadbalancer.operations.list` — катологическое имя
// без отдельного RPC (per-resource `ListOperations` использует свой permission).
package check

import (
	"github.com/PRO-Robotech/kacho-corelib/authz"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// FGA object types kacho-nlb (см. design §6.1 — 3 типа).
//
// `objectTypeProject` — parent scope: на нём висят RBAC bindings;
// используется для Create / List (caller должен иметь `editor`/`viewer`
// на project'е).
const (
	// KAC-227: object types MUST match the FGA model (iam/v1/fga_model.fga) and
	// the nlb owner-tuple intents (internal/domain FGAObjectType*, applied via
	// SEC-D fga_register_outbox → IAM) — both use the `lb_*` prefix. The
	// interceptor Checks these object types; mismatched names → no tuple →
	// per-RPC 403 even when the gateway-edge FGA Check allowed.
	objectTypeProject      = "project"
	objectTypeLoadBalancer = "lb_network_load_balancer"
	objectTypeListener     = "lb_listener"
	objectTypeTargetGroup  = "lb_target_group"
)

// FGA relations (design §6.1). Дублирует константы из
// `kacho-iam/internal/authzmap` (там — source of truth); тут — backend
// view-only, чтобы не плодить cross-repo import просто ради двух строк.
const (
	relationViewer = "viewer"
	relationEditor = "editor"
)

// Permission strings (design §6.2). Каждая строка должна совпадать с
// каталогом `kacho-iam/internal/authzmap/permission_catalog.go`.
//
// Формат — `loadbalancer.<resource>.<verb>`. Drift-test проверяет:
//   - regex `^loadbalancer\.[a-z]+\.[a-z][A-Za-z]+$` для каждого Permission
//     поля в PermissionMap;
//   - уникальность Permission строк внутри PermissionMap;
//   - суммарный набор (PermissionMap + `catalogOnlyOperationPermissions`) =
//     30 distinct (design §6.2 §AZD-019).
const (
	// NLB (12)
	permNLBGet               = "loadbalancer.networkLoadBalancers.get"
	permNLBList              = "loadbalancer.networkLoadBalancers.list"
	permNLBCreate            = "loadbalancer.networkLoadBalancers.create"
	permNLBUpdate            = "loadbalancer.networkLoadBalancers.update"
	permNLBDelete            = "loadbalancer.networkLoadBalancers.delete"
	permNLBStart             = "loadbalancer.networkLoadBalancers.start"
	permNLBStop              = "loadbalancer.networkLoadBalancers.stop"
	permNLBMove              = "loadbalancer.networkLoadBalancers.move"
	permNLBAttachTargetGroup = "loadbalancer.networkLoadBalancers.attachTargetGroup"
	permNLBDetachTargetGroup = "loadbalancer.networkLoadBalancers.detachTargetGroup"
	permNLBGetTargetStates   = "loadbalancer.networkLoadBalancers.getTargetStates"
	permNLBListOperations    = "loadbalancer.networkLoadBalancers.listOperations"
	// Listener (6)
	permLstGet            = "loadbalancer.listeners.get"
	permLstList           = "loadbalancer.listeners.list"
	permLstCreate         = "loadbalancer.listeners.create"
	permLstUpdate         = "loadbalancer.listeners.update"
	permLstDelete         = "loadbalancer.listeners.delete"
	permLstListOperations = "loadbalancer.listeners.listOperations"
	// TargetGroup (9)
	permTGRGet            = "loadbalancer.targetGroups.get"
	permTGRList           = "loadbalancer.targetGroups.list"
	permTGRCreate         = "loadbalancer.targetGroups.create"
	permTGRUpdate         = "loadbalancer.targetGroups.update"
	permTGRDelete         = "loadbalancer.targetGroups.delete"
	permTGRMove           = "loadbalancer.targetGroups.move"
	permTGRAddTargets     = "loadbalancer.targetGroups.addTargets"
	permTGRRemoveTargets  = "loadbalancer.targetGroups.removeTargets"
	permTGRListOperations = "loadbalancer.targetGroups.listOperations"
	// OperationService (3 catalog-only — exempt из per-RPC Check).
	permOPGet    = "loadbalancer.operations.get"
	permOPCancel = "loadbalancer.operations.cancel"
	permOPList   = "loadbalancer.operations.list"
)

// catalogOnlyOperationPermissions — 3 catalog-only имена, не привязанные к
// конкретному NLB-RPC. См. doc.go раздел "catalog-only".
//
// Drift-test использует это, чтобы проверить полноту 30-string каталога
// (PermissionMap values ∪ catalogOnlyOperationPermissions = 30).
var catalogOnlyOperationPermissions = []string{permOPGet, permOPCancel, permOPList}

// Catalog возвращает union всех 30 catalog strings (design §6.2):
// 27 RPC-mapped через PermissionMap + 3 catalog-only operation strings.
//
// Сортировка не гарантируется; drift-test сравнивает как set.
func Catalog() []string {
	m := PermissionMap()
	out := make([]string, 0, len(m)+len(catalogOnlyOperationPermissions))
	for _, e := range m {
		if e.Permission != "" {
			out = append(out, e.Permission)
		}
	}
	out = append(out, catalogOnlyOperationPermissions...)
	return out
}

// PermissionMap — карта `<gRPC FullMethod>` → required relation + extract +
// permission.
//
// Семантика per-RPC (design §6.3 + §6.5):
//   - Create / List              — на parent scope `project:<project_id>` (из request);
//   - Get/Update/Delete/<verb>   — на самом ресурсе `nlb_<type>:<id>`;
//   - OperationService.Get/Cancel — `Public: true` (proto-аннотация `<exempt>`);
//     api-gateway opsproxy маршрутизирует по prefix, single-resource Check
//     здесь нерелевантен (op-id opaque + поллится creator'ом сразу после Create —
//     в этом окне tuple ещё может не быть видим).
//
// **List RPCs** помечены `ScopeFiltered: true` (per design §3.8 / KAC-127 #25):
// handler сам делает ListObjects-based фильтрацию (200 + filtered/EMPTY если
// caller не имеет grant'а в project'е). Single per-RPC Check здесь
// семантически неверен — он бы отверг весь вызов `no path` 403 ДО того, как
// scope-filter успеет отработать. Extract сохраняется для catalog/tooling
// parity, но Interceptor его не вызывает.
//
// **Move** (NLB & TG): per-RPC Check выполняется на ресурсе (`editor on
// nlb_*`) — это гарантирует authz на source project через FGA cascade
// (`editor on nlb_*` ← `editor on project`). Проверка editor'а на
// destination_project_id — задача handler'а (он зовёт `iam.Check` напрямую
// перед `repo.Update(project_id=<dst>)`). Acceptance §AZD-006 покрывает оба
// edge'а в end-to-end newman case.
//
// **AttachTargetGroup**: per-RPC Check — `editor on nlb_load_balancer:<lb_id>`.
// Дополнительный `viewer on nlb_target_group:<tg_id>` (design §6.5) —
// handler'ом перед attach (`iam.Check` напрямую). Acceptance §AZD-007.
//
// scope-guard (KAC-108): для Get/Update/Delete/<verb> мы НЕ резолвим
// project_id из БД заранее — лишний DB-trip; relation проверяется на самом
// ресурсе, FGA-модель E3 настроена так, что `editor on nlb_load_balancer` →
// computed через `editor on project` → `member on group`.
func PermissionMap() authz.RPCMap {
	return authz.RPCMap{
		// =========================
		// NetworkLoadBalancerService (12 RPCs)
		// =========================
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get": {
			Relation:   relationViewer,
			Permission: permNLBGet,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.GetNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/List": {
			Relation:      relationViewer,
			Permission:    permNLBList,
			ScopeFiltered: true,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*lbv1.ListNetworkLoadBalancersRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create": {
			Relation:   relationEditor,
			Permission: permNLBCreate,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*lbv1.CreateNetworkLoadBalancerRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Update": {
			Relation:   relationEditor,
			Permission: permNLBUpdate,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.UpdateNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Delete": {
			Relation:   relationEditor,
			Permission: permNLBDelete,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.DeleteNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Start": {
			Relation:   relationEditor,
			Permission: permNLBStart,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.StartNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Stop": {
			Relation:   relationEditor,
			Permission: permNLBStop,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.StopNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Move": {
			// Per-RPC Check на ресурсе (editor on src LB → cascades через editor on
			// project src). Destination project Check — handler'ом (см. doc).
			Relation:   relationEditor,
			Permission: permNLBMove,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.MoveNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/AttachTargetGroup": {
			// Per-RPC Check — editor on LB. Дополнительный viewer on TG — handler'ом.
			Relation:   relationEditor,
			Permission: permNLBAttachTargetGroup,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.AttachNetworkLoadBalancerTargetGroupRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/DetachTargetGroup": {
			Relation:   relationEditor,
			Permission: permNLBDetachTargetGroup,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.DetachNetworkLoadBalancerTargetGroupRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/GetTargetStates": {
			Relation:   relationViewer,
			Permission: permNLBGetTargetStates,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.GetTargetStatesRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/ListOperations": {
			Relation:   relationViewer,
			Permission: permNLBListOperations,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.ListNetworkLoadBalancerOperationsRequest).GetNetworkLoadBalancerId(), nil
			}),
		},

		// =========================
		// ListenerService (6 RPCs)
		// =========================
		"/kacho.cloud.loadbalancer.v1.ListenerService/Get": {
			Relation:   relationViewer,
			Permission: permLstGet,
			Extract: authz.StaticExtractor(objectTypeListener, func(req any) (string, error) {
				return req.(*lbv1.GetListenerRequest).GetListenerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.ListenerService/List": {
			// KAC-229: project-scoped (parity with NLB/TG List). viewer on the
			// project; data-level ListObjects still filters by accessible LBs.
			Relation:      relationViewer,
			Permission:    permLstList,
			ScopeFiltered: true,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*lbv1.ListListenersRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.ListenerService/Create": {
			// editor on parent LB (FGA cascades через nlb_listener.load_balancer).
			Relation:   relationEditor,
			Permission: permLstCreate,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.CreateListenerRequest).GetLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.ListenerService/Update": {
			Relation:   relationEditor,
			Permission: permLstUpdate,
			Extract: authz.StaticExtractor(objectTypeListener, func(req any) (string, error) {
				return req.(*lbv1.UpdateListenerRequest).GetListenerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.ListenerService/Delete": {
			Relation:   relationEditor,
			Permission: permLstDelete,
			Extract: authz.StaticExtractor(objectTypeListener, func(req any) (string, error) {
				return req.(*lbv1.DeleteListenerRequest).GetListenerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.ListenerService/ListOperations": {
			Relation:   relationViewer,
			Permission: permLstListOperations,
			Extract: authz.StaticExtractor(objectTypeListener, func(req any) (string, error) {
				return req.(*lbv1.ListListenerOperationsRequest).GetListenerId(), nil
			}),
		},

		// =========================
		// TargetGroupService (9 RPCs)
		// =========================
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/Get": {
			Relation:   relationViewer,
			Permission: permTGRGet,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.GetTargetGroupRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/List": {
			Relation:      relationViewer,
			Permission:    permTGRList,
			ScopeFiltered: true,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*lbv1.ListTargetGroupsRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/Create": {
			Relation:   relationEditor,
			Permission: permTGRCreate,
			Extract: authz.StaticExtractor(objectTypeProject, func(req any) (string, error) {
				return req.(*lbv1.CreateTargetGroupRequest).GetProjectId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/Update": {
			Relation:   relationEditor,
			Permission: permTGRUpdate,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.UpdateTargetGroupRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/Delete": {
			Relation:   relationEditor,
			Permission: permTGRDelete,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.DeleteTargetGroupRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/Move": {
			// editor on src TG; destination project — handler'ом.
			Relation:   relationEditor,
			Permission: permTGRMove,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.MoveTargetGroupRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/AddTargets": {
			Relation:   relationEditor,
			Permission: permTGRAddTargets,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.AddTargetsRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/RemoveTargets": {
			Relation:   relationEditor,
			Permission: permTGRRemoveTargets,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.RemoveTargetsRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/ListOperations": {
			Relation:   relationViewer,
			Permission: permTGRListOperations,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.ListTargetGroupOperationsRequest).GetTargetGroupId(), nil
			}),
		},

		// =========================
		// OperationService (proto `kacho.cloud.operation`, NOT `.v1`).
		//
		// Proto-аннотация `(kacho.iam.authz.v1.permission) = "<exempt>"` для
		// Get/Cancel ⇒ Public, без per-RPC FGA Check. Семантика: op-id opaque
		// + creator поллит сразу после Create, в этом окне FGA-tuple ещё может
		// быть не виден (no path) → лишний 403. Owner-check для Cancel
		// (acceptance §AZD-011 "only the operation creator may cancel") —
		// handler'ом на основе `operation.created_by` в БД.
		// =========================
		"/kacho.cloud.operation.OperationService/Get":    {Public: true},
		"/kacho.cloud.operation.OperationService/Cancel": {Public: true},
	}
}
