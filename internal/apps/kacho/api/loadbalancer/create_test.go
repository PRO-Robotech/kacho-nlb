// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// Тестовые cross-service id (prefix из известного набора corevalidate).
const (
	lbTestSubnetRegional = "sub-reg"
	lbTestSubnetZonal    = "sub-zon"
	lbTestAddrInternal   = "adr-int"
	lbTestAddrExternal   = "adr-ext"
)

// ---- Конструкторы use-case для тестов --------------------------------------

// createDeps — инъекция peer-двойников (nil → sensible default).
type createDeps struct {
	subnet SubnetClient
	reader AddressClient
	addr   InternalAddressClient
	zone   ZoneClient
	region RegionClient
}

func newCreateUC(repo *fakeRepo, opsRepo *fakeOpsRepo, d createDeps) *CreateLoadBalancerUseCase {
	if d.subnet == nil {
		d.subnet = &fakeSubnetClient{}
	}
	if d.reader == nil {
		d.reader = &fakeAddressReader{}
	}
	if d.addr == nil {
		d.addr = &fakeAddressClient{}
	}
	if d.zone == nil {
		d.zone = &fakeZoneClient{}
	}
	if d.region == nil {
		d.region = &fakeRegionClient{}
	}
	return NewCreateLoadBalancerUseCase(repo, opsRepo,
		&fakeProjectClient{}, d.region, d.zone, d.subnet, d.reader, d.addr, slog.Default())
}

// ---- VipSource-хелперы -----------------------------------------------------

func vipSubnet(id string) *lbv1.VipSource {
	return &lbv1.VipSource{Source: &lbv1.VipSource_SubnetId{SubnetId: id}}
}
func vipAddress(id string) *lbv1.VipSource {
	return &lbv1.VipSource{Source: &lbv1.VipSource_AddressId{AddressId: id}}
}
func vipPublic() *lbv1.VipSource {
	return &lbv1.VipSource{Source: &lbv1.VipSource_Public{Public: &lbv1.PublicVip{}}}
}

func baseCreateReq() *lbv1.CreateNetworkLoadBalancerRequest {
	return &lbv1.CreateNetworkLoadBalancerRequest{
		ProjectId: "prj-a", RegionId: "region-1", Name: "lb-1",
	}
}

// ---- Группа A/B — happy positives ------------------------------------------

// 8.1-01: INTERNAL ZONAL subnet-auto (unicast).
func TestCreate_InternalZonal_SubnetAuto(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: &fakeSubnetClient{placement: "ZONAL"}})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
	req.V4Source = vipSubnet(lbTestSubnetZonal)

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)

	rec := lbByName(t, repo, "lb-1")
	require.Equal(t, domain.PlacementZonal, rec.PlacementType)
	require.NotEmpty(t, string(rec.AddressIDV4))
	require.Empty(t, string(rec.AddressIDV6))
	require.Equal(t, domain.VipOriginAuto, rec.VipOriginV4)
	require.Equal(t, domain.LBStatusInactive, rec.Status)
}

// 8.1-02: INTERNAL REGIONAL subnet-auto (anycast).
func TestCreate_InternalRegional_SubnetAuto(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	uc := newCreateUC(repo, opsRepo, createDeps{})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet(lbTestSubnetRegional)

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	rec := lbByName(t, repo, "lb-1")
	require.Equal(t, domain.PlacementRegional, rec.PlacementType)
	require.Empty(t, rec.DisabledAnnounceZones)
}

// 8.1-03: INTERNAL REGIONAL + disabled_announce_zones on Create.
func TestCreate_InternalRegional_Drain(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	uc := newCreateUC(repo, opsRepo, createDeps{})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet(lbTestSubnetRegional)
	req.DisabledAnnounceZones = []string{"region-1-b"}

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Equal(t, []string{"region-1-b"}, lbByName(t, repo, "lb-1").DisabledAnnounceZones)
}

// 8.1-04: INTERNAL address-link (owned=false).
func TestCreate_InternalRegional_AddressLink(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	addr := &fakeAddressClient{}
	uc := newCreateUC(repo, opsRepo, createDeps{
		reader: &fakeAddressReader{},
		addr:   addr,
	})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipAddress(lbTestAddrInternal)

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	rec := lbByName(t, repo, "lb-1")
	require.Equal(t, domain.VipOriginLinked, rec.VipOriginV4)
	require.Equal(t, lbTestAddrInternal, string(rec.AddressIDV4))
	require.Len(t, addr.byoReqs, 1)
	require.False(t, addr.byoReqs[0].Owned)
}

// 8.1-05: INTERNAL REGIONAL dualstack — v4 subnet-auto + v6 link, same network.
func TestCreate_InternalRegional_Dualstack_Mixed(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	uc := newCreateUC(repo, opsRepo, createDeps{
		reader: &fakeAddressReader{family: vpcclient.AddressFamilyIPv6},
	})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet(lbTestSubnetRegional)
	req.V6Source = vipAddress("adr-6")

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	rec := lbByName(t, repo, "lb-1")
	require.NotEmpty(t, string(rec.AddressIDV4))
	require.Equal(t, "adr-6", string(rec.AddressIDV6))
}

