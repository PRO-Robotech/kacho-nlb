// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg/dto"
)

// loadBalancerCols — column-list для SELECT (порядок матчится scanLB).
const loadBalancerCols = `
    id, project_id, region_id, created_at, updated_at,
    name, description, labels, type, status, session_affinity,
    deletion_protection, placement_type, disabled_announce_zones,
    ip_families, address_v4, address_v6, address_id_v4, address_id_v6,
    vip_origin_v4, vip_origin_v6, xmin::text`

// loadBalancerReader — Get/List поверх произвольной pgx.Tx (read-only или RW).
type loadBalancerReader struct {
	tx pgx.Tx
}

// scanLB — общий scanner для load_balancers row (в порядке loadBalancerCols).
func scanLB(row pgx.Row) (*kacho.LoadBalancerRecord, error) {
	var (
		rec           kacho.LoadBalancerRecord
		labelsRaw     []byte
		nameStr       string
		descStr       string
		typeStr       string
		statusStr     string
		affinStr      string
		regionIDs     string
		projectIDs    string
		idStr         string
		placementStr  string
		disabledZones []string
		ipFamilies    []string
		addrV4        string
		addrV6        string
		addrIDV4      string
		addrIDV6      string
		vipOriginV4   string
		vipOriginV6   string
	)
	if err := row.Scan(
		&idStr, &projectIDs, &regionIDs, &rec.CreatedAt, &rec.UpdatedAt,
		&nameStr, &descStr, &labelsRaw, &typeStr, &statusStr, &affinStr,
		&rec.DeletionProtection, &placementStr, &disabledZones,
		&ipFamilies, &addrV4, &addrV6, &addrIDV4, &addrIDV6,
		&vipOriginV4, &vipOriginV6, &rec.Xmin,
	); err != nil {
		return nil, err
	}
	rec.ID = domain.ResourceID(idStr)
	rec.ProjectID = domain.ProjectID(projectIDs)
	rec.RegionID = domain.RegionID(regionIDs)
	rec.PlacementType = domain.PlacementType(placementStr)
	rec.DisabledAnnounceZones = disabledZonesFromDB(disabledZones)
	rec.IPFamilies = ipVersionsFromStrings(ipFamilies)
	rec.AddressV4 = domain.IPAddress(addrV4)
	rec.AddressV6 = domain.IPAddress(addrV6)
	rec.AddressIDV4 = domain.AddressID(addrIDV4)
	rec.AddressIDV6 = domain.AddressID(addrIDV6)
	rec.VipOriginV4 = domain.VipOrigin(vipOriginV4)
	rec.VipOriginV6 = domain.VipOrigin(vipOriginV6)
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

// disabledZonesFromDB — text[] → []string, nil для пустого набора (паритет
// с proto-семантикой «отсутствие = поле не задано»).
func disabledZonesFromDB(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	return append([]string(nil), raw...)
}

// disabledZonesParam — []string → NOT NULL text[] (nil → пустой non-nil slice,
// иначе нарушил бы NOT NULL).
func disabledZonesParam(zones []string) []string {
	if zones == nil {
		return []string{}
	}
	return zones
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
             type, status, session_affinity, deletion_protection,
             placement_type, disabled_announce_zones, ip_families,
             address_v4, address_v6, address_id_v4, address_id_v6,
             vip_origin_v4, vip_origin_v6)
        VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13,
                $14, $15, $16, $17, $18, $19)
        RETURNING %s`, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q,
		string(lb.ID), string(lb.ProjectID), string(lb.RegionID),
		string(lb.Name), string(lb.Description), labelsJSON,
		string(lb.Type), string(lb.Status), string(lb.SessionAffinity),
		lb.DeletionProtection,
		string(lb.PlacementType), disabledZonesParam(lb.DisabledAnnounceZones), ipFamiliesParam(lb.IPFamilies),
		string(lb.AddressV4), string(lb.AddressV6), string(lb.AddressIDV4), string(lb.AddressIDV6),
		string(lb.VipOriginV4), string(lb.VipOriginV6),
	)
	rec, err := scanLB(row)
	if err != nil {
		return nil, mapPgErr(err, "NetworkLoadBalancer", string(lb.ID))
	}
	return rec, nil
}

// AttachVIP — атомарный CAS-attach anycast-VIP одного семейства к LB-строке
// (single-VIP-per-LB через кардинальность строки + CAS, не TOCTOU).
//
//	UPDATE … SET address_v4=$, address_id_v4=$, vip_origin_v4=$
//	 WHERE id=$ AND (address_v4='' OR address_v4=$new) RETURNING …
//
// 0 rows → FailedPrecondition "load balancer already has an address for this
// family" (семейство уже несёт другой адрес; повтор того же адреса — no-op,
// идемпотентность retry). per-region UNIQUE (23505) → generic FailedPrecondition
// "could not assign address to load balancer" (анти-oracle: не раскрываем чей
// именно адрес). status-aware CHECK (23514) → InvalidArgument — означает, что
// семейство не объявлено в ip_families ДО persist (sequencing-баг саги).
func (w *loadBalancerWriter) AttachVIP(
	ctx context.Context, id string, family domain.IPVersion, address, addressID string, origin domain.VipOrigin,
) (*kacho.LoadBalancerRecord, error) {
	addrCol, addrIDCol, originCol, err := vipColumnsForFamily(family)
	if err != nil {
		return nil, err
	}
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.load_balancers
           SET %[1]s = $2, %[2]s = $3, %[3]s = $4, updated_at = now()
         WHERE id = $1 AND (%[1]s = '' OR %[1]s = $2)
        RETURNING %[4]s`, addrCol, addrIDCol, originCol, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q, id, address, addressID, string(origin))
	rec, err := scanLB(row)
	if err != nil {
		if pgxIsNoRows(err) {
			return nil, fmt.Errorf("%w: load balancer already has an address for this family", kacho.ErrFailedPrecondition)
		}
		return nil, mapAttachVIPErr(err)
	}
	return rec, nil
}

