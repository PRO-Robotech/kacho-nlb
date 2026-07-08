// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"github.com/PRO-Robotech/kacho-corelib/ids"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
)

// validateListenerID — malformed-id guard (api-conventions error-format): id с
// неизвестным 3-char prefix → sync InvalidArgument "invalid listener id '<X>'"
// (НЕ NotFound). Пустой id пропускается — required-проверка делается отдельным
// стейтментом с собственным сообщением; well-formed-но-несуществующий id →
// NotFound через repo.Get.
func validateListenerID(id string) error {
	return corevalidate.ResourceID("listener", ids.PrefixListener, id)
}

// validateLoadBalancerRefID — тот же malformed-id guard для ссылочного
// load_balancer_id, который принимает Listener.Create: неизвестный 3-char prefix →
// sync InvalidArgument "invalid network load balancer id '<X>'" (НЕ NotFound из
// repo.Get). Пустой id пропускается — required-проверка отдельным стейтментом.
func validateLoadBalancerRefID(id string) error {
	return corevalidate.ResourceID("network load balancer", ids.PrefixLoadBalancer, id)
}
