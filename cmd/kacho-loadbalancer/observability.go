// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

// Observability-проводка composition root: Prometheus diagnostic-listener
// (metrics + /healthz + /readyz), dependency-aware readiness, LRO-worker boot и
// супервизор фоновых goroutine. prometheus импортируется только в adapter-пакете
// internal/observability/metrics (Clean Architecture) — здесь лишь wiring.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/jobs"
	"github.com/PRO-Robotech/kacho-nlb/internal/observability/health"
	"github.com/PRO-Robotech/kacho-nlb/internal/observability/metrics"
)

// Сентинелы readiness-чекеров: причина «down» в логах/ответе /readyz без leak'а
// внутренних деталей наружу (имена зависимостей — operational, cluster-internal).
var (
	errDrainerNotConnected       = errors.New("register-drainer not connected to kacho-iam")
	errLROWorkerDown             = errors.New("LRO dispatcher loop not running")
	errVIPOriginReconcilePending = errors.New("vip_origin boot reconcile not complete")
)

// readyProbe — узкий булев readiness-сигнал (boot-once задачи). Реализуется
// vipOriginReconcileGate; тесты подставляют стаб.
type readyProbe interface {
	Ready() bool
}

// vipOriginReconcileGate — boolean-флаг готовности boot-reconcile'а vip_origin.
// До успешного backfill (или no-op на свежем стенде) /readyz держится not-ready
// (fail-closed): ни один Delete не должен уйти по неверной release-ветке, пока
// существующие BYO-листенеры не получили реальный vip_origin.
type vipOriginReconcileGate struct {
	done atomic.Bool
}

// markReady открывает gate (reconcile успешно завершён, в т.ч. no-op).
func (g *vipOriginReconcileGate) markReady() { g.done.Store(true) }

// Ready — readyProbe: true когда reconcile завершён.
func (g *vipOriginReconcileGate) Ready() bool { return g.done.Load() }

// build-info — инжектится через -ldflags "-X main.buildVersion=… -X main.buildCommit=…";
// дефолты для локальной сборки.
var (
	buildVersion = "dev"
	buildCommit  = "unknown"
)

// readinessPinger — узкий DB-порт readiness (Ping). *pgxpool.Pool его
// удовлетворяет; тесты подставляют фейк.
type readinessPinger interface {
	Ping(ctx context.Context) error
}

// startLROWorker подключает Prometheus-Recorder и логгер к package-level
// default-registry LRO-worker'а (ConfigureDefault) и поднимает его dispatcher-loop
// (Start) ДО приёма трафика. Решает два дефекта boot'а:
//   - readiness-deadlock: без явного Start dispatcher стартует лениво на первом
//     Run, но под в NotReady трафика не получает → Run не происходит → вечный
//     NotReady. Явный Start делает Ready=true до трафика;
//   - dead live-worker метрики: default-registry создаётся с NopRecorder, поэтому
//     terminal-write retries/failures и inflight gauge от ЖИВОГО worker-пути не
//     эмитились. WithRecorder подключает их к /metrics.
//
// ConfigureDefault обязан предшествовать Start; вызывается один раз из composition
// root (повторный вызов после старта вернул бы ErrWorkerStarted).
func startLROWorker(rec operations.Recorder, logger *slog.Logger) error {
	if err := operations.ConfigureDefault(operations.WithRecorder(rec), operations.WithLogger(logger)); err != nil {
		return fmt.Errorf("configure LRO default-registry: %w", err)
	}
	operations.Start()
	return nil
}

