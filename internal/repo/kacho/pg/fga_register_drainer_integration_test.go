// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"
	iampb "github.com/PRO-Robotech/kacho-iam/proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fga_register_drainer_integration_test.go — register-drainer applies
// FGA-register/unregister intents through kacho-iam InternalIAMService.Register-
// Resource / UnregisterResource by mTLS. Scenarios.
//
// The drainer mechanics themselves (claim / mark / poison / NOTIFY) are covered
// by corelib; here we test the nlb consumer-applier (iam.NewRegisterApplier
// + iam.DecodeFGARegisterIntent) end-to-end against testcontainers Postgres with
// the migration-0002 fga_register_outbox table, driven by a scripted fake
// RegisterResourceClient (no real OpenFGA — record-recorder).

// ---- fake RegisterResourceClient (records + scripts replies) ----------------

type registerCall struct {
	eventType string // "register" | "unregister"
	subjectID string
	relation  string
	object    string
}

// fakeIAMRegister is a fake iam.RegisterResourceClient. It records every call
// and applies a scriptable reply policy: the first failN calls return failCode,
// then OK. With failN==0 every call returns OK (happy). A per-tuple applied set
// models IAM idempotency (repeat owner-tuple → OK, NOT AlreadyExists) so the
// recorder can assert "tuple present exactly once".
type fakeIAMRegister struct {
	mu       sync.Mutex
	calls    []registerCall
	applied  map[string]int // object-tuple key → count of successful applies
	failN    int64          // first N calls fail (atomic countdown)
	failCode codes.Code     // code returned while failing
}

func newFakeIAMRegister() *fakeIAMRegister {
	return &fakeIAMRegister{applied: map[string]int{}}
}

func tupleKey(sub, rel, obj string) string { return sub + "#" + rel + "@" + obj }

func (f *fakeIAMRegister) maybeFail() error {
	if atomic.LoadInt64(&f.failN) <= 0 {
		return nil
	}
	if atomic.AddInt64(&f.failN, -1) >= 0 {
		return status.Errorf(f.failCode, "fake-iam scripted failure (%s)", f.failCode)
	}
	return nil
}

func (f *fakeIAMRegister) RegisterResource(
	ctx context.Context, in *iampb.RegisterResourceRequest, _ ...grpc.CallOption,
) (*iampb.RegisterResourceResponse, error) {
	if err := f.maybeFail(); err != nil {
		f.record(registerCall{"register", in.GetSubjectId(), in.GetRelation(), in.GetObject()})
		return nil, err
	}
	// Idempotent set-presence (models: repeat owner-tuple → OK, tuple
	// stored exactly once). A replay keeps presence at 1, never duplicates.
	f.mu.Lock()
	f.applied[tupleKey(in.GetSubjectId(), in.GetRelation(), in.GetObject())] = 1
	f.mu.Unlock()
	f.record(registerCall{"register", in.GetSubjectId(), in.GetRelation(), in.GetObject()})
	return &iampb.RegisterResourceResponse{}, nil
}

func (f *fakeIAMRegister) UnregisterResource(
	ctx context.Context, in *iampb.UnregisterResourceRequest, _ ...grpc.CallOption,
) (*iampb.UnregisterResourceResponse, error) {
	if err := f.maybeFail(); err != nil {
		f.record(registerCall{"unregister", in.GetSubjectId(), in.GetRelation(), in.GetObject()})
		return nil, err
	}
	f.mu.Lock()
	delete(f.applied, tupleKey(in.GetSubjectId(), in.GetRelation(), in.GetObject()))
	f.mu.Unlock()
	f.record(registerCall{"unregister", in.GetSubjectId(), in.GetRelation(), in.GetObject()})
	return &iampb.UnregisterResourceResponse{}, nil
}

func (f *fakeIAMRegister) record(c registerCall) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

