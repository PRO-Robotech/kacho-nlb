// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// vipSourceKind — тип источника VIP одного семейства (VipSource oneof).
type vipSourceKind int

const (
	srcSubnetAuto  vipSourceKind = iota // subnet_id → auto-аллокация internal Address
	srcPublicAuto                       // public {} → платформенный public Address
	srcAddressLink                      // address_id → линк существующего Address
)

// familyVIPSpec — разобранный + резолвнутый per-family источник VIP.
type familyVIPSpec struct {
	family    domain.IPVersion
	kind      vipSourceKind
	subnetID  string // srcSubnetAuto — подсеть, из которой аллоцируется VIP
	addressID string // srcAddressLink — существующий Address
	// networkID — derived сеть семейства (INTERNAL): подсеть auto либо подсеть
	// linked-адреса. Пусто для EXTERNAL/public (сети нет). Используется для
	// dualstack same-network инварианта.
	networkID string
}

// origin — release-дискриминатор источника (auto owned / linked).
func (fs familyVIPSpec) origin() domain.VipOrigin {
	if fs.kind == srcAddressLink {
		return domain.VipOriginLinked
	}
	return domain.VipOriginAuto
}

// CreateLoadBalancerUseCase — async Create с sync-precheck матрицы source×type×
// placement (fail-fast ДО Operation) и per-family VIP fan-out в worker'е.
type CreateLoadBalancerUseCase struct {
	repo          Repo
	opsRepo       operations.Repo
	projectClient ProjectClient
	regionClient  RegionClient
	zoneClient    ZoneClient
	subnetClient  SubnetClient
	addressReader AddressClient         // public AddressService.Get — link-resolution
	addressClient InternalAddressClient // VIP alloc/link/release
	logger        *slog.Logger
}

// NewCreateLoadBalancerUseCase конструктор.
func NewCreateLoadBalancerUseCase(
	repo Repo, opsRepo operations.Repo,
	pc ProjectClient, rc RegionClient, zc ZoneClient,
	snc SubnetClient, ar AddressClient, ac InternalAddressClient,
	logger *slog.Logger,
) *CreateLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &CreateLoadBalancerUseCase{
		repo: repo, opsRepo: opsRepo,
		projectClient: pc, regionClient: rc, zoneClient: zc,
		subnetClient: snc, addressReader: ar, addressClient: ac,
		logger: logger,
	}
}

// Execute — sync-precheck (тип/placement/матрица источника/drain/резолв
// адресов+подсетей+сети) fail-fast ДО Operation; затем ops insert + worker.
func (u *CreateLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.CreateNetworkLoadBalancerRequest,
) (*operations.Operation, error) {
	if req.GetProjectId() == "" {
		return nil, errInvalidArg("project_id", "required")
	}
	if req.GetRegionId() == "" {
		return nil, errInvalidArg("region_id", "required")
	}

	lbType, err := lbTypeFromPb(req.GetType())
	if err != nil {
		return nil, err
	}

	// placement_type ↔ type coupling (§3.2).
	placement, err := resolvePlacement(lbType, req.GetPlacementType())
	if err != nil {
		return nil, err
	}

	// VipSource oneof → упорядоченный (v4, v6) набор; ≥1 семейство; malformed id sync.
	specs, err := resolveVipSources(req.GetV4Source(), req.GetV6Source())
	if err != nil {
		return nil, err
	}

	// source × type матрица (§3.3): subnet⟹INTERNAL, public⟹EXTERNAL.
	if err := validateSourceTypeMatrix(specs, lbType); err != nil {
		return nil, err
	}

	// Builder + Validate (multi-err).
	lb := domain.NewLoadBalancer(
		domain.ProjectID(req.GetProjectId()),
		domain.RegionID(req.GetRegionId()),
		domain.LbName(req.GetName()),
		domain.LbDescription(req.GetDescription()),
		domain.LabelsFromMap(req.GetLabels()),
		lbType,
	)
	lb.PlacementType = placement
	lb.DisabledAnnounceZones = normalizeZones(req.GetDisabledAnnounceZones())
	if req.GetDeletionProtection() {
		lb.DeletionProtection = true
	}
	sa, err := lbSessionAffinityFromPb(req.GetSessionAffinity())
	if err != nil {
		return nil, mapDomainErr(err)
	}
	lb.SessionAffinity = sa
	// ip_families — заявленные семейства VIP (проставляются ДО Insert-handle:
	// family-guard CHECK требует семейство в ip_families прежде чем persist-VIP
	// запишет непустой address).
	lb.IPFamilies = familiesFromSpecs(specs)
	if err := lb.Validate(); err != nil {
		return nil, mapDomainErr(err)
	}

	// disabled_announce_zones (§3.4): REGIONAL-only + зоны ∈ регион + не все зоны (geo).
	if err := u.validateDisabledAnnounceZones(ctx, lb); err != nil {
		return nil, err
	}

	// Резолв источников (§3.3): placement подсети/адреса == placement LB;
	// kind/family/ownership link'а; derived network + dualstack same-network.
	if err := u.resolveSources(ctx, lb, specs); err != nil {
		return nil, err
	}

	// Sync duplicate-name check (worker-Insert UNIQUE — атомарный backstop).
	if string(lb.Name) != "" {
		if err := u.assertNameUnique(ctx, string(lb.ProjectID), string(lb.Name)); err != nil {
			return nil, err
		}
	}

	// ---- Operation row ----
	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Create NetworkLoadBalancer %s", lb.Name),
		&lbv1.CreateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: string(lb.ID)},
	)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}

	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doCreate(workerCtx, lb, principal, specs)
	})

	return &op, nil
}

