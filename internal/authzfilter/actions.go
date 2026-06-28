// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

// kacho-nlb FGA object types (передаются в iam.ListObjects.resource_type).
//
// префикс `lb_` (НЕ `nlb_`) — совпадает с FGA-моделью
// (`type lb_network_load_balancer / lb_listener / lb_target_group` в kacho-proto
// fga_model.fga) и api-gateway permission_catalog. Зеркало
// internal/domain/fga_intent.go FGAObjectType* + internal/check/permission_map.go.
const (
	ResourceTypeLoadBalancer = "lb_network_load_balancer"
	ResourceTypeListener     = "lb_listener"
	ResourceTypeTargetGroup  = "lb_target_group"
)

// kacho-nlb action-строки — iam-сервер мапит на FGA relation (viewer для read/list).
// Формат `<domain>.<resource>.<verb>` per IAM permission catalog. verb=list →
// iam мапит на relation viewer (read==enforce: та же relation, что per-RPC Check
// для Get; см. internal/check/permission_map.go relationViewer).
const (
	ActionLoadBalancerList = "loadbalancer.networkLoadBalancers.list"
	ActionListenerList     = "loadbalancer.listeners.list"
	ActionTargetGroupList  = "loadbalancer.targetGroups.list"
)
