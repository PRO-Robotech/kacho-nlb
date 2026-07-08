// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package geo

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	geopb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/geo/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// zoneListPageSize — размер страницы при обходе каталога зон (leaf-справочник —
// зон мало; одной-двух страниц достаточно).
const zoneListPageSize = 1000

// DefaultZoneListTimeout — per-call deadline применяемый к каждому
// ZoneService.List-вызову (одна страница), когда client построен без явного
// timeout'а. Тот же класс проблемы, что DefaultRegionGetTimeout (region_client.go,
// тот же пакет): без него зависший (не отвечающий, не Unavailable) geo-peer
// парковал бы вызывающую горутину навсегда на любой странице обхода (round-6
// audit sweep).
const DefaultZoneListTimeout = 5 * time.Second

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
	zones   geopb.ZoneServiceClient
	timeout time.Duration
}

// NewZoneClient оборачивает grpc-conn в typed adapter. ZoneService живёт на
// public-listener kacho-geo — публичный read-only справочник Geography.
// Per-call timeout — DefaultZoneListTimeout.
func NewZoneClient(conn grpc.ClientConnInterface) ZoneClient {
	return NewZoneClientWithTimeout(conn, DefaultZoneListTimeout)
}

// NewZoneClientWithTimeout — как NewZoneClient, но с явным per-call timeout'ом.
// timeout<=0 → DefaultZoneListTimeout.
func NewZoneClientWithTimeout(conn grpc.ClientConnInterface, timeout time.Duration) ZoneClient {
	if conn == nil {
		return nil
	}
	return &zoneClient{zones: geopb.NewZoneServiceClient(conn), timeout: resolveZoneTimeout(timeout)}
}

// NewZoneClientFromStubs — конструктор для тестов: принимает напрямую stub.
func NewZoneClientFromStubs(zones geopb.ZoneServiceClient) ZoneClient {
	return NewZoneClientFromStubsWithTimeout(zones, DefaultZoneListTimeout)
}

// NewZoneClientFromStubsWithTimeout — как NewZoneClientFromStubs, но с явным
// per-call timeout'ом (используется тестами concurrency/timeout-фиксов).
func NewZoneClientFromStubsWithTimeout(zones geopb.ZoneServiceClient, timeout time.Duration) ZoneClient {
	if zones == nil {
		return nil
	}
	return &zoneClient{zones: zones, timeout: resolveZoneTimeout(timeout)}
}

func resolveZoneTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultZoneListTimeout
	}
	return d
}

// ListZoneIDsInRegion — см. контракт ZoneClient.ListZoneIDsInRegion.
func (c *zoneClient) ListZoneIDsInRegion(ctx context.Context, regionID string) ([]string, error) {
	if regionID == "" {
		return nil, fmt.Errorf("%w: region_id is empty", domain.ErrInvalidArg)
	}

	var out []string
	pageToken := ""
	for {
		// Per-call deadline — bounds a SINGLE page's retry.OnUnavailable
		// operation, independent of the caller's own ctx (architecture.md
		// "Per-call deadline на КАЖДОМ внешнем вызове"). Re-derived every
		// iteration so a multi-page walk isn't bounded by one page's budget.
		pageCtx, cancel := context.WithTimeout(ctx, c.timeout)
		var resp *geopb.ListZonesResponse
		err := retry.OnUnavailable(pageCtx, func(ctx context.Context) error {
			var rerr error
			resp, rerr = c.zones.List(auth.PropagateOutgoing(ctx), &geopb.ListZonesRequest{
				PageSize:  zoneListPageSize,
				PageToken: pageToken,
			})
			return rerr
		})
		cancel()
		if err != nil {
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
