package targetgroup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/genproto/googleapis/rpc/status"
	grpcStatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/geo"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	// Blank-import регистрирует TargetGroup/Target/HC DTO трансферы.
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// ---- In-memory Repo --------------------------------------------------------

// fakeRepo — in-memory `kacho.Repository` для unit-тестов use-case'ов TG.
// Покрывает методы, реально вызываемые TG-use-case'ами: TargetGroups
// Reader+Writer (Get/List/Insert/Update/Delete/MoveProject/AddTargets/
// RemoveTargetsMarkDraining/HasAttachedLB/ListTargets) + AttachedTargetGroups
// (ListByTG) + Outbox emit.
type fakeRepo struct {
	mu               sync.Mutex
	tgs              map[string]*kachorepo.TargetGroupRecord
	targets          map[string]map[string]*kachorepo.TargetRecord // tgID → target.ID → row
	pivot            map[string]bool                               // "lbID/tgID" → attached
	outbox           []fakeOutboxEvent
	fga              []fgaIntentEvent // SEC-D FGARegisterOutbox intents (flushed on Commit)
	failOnInsert     error
	failOnUpdate     error
	failOnDelete     error
	failOnAddTargets error
	failOnOutbox     error
}

type fakeOutboxEvent struct {
	ResourceType string
	ResourceID   string
	ProjectID    string
	Action       string
	Payload      map[string]any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		tgs:     make(map[string]*kachorepo.TargetGroupRecord),
		targets: make(map[string]map[string]*kachorepo.TargetRecord),
		pivot:   make(map[string]bool),
	}
}

func (r *fakeRepo) seedTG(rec *kachorepo.TargetGroupRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tgs[string(rec.ID)] = rec
}

func (r *fakeRepo) seedTarget(tgID string, rec *kachorepo.TargetRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.targets[tgID]
	if !ok {
		m = make(map[string]*kachorepo.TargetRecord)
		r.targets[tgID] = m
	}
	m[rec.ID] = rec
}

func (r *fakeRepo) seedAttached(lbID, tgID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pivot[lbID+"/"+tgID] = true
}

func (r *fakeRepo) outboxEvents() []fakeOutboxEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]fakeOutboxEvent, len(r.outbox))
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

// ---- Reader / Writer wrappers ----

type fakeReader struct{ r *fakeRepo }

func (rd *fakeReader) LoadBalancers() kachorepo.LoadBalancerReaderIface { return &fakeLBStub{} }
func (rd *fakeReader) Listeners() kachorepo.ListenerReaderIface         { return &fakeListenerStub{} }
func (rd *fakeReader) TargetGroups() kachorepo.TargetGroupReaderIface   { return &fakeTGReader{r: rd.r} }
func (rd *fakeReader) AttachedTargetGroups() kachorepo.AttachedTargetGroupReaderIface {
	return &fakeATGReader{r: rd.r}
}
func (rd *fakeReader) Close() error { return nil }

type fakeWriter struct {
	r             *fakeRepo
	committed     bool
	pendingOutbox []fakeOutboxEvent
	pendingFGA    []fgaIntentEvent
	pendingTGs    []*kachorepo.TargetGroupRecord
	pendingTGDel  []string
	pendingAddTgt []pendingAddTarget
	pendingMark   []pendingMarkDraining
}

// fgaIntentEvent records one FGARegisterOutbox.Emit (SEC-D) for assertions.
type fgaIntentEvent struct {
	EventType string
	Intent    domain.FGARegisterIntent
}

type pendingAddTarget struct {
	tgID    string
	targets []domain.Target
}

type pendingMarkDraining struct {
	tgID      string
	targetIDs []string
}

func (w *fakeWriter) LoadBalancers() kachorepo.LoadBalancerWriterIface { return &fakeLBStub{} }
func (w *fakeWriter) Listeners() kachorepo.ListenerWriterIface         { return &fakeListenerStub{} }
func (w *fakeWriter) TargetGroups() kachorepo.TargetGroupWriterIface {
	return &fakeTGWriter{r: w.r, w: w}
}
func (w *fakeWriter) AttachedTargetGroups() kachorepo.AttachedTargetGroupWriterIface {
	return &fakeATGWriter{r: w.r}
}
func (w *fakeWriter) Outbox() kachorepo.OutboxEmitter { return &fakeOutbox{r: w.r, w: w} }
func (w *fakeWriter) FGARegisterOutbox() kachorepo.FGARegisterEmitter {
	return &fakeFGARegisterOutbox{w: w}
}

