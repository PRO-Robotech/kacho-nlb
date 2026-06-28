// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operationresolver

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeLB / fakeListener / fakeTG — конфигурируемые read-порты: Get отдаёт либо
// заданный proto-ресурс (present), либо domain.ErrNotFound (absent), либо
// произвольную transient-ошибку.
type fakeLB struct {
	rec *lbv1.NetworkLoadBalancer
	err error
}

func (f fakeLB) Get(context.Context, string) (*lbv1.NetworkLoadBalancer, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.rec == nil {
		return nil, domain.ErrNotFound
	}
	return f.rec, nil
}

type fakeListener struct {
	rec *lbv1.Listener
	err error
}

func (f fakeListener) Get(context.Context, string) (*lbv1.Listener, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.rec == nil {
		return nil, domain.ErrNotFound
	}
	return f.rec, nil
}

type fakeTG struct {
	rec *lbv1.TargetGroup
	err error
}

func (f fakeTG) Get(context.Context, string) (*lbv1.TargetGroup, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.rec == nil {
		return nil, domain.ErrNotFound
	}
	return f.rec, nil
}

func newOp(t *testing.T, meta proto.Message) operations.Operation {
	t.Helper()
	op, err := operations.New("nlb", "test op", meta)
	if err != nil {
		t.Fatalf("operations.New: %v", err)
	}
	return op
}

func TestResolve_CreateLoadBalancerPresent_Done(t *testing.T) {
	r := New(Readers{LoadBalancer: fakeLB{rec: &lbv1.NetworkLoadBalancer{Id: "nlb1"}}})
	op := newOp(t, &lbv1.CreateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: "nlb1"})

	res, err := r.Resolve(context.Background(), op)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeDone {
		t.Fatalf("outcome = %v, want Done", res.Outcome)
	}
	if res.Response == nil {
		t.Fatal("Response nil, want marshalled NetworkLoadBalancer")
	}
	got, err := res.Response.UnmarshalNew()
	if err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	lb, ok := got.(*lbv1.NetworkLoadBalancer)
	if !ok || lb.GetId() != "nlb1" {
		t.Fatalf("response = %T %v, want NetworkLoadBalancer{id:nlb1}", got, got)
	}
}

func TestResolve_CreateLoadBalancerAbsent_Interrupted(t *testing.T) {
	r := New(Readers{LoadBalancer: fakeLB{rec: nil}})
	op := newOp(t, &lbv1.CreateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: "nlb1"})

	res, err := r.Resolve(context.Background(), op)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeInterrupted {
		t.Fatalf("outcome = %v, want Interrupted", res.Outcome)
	}
}

func TestResolve_DeleteTargetGroupAbsent_Done(t *testing.T) {
	r := New(Readers{TargetGroup: fakeTG{rec: nil}})
	op := newOp(t, &lbv1.DeleteTargetGroupMetadata{TargetGroupId: "tgr9"})

	res, err := r.Resolve(context.Background(), op)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeDone {
		t.Fatalf("outcome = %v, want Done", res.Outcome)
	}
	if res.Response != nil {
		t.Fatalf("Response = %v, want nil (Empty semantics) for delete", res.Response)
	}
}

func TestResolve_DeleteListenerPresent_Interrupted(t *testing.T) {
	r := New(Readers{Listener: fakeListener{rec: &lbv1.Listener{Id: "lst9"}}})
	op := newOp(t, &lbv1.DeleteListenerMetadata{ListenerId: "lst9"})

	res, err := r.Resolve(context.Background(), op)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeInterrupted {
		t.Fatalf("outcome = %v, want Interrupted", res.Outcome)
	}
}

func TestResolve_AddTargetsPresent_DoneWithTargetGroup(t *testing.T) {
	r := New(Readers{TargetGroup: fakeTG{rec: &lbv1.TargetGroup{Id: "tgr9"}}})
	op := newOp(t, &lbv1.AddTargetsMetadata{TargetGroupId: "tgr9"})

	res, err := r.Resolve(context.Background(), op)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeDone {
		t.Fatalf("outcome = %v, want Done", res.Outcome)
	}
	got, err := res.Response.UnmarshalNew()
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tg, ok := got.(*lbv1.TargetGroup); !ok || tg.GetId() != "tgr9" {
		t.Fatalf("response = %T, want TargetGroup{id:tgr9}", got)
	}
}

func TestResolve_AttachTargetGroupPresent_DoneWithLoadBalancer(t *testing.T) {
	r := New(Readers{LoadBalancer: fakeLB{rec: &lbv1.NetworkLoadBalancer{Id: "nlb1"}}})
	op := newOp(t, &lbv1.AttachNetworkLoadBalancerTargetGroupMetadata{
		NetworkLoadBalancerId: "nlb1", TargetGroupId: "tgr9",
	})

	res, err := r.Resolve(context.Background(), op)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeDone {
		t.Fatalf("outcome = %v, want Done (LB existence preserved by attach)", res.Outcome)
	}
}

func TestResolve_TransientReadError_Propagates(t *testing.T) {
	boom := errors.New("db down")
	r := New(Readers{LoadBalancer: fakeLB{err: boom}})
	op := newOp(t, &lbv1.UpdateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: "nlb1"})

	_, err := r.Resolve(context.Background(), op)
	if err == nil {
		t.Fatal("want error on transient read failure, got nil")
	}
}

func TestResolve_NilMetadata_Skip(t *testing.T) {
	r := New(Readers{})
	res, err := r.Resolve(context.Background(), operations.Operation{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeSkip {
		t.Fatalf("outcome = %v, want Skip", res.Outcome)
	}
}

func TestResolve_UnknownMetadata_Skip(t *testing.T) {
	r := New(Readers{LoadBalancer: fakeLB{rec: &lbv1.NetworkLoadBalancer{Id: "nlb1"}}})
	// A non-operation metadata proto (request message) → not in the switch → Skip.
	op := newOp(t, &lbv1.GetTargetGroupRequest{TargetGroupId: "tgr1"})

	res, err := r.Resolve(context.Background(), op)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeSkip {
		t.Fatalf("outcome = %v, want Skip", res.Outcome)
	}
}

func TestResolve_NilReader_Skip(t *testing.T) {
	// LoadBalancer reader not wired → orphan skipped (dev / partial wiring).
	r := New(Readers{})
	op := newOp(t, &lbv1.CreateNetworkLoadBalancerMetadata{NetworkLoadBalancerId: "nlb1"})

	res, err := r.Resolve(context.Background(), op)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Outcome != operations.OutcomeSkip {
		t.Fatalf("outcome = %v, want Skip when reader nil", res.Outcome)
	}
}
