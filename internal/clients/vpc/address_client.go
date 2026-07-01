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

// Family enum для Address (минимальный набор для NLB — только v4 / v6).
const (
	AddressFamilyIPv4 = "IPV4"
	AddressFamilyIPv6 = "IPV6"
)

// Address — projection ресурса kacho-vpc.Address, ограниченная полями
// необходимыми consumer'ам NLB (Listener BYO attach + Get on Listener.Delete).
//
// Name — output-only поле, заполняется adapter'ом из `vpc.Address.name`.
// NLB Delete worker использует его для эвристики "BYO vs auto-alloc" (auto-
// alloc Address всегда создан с детерминированным именем `nlb-listener-<short-id>`;
// BYO Address имеет любое другое имя, заданное tenant'ом). Без этого поля Delete-
// branch было бы неотличимо.
type Address struct {
	ID        string
	ProjectID string
	Name      string
	Value     string // IP в строковой форме (resolved)
	Family    string // AddressFamilyIPv4 | AddressFamilyIPv6
	External  bool
	// SubnetID — подсеть internal-адреса (пусто для external). Нужна consumer'у
	// для placement-guard link'а (подсеть адреса == placement LB).
	SubnetID string
	UsedBy   *AddressOwner // nil если адрес свободен (Used=false)
}

// AddressOwner — текущий referrer Address-ресурса.
type AddressOwner struct {
	Kind string // "nlb_listener" | "compute_instance" |...
	ID   string
	Name string // display-имя потребителя для used_by-зеркала (vpc не резолвит
	//            имя сам — это создало бы cycle vpc→nlb; передаётся на SetReference).
}

// AddressClient — port-интерфейс для service-слоя.
type AddressClient interface {
	// Get возвращает Address metadata + resolved Value/Family/UsedBy.
	// Семантика ошибок:
	//   - vpc NotFound                 → domain.ErrInvalidArg "address <id> not found"
	//   - PermissionDenied             → domain.ErrInvalidArg (не лик'аем authz).
	//   - Unavailable/DeadlineExceeded → domain.ErrUnavailable
	//   - InvalidArgument              → domain.ErrInvalidArg
	//   - Любая другая ошибка          → wrapped error без sentinel-обёртки.
	Get(ctx context.Context, addressID string) (*Address, error)
}

// addressClient — реализация AddressClient через gRPC.
type addressClient struct {
	cli vpcpb.AddressServiceClient
}

// NewAddressClient оборачивает grpc-conn в typed adapter.
func NewAddressClient(conn grpc.ClientConnInterface) AddressClient {
	if conn == nil {
		return nil
	}
	return &addressClient{cli: vpcpb.NewAddressServiceClient(conn)}
}

// NewAddressClientFromStub — конструктор для тестов.
func NewAddressClientFromStub(cli vpcpb.AddressServiceClient) AddressClient {
	if cli == nil {
		return nil
	}
	return &addressClient{cli: cli}
}

// Get — см. контракт AddressClient.Get.
func (c *addressClient) Get(ctx context.Context, addressID string) (*Address, error) {
	if addressID == "" {
		return nil, fmt.Errorf("%w: address_id is empty", domain.ErrInvalidArg)
	}

	ctx = auth.PropagateOutgoing(ctx)

	var resp *vpcpb.Address
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.cli.Get(ctx, &vpcpb.GetAddressRequest{AddressId: addressID})
		return rerr
	}); err != nil {
		return nil, mapAddressErr(addressID, err)
	}

	addr := &Address{
		ID:        resp.GetId(),
		ProjectID: resp.GetProjectId(),
		Name:      resp.GetName(),
	}
	switch {
	case resp.GetExternalIpv4Address() != nil:
		addr.Value = resp.GetExternalIpv4Address().GetAddress()
		addr.Family = AddressFamilyIPv4
		addr.External = true
	case resp.GetInternalIpv4Address() != nil:
		addr.Value = resp.GetInternalIpv4Address().GetAddress()
		addr.Family = AddressFamilyIPv4
		addr.External = false
		addr.SubnetID = resp.GetInternalIpv4Address().GetSubnetId()
	case resp.GetInternalIpv6Address() != nil:
		addr.Value = resp.GetInternalIpv6Address().GetAddress()
		addr.Family = AddressFamilyIPv6
		addr.External = false
		addr.SubnetID = resp.GetInternalIpv6Address().GetSubnetId()
	case resp.GetExternalIpv6Address() != nil:
		addr.Value = resp.GetExternalIpv6Address().GetAddress()
		addr.Family = AddressFamilyIPv6
		addr.External = true
	}

	if resp.GetUsed() {
		// Берём первый Reference как primary owner (NLB Listener attach
		// модель: один Address — один owner; pattern «one-owner-per-resource»).
		if usedBy := resp.GetUsedBy(); len(usedBy) > 0 && usedBy[0].GetReferrer() != nil {
			addr.UsedBy = &AddressOwner{
				Kind: usedBy[0].GetReferrer().GetType(),
				ID:   usedBy[0].GetReferrer().GetId(),
			}
		}
	}

	return addr, nil
}

// mapAddressErr транслирует gRPC-status в domain-sentinel-ошибки.
func mapAddressErr(addressID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("vpc address get %q: %w", addressID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: address %s not found", domain.ErrInvalidArg, addressID)
	case codes.PermissionDenied:
		return fmt.Errorf("%w: address %s not found", domain.ErrInvalidArg, addressID)
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: vpc address %s: %s", domain.ErrUnavailable, addressID, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: vpc address %s: %s", domain.ErrInvalidArg, addressID, st.Message())
	default:
		return fmt.Errorf("vpc address get %q: %w", addressID, err)
	}
}
