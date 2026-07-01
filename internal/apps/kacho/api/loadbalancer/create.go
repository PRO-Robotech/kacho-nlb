// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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

// CreateLoadBalancerUseCase — async Create flow.
//
// Sync part:
//   - sync-validate request → domain.LoadBalancer.Validate (multi-err fast-fail);
//   - sync-check duplicate-name via repo.Reader.List(project+name) → AlreadyExists;
//   - operations.New + opsRepo.CreateWithPrincipal → return Operation immediately.
//
// Async part (worker):
//   - peer-check `project_id` (`InvalidArgument`/`Unavailable` on failure);
//   - peer-check `region_id`;
//   - open Writer-TX → Insert(LB) + Outbox.Emit("CREATED") +
//     FGARegisterOutbox.Emit(fga.register) → Commit (Вариант A: the
//     owner-hierarchy + creator tuple intent is written in the SAME writer-tx
//     as the Insert — no dual-write; a register-drainer applies it through
//     kacho-iam InternalIAMService.RegisterResource);
//   - return Operation.Response = NetworkLoadBalancer.
type CreateLoadBalancerUseCase struct {
	repo                Repo
	opsRepo             operations.Repo
	projectClient       ProjectClient
	regionClient        RegionClient
	networkClient       NetworkClient
	securityGroupClient SecurityGroupClient
	subnetClient        SubnetClient
	addressReader       AddressClient // публичный AddressService.Get — BYO ownership-валидация
	addressClient       InternalAddressClient
	logger              *slog.Logger
}

// NewCreateLoadBalancerUseCase конструктор.
func NewCreateLoadBalancerUseCase(
	repo Repo, opsRepo operations.Repo,
	pc ProjectClient, rc RegionClient, nc NetworkClient, sgc SecurityGroupClient,
	snc SubnetClient, ar AddressClient, ac InternalAddressClient,
	logger *slog.Logger,
) *CreateLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &CreateLoadBalancerUseCase{
		repo: repo, opsRepo: opsRepo,
		projectClient: pc, regionClient: rc, networkClient: nc, securityGroupClient: sgc,
		subnetClient: snc, addressReader: ar, addressClient: ac,
		logger: logger,
	}
}

