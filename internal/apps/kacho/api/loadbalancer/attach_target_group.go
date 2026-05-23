package loadbalancer

// TODO(KAC-155): AttachTargetGroupUseCase — idempotent INSERT ... ON CONFLICT DO NOTHING
// в pivot-таблицу attached_target_groups. Acceptance §0.2: ОТМЕНЯЕТ старую
// full-replace семантику attached_target_groups[].
//   - same-region check на LB.region_id == TG.region_id (DB CHECK constraint).
//   - DB-level partial UNIQUE на (lb_id, tg_id) — gracefully no-op на повторный attach.