func (f *fakeIAMRegister) callsFor(eventType, object string) []registerCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []registerCall
	for _, c := range f.calls {
		if c.eventType == eventType && c.object == object {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeIAMRegister) appliedCount(sub, rel, obj string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied[tupleKey(sub, rel, obj)]
}

var _ iam.RegisterResourceClient = (*fakeIAMRegister)(nil)

// ---- drainer test harness ---------------------------------------------------

func drainerLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func drainerCfg() drainer.Config {
	return drainer.Config{
		Table:        "kacho_nlb.fga_register_outbox",
		Channel:      "kacho_nlb_fga_register_outbox",
		BatchSize:    32,
		PollFallback: 500 * time.Millisecond,
		MaxAttempts:  4,
		BackoffMin:   100 * time.Millisecond,
		BackoffMax:   500 * time.Millisecond,
	}
}

// startDrainer spins a register-drainer in a goroutine; cancel via returned fn.
func startDrainer(t *testing.T, pool *pgxpool.Pool, cli iam.RegisterResourceClient) func() {
	t.Helper()
	d, err := drainer.New[domain.FGARegisterIntent](
		pool, drainerCfg(),
		iam.DecodeFGARegisterIntent,
		iam.NewRegisterApplier(cli),
		drainerLogger(),
	)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

// insertIntent writes one fga_register_outbox row (raw, mimics writer-tx emit).
func insertIntent(t testing.TB, ctx context.Context, tc *testContext, eventType string, intent domain.FGARegisterIntent) {
	t.Helper()
	payload, err := intent.Marshal()
	require.NoError(t, err)
	_, err = tc.Pool.Exec(ctx,
		`INSERT INTO kacho_nlb.fga_register_outbox (event_type, payload, resource_kind, resource_id)
		 VALUES ($1, $2::jsonb, $3, $4)`,
		eventType, payload, intent.Kind, intent.ResourceID)
	require.NoError(t, err)
}

// waitRow polls until predicate over the single row holds or deadline elapses.
func waitRow(t testing.TB, ctx context.Context, tc *testContext, deadline time.Duration, pred func(registerRow) bool) registerRow {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		rows := queryRegisterRows(t, ctx, tc)
		if len(rows) == 1 && pred(rows[0]) {
			return rows[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	rows := queryRegisterRows(t, ctx, tc)
	require.Len(t, rows, 1, "expected exactly one row")
	require.Failf(t, "predicate not met in time", "row=%+v", rows[0])
	return rows[0]
}

// ---- happy register apply ----------------------------------------

func TestFGARegisterDrainer_SECD09_HappyApply(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()
	fake := newFakeIAMRegister()

	const projectID = "prj-aaaaaaaaaaaaaaaaa"
	lbID := "nlb-aaaaaaaaaaaaaaaaa"
	intent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: lbID,
		Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, lbID, projectID)},
	}
	insertIntent(t, ctx, tc, domain.FGAEventRegister, intent)

	stop := startDrainer(t, tc.Pool, fake)
	defer stop()

	obj := domain.FGAObjectTypeLoadBalancer + ":" + lbID
	row := waitRow(t, ctx, tc, 3*time.Second, func(r registerRow) bool { return r.sentAt != nil })
	assert.Nil(t, row.lastError)
	calls := fake.callsFor("register", obj)
	require.Len(t, calls, 1, "exactly one RegisterResource call")
	assert.Equal(t, "project:"+projectID, calls[0].subjectID)
}

// ---- happy unregister apply --------------------------------------

func TestFGARegisterDrainer_SECD10_UnregisterApply(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()
	fake := newFakeIAMRegister()

	const projectID = "prj-bbbbbbbbbbbbbbbbb"
	lbID := "nlb-bbbbbbbbbbbbbbbbb"
	intent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: lbID,
		Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, lbID, projectID)},
	}
	insertIntent(t, ctx, tc, domain.FGAEventUnregister, intent)

	stop := startDrainer(t, tc.Pool, fake)
	defer stop()

	obj := domain.FGAObjectTypeLoadBalancer + ":" + lbID
	waitRow(t, ctx, tc, 3*time.Second, func(r registerRow) bool { return r.sentAt != nil })
	require.Len(t, fake.callsFor("unregister", obj), 1, "exactly one UnregisterResource call")
}

