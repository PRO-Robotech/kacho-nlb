// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import (
	"time"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// TargetGroupRecord — repo-entity TargetGroup. domain.TargetGroup + DB-managed
// CreatedAt/UpdatedAt. Поле Targets (TargetRecord) заполняется при Get/List
// через JOIN на child-таблицу `targets` (см. pg/target_group_repo.go).
type TargetGroupRecord struct {
	domain.TargetGroup
	CreatedAt time.Time
	UpdatedAt time.Time
	// Xmin — `xmin::text` OCC snapshot; see LoadBalancerRecord.Xmin.
	Xmin string
}

// TargetGroupFilter — фильтр для List target_groups.
type TargetGroupFilter struct {
	ProjectID string
	Name      string
	Filter    string
	// AllowedIDs — per-object FGA allow-set (RBAC; iam ListObjects).
	// nil → bypass; len==0 → пусто (no-leak); len>0 → `WHERE id = ANY` ДО LIMIT.
	AllowedIDs []string
}

// TargetRecord — repo-entity для одного target внутри TG. domain.Target +
// DB-managed CreatedAt/UpdatedAt + Status (ACTIVE | DRAINING) + DrainStartedAt
// (NULL когда Status='ACTIVE'; NOT NULL когда 'DRAINING').
//
// Status и DrainStartedAt живут в repo-leaf (а не в domain.Target), потому что
// это lifecycle-поля управляемые worker'ом (фаза A drain mark / фаза B delete);
// domain.Target — что просит tenant на AddTargets (identity + weight).
type TargetRecord struct {
	domain.Target
	ID             string
	TargetGroupID  string
	Status         string
	DrainStartedAt *time.Time // nil if Status == "ACTIVE"
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
