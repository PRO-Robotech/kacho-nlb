// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package vpc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
	vpcpb "github.com/PRO-Robotech/kacho-vpc/proto/gen/go/kacho/cloud/vpc/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeAddressForAlloc реализует AddressService.{Create,Delete}.
// Возвращает done=true Operation с inline Address response (для теста auto-alloc).
type fakeAddressForAlloc struct {
	vpcpb.UnimplementedAddressServiceServer

	mu sync.Mutex

	createResp *vpcpb.Address // что положить в Operation.response
	createErr  error          // ошибка на сам Create call

	deleteErr      error
	deleteNotFound bool // если true — Delete возвращает NotFound

	createCalls int
	deleteCalls int
	lastCreate  *vpcpb.CreateAddressRequest
}

func (f *fakeAddressForAlloc) Create(_ context.Context, req *vpcpb.CreateAddressRequest) (*operationpb.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	f.lastCreate = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	any, _ := anypb.New(f.createResp)
	return &operationpb.Operation{
		Id:     "op-alloc-1",
		Done:   true,
		Result: &operationpb.Operation_Response{Response: any},
	}, nil
}

func (f *fakeAddressForAlloc) Delete(_ context.Context, _ *vpcpb.DeleteAddressRequest) (*operationpb.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteCalls++
	if f.deleteNotFound {
		return nil, status.Error(codes.NotFound, "no such address")
	}
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	emptyAny, _ := anypb.New(&vpcpb.Address{}) // payload не важен для Delete
	return &operationpb.Operation{
		Id:     "op-del-1",
		Done:   true,
		Result: &operationpb.Operation_Response{Response: emptyAny},
	}, nil
}

// fakeInternalAddressService реализует InternalAddressService.{Set,Clear}Reference.
type fakeInternalAddressService struct {
	vpcpb.UnimplementedInternalAddressServiceServer

	mu sync.Mutex

	setErr   error
	clearErr error

	setCalls   []*vpcpb.SetAddressReferenceRequest
	clearCalls []*vpcpb.ClearAddressReferenceRequest
}

func (f *fakeInternalAddressService) SetAddressReference(
	_ context.Context, req *vpcpb.SetAddressReferenceRequest,
) (*vpcpb.AddressReference, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, req)
	if f.setErr != nil {
		return nil, f.setErr
	}
	return &vpcpb.AddressReference{
		AddressId:    req.AddressId,
		ReferrerType: req.ReferrerType,
		ReferrerId:   req.ReferrerId,
	}, nil
}

func (f *fakeInternalAddressService) ClearAddressReference(
	_ context.Context, req *vpcpb.ClearAddressReferenceRequest,
) (*vpcpb.ClearAddressReferenceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clearCalls = append(f.clearCalls, req)
	if f.clearErr != nil {
		return nil, f.clearErr
	}
	return &vpcpb.ClearAddressReferenceResponse{}, nil
}

// fakeOperationService — Operation.Get для долгих операций
// (наш fake возвращает Done=true сразу, OperationService не вызывается; но он
// нужен для регистрации, иначе server NewClient'у может не нравиться).
type fakeOperationService struct {
	operationpb.UnimplementedOperationServiceServer
}

func (f *fakeOperationService) Get(_ context.Context, req *operationpb.GetOperationRequest) (*operationpb.Operation, error) {
	return &operationpb.Operation{Id: req.OperationId, Done: true}, nil
}

