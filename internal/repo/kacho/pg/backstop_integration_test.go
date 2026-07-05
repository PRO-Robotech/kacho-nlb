// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// backstop_integration_test.go — (kacho-nlb backstop):
// reconciler + metrics + fail-closed boot-gate over the existing
// register-outbox, WITHOUT changing co-commit atomicity (no migration).
//
//	1.4-30  reconciler re-drives a poisoned row back to claimable → delivered
//	1.4-31  fail-closed boot-gate: require-iam + no drainer → Create refused
//	1.4-32  long-outage no-poison: IAM down > MaxAttempts (transient) → not
//	        poisoned → delivered exactly once on recovery (corelib classify) +
//	        metrics surface backlog while pending (1.4-23)
//
// testcontainers Postgres 16; real corelib reconciler/drainer/metrics + the nlb
// applier over a fake RegisterResourceClient. Reuses the harness in
// fga_register_drainer_integration_test.go / setup_integration_test.go.
package pg_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"
	"github.com/PRO-Robotech/kacho-corelib/outbox/metrics"
	"github.com/PRO-Robotech/kacho-corelib/outbox/reconciler"
	iampb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/fgaboot"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

const nlbOutboxTbl = "kacho_nlb.fga_register_outbox"

// Test_1_4_30_ReconcilerRedrivesPoisoned — 1.4-30: a poisoned register-intent
// (attempt_count >= MaxAttempts, sent_at NULL) is re-driven to claimable by the
// reconciler → the drainer then delivers it with its ORIGINAL decoder-correct
// payload. Atomicity untouched.
func Test_1_4_30_ReconcilerRedrivesPoisoned(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	intent := domain.FGARegisterIntent{
		Kind: "NetworkLoadBalancer", ResourceID: "nlb-redrive",
		Tuples: []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, "nlb-redrive", "prj-x")},
	}
	insertIntent(t, ctx, tc, "fga.register", intent)
	_, err := tc.Pool.Exec(ctx,
		`UPDATE kacho_nlb.fga_register_outbox SET attempt_count = 10, last_error = 'was permanent'
		   WHERE resource_id = 'nlb-redrive'`)
	require.NoError(t, err)

	ad := kachopg.NewFGAReconcileAdapter(tc.Pool, nlbOutboxTbl)
	rc, err := reconciler.New(tc.Pool, reconciler.Config{
		Table:       nlbOutboxTbl,
		Channel:     "kacho_nlb_fga_register_outbox",
		MaxAttempts: 10,
	}, reconciler.Adapters{Enumerator: ad, Registry: ad}, nil)
	require.NoError(t, err)

	n, err := rc.RedrivePoisoned(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly one poisoned row re-driven")

	var attempt int
	var lastErr *string
	require.NoError(t, tc.Pool.QueryRow(ctx,
		`SELECT attempt_count, last_error FROM kacho_nlb.fga_register_outbox WHERE resource_id='nlb-redrive'`).
		Scan(&attempt, &lastErr))
	assert.Less(t, attempt, 10, "attempt_count reset below MaxAttempts (claimable)")
	assert.Nil(t, lastErr, "last_error cleared")

	// The drainer now delivers the re-driven intent (IAM healthy).
	fake := newFakeIAMRegister()
	stop := startDrainer(t, tc.Pool, fake)
	defer stop()
	require.Eventually(t, func() bool {
		return fake.appliedCount("project:prj-x", "project", "lb_network_load_balancer:nlb-redrive") == 1
	}, 5*time.Second, 50*time.Millisecond, "re-driven intent delivered exactly once")
}

// Test_1_4_31_FailClosedBootGate_RefusesCreate — 1.4-31: require-iam armed +
// register-drainer not connected → guardCreateUnary refuses a mutating Create
// (UNAVAILABLE); read RPCs pass; connect → Create allowed.
func Test_1_4_31_FailClosedBootGate_RefusesCreate(t *testing.T) {
	gate := bootgate.New(bootgate.Config{RequireIAM: true, Service: "kacho-nlb"})
	assert.False(t, gate.Ready(), "require-iam + not connected → NotReady")
	guard := fgaboot.GuardCreateUnary(gate)

	createInvoked := false
	createHandler := func(_ context.Context, _ any) (any, error) { createInvoked = true; return "ok", nil }
	_, err := guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create"}, createHandler)
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err), "Create refused fail-closed (UNAVAILABLE)")
	assert.False(t, createInvoked, "resource not created — handler never reached")

	getInvoked := false
	_, err = guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Get"},
		func(_ context.Context, _ any) (any, error) { getInvoked = true; return "lb", nil })
	require.NoError(t, err)
	assert.True(t, getInvoked, "read RPC works on a not-yet-ready instance")

	gate.SetConnected(true)
	assert.True(t, gate.Ready(), "connected → Ready")
	createInvoked = false
	_, err = guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create"}, createHandler)
	require.NoError(t, err)
	assert.True(t, createInvoked, "Create allowed once IAM-register path connected")
}

