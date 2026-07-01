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

	"google.golang.org/protobuf/types/known/anypb"

	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
	"github.com/PRO-Robotech/kacho-corelib/retry"
	vpcpb "github.com/PRO-Robotech/kacho-vpc/proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// Polling параметры для in-flight VPC operations (Create/Delete Address).
// kacho-vpc control-plane операции завершаются за ~1с (no real data-plane).
const (
	vpcOpPollInterval = 50 * time.Millisecond
	vpcOpPollTimeout  = 15 * time.Second
)

// AllocateExternalIPRequest — параметры аллокации внешнего VIP под Listener.
type AllocateExternalIPRequest struct {
	ProjectID string // folder owning the Address row
	Name      string // resource name (unique within ProjectID; suffix runId in tests)
	ZoneID    string // zone из которого аллоцировать IP
	Owner     AddressOwner
}

// AllocateInternalIPRequest — параметры аллокации внутреннего VIP под Listener.
type AllocateInternalIPRequest struct {
	ProjectID string
	Name      string
	SubnetID  string // обязательный scope для internal allocation
	Owner     AddressOwner
}

// AllocateResponse — результат аллокации IP (auto-alloc флоу).
type AllocateResponse struct {
	AddressID string
	Value     string // resolved IP в строковой форме
	PoolID    string // pool_id для external (пусто для internal)
}

// AttachExistingRequest — параметры link-привязки принесённого tenant'ом Address к
// owner-ресурсу (LoadBalancer VIP). Server-side привязка идёт через
// InternalAddressService.SetAddressReference (атомарный CAS в vpc); mismatch /
// not-found мапится в generic InvalidArgument (анти-oracle). Owned=false —
// tenant-owned адрес (link): release снимает только референс, адрес уцелевает.
type AttachExistingRequest struct {
	AddressID string
	Owner     AddressOwner
	Owned     bool
}

// InternalAddressClient — port-интерфейс для service-слоя.
// Каждый метод выполняет атомарную операцию VPC IPAM и устанавливает
// referrer на новосозданном/изменённом Address-ресурсе.
type InternalAddressClient interface {
	// AllocateExternalIP создаёт внешний Address (auto-alloc IP из дефолтного
	// pool в zone) + atomic SetReference. Семантика ошибок:
	//   - FailedPrecondition (pool exhausted / zone unavailable) → domain.ErrFailedPrecondition
	//   - InvalidArgument                                       → domain.ErrInvalidArg
	//   - Unavailable/DeadlineExceeded                          → domain.ErrUnavailable
	AllocateExternalIP(ctx context.Context, req AllocateExternalIPRequest) (*AllocateResponse, error)

	// AllocateInternalIP создаёт внутренний Address в указанной subnet +
	// atomic SetReference.
	AllocateInternalIP(ctx context.Context, req AllocateInternalIPRequest) (*AllocateResponse, error)

	// AllocateExternalIPv6 — как AllocateExternalIP, но аллоцирует внешний
	// IPv6-VIP (external_ipv6 pool-IPAM). Контракт request/response и семантика
	// ошибок идентичны AllocateExternalIP.
	AllocateExternalIPv6(ctx context.Context, req AllocateExternalIPRequest) (*AllocateResponse, error)

	// AllocateInternalIPv6 — как AllocateInternalIP, но аллоцирует внутренний
	// IPv6-VIP из subnet.v6_cidr_blocks. Контракт идентичен AllocateInternalIP.
	AllocateInternalIPv6(ctx context.Context, req AllocateInternalIPRequest) (*AllocateResponse, error)

	// AttachExisting привязывает принесённый tenant'ом Address к owner-ресурсу
	// через InternalAddressService.SetAddressReference. Семантика ошибок
	// (анти-oracle: не подтверждаем чужой ownership/семейство/существование):
	//   - AlreadyExists (address занят другим referrer)  → domain.ErrFailedPrecondition
	//   - NotFound / InvalidArgument / PermissionDenied  → generic domain.ErrInvalidArg
	//                                                       "Illegal argument addressId"
	//   - Unavailable/DeadlineExceeded                   → domain.ErrUnavailable
	// Возвращает resolved-значение привязанного Address (Get после успеха).
	AttachExisting(ctx context.Context, req AttachExistingRequest) (*AllocateResponse, error)

	// FreeIP освобождает Address (idempotent через AddressService.Delete →
	// NotFound трактуется как успех). ClearReference вызывается автоматически
	// kacho-vpc при Delete.
	FreeIP(ctx context.Context, addressID string, owner AddressOwner) error

	// SetReference — атомарный CAS Set used_by=owner на существующем Address.
	// owned помечает референс как owned (auto-alloc, lifecycle связан) либо
	// used_by (linked, tenant-owned). Семантика ошибок:
	//   - AlreadyExists (address уже занят другим owner) → domain.ErrFailedPrecondition
	//   - NotFound                                       → domain.ErrInvalidArg
	SetReference(ctx context.Context, addressID string, owner AddressOwner, owned bool) error

	// ClearReference — снимает used_by с Address (Listener.Delete release BYO).
	// Идемпотентно: NotFound → успех.
	ClearReference(ctx context.Context, addressID string, owner AddressOwner) error
}

