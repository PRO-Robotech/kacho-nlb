// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"github.com/PRO-Robotech/kacho-corelib/operations"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// lbAddressOwnerKind — Reference.type для NLB LoadBalancer в vpc.Address referrer.
const lbAddressOwnerKind = "network_load_balancer"

// lbAddressOwner — owner-tuple для vpc.Address referrer ("network_load_balancer:<id>").
// name — display-имя LB для used_by-зеркала (пусто на release-пути, где имя не нужно).
func lbAddressOwner(lbID, name string) vpcclient.AddressOwner {
	return vpcclient.AddressOwner{Kind: lbAddressOwnerKind, ID: lbID, Name: name}
}

// lbOutboxPayload — JSON-payload для outbox. Минимальный snapshot.
func lbOutboxPayload(lb *kachorepo.LoadBalancerRecord) map[string]any {
	if lb == nil {
		return nil
	}
	return map[string]any{
		"id":         string(lb.ID),
		"project_id": string(lb.ProjectID),
		"region_id":  string(lb.RegionID),
		"name":       string(lb.Name),
		"status":     string(lb.Status),
		"type":       string(lb.Type),
	}
}

// lbRegisterIntent — FGA-register-intent свежесозданного LB (project-hierarchy +
// creator-tuple, если principal — аутентифицированный пользователь).
func lbRegisterIntent(lb *kachorepo.LoadBalancerRecord, principal operations.Principal) domain.FGARegisterIntent {
	id := string(lb.ID)
	tuples := []domain.FGATuple{
		domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, id, string(lb.ProjectID)),
	}
	if subject := domain.FGASubjectFromPrincipal(principal.Type, principal.ID); subject != "" {
		tuples = append(tuples, domain.FGACreatorTuple(subject, domain.FGAObjectTypeLoadBalancer, id))
	}
	return domain.FGARegisterIntent{
		Kind:            "NetworkLoadBalancer",
		ResourceID:      id,
		Tuples:          tuples,
		Labels:          domain.LabelsToMap(lb.Labels),
		ParentProjectID: string(lb.ProjectID),
	}
}

// lbMirrorIntent — mirror-feed register-intent для UPDATED LB (project-hierarchy
// re-register с обновлёнными labels; без creator-tuple).
func lbMirrorIntent(lb *kachorepo.LoadBalancerRecord) domain.FGARegisterIntent {
	id := string(lb.ID)
	return domain.FGARegisterIntent{
		Kind:       "NetworkLoadBalancer",
		ResourceID: id,
		Tuples: []domain.FGATuple{
			domain.FGAProjectTuple(domain.FGAObjectTypeLoadBalancer, id, string(lb.ProjectID)),
		},
		Labels:          domain.LabelsToMap(lb.Labels),
		ParentProjectID: string(lb.ProjectID),
	}
}
