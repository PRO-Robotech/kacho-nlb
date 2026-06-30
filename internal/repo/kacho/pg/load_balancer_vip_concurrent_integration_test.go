// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Concurrent integration-тесты single-VIP-per-LB инвариантов CAS-attach под
// реальной конкуренцией goroutine'ов (testcontainers Postgres). Single-goroutine
// версии лежат в load_balancer_integration_test.go; здесь — гонки, которые
// ловятся ТОЛЬКО при настоящей конкуренции (data-integrity: «concurrent-goroutine
// integration-тест на спорный путь обязателен — race не ловится unit-тестом»):
//
//   - CAS-attach двух разных адресов на ОДНУ LB-строку одного семейства → ровно
//     один победитель (RETURNING 1 row), второй 0 rows → FailedPrecondition;
//     итоговый address — адрес победителя (single-VIP-per-LB не нарушен).
//   - per-region partial UNIQUE: двойной claim одного IP двумя РАЗНЫМИ LB в одном
//     регионе → ровно один проходит, второй 23505 → generic FailedPrecondition.
//   - region-scope того же UNIQUE: один IP в РАЗНЫХ регионах → оба проходят.
package pg_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// vipAttachCandidate — один конкурентный CAS-attach: к какой LB-строке какой адрес
// какого семейства привязывается.
type vipAttachCandidate struct {
	lbID      string
	family    domain.IPVersion
	address   string
	addressID string
}

// concurrentAttachResult — агрегат итогов гонки (mutex-guarded — пишется из
// нескольких goroutine).
type concurrentAttachResult struct {
	mu        sync.Mutex
	winners   []vipAttachCandidate // CAS вернул 1 row И writer закоммитился
	conflicts []error              // CAS отверг кандидата (ожидаем FailedPrecondition)
	otherErr  []error              // неожиданное (writer-open / commit-time) — должен быть пуст
}

// runConcurrentAttachVIP запускает по одной goroutine на кандидата. Каждая
// открывает СОБСТВЕННЫЙ writer-tx ДО старт-барьера (реальная конкуренция
// транзакций на DB-уровне, не последовательность), затем по сигналу барьера
// делает CAS-attach: победитель коммитит, проигравший фиксирует ошибку и
// откатывается. ready-барьер гарантирует, что все TX открыты прежде чем хоть один
// тронет строку — иначе гонка вырождается в последовательность.
func runConcurrentAttachVIP(t *testing.T, repo kacho.Repository, cands []vipAttachCandidate) *concurrentAttachResult {
	t.Helper()
	ctx := context.Background()
	res := &concurrentAttachResult{}

	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(len(cands))
	done.Add(len(cands))

	for _, c := range cands {
		c := c
		go func() {
			defer done.Done()

			w, err := repo.Writer(ctx)
			if err != nil {
				ready.Done()
				res.mu.Lock()
				res.otherErr = append(res.otherErr, err)
				res.mu.Unlock()
				return
			}
			committed := false
			defer func() {
				if !committed {
					w.Abort()
				}
			}()

			ready.Done()
			<-start // старт-барьер: все writer-TX уже открыты (BeginTx eager)

			if _, err := w.LoadBalancers().AttachVIP(ctx, c.lbID, c.family, c.address, c.addressID, domain.VipOriginAuto); err != nil {
				res.mu.Lock()
				res.conflicts = append(res.conflicts, err)
				res.mu.Unlock()
				return
			}
			if err := w.Commit(); err != nil {
				res.mu.Lock()
				res.otherErr = append(res.otherErr, err)
				res.mu.Unlock()
				return
			}
			committed = true
			res.mu.Lock()
			res.winners = append(res.winners, c)
			res.mu.Unlock()
		}()
	}

	ready.Wait()
	close(start)
	done.Wait()
	return res
}

// lbAddressV4 — читает текущий address_v4 LB-строки через committed-snapshot Reader.
func lbAddressV4(t *testing.T, repo kacho.Repository, lbID string) string {
	t.Helper()
	rd, err := repo.Reader(context.Background())
	require.NoError(t, err)
	defer func() { _ = rd.Close() }()
	rec, err := rd.LoadBalancers().Get(context.Background(), lbID)
	require.NoError(t, err)
	return string(rec.AddressV4)
}

// TestLB_AttachVIP_ConcurrentSingleVIPRace — single-VIP-per-LB CAS-гонка на одной
// LB-строке: два конкурентных AttachVIP одного семейства с РАЗНЫМИ адресами →
// ровно один привязывает свой адрес (row-lock сериализует writer'ов; победитель
// RETURNING 1 row), второй после commit'а победителя видит занятое семейство →
// 0 rows → FailedPrecondition. Строка несёт ровно один адрес — адрес победителя.
// Если оба проходят — CAS не атомарен (TOCTOU); это баг прода, а не тест.
func TestLB_AttachVIP_ConcurrentSingleVIPRace(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	lb := newInternalHandle("prj01VIPRACE00000001", "vip-race", domain.IPVersionV4)
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, lb)
		require.NoError(t, err)
	})

	res := runConcurrentAttachVIP(t, repo, []vipAttachCandidate{
		{lbID: string(lb.ID), family: domain.IPVersionV4, address: "100.64.10.1", addressID: "adr000000000000RACEA"},
		{lbID: string(lb.ID), family: domain.IPVersionV4, address: "100.64.10.2", addressID: "adr000000000000RACEB"},
	})

	require.Empty(t, res.otherErr, "no unexpected writer/commit errors: %v", res.otherErr)
	require.Len(t, res.winners, 1, "exactly one CAS attaches the VIP (single-VIP-per-LB atomic on DB)")
	require.Len(t, res.conflicts, 1, "the loser gets exactly one rejection")
	require.ErrorIs(t, res.conflicts[0], kacho.ErrFailedPrecondition)
	assert.Contains(t, res.conflicts[0].Error(), "load balancer already has an address for this family")

	// Итоговый address_v4 = адрес победителя; ровно один VIP на строке.
	assert.Equal(t, res.winners[0].address, lbAddressV4(t, repo, string(lb.ID)),
		"final VIP equals the winner's address")
}