// 8.1-06: EXTERNAL public → неявный public IP (underlying zone derived).
func TestCreate_External_Public(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	addr := &fakeAddressClient{}
	uc := newCreateUC(repo, opsRepo, createDeps{addr: addr})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_EXTERNAL
	req.V4Source = vipPublic()

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	rec := lbByName(t, repo, "lb-1")
	require.Equal(t, domain.PlacementUnspecified, rec.PlacementType)
	require.Equal(t, domain.VipOriginAuto, rec.VipOriginV4)
	require.Len(t, addr.extReqs, 1)
	require.NotEmpty(t, addr.extReqs[0].ZoneID)
}

// 8.1-07: EXTERNAL address-link (BYO external).
func TestCreate_External_AddressLink(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	uc := newCreateUC(repo, opsRepo, createDeps{
		reader: &fakeAddressReader{external: true},
	})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_EXTERNAL
	req.V4Source = vipAddress(lbTestAddrExternal)

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Equal(t, domain.VipOriginLinked, lbByName(t, repo, "lb-1").VipOriginV4)
}

// ---- Группа C — sync negatives (fail-fast, Operation НЕ создаётся) ----------

func TestCreate_SyncNegatives(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*lbv1.CreateNetworkLoadBalancerRequest)
		deps createDeps
		msg  string
	}{
		{ // 8.1-08
			name: "subnet source on EXTERNAL",
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_EXTERNAL
				r.V4Source = vipSubnet(lbTestSubnetRegional)
			},
			msg: "subnet address source is only valid for INTERNAL load balancer",
		},
		{ // 8.1-09
			name: "public source on INTERNAL",
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
				r.V4Source = vipPublic()
			},
			msg: "public address source is only valid for EXTERNAL load balancer",
		},
		{ // 8.1-10 kind mismatch: external address in INTERNAL
			name: "external address linked into INTERNAL",
			deps: createDeps{reader: &fakeAddressReader{external: true}},
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
				r.V4Source = vipAddress(lbTestAddrExternal)
			},
			msg: "Illegal argument addressId",
		},
		{ // 8.1-11a placement mismatch: ZONAL LB + REGIONAL subnet
			name: "zonal lb regional subnet",
			deps: createDeps{subnet: &fakeSubnetClient{placement: "REGIONAL"}},
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
				r.V4Source = vipSubnet(lbTestSubnetRegional)
			},
			msg: "subnet placement does not match load balancer placement",
		},
		{ // 8.1-11b placement mismatch via address_id → generic
			name: "regional lb zonal linked address",
			deps: createDeps{subnet: &fakeSubnetClient{placement: "ZONAL"}},
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
				r.V4Source = vipAddress(lbTestAddrInternal)
			},
			msg: "Illegal argument addressId",
		},
		{ // 8.1-12a placement on EXTERNAL
			name: "placement on external",
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_EXTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
				r.V4Source = vipPublic()
			},
			msg: "placement_type is only valid for INTERNAL load balancer",
		},
		{ // 8.1-12b placement missing on INTERNAL
			name: "placement missing on internal",
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.V4Source = vipSubnet(lbTestSubnetRegional)
			},
			msg: "placement_type is required for INTERNAL load balancer",
		},
		{ // 8.1-13 drain on ZONAL
			name: "drain on zonal",
			deps: createDeps{subnet: &fakeSubnetClient{placement: "ZONAL"}},
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
				r.V4Source = vipSubnet(lbTestSubnetZonal)
				r.DisabledAnnounceZones = []string{"region-1-a"}
			},
			msg: "disabled_announce_zones is only valid for REGIONAL load balancer",
		},
		{ // 8.1-14 drain covers all zones
			name: "drain covers all zones",
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
				r.V4Source = vipSubnet(lbTestSubnetRegional)
				r.DisabledAnnounceZones = []string{"region-1-a", "region-1-b"}
			},
			msg: "disabled_announce_zones must not cover all zones of the region",
		},
		{ // 8.1-15 drain zone not in region
			name: "drain zone outside region",
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
				r.V4Source = vipSubnet(lbTestSubnetRegional)
				r.DisabledAnnounceZones = []string{"region-2-a"}
			},
			msg: "zone region-2-a is not in region region-1",
		},
		{ // 8.1-16 foreign project address
			name: "foreign project address",
			deps: createDeps{reader: &fakeAddressReader{projectID: "prj-b"}},
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
				r.V4Source = vipAddress("adr-foreign")
			},
			msg: "Illegal argument addressId",
		},
		{ // 8.1-17 family/slot mismatch (v4 slot → v6 address)
			name: "family slot mismatch",
			deps: createDeps{reader: &fakeAddressReader{family: vpcclient.AddressFamilyIPv6}},
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
				r.V4Source = vipAddress("adr-6only")
			},
			msg: "Illegal argument addressId",
		},
		{ // 8.1-19 no source
			name: "no source",
			mut: func(r *lbv1.CreateNetworkLoadBalancerRequest) {
				r.Type = lbv1.NetworkLoadBalancer_INTERNAL
				r.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
			},
			msg: "load balancer must declare a vip source for at least one ip family",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
			uc := newCreateUC(repo, opsRepo, tc.deps)
			req := baseCreateReq()
			tc.mut(req)
			_, err := uc.Execute(context.Background(), req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Equal(t, tc.msg, status.Convert(err).Message())
			require.Empty(t, repo.lbs) // Operation не создаётся, LB не появляется
		})
	}
}

