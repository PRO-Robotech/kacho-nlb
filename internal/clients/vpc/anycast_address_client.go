// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package vpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/retry"
	vpcpb "github.com/PRO-Robotech/kacho-vpc/proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// AnycastAddressClient — port для lifecycle anycast-VIP LoadBalancer'а
// (per-family fan-out саги): аллокация network-scoped anycast-адреса из
// AnycastAddressPool (auto), привязка принесённого tenant'ом Address (BYO,
// server-side ownership/family CAS-guard), и release (FreeIP/ClearReference)
// в compensation/reconcile.
type AnycastAddressClient interface {
	// AllocateAnycast создаёт network-scoped anycast Address (auto) из пула +
	// atomic SetReference(owner). Семантика ошибок (kacho-vpc → sentinel):
	//   - FailedPrecondition (пул не доступен в сети / исчерпан) → domain.ErrFailedPrecondition
	//     (текст пробрасывается от vpc — анти-oracle на стороне vpc)
	//   - InvalidArgument                                       → domain.ErrInvalidArg
	//   - Unavailable/DeadlineExceeded                          → domain.ErrUnavailable
	AllocateAnycast(ctx context.Context, req AllocateAnycastRequest) (*AllocateResponse, error)

	// AttachAnycastBYO привязывает принесённый tenant'ом anycast Address к LB
	// через vpc.InternalAddressService.SetAddressReference с server-side CAS-guard
	// (expect_project_id / expect_ip_version). Mismatch ownership/family → generic
	// domain.ErrInvalidArg "Illegal argument addressId" (анти-oracle: без
	// подтверждения чужого ownership/семейства). Возвращает resolved-значение
	// адреса (Get после успешной привязки — адрес уже наш).
	AttachAnycastBYO(ctx context.Context, req AttachAnycastBYORequest) (*AllocateResponse, error)

	// FreeIP / ClearReference — release аллоцированного/привязанного VIP в
	// compensation и reconcile (auto → FreeIP, byo → ClearReference). Идемпотентны
	// (NotFound → успех).
	FreeIP(ctx context.Context, addressID string, owner AddressOwner) error
	ClearReference(ctx context.Context, addressID string, owner AddressOwner) error
}

// AllocateAnycastRequest — параметры auto-аллокации anycast-VIP одного семейства.
type AllocateAnycastRequest struct {
	ProjectID     string // folder, владеющий Address-строкой
	Name          string // имя ресурса (детерминированное, для reconcile-by-name)
	NetworkID     string // сеть, к которой scope'ится anycast-VIP
	Family        string // AddressFamilyIPv4 | AddressFamilyIPv6
	AnycastPoolID string // опционально; пусто → платформенный is_default-пул сети
	Owner         AddressOwner
}

// AttachAnycastBYORequest — параметры BYO-привязки принесённого Address.
type AttachAnycastBYORequest struct {
	AddressID       string
	Owner           AddressOwner
	ExpectProjectID string // server-side CAS-guard: Address принадлежит этому проекту
	ExpectFamily    string // server-side CAS-guard: семейство Address совпадает
}

// NewAnycastAddressClient — конструктор AnycastAddressClient (тот же gRPC-stack,
// что и InternalAddressClient: public AddressService/OperationService + internal
// InternalAddressService). nil-conn → nil (peer не сконфигурирован).
func NewAnycastAddressClient(publicConn, internalConn grpc.ClientConnInterface) AnycastAddressClient {
	c := NewInternalAddressClient(publicConn, internalConn)
	if c == nil {
		return nil
	}
	return c.(*internalAddressClient)
}

// AllocateAnycast — см. контракт AnycastAddressClient.AllocateAnycast.
func (c *internalAddressClient) AllocateAnycast(
	ctx context.Context, req AllocateAnycastRequest,
) (*AllocateResponse, error) {
	switch {
	case req.ProjectID == "":
		return nil, fmt.Errorf("%w: project_id is empty", domain.ErrInvalidArg)
	case req.NetworkID == "":
		return nil, fmt.Errorf("%w: network_id is empty", domain.ErrInvalidArg)
	case req.Owner.Kind == "" || req.Owner.ID == "":
		return nil, fmt.Errorf("%w: owner is empty", domain.ErrInvalidArg)
	}
	ipVer, err := vpcIPVersion(req.Family)
	if err != nil {
		return nil, err
	}
	createReq := &vpcpb.CreateAddressRequest{
		ProjectId: req.ProjectID,
		Name:      req.Name,
		AddressSpec: &vpcpb.CreateAddressRequest_AnycastAddressSpec{
			AnycastAddressSpec: &vpcpb.AnycastAddressSpec{
				NetworkId:     req.NetworkID,
				IpVersion:     ipVer,
				AnycastPoolId: req.AnycastPoolID,
			},
		},
	}
	return c.allocFromCreate(ctx, createReq, req.Owner, func(a *vpcpb.Address) string {
		return a.GetAnycastAddress().GetAddress()
	})
}

