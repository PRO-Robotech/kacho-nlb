// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/geo"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	// dto/type2pb init-registrations — handler-слой строит proto через DTO-реестр.
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ---- Fake Repo (in-memory) -------------------------------------------------
//
// Минимальная in-memory реализация `kacho.Repository` для unit-тестов
// use-case'ов. Скоуп — только методы, реально вызываемые use-case'ами LB
// (Insert, Get, List, Update, SetStatusCAS, MoveProject, Delete, HasListeners,
// HasAttachedTargetGroups, AttachedTargetGroups.Attach/Detach, TargetGroups
// limited Get/ListTargets). Listeners/TargetGroups reader методы — пока stub
// (panic on call) — unit-тесты per-RPC выборочно их активируют.

type fakeRepo struct {
	mu     sync.Mutex
	lbs    map[string]*kachorepo.LoadBalancerRecord
	tgs    map[string]*kachorepo.TargetGroupRecord
	lists  map[string][]*kachorepo.ListenerRecord
	pivot  map[string]*kachorepo.AttachedTargetGroupRecord // key=lbID+"/"+tgID
	outbox []outboxEvent
	fga    []fgaIntentEvent // FGARegisterOutbox intents (flushed on Commit)
	// Knobs for fault injection.
	failOnInsert    error
	failOnUpdate    error
	failOnDelete    error
	failOnSetStatus error
	failOnAttachVIP error
	failOnMove      error
	failOnAttach    error
	failOnList      error
	failOnGet       error
	failOnOutbox    error
	preCommitHook   func() error
}

type outboxEvent struct {
	ResourceType string
	ResourceID   string
	ProjectID    string
	Action       string
	Payload      map[string]any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		lbs:   make(map[string]*kachorepo.LoadBalancerRecord),
		tgs:   make(map[string]*kachorepo.TargetGroupRecord),
		lists: make(map[string][]*kachorepo.ListenerRecord),
		pivot: make(map[string]*kachorepo.AttachedTargetGroupRecord),
	}
}

// outboxEvents возвращает копию emit-журнала.
func (r *fakeRepo) outboxEvents() []outboxEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]outboxEvent, len(r.outbox))
	copy(out, r.outbox)
	return out
}

func (r *fakeRepo) Reader(ctx context.Context) (kachorepo.RepositoryReader, error) {
	return &fakeReader{r: r}, nil
}

func (r *fakeRepo) Writer(ctx context.Context) (kachorepo.RepositoryWriter, error) {
	return &fakeWriter{r: r}, nil
}

func (r *fakeRepo) Close() {}

// fakeReader implements RepositoryReader (sync read-snapshot).
type fakeReader struct {
	r      *fakeRepo
	closed bool
}

func (rd *fakeReader) LoadBalancers() kachorepo.LoadBalancerReaderIface {
	return &fakeLBReader{r: rd.r}
}
func (rd *fakeReader) Listeners() kachorepo.ListenerReaderIface {
	return &fakeListenerReader{r: rd.r}
}
func (rd *fakeReader) TargetGroups() kachorepo.TargetGroupReaderIface {
	return &fakeTGReader{r: rd.r}
}
func (rd *fakeReader) AttachedTargetGroups() kachorepo.AttachedTargetGroupReaderIface {
	return &fakeATGReader{r: rd.r}
}
func (rd *fakeReader) Close() error { rd.closed = true; return nil }

// fakeWriter implements RepositoryWriter (RW TX with explicit Commit/Abort).
type fakeWriter struct {
	r         *fakeRepo
	committed bool
	aborted   bool
	// pending mutations recorded until Commit.
	pendingLBs          []*kachorepo.LoadBalancerRecord
	pendingPivots       []*kachorepo.AttachedTargetGroupRecord
	pendingDeletes      []string // LB ids
	pendingPivotDeletes []string // "lb/tg"
	pendingOutbox       []outboxEvent
	pendingFGA          []fgaIntentEvent
}

// fgaIntentEvent records one FGARegisterOutbox.Emit  for assertions.
type fgaIntentEvent struct {
	EventType string
	Intent    domain.FGARegisterIntent
}

