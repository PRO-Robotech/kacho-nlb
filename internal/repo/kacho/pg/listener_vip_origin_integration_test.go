// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package pg_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/H-BF/corlib/pkg/option"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/jobs"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// fakeVIPAddrGetter — in-memory vpc.AddressService.Get для integration-reconcile.
type fakeVIPAddrGetter struct {
	byID map[string]*vpcclient.Address
}

func (g *fakeVIPAddrGetter) Get(_ context.Context, id string) (*vpcclient.Address, error) {
	if a, ok := g.byID[id]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("%w: address %s not found", domain.ErrInvalidArg, id)
}

// insertListenerWithAddr — вставляет листенер с address_id (для reconcile-
// кандидатов) + явным vip_origin. Возвращает id.
func insertListenerWithAddr(t testing.TB, repo kacho.Repository, lbID domain.ResourceID, projectID, name string, port int32, addrID, alloc string, origin domain.VipOrigin) string {
	t.Helper()
	l := newListener(lbID, projectID, name, port)
	l.AddressID = option.MustNewOption(domain.AddressID(addrID))
	l.AllocatedAddress = domain.IPAddress(alloc)
	l.VipOrigin = origin
	var id string
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		rec, err := w.Listeners().Insert(context.Background(), l)
		require.NoError(t, err)
		id = string(rec.ID)
	})
	return id
}

// TestIntegration_VIPOrigin_RoundTrip — Insert/Get round-trip колонки vip_origin
// (auto + byo) + coalesce пустого значения к DB DEFAULT 'auto'.
func TestIntegration_VIPOrigin_RoundTrip(t *testing.T) {
	t.Parallel()
	tc := newTestCtx(t)
	lb := newLB("prj01VIPORIGIN00001", "lb-vip-origin")
	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(context.Background(), lb)
		require.NoError(t, err)
	})

	byoID := insertListenerWithAddr(t, tc.Repo, lb.ID, string(lb.ProjectID), "byo-l", 80, "addr-byo-1", "203.0.113.21", domain.VipOriginBYO)
	autoID := insertListenerWithAddr(t, tc.Repo, lb.ID, string(lb.ProjectID), "auto-l", 81, "addr-auto-1", "203.0.113.22", domain.VipOriginAuto)

	// Empty VipOrigin from a thin builder coalesces to DB DEFAULT 'auto'.
	emptyL := newListener(lb.ID, string(lb.ProjectID), "empty-l", 82)
	emptyL.AllocatedAddress = "203.0.113.23"
	emptyL.VipOrigin = "" // intentionally unset
	var emptyID string
	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		rec, err := w.Listeners().Insert(context.Background(), emptyL)
		require.NoError(t, err)
		emptyID = string(rec.ID)
	})

	rd, err := tc.Repo.Reader(context.Background())
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	gotBYO, err := rd.Listeners().Get(context.Background(), byoID)
	require.NoError(t, err)
	require.Equal(t, domain.VipOriginBYO, gotBYO.VipOrigin)
	gotAuto, err := rd.Listeners().Get(context.Background(), autoID)
	require.NoError(t, err)
	require.Equal(t, domain.VipOriginAuto, gotAuto.VipOrigin)
	gotEmpty, err := rd.Listeners().Get(context.Background(), emptyID)
	require.NoError(t, err)
	require.Equal(t, domain.VipOriginAuto, gotEmpty.VipOrigin, "empty vip_origin must default to 'auto'")
}

// TestIntegration_VIPOrigin_CheckConstraint — CHECK (vip_origin IN
// ('byo','auto')) отвергает иное значение (SQLSTATE 23514).
func TestIntegration_VIPOrigin_CheckConstraint(t *testing.T) {
	t.Parallel()
	tc := newTestCtx(t)
	lb := newLB("prj01VIPORIGIN00002", "lb-vip-check")
	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(context.Background(), lb)
		require.NoError(t, err)
	})
	id := insertListenerWithAddr(t, tc.Repo, lb.ID, string(lb.ProjectID), "check-l", 80, "addr-check-1", "203.0.113.31", domain.VipOriginAuto)

	_, err := tc.Pool.Exec(context.Background(),
		`UPDATE kacho_nlb.listeners SET vip_origin = 'bogus' WHERE id = $1`, id)
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected pg error, got %T", err)
	require.Equal(t, "23514", pgErr.Code, "CHECK violation must be SQLSTATE 23514")
}

// TestIntegration_VIPOrigin_Reconcile_Backfill — boot-reconcile проставляет
// реальный vip_origin pre-existing строкам (defaulted to 'auto') по имени
// Address из vpc: auto-name → 'auto', иное → 'byo'. Включает data-loss-shaped
// имя `nlb-listener-edge` (≠ auto-name конкретного листенера → 'byo').
// Проверяет идемпотентность (повторный прогон не меняет результат).
func TestIntegration_VIPOrigin_Reconcile_Backfill(t *testing.T) {
	t.Parallel()
	tc := newTestCtx(t)
	lb := newLB("prj01VIPORIGIN00003", "lb-vip-recon")
	commitWriter(t, tc.Repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(context.Background(), lb)
		require.NoError(t, err)
	})

	// Both rows start as 'auto' (как после миграции DEFAULT).
	autoID := insertListenerWithAddr(t, tc.Repo, lb.ID, string(lb.ProjectID), "recon-auto", 80, "addr-auto-r", "203.0.113.41", domain.VipOriginAuto)
	byoID := insertListenerWithAddr(t, tc.Repo, lb.ID, string(lb.ProjectID), "recon-byo", 81, "addr-byo-r", "203.0.113.42", domain.VipOriginAuto)

	getter := &fakeVIPAddrGetter{byID: map[string]*vpcclient.Address{
		// auto-alloc Address: имя == детерминированное auto-name ЭТОГО листенера.
		"addr-auto-r": {ID: "addr-auto-r", Name: domain.ListenerAutoAddressName(domain.ResourceID(autoID))},
		// BYO Address, намеренно названный как auto-паттерн, но НЕ auto-name этого
		// листенера → должен классифицироваться как 'byo' (data-loss guard).
		"addr-byo-r": {ID: "addr-byo-r", Name: "nlb-listener-edge"},
	}}

	rec := jobs.NewVIPOriginReconciler(jobs.NewPgVIPOriginStore(tc.Pool), getter, slog.Default())
	require.NoError(t, rec.Reconcile(context.Background()))

	assertOrigin := func(id string, want domain.VipOrigin) {
		t.Helper()
		rd, err := tc.Repo.Reader(context.Background())
		require.NoError(t, err)
		defer func() { _ = rd.Close() }()
		got, err := rd.Listeners().Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, want, got.VipOrigin)
	}
	assertOrigin(autoID, domain.VipOriginAuto)
	assertOrigin(byoID, domain.VipOriginBYO)

	// Идемпотентность: повторный прогон даёт тот же результат.
	require.NoError(t, rec.Reconcile(context.Background()))
	assertOrigin(autoID, domain.VipOriginAuto)
	assertOrigin(byoID, domain.VipOriginBYO)
}
