// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package announce

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// ReportAnnounceStateUseCase — inbound write-канал data-plane→nlb: идемпотентный
// upsert наблюдаемой announce-state. Sync-ack (не Operation): это не tenant-
// мутация ресурса, а feedback, идемпотентный по (lb, zone, ip_version).
//
// Маппинг:
//
//	network_load_balancer_id == "" → InvalidArgument "required"
//	malformed id                   → InvalidArgument (corevalidate)
//	zone без zone_id               → InvalidArgument
//	LB отсутствует (FK)            → FailedPrecondition
type ReportAnnounceStateUseCase struct {
	store Store
}

// NewReportAnnounceStateUseCase конструктор.
func NewReportAnnounceStateUseCase(store Store) *ReportAnnounceStateUseCase {
	return &ReportAnnounceStateUseCase{store: store}
}

// Execute — validate id+zones → map proto→domain → store.ReportZones → ack.
func (u *ReportAnnounceStateUseCase) Execute(
	ctx context.Context, req *lbv1.ReportLoadBalancerAnnounceStateRequest,
) (*lbv1.ReportLoadBalancerAnnounceStateResponse, error) {
	if u.store == nil {
		return nil, status.Error(codes.Unavailable, "announce store not configured")
	}
	id := req.GetNetworkLoadBalancerId()
	if id == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(id); err != nil {
		return nil, mapErr(err)
	}

	zones := make([]domain.AnnounceZone, 0, len(req.GetZones()))
	for _, z := range req.GetZones() {
		if z.GetZoneId() == "" {
			return nil, errInvalidArg("zones.zone_id", "required")
		}
		zones = append(zones, zoneFromProto(z))
	}

	if err := u.store.ReportZones(ctx, id, zones); err != nil {
		return nil, mapErr(err)
	}
	return &lbv1.ReportLoadBalancerAnnounceStateResponse{}, nil
}
