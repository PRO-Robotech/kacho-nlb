// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package metrics — Prometheus observability adapter kacho-nlb.
//
// Живёт на adapter-границе (Clean Architecture): prometheus-клиент импортируется
// ТОЛЬКО здесь и в composition root (cmd/kacho-loadbalancer) — никогда в domain/
// или use-case-слое. Метрики снимаются с отдельного cluster-internal
// diagnostic-порта (НЕ на public/internal gRPC-поверхности — internal-cardinality
// не tenant-facing).
//
// Один тип Metrics реализует оба corelib Recorder-порта:
//   - operations.Recorder — terminal-write retries/failures, inflight, orphans,
//     reconcile runs/errors (durability-слой LRO);
//   - outbox/metrics.Recorder — backlog/oldest/poisoned register-outbox.
//
// Плюс dependency_up зеркало readiness и build_info. Реестр — ПРИВАТНЫЙ
// (prometheus.NewRegistry, не global default): тесты герметичны, нет
// duplicate-register panic при рестартах composition root в одном процессе.
package metrics

import (
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	opmetrics "github.com/PRO-Robotech/kacho-corelib/operations"
	outboxmetrics "github.com/PRO-Robotech/kacho-corelib/outbox/metrics"
)

// Metrics владеет приватным prometheus-реестром и коллекторами kacho-nlb.
// Создаётся один раз в composition root и шарится diagnostic HTTP-listener'ом,
// LRO-reconciler'ом, outbox-collector'ом/drainer'ом и readiness-агрегатором.
type Metrics struct {
	reg *prometheus.Registry

	// operations (durability LRO)
	terminalRetries  *prometheus.CounterVec
	terminalFailures *prometheus.CounterVec
	orphans          *prometheus.CounterVec
	reconcileRuns    prometheus.Counter
	reconcileErrors  prometheus.Counter
	inflight         atomic.Int64

	// outbox (register-intent доставка)
	outboxBacklog   *prometheus.GaugeVec
	outboxOldest    *prometheus.GaugeVec
	outboxPoisonCur *prometheus.GaugeVec
	outboxPoisonTot *prometheus.CounterVec

	// free-ip reconciler (застрявшие LoadBalancer'ы)
	freeIPPoisoned prometheus.Counter

	// readiness mirror
	dependencyUp *prometheus.GaugeVec
}

// New конструирует адаптер, регистрирует Go + process runtime-коллекторы,
// build_info (const-метка сборки) и доменные коллекторы kacho-nlb.
func New(version, commit string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		reg: reg,
		terminalRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kacho_nlb_operations_terminal_write_retries_total",
			Help: "Retries of LRO terminal write (MarkDone/MarkError) on transient DB failure, by op.",
		}, []string{"op"}),
		terminalFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kacho_nlb_operations_terminal_write_failures_total",
			Help: "LRO terminal writes that failed after exhausting retries (row stays done=false), by op.",
		}, []string{"op"}),
		orphans: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kacho_nlb_operations_orphans_recovered_total",
			Help: "Orphaned LRO resolved by the reconciler, by terminal outcome (done|error).",
		}, []string{"outcome"}),
		reconcileRuns: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kacho_nlb_operations_reconcile_runs_total",
			Help: "Reconciler sweep cycles executed.",
		}),
		reconcileErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kacho_nlb_operations_reconcile_errors_total",
			Help: "Reconciler sweep cycles that hit an error.",
		}),
		outboxBacklog: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kacho_nlb_outbox_backlog_depth",
			Help: "Pending rows in the register-outbox (sent_at IS NULL), by table.",
		}, []string{"table"}),
		outboxOldest: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kacho_nlb_outbox_oldest_pending_age_seconds",
			Help: "Age of the oldest pending register-outbox row, by table.",
		}, []string{"table"}),
		outboxPoisonCur: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kacho_nlb_outbox_poisoned_current",
			Help: "Current poisoned rows in the register-outbox, by table (Collector scan).",
		}, []string{"table"}),
		outboxPoisonTot: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kacho_nlb_outbox_poisoned_total",
			Help: "Monotonic register-outbox poison events (lost owner-tuple delivery), by table.",
		}, []string{"table"}),
		freeIPPoisoned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "kacho_nlb_free_ip_poisoned_total",
			Help: "Monotonic count of stuck LoadBalancers isolated by the free-ip reconciler due to a permanent VIP-release failure (VIP still allocated, no longer head-of-line-blocking the queue).",
		}),
		dependencyUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kacho_nlb_dependency_up",
			Help: "Readiness mirror: 1 if the dependency is up, 0 if down, by dependency.",
		}, []string{"dependency"}),
	}

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "kacho_nlb_build_info",
		Help:        "Build metadata of the running kacho-nlb binary (constant 1).",
		ConstLabels: prometheus.Labels{"version": version, "commit": commit},
	})
	buildInfo.Set(1)

	// lro_workers_active — живой gauge числа исполняемых LRO worker'ов; значение
	// питается SetInflight (operations.Recorder), читается через GaugeFunc, чтобы
	// быть согласованным с operations.Active без дубль-регистрации.
	lroActive := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "kacho_nlb_lro_workers_active",
		Help: "In-flight LRO worker goroutines (operations.Active()).",
	}, func() float64 { return float64(m.inflight.Load()) })

	reg.MustRegister(
		m.terminalRetries, m.terminalFailures, m.orphans,
		m.reconcileRuns, m.reconcileErrors,
		m.outboxBacklog, m.outboxOldest, m.outboxPoisonCur, m.outboxPoisonTot,
		m.freeIPPoisoned,
		m.dependencyUp, buildInfo, lroActive,
	)
	return m
}