// buildReadinessCheckers собирает чекеры критичных зависимостей для readiness.
// liveness намеренно НЕ включает их (защита от restart-storm).
//
//   - database — pgxpool.Ping;
//   - register-drainer — bootGate.Ready: в nlb register-drainer держит conn к
//     iam-internal (9091, тот же conn несёт InternalIAMService.Check), поэтому
//     этот чекер — и сигнал «IAM-достижим». При require-iam=false gate всегда
//     Ready (dev back-compat);
//   - lro-worker — operations.Ready: dispatcher-loop запущен и готов забирать
//     in-flight операции.
//   - vip-origin-reconcile — boot-once backfill listeners.vip_origin завершён
//     (fail-closed: until then Delete release-ветка может быть неверной для
//     pre-existing BYO-листенеров).
func buildReadinessCheckers(db readinessPinger, gate *bootgate.Gate, vipOrigin readyProbe) []health.Checker {
	return []health.Checker{
		{Name: "database", Check: func(ctx context.Context) error { return db.Ping(ctx) }},
		{Name: "register-drainer", Check: func(context.Context) error {
			if gate.Ready() {
				return nil
			}
			return errDrainerNotConnected
		}},
		{Name: "lro-worker", Check: func(context.Context) error {
			if operations.Ready() {
				return nil
			}
			return errLROWorkerDown
		}},
		{Name: "vip-origin-reconcile", Check: func(context.Context) error {
			if vipOrigin.Ready() {
				return nil
			}
			return errVIPOriginReconcilePending
		}},
	}
}

// vipOriginReconcileRetry — пауза между попытками boot-reconcile при недоступном
// vpc (readiness держится not-ready всё это время).
const vipOriginReconcileRetry = 5 * time.Second

// runVIPOriginReconcile — supervised boot-once задача: гоняет
// VIPOriginReconciler.Reconcile с retry, пока не пройдёт успешно (или ctx не
// отменён). На успехе открывает readiness-gate и блокируется до shutdown
// (чтобы superviseBackground видел ctx-cancel как штатный выход, а не
// «неожиданный exit»). Idempotent: повторные прогоны безопасны.
func runVIPOriginReconcile(ctx context.Context, rec *jobs.VIPOriginReconciler, gate *vipOriginReconcileGate, logger *slog.Logger) error {
	for {
		if err := rec.Reconcile(ctx); err == nil {
			gate.markReady()
			logger.Info("vip_origin reconcile complete — readiness gate opened")
			<-ctx.Done()
			return nil
		} else if ctx.Err() != nil {
			return nil
		} else {
			logger.Warn("vip_origin reconcile failed — readiness held not-ready",
				"err", err, "retry_in", vipOriginReconcileRetry)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(vipOriginReconcileRetry):
		}
	}
}

// superviseBackground оборачивает долгоживущий фоновый loop так, что его
// НЕОЖИДАННЫЙ возврат (loop вышел, пока ctx ещё жив) флипает readiness и
// триггерит graceful-shutdown. Возврат после отмены ctx — штатный путь (nil,
// onUnexpectedExit не вызывается). Убирает fire-and-forget семантику фоновых
// goroutine.
func superviseBackground(ctx context.Context, name string, run func(context.Context) error, onUnexpectedExit func(), logger *slog.Logger) error {
	err := run(ctx)
	if ctx.Err() != nil {
		// ctx отменён (SIGTERM / shutdown-триггер) — это штатное завершение.
		return nil
	}
	logger.Error("background task exited unexpectedly", "task", name, "err", err)
	if onUnexpectedExit != nil {
		onUnexpectedExit()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return fmt.Errorf("%s exited unexpectedly", name)
}

// startDiagnosticListener поднимает cluster-internal HTTP-listener для метрик и
// health-проб. Возвращает task для супервизора и shutdown-функцию. Отключён
// (пустой addr) → (nil, no-op): листенер не поднимается (back-compat).
func startDiagnosticListener(addr string, m *metrics.Metrics, agg *health.Aggregator, logger *slog.Logger) (task func() error, shutdown func(context.Context), err error) {
	if addr == "" {
		return nil, func(context.Context) {}, nil
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	mux.Handle("GET /healthz", agg.LiveHandler())
	mux.Handle("GET /readyz", agg.ReadyHandler())

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	lis, lerr := net.Listen("tcp", addr)
	if lerr != nil {
		return nil, nil, lerr
	}
	logger.Info("kacho-nlb diagnostic listener", "endpoint", addr, "paths", "/metrics,/healthz,/readyz")

	task = func() error {
		if serr := srv.Serve(lis); serr != nil && serr != http.ErrServerClosed {
			return serr
		}
		return nil
	}
	shutdown = func(ctx context.Context) { _ = srv.Shutdown(ctx) }
	return task, shutdown, nil
}
