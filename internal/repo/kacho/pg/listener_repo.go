// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg/dto"
)

const listenerCols = `
    id, load_balancer_id, project_id, region_id, created_at, updated_at,
    name, description, labels, protocol, port, target_port, ip_version,
    address_id, allocated_address, subnet_id, proxy_protocol_v2,
    default_target_group_id, status, vip_origin, xmin::text`

type listenerReader struct {
	tx pgx.Tx
}

func scanListener(row pgx.Row) (*kacho.ListenerRecord, error) {
	var (
		rec        kacho.ListenerRecord
		labelsRaw  []byte
		idStr      string
		lbIDStr    string
		projectIDs string
		regionIDs  string
		nameStr    string
		descStr    string
		protoStr   string
		port       int32
		tgtPort    int32
		ipVerStr   string
		addrIDStr  string
		allocAddr  string
		subnetIDs  string
		dfltTGStr  string
		statusStr  string
		vipOrigin  string
	)
	if err := row.Scan(
		&idStr, &lbIDStr, &projectIDs, &regionIDs, &rec.CreatedAt, &rec.UpdatedAt,
		&nameStr, &descStr, &labelsRaw, &protoStr, &port, &tgtPort, &ipVerStr,
		&addrIDStr, &allocAddr, &subnetIDs, &rec.ProxyProtocolV2,
		&dfltTGStr, &statusStr, &vipOrigin, &rec.Xmin,
	); err != nil {
		return nil, err
	}
	rec.ID = domain.ResourceID(idStr)
	rec.LoadBalancerID = domain.ResourceID(lbIDStr)
	rec.ProjectID = domain.ProjectID(projectIDs)
	rec.RegionID = domain.RegionID(regionIDs)
	rec.Name = domain.LbName(nameStr)
	rec.Description = domain.LbDescription(descStr)
	rec.Protocol = domain.LbProto(protoStr)
	rec.Port = domain.LbPort(port)
	rec.TargetPort = domain.LbPort(tgtPort)
	rec.IPVersion = domain.IPVersion(ipVerStr)
	rec.AddressID = dto.OptFromStr[domain.AddressID](addrIDStr)
	rec.AllocatedAddress = domain.IPAddress(allocAddr)
	rec.SubnetID = dto.OptFromStr[domain.SubnetID](subnetIDs)
	rec.DefaultTargetGroupID = dto.OptFromStr[domain.ResourceID](dfltTGStr)
	rec.Status = domain.ListenerStatus(statusStr)
	rec.VipOrigin = domain.VipOrigin(vipOrigin)
	labels, err := dto.LabelsFromJSONB(labelsRaw)
	if err != nil {
		return nil, fmt.Errorf("scan listener labels: %w", err)
	}
	rec.Labels = labels
	return &rec, nil
}

