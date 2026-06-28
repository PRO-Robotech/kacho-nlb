// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	opmetrics "github.com/PRO-Robotech/kacho-corelib/operations"
	outboxmetrics "github.com/PRO-Robotech/kacho-corelib/outbox/metrics"
)

// scrape собирает текст /metrics через приватный реестр адаптера.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics code=%d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func TestMetrics_ImplementsBothRecorders(t *testing.T) {
	var _ opmetrics.Recorder = New("v", "c")
	var _ outboxmetrics.Recorder = New("v", "c")
}

func TestMetrics_OperationsSeriesExported(t *testing.T) {
	m := New("test", "abc123")
	m.IncTerminalWriteRetries("MarkDone")
	m.IncTerminalWriteFailures("MarkError")
	m.IncOrphansRecovered("done")
	m.IncReconcileRuns()
	m.IncReconcileErrors()
	m.SetInflight(3)

	out := scrape(t, m)
	for _, want := range []string{
		"kacho_nlb_operations_terminal_write_retries_total",
		"kacho_nlb_operations_terminal_write_failures_total",
		"kacho_nlb_operations_orphans_recovered_total",
		"kacho_nlb_operations_reconcile_runs_total",
		"kacho_nlb_operations_reconcile_errors_total",
		"kacho_nlb_lro_workers_active",
		"kacho_nlb_build_info",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics missing series %q", want)
		}
	}
	if !strings.Contains(out, `kacho_nlb_operations_orphans_recovered_total{outcome="done"} 1`) {
		t.Errorf("orphans_recovered{done} not 1; out:\n%s", out)
	}
}

func TestMetrics_OutboxSeriesExported(t *testing.T) {
	m := New("test", "abc123")
	m.SetBacklogDepth("kacho_nlb.fga_register_outbox", 5)
	m.SetOldestPendingAgeSeconds("kacho_nlb.fga_register_outbox", 42)
	m.SetPoisonedCount("kacho_nlb.fga_register_outbox", 1)
	m.IncPoisoned("kacho_nlb.fga_register_outbox")

	out := scrape(t, m)
	for _, want := range []string{
		"kacho_nlb_outbox_backlog_depth",
		"kacho_nlb_outbox_oldest_pending_age_seconds",
		"kacho_nlb_outbox_poisoned_current",
		"kacho_nlb_outbox_poisoned_total",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics missing outbox series %q", want)
		}
	}
}

func TestMetrics_DependencyUpMirror(t *testing.T) {
	m := New("test", "abc123")
	m.SetDependencyUp("database", true)
	m.SetDependencyUp("lro-worker", false)

	out := scrape(t, m)
	if !strings.Contains(out, `kacho_nlb_dependency_up{dependency="database"} 1`) {
		t.Errorf("dependency_up{database} not 1; out:\n%s", out)
	}
	if !strings.Contains(out, `kacho_nlb_dependency_up{dependency="lro-worker"} 0`) {
		t.Errorf("dependency_up{lro-worker} not 0; out:\n%s", out)
	}
}
