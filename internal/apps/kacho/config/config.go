// Package config — viper-based YAML config для kacho-nlb (evgeniy §8.J).
//
// ENV-binding через viper с делимитером `__` для секций:
//
//	KACHO_NLB_API_SERVER__ENDPOINT       → api-server.endpoint
//	KACHO_NLB_REPOSITORY__POSTGRES__URL  → repository.postgres.url
//	KACHO_NLB_AUTHZ__IAM__ADDR           → authz.iam.addr
//	KACHO_NLB_FGA__ENDPOINT              → fga.endpoint
//	KACHO_NLB_LOGGER__LEVEL              → logger.level
//
// Defaults — в `defaults.go` (`RegisterDefaults`); validation — в `validate.go`.
// Loader — в `load.go` (`Load(path) (*Config, error)`).
//
// TODO(KAC-149): реализация (Load + defaults + validation + tests).
package config

import "time"

// Config — корневой config kacho-nlb. Все вложенные структуры с mapstructure-тегами
// для viper-биндинга.
type Config struct {
	Logger      LoggerConfig      `mapstructure:"logger"`
	APIServer   APIServerConfig   `mapstructure:"api-server"`
	Metrics     MetricsConfig     `mapstructure:"metrics"`
	Healthcheck HealthcheckConfig `mapstructure:"healthcheck"`
	Repository  RepositoryConfig  `mapstructure:"repository"`
	Authz       AuthzConfig       `mapstructure:"authz"`
	FGA         FGAConfig         `mapstructure:"fga"`
	ExtAPI      ExtAPIConfig      `mapstructure:"extapi"`
}

type LoggerConfig struct {
	Level string `mapstructure:"level"` // FATAL|ERROR|WARN|INFO|DEBUG; default DEBUG
}

type APIServerConfig struct {
	Endpoint         string        `mapstructure:"endpoint"`           // tcp://0.0.0.0:9090
	InternalEndpoint string        `mapstructure:"internal-endpoint"`  // tcp://0.0.0.0:9091
	GracefulShutdown time.Duration `mapstructure:"graceful-shutdown"`  // default 10s
	GRPCGatewayEnable bool         `mapstructure:"grpc-gw-enable"`     // false (gateway = separate svc)
}

type MetricsConfig struct {
	Enable bool `mapstructure:"enable"`
}

type HealthcheckConfig struct {
	Enable bool `mapstructure:"enable"`
}

type RepositoryConfig struct {
	Type     string         `mapstructure:"type"` // POSTGRES
	Postgres PostgresConfig `mapstructure:"postgres"`
}

type PostgresConfig struct {
	URL          string        `mapstructure:"url"`            // postgres://user:pass@host/kacho_nlb
	MaxConns     int32         `mapstructure:"max-conns"`      // pool size; 0 → default
	ConnLifetime time.Duration `mapstructure:"conn-lifetime"`
}

type AuthzConfig struct {
	IAM AuthzIAMConfig `mapstructure:"iam"`
}

type AuthzIAMConfig struct {
	Addr           string        `mapstructure:"addr"`             // iam.kacho.svc.cluster.local:9091
	DialDeadline   time.Duration `mapstructure:"dial-deadline"`    // default 3s
	RequestTimeout time.Duration `mapstructure:"request-timeout"`  // default 500ms
}

type FGAConfig struct {
	Endpoint string `mapstructure:"endpoint"` // OpenFGA HTTP/gRPC endpoint (optional; may run inside iam)
	StoreID  string `mapstructure:"store-id"`
}

type ExtAPIConfig struct {
	DefDialDuration time.Duration       `mapstructure:"def-dial-duration"`
	VPC             ExtAPIEndpoint      `mapstructure:"vpc"`
	Compute         ExtAPIEndpoint      `mapstructure:"compute"`
}

type ExtAPIEndpoint struct {
	Addr         string        `mapstructure:"addr"`
	InternalAddr string        `mapstructure:"internal-addr"`
	DialDuration time.Duration `mapstructure:"dial-duration"`
}

// TODO(KAC-149): Load(path string) (*Config, error) — viper.New + ReadInConfig + Unmarshal.
// TODO(KAC-149): Validate() error — multierr.Combine на required-поля.
