// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import (
	"go.uber.org/multierr"

	coreerrors "github.com/PRO-Robotech/kacho-corelib/errors"
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
	// NetworkID — VPC-сеть приватного VIP. Обязателен для INTERNAL, запрещён для
	// EXTERNAL (cross-field инвариант, validateNetworkBinding). Immutable после
	// Create. Cross-service ref на kacho-vpc Network (без FK).
	NetworkID NetworkID
	// SecurityGroupIDs — набор vpc.SecurityGroup, описывающий допустимый inbound
	// к VIP (control-plane intent). Валиден только для INTERNAL (SG живут в сети).
	// Mutable: Update заменяет набор целиком (full-replace через update_mask).
	// Cross-service refs на kacho-vpc SecurityGroup (без FK); существование +
	// same-network валидируются peer-API на request-path.
	SecurityGroupIDs []SecurityGroupID
	// IPFamilies — заявленные при Create семейства anycast-VIP (IPV4/IPV6 или
	// оба для dualstack). Источник истины для status-aware DB-CHECK: непустой
	// address_v4/v6 допустим только если соответствующее семейство объявлено
	// здесь. Immutable после Create.
	IPFamilies []IPVersion
	// AddressV4/AddressV6 — единый tenant-facing anycast-VIP на семейство
	// (output-only; пуст пока status=CREATING, заполняется worker'ом после
	// аллокации из vpc AnycastAddressPool).
	AddressV4 IPAddress
	AddressV6 IPAddress
	// AddressIDV4/AddressIDV6 — binding на vpc Address (auto-allocated либо BYO),
	// per-family. Immutable после Create; release-ключ для compensation/runner.
	AddressIDV4 AddressID
	AddressIDV6 AddressID
	// VipOriginV4/VipOriginV6 — DB-only дискриминатор источника VIP (auto/byo),
	// per-family. Управляет release-веткой: auto → FreeIP, byo → ClearReference.
	// В публичную proto-проекцию не выходит.
	VipOriginV4        VipOrigin
	VipOriginV6        VipOrigin
	Name               LbName
	Description        LbDescription
	Labels             LbLabels
	Type               LBType
	Status             LBStatus
	SessionAffinity    SessionAffinity
	CrossZoneEnabled   bool
	DeletionProtection bool
}

// Validate проверяет все семантически-нагруженные поля LoadBalancer
// . multierr.Combine агрегирует ошибки;
// nil — если всё валидно.
func (lb LoadBalancer) Validate() error {
	return multierr.Combine(
		lb.Name.Validate(),
		lb.Description.Validate(),
		ValidateLabels(lb.Labels),
		lb.Type.Validate(),
		lb.Status.Validate(),
		lb.SessionAffinity.Validate(),
		lb.validateNetworkBinding(),
		lb.validateSecurityGroupBinding(),
	)
}

// validateNetworkBinding — cross-field инвариант network_id ↔ type. INTERNAL-LB
// несёт приватный VIP внутри VPC-сети → network_id обязателен. EXTERNAL-LB несёт
// публичный VIP (не из сети) → network_id запрещён. Defense-in-depth дублируется
// DB CHECK (load_balancers_network_id_scheme_check).
func (lb LoadBalancer) validateNetworkBinding() error {
	switch lb.Type {
	case LBTypeInternal:
		if lb.NetworkID == "" {
			return coreerrors.InvalidArgument().
				AddFieldViolation("network_id", "network_id is required for INTERNAL load balancer").
				Err()
		}
	case LBTypeExternal:
		if lb.NetworkID != "" {
			return coreerrors.InvalidArgument().
				AddFieldViolation("network_id", "network_id is only valid for INTERNAL load balancer").
				Err()
		}
	}
	return nil
}

// validateSecurityGroupBinding — cross-field инвариант security_group_ids ↔ type
// + per-id и cardinality. SG живут внутри VPC-сети → непустой набор валиден
// только для INTERNAL (у EXTERNAL сети нет). Пустые id запрещены; размер набора
// ограничен MaxSecurityGroupsPerLB. Defense-in-depth дублируется DB CHECK
// (load_balancers_sg_internal_check).
func (lb LoadBalancer) validateSecurityGroupBinding() error {
	if len(lb.SecurityGroupIDs) == 0 {
		return nil
	}
	if lb.Type != LBTypeInternal {
		return coreerrors.InvalidArgument().
			AddFieldViolation("security_group_ids",
				"security_group_ids is only valid for INTERNAL load balancer").
			Err()
	}
	if len(lb.SecurityGroupIDs) > MaxSecurityGroupsPerLB {
		return coreerrors.InvalidArgument().
			AddFieldViolation("security_group_ids", "too many security groups (max 5)").
			Err()
	}
	for _, sg := range lb.SecurityGroupIDs {
		if sg == "" {
			return coreerrors.InvalidArgument().
				AddFieldViolation("security_group_ids", "security group id must not be empty").
				Err()
		}
	}
	return nil
}

// Equal — deep equality по domain-полям (для noop-detection в Update-flow).
// `CreatedAt` сюда не входит (он в repo-leaf).
func (lb LoadBalancer) Equal(other LoadBalancer) bool {
	return lb.ID == other.ID &&
		lb.ProjectID == other.ProjectID &&
		lb.RegionID == other.RegionID &&
		lb.NetworkID == other.NetworkID &&
		SecurityGroupIDsEqual(lb.SecurityGroupIDs, other.SecurityGroupIDs) &&
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
		lb.CrossZoneEnabled == other.CrossZoneEnabled &&
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
