package targetgroup

// TODO(KAC-164): RemoveTargetsUseCase — 2-phase drain (acceptance §0.2).
//   - Phase A (sync в worker'е, <500ms): UPDATE targets SET status='DRAINING',
//     drain_started_at=now() → ops.MarkDone(true). Client gets done=true immediately.
//   - Phase B (background `target_drain_runner` job, period 10s): DELETE FROM targets
//     WHERE status='DRAINING' AND drain_started_at < now() - tg.deregistration_delay_seconds.
//     Outbox-emit "nlb_target_group:<tg_id> UPDATED" после успешного DELETE.
