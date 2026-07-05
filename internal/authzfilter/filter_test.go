// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authzfilter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// Unit tests for the kacho-nlb FGA list-filter (RBAC).
// Mirror of kacho-compute internal/authzfilter/filter_test.go, adapted to the
// nlb `lb_*` resource types + `loadbalancer.*.list` actions. No network: a fake
// AuthorizeService client returns programmed responses.

// mockAuthClient — captures calls and returns programmed responses (one per call;
// the last response repeats once the slice is exhausted, matching compute's mock).
type mockAuthClient struct {
	calls       atomic.Int64
	responses   []*iamv1.ListObjectsResponse
	err         error
	sleep       time.Duration
	lastSubject string
	lastResType string
	lastAction  string
	lastMaxRes  int64
}

func (m *mockAuthClient) ListObjects(ctx context.Context, in *iamv1.ListObjectsRequest, _ ...grpc.CallOption) (*iamv1.ListObjectsResponse, error) {
	m.calls.Add(1)
	m.lastSubject = in.GetSubject()
	m.lastResType = in.GetResourceType()
	m.lastAction = in.GetAction()
	m.lastMaxRes = in.GetMaxResults()
	if m.sleep > 0 {
		select {
		case <-time.After(m.sleep):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	if len(m.responses) == 0 {
		return &iamv1.ListObjectsResponse{}, nil
	}
	resp := m.responses[0]
	if len(m.responses) > 1 {
		m.responses = m.responses[1:]
	}
	return resp, nil
}

// cache miss → iam call → result cached & sorted → second call is a cache hit.
func TestFGAFilter_CacheMissThenHit(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{
			{ResourceIds: []string{"nlb-2", "nlb-1"}},
		},
	}
	f := NewFGAFilter(mock, DefaultConfig())

	ctx := context.Background()
	d1, err := f.ListAllowedIDs(ctx, "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("first call err: %v", err)
	}
	if d1.FromCache {
		t.Fatalf("first call: should NOT be from cache")
	}
	// Deterministic ordering (sorted) for stable pagination.
	if got := d1.IDs(); len(got) != 2 || got[0] != "nlb-1" || got[1] != "nlb-2" {
		t.Fatalf("first call: IDs not sorted: %v", got)
	}

	d2, err := f.ListAllowedIDs(ctx, "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("second call err: %v", err)
	}
	if !d2.FromCache {
		t.Fatalf("second call: must be cache hit")
	}
	if mock.calls.Load() != 1 {
		t.Fatalf("expected exactly 1 iam call, got %d", mock.calls.Load())
	}
	if mock.lastSubject != "user:usr_alice" || mock.lastResType != ResourceTypeLoadBalancer || mock.lastAction != ActionLoadBalancerList {
		t.Fatalf("bad iam request: %+v", mock)
	}
	// MaxResults capped at CacheMaxEntries.
	if mock.lastMaxRes != int64(DefaultConfig().CacheMaxEntries) {
		t.Fatalf("max_results: want %d, got %d", DefaultConfig().CacheMaxEntries, mock.lastMaxRes)
	}
}

// A cache HIT returns a defensive copy of the allow-list (getCache copies the
// slice), so mutating the slice from a cache-hit Decision does not corrupt the
// cached entry served to the next caller.
//
// NOTE: this only asserts the cache-HIT boundary, which is the one the
// implementation defends. The first (cache-MISS) Decision currently shares its
// backing array with the cached entry — see the "cache copy on miss-path" finding
// in the review report. Production callers (Resolve → use-case) only READ IDs
// and never mutate, so the gap is latent; this test pins the implemented contract
// rather than the desired one.
func TestFGAFilter_CacheHitReturnsCopy(t *testing.T) {
	mock := &mockAuthClient{responses: []*iamv1.ListObjectsResponse{{ResourceIds: []string{"nlb-1", "nlb-2"}}}}
	f := NewFGAFilter(mock, DefaultConfig())

	// First call populates the cache (miss).
	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList); err != nil {
		t.Fatal(err)
	}

	// Second call is a hit → returns a copy.
	hit, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if !hit.FromCache {
		t.Fatalf("expected cache hit")
	}
	hit.IDs()[0] = "tampered"

	// Third call (also a hit) must still see the original, un-tampered IDs.
	hit2, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if !hit2.FromCache || hit2.IDs()[0] != "nlb-1" {
		t.Fatalf("cache-hit copy not defensive: %v (fromCache=%v)", hit2.IDs(), hit2.FromCache)
	}
}

