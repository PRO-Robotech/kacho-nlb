package pg

import (
	"context"
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

// Attach — INSERT с ON CONFLICT DO NOTHING (idempotent attach, acceptance
// NLB-033 / GWT-DB-011). Возвращает (record, true) если row реально вставлена;
// (existing-record, false) если pair уже был.
//
// FK на load_balancer_id или target_group_id → SQLSTATE 23503 → ErrFailedPrecondition.
func (w *attachedTGWriter) Attach(ctx context.Context, lbID, tgID string, priority int32) (*kacho.AttachedTargetGroupRecord, bool, error) {
	q := fmt.Sprintf(`
        INSERT INTO kacho_nlb.attached_target_groups
            (load_balancer_id, target_group_id, priority)
        VALUES ($1, $2, $3)
        ON CONFLICT (load_balancer_id, target_group_id) DO NOTHING
        RETURNING %s`, attachedTGCols)
	row := w.tx.QueryRow(ctx, q, lbID, tgID, priority)
	rec, err := scanAttachedTG(row)
	if err != nil {
		if pgxIsNoRows(err) {
			// ON CONFLICT DO NOTHING — pair уже существовал; вернём existing.
			existing, getErr := w.Get(ctx, lbID, tgID)
			if getErr != nil {
				return nil, false, getErr
			}
			return existing, false, nil
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