// resolvePlacement — placement_type ↔ type coupling. INTERNAL требует явный
// ZONAL|REGIONAL; EXTERNAL запрещает placement (§3.2, 8.1-12).
func resolvePlacement(lbType domain.LBType, pb lbv1.NetworkLoadBalancer_PlacementType) (domain.PlacementType, error) {
	set := pb != lbv1.NetworkLoadBalancer_PLACEMENT_TYPE_UNSPECIFIED
	if lbType == domain.LBTypeInternal {
		switch pb {
		case lbv1.NetworkLoadBalancer_ZONAL:
			return domain.PlacementZonal, nil
		case lbv1.NetworkLoadBalancer_REGIONAL:
			return domain.PlacementRegional, nil
		}
		return "", status.Error(codes.InvalidArgument,
			"placement_type is required for INTERNAL load balancer")
	}
	if set {
		return "", status.Error(codes.InvalidArgument,
			"placement_type is only valid for INTERNAL load balancer")
	}
	return domain.PlacementUnspecified, nil
}

// resolveVipSources — VipSource v4/v6 → упорядоченный набор familyVIPSpec. ≥1
// семейство обязательно; malformed subnet_id/address_id ловится синхронно.
func resolveVipSources(v4, v6 *lbv1.VipSource) ([]familyVIPSpec, error) {
	var out []familyVIPSpec
	add := func(family domain.IPVersion, src *lbv1.VipSource) error {
		if src == nil || src.GetSource() == nil {
			return nil
		}
		fs := familyVIPSpec{family: family}
		switch s := src.GetSource().(type) {
		case *lbv1.VipSource_SubnetId:
			if err := corevalidate.ResourceID("subnet", ids.PrefixSubnet, s.SubnetId); err != nil {
				return err
			}
			fs.kind = srcSubnetAuto
			fs.subnetID = s.SubnetId
		case *lbv1.VipSource_AddressId:
			if err := corevalidate.ResourceID("address", ids.PrefixAddress, s.AddressId); err != nil {
				return err
			}
			fs.kind = srcAddressLink
			fs.addressID = s.AddressId
		case *lbv1.VipSource_Public:
			fs.kind = srcPublicAuto
		default:
			return status.Errorf(codes.InvalidArgument,
				"%s_source has no vip source set", familyTag(family))
		}
		out = append(out, fs)
		return nil
	}
	if err := add(domain.IPVersionV4, v4); err != nil {
		return nil, err
	}
	if err := add(domain.IPVersionV6, v6); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"load balancer must declare a vip source for at least one ip family")
	}
	return out, nil
}

