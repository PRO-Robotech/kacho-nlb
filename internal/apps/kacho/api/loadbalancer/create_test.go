// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/geo"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// onlyLB returns the single LoadBalancer stored in the fake repo, failing if the
// count is not exactly one.
func onlyLB(t *testing.T, repo *fakeRepo) domain.LoadBalancer {
	t.Helper()
	require.Len(t, repo.lbs, 1)
	for _, lb := range repo.lbs {
		return lb.LoadBalancer
	}
	return domain.LoadBalancer{}
}

// lbFieldViolations flattens gRPC-status BadRequest field violations into
// "field: description" lines for assert.Contains.
func lbFieldViolations(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return err.Error()
	}
	parts := []string{st.Message()}
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, v := range br.GetFieldViolations() {
				parts = append(parts, v.GetField()+": "+v.GetDescription())
			}
		}
	}
	return strings.Join(parts, " | ")
}

// autoV4Spec — address_spec с auto-аллокацией одного семейства IPv4 (явный пул).
func autoV4Spec(poolID string) *lbv1.NetworkLoadBalancerAddressSpec {
	return &lbv1.NetworkLoadBalancerAddressSpec{
		V4: &lbv1.FamilyAddressSpec{Source: &lbv1.FamilyAddressSpec_Auto{
			Auto: &lbv1.FamilyAddressSpec_AnycastAllocate{AnycastPoolId: poolID},
		}},
	}
}

// internalReq — валидный INTERNAL Create-request (auto v4) с заданным именем.
func internalReq(name string) *lbv1.CreateNetworkLoadBalancerRequest {
	return &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "ru-central1", Name: name,
		Type: lbv1.NetworkLoadBalancer_INTERNAL, NetworkId: "net-1",
		AddressSpec: autoV4Spec("aap-1"),
	}
}

// newCreateUC — use-case с дефолтными network/sg/anycast фейками.
func newCreateUC(repo *fakeRepo, opsRepo *fakeOpsRepo, pc ProjectClient, rc RegionClient) *CreateLoadBalancerUseCase {
	return NewCreateLoadBalancerUseCase(repo, opsRepo, pc, rc, &fakeNetworkClient{}, &fakeSecurityGroupClient{}, &fakeAnycastClient{}, slog.Default())
}

// newCreateUCWithAnycast — вариант с явным anycast-фейком для assert'ов саги.
func newCreateUCWithAnycast(repo *fakeRepo, opsRepo *fakeOpsRepo, ac AnycastAddressClient) *CreateLoadBalancerUseCase {
	return NewCreateLoadBalancerUseCase(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{}, &fakeNetworkClient{}, &fakeSecurityGroupClient{}, ac, slog.Default())
}

// TestCreateLoadBalancer_SessionAffinity — session_affinity from the request is
// persisted; UNSPECIFIED falls back to the FIVE_TUPLE default.
func TestCreateLoadBalancer_SessionAffinity(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		in   lbv1.NetworkLoadBalancer_SessionAffinity
		want domain.SessionAffinity
	}{
		"client_ip_only": {lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY, domain.SessionAffinityClientIPOnly},
		"five_tuple":     {lbv1.NetworkLoadBalancer_FIVE_TUPLE, domain.SessionAffinity5Tuple},
		"unspecified":    {lbv1.NetworkLoadBalancer_SESSION_AFFINITY_UNSPECIFIED, domain.SessionAffinity5Tuple},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepo()
			opsRepo := newFakeOpsRepo()
			uc := newCreateUC(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{})
			req := internalReq("edge-affinity")
			req.SessionAffinity = tc.in
			op, err := uc.Execute(context.Background(), req)
			require.NoError(t, err)
			require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
			require.Equal(t, tc.want, onlyLB(t, repo).SessionAffinity)
		})
	}
}

// TestCreateLoadBalancer_SessionAffinityOutOfDomain — a numeric value outside
// {0,1,2} is rejected synchronously with the verbatim field message.
func TestCreateLoadBalancer_SessionAffinityOutOfDomain(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := newCreateUC(repo, newFakeOpsRepo(), &fakeProjectClient{}, &fakeRegionClient{})
	req := internalReq("edge")
	req.SessionAffinity = lbv1.NetworkLoadBalancer_SessionAffinity(99)
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, lbFieldViolations(err),
		"session_affinity: session_affinity must be one of: FIVE_TUPLE, CLIENT_IP_ONLY")
	require.Empty(t, repo.lbs, "LB must not be persisted on out-of-domain session_affinity")
}