func (w *fakeWriter) LoadBalancers() kachorepo.LoadBalancerWriterIface {
	return &fakeLBWriter{w: w}
}
func (w *fakeWriter) Listeners() kachorepo.ListenerWriterIface {
	return &fakeListenerWriter{w: w}
}
func (w *fakeWriter) TargetGroups() kachorepo.TargetGroupWriterIface {
	return &fakeTGWriter{w: w}
}
func (w *fakeWriter) AttachedTargetGroups() kachorepo.AttachedTargetGroupWriterIface {
	return &fakeATGWriter{w: w}
}
func (w *fakeWriter) Outbox() kachorepo.OutboxEmitter {
	return &fakeOutbox{w: w}
}
func (w *fakeWriter) FGARegisterOutbox() kachorepo.FGARegisterEmitter {
	return &fakeFGARegisterOutbox{w: w}
}

func (w *fakeWriter) Commit() error {
	if w.committed || w.aborted {
		return nil
	}
	w.committed = true
	if w.r.preCommitHook != nil {
		if err := w.r.preCommitHook(); err != nil {
			return err
		}
	}
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	for _, lb := range w.pendingLBs {
		w.r.lbs[string(lb.ID)] = lb
	}
	for _, p := range w.pendingPivots {
		w.r.pivot[p.LoadBalancerID+"/"+p.TargetGroupID] = p
	}
	for _, id := range w.pendingDeletes {
		delete(w.r.lbs, id)
	}
	for _, k := range w.pendingPivotDeletes {
		delete(w.r.pivot, k)
	}
	w.r.outbox = append(w.r.outbox, w.pendingOutbox...)
	w.r.fga = append(w.r.fga, w.pendingFGA...)
	return nil
}

func (w *fakeWriter) Abort() {
	if w.committed || w.aborted {
		return
	}
	w.aborted = true
}

// ---- LoadBalancers ----

type fakeLBReader struct{ r *fakeRepo }

func (q *fakeLBReader) Get(ctx context.Context, id string) (*kachorepo.LoadBalancerRecord, error) {
	if q.r.failOnGet != nil {
		return nil, q.r.failOnGet
	}
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	if lb, ok := q.r.lbs[id]; ok {
		copyLb := *lb
		return &copyLb, nil
	}
	return nil, fmt.Errorf("%w: NetworkLoadBalancer %s not found", kachorepo.ErrNotFound, id)
}

func (q *fakeLBReader) List(ctx context.Context, f kachorepo.LoadBalancerFilter, p kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	if q.r.failOnList != nil {
		return nil, "", q.r.failOnList
	}
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	// RBAC: per-object FGA allow-set push-down (parity с pg repo).
	// nil → no filter; len==0 → пусто (no-leak); len>0 → id ∈ AllowedIDs.
	var allowed map[string]struct{}
	if f.AllowedIDs != nil {
		if len(f.AllowedIDs) == 0 {
			return nil, "", nil
		}
		allowed = make(map[string]struct{}, len(f.AllowedIDs))
		for _, id := range f.AllowedIDs {
			allowed[id] = struct{}{}
		}
	}
	var out []*kachorepo.LoadBalancerRecord
	for _, lb := range q.r.lbs {
		if f.ProjectID != "" && string(lb.ProjectID) != f.ProjectID {
			continue
		}
		if f.Name != "" && string(lb.Name) != f.Name {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[string(lb.ID)]; !ok {
				continue
			}
		}
		c := *lb
		out = append(out, &c)
	}
	return out, "", nil
}

func (q *fakeLBReader) ListByProject(ctx context.Context, projectID string, p kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return q.List(ctx, kachorepo.LoadBalancerFilter{ProjectID: projectID}, p)
}

func (q *fakeLBReader) HasListeners(ctx context.Context, lbID string) (bool, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	return len(q.r.lists[lbID]) > 0, nil
}

func (q *fakeLBReader) HasAttachedTargetGroups(ctx context.Context, lbID string) (bool, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	for k := range q.r.pivot {
		if len(k) > len(lbID) && k[:len(lbID)] == lbID && k[len(lbID)] == '/' {
			return true, nil
		}
	}
	return false, nil
}

type fakeLBWriter struct{ w *fakeWriter }

