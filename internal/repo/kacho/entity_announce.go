// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import (
	"time"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// AnnounceZoneRecord — repo-entity per-zone announce-state: domain.AnnounceZone +
// DB-managed UpdatedAt (момент последнего репорта data-plane'ом).
type AnnounceZoneRecord struct {
	domain.AnnounceZone
	UpdatedAt time.Time
}

// AnnounceStateRecord — агрегат наблюдаемой announce-state одного LB: tenant-facing
// anycast-VIP (из load_balancers) + per-zone announce-rows. Internal-проекция
// (:9091 only) — на публичную поверхность NetworkLoadBalancer не выходит.
type AnnounceStateRecord struct {
	LoadBalancerID string
	AddressV4      string
	AddressV6      string
	Zones          []AnnounceZoneRecord
	// ObservedAt — max(updated_at) по зонам; нулевой, если зон ещё нет.
	ObservedAt time.Time
}
