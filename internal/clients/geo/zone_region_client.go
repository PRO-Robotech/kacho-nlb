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

// DefaultZoneGetTimeout — per-call deadline применяемый к ZoneService.Get, когда
// client построен без явного timeout'а. Тот же класс проблемы, что
// DefaultRegionGetTimeout (тот же пакет): без него зависший (не отвечающий, не
// Unavailable) geo-peer парковал бы вызывающую горутину навсегда (architecture.md
// "per-call deadline на КАЖДОМ внешнем вызове").
const DefaultZoneGetTimeout = 5 * time.Second

// ZoneRegionClient резолвит регион зоны через geo.v1.ZoneService.Get
// (`Zone.region_id`). Используется nlb subnet-client'ом для заполнения
// denormalised `Subnet.RegionID` у ZONAL-подсети (REGIONAL несёт region_id
// напрямую) — placement-coherence region-precheck (ребро nlb→geo). Отдельный
// stateless pass-through, без кэша: zone→region — request-path прекондишн на
// мутации, ошибка fail-closed.
type ZoneRegionClient struct {
	zones   geopb.ZoneServiceClient
	timeout time.Duration
}

// NewZoneRegionClient оборачивает grpc-conn в typed adapter. conn — `clients.Build`
// (public-listener kacho-geo). nil conn → nil (graceful start без geo).
func NewZoneRegionClient(conn grpc.ClientConnInterface) *ZoneRegionClient {
	if conn == nil {
		return nil
	}
	return &ZoneRegionClient{zones: geopb.NewZoneServiceClient(conn), timeout: DefaultZoneGetTimeout}
}

// NewZoneRegionClientFromStub — конструктор для тестов: принимает напрямую stub.
func NewZoneRegionClientFromStub(zones geopb.ZoneServiceClient) *ZoneRegionClient {
	if zones == nil {
		return nil
	}
	return &ZoneRegionClient{zones: zones, timeout: DefaultZoneGetTimeout}
}

// RegionOfZone возвращает region_id, которому принадлежит zoneID. Семантика ошибок
// (fail-closed для мутаций — недоступность/отсутствие owner'а НЕ трактуется как
// «валидно»):
//   - geo NotFound (зона отсутствует — dangling ref, зону удалили после
//     Subnet.Create) → domain.ErrUnavailable (coherence неверифицируема → fail-closed).
//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable.
//   - PermissionDenied → domain.ErrUnavailable (не лик'аем authz, но не «валидно»).
//   - прочее → wrapped error без sentinel-обёртки.
func (c *ZoneRegionClient) RegionOfZone(ctx context.Context, zoneID string) (string, error) {
	if zoneID == "" {
		return "", fmt.Errorf("%w: zone_id is empty", domain.ErrInvalidArg)
	}

	// Per-call deadline — bounds the ENTIRE retry.OnUnavailable operation,
	// independent of the caller's own ctx (architecture.md "Per-call deadline
	// на КАЖДОМ внешнем вызове").
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var resp *geopb.Zone
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.zones.Get(auth.PropagateOutgoing(ctx), &geopb.GetZoneRequest{ZoneId: zoneID})
		return rerr
	}); err != nil {
		return "", mapZoneGetErr(zoneID, err)
	}
	return resp.GetRegionId(), nil
}

// mapZoneGetErr транслирует gRPC-status ZoneService.Get в domain-sentinel-ошибки
// (fail-closed: любой не-успех zone→region резолва → ErrUnavailable, кроме
// нераспознанного raw-err без обёртки).
func mapZoneGetErr(zoneID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("geo zone get %q: %w", zoneID, err)
	}
	switch st.Code() {
	case codes.NotFound, codes.PermissionDenied, codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: geo zone %s: %s", domain.ErrUnavailable, zoneID, st.Message())
	default:
		return fmt.Errorf("geo zone get %q: %w", zoneID, err)
	}
}
