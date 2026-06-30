// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package check — FGA Check interceptor + permission map для kacho-nlb.
//
// Каждый RPC kacho-nlb проходит через FGA `Check` (kacho-corelib/authz.Interceptor)
// до выполнения handler'а. Маппинг RPC → required (relation, object, permission)
// статическая таблица `PermissionMap`, собранная из 4-х под-сервисов
// (NetworkLoadBalancer, Listener, TargetGroup, Operation).
//
// Drift-test (`drift_test.go`) гарантирует, что **каждый** публичный RPC
// зарегистрированных gRPC-сервисов покрыт map'ой (`RPCEntry` либо `Public:true`).
// Несовпадение → CI fail до merge'а.
//
// Source-of-truth permission catalog — `kacho-iam/internal/authzmap/permission_catalog.go`
// (30 строк под namespace `loadbalancer.*`). Эти 30 имён
// валидируются iam'ом против каталога при создании custom roles. Все 30 строк
// перечислены в `Catalog` ниже; 27 из них привязаны к конкретным RPC через
// PermissionMap, ещё 3 (`loadbalancer.operations.{get,cancel,list}`) — это
// "catalog-only" permissions: OperationService.Get/Cancel помечены
// `(kacho.iam.authz.v1.permission) = "<exempt>"` в proto-аннотации (Public,
// без per-RPC Check), а `loadbalancer.operations.list` — катологическое имя
// без отдельного RPC (per-resource `ListOperations` использует свой permission).
package check

