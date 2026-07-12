// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package vpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	vpcpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// DefaultSubnetGetTimeout — per-call deadline применяемый к SubnetService.Get,
// когда client построен без явного timeout'а (round-6 audit sweep, см.
// vpc.DefaultAddressGetTimeout для rationale).
const DefaultSubnetGetTimeout = 5 * time.Second

// Subnet — projection ресурса kacho-vpc.Subnet, ограниченная полями
// необходимыми consumer'ам NLB (Listener.subnet_id / Target.ip_ref validation).
//
// RegionID — denormalised mirror региона подсети, заполняемый adapter'ом для
// placement-coherence region-precheck: REGIONAL-подсеть несёт region_id напрямую
// (`vpc.Subnet.region_id`); ZONAL-подсеть — регион резолвится zone→region через
// geo.v1.ZoneService.Get (ZoneRegionResolver, ребро nlb→geo). Пусто, если adapter
// построен без zone-resolver'а (back-compat) — тогда region-precheck пропускается.
type Subnet struct {
	ID            string
	ProjectID     string
	NetworkID     string
	ZoneID        string
	RegionID      string // denormalised mirror; см. doc выше (adapter заполняет)
	PlacementType string // "REGIONAL" | "ZONAL" | "" (см. SubnetPlacementRegional)
	V4CIDRBlocks  []string
	V6CIDRBlocks  []string
}

// SubnetPlacementRegional — значение Subnet.PlacementType для region-scoped
// подсети (anycast-префикс, анонсируется active-active из здоровых зон региона).
// VIP LoadBalancer'а аллоцируется ТОЛЬКО из такой подсети.
const SubnetPlacementRegional = "REGIONAL"

// ZoneRegionResolver — узкий port zone→region резолва (geo.v1.ZoneService.Get →
// `Zone.region_id`). Subnet-adapter использует его для заполнения denormalised
// Subnet.RegionID у ZONAL-подсети. Инжектируется в composition root; nil →
// RegionID у ZONAL-подсети остаётся пустым (region-precheck пропускается —
// back-compat). Concrete `*geo.ZoneRegionClient` удовлетворяет структурно.
type ZoneRegionResolver interface {
	RegionOfZone(ctx context.Context, zoneID string) (string, error)
}

// SubnetClient — port-интерфейс для service-слоя.
type SubnetClient interface {
	// Get возвращает Subnet metadata. Семантика ошибок:
	//   - vpc NotFound                 → domain.ErrInvalidArg "Subnet <id> not found"
	//   - PermissionDenied             → domain.ErrInvalidArg (не лик'аем authz).
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable
	//   - InvalidArgument              → domain.ErrInvalidArg
	//   - Любая другая ошибка          → wrapped error без sentinel-обёртки.
	Get(ctx context.Context, subnetID string) (*Subnet, error)
}

// subnetClient — реализация SubnetClient через gRPC.
type subnetClient struct {
	cli        vpcpb.SubnetServiceClient
	timeout    time.Duration
	zoneRegion ZoneRegionResolver // nil → RegionID у ZONAL-подсети остаётся пустым
}

// NewSubnetClient оборачивает grpc-conn в typed adapter. conn — `clients.Build`.
// Per-call timeout — DefaultSubnetGetTimeout. Без zone-resolver'а: RegionID
// заполняется только для REGIONAL-подсети (из region_id); ZONAL остаётся пустым.
func NewSubnetClient(conn grpc.ClientConnInterface) SubnetClient {
	return NewSubnetClientWithTimeout(conn, DefaultSubnetGetTimeout)
}

// NewSubnetClientWithTimeout — как NewSubnetClient, но с явным per-call
// timeout'ом. timeout<=0 → DefaultSubnetGetTimeout.
func NewSubnetClientWithTimeout(conn grpc.ClientConnInterface, timeout time.Duration) SubnetClient {
	if conn == nil {
		return nil
	}
	return &subnetClient{cli: vpcpb.NewSubnetServiceClient(conn), timeout: resolveSubnetTimeout(timeout)}
}

