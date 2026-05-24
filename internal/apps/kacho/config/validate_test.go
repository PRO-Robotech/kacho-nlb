// validate_test.go — KAC-160; Mode enum + Config.Validate.
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

func TestValidate_AuthnTLSRequiresKeyAndCert(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.Authn.Type = "tls"
	// key-file/cert-file пусто
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "authn.tls") {
		t.Fatalf("expected authn.tls key/cert error, got %v", err)
	}
}

// TestValidate_JobsTargetDrainIntervalZero — KAC-159: interval=0s rejected.
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

// TestValidate_InternalLifecycleMaxStreamsZero — KAC-157: max-streams=0 rejected
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
