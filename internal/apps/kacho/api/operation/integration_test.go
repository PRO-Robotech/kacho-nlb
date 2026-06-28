// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operation_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-corelib/operations"

	operationpb "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"

	opapi "github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/operation"
	"github.com/PRO-Robotech/kacho-nlb/internal/migrations"
)

// setupTestDB поднимает testcontainers Postgres 16 + применяет baseline migrations
// (0001_initial.sql) → возвращает DSN с search_path=kacho_nlb,public.
//
// Зеркалит проверенный pattern kacho-vpc/internal/repo/integration_test.go.
func setupTestDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	pgc, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase("kacho_nlb_test"),
		postgres.WithUsername("nlb"),
		postgres.WithPassword("nlb"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgc.Terminate(ctx) })

	dsn, err := pgc.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(db, "."))

	return appendSearchPathOptions(dsn)
}

func appendSearchPathOptions(dsn string) string {
	const optionsParam = "options=-c%20search_path%3Dkacho_nlb%2Cpublic"
	if strings.Contains(dsn, "options=") || strings.Contains(dsn, "options%3D") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + optionsParam
}

// TestIntegration_OperationService_GetCancel_AgainstPostgres покрывает
// happy-path Get + Cancel против реальной БД с baseline migrations:
//   - Insert op через operations.Repo.Create → handler.Get → done=false.
//   - handler.Cancel → done=true, error.code=CANCELLED.
//   - handler.Cancel снова → FailedPrecondition.
//   - handler.Get unknown id → NotFound.
//
// Соответствует (002/003/005/006).
func TestIntegration_OperationService_GetCancel_AgainstPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	repo := operations.NewRepo(pool, "kacho_nlb")
	handler := opapi.NewHandler(repo)

	// --- Get in-flight op ---
	op, err := operations.New(ids.PrefixOperationNLB,
		"Create NLB integration-test", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	got, err := handler.Get(ctx, &operationpb.GetOperationRequest{OperationId: op.ID})
	require.NoError(t, err)
	assert.Equal(t, op.ID, got.GetId())
	assert.False(t, got.GetDone())
	assert.Equal(t, op.Description, got.GetDescription())

	// --- Get completed (success path: MarkDone with response) ---
	op2, err := operations.New(ids.PrefixOperationNLB, "Delete NLB completed", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op2))
	emptyResp, err := anypb.New(&emptypb.Empty{})
	require.NoError(t, err)
	require.NoError(t, repo.MarkDone(ctx, op2.ID, emptyResp))

	got2, err := handler.Get(ctx, &operationpb.GetOperationRequest{OperationId: op2.ID})
	require.NoError(t, err)
	assert.True(t, got2.GetDone())
	require.NotNil(t, got2.GetResponse(), "completed op should have response oneof")

	// --- Get unknown id ---
	_, err = handler.Get(ctx, &operationpb.GetOperationRequest{
		OperationId: ids.NewID(ids.PrefixOperationNLB),
	})
	require.Error(t, err)
	st, _ := grpcstatus.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code())

	// --- Cancel in-flight ---
	cancelled, err := handler.Cancel(ctx, &operationpb.CancelOperationRequest{
		OperationId: op.ID,
	})
	require.NoError(t, err)
	assert.True(t, cancelled.GetDone(), "Cancel must flip done=true")
	require.NotNil(t, cancelled.GetError(), "Cancel must populate error oneof")
	assert.EqualValues(t, 1, cancelled.GetError().GetCode(), "Cancelled code = 1")
	assert.Contains(t, strings.ToLower(cancelled.GetError().GetMessage()), "cancel")

	// Verify DB-state: re-read op via raw repo (not handler).
	stored, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, stored.Done)
	require.NotNil(t, stored.Error)
	assert.EqualValues(t, 1, stored.Error.GetCode())

	// --- Cancel already done → FailedPrecondition ---
	_, err = handler.Cancel(ctx, &operationpb.CancelOperationRequest{OperationId: op.ID})
	require.Error(t, err)
	st, _ = grpcstatus.FromError(err)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "already completed")

	// Cancel on an op that finished with response (not via Cancel) — also FailedPrecondition.
	_, err = handler.Cancel(ctx, &operationpb.CancelOperationRequest{OperationId: op2.ID})
	require.Error(t, err)
	st, _ = grpcstatus.FromError(err)
	assert.Equal(t, codes.FailedPrecondition, st.Code())

	// Cancel unknown id → NotFound.
	_, err = handler.Cancel(ctx, &operationpb.CancelOperationRequest{
		OperationId: ids.NewID(ids.PrefixOperationNLB),
	})
	require.Error(t, err)
	st, _ = grpcstatus.FromError(err)
	assert.Equal(t, codes.NotFound, st.Code())
}

// TestIntegration_OperationService_CancelRace проверяет атомарность Cancel:
// две параллельные горутины Cancel на одну op — ровно одна должна получить OK,
// другая — FailedPrecondition. Это покрывает single-statement CAS-семантику
// `UPDATE... WHERE id=$1 AND done=false` в kacho-corelib/operations.Cancel.
func TestIntegration_OperationService_CancelRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	dsn := setupTestDB(t)
	pool, err := coredb.NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	repo := operations.NewRepo(pool, "kacho_nlb")
	handler := opapi.NewHandler(repo)

	op, err := operations.New(ids.PrefixOperationNLB, "race op", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	const N = 8
	errs := make(chan error, N)
	start := make(chan struct{})
	for range N {
		go func() {
			<-start // align goroutines
			_, e := handler.Cancel(ctx, &operationpb.CancelOperationRequest{OperationId: op.ID})
			errs <- e
		}()
	}
	close(start)

	var okCount, preCount int
	for range N {
		e := <-errs
		if e == nil {
			okCount++
			continue
		}
		st, _ := grpcstatus.FromError(e)
		assert.Equal(t, codes.FailedPrecondition, st.Code(),
			"all non-winners must be FailedPrecondition; got %v", st.Code())
		preCount++
	}
	assert.Equal(t, 1, okCount, "exactly one Cancel must win the CAS race")
	assert.Equal(t, N-1, preCount, "all other Cancel calls must see done=true")
}
