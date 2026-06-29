// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

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
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// CreateUseCase инициирует создание Listener'а.
//
// Sync (handler-thread, до возврата Operation клиенту):
//  1. Required: load_balancer_id.
//  2. LB.Get (same project + status != DELETING) — по конвенции Kachō NotFound иначе.
//  3. domain.Listener builder + Validate (name regex, port range, protocol,
//     ip_version, labels).
//  4. INTERNAL LB → subnet_id required (по конвенции Kachō error).
//  5. address_spec.address_id ↔ subnet_id: ровно одна BYO/auto-ветка может
//     отсутствовать (proto oneof обеспечивает это на схеме; sync проверяем
//     совместимость с типом LB).
//  6. opsRepo.CreateWithPrincipal(op, principal).
//  7. operations.Run(callerCtx, opsRepo, op.ID, worker) — fire-and-trigger.
//
// Async worker — durable-handle сага в 3 TX (детали — doc у doCreate):
//  1. TX-1: INSERT durable handle (status='CREATING'; address_id известен для
//     BYO, пуст для auto) — ДО alloc.
//  2. acquireVIP: BYO `Get`+`SetReference`; auto `AllocateExternalIP`/
//     `AllocateInternalIP`.
//  3. TX-2: отдельный немедленный commit `address_id`+`allocated_address` в
//     CREATING-handle (durable address_id для reconcile-by-address).
//  4. TX-3 (writer-TX): CREATING→ACTIVE + outbox `nlb_listener:<id> CREATED` +
//     `nlb_load_balancer:<lb_id> UPDATED` + FGA-register-intent (creator +
//     parent-link `lb_network_load_balancer:<lb_id>#load_balancer@lb_listener:<id>`),
//     атомарно; register-drainer применяет intent через kacho-iam.
//  5. operations.Run возвращает marshalled Listener → ops.MarkDone(response).
//
// Compensation (defer guard в worker) до финализации: release VIP (если
// аллоцирован) + delete handle. Best-effort — ошибка compensation НЕ маскирует
// исходную ошибку worker'а (cleanup только log'ируется). Если процесс умирает
// раньше compensation — осиротевший CREATING-handle добивает free_ip_runner
// (docs/architecture/15-free-ip-runner.md).
type CreateUseCase struct {
	repo          RepoFactory
	opsRepo       OperationsRepo
	addresses     AddressClient         // BYO: Address.Get
	internalAddrs InternalAddressClient // Auto-alloc + SetReference + Free + Clear
	subnets       SubnetClient          // INTERNAL Listener subnet validation
	subject       permissionsCtxAccessor
	logger        *slog.Logger
}

// NewCreateUseCase — конструктор. Все зависимости — port-интерфейсы (composition
// root wires в `cmd/kacho-loadbalancer/main.go`). logger допускается nil — write-
// helpers это переживают (см. helpers.go loggerOrDiscard). FGA owner/parent-link
// tuple-регистрация — через outbox (FGARegisterOutbox в writer-tx), не
// прямым FGA-клиентом.
func NewCreateUseCase(
	repo RepoFactory,
	opsRepo OperationsRepo,
	addresses AddressClient,
	internalAddrs InternalAddressClient,
	subnets SubnetClient,
	logger *slog.Logger,
) *CreateUseCase {
	return &CreateUseCase{
		repo:          repo,
		opsRepo:       opsRepo,
		addresses:     addresses,
		internalAddrs: internalAddrs,
		subnets:       subnets,
		subject:       principalSubjectAccessor{},
		logger:        logger,
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

	// Build domain entity + run domain Validate (name regex, port range,
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
		// BYO: tenant передал address_id — Delete снимет лишь ссылку
		// (ClearReference), сам Address не удаляется. auto-alloc — дефолт
		// (NewListener уже проставил VipOriginAuto).
		listener.VipOrigin = domain.VipOriginBYO
	}
	if addrCtx.subnetID != "" {
		listener.SubnetID = option.MustNewOption(domain.SubnetID(addrCtx.subnetID))
	}

	// Domain self-validate.
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
		return nil, mapDomainErr(err)
	}
	principal := operations.PrincipalFromContext(ctx)
	if err := u.opsRepo.CreateWithPrincipal(ctx, op, principal); err != nil {
		return nil, mapDomainErr(err)
	}

	// Snapshot входов для worker'а — handler-ctx канселится сразу после
	// возврата Operation; worker должен жить на собственном baggage-ctx.
	subject := u.subject.SubjectFromContext(ctx)
	in := createInput{
		listener: listener,
		addrCtx:  addrCtx,
		lb:       lb,
		fgaOwner: subject,
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
//	FailedPrecond.  — LB.Status == DELETING (pre-cond).
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
//   - INTERNAL LB + auto-alloc → subnet_id обязателен.
//   - BYO + ничего иного — допускается; INTERNAL + BYO + subnet_id ignored
//     (BYO Address уже несёт привязку к Subnet через vpc.Address.scope.subnet_id).
//   - EXTERNAL LB + auto.subnet_id — silent-ignore (proto allows; поведение по конвенции Kachō).
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
		// EXTERNAL: subnet_id silent-ignored (поведение по конвенции Kachō); INTERNAL: keep.
		subnet := ""
		if lbType == domain.LBTypeInternal {
			subnet = auto.GetSubnetId()
		}
		return addressContext{byo: false, subnetID: subnet}, nil
	}
	return addressContext{}, status.Error(codes.InvalidArgument, "address_spec.source is not set")
}

