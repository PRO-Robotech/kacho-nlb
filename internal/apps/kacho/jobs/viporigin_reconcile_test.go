// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package jobs_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/jobs"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// fakeVIPOriginStore — in-memory store для unit-теста reconcile.
type fakeVIPOriginStore struct {
	candidates []jobs.VIPOriginCandidate
	listErr    error
	set        map[string]domain.VipOrigin
}

func newFakeVIPOriginStore(c ...jobs.VIPOriginCandidate) *fakeVIPOriginStore {
	return &fakeVIPOriginStore{candidates: c, set: map[string]domain.VipOrigin{}}
}
func (s *fakeVIPOriginStore) ListVIPOriginCandidates(context.Context) ([]jobs.VIPOriginCandidate, error) {
	return s.candidates, s.listErr
}
func (s *fakeVIPOriginStore) SetVIPOrigin(_ context.Context, id string, o domain.VipOrigin) error {
	s.set[id] = o
	return nil
}

// fakeVIPAddrGetter — in-memory vpc.AddressService.Get.
type fakeVIPAddrGetter struct {
	byID map[string]*vpcclient.Address
	err  error
}

func (g *fakeVIPAddrGetter) Get(_ context.Context, id string) (*vpcclient.Address, error) {
	if g.err != nil {
		return nil, g.err
	}
	if a, ok := g.byID[id]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("%w: address %s not found", domain.ErrInvalidArg, id)
}

func autoNamed(listenerID string) *vpcclient.Address {
	return &vpcclient.Address{
		ID:   "addr-" + listenerID,
		Name: domain.ListenerAutoAddressName(domain.ResourceID(listenerID)),
	}
}

// TestVIPOriginReconcile_NoCandidates_NoOp — свежий стенд: нет строк → no-op
// (даже без vpc-клиента), без обращений к vpc.
func TestVIPOriginReconcile_NoCandidates_NoOp(t *testing.T) {
	t.Parallel()
	store := newFakeVIPOriginStore()
	rec := jobs.NewVIPOriginReconciler(store, nil, slog.Default())
	require.NoError(t, rec.Reconcile(context.Background()))
	require.Empty(t, store.set)
}

// TestVIPOriginReconcile_AutoAndBYO_BackfillsByRealAddress — auto-alloc Address
// (имя == auto-name листенера) → 'auto'; BYO Address (любое иное имя) → 'byo'.
func TestVIPOriginReconcile_AutoAndBYO_BackfillsByRealAddress(t *testing.T) {
	t.Parallel()
	const lstA = "lstAUTO00000000001"
	const lstB = "lstBYO000000000002"
	store := newFakeVIPOriginStore(
		jobs.VIPOriginCandidate{ListenerID: lstA, AddressID: "addr-" + lstA},
		jobs.VIPOriginCandidate{ListenerID: lstB, AddressID: "addr-byo-b"},
	)
	getter := &fakeVIPAddrGetter{byID: map[string]*vpcclient.Address{
		"addr-" + lstA: autoNamed(lstA),
		"addr-byo-b":   {ID: "addr-byo-b", Name: "tenant-static-ip"},
	}}
	rec := jobs.NewVIPOriginReconciler(store, getter, slog.Default())
	require.NoError(t, rec.Reconcile(context.Background()))
	require.Equal(t, domain.VipOriginAuto, store.set[lstA])
	require.Equal(t, domain.VipOriginBYO, store.set[lstB])
}

// TestVIPOriginReconcile_BYONamedLikeAuto_StaysByo — BYO Address с именем
// `nlb-listener-edge` (auto-префикс, но НЕ auto-name данного листенера) → 'byo'.
// Exact-name binding устойчив к data-loss сценарию.
func TestVIPOriginReconcile_BYONamedLikeAuto_StaysByo(t *testing.T) {
	t.Parallel()
	const lst = "lstBYOEDGE000000001"
	store := newFakeVIPOriginStore(jobs.VIPOriginCandidate{ListenerID: lst, AddressID: "addr-edge"})
	getter := &fakeVIPAddrGetter{byID: map[string]*vpcclient.Address{
		"addr-edge": {ID: "addr-edge", Name: "nlb-listener-edge"},
	}}
	rec := jobs.NewVIPOriginReconciler(store, getter, slog.Default())
	require.NoError(t, rec.Reconcile(context.Background()))
	require.Equal(t, domain.VipOriginBYO, store.set[lst],
		"BYO address named like an auto one must NOT be classified auto")
}

// TestVIPOriginReconcile_VPCUnavailable_FailClosed — vpc недоступен → error
// (caller держит readiness not-ready, retry); origin не проставлен.
func TestVIPOriginReconcile_VPCUnavailable_FailClosed(t *testing.T) {
	t.Parallel()
	const lst = "lstUNAVAIL000000001"
	store := newFakeVIPOriginStore(jobs.VIPOriginCandidate{ListenerID: lst, AddressID: "addr-x"})
	getter := &fakeVIPAddrGetter{err: fmt.Errorf("%w: vpc down", domain.ErrUnavailable)}
	rec := jobs.NewVIPOriginReconciler(store, getter, slog.Default())
	require.Error(t, rec.Reconcile(context.Background()))
	require.Empty(t, store.set, "no origin must be set when vpc is unavailable")
}

// TestVIPOriginReconcile_AddressGone_Skips — Address удалён (NotFound) → пропуск
// (release idempotent в любой ветке), без ошибки.
func TestVIPOriginReconcile_AddressGone_Skips(t *testing.T) {
	t.Parallel()
	const lst = "lstGONE0000000001"
	store := newFakeVIPOriginStore(jobs.VIPOriginCandidate{ListenerID: lst, AddressID: "addr-missing"})
	getter := &fakeVIPAddrGetter{byID: map[string]*vpcclient.Address{}} // Get → InvalidArg not found
	rec := jobs.NewVIPOriginReconciler(store, getter, slog.Default())
	require.NoError(t, rec.Reconcile(context.Background()))
	require.Empty(t, store.set)
}

// TestVIPOriginReconcile_CandidatesButNoVPCClient_FailClosed — есть строки, но
// vpc-клиент не сконфигурирован → fail-closed (нельзя безопасно определить).
func TestVIPOriginReconcile_CandidatesButNoVPCClient_FailClosed(t *testing.T) {
	t.Parallel()
	store := newFakeVIPOriginStore(jobs.VIPOriginCandidate{ListenerID: "lstX0000000000001", AddressID: "addr-x"})
	rec := jobs.NewVIPOriginReconciler(store, nil, slog.Default())
	require.Error(t, rec.Reconcile(context.Background()))
	require.Empty(t, store.set)
}
