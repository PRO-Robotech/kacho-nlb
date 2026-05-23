package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	// dto/type2pb init()-registrations — handler-слой строит proto через DTO-реестр.
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
	// Knobs for fault injection.
	failOnInsert     error
	failOnUpdate     error
	failOnDelete     error
	failOnSetStatus  error
	failOnMove       error
	failOnAttach     error
	failOnList       error
	failOnGet        error
	failOnOutbox     error
	preCommitHook    func() error
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
	pendingLBs    []*kachorepo.LoadBalancerRecord
	pendingPivots []*kachorepo.AttachedTargetGroupRecord
	pendingDeletes []string // LB ids
	pendingPivotDeletes []string // "lb/tg"
	pendingOutbox []outboxEvent
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
	var out []*kachorepo.LoadBalancerRecord
	for _, lb := range q.r.lbs {
		if f.ProjectID != "" && string(lb.ProjectID) != f.ProjectID {
			continue
		}
		if f.Name != "" && string(lb.Name) != f.Name {
			continue
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
	cur.CrossZoneEnabled = lb.CrossZoneEnabled
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

type fakeRegionClient struct {
	getFunc func(ctx context.Context, regionID string) (*compute.Region, error)
}

func (f *fakeRegionClient) Get(ctx context.Context, regionID string) (*compute.Region, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, regionID)
	}
	return &compute.Region{ID: regionID, Name: "fake-region"}, nil
}

type fakeHierarchy struct {
	mu               sync.Mutex
	writeCreatorErr  error
	rewriteProjectErr error
	creatorCalls     []string // "subject relation object"
	rewriteCalls     []string // "objType:objID src dst"
}

func (f *fakeHierarchy) WriteCreatorTuple(ctx context.Context, subjectID, relation, object string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creatorCalls = append(f.creatorCalls, fmt.Sprintf("%s %s %s", subjectID, relation, object))
	return f.writeCreatorErr
}
func (f *fakeHierarchy) RewriteProjectTuple(ctx context.Context, objectType, objectID, srcProject, dstProject string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rewriteCalls = append(f.rewriteCalls, fmt.Sprintf("%s:%s %s→%s", objectType, objectID, srcProject, dstProject))
	return f.rewriteProjectErr
}

// ensure interface conformance (compile-time).
var (
	_ kachorepo.Repository = (*fakeRepo)(nil)
	_ ProjectClient        = (*fakeProjectClient)(nil)
	_ RegionClient         = (*fakeRegionClient)(nil)
	_ HierarchyWriter      = (*fakeHierarchy)(nil)
)