func (r *listenerReader) Get(ctx context.Context, id string) (*kacho.ListenerRecord, error) {
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.listeners WHERE id = $1`, listenerCols)
	row := r.tx.QueryRow(ctx, q, id)
	rec, err := scanListener(row)
	if err != nil {
		return nil, mapPgErr(err, "Listener", id)
	}
	return rec, nil
}

func (r *listenerReader) List(ctx context.Context, f kacho.ListenerFilter, p kacho.Pagination) ([]*kacho.ListenerRecord, string, error) {
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
	if f.LoadBalancerID != "" {
		conditions = append(conditions, fmt.Sprintf("load_balancer_id = $%d", argIdx))
		args = append(args, f.LoadBalancerID)
		argIdx++
	}
	if f.Name != "" {
		conditions = append(conditions, fmt.Sprintf("name = $%d", argIdx))
		args = append(args, f.Name)
		argIdx++
	}
	// RBAC: per-object FGA filter push-down (см. load_balancer_repo.go).
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
	q := fmt.Sprintf(`SELECT %s FROM kacho_nlb.listeners %s ORDER BY created_at ASC, id ASC LIMIT $%d`,
		listenerCols, where, argIdx)
	args = append(args, pageSize+1)
	rows, err := r.tx.Query(ctx, q, args...)
	if err != nil {
		return nil, "", mapPgErr(err, "Listener", "")
	}
	defer rows.Close()
	var result []*kacho.ListenerRecord
	for rows.Next() {
		rec, err := scanListener(rows)
		if err != nil {
			return nil, "", mapPgErr(err, "Listener", "")
		}
		result = append(result, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapPgErr(err, "Listener", "")
	}
	nextToken := ""
	if int64(len(result)) > pageSize {
		last := result[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, string(last.ID))
		result = result[:pageSize]
	}
	return result, nextToken, nil
}

func (r *listenerReader) ListByLB(ctx context.Context, lbID string, p kacho.Pagination) ([]*kacho.ListenerRecord, string, error) {
	return r.List(ctx, kacho.ListenerFilter{LoadBalancerID: lbID}, p)
}

type listenerWriter struct {
	listenerReader
}

func (w *listenerWriter) Insert(ctx context.Context, l *domain.Listener) (*kacho.ListenerRecord, error) {
	labelsJSON, err := dto.LabelsToJSONB(l.Labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	// vip_origin: пустое значение от тонких builder'ов → DB DEFAULT 'auto'
	// (зеркалит NOT NULL DEFAULT 'auto' колонки); Create-флоу всегда передаёт
	// явное 'auto'/'byo'.
	vipOrigin := string(l.VipOrigin)
	if vipOrigin == "" {
		vipOrigin = string(domain.VipOriginAuto)
	}
	q := fmt.Sprintf(`
        INSERT INTO kacho_nlb.listeners
            (id, load_balancer_id, project_id, region_id, name, description, labels,
             protocol, port, target_port, ip_version,
             address_id, allocated_address, subnet_id, proxy_protocol_v2,
             default_target_group_id, status, vip_origin)
        VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
        RETURNING %s`, listenerCols)
	row := w.tx.QueryRow(ctx, q,
		string(l.ID), string(l.LoadBalancerID), string(l.ProjectID), string(l.RegionID),
		string(l.Name), string(l.Description), labelsJSON,
		string(l.Protocol), int32(l.Port), int32(l.TargetPort), string(l.IPVersion),
		dto.OptString(l.AddressID), string(l.AllocatedAddress), dto.OptString(l.SubnetID),
		l.ProxyProtocolV2, dto.OptString(l.DefaultTargetGroupID), string(l.Status), vipOrigin,
	)
	rec, err := scanListener(row)
	if err != nil {
		return nil, mapPgErr(err, "Listener", string(l.ID))
	}
	return rec, nil
}

func (w *listenerWriter) Update(ctx context.Context, l *domain.Listener, expectedXmin string) (*kacho.ListenerRecord, error) {
	labelsJSON, err := dto.LabelsToJSONB(l.Labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	// OCC на read-modify-write (`WHERE xmin::text=$exp`): concurrent-modify между
	// Get и этим UPDATE → 0 rows → FailedPrecondition (защита от lost update на
	// partial-mask Update). См. data-integrity.md OCC / LoadBalancerRecord.Xmin.
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.listeners
           SET name = $2,
               description = $3,
               labels = $4::jsonb,
               proxy_protocol_v2 = $5,
               default_target_group_id = $6,
               updated_at = now()
         WHERE id = $1 AND xmin::text = $7
        RETURNING %s`, listenerCols)
	row := w.tx.QueryRow(ctx, q,
		string(l.ID),
		string(l.Name), string(l.Description), labelsJSON,
		l.ProxyProtocolV2, dto.OptString(l.DefaultTargetGroupID),
		expectedXmin,
	)
	rec, err := scanListener(row)
	if err != nil {
		if pgxIsNoRows(err) {
			return nil, fmt.Errorf("%w: Listener %s was modified concurrently", kacho.ErrFailedPrecondition, string(l.ID))
		}
		return nil, mapPgErr(err, "Listener", string(l.ID))
	}
	return rec, nil
}

func (w *listenerWriter) SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.ListenerStatus) (*kacho.ListenerRecord, error) {
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.listeners
           SET status = $3, updated_at = now()
         WHERE id = $1 AND status = $2
        RETURNING %s`, listenerCols)
	row := w.tx.QueryRow(ctx, q, id, string(expected), string(newStatus))
	rec, err := scanListener(row)
	if err != nil {
		if pgxIsNoRows(err) {
			return nil, fmt.Errorf("%w: Listener %s status is not %s", kacho.ErrFailedPrecondition, id, expected)
		}
		return nil, mapPgErr(err, "Listener", id)
	}
	return rec, nil
}

func (w *listenerWriter) SetAllocatedAddress(ctx context.Context, id, address string) (*kacho.ListenerRecord, error) {
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.listeners
           SET allocated_address = $2, updated_at = now()
         WHERE id = $1
        RETURNING %s`, listenerCols)
	row := w.tx.QueryRow(ctx, q, id, address)
	rec, err := scanListener(row)
	if err != nil {
		return nil, mapPgErr(err, "Listener", id)
	}
	return rec, nil
}

func (w *listenerWriter) SetVIP(ctx context.Context, id, addressID, allocatedAddress string) (*kacho.ListenerRecord, error) {
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.listeners
           SET address_id = $2, allocated_address = $3, updated_at = now()
         WHERE id = $1
        RETURNING %s`, listenerCols)
	row := w.tx.QueryRow(ctx, q, id, addressID, allocatedAddress)
	rec, err := scanListener(row)
	if err != nil {
		return nil, mapPgErr(err, "Listener", id)
	}
	return rec, nil
}

func (w *listenerWriter) MoveProject(ctx context.Context, lbID, newProjectID string) (int64, error) {
	tag, err := w.tx.Exec(ctx,
		`UPDATE kacho_nlb.listeners SET project_id = $2, updated_at = now() WHERE load_balancer_id = $1`,
		lbID, newProjectID,
	)
	if err != nil {
		return 0, mapPgErr(err, "Listener", "")
	}
	return tag.RowsAffected(), nil
}

func (w *listenerWriter) Delete(ctx context.Context, id string) error {
	tag, err := w.tx.Exec(ctx, `DELETE FROM kacho_nlb.listeners WHERE id = $1`, id)
	if err != nil {
		return mapPgErr(err, "Listener", id)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: Listener %s not found", kacho.ErrNotFound, id)
	}
	return nil
}
