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
	const q = `INSERT INTO kacho_nlb.fga_register_outbox
        (event_type, payload, resource_kind, resource_id)
        VALUES ($1, $2::jsonb, $3, $4)`
	if _, err := e.tx.Exec(ctx, q, eventType, payload, intent.Kind, intent.ResourceID); err != nil {
		return mapPgErr(err, "fga_register_outbox", "")
	}
	return nil
}