// TestCreateLoadBalancer_CrossZoneEnabled — explicit cross_zone_enabled honoured;
// omitted keeps the default (true).
func TestCreateLoadBalancer_CrossZoneEnabled(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		in   *bool
		want bool
	}{
		"explicit_false": {proto.Bool(false), false},
		"explicit_true":  {proto.Bool(true), true},
		"omitted":        {nil, true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepo()
			opsRepo := newFakeOpsRepo()
			uc := newCreateUC(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{})
			req := internalReq("edge-cz")
			req.CrossZoneEnabled = tc.in
			op, err := uc.Execute(context.Background(), req)
			require.NoError(t, err)
			require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
			require.Equal(t, tc.want, onlyLB(t, repo).CrossZoneEnabled)
		})
	}
}

// TestCreateLoadBalancer_AutoV4HappyPath — INTERNAL auto-alloc single-family v4:
// durable-handle сага финализирует в INACTIVE, VIP проставлен, ip_families=[IPV4],
// AllocateAnycast вызван ровно один раз (GWT-01).
func TestCreateLoadBalancer_AutoV4HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	ac := &fakeAnycastClient{}
	uc := newCreateUCWithAnycast(repo, opsRepo, ac)

	op, err := uc.Execute(context.Background(), internalReq("edge-internal"))
	require.NoError(t, err)
	require.False(t, op.Done)

	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)
	require.NotNil(t, final.Response)

	lb := onlyLB(t, repo)
	require.Equal(t, domain.LBStatusInactive, lb.Status)
	require.Equal(t, domain.LBTypeInternal, lb.Type)
	require.Equal(t, []domain.IPVersion{domain.IPVersionV4}, lb.IPFamilies)
	require.NotEmpty(t, string(lb.AddressV4), "address_v4 set by worker")
	require.NotEmpty(t, string(lb.AddressIDV4), "address_id_v4 bound")
	require.Equal(t, domain.VipOriginAuto, lb.VipOriginV4)
	require.Empty(t, string(lb.AddressV6))
	require.Len(t, ac.allocReqs, 1, "AllocateAnycast called once for v4")
	require.Equal(t, vpcclient.AddressFamilyIPv4, ac.allocReqs[0].Family)
	require.Equal(t, "aap-1", ac.allocReqs[0].AnycastPoolID)
	require.Equal(t, "net-1", ac.allocReqs[0].NetworkID)

	evts := repo.outboxEvents()
	require.Len(t, evts, 1)
	require.Equal(t, "CREATED", evts[0].Action)
}

// TestCreateLoadBalancer_DualstackFanOut — v4+v6 auto: оба семейства аллоцированы
// и привязаны; ip_families=[IPV4,IPV6]; AllocateAnycast вызван дважды (GWT-03).
func TestCreateLoadBalancer_DualstackFanOut(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	ac := &fakeAnycastClient{}
	uc := newCreateUCWithAnycast(repo, opsRepo, ac)

	req := internalReq("edge-ds")
	req.AddressSpec = &lbv1.NetworkLoadBalancerAddressSpec{
		V4: &lbv1.FamilyAddressSpec{Source: &lbv1.FamilyAddressSpec_Auto{Auto: &lbv1.FamilyAddressSpec_AnycastAllocate{AnycastPoolId: "aap-1"}}},
		V6: &lbv1.FamilyAddressSpec{Source: &lbv1.FamilyAddressSpec_Auto{Auto: &lbv1.FamilyAddressSpec_AnycastAllocate{AnycastPoolId: "aap-6"}}},
	}
	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)

	lb := onlyLB(t, repo)
	require.Equal(t, []domain.IPVersion{domain.IPVersionV4, domain.IPVersionV6}, lb.IPFamilies)
	require.NotEmpty(t, string(lb.AddressV4))
	require.NotEmpty(t, string(lb.AddressV6))
	require.NotEmpty(t, string(lb.AddressIDV4))
	require.NotEmpty(t, string(lb.AddressIDV6))
	require.Len(t, ac.allocReqs, 2, "AllocateAnycast called for v4 and v6")
}