// validateSourceTypeMatrix — subnet_id ⟹ INTERNAL; public {} ⟹ EXTERNAL (§3.3).
// Несоответствие → каноничный field-текст (не generic — это форма запроса, не oracle).
func validateSourceTypeMatrix(specs []familyVIPSpec, lbType domain.LBType) error {
	for _, fs := range specs {
		switch fs.kind {
		case srcSubnetAuto:
			if lbType != domain.LBTypeInternal {
				return status.Error(codes.InvalidArgument,
					"subnet address source is only valid for INTERNAL load balancer")
			}
		case srcPublicAuto:
			if lbType != domain.LBTypeExternal {
				return status.Error(codes.InvalidArgument,
					"public address source is only valid for EXTERNAL load balancer")
			}
		}
	}
	return nil
}

// validateDisabledAnnounceZones — REGIONAL-only + зоны ∈ регион + не все зоны.
func (u *CreateLoadBalancerUseCase) validateDisabledAnnounceZones(ctx context.Context, lb domain.LoadBalancer) error {
	return checkDisabledAnnounceZones(ctx, u.zoneClient, lb.PlacementType, string(lb.RegionID), lb.DisabledAnnounceZones)
}

// checkDisabledAnnounceZones — общая валидация drain-набора (Create + Update):
// REGIONAL-only; каждая зона ∈ регион; набор не покрывает все зоны региона. nil
// zoneClient (dev-стенд без geo) → пропуск geo-проверок (REGIONAL-only остаётся).
func checkDisabledAnnounceZones(ctx context.Context, zc ZoneClient, placement domain.PlacementType, regionID string, zones []string) error {
	if len(zones) == 0 {
		return nil
	}
	if placement != domain.PlacementRegional {
		return status.Error(codes.InvalidArgument,
			"disabled_announce_zones is only valid for REGIONAL load balancer")
	}
	if zc == nil {
		return nil
	}
	regionZones, err := zc.ListZoneIDsInRegion(ctx, regionID)
	if err != nil {
		return zonePeerErr(err)
	}
	inRegion := make(map[string]struct{}, len(regionZones))
	for _, z := range regionZones {
		inRegion[z] = struct{}{}
	}
	drained := make(map[string]struct{}, len(zones))
	for _, z := range zones {
		if _, ok := inRegion[z]; !ok {
			return status.Errorf(codes.InvalidArgument,
				"zone %s is not in region %s", z, regionID)
		}
		drained[z] = struct{}{}
	}
	// Набор не должен покрывать ВСЕ зоны региона (VIP стал бы недостижим).
	if len(regionZones) > 0 && len(drained) >= len(inRegion) {
		return status.Error(codes.InvalidArgument,
			"disabled_announce_zones must not cover all zones of the region")
	}
	return nil
}

// resolveSources — резолв каждого источника через peer-API (§3.3): placement
// подсети/адреса == placement LB; link kind/family/ownership; derived network +
// dualstack same-network. Заполняет specs[i].networkID (INTERNAL).
func (u *CreateLoadBalancerUseCase) resolveSources(ctx context.Context, lb domain.LoadBalancer, specs []familyVIPSpec) error {
	for i := range specs {
		if err := u.resolveOneSource(ctx, lb, &specs[i]); err != nil {
			return err
		}
	}
	// dualstack same-network (INTERNAL): derived network семейств должен совпасть.
	var net string
	for _, fs := range specs {
		if fs.networkID == "" {
			continue
		}
		if net == "" {
			net = fs.networkID
			continue
		}
		if fs.networkID != net {
			return status.Error(codes.InvalidArgument,
				"dualstack load balancer families must resolve to the same network")
		}
	}
	return nil
}

// resolveOneSource — резолв одного семейства.
func (u *CreateLoadBalancerUseCase) resolveOneSource(ctx context.Context, lb domain.LoadBalancer, fs *familyVIPSpec) error {
	switch fs.kind {
	case srcSubnetAuto:
		if u.subnetClient == nil {
			return nil
		}
		sn, err := u.subnetClient.Get(ctx, fs.subnetID)
		if err != nil {
			return subnetPeerErr(err, fs.subnetID)
		}
		if !subnetPlacementMatches(sn.PlacementType, lb.PlacementType) {
			return status.Error(codes.InvalidArgument,
				"subnet placement does not match load balancer placement")
		}
		fs.networkID = sn.NetworkID
		return nil
	case srcPublicAuto:
		return nil // EXTERNAL public — сети нет; underlying-зона деривится в worker'е
	case srcAddressLink:
		return u.resolveLinkedAddress(ctx, lb, fs)
	}
	return nil
}

