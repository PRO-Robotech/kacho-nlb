// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package geo

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	geopb "github.com/PRO-Robotech/kacho-geo/proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// zoneListPageSize — размер страницы при обходе каталога зон (leaf-справочник —
// зон мало; одной-двух страниц достаточно).
const zoneListPageSize = 1000

// ZoneClient — port-интерфейс валидации зон anycast-drain (disabled_announce_zones):
// resolve множества зон региона через geo.ZoneService.List.
type ZoneClient interface {
	// ListZoneIDsInRegion возвращает id всех зон, принадлежащих regionID
	// (детерминированный обход каталога). Семантика ошибок:
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable (fail-closed для мутации).
	//   - PermissionDenied             → domain.ErrInvalidArg (не лик'аем authz).
	//   - прочее                       → wrapped error без sentinel-обёртки.
	ListZoneIDsInRegion(ctx context.Context, regionID string) ([]string, error)
}

// zoneClient — реализация ZoneClient через gRPC. Stateless pass-through без кэша.
type zoneClient struct {
	zones geopb.ZoneServiceClient
}

// NewZoneClient оборачивает grpc-conn в typed adapter. ZoneService живёт на
// public-listener kacho-geo — публичный read-only справочник Geography.
func NewZoneClient(conn grpc.ClientConnInterface) ZoneClient {
	if conn == nil {
		return nil
	}
	return &zoneClient{zones: geopb.NewZoneServiceClient(conn)}
}

// NewZoneClientFromStubs — конструктор для тестов: принимает напрямую stub.
func NewZoneClientFromStubs(zones geopb.ZoneServiceClient) ZoneClient {
	if zones == nil {
		return nil
	}
	return &zoneClient{zones: zones}
}

// ListZoneIDsInRegion — см. контракт ZoneClient.ListZoneIDsInRegion.
func (c *zoneClient) ListZoneIDsInRegion(ctx context.Context, regionID string) ([]string, error) {
	if regionID == "" {
		return nil, fmt.Errorf("%w: region_id is empty", domain.ErrInvalidArg)
	}

	var out []string
	pageToken := ""
	for {
		var resp *geopb.ListZonesResponse
		if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
			var rerr error
			resp, rerr = c.zones.List(auth.PropagateOutgoing(ctx), &geopb.ListZonesRequest{
				PageSize:  zoneListPageSize,
				PageToken: pageToken,
			})
			return rerr
		}); err != nil {
			return nil, mapZoneErr(err)
		}
		for _, z := range resp.GetZones() {
			if z.GetRegionId() == regionID {
				out = append(out, z.GetId())
			}
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	return out, nil
}

// mapZoneErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapZoneErr(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("geo zone list: %w", err)
	}
	switch st.Code() {
	case codes.PermissionDenied:
		return fmt.Errorf("%w: zone lookup denied", domain.ErrInvalidArg)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: geo zone list: %s", domain.ErrUnavailable, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: geo zone list: %s", domain.ErrInvalidArg, st.Message())
	default:
		return fmt.Errorf("geo zone list: %w", err)
	}
}