func (q *fakeLBWriter) Get(ctx context.Context, id string) (*kachorepo.LoadBalancerRecord, error) {
	return (&fakeLBReader{r: q.w.r}).Get(ctx, id)
}
func (q *fakeLBWriter) List(ctx context.Context, f kachorepo.LoadBalancerFilter, p kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return (&fakeLBReader{r: q.w.r}).List(ctx, f, p)
}
func (q *fakeLBWriter) ListByProject(ctx context.Context, projectID string, p kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return q.List(ctx, kachorepo.LoadBalancerFilter{ProjectID: projectID}, p)
}
func (q *fakeLBWriter) HasListeners(ctx context.Context, lbID string) (bool, error) {
	return (&fakeLBReader{r: q.w.r}).HasListeners(ctx, lbID)
}
func (q *fakeLBWriter) HasAttachedTargetGroups(ctx context.Context, lbID string) (bool, error) {
	return (&fakeLBReader{r: q.w.r}).HasAttachedTargetGroups(ctx, lbID)
}

func (q *fakeLBWriter) Insert(ctx context.Context, lb *domain.LoadBalancer) (*kachorepo.LoadBalancerRecord, error) {
	if q.w.r.failOnInsert != nil {
		return nil, q.w.r.failOnInsert
	}
	q.w.r.mu.Lock()
	if _, ok := q.w.r.lbs[string(lb.ID)]; ok {
		q.w.r.mu.Unlock()
		return nil, fmt.Errorf("%w: NetworkLoadBalancer with name already exists", kachorepo.ErrAlreadyExists)
	}
	for _, existing := range q.w.r.lbs {
		if string(existing.ProjectID) == string(lb.ProjectID) && string(existing.Name) == string(lb.Name) && string(lb.Name) != "" {
			q.w.r.mu.Unlock()
			return nil, fmt.Errorf("%w: NetworkLoadBalancer with name already exists", kachorepo.ErrAlreadyExists)
		}
	}
	q.w.r.mu.Unlock()
	rec := &kachorepo.LoadBalancerRecord{LoadBalancer: *lb}
	q.w.pendingLBs = append(q.w.pendingLBs, rec)
	return rec, nil
}

func (q *fakeLBWriter) Update(ctx context.Context, lb *domain.LoadBalancer) (*kachorepo.LoadBalancerRecord, error) {
	if q.w.r.failOnUpdate != nil {
		return nil, q.w.r.failOnUpdate
	}
	q.w.r.mu.Lock()
	cur, ok := q.w.r.lbs[string(lb.ID)]
	q.w.r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: NetworkLoadBalancer %s not found", kachorepo.ErrNotFound, lb.ID)
	}
	cur.Name = lb.Name
	cur.Description = lb.Description
	cur.Labels = lb.Labels
	cur.DeletionProtection = lb.DeletionProtection
	cur.SessionAffinity = lb.SessionAffinity
	cur.DisabledAnnounceZones = lb.DisabledAnnounceZones
	c := *cur
	q.w.pendingLBs = append(q.w.pendingLBs, &c)
	return &c, nil
}

func (q *fakeLBWriter) SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.LBStatus) (*kachorepo.LoadBalancerRecord, error) {
	if q.w.r.failOnSetStatus != nil {
		return nil, q.w.r.failOnSetStatus
	}
	q.w.r.mu.Lock()
	defer q.w.r.mu.Unlock()
	cur, ok := q.w.r.lbs[id]
	if !ok {
		return nil, fmt.Errorf("%w: NetworkLoadBalancer %s not found", kachorepo.ErrNotFound, id)
	}
	if cur.Status != expected {
		return nil, fmt.Errorf("%w: LoadBalancer %s status is not %s", kachorepo.ErrFailedPrecondition, id, expected)
	}
	cur.Status = newStatus
	c := *cur
	q.w.pendingLBs = append(q.w.pendingLBs, &c)
	// Pending hasn't been applied yet — for fake purpose, also update in-place
	// so subsequent SetStatusCAS in same writer sees new state.
	*cur = c
	return &c, nil
}