// TestLB_AttachVIP_ConcurrentPerRegionDoubleClaim — per-region partial UNIQUE
// `(region_id, address_v4) WHERE address_v4<>”`: два РАЗНЫХ LB в ОДНОМ регионе
// конкурентно привязывают ОДИН И ТОТ ЖЕ address_v4 → ровно один проходит, второй
// после commit'а первого ловит 23505 → generic FailedPrecondition (анти-oracle:
// сообщение не раскрывает, какой LB уже держит адрес). Один IP не привязан к двум
// LB в регионе.
func TestLB_AttachVIP_ConcurrentPerRegionDoubleClaim(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	a := newInternalHandle("prj01REGCLAIM0000001", "reg-claim-a", domain.IPVersionV4)
	b := newInternalHandle("prj01REGCLAIM0000001", "reg-claim-b", domain.IPVersionV4)
	a.RegionID = "region-1"
	b.RegionID = "region-1" // тот же регион — UNIQUE обязан отвергнуть второй claim
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, a)
		require.NoError(t, err)
		_, err = w.LoadBalancers().Insert(ctx, b)
		require.NoError(t, err)
	})

	const shared = "100.64.20.42"
	res := runConcurrentAttachVIP(t, repo, []vipAttachCandidate{
		{lbID: string(a.ID), family: domain.IPVersionV4, address: shared, addressID: "adr00000000000CLAIMA"},
		{lbID: string(b.ID), family: domain.IPVersionV4, address: shared, addressID: "adr00000000000CLAIMB"},
	})

	require.Empty(t, res.otherErr, "no unexpected writer/commit errors: %v", res.otherErr)
	require.Len(t, res.winners, 1, "exactly one LB claims the shared IP in the region")
	require.Len(t, res.conflicts, 1, "the double-claim is rejected once")
	require.ErrorIs(t, res.conflicts[0], kacho.ErrFailedPrecondition)
	msg := res.conflicts[0].Error()
	assert.Contains(t, msg, "could not assign anycast address to load balancer")
	// Анти-oracle: ни ёмкости пула, ни чужого ownership/LB в тексте.
	for _, leak := range []string{"exhausted", "capacity", "owned", "owner", string(a.ID), string(b.ID)} {
		assert.NotContains(t, msg, leak, "generic conflict must not leak %q", leak)
	}

	// Ровно одна из двух строк несёт shared-IP; проигравшая осталась пустой (abort).
	addrA := lbAddressV4(t, repo, string(a.ID))
	addrB := lbAddressV4(t, repo, string(b.ID))
	withShared := 0
	for _, got := range []string{addrA, addrB} {
		if got == shared {
			withShared++
		} else {
			assert.Equal(t, "", got, "loser row keeps empty address_v4 after abort")
		}
	}
	assert.Equal(t, 1, withShared, "one IP is bound to exactly one LB in the region")
}

// TestLB_AttachVIP_ConcurrentCrossRegionScope — region-scope того же partial
// UNIQUE: тот же address_v4 в РАЗНЫХ регионах конкурентно → ОБА проходят
// (уникальность ключуется (region_id, address_v4), не глобально). Зеркало
// double-claim теста, доказывающее, что scope = region_id.
func TestLB_AttachVIP_ConcurrentCrossRegionScope(t *testing.T) {
	repo, cleanup := newRepo(t, setupTestDB(t))
	defer cleanup()
	ctx := context.Background()

	a := newInternalHandle("prj01XREGION00000001", "xreg-a", domain.IPVersionV4)
	b := newInternalHandle("prj01XREGION00000001", "xreg-b", domain.IPVersionV4)
	a.RegionID = "region-1"
	b.RegionID = "region-2" // разные регионы — один IP допустим в обоих
	commitWriter(t, repo, func(w kacho.RepositoryWriter) {
		_, err := w.LoadBalancers().Insert(ctx, a)
		require.NoError(t, err)
		_, err = w.LoadBalancers().Insert(ctx, b)
		require.NoError(t, err)
	})

	const shared = "100.64.30.7"
	res := runConcurrentAttachVIP(t, repo, []vipAttachCandidate{
		{lbID: string(a.ID), family: domain.IPVersionV4, address: shared, addressID: "adr0000000000XREGA01"},
		{lbID: string(b.ID), family: domain.IPVersionV4, address: shared, addressID: "adr0000000000XREGB01"},
	})

	require.Empty(t, res.otherErr, "no unexpected writer/commit errors: %v", res.otherErr)
	require.Lenf(t, res.conflicts, 0, "cross-region same IP must not conflict: %v", conflictMsgs(res.conflicts))
	require.Len(t, res.winners, 2, "both LB claim the same IP in different regions")

	assert.Equal(t, shared, lbAddressV4(t, repo, string(a.ID)))
	assert.Equal(t, shared, lbAddressV4(t, repo, string(b.ID)))
}

// conflictMsgs — сообщения ошибок для читаемого assert-output.
func conflictMsgs(errs []error) string {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return strings.Join(msgs, "; ")
}
