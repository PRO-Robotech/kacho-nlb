package listener

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	// Blank-import регистрирует Listener/time DTO трансферы (skill evgeniy
	// §3 C.4); без него dto.Transfer Listener возвращает «no transfer
	// registered» в worker'е.
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// In-memory port-fakes for unit tests. Each fake implements one port-interface
// from `ports.go` (or peer-client interface) и моделирует только тот контракт,
// который use-case'ы реально дёргают. Race-corner cases — отдельные integration
// тесты в `integration_test.go` против реального Postgres.

// ---- RepoFactory fake ----

// fakeRepo — in-memory `kachorepo.Repository` (`RepoFactory`).
//
// Хранит таблицы listeners + load_balancers + target_groups + outbox; CQRS
// reader/writer возвращают одну и ту же in-memory таблицу (writer-side
// видит свои writes, как evgeniy §G.2). Commit/Abort моделируется через
// snapshot-restore при Abort'е.
type fakeRepo struct {
	mu            sync.Mutex
	listeners     map[string]*kachorepo.ListenerRecord
	loadBalancers map[string]*kachorepo.LoadBalancerRecord
	targetGroups  map[string]*kachorepo.TargetGroupRecord
	outbox        []fakeOutboxEvent
	fga           []fgaIntentEvent // SEC-D FGARegisterOutbox intents (flushed on Commit)
	insertErr     error            // injected error for next Insert
	commitErr     error            // injected error for next Commit
	currentWriter *fakeWriter
}

type fakeOutboxEvent struct {
	ResourceType string
	ResourceID   string
	ProjectID    string
	Action       string
	Payload      map[string]any
}

// newFakeRepo создаёт пустой fakeRepo.
func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		listeners:     map[string]*kachorepo.ListenerRecord{},
		loadBalancers: map[string]*kachorepo.LoadBalancerRecord{},
		targetGroups:  map[string]*kachorepo.TargetGroupRecord{},
	}
}

// seedLB кладёт LoadBalancer-record (для parent-resolve в Listener.Create).
func (r *fakeRepo) seedLB(lb *kachorepo.LoadBalancerRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadBalancers[string(lb.ID)] = lb
}

// seedListener кладёт Listener-record (для Get/Update/Delete тестов).
func (r *fakeRepo) seedListener(rec *kachorepo.ListenerRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners[string(rec.ID)] = rec
}

// seedTG кладёт TG-record (для same-region precheck в Update).
func (r *fakeRepo) seedTG(tg *kachorepo.TargetGroupRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.targetGroups[string(tg.ID)] = tg
}

func (r *fakeRepo) Reader(ctx context.Context) (kachorepo.RepositoryReader, error) {
	return &fakeReader{r: r}, nil
}

func (r *fakeRepo) Writer(ctx context.Context) (kachorepo.RepositoryWriter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentWriter != nil {
		return nil, errors.New("fakeRepo: concurrent writers not supported")
	}
	w := &fakeWriter{r: r}
	r.currentWriter = w
	return w, nil
}

func (r *fakeRepo) Close() {}

// pendingOutbox — все события, эмитнутые через committed writers.
func (r *fakeRepo) pendingOutbox() []fakeOutboxEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]fakeOutboxEvent, len(r.outbox))
	copy(out, r.outbox)
	return out
}

// ---- Reader ----

type fakeReader struct {
	r *fakeRepo
}

func (rd *fakeReader) LoadBalancers() kachorepo.LoadBalancerReaderIface {
	return &fakeLBReader{r: rd.r}
}
func (rd *fakeReader) Listeners() kachorepo.ListenerReaderIface       { return &fakeListenerReader{r: rd.r} }
func (rd *fakeReader) TargetGroups() kachorepo.TargetGroupReaderIface { return &fakeTGReader{r: rd.r} }
func (rd *fakeReader) AttachedTargetGroups() kachorepo.AttachedTargetGroupReaderIface {
	return &fakeAttachedTGReader{}
}
func (rd *fakeReader) Close() error { return nil }

// ---- Writer ----

type fakeWriter struct {
	r          *fakeRepo
	pending    []fakeOutboxEvent
	pendingFGA []fgaIntentEvent
	inserted   []domain.ResourceID // for rollback on Abort
	updated    []domain.ResourceID
	deleted    []string
	finalize   bool
}

