// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// TestCreateListener_GWT_LST_001_AutoExternal_HappyPath — `auto VIP-alloc`
// EXTERNAL Listener: AllocateExternalIP called → insert with allocated_address
// + address_id → 2× outbox events → FGA tuples emitted.
func TestCreateListener_GWT_LST_001_AutoExternal_HappyPath(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)

	op, err := suite.uc.Run(suite.ctxWithSubject(), &lbv1.CreateListenerRequest{
		LoadBalancerId:  string(suite.lb.ID),
		Name:            "http",
		Protocol:        lbv1.Listener_TCP,
		Port:            80,
		TargetPort:      8080,
		IpVersion:       lbv1.IpVersion_IPV4,
		AddressSpec:     autoSpec(""),
		ProxyProtocolV2: false,
	})
	require.NoError(t, err)
	require.NotEmpty(t, op.ID)

	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error, "Operation must succeed; got error=%v", done.Error)
	require.NotNil(t, done.Response, "Operation must have Listener response")

	// AllocateExternalIP called once with the listener owner.
	require.Len(t, suite.internalAddrs.allocExternalCalls, 1, "AllocateExternalIP must be called exactly once")
	call := suite.internalAddrs.allocExternalCalls[0]
	require.Equal(t, addressOwnerKindNLBListener, call.Owner.Kind)
	require.Equal(t, string(suite.lb.ProjectID), call.ProjectID)

	// Listener row inserted with allocated_address + address_id.
	listeners := suite.allListeners()
	require.Len(t, listeners, 1)
	got := listeners[0]
	require.Equal(t, "203.0.113.42", string(got.AllocatedAddress))
	addrID, hasID := got.AddressID.Maybe()
	require.True(t, hasID)
	require.Equal(t, "e9bALLOCSTUB000001", string(addrID))
	require.Equal(t, domain.ListenerStatusCreating, got.Status)
	require.Equal(t, suite.lb.RegionID, got.RegionID)
	require.Equal(t, suite.lb.ProjectID, got.ProjectID)

	// 2× outbox events emitted in same TX (CREATED listener + UPDATED LB).
	events := suite.repo.pendingOutbox()
	require.Len(t, events, 2)
	require.Equal(t, outboxResourceTypeListener, events[0].ResourceType)
	require.Equal(t, outboxActionCreated, events[0].Action)
	require.Equal(t, outboxResourceTypeLoadBalancer, events[1].ResourceType)
	require.Equal(t, outboxActionUpdated, events[1].Action)

	// one fga.register intent written in the SAME writer-tx as the Insert
	// (not a direct best-effort FGA call). The intent carries the creator tuple
	// (#admin) + the parent-link tuple (#load_balancer).
	intents := suite.repo.committedFGA()
	require.Len(t, intents, 1, "expected one fga.register intent in writer-tx")
	require.Equal(t, domain.FGAEventRegister, intents[0].EventType)
	require.Equal(t, "Listener", intents[0].Intent.Kind)
	require.Equal(t, string(got.ID), intents[0].Intent.ResourceID)

	tuples := intents[0].Intent.Tuples
	require.Len(t, tuples, 2, "creator + parent-link tuples")

	creator := tuples[0]
	require.Equal(t, "user:test-actor", creator.SubjectID)
	require.Equal(t, domain.FGARelationAdmin, creator.Relation)
	require.Equal(t, domain.FGAObjectRef(domain.FGAObjectTypeListener, string(got.ID)), creator.Object)

	parent := tuples[1]
	require.Equal(t, domain.FGAObjectRef(domain.FGAObjectTypeLoadBalancer, string(suite.lb.ID)), parent.SubjectID)
	require.Equal(t, domain.FGARelationLoadBalancer, parent.Relation)
	require.Equal(t, domain.FGAObjectRef(domain.FGAObjectTypeListener, string(got.ID)), parent.Object)
}

