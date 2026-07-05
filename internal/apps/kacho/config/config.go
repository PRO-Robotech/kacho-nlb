// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package config — viper-based YAML config для kacho-nlb.
//
// Иерархия секций — спека `docs/superpowers/specs/2026-05-23-kacho-nlb-design.md `:
//
//	logger / api-server / metrics / healthcheck / repository.postgres /
//	authn / extapi / authz.iam (+ cache, listen-invalidator) /
//	fga.register-drainer  / mtls (opt-in).
//
// ENV-binding через viper с делимитером `__`:
//
//	KACHO_NLB_API_SERVER__ENDPOINT              → api-server.endpoint
//	KACHO_NLB_REPOSITORY__POSTGRES__URL         → repository.postgres.url
//	KACHO_NLB_AUTHZ__IAM__ADDR                  → authz.iam.addr
//	KACHO_NLB_FGA__REGISTER_DRAINER__ENABLE     → fga.register-drainer.enable
//	KACHO_NLB_LOGGER__LEVEL                     → logger.level
//
// Defaults — в `defaults.go` (`RegisterDefaults`); validation — в `validate.go`
// (`Config.Validate`, `Mode` enum); loader — в `load.go` (`Load(path)`).
//
// **Anti-pattern protection :**
//   - НЕ envconfig в struct tags — только mapstructure через viper.
//   - НЕ defaults в struct tags — только в `RegisterDefaults`.
//   - НЕ `bool productionMode` — `Mode` enum (ModeDev / ModeProduction).
//   - НЕ `cfg.AuthMode` — `cfg.Mode` (общий режим работы, а не «auth mode»).
package config

import (
	"time"

	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
)

// Config — корневой config kacho-nlb. Все вложенные структуры с mapstructure-тегами
// для viper-биндинга; defaults — в `defaults.go`, validation — в `validate.go`.
type Config struct {
	// Mode — общий режим работы (dev / production). Production-mode требует
	// TLS + не-пустую FGA endpoint + не-пустой Postgres DSN. См. validate.go.
	// Хранится как строка в YAML (`mode: production` / `mode: dev`), мапится
	// на enum через `ParseMode`.
	ModeRaw string `mapstructure:"mode"`

	Logger      LoggerConfig      `mapstructure:"logger"`
	APIServer   APIServerConfig   `mapstructure:"api-server"`
	Metrics     MetricsConfig     `mapstructure:"metrics"`
	Healthcheck HealthcheckConfig `mapstructure:"healthcheck"`
	Repository  RepositoryConfig  `mapstructure:"repository"`
	Authn       AuthnConfig       `mapstructure:"authn"`
	ExtAPI      ExtAPIConfig      `mapstructure:"extapi"`
	Authz       AuthzConfig       `mapstructure:"authz"`
	FGA         FGAConfig         `mapstructure:"fga"`
	MTLS        MTLSConfig        `mapstructure:"mtls"`
	Jobs        JobsConfig        `mapstructure:"jobs"`

	// InternalLifecycle — параметры InternalResourceLifecycleService.Subscribe
	// (server-stream к kacho-iam, см.
	// `internal/apps/kacho/api/internal_lifecycle`).
	InternalLifecycle InternalLifecycleConfig `mapstructure:"internal-lifecycle"`
}

// Mode возвращает резолвленный enum-режим (после `Validate`).
func (c Config) Mode() ModeEnum {
	m, _ := ParseMode(c.ModeRaw)
	return m
}

// ─── Logger ─────────────────────────────────────────────────────────────────

type LoggerConfig struct {
	Level string `mapstructure:"level"` // FATAL|ERROR|WARN|INFO|DEBUG; default DEBUG
}

// ─── API-server ──────────────────────────────────────────────────────────────

type APIServerConfig struct {
	Endpoint          string        `mapstructure:"endpoint"`          // tcp://0.0.0.0:9090
	InternalEndpoint  string        `mapstructure:"internal-endpoint"` // tcp://0.0.0.0:9091
	GracefulShutdown  time.Duration `mapstructure:"graceful-shutdown"` // default 10s
	GRPCGatewayEnable bool          `mapstructure:"grpc-gw-enable"`    // false (gateway = separate svc)
}

