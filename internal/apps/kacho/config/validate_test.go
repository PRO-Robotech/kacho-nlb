// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// validate_test.go —; Mode enum + Config.Validate.
package config

import (
	"strings"
	"testing"
	"time"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in      string
		want    ModeEnum
		wantErr bool
	}{
		{"", ModeDev, false},
		{"dev", ModeDev, false},
		{"DEV", ModeDev, false},
		{"development", ModeDev, false},
		{"production", ModeProduction, false},
		{"PROD", ModeProduction, false},
		{"bogus", 0, true},
	}
	for _, c := range cases {
		got, err := ParseMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseMode(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMode(%q): unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseMode(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}

// minimalValidConfig — самая базовая корректная Config (для модификации в тестах).
func minimalValidConfig() Config {
	return Config{
		ModeRaw: "dev",
		Logger:  LoggerConfig{Level: "INFO"},
		APIServer: APIServerConfig{
			Endpoint:         "tcp://0.0.0.0:9090",
			InternalEndpoint: "tcp://0.0.0.0:9091",
			GracefulShutdown: 10 * time.Second,
		},
		Repository: RepositoryConfig{
			Type:     "POSTGRES",
			Postgres: PostgresConfig{URL: "postgres://u:p@h/d"},
		},
		Jobs: JobsConfig{
			TargetDrain: TargetDrainConfig{Interval: 10 * time.Second},
			FreeIP:      FreeIPConfig{Interval: 30 * time.Second, AgeThreshold: 5 * time.Minute},
		},
		InternalLifecycle: InternalLifecycleConfig{MaxStreams: 32},
	}
}

func TestValidate_Minimal_OK(t *testing.T) {
	cfg := minimalValidConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("minimal valid config: unexpected err: %v", err)
	}
}

func TestValidate_BadLogLevel(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Logger.Level = "TRACE"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "logger.level") {
		t.Fatalf("expected logger.level error, got %v", err)
	}
}

func TestValidate_EndpointMissingPort(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.APIServer.Endpoint = "tcp://0.0.0.0" // нет :port
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "api-server.endpoint") {
		t.Fatalf("expected endpoint error, got %v", err)
	}
}

func TestValidate_EndpointBadScheme(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.APIServer.Endpoint = "http://localhost:9090"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("expected scheme error, got %v", err)
	}
}

func TestValidate_RepositoryURLRequired(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Repository.Postgres.URL = ""
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "repository.postgres.url") {
		t.Fatalf("expected url required error, got %v", err)
	}
}

func TestValidate_RepositoryUnknownType(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Repository.Type = "MYSQL"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "POSTGRES") {
		t.Fatalf("expected POSTGRES-only error, got %v", err)
	}
}

func TestValidate_NegativeMaxConns(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Repository.Postgres.MaxConns = -1
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "max-conns") {
		t.Fatalf("expected max-conns error, got %v", err)
	}
}

func TestValidate_GracefulShutdownZero(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.APIServer.GracefulShutdown = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "graceful-shutdown") {
		t.Fatalf("expected graceful-shutdown error, got %v", err)
	}
}

func TestValidate_ProductionRequiresAuthzIAM(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.ModeRaw = "production"
	// authz.iam.addr пустой → must fail в production
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "authz.iam.addr") {
		t.Fatalf("expected authz.iam.addr error in production, got %v", err)
	}
}

func TestValidate_ProductionForbidsBreakglass(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.ModeRaw = "production"
	cfg.Authz.IAM.Addr = "iam.kacho.svc:9091"
	cfg.Authz.Breakglass = true
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "breakglass") {
		t.Fatalf("expected breakglass-in-prod error, got %v", err)
	}
}

// TestValidate_Production_RequiresSecureServerTransport — production mode с
// plaintext-listener'ом (server mTLS off + authn=none) обязан fail-close'иться
// (security.md «AuthN+AuthZ ВЕЗДЕ»: plaintext в проде запрещён).
func TestValidate_Production_RequiresSecureServerTransport(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.ModeRaw = "production"
	cfg.Authz.IAM.Addr = "iam.kacho.svc:9091"
	cfg.MTLS.IAMRegister.Enable = true // iam-edge ok — изолируем server-transport check
	// server mTLS off + authn none → insecure listener.
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure server transport") {
		t.Fatalf("expected insecure-server-transport error in production, got %v", err)
	}
}

