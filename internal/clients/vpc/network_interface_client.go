// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package vpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/auth"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	vpcpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// NetworkInterface — projection NIC, ограниченная полями необходимыми consumer'ам
// NLB (Target.nic_id resolve в TG.AddTargets).
//
// PrimaryV4Address — это **первый** id из NIC.V4AddressIds (kacho-vpc model:
// один NIC ≤ 1 v4 / ≤ 1 v6 Address). Resolve в IP-строку требует
// AddressClient.Get; здесь оставляем raw Address.id.
type NetworkInterface struct {
	ID               string
	ProjectID        string
	SubnetID         string
	PrimaryV4Address string // Address.id (resolve в IP — отдельно через AddressClient)
	Status           string
}

// NetworkInterfaceClient — port-интерфейс для service-слоя.
type NetworkInterfaceClient interface {
	// Get возвращает NIC metadata. Семантика ошибок:
	//   - vpc NotFound                 → domain.ErrInvalidArg "NetworkInterface <id> not found"
	//   - PermissionDenied             → domain.ErrInvalidArg (не лик'аем authz).
	//   - FailedPrecondition           → domain.ErrFailedPrecondition (NIC в DELETING).
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable
	//   - InvalidArgument              → domain.ErrInvalidArg
	//   - Любая другая ошибка          → wrapped error без sentinel-обёртки.
	Get(ctx context.Context, nicID string) (*NetworkInterface, error)
}

// networkInterfaceClient — реализация NetworkInterfaceClient через gRPC.
type networkInterfaceClient struct {
	cli vpcpb.NetworkInterfaceServiceClient
}

// NewNetworkInterfaceClient оборачивает grpc-conn в typed adapter.
func NewNetworkInterfaceClient(conn grpc.ClientConnInterface) NetworkInterfaceClient {
	if conn == nil {
		return nil
	}
	return &networkInterfaceClient{cli: vpcpb.NewNetworkInterfaceServiceClient(conn)}
}

// NewNetworkInterfaceClientFromStub — конструктор для тестов.
func NewNetworkInterfaceClientFromStub(cli vpcpb.NetworkInterfaceServiceClient) NetworkInterfaceClient {
	if cli == nil {
		return nil
	}
	return &networkInterfaceClient{cli: cli}
}

// Get — см. контракт NetworkInterfaceClient.Get.
func (c *networkInterfaceClient) Get(ctx context.Context, nicID string) (*NetworkInterface, error) {
	if nicID == "" {
		return nil, fmt.Errorf("%w: nic_id is empty", domain.ErrInvalidArg)
	}

	ctx = auth.PropagateOutgoing(ctx)

	var resp *vpcpb.NetworkInterface
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Get(ctx, &vpcpb.GetNetworkInterfaceRequest{NetworkInterfaceId: nicID})
		return rerr
	}); err != nil {
		return nil, mapNICErr(nicID, err)
	}

	var primaryV4 string
	if v4ids := resp.GetV4AddressIds(); len(v4ids) > 0 {
		primaryV4 = v4ids[0]
	}

	return &NetworkInterface{
		ID:               resp.GetId(),
		ProjectID:        resp.GetProjectId(),
		SubnetID:         resp.GetSubnetId(),
		PrimaryV4Address: primaryV4,
		Status:           resp.GetStatus().String(),
	}, nil
}

// mapNICErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapNICErr(nicID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("vpc network interface get %q: %w", nicID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: NetworkInterface %s not found", domain.ErrInvalidArg, nicID)
	case codes.PermissionDenied:
		return fmt.Errorf("%w: NetworkInterface %s not found", domain.ErrInvalidArg, nicID)
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: vpc network interface %s: %s", domain.ErrFailedPrecondition, nicID, st.Message())
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: vpc network interface %s: %s", domain.ErrUnavailable, nicID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: vpc network interface %s: %s", domain.ErrInvalidArg, nicID, st.Message())
	default:
		return fmt.Errorf("vpc network interface get %q: %w", nicID, err)
	}
}