// TestCreateLoadBalancer_BYOv4 — BYO v4: AttachAnycastBYO вызван с expect-guard
// (project/family); VIP проставлен из принесённого Address (GWT-04).
func TestCreateLoadBalancer_BYOv4(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	ac := &fakeAnycastClient{}
	uc := newCreateUCWithAnycast(repo, opsRepo, ac)

	req := internalReq("edge-byo")
	req.AddressSpec = &lbv1.NetworkLoadBalancerAddressSpec{
		V4: &lbv1.FamilyAddressSpec{Source: &lbv1.FamilyAddressSpec_Byo{
			Byo: &lbv1.FamilyAddressSpec_AnycastByo{AddressId: "adr00000000000000byo"},
		}},
	}
	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)

	require.Len(t, ac.byoReqs, 1)
	require.Equal(t, "prj-a", ac.byoReqs[0].ExpectProjectID)
	require.Equal(t, vpcclient.AddressFamilyIPv4, ac.byoReqs[0].ExpectFamily)
	require.Equal(t, "adr00000000000000byo", ac.byoReqs[0].AddressID)
	lb := onlyLB(t, repo)
	require.Equal(t, domain.VipOriginBYO, lb.VipOriginV4)
	require.Equal(t, "adr00000000000000byo", string(lb.AddressIDV4))
}

// TestCreateLoadBalancer_CompensationOnV6Fail — dualstack где v6-acquire падает:
// worker компенсирует уже аллоцированный v4 (FreeIP по address_id_v4) и снимает
// handle; Operation done с error; LB не остаётся (GWT-19).
func TestCreateLoadBalancer_CompensationOnV6Fail(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	ac := &fakeAnycastClient{
		allocFunc: func(ctx context.Context, req vpcclient.AllocateAnycastRequest) (*vpcclient.AllocateResponse, error) {
			if req.Family == vpcclient.AddressFamilyIPv6 {
				return nil, fmt.Errorf("%w: could not allocate anycast address", domain.ErrFailedPrecondition)
			}
			return &vpcclient.AllocateResponse{AddressID: "adr0000000000000v4x", Value: "10.0.0.7"}, nil
		},
	}
	uc := newCreateUCWithAnycast(repo, opsRepo, ac)

	req := internalReq("edge-ds-fail")
	req.AddressSpec = &lbv1.NetworkLoadBalancerAddressSpec{
		V4: &lbv1.FamilyAddressSpec{Source: &lbv1.FamilyAddressSpec_Auto{Auto: &lbv1.FamilyAddressSpec_AnycastAllocate{AnycastPoolId: "aap-1"}}},
		V6: &lbv1.FamilyAddressSpec{Source: &lbv1.FamilyAddressSpec_Auto{Auto: &lbv1.FamilyAddressSpec_AnycastAllocate{AnycastPoolId: "aap-6"}}},
	}
	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)

	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.FailedPrecondition), final.Error.GetCode())
	// v4 освобождён compensation'ом; handle снят → LB не остаётся.
	require.Equal(t, []string{"adr0000000000000v4x"}, ac.freed, "v4 freed by compensation")
	require.Empty(t, repo.lbs, "handle deleted by compensation")
}

// TestCreateLoadBalancer_ExternalRejected — type=EXTERNAL отклоняется синхронно
// (фаза 1 INTERNAL-only) — Operation не создаётся (GWT-22).
func TestCreateLoadBalancer_ExternalRejected(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := newCreateUC(repo, newFakeOpsRepo(), &fakeProjectClient{}, &fakeRegionClient{})
	req := internalReq("edge-ext")
	req.Type = lbv1.NetworkLoadBalancer_EXTERNAL
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "only INTERNAL is supported")
	require.Empty(t, repo.lbs)
}

// TestCreateLoadBalancer_NoFamily — пустой address_spec (ни v4, ни v6) →
// синхронный InvalidArgument (GWT-08).
func TestCreateLoadBalancer_NoFamily(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := newCreateUC(repo, newFakeOpsRepo(), &fakeProjectClient{}, &fakeRegionClient{})
	req := internalReq("edge-nf")
	req.AddressSpec = nil
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "at least one ip family")
	require.Empty(t, repo.lbs)
}

// TestCreateLoadBalancer_BYOMalformed — malformed BYO addressId → синхронный
// InvalidArgument "invalid address id '<x>'" (GWT-06).
func TestCreateLoadBalancer_BYOMalformed(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := newCreateUC(repo, newFakeOpsRepo(), &fakeProjectClient{}, &fakeRegionClient{})
	req := internalReq("edge-bad")
	req.AddressSpec = &lbv1.NetworkLoadBalancerAddressSpec{
		V4: &lbv1.FamilyAddressSpec{Source: &lbv1.FamilyAddressSpec_Byo{
			Byo: &lbv1.FamilyAddressSpec_AnycastByo{AddressId: "not-an-id"},
		}},
	}
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "invalid address id 'not-an-id'")
	require.Empty(t, repo.lbs)
}

func TestCreateLoadBalancer_MissingNetworkID(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	uc := newCreateUC(repo, newFakeOpsRepo(), &fakeProjectClient{}, &fakeRegionClient{})
	req := internalReq("edge-nonet")
	req.NetworkId = ""
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, lbFieldViolations(err), "network_id is required for INTERNAL load balancer")
}