// buildDomainName — обёртка над domain.LbName с верхним sync-маппингом proto
// → domain newtype. Возвращает gRPC InvalidArgument из Validate.
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

// doCreate — worker-side флоу Listener.Create как durable-handle сага в 3 TX.
// VIP-аллокация — внешний side-effect (единственный dual-write edge), поэтому
// строка-handle создаётся ДО alloc и address_id персистится сразу после
// alloc-ответа отдельным commit'ом — чтобы при сбое финала free_ip_runner мог
// детерминированно освободить VIP по address_id:
//
//	TX-1: INSERT listeners (status='CREATING', allocated_address='';
//	      address_id — известен для BYO, пуст для auto) — durable handle.
//	acquireVIP — BYO: Get+SetReference; auto: AllocateExternal/InternalIP.
//	TX-2: UPDATE SET address_id, allocated_address (отдельный немедленный commit,
//	      всё ещё CREATING) — persist durable address_id.
//	TX-3: UPDATE CREATING→ACTIVE + outbox CREATED + LB UPDATED + fga-register.
//
// Откат до финализации (defer compensateCreate) — освобождает VIP (если
// аллоцирован) и удаляет handle (best-effort). Если процесс умирает раньше —
// осиротевший handle (CREATING) добивает free_ip_runner. Узкий auto-only
// known-gap: краш в окне «alloc-ответ ↔ TX-2 commit» → пустой address_id в
// строке → reconcile by-address невозможен (docs/architecture/15-free-ip-runner.md).
// Возвращает anypb.Any(Listener) при успехе либо gRPC-status при ошибке.
func (u *CreateUseCase) doCreate(ctx context.Context, in createInput) (*anypb.Any, error) {
	listener := in.listener

	// TX-1: durable handle (status='CREATING'). allocated_address пуст до alloc;
	// для BYO address_id уже известен (option выставлен в Run), для auto — пуст.
	if err := u.insertHandle(ctx, &listener); err != nil {
		return nil, mapDomainErr(err)
	}

	// Compensation guard (до финализации): release VIP (если аллоцирован) + delete
	// handle. best-effort; ошибка compensation не маскирует исходную ошибку.
	finalized := false
	var alloc vipAllocResult
	defer func() {
		if finalized {
			return
		}
		u.compensateCreate(ctx, listener.ID, alloc)
	}()

	// acquireVIP — внешний side-effect (BYO / auto-EXTERNAL / auto-INTERNAL).
	var err error
	alloc, err = u.acquireVIP(ctx, listener, in.addrCtx)
	if err != nil {
		return nil, mapDomainErr(err)
	}

	// TX-2 (немедленный отдельный commit): persist address_id + allocated_address
	// в CREATING-handle сразу после alloc-ответа. UNIQUE (region,VIP,port,proto)
	// ловится здесь (allocated_address становится непустым) → ErrAlreadyExists.
	if _, err := u.persistVIP(ctx, string(listener.ID), alloc.addressID, alloc.address); err != nil {
		return nil, mapDomainErr(err)
	}

	// TX-3 (writer-TX): CREATING→ACTIVE + outbox CREATED + LB UPDATED + fga-register.
	created, err := u.finalizeCreate(ctx, listener, in)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	finalized = true

	return marshalListener(created)
}

