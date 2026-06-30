// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package announce — gRPC handler + use-cases для InternalLoadBalancerAnnounceService
// (cluster-internal :9091, запрет #6).
//
// Наблюдаемая per-zone announce-state anycast-VIP — control-plane сторона
// feedback-петли: data plane анонсирует/отзывает VIP и репортит наблюдаемую
// announce-state обратно (ReportAnnounceState), потребители читают её через
// GetAnnounceState. Инфра-чувствительные данные (security.md): живут только за
// Internal API, на публичную проекцию NetworkLoadBalancer не выходят.
//
// AuthN+AuthZ на КАЖДОМ RPC (security.md «Internal = trusted» — запрещённое
// допущение): read viewer-gated (`v_get`), inbound write least-priv
// (`announce_writer`) — per-RPC Check энфорсится interceptor'ом, не handler'ом.
package announce

import (
	"context"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// Store — port announce-state хранилища (impl — repo/kacho/pg.AnnounceStore).
// Use-case'ы зависят только от этого порта; конкретная pgx-реализация
// инжектируется в composition root.
type Store interface {
	// ReportZones идемпотентно upsert'ит набор per-zone announce-state одного LB.
	ReportZones(ctx context.Context, lbID string, zones []domain.AnnounceZone) error
	// LoadState читает наблюдаемую announce-state одного LB (VIP + per-zone).
	// found=false → LB отсутствует (→ NotFound).
	LoadState(ctx context.Context, lbID string) (rec *kachorepo.AnnounceStateRecord, found bool, err error)
}
