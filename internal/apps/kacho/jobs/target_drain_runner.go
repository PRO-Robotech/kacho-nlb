// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// target_drain_runner.go — двухфазный drain background worker.
//
// Контекст: NetworkLoadBalancer.TargetGroupService.RemoveTargets — двухфазный
// (+):
//
//   - фаза A (synchronous, в request-path RemoveTargets handler'а): атомарный
//     `UPDATE targets SET status='DRAINING', drain_started_at=now WHERE...`.
//     Клиент сразу видит target вне traffic-pool (data-plane прекращает new
//     connections), но строка ещё в БД — flow'ам разрешено доиграть.
//
//   - фаза B (этот runner): периодический tick (default 10s).
//     `DELETE FROM targets WHERE status='DRAINING' AND
//     drain_started_at < now - tg.deregistration_delay_seconds`.
//     После DELETE — INSERT в `nlb_outbox` (DISTINCT per TG)
//     событие `nlb_target_group:<tg_id> UPDATED` → trigger `nlb_outbox_notify_trg`
//     шлёт `pg_notify('nlb_outbox', seq)` → lifecycle stream к iam.
//
// Архитектура (workspace CLAUDE.md «Чистая архитектура»): runner использует
// `*pgxpool.Pool` напрямую, минуя CQRS Repository — это намеренно (godzila
// pattern для админ-job'ов: drain — pure SQL operator, не use-case с
// бизнес-логикой; не пересекается с handler'ами).
//
// Failure isolation: транзиентные SQL-errors на drainOnce логируются и
// **НЕ** завершают Run (continue loop). Только `ctx.Done` exits.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// drainSQL — атомарный DELETE expired DRAINING targets + DISTINCT outbox emit
// одним statement'ом (CTE). Per-TG expiry: `drain_started_at + interval` берёт
// `tg.deregistration_delay_seconds` из owning TG.
//
// Возвращает:
//
//	deleted (bigint) — сколько targets удалено;
//	tgs     (int4)   — сколько DISTINCT TG получили UPDATED outbox row.
//
// Single round-trip → один acquire pool conn, один TX-implicit, минимум
// latency. JOIN на target_groups — ON DELETE RESTRICT FK уже гарантирует, что
// tg существует для каждого target.
const drainSQL = `
WITH drained AS (
    DELETE FROM kacho_nlb.targets t
    USING kacho_nlb.target_groups tg
    WHERE t.target_group_id = tg.id
      AND t.status = 'DRAINING'
      AND t.drain_started_at < now() - make_interval(secs => tg.deregistration_delay_seconds)
    RETURNING t.target_group_id
),
outbox_emitted AS (
    INSERT INTO kacho_nlb.nlb_outbox
        (resource_type, resource_id, project_id, action, payload)
    SELECT DISTINCT
        'nlb_target_group',
        d.target_group_id,
        tg.project_id,
        'UPDATED',
        jsonb_build_object(
            'id', d.target_group_id,
            'reason', 'drain_complete'
        )
    FROM drained d
    JOIN kacho_nlb.target_groups tg ON tg.id = d.target_group_id
    RETURNING 1
)
SELECT
    (SELECT count(*) FROM drained)        AS deleted,
    (SELECT count(*) FROM outbox_emitted) AS tgs
`

// TargetDrainRunner — фоновый worker, реализующий двухфазный drain.
// Запускается из cmd/kacho-loadbalancer/main.go параллельно с gRPC-серверами
// через H-BF/corlib/pkg/parallel.ExecAbstract.
type TargetDrainRunner struct {
	pool     *pgxpool.Pool
	logger   *slog.Logger
	interval time.Duration

	// onTickErr — test-only observation hook (nil в проде), вызывается с
	// non-ctx ошибкой tick'а. Позволяет integration-тесту детерминированно
	// дождаться реально произошедшей transient-ошибки вместо wall-clock sleep
	// (audit TEST #7, CWE-367).
	onTickErr func(error)
}

// NewTargetDrainRunner создаёт runner. `interval` — период между tick'ами
// (рекомендованный default 10s; задаётся через `cfg.Jobs.TargetDrain.Interval`).
// Если interval <= 0 — используется fallback 10s (defense-in-depth от
// мисконфига; основная защита — config.Validate).
func NewTargetDrainRunner(pool *pgxpool.Pool, logger *slog.Logger, interval time.Duration) *TargetDrainRunner {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &TargetDrainRunner{
		pool:     pool,
		logger:   logger,
		interval: interval,
	}
}

// Run блокирует goroutine до отмены ctx. Каждые `r.interval` вызывает
// drainOnce(ctx); transient errors logging + continue (не exit Run).
//
// Возвращает nil после ctx.Done — это «штатное завершение» runner'а
// при SIGTERM (parallel.ExecAbstract воспринимает nil как успех task'а).
func (r *TargetDrainRunner) Run(ctx context.Context) error {
	r.logger.InfoContext(ctx, "target drain runner started", "interval", r.interval)
	defer r.logger.InfoContext(ctx, "target drain runner stopped")

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Первый tick — сразу, не ждать interval (быстрее «убираем мусор»,
	// если процесс рестартовал когда expired targets уже накопились).
	r.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick — одна итерация: вызывает drainOnce и логирует результат. Транзиентные
// errors не пропускаются наружу («runner respects ctx cancel
// cleanly; transient errors do not abort the loop»).
func (r *TargetDrainRunner) tick(ctx context.Context) {
	start := time.Now()
	deleted, tgs, err := r.drainOnce(ctx)
	took := time.Since(start)

	if err != nil {
		// ctx.Err ловится отдельно: cancel в середине tick'а — штатно.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		r.logger.ErrorContext(ctx, "target drain tick failed",
			"err", err, "took", took)
		if r.onTickErr != nil {
			r.onTickErr(err)
		}
		return
	}
	r.logger.InfoContext(ctx, "target drain tick",
		"deleted", deleted, "tgs", tgs, "took_ms", took.Milliseconds())
}

// drainOnce — один tick: атомарный DELETE+outbox emit (см. drainSQL).
// Возвращает (deleted_count, distinct_tg_count, err).
func (r *TargetDrainRunner) drainOnce(ctx context.Context) (int64, int, error) {
	var deleted int64
	var tgs int
	if err := r.pool.QueryRow(ctx, drainSQL).Scan(&deleted, &tgs); err != nil {
		return 0, 0, fmt.Errorf("drain expired targets: %w", err)
	}
	return deleted, tgs, nil
}
