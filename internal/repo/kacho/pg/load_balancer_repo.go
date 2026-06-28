// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg/dto"
)

// loadBalancerCols — column-list для SELECT (порядок матчится scanLB).
const loadBalancerCols = `
    id, project_id, region_id, created_at, updated_at,
    name, description, labels, type, status, session_affinity,
    cross_zone_enabled, deletion_protection`

// loadBalancerReader — Get/List поверх произвольной pgx.Tx (read-only или RW).
type loadBalancerReader struct {
	tx pgx.Tx
}

// scanLB — общий scanner для load_balancers row (в порядке loadBalancerCols).
func scanLB(row pgx.Row) (*kacho.LoadBalancerRecord, error) {
	var (
		rec        kacho.LoadBalancerRecord
		labelsRaw  []byte
		nameStr    string
		descStr    string
		typeStr    string
		statusStr  string
		affinStr   string
		regionIDs  string
		projectIDs string
		idStr      string
	)
	if err := row.Scan(
		&idStr, &projectIDs, &regionIDs, &rec.CreatedAt, &rec.UpdatedAt,
		&nameStr, &descStr, &labelsRaw, &typeStr, &statusStr, &affinStr,
		&rec.CrossZoneEnabled, &rec.DeletionProtection,
	); err != nil {
		return nil, err
	}
	rec.ID = domain.ResourceID(idStr)
	rec.ProjectID = domain.ProjectID(projectIDs)
	rec.RegionID = domain.RegionID(regionIDs)
	rec.Name = domain.LbName(nameStr)
	rec.Description = domain.LbDescription(descStr)
	rec.Type = domain.LBType(typeStr)
	rec.Status = domain.LBStatus(statusStr)
	rec.SessionAffinity = domain.SessionAffinity(affinStr)
	labels, err := dto.LabelsFromJSONB(labelsRaw)
	if err != nil {
		return nil, fmt.Errorf("scan lb labels: %w", err)
	}
	rec.Labels = labels
	return &rec, nil
}

// Get — по конвенции Kachō: well-formed-but-absent → ErrNotFound "NetworkLoadBalancer <id> not found".
func (r *loadBalancerReader) Get(ctx context.Context, id string) (*kacho.LoadBalancerRecord, error) {
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.load_balancers WHERE id = $1`, loadBalancerCols)
	row := r.tx.QueryRow(ctx, q, id)
	rec, err := scanLB(row)
	if err != nil {
		return nil, mapPgErr(err, "NetworkLoadBalancer", id)
	}
	return rec, nil
}

// List — cursor-based pagination + filter (ProjectID, Name).
func (r *loadBalancerReader) List(ctx context.Context, f kacho.LoadBalancerFilter, p kacho.Pagination) ([]*kacho.LoadBalancerRecord, string, error) {
	pageSize, err := pageSizeOrDefault(p.PageSize)
	if err != nil {
		return nil, "", err
	}

	conditions := []string{}
	args := []any{}
	argIdx := 1
	if f.ProjectID != "" {
		conditions = append(conditions, fmt.Sprintf("project_id = $%d", argIdx))
		args = append(args, f.ProjectID)
		argIdx++
	}
	if f.Name != "" {
		conditions = append(conditions, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, f.Name)
		argIdx++
	}
	// RBAC: per-object FGA filter push-down (iam ListObjects allow-set).
	// nil → no filter; len==0 → 0 rows short-circuit (no-leak); len>0 → id = ANY ДО LIMIT
	// (плотная keyset-пагинация по отфильтрованному набору).
	if f.AllowedIDs != nil {
		if len(f.AllowedIDs) == 0 {
			return nil, "", nil
		}
		conditions = append(conditions, fmt.Sprintf("id = ANY($%d::text[])", argIdx))
		args = append(args, f.AllowedIDs)
		argIdx++
	}
	if p.PageToken != "" {
		cur, err := decodePageToken(p.PageToken)
		if err != nil {
			return nil, "", err
		}
		conditions = append(conditions, fmt.Sprintf("(created_at, id) > ($%d, $%d)", argIdx, argIdx+1))
		args = append(args, cur.CreatedAt, cur.ID)
		argIdx += 2
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	q := fmt.Sprintf(
		`SELECT %s FROM kacho_nlb.load_balancers %s ORDER BY created_at ASC, id ASC LIMIT $%d`,
		loadBalancerCols, where, argIdx,
	)
	args = append(args, pageSize+1)

	rows, err := r.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, "", mapPgErr(err, "NetworkLoadBalancer", "")
	}
	defer rows.Close()
	var result []*kacho.LoadBalancerRecord
	for rows.Next() {
		rec, err := scanLB(rows)
		if err != nil {
			return nil, "", mapPgErr(err, "NetworkLoadBalancer", "")
		}
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapPgErr(err, "NetworkLoadBalancer", "")
	}
	nextToken := ""
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, string(last.ID))
		result = result[:pageSize]
	}
	return result, nextToken, nil
}

// ListByProject — wrapper над List.
func (r *loadBalancerReader) ListByProject(ctx context.Context, projectID string, p kacho.Pagination) ([]*kacho.LoadBalancerRecord, string, error) {
	return r.List(ctx, kacho.LoadBalancerFilter{ProjectID: projectID}, p)
}

// HasListeners — EXISTS query для Delete-precheck.
func (r *loadBalancerReader) HasListeners(ctx context.Context, lbID string) (bool, error) {
	var exists bool
	err := r.tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM kacho_nlb.listeners WHERE load_balancer_id = $1)`,
		lbID,
	).Scan(&exists)
	if err != nil {
		return false, mapPgErr(err, "NetworkLoadBalancer", lbID)
	}
	return exists, nil
}