// internalAddressClient — реализация InternalAddressClient через gRPC.
//
// Использует ТРИ generated stub'а:
//   - AddressServiceClient        (public)  — Create / Delete (auto-alloc flow).
//   - InternalAddressServiceClient (internal) — SetReference / ClearReference.
//   - OperationServiceClient      (public)  — poll Operation на Create/Delete.
type internalAddressClient struct {
	addrs    vpcpb.AddressServiceClient
	internal vpcpb.InternalAddressServiceClient
	ops      operationpb.OperationServiceClient
}

// NewInternalAddressClient оборачивает grpc-conn'ы в typed adapter.
//
// publicConn — kacho-vpc public listener (`:9090`); содержит AddressService +
// OperationService.
// internalConn — kacho-vpc internal listener (`:9091`); содержит
// InternalAddressService (SetReference / ClearReference — не публикуются на
// external endpoint, Internal-only).
func NewInternalAddressClient(publicConn, internalConn grpc.ClientConnInterface) InternalAddressClient {
	if publicConn == nil || internalConn == nil {
		return nil
	}
	return &internalAddressClient{
		addrs:    vpcpb.NewAddressServiceClient(publicConn),
		internal: vpcpb.NewInternalAddressServiceClient(internalConn),
		ops:      operationpb.NewOperationServiceClient(publicConn),
	}
}

// NewInternalAddressClientFromStubs — конструктор для тестов.
func NewInternalAddressClientFromStubs(
	addrs vpcpb.AddressServiceClient,
	internal vpcpb.InternalAddressServiceClient,
	ops operationpb.OperationServiceClient,
) InternalAddressClient {
	if addrs == nil || internal == nil || ops == nil {
		return nil
	}
	return &internalAddressClient{addrs: addrs, internal: internal, ops: ops}
}

// AllocateExternalIP — см. контракт InternalAddressClient.AllocateExternalIP.
func (c *internalAddressClient) AllocateExternalIP(
	ctx context.Context, req AllocateExternalIPRequest,
) (*AllocateResponse, error) {
	if err := validateExternalReq(req); err != nil {
		return nil, err
	}
	createReq := &vpcpb.CreateAddressRequest{
		ProjectId: req.ProjectID,
		Name:      req.Name,
		AddressSpec: &vpcpb.CreateAddressRequest_ExternalIpv4AddressSpec{
			ExternalIpv4AddressSpec: &vpcpb.ExternalIpv4AddressSpec{ZoneId: req.ZoneID},
		},
	}
	return c.allocFromCreate(ctx, createReq, req.Owner, func(a *vpcpb.Address) string {
		return a.GetExternalIpv4Address().GetAddress()
	})
}

// AllocateExternalIPv6 — см. контракт InternalAddressClient.AllocateExternalIPv6.
// Зеркало AllocateExternalIP для external_ipv6: AddressService.Create с
// external-IPv6-spec (vpc аллоцирует v6-VIP из EXTERNAL_PUBLIC v6-pool в writer-TX).
func (c *internalAddressClient) AllocateExternalIPv6(
	ctx context.Context, req AllocateExternalIPRequest,
) (*AllocateResponse, error) {
	if err := validateExternalReq(req); err != nil {
		return nil, err
	}
	createReq := &vpcpb.CreateAddressRequest{
		ProjectId: req.ProjectID,
		Name:      req.Name,
		AddressSpec: &vpcpb.CreateAddressRequest_ExternalIpv6AddressSpec{
			ExternalIpv6AddressSpec: &vpcpb.ExternalIpv6AddressSpec{ZoneId: req.ZoneID},
		},
	}
	return c.allocFromCreate(ctx, createReq, req.Owner, func(a *vpcpb.Address) string {
		return a.GetExternalIpv6Address().GetAddress()
	})
}