// Execute — entry-point Create. Возвращает `*operations.Operation` (handler
// конвертит в proto). Sync-validation + Operation insert; worker — отдельная
// goroutine через operations.Run.
func (u *CreateLoadBalancerUseCase) Execute(
	ctx context.Context, req *lbv1.CreateNetworkLoadBalancerRequest,
) (*operations.Operation, error) {
	// ---- Sync validation ----
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
	// INTERNAL-only: EXTERNAL anycast — следующая фаза. Reject синхронно (LB не
	// создаётся), generic-текст по конвенции Kachō.
	if lbType != domain.LBTypeInternal {
		return nil, status.Error(codes.InvalidArgument, "Illegal argument type: only INTERNAL is supported")
	}

	// address_spec → семейства VIP + per-family источник (auto/byo). Присутствие
	// семейства задаёт его в ip_families (dualstack = оба). ≥1 семейство обязательно;
	// malformed BYO addressId ловится синхронно первым стейтментом.
	specs, err := resolveAddressSpec(req.GetAddressSpec())
	if err != nil {
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
	if req.GetDeletionProtection() {
		lb.DeletionProtection = true
	}
	// network_id — VPC-сеть приватного VIP (INTERNAL). Cross-field инвариант
	// INTERNAL⟺non-empty проверяет lb.Validate ниже; существование — sync-precheck
	// через vpc.NetworkService.Get.
	lb.NetworkID = domain.NetworkID(req.GetNetworkId())
	// security_group_ids — набор vpc.SecurityGroup (control-plane intent). Set-
	// семантика (dedup). Cross-field INTERNAL-only + cardinality проверяет
	// lb.Validate; существование + same-network — sync-precheck через
	// vpc.SecurityGroupService.Get.
	lb.SecurityGroupIDs = domain.SecurityGroupIDsFromStrings(req.GetSecurityGroupIds())
	// session_affinity: UNSPECIFIED оставляет builder-default (FIVE_TUPLE);
	// out-of-domain — sync fail-fast с каноничным field-сообщением (зеркало DB CHECK).
	sa, err := lbSessionAffinityFromPb(req.GetSessionAffinity())
	if err != nil {
		return nil, mapDomainErr(err)
	}
	lb.SessionAffinity = sa
	// cross_zone_enabled — optional: omitted сохраняет builder-default (true),
	// явный false/true применяется.
	if req.CrossZoneEnabled != nil {
		lb.CrossZoneEnabled = req.GetCrossZoneEnabled()
	}
	// ip_families — заявленные семейства anycast-VIP (точные токены IPV4/IPV6).
	// КРИТИЧНО: проставляются ДО Insert-handle, т.к. status-aware CHECK требует
	// семейство в ip_families прежде чем persist-VIP запишет непустой address.
	lb.IPFamilies = familiesFromSpecs(specs)
	if err := lb.Validate(); err != nil {
		// Validate возвращает coreerrors.InvalidArgument (gRPC-shaped). mapDomainErr
		// сохранит её as-is.
		return nil, mapDomainErr(err)
	}

	// Sync-precheck существования network_id (только INTERNAL — Validate выше
	// гарантировал INTERNAL⟺non-empty). Cross-domain ref на kacho-vpc Network
	// (request-path, fail-closed): not-found → InvalidArgument, peer недоступен →
	// Unavailable. Gonko-зависимый dangling до writer-TX переживается graceful на
	// чтении (TEXT-ref без FK).
	if lb.Type == domain.LBTypeInternal && u.networkClient != nil {
		if _, err := u.networkClient.Get(ctx, string(lb.NetworkID)); err != nil {
			return nil, networkPeerErr(err, string(lb.NetworkID))
		}
	}

	// Sync-precheck security_group_ids: каждый SG существует И принадлежит
	// network_id LB (same-network). Domain.Validate выше уже гарантировал, что
	// непустой набор бывает только у INTERNAL (где network_id задан). not-found/
	// чужая сеть → InvalidArgument; vpc недоступен → Unavailable (fail-closed).
	if err := validateSecurityGroups(ctx, u.securityGroupClient, string(lb.NetworkID), lb.SecurityGroupIDs); err != nil {
		return nil, err
	}

	// Sync-precheck REGIONAL-подсети auto-семейств: VIP аллоцируется только из
	// region-scoped (REGIONAL) подсети — тогда он anycast. ZONAL/UNSPECIFIED →
	// InvalidArgument; not-found → InvalidArgument; vpc недоступен → Unavailable
	// (fail-closed для мутации). BYO-семейства subnet_id не несут → пропускаются.
	if err := u.validateRegionalSubnets(ctx, specs); err != nil {
		return nil, err
	}

	// Sync-precheck BYO-VIP: принесённый tenant'ом Address обязан принадлежать
	// проекту LB И совпадать по семейству (cross-domain ownership через owner-read
	// API, data-integrity §2 — восстанавливает project/family-guard, снятый с
	// vpc.SetAddressReference). Резолв под tenant-identity: authz-gate `v_get` не
	// даст прочитать чужой Address. Несоответствие/no-access → generic
	// InvalidArgument (анти-oracle); vpc недоступен → Unavailable (fail-closed).
	if err := u.validateBYOAddresses(ctx, lb, specs); err != nil {
		return nil, err
	}

	// Sync duplicate-name check. Race against concurrent insert
	// финализируется UNIQUE-constraint backstop в worker'е.
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

	// ---- Spawn worker ----
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doCreate(workerCtx, lb, principal, specs)
	})

	return &op, nil
}

