// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package jobs — фоновые workers сервиса kacho-nlb.
//
// Запускаются параллельно с gRPC-серверами через
// H-BF/corlib/pkg/parallel.ExecAbstract (composition root в
// cmd/kacho-loadbalancer/main.go).
//
// Реализованные:
//
//   - target_drain_runner.go — двухфазный drain. Периодически
//     (interval из cfg.Jobs.TargetDrain.Interval, default 10s) делает
//     `DELETE FROM kacho_nlb.targets WHERE status='DRAINING' AND
//     drain_started_at < now - tg.deregistration_delay_seconds` + INSERT
//     DISTINCT outbox `nlb_target_group:<tg_id> UPDATED`. Логирует каждый
//     tick (deleted/tgs/took_ms). Transient errors → log + continue;
//     ctx cancel → штатное завершение.
//
// Запланированные (отдельные tracking-issues, не в текущем PR):
//
//   - outbox_drainer (kacho-corelib/outbox consumer для lifecycle stream).
//   - fga_tuple_writer (обработчик resource-lifecycle событий → iam FGA tuples).
package jobs