// Handler возвращает promhttp-handler приватного реестра. Монтируется ТОЛЬКО на
// выделенном cluster-internal diagnostic-listener'е.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// ---- operations.Recorder ----

// IncTerminalWriteRetries инкрементит ретраи терминальной записи по op-лейблу.
func (m *Metrics) IncTerminalWriteRetries(op string) { m.terminalRetries.WithLabelValues(op).Inc() }

// IncTerminalWriteFailures инкрементит невосстановимые терминальные записи.
func (m *Metrics) IncTerminalWriteFailures(op string) { m.terminalFailures.WithLabelValues(op).Inc() }

// SetInflight выставляет число исполняемых worker'ов (lro_workers_active gauge).
func (m *Metrics) SetInflight(n float64) { m.inflight.Store(int64(n)) }

// IncOrphansRecovered инкрементит разрешённые reconciler'ом orphan'ы по outcome.
func (m *Metrics) IncOrphansRecovered(outcome string) { m.orphans.WithLabelValues(outcome).Inc() }

// IncReconcileRuns инкрементит прогоны sweep-цикла reconciler'а.
func (m *Metrics) IncReconcileRuns() { m.reconcileRuns.Inc() }

// IncReconcileErrors инкрементит ошибки sweep-цикла reconciler'а.
func (m *Metrics) IncReconcileErrors() { m.reconcileErrors.Inc() }

// ---- outbox/metrics.Recorder ----

// SetBacklogDepth выставляет глубину pending-очереди register-outbox по таблице.
func (m *Metrics) SetBacklogDepth(table string, depth float64) {
	m.outboxBacklog.WithLabelValues(table).Set(depth)
}

// SetOldestPendingAgeSeconds выставляет возраст старейшей pending-строки.
func (m *Metrics) SetOldestPendingAgeSeconds(table string, age float64) {
	m.outboxOldest.WithLabelValues(table).Set(age)
}

// SetPoisonedCount выставляет текущее число отравленных строк (Collector scan).
func (m *Metrics) SetPoisonedCount(table string, count float64) {
	m.outboxPoisonCur.WithLabelValues(table).Set(count)
}

// IncPoisoned инкрементит монотонный poison-счётчик (drainer poison-observer).
func (m *Metrics) IncPoisoned(table string) { m.outboxPoisonTot.WithLabelValues(table).Inc() }

// ---- free-ip reconciler ----

// IncFreeIPPoisoned инкрементит счётчик изолированных free-ip reconciler'ом
// ядовитых LoadBalancer'ов (permanent VIP-release failure; poison-observer).
func (m *Metrics) IncFreeIPPoisoned() { m.freeIPPoisoned.Inc() }

// ---- readiness mirror ----

// SetDependencyUp зеркалит readiness-состояние зависимости (1=up, 0=down).
func (m *Metrics) SetDependencyUp(dependency string, up bool) {
	v := 0.0
	if up {
		v = 1.0
	}
	m.dependencyUp.WithLabelValues(dependency).Set(v)
}

// Compile-time: адаптер удовлетворяет обоим corelib Recorder-портам.
var (
	_ opmetrics.Recorder     = (*Metrics)(nil)
	_ outboxmetrics.Recorder = (*Metrics)(nil)
)
