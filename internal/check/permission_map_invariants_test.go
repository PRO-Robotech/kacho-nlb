// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package check_test

import (
	"testing"

	"github.com/PRO-Robotech/kacho-corelib/ids"
	corevalidate "github.com/PRO-Robotech/kacho-corelib/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/check"
)

// Инварианты authz-доступа к ресурсам kacho-nlb (service-сторона), дополняющие
// drift_test (полнота map'ы) и verb_bearing (relations). Закрывают тот же класс
// ошибок ролевок, что вскрыл anycast pool.

// nlbTopProjectListRPCs — top-level project-scoped List. Их handler фильтрует
// результат через iam ListObjects (per-object viewer ∪ v_list), поэтому per-RPC
// Check ДОЛЖЕН пропускаться (ScopeFiltered) И на service-, И на gateway-стороне
// (там — <exempt>). Несогласованность (ScopeFiltered, но gateway call-gate'ит)
// отклоняет legit viewer'а без отдельного v_list-tuple 403.
var nlbTopProjectListRPCs = []string{
	"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/List",
	"/kacho.cloud.loadbalancer.v1.ListenerService/List",
	"/kacho.cloud.loadbalancer.v1.TargetGroupService/List",
}

// TestInvariant_TopProjectList_ScopeFiltered — каждый top-level project-List
// помечен ScopeFiltered: interceptor пропускает per-RPC Check, handler фильтрует
// через ListObjects. Это service-парный инвариант к gateway <exempt>: без него
// (или с tier-Check'ом) viewer без v_list-tuple отклоняется до scope-filter'а.
func TestInvariant_TopProjectList_ScopeFiltered(t *testing.T) {
	m := check.PermissionMap()
	for _, rpc := range nlbTopProjectListRPCs {
		e, ok := m.Lookup(rpc)
		require.Truef(t, ok, "%s must be mapped", rpc)
		assert.Truef(t, e.ScopeFiltered,
			"%s: top-level project List must be ScopeFiltered (handler filters via ListObjects); "+
				"otherwise a viewer without an explicit v_list tuple is rejected before the filter runs", rpc)
		assert.Equalf(t, "viewer", e.Relation, "%s: top-List stays tier viewer (visibility via ListObjects union)", rpc)
	}
}

// TestInvariant_NLBIdPrefixesRegistered — id-префиксы ресурсов kacho-nlb
// зарегистрированы в corelib validate.resourceIDPrefixes. Без этого
// corevalidate.ResourceID отвергает любой well-formed id как malformed (400),
// ломая Get/Update/Delete/<verb> (реальный инцидент с префиксом `aap`). Guard
// против того же класса для nlb.
func TestInvariant_NLBIdPrefixesRegistered(t *testing.T) {
	cases := map[string]string{
		"NetworkLoadBalancer": ids.PrefixLoadBalancer,
		"Listener":            ids.PrefixListener,
		"TargetGroup":         ids.PrefixTargetGroup,
	}
	for name, prefix := range cases {
		id := prefix + "00000000000000000"
		err := corevalidate.ResourceID(name, prefix, id)
		require.NoErrorf(t, err, "well-formed %s id %q must be accepted by corevalidate.ResourceID "+
			"(prefix %q must be registered in corelib validate.resourceIDPrefixes)", name, id, prefix)
	}
}