func (w *fakeWriter) Commit() error {
	if w.committed {
		return nil
	}
	w.committed = true
	w.r.mu.Lock()
	defer w.r.mu.Unlock()
	for _, rec := range w.pendingTGs {
		w.r.tgs[string(rec.ID)] = rec
	}
	for _, id := range w.pendingTGDel {
		delete(w.r.tgs, id)
		delete(w.r.targets, id)
	}
	for _, p := range w.pendingAddTgt {
		m, ok := w.r.targets[p.tgID]
		if !ok {
			m = make(map[string]*kachorepo.TargetRecord)
			w.r.targets[p.tgID] = m
		}
		for _, t := range p.targets {
			if findExistingTargetID(m, t) != "" {
				continue // ON CONFLICT DO NOTHING
			}
			id := ids.NewID("tgt")
			rec := &kachorepo.TargetRecord{
				Target:        t,
				ID:            id,
				TargetGroupID: p.tgID,
				Status:        "ACTIVE",
				CreatedAt:     time.Now().UTC(),
				UpdatedAt:     time.Now().UTC(),
			}
			m[id] = rec
		}
	}
	for _, p := range w.pendingMark {
		m, ok := w.r.targets[p.tgID]
		if !ok {
			continue
		}
		now := time.Now().UTC()
		for _, id := range p.targetIDs {
			if rec, ok := m[id]; ok && rec.Status == "ACTIVE" {
				rec.Status = "DRAINING"
				t := now
				rec.DrainStartedAt = &t
				rec.UpdatedAt = now
			}
		}
	}
	w.r.outbox = append(w.r.outbox, w.pendingOutbox...)
	w.r.fga = append(w.r.fga, w.pendingFGA...)
	return nil
}

func (w *fakeWriter) Abort() {
	if w.committed {
		return
	}
	w.committed = true
	w.pendingOutbox = nil
	w.pendingFGA = nil
	w.pendingTGs = nil
	w.pendingTGDel = nil
	w.pendingAddTgt = nil
	w.pendingMark = nil
}

// ---- TG reader ----

type fakeTGReader struct{ r *fakeRepo }

func (q *fakeTGReader) Get(_ context.Context, id string) (*kachorepo.TargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	rec, ok := q.r.tgs[id]
	if !ok {
		return nil, fmt.Errorf("%w: TargetGroup %s not found", kachorepo.ErrNotFound, id)
	}
	c := *rec
	// inline targets
	if m, ok := q.r.targets[id]; ok && len(m) > 0 {
		c.Targets = make([]domain.Target, 0, len(m))
		for _, t := range m {
			c.Targets = append(c.Targets, t.Target)
		}
	} else {
		c.Targets = nil
	}
	return &c, nil
}

func (q *fakeTGReader) List(_ context.Context, f kachorepo.TargetGroupFilter, _ kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	var out []*kachorepo.TargetGroupRecord
	for _, rec := range q.r.tgs {
		if f.ProjectID != "" && string(rec.ProjectID) != f.ProjectID {
			continue
		}
		if f.Name != "" && string(rec.Name) != f.Name {
			continue
		}
		c := *rec
		out = append(out, &c)
	}
	return out, "", nil
}

func (q *fakeTGReader) ListByProject(ctx context.Context, projectID string, p kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return q.List(ctx, kachorepo.TargetGroupFilter{ProjectID: projectID}, p)
}

func (q *fakeTGReader) ListTargets(_ context.Context, tgID string) ([]*kachorepo.TargetRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	m, ok := q.r.targets[tgID]
	if !ok {
		return nil, nil
	}
	out := make([]*kachorepo.TargetRecord, 0, len(m))
	for _, rec := range m {
		c := *rec
		out = append(out, &c)
	}
	return out, nil
}

func (q *fakeTGReader) ListDrainingExpired(_ context.Context, _ string, _ int32) ([]*kachorepo.TargetRecord, error) {
	return nil, nil
}

