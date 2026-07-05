// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operation

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"
)

// TestOperation_OP005_CancelInFlight —.
//
// Given: long-running op in-flight; subject = creator of op.
// When:  OperationService.Cancel(operation_id=<op-id>).
// Then:  OK; eventual op state done=true, error=CANCELLED.
func TestOperation_OP005_CancelInFlight(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	op, err := operations.New(ids.PrefixOperationNLB, "Create NLB long-op", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), op))

	got, err := h.Cancel(context.Background(), &operationpb.CancelOperationRequest{
		OperationId: op.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, op.ID, got.GetId())
	assert.True(t, got.GetDone(), "op must be done after Cancel")

	require.NotNil(t, got.GetError(), "error result oneof must be set for CANCELLED")
	assert.EqualValues(t, 1, got.GetError().GetCode(), "code must be CANCELLED (1)")
	assert.Contains(t, got.GetError().GetMessage(), "cancel")

	// repo-state также done=true
	stored, gerr := repo.Get(context.Background(), op.ID)
	require.NoError(t, gerr)
	assert.True(t, stored.Done)
}

// TestOperation_OP006_CancelAlreadyDone —.
//
// When:  Cancel an op already done=true.
// Then:  FAILED_PRECONDITION; message contains "already completed".
func TestOperation_OP006_CancelAlreadyDone(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	op, err := operations.New(ids.PrefixOperationNLB, "Done op", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), op))
	require.NoError(t, repo.MarkDone(context.Background(), op.ID, nil))

	_, err = h.Cancel(context.Background(), &operationpb.CancelOperationRequest{
		OperationId: op.ID,
	})
	require.Error(t, err)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "already completed")
}

func TestOperation_Cancel_NotFound(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	_, err := h.Cancel(context.Background(), &operationpb.CancelOperationRequest{
		OperationId: ids.NewID(ids.PrefixOperationNLB),
	})
	require.Error(t, err)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestOperation_Cancel_EmptyOperationID(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	_, err := h.Cancel(context.Background(), &operationpb.CancelOperationRequest{OperationId: ""})
	require.Error(t, err)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestOperation_Cancel_CrossTenant_NotFound — (owner-scope on Cancel).
//
// Given: in-flight Operation created by principal A.
// When:  principal B calls Cancel(op-id).
// Then:  existence-hiding NotFound AND the op stays in-flight (B's Cancel had no
//
//	effect — a foreign caller must not be able to abort another tenant's mutation).
func TestOperation_Cancel_CrossTenant_NotFound(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	op, err := operations.New(ids.PrefixOperationNLB, "in-flight owned by A", nil)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(context.Background(), op,
		operations.Principal{Type: "user", ID: "usr_alice"}))

	ctxB := operations.WithPrincipal(context.Background(),
		operations.Principal{Type: "user", ID: "usr_bob"})
	_, err = h.Cancel(ctxB, &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.Error(t, err)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())

	stored, gerr := repo.Get(context.Background(), op.ID)
	require.NoError(t, gerr)
	assert.False(t, stored.Done, "cross-tenant Cancel must not affect the op")
}

// TestOperation_Cancel_Owner_OK — creator cancels their own in-flight op (no regress).
func TestOperation_Cancel_Owner_OK(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	op, err := operations.New(ids.PrefixOperationNLB, "owned in-flight", nil)
	require.NoError(t, err)
	pa := operations.Principal{Type: "user", ID: "usr_alice"}
	require.NoError(t, repo.CreateWithPrincipal(context.Background(), op, pa))

	got, err := h.Cancel(operations.WithPrincipal(context.Background(), pa),
		&operationpb.CancelOperationRequest{OperationId: op.ID})
	require.NoError(t, err)
	assert.True(t, got.GetDone())
	require.NotNil(t, got.GetError())
	assert.EqualValues(t, 1, got.GetError().GetCode())
}

func TestOperation_Cancel_RepoError_MapsToInternal(t *testing.T) {
	fail := &failingOpsRepo{cancelErr: errors.New("pgx: connection refused")}
	h := NewHandler(fail)

	_, err := h.Cancel(context.Background(), &operationpb.CancelOperationRequest{
		OperationId: ids.NewID(ids.PrefixOperationNLB),
	})
	require.Error(t, err)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	assert.NotContains(t, st.Message(), "pgx")
}

func TestOperation_Cancel_DoubleCancel(t *testing.T) {
	// Дополнительный case: Cancel применённый дважды подряд — первый OK, второй FailedPrecondition.
	// Контракт: Cancel НЕ идемпотентен (расхождение с типичным
	// Delete-семантикой; design choice следует по конвенции Kachō / kacho-vpc / kacho-compute).
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	op, err := operations.New(ids.PrefixOperationNLB, "double-cancel op", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), op))

	_, err = h.Cancel(context.Background(), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.NoError(t, err)

	_, err = h.Cancel(context.Background(), &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.Error(t, err)
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
}
