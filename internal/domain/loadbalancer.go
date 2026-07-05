// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import (
	"go.uber.org/multierr"
)

// LoadBalancer — domain entity NetworkLoadBalancer.
//
// Поля семантически-нагруженных колонок — newtypes со встроенным Validate.
// `CreatedAt` сюда НЕ входит — это DB-managed (DEFAULT now), живёт в
// repo-сущности.
type LoadBalancer struct {
	ID        ResourceID
	ProjectID ProjectID
	RegionID  RegionID
	// PlacementType — размещение INTERNAL-LB (ZONAL|REGIONAL); пусто для EXTERNAL.
	// Immutable после Create. Coupling placement↔type и матрица источника
	// валидируются в use-case'е (точные тексты) + DB CHECK (defense-in-depth).
	PlacementType PlacementType
	// DisabledAnnounceZones — deny-list зон anycast-drain (REGIONAL only), mutable.
	// Каждая зона ∈ регион LB; набор не покрывает все зоны (валидация geo в use-case).
	DisabledAnnounceZones []string
	// IPFamilies — заявленные при Create семейства VIP (IPV4/IPV6 или оба для
	// dualstack). Источник истины для family-guard DB-CHECK: непустой address_v4/v6
	// допустим только если соответствующее семейство объявлено здесь. Immutable.
	IPFamilies []IPVersion
	// AddressV4/AddressV6 — output-only VIP-IP на семейство (пуст пока
	// status=CREATING; заполняется worker'ом после bind — резолвится из
	// связанного vpc Address). В публичную proto-проекцию НЕ выходит (единый
	// источник истины IP — связанный Address; §3.6).
	AddressV4 IPAddress
	AddressV6 IPAddress
	// AddressIDV4/AddressIDV6 — связанный vpc Address (auto-allocated либо linked),
	// per-family. Output-only; immutable после Create; release-ключ compensation/runner.
	AddressIDV4 AddressID
	AddressIDV6 AddressID
	// VipOriginV4/VipOriginV6 — DB-only дискриминатор источника VIP (auto/linked),
	// per-family. Управляет release-веткой: auto → owned two-step (ClearReference→
	// FreeIP), linked → ClearReference. В публичную proto-проекцию не выходит.
	VipOriginV4        VipOrigin
	VipOriginV6        VipOrigin
	Name               LbName
	Description        LbDescription
	Labels             LbLabels
	Type               LBType
	Status             LBStatus
	SessionAffinity    SessionAffinity
	DeletionProtection bool
}

// Validate проверяет семантически-нагруженные поля LoadBalancer. multierr.Combine
// агрегирует ошибки; nil — если всё валидно. Coupling placement↔type / drain↔
// placement и матрица источника VIP валидируются в use-case'е (точные тексты
// сообщений) + DB CHECK; здесь — только per-field инварианты.
func (lb LoadBalancer) Validate() error {
	return multierr.Combine(
		lb.Name.Validate(),
		lb.Description.Validate(),
		ValidateLabels(lb.Labels),
		lb.Type.Validate(),
		lb.Status.Validate(),
		lb.SessionAffinity.Validate(),
		lb.PlacementType.Validate(),
	)
}

// Equal — deep equality по domain-полям (для noop-detection в Update-flow).
// `CreatedAt` сюда не входит (он в repo-leaf).
func (lb LoadBalancer) Equal(other LoadBalancer) bool {
	return lb.ID == other.ID &&
		lb.ProjectID == other.ProjectID &&
		lb.RegionID == other.RegionID &&
		lb.PlacementType == other.PlacementType &&
		stringsEqualOrdered(lb.DisabledAnnounceZones, other.DisabledAnnounceZones) &&
		ipVersionsEqual(lb.IPFamilies, other.IPFamilies) &&
		lb.AddressV4 == other.AddressV4 &&
		lb.AddressV6 == other.AddressV6 &&
		lb.AddressIDV4 == other.AddressIDV4 &&
		lb.AddressIDV6 == other.AddressIDV6 &&
		lb.VipOriginV4 == other.VipOriginV4 &&
		lb.VipOriginV6 == other.VipOriginV6 &&
		lb.Name == other.Name &&
		lb.Description == other.Description &&
		LabelsEqual(lb.Labels, other.Labels) &&
		lb.Type == other.Type &&
		lb.Status == other.Status &&
		lb.SessionAffinity == other.SessionAffinity &&
		lb.DeletionProtection == other.DeletionProtection
}

// ipVersionsEqual — order-insensitive equality двух наборов IPVersion (семейства
// VIP). Наборы маленькие (≤2), поэтому простое сравнение по членству.
func ipVersionsEqual(a, b []IPVersion) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		found := false
		for _, y := range b {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