// mapAttachVIPErr — SQLSTATE→sentinel для CAS-attach VIP. per-region UNIQUE
// 23505 → generic FailedPrecondition (анти-oracle); status-aware CHECK 23514 →
// InvalidArgument (sequencing: семейство не в ip_families до persist).
func mapAttachVIPErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: could not assign address to load balancer", kacho.ErrFailedPrecondition)
		case "23514":
			return fmt.Errorf("%w: load balancer violates address constraint", kacho.ErrInvalidArg)
		}
	}
	return mapPgErr(err, "NetworkLoadBalancer", "")
}

// vipColumnsForFamily → имена per-family колонок (address/address_id/vip_origin).
func vipColumnsForFamily(family domain.IPVersion) (addrCol, addrIDCol, originCol string, err error) {
	switch family {
	case domain.IPVersionV4:
		return "address_v4", "address_id_v4", "vip_origin_v4", nil
	case domain.IPVersionV6:
		return "address_v6", "address_id_v6", "vip_origin_v6", nil
	}
	return "", "", "", fmt.Errorf("%w: unsupported ip family %q", kacho.ErrInvalidArg, family)
}

// ipFamiliesParam — кодирует набор семейств в NOT NULL text[] (nil → пустой
// non-nil slice, иначе нарушил бы NOT NULL); значения — точные токены 'IPV4'/'IPV6'.
func ipFamiliesParam(fams []domain.IPVersion) []string {
	out := make([]string, len(fams))
	for i, f := range fams {
		out[i] = string(f)
	}
	return out
}

// ipVersionsFromStrings — обратное преобразование text[] → []domain.IPVersion.
func ipVersionsFromStrings(raw []string) []domain.IPVersion {
	if len(raw) == 0 {
		return nil
	}
	out := make([]domain.IPVersion, len(raw))
	for i, s := range raw {
		out[i] = domain.IPVersion(s)
	}
	return out
}

