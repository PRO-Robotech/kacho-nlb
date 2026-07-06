// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
)

// dialpeers_mtls_test.go — nlb→vpc + nlb→compute CLIENT mTLS dial wiring.
//
// Ground truth (mirrors the already-working nlb→iam mtls.iam-register edge and
// vpc→compute / edges): kacho-vpc & kacho-compute run mTLS servers
// (RequireAndVerifyClientCert). A nlb dial reaching them in PLAINTEXT is reset
// ("error reading server preface: EOF", code 14). Therefore each peer dial MUST
// consume its OWN per-edge grpcclient.TLSClient creds (cfg.MTLS.VPC /
// cfg.MTLS.Compute) — exactly the way the iam-internal dial consumes
// cfg.MTLS.IAMRegister and iam-public consumes cfg.MTLS.IAMProject.
//
// The wiring lives in dialPeers (composition root). peerDialSpecs is the pure,
// testable projection of that wiring: each spec pairs an edge with the
// grpcclient.TLSClient config that its dial will present. This test pins the
// contract so a regression to a zero-value (insecure) TLSClient on the vpc/
// compute edges is caught — i.e. no plaintext nlb→{vpc,compute} dial remains.

// edgeSpec finds the dial-spec for a given peer name; fails the test if absent.
func edgeSpec(t *testing.T, specs []peerDialSpec, name string) peerDialSpec {
	t.Helper()
	for _, s := range specs {
		if s.name == name {
			return s
		}
	}
	t.Fatalf("peerDialSpecs missing edge %q", name)
	return peerDialSpec{}
}

// TestPeerDialSpecs_VPCComputeWiredToPerEdgeMTLS — the vpc-public,
// vpc-internal and compute dial-specs carry their per-edge mTLS config, so when
// the edge is enabled the dial presents the kacho-nlb client-cert (NOT insecure).
// RED until dialPeers exposes peerDialSpecs wiring cfg.MTLS.VPC / cfg.MTLS.Compute.
func TestPeerDialSpecs_VPCComputeWiredToPerEdgeMTLS(t *testing.T) {
	t.Setenv("KACHO_NLB_MODE", "dev") // dev-opt-in: default mode is fail-closed production
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://u:p@h/kacho_nlb")
	// Enable the vpc + compute edges with distinct ServerNames so a zero-value
	// (insecure) TLSClient on either edge is observable as a mismatch.
	t.Setenv("KACHO_NLB_MTLS__VPC__ENABLE", "true")
	t.Setenv("KACHO_NLB_MTLS__VPC__SERVERNAME", "vpc.kacho.svc.cluster.local")
	t.Setenv("KACHO_NLB_MTLS__COMPUTE__ENABLE", "true")
	t.Setenv("KACHO_NLB_MTLS__COMPUTE__SERVERNAME", "compute.kacho.svc.cluster.local")
	// Give vpc/compute peer addrs so the specs are populated.
	t.Setenv("KACHO_NLB_EXTAPI__VPC__ADDR", "kacho-vpc.kacho.svc.cluster.local:9090")
	t.Setenv("KACHO_NLB_EXTAPI__VPC__INTERNAL_ADDR", "kacho-vpc.kacho.svc.cluster.local:9091")
	t.Setenv("KACHO_NLB_EXTAPI__COMPUTE__ADDR", "kacho-compute.kacho.svc.cluster.local:9090")

	cfg, err := config.Load("")
	require.NoError(t, err)
	require.True(t, cfg.MTLS.VPC.Enable)
	require.True(t, cfg.MTLS.Compute.Enable)

	specs := peerDialSpecs(cfg)

	// vpc-public and vpc-internal BOTH dial the vpc server (mTLS), so both carry
	// cfg.MTLS.VPC — neither may be a zero-value insecure TLSClient.
	vpcPub := edgeSpec(t, specs, "vpc-public")
	vpcInt := edgeSpec(t, specs, "vpc-internal")
	assert.Equal(t, cfg.MTLS.VPC, vpcPub.mtls, "vpc-public dial must present the nlb→vpc client-cert (cfg.MTLS.VPC)")
	assert.Equal(t, cfg.MTLS.VPC, vpcInt.mtls, "vpc-internal dial must present the nlb→vpc client-cert (cfg.MTLS.VPC)")
	assert.True(t, vpcPub.mtls.Enable, "vpc-public dial must NOT fall back to a plaintext (insecure) TLSClient")
	assert.True(t, vpcInt.mtls.Enable, "vpc-internal dial must NOT fall back to a plaintext (insecure) TLSClient")
	assert.Equal(t, "vpc.kacho.svc.cluster.local", vpcPub.mtls.ServerName)

	// compute dials the compute server (mTLS) → carries cfg.MTLS.Compute.
	cmp := edgeSpec(t, specs, "compute")
	assert.Equal(t, cfg.MTLS.Compute, cmp.mtls, "compute dial must present the nlb→compute client-cert (cfg.MTLS.Compute)")
	assert.True(t, cmp.mtls.Enable, "compute dial must NOT fall back to a plaintext (insecure) TLSClient")
	assert.Equal(t, "compute.kacho.svc.cluster.local", cmp.mtls.ServerName)
}

// TestPeerDialSpecs_IAMPerListenerMTLS — regression mirror: the two iam dials
// keep their per-listener split (iam-internal=IAMRegister, iam-public=IAMProject)
// that the vpc/compute edges are modelled on. Pins that no edge accidentally
// shares another edge's TLSClient.
func TestPeerDialSpecs_IAMPerListenerMTLS(t *testing.T) {
	t.Setenv("KACHO_NLB_MODE", "dev") // dev-opt-in: default mode is fail-closed production
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://u:p@h/kacho_nlb")
	cfg, err := config.Load("")
	require.NoError(t, err)

	specs := peerDialSpecs(cfg)
	assert.Equal(t, cfg.MTLS.IAMProject, edgeSpec(t, specs, "iam-public").mtls,
		"iam-public (:9090 ProjectService.Get) dial carries cfg.MTLS.IAMProject")
	assert.Equal(t, cfg.MTLS.IAMRegister, edgeSpec(t, specs, "iam-internal").mtls,
		"iam-internal (:9091 Check+Register) dial carries cfg.MTLS.IAMRegister")
}

// TestPeerDialSpecs_DisabledEdgesInsecure — zero-regression: with every
// edge default-off, no vpc/compute spec is mTLS-enabled (insecure dev dial). The
// load-bearing property is Enable=false → dialOne builds insecure creds and the
// cert paths are never read (viper seeds CAFiles to an empty string default,
// which is functionally equivalent to nil for an off edge).
func TestPeerDialSpecs_DisabledEdgesInsecure(t *testing.T) {
	t.Setenv("KACHO_NLB_MODE", "dev") // dev-opt-in: default mode is fail-closed production
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://u:p@h/kacho_nlb")
	cfg, err := config.Load("")
	require.NoError(t, err)

	for _, s := range peerDialSpecs(cfg) {
		assert.False(t, s.mtls.Enable, "edge %q mTLS off by default (zero regression)", s.name)
	}
}
