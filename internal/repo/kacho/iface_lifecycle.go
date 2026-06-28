// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import (
	"context"
	"time"
)

// OutboxEvent — репо-нейтральная проекция одной строки `nlb_outbox` для
// lifecycle-фида. Без pgx-типов: app-слой (InternalResourceLifecycleService)
// зависит только от этой структуры, не от драйвера БД (dependency rule).
//
// resourceType ∈ {`nlb_load_balancer`,`nlb_listener`,`nlb_target_group`};
// action ∈ {`CREATED`,`UPDATED`,`DELETED`,`MOVED`,`FAILED`} (CHECK в миграции 0001).
// Payload — сырой JSON outbox-row (consumer извлекает parent_resource_id /
// old_project_id сам).
type OutboxEvent struct {
	SequenceNo   int64
	ResourceType string
	ResourceID   string
	ProjectID    string
	Action       string
	Payload      []byte
	EmittedAt    time.Time
}

// LifecycleFeed — порт доступа к LISTEN/NOTIFY-фиду `nlb_outbox`. Реализация
// (internal/repo/kacho/pg) инкапсулирует pgx; use-case держит только этот
// интерфейс, поэтому app-слой не импортирует драйвер.
//
// Open открывает dedicated-сессию (Connect + `LISTEN nlb_outbox`) ВНЕ pgxpool —
// LISTEN некорректно работает на pooled-conn. Возвращённую сессию обязан
// закрыть caller (defer Close).
type LifecycleFeed interface {
	Open(ctx context.Context) (LifecycleConn, error)
}

// LifecycleConn — dedicated LISTEN-сессия одного Subscribe-стрима. Не
// потокобезопасна (используется одной горутиной). Close обязателен и идемпотентен.
type LifecycleConn interface {
	// EventsSince возвращает события с sequence_no > cursor (resource_type ∈ kinds,
	// если непустой), не более limit за вызов, ORDER BY sequence_no ASC.
	EventsSince(ctx context.Context, cursor int64, kinds []string, limit int) ([]OutboxEvent, error)
	// WaitForNotification блокирует до NOTIFY на канал `nlb_outbox` либо
	// ctx-таймаута/отмены.
	WaitForNotification(ctx context.Context) error
	// Close снимает LISTEN и закрывает соединение (best-effort).
	Close()
}
