// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// viporigin_reconcile.go — одношаговый boot-time backfill `listeners.vip_origin`.
//
// Контекст: release-ветка Listener.Delete (auto → FreeIP, byo → ClearReference)
// выбирается по колонке `vip_origin`. Колонка добавлена миграцией с DEFAULT
// 'auto', поэтому уже существующие BYO-листенеры временно несут 'auto'. Чистая
// SQL-миграция «по факту» невозможна — в `listeners` нет origin-сигнала, кроме
// address_id. Этот reconcile опрашивает vpc.AddressService.Get(address_id) и
// проставляет реальный origin: 'auto', если Address назван детерминированным
// auto-alloc-именем ИМЕННО этого листенера (`nlb-listener-<short-id>`); иначе
// 'byo'. Привязка к конкретному listener-id устойчива к чужому BYO-адресу с
// похожим именем (data-loss сценарий).
//
// Идемпотентен (повторный прогон даёт тот же результат, безопасен на нескольких
// репликах). Запускается ДО приёма трафика; vpc недоступен → возвращает ошибку,
// caller держит readiness not-ready (fail-closed), пока reconcile не завершён.
// Свежий стенд (нет строк) → no-op. Подробности — docs/architecture/14-vip-origin.md.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// VIPOriginCandidate — lean-проекция листенера для backfill: id + address_id.
type VIPOriginCandidate struct {
	ListenerID string
	AddressID  string
}

// VIPOriginStore — DB-порт reconcile: список кандидатов + idempotent set.
type VIPOriginStore interface {
	ListVIPOriginCandidates(ctx context.Context) ([]VIPOriginCandidate, error)
	SetVIPOrigin(ctx context.Context, listenerID string, origin domain.VipOrigin) error
}

// VIPOriginAddressGetter — read-порт vpc.AddressService (имя Address).
type VIPOriginAddressGetter interface {
	Get(ctx context.Context, addressID string) (*vpcclient.Address, error)
}

// VIPOriginReconciler — одношаговый idempotent backfill listeners.vip_origin.
type VIPOriginReconciler struct {
	store  VIPOriginStore
	addrs  VIPOriginAddressGetter
	logger *slog.Logger
}

// NewVIPOriginReconciler — конструктор. addrs допускается nil (vpc не
// сконфигурирован): тогда при наличии кандидатов Reconcile fail-closed.
func NewVIPOriginReconciler(store VIPOriginStore, addrs VIPOriginAddressGetter, logger *slog.Logger) *VIPOriginReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &VIPOriginReconciler{store: store, addrs: addrs, logger: logger}
}

// Reconcile проставляет vip_origin существующим строкам по реальному Address из
// vpc. Возвращает nil при успехе (включая no-op при отсутствии кандидатов);
// ошибку — если vpc недоступен (fail-closed) либо store-операция упала.
func (r *VIPOriginReconciler) Reconcile(ctx context.Context) error {
	if r.store == nil {
		return errors.New("vip_origin reconcile: store not configured")
	}
	candidates, err := r.store.ListVIPOriginCandidates(ctx)
	if err != nil {
		return fmt.Errorf("vip_origin reconcile: list candidates: %w", err)
	}
	if len(candidates) == 0 {
		// Свежий стенд / нечего бэкфиллить — no-op (readiness сразу ready).
		return nil
	}
	if r.addrs == nil {
		// Есть строки, но vpc-клиент не сконфигурирован — нельзя безопасно
		// определить origin → fail-closed (readiness держится not-ready).
		return errors.New("vip_origin reconcile: vpc address client not configured but listeners exist")
	}

	var reconciled int
	for _, c := range candidates {
		addr, gerr := r.addrs.Get(ctx, c.AddressID)
		if gerr != nil {
			if errors.Is(gerr, domain.ErrUnavailable) {
				// vpc недоступен — fail-closed, ретраим весь reconcile позже.
				return fmt.Errorf("vip_origin reconcile: vpc unavailable for address %s: %w", c.AddressID, gerr)
			}
			// Address удалён / не найден (NotFound / InvalidArg): release будет
			// idempotent в любой ветке (отсутствующий адрес → NotFound = ok).
			// Оставляем DEFAULT 'auto', пропускаем.
			r.logger.Warn("vip_origin reconcile: skip listener with unresolvable address",
				"listener_id", c.ListenerID, "address_id", c.AddressID, "err", gerr)
			continue
		}
		origin := deriveVIPOrigin(addr, c.ListenerID)
		if serr := r.store.SetVIPOrigin(ctx, c.ListenerID, origin); serr != nil {
			return fmt.Errorf("vip_origin reconcile: set %s=%s: %w", c.ListenerID, origin, serr)
		}
		reconciled++
	}
	r.logger.Info("vip_origin reconcile complete", "candidates", len(candidates), "reconciled", reconciled)
	return nil
}

// deriveVIPOrigin — auto iff Address назван детерминированным auto-alloc-именем
// ИМЕННО этого листенера; иначе byo. См. domain.ListenerAutoAddressName.
func deriveVIPOrigin(addr *vpcclient.Address, listenerID string) domain.VipOrigin {
	if addr.Name == domain.ListenerAutoAddressName(domain.ResourceID(listenerID)) {
		return domain.VipOriginAuto
	}
	return domain.VipOriginBYO
}

// PgVIPOriginStore — pgxpool-backed VIPOriginStore (boot admin-job, минует CQRS
// Repository — как TargetDrainRunner).
type PgVIPOriginStore struct {
	pool *pgxpool.Pool
}

// NewPgVIPOriginStore — конструктор production-store'а поверх pgxpool.
func NewPgVIPOriginStore(pool *pgxpool.Pool) *PgVIPOriginStore {
	return &PgVIPOriginStore{pool: pool}
}

// ListVIPOriginCandidates — все листенеры с непустым address_id (только их
// origin определим по реальному Address).
func (s *PgVIPOriginStore) ListVIPOriginCandidates(ctx context.Context) ([]VIPOriginCandidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, address_id FROM kacho_nlb.listeners WHERE address_id <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VIPOriginCandidate
	for rows.Next() {
		var c VIPOriginCandidate
		if err := rows.Scan(&c.ListenerID, &c.AddressID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetVIPOrigin — idempotent per-row UPDATE (детерминированное значение → запуск
// на нескольких репликах безопасен; CHECK отвергает иное значение).
func (s *PgVIPOriginStore) SetVIPOrigin(ctx context.Context, listenerID string, origin domain.VipOrigin) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE kacho_nlb.listeners SET vip_origin = $2, updated_at = now() WHERE id = $1`,
		listenerID, string(origin))
	return err
}