// ─── Metrics / Healthcheck ───────────────────────────────────────────────────

type MetricsConfig struct {
	Enable bool `mapstructure:"enable"`
	// Address — listen-адрес cluster-internal diagnostic HTTP-listener'а
	// (metrics + /healthz + /readyz). Default `:9101` (см. RegisterDefaults).
	// Пустая строка ИЛИ enable=false → listener не поднимается (back-compat).
	Address string `mapstructure:"address"`
}

type HealthcheckConfig struct {
	Enable bool `mapstructure:"enable"`
}

// ─── Repository ──────────────────────────────────────────────────────────────

type RepositoryConfig struct {
	Type     string         `mapstructure:"type"` // POSTGRES
	Postgres PostgresConfig `mapstructure:"postgres"`
}

type PostgresConfig struct {
	URL          string        `mapstructure:"url"`           // postgres://user:pass@host/kacho_nlb
	MaxConns     int32         `mapstructure:"max-conns"`     // pool size; 0 → pgxpool default
	ConnLifetime time.Duration `mapstructure:"conn-lifetime"` // 0 → pgxpool default
	SlaveURL     string        `mapstructure:"slave-url"`     // optional read-replica DSN

	// PasswordFromEnv — имя ENV-переменной с паролем, который подставляется
	// в URL/SlaveURL по shell-placeholder'у `$(<ИМЯ>)` на этапе Load (см.
	// load.go::expandPasswordFromEnv). Пароль — Secret в Helm, в ConfigMap
	// его держать нельзя; viper не понимает `$(VAR)` синтаксис, поэтому
	// expand делаем явно. Пустая строка — substitution отключён (URL
	// используется как есть). regression-fix.
	PasswordFromEnv string `mapstructure:"password-from-env"`
}

// ─── Authn (transport TLS) ───────────────────────────────────────────────────

// AuthnConfig — TLS-параметры серверной части (опционально). См.
type AuthnConfig struct {
	Type string         `mapstructure:"type"` // none | tls
	TLS  AuthnTLSConfig `mapstructure:"tls"`
}

type AuthnTLSConfig struct {
	KeyFile  string               `mapstructure:"key-file"`
	CertFile string               `mapstructure:"cert-file"`
	Client   AuthnTLSClientConfig `mapstructure:"client"`
}

type AuthnTLSClientConfig struct {
	Verify  string   `mapstructure:"verify"`   // skip | certs-required | verify
	CAFiles []string `mapstructure:"ca-files"` // PEM-bundle for client-cert verification
}

// ─── ExtAPI (peer gRPC clients: vpc / compute / iam) ─────────────────────────

type ExtAPIConfig struct {
	DefDialDuration time.Duration  `mapstructure:"def-dial-duration"` // default 10s
	VPC             ExtAPIEndpoint `mapstructure:"vpc"`
	Compute         ExtAPIEndpoint `mapstructure:"compute"`
	IAM             ExtAPIEndpoint `mapstructure:"iam"`
	// Geo — kacho-geo (Geography Region/Zone, leaf-owner; kacho-geo).
	// NetworkLoadBalancer.region_id / TargetGroup.region_id валидируются через
	// geo.RegionService.Get (sync precheck). Ребро nlb→geo заменяет прежнее
	// nlb→compute «ради region»; nlb→compute остаётся для InstanceService
	// (instance-resolve — НЕ geography). Addr биндится также из явной ENV
	// `KACHO_NLB_GEO_GRPC_ADDR` (BindEnv в defaults.go).
	Geo ExtAPIEndpoint `mapstructure:"geo"`
}