// fgaIntentEvent records one FGARegisterOutbox.Emit (SEC-D) for assertions.
type fgaIntentEvent struct {
	EventType string
	Intent    domain.FGARegisterIntent
}

func (w *fakeWriter) LoadBalancers() kachorepo.LoadBalancerWriterIface {
	return &fakeLBWriter{r: w.r}
}
func (w *fakeWriter) Listeners() kachorepo.ListenerWriterIface {
	return &fakeListenerWriter{r: w.r, w: w}
}
func (w *fakeWriter) TargetGroups() kachorepo.TargetGroupWriterIface { return &fakeTGWriter{r: w.r} }
func (w *fakeWriter) AttachedTargetGroups() kachorepo.AttachedTargetGroupWriterIface {
	return &fakeAttachedTGWriter{}
}
func (w *fakeWriter) Outbox() kachorepo.OutboxEmitter { return &fakeOutbox{w: w} }
func (w *fakeWriter) FGARegisterOutbox() kachorepo.FGARegisterEmitter {
	return &fakeFGARegisterOutbox{w: w}
}
func (w *fakeWriter) Commit() error {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if w.r.commitErr != nil {
		err := w.r.commitErr
		w.r.commitErr = nil
		w.r.currentWriter = nil
		return err
	}
	w.r.outbox = append(w.r.outbox, w.pending...)
	w.r.fga = append(w.r.fga, w.pendingFGA...)
	w.r.currentWriter = nil
	w.finalize = true
	return nil
}
func (w *fakeWriter) Abort() {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	if w.finalize {
		return
	}
	for _, id := range w.inserted {
		delete(w.r.listeners, string(id))
	}
	for _, id := range w.deleted {
		// can't restore; tests don't exercise rollback-after-delete commits
		_ = id
	}
	_ = w.updated
	w.r.currentWriter = nil
	w.finalize = true
}

// fakeLBReader / fakeLBWriter — only methods actually called by Listener
// UseCases are implemented; the rest panic если затронут (defensive).

type fakeLBReader struct{ r *fakeRepo }

func (r *fakeLBReader) Get(_ context.Context, id string) (*kachorepo.LoadBalancerRecord, error) {
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	if rec, ok := r.r.loadBalancers[id]; ok {
		copy := *rec
		return &copy, nil
	}
	return nil, fmt.Errorf("%w: NetworkLoadBalancer %s not found", domain.ErrNotFound, id)
}
func (r *fakeLBReader) List(context.Context, kachorepo.LoadBalancerFilter, kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return nil, "", nil
}
func (r *fakeLBReader) ListByProject(context.Context, string, kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return nil, "", nil
}
func (r *fakeLBReader) HasListeners(_ context.Context, lbID string) (bool, error) {
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	for _, l := range r.r.listeners {
		if string(l.LoadBalancerID) == lbID {
			return true, nil
		}
	}
	return false, nil
}
func (r *fakeLBReader) HasAttachedTargetGroups(context.Context, string) (bool, error) {
	return false, nil
}

type fakeLBWriter struct{ r *fakeRepo }