func (q *fakeTGReader) HasAttachedLB(_ context.Context, tgID string) (bool, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	suffix := "/" + tgID
	for k := range q.r.pivot {
		if len(k) > len(suffix) && k[len(k)-len(suffix):] == suffix {
			return true, nil
		}
	}
	return false, nil
}

// ---- TG writer ----

type fakeTGWriter struct {
	r *fakeRepo
	w *fakeWriter
}

func (q *fakeTGWriter) Get(ctx context.Context, id string) (*kachorepo.TargetGroupRecord, error) {
	return (&fakeTGReader{r: q.r}).Get(ctx, id)
}
func (q *fakeTGWriter) List(ctx context.Context, f kachorepo.TargetGroupFilter, p kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return (&fakeTGReader{r: q.r}).List(ctx, f, p)
}
func (q *fakeTGWriter) ListByProject(ctx context.Context, projectID string, p kachorepo.Pagination) ([]*kachorepo.TargetGroupRecord, string, error) {
	return (&fakeTGReader{r: q.r}).ListByProject(ctx, projectID, p)
}
func (q *fakeTGWriter) ListTargets(ctx context.Context, tgID string) ([]*kachorepo.TargetRecord, error) {
	return (&fakeTGReader{r: q.r}).ListTargets(ctx, tgID)
}
func (q *fakeTGWriter) ListDrainingExpired(ctx context.Context, tgID string, delaySeconds int32) ([]*kachorepo.TargetRecord, error) {
	return (&fakeTGReader{r: q.r}).ListDrainingExpired(ctx, tgID, delaySeconds)
}
func (q *fakeTGWriter) HasAttachedLB(ctx context.Context, tgID string) (bool, error) {
	return (&fakeTGReader{r: q.r}).HasAttachedLB(ctx, tgID)
}

func (q *fakeTGWriter) Insert(_ context.Context, tg *domain.TargetGroup) (*kachorepo.TargetGroupRecord, error) {
	if q.r.failOnInsert != nil {
		return nil, q.r.failOnInsert
	}
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	if _, ok := q.r.tgs[string(tg.ID)]; ok {
		return nil, fmt.Errorf("%w: TargetGroup %s already exists", kachorepo.ErrAlreadyExists, tg.ID)
	}
	for _, existing := range q.r.tgs {
		if string(existing.ProjectID) == string(tg.ProjectID) &&
			string(existing.Name) == string(tg.Name) && string(tg.Name) != "" {
			return nil, fmt.Errorf("%w: TargetGroup '%s' already exists in project %s",
				kachorepo.ErrAlreadyExists, tg.Name, tg.ProjectID)
		}
	}
	rec := &kachorepo.TargetGroupRecord{
		TargetGroup: *tg,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	// Inline targets handled in same pending bucket as AddTargets.
	q.w.pendingTGs = append(q.w.pendingTGs, rec)
	if len(tg.Targets) > 0 {
		q.w.pendingAddTgt = append(q.w.pendingAddTgt, pendingAddTarget{
			tgID: string(tg.ID), targets: tg.Targets,
		})
	}
	return rec, nil
}

func (q *fakeTGWriter) Update(_ context.Context, tg *domain.TargetGroup) (*kachorepo.TargetGroupRecord, error) {
	if q.r.failOnUpdate != nil {
		return nil, q.r.failOnUpdate
	}
	q.r.mu.Lock()
	cur, ok := q.r.tgs[string(tg.ID)]
	q.r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: TargetGroup %s not found", kachorepo.ErrNotFound, tg.ID)
	}
	updated := *cur
	updated.Name = tg.Name
	updated.Description = tg.Description
	updated.Labels = tg.Labels
	updated.HealthCheck = tg.HealthCheck
	updated.DeregistrationDelaySeconds = tg.DeregistrationDelaySeconds
	updated.SlowStartSeconds = tg.SlowStartSeconds
	updated.UpdatedAt = time.Now().UTC()
	q.w.pendingTGs = append(q.w.pendingTGs, &updated)
	c := updated
	return &c, nil
}

