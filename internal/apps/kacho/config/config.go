// Package config — viper-based YAML config для kacho-nlb (evgeniy §8.J / KAC-160).
//
// Иерархия секций — спека `docs/superpowers/specs/2026-05-23-kacho-nlb-design.md §6.9`:
//
//	logger / api-server / metrics / healthcheck / repository.postgres /
//	authn / extapi / authz.iam (+ cache, listen-invalidator) / fga.tuple-write.
//
// ENV-binding через viper с делимитером `__`:
//
//	KACHO_NLB_API_SERVER__ENDPOINT           → api-server.endpoint
//	KACHO_NLB_REPOSITORY__POSTGRES__URL      → repository.postgres.url
//	KACHO_NLB_AUTHZ__IAM__ADDR               → authz.iam.addr
//	KACHO_NLB_FGA__TUPLE_WRITE__TIMEOUT      → fga.tuple-write.timeout
//	KACHO_NLB_LOGGER__LEVEL                  → logger.level
//
// Defaults — в `defaults.go` (`RegisterDefaults`); validation — в `validate.go`
// (`Config.Validate()`, `Mode` enum); loader — в `load.go` (`Load(path)`).
//
// **Anti-pattern protection (evgeniy §J.2/J.3/J.6):**
//   - НЕ envconfig в struct tags — только mapstructure через viper.
//   - НЕ defaults в struct tags — только в `RegisterDefaults`.
//   - НЕ `bool productionMode` — `Mode` enum (ModeDev / ModeProduction).
//   - НЕ `cfg.AuthMode` — `cfg.Mode` (общий режим работы, а не «auth mode»).
package config

import "time"

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
	Jobs        JobsConfig        `mapstructure:"jobs"`

	// InternalLifecycle — параметры InternalResourceLifecycleService.Subscribe
	// (D-13 server-stream к kacho-iam, см.
	// `internal/apps/kacho/api/internal_lifecycle`).
	InternalLifecycle InternalLifecycleConfig `mapstructure:"internal-lifecycle"`
}

// Mode возвращает резолвленный enum-режим (после `Validate()`).
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
}

// ─── Authn (transport TLS) ───────────────────────────────────────────────────

// AuthnConfig — TLS-параметры серверной части (опционально). См. evgeniy §8.J.1.
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
}

// ExtAPIEndpoint — параметры одного peer-сервиса. Public/Internal — два
// отдельных адреса (NLB зовёт internal-сервисы напрямую через cluster-internal
// :9091, не через api-gateway). TLS опционален (см. evgeniy §8.J.1).
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
	Breakglass        bool                         `mapstructure:"breakglass"` // dev-only; production validation rejects
}

type AuthzIAMConfig struct {
	Addr           string        `mapstructure:"addr"`            // iam.kacho.svc.cluster.local:9091
	DialDeadline   time.Duration `mapstructure:"dial-deadline"`   // default 3s
	RequestTimeout time.Duration `mapstructure:"request-timeout"` // default 500ms
}

type AuthzCacheConfig struct {
	Enable bool          `mapstructure:"enable"` // default true (positive-only)
	TTL    time.Duration `mapstructure:"ttl"`    // default 5s (NFR KAC-108: ≤10s)
	Size   int           `mapstructure:"size"`   // LRU capacity; 0 → 10_000 default
}

type AuthzListenInvalidatorConfig struct {
	Enable     bool   `mapstructure:"enable"`     // LISTEN kacho_iam_subjects on iam-PG
	Channel    string `mapstructure:"channel"`    // default "kacho_iam_subjects"
	IAMDirectDSN string `mapstructure:"iam-dsn"`  // dedicated pgx conn to iam-DB (optional)
}

// ─── FGA (tuple write) ───────────────────────────────────────────────────────

type FGAConfig struct {
	Endpoint   string             `mapstructure:"endpoint"`    // OpenFGA HTTP/gRPC endpoint
	StoreID    string             `mapstructure:"store-id"`    // FGA store identifier
	ModelID    string             `mapstructure:"model-id"`    // optional explicit authorization model
	TupleWrite FGATupleWriteConfig `mapstructure:"tuple-write"`
}

type FGATupleWriteConfig struct {
	Timeout    time.Duration `mapstructure:"timeout"`     // default 2s
	MaxRetries int           `mapstructure:"max-retries"` // default 3
}

// ─── Jobs (background workers) ───────────────────────────────────────────────

// JobsConfig — конфигурация фоновых worker'ов (см. internal/apps/kacho/jobs).
type JobsConfig struct {
	TargetDrain TargetDrainConfig `mapstructure:"target-drain"`
}

// TargetDrainConfig — параметры Phase B 2-phase drain runner (KAC-159).
type TargetDrainConfig struct {
	// Interval — период между тиками drain-runner'а. Default 10s
	// (см. RegisterDefaults). Должен быть > 0.
	Interval time.Duration `mapstructure:"interval"`
}

// ─── InternalLifecycle (D-13 stream) ─────────────────────────────────────────

// InternalLifecycleConfig — параметры InternalResourceLifecycleService.Subscribe
// (KAC-157). Server-stream к kacho-iam для FGA tuple-sync; cluster-internal
// only (port 9091, workspace CLAUDE.md «Запреты» #6).
type InternalLifecycleConfig struct {
	// MaxStreams — максимальное число одновременных Subscribe-стримов.
	// Каждый стрим держит dedicated pgx.Conn для LISTEN/NOTIFY (вне pool'а),
	// поэтому слот ≈ +1 conn'у к Postgres. Default 32 (см. RegisterDefaults).
	// Должен быть > 0.
	MaxStreams int `mapstructure:"max-streams"`
}