func (q *fakeLBWriter) AttachVIP(ctx context.Context, id string, family domain.IPVersion, address, addressID string, origin domain.VipOrigin) (*kachorepo.LoadBalancerRecord, error) {
	if q.w.r.failOnAttachVIP != nil {
		return nil, q.w.r.failOnAttachVIP
	}
	q.w.r.mu.Lock()
	defer q.w.r.mu.Unlock()
	cur, ok := q.w.r.lbs[id]
	if !ok {
		return nil, fmt.Errorf("%w: NetworkLoadBalancer %s not found", kachorepo.ErrNotFound, id)
	}
	// CAS-attach: семейство свободно ИЛИ уже несёт тот же адрес (идемпотентность
	// retry). Иначе — single-VIP-per-LB conflict → FailedPrecondition.
	var existing string
	switch family {
	case domain.IPVersionV4:
		existing = string(cur.AddressV4)
	case domain.IPVersionV6:
		existing = string(cur.AddressV6)
	default:
		return nil, fmt.Errorf("%w: unsupported ip family %q", kachorepo.ErrInvalidArg, family)
	}
	if existing != "" && existing != address {
		return nil, fmt.Errorf("%w: load balancer already has an address for this family", kachorepo.ErrFailedPrecondition)
	}
	switch family {
	case domain.IPVersionV4:
		cur.AddressV4 = domain.IPAddress(address)
		cur.AddressIDV4 = domain.AddressID(addressID)
		cur.VipOriginV4 = origin
	case domain.IPVersionV6:
		cur.AddressV6 = domain.IPAddress(address)
		cur.AddressIDV6 = domain.AddressID(addressID)
		cur.VipOriginV6 = origin
	}
	c := *cur
	q.w.pendingLBs = append(q.w.pendingLBs, &c)
	*cur = c
	return &c, nil
}

func (q *fakeLBWriter) MoveProject(ctx context.Context, id, newProjectID string) (*kachorepo.LoadBalancerRecord, error) {
	if q.w.r.failOnMove != nil {
		return nil, q.w.r.failOnMove
	}
	q.w.r.mu.Lock()
	defer q.w.r.mu.Unlock()
	cur, ok := q.w.r.lbs[id]
	if !ok {
		return nil, fmt.Errorf("%w: NetworkLoadBalancer %s not found", kachorepo.ErrNotFound, id)
	}
	cur.ProjectID = domain.ProjectID(newProjectID)
	c := *cur
	q.w.pendingLBs = append(q.w.pendingLBs, &c)
	return &c, nil
}

func (q *fakeLBWriter) Delete(ctx context.Context, id string) error {
	if q.w.r.failOnDelete != nil {
		return q.w.r.failOnDelete
	}
	q.w.r.mu.Lock()
	defer q.w.r.mu.Unlock()
	if _, ok := q.w.r.lbs[id]; !ok {
		return fmt.Errorf("%w: NetworkLoadBalancer %s not found", kachorepo.ErrNotFound, id)
	}
	q.w.pendingDeletes = append(q.w.pendingDeletes, id)
	return nil
}

// ---- Listeners (stub) ----

type fakeListenerReader struct{ r *fakeRepo }

func (q *fakeListenerReader) Get(ctx context.Context, id string) (*kachorepo.ListenerRecord, error) {
	return nil, fmt.Errorf("%w: Listener %s not found", kachorepo.ErrNotFound, id)
}
func (q *fakeListenerReader) List(ctx context.Context, f kachorepo.ListenerFilter, p kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	return nil, "", nil
}
func (q *fakeListenerReader) ListByLB(ctx context.Context, lbID string, p kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	ls := q.r.lists[lbID]
	out := make([]*kachorepo.ListenerRecord, len(ls))
	copy(out, ls)
	return out, "", nil
}

type fakeListenerWriter struct{ w *fakeWriter }

func (q *fakeListenerWriter) Get(ctx context.Context, id string) (*kachorepo.ListenerRecord, error) {
	return (&fakeListenerReader{r: q.w.r}).Get(ctx, id)
}
func (q *fakeListenerWriter) List(ctx context.Context, f kachorepo.ListenerFilter, p kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	return nil, "", nil
}
func (q *fakeListenerWriter) ListByLB(ctx context.Context, lbID string, p kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	return (&fakeListenerReader{r: q.w.r}).ListByLB(ctx, lbID, p)
}
func (q *fakeListenerWriter) Insert(ctx context.Context, l *domain.Listener) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeListenerWriter) Update(ctx context.Context, l *domain.Listener) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeListenerWriter) SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.ListenerStatus) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeListenerWriter) SetAllocatedAddress(ctx context.Context, id, address string) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeListenerWriter) SetVIP(ctx context.Context, id, addressID, allocatedAddress string) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeListenerWriter) MoveProject(ctx context.Context, lbID, newProjectID string) (int64, error) {
	return 0, nil
}
func (q *fakeListenerWriter) Delete(ctx context.Context, id string) error {
	return errors.New("not implemented in fake")
}