// Update — мутирует name/description/labels/session_affinity/deletion_protection/
// disabled_announce_zones. NB: type, placement_type, region_id, project_id,
// status, VIP-binding — НЕ меняются тут (immutable / managed через отдельные методы).
func (w *loadBalancerWriter) Update(ctx context.Context, lb *domain.LoadBalancer, expectedXmin string) (*kacho.LoadBalancerRecord, error) {
	labelsJSON, err := dto.LabelsToJSONB(lb.Labels)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kacho.ErrInvalidArg, err)
	}
	// OCC на read-modify-write (нет version-колонки): `WHERE xmin::text=$exp`
	// (snapshot из предшествующего Get). Конкурентный writer, закоммитивший
	// между Get и этим UPDATE, сдвинул xmin → 0 rows → FailedPrecondition (клиент
	// перечитывает и повторяет). Без него partial-mask Update перезаписывал бы
	// НЕ-masked поля stale-snapshot'ом (lost update: revert deletion_protection).
	// disabled_announce_zones (text[]) переписывается целиком одной statement'ой.
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.load_balancers
           SET name = $2,
               description = $3,
               labels = $4::jsonb,
               session_affinity = $5,
               deletion_protection = $6,
               disabled_announce_zones = $7,
               updated_at = now()
         WHERE id = $1 AND xmin::text = $8
        RETURNING %s`, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q,
		string(lb.ID),
		string(lb.Name), string(lb.Description), labelsJSON,
		string(lb.SessionAffinity), lb.DeletionProtection,
		disabledZonesParam(lb.DisabledAnnounceZones),
		expectedXmin,
	)
	rec, err := scanLB(row)
	if err != nil {
		if pgxIsNoRows(err) {
			return nil, fmt.Errorf("%w: NetworkLoadBalancer %s was modified concurrently", kacho.ErrFailedPrecondition, string(lb.ID))
		}
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

// MarkDeleting — atomic guarded transition в status=DELETING (первый шаг
// Delete-саги ДО release VIP). Guard'ы на DB-уровне:
//
//	SELECT 1 FROM load_balancers WHERE id=$1 FOR NO KEY UPDATE   -- lock-acquire
//	UPDATE ... SET status='DELETING'
//	 WHERE id=$1 AND deletion_protection=false
//	   AND NOT EXISTS(listeners) AND NOT EXISTS(attached_target_groups)
//	RETURNING ...
//
// Два стейтмента, НЕ один. Row-lock (FOR NO KEY UPDATE на LB) сериализуется с
// child-INSERT'ами (Listener.Insert / Attach держат FOR NO KEY UPDATE OF lb и
// отвергают DELETING-родителя), но одного row-lock'а НЕДОСТАТОЧНО, если guard —
// cross-table NOT EXISTS(children): single-statement UPDATE вычисляет подзапрос по
// СВОЕМУ start-снапшоту; при разблокировке row-lock'а EvalPlanQual (READ COMMITTED)
// пере-проверяет только целевую LB-строку, но НЕ пере-исполняет cross-table
// подзапрос против свежего снапшота → mark не видит только что закоммиченного
// child'а → mark и child-INSERT коммитятся ОБА (TOCTOU, реальный инцидент). Поэтому
// сначала явный lock-acquire (блокируется до commit'а любого in-flight child-INSERT
// на этой LB), затем guarded UPDATE ОТДЕЛЬНЫМ стейтментом — тот берёт свежий
// READ COMMITTED снапшот, уже видящий закоммиченного child'а → NOT EXISTS ловит его
// → 0 rows. Симметрично: если mark успел первым, его commit ставит DELETING, и
// child-INSERT ловит `status <> DELETING` через EvalPlanQual целевой строки.
// Идемпотентно (уже DELETING, unprotected, без детей → re-mark). 0 rows от UPDATE:
// disambig (защищён/есть дети → FailedPrecondition); LB нет → NotFound (lock-acquire).
func (w *loadBalancerWriter) MarkDeleting(ctx context.Context, id string) (*kacho.LoadBalancerRecord, error) {
	// Lock-acquire: берём row-lock на LB, блокируясь до commit'а любого конкурентного
	// child-INSERT'а (тот держит FOR NO KEY UPDATE OF lb). После разблокировки
	// следующий стейтмент возьмёт свежий снапшот, видящий закоммиченного child'а.
	var locked bool
	if err := w.tx.QueryRow(ctx,
		`SELECT true FROM kacho_nlb.load_balancers WHERE id = $1 FOR NO KEY UPDATE`, id,
	).Scan(&locked); err != nil {
		if pgxIsNoRows(err) {
			return nil, fmt.Errorf("%w: NetworkLoadBalancer %s not found", kacho.ErrNotFound, id)
		}
		return nil, mapPgErr(err, "NetworkLoadBalancer", id)
	}
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.load_balancers
           SET status = $2, updated_at = now()
         WHERE id = $1
           AND deletion_protection = false
           AND NOT EXISTS (SELECT 1 FROM kacho_nlb.listeners WHERE load_balancer_id = $1)
           AND NOT EXISTS (SELECT 1 FROM kacho_nlb.attached_target_groups WHERE load_balancer_id = $1)
        RETURNING %s`, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q, id, string(domain.LBStatusDeleting))
	rec, err := scanLB(row)
	if err != nil {
		if pgxIsNoRows(err) {
			return nil, w.markDeletingBlockReason(ctx, id)
		}
		return nil, mapPgErr(err, "NetworkLoadBalancer", id)
	}
	return rec, nil
}

