// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"
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
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// CreateUseCase инициирует создание Listener'а.
//
// VIP консолидирован на LoadBalancer (anycast active-active): листенер — «порт на
// VIP LB», собственной аллокации адреса больше не делает. Поэтому Create —
// чистый INSERT строки-листенера (FK на LB), без acquireVIP-саги и без обращения
// к vpc.
//
// Sync (handler-thread, до возврата Operation клиенту):
//  1. Required: load_balancer_id.
//  2. LB.Get (same project + status != DELETING) — NotFound иначе.
//  3. domain.Listener builder + Validate (name regex, port range, protocol, labels).
//  4. opsRepo.CreateWithPrincipal(op, principal).
//  5. operations.Run(callerCtx, opsRepo, op.ID, worker) — fire-and-trigger.
//
// Async worker — одна writer-TX (внешнего side-effect нет):
//
//	INSERT listener (status='ACTIVE') + outbox `nlb_listener:<id> CREATED` +
//	`nlb_load_balancer:<lb_id> UPDATED` + FGA-register-intent (creator +
//	parent-link). Триггер lb_status_recompute переводит LB INACTIVE→ACTIVE, если
//	теперь есть листенер И attached TG.
type CreateUseCase struct {
	repo    RepoFactory
	opsRepo OperationsRepo
	logger  *slog.Logger
}

// NewCreateUseCase — конструктор. Зависимости — port-интерфейсы (composition
// root wires в `cmd/kacho-loadbalancer/main.go`). logger допускается nil.
func NewCreateUseCase(
	repo RepoFactory,
	opsRepo OperationsRepo,
	logger *slog.Logger,
) *CreateUseCase {
	return &CreateUseCase{
		repo:    repo,
		opsRepo: opsRepo,
		logger:  logger,
	}
}

