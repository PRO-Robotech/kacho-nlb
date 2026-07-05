// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// vipSourceKind — тип источника VIP одного семейства (VipSource oneof).
type vipSourceKind int

const (
	srcSubnetAuto  vipSourceKind = iota // subnet_id → auto-аллокация internal Address
	srcPublicAuto                       // public {} → платформенный public Address
	srcAddressLink                      // address_id → линк существующего Address
)

// familyVIPSpec — разобранный + резолвнутый per-family источник VIP.
type familyVIPSpec struct {
	family    domain.IPVersion
	kind      vipSourceKind
	subnetID  string // srcSubnetAuto — подсеть, из которой аллоцируется VIP
	addressID string // srcAddressLink — существующий Address
	// networkID — derived сеть семейства (INTERNAL): подсеть auto либо подсеть
	// linked-адреса. Пусто для EXTERNAL/public (сети нет). Используется для
	// dualstack same-network инварианта.
	networkID string
}

// origin — release-дискриминатор источника (auto owned / linked).
func (fs familyVIPSpec) origin() domain.VipOrigin {
	if fs.kind == srcAddressLink {
		return domain.VipOriginLinked
	}
	return domain.VipOriginAuto
}

// resolvePlacement — placement_type ↔ type coupling. INTERNAL требует явный
// ZONAL|REGIONAL; EXTERNAL запрещает placement (§3.2, 8.1-12).
func resolvePlacement(lbType domain.LBType, pb lbv1.NetworkLoadBalancer_PlacementType) (domain.PlacementType, error) {
	set := pb != lbv1.NetworkLoadBalancer_PLACEMENT_TYPE_UNSPECIFIED
	if lbType == domain.LBTypeInternal {
		switch pb {
		case lbv1.NetworkLoadBalancer_ZONAL:
			return domain.PlacementZonal, nil
		case lbv1.NetworkLoadBalancer_REGIONAL:
			return domain.PlacementRegional, nil
		}
		return "", status.Error(codes.InvalidArgument,
			"placement_type is required for INTERNAL load balancer")
	}
	if set {
		return "", status.Error(codes.InvalidArgument,
			"placement_type is only valid for INTERNAL load balancer")
	}
	return domain.PlacementUnspecified, nil
}

// resolveVipSources — VipSource v4/v6 → упорядоченный набор familyVIPSpec. ≥1
// семейство обязательно; malformed subnet_id/address_id ловится синхронно.
func resolveVipSources(v4, v6 *lbv1.VipSource) ([]familyVIPSpec, error) {
	var out []familyVIPSpec
	add := func(family domain.IPVersion, src *lbv1.VipSource) error {
		if src == nil || src.GetSource() == nil {
			return nil
		}
		fs := familyVIPSpec{family: family}
		switch s := src.GetSource().(type) {
		case *lbv1.VipSource_SubnetId:
			if err := corevalidate.ResourceID("subnet", ids.PrefixSubnet, s.SubnetId); err != nil {
				return err
			}
			fs.kind = srcSubnetAuto
			fs.subnetID = s.SubnetId
		case *lbv1.VipSource_AddressId:
			if err := corevalidate.ResourceID("address", ids.PrefixAddress, s.AddressId); err != nil {
				return err
			}
			fs.kind = srcAddressLink
			fs.addressID = s.AddressId
		case *lbv1.VipSource_Public:
			fs.kind = srcPublicAuto
		default:
			return status.Errorf(codes.InvalidArgument,
				"%s_source has no vip source set", familyTag(family))
		}
		out = append(out, fs)
		return nil
	}
	if err := add(domain.IPVersionV4, v4); err != nil {
		return nil, err
	}
	if err := add(domain.IPVersionV6, v6); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"load balancer must declare a vip source for at least one ip family")
	}
	return out, nil
}

// validateSourceTypeMatrix — subnet_id ⟹ INTERNAL; public {} ⟹ EXTERNAL (§3.3).
// Несоответствие → каноничный field-текст (не generic — это форма запроса, не oracle).
func validateSourceTypeMatrix(specs []familyVIPSpec, lbType domain.LBType) error {
	for _, fs := range specs {
		switch fs.kind {
		case srcSubnetAuto:
			if lbType != domain.LBTypeInternal {
				return status.Error(codes.InvalidArgument,
					"subnet address source is only valid for INTERNAL load balancer")
			}
		case srcPublicAuto:
			if lbType != domain.LBTypeExternal {
				return status.Error(codes.InvalidArgument,
					"public address source is only valid for EXTERNAL load balancer")
			}
		}
	}
	return nil
}

// familiesFromSpecs — список заявленных семейств в порядке fan-out.
func familiesFromSpecs(specs []familyVIPSpec) []domain.IPVersion {
	out := make([]domain.IPVersion, 0, len(specs))
	for _, fs := range specs {
		out = append(out, fs.family)
	}
	return out
}

// familyTag — короткий тег семейства ("v4"/"v6").
func familyTag(family domain.IPVersion) string {
	if family == domain.IPVersionV6 {
		return "v6"
	}
	return "v4"
}
