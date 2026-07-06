// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// seedLB вставляет LB-строку (через CQRS writer) и возвращает её id —
// announce-state навешивается на существующий LB (FK).
func seedLB(t *testing.T, repo kacho.Repository, projectID, name string) string {
	t.Helper()
	lb := newLB(projectID, name)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(context.Background(), lb)
		require.NoError(t, err)
	})
	return string(lb.ID)
}

func TestAnnounceStore_UpsertAndRead(t *testing.T) {
	tc := newTestCtx(t)
	store := kachopg.NewAnnounceStore(tc.Pool)
	ctx := context.Background()

	lbID := seedLB(t, tc.Repo, "prj-A", "lb-announce")

	// Первый репорт: zone-a/v4 + zone-a/v6 (одна зона, два семейства).
	zones := []domain.AnnounceZone{
		{ZoneID: "zone-a", IPVersion: domain.IPVersionV4, BGPSessionUp: true, RouteID: "rt-1", VrfID: "vrf-1", KernelProgrammed: true, InfraID: 11},
		{ZoneID: "zone-a", IPVersion: domain.IPVersionV6, BGPSessionUp: false, RouteID: "rt-2", VrfID: "vrf-2", KernelProgrammed: false, InfraID: 22},
	}
	require.NoError(t, store.ReportZones(ctx, lbID, zones))

	rec, found, err := store.LoadState(ctx, lbID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, lbID, rec.LoadBalancerID)
	require.Len(t, rec.Zones, 2, "both families of zone-a persisted (ip_version in PK)")
	require.False(t, rec.ObservedAt.IsZero())
}

func TestAnnounceStore_UpsertIdempotent(t *testing.T) {
	tc := newTestCtx(t)
	store := kachopg.NewAnnounceStore(tc.Pool)
	ctx := context.Background()
	lbID := seedLB(t, tc.Repo, "prj-A", "lb-idem")

	z := domain.AnnounceZone{ZoneID: "zone-a", IPVersion: domain.IPVersionV4, BGPSessionUp: false, RouteID: "rt-1", InfraID: 1}
	require.NoError(t, store.ReportZones(ctx, lbID, []domain.AnnounceZone{z}))

	// Повторный upsert той же (lb, zone, family) — не дублирует строку, обновляет.
	z.BGPSessionUp = true
	z.InfraID = 99
	require.NoError(t, store.ReportZones(ctx, lbID, []domain.AnnounceZone{z}))

	rec, found, err := store.LoadState(ctx, lbID)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, rec.Zones, 1, "upsert on (lb, zone, ip_version) — single row")
	require.True(t, rec.Zones[0].BGPSessionUp)
	require.Equal(t, int64(99), rec.Zones[0].InfraID)
}

func TestAnnounceStore_FKCascadeOnLBDelete(t *testing.T) {
	tc := newTestCtx(t)
	store := kachopg.NewAnnounceStore(tc.Pool)
	ctx := context.Background()
	lbID := seedLB(t, tc.Repo, "prj-A", "lb-cascade")

	require.NoError(t, store.ReportZones(ctx, lbID,
		[]domain.AnnounceZone{{ZoneID: "zone-a", IPVersion: domain.IPVersionV4, InfraID: 1}}))

	// Удаляем LB напрямую (announce-state не блокирует Delete — FK CASCADE).
	_, err := tc.Pool.Exec(ctx, `DELETE FROM kacho_nlb.load_balancers WHERE id=$1`, lbID)
	require.NoError(t, err)

	// announce-state снята каскадно.
	var cnt int
	require.NoError(t, tc.Pool.QueryRow(ctx,
		`SELECT count(*) FROM kacho_nlb.load_balancer_announce_state WHERE load_balancer_id=$1`, lbID).
		Scan(&cnt))
	require.Equal(t, 0, cnt, "announce-state cascaded on LB delete")

	_, found, err := store.LoadState(ctx, lbID)
	require.NoError(t, err)
	require.False(t, found, "LB gone → LoadState found=false")
}

func TestAnnounceStore_LoadStateAbsentLB(t *testing.T) {
	tc := newTestCtx(t)
	store := kachopg.NewAnnounceStore(tc.Pool)
	_, found, err := store.LoadState(context.Background(), "nlbDOESNOTEXIST00000")
	require.NoError(t, err)
	require.False(t, found)
}

// TestAnnounceStore_ReportZonesAbsentLB — репорт announce-state для
// несуществующего LB нарушает FK load_balancer_announce_state → load_balancers
// (SQLSTATE 23503) и обязан маппиться в FailedPrecondition-сентинел БЕЗ утечки
// raw pgx/SQL-текста наружу (data-integrity.md «Никогда не leak'ай pgx-текст»).
// Покрывает ранее незакрытую FK-violation → error-mapping ветку ReportZones.
func TestAnnounceStore_ReportZonesAbsentLB(t *testing.T) {
	tc := newTestCtx(t)
	store := kachopg.NewAnnounceStore(tc.Pool)

	err := store.ReportZones(context.Background(), "nlbDOESNOTEXIST00000",
		[]domain.AnnounceZone{{ZoneID: "zone-a", IPVersion: domain.IPVersionV4, InfraID: 1}})
	require.Error(t, err, "report to a nonexistent LB must fail (FK 23503)")
	require.ErrorIs(t, err, kacho.ErrFailedPrecondition, "FK-violation must map to FailedPrecondition")
	for _, leak := range []string{"SQLSTATE", "23503", "foreign key", "violates", "load_balancer_announce_state"} {
		require.NotContains(t, err.Error(), leak, "mapped error must not leak raw pgx/SQL text")
	}
}
