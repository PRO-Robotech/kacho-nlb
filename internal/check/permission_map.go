// Package check — FGA Check interceptor + permission map для kacho-nlb.
//
// Каждый RPC сервиса требует FGA `Check` через `iam.InternalIAMService.Check`
// до выполнения. Permission map — статическая таблица "method-full-name → permission",
// собирается в init() из catalog kacho-corelib/authz.
//
// TODO(KAC-152): populate PermissionMap всеми 30 permissions из namespace
// `loadbalancer.*` (design §6.2). Source of truth — kacho-iam permission catalog.
package check

// PermissionMap возвращает таблицу "rpc-method → permission-string" для FGA Check.
//
// TODO(KAC-152): заменить заглушку на полный map.
func PermissionMap() map[string]string {
	return map[string]string{
		// "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create": "loadbalancer.networkLoadBalancers.create",
		// ... 30 entries total ...
	}
}