// ExtAPIEndpoint — параметры одного peer-сервиса. Public/Internal — два
// отдельных адреса (NLB зовёт internal-сервисы напрямую через cluster-internal
// :9091, не через api-gateway). TLS опционален.
type ExtAPIEndpoint struct {
	Addr         string        `mapstructure:"addr"`          // host:port (public RPC)
	InternalAddr string        `mapstructure:"internal-addr"` // host:port (internal :9091)
	DialDuration time.Duration `mapstructure:"dial-duration"` // 0 → берётся ExtAPI.DefDialDuration
	TLS          bool          `mapstructure:"tls"`           // production-mode требует true
}

// ─── Authz (FGA Check + cache + listen-invalidator) ──────────────────────────

type AuthzConfig struct {
	IAM               AuthzIAMConfig               `mapstructure:"iam"`
	Cache             AuthzCacheConfig             `mapstructure:"cache"`
	ListenInvalidator AuthzListenInvalidatorConfig `mapstructure:"listen-invalidator"`
	ListFilter        AuthzListFilterConfig        `mapstructure:"list-filter"`
	Breakglass        bool                         `mapstructure:"breakglass"` // dev-only; production validation rejects

	// TrustedForwarderSANs — allow-list cert-identity SAN'ов, которым разрешено
	// форвардить end-user principal-metadata (обычно единственный — api-gateway SA).
	// Пробрасывается в grpcsrv.UnaryTrustedPrincipalExtract(WithTrustedForwarders)
	// на ОБОИХ листенерах. Пусто (default) → любой mTLS-verified peer доверен как
	// форвардер (паритет с insecure dev back-compat и kacho-iam internal); задаётся в
	// production для defense-in-depth против confused-deputy (внутренний сервис со
	// своим валидным cert'ом не может выдать себя за пользователя). ENV
	// `KACHO_NLB_AUTHZ__TRUSTED_FORWARDER_SANS` (comma-separated).
	TrustedForwarderSANs []string `mapstructure:"trusted-forwarder-sans"`
}

// AuthzListFilterConfig — per-object filtered List (RBAC).
// Каждый публичный List<Resource> прогоняет id-set через
// iam.AuthorizeService.ListObjects(subject, action, "lb_*") и отдаёт пересечение
// (только доступные объекты), read==enforce, fail-closed (security.md). Endpoint
// iam (там AuthorizeService); по умолчанию переиспользуется conn, которым nlb
// уже зовёт iam (см. main.go buildListFilter), mTLS — через mtls.iam-register.
type AuthzListFilterConfig struct {
	// Enabled — master-switch. default true (D «применяется во всех доменах»).
	// false → use-case'ы получают nil-Filter → нефильтрованный project-scoped
	// passthrough (dev / graceful start без iam).
	Enabled bool `mapstructure:"enabled"`
	// Timeout — per-request deadline к iam.ListObjects (default 500ms).
	Timeout time.Duration `mapstructure:"timeout"`
	// CacheTTL — TTL decision-cache (default 5s; ≤10s).
	CacheTTL time.Duration `mapstructure:"cache-ttl"`
	// CacheMaxEntries — bound cache + MaxResults cap (default 10000).
	CacheMaxEntries int `mapstructure:"cache-max-entries"`
	// FailOpen — на FGA error: false (default) → Unavailable (fail-closed,
	// security.md); true → bypass + audit-warn (dev escape-hatch).
	FailOpen bool `mapstructure:"fail-open"`
}

type AuthzIAMConfig struct {
	Addr           string        `mapstructure:"addr"`            // iam.kacho.svc.cluster.local:9091
	DialDeadline   time.Duration `mapstructure:"dial-deadline"`   // default 3s
	RequestTimeout time.Duration `mapstructure:"request-timeout"` // default 500ms
}

type AuthzCacheConfig struct {
	Enable bool          `mapstructure:"enable"` // default true (positive-only)
	TTL    time.Duration `mapstructure:"ttl"`    // default 5s (≤10s)
}