import (
	"github.com/PRO-Robotech/kacho-corelib/authz"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// FGA object types kacho-nlb (3 типа).
//
// `objectTypeProject` — parent scope: на нём висят RBAC bindings;
// используется для Create / List (caller должен иметь `editor`/`viewer`
// на project'е).
const (
	// object types MUST match the FGA model (iam/v1/fga_model.fga) and
	// the nlb owner-tuple intents (internal/domain FGAObjectType*, applied via
	// fga_register_outbox → IAM) — both use the `lb_*` prefix. The
	// interceptor Checks these object types; mismatched names → no tuple →
	// per-RPC 403 even when the gateway-edge FGA Check allowed.
	objectTypeProject      = "project"
	objectTypeLoadBalancer = "lb_network_load_balancer"
	objectTypeListener     = "lb_listener"
	objectTypeTargetGroup  = "lb_target_group"

	// objectTypeCluster / clusterSingletonRoot — singleton root объекта FGA-иерархии
	// (cluster ▸ account ▸ project ▸ resource). Используется как scope cluster-floor
	// Check'а для internal RPC, не привязанного к конкретному project/ресурсу
	// (InternalResourceLifecycleService.Subscribe).
	objectTypeCluster    = "cluster"
	clusterSingletonRoot = "cluster_kacho_root"
)

// FGA relations. Дублирует константы из
// `kacho-iam/internal/authzmap` (там — source of truth); тут — backend
// view-only, чтобы не плодить cross-repo import просто ради двух строк.
const (
	// relationViewer / relationEditor — tier-relations. Сохраняются для Create
	// (parent-scoped, F-7: NLB/TG на project, Listener на parent LB) и top-level
	// project-List (visibility per-object идёт через iam ListObjects `viewer ∪
	// v_list`, не через per-RPC Check). Для object-self CRUD энфорс — verb-bearing
	// relations ниже.
	relationViewer = "viewer"
	relationEditor = "editor"

	// verb-bearing relations (v_*) — enforcement резолвит object-self action на
	// verb, а не на tier (anchor- «Explicit RBAC model 2026», /D-6a:
	// доступ по verb развязан с tier). Материализуются per-object reconciler'ом
	// kacho-iam; consumer гейтит ими object-self RPC. Source of truth relation-имён
	// kacho-iam/internal/authzmap; тут — backend view-only.
	//
	//	v_get    — чтение содержимого самого ресурса (Get / GetTargetStates);
	//	v_list   — видимость операций на самом ресурсе (ListOperations) — НЕ
	//	           top-level project-List;
	//	v_update — мутация самого ресурса (Update + start/stop/move/attach/targets);
	//	v_delete — удаление самого ресурса.
	relationVGet    = "v_get"
	relationVList   = "v_list"
	relationVUpdate = "v_update"
	relationVDelete = "v_delete"

	// relationAnnounceWriter — least-priv writer-relation для inbound announce-state
	// write (ReportAnnounceState). Единственный носитель — data plane (новое
	// одностороннее runtime-ребро kacho-vpc-implement → kacho-nlb). object-scoped на
	// `lb_network_load_balancer:<id>`: viewer/tenant НЕ несут её → PermissionDenied.
	// Материализация relation'а в FGA-модели — часть iam-стороны интеграции; тут —
	// backend view-only имя для per-RPC Check.
	relationAnnounceWriter = "announce_writer"

	// relationSystemViewer — cluster-scoped read relation (`cluster.system_viewer`
	// = [user, service_account] в FGA-модели). Floor для internal read/stream RPC,
	// который не имеет project/resource-scope (InternalResourceLifecycleService.
	// Subscribe). Легитимный internal-reader (kacho-iam consumer) держит
	// `system_viewer@cluster:cluster_kacho_root` (seed как в kacho-iam SystemViewerFloor);
	// любой другой subject → PermissionDenied.
	relationSystemViewer = "system_viewer"
)

// staticClusterFloor — ObjectExtractor, всегда возвращающий
// (cluster, cluster_kacho_root) независимо от request'а. Нужен для stream-RPC:
// corelib stream-interceptor вызывает Extract с req==nil (request недоступен до
// первого Recv), поэтому scope обязан быть фиксированным cluster-singleton'ом.
func staticClusterFloor() authz.ObjectExtractor {
	return func(any) (string, string, error) {
		return objectTypeCluster, clusterSingletonRoot, nil
	}
}

// Permission strings. Каждая строка должна совпадать с
// каталогом `kacho-iam/internal/authzmap/permission_catalog.go`.
//
// Формат — `loadbalancer.<resource>.<verb>`. Drift-test проверяет:
//   - regex `^loadbalancer\.[a-z]+\.[a-z][A-Za-z]+$` для каждого Permission
//     поля в PermissionMap;
//   - уникальность Permission строк внутри PermissionMap;
//   - суммарный набор (PermissionMap + `catalogOnlyOperationPermissions`) =
//     30 distinct.
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

// Catalog возвращает union всех 30 catalog strings:
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
// Семантика per-RPC:
//   - Create / List              — на parent scope `project:<project_id>` (из request);
//   - Get/Update/Delete/<verb>   — на самом ресурсе `lb_<type>:<id>`;
//   - OperationService.Get/Cancel — `Public: true` (proto-аннотация `<exempt>`);
//     api-gateway opsproxy маршрутизирует по prefix, single-resource Check
//     здесь нерелевантен (op-id opaque + поллится creator'ом сразу после Create —
//     в этом окне tuple ещё может не быть видим).
//
// **List RPCs** помечены `ScopeFiltered: true`:
// handler сам делает ListObjects-based фильтрацию (200 + filtered/EMPTY если
// caller не имеет grant'а в project'е). Single per-RPC Check здесь
// семантически неверен — он бы отверг весь вызов `no path` 403 ДО того, как
// scope-filter успеет отработать. Extract сохраняется для catalog/tooling
// parity, но Interceptor его не вызывает.
//
// **Move** (NLB & TG): per-RPC Check выполняется на ресурсе (`editor on
// lb_*`) — это гарантирует authz на source project через FGA cascade
// (`editor on lb_*` ← `editor on project`). Проверка editor'а на
// destination_project_id — задача handler'а (он зовёт `iam.Check` напрямую
// перед `repo.Update(project_id=<dst>)`). Acceptance покрывает оба
// edge'а в end-to-end newman case.
//
// **AttachTargetGroup**: per-RPC Check — `editor on lb_network_load_balancer:<lb_id>`.
// Дополнительный `viewer on lb_target_group:<tg_id>` проверяется
// handler'ом перед attach (`iam.Check` напрямую).
//
// scope-guard: для Get/Update/Delete/<verb> мы НЕ резолвим
// project_id из БД заранее — лишний DB-trip; relation проверяется на самом
// ресурсе, FGA-модель E3 настроена так, что `editor on lb_network_load_balancer` →
// computed через `editor on project` → `member on group`.
func PermissionMap() authz.RPCMap {
	return authz.RPCMap{
		// =========================
		// NetworkLoadBalancerService (12 RPCs)
		// =========================
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get": {
			Relation:   relationVGet,
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
			Relation:   relationVUpdate,
			Permission: permNLBUpdate,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.UpdateNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Delete": {
			Relation:   relationVDelete,
			Permission: permNLBDelete,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.DeleteNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Start": {
			Relation:   relationVUpdate,
			Permission: permNLBStart,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.StartNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Stop": {
			Relation:   relationVUpdate,
			Permission: permNLBStop,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.StopNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Move": {
			// Per-RPC Check на ресурсе (editor on src LB → cascades через editor on
			// project src). Destination project Check — handler'ом (см. doc).
			Relation:   relationVUpdate,
			Permission: permNLBMove,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.MoveNetworkLoadBalancerRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/AttachTargetGroup": {
			// Per-RPC Check — editor on LB. Дополнительный viewer on TG — handler'ом.
			Relation:   relationVUpdate,
			Permission: permNLBAttachTargetGroup,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.AttachNetworkLoadBalancerTargetGroupRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/DetachTargetGroup": {
			Relation:   relationVUpdate,
			Permission: permNLBDetachTargetGroup,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.DetachNetworkLoadBalancerTargetGroupRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/GetTargetStates": {
			Relation:   relationVGet,
			Permission: permNLBGetTargetStates,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.GetTargetStatesRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/ListOperations": {
			Relation:   relationVList,
			Permission: permNLBListOperations,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.ListNetworkLoadBalancerOperationsRequest).GetNetworkLoadBalancerId(), nil
			}),
		},

		// =========================
		// ListenerService (6 RPCs)
		// =========================
		"/kacho.cloud.loadbalancer.v1.ListenerService/Get": {
			Relation:   relationVGet,
			Permission: permLstGet,
			Extract: authz.StaticExtractor(objectTypeListener, func(req any) (string, error) {
				return req.(*lbv1.GetListenerRequest).GetListenerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.ListenerService/List": {
			// project-scoped (parity with NLB/TG List). viewer on the
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
			Relation:   relationVUpdate,
			Permission: permLstUpdate,
			Extract: authz.StaticExtractor(objectTypeListener, func(req any) (string, error) {
				return req.(*lbv1.UpdateListenerRequest).GetListenerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.ListenerService/Delete": {
			Relation:   relationVDelete,
			Permission: permLstDelete,
			Extract: authz.StaticExtractor(objectTypeListener, func(req any) (string, error) {
				return req.(*lbv1.DeleteListenerRequest).GetListenerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.ListenerService/ListOperations": {
			Relation:   relationVList,
			Permission: permLstListOperations,
			Extract: authz.StaticExtractor(objectTypeListener, func(req any) (string, error) {
				return req.(*lbv1.ListListenerOperationsRequest).GetListenerId(), nil
			}),
		},

		// =========================
		// TargetGroupService (9 RPCs)
		// =========================
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/Get": {
			Relation:   relationVGet,
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
			Relation:   relationVUpdate,
			Permission: permTGRUpdate,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.UpdateTargetGroupRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/Delete": {
			Relation:   relationVDelete,
			Permission: permTGRDelete,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.DeleteTargetGroupRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/Move": {
			// editor on src TG; destination project — handler'ом.
			Relation:   relationVUpdate,
			Permission: permTGRMove,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.MoveTargetGroupRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/AddTargets": {
			Relation:   relationVUpdate,
			Permission: permTGRAddTargets,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.AddTargetsRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/RemoveTargets": {
			Relation:   relationVUpdate,
			Permission: permTGRRemoveTargets,
			Extract: authz.StaticExtractor(objectTypeTargetGroup, func(req any) (string, error) {
				return req.(*lbv1.RemoveTargetsRequest).GetTargetGroupId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.TargetGroupService/ListOperations": {
			Relation:   relationVList,
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
		// ("only the operation creator may cancel") —
		// handler'ом на основе `operation.created_by` в БД.
		// =========================
		"/kacho.cloud.operation.OperationService/Get":    {Public: true},
		"/kacho.cloud.operation.OperationService/Cancel": {Public: true},

		// =========================
		// InternalResourceLifecycleService (cluster-internal :9091, stream).
		//
		// Subscribe стримит resource_id/project_id ВСЕХ проектов kacho-nlb. Internal-
		// листенер гоняет ТОТ ЖЕ authzIntr, что и public (security.md «authN+authZ на
		// ОБОИХ listener'ах»; «Internal = trusted» — запрещённое допущение). Поэтому
		// RPC обязан быть в map'е: не Public (это слило бы стрим всем), а с реальным
		// Check на cluster-floor `system_viewer` @ singleton `cluster:cluster_kacho_root`.
		// Легитимный consumer (kacho-iam) держит `system_viewer@cluster` (seed как в
		// kacho-iam SystemViewerFloor); любой другой subject → PermissionDenied.
		//
		// Permission намеренно пустой: это НЕ tenant-facing каталожный permission
		// (30-string `loadbalancer.*`-каталог покрывает только public-RPC) — gate тут
		// чисто relation-based, как cluster-floor у других сервисов.
		"/kacho.cloud.loadbalancer.v1.InternalResourceLifecycleService/Subscribe": {
			Relation: relationSystemViewer,
			Extract:  staticClusterFloor(),
		},

		// =========================
		// InternalLoadBalancerAnnounceService (cluster-internal :9091).
		//
		// Наблюдаемая per-zone announce-state anycast-VIP (data-plane feedback).
		// Internal-листенер гоняет ТОТ ЖЕ per-RPC Check, что и public (security.md
		// «authN+authZ на ОБОИХ listener'ах»; «Internal = trusted» — запрещённое
		// допущение): оба RPC замаплены с реальным object-scoped Relation, НЕ Public.
		//
		// Scope — `lb_network_load_balancer:<network_load_balancer_id>` (из request):
		//   - GetAnnounceState  — read, viewer-tier verb-bearing `v_get` (как Get LB);
		//   - ReportAnnounceState — inbound write, least-priv `announce_writer`
		//     (единственный носитель = data plane; viewer/tenant → PermissionDenied).
		//
		// Permission намеренно пустой: announce — internal RPC, gate чисто
		// relation-based; в 30-string tenant-каталог `loadbalancer.*` он не входит
		// (каталогизация announce-permission на iam-стороне — отдельная задача).
		"/kacho.cloud.loadbalancer.v1.InternalLoadBalancerAnnounceService/GetAnnounceState": {
			Relation: relationVGet,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.GetLoadBalancerAnnounceStateRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
		"/kacho.cloud.loadbalancer.v1.InternalLoadBalancerAnnounceService/ReportAnnounceState": {
			Relation: relationAnnounceWriter,
			Extract: authz.StaticExtractor(objectTypeLoadBalancer, func(req any) (string, error) {
				return req.(*lbv1.ReportLoadBalancerAnnounceStateRequest).GetNetworkLoadBalancerId(), nil
			}),
		},
	}
}
