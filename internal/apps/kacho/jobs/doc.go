// Package jobs — фоновые workers сервиса.
//
// TODO(KAC-167):
//   - outbox_drainer.go — потребитель nlb_outbox через kacho-corelib/outbox/drainer.
//   - fga_tuple_writer.go — обработчик resource-lifecycle event'ов из outbox →
//     iam.InternalIAMService.WriteCreatorTuple / DeleteTuples (at-least-once).
//   - target_drain_runner.go — Phase B 2-phase drain (period 10s, DELETE
//     FROM targets WHERE status='DRAINING' AND drain_started_at < now() -
//     tg.deregistration_delay_seconds).
//
// Запускаются параллельно с gRPC-серверами через H-BF/corlib/pkg/parallel.ExecAbstract.
package jobs