// ---- IAM Unavailable → intent durable → recover -------

func TestFGARegisterDrainer_SECD11_IAMDownThenRecover(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()
	fake := newFakeIAMRegister()
	// First 2 calls return Unavailable, then OK (models IAM down → recovery).
	atomic.StoreInt64(&fake.failN, 2)
	fake.failCode = codes.Unavailable

	const projectID = "prj-ccccccccccccccccc"
	lbID := "nlb-ccccccccccccccccc"
	intent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: lbID,
		Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, lbID, projectID)},
	}
	insertIntent(t, ctx, tc, domain.FGAEventRegister, intent)

	stop := startDrainer(t, tc.Pool, fake)
	defer stop()

	// During IAM-down: intent stays durable (sent_at NULL, last_error set, attempts grow).
	mid := waitRow(t, ctx, tc, 2*time.Second, func(r registerRow) bool {
		return r.sentAt == nil && r.lastError != nil && r.attemptCount >= 1
	})
	require.Contains(t, *mid.lastError, "Unavailable", "intent durable with Unavailable last_error")

	// After recovery: drainer drives it home.
	row := waitRow(t, ctx, tc, 5*time.Second, func(r registerRow) bool { return r.sentAt != nil })
	assert.Nil(t, row.lastError)
	obj := domain.FGAObjectTypeLoadBalancer + ":" + lbID
	require.GreaterOrEqual(t, len(fake.callsFor("register", obj)), 3, "retried until OK")
	require.Equal(t, 1, fake.appliedCount("project:"+projectID, domain.FGARelationProject, obj),
		"tuple applied exactly once after recovery")
}

// ---- idempotent re-apply (crash between apply and mark) -----------

func TestFGARegisterDrainer_SECD12_IdempotentReapply(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()
	fake := newFakeIAMRegister()

	const projectID = "prj-ddddddddddddddddd"
	lbID := "nlb-ddddddddddddddddd"
	obj := domain.FGAObjectTypeLoadBalancer + ":" + lbID
	intent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: lbID,
		Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, lbID, projectID)},
	}
	insertIntent(t, ctx, tc, domain.FGAEventRegister, intent)

	// Run 1: apply succeeds; we "crash" by stopping the drainer and forcibly
	// resetting sent_at to NULL (models crash between RPC-OK and the sent_at UPDATE).
	stop1 := startDrainer(t, tc.Pool, fake)
	waitRow(t, ctx, tc, 3*time.Second, func(r registerRow) bool { return r.sentAt != nil })
	stop1()
	_, err := tc.Pool.Exec(ctx, `UPDATE kacho_nlb.fga_register_outbox SET sent_at = NULL, attempt_count = 0`)
	require.NoError(t, err)

	// Run 2: drainer re-claims the row; IAM idempotency → OK on replay.
	stop2 := startDrainer(t, tc.Pool, fake)
	defer stop2()
	row := waitRow(t, ctx, tc, 3*time.Second, func(r registerRow) bool { return r.sentAt != nil })
	assert.Nil(t, row.lastError)
	require.GreaterOrEqual(t, len(fake.callsFor("register", obj)), 2, "called again on replay")
	require.Equal(t, 1, fake.appliedCount("project:"+projectID, domain.FGARelationProject, obj),
		"tuple present exactly once — replay did not duplicate")
}

// ---- concurrent two replicas → exactly-once ----------------------