func (w *fakeLBWriter) Get(ctx context.Context, id string) (*kachorepo.LoadBalancerRecord, error) {
	return (&fakeLBReader{r: w.r}).Get(ctx, id)
}
func (w *fakeLBWriter) List(context.Context, kachorepo.LoadBalancerFilter, kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return nil, "", nil
}
func (w *fakeLBWriter) ListByProject(context.Context, string, kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return nil, "", nil
}
func (w *fakeLBWriter) HasListeners(ctx context.Context, lbID string) (bool, error) {
	return (&fakeLBReader{r: w.r}).HasListeners(ctx, lbID)
}
func (w *fakeLBWriter) HasAttachedTargetGroups(context.Context, string) (bool, error) {
	return false, nil
}
func (w *fakeLBWriter) Insert(_ context.Context, lb *domain.LoadBalancer) (*kachorepo.LoadBalancerRecord, error) {
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	rec := &kachorepo.LoadBalancerRecord{
		LoadBalancer: *lb,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	w.r.loadBalancers[string(lb.ID)] = rec
	return rec, nil
}
func (w *fakeLBWriter) Update(context.Context, *domain.LoadBalancer) (*kachorepo.LoadBalancerRecord, error) {
	return nil, errors.New("fakeLBWriter.Update not implemented")
}
func (w *fakeLBWriter) SetStatusCAS(context.Context, string, domain.LBStatus, domain.LBStatus) (*kachorepo.LoadBalancerRecord, error) {
	return nil, errors.New("fakeLBWriter.SetStatusCAS not implemented")
}
func (w *fakeLBWriter) MoveProject(context.Context, string, string) (*kachorepo.LoadBalancerRecord, error) {
	return nil, errors.New("fakeLBWriter.MoveProject not implemented")
}
func (w *fakeLBWriter) Delete(context.Context, string) error {
	return errors.New("fakeLBWriter.Delete not implemented")
}

// ---- Listener reader/writer ----

type fakeListenerReader struct{ r *fakeRepo }

func (r *fakeListenerReader) Get(_ context.Context, id string) (*kachorepo.ListenerRecord, error) {
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	if rec, ok := r.r.listeners[id]; ok {
		copy := *rec
		return &copy, nil
	}
	return nil, fmt.Errorf("%w: Listener %s not found", domain.ErrNotFound, id)
}
func (r *fakeListenerReader) List(_ context.Context, f kachorepo.ListenerFilter, p kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	// RBAC sub-phase D §11: per-object FGA allow-set push-down (parity с pg repo).
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
	var out []*kachorepo.ListenerRecord
	for _, l := range r.r.listeners {
		if f.LoadBalancerID != "" && string(l.LoadBalancerID) != f.LoadBalancerID {
			continue
		}
		if f.ProjectID != "" && string(l.ProjectID) != f.ProjectID {
			continue
		}
		if f.Name != "" && string(l.Name) != f.Name {
			continue
		}
		if allowed != nil {
			if _, ok := allowed[string(l.ID)]; !ok {
				continue
			}
		}
		c := *l
		out = append(out, &c)
	}
	return out, "", nil
}
func (r *fakeListenerReader) ListByLB(ctx context.Context, lbID string, p kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	return r.List(ctx, kachorepo.ListenerFilter{LoadBalancerID: lbID}, p)
}

type fakeListenerWriter struct {
	r *fakeRepo
	w *fakeWriter
}

func (lw *fakeListenerWriter) Get(ctx context.Context, id string) (*kachorepo.ListenerRecord, error) {
	return (&fakeListenerReader{r: lw.r}).Get(ctx, id)
}
func (lw *fakeListenerWriter) List(ctx context.Context, f kachorepo.ListenerFilter, p kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	return (&fakeListenerReader{r: lw.r}).List(ctx, f, p)
}
func (lw *fakeListenerWriter) ListByLB(ctx context.Context, lbID string, p kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	return (&fakeListenerReader{r: lw.r}).ListByLB(ctx, lbID, p)
}
func (lw *fakeListenerWriter) Insert(_ context.Context, l *domain.Listener) (*kachorepo.ListenerRecord, error) {
	lw.r.mu.Lock()
	defer lw.r.mu.Unlock()
	if lw.r.insertErr != nil {
		err := lw.r.insertErr
		lw.r.insertErr = nil
		return nil, err
	}
	for _, existing := range lw.r.listeners {
		if existing.LoadBalancerID == l.LoadBalancerID &&
			existing.Port == l.Port &&
			existing.Protocol == l.Protocol &&
			existing.Status != domain.ListenerStatusDeleting {
			return nil, fmt.Errorf("%w: listener with port %d and protocol %s already exists on this load balancer",
				domain.ErrAlreadyExists, l.Port, l.Protocol)
		}
		if existing.RegionID == l.RegionID &&
			existing.AllocatedAddress == l.AllocatedAddress && l.AllocatedAddress != "" &&
			existing.Port == l.Port &&
			existing.Protocol == l.Protocol &&
			existing.Status != domain.ListenerStatusDeleting {
			return nil, fmt.Errorf("%w: listener with VIP %s port %d protocol %s already exists in region %s",
				domain.ErrAlreadyExists, l.AllocatedAddress, l.Port, l.Protocol, l.RegionID)
		}
	}
	rec := &kachorepo.ListenerRecord{
		Listener:  *l,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	lw.r.listeners[string(l.ID)] = rec
	if lw.w != nil {
		lw.w.inserted = append(lw.w.inserted, l.ID)
	}
	return rec, nil
}
func (lw *fakeListenerWriter) Update(_ context.Context, l *domain.Listener) (*kachorepo.ListenerRecord, error) {
	lw.r.mu.Lock()
	defer lw.r.mu.Unlock()
	cur, ok := lw.r.listeners[string(l.ID)]
	if !ok {
		return nil, fmt.Errorf("%w: Listener %s not found", domain.ErrNotFound, string(l.ID))
	}
	// Apply mutable fields only — mirrors pg writer behaviour.
	cur.Name = l.Name
	cur.Description = l.Description
	cur.Labels = l.Labels
	cur.ProxyProtocolV2 = l.ProxyProtocolV2
	cur.DefaultTargetGroupID = l.DefaultTargetGroupID
	cur.UpdatedAt = time.Now().UTC()
	rec := *cur
	return &rec, nil
}
func (lw *fakeListenerWriter) SetStatusCAS(_ context.Context, id string, expected, newStatus domain.ListenerStatus) (*kachorepo.ListenerRecord, error) {
	lw.r.mu.Lock()
	defer lw.r.mu.Unlock()
	cur, ok := lw.r.listeners[id]
	if !ok {
		return nil, fmt.Errorf("%w: Listener %s not found", domain.ErrNotFound, id)
	}
	if cur.Status != expected {
		return nil, fmt.Errorf("%w: Listener %s status is not %s", domain.ErrFailedPrecondition, id, expected)
	}
	cur.Status = newStatus
	rec := *cur
	return &rec, nil
}
func (lw *fakeListenerWriter) SetAllocatedAddress(_ context.Context, id, address string) (*kachorepo.ListenerRecord, error) {
	lw.r.mu.Lock()
	defer lw.r.mu.Unlock()
	cur, ok := lw.r.listeners[id]
	if !ok {
		return nil, fmt.Errorf("%w: Listener %s not found", domain.ErrNotFound, id)
	}
	cur.AllocatedAddress = domain.IPAddress(address)
	rec := *cur
	return &rec, nil
}
func (lw *fakeListenerWriter) MoveProject(_ context.Context, lbID, newProjectID string) (int64, error) {
	lw.r.mu.Lock()
	defer lw.r.mu.Unlock()
	var n int64
	for _, l := range lw.r.listeners {
		if string(l.LoadBalancerID) == lbID {
			l.ProjectID = domain.ProjectID(newProjectID)
			n++
		}
	}
	return n, nil
}
func (lw *fakeListenerWriter) Delete(_ context.Context, id string) error {
	lw.r.mu.Lock()
	defer lw.r.mu.Unlock()
	if _, ok := lw.r.listeners[id]; !ok {
		return fmt.Errorf("%w: Listener %s not found", domain.ErrNotFound, id)
	}
	delete(lw.r.listeners, id)
	if lw.w != nil {
		lw.w.deleted = append(lw.w.deleted, id)
	}
	return nil
}

// ---- TG (read-only stub for Update.same-region check) ----

type fakeTGReader struct{ r *fakeRepo }

func (r *fakeTGReader) Get(_ context.Context, id string) (*kachorepo.TargetGroupRecord, error) {
	r.r.mu.Lock()
	defer r.r.mu.Unlock()
	if tg, ok := r.r.targetGroups[id]; ok {
		c := *tg
		return &c, nil
	}
	return nil, fmt.Errorf("%w: TargetGroup %s not found", domain.ErrNotFound, id)
}
func (r *fakeTGReader) List(context.Context, kachorepo.TargetGroupFilter, kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return nil, "", nil
}
func (r *fakeTGReader) ListByProject(context.Context, string, kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return nil, "", nil
}
func (r *fakeTGReader) ListTargets(context.Context, string) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}
func (r *fakeTGReader) ListDrainingExpired(context.Context, string, int32) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}
func (r *fakeTGReader) HasAttachedLB(context.Context, string) (bool, error) {
	return false, nil
}