// 8.1-18: dualstack INTERNAL, subnets of different networks → reject.
func TestCreate_Dualstack_DifferentNetworks(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		net := "net-1"
		if id == "sub-net2" {
			net = "net-2"
		}
		return &vpcclient.Subnet{ID: id, ProjectID: "prj-a", NetworkID: net, PlacementType: vpcclient.SubnetPlacementRegional}, nil
	}}
	rd := &fakeAddressReader{family: vpcclient.AddressFamilyIPv6, subnetID: "sub-net2"}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn, reader: rd})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet(lbTestSubnetRegional)
	req.V6Source = vipAddress("adr-6")

	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "dualstack load balancer families must resolve to the same network", status.Convert(err).Message())
}

// ---- Группа D — worker alloc / cross-service unavailable --------------------

// 8.1-20: подсеть исчерпана на аллокации → generic FAILED_PRECONDITION.
func TestCreate_Worker_SubnetExhausted(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	addr := &fakeAddressClient{allocFunc: func(_ context.Context, _ vpcclient.AllocateInternalIPRequest, _ string) (*vpcclient.AllocateResponse, error) {
		return nil, domain.ErrFailedPrecondition
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{addr: addr})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet(lbTestSubnetRegional)

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.FailedPrecondition), final.Error.GetCode())
	require.Equal(t, "could not allocate load balancer address", final.Error.GetMessage())
	require.Empty(t, repo.lbs)
}

// 8.1-21a: sync-precheck resolution недоступен (subnet) → sync UNAVAILABLE.
func TestCreate_Sync_SubnetUnavailable(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, _ string) (*vpcclient.Subnet, error) {
		return nil, domain.ErrUnavailable
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet(lbTestSubnetRegional)

	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Empty(t, repo.lbs)
}

// 8.1-21b: worker-alloc недоступен → Operation done UNAVAILABLE + compensation.
func TestCreate_Worker_AllocUnavailable(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	addr := &fakeAddressClient{allocFunc: func(_ context.Context, _ vpcclient.AllocateInternalIPRequest, _ string) (*vpcclient.AllocateResponse, error) {
		return nil, domain.ErrUnavailable
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{addr: addr})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet(lbTestSubnetRegional)

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.Unavailable), final.Error.GetCode())
	require.Empty(t, repo.lbs)
}

// 8.1-24 (nlb-side): worker link-CAS проигрыш (адрес уже занят) → Operation done
// FAILED_PRECONDITION "Illegal argument addressId" (generic, anti-oracle).
func TestCreate_Worker_LinkConflict(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	addr := &fakeAddressClient{byoFunc: func(_ context.Context, _ vpcclient.AttachExistingRequest) (*vpcclient.AllocateResponse, error) {
		return nil, domain.ErrFailedPrecondition // used_by CAS проигран
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{reader: &fakeAddressReader{}, addr: addr})
	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipAddress(lbTestAddrInternal)

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	final := awaitOpDone(t, opsRepo, op.ID)
	require.NotNil(t, final.Error)
	require.Equal(t, int32(codes.FailedPrecondition), final.Error.GetCode())
	require.Equal(t, "Illegal argument addressId", final.Error.GetMessage())
	require.Empty(t, repo.lbs)
}

// 8.1-36 (unit): duplicate name → sync ALREADY_EXISTS.
func TestCreate_DuplicateName(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	seedLB(t, repo, "prj-a", "lb-dup")
	uc := newCreateUC(repo, opsRepo, createDeps{})
	req := baseCreateReq()
	req.Name = "lb-dup"
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet(lbTestSubnetRegional)

	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "already exists in project")
}

// lbByName — находит LB-запись по имени в fakeRepo (для post-op assert'ов).
func lbByName(t *testing.T, repo *fakeRepo, name string) *domain.LoadBalancer {
	t.Helper()
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, rec := range repo.lbs {
		if string(rec.Name) == name {
			lb := rec.LoadBalancer
			return &lb
		}
	}
	t.Fatalf("load balancer %q not found", name)
	return nil
}