type AuthzListenInvalidatorConfig struct {
	Enable       bool   `mapstructure:"enable"`  // LISTEN kacho_iam_subjects on iam-PG
	Channel      string `mapstructure:"channel"` // default "kacho_iam_subjects"
	IAMDirectDSN string `mapstructure:"iam-dsn"` // dedicated pgx conn to iam-DB (optional)
}

// ─── FGA (owner-tuple registration via IAM) ───────────────────────────

// FGAConfig — kacho-nlb НЕ ходит в FGA напрямую.
// Прямой best-effort tuple-write (OpenFGA endpoint/store/model) удалён; вместо него
// owner-hierarchy tuple пишется register-intent'ом в `fga_register_outbox` в той же
// writer-tx (Вариант A), а register-drainer применяет его через kacho-iam
// InternalIAMService.RegisterResource/UnregisterResource по mTLS.
type FGAConfig struct {
	// RegisterDrainer — register-drainer (fga_register_outbox → IAM
	// RegisterResource/UnregisterResource).: default-on (без него
	// созданные ресурсы не получат owner-tuple — деградация хуже текущей).
	RegisterDrainer FGARegisterDrainerConfig `mapstructure:"register-drainer"`

	// RequireIAM — fail-closed boot-gate. Когда true,
	// мутирующий Create отказывает (UNAVAILABLE) и readiness=NotReady, пока
	// register-drainer не подключён к IAM — ни один ресурс не создаётся без
	// доставляемого owner-tuple-intent'а. Default false (dev back-compat). В
	// production: true (единый канонический режим, N5). ENV
	// `KACHO_NLB_REQUIRE_IAM` (явный BindEnv в defaults.go).
	RequireIAM bool `mapstructure:"require-iam"`
}

// FGARegisterDrainerConfig — параметры corelib outbox/drainer на таблице
// `kacho_nlb.fga_register_outbox` (Вариант A). Drainer — внутренняя
// goroutine (не cross-cluster flag); под FOR UPDATE SKIP LOCKED claim одна
// реплика дренит каждую строку (exactly-once across pods).
type FGARegisterDrainerConfig struct {
	Enable       bool          `mapstructure:"enable"`        // default true
	BatchSize    int           `mapstructure:"batch-size"`    // default 32
	PollFallback time.Duration `mapstructure:"poll-fallback"` // default 30s (missed-NOTIFY safety)
	MaxAttempts  int           `mapstructure:"max-attempts"`  // default 10 (poison threshold)
	BackoffMin   time.Duration `mapstructure:"backoff-min"`   // default 1s
	BackoffMax   time.Duration `mapstructure:"backoff-max"`   // default 30s
}

// ─── mTLS (opt-in, per-edge) ───────────────────────────────────────────

