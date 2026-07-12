// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

// RED-фаза (строгий TDD, ban #12) под APPROVED acceptance-док
// docs/specs/sub-phase-nlb-vpc-zone-coherence-acceptance.md — трек B,
// GAP-1 (ZONAL dualstack same-zone) + GAP-2 (region-coherence VIP↔LB).
//
// Эти тесты фиксируют placement-coherence инвариант (data-integrity.md
// §Placement-coherence): ZONAL LB — обе VIP-семьи в ОДНОЙ зоне; INTERNAL LB —
// subnet/address-source из региона lb.region_id. Behaviour-level: assert точную
// строку ошибки, не только gRPC-код.
//
// Уровень — unit use-case с fake peer-клиентами (subnetClient/addressReader),
// санкционировано acceptance DoD ("unit use-case (fake SubnetClient/…)"). Зона и
// регион подсети приходят из проекции vpcclient.Subnet (ZoneID/RegionID) — их
// заполняет SubnetClient-adapter (для ZONAL — zone→region резолвом через geo,
// см. план Stage 1 GAP-2); в unit-тесте это моделирует getFunc.
//
// RED-состояние (до фикса): resolveSources сверяет только same-network
// (create.go:180-194), а resolveOneSource — только placement TYPE
// (create.go:209-212). Ни зона (dualstack), ни регион подсети не сверяются →
// разнозональный / чужерегиональный Create проходит. GREEN-фиксы делает
// rpc-implementer в том же PR; тесты падают до фикса по нужной причине.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// wantSameZoneMsg — verbatim из acceptance ZC-NLB-ZONE-01/05 (Q1-дефолт, зеркалит
// sibling create.go:191-192 "...same network"). GAP-1 контракт-текст.
const wantSameZoneMsg = "dualstack load balancer families must resolve to the same zone"

// wantRegionMismatchMsg — verbatim из acceptance ZC-NLB-REGION-01/02 (Then-clause).
//
// FLAG(reconcile): acceptance-док даёт ИМЕННО эту строку; краткая формулировка
// задачи трека B ("load balancer VIP must be in the same region") — её пересказ.
// Источник истины для RED→GREEN — acceptance-док (его же читает rpc-implementer),
// поэтому здесь verbatim из дока. Если ревью зафиксирует короткую форму — правится
// ЭТА одна константа. Разночтение вынесено в отчёт для сверки с acceptance-author.
const wantRegionMismatchMsg = "load balancer vip subnet must be in the same region as the load balancer"

// zonalSubnet — helper: проекция ZONAL-подсети с заданными зоной/регионом/сетью.
func zonalSubnet(id, zone, region, network string) *vpcclient.Subnet {
	return &vpcclient.Subnet{
		ID: id, ProjectID: "prj-a", NetworkID: network,
		ZoneID: zone, RegionID: region, PlacementType: "ZONAL",
	}
}

// regionalSubnet — helper: проекция REGIONAL-подсети (anycast, zone_id='').
func regionalSubnet(id, region, network string) *vpcclient.Subnet {
	return &vpcclient.Subnet{
		ID: id, ProjectID: "prj-a", NetworkID: network,
		ZoneID: "", RegionID: region, PlacementType: vpcclient.SubnetPlacementRegional,
	}
}

// ---- GAP-1 — ZONAL dualstack same-zone -------------------------------------

// TestCreate_ZCNLBZONE01_DualstackCrossZone_Rejected — ZC-NLB-ZONE-01:
// ZONAL LB, v4-subnet зоны R1-a + v6-subnet зоны R1-b (обе регион R1, одна сеть)
// → sync INVALID_ARGUMENT "…same zone". RED: сейчас проверяется только same-network.
func TestCreate_ZCNLBZONE01_DualstackCrossZone_Rejected(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		if id == "sub-v6b" {
			return zonalSubnet(id, "region-1-b", "region-1", "net-1"), nil
		}
		return zonalSubnet(id, "region-1-a", "region-1", "net-1"), nil
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn})

	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
	req.V4Source = vipSubnet("sub-v4a") // зона R1-a
	req.V6Source = vipSubnet("sub-v6b") // зона R1-b

	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"cross-zone ZONAL dualstack must be rejected synchronously")
	require.Equal(t, wantSameZoneMsg, status.Convert(err).Message())
	require.Empty(t, repo.lbs, "Operation must not be created; no LB row")
}

