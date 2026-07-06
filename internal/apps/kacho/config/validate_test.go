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
		{"", ModeProduction, false}, // unset/empty fails closed (security.md)
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
			Postgres: PostgresConfig{URL: "postgres://u:p@h/d?sslmode=require"},
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
	cfg.Authz.ListFilter.Enabled = true
	cfg.Authz.TrustedForwarderSANs = []string{"spiffe://kacho.cloud/ns/kacho/sa/api-gateway"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + secure transport: unexpected err: %v", err)
	}
}

// TestValidate_Production_RejectsDeadAuthnTLS — authn.type=tls в production ОБЯЗАН
// быть отвергнут: это МЁРТВОЕ значение. cfg.Authn не проброшен ни в один транспорт
// (server-creds строятся исключительно из grpcsrv.TLSServerCreds(cfg.MTLS.Server) в
// composition root), поэтому authn.type=tls НЕ настраивает TLS на listener'ах — boot
// поднял бы plaintext gRPC на public И internal :9091, доверяя client-asserted
// principal без mTLS (CWE-319/CWE-290). Fail-closed: prod требует
// mtls.server.enable=true; dead authn.type=tls отвергается, чтобы оператор не принял
// его за реальную transport-security. RED до фикса (сейчас проходит как «one-way TLS»).
func TestValidate_Production_RejectsDeadAuthnTLS(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.ModeRaw = "production"
	cfg.Authz.IAM.Addr = "iam.kacho.svc:9091"
	cfg.MTLS.IAMRegister.Enable = true
	cfg.Authz.ListFilter.Enabled = true
	// authn.type=tls (мёртвое значение) вместо mtls.server → должно быть отвергнуто.
	cfg.Authn.Type = "tls"
	cfg.Authn.TLS.KeyFile = "/etc/tls/key.pem"
	cfg.Authn.TLS.CertFile = "/etc/tls/cert.pem"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "authn.type=tls") {
		t.Fatalf("expected dead authn.type=tls rejection in production, got %v", err)
	}
}

// TestValidate_Production_RejectsDeadAuthnTLS_EvenWithServerMTLS — dead authn.type=tls
// отвергается в production ДАЖЕ когда реальный server-mTLS уже включён: knob мёртвый и
// вводит оператора в заблуждение, поэтому его присутствие в prod-конфиге — ошибка (не
// silent-ignore). RED до фикса (mtls.server удовлетворял serverSecure, authn.type
// молча принимался).
func TestValidate_Production_RejectsDeadAuthnTLS_EvenWithServerMTLS(t *testing.T) {
	cfg := productionSecureConfig() // mtls.server.enable=true, forwarder-sans set
	cfg.Authn.Type = "tls"
	cfg.Authn.TLS.KeyFile = "/etc/tls/key.pem"
	cfg.Authn.TLS.CertFile = "/etc/tls/cert.pem"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "authn.type=tls") {
		t.Fatalf("expected dead authn.type=tls rejection in production even with server mTLS, got %v", err)
	}
}

// productionSecureConfig — production-mode Config, прошедшая transport/iam-edge
// gate'ы (secure server + mTLS iam-edge + list-filter fail-closed). Базис для
// изоляции list-filter-специфичных проверок.
func productionSecureConfig() Config {
	cfg := minimalValidConfig()
	cfg.ModeRaw = "production"
	cfg.Authz.IAM.Addr = "iam.kacho.svc:9091"
	cfg.MTLS.Server.Enable = true
	cfg.MTLS.IAMRegister.Enable = true
	cfg.Authz.ListFilter.Enabled = true
	cfg.Authz.ListFilter.FailOpen = false
	cfg.Authz.TrustedForwarderSANs = []string{"spiffe://kacho.cloud/ns/kacho/sa/api-gateway"}
	return cfg
}

// TestValidate_Production_RequiresListFilterEnabled — production с
// authz.list-filter.enabled=false обязан fail-close'иться: это единственный
// authorization-слой для ScopeFiltered List RPC (interceptor их пропускает) —
// отключение превращает List в нефильтрованный project-scoped passthrough
// (cross-tenant enumeration).
func TestValidate_Production_RequiresListFilterEnabled(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.Authz.ListFilter.Enabled = false
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "list-filter.enabled") {
		t.Fatalf("expected list-filter.enabled error in production, got %v", err)
	}
}

