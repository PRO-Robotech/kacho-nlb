// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package announce

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// validateLoadBalancerID — malformed-id guard (api-conventions error-format):
// неизвестный 3-char prefix → sync InvalidArgument "invalid network load
// balancer id '<X>'" (НЕ NotFound). Пустой id — отдельной required-проверкой.
func validateLoadBalancerID(id string) error {
	return corevalidate.ResourceID("network load balancer", ids.PrefixLoadBalancer, id)
}

// errInvalidArg — InvalidArgument с указанием поля для sync-проверок handler'а.
func errInvalidArg(field, msg string) error {
	return status.Errorf(codes.InvalidArgument, "%s: %s", field, msg)
}

// mapErr транслирует sentinel-ошибки repo-слоя в gRPC-status. Уже gRPC-shaped
// ошибки (sync-валидация) пробрасываются как есть; Internal — без leak'а pgx.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, kachorepo.ErrNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, kachorepo.ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, "failed precondition")
	case errors.Is(err, kachorepo.ErrInvalidArg):
		return status.Error(codes.InvalidArgument, "invalid argument")
	case errors.Is(err, kachorepo.ErrUnavailable):
		return status.Error(codes.Unavailable, "service unavailable")
	default:
		// Internal: НЕ leak'аем raw pgx-текст наружу.
		return status.Error(codes.Internal, "internal error")
	}
}

// zoneFromProto — proto AnnounceZoneState → domain.AnnounceZone (inbound write).
func zoneFromProto(z *lbv1.AnnounceZoneState) domain.AnnounceZone {
	return domain.AnnounceZone{
		ZoneID:           z.GetZoneId(),
		IPVersion:        ipVersionFromProto(z.GetIpVersion()),
		BGPSessionUp:     z.GetBgpUp(),
		RouteID:          z.GetRouteId(),
		VrfID:            z.GetVrfId(),
		KernelProgrammed: z.GetKernelProgrammed(),
		InfraID:          z.GetInfraId(),
	}
}

// stateToProto — repo AnnounceStateRecord → proto LoadBalancerAnnounceState (read).
func stateToProto(rec *kachorepo.AnnounceStateRecord) *lbv1.LoadBalancerAnnounceState {
	out := &lbv1.LoadBalancerAnnounceState{
		NetworkLoadBalancerId: rec.LoadBalancerID,
		AddressV4:             rec.AddressV4,
		AddressV6:             rec.AddressV6,
		Zones:                 make([]*lbv1.AnnounceZoneState, 0, len(rec.Zones)),
	}
	if !rec.ObservedAt.IsZero() {
		out.ObservedAt = timestamppb.New(rec.ObservedAt)
	}
	for i := range rec.Zones {
		z := &rec.Zones[i]
		out.Zones = append(out.Zones, &lbv1.AnnounceZoneState{
			ZoneId:           z.ZoneID,
			IpVersion:        ipVersionToProto(z.IPVersion),
			BgpUp:            z.BGPSessionUp,
			RouteId:          z.RouteID,
			VrfId:            z.VrfID,
			KernelProgrammed: z.KernelProgrammed,
			InfraId:          z.InfraID,
		})
	}
	return out
}

// ipVersionFromProto — proto IpVersion → domain.IPVersion ("" для UNSPECIFIED).
func ipVersionFromProto(v lbv1.IpVersion) domain.IPVersion {
	switch v {
	case lbv1.IpVersion_IPV4:
		return domain.IPVersionV4
	case lbv1.IpVersion_IPV6:
		return domain.IPVersionV6
	default:
		return ""
	}
}

// ipVersionToProto — domain.IPVersion → proto IpVersion (UNSPECIFIED для "").
func ipVersionToProto(v domain.IPVersion) lbv1.IpVersion {
	switch v {
	case domain.IPVersionV4:
		return lbv1.IpVersion_IPV4
	case domain.IPVersionV6:
		return lbv1.IpVersion_IPV6
	default:
		return lbv1.IpVersion_IP_VERSION_UNSPECIFIED
	}
}