// ---- TargetGroups (limited) ----

type fakeTGReader struct{ r *fakeRepo }

func (q *fakeTGReader) Get(ctx context.Context, id string) (*kachorepo.TargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	tg, ok := q.r.tgs[id]
	if !ok {
		return nil, fmt.Errorf("%w: TargetGroup %s not found", kachorepo.ErrNotFound, id)
	}
	c := *tg
	return &c, nil
}
func (q *fakeTGReader) List(ctx context.Context, f kachorepo.TargetGroupFilter, p kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return nil, "", nil
}
func (q *fakeTGReader) ListByProject(ctx context.Context, projectID string, p kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return nil, "", nil
}
func (q *fakeTGReader) ListTargets(ctx context.Context, tgID string) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}
func (q *fakeTGReader) ListDrainingExpired(ctx context.Context, tgID string, delaySeconds int32) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}
func (q *fakeTGReader) HasAttachedLB(ctx context.Context, tgID string) (bool, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	for k := range q.r.pivot {
		if len(k) > len(tgID)+1 && k[len(k)-len(tgID):] == tgID {
			return true, nil
		}
	}
	return false, nil
}

type fakeTGWriter struct{ w *fakeWriter }

func (q *fakeTGWriter) Get(ctx context.Context, id string) (*kachorepo.TargetGroupRecord, error) {
	return (&fakeTGReader{r: q.w.r}).Get(ctx, id)
}
func (q *fakeTGWriter) List(ctx context.Context, f kachorepo.TargetGroupFilter, p kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return nil, "", nil
}
func (q *fakeTGWriter) ListByProject(ctx context.Context, projectID string, p kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return nil, "", nil
}
func (q *fakeTGWriter) ListTargets(ctx context.Context, tgID string) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}
func (q *fakeTGWriter) ListDrainingExpired(ctx context.Context, tgID string, delaySeconds int32) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}
func (q *fakeTGWriter) HasAttachedLB(ctx context.Context, tgID string) (bool, error) {
	return (&fakeTGReader{r: q.w.r}).HasAttachedLB(ctx, tgID)
}
func (q *fakeTGWriter) Insert(ctx context.Context, tg *domain.TargetGroup) (*kachorepo.TargetGroupRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeTGWriter) Update(ctx context.Context, tg *domain.TargetGroup) (*kachorepo.TargetGroupRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeTGWriter) SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.TargetGroupStatus) (*kachorepo.TargetGroupRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeTGWriter) MoveProject(ctx context.Context, id, newProjectID string) (*kachorepo.TargetGroupRecord, error) {
	return nil, errors.New("not implemented in fake")
}
func (q *fakeTGWriter) AddTargets(ctx context.Context, tgID string, targets []domain.Target) (int, error) {
	return 0, errors.New("not implemented in fake")
}
func (q *fakeTGWriter) RemoveTargetsMarkDraining(ctx context.Context, tgID string, targetIDs []string) (int, error) {
	return 0, errors.New("not implemented in fake")
}
func (q *fakeTGWriter) DeleteTargetsDrained(ctx context.Context, tgID string, delaySeconds int32) (int, error) {
	return 0, nil
}
func (q *fakeTGWriter) Delete(ctx context.Context, id string) error {
	return errors.New("not implemented in fake")
}

// ---- AttachedTGs ----

type fakeATGReader struct{ r *fakeRepo }

func (q *fakeATGReader) Get(ctx context.Context, lbID, tgID string) (*kachorepo.AttachedTargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	if v, ok := q.r.pivot[lbID+"/"+tgID]; ok {
		c := *v
		return &c, nil
	}
	return nil, fmt.Errorf("%w: AttachedTargetGroup %s/%s not found", kachorepo.ErrNotFound, lbID, tgID)
}
func (q *fakeATGReader) ListByLB(ctx context.Context, lbID string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	var out []*kachorepo.AttachedTargetGroupRecord
	prefix := lbID + "/"
	for k, v := range q.r.pivot {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			c := *v
			out = append(out, &c)
		}
	}
	return out, nil
}
func (q *fakeATGReader) ListByTG(ctx context.Context, tgID string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	var out []*kachorepo.AttachedTargetGroupRecord
	suffix := "/" + tgID
	for k, v := range q.r.pivot {
		if len(k) > len(suffix) && k[len(k)-len(suffix):] == suffix {
			c := *v
			out = append(out, &c)
		}
	}
	return out, nil
}

