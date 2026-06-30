// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package type2pb

import (
	"fmt"

	lbv1 "github.com/PRO-Robotech/kacho-nlb/proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/dto"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// networkLoadBalancer — трансфер kachorepo.LoadBalancerRecord → *lbv1.NetworkLoadBalancer.
type networkLoadBalancer struct{}

func (networkLoadBalancer) toPb(rec kachorepo.LoadBalancerRecord) (*lbv1.NetworkLoadBalancer, error) {
	ts, err := timeObj{}.toPb(rec.CreatedAt)
	if err != nil {
		return nil, err
	}
	statusPb, err := lbStatusToPb(rec.Status)
	if err != nil {
		return nil, err
	}
	typePb, err := lbTypeToPb(rec.Type)
	if err != nil {
		return nil, err
	}
	affinityPb, err := lbAffinityToPb(rec.SessionAffinity)
	if err != nil {
		return nil, err
	}
	ipFamilies, err := ipFamiliesToPb(rec.IPFamilies)
	if err != nil {
		return nil, err
	}
	// Lean-проекция: anycast-VIP (address_v4/v6) + binding (address_id_v4/v6) +
	// заявленные семейства. vip_origin_v4/v6 — DB-only, в proto не выходят;
	// announce/route/VRF/per-zone — отдельная Internal-проекция (:9091).
	return &lbv1.NetworkLoadBalancer{
		Id:                 string(rec.ID),
		ProjectId:          string(rec.ProjectID),
		CreatedAt:          ts,
		Name:               string(rec.Name),
		Description:        string(rec.Description),
		Labels:             domain.LabelsToMap(rec.Labels),
		RegionId:           string(rec.RegionID),
		NetworkId:          string(rec.NetworkID),
		SecurityGroupIds:   domain.SecurityGroupIDsToStrings(rec.SecurityGroupIDs),
		Status:             statusPb,
		Type:               typePb,
		SessionAffinity:    affinityPb,
		CrossZoneEnabled:   rec.CrossZoneEnabled,
		DeletionProtection: rec.DeletionProtection,
		AddressV4:          string(rec.AddressV4),
		AddressV6:          string(rec.AddressV6),
		AddressIdV4:        string(rec.AddressIDV4),
		AddressIdV6:        string(rec.AddressIDV6),
		IpFamilies:         ipFamilies,
	}, nil
}

// ipFamiliesToPb — []domain.IPVersion → []lbv1.IpVersion. Пустые/неизвестные
// токены — ошибка (repo читает только валидные значения из text[]-колонки).
func ipFamiliesToPb(fams []domain.IPVersion) ([]lbv1.IpVersion, error) {
	if len(fams) == 0 {
		return nil, nil
	}
	out := make([]lbv1.IpVersion, 0, len(fams))
	for _, f := range fams {
		pb, err := ipVersionToPb(f)
		if err != nil {
			return nil, err
		}
		out = append(out, pb)
	}
	return out, nil
}

// lbStatusToPb — domain LBStatus → proto enum NetworkLoadBalancer_Status.
func lbStatusToPb(s domain.LBStatus) (lbv1.NetworkLoadBalancer_Status, error) {
	switch s {
	case domain.LBStatusCreating:
		return lbv1.NetworkLoadBalancer_CREATING, nil
	case domain.LBStatusStarting:
		return lbv1.NetworkLoadBalancer_STARTING, nil
	case domain.LBStatusActive:
		return lbv1.NetworkLoadBalancer_ACTIVE, nil
	case domain.LBStatusStopping:
		return lbv1.NetworkLoadBalancer_STOPPING, nil
	case domain.LBStatusStopped:
		return lbv1.NetworkLoadBalancer_STOPPED, nil
	case domain.LBStatusDeleting:
		return lbv1.NetworkLoadBalancer_DELETING, nil
	case domain.LBStatusInactive:
		return lbv1.NetworkLoadBalancer_INACTIVE, nil
	}
	return lbv1.NetworkLoadBalancer_STATUS_UNSPECIFIED, fmt.Errorf("unknown LBStatus: %q", s)
}

// lbTypeToPb — domain LBType → proto enum NetworkLoadBalancer_Type.
func lbTypeToPb(t domain.LBType) (lbv1.NetworkLoadBalancer_Type, error) {
	switch t {
	case domain.LBTypeExternal:
		return lbv1.NetworkLoadBalancer_EXTERNAL, nil
	case domain.LBTypeInternal:
		return lbv1.NetworkLoadBalancer_INTERNAL, nil
	}
	return lbv1.NetworkLoadBalancer_TYPE_UNSPECIFIED, fmt.Errorf("unknown LBType: %q", t)
}

// lbAffinityToPb — domain SessionAffinity → proto enum. Значения proto и DB-домена
// совпадают 1:1 (FIVE_TUPLE / CLIENT_IP_ONLY).
func lbAffinityToPb(a domain.SessionAffinity) (lbv1.NetworkLoadBalancer_SessionAffinity, error) {
	switch a {
	case domain.SessionAffinity5Tuple:
		return lbv1.NetworkLoadBalancer_FIVE_TUPLE, nil
	case domain.SessionAffinityClientIPOnly:
		return lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY, nil
	}
	return lbv1.NetworkLoadBalancer_SESSION_AFFINITY_UNSPECIFIED, fmt.Errorf("unknown SessionAffinity: %q", a)
}

func init() {
	dto.RegTransfer(dto.Fn2Face(networkLoadBalancer{}.toPb))
}
