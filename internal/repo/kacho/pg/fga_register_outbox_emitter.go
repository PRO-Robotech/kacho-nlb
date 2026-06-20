package pg

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fgaRegisterEmitter — реализация kacho.FGARegisterEmitter. INSERT в
// `fga_register_outbox` в той же TX, что и DML ресурса; trigger
// `fga_register_outbox_notify_trg` шлёт `pg_notify('kacho_nlb_fga_register_outbox',
// id::text)` после commit'а, будя register-drainer (SEC-D).
type fgaRegisterEmitter struct {
	tx pgx.Tx
}

// Emit добавляет register-intent строку в текущей TX writer'а. Пустой набор
// tuple → no-op (нечего регистрировать — не пишем пустую строку).
//
// payload — JSON-сериализованный набор tuple-намерений (project-hierarchy +
// creator + parent-link), OQ-SEC-D-2: весь набор ресурса одной строкой.
// CHECK на event_type / jsonb_typeof заложены в миграции 0002 — typo в caller'е
// → SQLSTATE 23514 → ErrInvalidArg в mapPgErr.
func (e *fgaRegisterEmitter) Emit(ctx context.Context, eventType string, intent domain.FGARegisterIntent) error {
	if len(intent.Tuples) == 0 {
		return nil
	}
	payload, err := intent.Marshal()
	if err != nil {
		return mapPgErr(err, "fga_register_outbox", "")
	}
	// epic-rsab T3 (β-hardening): stamp the monotonic source_version into the
	// payload from the DB clock (now()) AT INSERT TIME, inside this writer-tx — the
	// exact instant the source-state is recorded. jsonb_set merges it into the
	// encoded payload so the register-drainer forwards it to
	// RegisterResourceRequest.source_version (last-source-state-wins: a reordered
	// stale intent → no-op in IAM, not an overwrite). For sequential mutations of
	// one object a later writer-tx commits-after the earlier → its now() is strictly
	// greater → monotonic per-object. now() === transaction_timestamp(), matching
	// the row's own created_at default (same tx instant).
	const q = `INSERT INTO kacho_nlb.fga_register_outbox
        (event_type, payload, resource_kind, resource_id)
        VALUES ($1, jsonb_set($2::jsonb, '{source_version}', to_jsonb(now())), $3, $4)`
	if _, err := e.tx.Exec(ctx, q, eventType, payload, intent.Kind, intent.ResourceID); err != nil {
		return mapPgErr(err, "fga_register_outbox", "")
	}
	return nil
}
