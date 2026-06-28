// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/grpcclient"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
)

// mtls_iam_test.go — CLIENT mTLS на read/authz рёбрах nlb→iam.
//
// Ground truth: NLB уже предъявляет client-cert на обоих
// iam conn'ах (iam-public ProjectService.Get :9090 и iam-internal Check+Register
// :9091), но ОБА наследуют единственный ServerName register-ребра
// (cfg.MTLS.IAMRegister.ServerName = kacho-iam-internal.*). Под это валит
// ServerName-SAN-проверку на public :9090 ребре (его dial-host = kacho-iam, не
// kacho-iam-internal) — latent-bug (два ServerName на iam).
//
// (b): per-listener split. Внутренний iam-conn (9091, Check+Register)
// продолжает использовать cfg.MTLS.IAMRegister (ServerName=kacho-iam-internal);
// public iam-conn (9090, ProjectService.Get) получает СВОЁ поле
// cfg.MTLS.IAMProject (ServerName=kacho-iam). Так оба conn'а проходят
// ServerName-SAN-проверку под.

// TestMTLS_SECI_IAMProjectEdgeExistsDisabledByDefault — новое read-
// ребро iam-project выключено по умолчанию → insecure creds без чтения cert-файлов
// (нулевая dev-регрессия).
func TestMTLS_SECI_IAMProjectEdgeExistsDisabledByDefault(t *testing.T) {
	minimalEnv(t)
	cfg, err := config.Load("")
	require.NoError(t, err)

	// RED until config.MTLSConfig gains the IAMProject (iam-public :9090) field.
	assert.False(t, cfg.MTLS.IAMProject.Enable, "nlb→iam ProjectService.Get (public :9090) mTLS off by default")

	_, err = grpcclient.TLSClientCreds(cfg.MTLS.IAMProject)
	require.NoError(t, err, "disabled iam-project edge builds insecure creds without touching cert paths")
}

// TestMTLS_SECI_IAMProjectPerListenerServerName verifies per-listener ServerName:
// when both iam edges are enabled, the public read edge (iam-project, :9090) must
// carry ServerName=kacho-iam while the internal edge (iam-register, :9091) carries
// ServerName=kacho-iam-internal. A single shared ServerName cannot be correct for
// both listeners under strict client-cert verification — this is the latent bug it fixes.
func TestMTLS_SECI_IAMProjectPerListenerServerName(t *testing.T) {
	minimalEnv(t)
	caFile, certFile, keyFile := writeTestPKI(t)

	// Internal edge (9091, Check + RegisterResource) — existing register-drainer field.
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__ENABLE", "true")
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__CERTFILE", certFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__KEYFILE", keyFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__CAFILES", caFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-REGISTER__SERVERNAME", "kacho-iam-internal.kacho.svc.cluster.local")

	// Public read edge (9090, ProjectService.Get) — NEW per-listener field.
	t.Setenv("KACHO_NLB_MTLS__IAM-PROJECT__ENABLE", "true")
	t.Setenv("KACHO_NLB_MTLS__IAM-PROJECT__CERTFILE", certFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-PROJECT__KEYFILE", keyFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-PROJECT__CAFILES", caFile)
	t.Setenv("KACHO_NLB_MTLS__IAM-PROJECT__SERVERNAME", "kacho-iam.kacho.svc.cluster.local")

	cfg, err := config.Load("")
	require.NoError(t, err)

	require.True(t, cfg.MTLS.IAMProject.Enable)
	require.True(t, cfg.MTLS.IAMRegister.Enable)

	// the two iam listeners need DISTINCT ServerNames (kacho-iam vs
	// kacho-iam-internal). The public read edge must NOT inherit the internal
	// edge's ServerName (latent-bug).
	assert.Equal(t, "kacho-iam.kacho.svc.cluster.local", cfg.MTLS.IAMProject.ServerName,
		"iam-project (public :9090) ServerName must be the :9090 dial-host kacho-iam")
	assert.Equal(t, "kacho-iam-internal.kacho.svc.cluster.local", cfg.MTLS.IAMRegister.ServerName,
		"iam-register (internal :9091) ServerName must be the :9091 dial-host kacho-iam-internal")
	assert.NotEqual(t, cfg.MTLS.IAMRegister.ServerName, cfg.MTLS.IAMProject.ServerName,
		"per-listener split: public and internal iam edges carry different ServerNames (I6/D-04)")

	// Both edges build mTLS creds presenting the kacho-nlb client-cert.
	optProject, err := grpcclient.TLSClientCreds(cfg.MTLS.IAMProject)
	require.NoError(t, err)
	require.NotNil(t, optProject)
}

// TestMTLS_SECI_IAMProjectFailClosed — (mirror): iam-project
// edge enabled with empty CA must fail-closed (no silent insecure downgrade).
func TestMTLS_SECI_IAMProjectFailClosed(t *testing.T) {
	minimalEnv(t)
	t.Setenv("KACHO_NLB_MTLS__IAM-PROJECT__ENABLE", "true")
	// no CAFILES / SERVERNAME set.

	cfg, err := config.Load("")
	require.NoError(t, err)
	require.True(t, cfg.MTLS.IAMProject.Enable)

	_, err = grpcclient.TLSClientCreds(cfg.MTLS.IAMProject)
	require.Error(t, err, "iam-project mTLS enabled with empty CA must fail-closed")
}