// resolveLinkedAddress — sync-precheck link'а: kind/family/ownership/placement
// через public AddressService.Get под tenant-identity. Анти-oracle: любой
// mismatch/no-access → generic InvalidArgument "Illegal argument addressId".
func (u *CreateLoadBalancerUseCase) resolveLinkedAddress(ctx context.Context, lb domain.LoadBalancer, fs *familyVIPSpec) error {
	if u.addressReader == nil {
		return nil
	}
	addr, err := u.addressReader.Get(ctx, fs.addressID)
	if err != nil {
		return linkedAddressErr(err)
	}
	// ownership + family + kind↔type.
	internalWanted := lb.Type == domain.LBTypeInternal
	if addr.ProjectID != string(lb.ProjectID) ||
		addr.Family != string(fs.family) ||
		addr.External == internalWanted {
		return status.Error(codes.InvalidArgument, "Illegal argument addressId")
	}
	// INTERNAL: placement подсети адреса == placement LB (derived network).
	if internalWanted {
		if u.subnetClient == nil {
			return nil
		}
		sn, err := u.subnetClient.Get(ctx, addr.SubnetID)
		if err != nil {
			// подсеть адреса не резолвится — не подтверждаем детали (generic),
			// vpc недоступен → Unavailable (fail-closed).
			if errors.Is(err, domain.ErrUnavailable) {
				return status.Error(codes.Unavailable, "subnet lookup unavailable")
			}
			return status.Error(codes.InvalidArgument, "Illegal argument addressId")
		}
		if !subnetPlacementMatches(sn.PlacementType, lb.PlacementType) {
			return status.Error(codes.InvalidArgument, "Illegal argument addressId")
		}
		fs.networkID = sn.NetworkID
	}
	return nil
}

// subnetPlacementMatches — placement подсети (vpc) == placement LB (domain).
func subnetPlacementMatches(subnetPlacement string, lbPlacement domain.PlacementType) bool {
	switch lbPlacement {
	case domain.PlacementRegional:
		return subnetPlacement == vpcclient.SubnetPlacementRegional
	case domain.PlacementZonal:
		return subnetPlacement != vpcclient.SubnetPlacementRegional && subnetPlacement != ""
	}
	return false
}

