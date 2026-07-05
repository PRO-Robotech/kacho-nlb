// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"
	"time"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// GetTargetStatesUseCase — sync computed per-target health.
// Deterministic ramp без реальных
// healthcheck probes (control-plane-only фаза):
//
//   - TargetHealthInactive если LB.status == STOPPED
//   - TargetHealthDraining если target.status == DRAINING
//   - TargetHealthInitial  если age < HC.interval * HC.healthy_threshold
//   - TargetHealthHealthy  иначе
//
// Возвращает TargetState: subnet_id / address / status / zone_shifted.
type GetTargetStatesUseCase struct {
	repo Repo
	now  func() time.Time
}

// NewGetTargetStatesUseCase — конструктор. `now` для тестов (можно подменить).
func NewGetTargetStatesUseCase(repo Repo) *GetTargetStatesUseCase {
	return &GetTargetStatesUseCase{repo: repo, now: time.Now}
}

// Execute — sync read LB + TG (with health_check) + targets → compute per-target
// state.
func (u *GetTargetStatesUseCase) Execute(
	ctx context.Context, req *lbv1.GetTargetStatesRequest,
) (*lbv1.GetTargetStatesResponse, error) {
	lbID := req.GetNetworkLoadBalancerId()
	if lbID == "" {
		return nil, errInvalidArg("network_load_balancer_id", "required")
	}
	if err := validateLoadBalancerID(lbID); err != nil {
		return nil, err
	}
	tgID := req.GetTargetGroupId()
	if tgID == "" {
		return nil, errInvalidArg("target_group_id", "required")
	}
	if err := validateTargetGroupRefID(tgID); err != nil {
		return nil, err
	}

	rd, err := u.repo.Reader(ctx)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	defer func() { _ = rd.Close() }()

	lb, err := rd.LoadBalancers().Get(ctx, lbID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	tg, err := rd.TargetGroups().Get(ctx, tgID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	targets, err := rd.TargetGroups().ListTargets(ctx, tgID)
	if err != nil {
		return nil, mapDomainErr(err)
	}

	resp := &lbv1.GetTargetStatesResponse{
		TargetStates: make([]*lbv1.TargetState, 0, len(targets)),
	}
	now := u.now()
	for _, t := range targets {
		resp.TargetStates = append(resp.TargetStates,
			computeTargetState(lb.Status, tg.HealthCheck, t, now))
	}
	return resp, nil
}

// computeTargetState — deterministic ramp formula.
func computeTargetState(
	lbStatus domain.LBStatus,
	hc domain.HealthCheck,
	t *kachorepo.TargetRecord,
	now time.Time,
) *lbv1.TargetState {
	state := &lbv1.TargetState{
		SubnetId: subnetOfTarget(t.Target),
		Address:  addressOfTarget(t.Target),
	}
	switch {
	case lbStatus == domain.LBStatusStopped:
		state.Status = lbv1.TargetState_INACTIVE
	case t.Status == "DRAINING":
		state.Status = lbv1.TargetState_DRAINING
	case isInInitialRamp(hc, t.CreatedAt, now):
		state.Status = lbv1.TargetState_INITIAL
	default:
		state.Status = lbv1.TargetState_HEALTHY
	}
	return state
}

// isInInitialRamp — true пока age < interval * healthy_threshold.
func isInInitialRamp(hc domain.HealthCheck, createdAt, now time.Time) bool {
	interval := time.Duration(hc.Interval)
	if interval <= 0 {
		interval = time.Duration(domain.DefaultHealthInterval)
	}
	threshold := hc.HealthyThreshold
	if threshold <= 0 {
		threshold = domain.DefaultHealthyThreshold
	}
	rampWindow := interval * time.Duration(threshold)
	return now.Sub(createdAt) < rampWindow
}

// subnetOfTarget — извлекает subnet_id из 4-way identity, либо "".
func subnetOfTarget(t domain.Target) string {
	if t.IPRef != nil {
		return string(t.IPRef.SubnetID)
	}
	return ""
}

// addressOfTarget — string-representation IP / id для TargetState.address.
// Для in-cloud-by-id (Instance/NIC) возвращает id целиком (worker peer-resolve
// happens out-of-band; sync GetTargetStates даёт declarative identity).
func addressOfTarget(t domain.Target) string {
	if t.IPRef != nil {
		return string(t.IPRef.Address)
	}
	if t.ExternalIP != nil {
		return string(t.ExternalIP.Address)
	}
	if v, ok := t.InstanceID.Maybe(); ok {
		return string(v)
	}
	if v, ok := t.NicID.Maybe(); ok {
		return string(v)
	}
	return ""
}