type fakeATGWriter struct{ w *fakeWriter }

func (q *fakeATGWriter) Get(ctx context.Context, lbID, tgID string) (*kachorepo.AttachedTargetGroupRecord, error) {
	return (&fakeATGReader{r: q.w.r}).Get(ctx, lbID, tgID)
}
func (q *fakeATGWriter) ListByLB(ctx context.Context, lbID string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	return (&fakeATGReader{r: q.w.r}).ListByLB(ctx, lbID)
}
func (q *fakeATGWriter) ListByTG(ctx context.Context, tgID string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	return (&fakeATGReader{r: q.w.r}).ListByTG(ctx, tgID)
}
func (q *fakeATGWriter) Attach(ctx context.Context, lbID, tgID string, priority int32) (*kachorepo.AttachedTargetGroupRecord, bool, error) {
	if q.w.r.failOnAttach != nil {
		return nil, false, q.w.r.failOnAttach
	}
	q.w.r.mu.Lock()
	defer q.w.r.mu.Unlock()
	key := lbID + "/" + tgID
	if existing, ok := q.w.r.pivot[key]; ok {
		c := *existing
		return &c, false, nil
	}
	rec := &kachorepo.AttachedTargetGroupRecord{
		LoadBalancerID: lbID, TargetGroupID: tgID, Priority: priority,
	}
	q.w.pendingPivots = append(q.w.pendingPivots, rec)
	return rec, true, nil
}
func (q *fakeATGWriter) Detach(ctx context.Context, lbID, tgID string) error {
	q.w.r.mu.Lock()
	defer q.w.r.mu.Unlock()
	q.w.pendingPivotDeletes = append(q.w.pendingPivotDeletes, lbID+"/"+tgID)
	return nil
}

// ---- Outbox ----

type fakeOutbox struct{ w *fakeWriter }

func (o *fakeOutbox) Emit(ctx context.Context, resourceType, resourceID, projectID, action string, payload map[string]any) error {
	if o.w.r.failOnOutbox != nil {
		return o.w.r.failOnOutbox
	}
	o.w.pendingOutbox = append(o.w.pendingOutbox, outboxEvent{
		ResourceType: resourceType, ResourceID: resourceID, ProjectID: projectID,
		Action: action, Payload: payload,
	})
	return nil
}

// ---- Peer client fakes -----------------------------------------------------

type fakeProjectClient struct {
	getFunc func(ctx context.Context, projectID string) (*iam.Project, error)
}

func (f *fakeProjectClient) Get(ctx context.Context, projectID string) (*iam.Project, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, projectID)
	}
	return &iam.Project{ID: projectID, Name: "fake-project", Status: "ACTIVE"}, nil
}

// fakeCheckClient — in-memory CheckClient (iam.InternalIAMService.Check) двойник.
type fakeCheckClient struct {
	allowed     bool
	err         error
	calls       int
	gotSubject  string
	gotRelation string
	gotObject   string
}

func (f *fakeCheckClient) Check(_ context.Context, subject, relation, object string) (bool, error) {
	f.calls++
	f.gotSubject, f.gotRelation, f.gotObject = subject, relation, object
	if f.err != nil {
		return false, f.err
	}
	return f.allowed, nil
}

type fakeRegionClient struct {
	getFunc func(ctx context.Context, regionID string) (*geo.Region, error)
}

func (f *fakeRegionClient) Get(ctx context.Context, regionID string) (*geo.Region, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, regionID)
	}
	return &geo.Region{ID: regionID, Name: "fake-region"}, nil
}

// fakeZoneClient — двойник geo.ZoneClient для валидации disabled_announce_zones
// и деривации underlying-зоны public-VIP. Дефолт — регион имеет зоны
// region-1-a/region-1-b; zones/listFunc подменяют под negative/edge-сценарии.
type fakeZoneClient struct {
	zones    []string // зоны региона (дефолт: region-1-a, region-1-b)
	listFunc func(ctx context.Context, regionID string) ([]string, error)
}

