package operation

import (
	"context"
	"sort"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// fakeOpsRepo — in-memory реализация operations.Repo для unit-тестов
// OperationServiceServer (handler-уровень).
//
// Семантика зеркалит pgRepo из kacho-corelib/operations:
//   - Cancel над done=true → ErrAlreadyDone (FailedPrecondition в handler).
//   - Get/Cancel над несуществующим id → ErrNotFound.
//   - Cancel заполняет op.Error.Code = 1 (Cancelled) + message "operation cancelled"
//     до return (handler потом делает re-Get для актуального состояния — fake
//     должен отдавать актуал).
//
// Локальный для пакета (по принципу minimum-surface — этот fake не нужен другим
// пакетам, не выносим в shared `repomock`).
type fakeOpsRepo struct {
	mu  sync.Mutex
	ops map[string]*operations.Operation
}

func newFakeOpsRepo() *fakeOpsRepo {
	return &fakeOpsRepo{ops: make(map[string]*operations.Operation)}
}

func (r *fakeOpsRepo) Create(_ context.Context, op operations.Operation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := op
	if cp.Principal == (operations.Principal{}) {
		cp.Principal = operations.SystemPrincipal()
	}
	r.ops[cp.ID] = &cp
	return nil
}

func (r *fakeOpsRepo) CreateWithPrincipal(_ context.Context, op operations.Operation, p operations.Principal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op.Principal = p
	cp := op
	r.ops[cp.ID] = &cp
	return nil
}

func (r *fakeOpsRepo) Get(_ context.Context, id string) (*operations.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return nil, operations.ErrNotFound
	}
	cp := *op // shallow copy: caller не должен мутировать shared-state.
	return &cp, nil
}

func (r *fakeOpsRepo) List(_ context.Context, filter operations.ListFilter) ([]operations.Operation, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]operations.Operation, 0, len(r.ops))
	for _, op := range r.ops {
		if filter.ResourceID != "" {
			// Тест не использует resource_id-фильтр; защитный no-op.
			continue
		}
		out = append(out, *op)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
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
	op.ModifiedAt = time.Now().UTC()
	op.Response = resp
	return nil
}

func (r *fakeOpsRepo) MarkError(_ context.Context, id string, errStatus *status.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	op, ok := r.ops[id]
	if !ok {
		return operations.ErrNotFound
	}
	op.Done = true
	op.ModifiedAt = time.Now().UTC()
	op.Error = errStatus
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
	op.ModifiedAt = time.Now().UTC()
	op.Error = &status.Status{Code: 1, Message: "operation cancelled"} // codes.Cancelled
	return nil
}

// failingOpsRepo — opt-in fake, который возвращает кастомные ошибки на Get / Cancel.
// Используется для проверки Internal-маппинга (any-non-sentinel error → codes.Internal).
type failingOpsRepo struct {
	getErr    error
	cancelErr error
}

func (r *failingOpsRepo) Create(_ context.Context, _ operations.Operation) error { return nil }
func (r *failingOpsRepo) CreateWithPrincipal(_ context.Context, _ operations.Operation, _ operations.Principal) error {
	return nil
}
func (r *failingOpsRepo) Get(_ context.Context, _ string) (*operations.Operation, error) {
	return nil, r.getErr
}
func (r *failingOpsRepo) List(_ context.Context, _ operations.ListFilter) ([]operations.Operation, string, error) {
	return nil, "", nil
}
func (r *failingOpsRepo) MarkDone(_ context.Context, _ string, _ *anypb.Any) error  { return nil }
func (r *failingOpsRepo) MarkError(_ context.Context, _ string, _ *status.Status) error {
	return nil
}
func (r *failingOpsRepo) Cancel(_ context.Context, _ string) error { return r.cancelErr }

// compile-time gates: оба fake-репо удовлетворяют operations.Repo.
var (
	_ operations.Repo = (*fakeOpsRepo)(nil)
	_ operations.Repo = (*failingOpsRepo)(nil)
)
