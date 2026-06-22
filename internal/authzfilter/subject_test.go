package authzfilter

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// Subject-resolution + Resolve normalisation tests. kacho-nlb derives the FGA
// subject from the operations.Principal in ctx (set by grpcsrv.UnaryPrincipalExtract),
// NOT from raw gRPC metadata (the compute convention) — so these tests drive
// operations.WithPrincipal / SystemPrincipal through domain.FGASubjectFromPrincipal.

func ctxWithPrincipal(typ, id string) context.Context {
	return operations.WithPrincipal(context.Background(), operations.Principal{Type: typ, ID: id})
}

// user principal → "user:<id>".
func TestSubjectFromCtx_User(t *testing.T) {
	got := SubjectFromCtx(ctxWithPrincipal("user", "usr_alice"))
	if got != "user:usr_alice" {
		t.Fatalf("user: want user:usr_alice, got %q", got)
	}
}

// service account principal → "service_account:<id>".
func TestSubjectFromCtx_ServiceAccount(t *testing.T) {
	got := SubjectFromCtx(ctxWithPrincipal("service_account", "sa_xyz"))
	if got != "service_account:sa_xyz" {
		t.Fatalf("sa: want service_account:sa_xyz, got %q", got)
	}
}

// system principal (background / dev) → "" (use-case treats as bypass).
func TestSubjectFromCtx_SystemIsEmpty(t *testing.T) {
	// Bare context resolves to SystemPrincipal() → "".
	if got := SubjectFromCtx(context.Background()); got != "" {
		t.Fatalf("system (bare ctx): want empty, got %q", got)
	}
	// Explicit system principal → "".
	if got := SubjectFromCtx(ctxWithPrincipal("system", "bootstrap")); got != "" {
		t.Fatalf("system (explicit): want empty, got %q", got)
	}
}

// fakeResolveFilter — minimal Filter for Resolve() normalisation tests.
type fakeResolveFilter struct {
	dec     Decision
	err     error
	gotSubj string
	gotType string
	gotAct  string
	calls   int
}

func (f *fakeResolveFilter) ListAllowedIDs(_ context.Context, subject, resourceType, action string) (Decision, error) {
	f.calls++
	f.gotSubj, f.gotType, f.gotAct = subject, resourceType, action
	return f.dec, f.err
}

// nil filter → bypass without resolving a subject (list-filter disabled / dev).
func TestResolve_NilFilterBypass(t *testing.T) {
	dec, err := Resolve(ctxWithPrincipal("user", "usr_alice"), nil,
		ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("nil filter: %v", err)
	}
	if !dec.IsBypass() {
		t.Fatalf("nil filter: expected bypass, got %+v", dec)
	}
}

// system subject → bypass without calling the filter (no identity to scope by).
func TestResolve_SystemSubjectBypass(t *testing.T) {
	flt := &fakeResolveFilter{dec: Decision{Empty: true}}
	dec, err := Resolve(context.Background(), flt,
		ResourceTypeLoadBalancer, ActionLoadBalancerList)
	if err != nil {
		t.Fatalf("system subject: %v", err)
	}
	if !dec.IsBypass() {
		t.Fatalf("system subject: expected bypass, got %+v", dec)
	}
	if flt.calls != 0 {
		t.Fatalf("system subject: filter must NOT be called, calls=%d", flt.calls)
	}
}

// user subject → filter called with the resolved subject + given type/action.
func TestResolve_UserSubjectCallsFilter(t *testing.T) {
	flt := &fakeResolveFilter{dec: Decision{AllowedIDs: []string{"nlb-1"}}}
	dec, err := Resolve(ctxWithPrincipal("user", "usr_alice"), flt,
		ResourceTypeListener, ActionListenerList)
	if err != nil {
		t.Fatalf("user subject: %v", err)
	}
	if flt.gotSubj != "user:usr_alice" || flt.gotType != ResourceTypeListener || flt.gotAct != ActionListenerList {
		t.Fatalf("filter not called with resolved args: %+v", flt)
	}
	if dec.IsBypass() || len(dec.IDs()) != 1 || dec.IDs()[0] != "nlb-1" {
		t.Fatalf("decision passthrough wrong: %+v", dec)
	}
}

// filter status error is passed through verbatim (fail-closed).
func TestResolve_FilterStatusErrPassthrough(t *testing.T) {
	flt := &fakeResolveFilter{err: status.Error(codes.Unavailable, "iam down")}
	_, err := Resolve(ctxWithPrincipal("user", "usr_alice"), flt,
		ResourceTypeTargetGroup, ActionTargetGroupList)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("expected Unavailable passthrough, got %s", got)
	}
}

// non-status filter error is coerced to Unavailable (defensive fail-closed guard:
// a raw error would otherwise leak as codes.Unknown).
func TestResolve_NonStatusErrCoercedUnavailable(t *testing.T) {
	flt := &fakeResolveFilter{err: errors.New("boom")}
	_, err := Resolve(ctxWithPrincipal("user", "usr_alice"), flt,
		ResourceTypeTargetGroup, ActionTargetGroupList)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("expected Unavailable coercion, got %s", got)
	}
}
