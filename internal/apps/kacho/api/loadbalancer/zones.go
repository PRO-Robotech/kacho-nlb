// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// checkDisabledAnnounceZones — общая валидация drain-набора (Create + Update):
// REGIONAL-only; каждая зона ∈ регион; набор не покрывает все зоны региона. nil
// zoneClient (dev-стенд без geo) → пропуск geo-проверок (REGIONAL-only остаётся).
func checkDisabledAnnounceZones(ctx context.Context, zc ZoneClient, placement domain.PlacementType, regionID string, zones []string) error {
	if len(zones) == 0 {
		return nil
	}
	if placement != domain.PlacementRegional {
		return status.Error(codes.InvalidArgument,
			"disabled_announce_zones is only valid for REGIONAL load balancer")
	}
	if zc == nil {
		return nil
	}
	regionZones, err := zc.ListZoneIDsInRegion(ctx, regionID)
	if err != nil {
		return zonePeerErr(err)
	}
	inRegion := make(map[string]struct{}, len(regionZones))
	for _, z := range regionZones {
		inRegion[z] = struct{}{}
	}
	drained := make(map[string]struct{}, len(zones))
	for _, z := range zones {
		if _, ok := inRegion[z]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"zone %s is not in region %s", z, regionID)
		}
		drained[z] = struct{}{}
	}
	// Набор не должен покрывать ВСЕ зоны региона (VIP стал бы недостижим).
	if len(regionZones) > 0 && len(drained) >= len(inRegion) {
		return status.Error(codes.InvalidArgument,
			"disabled_announce_zones must not cover all zones of the region")
	}
	return nil
}

// normalizeZones — dedup + стабильный порядок набора зон (для DB-записи и Equal).
func normalizeZones(zones []string) []string {
	if len(zones) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(zones))
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		if z == "" {
			continue
		}
		if _, ok := seen[z]; ok {
			continue
		}
		seen[z] = struct{}{}
		out = append(out, z)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
