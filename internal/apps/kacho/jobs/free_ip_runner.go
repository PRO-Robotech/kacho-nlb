// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// free_ip_runner.go — background reconciler «застрявших» листенеров (durable
// handle). Устраняет утечку VIP при сбое create/delete-саги.
//
// Контекст. VIP-аллокация — внешний side-effect (kacho-vpc), единственный
// dual-write edge create/delete листенера. Если процесс падает (или peer-vpc
// недоступен) между аллокацией VIP и финализацией строки, остаётся «сирота»:
//
//   - create-path: durable-handle строка в status='CREATING' с уже
//     персистнутым address_id (см. listener.Create — INSERT 'CREATING' ДО alloc,
//     отдельный commit address_id сразу после alloc), но без перехода в ACTIVE;
//   - delete-path: строка в status='DELETING' (release VIP упал → Delete оставил
//     её на retry).
//
// В обоих случаях VIP остаётся аллоцированным в vpc, а строка-handle висит в
// нетерминальном статусе. Reconciler периодически сканирует такие строки старше
// age-порога (свежий in-flight не трогаем — легитимный worker дорабатывает) и
// детерминированно по address_id освобождает VIP (vip_origin='auto' → FreeIP;
// 'byo' → ClearReference — чужой статический Address не удаляется), затем
// финализирует/удаляет handle.
//
// Идемпотентность: release client'а трактует NotFound как успех (повторный
// проход безопасен). Multi-replica-safety: claim строки — FOR UPDATE SKIP
// LOCKED, release+DELETE по строке выполняет ровно одна реплика (вторая
// пропускает залоченную строку). Узкий auto-only known-gap (пустой address_id
// из-за краха в окне «alloc-ответ ↔ persist address_id») — handle удаляется без
// release; подробности — docs/architecture/15-free-ip-runner.md.
//
// Архитектура (как TargetDrainRunner): admin-job поверх *pgxpool.Pool, минуя
// CQRS Repository (pure SQL reconcile, не use-case). Failure isolation:
// транзиентные ошибки (vpc Unavailable / SQL) логируются и НЕ завершают Run —
// строка остаётся и переехзамится на следующем тике. Только ctx.Done() выходит.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// freeIPOwnerKind — Reference.kind для NLB Listener в vpc.Address used_by
// («owner="nlb_listener:<id>"»). Совпадает с listener.addressOwnerKindNLBListener
// (release ключуется address_id; owner — forward-compat для verify-CAS в vpc).
const freeIPOwnerKind = "nlb_listener"

// freeIPMaxPerTick — верхняя граница строк, реконсилируемых за один тик (защита
// от unbounded-loop при большом backlog; остаток доедет на следующем тике).
const freeIPMaxPerTick = 100

// selectStuckSQL — claim одной застрявшей строки старше age-порога под
// FOR UPDATE SKIP LOCKED (exactly-once между репликами). make_interval(secs=>$1)
// — age в секундах; ORDER BY updated_at — старейшие первыми (partial index
// listeners_reconcile_idx).
const selectStuckSQL = `
SELECT id, load_balancer_id, project_id, region_id, address_id, vip_origin, status
  FROM kacho_nlb.listeners
 WHERE status IN ('DELETING','CREATING')
   AND updated_at < now() - make_interval(secs => $1::double precision)
 ORDER BY updated_at ASC
 LIMIT 1
 FOR UPDATE SKIP LOCKED`

// FreeIPRunner — фоновый reconciler застрявших листенеров (durable handle).
type FreeIPRunner struct {
	pool         *pgxpool.Pool
	addrs        vpcclient.InternalAddressClient // release VIP (FreeIP / ClearReference)
	logger       *slog.Logger
	interval     time.Duration
	ageThreshold time.Duration
}