// doCreate — async worker: durable-handle сага с per-family VIP fan-out.
func (u *CreateLoadBalancerUseCase) doCreate(
	ctx context.Context, lb domain.LoadBalancer, principal operations.Principal, specs []familyVIPSpec,
) (*anypb.Any, error) {
	if u.projectClient != nil {
		if _, err := u.projectClient.Get(ctx, string(lb.ProjectID)); err != nil {
			return nil, peerErrToStatus(err, "project", string(lb.ProjectID))
		}
	}
	if u.regionClient != nil {
		if _, err := u.regionClient.Get(ctx, string(lb.RegionID)); err != nil {
			return nil, peerErrToStatus(err, "region", string(lb.RegionID))
		}
	}

	lb.Status = domain.LBStatusCreating
	if err := u.insertHandle(ctx, &lb); err != nil {
		return nil, err
	}

	finalized := false
	allocated := map[domain.IPVersion]vipAllocResult{}
	defer func() {
		if finalized {
			return
		}
		u.compensateCreate(ctx, string(lb.ID), allocated)
	}()

	for _, fs := range specs {
		alloc, err := u.acquireFamilyVIP(ctx, lb, fs)
		if err != nil {
			return nil, err
		}
		allocated[fs.family] = alloc
		if _, err := u.persistVIP(ctx, string(lb.ID), fs.family, alloc.address, alloc.addressID, alloc.origin); err != nil {
			return nil, mapDomainErr(err)
		}
	}

	created, err := u.finalizeCreate(ctx, lb, principal)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	finalized = true

	pb, err := lbRecordToProto(created)
	if err != nil {
		return nil, err
	}
	out, err := anypb.New(pb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	return out, nil
}

// vipAllocResult — outcome acquire-ветки одного семейства.
type vipAllocResult struct {
	addressID string
	address   string
	origin    domain.VipOrigin // auto → two-step release; linked → ClearReference
}

// acquireFamilyVIP — внешний side-effect одного семейства: auto-аллокация
// (subnet internal / platform public) либо link существующего Address. Анти-oracle:
// alloc-провал (ёмкость) → generic FAILED_PRECONDITION; link-конфликт → generic
// `Illegal argument addressId`; vpc недоступен → Unavailable.
func (u *CreateLoadBalancerUseCase) acquireFamilyVIP(
	ctx context.Context, lb domain.LoadBalancer, fs familyVIPSpec,
) (vipAllocResult, error) {
	if u.addressClient == nil {
		return vipAllocResult{}, status.Error(codes.Unavailable, "vpc internal address client not configured")
	}
	owner := lbAddressOwner(string(lb.ID))
	switch fs.kind {
	case srcAddressLink:
		resp, err := u.addressClient.AttachExisting(ctx, vpcclient.AttachExistingRequest{
			AddressID: fs.addressID,
			Owner:     owner,
			Owned:     false,
		})
		if err != nil {
			return vipAllocResult{}, linkAcquireErr(err)
		}
		return vipAllocResult{addressID: resp.AddressID, address: resp.Value, origin: domain.VipOriginLinked}, nil
	case srcPublicAuto:
		zone, err := u.deriveUnderlayZone(ctx, string(lb.RegionID))
		if err != nil {
			return vipAllocResult{}, err
		}
		req := vpcclient.AllocateExternalIPRequest{
			ProjectID: string(lb.ProjectID),
			Name:      domain.LBAnycastAddressName(lb.ID, fs.family),
			ZoneID:    zone,
			Owner:     owner,
		}
		var (
			resp *vpcclient.AllocateResponse
			err2 error
		)
		if fs.family == domain.IPVersionV6 {
			resp, err2 = u.addressClient.AllocateExternalIPv6(ctx, req)
		} else {
			resp, err2 = u.addressClient.AllocateExternalIP(ctx, req)
		}
		if err2 != nil {
			return vipAllocResult{}, allocAcquireErr(err2)
		}
		return vipAllocResult{addressID: resp.AddressID, address: resp.Value, origin: domain.VipOriginAuto}, nil
	default: // srcSubnetAuto
		req := vpcclient.AllocateInternalIPRequest{
			ProjectID: string(lb.ProjectID),
			Name:      domain.LBAnycastAddressName(lb.ID, fs.family),
			SubnetID:  fs.subnetID,
			Owner:     owner,
		}
		var (
			resp *vpcclient.AllocateResponse
			err  error
		)
		if fs.family == domain.IPVersionV6 {
			resp, err = u.addressClient.AllocateInternalIPv6(ctx, req)
		} else {
			resp, err = u.addressClient.AllocateInternalIP(ctx, req)
		}
		if err != nil {
			return vipAllocResult{}, allocAcquireErr(err)
		}
		return vipAllocResult{addressID: resp.AddressID, address: resp.Value, origin: domain.VipOriginAuto}, nil
	}
}

// deriveUnderlayZone — детерминированная underlying-зона public-VIP из региона
// (первая по сортировке). Скрыта от публичной поверхности (placement-leak).
func (u *CreateLoadBalancerUseCase) deriveUnderlayZone(ctx context.Context, regionID string) (string, error) {
	if u.zoneClient == nil {
		return "", status.Error(codes.Unavailable, "zone lookup unavailable")
	}
	zones, err := u.zoneClient.ListZoneIDsInRegion(ctx, regionID)
	if err != nil {
		return "", zonePeerErr(err)
	}
	if len(zones) == 0 {
		return "", status.Error(codes.FailedPrecondition, "could not allocate load balancer address")
	}
	sort.Strings(zones)
	return zones[0], nil
}

// allocAcquireErr — анти-oracle маппинг ошибок auto-аллокации (ёмкость/недоступность).
func allocAcquireErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, "load balancer address allocation unavailable")
	}
	// ErrFailedPrecondition (пул/подсеть исчерпаны) и прочее → generic (без ёмкости).
	return status.Error(codes.FailedPrecondition, "could not allocate load balancer address")
}