// TestCreate_ZCNLBZONE05_MixedSourceCrossZone_Rejected — ZC-NLB-ZONE-05:
// ZONAL LB, v4 subnet-auto (зона R1-a) + v6 linked-address чья подсеть в зоне R1-b
// → sync INVALID_ARGUMENT "…same zone". Зона семейства берётся из резолвнутой
// подсети независимо от вида source. RED: зона linked-адреса не сверяется.
func TestCreate_ZCNLBZONE05_MixedSourceCrossZone_Rejected(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		if id == "sub-addr6" { // подсеть linked-адреса — зона R1-b
			return zonalSubnet(id, "region-1-b", "region-1", "net-1"), nil
		}
		return zonalSubnet(id, "region-1-a", "region-1", "net-1"), nil // v4 subnet — зона R1-a
	}}
	// v6 internal-адрес prj-a, привязан к подсети "sub-addr6" (зона R1-b).
	rd := &fakeAddressReader{family: vpcclient.AddressFamilyIPv6, subnetID: "sub-addr6"}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn, reader: rd})

	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
	req.V4Source = vipSubnet("sub-v4a")   // зона R1-a
	req.V6Source = vipAddress("adr-mix6") // адрес → подсеть зоны R1-b

	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"cross-zone mixed source (subnet + linked address) must be rejected")
	require.Equal(t, wantSameZoneMsg, status.Convert(err).Message())
	require.Empty(t, repo.lbs)
}

// TestCreate_ZCNLBZONE02_DualstackSameZone_OK — ZC-NLB-ZONE-02 (regression-lock):
// обе VIP из ОДНОЙ зоны R1-a → создаётся. Гарантирует, что same-zone-фикс не
// over-reject'ит валидный same-zone dualstack.
func TestCreate_ZCNLBZONE02_DualstackSameZone_OK(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		return zonalSubnet(id, "region-1-a", "region-1", "net-1"), nil
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn})

	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
	req.V4Source = vipSubnet("sub-v4")
	req.V6Source = vipSubnet("sub-v6")

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Equal(t, domain.PlacementZonal, lbByName(t, repo, "lb-1").PlacementType)
}

// TestCreate_ZCNLBZONE03_SingleFamily_OK — ZC-NLB-ZONE-03 (regression-lock):
// одно семейство (только v4) → same-zone-проверка не применяется → создаётся.
func TestCreate_ZCNLBZONE03_SingleFamily_OK(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		return zonalSubnet(id, "region-1-a", "region-1", "net-1"), nil
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn})

	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
	req.V4Source = vipSubnet("sub-v4")

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
}

// TestCreate_ZCNLBZONE04_RegionalDualstackExempt_OK — ZC-NLB-ZONE-04
// (regression-lock, anycast-исключение): REGIONAL dualstack, обе подсети zone_id=''
// → same-zone-проверка ИСКЛЮЧЕНА by construction → создаётся.
func TestCreate_ZCNLBZONE04_RegionalDualstackExempt_OK(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		return regionalSubnet(id, "region-1", "net-1"), nil
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn})

	req := baseCreateReq()
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet("sub-r4")
	req.V6Source = vipSubnet("sub-r6")

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Equal(t, domain.PlacementRegional, lbByName(t, repo, "lb-1").PlacementType)
}

// ---- GAP-2 — region-coherence VIP↔LoadBalancer -----------------------------

