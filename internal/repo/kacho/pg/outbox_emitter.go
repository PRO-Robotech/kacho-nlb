// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// outboxEmitter — реализация kacho.OutboxEmitter. INSERT в `nlb_outbox` в той
// же TX, что и DML; trigger `nlb_outbox_notify_trg` шлёт
// `pg_notify('nlb_outbox', sequence_no::text)` после commit'а.
type outboxEmitter struct {
	tx pgx.Tx
}

// Emit добавляет outbox-row в текущей TX writer'а.
//
// CHECK constraints на resource_type / action заложены в миграции 0001 — typo
// в caller'е → SQLSTATE 23514 → ErrInvalidArg в mapPgErr. Это намеренный
// belt-and-suspenders: каждый caller обязан использовать константы
// (`kacho.OutboxResource*` / `kacho.OutboxAction*` из leaf-пакета), но DB их
// валидирует тоже.
func (e *outboxEmitter) Emit(ctx context.Context, resourceType, resourceID, projectID, action string, payload map[string]any) error {
	payloadJSON := []byte(`{}`)
	if len(payload) > 0 {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal outbox payload: %w", err)
		}
		payloadJSON = b
	}
	const q = `INSERT INTO kacho_nlb.nlb_outbox
        (resource_type, resource_id, project_id, action, payload)
        VALUES ($1, $2, $3, $4, $5::jsonb)`
	if _, err := e.tx.Exec(ctx, q, resourceType, resourceID, projectID, action, payloadJSON); err != nil {
		return mapPgErr(err, "outbox", "")
	}
	return nil
}
