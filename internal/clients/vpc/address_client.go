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

// DefaultAddressGetTimeout — per-call deadline применяемый к AddressService.Get,
// когда client построен без явного timeout'а. Тот же класс проблемы, что
// DefaultCheckTimeout (iam) / DefaultRegionGetTimeout (geo) / DefaultInstanceGetTimeout
// (compute): без него зависший (не отвечающий, не Unavailable) vpc-peer парковал бы
// вызывающую горутину навсегда (round-6 audit sweep — architecture.md "per-call
// deadline на КАЖДОМ внешнем вызове").
const DefaultAddressGetTimeout = 5 * time.Second

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
	cli     vpcpb.AddressServiceClient
	timeout time.Duration
}

// NewAddressClient оборачивает grpc-conn в typed adapter. Per-call timeout —
// DefaultAddressGetTimeout.
func NewAddressClient(conn grpc.ClientConnInterface) AddressClient {
	return NewAddressClientWithTimeout(conn, DefaultAddressGetTimeout)
}

// NewAddressClientWithTimeout — как NewAddressClient, но с явным per-call
// timeout'ом. timeout<=0 → DefaultAddressGetTimeout.
func NewAddressClientWithTimeout(conn grpc.ClientConnInterface, timeout time.Duration) AddressClient {
	if conn == nil {
		return nil
	}
	return &addressClient{cli: vpcpb.NewAddressServiceClient(conn), timeout: resolveAddressTimeout(timeout)}
}

// NewAddressClientFromStub — конструктор для тестов.
func NewAddressClientFromStub(cli vpcpb.AddressServiceClient) AddressClient {
	return NewAddressClientFromStubWithTimeout(cli, DefaultAddressGetTimeout)
}

// NewAddressClientFromStubWithTimeout — как NewAddressClientFromStub, но с
// явным per-call timeout'ом (используется тестами concurrency/timeout-фиксов).
func NewAddressClientFromStubWithTimeout(cli vpcpb.AddressServiceClient, timeout time.Duration) AddressClient {
	if cli == nil {
		return nil
	}
	return &addressClient{cli: cli, timeout: resolveAddressTimeout(timeout)}
}

func resolveAddressTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultAddressGetTimeout
	}
	return d
}

// Get — см. контракт AddressClient.Get.
func (c *addressClient) Get(ctx context.Context, addressID string) (*Address, error) {
	if addressID == "" {
		return nil, fmt.Errorf("%w: address_id is empty", domain.ErrInvalidArg)
	}

	ctx = auth.PropagateOutgoing(ctx)

	// Per-call deadline — bounds the ENTIRE retry.OnUnavailable operation,
	// independent of the caller's own ctx (architecture.md "Per-call deadline
	// на КАЖДОМ внешнем вызове"; see iam.DefaultCheckTimeout for the sibling
	// rationale).
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

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
