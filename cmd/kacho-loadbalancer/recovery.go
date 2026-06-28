// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

// Durable LRO recovery wiring: доменный resolver nlb + corelib-reconciler.
//
// При крахе процесса live-worker'ы умирают, их in-flight операции остаются
// done=false навсегда (worker добирает только операции, диспетчеризованные в ЭТОМ
// процессе — клиентский poll OperationService.Get никогда не done). Reconciler
// при старте (RecoverAll — ДО приёма трафика) и периодическим sweep'ом (Run —
// backstop под супервизором) разрешает осиротевшие операции в терминал, сверяясь
// с committed-реальностью ресурса через доменный resolver. Покрывает
// backlog-overflow, исчерпание terminal-write retry, shutdown и crash mid-op.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	"github.com/PRO-Robotech/kacho-nlb/internal/operationresolver"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

const (
	// nlbReconcileOrphanGrace — orphan-кандидат должен быть старше этого окна,
	// чтобы reconciler не разрешил преждевременно ещё-живого worker'а. Должен
	// превышать максимальную ожидаемую длительность операции.
	nlbReconcileOrphanGrace = 5 * time.Minute
	// nlbReconcileInterval — каденция периодического backstop-sweep'а.
	nlbReconcileInterval = 30 * time.Second
	// nlbReconcileBatchSize — размер пачки claim'а за один sweep.
	nlbReconcileBatchSize = 100
	// nlbOperationsSchema — schema-квалификатор таблицы operations nlb (совпадает
	// с operations.NewRepo(pool, "kacho_nlb")).
	nlbOperationsSchema = "kacho_nlb"
)

// startLRORecovery конструирует доменный resolver (поверх Repository-read-портов)
// + corelib-reconciler поверх schema kacho_nlb, прогоняет startup-recovery
// (RecoverAll, ДО Serve) и возвращает reconciler — его периодический Run(ctx)
// вешается на супервизор в runServe. Ошибка startup-recovery — не фатальна
// (best-effort backstop; периодический Run добьёт позже): boot не валится из-за
// transient DB-сбоя reconciler'а.
func startLRORecovery(ctx context.Context, pool *pgxpool.Pool, repo kachorepo.Repository, rec operations.Recorder, logger *slog.Logger) *operations.Reconciler {
	readers := operationresolver.Readers{
		LoadBalancer: lbReader{repo: repo},
		Listener:     lstReader{repo: repo},
		TargetGroup:  tgReader{repo: repo},
	}
	resolver := operationresolver.New(readers, operationresolver.WithLogger(logger))
	reconciler := operations.NewReconciler(pool, resolver, operations.ReconcilerConfig{
		Schema:      nlbOperationsSchema,
		OrphanGrace: nlbReconcileOrphanGrace,
		BatchSize:   nlbReconcileBatchSize,
		Interval:    nlbReconcileInterval,
	},
		operations.WithReconcilerRecorder(rec),
		operations.WithReconcilerLogger(logger.With(slog.String("component", "lro-reconciler"))),
	)

	if err := reconciler.RecoverAll(ctx); err != nil {
		logger.Error("LRO startup-recovery failed; periodic sweep will retry", "err", err)
	} else {
		logger.Info("LRO startup-recovery complete (orphaned operations resolved)")
	}
	return reconciler
}

// lbReader / lstReader / tgReader — adapter'ы Repository → read-порты resolver'а.
// Каждый открывает read-TX, читает запись по id и конвертит её в proto через
// зарегистрированный DTO transfer. repo возвращает domain.ErrNotFound (через
// %w-wrap) для отсутствующего ресурса — resolver трактует это как absent.

type lbReader struct{ repo kachorepo.Repository }

func (r lbReader) Get(ctx context.Context, id string) (*lbv1.NetworkLoadBalancer, error) {
	rd, err := r.repo.Reader(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }()
	rec, err := rd.LoadBalancers().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var dst *lbv1.NetworkLoadBalancer
	if err := dto.Transfer(dto.FromTo(*rec, &dst)); err != nil {
		return nil, fmt.Errorf("dto.Transfer NetworkLoadBalancer: %w", err)
	}
	return dst, nil
}

type lstReader struct{ repo kachorepo.Repository }

func (r lstReader) Get(ctx context.Context, id string) (*lbv1.Listener, error) {
	rd, err := r.repo.Reader(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }()
	rec, err := rd.Listeners().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var dst *lbv1.Listener
	if err := dto.Transfer(dto.FromTo(*rec, &dst)); err != nil {
		return nil, fmt.Errorf("dto.Transfer Listener: %w", err)
	}
	return dst, nil
}

type tgReader struct{ repo kachorepo.Repository }

func (r tgReader) Get(ctx context.Context, id string) (*lbv1.TargetGroup, error) {
	rd, err := r.repo.Reader(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }()
	rec, err := rd.TargetGroups().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	var dst *lbv1.TargetGroup
	if err := dto.Transfer(dto.FromTo(*rec, &dst)); err != nil {
		return nil, fmt.Errorf("dto.Transfer TargetGroup: %w", err)
	}
	return dst, nil
}
