// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import "time"

// AttachedTargetGroupRecord — repo-entity для M:N pivot
// (load_balancer_id, target_group_id) с priority и attached_at.
//
// Не имеет domain-эквивалента: pivot — пограничная между двумя ресурсами связь,
// её состав (priority, attached_at) полностью DB-managed (priority задаётся на
// AttachTargetGroup, attached_at — DEFAULT now).
type AttachedTargetGroupRecord struct {
	LoadBalancerID string
	TargetGroupID  string
	Priority       int32
	AttachedAt     time.Time
}