// NewSubnetClientWithZoneRegion — adapter с zone→region резолвером: заполняет
// denormalised Subnet.RegionID для ZONAL-подсети (zone→region через geo).
// resolver nil → поведение как у NewSubnetClient (ZONAL RegionID пуст). Per-call
// timeout — DefaultSubnetGetTimeout.
func NewSubnetClientWithZoneRegion(conn grpc.ClientConnInterface, resolver ZoneRegionResolver) SubnetClient {
	if conn == nil {
		return nil
	}
	return &subnetClient{
		cli:        vpcpb.NewSubnetServiceClient(conn),
		timeout:    resolveSubnetTimeout(DefaultSubnetGetTimeout),
		zoneRegion: resolver,
	}
}

// NewSubnetClientFromStub — конструктор для тестов: принимает stub.
func NewSubnetClientFromStub(cli vpcpb.SubnetServiceClient) SubnetClient {
	return NewSubnetClientFromStubWithTimeout(cli, DefaultSubnetGetTimeout)
}

// NewSubnetClientFromStubWithTimeout — как NewSubnetClientFromStub, но с
// явным per-call timeout'ом (используется тестами concurrency/timeout-фиксов).
func NewSubnetClientFromStubWithTimeout(cli vpcpb.SubnetServiceClient, timeout time.Duration) SubnetClient {
	if cli == nil {
		return nil
	}
	return &subnetClient{cli: cli, timeout: resolveSubnetTimeout(timeout)}
}

func resolveSubnetTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultSubnetGetTimeout
	}
	return d
}

// Get — см. контракт SubnetClient.Get.
func (c *subnetClient) Get(ctx context.Context, subnetID string) (*Subnet, error) {
	if subnetID == "" {
		return nil, fmt.Errorf("%w: subnet_id is empty", domain.ErrInvalidArg)
	}

	ctx = auth.PropagateOutgoing(ctx)

	// Per-call deadline — bounds the ENTIRE retry.OnUnavailable operation,
	// independent of the caller's own ctx (architecture.md "Per-call deadline
	// на КАЖДОМ внешнем вызове").
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var resp *vpcpb.Subnet
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Get(ctx, &vpcpb.GetSubnetRequest{SubnetId: subnetID})
		return rerr
	}); err != nil {
		return nil, mapSubnetErr(subnetID, err)
	}

	// RegionID (denormalised mirror) — заполняется для placement-coherence
	// region-precheck: REGIONAL несёт region_id напрямую; ZONAL — zone→region
	// резолв через geo (fail-closed: geo недоступен → ошибка, не пустой RegionID).
	regionID, err := c.resolveRegion(ctx, resp)
	if err != nil {
		return nil, err
	}

	return &Subnet{
		ID:            resp.GetId(),
		ProjectID:     resp.GetProjectId(),
		NetworkID:     resp.GetNetworkId(),
		ZoneID:        resp.GetZoneId(),
		RegionID:      regionID,
		PlacementType: resp.GetPlacementType().String(),
		V4CIDRBlocks:  append([]string(nil), resp.GetV4CidrBlocks()...),
		V6CIDRBlocks:  append([]string(nil), resp.GetV6CidrBlocks()...),
	}, nil
}

// resolveRegion — регион подсети для denormalised Subnet.RegionID: REGIONAL несёт
// region_id напрямую (geo не нужен); ZONAL — резолв zone→region через geo
// (ZoneRegionResolver). Без resolver'а ZONAL RegionID пуст (back-compat).
// UNSPECIFIED/прочее → пусто.
func (c *subnetClient) resolveRegion(ctx context.Context, resp *vpcpb.Subnet) (string, error) {
	switch resp.GetPlacementType() {
	case vpcpb.SubnetPlacementType_REGIONAL:
		return resp.GetRegionId(), nil
	case vpcpb.SubnetPlacementType_ZONAL:
		if c.zoneRegion == nil || resp.GetZoneId() == "" {
			return "", nil
		}
		return c.zoneRegion.RegionOfZone(ctx, resp.GetZoneId())
	default:
		return "", nil
	}
}

// mapSubnetErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapSubnetErr(subnetID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("vpc subnet get %q: %w", subnetID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: Subnet %s not found", domain.ErrInvalidArg, subnetID)
	case codes.PermissionDenied:
		return fmt.Errorf("%w: Subnet %s not found", domain.ErrInvalidArg, subnetID)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: vpc subnet %s: %s", domain.ErrUnavailable, subnetID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: vpc subnet %s: %s", domain.ErrInvalidArg, subnetID, st.Message())
	default:
		return fmt.Errorf("vpc subnet get %q: %w", subnetID, err)
	}
}
