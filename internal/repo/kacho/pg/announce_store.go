// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// AnnounceStore — pgxpool-хранилище наблюдаемой announce-state anycast-VIP
// (Internal-проекция :9091). Standalone-store вне CQRS Reader/Writer: announce-
// state — не tenant-ресурс с outbox/LRO, а data-plane feedback, поэтому upsert
// идёт собственной короткой TX без outbox-эмита.
//
// Единственный writer — data plane (ReportAnnounceState). Запись идемпотентна
// (ON CONFLICT по (load_balancer_id, zone_id, ip_version)).
type AnnounceStore struct {
	pool *pgxpool.Pool
}

// NewAnnounceStore — конструктор. pool создаётся в composition root.
func NewAnnounceStore(pool *pgxpool.Pool) *AnnounceStore {
	return &AnnounceStore{pool: pool}
}

// ReportZones идемпотентно upsert'ит набор per-zone announce-state одного LB в
// одной TX. Пустой набор → no-op (idempotent). FK-violation (LB отсутствует) →
// ErrFailedPrecondition; остальные SQLSTATE сводятся через mapPgErr.
func (s *AnnounceStore) ReportZones(ctx context.Context, lbID string, zones []domain.AnnounceZone) error {
	if len(zones) == 0 {
		return nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return mapPgErr(err, "AnnounceState", lbID)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `
		INSERT INTO kacho_nlb.load_balancer_announce_state
			(load_balancer_id, zone_id, ip_version, bgp_session_up,
			 route_id, vrf_id, kernel_programmed, infra_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (load_balancer_id, zone_id, ip_version) DO UPDATE SET
			bgp_session_up    = EXCLUDED.bgp_session_up,
			route_id          = EXCLUDED.route_id,
			vrf_id            = EXCLUDED.vrf_id,
			kernel_programmed = EXCLUDED.kernel_programmed,
			infra_id          = EXCLUDED.infra_id,
			updated_at        = now()`
	for _, z := range zones {
		if _, err := tx.Exec(ctx, q,
			lbID, z.ZoneID, string(z.IPVersion), z.BGPSessionUp,
			z.RouteID, z.VrfID, z.KernelProgrammed, z.InfraID,
		); err != nil {
			return mapPgErr(err, "load balancer", lbID)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return mapPgErr(err, "AnnounceState", lbID)
	}
	return nil
}

// LoadState читает наблюдаемую announce-state одного LB: tenant-facing VIP (из
// load_balancers) + per-zone announce-rows. found=false → LB отсутствует (→
// NotFound в use-case). ObservedAt = max(updated_at) по зонам.
func (s *AnnounceStore) LoadState(ctx context.Context, lbID string) (*kacho.AnnounceStateRecord, bool, error) {
	rec := &kacho.AnnounceStateRecord{LoadBalancerID: lbID}

	err := s.pool.QueryRow(ctx,
		`SELECT address_v4, address_v6 FROM kacho_nlb.load_balancers WHERE id = $1`, lbID).
		Scan(&rec.AddressV4, &rec.AddressV6)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, mapPgErr(err, "load balancer", lbID)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT zone_id, ip_version, bgp_session_up, route_id, vrf_id,
		       kernel_programmed, infra_id, updated_at
		  FROM kacho_nlb.load_balancer_announce_state
		 WHERE load_balancer_id = $1
		 ORDER BY zone_id, ip_version`, lbID)
	if err != nil {
		return nil, false, mapPgErr(err, "AnnounceState", lbID)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			z   kacho.AnnounceZoneRecord
			ipv string
		)
		if err := rows.Scan(&z.ZoneID, &ipv, &z.BGPSessionUp, &z.RouteID,
			&z.VrfID, &z.KernelProgrammed, &z.InfraID, &z.UpdatedAt); err != nil {
			return nil, false, mapPgErr(err, "AnnounceState", lbID)
		}
		z.IPVersion = domain.IPVersion(ipv)
		if z.UpdatedAt.After(rec.ObservedAt) {
			rec.ObservedAt = z.UpdatedAt
		}
		rec.Zones = append(rec.Zones, z)
	}
	if err := rows.Err(); err != nil {
		return nil, false, mapPgErr(err, "AnnounceState", lbID)
	}
	return rec, true, nil
}