// doCreate — async worker: durable-handle сага с per-family VIP fan-out.
//
//	peer-check project_id / region_id (до Insert — отказ компенсировать нечего);
//	TX-1: INSERT durable-handle (status='CREATING', ip_families заполнен, address_*='');
//	per-family (v4 затем v6): acquire VIP (auto AllocateInternalIP из REGIONAL-
//	  подсети / BYO AttachExisting) → persist CAS-attach
//	  (address_<fam>/address_id_<fam>/vip_origin_<fam>);
//	finalize: CAS CREATING→INACTIVE + outbox CREATED + FGA-register.
//
// Compensation (defer-guard, активна после Insert-handle и до finalize):
// освобождает VIP по КАЖДОМУ непустому address_id (auto → FreeIP, byo →
// ClearReference) и удаляет handle (best-effort; краш раньше → free_ip_runner).
// Возвращает anypb.Any(NetworkLoadBalancer) при успехе либо gRPC-status при ошибке.
func (u *CreateLoadBalancerUseCase) doCreate(
	ctx context.Context, lb domain.LoadBalancer, principal operations.Principal, specs []familyVIPSpec,
) (*anypb.Any, error) {
	// Peer-check `project_id` / `region_id` ДО Insert-handle. Отказ здесь —
	// компенсировать нечего (handle не вставлен, VIP не аллоцирован).
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

	// TX-1: durable-handle (status='CREATING'). ip_families заполнен; address_*=''
	// — status-aware CHECK пропускает INSERT с пустым address.
	lb.Status = domain.LBStatusCreating
	if err := u.insertHandle(ctx, &lb); err != nil {
		return nil, mapDomainErr(err)
	}

	// Compensation guard (до finalize): release каждого аллоцированного VIP +
	// delete handle. best-effort; ошибка compensation не маскирует исходную.
	finalized := false
	allocated := map[domain.IPVersion]vipAllocResult{}
	defer func() {
		if finalized {
			return
		}
		u.compensateCreate(ctx, string(lb.ID), allocated)
	}()

	// per-family fan-out: acquire → persist для каждого заявленного семейства.
	for _, fs := range specs {
		alloc, err := u.acquireFamilyVIP(ctx, lb, fs)
		if err != nil {
			return nil, mapDomainErr(err)
		}
		allocated[fs.family] = alloc
		if _, err := u.persistVIP(ctx, string(lb.ID), fs.family, alloc.address, alloc.addressID, alloc.origin); err != nil {
			return nil, mapDomainErr(err)
		}
	}

	// Finalize: CREATING→INACTIVE (терминальное Create-состояние VIP-only LB) +
	// outbox CREATED + FGA-register, атомарно одним commit'ом.
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
	origin    domain.VipOrigin // auto → FreeIP; byo → ClearReference (release-ветка)
}

// acquireFamilyVIP — внешний side-effect одного семейства: auto-аллокация VIP из
// REGIONAL-подсети (region-scoped → anycast) либо BYO-привязка принесённого Address.
func (u *CreateLoadBalancerUseCase) acquireFamilyVIP(
	ctx context.Context, lb domain.LoadBalancer, fs familyVIPSpec,
) (vipAllocResult, error) {
	if u.addressClient == nil {
		return vipAllocResult{}, status.Error(codes.Unavailable, "vpc internal address client not configured")
	}
	owner := lbAddressOwner(string(lb.ID))
	if fs.byo {
		resp, err := u.addressClient.AttachExisting(ctx, vpcclient.AttachExistingRequest{
			AddressID: fs.addressID,
			Owner:     owner,
		})
		if err != nil {
			return vipAllocResult{}, err
		}
		return vipAllocResult{addressID: resp.AddressID, address: resp.Value, origin: domain.VipOriginBYO}, nil
	}
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
		return vipAllocResult{}, err
	}
	return vipAllocResult{addressID: resp.AddressID, address: resp.Value, origin: domain.VipOriginAuto}, nil
}

