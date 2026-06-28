// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"github.com/PRO-Robotech/kacho-corelib/ids"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
)

// validateLoadBalancerID — malformed-id guard (api-conventions error-format): id
// с неизвестным 3-char prefix → sync InvalidArgument "invalid network load
// balancer id '<X>'" (НЕ NotFound). Пустой id пропускается — required-проверка
// отдельным стейтментом; well-formed-но-нет → NotFound через repo.Get.
func validateLoadBalancerID(id string) error {
	return corevalidate.ResourceID("network load balancer", ids.PrefixLoadBalancer, id)
}

// validateTargetGroupRefID — тот же guard для ссылочного target_group_id, который
// принимают LB-action-RPC (Attach/Detach/GetTargetStates). Malformed → sync
// InvalidArgument вместо NotFound/FailedPrecondition из repo.
func validateTargetGroupRefID(id string) error {
	return corevalidate.ResourceID("target group", ids.PrefixTargetGroup, id)
}
