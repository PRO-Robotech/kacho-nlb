// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// geo_addr_test.go — kacho-nlb валидирует region через kacho-geo.
//
// Geo-endpoint биндится из ENV `KACHO_NLB_GEO_GRPC_ADDR` (явный BindEnv,
// см. defaults.go) → cfg.ExtAPI.Geo.Addr. Mirror того, что compute/vpc/iam
// peer-эндпоинты задаются конфигом; geo — новое ребро region-валидации.
package config

import "testing"

func TestLoad_GeoGRPCAddr_FromEnv(t *testing.T) {
	t.Setenv("KACHO_NLB_MODE", "dev") // dev-opt-in: default fail-closed production
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://u:p@h/d")
	t.Setenv("KACHO_NLB_GEO_GRPC_ADDR", "kacho-geo.kacho.svc.cluster.local:9090")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ExtAPI.Geo.Addr != "kacho-geo.kacho.svc.cluster.local:9090" {
		t.Errorf("ExtAPI.Geo.Addr: got %q, want kacho-geo.kacho.svc.cluster.local:9090", cfg.ExtAPI.Geo.Addr)
	}
}

func TestLoad_GeoMTLS_FromEnv(t *testing.T) {
	t.Setenv("KACHO_NLB_MODE", "dev") // dev-opt-in: default fail-closed production
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://u:p@h/d")
	t.Setenv("KACHO_NLB_MTLS__GEO__ENABLE", "true")
	t.Setenv("KACHO_NLB_MTLS__GEO__SERVERNAME", "kacho-geo.kacho.svc.cluster.local")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MTLS.Geo.Enable {
		t.Error("MTLS.Geo.Enable: got false, want true")
	}
	if cfg.MTLS.Geo.ServerName != "kacho-geo.kacho.svc.cluster.local" {
		t.Errorf("MTLS.Geo.ServerName: got %q", cfg.MTLS.Geo.ServerName)
	}
}