// validateRegionalSubnets — sync-precheck: каждая auto-подсеть существует и
// REGIONAL (region-scoped → VIP anycast). Дедуп по subnet_id. nil subnetClient
// (dev/стенд без vpc) → пропуск. ZONAL/UNSPECIFIED → InvalidArgument.
func (u *CreateLoadBalancerUseCase) validateRegionalSubnets(ctx context.Context, specs []familyVIPSpec) error {
	if u.subnetClient == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(specs))
	for _, fs := range specs {
		if fs.byo || fs.subnetID == "" {
			continue
		}
		if _, ok := seen[fs.subnetID]; ok {
			continue
		}
		seen[fs.subnetID] = struct{}{}
		sn, err := u.subnetClient.Get(ctx, fs.subnetID)
		if err != nil {
			return subnetPeerErr(err, fs.subnetID)
		}
		if sn.PlacementType != vpcclient.SubnetPlacementRegional {
			return status.Error(codes.InvalidArgument,
				"subnet must be REGIONAL for anycast load balancer VIP")
		}
	}
	return nil
}

// validateBYOAddresses — sync-precheck принесённых BYO-адресов: каждый Address
// принадлежит проекту LB И его семейство совпадает с заявленным. Резолв через
// публичный AddressService.Get под tenant-identity (authz-gate `v_get`
// изолирует по tenant'у). Анти-oracle: mismatch / not-found / no-access →
// generic InvalidArgument "Illegal argument addressId" (не раскрываем чужой
// ownership/существование); vpc недоступен → Unavailable. nil addressReader
// (dev/стенд без vpc) → пропуск. auto-семейства (subnet_id) не проверяются здесь.
func (u *CreateLoadBalancerUseCase) validateBYOAddresses(ctx context.Context, lb domain.LoadBalancer, specs []familyVIPSpec) error {
	if u.addressReader == nil {
		return nil
	}
	for _, fs := range specs {
		if !fs.byo {
			continue
		}
		addr, err := u.addressReader.Get(ctx, fs.addressID)
		if err != nil {
			return byoAddressErr(err)
		}
		if addr.ProjectID != string(lb.ProjectID) || addr.Family != string(fs.family) {
			return status.Error(codes.InvalidArgument, "Illegal argument addressId")
		}
	}
	return nil
}

// byoAddressErr — маппинг ошибок AddressService.Get в BYO-precheck. Анти-oracle:
// любое not-found/no-access/bad-input → generic InvalidArgument (не раскрываем,
// существует ли адрес и чей он); vpc недоступен → Unavailable (fail-closed);
// прочее → Internal (без leak'а).
func byoAddressErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArg):
		return status.Error(codes.InvalidArgument, "Illegal argument addressId")
	case errors.Is(err, domain.ErrUnavailable):
		return status.Error(codes.Unavailable, "address lookup unavailable")
	}
	return status.Error(codes.Internal, "address lookup failed")
}

// insertHandle — TX-1: INSERT durable-handle строки LB (status='CREATING').
func (u *CreateLoadBalancerUseCase) insertHandle(ctx context.Context, lb *domain.LoadBalancer) error {
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
	if _, err := w.LoadBalancers().Insert(ctx, lb); err != nil {
		return err
	}
	if err := w.Commit(); err != nil {
		return err
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
// аллоцированный VIP (auto → FreeIP, byo → ClearReference) и удаляет handle.
// Ошибки логируются, не пробрасываются (worker уже вернул исходную ошибку);
// краш раньше — handle добивает free_ip_runner.
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
			var rerr error
			if alloc.origin == domain.VipOriginBYO {
				rerr = u.addressClient.ClearReference(ctx, alloc.addressID, owner)
			} else {
				rerr = u.addressClient.FreeIP(ctx, alloc.addressID, owner)
			}
			if rerr != nil {
				logger.Warn("LoadBalancer.Create compensation release failed",
					"err", rerr, "address_id", alloc.addressID, "family", string(family))
			}
		}
	}
	if err := u.deleteHandle(ctx, lbID); err != nil {
		logger.Warn("LoadBalancer.Create compensation delete handle failed; free_ip_runner will reconcile", "err", err)
	}
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

// familyVIPSpec — разобранный per-family источник VIP (auto/byo) из address_spec.
type familyVIPSpec struct {
	family    domain.IPVersion // IPV4 / IPV6
	byo       bool             // true → BYO addressID, false → auto-alloc
	addressID string           // BYO
	subnetID  string           // auto — REGIONAL-подсеть, из которой аллоцируется VIP
}

