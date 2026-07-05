// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import "context"

// OutboxEmitter — emit одного outbox-события (`nlb_outbox` row + trigger
// `pg_notify('nlb_outbox', sequence_no::text)`). Использует pgx.Tx writer'а,
// поэтому DML + outbox commit'ятся атомарно: либо resource + event оба видны
// watcher'у, либо ничего (Abort/crash).
//
// resourceType ∈ {`nlb_load_balancer`,`nlb_listener`,`nlb_target_group`}.
// action ∈ {`CREATED`,`UPDATED`,`DELETED`,`MOVED`,`FAILED`}.
// CHECK constraints в `nlb_outbox` (миграция 0001) защищают от typo.
//
// payload — произвольная map (snapshot resource'а после DML). nil → пустой
// JSON-объект. JSON-encoding делается helper'ом, caller передаёт map.
type OutboxEmitter interface {
	Emit(ctx context.Context, resourceType, resourceID, projectID, action string, payload map[string]any) error
}

// Outbox resource_type values (parity с CHECK в миграции 0001).
//
// Живут в neutral leaf-пакете (не в pgx-backed adapter `pg`), чтобы use-case-слой
// мог ссылаться на них без транзитивной зависимости от concrete DB-adapter —
// dependency-rule Clean Architecture (service/ импортирует только domain + порты).
const (
	OutboxResourceLoadBalancer = "nlb_load_balancer"
	OutboxResourceListener     = "nlb_listener"
	OutboxResourceTargetGroup  = "nlb_target_group"
)

// Outbox action values.
const (
	OutboxActionCreated = "CREATED"
	OutboxActionUpdated = "UPDATED"
	OutboxActionDeleted = "DELETED"
	OutboxActionMoved   = "MOVED"
	OutboxActionFailed  = "FAILED"
)