// TestValidate_Production_RequiresMTLSOnIAMEdge — production требует mTLS на ребре
// nlb→iam (per-RPC InternalIAMService.Check): insecure Check-edge запрещён.
func TestValidate_Production_RequiresMTLSOnIAMEdge(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.ModeRaw = "production"
	cfg.Authz.IAM.Addr = "iam.kacho.svc:9091"
	cfg.MTLS.Server.Enable = true // server transport ok — изолируем iam-edge check
	// iam-register mTLS off → insecure Check edge.
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "iam-register") {
		t.Fatalf("expected iam-edge mTLS error in production, got %v", err)
	}
}

// TestValidate_Production_SecureTransport_OK — production с server mTLS + iam-edge
// mTLS проходит валидацию (позитивная ветка fail-closed транспорта).
func TestValidate_Production_SecureTransport_OK(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.ModeRaw = "production"
	cfg.Authz.IAM.Addr = "iam.kacho.svc:9091"
	cfg.MTLS.Server.Enable = true
	cfg.MTLS.IAMRegister.Enable = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + secure transport: unexpected err: %v", err)
	}
}

// TestValidate_Production_AuthnTLSSatisfiesServerTransport — one-way TLS+JWT
// (authn.type=tls) удовлетворяет требованию защищённого server-транспорта (mTLS
// на listener'е не обязателен, если есть TLS+JWT user→edge).
func TestValidate_Production_AuthnTLSSatisfiesServerTransport(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.ModeRaw = "production"
	cfg.Authz.IAM.Addr = "iam.kacho.svc:9091"
	cfg.MTLS.IAMRegister.Enable = true
	cfg.Authn.Type = "tls"
	cfg.Authn.TLS.KeyFile = "/etc/tls/key.pem"
	cfg.Authn.TLS.CertFile = "/etc/tls/cert.pem"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + authn.tls + iam mTLS: unexpected err: %v", err)
	}
}

func TestValidate_AuthnTLSRequiresKeyAndCert(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Authn.Type = "tls"
	// key-file/cert-file пусто
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "authn.tls") {
		t.Fatalf("expected authn.tls key/cert error, got %v", err)
	}
}

// TestValidate_JobsTargetDrainIntervalZero — interval=0s rejected.
func TestValidate_JobsTargetDrainIntervalZero(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Jobs.TargetDrain.Interval = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "jobs.target-drain.interval") {
		t.Fatalf("expected target-drain.interval error, got %v", err)
	}
}

// TestValidate_JobsTargetDrainIntervalNegative — defense vs negative.
func TestValidate_JobsTargetDrainIntervalNegative(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Jobs.TargetDrain.Interval = -5 * time.Second
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "jobs.target-drain.interval") {
		t.Fatalf("expected target-drain.interval error, got %v", err)
	}
}

// TestValidate_JobsFreeIPIntervalZero — free-ip.interval=0s rejected.
func TestValidate_JobsFreeIPIntervalZero(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Jobs.FreeIP.Interval = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "jobs.free-ip.interval") {
		t.Fatalf("expected free-ip.interval error, got %v", err)
	}
}

// TestValidate_JobsFreeIPAgeThresholdZero — free-ip.age-threshold=0 rejected:
// нулевой порог → reconciler схватит свежий in-flight create/delete.
func TestValidate_JobsFreeIPAgeThresholdZero(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Jobs.FreeIP.AgeThreshold = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "jobs.free-ip.age-threshold") {
		t.Fatalf("expected free-ip.age-threshold error, got %v", err)
	}
}

// TestValidate_InternalLifecycleMaxStreamsZero — max-streams=0 rejected
// (нулевой лимит = kacho-iam не сможет подключиться).
func TestValidate_InternalLifecycleMaxStreamsZero(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.InternalLifecycle.MaxStreams = 0
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "internal-lifecycle.max-streams") {
		t.Fatalf("expected internal-lifecycle.max-streams error, got %v", err)
	}
}

// TestValidate_InternalLifecycleMaxStreamsNegative — defense vs negative.
func TestValidate_InternalLifecycleMaxStreamsNegative(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.InternalLifecycle.MaxStreams = -1
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "internal-lifecycle.max-streams") {
		t.Fatalf("expected internal-lifecycle.max-streams error, got %v", err)
	}
}

func TestModeString(t *testing.T) {
	if ModeDev.String() != "dev" {
		t.Errorf("ModeDev.String(): got %q", ModeDev.String())
	}
	if ModeProduction.String() != "production" {
		t.Errorf("ModeProduction.String(): got %q", ModeProduction.String())
	}
}
