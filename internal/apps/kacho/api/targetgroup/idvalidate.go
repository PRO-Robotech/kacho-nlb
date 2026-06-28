// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package targetgroup

import (
	"github.com/PRO-Robotech/kacho-corelib/ids"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
)

// validateTargetGroupID — malformed-id guard (api-conventions error-format): id с
// неизвестным 3-char prefix → sync InvalidArgument "invalid target group id
// '<X>'" (НЕ NotFound). Пустой id пропускается — required-проверка отдельным
// стейтментом; well-formed-но-нет → NotFound через repo.Get.
func validateTargetGroupID(id string) error {
	return corevalidate.ResourceID("target group", ids.PrefixTargetGroup, id)
}