func (q *fakeTGWriter) SetStatusCAS(_ context.Context, id string, expected, newStatus domain.TargetGroupStatus) (*kachorepo.TargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	cur, ok := q.r.tgs[id]
	if !ok {
		return nil, fmt.Errorf("%w: TargetGroup %s not found", kachorepo.ErrNotFound, id)
	}
	if cur.Status != expected {
		return nil, fmt.Errorf("%w: TargetGroup %s status is not %s", kachorepo.ErrFailedPrecondition, id, expected)
	}
	cur.Status = newStatus
	c := *cur
	q.w.pendingTGs = append(q.w.pendingTGs, &c)
	return &c, nil
}

func (q *fakeTGWriter) MoveProject(_ context.Context, id, newProjectID string) (*kachorepo.TargetGroupRecord, error) {
	q.r.mu.Lock()
	cur, ok := q.r.tgs[id]
	q.r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: TargetGroup %s not found", kachorepo.ErrNotFound, id)
	}
	updated := *cur
	updated.ProjectID = domain.ProjectID(newProjectID)
	updated.UpdatedAt = time.Now().UTC()
	q.w.pendingTGs = append(q.w.pendingTGs, &updated)
	c := updated
	return &c, nil
}

func (q *fakeTGWriter) AddTargets(_ context.Context, tgID string, targets []domain.Target) (int, error) {
	if q.r.failOnAddTargets != nil {
		return 0, q.r.failOnAddTargets
	}
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	if _, ok := q.r.tgs[tgID]; !ok {
		return 0, fmt.Errorf("%w: TargetGroup %s not found", kachorepo.ErrFailedPrecondition, tgID)
	}
	existing := q.r.targets[tgID]
	inserted := 0
	// Count новых identity (не дубликаты). Реальный insert происходит в Commit.
	for _, t := range targets {
		if findExistingTargetID(existing, t) != "" {
			continue
		}
		inserted++
	}
	q.w.pendingAddTgt = append(q.w.pendingAddTgt, pendingAddTarget{tgID: tgID, targets: targets})
	return inserted, nil
}

func (q *fakeTGWriter) RemoveTargetsMarkDraining(_ context.Context, tgID string, targetIDs []string) (int, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	m, ok := q.r.targets[tgID]
	if !ok {
		return 0, nil
	}
	affected := 0
	for _, id := range targetIDs {
		if rec, ok := m[id]; ok && rec.Status == "ACTIVE" {
			affected++
		}
	}
	q.w.pendingMark = append(q.w.pendingMark, pendingMarkDraining{tgID: tgID, targetIDs: targetIDs})
	return affected, nil
}

func (q *fakeTGWriter) DeleteTargetsDrained(_ context.Context, _ string, _ int32) (int, error) {
	return 0, nil
}

func (q *fakeTGWriter) Delete(_ context.Context, id string) error {
	if q.r.failOnDelete != nil {
		return q.r.failOnDelete
	}
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	if _, ok := q.r.tgs[id]; !ok {
		return fmt.Errorf("%w: TargetGroup %s not found", kachorepo.ErrNotFound, id)
	}
	// emulate FK 23503 when targets still exist (GWT-TGR-024).
	if m, ok := q.r.targets[id]; ok && len(m) > 0 {
		return fmt.Errorf("%w: TargetGroup %s has child targets (FK 23503)", kachorepo.ErrFailedPrecondition, id)
	}
	q.w.pendingTGDel = append(q.w.pendingTGDel, id)
	return nil
}

// ---- AttachedTG (ListByTG only) ----

type fakeATGReader struct{ r *fakeRepo }

func (q *fakeATGReader) Get(_ context.Context, lbID, tgID string) (*kachorepo.AttachedTargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	if q.r.pivot[lbID+"/"+tgID] {
		return &kachorepo.AttachedTargetGroupRecord{LoadBalancerID: lbID, TargetGroupID: tgID}, nil
	}
	return nil, fmt.Errorf("%w: AttachedTargetGroup %s/%s not found", kachorepo.ErrNotFound, lbID, tgID)
}
func (q *fakeATGReader) ListByLB(_ context.Context, lbID string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	var out []*kachorepo.AttachedTargetGroupRecord
	prefix := lbID + "/"
	for k := range q.r.pivot {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, &kachorepo.AttachedTargetGroupRecord{LoadBalancerID: lbID, TargetGroupID: k[len(prefix):]})
		}
	}
	return out, nil
}
func (q *fakeATGReader) ListByTG(_ context.Context, tgID string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	var out []*kachorepo.AttachedTargetGroupRecord
	suffix := "/" + tgID
	for k := range q.r.pivot {
		if len(k) > len(suffix) && k[len(k)-len(suffix):] == suffix {
			lb := k[:len(k)-len(suffix)]
			out = append(out, &kachorepo.AttachedTargetGroupRecord{LoadBalancerID: lb, TargetGroupID: tgID})
		}
	}
	return out, nil
}

