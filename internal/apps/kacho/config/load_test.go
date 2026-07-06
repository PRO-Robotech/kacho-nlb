// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// load_test.go —; парсинг YAML, defaults, ENV-override, validation
// (RED-first: эти тесты были написаны до реализации load/validate; см.
// `internal/apps/kacho/config/{load,validate,defaults}.go`).
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeYAML — helper: пишет YAML-снэппет во временный файл, возвращает path.
func writeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write tmp YAML: %v", err)
	}
	return p
}

const minimalValidYAML = `
mode: dev
logger:
  level: INFO
api-server:
  endpoint: tcp://0.0.0.0:9090
  internal-endpoint: tcp://0.0.0.0:9091
  graceful-shutdown: 10s
repository:
  type: POSTGRES
  postgres:
    url: postgres://kacho:secret@db.example/kacho_nlb?sslmode=disable
`

func TestLoad_MinimalYAML(t *testing.T) {
	cfg, err := Load(writeYAML(t, minimalValidYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Mode() != ModeDev {
		t.Errorf("Mode: got %v, want ModeDev", cfg.Mode())
	}
	if cfg.Logger.Level != "INFO" {
		t.Errorf("Logger.Level: got %q, want INFO", cfg.Logger.Level)
	}
	if cfg.APIServer.GracefulShutdown != 10*time.Second {
		t.Errorf("APIServer.GracefulShutdown: got %v, want 10s", cfg.APIServer.GracefulShutdown)
	}
	if cfg.Repository.Postgres.URL == "" {
		t.Error("Repository.Postgres.URL: empty (defaults must not blank required fields)")
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	// path == "" → только defaults + ENV (ENV для required url задан ниже).
	// mode=dev задаётся ЯВНО: default fail-closed (production, см.
	// TestLoad_DefaultMode_FailsClosedProduction) не даст Load'у пройти без
	// mTLS/iam — тут проверяются НЕ-mode дефолты, поэтому dev-opt-in.
	t.Setenv("KACHO_NLB_MODE", "dev")
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://u:p@h/d")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Дефолты из RegisterDefaults:
	if cfg.Mode() != ModeDev {
		t.Errorf("explicit Mode=dev: got %v, want ModeDev", cfg.Mode())
	}
	if cfg.Logger.Level != "DEBUG" {
		t.Errorf("default Logger.Level: got %q, want DEBUG", cfg.Logger.Level)
	}
	if cfg.APIServer.Endpoint != "tcp://0.0.0.0:9090" {
		t.Errorf("default APIServer.Endpoint: got %q", cfg.APIServer.Endpoint)
	}
	if cfg.APIServer.InternalEndpoint != "tcp://0.0.0.0:9091" {
		t.Errorf("default APIServer.InternalEndpoint: got %q", cfg.APIServer.InternalEndpoint)
	}
	if cfg.APIServer.GracefulShutdown != 10*time.Second {
		t.Errorf("default APIServer.GracefulShutdown: got %v", cfg.APIServer.GracefulShutdown)
	}
	if !cfg.Metrics.Enable {
		t.Error("default Metrics.Enable: got false, want true")
	}
	if !cfg.Healthcheck.Enable {
		t.Error("default Healthcheck.Enable: got false, want true")
	}
	if cfg.Authz.Cache.TTL != 5*time.Second {
		t.Errorf("default Authz.Cache.TTL: got %v, want 5s", cfg.Authz.Cache.TTL)
	}
	if cfg.Jobs.TargetDrain.Interval != 10*time.Second {
		t.Errorf("default Jobs.TargetDrain.Interval: got %v, want 10s", cfg.Jobs.TargetDrain.Interval)
	}
	if !cfg.FGA.RegisterDrainer.Enable {
		t.Errorf("default FGA.RegisterDrainer.Enable: got %v, want true (OQ-SEC-D-5 default-on)", cfg.FGA.RegisterDrainer.Enable)
	}
	if cfg.FGA.RegisterDrainer.MaxAttempts != 10 {
		t.Errorf("default FGA.RegisterDrainer.MaxAttempts: got %d, want 10", cfg.FGA.RegisterDrainer.MaxAttempts)
	}
	if cfg.InternalLifecycle.MaxStreams != 32 {
		t.Errorf("default InternalLifecycle.MaxStreams: got %d, want 32", cfg.InternalLifecycle.MaxStreams)
	}
}

// TestLoad_DefaultMode_FailsClosedProduction — безопасный дефолт fail-closed:
// пропущенный `mode` резолвится в production (security.md «Любой деплой —
// production-mode»), поэтому конфиг без mTLS/iam НЕ грузится молча в dev-режиме
// (CWE-1188 insecure default). Раньше default был `dev` → отсутствие ключа
// молча снимало authN/authZ.
func TestLoad_DefaultMode_FailsClosedProduction(t *testing.T) {
	// Только required url; mode НЕ задан → default production → Validate
	// обязана потребовать production-гварды (mtls.server / iam-register / ...).
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://u:p@h/d?sslmode=require")
	_, err := Load("")
	if err == nil {
		t.Fatalf("unset mode must default to production and fail closed without mTLS/iam, got nil error")
	}
	if !strings.Contains(err.Error(), "production mode") {
		t.Errorf("expected production-mode guard error (fail-closed default), got: %v", err)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("KACHO_NLB_MODE", "dev") // dev-opt-in: default fail-closed production
	// ENV: KACHO_NLB_REPOSITORY__POSTGRES__URL → repository.postgres.url
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://envuser:envpass@envhost/kacho_nlb")
	t.Setenv("KACHO_NLB_LOGGER__LEVEL", "WARN")
	t.Setenv("KACHO_NLB_AUTHZ__IAM__ADDR", "iam.kacho.svc:9091")

	cfg, err := Load("") // только defaults + ENV
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Repository.Postgres.URL; got != "postgres://envuser:envpass@envhost/kacho_nlb" {
		t.Errorf("Repository.Postgres.URL from ENV: got %q", got)
	}
	if got := cfg.Logger.Level; got != "WARN" {
		t.Errorf("Logger.Level from ENV: got %q", got)
	}
	if got := cfg.Authz.IAM.Addr; got != "iam.kacho.svc:9091" {
		t.Errorf("Authz.IAM.Addr from ENV: got %q", got)
	}
}

func TestLoad_YAMLPlusEnvPrecedence(t *testing.T) {
	// viper: ENV > config-file. Поднимем YAML с url=postgres://yaml/...,
	// затем поверх ENV перекроет.
	yaml := `
mode: dev
logger:
  level: INFO
api-server:
  endpoint: tcp://0.0.0.0:9090
  internal-endpoint: tcp://0.0.0.0:9091
  graceful-shutdown: 10s
repository:
  type: POSTGRES
  postgres:
    url: postgres://yaml@host/db
`
	t.Setenv("KACHO_NLB_REPOSITORY__POSTGRES__URL", "postgres://env@host/db")
	cfg, err := Load(writeYAML(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Repository.Postgres.URL; got != "postgres://env@host/db" {
		t.Errorf("ENV must override YAML: got %q", got)
	}
}

func TestLoad_MissingRequired_Postgres(t *testing.T) {
	// Никакого YAML, никакого ENV для url — Validate должен пожаловаться.
	cfg, err := Load("")
	if err == nil {
		t.Fatalf("expected validation error for missing repository.postgres.url, got cfg=%+v", cfg)
	}
	if !strings.Contains(err.Error(), "repository.postgres.url") {
		t.Errorf("error must mention repository.postgres.url, got: %v", err)
	}
}

// TestLoad_PasswordFromEnv_SubstitutesPlaceholder — regression.
//
// Helm рендерит `postgres.url` с shell-style placeholder
// `$(KACHO_NLB_DB_PASSWORD)` (password — Secret, не в ConfigMap). Viper НЕ
// expand'ит `$(VAR)` синтаксис — без подстановки migrator передаёт literal
// строку в pgx → connection fail → init-container CrashLoopBackOff.
//
// Load обязана: если `password-from-env: <NAME>` задан и URL содержит
// `$(<NAME>)`-placeholder — substitution из env при Load.
func TestLoad_PasswordFromEnv_SubstitutesPlaceholder(t *testing.T) {
	t.Setenv("KACHO_NLB_DB_PASSWORD", "secret-pw-123")

	yaml := `
mode: dev
logger:
  level: INFO
api-server:
  endpoint: tcp://0.0.0.0:9090
  internal-endpoint: tcp://0.0.0.0:9091
  graceful-shutdown: 10s
repository:
  type: POSTGRES
  postgres:
    url: postgres://kacho_nlb:$(KACHO_NLB_DB_PASSWORD)@pg-nlb:5432/kacho_nlb?sslmode=disable
    password-from-env: KACHO_NLB_DB_PASSWORD
`
	cfg, err := Load(writeYAML(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := "postgres://kacho_nlb:secret-pw-123@pg-nlb:5432/kacho_nlb?sslmode=disable"
	if got := cfg.Repository.Postgres.URL; got != want {
		t.Errorf("Repository.Postgres.URL: placeholder must be expanded\n got: %q\nwant: %q", got, want)
	}
	if got := cfg.Repository.Postgres.PasswordFromEnv; got != "KACHO_NLB_DB_PASSWORD" {
		t.Errorf("Repository.Postgres.PasswordFromEnv: got %q, want %q", got, "KACHO_NLB_DB_PASSWORD")
	}
}

// TestLoad_PasswordFromEnv_UnsetVar — env-var не задан → placeholder
// остаётся как есть; ошибка валидации Postgres URL произойдёт позже на
// connect-step (тут — конфиг просто грузится без панcики).
func TestLoad_PasswordFromEnv_UnsetVar(t *testing.T) {
	// Гарантируем, что переменная не утекла из других тестов.
	t.Setenv("KACHO_NLB_DB_PASSWORD_MISSING", "")
	yaml := `
mode: dev
logger:
  level: INFO
api-server:
  endpoint: tcp://0.0.0.0:9090
  internal-endpoint: tcp://0.0.0.0:9091
  graceful-shutdown: 10s
repository:
  type: POSTGRES
  postgres:
    url: postgres://user:$(KACHO_NLB_DB_PASSWORD_MISSING)@h/d
    password-from-env: KACHO_NLB_DB_PASSWORD_MISSING
`
	cfg, err := Load(writeYAML(t, yaml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Placeholder остаётся литералом — failure surface на connect, не silent
	// «постгрес с пустым паролем».
	want := "postgres://user:$(KACHO_NLB_DB_PASSWORD_MISSING)@h/d"
	if got := cfg.Repository.Postgres.URL; got != want {
		t.Errorf("URL must be left intact when env-var unset\n got: %q\nwant: %q", got, want)
	}
}

func TestLoad_ConfigFileMissing(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error must mention 'does not exist', got: %v", err)
	}
}