// TestValidate_Production_ForbidsListFilterFailOpen — production с
// authz.list-filter.fail-open=true обязан fail-close'иться: fail-open отдаёт
// нефильтрованные результаты при недоступном IAM/FGA (cross-tenant enumeration
// во время outage). В проде — только fail-closed.
func TestValidate_Production_ForbidsListFilterFailOpen(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.Authz.ListFilter.FailOpen = true
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "list-filter.fail-open") {
		t.Fatalf("expected list-filter.fail-open error in production, got %v", err)
	}
}

// TestValidate_Production_ListFilterFailClosed_OK — production с list-filter
// enabled + fail-closed проходит (позитивная ветка).
func TestValidate_Production_ListFilterFailClosed_OK(t *testing.T) {
	cfg := productionSecureConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + list-filter fail-closed: unexpected err: %v", err)
	}
}

// TestValidate_Dev_ListFilterDisabled_OK — в dev-mode list-filter.enabled=false
// допустим (graceful start без iam); prod-gate не применяется.
func TestValidate_Dev_ListFilterDisabled_OK(t *testing.T) {
	cfg := minimalValidConfig() // mode=dev
	cfg.Authz.ListFilter.Enabled = false
	cfg.Authz.ListFilter.FailOpen = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dev + list-filter disabled/fail-open: unexpected err: %v", err)
	}
}

// TestValidate_Production_MTLSRequiresTrustedForwarderSANs — production с mTLS-
// server и ПУСТЫМ authz.trusted-forwarder-sans обязан fail-close'иться: пустой
// allow-list в grpcsrv означает «доверять форвардинг x-kacho-principal-* ЛЮБОМУ
// mTLS-verified peer'у» → в общем mTLS-mesh любой воркер с валидным клиентским
// cert'ом может форжить произвольного principal'а (confused-deputy). В проде с
// mTLS-server allow-list обязан быть непустым (перечисляет SAN api-gateway).
func TestValidate_Production_MTLSRequiresTrustedForwarderSANs(t *testing.T) {
	cfg := productionSecureConfig() // mtls.server.enable=true
	cfg.Authz.TrustedForwarderSANs = nil
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "trusted-forwarder-sans") {
		t.Fatalf("expected trusted-forwarder-sans error in production+mTLS, got %v", err)
	}
}

// TestValidate_Production_MTLSWithTrustedForwarderSANs_OK — production+mTLS с
// непустым allow-list проходит (позитивная ветка).
func TestValidate_Production_MTLSWithTrustedForwarderSANs_OK(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.Authz.TrustedForwarderSANs = []string{"spiffe://kacho.cloud/ns/kacho/sa/api-gateway"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + mTLS + forwarder allow-list: unexpected err: %v", err)
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

// ─── Peer-edge transport fail-closed ─────────────────────────────────────────
//
// В production каждое СКОНФИГУРИРОВАННОЕ (addr задан) cross-service ребро
// (vpc / compute / geo / iam-project) обязано иметь transport-security: mTLS
// (mtls.<edge>.enable=true) ЛИБО one-way TLS (<edge>.tls=true). Иначе nlb дилит
// peer по plaintext gRPC (IPAM/instance-resolve/region) → on-path
// read/tamper (CWE-319). productionSecureConfig() не задаёт peer-addr'ов, поэтому
// базовый secure-config эти проверки не триггерит (backward-compat).

func TestValidate_Production_VPCEdgeInsecure(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.ExtAPI.VPC.Addr = "vpc.kacho.svc:9090" // configured, no mTLS, no tls
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure peer transport on vpc") {
		t.Fatalf("expected insecure-vpc-edge error in production, got %v", err)
	}
}

func TestValidate_Production_ComputeEdgeInsecure(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.ExtAPI.Compute.Addr = "compute.kacho.svc:9090"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure peer transport on compute") {
		t.Fatalf("expected insecure-compute-edge error in production, got %v", err)
	}
}

func TestValidate_Production_GeoEdgeInsecure(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.ExtAPI.Geo.Addr = "geo.kacho.svc:9090"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure peer transport on geo") {
		t.Fatalf("expected insecure-geo-edge error in production, got %v", err)
	}
}