// AllocateInternalIP — см. контракт InternalAddressClient.AllocateInternalIP.
func (c *internalAddressClient) AllocateInternalIP(
	ctx context.Context, req AllocateInternalIPRequest,
) (*AllocateResponse, error) {
	if err := validateInternalReq(req); err != nil {
		return nil, err
	}
	createReq := &vpcpb.CreateAddressRequest{
		ProjectId: req.ProjectID,
		Name:      req.Name,
		AddressSpec: &vpcpb.CreateAddressRequest_InternalIpv4AddressSpec{
			InternalIpv4AddressSpec: &vpcpb.InternalIpv4AddressSpec{
				Scope: &vpcpb.InternalIpv4AddressSpec_SubnetId{SubnetId: req.SubnetID},
			},
		},
	}
	return c.allocFromCreate(ctx, createReq, req.Owner, func(a *vpcpb.Address) string {
		return a.GetInternalIpv4Address().GetAddress()
	})
}

// AllocateInternalIPv6 — см. контракт InternalAddressClient.AllocateInternalIPv6.
// Зеркало AllocateInternalIP для internal_ipv6: адрес из subnet.v6_cidr_blocks.
func (c *internalAddressClient) AllocateInternalIPv6(
	ctx context.Context, req AllocateInternalIPRequest,
) (*AllocateResponse, error) {
	if err := validateInternalReq(req); err != nil {
		return nil, err
	}
	createReq := &vpcpb.CreateAddressRequest{
		ProjectId: req.ProjectID,
		Name:      req.Name,
		AddressSpec: &vpcpb.CreateAddressRequest_InternalIpv6AddressSpec{
			InternalIpv6AddressSpec: &vpcpb.InternalIpv6AddressSpec{
				Scope: &vpcpb.InternalIpv6AddressSpec_SubnetId{SubnetId: req.SubnetID},
			},
		},
	}
	return c.allocFromCreate(ctx, createReq, req.Owner, func(a *vpcpb.Address) string {
		return a.GetInternalIpv6Address().GetAddress()
	})
}

// allocFromCreate — общий хвост per-family auto-alloc: AddressService.Create
// (vpc аллоцирует IP нужной family в writer-TX) + atomic SetReference сразу
// после Create (used_by=<owner> до commit Listener.Create). readIP извлекает
// family-specific resolved-адрес из ответа. pool_id не expose'ится через
// Create-response (для NLB-флоу не критично — pool tracking отдельный enhancement).
func (c *internalAddressClient) allocFromCreate(
	ctx context.Context,
	createReq *vpcpb.CreateAddressRequest,
	owner AddressOwner,
	readIP func(*vpcpb.Address) string,
) (*AllocateResponse, error) {
	addr, err := c.createAddressAndWait(ctx, createReq)
	if err != nil {
		return nil, err
	}
	// auto-alloc → owned=true (адрес заказан LB неявно, lifecycle связан).
	if err := c.SetReference(ctx, addr.GetId(), owner, true); err != nil {
		// Best-effort cleanup: tear down half-allocated address. Не маскируем
		// исходную ошибку (она важнее для caller'а).
		_ = c.FreeIP(ctx, addr.GetId(), owner)
		return nil, err
	}
	return &AllocateResponse{AddressID: addr.GetId(), Value: readIP(addr)}, nil
}

// validateExternalReq — общая sync-валидация аргументов external-alloc (v4/v6).
func validateExternalReq(req AllocateExternalIPRequest) error {
	switch {
	case req.ProjectID == "":
		return fmt.Errorf("%w: project_id is empty", domain.ErrInvalidArg)
	case req.ZoneID == "":
		return fmt.Errorf("%w: zone_id is empty", domain.ErrInvalidArg)
	case req.Owner.Kind == "" || req.Owner.ID == "":
		return fmt.Errorf("%w: owner is empty", domain.ErrInvalidArg)
	}
	return nil
}

// validateInternalReq — общая sync-валидация аргументов internal-alloc (v4/v6).
func validateInternalReq(req AllocateInternalIPRequest) error {
	switch {
	case req.ProjectID == "":
		return fmt.Errorf("%w: project_id is empty", domain.ErrInvalidArg)
	case req.SubnetID == "":
		return fmt.Errorf("%w: subnet_id is empty", domain.ErrInvalidArg)
	case req.Owner.Kind == "" || req.Owner.ID == "":
		return fmt.Errorf("%w: owner is empty", domain.ErrInvalidArg)
	}
	return nil
}

