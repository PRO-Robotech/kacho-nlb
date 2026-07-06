// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

const attachedTGCols = `load_balancer_id, target_group_id, priority, attached_at`

type attachedTGReader struct {
	tx pgx.Tx
}

func scanAttachedTG(row pgx.Row) (*kacho.AttachedTargetGroupRecord, error) {
	var rec kacho.AttachedTargetGroupRecord
	if err := row.Scan(&rec.LoadBalancerID, &rec.TargetGroupID, &rec.Priority, &rec.AttachedAt); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (r *attachedTGReader) Get(ctx context.Context, lbID, tgID string) (*kacho.AttachedTargetGroupRecord, error) {
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.attached_target_groups
        WHERE load_balancer_id = $1 AND target_group_id = $2`, attachedTGCols)
	row := r.tx.QueryRow(ctx, q, lbID, tgID)
	rec, err := scanAttachedTG(row)
	if err != nil {
		return nil, mapPgErr(err, "AttachedTargetGroup", lbID+"/"+tgID)
	}
	return rec, nil
}

func (r *attachedTGReader) ListByLB(ctx context.Context, lbID string) ([]*kacho.AttachedTargetGroupRecord, error) {
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.attached_target_groups
        WHERE load_balancer_id = $1 ORDER BY attached_at ASC, target_group_id ASC`, attachedTGCols)
	rows, err := r.tx.Query(ctx, q, lbID)
	if err != nil {
		return nil, mapPgErr(err, "AttachedTargetGroup", "")
	}
	defer rows.Close()
	var out []*kacho.AttachedTargetGroupRecord
	for rows.Next() {
		rec, err := scanAttachedTG(rows)
		if err != nil {
			return nil, mapPgErr(err, "AttachedTargetGroup", "")
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgErr(err, "AttachedTargetGroup", "")
	}
	return out, nil
}

func (r *attachedTGReader) ListByTG(ctx context.Context, tgID string) ([]*kacho.AttachedTargetGroupRecord, error) {
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.attached_target_groups
        WHERE target_group_id = $1 ORDER BY attached_at ASC, load_balancer_id ASC`, attachedTGCols)
	rows, err := r.tx.Query(ctx, q, tgID)
	if err != nil {
		return nil, mapPgErr(err, "AttachedTargetGroup", "")
	}
	defer rows.Close()
	var out []*kacho.AttachedTargetGroupRecord
	for rows.Next() {
		rec, err := scanAttachedTG(rows)
		if err != nil {
			return nil, mapPgErr(err, "AttachedTargetGroup", "")
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgErr(err, "AttachedTargetGroup", "")
	}
	return out, nil
}

type attachedTGWriter struct {
	attachedTGReader
}

// Attach — atomic conditional INSERT (idempotent). Возвращает (record, true)
// если row реально вставлена; (existing-record, false) если pair уже был.
//
// Инвариант (within-service refs на DB-уровне):
// LB и TG обязаны быть в ОДНОМ проекте и регионе — cross-project attach запрещён
// моделью. Sync-precheck в use-case'е (lb.ProjectID==tg.ProjectID, region match) —
// только UX/fast-fail; здесь инвариант прибит атомарно: INSERT ... SELECT с JOIN,
// который re-check'ает project/region прямо на строках LB/TG в момент вставки.
// Это сериализуется с конкурентным LB/TG.MoveProject (те двигают ресурс только
// при NOT EXISTS attach) — TOCTOU-race «Move ↔ Attach» закрыт с обеих сторон.
//
// `FOR NO KEY UPDATE OF lb, tg` — locking-read source-строк LB/TG. Без него
// plain-read JOIN под READ COMMITTED видит stale (до-move) project конкурентного
// незакоммиченного Move'а и вставлял бы cross-project attach (move-first порядок).
// Locking-read вместо этого блокируется на
// move'нутой row-е; после commit'а Move PostgreSQL через EvalPlanQual пере-
// оценивает JOIN `tg.project_id = lb.project_id` на СВЕЖЕМ project → mismatch →
// строка отфильтрована → 0 rows → guard-miss (FailedPrecondition). FOR NO KEY
// UPDATE (не KEY SHARE) обязателен: он конфликтует с FOR NO KEY UPDATE, который
// Move берёт на свою row-у, — только так возникает блокировка + re-check.
//
// FK на load_balancer_id/target_group_id → SQLSTATE 23503 → ErrFailedPrecondition.
func (w *attachedTGWriter) Attach(ctx context.Context, lbID, tgID string, priority int32) (*kacho.AttachedTargetGroupRecord, bool, error) {
	q := fmt.Sprintf(`
        INSERT INTO kacho_nlb.attached_target_groups
            (load_balancer_id, target_group_id, priority)
        SELECT lb.id, tg.id, $3
          FROM kacho_nlb.load_balancers lb
          JOIN kacho_nlb.target_groups  tg
            ON tg.project_id = lb.project_id
           AND tg.region_id  = lb.region_id
         WHERE lb.id = $1 AND tg.id = $2
           FOR NO KEY UPDATE OF lb, tg
        ON CONFLICT (load_balancer_id, target_group_id) DO NOTHING
        RETURNING %s`, attachedTGCols)
	row := w.tx.QueryRow(ctx, q, lbID, tgID, priority)
	rec, err := scanAttachedTG(row)
	if err != nil {
		if pgxIsNoRows(err) {
			// 0 rows: либо pair уже был (ON CONFLICT), либо guard не прошёл
			// (LB/TG отсутствуют или project/region разошлись после sync-check).
			existing, getErr := w.Get(ctx, lbID, tgID)
			if getErr == nil {
				return existing, false, nil // idempotent re-attach
			}
			if !errors.Is(getErr, kacho.ErrNotFound) {
				return nil, false, getErr
			}
			// Pair не существует → guard-miss (mismatch/missing) → FailedPrecondition.
			return nil, false, fmt.Errorf(
				"%w: load balancer and target group must be in the same project and region",
				kacho.ErrFailedPrecondition)
		}
		return nil, false, mapPgErr(err, "AttachedTargetGroup", lbID+"/"+tgID)
	}
	return rec, true, nil
}

// Detach — DELETE WHERE pair. 0 affected → no-op (idempotent detach).
func (w *attachedTGWriter) Detach(ctx context.Context, lbID, tgID string) error {
	_, err := w.tx.Exec(ctx,
		`DELETE FROM kacho_nlb.attached_target_groups
          WHERE load_balancer_id = $1 AND target_group_id = $2`,
		lbID, tgID,
	)
	if err != nil {
		return mapPgErr(err, "AttachedTargetGroup", lbID+"/"+tgID)
	}
	return nil
}