// NewFreeIPRunner создаёт reconciler. interval — период тиков; ageThreshold —
// минимальный возраст строки (по updated_at), чтобы не трогать свежий in-flight.
// Невалидные (<=0) значения подменяются безопасными дефолтами (defense-in-depth;
// основная защита — config.Validate). addrs допускается nil (vpc не
// сконфигурирован) — тогда reconciler — no-op (без release нельзя безопасно
// удалять handle, иначе утечка).
func NewFreeIPRunner(pool *pgxpool.Pool, addrs vpcclient.InternalAddressClient, logger *slog.Logger, interval, ageThreshold time.Duration) *FreeIPRunner {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if ageThreshold <= 0 {
		ageThreshold = 5 * time.Minute
	}
	return &FreeIPRunner{
		pool:         pool,
		addrs:        addrs,
		logger:       logger,
		interval:     interval,
		ageThreshold: ageThreshold,
	}
}

// Run блокирует goroutine до отмены ctx. Каждые r.interval — tick reconcile;
// транзиентные ошибки логируются и НЕ завершают Run (continue). Возврат nil
// после ctx.Done — штатное завершение (parallel-supervisor трактует как успех).
func (r *FreeIPRunner) Run(ctx context.Context) error {
	r.logger.InfoContext(ctx, "free_ip_runner started",
		"interval", r.interval, "age_threshold", r.ageThreshold, "vpc_configured", r.addrs != nil)
	defer r.logger.InfoContext(ctx, "free_ip_runner stopped")

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Первый tick — сразу (быстро убираем сирот, накопившихся пока процесс был мёртв).
	r.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick — одна итерация: reconcileOnce + лог. Транзиентные ошибки не пропускаются
// наружу (loop respects ctx cancel; transient errors do not abort).
func (r *FreeIPRunner) tick(ctx context.Context) {
	start := time.Now()
	n, err := r.reconcileOnce(ctx)
	took := time.Since(start)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		r.logger.ErrorContext(ctx, "free_ip_runner tick failed",
			"err", err, "reconciled", n, "took", took)
		return
	}
	if n > 0 {
		r.logger.InfoContext(ctx, "free_ip_runner tick",
			"reconciled", n, "took_ms", took.Milliseconds())
	}
}

// reconcileOnce реконсилит застрявшие строки до исчерпания (или до
// freeIPMaxPerTick). addrs==nil → no-op (release невозможен). Возвращает число
// реконсилированных строк; первая транзиентная ошибка прерывает тик (строка
// остаётся, доедет на следующем).
func (r *FreeIPRunner) reconcileOnce(ctx context.Context) (int, error) {
	if r.addrs == nil {
		return 0, nil
	}
	reconciled := 0
	for i := 0; i < freeIPMaxPerTick; i++ {
		processed, err := r.reconcileOne(ctx)
		if err != nil {
			return reconciled, err
		}
		if !processed {
			return reconciled, nil
		}
		reconciled++
	}
	return reconciled, nil
}