// resolveAddressSpec — разбирает NetworkLoadBalancerAddressSpec в упорядоченный
// (v4, затем v6) набор familyVIPSpec. ≥1 семейство обязательно; malformed BYO
// addressId / auto subnet_id ловится синхронно. auto без subnet_id → InvalidArgument
// (subnet_id обязателен). Источник без auto/byo → InvalidArgument.
func resolveAddressSpec(spec *lbv1.NetworkLoadBalancerAddressSpec) ([]familyVIPSpec, error) {
	var out []familyVIPSpec
	add := func(family domain.IPVersion, fam *lbv1.FamilyAddressSpec) error {
		if fam == nil {
			return nil
		}
		fs := familyVIPSpec{family: family}
		switch {
		case fam.GetByo() != nil:
			addressID := fam.GetByo().GetAddressId()
			if err := corevalidate.ResourceID("address", ids.PrefixAddress, addressID); err != nil {
				return err
			}
			fs.byo = true
			fs.addressID = addressID
		case fam.GetAuto() != nil:
			subnetID := fam.GetAuto().GetSubnetId()
			if subnetID == "" {
				return status.Errorf(codes.InvalidArgument,
					"address_spec.%s.auto.subnet_id is required", familyTag(family))
			}
			if err := corevalidate.ResourceID("subnet", ids.PrefixSubnet, subnetID); err != nil {
				return err
			}
			fs.subnetID = subnetID
		default:
			return status.Errorf(codes.InvalidArgument,
				"address_spec.%s.source is not set", familyTag(family))
		}
		out = append(out, fs)
		return nil
	}
	if spec != nil {
		if err := add(domain.IPVersionV4, spec.GetV4()); err != nil {
			return nil, err
		}
		if err := add(domain.IPVersionV6, spec.GetV6()); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, status.Error(codes.InvalidArgument,
			"address_spec must declare at least one ip family for INTERNAL load balancer")
	}
	return out, nil
}

// familiesFromSpecs — список заявленных семейств (точные токены IPV4/IPV6) для
// ip_families в порядке fan-out.
func familiesFromSpecs(specs []familyVIPSpec) []domain.IPVersion {
	out := make([]domain.IPVersion, 0, len(specs))
	for _, fs := range specs {
		out = append(out, fs.family)
	}
	return out
}

// familyTag — короткий тег семейства для error-текста ("v4"/"v6").
func familyTag(family domain.IPVersion) string {
	if family == domain.IPVersionV6 {
		return "v6"
	}
	return "v4"
}

// lbAddressOwner — owner-tuple для vpc.Address used_by ("nlb_load_balancer:<id>").
func lbAddressOwner(lbID string) vpcclient.AddressOwner {
	return vpcclient.AddressOwner{Kind: lbAddressOwnerKind, ID: lbID}
}

// lbAddressOwnerKind — Reference.kind для NLB LoadBalancer в vpc.Address used_by.
const lbAddressOwnerKind = "nlb_load_balancer"

// assertNameUnique — sync precheck дубликата (project_id, name). UNIQUE-violation
// в Insert — атомарный backstop, но sync-fail-fast → "лучше UX" (operation не
// создаётся; client не ждёт worker).
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
			"NetworkLoadBalancer with name %s already exists", name)
	}
	return nil
}

// lbRegisterIntent builds the FGA-register-intent for a freshly created
// LoadBalancer: a project-hierarchy tuple plus, if the principal is an
// authenticated (non-system) user, a creator (admin) tuple. The empty-subject
// creator tuple is skipped (parity with the former EmitCreator skip-on-empty-
// subject — system-initiated resources have no human owner).
func lbRegisterIntent(lb *kachorepo.LoadBalancerRecord, principal operations.Principal) domain.FGARegisterIntent {
	id := string(lb.ID)
	tuples := []domain.FGATuple{
		domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, id, string(lb.ProjectID)),
	}
	if subject := domain.FGASubjectFromPrincipal(principal.Type, principal.ID); subject != "" {
		tuples = append(tuples, domain.FGACreatorTuple(subject, domain.FGAObjectTypeLoadBalancer, id))
	}
	// carry tenant labels + parent-project so kacho-iam feeds its
	// resource_mirror (γ selector matchLabels / containment). source_version is
	// stamped by the outbox emitter from the DB clock inside the writer-tx.
	return domain.FGARegisterIntent{
		Kind:            "NetworkLoadBalancer",
		ResourceID:      id,
		Tuples:          tuples,
		Labels:          domain.LabelsToMap(lb.Labels),
		ParentProjectID: string(lb.ProjectID),
	}
}