// HasAttachedTargetGroups — EXISTS query.
func (r *loadBalancerReader) HasAttachedTargetGroups(ctx context.Context, lbID string) (bool, error) {
	var exists bool
	err := r.tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM kacho_nlb.attached_target_groups WHERE load_balancer_id = $1)`,
		lbID,
	).Scan(&exists)
	if err != nil {
		return false, mapPgErr(err, "NetworkLoadBalancer", lbID)
	}
	return exists, nil
}

// loadBalancerWriter — embeds reader (writer видит свои writes).
type loadBalancerWriter struct {
	loadBalancerReader
}

// Insert — INSERT load_balancers RETURNING полный row.
func (w *loadBalancerWriter) Insert(ctx context.Context, lb *domain.LoadBalancer) (*kacho.LoadBalancerRecord, error) {
	labelsJSON, err := dto.LabelsToJSONB(lb.Labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	q := fmt.Sprintf(`
        INSERT INTO kacho_nlb.load_balancers
            (id, project_id, region_id, name, description, labels,
             type, status, session_affinity, cross_zone_enabled, deletion_protection)
        VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11)
        RETURNING %s`, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q,
		string(lb.ID), string(lb.ProjectID), string(lb.RegionID),
		string(lb.Name), string(lb.Description), labelsJSON,
		string(lb.Type), string(lb.Status), string(lb.SessionAffinity),
		lb.CrossZoneEnabled, lb.DeletionProtection,
	)
	rec, err := scanLB(row)
	if err != nil {
		return nil, mapPgErr(err, "NetworkLoadBalancer", string(lb.ID))
	}
	return rec, nil
}

// Update — мутирует name/description/labels/session_affinity/cross_zone_enabled/
// deletion_protection. NB: type, region_id, project_id, status — НЕ меняются
// тут (immutable / managed через отдельные методы).
func (w *loadBalancerWriter) Update(ctx context.Context, lb *domain.LoadBalancer) (*kacho.LoadBalancerRecord, error) {
	labelsJSON, err := dto.LabelsToJSONB(lb.Labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.load_balancers
           SET name = $2,
               description = $3,
               labels = $4::jsonb,
               session_affinity = $5,
               cross_zone_enabled = $6,
               deletion_protection = $7,
               updated_at = now()
         WHERE id = $1
        RETURNING %s`, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q,
		string(lb.ID),
		string(lb.Name), string(lb.Description), labelsJSON,
		string(lb.SessionAffinity), lb.CrossZoneEnabled, lb.DeletionProtection,
	)
	rec, err := scanLB(row)
	if err != nil {
		return nil, mapPgErr(err, "NetworkLoadBalancer", string(lb.ID))
	}
	return rec, nil
}

// SetStatusCAS — atomic compare-and-swap на status. 0 affected → ErrFailedPrecondition.
func (w *loadBalancerWriter) SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.LBStatus) (*kacho.LoadBalancerRecord, error) {
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.load_balancers
           SET status = $3, updated_at = now()
         WHERE id = $1 AND status = $2
        RETURNING %s`, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q, id, string(expected), string(newStatus))
	rec, err := scanLB(row)
	if err != nil {
		// CAS-miss → pgx.ErrNoRows → mapPgErr → ErrNotFound; в CAS-семантике это
		// FailedPrecondition (либо id absent либо status != expected — оба для
		// клиента FailedPrecondition по precondition-нарушению).
		if pgxIsNoRows(err) {
			return nil, fmt.Errorf("%w: LoadBalancer %s status is not %s", kacho.ErrFailedPrecondition, id, expected)
		}
		return nil, mapPgErr(err, "NetworkLoadBalancer", id)
	}
	return rec, nil
}

// MoveProject — UPDATE load_balancers SET project_id=$1 + каскад на listeners
// (denorm sync). Возвращает обновлённый LB-record.
func (w *loadBalancerWriter) MoveProject(ctx context.Context, id, newProjectID string) (*kacho.LoadBalancerRecord, error) {
	// 1. Каскад на listeners (denorm).
	if _, err := w.tx.Exec(ctx,
		`UPDATE kacho_nlb.listeners SET project_id = $2, updated_at = now() WHERE load_balancer_id = $1`,
		id, newProjectID,
	); err != nil {
		return nil, mapPgErr(err, "Listener", "")
	}
	// 2. Сам LB.
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.load_balancers
           SET project_id = $2, updated_at = now()
         WHERE id = $1
        RETURNING %s`, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q, id, newProjectID)
	rec, err := scanLB(row)
	if err != nil {
		return nil, mapPgErr(err, "NetworkLoadBalancer", id)
	}
	return rec, nil
}

// Delete — DELETE WHERE id. FK violation → ErrFailedPrecondition.
func (w *loadBalancerWriter) Delete(ctx context.Context, id string) error {
	tag, err := w.tx.Exec(ctx, `DELETE FROM kacho_nlb.load_balancers WHERE id = $1`, id)
	if err != nil {
		return mapPgErr(err, "NetworkLoadBalancer", id)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: NetworkLoadBalancer %s not found", kacho.ErrNotFound, id)
	}
	return nil
}

// pgxIsNoRows — true если err оборачивает pgx.ErrNoRows. Helper для CAS-семантики
// (отличить CAS-miss от других ошибок при QueryRow + Scan).
func pgxIsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