// linkAcquireErr — анти-oracle маппинг ошибок link-CAS (адрес занят → generic).
func linkAcquireErr(err error) error {
	if errors.Is(err, domain.ErrUnavailable) {
		return status.Error(codes.Unavailable, "address lookup unavailable")
	}
	return status.Error(codes.FailedPrecondition, "Illegal argument addressId")
}

// validateDisabledAnnounceZones/resolveSources используют этот маппер зон.
func zonePeerErr(err error) error {
	if errors.Is(err, domain.ErrUnavailable) {
		return status.Error(codes.Unavailable, "zone lookup unavailable")
	}
	return status.Error(codes.InvalidArgument, "Illegal argument disabledAnnounceZones")
}

// insertHandle — TX-1: INSERT durable-handle строки LB (status='CREATING').
// UNIQUE (project_id,name) 23505 → каноничный ALREADY_EXISTS-текст (name-dup).
func (u *CreateLoadBalancerUseCase) insertHandle(ctx context.Context, lb *domain.LoadBalancer) error {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return mapDomainErr(err)
	}
	committed := false
	defer func() {
		if !committed {
			w.Abort()
		}
	}()
	if _, err := w.LoadBalancers().Insert(ctx, lb); err != nil {
		if errors.Is(err, kachorepo.ErrAlreadyExists) || errors.Is(err, domain.ErrAlreadyExists) {
			return status.Errorf(codes.AlreadyExists,
				"NetworkLoadBalancer with name %s already exists in project", lb.Name)
		}
		return mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return mapDomainErr(err)
	}
	committed = true
	return nil
}

// persistVIP — отдельный commit CAS-attach VIP одного семейства в CREATING-handle.
func (u *CreateLoadBalancerUseCase) persistVIP(
	ctx context.Context, id string, family domain.IPVersion, address, addressID string, origin domain.VipOrigin,
) (*kachorepo.LoadBalancerRecord, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			w.Abort()
		}
	}()
	rec, err := w.LoadBalancers().AttachVIP(ctx, id, family, address, addressID, origin)
	if err != nil {
		return nil, err
	}
	if err := w.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return rec, nil
}

// finalizeCreate — финальный commit: CAS CREATING→INACTIVE + outbox CREATED +
// FGA-register-intent (project-hierarchy + creator) в одной writer-TX.
func (u *CreateLoadBalancerUseCase) finalizeCreate(
	ctx context.Context, lb domain.LoadBalancer, principal operations.Principal,
) (*kachorepo.LoadBalancerRecord, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			w.Abort()
		}
	}()
	created, err := w.LoadBalancers().SetStatusCAS(ctx, string(lb.ID),
		domain.LBStatusCreating, domain.LBStatusInactive)
	if err != nil {
		return nil, err
	}
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, string(created.ID), string(created.ProjectID),
		kachopg.OutboxActionCreated, lbOutboxPayload(created),
	); err != nil {
		return nil, err
	}
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
		lbRegisterIntent(created, principal)); err != nil {
		return nil, err
	}
	if err := w.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return created, nil
}

// compensateCreate — best-effort откат до finalize: освобождает каждый
// аллоцированный VIP по origin (auto → two-step ClearReference→FreeIP, linked →
// ClearReference) и удаляет handle. Ошибки логируются; краш раньше — free_ip_runner.
func (u *CreateLoadBalancerUseCase) compensateCreate(ctx context.Context, lbID string, allocated map[domain.IPVersion]vipAllocResult) {
	logger := u.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("load_balancer_id", lbID)

	owner := lbAddressOwner(lbID)
	if u.addressClient != nil {
		for family, alloc := range allocated {
			if alloc.addressID == "" {
				continue
			}
			if rerr := u.releaseAddress(ctx, alloc.addressID, owner, alloc.origin); rerr != nil {
				logger.Warn("LoadBalancer.Create compensation release failed",
					"err", rerr, "address_id", alloc.addressID, "family", string(family))
			}
		}
	}
	if err := u.deleteHandle(ctx, lbID); err != nil {
		logger.Warn("LoadBalancer.Create compensation delete handle failed; free_ip_runner will reconcile", "err", err)
	}
}