// MTLSConfig — per-edge mTLS via corelib value-structs. enable=false
// (default) → insecure (dev backward-compat). Каждое ребро —
// независимый enable (rollback per-edge).
//
// ENV (viper, делимитер `.`→`__`; hyphen in a section name stays literal):
// mapstructure лоуэркейсит имена полей corelib-структур →
// `enable`/`certfile`/`keyfile`/`clientcafiles`/`cafiles`/`servername`:
//
//	KACHO_NLB_MTLS__SERVER__ENABLE                 → server listener mTLS
//	KACHO_NLB_MTLS__SERVER__CERTFILE / __KEYFILE / __CLIENTCAFILES
//	KACHO_NLB_MTLS__IAM-REGISTER__ENABLE           → nlb→iam internal :9091
//	KACHO_NLB_MTLS__IAM-REGISTER__CERTFILE / __KEYFILE / __CAFILES / __SERVERNAME
//	KACHO_NLB_MTLS__IAM-PROJECT__ENABLE            → nlb→iam public :9090
//	KACHO_NLB_MTLS__IAM-PROJECT__CERTFILE / __KEYFILE / __CAFILES / __SERVERNAME
//	KACHO_NLB_MTLS__VPC__*                         → nlb→vpc
//	KACHO_NLB_MTLS__COMPUTE__*                     → nlb→compute (Instance-resolve)
//	KACHO_NLB_MTLS__GEO__*                         → nlb→geo (RegionService.Get)
type MTLSConfig struct {
	// Server — server-cert на public+internal listener'ах (RequireAndVerify-
	// ClientCert при enable=true).
	Server grpcsrv.TLSServer `mapstructure:"server"`
	// IAMRegister — client-cert на ВНУТРЕННЕМ ребре nlb→iam-internal (9091):
	// InternalIAMService.Check (per-RPC authz-gate) + RegisterResource/
	// UnregisterResource (register-drainer). ServerName =
	// kacho-iam-internal.* (фактический :9091 dial-host).:
	// этот же conn несёт Check, поэтому read/authz authz-ребро покрыто им.
	IAMRegister grpcclient.TLSClient `mapstructure:"iam-register"`
	// IAMProject — client-cert на ПУБЛИЧНОМ ребре nlb→iam (9090):
	// ProjectService.Get (existence + leaf-owner). ((b),
	// per-listener split): отдельное поле, потому что public dial-host =
	// kacho-iam.* (≠ kacho-iam-internal.*) и единый ServerName не может быть
	// корректен для обоих listener'ов под RequireAndVerifyClientCert (
	// latent-bug). ServerName = kacho-iam.* (фактический :9090 dial-host).
	IAMProject grpcclient.TLSClient `mapstructure:"iam-project"`
	// VPC — client-cert на ребре nlb→vpc (Address/Subnet/NIC IPAM).
	VPC grpcclient.TLSClient `mapstructure:"vpc"`
	// Compute — client-cert на ребре nlb→compute (Instance-resolve; geography
	// region-валидация перенесена на ребро nlb→geo, см. Geo ниже).
	Compute grpcclient.TLSClient `mapstructure:"compute"`
	// Geo — client-cert на ребре nlb→geo (RegionService.Get, kacho-geo).
	Geo grpcclient.TLSClient `mapstructure:"geo"`
}

// ─── Jobs (background workers) ───────────────────────────────────────────────

// JobsConfig — конфигурация фоновых worker'ов (см. internal/apps/kacho/jobs).
type JobsConfig struct {
	TargetDrain TargetDrainConfig `mapstructure:"target-drain"`
	FreeIP      FreeIPConfig      `mapstructure:"free-ip"`
}

// TargetDrainConfig — параметры двухфазного drain runner.
type TargetDrainConfig struct {
	// Interval — период между тиками drain-runner'а. Default 10s
	// (см. RegisterDefaults). Должен быть > 0.
	Interval time.Duration `mapstructure:"interval"`
}

// FreeIPConfig — параметры free_ip_runner (reconcile застрявших листенеров:
// durable-handle create-orphan 'CREATING' + незавершённый Delete 'DELETING').
type FreeIPConfig struct {
	// Interval — период между тиками reconciler'а. Default 30s (сироты редки,
	// чаще не нужно). Должен быть > 0.
	Interval time.Duration `mapstructure:"interval"`
	// AgeThreshold — минимальный возраст строки (по updated_at), при котором она
	// считается «застрявшей»: свежий in-flight create/delete (моложе порога) не
	// трогается, пока легитимный worker дорабатывает. Default 5m. Должен быть > 0.
	AgeThreshold time.Duration `mapstructure:"age-threshold"`
}

// ─── InternalLifecycle (stream) ─────────────────────────────────────────

// InternalLifecycleConfig — параметры InternalResourceLifecycleService.Subscribe.
// Server-stream к kacho-iam для FGA tuple-sync; cluster-internal
// only (port 9091, Internal-only).
type InternalLifecycleConfig struct {
	// MaxStreams — максимальное число одновременных Subscribe-стримов.
	// Каждый стрим держит dedicated pgx.Conn для LISTEN/NOTIFY (вне pool'а),
	// поэтому слот ≈ +1 conn'у к Postgres. Default 32 (см. RegisterDefaults).
	// Должен быть > 0.
	MaxStreams int `mapstructure:"max-streams"`
}