// lbMirrorIntent builds the mirror-feed register-intent for an
// UPDATED LoadBalancer: the project-hierarchy tuple (re-register is idempotent in
// IAM) carrying the refreshed labels + parent so kacho-iam updates its
// resource_mirror. No creator tuple — Update never re-assigns ownership; this is a
// pure labels-refresh feed. source_version is stamped by the outbox emitter.
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

// domainSessionAffinity — proto enum → domain.SessionAffinity. UNSPECIFIED →
// FIVE_TUPLE (DB DEFAULT); out-of-domain numeric value переносится своей
// строкой, чтобы Validate отверг его каноничным field-сообщением (зеркало
// DB CHECK IN ('FIVE_TUPLE','CLIENT_IP_ONLY')).
func domainSessionAffinity(a lbv1.NetworkLoadBalancer_SessionAffinity) domain.SessionAffinity {
	switch a {
	case lbv1.NetworkLoadBalancer_SESSION_AFFINITY_UNSPECIFIED, lbv1.NetworkLoadBalancer_FIVE_TUPLE:
		return domain.SessionAffinity5Tuple
	case lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY:
		return domain.SessionAffinityClientIPOnly
	}
	return domain.SessionAffinity(a.String())
}

// lbSessionAffinityFromPb — fail-fast вариант domainSessionAffinity: возвращает
// каноничную InvalidArgument-ошибку на значение вне домена.
func lbSessionAffinityFromPb(a lbv1.NetworkLoadBalancer_SessionAffinity) (domain.SessionAffinity, error) {
	sa := domainSessionAffinity(a)
	if err := sa.Validate(); err != nil {
		return "", err
	}
	return sa, nil
}

// peerErrToStatus — маппинг ошибок peer-client (project/region) в gRPC-status.
// Peer-clients оборачивают grpc-status в domain-sentinel ошибки:
//
//	domain.ErrNotFound          → InvalidArgument (peer-resource missing на input-time)
//	domain.ErrInvalidArg        → InvalidArgument
//	domain.ErrFailedPrecondition→ FailedPrecondition (e.g. project deleted)
//	domain.ErrUnavailable       → Unavailable
//	прочее                       → Internal
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

// networkPeerErr — sync-precheck network_id через vpc.NetworkService.Get. Клиент
// оборачивает grpc-status в domain-sentinel: NotFound/InvalidArgument peer'а — это
// bad-input на request-time → InvalidArgument "network <id> not found"; недоступность
// → Unavailable (fail-closed для мутации); прочее → Internal (без leak'а).
func networkPeerErr(err error, id string) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "network %s not found", id)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable, "network lookup unavailable")
	}
	return status.Errorf(codes.Internal, "network lookup failed")
}

// subnetPeerErr — sync-precheck subnet_id через vpc.SubnetService.Get. Клиент
// оборачивает grpc-status в domain-sentinel: NotFound/InvalidArgument peer'а — это
// bad-input на request-time → InvalidArgument "subnet <id> not found"; недоступность
// → Unavailable (fail-closed для мутации); прочее → Internal (без leak'а).
func subnetPeerErr(err error, id string) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidArg):
		return status.Errorf(codes.InvalidArgument, "subnet %s not found", id)
	case errors.Is(err, domain.ErrUnavailable):
		return status.Errorf(codes.Unavailable, "subnet lookup unavailable")
	}
	return status.Errorf(codes.Internal, "subnet lookup failed")
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