func (f *fakeZoneClient) ListZoneIDsInRegion(ctx context.Context, regionID string) ([]string, error) {
	if f.listFunc != nil {
		return f.listFunc(ctx, regionID)
	}
	if f.zones != nil {
		return append([]string(nil), f.zones...), nil
	}
	return []string{"region-1-a", "region-1-b"}, nil
}

// recordedAlloc — запись одного auto-alloc вызова (с семейством, т.к. семейство
// теперь задаётся выбором метода AllocateInternalIP/AllocateInternalIPv6, а не полем).
type recordedAlloc struct {
	req    vpcclient.AllocateInternalIPRequest
	family string // vpcclient.AddressFamilyIPv4 | AddressFamilyIPv6
}

// fakeAddressClient — двойник vpc InternalAddressClient (узкий VIP-lifecycle port)
// для per-family fan-out саги. Дефолт: auto (AllocateInternalIP/IPv6) и byo
// (AttachExisting) возвращают детерминированный адрес; frees/clears записываются
// для assert'ов compensation/Delete. allocFunc/byoFunc подменяют под negative-
// сценарии (подсеть исчерпана, BYO mismatch).
type fakeAddressClient struct {
	mu         sync.Mutex
	allocFunc  func(ctx context.Context, req vpcclient.AllocateInternalIPRequest, family string) (*vpcclient.AllocateResponse, error)
	extAllocFn func(ctx context.Context, req vpcclient.AllocateExternalIPRequest, family string) (*vpcclient.AllocateResponse, error)
	byoFunc    func(ctx context.Context, req vpcclient.AttachExistingRequest) (*vpcclient.AllocateResponse, error)
	allocReqs  []recordedAlloc
	extReqs    []vpcclient.AllocateExternalIPRequest
	byoReqs    []vpcclient.AttachExistingRequest
	freed      []string
	cleared    []string
	seq        int
}

func (f *fakeAddressClient) AllocateInternalIP(ctx context.Context, req vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return f.alloc(ctx, req, vpcclient.AddressFamilyIPv4)
}

func (f *fakeAddressClient) AllocateInternalIPv6(ctx context.Context, req vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return f.alloc(ctx, req, vpcclient.AddressFamilyIPv6)
}

func (f *fakeAddressClient) AllocateExternalIP(ctx context.Context, req vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return f.extAlloc(ctx, req, vpcclient.AddressFamilyIPv4)
}

func (f *fakeAddressClient) AllocateExternalIPv6(ctx context.Context, req vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	return f.extAlloc(ctx, req, vpcclient.AddressFamilyIPv6)
}

func (f *fakeAddressClient) extAlloc(ctx context.Context, req vpcclient.AllocateExternalIPRequest, family string) (*vpcclient.AllocateResponse, error) {
	f.mu.Lock()
	f.extReqs = append(f.extReqs, req)
	f.seq++
	seq := f.seq
	f.mu.Unlock()
	if f.extAllocFn != nil {
		return f.extAllocFn(ctx, req, family)
	}
	prefix := "203.0.113."
	if family == vpcclient.AddressFamilyIPv6 {
		prefix = "2001:db8::"
	}
	return &vpcclient.AllocateResponse{
		AddressID: fmt.Sprintf("adr%017d", seq),
		Value:     fmt.Sprintf("%s%d", prefix, seq),
	}, nil
}

func (f *fakeAddressClient) alloc(ctx context.Context, req vpcclient.AllocateInternalIPRequest, family string) (*vpcclient.AllocateResponse, error) {
	f.mu.Lock()
	f.allocReqs = append(f.allocReqs, recordedAlloc{req: req, family: family})
	f.seq++
	seq := f.seq
	f.mu.Unlock()
	if f.allocFunc != nil {
		return f.allocFunc(ctx, req, family)
	}
	prefix := "10.0.0."
	if family == vpcclient.AddressFamilyIPv6 {
		prefix = "fd00::"
	}
	return &vpcclient.AllocateResponse{
		AddressID: fmt.Sprintf("adr%017d", seq),
		Value:     fmt.Sprintf("%s%d", prefix, seq),
	}, nil
}

