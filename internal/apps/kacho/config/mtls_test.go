// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
)

// mtls_test.go — opt-in mTLS per-edge config (corelib value-
// structs). Scenarios (disabled default → insecure),
// (enabled → creds build), fail-closed on missing CA. Newman (mismatch)
// is an e2e concern; here we verify the config→creds wiring contract.

// minimalEnv sets the only hard-required config field (postgres URL) plus an
// explicit dev opt-in so config.Load("") passes validation for an env-driven
// mTLS test. mode defaults to production (fail-closed, security.md); these
// tests exercise the config→creds wiring in the relaxed dev path, so dev is set
// explicitly.
func minimalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KACHO_NLB_MODE", "dev")
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://u:p@h/kacho_nlb")
}

// TestMTLS_SECD16_DisabledDefaultInsecure — every edge mTLS off by
// default → insecure creds build without reading any cert file (dev unchanged).
func TestMTLS_SECD16_DisabledDefaultInsecure(t *testing.T) {
	minimalEnv(t)
	cfg, err := config.Load("")
	require.NoError(t, err)

	assert.False(t, cfg.MTLS.Server.Enable, "server listener mTLS off by default")
	assert.False(t, cfg.MTLS.IAMRegister.Enable, "nlb→iam register mTLS off by default")
	assert.False(t, cfg.MTLS.VPC.Enable, "nlb→vpc mTLS off by default")
	assert.False(t, cfg.MTLS.Compute.Enable, "nlb→compute mTLS off by default")

	// Disabled → creds build to insecure without touching cert paths.
	_, err = grpcsrv.TLSServerCreds(cfg.MTLS.Server)
	require.NoError(t, err)
	_, err = grpcclient.TLSClientCreds(cfg.MTLS.IAMRegister)
	require.NoError(t, err)
	_, err = grpcclient.TLSClientCreds(cfg.MTLS.VPC)
	require.NoError(t, err)
	_, err = grpcclient.TLSClientCreds(cfg.MTLS.Compute)
	require.NoError(t, err)
}

// TestMTLS_SECD17_EnabledClientCredsBuild — a per-edge client
// config loaded from ENV with valid cert material builds mTLS dial creds.
func TestMTLS_SECD17_EnabledClientCredsBuild(t *testing.T) {
	minimalEnv(t)
	caFile, certFile, keyFile := writeTestPKI(t)
	// viper env-key replacer maps '.'→'__' only; the hyphen in the 'iam-register'
	// section stays literal in the env name (KACHO_NLB_MTLS__IAM-REGISTER__*).
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__ENABLE", "true")
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__CERTFILE", certFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__KEYFILE", keyFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__CAFILES", caFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__SERVERNAME", "kacho-iam.kacho.svc.cluster.local")

	cfg, err := config.Load("")
	require.NoError(t, err)
	require.True(t, cfg.MTLS.IAMRegister.Enable)
	require.Equal(t, certFile, cfg.MTLS.IAMRegister.CertFile)
	require.Equal(t, "kacho-iam.kacho.svc.cluster.local", cfg.MTLS.IAMRegister.ServerName)

	opt, err := grpcclient.TLSClientCreds(cfg.MTLS.IAMRegister)
	require.NoError(t, err)
	require.NotNil(t, opt)
}

// TestMTLS_SECD_FailClosedMissingCA — enable=true but ca_files empty → error
// (fail-closed, no silent insecure downgrade).
func TestMTLS_SECD_FailClosedMissingCA(t *testing.T) {
	minimalEnv(t)
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__ENABLE", "true")
	// no CAFILES / SERVERNAME set.

	cfg, err := config.Load("")
	require.NoError(t, err)
	require.True(t, cfg.MTLS.IAMRegister.Enable)

	_, err = grpcclient.TLSClientCreds(cfg.MTLS.IAMRegister)
	require.Error(t, err, "mTLS enabled with empty CA must fail-closed")
}

// TestMTLS_SECD17_ServerCredsBuild — server-edge mTLS builds RequireAndVerify-
// ClientCert creds from cert+key+client-CA.
func TestMTLS_SECD17_ServerCredsBuild(t *testing.T) {
	minimalEnv(t)
	caFile, certFile, keyFile := writeTestPKI(t)
	t.Setenv("KACHO_NLB_MTLS__SERVER__ENABLE", "true")
	t.Setenv("KACHO_NLB_MTLS__SERVER__CERTFILE", certFile)
	t.Setenv("KACHO_NLB_MTLS__SERVER__KEYFILE", keyFile)
	t.Setenv("KACHO_NLB_MTLS__SERVER__CLIENTCAFILES", caFile)

	cfg, err := config.Load("")
	require.NoError(t, err)
	require.True(t, cfg.MTLS.Server.Enable)

	opt, err := grpcsrv.TLSServerCreds(cfg.MTLS.Server)
	require.NoError(t, err)
	require.NotNil(t, opt)
}