func TestCreateLoadBalancer_InvalidProjectID(t *testing.T) {
	t.Parallel()
	uc := newCreateUC(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	req := internalReq("edge")
	req.ProjectId = ""
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateLoadBalancer_InvalidName(t *testing.T) {
	t.Parallel()
	uc := newCreateUC(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	req := internalReq("Edge!")
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateLoadBalancer_TypeUnspecified(t *testing.T) {
	t.Parallel()
	uc := newCreateUC(newFakeRepo(), newFakeOpsRepo(), nil, nil)
	req := internalReq("edge")
	req.Type = lbv1.NetworkLoadBalancer_TYPE_UNSPECIFIED
	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateLoadBalancer_DuplicateName(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	seedLB(t, repo, "prj-a", "edge")
	uc := newCreateUC(repo, newFakeOpsRepo(), nil, nil)
	_, err := uc.Execute(context.Background(), internalReq("edge"))
	require.Equal(t, codes.AlreadyExists, status.Code(err))
}

func TestCreateLoadBalancer_ProjectNotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	pc := &fakeProjectClient{
		getFunc: func(ctx context.Context, projectID string) (*iam.Project, error) {
			return nil, fmt.Errorf("%w: Project %s not found", domain.ErrNotFound, projectID)
		},
	}
	uc := newCreateUC(repo, opsRepo, pc, &fakeRegionClient{})
	op, err := uc.Execute(context.Background(), internalReq("edge"))
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error, "operation should have async error")
	require.Equal(t, int32(codes.InvalidArgument), final.Error.GetCode())
	require.Empty(t, repo.lbs)
}

// TestCreateLoadBalancer_RegionNotFound — region отсутствует: worker peer-check
// возвращает error ДО Insert-handle (компенсировать нечего) — LB не создан (GWT-25).
func TestCreateLoadBalancer_RegionNotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	rc := &fakeRegionClient{
		getFunc: func(ctx context.Context, regionID string) (*geo.Region, error) {
			return nil, fmt.Errorf("%w: Region %s not found", domain.ErrInvalidArg, regionID)
		},
	}
	uc := newCreateUC(repo, opsRepo, &fakeProjectClient{}, rc)
	op, err := uc.Execute(context.Background(), internalReq("edge"))
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Empty(t, repo.lbs, "handle not inserted (peer-check before insert)")
}

// TestCreateLoadBalancer_FGARegisterIntentEmitted — finalize пишет fga.register-
// intent (project-hierarchy) в writer-tx.
func TestCreateLoadBalancer_FGARegisterIntentEmitted(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	opsRepo := newFakeOpsRepo()
	uc := newCreateUC(repo, opsRepo, &fakeProjectClient{}, &fakeRegionClient{})
	op, err := uc.Execute(context.Background(), internalReq("edge"))
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.Nil(t, final.Error)

	require.Len(t, repo.fga, 1, "expected one fga.register intent in writer-tx")
	ev := repo.fga[0]
	require.Equal(t, domain.FGAEventRegister, ev.EventType)
	require.Equal(t, "NetworkLoadBalancer", ev.Intent.Kind)
	require.NotEmpty(t, ev.Intent.Tuples)
	require.Equal(t, domain.FGARelationProject, ev.Intent.Tuples[0].Relation)
	require.Equal(t, "project:prj-a", ev.Intent.Tuples[0].SubjectID)
}

func TestCreateLoadBalancer_ProjectClientErrorMapped(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		peerErr  error
		wantCode codes.Code
	}{
		"unavailable":         {fmt.Errorf("%w: dial", domain.ErrUnavailable), codes.Unavailable},
		"invalid_arg":         {fmt.Errorf("%w: invalid project", domain.ErrInvalidArg), codes.InvalidArgument},
		"failed_precondition": {fmt.Errorf("%w: project deleted", domain.ErrFailedPrecondition), codes.FailedPrecondition},
		"generic":             {errors.New("boom"), codes.Internal},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opsRepo := newFakeOpsRepo()
			pc := &fakeProjectClient{getFunc: func(_ context.Context, _ string) (*iam.Project, error) {
				return nil, tc.peerErr
			}}
			uc := newCreateUC(newFakeRepo(), opsRepo, pc, &fakeRegionClient{})
			op, err := uc.Execute(context.Background(), internalReq("edge"))
			require.NoError(t, err)
			final := awaitOpDone(t, opsRepo, op.ID)
			require.NotNil(t, final.Error)
			require.Equal(t, int32(tc.wantCode), final.Error.GetCode())
		})
	}
}