// insertHandle — TX-1: INSERT durable-handle строки (status='CREATING').
func (u *CreateUseCase) insertHandle(ctx context.Context, l *domain.Listener) error {
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
	if _, err := w.Listeners().Insert(ctx, l); err != nil {
		return err
	}
	if err := w.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// persistVIP — TX-2: отдельный немедленный commit address_id + allocated_address
// в CREATING-handle (durable address_id для reconcile-by-address).
func (u *CreateUseCase) persistVIP(ctx context.Context, id, addressID, allocatedAddress string) (*kachorepo.ListenerRecord, error) {
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
	rec, err := w.Listeners().SetVIP(ctx, id, addressID, allocatedAddress)
	if err != nil {
		return nil, err
	}
	if err := w.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return rec, nil
}

// finalizeCreate — TX-3 (writer-TX): CREATING→ACTIVE + outbox CREATED + LB
// UPDATED + fga-register-intent (creator + parent-link) атомарно одним commit'ом.
func (u *CreateUseCase) finalizeCreate(ctx context.Context, l domain.Listener, in createInput) (*kachorepo.ListenerRecord, error) {
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
	created, err := w.Listeners().SetStatusCAS(ctx, string(l.ID),
		domain.ListenerStatusCreating, domain.ListenerStatusActive)
	if err != nil {
		return nil, err
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeListener, string(created.ID), string(created.ProjectID),
		outboxActionCreated, listenerPayloadMap(created),
	); err != nil {
		return nil, fmt.Errorf("%w: outbox emit listener CREATED: %v", domain.ErrInternal, err)
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeLoadBalancer, string(in.lb.ID), string(in.lb.ProjectID),
		outboxActionUpdated, lbUpdatedPayloadMap(string(in.lb.ID), string(in.lb.ProjectID), string(in.lb.RegionID), "listener_created"),
	); err != nil {
		return nil, fmt.Errorf("%w: outbox emit lb UPDATED: %v", domain.ErrInternal, err)
	}
	// FGA-register-intent (creator + parent-link) в той же writer-tx —
	// register-drainer применяет через kacho-iam RegisterResource.
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
		listenerRegisterIntent(created, in.fgaOwner)); err != nil {
		return nil, fmt.Errorf("%w: fga register-intent emit: %v", domain.ErrInternal, err)
	}
	if err := w.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return created, nil
}

// vipAllocResult — outcome VIP-allocation branch.
type vipAllocResult struct {
	addressID string
	address   string
	byo       bool // если true — освобождать через ClearReference, иначе через FreeIP.
	owner     vpcclient.AddressOwner
}

