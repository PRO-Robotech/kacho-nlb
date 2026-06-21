package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg/dto"
)

const targetGroupCols = `
    id, project_id, region_id, created_at, updated_at,
    name, description, labels, health_check,
    deregistration_delay_seconds, slow_start_seconds, status`

const targetCols = `
    id, target_group_id, created_at, updated_at,
    instance_id, nic_id, ip_ref_subnet_id, ip_ref_address,
    external_ip_address, external_ip_zone_id,
    weight, status, drain_started_at`

type targetGroupReader struct {
	tx pgx.Tx
}

func scanTG(row pgx.Row) (*kacho.TargetGroupRecord, error) {
	var (
		rec       kacho.TargetGroupRecord
		idStr     string
		projIDs   string
		regionIDs string
		nameStr   string
		descStr   string
		statusStr string
		labelsRaw []byte
		hcRaw     []byte
	)
	if err := row.Scan(
		&idStr, &projIDs, &regionIDs, &rec.CreatedAt, &rec.UpdatedAt,
		&nameStr, &descStr, &labelsRaw, &hcRaw,
		&rec.DeregistrationDelaySeconds, &rec.SlowStartSeconds, &statusStr,
	); err != nil {
		return nil, err
	}
	rec.ID = domain.ResourceID(idStr)
	rec.ProjectID = domain.ProjectID(projIDs)
	rec.RegionID = domain.RegionID(regionIDs)
	rec.Name = domain.LbName(nameStr)
	rec.Description = domain.LbDescription(descStr)
	rec.Status = domain.TargetGroupStatus(statusStr)
	labels, err := dto.LabelsFromJSONB(labelsRaw)
	if err != nil {
		return nil, fmt.Errorf("scan tg labels: %w", err)
	}
	rec.Labels = labels
	hc, err := dto.HealthCheckFromJSONB(hcRaw)
	if err != nil {
		return nil, fmt.Errorf("scan tg health_check: %w", err)
	}
	rec.HealthCheck = hc
	return &rec, nil
}

func scanTarget(row pgx.Row) (*kacho.TargetRecord, error) {
	var (
		rec       kacho.TargetRecord
		idStr     string
		tgIDStr   string
		instID    *string
		nicID     *string
		ipSubnet  *string
		ipAddr    *string
		extAddr   *string
		extZoneID *string
		weight    int32
		statusStr string
	)
	if err := row.Scan(
		&idStr, &tgIDStr, &rec.CreatedAt, &rec.UpdatedAt,
		&instID, &nicID, &ipSubnet, &ipAddr,
		&extAddr, &extZoneID,
		&weight, &statusStr, &rec.DrainStartedAt,
	); err != nil {
		return nil, err
	}
	rec.ID = idStr
	rec.TargetGroupID = tgIDStr
	rec.Status = statusStr
	rec.Weight = domain.LbWeight(weight)
	switch {
	case instID != nil:
		rec.InstanceID = option.MustNewOption(domain.InstanceID(*instID))
	case nicID != nil:
		rec.NicID = option.MustNewOption(domain.NicID(*nicID))
	case ipSubnet != nil && ipAddr != nil:
		rec.IPRef = &domain.TargetIPRef{
			SubnetID: domain.SubnetID(*ipSubnet),
			Address:  domain.IPAddress(*ipAddr),
		}
	case extAddr != nil:
		rec.ExternalIP = &domain.TargetExternalIP{Address: domain.IPAddress(*extAddr)}
		if extZoneID != nil && *extZoneID != "" {
			rec.ExternalIP.ZoneID = option.MustNewOption(domain.ZoneID(*extZoneID))
		}
	}
	return &rec, nil
}