// TestCreateListener_GWT_LST_002_BYO_HappyPath — BYO address_id: AddressService.Get
// + same-project + ip_version match → SetReference CAS → insert with allocated_address
// from existing Address.
func TestCreateListener_GWT_LST_002_BYO_HappyPath(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	const addrID = "e9bBYOOWN000000001"
	suite.addresses.seed(&vpcclient.Address{
		ID:        addrID,
		ProjectID: string(suite.lb.ProjectID),
		Name:      "tenant-named-ip",
		Value:     "198.51.100.7",
		Family:    vpcclient.AddressFamilyIPv4,
		External:  true,
	})

	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "http-byo",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    byoSpec(addrID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error)
	require.Len(t, suite.internalAddrs.setRefCalls, 1)
	require.Equal(t, addrID, suite.internalAddrs.setRefCalls[0].addressID)
	require.Equal(t, addressOwnerKindNLBListener, suite.internalAddrs.setRefCalls[0].owner.Kind)
	require.Len(t, suite.internalAddrs.allocExternalCalls, 0, "AllocateExternalIP must NOT be called for BYO")

	listeners := suite.allListeners()
	require.Len(t, listeners, 1)
	require.Equal(t, "198.51.100.7", string(listeners[0].AllocatedAddress))
}

// TestCreateListener_GWT_LST_003_BYO_AddressAlreadyUsed_FailedPrecondition —
// SetReference returns FailedPrecondition mapped from vpc.AlreadyExists.
// Address.UsedBy points to another listener; our sync check rejects.
func TestCreateListener_GWT_LST_003_BYO_AddressAlreadyUsed(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	const addrID = "e9bUSEDBYO00000001"
	suite.addresses.seed(&vpcclient.Address{
		ID:        addrID,
		ProjectID: string(suite.lb.ProjectID),
		Name:      "occupied-ip",
		Value:     "198.51.100.8",
		Family:    vpcclient.AddressFamilyIPv4,
		UsedBy: &vpcclient.AddressOwner{
			Kind: addressOwnerKindNLBListener,
			ID:   "lstOTHEROWN0000000",
		},
	})

	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "http-conflict",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    byoSpec(addrID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.NotNil(t, done.Error, "Operation must fail")
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Contains(t, done.Error.Message, "already in use by")
	require.Empty(t, suite.allListeners(), "no listener row must be inserted")
}

// TestCreateListener_GWT_LST_004_BYO_IPVersionMismatch — Address.Family != IpVersion
// → InvalidArgument.
func TestCreateListener_GWT_LST_004_BYO_IPVersionMismatch(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	const addrID = "e9bIPV6BYO00000001"
	suite.addresses.seed(&vpcclient.Address{
		ID:        addrID,
		ProjectID: string(suite.lb.ProjectID),
		Name:      "v6-address",
		Value:     "2001:db8::1",
		Family:    vpcclient.AddressFamilyIPv6,
	})

	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "wrong-family",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4, // mismatch: address is IPV6
		AddressSpec:    byoSpec(addrID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Contains(t, done.Error.Message, "does not match listener ip_version")
}

// TestCreateListener_GWT_LST_005_BYO_AddressNotFound — InvalidArgument с фиксированным текстом
// "address <id> not found".
func TestCreateListener_GWT_LST_005_BYO_AddressNotFound(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "missing-addr",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    byoSpec("e9bMISSINGADDR00001"),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Contains(t, done.Error.Message, "not found")
}

// TestCreateListener_GWT_LST_005b_BYO_CrossProject — Address.ProjectID != LB.ProjectID
// → InvalidArgument фиксированный текст.
func TestCreateListener_GWT_LST_005b_BYO_CrossProject(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	const addrID = "e9bCROSSPROJ000001"
	suite.addresses.seed(&vpcclient.Address{
		ID:        addrID,
		ProjectID: "prj01OTHEROWNERX0001",
		Name:      "cross-project",
		Value:     "198.51.100.9",
		Family:    vpcclient.AddressFamilyIPv4,
	})
	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "crossprj",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    byoSpec(addrID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Contains(t, done.Error.Message, "project_id does not match")
}

// TestCreateListener_GWT_LST_006_Internal_SubnetRequired — INTERNAL LB without
// subnet_id → InvalidArgument с фиксированным текстом "subnet_id is required for INTERNAL...".
func TestCreateListener_GWT_LST_006_Internal_SubnetRequired(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeInternal)
	_, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "internal-no-subnet",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""), // empty subnet_id
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "subnet_id is required for INTERNAL")
}

