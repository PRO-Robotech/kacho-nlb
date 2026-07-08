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

// DefaultRegionGetTimeout — per-call deadline применяемый к
// RegionService.Get, когда client построен без явного timeout'а. Без него
// retry.OnUnavailable не бывает бесконечным (MaxElapsed 30s), но НИ один из
// его retry-попыток не ограничен по времени сам по себе — зависший
// (не отвечающий, не Unavailable) peer парковал бы вызывающую горутину
// навсегда (см. check_client.go DefaultCheckTimeout — тот же класс проблемы).
const DefaultRegionGetTimeout = 5 * time.Second

// Region — lean projection ресурса kacho-geo.Region. Используется sync-валидацией
// NetworkLoadBalancer.region_id / TargetGroup.region_id. Зоны региона не
// перечисляются — kacho-nlb region-precheck зоны не использует (см. doc.go).
type Region struct {
	ID   string
	Name string
}

// RegionClient — port-интерфейс для service-слоя.
type RegionClient interface {
	// Get возвращает Region. Семантика ошибок:
	//   - kacho-geo NotFound (regionID не существует) → domain.ErrInvalidArg
	//     с текстом "Region <id> not found" — на input-time это не NotFound от
	//     tenant perspective, а bad input.
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable (fail-closed на мутации).
	//   - PermissionDenied (region — публичный read-only справочник, но edge-
	//     case при agg-route filtering) → domain.ErrInvalidArg "Region... not
	//     found" (не лик'аем authz).
	//   - Любая другая ошибка → wrapped error без sentinel-обёртки.
	Get(ctx context.Context, regionID string) (*Region, error)
}

// regionClient — реализация RegionClient через gRPC. Stateless pass-through:
// один geo.RegionService.Get-вызов под retry.OnUnavailable, без кэша.
type regionClient struct {
	regions geopb.RegionServiceClient
	timeout time.Duration
}

// NewRegionClient оборачивает grpc-conn в typed adapter. conn — `clients.Build`.
// RegionService живёт на public-listener kacho-geo (9090) — публичный read-only
// справочник Geography. Per-call timeout — DefaultRegionGetTimeout.
func NewRegionClient(conn grpc.ClientConnInterface) RegionClient {
	return NewRegionClientWithTimeout(conn, DefaultRegionGetTimeout)
}

// NewRegionClientWithTimeout — как NewRegionClient, но с явным per-call
// timeout'ом. timeout<=0 → DefaultRegionGetTimeout.
func NewRegionClientWithTimeout(conn grpc.ClientConnInterface, timeout time.Duration) RegionClient {
	if conn == nil {
		return nil
	}
	return &regionClient{regions: geopb.NewRegionServiceClient(conn), timeout: resolveRegionTimeout(timeout)}
}

// NewRegionClientFromStubs — конструктор для тестов: принимает напрямую stub.
func NewRegionClientFromStubs(regions geopb.RegionServiceClient) RegionClient {
	return NewRegionClientFromStubsWithTimeout(regions, DefaultRegionGetTimeout)
}

// NewRegionClientFromStubsWithTimeout — как NewRegionClientFromStubs, но с
// явным per-call timeout'ом (используется тестами concurrency/timeout-фиксов).
func NewRegionClientFromStubsWithTimeout(regions geopb.RegionServiceClient, timeout time.Duration) RegionClient {
	if regions == nil {
		return nil
	}
	return &regionClient{regions: regions, timeout: resolveRegionTimeout(timeout)}
}

func resolveRegionTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultRegionGetTimeout
	}
	return d
}

// Get — см. контракт RegionClient.Get.
func (c *regionClient) Get(ctx context.Context, regionID string) (*Region, error) {
	if regionID == "" {
		return nil, fmt.Errorf("%w: region_id is empty", domain.ErrInvalidArg)
	}

	// Per-call deadline — bounds the ENTIRE retry.OnUnavailable operation,
	// independent of the caller's own ctx (architecture.md "Per-call deadline
	// на КАЖДОМ внешнем вызове"; see check_client.go DefaultCheckTimeout for
	// the sibling rationale).
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var resp *geopb.Region
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.regions.Get(auth.PropagateOutgoing(ctx), &geopb.GetRegionRequest{RegionId: regionID})
		return rerr
	}); err != nil {
		return nil, mapRegionErr(regionID, err)
	}

	return &Region{ID: resp.GetId(), Name: resp.GetName()}, nil
}

// mapRegionErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapRegionErr(regionID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("geo region get %q: %w", regionID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: Region %s not found", domain.ErrInvalidArg, regionID)
	case codes.PermissionDenied:
		// edge-case: agg-route filtering — не лик'аем authz.
		return fmt.Errorf("%w: Region %s not found", domain.ErrInvalidArg, regionID)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: geo region %s: %s", domain.ErrUnavailable, regionID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: geo region %s: %s", domain.ErrInvalidArg, regionID, st.Message())
	default:
		return fmt.Errorf("geo region get %q: %w", regionID, err)
	}
}