func (r *targetGroupReader) Get(ctx context.Context, id string) (*kacho.TargetGroupRecord, error) {
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.target_groups WHERE id = $1`, targetGroupCols)
	row := r.tx.QueryRow(ctx, q, id)
	rec, err := scanTG(row)
	if err != nil {
		return nil, mapPgErr(err, "TargetGroup", id)
	}
	// Подгружаем targets inline (≤100 на TG, влезает в один SELECT).
	targets, err := r.ListTargets(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(targets) > 0 {
		rec.Targets = make([]domain.Target, 0, len(targets))
		for _, t := range targets {
			rec.Targets = append(rec.Targets, t.Target)
		}
	}
	return rec, nil
}

func (r *targetGroupReader) List(ctx context.Context, f kacho.TargetGroupFilter, p kacho.Pagination) ([]*kacho.TargetGroupRecord, string, error) {
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
	// RBAC sub-phase D §11: per-object FGA filter push-down (см. load_balancer_repo.go).
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
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.target_groups %s ORDER BY created_at ASC, id ASC LIMIT $%d`,
		targetGroupCols, where, argIdx)
	args = append(args, pageSize+1)
	rows, err := r.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, "", mapPgErr(err, "TargetGroup", "")
	}
	defer rows.Close()
	var result []*kacho.TargetGroupRecord
	for rows.Next() {
		rec, err := scanTG(rows)
		if err != nil {
			return nil, "", mapPgErr(err, "TargetGroup", "")
		}
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapPgErr(err, "TargetGroup", "")
	}
	nextToken := ""
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, string(last.ID))
		result = result[:pageSize]
	}
	return result, nextToken, nil
}

func (r *targetGroupReader) ListByProject(ctx context.Context, projectID string, p kacho.Pagination) ([]*kacho.TargetGroupRecord, string, error) {
	return r.List(ctx, kacho.TargetGroupFilter{ProjectID: projectID}, p)
}

func (r *targetGroupReader) ListTargets(ctx context.Context, tgID string) ([]*kacho.TargetRecord, error) {
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.targets WHERE target_group_id = $1 ORDER BY created_at ASC, id ASC`,
		targetCols)
	rows, err := r.tx.Query(ctx, q, tgID)
	if err != nil {
		return nil, mapPgErr(err, "Target", "")
	}
	defer rows.Close()
	var out []*kacho.TargetRecord
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, mapPgErr(err, "Target", "")
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgErr(err, "Target", "")
	}
	return out, nil
}

func (r *targetGroupReader) ListDrainingExpired(ctx context.Context, tgID string, delaySeconds int32) ([]*kacho.TargetRecord, error) {
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.targets
        WHERE target_group_id = $1
          AND status = 'DRAINING'
          AND drain_started_at IS NOT NULL
          AND drain_started_at < now() - make_interval(secs => $2)`, targetCols)
	rows, err := r.tx.Query(ctx, q, tgID, delaySeconds)
	if err != nil {
		return nil, mapPgErr(err, "Target", "")
	}
	defer rows.Close()
	var out []*kacho.TargetRecord
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, mapPgErr(err, "Target", "")
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgErr(err, "Target", "")
	}
	return out, nil
}

func (r *targetGroupReader) HasAttachedLB(ctx context.Context, tgID string) (bool, error) {
	var exists bool
	err := r.tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM kacho_nlb.attached_target_groups WHERE target_group_id = $1)`,
		tgID,
	).Scan(&exists)
	if err != nil {
		return false, mapPgErr(err, "TargetGroup", tgID)
	}
	return exists, nil
}

type targetGroupWriter struct {
	targetGroupReader
}

func (w *targetGroupWriter) Insert(ctx context.Context, tg *domain.TargetGroup) (*kacho.TargetGroupRecord, error) {
	labelsJSON, err := dto.LabelsToJSONB(tg.Labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	hcJSON, err := dto.HealthCheckToJSONB(tg.HealthCheck)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	q := fmt.Sprintf(`
        INSERT INTO kacho_nlb.target_groups
            (id, project_id, region_id, name, description, labels, health_check,
             deregistration_delay_seconds, slow_start_seconds, status)
        VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9, $10)
        RETURNING %s`, targetGroupCols)
	row := w.tx.QueryRow(ctx, q,
		string(tg.ID), string(tg.ProjectID), string(tg.RegionID),
		string(tg.Name), string(tg.Description), labelsJSON, hcJSON,
		tg.DeregistrationDelaySeconds, tg.SlowStartSeconds, string(tg.Status),
	)
	rec, err := scanTG(row)
	if err != nil {
		return nil, mapPgErr(err, "TargetGroup", string(tg.ID))
	}
	// Inline insert targets если они есть.
	if len(tg.Targets) > 0 {
		if _, err := w.AddTargets(ctx, string(tg.ID), tg.Targets); err != nil {
			return nil, err
		}
		// Re-load inline-targets для completeness response'а.
		targets, err := w.ListTargets(ctx, string(tg.ID))
		if err != nil {
			return nil, err
		}
		rec.Targets = make([]domain.Target, 0, len(targets))
		for _, t := range targets {
			rec.Targets = append(rec.Targets, t.Target)
		}
	}
	return rec, nil
}

func (w *targetGroupWriter) Update(ctx context.Context, tg *domain.TargetGroup) (*kacho.TargetGroupRecord, error) {
	labelsJSON, err := dto.LabelsToJSONB(tg.Labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	hcJSON, err := dto.HealthCheckToJSONB(tg.HealthCheck)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.target_groups
           SET name = $2,
               description = $3,
               labels = $4::jsonb,
               health_check = $5::jsonb,
               deregistration_delay_seconds = $6,
               slow_start_seconds = $7,
               updated_at = now()
         WHERE id = $1
        RETURNING %s`, targetGroupCols)
	row := w.tx.QueryRow(ctx, q,
		string(tg.ID),
		string(tg.Name), string(tg.Description), labelsJSON, hcJSON,
		tg.DeregistrationDelaySeconds, tg.SlowStartSeconds,
	)
	rec, err := scanTG(row)
	if err != nil {
		return nil, mapPgErr(err, "TargetGroup", string(tg.ID))
	}
	return rec, nil
}