type fakeTGWriter struct{ r *fakeRepo }

func (w *fakeTGWriter) Get(ctx context.Context, id string) (*kachorepo.TargetGroupRecord, error) {
	return (&fakeTGReader{r: w.r}).Get(ctx, id)
}
func (w *fakeTGWriter) List(context.Context, kachorepo.TargetGroupFilter, kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return nil, "", nil
}
func (w *fakeTGWriter) ListByProject(context.Context, string, kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return nil, "", nil
}
func (w *fakeTGWriter) ListTargets(context.Context, string) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}
func (w *fakeTGWriter) ListDrainingExpired(context.Context, string, int32) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}
func (w *fakeTGWriter) HasAttachedLB(context.Context, string) (bool, error) {
	return false, nil
}
func (w *fakeTGWriter) Insert(context.Context, *domain.TargetGroup) (*kachorepo.TargetGroupRecord, error) {
	return nil, errors.New("fakeTGWriter.Insert not implemented")
}
func (w *fakeTGWriter) Update(context.Context, *domain.TargetGroup) (*kachorepo.TargetGroupRecord, error) {
	return nil, errors.New("fakeTGWriter.Update not implemented")
}
func (w *fakeTGWriter) SetStatusCAS(context.Context, string, domain.TargetGroupStatus, domain.TargetGroupStatus) (*kachorepo.TargetGroupRecord, error) {
	return nil, errors.New("not implemented")
}
func (w *fakeTGWriter) MoveProject(context.Context, string, string) (*kachorepo.TargetGroupRecord, error) {
	return nil, errors.New("not implemented")
}
func (w *fakeTGWriter) AddTargets(context.Context, string, []domain.Target) (int, error) {
	return 0, errors.New("not implemented")
}
func (w *fakeTGWriter) RemoveTargetsMarkDraining(context.Context, string, []string) (int, error) {
	return 0, errors.New("not implemented")
}
func (w *fakeTGWriter) DeleteTargetsDrained(context.Context, string, int32) (int, error) {
	return 0, errors.New("not implemented")
}
func (w *fakeTGWriter) Delete(context.Context, string) error {
	return errors.New("not implemented")
}

