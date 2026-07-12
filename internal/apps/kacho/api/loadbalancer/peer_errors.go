// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/shared"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// subnetRegionMatches — region-coherence: регион подсети совпадает с регионом LB
// (placement-coherence). subnet.RegionID — denormalised mirror, заполняемый
// adapter'ом (REGIONAL → region_id; ZONAL → zone→region резолв через geo). Пустой
// RegionID (mirror не заполнен — только в back-compat без zone-resolver'а) →
// проверка пропускается: в проде adapter всегда заполняет его либо fail-closed
// Unavailable. Возвращает true при совпадении/пропуске.
func subnetRegionMatches(sn *vpcclient.Subnet, lbRegion domain.RegionID) bool {
	return sn.RegionID == "" || sn.RegionID == string(lbRegion)
}

// subnetRegionCoherent — региональная проверка для caller-supplied subnet_id
// (descriptive текст — форма запроса, не oracle). Несовпадение → InvalidArgument
// со стабильным контракт-текстом (data-integrity.md §Placement-coherence).
func subnetRegionCoherent(sn *vpcclient.Subnet, lbRegion domain.RegionID) error {
	if !subnetRegionMatches(sn, lbRegion) {
		return status.Error(codes.InvalidArgument,
			"load balancer vip subnet must be in the same region as the load balancer")
	}
	return nil
}

// subnetPlacementMatches — placement подсети (vpc) == placement LB (domain).
func subnetPlacementMatches(subnetPlacement string, lbPlacement domain.PlacementType) bool {
	switch lbPlacement {
	case domain.PlacementRegional:
		return subnetPlacement == vpcclient.SubnetPlacementRegional
	case domain.PlacementZonal:
		return subnetPlacement != vpcclient.SubnetPlacementRegional && subnetPlacement != ""
	}
	return false
}

// allocAcquireErr — анти-oracle маппинг ошибок auto-аллокации (ёмкость/недоступность).
func allocAcquireErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, "load balancer address allocation unavailable")
	}
	// ErrFailedPrecondition (пул/подсеть исчерпаны) и прочее → generic (без ёмкости).
	return status.Error(codes.FailedPrecondition, "could not allocate load balancer address")
}

// linkAcquireErr — анти-oracle маппинг ошибок link-CAS (адрес занят → generic).
func linkAcquireErr(err error) error {
	if errors.Is(err, domain.ErrUnavailable) {
		return status.Error(codes.Unavailable, "address lookup unavailable")
	}
	return status.Error(codes.FailedPrecondition, "Illegal argument addressId")
}

// zonePeerErr — маппер зон для validateDisabledAnnounceZones/resolveSources.
func zonePeerErr(err error) error {
	if errors.Is(err, domain.ErrUnavailable) {
		return status.Error(codes.Unavailable, "zone lookup unavailable")
	}
	return status.Error(codes.InvalidArgument, "Illegal argument disabledAnnounceZones")
}

// peerErrToStatus — тонкий делегатор к единому `shared.PeerErrToStatus`
// (один источник истины для project/region precheck).
func peerErrToStatus(err error, kind, id string) error {
	return shared.PeerErrToStatus(err, kind, id)
}

// subnetPeerErr — sync-precheck subnet_id через vpc.SubnetService.Get.
func subnetPeerErr(err error, id string) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "subnet %s not found", id)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable, "subnet lookup unavailable")
	}
	return status.Errorf(codes.Internal, "subnet lookup failed")
}

// linkedAddressErr — анти-oracle маппинг AddressService.Get в link-precheck.
func linkedAddressErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArg):
		return status.Error(codes.InvalidArgument, "Illegal argument addressId")
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, "address lookup unavailable")
	}
	return status.Error(codes.Internal, "address lookup failed")
}
