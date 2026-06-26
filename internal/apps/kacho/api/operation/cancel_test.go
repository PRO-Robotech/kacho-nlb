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
	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
)

// TestOperation_OP005_CancelInFlight — GWT-OP-005.
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

// TestOperation_OP006_CancelAlreadyDone — GWT-OP-006.
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
	// Контракт acceptance §7 GWT-OP-006: Cancel НЕ идемпотентен (расхождение с типичным
	// Delete-семантикой; design choice следует verbatim YC / kacho-vpc / kacho-compute).
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