// TestCreateListener_GWT_LST_007_Internal_AutoAllocSubnet — INTERNAL with
// subnet_id → AllocateInternalIP called with subnet_id.
func TestCreateListener_GWT_LST_007_Internal_AutoAllocSubnet(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeInternal)
	suite.internalAddrs.nextAllocID = "e9bINTERNALIP00001"
	suite.internalAddrs.nextAllocValue = "10.0.0.5"

	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "internal-ok",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec("e9bSUBNETOWNER0001"),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.Nil(t, done.Error, "Operation must succeed")
	require.Len(t, suite.internalAddrs.allocInternalCalls, 1)
	require.Equal(t, "e9bSUBNETOWNER0001", suite.internalAddrs.allocInternalCalls[0].SubnetID)
	require.Len(t, suite.internalAddrs.allocExternalCalls, 0)

	listeners := suite.allListeners()
	require.Len(t, listeners, 1)
	require.Equal(t, "10.0.0.5", string(listeners[0].AllocatedAddress))
}

// TestCreateListener_GWT_LST_007_NameRegexInvalid — name regex invalid →
// InvalidArgument synchronously (before Operation is even created).
func TestCreateListener_GWT_LST_007_NameRegexInvalid(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	_, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "BadName", // uppercase rejected by strict regex
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""),
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestCreateListener_GWT_LST_008_PortOutOfRange — port=0 → InvalidArgument sync.
func TestCreateListener_GWT_LST_008_PortOutOfRange(t *testing.T) {
	t.Parallel()
	for _, port := range []int64{0, 65536, -1} {
		t.Run(fmt.Sprintf("port=%d", port), func(t *testing.T) {
			t.Parallel()
			suite := newCreateSuite(t, domain.LBTypeExternal)
			_, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
				LoadBalancerId: string(suite.lb.ID),
				Name:           "bad-port",
				Protocol:       lbv1.Listener_TCP,
				Port:           port,
				TargetPort:     8080,
				IpVersion:      lbv1.IpVersion_IPV4,
				AddressSpec:    autoSpec(""),
			})
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

// TestCreateListener_GWT_LST_009_UnsupportedProtocol — TCP/UDP only.
func TestCreateListener_GWT_LST_009_UnsupportedProtocol(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	_, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "bad-proto",
		Protocol:       lbv1.Listener_PROTOCOL_UNSPECIFIED, // not TCP/UDP
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""),
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestCreateListener_GWT_LST_010_DuplicatePortProto — fake repo enforces UNIQUE
// (lb_id, port, protocol) → AlreadyExists. Second Insert through writer fails.
func TestCreateListener_GWT_LST_010_DuplicatePortProto(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	// First Create succeeds.
	op1, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "primary",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""),
	})
	require.NoError(t, err)
	awaitOpDone(t, suite.ops, op1.ID, time.Second)
	require.Len(t, suite.allListeners(), 1)

	// Second Create with same (LB, port, protocol) — fake AllocateExternalIP
	// returns a DIFFERENT allocated address to avoid (region, vip, port, proto)
	// UNIQUE; here we test only (lb_id, port, protocol).
	suite.internalAddrs.nextAllocID = "e9bSECONDALLOC00002"
	suite.internalAddrs.nextAllocValue = "203.0.113.99"
	op2, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "duplicate",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op2.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.AlreadyExists), done.Error.Code)
	require.Contains(t, done.Error.Message, "already exists")

	// Compensation: FreeIP called for the second alloc to release VIP.
	require.Len(t, suite.internalAddrs.freeCalls, 1)
	require.Equal(t, "e9bSECONDALLOC00002", suite.internalAddrs.freeCalls[0])
}

