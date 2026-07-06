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
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// CreateLoadBalancerUseCase — async Create с sync-precheck матрицы source×type×
// placement (fail-fast ДО Operation) и per-family VIP fan-out в worker'е.
//
// Файл сфокусирован на оркестрации саги allocate→persist→finalize→compensate.
// Смежные концерны вынесены в соседние файлы пакета:
//   - vip_source.go   — parse VipSource oneof → familyVIPSpec + матрица source×type.
//   - enum_mapping.go — proto enum ↔ domain (type/session-affinity).
//   - payloads.go     — outbox/FGA-intent/owner-tuple builders.
//   - peer_errors.go  — анти-oracle маппинг peer-ошибок в gRPC-status.
//   - zones.go        — валидация disabled_announce_zones + normalize.
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

	// placement_type ↔ type coupling.
	placement, err := resolvePlacement(lbType, req.GetPlacementType())
	if err != nil {
		return nil, err
	}

	// VipSource oneof → упорядоченный (v4, v6) набор; ≥1 семейство; malformed id sync.
	specs, err := resolveVipSources(req.GetV4Source(), req.GetV6Source())
	if err != nil {
		return nil, err
	}

	// source × type матрица: subnet⟹INTERNAL, public⟹EXTERNAL.
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

	// disabled_announce_zones: REGIONAL-only + зоны ∈ регион + не все зоны (geo).
	if err := u.validateDisabledAnnounceZones(ctx, lb); err != nil {
		return nil, err
	}

	// Резолв источников: placement подсети/адреса == placement LB;
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

// validateDisabledAnnounceZones — REGIONAL-only + зоны ∈ регион + не все зоны.
func (u *CreateLoadBalancerUseCase) validateDisabledAnnounceZones(ctx context.Context, lb domain.LoadBalancer) error {
	return checkDisabledAnnounceZones(ctx, u.zoneClient, lb.PlacementType, string(lb.RegionID), lb.DisabledAnnounceZones)
}

// resolveSources — резолв каждого источника через peer-API: placement
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

// doCreate — async worker: durable-handle сага с per-family VIP fan-out.
func (u *CreateLoadBalancerUseCase) doCreate(
	ctx context.Context, lb domain.LoadBalancer, principal operations.Principal, specs []familyVIPSpec,
) (*anypb.Any, error) {
	// Worker-ctx детачнут от request'а — восстанавливаем principal из Operation,
	// чтобы downstream-вызовы (vpc AddressService.Create / SetReference, geo Zone/
	// Region) несли identity тенанта (auth.PropagateOutgoing). Без этого vpc authz
	// отвергает Create как authz_no_principal.
	ctx = operations.WithPrincipal(ctx, principal)
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
	owner := lbAddressOwner(string(lb.ID), string(lb.Name))
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
		kachorepo.OutboxResourceLoadBalancer, string(created.ID), string(created.ProjectID),
		kachorepo.OutboxActionCreated, lbOutboxPayload(created),
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

	if u.addressClient != nil {
		for family, alloc := range allocated {
			if alloc.addressID == "" {
				continue
			}
			if rerr := u.releaseAddress(ctx, alloc.addressID, alloc.origin); rerr != nil {
				logger.Warn("LoadBalancer.Create compensation release failed",
					"err", rerr, "address_id", alloc.addressID, "family", string(family))
			}
		}
	}
	if err := u.deleteHandle(ctx, lbID); err != nil {
		logger.Warn("LoadBalancer.Create compensation delete handle failed; free_ip_runner will reconcile", "err", err)
	}
}

// releaseAddress — release одного Address по origin: owned (auto) →
// two-step ClearReference → FreeIP (иначе FreeIP==Delete упрётся в собственный
// guard); linked → ClearReference без Delete. Идемпотентно.
func (u *CreateLoadBalancerUseCase) releaseAddress(ctx context.Context, addressID string, origin domain.VipOrigin) error {
	if u.addressClient == nil {
		return status.Error(codes.Unavailable, "vpc internal address client not configured")
	}
	if origin == domain.VipOriginLinked {
		return u.addressClient.ClearReference(ctx, addressID)
	}
	// owned (auto): снять собственный owned-референс, затем удалить адрес.
	if err := u.addressClient.ClearReference(ctx, addressID); err != nil {
		return err
	}
	return u.addressClient.FreeIP(ctx, addressID)
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