type fakeATGWriter struct{ r *fakeRepo }

func (q *fakeATGWriter) Get(ctx context.Context, lbID, tgID string) (*kachorepo.AttachedTargetGroupRecord, error) {
	return (&fakeATGReader{r: q.r}).Get(ctx, lbID, tgID)
}
func (q *fakeATGWriter) ListByLB(ctx context.Context, lbID string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	return (&fakeATGReader{r: q.r}).ListByLB(ctx, lbID)
}
func (q *fakeATGWriter) ListByTG(ctx context.Context, tgID string) ([]*kachorepo.AttachedTargetGroupRecord, error) {
	return (&fakeATGReader{r: q.r}).ListByTG(ctx, tgID)
}
func (q *fakeATGWriter) Attach(_ context.Context, _, _ string, _ int32) (*kachorepo.AttachedTargetGroupRecord, bool, error) {
	return nil, false, errors.New("fakeATGWriter.Attach not used by TG use-cases")
}
func (q *fakeATGWriter) Detach(_ context.Context, _, _ string) error {
	return errors.New("fakeATGWriter.Detach not used by TG use-cases")
}

// ---- Outbox ----

type fakeOutbox struct {
	r *fakeRepo
	w *fakeWriter
}

func (o *fakeOutbox) Emit(_ context.Context, resourceType, resourceID, projectID, action string, payload map[string]any) error {
	if o.r.failOnOutbox != nil {
		return o.r.failOnOutbox
	}
	o.w.pendingOutbox = append(o.w.pendingOutbox, fakeOutboxEvent{
		ResourceType: resourceType, ResourceID: resourceID, ProjectID: projectID,
		Action: action, Payload: payload,
	})
	return nil
}

// ---- Unused-by-TG stubs (panic-on-call defensive) ----

type fakeLBStub struct{}

func (fakeLBStub) Get(context.Context, string) (*kachorepo.LoadBalancerRecord, error) {
	return nil, errors.New("fakeLBStub.Get not used by TG use-cases")
}
func (fakeLBStub) List(context.Context, kachorepo.LoadBalancerFilter, kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return nil, "", nil
}
func (fakeLBStub) ListByProject(context.Context, string, kachorepo.Pagination) ([]*kachorepo.LoadBalancerRecord, string, error) {
	return nil, "", nil
}
func (fakeLBStub) HasListeners(context.Context, string) (bool, error)            { return false, nil }
func (fakeLBStub) HasAttachedTargetGroups(context.Context, string) (bool, error) { return false, nil }
func (fakeLBStub) Insert(context.Context, *domain.LoadBalancer) (*kachorepo.LoadBalancerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeLBStub) Update(context.Context, *domain.LoadBalancer) (*kachorepo.LoadBalancerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeLBStub) SetStatusCAS(context.Context, string, domain.LBStatus, domain.LBStatus) (*kachorepo.LoadBalancerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeLBStub) MoveProject(context.Context, string, string) (*kachorepo.LoadBalancerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeLBStub) Delete(context.Context, string) error {
	return errors.New("not used")
}

type fakeListenerStub struct{}

