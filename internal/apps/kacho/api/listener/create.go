package listener

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/H-BF/corlib/pkg/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
)

// CreateUseCase инициирует создание Listener'а (acceptance GWT-LST-001..LST-015).
//
// Sync (handler-thread, до возврата Operation клиенту):
//  1. Required: load_balancer_id.
//  2. LB.Get (same project + status != DELETING) — verbatim YC NotFound иначе.
//  3. domain.Listener builder + Validate() (name regex, port range, protocol,
//     ip_version, labels).
//  4. INTERNAL LB → subnet_id required (verbatim YC error GWT-LST-006).
//  5. address_spec.address_id ↔ subnet_id: ровно одна BYO/auto-ветка может
//     отсутствовать (proto oneof обеспечивает это на схеме; sync проверяем
//     совместимость с типом LB).
//  6. opsRepo.CreateWithPrincipal(op, principal).
//  7. operations.Run(callerCtx, opsRepo, op.ID, worker) — fire-and-trigger.
//
// Async worker:
//  1. VIP-acquire branch:
//     - BYO: vpc.AddressService.Get → verify project + ip_version → SetReference CAS.
//     - Auto/EXTERNAL: vpc.InternalAddressService.AllocateExternalIP.
//     - Auto/INTERNAL: vpc.InternalAddressService.AllocateInternalIP.
//  2. repo.Writer open: Insert listener (with allocated_address + address_id) +
//     outbox emit `nlb_listener:<id> CREATED` + `nlb_load_balancer:<lb_id> UPDATED`
//     atomically.
//  3. Commit. Если ошибка после Commit (defer compensation no-op).
//  4. fgawrite: best-effort WriteCreatorTuple(`owner`, "nlb_listener:<id>") +
//     parent-link tuple `nlb_listener:<id>#load_balancer@nlb_load_balancer:<lb_id>`.
//  5. operations.Run возвращает marshalled Listener → ops.MarkDone(response).
//
// Compensation (defer guard в worker):
//   - VIP allocated AND (subsequent SetReference|Insert|Commit failed) →
//     vpc.InternalAddressService.FreeIP (auto-alloc branch) или
//     vpc.InternalAddressService.ClearReference (BYO branch). Best-effort —
//     ошибка compensation НЕ маскирует исходную ошибку worker'а (она важнее
//     для caller'а; cleanup только log'ируется).
type CreateUseCase struct {
	repo            RepoFactory
	opsRepo         OperationsRepo
	addresses       AddressClient         // BYO: Address.Get
	internalAddrs   InternalAddressClient // Auto-alloc + SetReference + Free + Clear
	subnets         SubnetClient          // INTERNAL Listener subnet validation
	hierarchyWriter HierarchyWriter       // D-11 sync hierarchy tuples (best-effort)
	subject         permissionsCtxAccessor
	logger          *slog.Logger
}

// NewCreateUseCase — конструктор. Все зависимости — port-интерфейсы (composition
// root wires в `cmd/kacho-loadbalancer/main.go`). hierarchyWriter и logger
// допускаются nil (dev mode без FGA) — write-helpers это переживают (см.
// helpers.go loggerOrDiscard).
func NewCreateUseCase(
	repo RepoFactory,
	opsRepo OperationsRepo,
	addresses AddressClient,
	internalAddrs InternalAddressClient,
	subnets SubnetClient,
	hierarchyWriter HierarchyWriter,
	logger *slog.Logger,
) *CreateUseCase {
	return &CreateUseCase{
		repo:            repo,
		opsRepo:         opsRepo,
		addresses:       addresses,
		internalAddrs:   internalAddrs,
		subnets:         subnets,
		hierarchyWriter: hierarchyWriter,
		subject:         principalSubjectAccessor{},
		logger:          logger,
	}
}