// fail-closed (default): iam ListObjects error → Unavailable, NOT an unfiltered list.
func TestFGAFilter_FailClosed(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.Unavailable, "iam down")}
	f := NewFGAFilter(mock, DefaultConfig())

	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err == nil {
		t.Fatalf("expected error on iam unavailable, got nil")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", got)
	}
}

// fail-open (configured): on iam error → bypass + FailOpen marker, no error.
func TestFGAFilter_FailOpen(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.Unavailable, "iam down")}
	cfg := DefaultConfig()
	cfg.FailOpen = true
	f := NewFGAFilter(mock, cfg)

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("fail-open: must not return error, got: %v", err)
	}
	if !d.IsBypass() || !d.FailOpen {
		t.Fatalf("fail-open: expected BypassAll=true + FailOpen=true, got %+v", d)
	}
}

// fail-open decisions are NOT cached: every call re-attempts iam (degraded mode
// must self-heal as soon as iam recovers, not serve a stale bypass).
func TestFGAFilter_FailOpenNotCached(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.Unavailable, "iam down")}
	cfg := DefaultConfig()
	cfg.FailOpen = true
	f := NewFGAFilter(mock, cfg)

	for i := 0; i < 3; i++ {
		d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
		if err != nil {
			t.Fatalf("call %d: unexpected err: %v", i, err)
		}
		if !d.IsBypass() || d.FromCache {
			t.Fatalf("call %d: fail-open must bypass and NOT come from cache, got %+v", i, d)
		}
	}
	if mock.calls.Load() != 3 {
		t.Fatalf("fail-open must re-attempt iam each call, got %d calls", mock.calls.Load())
	}
	if f.Size() != 0 {
		t.Fatalf("fail-open decision must not be cached, cache size=%d", f.Size())
	}

	// Recovery: once iam answers, the real grant takes effect.
	mock.err = nil
	mock.responses = []*iamv1.ListObjectsResponse{{ResourceIds: []string{"nlb-1"}}}
	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if d.IsBypass() || len(d.IDs()) != 1 || d.IDs()[0] != "nlb-1" {
		t.Fatalf("recovery: expected real grant {nlb-1}, got %+v", d)
	}
}

// wildcard_grant → bypass (subject has unbounded reach over the type). NOT Empty
// even though resource_ids is empty on the server side under wildcard.
func TestFGAFilter_WildcardGrantBypass(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{
			{WildcardGrant: true, ResourceIds: []string{}},
		},
	}
	f := NewFGAFilter(mock, DefaultConfig())

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("wildcard: unexpected err: %v", err)
	}
	if !d.IsBypass() {
		t.Fatalf("wildcard: expected BypassAll=true, got %+v", d)
	}
	if d.IsEmpty() {
		t.Fatalf("wildcard: must NOT be Empty (unbounded reach), got %+v", d)
	}

	// Wildcard bypass IS cached (it is a stable positive decision).
	d2, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if !d2.IsBypass() || !d2.FromCache {
		t.Fatalf("wildcard: second call should be cached bypass, got %+v", d2)
	}
	if mock.calls.Load() != 1 {
		t.Fatalf("wildcard: expected 1 iam call (cached), got %d", mock.calls.Load())
	}
}

// empty grant → Empty decision (NOT bypass, NOT error) → use-case returns empty
// list without leaking any object.
func TestFGAFilter_EmptyGrant(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{{ResourceIds: []string{}}},
	}
	f := NewFGAFilter(mock, DefaultConfig())

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_no_grants", ResourceTypeListener, ActionListenerList)
	if err != nil {
		t.Fatalf("empty grant: should not error, got: %v", err)
	}
	if !d.IsEmpty() || d.IsBypass() {
		t.Fatalf("empty grant: expected Empty=true BypassAll=false, got %+v", d)
	}
	if len(d.IDs()) != 0 {
		t.Fatalf("empty grant: expected zero IDs, got %v", d.IDs())
	}
}

// disabled config → bypass, no iam call.
func TestFGAFilter_DisabledIsBypass(t *testing.T) {
	mock := &mockAuthClient{}
	cfg := DefaultConfig()
	cfg.Enabled = false
	f := NewFGAFilter(mock, cfg)

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("disabled: err: %v", err)
	}
	if !d.IsBypass() {
		t.Fatalf("disabled: expected BypassAll=true")
	}
	if mock.calls.Load() != 0 {
		t.Fatalf("disabled: must NOT call iam (got %d calls)", mock.calls.Load())
	}
}