func (fakeListenerStub) Get(context.Context, string) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeListenerStub) List(context.Context, kachorepo.ListenerFilter, kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	return nil, "", nil
}
func (fakeListenerStub) ListByLB(context.Context, string, kachorepo.Pagination) ([]*kachorepo.ListenerRecord, string, error) {
	return nil, "", nil
}
func (fakeListenerStub) Insert(context.Context, *domain.Listener) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeListenerStub) Update(context.Context, *domain.Listener) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeListenerStub) SetStatusCAS(context.Context, string, domain.ListenerStatus, domain.ListenerStatus) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeListenerStub) SetAllocatedAddress(context.Context, string, string) (*kachorepo.ListenerRecord, error) {
	return nil, errors.New("not used")
}
func (fakeListenerStub) MoveProject(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (fakeListenerStub) Delete(context.Context, string) error {
	return errors.New("not used")
}

// ---- Helper: identity-key match (mirror pg writer ON CONFLICT) ----

// findExistingTargetID — возвращает target.ID существующего ряда, identity которого
// совпадает с t. "" если не найдено.
func findExistingTargetID(existing map[string]*kachorepo.TargetRecord, t domain.Target) string {
	for _, rec := range existing {
		if targetIdentityEqual(t, rec.Target) {
			return rec.ID
		}
	}
	return ""
}

// ---- fakeOpsRepo (mirror loadbalancer pkg pattern) -------------------------

type fakeOpsRepo struct {
	mu  sync.Mutex
	ops map[string]*operations.Operation
}

func newFakeOpsRepo() *fakeOpsRepo {
	return &fakeOpsRepo{ops: make(map[string]*operations.Operation)}
}

func (r *fakeOpsRepo) Create(ctx context.Context, op operations.Operation) error {
	return r.CreateWithPrincipal(ctx, op, op.Principal)
}
func (r *fakeOpsRepo) CreateWithPrincipal(_ context.Context, op operations.Operation, p operations.Principal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.ops[op.ID]; ok {
		return errors.New("op already exists")
	}
	op.Principal = p
	c := op
	r.ops[op.ID] = &c
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
func (r *fakeOpsRepo) List(_ context.Context, _ operations.ListFilter) ([]operations.Operation, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]operations.Operation, 0, len(r.ops))
	for _, op := range r.ops {
		c := *op
		out = append(out, c)
	}
	return out, "", nil
}
func (r *fakeOpsRepo) MarkDone(_ context.Context, id string, response *anypb.Any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	op.Response = response
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
	op.Error = &status.Status{Code: 1, Message: "cancelled"}
	op.ModifiedAt = time.Now().UTC()
	return nil
}

// awaitOpDone — детерминированно ждёт Operation.Done (5s polling).
func awaitOpDone(t *testing.T, repo *fakeOpsRepo, opID string) *operations.Operation {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		op, err := repo.Get(context.Background(), opID)
		if err == nil && op.Done {
			return op
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("operation %s did not finish within 5s", opID)
	return nil
}

// ---- Peer-client fakes ----

type fakeProjectClient struct {
	getFunc func(ctx context.Context, id string) (*iam.Project, error)
}

func (f *fakeProjectClient) Get(ctx context.Context, id string) (*iam.Project, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, id)
	}
	return &iam.Project{ID: id, Status: "ACTIVE"}, nil
}

type fakeRegionClient struct {
	getFunc func(ctx context.Context, id string) (*geo.Region, error)
}

func (f *fakeRegionClient) Get(ctx context.Context, id string) (*geo.Region, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, id)
	}
	return &geo.Region{ID: id}, nil
}

type fakeInstanceClient struct {
	getFunc func(ctx context.Context, id string) (*compute.Instance, error)
}

func (f *fakeInstanceClient) Get(ctx context.Context, id string) (*compute.Instance, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, id)
	}
	return &compute.Instance{ID: id, ZoneID: "ru-central1-a", PrimaryNICAddress: "10.0.0.10"}, nil
}

type fakeNICClient struct {
	getFunc func(ctx context.Context, id string) (*vpc.NetworkInterface, error)
}

func (f *fakeNICClient) Get(ctx context.Context, id string) (*vpc.NetworkInterface, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, id)
	}
	return &vpc.NetworkInterface{ID: id, SubnetID: "e9b-default-sub"}, nil
}

type fakeSubnetClient struct {
	getFunc func(ctx context.Context, id string) (*vpc.Subnet, error)
}

func (f *fakeSubnetClient) Get(ctx context.Context, id string) (*vpc.Subnet, error) {
	if f.getFunc != nil {
		return f.getFunc(ctx, id)
	}
	return &vpc.Subnet{ID: id, ZoneID: "ru-central1-a", V4CIDRBlocks: []string{"10.0.0.0/24"}}, nil
}

// fakeFGARegisterOutbox records SEC-D FGARegisterOutbox.Emit into the writer's
// pending buffer (flushed to fakeRepo.fga on Commit, dropped on Abort).
type fakeFGARegisterOutbox struct{ w *fakeWriter }