// FreeIP — см. контракт InternalAddressClient.FreeIP.
func (c *internalAddressClient) FreeIP(ctx context.Context, addressID string, owner AddressOwner) error {
	if addressID == "" {
		return fmt.Errorf("%w: address_id is empty", domain.ErrInvalidArg)
	}
	_ = owner // owner reserved для будущей verify-before-delete CAS-семантики

	var op *operationpb.Operation
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		op, rerr = c.addrs.Delete(ctx, &vpcpb.DeleteAddressRequest{AddressId: addressID})
		if rerr != nil {
			if st, ok := status.FromError(rerr); ok && st.Code() == codes.NotFound {
				// Idempotent: уже удалён.
				op = nil
				return nil
			}
			return rerr
		}
		return nil
	}); err != nil {
		return mapAllocErr(addressID, err)
	}
	if op == nil {
		return nil
	}
	if _, err := c.waitOperation(ctx, op); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return nil
		}
		return mapAllocErr(addressID, err)
	}
	return nil
}

// SetReference — см. контракт InternalAddressClient.SetReference.
func (c *internalAddressClient) SetReference(
	ctx context.Context, addressID string, owner AddressOwner, owned bool,
) error {
	switch {
	case addressID == "":
		return fmt.Errorf("%w: address_id is empty", domain.ErrInvalidArg)
	case owner.Kind == "":
		return fmt.Errorf("%w: owner.Kind is empty", domain.ErrInvalidArg)
	case owner.ID == "":
		return fmt.Errorf("%w: owner.ID is empty", domain.ErrInvalidArg)
	}

	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.internal.SetAddressReference(ctx, &vpcpb.SetAddressReferenceRequest{
			AddressId:    addressID,
			ReferrerType: owner.Kind,
			ReferrerId:   owner.ID,
			Owned:        owned,
		})
		if rerr == nil {
			return nil
		}
		st, ok := status.FromError(rerr)
		if !ok {
			return fmt.Errorf("vpc set address reference %q: %w", addressID, rerr)
		}
		switch st.Code() {
		case codes.AlreadyExists:
			return fmt.Errorf("%w: address %s already used by another resource", domain.ErrFailedPrecondition, addressID)
		case codes.NotFound:
			return fmt.Errorf("%w: address %s not found", domain.ErrInvalidArg, addressID)
		case codes.InvalidArgument:
			return fmt.Errorf("%w: vpc set address reference %s: %s", domain.ErrInvalidArg, addressID, st.Message())
		default:
			return fmt.Errorf("vpc set address reference %q: %w", addressID, rerr)
		}
	})
}

// ClearReference — см. контракт InternalAddressClient.ClearReference.
func (c *internalAddressClient) ClearReference(
	ctx context.Context, addressID string, owner AddressOwner,
) error {
	if addressID == "" {
		return fmt.Errorf("%w: address_id is empty", domain.ErrInvalidArg)
	}
	_ = owner // proto ClearAddressReferenceRequest пока не различает owner —
	// добавим verify когда kacho-vpc дополнит CAS-семантику.

	return retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.internal.ClearAddressReference(ctx, &vpcpb.ClearAddressReferenceRequest{
			AddressId: addressID,
		})
		if rerr == nil {
			return nil
		}
		st, ok := status.FromError(rerr)
		if !ok {
			return fmt.Errorf("vpc clear address reference %q: %w", addressID, rerr)
		}
		switch st.Code() {
		case codes.NotFound:
			// Idempotent: уже снят / address удалён.
			return nil
		case codes.InvalidArgument:
			return fmt.Errorf("%w: vpc clear address reference %s: %s", domain.ErrInvalidArg, addressID, st.Message())
		default:
			return fmt.Errorf("vpc clear address reference %q: %w", addressID, rerr)
		}
	})
}