func (f *fakeAddressClient) AttachExisting(ctx context.Context, req vpcclient.AttachExistingRequest) (*vpcclient.AllocateResponse, error) {
	f.mu.Lock()
	f.byoReqs = append(f.byoReqs, req)
	f.mu.Unlock()
	if f.byoFunc != nil {
		return f.byoFunc(ctx, req)
	}
	return &vpcclient.AllocateResponse{AddressID: req.AddressID, Value: "10.0.0.250"}, nil
}

func (f *fakeAddressClient) FreeIP(ctx context.Context, addressID string, _ vpcclient.AddressOwner) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.freed = append(f.freed, addressID)
	return nil
}

func (f *fakeAddressClient) ClearReference(ctx context.Context, addressID string, _ vpcclient.AddressOwner) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, addressID)
	return nil
}

// fakeSubnetClient — двойник vpc.SubnetClient для sync-precheck REGIONAL-подсети.
// Дефолт: подсеть найдена и REGIONAL; placement/getFunc подменяют под negative-
// сценарии (ZONAL-подсеть / not-found / vpc-Unavailable).
type fakeSubnetClient struct {
	placement string // "" → REGIONAL по умолчанию
	getFunc   func(ctx context.Context, subnetID string) (*vpcclient.Subnet, error)
}

func (f *fakeSubnetClient) Get(ctx context.Context, subnetID string) (*vpcclient.Subnet, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, subnetID)
	}
	pt := f.placement
	if pt == "" {
		pt = vpcclient.SubnetPlacementRegional
	}
	return &vpcclient.Subnet{ID: subnetID, ProjectID: "prj-a", NetworkID: "net-1", PlacementType: pt}, nil
}

// fakeAddressReader — двойник vpc.AddressClient (публичный AddressService.Get) для
// BYO ownership-precheck. Дефолт: адрес принадлежит проекту "prj-a", семейство
// IPV4 (совпадает с happy-path BYO v4). projectID/family/getFunc подменяют под
// negative-сценарии (чужой проект, несовпадение семейства, not-found/vpc-Unavailable).
type fakeAddressReader struct {
	projectID string // "" → "prj-a"
	family    string // "" → AddressFamilyIPv4
	external  bool   // kind: internal (false) / external (true)
	subnetID  string // подсеть internal-адреса ("" → "snt-01" для internal)
	getFunc   func(ctx context.Context, addressID string) (*vpcclient.Address, error)
}

func (f *fakeAddressReader) Get(ctx context.Context, addressID string) (*vpcclient.Address, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, addressID)
	}
	pid := f.projectID
	if pid == "" {
		pid = "prj-a"
	}
	fam := f.family
	if fam == "" {
		fam = vpcclient.AddressFamilyIPv4
	}
	sn := f.subnetID
	if sn == "" && !f.external {
		sn = "snt01000000000000000"
	}
	return &vpcclient.Address{
		ID: addressID, ProjectID: pid, Family: fam,
		External: f.external, SubnetID: sn,
	}, nil
}

// fakeFGARegisterOutbox records FGARegisterOutbox.Emit into the writer's
// pending buffer (flushed to fakeRepo.fga on Commit, dropped on Abort).
type fakeFGARegisterOutbox struct{ w *fakeWriter }

func (o *fakeFGARegisterOutbox) Emit(ctx context.Context, eventType string, intent domain.FGARegisterIntent) error {
	if o.w.r.failOnOutbox != nil {
		return o.w.r.failOnOutbox
	}
	o.w.pendingFGA = append(o.w.pendingFGA, fgaIntentEvent{EventType: eventType, Intent: intent})
	return nil
}

// ensure interface conformance (compile-time).
var (
	_ kachorepo.Repository  = (*fakeRepo)(nil)
	_ ProjectClient         = (*fakeProjectClient)(nil)
	_ CheckClient           = (*fakeCheckClient)(nil)
	_ RegionClient          = (*fakeRegionClient)(nil)
	_ ZoneClient            = (*fakeZoneClient)(nil)
	_ SubnetClient          = (*fakeSubnetClient)(nil)
	_ AddressClient         = (*fakeAddressReader)(nil)
	_ InternalAddressClient = (*fakeAddressClient)(nil)
)
