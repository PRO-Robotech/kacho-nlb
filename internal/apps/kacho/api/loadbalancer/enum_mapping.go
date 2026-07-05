// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// lbTypeFromPb — proto enum → domain.LBType. UNSPECIFIED → InvalidArgument.
func lbTypeFromPb(t lbv1.NetworkLoadBalancer_Type) (domain.LBType, error) {
	switch t {
	case lbv1.NetworkLoadBalancer_EXTERNAL:
		return domain.LBTypeExternal, nil
	case lbv1.NetworkLoadBalancer_INTERNAL:
		return domain.LBTypeInternal, nil
	}
	return "", errInvalidArg("type", "type must be one of: EXTERNAL, INTERNAL")
}

// domainSessionAffinity — proto enum → domain.SessionAffinity.
func domainSessionAffinity(a lbv1.NetworkLoadBalancer_SessionAffinity) domain.SessionAffinity {
	switch a {
	case lbv1.NetworkLoadBalancer_SESSION_AFFINITY_UNSPECIFIED, lbv1.NetworkLoadBalancer_FIVE_TUPLE:
		return domain.SessionAffinity5Tuple
	case lbv1.NetworkLoadBalancer_CLIENT_IP_ONLY:
		return domain.SessionAffinityClientIPOnly
	}
	return domain.SessionAffinity(a.String())
}

// lbSessionAffinityFromPb — fail-fast вариант: каноничная InvalidArgument на out-of-domain.
func lbSessionAffinityFromPb(a lbv1.NetworkLoadBalancer_SessionAffinity) (domain.SessionAffinity, error) {
	sa := domainSessionAffinity(a)
	if err := sa.Validate(); err != nil {
		return "", err
	}
	return sa, nil
}
