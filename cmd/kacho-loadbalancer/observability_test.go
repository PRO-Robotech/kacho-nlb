// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"

	"github.com/PRO-Robotech/kacho-nlb/internal/observability/health"
)

// okPinger — readiness DB-pinger, всегда здоров.
type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// readyStub — readyProbe-стаб для readiness-чекеров (vip-origin-reconcile).
type readyStub struct{ ready bool }

func (s readyStub) Ready() bool { return s.ready }

func hasChecker(checkers []health.Checker, name string) bool {
	for _, c := range checkers {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestBuildReadinessCheckers_DrainerFlipsWithBootGate(t *testing.T) {
	// lro-worker checker reads the package-level operations.Ready; start the
	// default dispatcher so the only variable under test is the drainer/bootGate.
	operations.Start()

	gate := bootgate.New(bootgate.Config{RequireIAM: true, Service: "kacho-nlb"})
	checkers := buildReadinessCheckers(okPinger{}, gate, readyStub{ready: true})
	agg := health.New(checkers)

	// register-drainer not connected → not ready.
	if ready, down := agg.Evaluate(context.Background()); ready {
		t.Fatalf("expected not-ready before drainer connect; down=%v", down)
	}
	// Connect → ready.
	gate.SetConnected(true)
	if ready, down := agg.Evaluate(context.Background()); !ready {
		t.Fatalf("expected ready after drainer connect; down=%v", down)
	}
}

// TestBuildReadinessCheckers_VIPOriginReconcileGate — until the boot-once
// vip_origin backfill completes, /readyz is held not-ready (fail-closed).
func TestBuildReadinessCheckers_VIPOriginReconcileGate(t *testing.T) {
	operations.Start()
	gate := bootgate.New(bootgate.Config{}) // RequireIAM=false → drainer checker ready
	checkers := buildReadinessCheckers(okPinger{}, gate, readyStub{ready: false})
	agg := health.New(checkers)
	if ready, down := agg.Evaluate(context.Background()); ready {
		t.Fatalf("expected not-ready while vip_origin reconcile pending; down=%v", down)
	}
	checkersReady := buildReadinessCheckers(okPinger{}, gate, readyStub{ready: true})
	if ready, down := health.New(checkersReady).Evaluate(context.Background()); !ready {
		t.Fatalf("expected ready after vip_origin reconcile complete; down=%v", down)
	}
}

func TestBuildReadinessCheckers_CoreDependenciesPresent(t *testing.T) {
	gate := bootgate.New(bootgate.Config{})
	checkers := buildReadinessCheckers(okPinger{}, gate, readyStub{ready: true})
	for _, want := range []string{"database", "register-drainer", "lro-worker", "vip-origin-reconcile"} {
		if !hasChecker(checkers, want) {
			t.Fatalf("readiness checker %q missing", want)
		}
	}
}

func TestSuperviseBackground_UnexpectedExitTriggersShutdown(t *testing.T) {
	var fired atomic.Bool
	ctx := context.Background() // live ctx: a return is UNEXPECTED
	err := superviseBackground(ctx, "drainer",
		func(context.Context) error { return errors.New("loop crashed") },
		func() { fired.Store(true) },
		quietLogger())
	if err == nil {
		t.Fatal("unexpected exit must return a non-nil error")
	}
	if !fired.Load() {
		t.Fatal("unexpected exit must invoke onUnexpectedExit (readiness flip)")
	}
}

func TestSuperviseBackground_NormalShutdownNoTrigger(t *testing.T) {
	var fired atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ctx already cancelled: a return is the NORMAL shutdown path
	err := superviseBackground(ctx, "reconciler",
		func(c context.Context) error { <-c.Done(); return nil },
		func() { fired.Store(true) },
		quietLogger())
	if err != nil {
		t.Fatalf("normal shutdown must return nil, got %v", err)
	}
	if fired.Load() {
		t.Fatal("normal shutdown must NOT invoke onUnexpectedExit")
	}
}