// Test_1_4_31_RequireIAMOff_NoOp — contrast: require-iam=false (dev) → no-op gate.
func Test_1_4_31_RequireIAMOff_NoOp(t *testing.T) {
	gate := bootgate.New(bootgate.Config{RequireIAM: false, Service: "kacho-nlb"})
	assert.True(t, gate.Ready(), "require-iam off → always Ready (dev)")
	guard := fgaboot.GuardCreateUnary(gate)
	invoked := false
	_, err := guard(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Create"},
		func(_ context.Context, _ any) (any, error) { invoked = true; return "ok", nil })
	require.NoError(t, err)
	assert.True(t, invoked, "Create allowed in dev back-compat mode")
}

// downIAM — a fake RegisterResourceClient whose outage is flipped by the test.
type downIAM struct {
	down     atomic.Bool
	attempts atomic.Int32
	applied  atomic.Int32
}

func (c *downIAM) RegisterResource(_ context.Context, _ *iampb.RegisterResourceRequest, _ ...grpc.CallOption) (*iampb.RegisterResourceResponse, error) {
	if c.down.Load() {
		c.attempts.Add(1)
		return nil, status.Error(codes.Unavailable, "iam down")
	}
	c.applied.Add(1)
	return &iampb.RegisterResourceResponse{}, nil
}

func (c *downIAM) UnregisterResource(_ context.Context, _ *iampb.UnregisterResourceRequest, _ ...grpc.CallOption) (*iampb.UnregisterResourceResponse, error) {
	return &iampb.UnregisterResourceResponse{}, nil
}

// Test_1_4_32_LongOutageNoPoison_ThenMetricsSurface — 1.4-32 + 1.4-23: IAM
// Unavailable for MORE than MaxAttempts consecutive transient attempts  → the
// intent is NOT poisoned (stays pending) → delivered exactly once on recovery; the
// metrics Collector surfaces backlog while pending, poisoned stays 0.
func Test_1_4_32_LongOutageNoPoison_ThenMetricsSurface(t *testing.T) {
	tc := newTestCtx(t)
	ctx := context.Background()

	const maxAttempts = 4 // drainerCfg uses MaxAttempts=4
	iam := &downIAM{}
	iam.down.Store(true)
	stop := startDrainer(t, tc.Pool, iam)
	defer stop()

	intent := domain.FGARegisterIntent{
		Kind: "NetworkLoadBalancer", ResourceID: "nlb-long",
		Tuples: []domain.FGATuple{domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, "nlb-long", "prj-x")},
	}
	insertIntent(t, ctx, tc, "fga.register", intent)

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(tc.Pool, rec, metrics.CollectorConfig{Table: nlbOutboxTbl, MaxAttempts: maxAttempts})

	// While IAM is down: > maxAttempts transient attempts yet the intent is NOT
	// poisoned (still pending) — and metrics surface backlog + oldest age.
	require.Eventually(t, func() bool {
		_ = col.Scan(ctx)
		return iam.attempts.Load() > maxAttempts &&
			rec.BacklogDepth(nlbOutboxTbl) >= 1 && rec.OldestPendingAgeSeconds(nlbOutboxTbl) > 0
	}, 10*time.Second, 100*time.Millisecond, "> maxAttempts transient attempts, still pending, backlog surfaced")

	var sentNull bool
	require.NoError(t, tc.Pool.QueryRow(ctx,
		`SELECT sent_at IS NULL FROM kacho_nlb.fga_register_outbox WHERE resource_id='nlb-long'`).Scan(&sentNull))
	assert.True(t, sentNull, "intent durable (pending) through a transient outage longer than MaxAttempts")

	// IAM recovers → delivered exactly once.
	iam.down.Store(false)
	require.Eventually(t, func() bool {
		return iam.applied.Load() == 1
	}, 10*time.Second, 100*time.Millisecond, "tuple delivered exactly once after long transient outage (no poison)")

	var sentNotNull bool
	require.NoError(t, tc.Pool.QueryRow(ctx,
		`SELECT sent_at IS NOT NULL FROM kacho_nlb.fga_register_outbox WHERE resource_id='nlb-long'`).Scan(&sentNotNull))
	assert.True(t, sentNotNull, "intent ultimately delivered (not lost)")

	require.NoError(t, col.Scan(ctx))
	assert.Equal(t, float64(0), rec.PoisonedCount(nlbOutboxTbl),
		"a transient (Unavailable) outage must NOT poison — outbox_poisoned stays 0")
}