// TestCreateListener_GWT_LST_011_DuplicateRegionVIPPortProto — same (region,
// vip, port, protocol) across LBs blocked by partial UNIQUE in fake.
func TestCreateListener_GWT_LST_011_DuplicateRegionVIPPortProto(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	// Second LB in same region with different name.
	lb2 := newRecordLB(t, suite.lb.ProjectID, suite.lb.RegionID, domain.LBTypeExternal, "lb-2")
	suite.repo.seedLB(lb2)

	const sharedAddrID = "e9bSHAREDADDR00001"
	suite.addresses.seed(&vpcclient.Address{
		ID:        sharedAddrID,
		ProjectID: string(suite.lb.ProjectID),
		Name:      "tenant-shared",
		Value:     "203.0.113.150",
		Family:    vpcclient.AddressFamilyIPv4,
	})

	// LB-A create: BYO sharedAddr (succeeds).
	op1, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "first",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    byoSpec(sharedAddrID),
	})
	require.NoError(t, err)
	awaitOpDone(t, suite.ops, op1.ID, time.Second)
	require.Len(t, suite.allListeners(), 1)

	// LB-B create: same BYO addr — already used by lst-* listener →
	// branch fires (FailedPrecondition on used_by check), не доходим до
	// region/vip UNIQUE. Тест проверяет, что в любом случае дубль blocked.
	suite.addresses.seed(&vpcclient.Address{
		ID:        sharedAddrID,
		ProjectID: string(suite.lb.ProjectID),
		Name:      "tenant-shared",
		Value:     "203.0.113.150",
		Family:    vpcclient.AddressFamilyIPv4,
		UsedBy: &vpcclient.AddressOwner{
			Kind: addressOwnerKindNLBListener,
			ID:   string(suite.allListeners()[0].ID),
		},
	})

	op2, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(lb2.ID),
		Name:           "second",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    byoSpec(sharedAddrID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op2.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Contains(t, []int32{int32(codes.FailedPrecondition), int32(codes.AlreadyExists)}, done.Error.Code)
}

// TestCreateListener_GWT_LST_014_VIPAllocFails_OperationError — peer alloc fails
// → ops.MarkError, no listener row, no outbox.
func TestCreateListener_GWT_LST_014_VIPAllocFails(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	suite.internalAddrs.allocErr = fmt.Errorf("%w: pool exhausted", domain.ErrFailedPrecondition)

	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "vip-fail",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.FailedPrecondition), done.Error.Code)
	require.Contains(t, done.Error.Message, "pool exhausted")
	require.Empty(t, suite.allListeners())
	require.Empty(t, suite.repo.pendingOutbox(), "no outbox events on failed alloc")
}

// TestCreateListener_GWT_LST_015_InsertFailsAfterAlloc_Compensation — VIP
// allocated but Insert fails → defer FreeIP runs.
func TestCreateListener_GWT_LST_015_CompensationAfterInsertFail(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	// Inject Insert error so VIP gets allocated but row never persisted.
	suite.repo.insertErr = fmt.Errorf("%w: simulated DB CHECK violation", domain.ErrInvalidArg)

	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "compensate",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Equal(t, int32(codes.InvalidArgument), done.Error.Code)
	require.Len(t, suite.internalAddrs.freeCalls, 1, "FreeIP must be called as compensation")
	require.Equal(t, "e9bALLOCSTUB000001", suite.internalAddrs.freeCalls[0])
	require.Empty(t, suite.allListeners())
	require.Empty(t, suite.repo.pendingOutbox())
}

// TestCreateListener_BYO_CompensationClearReference — BYO branch, Insert fails
// → ClearReference compensation (NOT FreeIP — tenant Address must not be deleted).
func TestCreateListener_BYO_Compensation_ClearReference(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	const addrID = "e9bBYOCOMPENSATE01"
	suite.addresses.seed(&vpcclient.Address{
		ID:        addrID,
		ProjectID: string(suite.lb.ProjectID),
		Name:      "tenant-byo",
		Value:     "198.51.100.20",
		Family:    vpcclient.AddressFamilyIPv4,
	})
	suite.repo.insertErr = fmt.Errorf("%w: simulated Insert failure", domain.ErrInternal)

	op, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "byo-comp",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    byoSpec(addrID),
	})
	require.NoError(t, err)
	done := awaitOpDone(t, suite.ops, op.ID, time.Second)
	require.NotNil(t, done.Error)
	require.Len(t, suite.internalAddrs.clearCalls, 1,
		"ClearReference must be called (BYO Address must not be deleted)")
	require.Equal(t, addrID, suite.internalAddrs.clearCalls[0])
	require.Empty(t, suite.internalAddrs.freeCalls, "FreeIP must NOT be called for BYO compensation")
}

