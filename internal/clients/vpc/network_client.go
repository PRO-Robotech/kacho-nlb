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
	vpcpb "github.com/PRO-Robotech/kacho-vpc/proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// Network — projection ресурса kacho-vpc.Network, ограниченная полями, нужными
// consumer'у NLB для sync-валидации NetworkLoadBalancer.network_id (INTERNAL
// scheme). source of truth = kacho-vpc.Network.
type Network struct {
	ID        string
	ProjectID string
	Name      string
}

// NetworkClient — port-интерфейс для service-слоя.
type NetworkClient interface {
	// Get возвращает Network metadata. Семантика ошибок (зеркалит SubnetClient):
	//   - vpc NotFound                 → domain.ErrInvalidArg "Network <id> not found"
	//   - PermissionDenied             → domain.ErrInvalidArg (не лик'аем authz).
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable (fail-closed на мутации)
	//   - InvalidArgument              → domain.ErrInvalidArg
	//   - Любая другая ошибка          → wrapped error без sentinel-обёртки.
	Get(ctx context.Context, networkID string) (*Network, error)
}

// networkClient — реализация NetworkClient через gRPC. Stateless pass-through:
// один vpc.NetworkService.Get-вызов под retry.OnUnavailable, без кэша.
type networkClient struct {
	cli vpcpb.NetworkServiceClient
}

// NewNetworkClient оборачивает grpc-conn в typed adapter. conn — `clients.Build`.
// NetworkService живёт на public-listener kacho-vpc.
func NewNetworkClient(conn grpc.ClientConnInterface) NetworkClient {
	if conn == nil {
		return nil
	}
	return &networkClient{cli: vpcpb.NewNetworkServiceClient(conn)}
}

// NewNetworkClientFromStub — конструктор для тестов: принимает stub.
func NewNetworkClientFromStub(cli vpcpb.NetworkServiceClient) NetworkClient {
	if cli == nil {
		return nil
	}
	return &networkClient{cli: cli}
}

// Get — см. контракт NetworkClient.Get.
func (c *networkClient) Get(ctx context.Context, networkID string) (*Network, error) {
	if networkID == "" {
		return nil, fmt.Errorf("%w: network_id is empty", domain.ErrInvalidArg)
	}

	ctx = auth.PropagateOutgoing(ctx)

	var resp *vpcpb.Network
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Get(ctx, &vpcpb.GetNetworkRequest{NetworkId: networkID})
		return rerr
	}); err != nil {
		return nil, mapNetworkErr(networkID, err)
	}

	return &Network{
		ID:        resp.GetId(),
		ProjectID: resp.GetProjectId(),
		Name:      resp.GetName(),
	}, nil
}

// mapNetworkErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapNetworkErr(networkID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("vpc network get %q: %w", networkID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: Network %s not found", domain.ErrInvalidArg, networkID)
	case codes.PermissionDenied:
		return fmt.Errorf("%w: Network %s not found", domain.ErrInvalidArg, networkID)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: vpc network %s: %s", domain.ErrUnavailable, networkID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: vpc network %s: %s", domain.ErrInvalidArg, networkID, st.Message())
	default:
		return fmt.Errorf("vpc network get %q: %w", networkID, err)
	}
}
