// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

// AnnounceZone — наблюдаемая per-zone announce-state одного семейства anycast-VIP,
// репортируемая data plane'ом обратно в control plane (feedback-петля).
//
// Зерно — per-(zone, ip_version): один зональный анонс относится к одному
// семейству, а зона может анонсировать оба семейства одновременно.
//
// Инфра-чувствительные данные (security.md): BGP-статус, route/VRF id, статус
// программирования ядра, числовой инфра-id — живут только за Internal API
// (:9091) и на публичную проекцию NetworkLoadBalancer не выходят.
type AnnounceZone struct {
	// ZoneID — зона, из которой анонсируется (withdraw'ится) VIP.
	ZoneID string
	// IPVersion — семейство адреса зонального анонса (IPV4/IPV6).
	IPVersion IPVersion
	// BGPSessionUp — BGP-сессия зоны для этого VIP поднята (true) / отозвана.
	BGPSessionUp bool
	// RouteID — идентификатор запрограммированного маршрута VIP в зоне.
	RouteID string
	// VrfID — идентификатор VRF/routing-таблицы, в которой живёт маршрут.
	VrfID string
	// KernelProgrammed — маршрут запрограммирован в ядре data-plane-узла.
	KernelProgrammed bool
	// InfraID — числовой инфра-идентификатор зонального анонса (внутренний для
	// data plane).
	InfraID int64
}