// markDeletingBlockReason — различает причину 0-rows у MarkDeleting: LB отсутствует
// → ErrNotFound; защищён/есть дети → ErrFailedPrecondition (тексты зеркалят
// sync-precheck Delete-use-case'а). Читает под той же writer-TX (row уже
// row-locked mark-UPDATE'ом, если существует).
func (w *loadBalancerWriter) markDeletingBlockReason(ctx context.Context, id string) error {
	var protected, hasListener, hasAttached bool
	e := w.tx.QueryRow(ctx, `
        SELECT lb.deletion_protection,
               EXISTS(SELECT 1 FROM kacho_nlb.listeners WHERE load_balancer_id = lb.id),
               EXISTS(SELECT 1 FROM kacho_nlb.attached_target_groups WHERE load_balancer_id = lb.id)
          FROM kacho_nlb.load_balancers lb
         WHERE lb.id = $1`, id,
	).Scan(&protected, &hasListener, &hasAttached)
	if e != nil {
		if pgxIsNoRows(e) {
			return fmt.Errorf("%w: NetworkLoadBalancer %s not found", kacho.ErrNotFound, id)
		}
		return mapPgErr(e, "NetworkLoadBalancer", id)
	}
	switch {
	case protected:
		return fmt.Errorf("%w: NetworkLoadBalancer %s has deletion protection enabled", kacho.ErrFailedPrecondition, id)
	case hasListener:
		return fmt.Errorf("%w: NetworkLoadBalancer %s has listener(s); delete first", kacho.ErrFailedPrecondition, id)
	case hasAttached:
		return fmt.Errorf("%w: NetworkLoadBalancer %s has attached target group(s); detach first", kacho.ErrFailedPrecondition, id)
	}
	// Guards очистились между UPDATE и этим SELECT (ребёнок удалён под гонку) —
	// generic precondition-miss; повторный Delete пройдёт.
	return fmt.Errorf("%w: NetworkLoadBalancer %s could not be marked for deletion", kacho.ErrFailedPrecondition, id)
}

