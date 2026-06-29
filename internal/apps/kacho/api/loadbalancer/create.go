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
	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

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
	repo          Repo
	opsRepo       operations.Repo
	projectClient ProjectClient
	regionClient  RegionClient
	logger        *slog.Logger
}

// NewCreateLoadBalancerUseCase конструктор.
func NewCreateLoadBalancerUseCase(
	repo Repo, opsRepo operations.Repo,
	pc ProjectClient, rc RegionClient,
	logger *slog.Logger,
) *CreateLoadBalancerUseCase {
	if logger == nil {
		logger = slog.Default()
	}
	return &CreateLoadBalancerUseCase{
		repo: repo, opsRepo: opsRepo,
		projectClient: pc, regionClient: rc,
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
	if err := lb.Validate(); err != nil {
		// Validate возвращает coreerrors.InvalidArgument (gRPC-shaped). mapDomainErr
		// сохранит её as-is.
		return nil, mapDomainErr(err)
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
		return u.doCreate(workerCtx, lb, principal)
	})

	return &op, nil
}

// doCreate — async worker. Возвращает anypb.Any(NetworkLoadBalancer) при
// успехе либо gRPC-status error при failure (operations.runOn маппит его в
// Operation.Error).
func (u *CreateLoadBalancerUseCase) doCreate(
	ctx context.Context, lb domain.LoadBalancer, principal operations.Principal,
) (*anypb.Any, error) {
	// 1. Peer-check `project_id`.
	if u.projectClient != nil {
		if _, err := u.projectClient.Get(ctx, string(lb.ProjectID)); err != nil {
			return nil, peerErrToStatus(err, "project", string(lb.ProjectID))
		}
	}
	// 2. Peer-check `region_id`.
	if u.regionClient != nil {
		if _, err := u.regionClient.Get(ctx, string(lb.RegionID)); err != nil {
			return nil, peerErrToStatus(err, "region", string(lb.RegionID))
		}
	}

	// 3. Writer-TX: Insert + outbox-emit + Commit.
	w, err := u.repo.Writer(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer w.Abort()

	// Set Status to INACTIVE (trigger lb_status_recompute will adjust
	// to ACTIVE if listeners + attached TG arrive; default CREATING from builder
	// would block Start preconditions). Use INACTIVE as terminal Create state.
	lb.Status = domain.LBStatusInactive

	created, err := w.LoadBalancers().Insert(ctx, &lb)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Outbox().Emit(ctx,
		kachopg.OutboxResourceLoadBalancer, string(created.ID), string(created.ProjectID),
		kachopg.OutboxActionCreated, lbOutboxPayload(created),
	); err != nil {
		return nil, mapDomainErr(err)
	}
	// FGA-register-intent (project-hierarchy + creator) in the SAME tx as
	// the Insert — durable, applied async by the register-drainer (
	// Вариант A; replaces the former best-effort post-commit fgawrite, Issue N5).
	if err := w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
		lbRegisterIntent(created, principal)); err != nil {
		return nil, mapDomainErr(err)
	}
	if err := w.Commit(); err != nil {
		return nil, mapDomainErr(err)
	}

	// 4. Marshal response.
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
