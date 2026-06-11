package pg_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// fga_register_outbox_integration_test.go — SEC-D S1: FGA-register-intent
// пишется в той же writer-tx, что и INSERT/DELETE ресурса (epic §3.1 Вариант A,
// no dual-write). Сценарии SEC-D-01/02/03/05/06.
//
// register-drainer тут НЕ запущен — изолируем ЗАПИСЬ intent от ПРИМЕНЕНИЯ
// (drainer-applier тесты — fga_register_drainer_integration_test.go).

// queryRegisterRows читает все строки fga_register_outbox (для assert'ов).
type registerRow struct {
	id           int64
	eventType    string
	payload      []byte
	resourceKind string
	resourceID   string
	sentAt       *string
	attemptCount int
	lastError    *string
}

func queryRegisterRows(t testing.TB, ctx context.Context, tc *testContext) []registerRow {
	t.Helper()
	rows, err := tc.Pool.Query(ctx, `
        SELECT id, event_type, payload, resource_kind, resource_id,
               sent_at::text, attempt_count, last_error
          FROM kacho_nlb.fga_register_outbox
         ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []registerRow
	for rows.Next() {
		var r registerRow
		require.NoError(t, rows.Scan(&r.id, &r.eventType, &r.payload, &r.resourceKind,
			&r.resourceID, &r.sentAt, &r.attemptCount, &r.lastError))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// TestFGARegisterOutbox_SECD01_CreateIntentInWriterTx — Сценарий SEC-D-01/05/06.
// Create-flow пишет fga.register-intent в той же writer-tx, что и Insert
// ресурса; payload содержит ожидаемый набор tuple; обе строки видны после
// одного Commit.
func TestFGARegisterOutbox_SECD01_CreateIntentInWriterTx(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	const projectID = "prj-aaaaaaaaaaaaaaaaa"
	const subject = "user:usr-xxxxxxxxxxxxxxxxx"
	lb := newLB(projectID, "lb-a")

	// project-hierarchy + creator tuple set (nlb writes project + admin@creator).
	intent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: string(lb.ID),
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, string(lb.ID), projectID),
			domain.FGACreatorTuple(subject, domain.FGAObjectTypeLoadBalancer, string(lb.ID)),
		},
	}

	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
		// domain-outbox CREATED row (same tx).
		require.NoError(t, w.Outbox().Emit(ctx,
			"nlb_load_balancer", string(lb.ID), projectID, "CREATED", map[string]any{"id": string(lb.ID)}))
		// SEC-D: FGA-register-intent in the SAME writer-tx.
		require.NoError(t, w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister, intent))
	})

	rows := queryRegisterRows(t, ctx, tc)
	require.Len(t, rows, 1, "exactly one fga.register row after Commit")
	r := rows[0]
	require.Equal(t, domain.FGAEventRegister, r.eventType)
	require.Equal(t, "NetworkLoadBalancer", r.resourceKind)
	require.Equal(t, string(lb.ID), r.resourceID)
	require.Nil(t, r.sentAt, "intent pending (drainer not running)")
	require.Equal(t, 0, r.attemptCount)

	// payload contains the expected project-hierarchy + creator tuple intents.
	decoded, err := decodeIntent(r.payload)
	require.NoError(t, err)
	require.Contains(t, decoded.Tuples, domain.FGATuple{
		SubjectID: "project:" + projectID,
		Relation:  domain.FGARelationProject,
		Object:    domain.FGAObjectTypeLoadBalancer + ":" + string(lb.ID),
	})
	require.Contains(t, decoded.Tuples, domain.FGATuple{
		SubjectID: subject,
		Relation:  domain.FGARelationAdmin,
		Object:    domain.FGAObjectTypeLoadBalancer + ":" + string(lb.ID),
	})
}

// TestFGARegisterOutbox_SECD02_AbortNoIntent — Сценарий SEC-D-02. Writer-tx
// абортится → ни Insert ресурса, ни register-intent НЕ остаются (атомарность).
func TestFGARegisterOutbox_SECD02_AbortNoIntent(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	const projectID = "prj-bbbbbbbbbbbbbbbbb"
	lb := newLB(projectID, "lb-abort")
	intent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: string(lb.ID),
		Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, string(lb.ID), projectID)},
	}

	w, err := tc.Repo.Writer(ctx)
	require.NoError(t, err)
	_, err = w.LoadBalancers().Insert(ctx, lb)
	require.NoError(t, err)
	require.NoError(t, w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister, intent))
	// Abort instead of Commit (e.g. inline step failed).
	w.Abort()

	rows := queryRegisterRows(t, ctx, tc)
	require.Empty(t, rows, "no register-intent after Abort")

	// no orphan resource row.
	var n int
	require.NoError(t, tc.Pool.QueryRow(ctx,
		`SELECT count(*) FROM kacho_nlb.load_balancers WHERE id = $1`, string(lb.ID)).Scan(&n))
	require.Equal(t, 0, n, "no orphan load_balancer row")
}

// TestFGARegisterOutbox_SECD03_UnregisterIntentOnDelete — Сценарий SEC-D-03.
// Delete-flow пишет fga.unregister-intent в той же writer-tx, что и Delete;
// строка ресурса удалена в той же tx.
func TestFGARegisterOutbox_SECD03_UnregisterIntentOnDelete(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	const projectID = "prj-ccccccccccccccccc"
	lb := newLB(projectID, "lb-del")
	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	unregIntent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: string(lb.ID),
		Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, string(lb.ID), projectID)},
	}
	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		require.NoError(t, w.LoadBalancers().Delete(ctx, string(lb.ID)))
		require.NoError(t, w.FGARegisterOutbox().Emit(ctx, domain.FGAEventUnregister, unregIntent))
	})

	rows := queryRegisterRows(t, ctx, tc)
	require.Len(t, rows, 1)
	require.Equal(t, domain.FGAEventUnregister, rows[0].eventType)
	require.Equal(t, string(lb.ID), rows[0].resourceID)

	var n int
	require.NoError(t, tc.Pool.QueryRow(ctx,
		`SELECT count(*) FROM kacho_nlb.load_balancers WHERE id = $1`, string(lb.ID)).Scan(&n))
	require.Equal(t, 0, n, "resource row deleted in the same tx")
}

// TestFGARegisterOutbox_SECD06_EmptyTupleSetNoRow — paritет с
// EmitCreator-skip-on-empty-subject: пустой набор tuple (system-initiated, нет
// ни одной валидной tuple) → строка не пишется.
func TestFGARegisterOutbox_SECD06_EmptyTupleSetNoRow(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		require.NoError(t, w.FGARegisterOutbox().Emit(ctx, domain.FGAEventRegister,
			domain.FGARegisterIntent{Kind: "X", ResourceID: "y"}))
	})
	require.Empty(t, queryRegisterRows(t, ctx, tc), "empty tuple set → no row")
}

// TestFGARegisterOutbox_EventTypeCheck — DB CHECK constraint на event_type
// (belt-and-suspenders против typo в caller'е). Raw INSERT с невалидным
// event_type → SQLSTATE 23514.
func TestFGARegisterOutbox_EventTypeCheck(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	_, err := tc.Pool.Exec(ctx, `
        INSERT INTO kacho_nlb.fga_register_outbox (event_type, payload)
        VALUES ('fga.bogus', '{"tuples":[]}'::jsonb)`)
	require.Error(t, err)
	// pgconn.PgError SQLSTATE 23514 expected → CHECK constraint violation.
	assert.Contains(t, err.Error(), "fga_register_outbox_event_type_check",
		"want CHECK constraint violation, got %v", err)
}

// decodeIntent — local helper (mirror of clients/iam.DecodeFGARegisterIntent
// without the drainer-error wrapping; the test only needs the shape).
func decodeIntent(payload []byte) (domain.FGARegisterIntent, error) {
	var i domain.FGARegisterIntent
	err := json.Unmarshal(payload, &i)
	return i, err
}