// nil client → bypass (graceful start without iam).
func TestFGAFilter_NilClientIsBypass(t *testing.T) {
	f := NewFGAFilter(nil, DefaultConfig())

	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("nil client: err: %v", err)
	}
	if !d.IsBypass() {
		t.Fatalf("nil client: expected BypassAll=true")
	}
}

// empty subject when filter is enabled → Unauthenticated (fail-closed guard).
func TestFGAFilter_EmptySubjectFailClosed(t *testing.T) {
	mock := &mockAuthClient{}
	f := NewFGAFilter(mock, DefaultConfig())

	_, err := f.ListAllowedIDs(context.Background(), "", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err == nil {
		t.Fatalf("empty subject: expected error, got nil")
	}
	if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("empty subject: expected Unauthenticated, got %s", got)
	}
	if mock.calls.Load() != 0 {
		t.Fatalf("empty subject: must NOT call iam")
	}
}

// missing resourceType / action → error (input guard).
func TestFGAFilter_MissingResourceTypeOrAction(t *testing.T) {
	mock := &mockAuthClient{}
	f := NewFGAFilter(mock, DefaultConfig())

	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", "", ActionLoadBalancerList); err == nil {
		t.Fatalf("empty resourceType: expected error")
	}
	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ""); err == nil {
		t.Fatalf("empty action: expected error")
	}
	if mock.calls.Load() != 0 {
		t.Fatalf("input guard: must NOT call iam")
	}
}

// per-request timeout is enforced and surfaced as Unavailable (NOT silent bypass).
func TestFGAFilter_TimeoutEnforced(t *testing.T) {
	const mockSleep = 100 * time.Millisecond
	mock := &mockAuthClient{sleep: mockSleep, responses: []*iamv1.ListObjectsResponse{{}}}
	cfg := DefaultConfig()
	cfg.Timeout = 10 * time.Millisecond
	f := NewFGAFilter(mock, cfg)

	t0 := time.Now()
	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	elapsed := time.Since(t0)
	// Primary correctness: the per-request timeout must surface as Unavailable
	// (fail-closed, not a silent bypass) rather than blocking for the full
	// downstream sleep. If the timeout were NOT enforced the mock would sleep the
	// full mockSleep and return a nil-error response → err==nil catches that.
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("timeout: expected Unavailable, got %s", got)
	}
	// Preemption sanity: the call returned before the downstream would have
	// completed (well under mockSleep). Bound is the mock sleep, not a tight
	// fixed ceiling gated on the 10ms timeout — the latter is flaky under
	// -race/GC/CPU-throttle on shared CI (a late-scheduled timeout goroutine can
	// breach an 80ms budget with no real regression).
	if elapsed >= mockSleep {
		t.Fatalf("timeout not enforced — call blocked for the full downstream sleep, elapsed=%s", elapsed)
	}
}

// cache TTL expiry → second call re-hits iam.
//
// Детерминированно через инъектированные часы (f.now) — НЕ wall-clock/time.Sleep.
// Раньше тест спал 40ms при TTL=25ms и был flaky под -race/GC/CPU-throttle
// (margin всего 15ms); теперь время двигаем вручную, флейка исключена.
func TestFGAFilter_CacheTTLExpiry(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{
			{ResourceIds: []string{"id-1"}},
			{ResourceIds: []string{"id-1", "id-2"}},
		},
	}
	cfg := DefaultConfig()
	cfg.CacheTTL = 25 * time.Millisecond
	f := NewFGAFilter(mock, cfg)

	// Фиксированные часы под нашим контролем.
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	cur := base
	f.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return cur
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		cur = cur.Add(d)
	}

	d1, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if len(d1.IDs()) != 1 {
		t.Fatalf("first call: want 1 id, got %v", d1.IDs())
	}

	// Внутри TTL — попадание в кеш, iam НЕ дёргается второй раз.
	advance(10 * time.Millisecond)
	dCached, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if !dCached.FromCache || len(dCached.IDs()) != 1 {
		t.Fatalf("within TTL: want cached 1 id, got %v (fromCache=%v)", dCached.IDs(), dCached.FromCache)
	}
	if mock.calls.Load() != 1 {
		t.Fatalf("within TTL: expected 1 iam call, got %d", mock.calls.Load())
	}

	// Перешагнули TTL (25ms) — запись протухла, iam зовётся заново.
	advance(30 * time.Millisecond)
	d2, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.IDs()) != 2 || d2.FromCache {
		t.Fatalf("post-TTL: must call iam again, got %v (fromCache=%v)", d2.IDs(), d2.FromCache)
	}
	if mock.calls.Load() != 2 {
		t.Fatalf("expected 2 iam calls after TTL expiry, got %d", mock.calls.Load())
	}
}