// ---- AttachedTG stubs (unused in listener UseCases). ----

type fakeAttachedTGReader struct{}

func (fakeAttachedTGReader) Get(context.Context, string, string) (*kachorepo.AttachedTargetGroupRecord, error) {
	return nil, errors.New("not implemented")
}
func (fakeAttachedTGReader) ListByLB(context.Context, string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	return nil, nil
}
func (fakeAttachedTGReader) ListByTG(context.Context, string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	return nil, nil
}

type fakeAttachedTGWriter struct{ fakeAttachedTGReader }

func (fakeAttachedTGWriter) Attach(context.Context, string, string, int32) (*kachorepo.AttachedTargetGroupRecord, bool, error) {
	return nil, false, errors.New("not implemented")
}
func (fakeAttachedTGWriter) Detach(context.Context, string, string) error {
	return errors.New("not implemented")
}

// ---- Outbox ----

type fakeOutbox struct{ w *fakeWriter }

func (o *fakeOutbox) Emit(_ context.Context, rt, rid, prj, action string, payload map[string]any) error {
	o.w.pending = append(o.w.pending, fakeOutboxEvent{
		ResourceType: rt,
		ResourceID:   rid,
		ProjectID:    prj,
		Action:       action,
		Payload:      payload,
	})
	return nil
}

// ---- OperationsRepo fake ----

// fakeOpsRepo — in-memory `operations.Repo`. Поддерживает Create, MarkDone,
// MarkError, Get, List, Cancel в минимальной достаточной для тестов форме.
type fakeOpsRepo struct {
	mu  sync.Mutex
	ops map[string]*operations.Operation
}

func newFakeOpsRepo() *fakeOpsRepo {
	return &fakeOpsRepo{ops: map[string]*operations.Operation{}}
}