// reconcileOne — claim+release+finalize одной строки в одной TX. FOR UPDATE SKIP
// LOCKED держит row-lock на время release (network) → ровно одна реплика
// освобождает VIP и удаляет handle; параллельная реплика пропускает залоченную
// строку. release-ошибка (vpc Unavailable) → rollback, строка остаётся в
// прежнем статусе → retry на следующем тике (идемпотентно).
func (r *FreeIPRunner) reconcileOne(ctx context.Context) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin reconcile tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	var lstID, lbID, projectID, regionID, addressID, vipOrigin, statusStr string
	err = tx.QueryRow(ctx, selectStuckSQL, r.ageThreshold.Seconds()).Scan(
		&lstID, &lbID, &projectID, &regionID, &addressID, &vipOrigin, &statusStr)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim stuck listener: %w", err)
	}

	// Release VIP по address_id (детерминированно, не листингом). Idempotent:
	// клиент трактует NotFound как успех. Пустой address_id (auto-only known-gap:
	// краш в окне «alloc-ответ ↔ persist») — освобождать нечем, удаляем handle.
	if addressID != "" {
		if rerr := r.release(ctx, addressID, domain.VipOrigin(vipOrigin), lstID); rerr != nil {
			return false, fmt.Errorf("release vip %s (origin=%s): %w", addressID, vipOrigin, rerr)
		}
	} else {
		r.logger.WarnContext(ctx,
			"free_ip_runner: stuck listener has empty address_id — deleting handle without VIP release (auto-only residual gap)",
			"listener_id", lstID, "status", statusStr)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM kacho_nlb.listeners WHERE id = $1`, lstID); err != nil {
		return false, fmt.Errorf("delete stuck listener %s: %w", lstID, err)
	}

	// DELETING — finalize то, что сделал бы успешный Delete (DELETED + LB UPDATED
	// + fga-unregister). CREATING-сирота никогда не достиг ACTIVE и не
	// анонсировался (CREATED/fga-register не эмитились) → ничего не эмитим.
	if statusStr == string(domain.ListenerStatusDeleting) {
		if err := emitReconcileFinalize(ctx, tx, lstID, lbID, projectID); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit reconcile %s: %w", lstID, err)
	}
	committed = true
	r.logger.InfoContext(ctx, "free_ip_runner reconciled stuck listener",
		"listener_id", lstID, "status", statusStr, "vip_origin", vipOrigin, "address_id", addressID)
	return true, nil
}

// release освобождает VIP по дискриминатору vip_origin: 'byo' → ClearReference
// (снимает referrer, Address tenant'а уцелел), иначе FreeIP (Address удаляется
// целиком). owner — forward-compat verify-CAS (release ключуется address_id).
func (r *FreeIPRunner) release(ctx context.Context, addressID string, origin domain.VipOrigin, listenerID string) error {
	owner := vpcclient.AddressOwner{Kind: freeIPOwnerKind, ID: listenerID}
	if origin == domain.VipOriginBYO {
		return r.addrs.ClearReference(ctx, addressID, owner)
	}
	return r.addrs.FreeIP(ctx, addressID, owner)
}

// emitReconcileFinalize эмитит в текущей TX outbox DELETED (nlb_listener) +
// UPDATED (nlb_load_balancer) + fga-unregister (parent-link) — то же, что
// финальный шаг успешного Delete. Все INSERT'ы — в той же TX, что и DELETE
// строки (атомарно: либо handle удалён вместе с уведомлениями, либо ничего).
func emitReconcileFinalize(ctx context.Context, tx pgx.Tx, listenerID, lbID, projectID string) error {
	lstPayload, err := json.Marshal(map[string]any{
		"id":     listenerID,
		"reason": "free_ip_runner_reconcile",
	})
	if err != nil {
		return fmt.Errorf("marshal listener DELETED payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO kacho_nlb.nlb_outbox (resource_type, resource_id, project_id, action, payload)
		VALUES ('nlb_listener', $1, $2, 'DELETED', $3::jsonb)
	`, listenerID, projectID, lstPayload); err != nil {
		return fmt.Errorf("emit listener DELETED: %w", err)
	}

	lbPayload, err := json.Marshal(map[string]any{
		"id":     lbID,
		"reason": "listener_reconciled",
	})
	if err != nil {
		return fmt.Errorf("marshal lb UPDATED payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO kacho_nlb.nlb_outbox (resource_type, resource_id, project_id, action, payload)
		VALUES ('nlb_load_balancer', $1, $2, 'UPDATED', $3::jsonb)
	`, lbID, projectID, lbPayload); err != nil {
		return fmt.Errorf("emit lb UPDATED: %w", err)
	}

	// fga-unregister parent-link (как listener.Delete). source_version
	// штампуется из DB-clock внутри этой writer-TX (last-source-state-wins).
	intent := domain.FGARegisterIntent{
		Kind:       "Listener",
		ResourceID: listenerID,
		Tuples: []domain.FGATuple{
			domain.FGAParentLinkTuple(
				domain.FGAObjectTypeLoadBalancer, lbID,
				domain.FGARelationLoadBalancer,
				domain.FGAObjectTypeListener, listenerID,
			),
		},
	}
	payload, err := intent.Marshal()
	if err != nil {
		return fmt.Errorf("marshal fga unregister intent: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO kacho_nlb.fga_register_outbox (event_type, payload, resource_kind, resource_id)
		VALUES ('fga.unregister', jsonb_set($1::jsonb, '{source_version}', to_jsonb(now())), 'Listener', $2)
	`, payload, listenerID); err != nil {
		return fmt.Errorf("emit fga unregister: %w", err)
	}
	return nil
}