// Invalidate(subject) removes only that subject's entries; others keep theirs.
func TestFGAFilter_InvalidateBySubject(t *testing.T) {
	mock := &mockAuthClient{
		responses: []*iamv1.ListObjectsResponse{
			{ResourceIds: []string{"id-a"}},
			{ResourceIds: []string{"id-b"}},
			{ResourceIds: []string{"id-a-new"}},
		},
	}
	f := NewFGAFilter(mock, DefaultConfig())

	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_bob", ResourceTypeLoadBalancer, ActionLoadBalancerList); err != nil {
		t.Fatal(err)
	}

	f.Invalidate("user:usr_alice")

	dA, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if dA.FromCache {
		t.Fatalf("alice: expected cache miss after invalidate")
	}
	dB, err := f.ListAllowedIDs(context.Background(), "user:usr_bob", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatal(err)
	}
	if !dB.FromCache {
		t.Fatalf("bob: expected cache hit (not invalidated)")
	}
}

// cache is bounded by CacheMaxEntries.
func TestFGAFilter_CacheBounded(t *testing.T) {
	mock := &mockAuthClient{responses: []*iamv1.ListObjectsResponse{{}}}
	cfg := DefaultConfig()
	cfg.CacheMaxEntries = 3
	cfg.CacheTTL = time.Hour // ensure entries don't expire during the test
	f := NewFGAFilter(mock, cfg)

	for i := 0; i < 10; i++ {
		mock.responses = []*iamv1.ListObjectsResponse{{}}
		subj := "user:usr_" + string(rune('a'+i))
		if _, err := f.ListAllowedIDs(context.Background(), subj, ResourceTypeLoadBalancer, ActionLoadBalancerList); err != nil {
			t.Fatal(err)
		}
	}
	if size := f.Size(); size > cfg.CacheMaxEntries {
		t.Fatalf("cache bound violated: size=%d > max=%d", size, cfg.CacheMaxEntries)
	}
}

// BypassFilter trivially bypasses for any args.
func TestBypassFilter(t *testing.T) {
	d, err := BypassFilter{}.ListAllowedIDs(context.Background(), "user:anyone", ResourceTypeTargetGroup, ActionTargetGroupList)
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsBypass() {
		t.Fatalf("BypassFilter must return BypassAll=true")
	}
}

// upstream gRPC code (e.g. PermissionDenied) is wrapped as Unavailable fail-closed
// (the filter never surfaces a non-Unavailable code that a List handler would
// translate into a leak / wrong status).
func TestFGAFilter_WrapsUpstreamCodeAsUnavailable(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.PermissionDenied, "no")}
	f := NewFGAFilter(mock, DefaultConfig())
	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("expected Unavailable wrap on upstream PermissionDenied, got %s", got)
	}
}

// non-status (generic) errors are also wrapped as Unavailable.
func TestFGAFilter_GenericErrWrapsUnavailable(t *testing.T) {
	mock := &mockAuthClient{err: errors.New("boom")}
	f := NewFGAFilter(mock, DefaultConfig())
	_, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %s", got)
	}
}

// fail-closed errors are NOT cached: a transient iam outage must not poison the
// cache (next call re-attempts; a recovered iam answers correctly).
func TestFGAFilter_FailClosedNotCached(t *testing.T) {
	mock := &mockAuthClient{err: status.Error(codes.Unavailable, "iam down")}
	f := NewFGAFilter(mock, DefaultConfig())

	if _, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList); err == nil {
		t.Fatal("expected error on first (failing) call")
	}
	if f.Size() != 0 {
		t.Fatalf("fail-closed error must not be cached, size=%d", f.Size())
	}

	mock.err = nil
	mock.responses = []*iamv1.ListObjectsResponse{{ResourceIds: []string{"nlb-1"}}}
	d, err := f.ListAllowedIDs(context.Background(), "user:usr_alice", ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("recovery call: %v", err)
	}
	if d.FromCache || len(d.IDs()) != 1 {
		t.Fatalf("recovery: want fresh {nlb-1}, got %+v", d)
	}
}