func TestFGARegisterDrainer_SECD13_ConcurrentTwoReplicas(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()
	fake := newFakeIAMRegister()

	const projectID = "prj-eeeeeeeeeeeeeeeee"
	const n = 20
	for i := 0; i < n; i++ {
		lbID := fmt.Sprintf("nlb-e%016d", i)
		intent := domain.FGARegisterIntent{
			Kind:       "NetworkLoadBalancer",
			ResourceID: lbID,
			Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, lbID, projectID)},
		}
		insertIntent(t, ctx, tc, domain.FGAEventRegister, intent)
	}

	// Two replicas on the same DB (distinct pools — models 2 pods). The test
	// pool's ConnString already carries search_path=kacho_nlb,public.
	pool2, err := coredb.NewPool(ctx, tc.Pool.Config().ConnString())
	require.NoError(t, err)
	defer pool2.Close()

	stop1 := startDrainer(t, tc.Pool, fake)
	defer stop1()
	stop2 := startDrainer(t, pool2, fake)
	defer stop2()

	// All 20 rows sent within deadline.
	end := time.Now().Add(6 * time.Second)
	var sent int
	for time.Now().Before(end) {
		require.NoError(t, tc.Pool.QueryRow(ctx,
			`SELECT count(*) FROM kacho_nlb.fga_register_outbox WHERE sent_at IS NOT NULL`).Scan(&sent))
		if sent == n {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, n, sent, "all intents applied")

	// Exactly n successful RegisterResource calls — no double-apply across replicas.
	fake.mu.Lock()
	total := 0
	for _, c := range fake.calls {
		if c.eventType == "register" {
			total++
		}
	}
	applied := len(fake.applied)
	fake.mu.Unlock()
	require.Equal(t, n, total, "exactly n RegisterResource calls (no double-apply, no miss)")
	require.Equal(t, n, applied, "exactly n distinct tuples applied")
}

// ---- permanent poison (InvalidArgument) --------------------------

func TestFGARegisterDrainer_SECD14_PermanentPoison(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()
	fake := newFakeIAMRegister()
	// Always InvalidArgument (models poison-classification — malformed tuple).
	atomic.StoreInt64(&fake.failN, 1<<30)
	fake.failCode = codes.InvalidArgument

	const projectID = "prj-fffffffffffffffff"
	poisonID := "nlb-fffffffffffffffff"
	poison := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: poisonID,
		Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, poisonID, projectID)},
	}
	insertIntent(t, ctx, tc, domain.FGAEventRegister, poison)

	stop := startDrainer(t, tc.Pool, fake)
	defer stop()

	// Poisoned: attempt_count reaches MaxAttempts, sent_at stays NULL, last_error set.
	end := time.Now().Add(4 * time.Second)
	var got registerRow
	for time.Now().Before(end) {
		rows := queryRegisterRows(t, ctx, tc)
		require.Len(t, rows, 1)
		got = rows[0]
		if got.attemptCount >= drainerCfg().MaxAttempts {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.GreaterOrEqual(t, got.attemptCount, drainerCfg().MaxAttempts, "poisoned (no infinite retry)")
	require.Nil(t, got.sentAt, "poison row not marked sent")
	require.NotNil(t, got.lastError)
	assert.Contains(t, *got.lastError, "InvalidArgument")

	// A subsequent normal intent is still processed (drainer not stuck on poison).
	okID := "nlb-fffffffffffffff01"
	atomic.StoreInt64(&fake.failN, 0) // recover for the normal row
	ok := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: okID,
		Tuples:     []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, okID, projectID)},
	}
	insertIntent(t, ctx, tc, domain.FGAEventRegister, ok)
	end = time.Now().Add(4 * time.Second)
	for time.Now().Before(end) {
		var n int
		require.NoError(t, tc.Pool.QueryRow(ctx,
			`SELECT count(*) FROM kacho_nlb.fga_register_outbox WHERE resource_id=$1 AND sent_at IS NOT NULL`,
			okID).Scan(&n))
		if n == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("normal intent not processed after poison row — drainer stuck")
}