// Run — sync validation + Operation creation + async worker spawn.
//
// Возвращает Operation клиенту до того, как worker завершится. Клиент поллит
// `OperationService.Get(id)` до `done=true`.
func (u *CreateUseCase) Run(ctx context.Context, req *lbv1.CreateListenerRequest) (*operations.Operation, error) {
	lbID := req.GetLoadBalancerId()
	if lbID == "" {
		return nil, status.Error(codes.InvalidArgument, "load_balancer_id required")
	}

	// Sync read parent LB (single-row Read-only TX). Verifies LB exists, not
	// DELETING; пробрасывает project_id/region_id для denormalisation.
	lb, err := u.fetchParentLB(ctx, lbID)
	if err != nil {
		return nil, err
	}

	// address_spec required (proto annotation `(required)=true`).
	spec := req.GetAddressSpec()
	if spec == nil {
		return nil, status.Error(codes.InvalidArgument, "address_spec is required")
	}

	// Build domain entity + run domain Validate() (name regex, port range,
	// protocol, ip_version, labels). Allocated_address оставляем пустым —
	// заполняется в worker'е после VIP-allocation.
	name, err := buildDomainName(req.GetName())
	if err != nil {
		return nil, err
	}
	listener := domain.NewListener(
		lb.LoadBalancer,
		name,
		domain.LbProto(req.GetProtocol().String()),
		domain.LbPort(req.GetPort()),
		domain.LbPort(req.GetTargetPort()),
		domain.IPVersion(req.GetIpVersion().String()),
	)
	listener.Description = domain.LbDescription(req.GetDescription())
	listener.Labels = domain.LabelsFromMap(req.GetLabels())
	listener.ProxyProtocolV2 = req.GetProxyProtocolV2()
	if tgID := req.GetDefaultTargetGroupId(); tgID != "" {
		listener.DefaultTargetGroupID = option.MustNewOption(domain.ResourceID(tgID))
	}

	// AddressSpec → AddressID / SubnetID handling.
	addrCtx, err := resolveAddressContext(spec, lb.Type)
	if err != nil {
		return nil, err
	}
	if addrCtx.byo {
		listener.AddressID = option.MustNewOption(domain.AddressID(addrCtx.addressID))
	}
	if addrCtx.subnetID != "" {
		listener.SubnetID = option.MustNewOption(domain.SubnetID(addrCtx.subnetID))
	}

	// Domain self-validate (acceptance GWT-LST-007..LST-009).
	if err := listener.Validate(); err != nil {
		return nil, err
	}

	// Create Operation row (done=false). Principal pulled from ctx (E2 auth-
	// interceptor) или SystemPrincipal на E0.
	op, err := operations.NewFromContext(ctx,
		ids.PrefixOperationNLB,
		fmt.Sprintf("Create listener %s", string(name)),
		&lbv1.CreateListenerMetadata{
			ListenerId:     string(listener.ID),
			LoadBalancerId: lbID,
		},
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "operations.New: %v", err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, status.Errorf(codes.Internal, "ops.Create: %v", err)
	}

	// Snapshot входов для worker'а — handler-ctx канселится сразу после
	// возврата Operation; worker должен жить на собственном baggage-ctx.
	subject := u.subject.SubjectFromContext(ctx)
	in := createInput{
		listener:  listener,
		addrCtx:   addrCtx,
		lb:        lb,
		fgaOwner:  subject,
	}
	operations.Run(ctx, u.opsRepo, op.ID, func(workerCtx context.Context) (*anypb.Any, error) {
		return u.doCreate(workerCtx, in)
	})
	return &op, nil
}

// parentLB — snapshot полей LB, нужных Listener-Create worker'у.
type parentLB struct {
	domain.LoadBalancer
}

