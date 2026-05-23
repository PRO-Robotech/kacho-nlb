package compute

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/retry"
	computepb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/compute/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// Region — projection ресурса kacho-compute.Region с привязанными зонами.
// Используется sync-валидацией NetworkLoadBalancer.region_id (Wave 6).
type Region struct {
	ID    string
	Name  string
	Zones []string
}

// RegionClient — port-интерфейс для service-слоя.
type RegionClient interface {
	// Get возвращает Region + его Zones. Семантика ошибок:
	//   - kacho-compute NotFound (regionID не существует) → domain.ErrInvalidArg
	//     с текстом "Region <id> not found" — на input-time это не NotFound от
	//     tenant perspective, а bad input.
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable
	//   - PermissionDenied (region — публичный read-only справочник, но edge-
	//     case при agg-route filtering) → domain.ErrInvalidArg "Region ... not
	//     found" (не лик'аем authz).
	//   - Любая другая ошибка → wrapped error без sentinel-обёртки.
	Get(ctx context.Context, regionID string) (*Region, error)
}

// regionClient — реализация RegionClient через gRPC.
type regionClient struct {
	regions computepb.RegionServiceClient
	zones   computepb.ZoneServiceClient
}

// NewRegionClient оборачивает grpc-conn в typed adapter. conn — `clients.Build(...)`.
// Region/Zone services живут на public-listener kacho-compute (`:9090`) —
// это публичные read-only справочники (см. kacho-compute CLAUDE.md §«Geography»).
func NewRegionClient(conn grpc.ClientConnInterface) RegionClient {
	if conn == nil {
		return nil
	}
	return &regionClient{
		regions: computepb.NewRegionServiceClient(conn),
		zones:   computepb.NewZoneServiceClient(conn),
	}
}

// NewRegionClientFromStubs — конструктор для тестов: принимает напрямую stub'ы.
func NewRegionClientFromStubs(regions computepb.RegionServiceClient, zones computepb.ZoneServiceClient) RegionClient {
	if regions == nil || zones == nil {
		return nil
	}
	return &regionClient{regions: regions, zones: zones}
}

// Get — см. контракт RegionClient.Get.
func (c *regionClient) Get(ctx context.Context, regionID string) (*Region, error) {
	if regionID == "" {
		return nil, fmt.Errorf("%w: region_id is empty", domain.ErrInvalidArg)
	}

	// 1) Region existence.
	var regionResp *computepb.Region
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		regionResp, rerr = c.regions.Get(ctx, &computepb.GetRegionRequest{RegionId: regionID})
		return rerr
	}); err != nil {
		return nil, mapRegionErr(regionID, err)
	}

	// 2) Zones in region: ZoneService.List не поддерживает filter; листаем все,
	// фильтруем по RegionId. Для текущей фазы (3 zones / 1 region) это
	// один RTT с ≤ kilobytes payload — приемлемо. Pagination — handled.
	zones, err := c.listZonesForRegion(ctx, regionID)
	if err != nil {
		return nil, err
	}

	return &Region{
		ID:    regionResp.GetId(),
		Name:  regionResp.GetName(),
		Zones: zones,
	}, nil
}

// listZonesForRegion перечисляет все ZoneService.List страницы и собирает Zone.id
// для запрошенного regionID. Pagination: page_size = 1000 — purpose-fit для
// kacho-compute Geography (текущий seed — 3 зоны; даже 10x запас).
func (c *regionClient) listZonesForRegion(ctx context.Context, regionID string) ([]string, error) {
	var out []string
	pageToken := ""
	for {
		var resp *computepb.ListZonesResponse
		if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
			var rerr error
			resp, rerr = c.zones.List(ctx, &computepb.ListZonesRequest{
				PageSize:  1000,
				PageToken: pageToken,
			})
			return rerr
		}); err != nil {
			return nil, mapRegionErr(regionID, err)
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

// mapRegionErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapRegionErr(regionID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("compute region get %q: %w", regionID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: Region %s not found", domain.ErrInvalidArg, regionID)
	case codes.PermissionDenied:
		// edge-case: agg-route filtering — не лик'аем authz.
		return fmt.Errorf("%w: Region %s not found", domain.ErrInvalidArg, regionID)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: compute region %s: %s", domain.ErrUnavailable, regionID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: compute region %s: %s", domain.ErrInvalidArg, regionID, st.Message())
	default:
		return fmt.Errorf("compute region get %q: %w", regionID, err)
	}
}