// acquireVIP — branch:
//
//   - BYO: vpc.AddressService.Get → verify same project + matching
//     ip_version → vpc.InternalAddressService.SetReference atomic CAS.
//     Errors:
//     NotFound          → InvalidArgument "address <id> not found"
//     cross-project     → InvalidArgument фиксированный текст
//     ip_version mismatch → InvalidArgument фиксированный текст
//     used_by occupied  → FailedPrecondition фиксированный текст
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
		// Same-project guard (с фиксированным текстом text).
		if addr.ProjectID != string(l.ProjectID) {
			return vipAllocResult{}, fmt.Errorf("%w: address project_id does not match listener load_balancer project_id",
				domain.ErrInvalidArg)
		}
		// IP-version guard (с фиксированным текстом text).
		want := familyForIPVersion(l.IPVersion)
		if addr.Family != "" && addr.Family != want {
			return vipAllocResult{}, fmt.Errorf("%w: address ip_version %s does not match listener ip_version %s",
				domain.ErrInvalidArg, addr.Family, l.IPVersion)
		}
		// Used-by guard (с фиксированным текстом text). Idempotent re-attach to self
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
		// pool, cascade selector).
		resp, err := u.internalAddrs.AllocateExternalIP(ctx, vpcclient.AllocateExternalIPRequest{
			ProjectID: string(l.ProjectID),
			Name:      domain.ListenerAutoAddressName(l.ID),
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
		Name:      domain.ListenerAutoAddressName(l.ID),
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

// compensateCreate — best-effort rollback осиротевшей create-саги: освобождает
// VIP (если аллоцирован) и удаляет durable-handle строку. Auto-alloc → FreeIP
// (delete Address); BYO → ClearReference (Address tenant'а уцелел). Ошибки
// логируются, не пробрасываются (worker уже вернул исходную ошибку). Если
// процесс умирает раньше compensation — handle (CREATING) добивает free_ip_runner.
func (u *CreateUseCase) compensateCreate(ctx context.Context, listenerID domain.ResourceID, alloc vipAllocResult) {
	logger := loggerOrDiscard(u.logger).With("listener_id", string(listenerID))

	// 1. Release VIP, если он был аллоцирован (alloc.addressID непуст → acquireVIP
	//    завершился). Пустой → acquireVIP не дошёл до alloc, освобождать нечего.
	if u.internalAddrs != nil && alloc.addressID != "" {
		if alloc.byo {
			if err := u.internalAddrs.ClearReference(ctx, alloc.addressID, alloc.owner); err != nil {
				logger.Warn("listener.Create compensation ClearReference failed",
					"err", err, "address_id", alloc.addressID)
			}
		} else if err := u.internalAddrs.FreeIP(ctx, alloc.addressID, alloc.owner); err != nil {
			logger.Warn("listener.Create compensation FreeIP failed",
				"err", err, "address_id", alloc.addressID)
		}
	}

	// 2. Удаляем durable-handle строку. Идемпотентно (ErrNotFound → ok); сбой →
	//    handle добьёт free_ip_runner (VIP уже освобождён выше либо им же).
	if err := u.deleteHandle(ctx, string(listenerID)); err != nil {
		logger.Warn("listener.Create compensation delete handle failed; free_ip_runner will reconcile", "err", err)
	}
}

// deleteHandle — best-effort DELETE durable-handle строки в собственной TX.
func (u *CreateUseCase) deleteHandle(ctx context.Context, id string) error {
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
	if err := w.Listeners().Delete(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil // уже удалён — идемпотентно (defer Abort откатит пустую TX)
		}
		return err
	}
	if err := w.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// listenerRegisterIntent builds the FGA-register-intent for a created
// Listener:
//
//	<subject> #admin @lb_listener:<id>                                 (creator)
//	lb_network_load_balancer:<lb_id> #load_balancer @lb_listener:<id>  (parent-link)
//
// The creator tuple is skipped on empty subject (system-initiated). The listener
// resolves its project via the parent-link → LB hierarchy cascade, so it has no
// own project-hierarchy tuple (parity with the former emitHierarchyTuples).
//
// carry tenant labels + parent-project so
// kacho-iam feeds its resource_mirror (γ selector matchLabels / containment) —
// parity with lbMirrorIntent / tgMirrorIntent. Was a bare intent WITHOUT Labels,
// so label selectors did not match even a freshly created listener (double-bug).
// source_version is stamped by the outbox emitter from the DB clock in writer-tx.
func listenerRegisterIntent(l *kachorepo.ListenerRecord, subject string) domain.FGARegisterIntent {
	id := string(l.ID)
	var tuples []domain.FGATuple
	if subject != "" {
		tuples = append(tuples, domain.FGACreatorTuple(subject, domain.FGAObjectTypeListener, id))
	}
	tuples = append(tuples, domain.FGAParentLinkTuple(
		domain.FGAObjectTypeLoadBalancer, string(l.LoadBalancerID),
		domain.FGARelationLoadBalancer,
		domain.FGAObjectTypeListener, id,
	))
	return domain.FGARegisterIntent{
		Kind:            "Listener",
		ResourceID:      id,
		Tuples:          tuples,
		Labels:          domain.LabelsToMap(l.Labels),
		ParentProjectID: string(l.ProjectID),
	}
}

// listenerUnregisterIntent builds the FGA-unregister-intent (parent-link)
// for a deleted Listener (unregister parent-link; creator is left
// for IAM-side GC).
func listenerUnregisterIntent(listenerID, lbID string) domain.FGARegisterIntent {
	return domain.FGARegisterIntent{
		Kind:       "Listener",
		ResourceID: listenerID,
		Tuples: []domain.FGATuple{
			domain.FGAParentLinkTuple(
				domain.FGAObjectTypeLoadBalancer, lbID,
				domain.FGARelationLoadBalancer,
				domain.FGAObjectTypeListener, listenerID,
			),
		},
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