func (r *fakeOpsRepo) Create(ctx context.Context, op operations.Operation) error {
	return r.CreateWithPrincipal(ctx, op, op.Principal)
}
func (r *fakeOpsRepo) CreateWithPrincipal(_ context.Context, op operations.Operation, p operations.Principal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p == (operations.Principal{}) {
		p = operations.SystemPrincipal()
	}
	op.Principal = p
	r.ops[op.ID] = &op
	return nil
}
func (r *fakeOpsRepo) Get(_ context.Context, id string) (*operations.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if op, ok := r.ops[id]; ok {
		c := *op
		return &c, nil
	}
	return nil, operations.ErrNotFound
}
func (r *fakeOpsRepo) List(_ context.Context, f operations.ListFilter) ([]operations.Operation, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []operations.Operation
	for _, op := range r.ops {
		if f.ResourceID != "" {
			// Crude resource_id match: scan metadata bytes for listener_id field
			// — but in tests we'll seed metadata with listener_id and just check
			// substring on the description if necessary. Simpler: use the
			// extractResourceID by re-running unmarshal — but for fakes we
			// short-circuit by description contains.
			if !opMatchesResource(op, f.ResourceID) {
				continue
			}
		}
		out = append(out, *op)
	}
	return out, "", nil
}
func (r *fakeOpsRepo) MarkDone(_ context.Context, id string, resp *anypb.Any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	op.Response = resp
	op.ModifiedAt = time.Now().UTC()
	return nil
}
func (r *fakeOpsRepo) MarkError(_ context.Context, id string, st *status.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	op.Error = st
	op.ModifiedAt = time.Now().UTC()
	return nil
}
func (r *fakeOpsRepo) Cancel(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	if op.Done {
		return operations.ErrAlreadyDone
	}
	op.Done = true
	op.Error = &status.Status{Code: 1, Message: "operation cancelled"}
	return nil
}

// opMatchesResource — naive resource_id extractor для фейка. Через metadata-
// reflection, parity с pg repo.
func opMatchesResource(op *operations.Operation, resourceID string) bool {
	if op.Metadata == nil {
		return false
	}
	msg, err := op.Metadata.UnmarshalNew()
	if err != nil {
		return false
	}
	fields := msg.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		val := msg.ProtoReflect().Get(fd).String()
		if val == resourceID {
			return true
		}
	}
	return false
}

// awaitOpDone — busy-wait helper для тестов: ждёт пока op.Done не станет true
// либо timeout. Возвращает зашитый op либо t.Fatal через nil + last error.
func awaitOpDone(t interface {
	Helper()
	Fatalf(format string, args ...any)
}, r *fakeOpsRepo, id string, timeout time.Duration) *operations.Operation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		op, err := r.Get(context.Background(), id)
		if err == nil && op.Done {
			return op
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("operation %s did not reach done=true within %s", id, timeout)
	return nil
}

// ---- vpc clients ----

// fakeAddressClient — in-memory `vpcclient.AddressClient`.
type fakeAddressClient struct {
	mu        sync.Mutex
	byID      map[string]*vpcclient.Address
	getErr    error
	getErrCnt int // how many times to return getErr before returning success
}

func newFakeAddressClient() *fakeAddressClient {
	return &fakeAddressClient{byID: map[string]*vpcclient.Address{}}
}
func (c *fakeAddressClient) seed(addr *vpcclient.Address) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[addr.ID] = addr
}
func (c *fakeAddressClient) Get(_ context.Context, id string) (*vpcclient.Address, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErrCnt > 0 {
		c.getErrCnt--
		return nil, c.getErr
	}
	if addr, ok := c.byID[id]; ok {
		c := *addr
		return &c, nil
	}
	return nil, fmt.Errorf("%w: address %s not found", domain.ErrInvalidArg, id)
}

// fakeInternalAddressClient — in-memory `vpcclient.InternalAddressClient`.
type fakeInternalAddressClient struct {
	mu                  sync.Mutex
	allocExternalCalls  []vpcclient.AllocateExternalIPRequest
	allocInternalCalls  []vpcclient.AllocateInternalIPRequest
	freeCalls           []string
	clearCalls          []string
	setRefCalls         []setRefCall
	allocExternalResult *vpcclient.AllocateResponse
	allocInternalResult *vpcclient.AllocateResponse
	allocErr            error
	setRefErr           error
	freeErr             error
	clearErr            error
	nextAllocID         string
	nextAllocValue      string
}
type setRefCall struct {
	addressID string
	owner     vpcclient.AddressOwner
}

