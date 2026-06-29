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

// SecurityGroup — projection ресурса kacho-vpc.SecurityGroup, ограниченная
// полями, нужными consumer'у NLB для sync-валидации
// NetworkLoadBalancer.security_group_ids (existence + same-network). source of
// truth = kacho-vpc.SecurityGroup.
type SecurityGroup struct {
	ID        string
	ProjectID string
	NetworkID string
	Name      string
}

// SecurityGroupClient — port-интерфейс для service-слоя.
type SecurityGroupClient interface {
	// Get возвращает SecurityGroup metadata. Семантика ошибок (зеркалит
	// NetworkClient):
	//   - vpc NotFound                 → domain.ErrInvalidArg "SecurityGroup <id> not found"
	//   - PermissionDenied             → domain.ErrInvalidArg (не лик'аем authz).
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable (fail-closed на мутации)
	//   - InvalidArgument              → domain.ErrInvalidArg
	//   - Любая другая ошибка          → wrapped error без sentinel-обёртки.
	Get(ctx context.Context, securityGroupID string) (*SecurityGroup, error)
}

// securityGroupClient — реализация SecurityGroupClient через gRPC. Stateless
// pass-through: один vpc.SecurityGroupService.Get-вызов под retry.OnUnavailable,
// без кэша.
type securityGroupClient struct {
	cli vpcpb.SecurityGroupServiceClient
}

// NewSecurityGroupClient оборачивает grpc-conn в typed adapter. conn —
// `clients.Build`. SecurityGroupService живёт на public-listener kacho-vpc.
func NewSecurityGroupClient(conn grpc.ClientConnInterface) SecurityGroupClient {
	if conn == nil {
		return nil
	}
	return &securityGroupClient{cli: vpcpb.NewSecurityGroupServiceClient(conn)}
}

// NewSecurityGroupClientFromStub — конструктор для тестов: принимает stub.
func NewSecurityGroupClientFromStub(cli vpcpb.SecurityGroupServiceClient) SecurityGroupClient {
	if cli == nil {
		return nil
	}
	return &securityGroupClient{cli: cli}
}

// Get — см. контракт SecurityGroupClient.Get.
func (c *securityGroupClient) Get(ctx context.Context, securityGroupID string) (*SecurityGroup, error) {
	if securityGroupID == "" {
		return nil, fmt.Errorf("%w: security_group_id is empty", domain.ErrInvalidArg)
	}

	ctx = auth.PropagateOutgoing(ctx)

	var resp *vpcpb.SecurityGroup
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Get(ctx, &vpcpb.GetSecurityGroupRequest{SecurityGroupId: securityGroupID})
		return rerr
	}); err != nil {
		return nil, mapSecurityGroupErr(securityGroupID, err)
	}

	return &SecurityGroup{
		ID:        resp.GetId(),
		ProjectID: resp.GetProjectId(),
		NetworkID: resp.GetNetworkId(),
		Name:      resp.GetName(),
	}, nil
}

// mapSecurityGroupErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapSecurityGroupErr(securityGroupID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("vpc security group get %q: %w", securityGroupID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: SecurityGroup %s not found", domain.ErrInvalidArg, securityGroupID)
	case codes.PermissionDenied:
		return fmt.Errorf("%w: SecurityGroup %s not found", domain.ErrInvalidArg, securityGroupID)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: vpc security group %s: %s", domain.ErrUnavailable, securityGroupID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: vpc security group %s: %s", domain.ErrInvalidArg, securityGroupID, st.Message())
	default:
		return fmt.Errorf("vpc security group get %q: %w", securityGroupID, err)
	}
}