// releaseAddress — release одного Address по origin (§3.9): owned (auto) →
// two-step owner-scoped ClearReference → FreeIP (иначе FreeIP==Delete упрётся в
// собственный guard); linked → ClearReference без Delete. Идемпотентно.
func (u *CreateLoadBalancerUseCase) releaseAddress(ctx context.Context, addressID string, owner vpcclient.AddressOwner, origin domain.VipOrigin) error {
	if u.addressClient == nil {
		return status.Error(codes.Unavailable, "vpc internal address client not configured")
	}
	if origin == domain.VipOriginLinked {
		return u.addressClient.ClearReference(ctx, addressID, owner)
	}
	// owned (auto): снять собственный owned-референс, затем удалить адрес.
	if err := u.addressClient.ClearReference(ctx, addressID, owner); err != nil {
		return err
	}
	return u.addressClient.FreeIP(ctx, addressID, owner)
}

// deleteHandle — best-effort DELETE durable-handle строки LB в собственной TX.
func (u *CreateLoadBalancerUseCase) deleteHandle(ctx context.Context, id string) error {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			w.Abort()
		}
	}()
	if err := w.LoadBalancers().Delete(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := w.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// familiesFromSpecs — список заявленных семейств в порядке fan-out.
func familiesFromSpecs(specs []familyVIPSpec) []domain.IPVersion {
	out := make([]domain.IPVersion, 0, len(specs))
	for _, fs := range specs {
		out = append(out, fs.family)
	}
	return out
}

// normalizeZones — dedup + стабильный порядок набора зон (для DB-записи и Equal).
func normalizeZones(zones []string) []string {
	if len(zones) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(zones))
	out := make([]string, 0, len(zones))
	for _, z := range zones {
		if z == "" {
			continue
		}
		if _, ok := seen[z]; ok {
			continue
		}
		seen[z] = struct{}{}
		out = append(out, z)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// familyTag — короткий тег семейства ("v4"/"v6").
func familyTag(family domain.IPVersion) string {
	if family == domain.IPVersionV6 {
		return "v6"
	}
	return "v4"
}

// lbAddressOwner — owner-tuple для vpc.Address referrer ("network_load_balancer:<id>").
func lbAddressOwner(lbID string) vpcclient.AddressOwner {
	return vpcclient.AddressOwner{Kind: lbAddressOwnerKind, ID: lbID}
}

// lbAddressOwnerKind — Reference.type для NLB LoadBalancer в vpc.Address referrer.
const lbAddressOwnerKind = "network_load_balancer"

// assertNameUnique — sync precheck дубликата (project_id, name).
func (u *CreateLoadBalancerUseCase) assertNameUnique(ctx context.Context, projectID, name string) error {
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	existing, _, err := rd.LoadBalancers().List(ctx,
		kachorepo.LoadBalancerFilter{ProjectID: projectID, Name: name},
		kachorepo.Pagination{},
	)
	if err != nil {
		return mapDomainErr(err)
	}
	if len(existing) > 0 {
		return status.Errorf(codes.AlreadyExists,
			"NetworkLoadBalancer with name %s already exists in project", name)
	}
	return nil
}

// lbRegisterIntent — FGA-register-intent свежесозданного LB (project-hierarchy +
// creator-tuple, если principal — аутентифицированный пользователь).
func lbRegisterIntent(lb *kachorepo.LoadBalancerRecord, principal operations.Principal) domain.FGARegisterIntent {
	id := string(lb.ID)
	tuples := []domain.FGATuple{
		domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, id, string(lb.ProjectID)),
	}
	if subject := domain.FGASubjectFromPrincipal(principal.Type, principal.ID); subject != "" {
		tuples = append(tuples, domain.FGACreatorTuple(subject, domain.FGAObjectTypeLoadBalancer, id))
	}
	return domain.FGARegisterIntent{
		Kind:            "NetworkLoadBalancer",
		ResourceID:      id,
		Tuples:          tuples,
		Labels:          domain.LabelsToMap(lb.Labels),
		ParentProjectID: string(lb.ProjectID),
	}
}

// lbMirrorIntent — mirror-feed register-intent для UPDATED LB (project-hierarchy
// re-register с обновлёнными labels; без creator-tuple).
func lbMirrorIntent(lb *kachorepo.LoadBalancerRecord) domain.FGARegisterIntent {
	id := string(lb.ID)
	return domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: id,
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, id, string(lb.ProjectID)),
		},
		Labels:          domain.LabelsToMap(lb.Labels),
		ParentProjectID: string(lb.ProjectID),
	}
}