func newFakeInternalAddressClient() *fakeInternalAddressClient {
	return &fakeInternalAddressClient{
		nextAllocID:    "e9bALLOCSTUB000001",
		nextAllocValue: "203.0.113.42",
	}
}
func (c *fakeInternalAddressClient) AllocateExternalIP(_ context.Context, req vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allocExternalCalls = append(c.allocExternalCalls, req)
	if c.allocErr != nil {
		err := c.allocErr
		return nil, err
	}
	if c.allocExternalResult != nil {
		return c.allocExternalResult, nil
	}
	return &vpcclient.AllocateResponse{
		AddressID: c.nextAllocID,
		Value:     c.nextAllocValue,
	}, nil
}
func (c *fakeInternalAddressClient) AllocateInternalIP(_ context.Context, req vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allocInternalCalls = append(c.allocInternalCalls, req)
	if c.allocErr != nil {
		return nil, c.allocErr
	}
	if c.allocInternalResult != nil {
		return c.allocInternalResult, nil
	}
	return &vpcclient.AllocateResponse{
		AddressID: c.nextAllocID,
		Value:     c.nextAllocValue,
	}, nil
}
func (c *fakeInternalAddressClient) FreeIP(_ context.Context, id string, _ vpcclient.AddressOwner) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.freeCalls = append(c.freeCalls, id)
	return c.freeErr
}
func (c *fakeInternalAddressClient) SetReference(_ context.Context, id string, owner vpcclient.AddressOwner) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setRefCalls = append(c.setRefCalls, setRefCall{addressID: id, owner: owner})
	return c.setRefErr
}
func (c *fakeInternalAddressClient) ClearReference(_ context.Context, id string, _ vpcclient.AddressOwner) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearCalls = append(c.clearCalls, id)
	return c.clearErr
}

// fakeSubnetClient — minimal SubnetClient stub (not exercised in current
// tests — Listener.Create defers to vpc.InternalAddressService.AllocateInternalIP
// which itself proxies subnet_id; UseCase не зовёт subnets.Get напрямую).
type fakeSubnetClient struct {
	byID map[string]*vpcclient.Subnet
}

func newFakeSubnetClient() *fakeSubnetClient {
	return &fakeSubnetClient{byID: map[string]*vpcclient.Subnet{}}
}
func (c *fakeSubnetClient) Get(_ context.Context, id string) (*vpcclient.Subnet, error) {
	if s, ok := c.byID[id]; ok {
		c := *s
		return &c, nil
	}
	return nil, fmt.Errorf("%w: Subnet %s not found", domain.ErrInvalidArg, id)
}

// fakeFGARegisterOutbox records SEC-D FGARegisterOutbox.Emit into the writer's
// pending buffer (flushed to fakeRepo.fga on Commit, dropped on Abort).
type fakeFGARegisterOutbox struct{ w *fakeWriter }

func (o *fakeFGARegisterOutbox) Emit(_ context.Context, eventType string, intent domain.FGARegisterIntent) error {
	o.w.pendingFGA = append(o.w.pendingFGA, fgaIntentEvent{EventType: eventType, Intent: intent})
	return nil
}

// contextWithSubject — helper для тестов: кладёт `operations.Principal` в
// ctx так, чтобы `principalSubjectAccessor.SubjectFromContext(ctx)` вернул
// `subject` (формат `<type>:<id>`). Используется create_test.go для проверки,
// что FGA creator-tuple содержит реального acting subject'а.
func contextWithSubject(subject string) context.Context {
	if subject == "" {
		return context.Background()
	}
	parts := splitSubject(subject)
	return operations.WithPrincipal(context.Background(), operations.Principal{
		Type: parts[0],
		ID:   parts[1],
	})
}
func splitSubject(s string) [2]string {
	var out [2]string
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			out[0] = s[:i]
			out[1] = s[i+1:]
			return out
		}
	}
	out[0] = s
	return out
}

// committedFGA — SEC-D: all FGA-register/unregister intents flushed by committed
// writers (parity with loadbalancer test pkg `repo.fga` direct access).
func (r *fakeRepo) committedFGA() []fgaIntentEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]fgaIntentEvent, len(r.fga))
	copy(out, r.fga)
	return out
}
