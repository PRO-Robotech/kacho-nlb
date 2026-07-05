// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

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

// peerErrToStatus — маппинг ошибок peer-client (project/region) в gRPC-status.
func peerErrToStatus(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Errorf(codes.InvalidArgument, "%s %s not found", caser(kind), id)
	case errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "%s: %v", kind, err)
	case errors.Is(err, domain.ErrFailedPrecondition):
		return status.Errorf(codes.FailedPrecondition, "%s %s: %v", kind, id, err)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable, "%s lookup unavailable", kind)
	}
	return status.Errorf(codes.Internal, "%s lookup failed", kind)
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

// caser — Title-case 1-char для kind ("project" → "Project").
func caser(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 32
	}
	return string(b)
}
