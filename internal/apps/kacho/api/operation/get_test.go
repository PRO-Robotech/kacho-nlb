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

// TestOperation_OP001_GetInFlight — GWT-OP-001 from acceptance §7.
//
// Given: Operation existed in `nlb` ops, done=false.
// When:  OperationService.Get(operation_id=<op-id>).
// Then:  OK; Operation message with id, description, created_at, done=false, metadata.
func TestOperation_OP001_GetInFlight(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	op, err := operations.New(ids.PrefixOperationNLB,
		"Create network load balancer test-nlb-001", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), op))

	got, err := h.Get(context.Background(), &operationpb.GetOperationRequest{
		OperationId: op.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, op.ID, got.GetId())
	assert.Equal(t, op.Description, got.GetDescription())
	assert.False(t, got.GetDone())
	require.NotNil(t, got.GetCreatedAt())
	assert.Nil(t, got.GetError())    // result oneof empty for in-flight
	assert.Nil(t, got.GetResponse()) // result oneof empty for in-flight
}

// TestOperation_OP002_GetCompletedResponse — GWT-OP-002 (success completion).
//
// Given: Operation done=true with response.
// When:  Get.
// Then:  done=true, response.value = <Resource proto>.
func TestOperation_OP002_GetCompletedResponse(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	op, err := operations.New(ids.PrefixOperationNLB, "Delete NLB", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), op))
	require.NoError(t, repo.MarkDone(context.Background(), op.ID, nil))

	got, err := h.Get(context.Background(), &operationpb.GetOperationRequest{
		OperationId: op.ID,
	})
	require.NoError(t, err)
	assert.True(t, got.GetDone())
	require.NotNil(t, got.GetModifiedAt())
}

// TestOperation_OP003_GetNotFound — GWT-OP-003.
//
// When: Get unknown op-id. Then: NOT_FOUND.
func TestOperation_OP003_GetNotFound(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	_, err := h.Get(context.Background(), &operationpb.GetOperationRequest{
		OperationId: ids.NewID(ids.PrefixOperationNLB),
	})
	require.Error(t, err)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
	assert.Contains(t, st.Message(), "not found")
}

func TestOperation_Get_EmptyOperationID(t *testing.T) {
	repo := newFakeOpsRepo()
	h := NewHandler(repo)

	_, err := h.Get(context.Background(), &operationpb.GetOperationRequest{OperationId: ""})
	require.Error(t, err)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "operation_id required")
}

func TestOperation_Get_RepoError_MapsToInternal(t *testing.T) {
	// Любая non-sentinel ошибка из repo → codes.Internal без leak'а текста (no pgx detail).
	fail := &failingOpsRepo{getErr: errors.New("pgx: connection refused to localhost:5432")}
	h := NewHandler(fail)

	_, err := h.Get(context.Background(), &operationpb.GetOperationRequest{
		OperationId: ids.NewID(ids.PrefixOperationNLB),
	})
	require.Error(t, err)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Internal, st.Code())
	assert.NotContains(t, st.Message(), "pgx")
	assert.NotContains(t, st.Message(), "localhost")
}