// MoveProject — atomic project-rewrite LB + каскад на listeners (denorm sync).
//
// Инвариант (within-service refs на DB-уровне):
// LB с приаттаченными target-group'ами двигать НЕЛЬЗЯ — иначе attached_target_groups
// свяжет LB в проекте B с TG в проекте A (cross-project attach, запрещён моделью).
// Sync-precheck HasAttachedTargetGroups в use-case'е — только UX/fast-fail; здесь
// гвоздём прибиваем инвариант атомарным `UPDATE ... WHERE NOT EXISTS(attach)`,
// который сериализуется с конкурентным Attach INSERT'ом (см. AttachedTargetGroups.
// Attach: тот re-check'ает project внутри своей writer-tx через conditional INSERT).
// 0 rows при существующем LB → приаттачен TG между sync-check и apply → FailedPrecondition.
func (w *loadBalancerWriter) MoveProject(ctx context.Context, id, newProjectID string) (*kacho.LoadBalancerRecord, error) {
	// 1. Сам LB — atomic CAS-подобный guard: двигаем только если нет attach'ей.
	q := fmt.Sprintf(`
        UPDATE kacho_nlb.load_balancers
           SET project_id = $2, updated_at = now()
         WHERE id = $1
           AND NOT EXISTS (
               SELECT 1 FROM kacho_nlb.attached_target_groups
                WHERE load_balancer_id = $1
           )
        RETURNING %s`, loadBalancerCols)
	row := w.tx.QueryRow(ctx, q, id, newProjectID)
	rec, err := scanLB(row)
	if err != nil {
		if pgxIsNoRows(err) {
			// Различаем «LB нет» (NotFound) и «есть attach'и» (FailedPrecondition).
			var exists bool
			if e := w.tx.QueryRow(ctx,
				`SELECT EXISTS(SELECT 1 FROM kacho_nlb.load_balancers WHERE id = $1)`, id,
			).Scan(&exists); e != nil {
				return nil, mapPgErr(e, "NetworkLoadBalancer", id)
			}
			if exists {
				return nil, fmt.Errorf("%w: NetworkLoadBalancer %s has attached target group(s); detach before Move",
					kacho.ErrFailedPrecondition, id)
			}
			return nil, fmt.Errorf("%w: NetworkLoadBalancer %s not found", kacho.ErrNotFound, id)
		}
		return nil, mapPgErr(err, "NetworkLoadBalancer", id)
	}
	// 2. Каскад на listeners (denorm) — только после успешного move LB.
	if _, err := w.tx.Exec(ctx,
		`UPDATE kacho_nlb.listeners SET project_id = $2, updated_at = now() WHERE load_balancer_id = $1`,
		id, newProjectID,
	); err != nil {
		return nil, mapPgErr(err, "Listener", "")
	}
	return rec, nil
}

// Delete — DELETE WHERE id (безусловный). FK violation → ErrFailedPrecondition.
// row absent → ErrNotFound. Используется для compensation-rollback (Create).
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

// DeleteIfUnprotected — atomic guarded delete: удаляет строку только если
// deletion_protection=false. Инвариант прибит на DB-уровне —
// конкурентный Update(protection=true) между sync-precheck и apply пресекается.
// 0 rows: различаем «LB нет» (NotFound) vs «защищён» (FailedPrecondition).
func (w *loadBalancerWriter) DeleteIfUnprotected(ctx context.Context, id string) error {
	tag, err := w.tx.Exec(ctx,
		`DELETE FROM kacho_nlb.load_balancers WHERE id = $1 AND deletion_protection = false`, id)
	if err != nil {
		return mapPgErr(err, "NetworkLoadBalancer", id)
	}
	if tag.RowsAffected() == 0 {
		var protected bool
		e := w.tx.QueryRow(ctx,
			`SELECT deletion_protection FROM kacho_nlb.load_balancers WHERE id = $1`, id,
		).Scan(&protected)
		if e != nil {
			if pgxIsNoRows(e) {
				return fmt.Errorf("%w: NetworkLoadBalancer %s not found", kacho.ErrNotFound, id)
			}
			return mapPgErr(e, "NetworkLoadBalancer", id)
		}
		// Row exists → guard заблокировал: защита включена.
		return fmt.Errorf("%w: NetworkLoadBalancer %s has deletion protection enabled",
			kacho.ErrFailedPrecondition, id)
	}
	return nil
}

// pgxIsNoRows — true если err оборачивает pgx.ErrNoRows. Helper для CAS-семантики
// (отличить CAS-miss от других ошибок при QueryRow + Scan).
func pgxIsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
