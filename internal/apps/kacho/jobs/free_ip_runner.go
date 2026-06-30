// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// free_ip_runner.go — background reconciler «застрявших» LoadBalancer'ов (durable
// handle). Устраняет утечку anycast-VIP при сбое create/delete-саги.
//
// Контекст. VIP консолидирован на LoadBalancer (anycast active-active): VIP
// аллоцируется per-family из vpc AnycastAddressPool — внешний side-effect,
// единственный dual-write edge create/delete LB. Если процесс падает (или
// peer-vpc недоступен) между аллокацией VIP и финализацией строки, остаётся
// «сирота»:
//
//   - create-path: durable-handle строка в status='CREATING' с уже
//     персистнутыми address_id_v4/v6 (INSERT 'CREATING' ДО alloc, отдельный
//     commit CAS-attach сразу после alloc), но без перехода в терминальный статус;
//   - delete-path: строка в status='DELETING' (release VIP упал → Delete оставил
//     её на retry).
//
// В обоих случаях VIP остаётся аллоцированным в vpc, а строка-handle висит в
// нетерминальном статусе. Reconciler периодически сканирует такие строки старше
// age-порога (свежий in-flight не трогаем — легитимный worker дорабатывает) и
// детерминированно по address_id освобождает VIP КАЖДОГО семейства РАЗДЕЛЬНО
// (vip_origin='auto' → FreeIP; 'byo' → ClearReference — чужой Address не
// удаляется), затем финализирует/удаляет handle.
//
// Идемпотентность: release client'а трактует NotFound как успех (повторный
// проход безопасен). Multi-replica-safety: claim строки — FOR UPDATE SKIP
// LOCKED, release+DELETE по строке выполняет ровно одна реплика. Узкий auto-only
// known-gap (пустой address_id из-за краха в окне «alloc-ответ ↔ persist») —
// handle удаляется без release; подробности — docs/architecture/15-free-ip-runner.md.
//
// Архитектура (как TargetDrainRunner): admin-job поверх *pgxpool.Pool, минуя
// CQRS Repository (pure SQL reconcile). Failure isolation: транзиентные ошибки
// (vpc Unavailable / SQL) логируются и НЕ завершают Run — строка остаётся и
// переедет на следующем тике. Только ctx.Done() выходит.
//
// Hard-cut / mixed-version: новый runner сканирует ТОЛЬКО load_balancers; старая
// версия (если ещё жива в кластере) сканирует listeners. Каждый Address
// освобождается ровно одним реклеймером (release идемпотентен по address_id).
// Pre-cut listener-VIP действующих листенеров этот runner НЕ трогает.
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

// freeIPOwnerKind — Reference.kind для NLB LoadBalancer в vpc.Address used_by
// («owner="nlb_load_balancer:<id>"»). Release ключуется address_id; owner —
// forward-compat для verify-CAS в vpc.
const freeIPOwnerKind = "nlb_load_balancer"

// freeIPMaxPerTick — верхняя граница строк, реконсилируемых за один тик (защита
// от unbounded-loop при большом backlog; остаток доедет на следующем тике).
const freeIPMaxPerTick = 100

// selectStuckSQL — claim одной застрявшей LB-строки старше age-порога под
// FOR UPDATE SKIP LOCKED (exactly-once между репликами). make_interval(secs=>$1)
// — age в секундах; ORDER BY updated_at — старейшие первыми (partial index
// load_balancers_reconcile_idx).
const selectStuckSQL = `
SELECT id, project_id, region_id,
       address_id_v4, address_id_v6, vip_origin_v4, vip_origin_v6, status
  FROM kacho_nlb.load_balancers
 WHERE status IN ('DELETING','CREATING')
   AND updated_at < now() - make_interval(secs => $1::double precision)
 ORDER BY updated_at ASC
 LIMIT 1
 FOR UPDATE SKIP LOCKED`

// FreeIPRunner — фоновый reconciler застрявших LoadBalancer'ов (durable handle).
type FreeIPRunner struct {
	pool         *pgxpool.Pool
	addrs        vpcclient.InternalAddressClient // release VIP (FreeIP / ClearReference)
	logger       *slog.Logger
	interval     time.Duration
	ageThreshold time.Duration
}

