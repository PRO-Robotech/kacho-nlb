// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package config

import "testing"

// Verify the shipped deploy/configmap-sample.yaml parses cleanly with
// defaults applied (smoke-test, not a behavioural unit test).
func TestSampleYAMLLoads(t *testing.T) {
	cfg, err := Load("../../../../deploy/configmap-sample.yaml")
	if err != nil {
		t.Fatalf("sample YAML must load: %v", err)
	}
	if cfg.Mode() != ModeDev {
		t.Errorf("sample mode: got %v, want ModeDev", cfg.Mode())
	}
	if cfg.Repository.Postgres.URL == "" {
		t.Error("sample must set repository.postgres.url")
	}
	if cfg.Authz.IAM.Addr == "" {
		t.Error("sample must set authz.iam.addr (used in dev too as guidance)")
	}
}