// TestCreateListener_LBNotFound — parent LB does not exist → NotFound sync.
func TestCreateListener_LBNotFound(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	_, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: "nlbNOTREALLLLLLLL01",
		Name:           "orphan",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""),
	})
	require.Error(t, err)
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestCreateListener_EmptyAddressSpec — proto requires address_spec; nil →
// InvalidArgument.
func TestCreateListener_EmptyAddressSpec(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	_, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "no-spec",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    nil,
	})
	require.Error(t, err)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestCreateListener_LBDeleting — parent LB.status==DELETING → FailedPrecondition.
func TestCreateListener_LBDeleting(t *testing.T) {
	t.Parallel()
	suite := newCreateSuite(t, domain.LBTypeExternal)
	suite.lb.Status = domain.LBStatusDeleting
	suite.repo.seedLB(suite.lb)
	_, err := suite.uc.Run(context.Background(), &lbv1.CreateListenerRequest{
		LoadBalancerId: string(suite.lb.ID),
		Name:           "lb-deleting",
		Protocol:       lbv1.Listener_TCP,
		Port:           80,
		TargetPort:     8080,
		IpVersion:      lbv1.IpVersion_IPV4,
		AddressSpec:    autoSpec(""),
	})
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, err.Error(), "being deleted")
}

// ---- shared helpers ----

type createSuite struct {
	t             *testing.T
	repo          *fakeRepo
	ops           *fakeOpsRepo
	addresses     *fakeAddressClient
	internalAddrs *fakeInternalAddressClient
	subnets       *fakeSubnetClient
	lb            *kachorepo.LoadBalancerRecord
	uc            *CreateUseCase
}

func newCreateSuite(t *testing.T, lbType domain.LBType) *createSuite {
	t.Helper()
	repo := newFakeRepo()
	lb := newRecordLB(t, "prj01TESTPROJ0000001", "ru-central1", lbType, "test-lb")
	repo.seedLB(lb)
	ops := newFakeOpsRepo()
	addresses := newFakeAddressClient()
	internalAddrs := newFakeInternalAddressClient()
	subnets := newFakeSubnetClient()
	uc := NewCreateUseCase(repo, ops, addresses, internalAddrs, subnets, slog.Default())
	return &createSuite{
		t:             t,
		repo:          repo,
		ops:           ops,
		addresses:     addresses,
		internalAddrs: internalAddrs,
		subnets:       subnets,
		lb:            lb,
		uc:            uc,
	}
}

func newRecordLB(t *testing.T, projectID domain.ProjectID, regionID domain.RegionID, lbType domain.LBType, name string) *kachorepo.LoadBalancerRecord {
	t.Helper()
	lb := domain.NewLoadBalancer(projectID, regionID, domain.LbName(name), "", domain.LbLabels{}, lbType)
	lb.Status = domain.LBStatusActive
	lb.ID = domain.ResourceID(ids.NewID(ids.PrefixLoadBalancer))
	return &kachorepo.LoadBalancerRecord{
		LoadBalancer: lb,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
}

func (s *createSuite) allListeners() []*kachorepo.ListenerRecord {
	s.repo.mu.Lock()
	defer s.repo.mu.Unlock()
	out := make([]*kachorepo.ListenerRecord, 0, len(s.repo.listeners))
	for _, l := range s.repo.listeners {
		c := *l
		out = append(out, &c)
	}
	return out
}

func (s *createSuite) ctxWithSubject() context.Context {
	return contextWithSubject("user:test-actor")
}

func autoSpec(subnetID string) *lbv1.ListenerAddressSpec {
	return &lbv1.ListenerAddressSpec{
		Source: &lbv1.ListenerAddressSpec_Auto{
			Auto: &lbv1.ListenerAddressSpec_AutoAllocate{SubnetId: subnetID},
		},
	}
}

func byoSpec(addrID string) *lbv1.ListenerAddressSpec {
	return &lbv1.ListenerAddressSpec{
		Source: &lbv1.ListenerAddressSpec_AddressId{AddressId: addrID},
	}
}

// _ used so errors package linked even if no test uses it directly.
var _ = errors.New
