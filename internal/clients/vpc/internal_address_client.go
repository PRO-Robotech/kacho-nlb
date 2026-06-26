package vpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/retry"
	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
	vpcpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/vpc/v1"

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

	// FreeIP освобождает Address (idempotent через AddressService.Delete →
	// NotFound трактуется как успех). ClearReference вызывается автоматически
	// kacho-vpc при Delete.
	FreeIP(ctx context.Context, addressID string, owner AddressOwner) error

	// SetReference — атомарный CAS Set used_by=owner на существующем Address
	// (BYO attach в Listener.Create). Семантика ошибок:
	//   - AlreadyExists (address уже занят другим owner) → domain.ErrFailedPrecondition
	//   - NotFound                                       → domain.ErrInvalidArg
	SetReference(ctx context.Context, addressID string, owner AddressOwner) error

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
// external endpoint, см. workspace CLAUDE.md «Запреты» #6).
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
	switch {
	case req.ProjectID == "":
		return nil, fmt.Errorf("%w: project_id is empty", domain.ErrInvalidArg)
	case req.ZoneID == "":
		return nil, fmt.Errorf("%w: zone_id is empty", domain.ErrInvalidArg)
	case req.Owner.Kind == "" || req.Owner.ID == "":
		return nil, fmt.Errorf("%w: owner is empty", domain.ErrInvalidArg)
	}

	createReq := &vpcpb.CreateAddressRequest{
		ProjectId: req.ProjectID,
		Name:      req.Name,
		AddressSpec: &vpcpb.CreateAddressRequest_ExternalIpv4AddressSpec{
			ExternalIpv4AddressSpec: &vpcpb.ExternalIpv4AddressSpec{
				ZoneId: req.ZoneID,
			},
		},
	}
	addr, err := c.createAddressAndWait(ctx, createReq)
	if err != nil {
		return nil, err
	}
	// SetReference сразу после Create — гарантирует, что свежий Address
	// помечен used_by=<owner> до того, как Listener.Create commit'нется.
	if err := c.SetReference(ctx, addr.GetId(), req.Owner); err != nil {
		// Best-effort cleanup: tear down half-allocated address. Не маскируем
		// исходную ошибку (она важнее для caller'а).
		_ = c.FreeIP(ctx, addr.GetId(), req.Owner)
		return nil, err
	}

	ip := addr.GetExternalIpv4Address().GetAddress()
	// pool_id не expose'ится через AddressService.Create response (только через
	// InternalAddressService.AllocateExternalIP) — для NLB-флоу это не критично:
	// pool tracking — отдельный enhancement (Wave 8 metrics / observability).
	return &AllocateResponse{
		AddressID: addr.GetId(),
		Value:     ip,
	}, nil
}

// AllocateInternalIP — см. контракт InternalAddressClient.AllocateInternalIP.
func (c *internalAddressClient) AllocateInternalIP(
	ctx context.Context, req AllocateInternalIPRequest,
) (*AllocateResponse, error) {
	switch {
	case req.ProjectID == "":
		return nil, fmt.Errorf("%w: project_id is empty", domain.ErrInvalidArg)
	case req.SubnetID == "":
		return nil, fmt.Errorf("%w: subnet_id is empty", domain.ErrInvalidArg)
	case req.Owner.Kind == "" || req.Owner.ID == "":
		return nil, fmt.Errorf("%w: owner is empty", domain.ErrInvalidArg)
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
	addr, err := c.createAddressAndWait(ctx, createReq)
	if err != nil {
		return nil, err
	}
	if err := c.SetReference(ctx, addr.GetId(), req.Owner); err != nil {
		_ = c.FreeIP(ctx, addr.GetId(), req.Owner)
		return nil, err
	}

	return &AllocateResponse{
		AddressID: addr.GetId(),
		Value:     addr.GetInternalIpv4Address().GetAddress(),
		// pool_id пусто для internal (см. AllocateIPResponse proto comments).
	}, nil
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
	ctx context.Context, addressID string, owner AddressOwner,
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