// AttachExisting — см. контракт InternalAddressClient.AttachExisting.
func (c *internalAddressClient) AttachExisting(
	ctx context.Context, req AttachExistingRequest,
) (*AllocateResponse, error) {
	switch {
	case req.AddressID == "":
		return nil, fmt.Errorf("%w: address_id is empty", domain.ErrInvalidArg)
	case req.Owner.Kind == "" || req.Owner.ID == "":
		return nil, fmt.Errorf("%w: owner is empty", domain.ErrInvalidArg)
	}

	// Атомарный CAS-referrer в vpc (та же tx, что и запись used_by). Mismatch /
	// not-found → generic InvalidArgument (анти-oracle: не раскрываем чужой
	// ownership/семейство/несуществование адреса).
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		_, rerr := c.internal.SetAddressReference(ctx, &vpcpb.SetAddressReferenceRequest{
			AddressId:    req.AddressID,
			ReferrerType: req.Owner.Kind,
			ReferrerId:   req.Owner.ID,
			Owned:        req.Owned,
		})
		if rerr == nil {
			return nil
		}
		st, ok := status.FromError(rerr)
		if !ok {
			return fmt.Errorf("vpc set address reference %q: %w", req.AddressID, rerr)
		}
		switch st.Code() {
		case codes.AlreadyExists:
			return fmt.Errorf("%w: address %s already used by another resource", domain.ErrFailedPrecondition, req.AddressID)
		case codes.NotFound, codes.InvalidArgument, codes.PermissionDenied:
			return fmt.Errorf("%w: Illegal argument addressId", domain.ErrInvalidArg)
		default:
			return fmt.Errorf("vpc set address reference %q: %w", req.AddressID, rerr)
		}
	}); err != nil {
		return nil, err
	}

	// Привязка прошла → адрес наш; читаем resolved-значение.
	addr, err := c.resolveAddressValue(ctx, req.AddressID)
	if err != nil {
		return nil, err
	}
	return &AllocateResponse{AddressID: req.AddressID, Value: addr}, nil
}

// resolveAddressValue — Get Address + извлечение resolved IP-строки (любое
// семейство). Используется после успешной BYO-привязки.
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

// createAddressAndWait вызывает AddressService.Create + poll Operation до
// done=true. Возвращает созданный Address. Маппит ошибки в sentinel'ы.
func (c *internalAddressClient) createAddressAndWait(
	ctx context.Context, req *vpcpb.CreateAddressRequest,
) (*vpcpb.Address, error) {
	var op *operationpb.Operation
	if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
		var rerr error
		op, rerr = c.addrs.Create(ctx, req)
		return rerr
	}); err != nil {
		return nil, mapAllocErr("", err)
	}
	resp, err := c.waitOperation(ctx, op)
	if err != nil {
		return nil, mapAllocErr("", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("vpc create address: operation %s returned no response", op.GetId())
	}
	addr := &vpcpb.Address{}
	if err := resp.UnmarshalTo(addr); err != nil {
		return nil, fmt.Errorf("vpc create address: unmarshal operation response: %w", err)
	}
	return addr, nil
}

// waitOperation поллит OperationService.Get до done=true. Возвращает
// Operation.response (`*anypb.Any`) либо смаппленную gRPC-status ошибку.
func (c *internalAddressClient) waitOperation(
	ctx context.Context, op *operationpb.Operation,
) (*anypb.Any, error) {
	if op.GetDone() {
		return operationResult(op)
	}
	deadline := time.Now().Add(vpcOpPollTimeout)
	ticker := time.NewTicker(vpcOpPollInterval)
	defer ticker.Stop()
	id := op.GetId()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
		var got *operationpb.Operation
		if err := retry.OnUnavailable(ctx, func(ctx context.Context) error {
			var rerr error
			got, rerr = c.ops.Get(ctx, &operationpb.GetOperationRequest{OperationId: id})
			return rerr
		}); err != nil {
			return nil, err
		}
		if got.GetDone() {
			return operationResult(got)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("vpc operation %s did not finish within %s", id, vpcOpPollTimeout)
		}
	}
}

// operationResult извлекает либо response (success), либо google.rpc.Status
// (failure → gRPC error).
func operationResult(op *operationpb.Operation) (*anypb.Any, error) {
	if e := op.GetError(); e != nil {
		return nil, status.ErrorProto(e)
	}
	return op.GetResponse(), nil
}

// mapAllocErr транслирует gRPC-status в domain-sentinel-ошибки для allocate-
// флоу (Create/Delete Address operations).
func mapAllocErr(addressID string, err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("vpc address allocate %q: %w", addressID, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: address %s not found", domain.ErrInvalidArg, addressID)
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: vpc address allocate: %s", domain.ErrFailedPrecondition, st.Message())
	case codes.AlreadyExists:
		return fmt.Errorf("%w: vpc address allocate: %s", domain.ErrFailedPrecondition, st.Message())
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("%w: vpc address allocate: %s", domain.ErrUnavailable, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: vpc address allocate: %s", domain.ErrInvalidArg, st.Message())
	default:
		return fmt.Errorf("vpc address allocate: %w", err)
	}
}
