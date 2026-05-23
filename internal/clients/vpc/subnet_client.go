package vpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/retry"
	vpcpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// Subnet — projection ресурса kacho-vpc.Subnet, ограниченная полями
// необходимыми consumer'ам NLB (Listener.subnet_id / Target.ip_ref validation).
//
// RegionID — нет на vpc.Subnet (Subnet привязан к ZoneId; Region резолвится
// дополнительным compute.ZoneService.Get у NLB Wave 6); поле оставлено в
// projection как denormalised mirror (заполняется consumer'ом, не adapter'ом).
type Subnet struct {
	ID           string
	ProjectID    string
	NetworkID    string
	ZoneID       string
	RegionID     string // denormalised mirror; adapter оставляет пустым
	V4CIDRBlocks []string
	V6CIDRBlocks []string
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
	cli vpcpb.SubnetServiceClient
}

// NewSubnetClient оборачивает grpc-conn в typed adapter. conn — `clients.Build(...)`.
func NewSubnetClient(conn grpc.ClientConnInterface) SubnetClient {
	if conn == nil {
		return nil
	}
	return &subnetClient{cli: vpcpb.NewSubnetServiceClient(conn)}
}

// NewSubnetClientFromStub — конструктор для тестов: принимает stub.
func NewSubnetClientFromStub(cli vpcpb.SubnetServiceClient) SubnetClient {
	if cli == nil {
		return nil
	}
	return &subnetClient{cli: cli}
}

// Get — см. контракт SubnetClient.Get.
func (c *subnetClient) Get(ctx context.Context, subnetID string) (*Subnet, error) {
	if subnetID == "" {
		return nil, fmt.Errorf("%w: subnet_id is empty", domain.ErrInvalidArg)
	}

	var resp *vpcpb.Subnet
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Get(ctx, &vpcpb.GetSubnetRequest{SubnetId: subnetID})
		return rerr
	}); err != nil {
		return nil, mapSubnetErr(subnetID, err)
	}

	return &Subnet{
		ID:           resp.GetId(),
		ProjectID:    resp.GetProjectId(),
		NetworkID:    resp.GetNetworkId(),
		ZoneID:       resp.GetZoneId(),
		V4CIDRBlocks: append([]string(nil), resp.GetV4CidrBlocks()...),
		V6CIDRBlocks: append([]string(nil), resp.GetV6CidrBlocks()...),
	}, nil
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