// TestCreate_ZCNLBREGION01_ZonalSubnetWrongRegion_Rejected — ZC-NLB-REGION-01:
// INTERNAL ZONAL LB (region R1), ZONAL subnet-source зоны R2-a (регион R2)
// → sync INVALID_ARGUMENT region-mismatch. RED: регион подсети не сверяется.
func TestCreate_ZCNLBREGION01_ZonalSubnetWrongRegion_Rejected(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	// ZONAL-подсеть зоны R2-a → регион R2 (adapter резолвит zone→region в RegionID).
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		return zonalSubnet(id, "region-2-a", "region-2", "net-1"), nil
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn})

	req := baseCreateReq() // region-1
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
	req.V4Source = vipSubnet("sub-r2")

	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"ZONAL subnet in region R2 must be rejected for R1 load balancer")
	require.Equal(t, wantRegionMismatchMsg, status.Convert(err).Message())
	require.Empty(t, repo.lbs)
}

// TestCreate_ZCNLBREGION02_RegionalSubnetWrongRegion_Rejected — ZC-NLB-REGION-02:
// INTERNAL REGIONAL LB (region R1), REGIONAL subnet-source региона R2
// → sync INVALID_ARGUMENT тот же region-mismatch текст. RED: регион не сверяется.
func TestCreate_ZCNLBREGION02_RegionalSubnetWrongRegion_Rejected(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		return regionalSubnet(id, "region-2", "net-1"), nil
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn})

	req := baseCreateReq() // region-1
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipSubnet("sub-rr2")

	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"REGIONAL subnet in region R2 must be rejected for R1 load balancer")
	require.Equal(t, wantRegionMismatchMsg, status.Convert(err).Message())
	require.Empty(t, repo.lbs)
}

// TestCreate_ZCNLBREGION03_SameRegion_OK — ZC-NLB-REGION-03 (regression-lock):
// ZONAL subnet зоны R1-a (регион R1) при lb.region=R1 → создаётся. Гарантирует,
// что region-фикс не over-reject'ит валидный same-region source.
func TestCreate_ZCNLBREGION03_SameRegion_OK(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		return zonalSubnet(id, "region-1-a", "region-1", "net-1"), nil
	}}
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn})

	req := baseCreateReq() // region-1
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_ZONAL
	req.V4Source = vipSubnet("sub-ok")

	op, err := uc.Execute(context.Background(), req)
	require.NoError(t, err)
	require.Nil(t, awaitOpDone(t, opsRepo, op.ID).Error)
	require.Equal(t, domain.RegionID("region-1"), lbByName(t, repo, "lb-1").RegionID)
}

// TestCreate_ZCNLBREGION04_LinkedAddressWrongRegion_Rejected — ZC-NLB-REGION-04
// (anti-oracle): INTERNAL LB (region R1), linked internal Address чья подсеть в
// регионе R2 → sync INVALID_ARGUMENT с GENERIC "Illegal argument addressId"
// (link-путь не подтверждает детали чужого адреса). RED: регион не сверяется.
func TestCreate_ZCNLBREGION04_LinkedAddressWrongRegion_Rejected(t *testing.T) {
	repo, opsRepo := newFakeRepo(), newFakeOpsRepo()
	// Подсеть linked-адреса — регион R2 (REGIONAL, чужой регион).
	sn := &fakeSubnetClient{getFunc: func(_ context.Context, id string) (*vpcclient.Subnet, error) {
		return regionalSubnet(id, "region-2", "net-1"), nil
	}}
	rd := &fakeAddressReader{subnetID: "sub-addr-r2"} // internal v4 prj-a
	uc := newCreateUC(repo, opsRepo, createDeps{subnet: sn, reader: rd})

	req := baseCreateReq() // region-1
	req.Type = lbv1.NetworkLoadBalancer_INTERNAL
	req.PlacementType = lbv1.NetworkLoadBalancer_REGIONAL
	req.V4Source = vipAddress("adr-r2")

	_, err := uc.Execute(context.Background(), req)
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"linked address from region R2 must be rejected for R1 load balancer")
	require.Equal(t, "Illegal argument addressId", status.Convert(err).Message(),
		"linked-address region mismatch stays generic (anti-oracle), not descriptive")
	require.Empty(t, repo.lbs)
}
