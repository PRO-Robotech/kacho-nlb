// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import (
	"context"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// TargetGroupReaderIface — read-операции TargetGroup + Target.
type TargetGroupReaderIface interface {
	// Get возвращает TG с inline'нутыми targets (заполнено через JOIN
	// на child-таблицу).
	Get(ctx context.Context, id string) (*TargetGroupRecord, error)
	List(ctx context.Context, f TargetGroupFilter, p Pagination) ([]*TargetGroupRecord, string, error)
	ListByProject(ctx context.Context, projectID string, p Pagination) ([]*TargetGroupRecord, string, error)

	// ListTargets — получить все targets для одного TG (без pagination —
	// MaxTargetsPerGroup=100, влезает в один SQL-запрос).
	ListTargets(ctx context.Context, tgID string) ([]*TargetRecord, error)

	// ListDrainingExpired — targets, у которых status='DRAINING' и
	// drain_started_at < now - delay (для фаза B drain-runner). Skill
	// workspace CLAUDE.md «within-service refs», но это lifecycle-query, не CAS.
	// delaySeconds — TG.deregistration_delay_seconds.
	ListDrainingExpired(ctx context.Context, tgID string, delaySeconds int32) ([]*TargetRecord, error)

	// HasAttachedLB — `EXISTS` для precheck в TG.Delete (нельзя удалить TG,
	// который привязан к LB).
	HasAttachedLB(ctx context.Context, tgID string) (bool, error)
}

// TargetGroupWriterIface — write-операции + read.
type TargetGroupWriterIface interface {
	TargetGroupReaderIface

	Insert(ctx context.Context, tg *domain.TargetGroup) (*TargetGroupRecord, error)

	// Update — мутирует name/description/labels/health_check/dereg_delay/
	// slow_start. Immutable project_id/region_id обрабатывается в use-case.
	Update(ctx context.Context, tg *domain.TargetGroup) (*TargetGroupRecord, error)

	// SetStatusCAS — atomic CAS на status (ACTIVE ↔ DELETING).
	SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.TargetGroupStatus) (*TargetGroupRecord, error)

	// MoveProject — UPDATE target_groups SET project_id=$1 WHERE id=$2.
	MoveProject(ctx context.Context, id, newProjectID string) (*TargetGroupRecord, error)

	// AddTargets — INSERT targets... ON CONFLICT DO NOTHING (идемпотентный
	// re-add того же identity-tuple). Возвращает количество реально вставленных
	// строк (для outbox-action UPDATED только если >0).
	AddTargets(ctx context.Context, tgID string, targets []domain.Target) (int, error)

	// RemoveTargetsMarkDraining — фаза A двухфазного drain.
	// Для каждого target из targetIDs обновляет status='DRAINING' +
	// drain_started_at=now (CHECK drain_consistency инфорсит NULL/NOT NULL).
	// Возвращает количество фактически обновлённых строк (для outbox).
	RemoveTargetsMarkDraining(ctx context.Context, tgID string, targetIDs []string) (int, error)

	// DeleteTargetsDrained — фаза B (jobs/target_drain_runner). DELETE targets
	// WHERE status='DRAINING' AND drain_started_at < now - $delay::interval.
	// Возвращает количество удалённых строк.
	DeleteTargetsDrained(ctx context.Context, tgID string, delaySeconds int32) (int, error)

	// Delete — DELETE target_groups WHERE id=$1. FK-violation от child
	// targets/attached_target_groups → ErrFailedPrecondition.
	Delete(ctx context.Context, id string) error
}