// AttachAnycastBYO — см. контракт AnycastAddressClient.AttachAnycastBYO.
func (c *internalAddressClient) AttachAnycastBYO(
	ctx context.Context, req AttachAnycastBYORequest,
) (*AllocateResponse, error) {
	switch {
	case req.AddressID == "":
		return nil, fmt.Errorf("%w: address_id is empty", domain.ErrInvalidArg)
	case req.Owner.Kind == "" || req.Owner.ID == "":
		return nil, fmt.Errorf("%w: owner is empty", domain.ErrInvalidArg)
	}
	expectVer, err := vpcIPVersion(req.ExpectFamily)
	if err != nil {
		return nil, err
	}

	// Server-side ownership/family CAS-guard в vpc (атомарно в той же tx, что и
	// SetReference). Mismatch / not-found → generic InvalidArgument (анти-oracle).
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.internal.SetAddressReference(ctx, &vpcpb.SetAddressReferenceRequest{
			AddressId:       req.AddressID,
			ReferrerType:    req.Owner.Kind,
			ReferrerId:      req.Owner.ID,
			ExpectProjectId: req.ExpectProjectID,
			ExpectIpVersion: expectVer,
		})
		if rerr == nil {
			return nil
		}
		st, ok := status.FromError(rerr)
		if !ok {
			return fmt.Errorf("vpc set anycast reference %q: %w", req.AddressID, rerr)
		}
		switch st.Code() {
		case codes.AlreadyExists:
			return fmt.Errorf("%w: address %s already used by another resource", domain.ErrFailedPrecondition, req.AddressID)
		case codes.NotFound, codes.InvalidArgument, codes.PermissionDenied:
			// Анти-oracle: чужой проект/семейство/несуществующий id — единый generic.
			return fmt.Errorf("%w: Illegal argument addressId", domain.ErrInvalidArg)
		default:
			return fmt.Errorf("vpc set anycast reference %q: %w", req.AddressID, rerr)
		}
	}); err != nil {
		return nil, err
	}

	// Привязка прошла CAS-guard → адрес наш; читаем resolved-значение.
	addr, err := c.resolveAddressValue(ctx, req.AddressID)
	if err != nil {
		return nil, err
	}
	return &AllocateResponse{AddressID: req.AddressID, Value: addr}, nil
}

// resolveAddressValue — Get Address + извлечение resolved IP-строки (любое
// семейство, включая anycast). Используется после успешной BYO-привязки.
func (c *internalAddressClient) resolveAddressValue(ctx context.Context, addressID string) (string, error) {
	var resp *vpcpb.Address
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		resp, rerr = c.addrs.Get(ctx, &vpcpb.GetAddressRequest{AddressId: addressID})
		return rerr
	}); err != nil {
		return "", mapAllocErr(addressID, err)
	}
	switch {
	case resp.GetAnycastAddress() != nil:
		return resp.GetAnycastAddress().GetAddress(), nil
	case resp.GetInternalIpv4Address() != nil:
		return resp.GetInternalIpv4Address().GetAddress(), nil
	case resp.GetInternalIpv6Address() != nil:
		return resp.GetInternalIpv6Address().GetAddress(), nil
	case resp.GetExternalIpv4Address() != nil:
		return resp.GetExternalIpv4Address().GetAddress(), nil
	case resp.GetExternalIpv6Address() != nil:
		return resp.GetExternalIpv6Address().GetAddress(), nil
	}
	return "", nil
}

// vpcIPVersion — domain-семейство ("IPV4"/"IPV6") → vpc proto enum.
func vpcIPVersion(family string) (vpcpb.Address_IpVersion, error) {
	switch family {
	case AddressFamilyIPv4:
		return vpcpb.Address_IPV4, nil
	case AddressFamilyIPv6:
		return vpcpb.Address_IPV6, nil
	}
	return vpcpb.Address_IP_VERSION_UNSPECIFIED, fmt.Errorf("%w: unsupported ip family %q", domain.ErrInvalidArg, family)
}

// compile-time: internalAddressClient удовлетворяет обоим port'ам.
var _ AnycastAddressClient = (*internalAddressClient)(nil)
