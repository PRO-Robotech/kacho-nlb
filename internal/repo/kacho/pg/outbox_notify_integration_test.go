package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// dsnForLISTEN — для LISTEN/NOTIFY нужно dedicated conn (pgx.Connect),
// нельзя через pgxpool. Этот helper извлекает DSN из pool'а тестового
// сетапа. Реализация — взять последний созданный DSN через setupTestDB.
// (Удобнее: setupTestDB возвращает DSN; используем его напрямую.)
//
// Тест ниже принимает DSN параметром и сам открывает pgx.Conn.

// TestOutboxNotify_FiresOnInsert — GWT-DB-003: trigger nlb_outbox_notify_trg
// шлёт pg_notify('nlb_outbox', sequence_no::text) после INSERT.
func TestOutboxNotify_FiresOnInsert(t *testing.T) {
	dsn := setupTestDB(t)
	tc := newTestCtxFromDSN(t, dsn)
	repo := tc.Repo
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Открыть dedicated conn для LISTEN.
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	_, err = conn.Exec(ctx, "LISTEN nlb_outbox")
	require.NoError(t, err)

	// В отдельной goroutine ждём notification (с deadline).
	notifyChan := make(chan *pgconn.Notification, 1)
	errChan := make(chan error, 1)
	go func() {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			errChan <- err
			return
		}
		notifyChan <- n
	}()

	// Выполнить INSERT через repo.
	lb := newLB("prj01NOTI1234567890ll", "noti-lb")
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		require.NoError(t, w.Outbox().Emit(ctx, "nlb_load_balancer", string(lb.ID),
			string(lb.ProjectID), "CREATED", map[string]any{"id": string(lb.ID)}))
	})

	select {
	case n := <-notifyChan:
		assert.Equal(t, "nlb_outbox", n.Channel)
		// Payload = sequence_no::text (число).
		require.NotEmpty(t, n.Payload)
		// Should parse as numeric (positive integer).
		assert.True(t, allDigits(n.Payload), "payload %q must be decimal", n.Payload)
	case err := <-errChan:
		t.Fatalf("WaitForNotification: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no notification received within 5s")
	}
}

// allDigits — true если s содержит только 0..9 (хотя бы один символ).
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) == -1
}

// newTestCtxFromDSN — версия newTestCtx, принимающая уже готовый DSN
// (testTestcontainer установлен ранее). Используется когда тест хочет иметь
// доступ к DSN для отдельного pgx.Connect (LISTEN/NOTIFY).
func newTestCtxFromDSN(t testing.TB, dsn string) *testContext {
	t.Helper()
	repo, cleanup := newRepo(t, dsn)
	t.Cleanup(cleanup)
	// Pool достать не получится без reflection; для LISTEN/NOTIFY теста pool
	// не нужен — только Repo.
	return &testContext{Repo: repo}
}
