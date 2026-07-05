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
// Ключи — из единого источника истины kachorepo.LifecyclePayload (тот же
// набор литералов, что читает Subscribe-consumer).
func lbOutboxPayload(lb *kachorepo.LoadBalancerRecord) map[string]any {
	if lb == nil {
		return nil
	}
	return kachorepo.LifecyclePayload{
		ID:        string(lb.ID),
		ProjectID: string(lb.ProjectID),
		RegionID:  string(lb.RegionID),
		Name:      string(lb.Name),
		Status:    string(lb.Status),
		Type:      string(lb.Type),
	}.Map()
}

// lbMovedPayload — MOVED-event outbox-payload. old_project_id — исходный project
// (canonical-ключ, который Subscribe-consumer читает в
// ResourceLifecycleEvent.OldProjectId для kacho-iam FGA-sync: снос stale
// owner/hierarchy-tuples на старом project). Единый источник имён ключей —
// kachorepo.LifecyclePayload.
func lbMovedPayload(id, srcProject, dstProject string) map[string]any {
	return kachorepo.LifecyclePayload{
		ID:           id,
		OldProjectID: srcProject,
		NewProjectID: dstProject,
	}.Map()
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