// NewFreeIPRunner создаёт reconciler. interval — период тиков; ageThreshold —
// минимальный возраст строки (по updated_at), чтобы не трогать свежий in-flight.
// Невалидные (<=0) значения подменяются безопасными дефолтами. addrs допускается
// nil (vpc не сконфигурирован) — тогда reconciler no-op (без release нельзя
// безопасно удалять handle, иначе утечка).
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
// после ctx.Done — штатное завершение.
func (r *FreeIPRunner) Run(ctx context.Context) error {
	r.logger.InfoContext(ctx, "free_ip_runner started",
		"interval", r.interval, "age_threshold", r.ageThreshold, "vpc_configured", r.addrs != nil)
	defer r.logger.InfoContext(ctx, "free_ip_runner stopped")

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

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
// freeIPMaxPerTick). addrs==nil → no-op. Возвращает число реконсилированных
// строк; первая транзиентная ошибка прерывает тик.
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

// stuckLB — claimed LB-handle (durable handle reconcile-вход).
type stuckLB struct {
	id          string
	projectID   string
	regionID    string
	addressIDV4 string
	addressIDV6 string
	originV4    string
	originV6    string
	status      string
}

// reconcileOne — claim+release+finalize одной строки в одной TX. FOR UPDATE SKIP
// LOCKED держит row-lock на время release (network) → ровно одна реплика
// освобождает VIP и удаляет handle. release-ошибка (vpc Unavailable) → rollback,
// строка остаётся → retry на следующем тике (идемпотентно).
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

	var lb stuckLB
	err = tx.QueryRow(ctx, selectStuckSQL, r.ageThreshold.Seconds()).Scan(
		&lb.id, &lb.projectID, &lb.regionID,
		&lb.addressIDV4, &lb.addressIDV6, &lb.originV4, &lb.originV6, &lb.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim stuck load balancer: %w", err)
	}

	// Per-family release VIP по address_id (детерминированно, раздельно v4/v6).
	// Idempotent: NotFound трактуется клиентом как успех.
	if err := r.releaseFamily(ctx, lb.id, lb.addressIDV4, lb.originV4); err != nil {
		return false, fmt.Errorf("release vip v4 %s (origin=%s): %w", lb.addressIDV4, lb.originV4, err)
	}
	if err := r.releaseFamily(ctx, lb.id, lb.addressIDV6, lb.originV6); err != nil {
		return false, fmt.Errorf("release vip v6 %s (origin=%s): %w", lb.addressIDV6, lb.originV6, err)
	}
	if lb.addressIDV4 == "" && lb.addressIDV6 == "" {
		r.logger.WarnContext(ctx,
			"free_ip_runner: stuck load balancer has no address_id — deleting handle without VIP release (auto-only residual gap)",
			"load_balancer_id", lb.id, "status", lb.status)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM kacho_nlb.load_balancers WHERE id = $1`, lb.id); err != nil {
		return false, fmt.Errorf("delete stuck load balancer %s: %w", lb.id, err)
	}

	// DELETING — finalize то, что сделал бы успешный Delete (DELETED + fga-unregister).
	// CREATING-сирота никогда не достиг терминального статуса и не анонсировался
	// (CREATED/fga-register не эмитились) → ничего не эмитим.
	if lb.status == string(domain.LBStatusDeleting) {
		if err := emitReconcileFinalize(ctx, tx, lb.id, lb.projectID); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit reconcile %s: %w", lb.id, err)
	}
	committed = true
	r.logger.InfoContext(ctx, "free_ip_runner reconciled stuck load balancer",
		"load_balancer_id", lb.id, "status", lb.status,
		"address_id_v4", lb.addressIDV4, "address_id_v6", lb.addressIDV6)
	return true, nil
}

// releaseFamily освобождает VIP одного семейства: пустой address_id → no-op;
// vip_origin='byo' → ClearReference (Address tenant'а уцелел), иначе FreeIP
// (Address удаляется). owner — forward-compat verify-CAS (release по address_id).
func (r *FreeIPRunner) releaseFamily(ctx context.Context, lbID, addressID, origin string) error {
	if addressID == "" {
		return nil
	}
	owner := vpcclient.AddressOwner{Kind: freeIPOwnerKind, ID: lbID}
	if domain.VipOrigin(origin) == domain.VipOriginBYO {
		return r.addrs.ClearReference(ctx, addressID, owner)
	}
	return r.addrs.FreeIP(ctx, addressID, owner)
}

// emitReconcileFinalize эмитит в текущей TX outbox DELETED (nlb_load_balancer) +
// fga-unregister (project-hierarchy) — то же, что финальный шаг успешного Delete.
// Все INSERT'ы — в той же TX, что и DELETE строки (атомарно).
func emitReconcileFinalize(ctx context.Context, tx pgx.Tx, lbID, projectID string) error {
	lbPayload, err := json.Marshal(map[string]any{
		"id":         lbID,
		"project_id": projectID,
		"reason":     "free_ip_runner_reconcile",
	})
	if err != nil {
		return fmt.Errorf("marshal lb DELETED payload: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO kacho_nlb.nlb_outbox (resource_type, resource_id, project_id, action, payload)
		VALUES ('nlb_load_balancer', $1, $2, 'DELETED', $3::jsonb)
	`, lbID, projectID, lbPayload); err != nil {
		return fmt.Errorf("emit lb DELETED: %w", err)
	}

	// fga-unregister project-hierarchy (как loadbalancer.Delete). source_version
	// штампуется из DB-clock внутри этой writer-TX (last-source-state-wins).
	intent := domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: lbID,
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, lbID, projectID),
		},
	}
	payload, err := intent.Marshal()
	if err != nil {
		return fmt.Errorf("marshal fga unregister intent: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO kacho_nlb.fga_register_outbox (event_type, payload, resource_kind, resource_id)
		VALUES ('fga.unregister', jsonb_set($1::jsonb, '{source_version}', to_jsonb(now())), 'NetworkLoadBalancer', $2)
	`, payload, lbID); err != nil {
		return fmt.Errorf("emit fga unregister: %w", err)
	}
	return nil
}
