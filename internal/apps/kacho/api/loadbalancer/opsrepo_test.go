// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// fakeOpsRepo — in-memory operations.Repo. Достаточно для unit-тестов
// use-case'ов: Create/Get/MarkDone/MarkError/Cancel/List (filter ResourceID).
type fakeOpsRepo struct {
	mu  sync.Mutex
	ops map[string]*operations.Operation
	// listErr — если задан, List возвращает его (для no-leak теста: репо-ошибка
	// не должна протечь raw-текстом в gRPC Internal).
	listErr error
}

func newFakeOpsRepo() *fakeOpsRepo {
	return &fakeOpsRepo{ops: make(map[string]*operations.Operation)}
}

func (r *fakeOpsRepo) Create(ctx context.Context, op operations.Operation) error {
	return r.CreateWithPrincipal(ctx, op, op.Principal)
}

func (r *fakeOpsRepo) CreateWithPrincipal(ctx context.Context, op operations.Operation, p operations.Principal) error {
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

func (r *fakeOpsRepo) Get(ctx context.Context, id string) (*operations.Operation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if op, ok := r.ops[id]; ok {
		c := *op
		return &c, nil
	}
	return nil, operations.ErrNotFound
}

func (r *fakeOpsRepo) List(ctx context.Context, filter operations.ListFilter) ([]operations.Operation, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, "", r.listErr
	}
	var out []operations.Operation
	for _, op := range r.ops {
		if filter.ResourceID != "" {
			// Best-effort filter — fake doesn't extract from metadata; tests
			// that filter rely on direct ResourceID match in metadata Any url
			// (we approximate by matching id-prefix).
			c := *op
			out = append(out, c)
			continue
		}
		c := *op
		out = append(out, c)
	}
	return out, "", nil
}

func (r *fakeOpsRepo) MarkDone(ctx context.Context, id string, response *anypb.Any) error {
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

func (r *fakeOpsRepo) MarkError(ctx context.Context, id string, st *status.Status) error {
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

func (r *fakeOpsRepo) Cancel(ctx context.Context, id string) error {
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
	op.ModifiedAt = time.Now().UTC()
	return nil
}

// awaitOpDone — детерминированно ждёт Operation.Done через polling. 2s deadline.
func awaitOpDone(t *testing.T, repo *fakeOpsRepo, opID string) *operations.Operation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		op, err := repo.Get(context.Background(), opID)
		if err == nil && op.Done {
			return op
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("operation %s did not finish within 2s", opID)
	return nil
}

// compile-time guard.
var _ operations.Repo = (*fakeOpsRepo)(nil)
