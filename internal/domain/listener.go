// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package domain

import (
	"github.com/H-BF/corlib/pkg/option"
	"go.uber.org/multierr"
)

// Listener — domain entity Listener. Принадлежит LoadBalancer'у;
// `RegionID` денормализован from-LB (same-region constraint — DB-CHECK).
//
// Address-семантика:
//   - AddressID пуст и AllocatedAddress пуст → auto-alloc на Create через
//     vpc.InternalAddressService.AllocateExternalIP/InternalIP.
//   - AddressID задан (BYO) → vpc.AddressService.Get + SetReference в worker'е.
//   - SubnetID обязателен для type=INTERNAL; для EXTERNAL
//     игнорируется.
type Listener struct {
	ID                   ResourceID
	ProjectID            ProjectID
	LoadBalancerID       ResourceID
	RegionID             RegionID
	Name                 LbName
	Description          LbDescription
	Labels               LbLabels
	Protocol             LbProto
	Port                 LbPort
	TargetPort           LbPort
	IPVersion            IPVersion
	AddressID            option.ValueOf[AddressID]
	AllocatedAddress     IPAddress
	SubnetID             option.ValueOf[SubnetID]
	ProxyProtocolV2      bool
	DefaultTargetGroupID option.ValueOf[ResourceID]
	Status               ListenerStatus
	// VipOrigin — источник VIP (auto-alloc vs BYO). Управляет release-веткой на
	// Delete: auto → FreeIP, byo → ClearReference. См. domain.VipOrigin.
	VipOrigin VipOrigin
}

// Validate — все семантически-нагруженные поля. Bind-семантика
// AddressID/SubnetID vs Type/IPVersion  проверяется в
// use-case-слое (требует знание LB-родителя) — в Validate проверяем только
// форму конкретных полей.
func (l Listener) Validate() error {
	// AllocatedAddress на Create обычно пуст (заполняется в worker'е после
	// VIP-allocation). Валидируем только если задан — repo читает Listener
	// уже с allocated_address.
	allocErr := error(nil)
	if l.AllocatedAddress != "" {
		allocErr = l.AllocatedAddress.Validate()
	}
	// VipOrigin валидируется только если задан — repo всегда читает непустое
	// значение (DB DEFAULT 'auto' + CHECK), а тонкие builder'ы (тесты) могут
	// оставить zero-value. Жёсткий within-service инвариант держит DB-CHECK.
	vipOriginErr := error(nil)
	if l.VipOrigin != "" {
		vipOriginErr = l.VipOrigin.Validate()
	}
	return multierr.Combine(
		l.Name.Validate(),
		l.Description.Validate(),
		ValidateLabels(l.Labels),
		l.Protocol.Validate(),
		l.Port.Validate(),
		l.TargetPort.Validate(),
		l.IPVersion.Validate(),
		allocErr,
		l.Status.Validate(),
		vipOriginErr,
	)
}

// Equal — deep equality (Update no-op detection).
func (l Listener) Equal(other Listener) bool {
	return l.ID == other.ID &&
		l.ProjectID == other.ProjectID &&
		l.LoadBalancerID == other.LoadBalancerID &&
		l.RegionID == other.RegionID &&
		l.Name == other.Name &&
		l.Description == other.Description &&
		LabelsEqual(l.Labels, other.Labels) &&
		l.Protocol == other.Protocol &&
		l.Port == other.Port &&
		l.TargetPort == other.TargetPort &&
		l.IPVersion == other.IPVersion &&
		optEqual(l.AddressID, other.AddressID) &&
		l.AllocatedAddress == other.AllocatedAddress &&
		optEqual(l.SubnetID, other.SubnetID) &&
		l.ProxyProtocolV2 == other.ProxyProtocolV2 &&
		optEqual(l.DefaultTargetGroupID, other.DefaultTargetGroupID) &&
		l.Status == other.Status &&
		l.VipOrigin == other.VipOrigin
}

// optEqual — equality двух option.ValueOf[T] по semantic-значению (some/none +
// inner). option.ValueOf.IsEq требует callback, эта обёртка дает удобный API
// для comparable T.
func optEqual[T comparable](a, b option.ValueOf[T]) bool {
	av, aok := a.Maybe()
	bv, bok := b.Maybe()
	if aok != bok {
		return false
	}
	if !aok {
		return true
	}
	return av == bv
}