// ---- Helpers ----

// lbTypeFromPb — proto enum → domain.LBType. UNSPECIFIED → InvalidArgument.
func lbTypeFromPb(t lbv1.NetworkLoadBalancer_Type) (domain.LBType, error) {
	switch t {
	case lbv1.NetworkLoadBalancer_EXTERNAL:
		return domain.LBTypeExternal, nil
	case lbv1.NetworkLoadBalancer_INTERNAL:
		return domain.LBTypeInternal, nil
	}
	return "", errInvalidArg("type", "type must be one of: EXTERNAL, INTERNAL")
}

// domainSessionAffinity — proto enum → domain.SessionAffinity.
func domainSessionAffinity(a lbv1.NetworkLoadBalancer_SessionAffinity) domain.SessionAffinity {
	switch a {
	case lbv1.NetworkLoadBalancer_SESSION_AFFINITY_UNSPECIFIED, lbv1.NetworkLoadBalancer_FIVE_TUPLE:
		return domain.SessionAffinity5Tuple
	case lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY:
		return domain.SessionAffinityClientIPOnly
	}
	return domain.SessionAffinity(a.String())
}

// lbSessionAffinityFromPb — fail-fast вариант: каноничная InvalidArgument на out-of-domain.
func lbSessionAffinityFromPb(a lbv1.NetworkLoadBalancer_SessionAffinity) (domain.SessionAffinity, error) {
	sa := domainSessionAffinity(a)
	if err := sa.Validate(); err != nil {
		return "", err
	}
	return sa, nil
}

// peerErrToStatus — маппинг ошибок peer-client (project/region) в gRPC-status.
func peerErrToStatus(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return status.Errorf(codes.InvalidArgument, "%s %s not found", caser(kind), id)
	case errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "%s: %v", kind, err)
	case errors.Is(err, domain.ErrFailedPrecondition):
		return status.Errorf(codes.FailedPrecondition, "%s %s: %v", kind, id, err)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable, "%s lookup unavailable", kind)
	}
	return status.Errorf(codes.Internal, "%s lookup failed", kind)
}

// subnetPeerErr — sync-precheck subnet_id через vpc.SubnetService.Get.
func subnetPeerErr(err error, id string) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "subnet %s not found", id)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable, "subnet lookup unavailable")
	}
	return status.Errorf(codes.Internal, "subnet lookup failed")
}

// linkedAddressErr — анти-oracle маппинг AddressService.Get в link-precheck.
func linkedAddressErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArg):
		return status.Error(codes.InvalidArgument, "Illegal argument addressId")
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, "address lookup unavailable")
	}
	return status.Error(codes.Internal, "address lookup failed")
}

// caser — Title-case 1-char для kind ("project" → "Project").
func caser(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 32
	}
	return string(b)
}

// lbOutboxPayload — JSON-payload для outbox. Минимальный snapshot.
func lbOutboxPayload(lb *kachorepo.LoadBalancerRecord) map[string]any {
	if lb == nil {
		return nil
	}
	return map[string]any{
		"id":         string(lb.ID),
		"project_id": string(lb.ProjectID),
		"region_id":  string(lb.RegionID),
		"name":       string(lb.Name),
		"status":     string(lb.Status),
		"type":       string(lb.Type),
	}
}