func (o *fakeFGARegisterOutbox) Emit(_ context.Context, eventType string, intent domain.FGARegisterIntent) error {
	if o.w.r.failOnOutbox != nil {
		return o.w.r.failOnOutbox
	}
	o.w.pendingFGA = append(o.w.pendingFGA, fgaIntentEvent{EventType: eventType, Intent: intent})
	return nil
}

// ensure interface conformance.
var (
	_ kachorepo.Repository   = (*fakeRepo)(nil)
	_ operations.Repo        = (*fakeOpsRepo)(nil)
	_ ProjectClient          = (*fakeProjectClient)(nil)
	_ RegionClient           = (*fakeRegionClient)(nil)
	_ InstanceClient         = (*fakeInstanceClient)(nil)
	_ NetworkInterfaceClient = (*fakeNICClient)(nil)
	_ SubnetClient           = (*fakeSubnetClient)(nil)
)

// ---- Shared test helpers ----

// projectIamProjection — type-alias на iam.Project (используется в peer-fake
// инжекторах getFunc).
type projectIamProjection = iam.Project

// projectNotFound — возвращает sentinel-обёрнутую ошибку «Project <id> not found»
// (mirror контракта iam.ProjectClient.Get для NotFound).
func projectNotFound(id string) error {
	return fmt.Errorf("%w: Project %s not found", domain.ErrNotFound, id)
}

// contextWithUser — добавляет Principal "user:<name>" в ctx (для проверки FGA
// creator-tuple write).
func contextWithUser(name string) context.Context {
	return operations.WithPrincipal(context.Background(), operations.Principal{
		Type: "user", ID: name,
	})
}

// fieldViolationsText — собирает все FieldViolation.Description из gRPC-status
// err.Details (corelib InvalidArgument().AddFieldViolation()) в одну строку,
// + сам status.Message впереди. Используется для assert.Contains в тестах,
// где verbatim текст ошибки лежит в BadRequest details.
func fieldViolationsText(err error) string {
	if err == nil {
		return ""
	}
	st, ok := grpcStatus.FromError(err)
	if !ok {
		return err.Error()
	}
	parts := []string{st.Message()}
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			for _, v := range br.GetFieldViolations() {
				parts = append(parts, v.GetField()+": "+v.GetDescription())
			}
		}
	}
	return strings.Join(parts, " | ")
}

// kachoTarget — фикстура: TargetRecord с фиксированным id, готов к seedTarget.
func kachoTarget(tgID string, t domain.Target) kachorepo.TargetRecord {
	return kachorepo.TargetRecord{
		Target:        t,
		ID:            ids.NewID("tgt"),
		TargetGroupID: tgID,
		Status:        "ACTIVE",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
}

// mkOp — minimal Operation (для прямой записи в opsRepo тестов List).
func mkOp(id string) operations.Operation {
	return operations.Operation{
		ID:          id,
		Description: "test op",
		CreatedAt:   time.Now().UTC(),
		ModifiedAt:  time.Now().UTC(),
	}
}

// makeTG — фикстура: minimum-valid TG-record. Готов к seedTG.
func makeTG(projectID, name string) *kachorepo.TargetGroupRecord {
	return &kachorepo.TargetGroupRecord{
		TargetGroup: domain.TargetGroup{
			ID:                         domain.ResourceID(ids.NewID(ids.PrefixTargetGroup)),
			ProjectID:                  domain.ProjectID(projectID),
			RegionID:                   "ru-central1",
			Name:                       domain.LbName(name),
			Description:                "",
			Labels:                     domain.LbLabels{},
			DeregistrationDelaySeconds: 300,
			SlowStartSeconds:           0,
			Status:                     domain.TargetGroupStatusActive,
			HealthCheck: domain.HealthCheck{
				Name:               "hc-tcp",
				Interval:           domain.DefaultHealthInterval,
				Timeout:            domain.DefaultHealthTimeout,
				UnhealthyThreshold: domain.DefaultUnhealthyThreshold,
				HealthyThreshold:   domain.DefaultHealthyThreshold,
				TCP:                &domain.HealthCheckTCP{Port: 80},
			},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
}