func TestInternalAddressClient_AllocateExternalIP_HappyPath(t *testing.T) {
	allocResp := &vpcpb.Address{
		Id:        "e9b-ip-1",
		ProjectId: "prj-1",
		Address: &vpcpb.Address_ExternalIpv4Address{
			ExternalIpv4Address: &vpcpb.ExternalIpv4Address{
				Address: "203.0.113.5",
				ZoneId:  "ru-central1-a",
			},
		},
	}
	addrSvc := &fakeAddressForAlloc{createResp: allocResp}
	intAddrSvc := &fakeInternalAddressService{}
	opSvc := &fakeOperationService{}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, opSvc)

	c := NewInternalAddressClient(conn, conn)
	require.NotNil(t, c)

	resp, err := c.AllocateExternalIP(ctxBackground(), AllocateExternalIPRequest{
		ProjectID: "prj-1",
		Name:      "listener-vip-1",
		ZoneID:    "ru-central1-a",
		Owner:     AddressOwner{Kind: "nlb_listener", ID: "lst-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "e9b-ip-1", resp.AddressID)
	assert.Equal(t, "203.0.113.5", resp.Value)
	assert.Equal(t, 1, addrSvc.createCalls)
	require.Len(t, intAddrSvc.setCalls, 1)
	assert.Equal(t, "e9b-ip-1", intAddrSvc.setCalls[0].AddressId)
	assert.Equal(t, "nlb_listener", intAddrSvc.setCalls[0].ReferrerType)
	assert.Equal(t, "lst-1", intAddrSvc.setCalls[0].ReferrerId)
}

func TestInternalAddressClient_AllocateExternalIP_SetReferenceFailsTriggersCleanup(t *testing.T) {
	allocResp := &vpcpb.Address{
		Id: "e9b-ip-2",
		Address: &vpcpb.Address_ExternalIpv4Address{
			ExternalIpv4Address: &vpcpb.ExternalIpv4Address{Address: "203.0.113.6"},
		},
	}
	addrSvc := &fakeAddressForAlloc{createResp: allocResp}
	intAddrSvc := &fakeInternalAddressService{setErr: status.Error(codes.AlreadyExists, "already attached")}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	_, err := c.AllocateExternalIP(ctxBackground(), AllocateExternalIPRequest{
		ProjectID: "prj-1", ZoneID: "z", Owner: AddressOwner{Kind: "nlb_listener", ID: "lst-1"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrFailedPrecondition))
	assert.Equal(t, 1, addrSvc.deleteCalls, "cleanup must call Delete to free half-allocated address")
}

func TestInternalAddressClient_AllocateExternalIP_PoolExhausted(t *testing.T) {
	addrSvc := &fakeAddressForAlloc{createErr: status.Error(codes.FailedPrecondition, "pool exhausted")}
	conn := startFakeVPC(t, nil, nil, addrSvc, &fakeInternalAddressService{}, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	_, err := c.AllocateExternalIP(ctxBackground(), AllocateExternalIPRequest{
		ProjectID: "prj-1", ZoneID: "z", Owner: AddressOwner{Kind: "nlb_listener", ID: "lst-1"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrFailedPrecondition))
}

func TestInternalAddressClient_AllocateInternalIP_HappyPath(t *testing.T) {
	allocResp := &vpcpb.Address{
		Id: "e9b-ip-3",
		Address: &vpcpb.Address_InternalIpv4Address{
			InternalIpv4Address: &vpcpb.InternalIpv4Address{Address: "10.128.0.7"},
		},
	}
	addrSvc := &fakeAddressForAlloc{createResp: allocResp}
	intAddrSvc := &fakeInternalAddressService{}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	resp, err := c.AllocateInternalIP(ctxBackground(), AllocateInternalIPRequest{
		ProjectID: "prj-1", Name: "n", SubnetID: "e9b-1",
		Owner: AddressOwner{Kind: "nlb_listener", ID: "lst-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "e9b-ip-3", resp.AddressID)
	assert.Equal(t, "10.128.0.7", resp.Value)
	assert.Empty(t, resp.PoolID, "internal IP не имеет pool_id")
}

func TestInternalAddressClient_AllocateExternalIPv6_HappyPath(t *testing.T) {
	allocResp := &vpcpb.Address{
		Id:        "e9b-ip6-1",
		ProjectId: "prj-1",
		Address: &vpcpb.Address_ExternalIpv6Address{
			ExternalIpv6Address: &vpcpb.ExternalIpv6Address{
				Address: "2001:db8::5",
				ZoneId:  "ru-central1-a",
			},
		},
	}
	addrSvc := &fakeAddressForAlloc{createResp: allocResp}
	intAddrSvc := &fakeInternalAddressService{}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	resp, err := c.AllocateExternalIPv6(ctxBackground(), AllocateExternalIPRequest{
		ProjectID: "prj-1",
		Name:      "v6-vip",
		ZoneID:    "ru-central1-a",
		Owner:     AddressOwner{Kind: "nlb_listener", ID: "lst-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "e9b-ip6-1", resp.AddressID)
	assert.Equal(t, "2001:db8::5", resp.Value)
	require.NotNil(t, addrSvc.lastCreate.GetExternalIpv6AddressSpec(), "must build external_ipv6 spec, not v4")
	require.Len(t, intAddrSvc.setCalls, 1)
	assert.Equal(t, "e9b-ip6-1", intAddrSvc.setCalls[0].AddressId)
}

func TestInternalAddressClient_AllocateExternalIPv6_EmptyZoneRejected(t *testing.T) {
	c := NewInternalAddressClient(
		startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{}),
		startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{}),
	)
	_, err := c.AllocateExternalIPv6(ctxBackground(), AllocateExternalIPRequest{
		ProjectID: "p", Owner: AddressOwner{Kind: "k", ID: "i"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg), "empty zone must be rejected (parity with AllocateExternalIP)")
}

func TestInternalAddressClient_AllocateInternalIPv6_HappyPath(t *testing.T) {
	allocResp := &vpcpb.Address{
		Id: "e9b-ip6-2",
		Address: &vpcpb.Address_InternalIpv6Address{
			InternalIpv6Address: &vpcpb.InternalIpv6Address{
				Address: "fd00::9",
				Scope:   &vpcpb.InternalIpv6Address_SubnetId{SubnetId: "e9b-sub6"},
			},
		},
	}
	addrSvc := &fakeAddressForAlloc{createResp: allocResp}
	intAddrSvc := &fakeInternalAddressService{}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	resp, err := c.AllocateInternalIPv6(ctxBackground(), AllocateInternalIPRequest{
		ProjectID: "prj-1", Name: "n", SubnetID: "e9b-sub6",
		Owner: AddressOwner{Kind: "nlb_listener", ID: "lst-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "e9b-ip6-2", resp.AddressID)
	assert.Equal(t, "fd00::9", resp.Value)
	require.NotNil(t, addrSvc.lastCreate.GetInternalIpv6AddressSpec(), "must build internal_ipv6 spec, not v4")
	assert.Equal(t, "e9b-sub6", addrSvc.lastCreate.GetInternalIpv6AddressSpec().GetSubnetId())
}

func TestInternalAddressClient_AllocateInternalIP_EmptySubnetRejected(t *testing.T) {
	c := NewInternalAddressClient(
		startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{}),
		startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{}),
	)
	_, err := c.AllocateInternalIP(ctxBackground(), AllocateInternalIPRequest{
		ProjectID: "p", Owner: AddressOwner{Kind: "k", ID: "i"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestInternalAddressClient_FreeIP_Idempotent(t *testing.T) {
	addrSvc := &fakeAddressForAlloc{deleteNotFound: true}
	intAddrSvc := &fakeInternalAddressService{}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	err := c.FreeIP(ctxBackground(), "e9b-already-gone", AddressOwner{Kind: "nlb_listener", ID: "lst-1"})
	require.NoError(t, err, "FreeIP must be idempotent: NotFound treated as success")
}

func TestInternalAddressClient_FreeIP_HappyPath(t *testing.T) {
	addrSvc := &fakeAddressForAlloc{}
	intAddrSvc := &fakeInternalAddressService{}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	err := c.FreeIP(ctxBackground(), "e9b-ip-1", AddressOwner{Kind: "nlb_listener", ID: "lst-1"})
	require.NoError(t, err)
	assert.Equal(t, 1, addrSvc.deleteCalls)
}

func TestInternalAddressClient_SetReference_AlreadyExistsMapsToPrecondition(t *testing.T) {
	intAddrSvc := &fakeInternalAddressService{setErr: status.Error(codes.AlreadyExists, "owner mismatch")}
	conn := startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	err := c.SetReference(ctxBackground(), "e9b-ip-1", AddressOwner{Kind: "nlb_listener", ID: "lst-1"}, true)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrFailedPrecondition))
}

func TestInternalAddressClient_SetReference_NotFoundMapsToInvalidArg(t *testing.T) {
	intAddrSvc := &fakeInternalAddressService{setErr: status.Error(codes.NotFound, "address not found")}
	conn := startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	err := c.SetReference(ctxBackground(), "e9b-nx", AddressOwner{Kind: "k", ID: "i"}, false)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrInvalidArg))
}

func TestInternalAddressClient_ClearReference_Idempotent(t *testing.T) {
	intAddrSvc := &fakeInternalAddressService{clearErr: status.Error(codes.NotFound, "already cleared")}
	conn := startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	err := c.ClearReference(ctxBackground(), "e9b-gone", AddressOwner{Kind: "k", ID: "i"})
	require.NoError(t, err, "ClearReference NotFound is idempotent")
}

func TestInternalAddressClient_ClearReference_HappyPath(t *testing.T) {
	intAddrSvc := &fakeInternalAddressService{}
	conn := startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, intAddrSvc, &fakeOperationService{})

	c := NewInternalAddressClient(conn, conn)
	err := c.ClearReference(ctxBackground(), "e9b-ip-1", AddressOwner{Kind: "nlb_listener", ID: "lst-1"})
	require.NoError(t, err)
	require.Len(t, intAddrSvc.clearCalls, 1)
	assert.Equal(t, "e9b-ip-1", intAddrSvc.clearCalls[0].AddressId)
}

func TestInternalAddressClient_AllocateExternalIP_EmptyArgs(t *testing.T) {
	c := NewInternalAddressClient(
		startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{}),
		startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{}),
	)
	cases := []struct {
		name string
		req  AllocateExternalIPRequest
	}{
		{"empty project", AllocateExternalIPRequest{ZoneID: "z", Owner: AddressOwner{Kind: "k", ID: "i"}}},
		{"empty zone", AllocateExternalIPRequest{ProjectID: "p", Owner: AddressOwner{Kind: "k", ID: "i"}}},
		{"empty owner", AllocateExternalIPRequest{ProjectID: "p", ZoneID: "z"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.AllocateExternalIP(ctxBackground(), tc.req)
			require.Error(t, err)
			assert.True(t, errors.Is(err, domain.ErrInvalidArg))
		})
	}
}

func TestInternalAddressClient_SetReference_EmptyArgs(t *testing.T) {
	c := NewInternalAddressClient(
		startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{}),
		startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{}),
	)
	cases := []struct {
		name, id string
		owner    AddressOwner
	}{
		{"empty id", "", AddressOwner{Kind: "k", ID: "i"}},
		{"empty kind", "e9b-ip-1", AddressOwner{ID: "i"}},
		{"empty owner id", "e9b-ip-1", AddressOwner{Kind: "k"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.SetReference(ctxBackground(), tc.id, tc.owner, false)
			require.Error(t, err)
			assert.True(t, errors.Is(err, domain.ErrInvalidArg))
		})
	}
}

func TestInternalAddressClient_NilConn(t *testing.T) {
	assert.Nil(t, NewInternalAddressClient(nil, nil))
	conn := startFakeVPC(t, nil, nil, &fakeAddressForAlloc{}, &fakeInternalAddressService{}, &fakeOperationService{})
	assert.Nil(t, NewInternalAddressClient(conn, nil))
	assert.Nil(t, NewInternalAddressClient(nil, conn))
}

// fakeAddressForAllocPolling — Create возвращает Done=false, OperationService
// должен поллить до Done=true (test for waitOperation loop).
type fakeAddressForAllocPolling struct {
	vpcpb.UnimplementedAddressServiceServer

	createResp *vpcpb.Address
}

func (f *fakeAddressForAllocPolling) Create(_ context.Context, _ *vpcpb.CreateAddressRequest) (*operationpb.Operation, error) {
	// Operation not done — caller will poll via OperationService.Get.
	return &operationpb.Operation{Id: "op-poll-1", Done: false}, nil
}

// fakeOpServicePolling — после N Get'ов возвращает Done=true с inline Address.
type fakeOpServicePolling struct {
	operationpb.UnimplementedOperationServiceServer

	mu        sync.Mutex
	getCalls  int
	doneAfter int
	addrResp  *vpcpb.Address
	opErr     *opErrStatus // nil → return inline response; non-nil → return with.error set
}

type opErrStatus struct {
	code    codes.Code
	message string
}

func (f *fakeOpServicePolling) Get(_ context.Context, req *operationpb.GetOperationRequest) (*operationpb.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getCalls < f.doneAfter {
		return &operationpb.Operation{Id: req.OperationId, Done: false}, nil
	}
	if f.opErr != nil {
		return &operationpb.Operation{
			Id:     req.OperationId,
			Done:   true,
			Result: &operationpb.Operation_Error{Error: status.New(f.opErr.code, f.opErr.message).Proto()},
		}, nil
	}
	any, _ := anypb.New(f.addrResp)
	return &operationpb.Operation{
		Id:     req.OperationId,
		Done:   true,
		Result: &operationpb.Operation_Response{Response: any},
	}, nil
}

func TestInternalAddressClient_AllocateExternalIP_PollLoop(t *testing.T) {
	// Create returns Done=false → adapter polls Operation.Get; on 3rd call Done=true.
	addrResp := &vpcpb.Address{
		Id: "e9b-ip-poll",
		Address: &vpcpb.Address_ExternalIpv4Address{
			ExternalIpv4Address: &vpcpb.ExternalIpv4Address{Address: "203.0.113.99"},
		},
	}
	addrSvc := &fakeAddressForAllocPolling{createResp: addrResp}
	intAddrSvc := &fakeInternalAddressService{}
	opSvc := &fakeOpServicePolling{doneAfter: 3, addrResp: addrResp}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, opSvc)

	c := NewInternalAddressClient(conn, conn)
	resp, err := c.AllocateExternalIP(ctxBackground(), AllocateExternalIPRequest{
		ProjectID: "prj-1", ZoneID: "z",
		Owner: AddressOwner{Kind: "nlb_listener", ID: "lst-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "e9b-ip-poll", resp.AddressID)
	assert.Equal(t, "203.0.113.99", resp.Value)
	assert.GreaterOrEqual(t, opSvc.getCalls, 3, "must have polled ≥3 times before Done")
}

func TestInternalAddressClient_AllocateExternalIP_OperationFailure(t *testing.T) {
	// Operation completes with error — adapter must surface gRPC status as sentinel.
	addrSvc := &fakeAddressForAllocPolling{}
	opSvc := &fakeOpServicePolling{
		doneAfter: 1,
		opErr:     &opErrStatus{code: codes.FailedPrecondition, message: "pool exhausted"},
	}
	conn := startFakeVPC(t, nil, nil, addrSvc, &fakeInternalAddressService{}, opSvc)

	c := NewInternalAddressClient(conn, conn)
	_, err := c.AllocateExternalIP(ctxBackground(), AllocateExternalIPRequest{
		ProjectID: "prj-1", ZoneID: "z",
		Owner: AddressOwner{Kind: "nlb_listener", ID: "lst-1"},
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrFailedPrecondition))
}

// AddressClientFromStub / etc. constructor wired tests
func TestVPC_FromStubConstructors_Nil(t *testing.T) {
	assert.Nil(t, NewAddressClientFromStub(nil))
	assert.Nil(t, NewSubnetClientFromStub(nil))
	assert.Nil(t, NewNetworkInterfaceClientFromStub(nil))
	assert.Nil(t, NewInternalAddressClientFromStubs(nil, nil, nil))
}

func TestInternalAddressClient_FreeIP_Unavailable(t *testing.T) {
	addrSvc := &fakeAddressForAlloc{deleteErr: status.Error(codes.Unavailable, "down")}
	intAddrSvc := &fakeInternalAddressService{}
	conn := startFakeVPC(t, nil, nil, addrSvc, intAddrSvc, &fakeOperationService{})
	c := NewInternalAddressClient(conn, conn)
	ctx, cancel := context.WithTimeout(ctxBackground(), 200*time.Millisecond)
	defer cancel()
	err := c.FreeIP(ctx, "e9b-ip-1", AddressOwner{Kind: "k", ID: "i"})
	require.Error(t, err)
	if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected ErrUnavailable or DeadlineExceeded; got %v", err)
	}
}
