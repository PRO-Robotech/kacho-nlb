// actions_test.go — RBAC sub-phase D (§11, D-40..D-47): the nlb list-filter must
// call iam AuthorizeService.ListObjects with an `action` whose verb the iam
// server resolves to the FGA `viewer` relation (read==enforce parity under the
// scope_grant rules-model).
//
// Why this test exists (the bug it pins, mirror of kacho-compute actions_test.go):
//
//	kacho-iam internal/service/authorize_service.go::resolveActionToRelation maps
//	ONLY the canonical RPC verbs get/list → "viewer". The verb "read" is UNMAPPED
//	→ ListObjects answers `Illegal argument action` (InvalidArgument), which the
//	nlb filter wraps as Unavailable for every List → with list-filter.enabled=true
//	ALL public Lists break (fail-closed on a contract mismatch, not a real denial).
//
//	read==enforce requires List visibility to use the SAME relation the per-RPC
//	Check gate uses for Get/List, which is "viewer" (internal/check/permission_map.go).
//
// This test embeds a faithful copy of the iam verb→relation contract (nlb cannot
// import iam internals) and asserts each nlb List action resolves to "viewer".
package authzfilter

import (
	"strings"
	"testing"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// resolveActionToRelationIAM mirrors the CONTRACT enforced by kacho-iam
// internal/service/authorize_service.go::resolveActionToRelation for the read-path
// verbs: the iam server splits "<domain>.<resource>.<verb>", lower-cases the verb,
// and maps get/list → "viewer". An unmapped verb (e.g. "read") → "" → the iam
// server answers InvalidArgument.
func resolveActionToRelationIAM(action string) string {
	last := strings.LastIndexByte(action, '.')
	if last < 0 || last == len(action)-1 {
		return ""
	}
	switch strings.ToLower(action[last+1:]) {
	case "get", "list":
		return "viewer"
	}
	return ""
}

// every nlb public-List action MUST resolve to the "viewer" relation on the iam
// ListObjects server (read==enforce).
func TestListActions_ResolveToViewer(t *testing.T) {
	cases := []struct {
		name   string
		action string
	}{
		{"loadbalancer", ActionLoadBalancerList},
		{"listener", ActionListenerList},
		{"targetgroup", ActionTargetGroupList},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveActionToRelationIAM(c.action); got != "viewer" {
				t.Fatalf("action %q must resolve to FGA relation %q on the iam ListObjects "+
					"server (read==enforce, D-40..D-47), got %q. The verb must be one the "+
					"iam resolveActionToRelation maps to viewer (get/list), NOT \"read\".",
					c.action, "viewer", got)
			}
		})
	}
}

// guards against regressing to the ".read" verb (the exact D-consumer bug):
// every list-filter action verb must be "list".
func TestListActions_VerbIsList(t *testing.T) {
	for _, action := range []string{ActionLoadBalancerList, ActionListenerList, ActionTargetGroupList} {
		last := strings.LastIndexByte(action, '.')
		if verb := action[last+1:]; verb != "list" {
			t.Fatalf("action %q: list-filter verb must be %q (iam maps it to viewer); got %q. "+
				"The verb \"read\" is unmapped by iam → ListObjects returns InvalidArgument → "+
				"every nlb List breaks fail-closed.", action, "list", verb)
		}
	}
}

// the FGA object-type constants must match the `lb_*` authorization-model types
// (single source of truth: internal/domain.FGAObjectType*). A drift here would
// scope ListObjects to a non-existent type → empty grants → no-leak over-blocking.
func TestResourceTypes_MatchDomainFGAObjectTypes(t *testing.T) {
	cases := []struct {
		name      string
		filterTyp string
		domainTyp string
	}{
		{"loadbalancer", ResourceTypeLoadBalancer, domain.FGAObjectTypeLoadBalancer},
		{"listener", ResourceTypeListener, domain.FGAObjectTypeListener},
		{"targetgroup", ResourceTypeTargetGroup, domain.FGAObjectTypeTargetGroup},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.filterTyp != c.domainTyp {
				t.Fatalf("resource-type drift: authzfilter %q != domain %q", c.filterTyp, c.domainTyp)
			}
			if !strings.HasPrefix(c.filterTyp, "lb_") {
				t.Fatalf("resource-type %q must carry the lb_ prefix (FGA model + permission_catalog)", c.filterTyp)
			}
		})
	}
}