func (w *targetGroupWriter) SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.TargetGroupStatus) (*kacho.TargetGroupRecord, error) {
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.target_groups
           SET status = $3, updated_at = now()
         WHERE id = $1 AND status = $2
        RETURNING %s`, targetGroupCols)
	row := w.tx.QueryRow(ctx, q, id, string(expected), string(newStatus))
	rec, err := scanTG(row)
	if err != nil {
		if pgxIsNoRows(err) {
			return nil, fmt.Errorf("%w: TargetGroup %s status is not %s", kacho.ErrFailedPrecondition, id, expected)
		}
		return nil, mapPgErr(err, "TargetGroup", id)
	}
	return rec, nil
}

func (w *targetGroupWriter) MoveProject(ctx context.Context, id, newProjectID string) (*kacho.TargetGroupRecord, error) {
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.target_groups
           SET project_id = $2, updated_at = now()
         WHERE id = $1
        RETURNING %s`, targetGroupCols)
	row := w.tx.QueryRow(ctx, q, id, newProjectID)
	rec, err := scanTG(row)
	if err != nil {
		return nil, mapPgErr(err, "TargetGroup", id)
	}
	return rec, nil
}

// AddTargets — INSERT ON CONFLICT DO NOTHING per-identity-type partial UNIQUE.
// Возвращает количество фактически вставленных строк.
//
// Skill workspace CLAUDE.md «within-service refs»: идемпотентный re-add того же
// identity-tuple обрабатывается через ON CONFLICT (на 4 partial UNIQUE индексах).
// Single-target INSERT — мы не делаем bulk INSERT с одним ON CONFLICT, потому что
// 4 partial UNIQUE индекса не покрываются одним ON CONFLICT-target — обработаем
// каждый INSERT отдельно (≤MaxTargetsPerGroup=100 за вызов, приемлемая
// per-target latency для нечастого RPC).
func (w *targetGroupWriter) AddTargets(ctx context.Context, tgID string, targets []domain.Target) (int, error) {
	inserted := 0
	for i := range targets {
		t := targets[i]
		id := newTargetID()
		instID, nicID, ipSubnet, ipAddr, extAddr, extZoneID := splitTargetIdentity(t)
		const q = `
            INSERT INTO kacho_nlb.targets
                (id, target_group_id,
                 instance_id, nic_id, ip_ref_subnet_id, ip_ref_address,
                 external_ip_address, external_ip_zone_id,
                 weight, status, drain_started_at)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'ACTIVE', NULL)
            ON CONFLICT DO NOTHING
            RETURNING id`
		var returnedID string
		err := w.tx.QueryRow(ctx, q,
			id, tgID,
			nullableStr(instID), nullableStr(nicID),
			nullableStr(ipSubnet), nullableStr(ipAddr),
			nullableStr(extAddr), nullableStr(extZoneID),
			int32(t.Weight),
		).Scan(&returnedID)
		if err != nil {
			if pgxIsNoRows(err) {
				// ON CONFLICT DO NOTHING — uniq violation погашена; не считаем.
				continue
			}
			return inserted, mapPgErr(err, "Target", "")
		}
		inserted++
	}
	return inserted, nil
}

