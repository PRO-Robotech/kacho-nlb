// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package announce

import (
	"context"
	"log/slog"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"
)

// Handler реализует lbv1.InternalLoadBalancerAnnounceServiceServer — тонкий
// transport, делегирует в use-case'ы. Регистрируется ТОЛЬКО на internal-сервере
// (:9091): инфра-чувствительная announce-state не выходит на external endpoint.
type Handler struct {
	lbv1.UnimplementedInternalLoadBalancerAnnounceServiceServer

	get    *GetAnnounceStateUseCase
	report *ReportAnnounceStateUseCase
	log    *slog.Logger
}

// NewHandler собирает Handler. store создаётся в composition root
// (kachopg.NewAnnounceStore(pool)); logger nil → slog.Default.
func NewHandler(store Store, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		get:    NewGetAnnounceStateUseCase(store),
		report: NewReportAnnounceStateUseCase(store),
		log:    logger,
	}
}

// GetAnnounceState — read наблюдаемой per-zone announce-state одного LB
// (viewer-gated per-RPC Check в interceptor'е, НЕ exempt).
func (h *Handler) GetAnnounceState(
	ctx context.Context, req *lbv1.GetLoadBalancerAnnounceStateRequest,
) (*lbv1.LoadBalancerAnnounceState, error) {
	return h.get.Execute(ctx, req)
}

// ReportAnnounceState — inbound write data-plane→nlb (least-priv writer-relation
// per-RPC Check, НЕ exempt). Идемпотентный sync-ack.
func (h *Handler) ReportAnnounceState(
	ctx context.Context, req *lbv1.ReportLoadBalancerAnnounceStateRequest,
) (*lbv1.ReportLoadBalancerAnnounceStateResponse, error) {
	return h.report.Execute(ctx, req)
}