// fetchParentLB — sync Get parent LB. Errors:
//
//	NotFound        — LB не существует.
//	FailedPrecond.  — LB.Status == DELETING (acceptance §4 LST-001 pre-cond).
//	Internal        — repo failure.
func (u *CreateUseCase) fetchParentLB(ctx context.Context, lbID string) (*parentLB, error) {
	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()
	rec, err := rd.LoadBalancers().Get(ctx, lbID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if rec.Status == domain.LBStatusDeleting {
		return nil, status.Errorf(codes.FailedPrecondition,
			"NetworkLoadBalancer %s is being deleted", lbID)
	}
	return &parentLB{LoadBalancer: rec.LoadBalancer}, nil
}

// addressContext — parsed VIP-source context от ListenerAddressSpec.
type addressContext struct {
	byo       bool   // true → BYO address_id, false → auto-alloc.
	addressID string // populated если BYO либо после auto-alloc commit.
	subnetID  string // populated для INTERNAL Listener (auto / BYO).
}

// resolveAddressContext — вычисляет addressContext из proto ListenerAddressSpec
// + parent LB.Type. Sync проверки:
//
//   - INTERNAL LB + auto-alloc → subnet_id обязателен (LST-006).
//   - BYO + ничего иного — допускается; INTERNAL + BYO + subnet_id ignored
//     (BYO Address уже несёт привязку к Subnet через vpc.Address.scope.subnet_id).
//   - EXTERNAL LB + auto.subnet_id — silent-ignore (proto allows; YC behaviour).
//   - Source not set (proto oneof default = nil) → InvalidArgument.
func resolveAddressContext(spec *lbv1.ListenerAddressSpec, lbType domain.LBType) (addressContext, error) {
	switch src := spec.GetSource().(type) {
	case *lbv1.ListenerAddressSpec_AddressId:
		if src.AddressId == "" {
			return addressContext{}, status.Error(codes.InvalidArgument,
				"address_spec.address_id is empty")
		}
		return addressContext{byo: true, addressID: src.AddressId}, nil
	case *lbv1.ListenerAddressSpec_Auto:
		auto := src.Auto
		if auto == nil {
			auto = &lbv1.ListenerAddressSpec_AutoAllocate{}
		}
		if lbType == domain.LBTypeInternal && auto.GetSubnetId() == "" {
			return addressContext{}, status.Error(codes.InvalidArgument,
				"subnet_id is required for INTERNAL load balancer")
		}
		// EXTERNAL: subnet_id silent-ignored (YC behaviour); INTERNAL: keep.
		subnet := ""
		if lbType == domain.LBTypeInternal {
			subnet = auto.GetSubnetId()
		}
		return addressContext{byo: false, subnetID: subnet}, nil
	}
	return addressContext{}, status.Error(codes.InvalidArgument, "address_spec.source is not set")
}

// buildDomainName — обёртка над domain.LbName с верхним sync-маппингом proto
// → domain newtype. Возвращает gRPC InvalidArgument из Validate().
func buildDomainName(raw string) (domain.LbName, error) {
	n := domain.LbName(raw)
	if err := n.Validate(); err != nil {
		return "", err
	}
	return n, nil
}

// createInput — snapshot входов для async worker. Кладём здесь, чтобы handler-
// ctx не утекал в worker (baggage-only propagation via operations.Run).
type createInput struct {
	listener domain.Listener
	addrCtx  addressContext
	lb       *parentLB
	fgaOwner string
}

// doCreate — worker-side флоу Listener.Create. Возвращает anypb.Any(Listener)
// при успехе либо gRPC-status при ошибке (operations.Run пишет в Operation.error).
func (u *CreateUseCase) doCreate(ctx context.Context, in createInput) (*anypb.Any, error) {
	listener := in.listener

	// VIP allocation branch (BYO / auto-EXTERNAL / auto-INTERNAL).
	allocResult, err := u.acquireVIP(ctx, listener, in.addrCtx)
	if err != nil {
		return nil, mapDomainErr(err)
	}

	// Compensation guard: defer rollback VIP if any subsequent step fails.
	committed := false
	defer func() {
		if committed {
			return
		}
		u.compensateVIP(ctx, listener.ID, allocResult)
	}()

	listener.AllocatedAddress = domain.IPAddress(allocResult.address)
	listener.AddressID = option.MustNewOption(domain.AddressID(allocResult.addressID))

	// Open writer-TX: Insert + 2× outbox-emit + Commit atomically.
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	rolledBack := false
	defer func() {
		if rolledBack {
			return
		}
		w.Abort()
	}()

	created, err := w.Listeners().Insert(ctx, &listener)
	if err != nil {
		w.Abort()
		rolledBack = true
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeListener, string(created.ID), string(created.ProjectID),
		outboxActionCreated, listenerPayloadMap(created),
	); err != nil {
		w.Abort()
		rolledBack = true
		return nil, mapDomainErr(fmt.Errorf("%w: outbox emit listener CREATED: %v", domain.ErrInternal, err))
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeLoadBalancer, string(in.lb.ID), string(in.lb.ProjectID),
		outboxActionUpdated, lbUpdatedPayloadMap(string(in.lb.ID), string(in.lb.ProjectID), string(in.lb.RegionID), "listener_created"),
	); err != nil {
		w.Abort()
		rolledBack = true
		return nil, mapDomainErr(fmt.Errorf("%w: outbox emit lb UPDATED: %v", domain.ErrInternal, err))
	}
	if err := w.Commit(); err != nil {
		rolledBack = true
		return nil, mapDomainErr(err)
	}
	committed = true

	// D-11 sync hierarchy tuple writes. Best-effort + non-fatal — listener row
	// already durable. Failures logged through helper (zero-deps; pattern
	// mirrors kacho-vpc/internal/apps/kacho/fgawrite.Emit).
	u.emitHierarchyTuples(ctx, created.ID, in.lb.ID, in.fgaOwner)

	return marshalListener(created)
}

// vipAllocResult — outcome VIP-allocation branch.
type vipAllocResult struct {
	addressID string
	address   string
	byo       bool                  // если true — освобождать через ClearReference, иначе через FreeIP.
	owner     vpcclient.AddressOwner
}

// acquireVIP — branch:
//
//   - BYO: vpc.AddressService.Get(...) → verify same project + matching
//     ip_version → vpc.InternalAddressService.SetReference(...) atomic CAS.
//     Errors:
//     NotFound          → InvalidArgument "address <id> not found"
//     cross-project     → InvalidArgument verbatim text
//     ip_version mismatch → InvalidArgument verbatim text
//     used_by occupied  → FailedPrecondition verbatim text (LST-003)
//   - EXTERNAL auto:    vpc.InternalAddressService.AllocateExternalIP.
//   - INTERNAL auto:    vpc.InternalAddressService.AllocateInternalIP(subnet_id=...).
func (u *CreateUseCase) acquireVIP(ctx context.Context, l domain.Listener, addrCtx addressContext) (vipAllocResult, error) {
	owner := addressOwner(string(l.ID))

	if addrCtx.byo {
		if u.addresses == nil || u.internalAddrs == nil {
			return vipAllocResult{}, status.Error(codes.Unavailable, "vpc address client not configured")
		}
		addr, err := u.addresses.Get(ctx, addrCtx.addressID)
		if err != nil {
			return vipAllocResult{}, err
		}
		// Same-project guard (verbatim LST-005 text).
		if addr.ProjectID != string(l.ProjectID) {
			return vipAllocResult{}, fmt.Errorf("%w: address project_id does not match listener load_balancer project_id",
				domain.ErrInvalidArg)
		}
		// IP-version guard (verbatim LST-004 text).
		want := familyForIPVersion(l.IPVersion)
		if addr.Family != "" && addr.Family != want {
			return vipAllocResult{}, fmt.Errorf("%w: address ip_version %s does not match listener ip_version %s",
				domain.ErrInvalidArg, addr.Family, l.IPVersion)
		}
		// Used-by guard (verbatim LST-003 text). Idempotent re-attach to self
		// passes (SetReference CAS allows owner == self).
		if addr.UsedBy != nil && !ownerMatches(*addr.UsedBy, owner) {
			return vipAllocResult{}, fmt.Errorf("%w: address %s is already in use by %s:%s",
				domain.ErrFailedPrecondition, addr.ID, addr.UsedBy.Kind, addr.UsedBy.ID)
		}
		if err := u.internalAddrs.SetReference(ctx, addr.ID, owner); err != nil {
			return vipAllocResult{}, err
		}
		return vipAllocResult{
			addressID: addr.ID,
			address:   addr.Value,
			byo:       true,
			owner:     owner,
		}, nil
	}

	if u.internalAddrs == nil {
		return vipAllocResult{}, status.Error(codes.Unavailable, "vpc internal-address client not configured")
	}
	switch l.IPVersion {
	case domain.IPVersionV4, domain.IPVersionV6:
	default:
		return vipAllocResult{}, fmt.Errorf("%w: ip_version %s not supported for auto-alloc",
			domain.ErrInvalidArg, l.IPVersion)
	}
	if addrCtx.subnetID == "" {
		// EXTERNAL → AllocateExternalIP. Передаём project_id для ownership
		// созданного Address-ресурса; zone — пустая (берётся по LB.region default
		// pool, KAC-71 cascade selector).
		resp, err := u.internalAddrs.AllocateExternalIP(ctx, vpcclient.AllocateExternalIPRequest{
			ProjectID: string(l.ProjectID),
			Name:      fmt.Sprintf("nlb-listener-%s", domain.TruncateID(l.ID)),
			Owner:     owner,
		})
		if err != nil {
			return vipAllocResult{}, err
		}
		return vipAllocResult{
			addressID: resp.AddressID,
			address:   resp.Value,
			byo:       false,
			owner:     owner,
		}, nil
	}
	// INTERNAL → AllocateInternalIP scoped to subnet_id.
	resp, err := u.internalAddrs.AllocateInternalIP(ctx, vpcclient.AllocateInternalIPRequest{
		ProjectID: string(l.ProjectID),
		Name:      fmt.Sprintf("nlb-listener-%s", domain.TruncateID(l.ID)),
		SubnetID:  addrCtx.subnetID,
		Owner:     owner,
	})
	if err != nil {
		return vipAllocResult{}, err
	}
	return vipAllocResult{
		addressID: resp.AddressID,
		address:   resp.Value,
		byo:       false,
		owner:     owner,
	}, nil
}

// compensateVIP — best-effort cleanup after VIP allocated but listener insert
// / commit failed. Auto-alloc → FreeIP (delete Address); BYO → ClearReference.
// Errors logged, not propagated (worker already returned original error).
func (u *CreateUseCase) compensateVIP(ctx context.Context, listenerID domain.ResourceID, alloc vipAllocResult) {
	if u.internalAddrs == nil || alloc.addressID == "" {
		return
	}
	logger := loggerOrDiscard(u.logger).With(
		"listener_id", string(listenerID),
		"address_id", alloc.addressID,
		"byo", alloc.byo,
	)
	if alloc.byo {
		if err := u.internalAddrs.ClearReference(ctx, alloc.addressID, alloc.owner); err != nil {
			logger.Warn("listener.Create compensation ClearReference failed", "err", err)
			return
		}
		logger.Info("listener.Create compensation ClearReference ok")
		return
	}
	if err := u.internalAddrs.FreeIP(ctx, alloc.addressID, alloc.owner); err != nil {
		logger.Warn("listener.Create compensation FreeIP failed", "err", err)
		return
	}
	logger.Info("listener.Create compensation FreeIP ok")
}

// emitHierarchyTuples — best-effort D-11 sync:
//
//	nlb_listener:<id> #owner @<subject>                        (creator)
//	nlb_listener:<id> #load_balancer @nlb_load_balancer:<lb_id>  (parent)
//
// nil hierarchyWriter or empty subject → tuple skipped (dev mode / unauthenticated).
func (u *CreateUseCase) emitHierarchyTuples(ctx context.Context, listenerID, lbID domain.ResourceID, subject string) {
	if u.hierarchyWriter == nil {
		return
	}
	logger := loggerOrDiscard(u.logger).With(
		"listener_id", string(listenerID),
		"lb_id", string(lbID),
	)
	if subject != "" {
		obj := fgaObjectTypeListener + ":" + string(listenerID)
		if err := u.hierarchyWriter.WriteCreatorTuple(ctx, subject, fgaRelationOwner, obj); err != nil {
			logger.Warn("listener creator-tuple write failed", "err", err, "subject", subject)
		}
	} else {
		logger.Debug("listener creator-tuple skipped (no subject)")
	}
	// Parent-link tuple: <subject> = nlb_load_balancer:<lb_id>; relation =
	// load_balancer; object = nlb_listener:<id>. Используем WriteCreatorTuple,
	// потому что Internal IAMService.WriteCreatorTuple — единственный универсальный
	// tuple-writer (текущее ограничение proto-поверхности; см. iam/hierarchy_client.go
	// doc).
	parentSubject := fgaObjectTypeLoadBalancer + ":" + string(lbID)
	parentObj := fgaObjectTypeListener + ":" + string(listenerID)
	if err := u.hierarchyWriter.WriteCreatorTuple(ctx, parentSubject, fgaRelationLoadBalancer, parentObj); err != nil {
		logger.Warn("listener parent-link tuple write failed", "err", err)
	}
}

// familyForIPVersion → vpcclient.AddressFamilyIPv4/IPv6.
func familyForIPVersion(v domain.IPVersion) string {
	switch v {
	case domain.IPVersionV6:
		return vpcclient.AddressFamilyIPv6
	}
	return vpcclient.AddressFamilyIPv4
}

// ownerMatches — true if existing vpc.Address.used_by matches our owner
// (idempotent re-attach to self после crash + retry).
func ownerMatches(have, want vpcclient.AddressOwner) bool {
	return have.Kind == want.Kind && have.ID == want.ID
}

// unused (defensive): silence unused import / errors helper. errors import is
// used by Update/Delete; create.go uses it through mapDomainErr indirectly.
var _ = errors.Is
