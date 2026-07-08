// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"

// Все enum-newtypes для статусов / типов NLB.
//
// Inline-литералы `"ACTIVE"` / `"CREATING"` / `"EXTERNAL"` в Go-коде вне
// этого файла — запрещены (запрет inline-status).
// Use-case-/handler-/repo-слои сравнивают только через эти именованные
// константы.

// ---- LBType ----------------------------------------------------------------

type LBType string

const (
	LBTypeExternal LBType = "EXTERNAL"
	LBTypeInternal LBType = "INTERNAL"
)

func (t LBType) Validate() error {
	switch t {
	case LBTypeExternal, LBTypeInternal:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("type", "type must be one of: EXTERNAL, INTERNAL").
		Err()
}

// ---- LBStatus --------------------------------------------------------------

type LBStatus string

const (
	LBStatusCreating LBStatus = "CREATING"
	LBStatusStarting LBStatus = "STARTING"
	LBStatusActive   LBStatus = "ACTIVE"
	LBStatusStopping LBStatus = "STOPPING"
	LBStatusStopped  LBStatus = "STOPPED"
	LBStatusDeleting LBStatus = "DELETING"
	LBStatusInactive LBStatus = "INACTIVE"
)

func (s LBStatus) Validate() error {
	switch s {
	case LBStatusCreating, LBStatusStarting, LBStatusActive,
		LBStatusStopping, LBStatusStopped, LBStatusDeleting, LBStatusInactive:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("status", "invalid LoadBalancer status").
		Err()
}

// ---- PlacementType ---------------------------------------------------------
// PlacementType — размещение INTERNAL-LB. Пусто для EXTERNAL (placement
// неприменим). ZONAL — unicast-VIP в одной зоне; REGIONAL — anycast-VIP,
// region-scoped. Coupling placement↔type и drain↔placement энфорсится sync-
// прекcheck'ом use-case'а и DB CHECK (не в LB.Validate — чтобы точный текст
// сообщения матрицы задавался в одном месте).

type PlacementType string

const (
	PlacementUnspecified PlacementType = ""
	PlacementZonal       PlacementType = "ZONAL"
	PlacementRegional    PlacementType = "REGIONAL"
)

// Validate — значение placement пустое (EXTERNAL) либо ZONAL/REGIONAL. Coupling
// placement с type проверяется отдельно (use-case).
func (p PlacementType) Validate() error {
	switch p {
	case PlacementUnspecified, PlacementZonal, PlacementRegional:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("placement_type", "placement_type must be one of: ZONAL, REGIONAL").
		Err()
}

// ---- SessionAffinity -------------------------------------------------------

type SessionAffinity string

const (
	SessionAffinity5Tuple       SessionAffinity = "FIVE_TUPLE"
	SessionAffinityClientIPOnly SessionAffinity = "CLIENT_IP_ONLY"
)

func (a SessionAffinity) Validate() error {
	switch a {
	case SessionAffinity5Tuple, SessionAffinityClientIPOnly:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("session_affinity",
			"session_affinity must be one of: FIVE_TUPLE, CLIENT_IP_ONLY").
		Err()
}

// ---- ListenerStatus --------------------------------------------------------

type ListenerStatus string

const (
	ListenerStatusCreating ListenerStatus = "CREATING"
	ListenerStatusActive   ListenerStatus = "ACTIVE"
	ListenerStatusUpdating ListenerStatus = "UPDATING"
	ListenerStatusDeleting ListenerStatus = "DELETING"
)

func (s ListenerStatus) Validate() error {
	switch s {
	case ListenerStatusCreating, ListenerStatusActive,
		ListenerStatusUpdating, ListenerStatusDeleting:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("status", "invalid Listener status").
		Err()
}

// ---- VipOrigin -------------------------------------------------------------
// VipOrigin — дискриминатор источника VIP листенера. Определяет release-ветку
// при Listener.Delete (и в boot-reconcile): `auto` — Address аллоцирован самим
// NLB и должен быть освобождён целиком (FreeIP); `byo` — Address передан
// tenant'ом, при удалении листенера снимается только ссылка (ClearReference),
// сам Address не удаляется. Хранится отдельной колонкой `listeners.vip_origin`
// (а не выводится эвристикой по имени Address) — имя tenant волен задать любым.

type VipOrigin string

const (
	// VipOriginAuto — Address заказан LB неявно (subnet-auto / platform-public);
	// lifecycle связан с LB → release = FreeIP (owned).
	VipOriginAuto VipOrigin = "auto"
	// VipOriginLinked — Address создан tenant'ом заранее и залинкован
	// (address_id); tenant-owned → release = ClearReference (адрес уцелевает).
	VipOriginLinked VipOrigin = "linked"
	// VipOriginBYO — legacy-дискриминатор listeners.vip_origin (BYO attach в
	// Listener); сохранён для листенерной ветки release.
	VipOriginBYO VipOrigin = "byo"
)

func (o VipOrigin) Validate() error {
	switch o {
	case VipOriginAuto, VipOriginLinked, VipOriginBYO:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("vip_origin", "vip_origin must be one of: auto, linked, byo").
		Err()
}

// ---- TargetGroupStatus -----------------------------------------------------

type TargetGroupStatus string

const (
	TargetGroupStatusActive   TargetGroupStatus = "ACTIVE"
	TargetGroupStatusDeleting TargetGroupStatus = "DELETING"
)

func (s TargetGroupStatus) Validate() error {
	switch s {
	case TargetGroupStatusActive, TargetGroupStatusDeleting:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("status", "invalid TargetGroup status").
		Err()
}

// ---- TargetHealthStatus ----------------------------------------------------
// (per-target observed health; computed в GetTargetStates.)

type TargetHealthStatus string

const (
	TargetHealthInitial   TargetHealthStatus = "INITIAL"
	TargetHealthHealthy   TargetHealthStatus = "HEALTHY"
	TargetHealthUnhealthy TargetHealthStatus = "UNHEALTHY"
	TargetHealthDraining  TargetHealthStatus = "DRAINING"
	TargetHealthInactive  TargetHealthStatus = "INACTIVE"
)

func (s TargetHealthStatus) Validate() error {
	switch s {
	case TargetHealthInitial, TargetHealthHealthy, TargetHealthUnhealthy,
		TargetHealthDraining, TargetHealthInactive:
		return nil
	}
	return coreerrors.InvalidArgument().
		AddFieldViolation("status", "invalid Target health status").
		Err()
}
