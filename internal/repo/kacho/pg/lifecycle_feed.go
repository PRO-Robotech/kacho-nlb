// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// lifecycleListenChannel — Postgres NOTIFY-канал, на который шлёт trigger
// `nlb_outbox_notify_trg` после INSERT в `nlb_outbox` (миграция 0001).
const lifecycleListenChannel = "nlb_outbox"

// lifecycleConnectTimeout — защита от self-DoS: медленный Connect под нагрузкой
// не должен надолго удерживать stream-слот.
const lifecycleConnectTimeout = 2 * time.Second

// LifecycleFeed — pgx-реализация kacho.LifecycleFeed поверх dedicated-conn'ов
// (вне pgxpool — LISTEN требует выделенной сессии).
type LifecycleFeed struct {
	dsn string
}

// NewLifecycleFeed — конструктор. dsn — connection string для dedicated
// LISTEN-conn'ов (composition root передаёт cfg.Repository.Postgres.URL).
func NewLifecycleFeed(dsn string) *LifecycleFeed {
	return &LifecycleFeed{dsn: dsn}
}

// Open поднимает dedicated pgx.Conn под connect-timeout и выполняет LISTEN.
// Ошибка Connect/LISTEN отдаётся как есть — app-слой маппит её в Unavailable
// без leak'а pgx-текста (db host / port / sslmode).
func (f *LifecycleFeed) Open(ctx context.Context) (kacho.LifecycleConn, error) {
	connectCtx, cancel := context.WithTimeout(ctx, lifecycleConnectTimeout)
	conn, err := pgx.Connect(connectCtx, f.dsn)
	cancel()
	if err != nil {
		return nil, err
	}
	// Идентификатор канала — literal, не из user-input (защита от SQL-injection).
	if _, err := conn.Exec(ctx, "LISTEN "+lifecycleListenChannel); err != nil {
		closeCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		_ = conn.Close(closeCtx)
		c()
		return nil, err
	}
	return &lifecycleConn{conn: conn}, nil
}

// lifecycleConn — dedicated LISTEN-сессия.
type lifecycleConn struct {
	conn *pgx.Conn
}

// EventsSince читает строки `nlb_outbox` с sequence_no > cursor (опц. фильтр по
// resource_type) батчем до limit. LIMIT — literal int (не user-input).
func (c *lifecycleConn) EventsSince(
	ctx context.Context, cursor int64, kinds []string, limit int,
) ([]kacho.OutboxEvent, error) {
	args := []any{cursor}
	var kindFilter string
	if len(kinds) > 0 {
		kindFilter = " AND resource_type = ANY($2)"
		args = append(args, kinds)
	}
	q := fmt.Sprintf(`
		SELECT sequence_no, resource_type, resource_id, project_id, action, payload, emitted_at
		FROM kacho_nlb.nlb_outbox
		WHERE sequence_no > $1%s
		ORDER BY sequence_no ASC
		LIMIT %d
	`, kindFilter, limit)

	rows, err := c.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []kacho.OutboxEvent
	for rows.Next() {
		var ev kacho.OutboxEvent
		if err := rows.Scan(&ev.SequenceNo, &ev.ResourceType, &ev.ResourceID,
			&ev.ProjectID, &ev.Action, &ev.Payload, &ev.EmittedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// WaitForNotification блокирует до NOTIFY на канал либо ctx-таймаута/отмены.
func (c *lifecycleConn) WaitForNotification(ctx context.Context) error {
	_, err := c.conn.WaitForNotification(ctx)
	return err
}

// Close снимает LISTEN и закрывает соединение (best-effort, под собственными
// bounded-таймаутами — вызывается из defer на любом пути выхода).
func (c *lifecycleConn) Close() {
	unlistenCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, _ = c.conn.Exec(unlistenCtx, "UNLISTEN "+lifecycleListenChannel)
	cancel()
	closeCtx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	_ = c.conn.Close(closeCtx)
	cancel2()
}

var (
	_ kacho.LifecycleFeed = (*LifecycleFeed)(nil)
	_ kacho.LifecycleConn = (*lifecycleConn)(nil)
)