// RemoveTargetsMarkDraining — Phase A 2-phase drain: status='DRAINING' +
// drain_started_at=now(). CHECK targets_drain_consistency требует drain_started_at
// IS NOT NULL когда status='DRAINING' (миграция 0001 GWT-DB-012).
func (w *targetGroupWriter) RemoveTargetsMarkDraining(ctx context.Context, tgID string, targetIDs []string) (int, error) {
	if len(targetIDs) == 0 {
		return 0, nil
	}
	tag, err := w.tx.Exec(ctx,
		`UPDATE kacho_nlb.targets
            SET status = 'DRAINING', drain_started_at = now(), updated_at = now()
          WHERE target_group_id = $1
            AND id = ANY($2::text[])
            AND status = 'ACTIVE'`,
		tgID, targetIDs,
	)
	if err != nil {
		return 0, mapPgErr(err, "Target", "")
	}
	return int(tag.RowsAffected()), nil
}

// DeleteTargetsDrained — Phase B: DELETE WHERE status='DRAINING' AND
// drain_started_at < now() - $delay::interval.
func (w *targetGroupWriter) DeleteTargetsDrained(ctx context.Context, tgID string, delaySeconds int32) (int, error) {
	tag, err := w.tx.Exec(ctx,
		`DELETE FROM kacho_nlb.targets
          WHERE target_group_id = $1
            AND status = 'DRAINING'
            AND drain_started_at IS NOT NULL
            AND drain_started_at < now() - make_interval(secs => $2)`,
		tgID, delaySeconds,
	)
	if err != nil {
		return 0, mapPgErr(err, "Target", "")
	}
	return int(tag.RowsAffected()), nil
}

func (w *targetGroupWriter) Delete(ctx context.Context, id string) error {
	tag, err := w.tx.Exec(ctx, `DELETE FROM kacho_nlb.target_groups WHERE id = $1`, id)
	if err != nil {
		return mapPgErr(err, "TargetGroup", id)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: TargetGroup %s not found", kacho.ErrNotFound, id)
	}
	return nil
}

// splitTargetIdentity — раскладывает domain.Target в 6 nullable-полей колонок
// (parity с CHECK targets_identity_exactly_one).
func splitTargetIdentity(t domain.Target) (instID, nicID, ipSubnet, ipAddr, extAddr, extZoneID string) {
	if v, ok := t.InstanceID.Maybe(); ok {
		instID = string(v)
	}
	if v, ok := t.NicID.Maybe(); ok {
		nicID = string(v)
	}
	if t.IPRef != nil {
		ipSubnet = string(t.IPRef.SubnetID)
		ipAddr = string(t.IPRef.Address)
	}
	if t.ExternalIP != nil {
		extAddr = string(t.ExternalIP.Address)
		if z, ok := t.ExternalIP.ZoneID.Maybe(); ok {
			extZoneID = string(z)
		}
	}
	return
}

// nullableStr — пустая строка → nil (для NULL в DB), иначе &s.
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// targetIDPrefix — 3-char prefix для target row id. Target — embedded child
// TargetGroup (не tenant-facing ресурс верхнего уровня), у него нет PrefixTarget
// в kacho-corelib/ids. Локальный prefix "tgt" парный с TargetGroup prefix "tgr".
const targetIDPrefix = "tgt"

// newTargetID — генерит stable id для target row. Используем
// kacho-corelib/ids.NewID с локальным 3-char prefix — это даёт 17-char
// crockford-base32 suffix с crypto/rand-энтропией, формат идентичен другим
// kacho-ресурсам. Stable id критичен для RemoveTargets/peer-validate.
func newTargetID() string {
	return ids.NewID(targetIDPrefix)
}