func TestValidate_Production_IAMProjectEdgeInsecure(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.ExtAPI.IAM.Addr = "iam.kacho.svc:9090" // ProjectService.Get public edge
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure peer transport on iam-project") {
		t.Fatalf("expected insecure-iam-project-edge error in production, got %v", err)
	}
}

// mTLS на ребре снимает ошибку.
func TestValidate_Production_VPCEdgeMTLS_OK(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.ExtAPI.VPC.Addr = "vpc.kacho.svc:9090"
	cfg.MTLS.VPC.Enable = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + vpc mTLS edge: unexpected err: %v", err)
	}
}

// One-way TLS bool на ребре тоже снимает ошибку (минимальный уровень).
func TestValidate_Production_ComputeEdgeOneWayTLS_OK(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.ExtAPI.Compute.Addr = "compute.kacho.svc:9090"
	cfg.ExtAPI.Compute.TLS = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + compute one-way TLS edge: unexpected err: %v", err)
	}
}

// Ребро, заданное только через internal-addr, тоже проверяется.
func TestValidate_Production_GeoEdgeInternalAddrInsecure(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.ExtAPI.Geo.InternalAddr = "geo-internal.kacho.svc:9091"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure peer transport on geo") {
		t.Fatalf("expected insecure-geo-edge (internal-addr) error, got %v", err)
	}
}

// В dev-mode plaintext peer-ребро допустимо (prod-gate не применяется).
func TestValidate_Dev_InsecurePeerEdges_OK(t *testing.T) {
	cfg := minimalValidConfig() // mode=dev
	cfg.ExtAPI.VPC.Addr = "vpc.kacho.svc:9090"
	cfg.ExtAPI.Compute.Addr = "compute.kacho.svc:9090"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dev + insecure peer edges: unexpected err: %v", err)
	}
}

// --- Postgres transport fail-closed (CWE-319) ------------------
// В production DSN с `sslmode=disable`/`allow`/`prefer` (или без sslmode вовсе)
// → plaintext-совместимое DB-соединение: credentials + tenant-данные идут по
// сети в открытую. Boot обязан отвергнуть такой prod-конфиг (parity с peer-edge
// insecure-transport gate'ами выше).

func TestValidate_Production_RejectsSSLModeDisable(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.Repository.Postgres.URL = "postgres://u:p@h/d?sslmode=disable&search_path=kacho_nlb,public"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure Postgres transport") {
		t.Fatalf("expected insecure-Postgres-transport error for sslmode=disable, got %v", err)
	}
}

func TestValidate_Production_RejectsSSLModePrefer(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.Repository.Postgres.URL = "postgres://u:p@h/d?sslmode=prefer"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure Postgres transport") {
		t.Fatalf("expected insecure-Postgres-transport error for sslmode=prefer, got %v", err)
	}
}

func TestValidate_Production_RejectsMissingSSLMode(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.Repository.Postgres.URL = "postgres://u:p@h/d" // no sslmode → libpq default 'prefer' (plaintext fallback)
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "insecure Postgres transport") {
		t.Fatalf("expected insecure-Postgres-transport error for missing sslmode, got %v", err)
	}
}

func TestValidate_Production_SSLModeRequire_OK(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.Repository.Postgres.URL = "postgres://u:p@h/d?sslmode=require&search_path=kacho_nlb,public"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + sslmode=require: unexpected err: %v", err)
	}
}

func TestValidate_Production_SSLModeVerifyFull_OK(t *testing.T) {
	cfg := productionSecureConfig()
	cfg.Repository.Postgres.URL = "postgres://u:p@h/d?sslmode=verify-full"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("production + sslmode=verify-full: unexpected err: %v", err)
	}
}

// В dev-mode plaintext DB-соединение допустимо (local-стенд / testcontainers).
func TestValidate_Dev_SSLModeDisable_OK(t *testing.T) {
	cfg := minimalValidConfig() // mode=dev
	cfg.Repository.Postgres.URL = "postgres://u:p@h/d?sslmode=disable"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dev + sslmode=disable: unexpected err: %v", err)
	}
}