// Run — sync validation + Operation creation + async worker spawn. Возвращает
// Operation клиенту до завершения worker'а; клиент поллит OperationService.Get.
func (u *CreateUseCase) Run(ctx context.Context, req *lbv1.CreateListenerRequest) (*operations.Operation, error) {
	lbID := req.GetLoadBalancerId()
	if lbID == "" {
		return nil, status.Error(codes.InvalidArgument, "load_balancer_id required")
	}

	// Sync read parent LB. Verifies LB exists, not DELETING; пробрасывает
	// project_id/region_id для denormalisation и семейства для vestigial ip_version.
	lb, err := u.fetchParentLB(ctx, lbID)
	if err != nil {
		return nil, err
	}

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
		listenerIPVersion(lb.LoadBalancer),
	)
	listener.Description = domain.LbDescription(req.GetDescription())
	listener.Labels = domain.LabelsFromMap(req.GetLabels())
	listener.ProxyProtocolV2 = req.GetProxyProtocolV2()
	if tgID := req.GetDefaultTargetGroupId(); tgID != "" {
		listener.DefaultTargetGroupID = option.MustNewOption(domain.ResourceID(tgID))
	}
	// VIP-only LB-консолидация: листенер не аллоцирует адрес, поэтому терминальное
	// состояние Create — сразу ACTIVE (durable-handle/CREATING-фаза не нужна).
	listener.Status = domain.ListenerStatusActive

	if err := listener.Validate(); err != nil {
		return nil, err
	}

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

	in := createInput{
		listener: listener,
		lb:       lb,
		// Acting subject FGA-id inline (parity с loadbalancer/targetgroup):
		// `<type>:<id>` либо "" для anonymous/system (creator-tuple пропускается).
		fgaOwner: domain.FGASubjectFromPrincipal(principal.Type, principal.ID),
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

// fetchParentLB — sync Get parent LB. NotFound — LB не существует;
// FailedPrecondition — LB.Status == DELETING; Internal — repo failure.
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

// buildDomainName — обёртка над domain.LbName с верхним sync-маппингом proto
// → domain newtype. Возвращает gRPC InvalidArgument из Validate.
func buildDomainName(raw string) (domain.LbName, error) {
	n := domain.LbName(raw)
	if err := n.Validate(); err != nil {
		return "", err
	}
	return n, nil
}

// listenerIPVersion — вестигиальное значение для NOT NULL колонки listeners.ip_version
// (снята с proto-листенера; колонка удаляется поздней миграцией). Берётся первое
// семейство родительского LB, иначе IPV4 — листенер обслуживает VIP LB всех его
// семейств одновременно.
func listenerIPVersion(lb domain.LoadBalancer) domain.IPVersion {
	for _, f := range lb.IPFamilies {
		if f == domain.IPVersionV4 || f == domain.IPVersionV6 {
			return f
		}
	}
	return domain.IPVersionV4
}

// createInput — snapshot входов для async worker.
type createInput struct {
	listener domain.Listener
	lb       *parentLB
	fgaOwner string
}

// doCreate — worker: одна writer-TX (внешнего side-effect нет). INSERT листенера
// (status='ACTIVE') + outbox CREATED + LB UPDATED + FGA-register-intent (creator +
// parent-link). Триггер lb_status_recompute сам переводит LB INACTIVE→ACTIVE при
// has_listener AND has_attached. Возвращает anypb.Any(Listener) при успехе.
func (u *CreateUseCase) doCreate(ctx context.Context, in createInput) (*anypb.Any, error) {
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	committed := false
	defer func() {
		if !committed {
			w.Abort()
		}
	}()

	listener := in.listener
	created, err := w.Listeners().Insert(ctx, &listener)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeListener, string(created.ID), string(created.ProjectID),
		outboxActionCreated, listenerPayloadMap(created),
	); err != nil {
		return nil, mapDomainErr(fmt.Errorf("%w: outbox emit listener CREATED: %v", domain.ErrInternal, err))
	}
	if err := w.Outbox().Emit(ctx,
		outboxResourceTypeLoadBalancer, string(in.lb.ID), string(in.lb.ProjectID),
		outboxActionUpdated, lbUpdatedPayloadMap(string(in.lb.ID), string(in.lb.ProjectID), string(in.lb.RegionID), "listener_created"),
	); err != nil {
		return nil, mapDomainErr(fmt.Errorf("%w: outbox emit lb UPDATED: %v", domain.ErrInternal, err))
	}
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
		listenerRegisterIntent(created, in.fgaOwner)); err != nil {
		return nil, mapDomainErr(fmt.Errorf("%w: fga register-intent emit: %v", domain.ErrInternal, err))
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}
	committed = true

	return marshalListener(created)
}

// listenerRegisterIntent — FGA-register-intent для созданного Listener:
//
//	<subject> #admin @lb_listener:<id>                                 (creator)
//	lb_network_load_balancer:<lb_id> #load_balancer @lb_listener:<id>  (parent-link)
//
// creator-tuple пропускается на пустом subject (system-initiated). Листенер
// резолвит проект через parent-link → LB-иерархию (своего project-tuple нет).
// Несёт labels + parent-project, чтобы kacho-iam обновил resource_mirror
// (γ-селекторы matchLabels). source_version штампует outbox-emitter из DB-clock.
func listenerRegisterIntent(l *kachorepo.ListenerRecord, subject string) domain.FGARegisterIntent {
	id := string(l.ID)
	// project-tuple идёт ПЕРВЫМ — он даёт видимость Listener через project (как у
	// LoadBalancer/TargetGroup: reconciler материализует v_*-relation по
	// parent-project). Дренер применяет tuples по порядку и short-circuit'ит на
	// первом отказе, а creator (relation admin) и parent-link (load_balancer)
	// iam-proxy отвергает (allowedProxyRelations = {project, account, parent,
	// owner}). Раньше первым шёл creator(admin) → падал сразу → НИ ОДИН tuple не
	// применялся → Listener был невидим в authz-filtered List. Теперь project-
	// tuple успевает примениться до отказа admin — Listener виден.
	tuples := []domain.FGATuple{
		domain.FGAProjectTuple(domain.FGAObjectTypeListener, id, string(l.ProjectID)),
	}
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

// listenerUnregisterIntent — FGA-unregister-intent (parent-link) для удалённого
// Listener (creator оставляется IAM-side GC).
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
