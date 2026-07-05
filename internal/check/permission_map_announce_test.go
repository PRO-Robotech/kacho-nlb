// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package check_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/check"
)

// InternalLoadBalancerAnnounceService (:9091) — read viewer-gated (v_get),
// inbound write data-plane→nlb least-priv (announce_writer). Per-RPC Check
// энфорсится на обоих листенерах (security.md «authN+authZ на каждом RPC»):
// ни один из RPC НЕ Public/exempt — internal-периметр не доверенный.
const (
	announceGetFM    = "/kacho.cloud.loadbalancer.v1.InternalLoadBalancerAnnounceService/GetAnnounceState"
	announceReportFM = "/kacho.cloud.loadbalancer.v1.InternalLoadBalancerAnnounceService/ReportAnnounceState"
)

func TestAnnounce_GetIsViewerGatedNotExempt(t *testing.T) {
	e, ok := check.PermissionMap()[announceGetFM]
	require.True(t, ok, "GetAnnounceState must be mapped (internal RPC fail-closed)")
	require.False(t, e.Public, "GetAnnounceState must NOT be exempt (per-RPC Check)")
	require.False(t, e.ScopeFiltered)
	require.Equal(t, "v_get", e.Relation, "read announce-state → viewer-tier verb-bearing relation")
	require.NotNil(t, e.Extract)

	const id = "nlb-xyz"
	objType, objID, err := e.Extract(&lbv1.GetLoadBalancerAnnounceStateRequest{NetworkLoadBalancerId: id})
	require.NoError(t, err)
	require.Equal(t, "lb_network_load_balancer", objType)
	require.Equal(t, id, objID)
}

func TestAnnounce_ReportIsWriterGatedNotExempt(t *testing.T) {
	e, ok := check.PermissionMap()[announceReportFM]
	require.True(t, ok, "ReportAnnounceState must be mapped (internal RPC fail-closed)")
	require.False(t, e.Public, "ReportAnnounceState must NOT be exempt (per-RPC Check)")
	require.False(t, e.ScopeFiltered)
	require.Equal(t, "announce_writer", e.Relation,
		"inbound write → least-priv data-plane writer-relation (not viewer/editor tier)")
	require.NotNil(t, e.Extract)

	const id = "nlb-xyz"
	objType, objID, err := e.Extract(&lbv1.ReportLoadBalancerAnnounceStateRequest{NetworkLoadBalancerId: id})
	require.NoError(t, err)
	require.Equal(t, "lb_network_load_balancer", objType)
	require.Equal(t, id, objID)
}

// Announce-permissions намеренно НЕ входят в 30-string tenant catalog (это
// internal RPC, relation-based gate; каталогизация announce-permission на iam-
// стороне — отдельная задача). Catalog остаётся ровно 30.
func TestAnnounce_PermissionsNotInTenantCatalog(t *testing.T) {
	get := check.PermissionMap()[announceGetFM]
	rep := check.PermissionMap()[announceReportFM]
	require.Empty(t, get.Permission)
	require.Empty(t, rep.Permission)
}
