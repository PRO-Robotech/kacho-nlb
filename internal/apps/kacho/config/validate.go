// validate.go — Mode enum + Config.Validate (evgeniy §8.J.5–J.7).
//
//   - `Mode` enum заменяет `bool productionMode` (J.6/AP-8) — `cfg.Mode`
//     (общий режим работы), а не `cfg.AuthMode` (J.7).
//   - Validate-логика — в config-пакете, не в main (J.5).
package config

import (
	"fmt"
	"net/url"
	"strings"

	"go.uber.org/multierr"
)

// ModeEnum — общий режим работы сервиса (evgeniy §J.6: bool → enum).
type ModeEnum int

const (
	// ModeDev — relaxed validation, TLS опционален, breakglass допускается.
	ModeDev ModeEnum = iota + 1
	// ModeProduction — TLS обязателен для public listener / peer-вызовов,
	// FGA endpoint обязателен, breakglass запрещён, Postgres DSN обязателен.
	ModeProduction
)

// String — для логирования / error-сообщений.
func (m ModeEnum) String() string {
	switch m {
	case ModeDev:
		return "dev"
	case ModeProduction:
		return "production"
	default:
		return "unknown"
	}
}

// ParseMode разбирает строку из YAML / ENV (`dev` / `production`). Регистр
// игнорируется. Пустая строка → ModeDev (см. RegisterDefaults).
func ParseMode(s string) (ModeEnum, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "dev", "development":
		return ModeDev, nil
	case "production", "prod":
		return ModeProduction, nil
	default:
		return 0, fmt.Errorf("invalid mode %q (want dev|production)", s)
	}
}

// validLogLevels — допустимые значения logger.level.
var validLogLevels = map[string]struct{}{
	"FATAL": {}, "ERROR": {}, "WARN": {}, "INFO": {}, "DEBUG": {},
}

// Validate — проверяет required-поля и согласованность mode-specific
// требований через multierr.Combine. Применяется один раз сразу после
// `viper.Unmarshal` в `Load(...)`.
func (c Config) Validate() error {
	var errs error

	// Mode
	mode, err := ParseMode(c.ModeRaw)
	if err != nil {
		errs = multierr.Append(errs, fmt.Errorf("mode: %w", err))
	}

	// Logger
	if _, ok := validLogLevels[strings.ToUpper(strings.TrimSpace(c.Logger.Level))]; !ok {
		errs = multierr.Append(errs, fmt.Errorf("logger.level %q: want one of FATAL|ERROR|WARN|INFO|DEBUG", c.Logger.Level))
	}

	// API-server endpoints — must be `tcp://host:port` parseable.
	if err := validateEndpoint("api-server.endpoint", c.APIServer.Endpoint); err != nil {
		errs = multierr.Append(errs, err)
	}
	if err := validateEndpoint("api-server.internal-endpoint", c.APIServer.InternalEndpoint); err != nil {
		errs = multierr.Append(errs, err)
	}
	if c.APIServer.GracefulShutdown <= 0 {
		errs = multierr.Append(errs, fmt.Errorf("api-server.graceful-shutdown must be > 0, got %v", c.APIServer.GracefulShutdown))
	}

	// Repository
	switch strings.ToUpper(strings.TrimSpace(c.Repository.Type)) {
	case "POSTGRES":
		// ok
	case "":
		errs = multierr.Append(errs, fmt.Errorf("repository.type: empty (want POSTGRES)"))
	default:
		errs = multierr.Append(errs, fmt.Errorf("repository.type %q: only POSTGRES supported", c.Repository.Type))
	}
	if strings.TrimSpace(c.Repository.Postgres.URL) == "" {
		errs = multierr.Append(errs, fmt.Errorf("repository.postgres.url: required"))
	}
	if c.Repository.Postgres.MaxConns < 0 {
		errs = multierr.Append(errs, fmt.Errorf("repository.postgres.max-conns must be >= 0, got %d", c.Repository.Postgres.MaxConns))
	}

	// Authn (TLS)
	switch strings.ToLower(strings.TrimSpace(c.Authn.Type)) {
	case "none", "":
		// ok
	case "tls":
		if c.Authn.TLS.KeyFile == "" || c.Authn.TLS.CertFile == "" {
			errs = multierr.Append(errs, fmt.Errorf("authn.tls: key-file and cert-file required when type=tls"))
		}
	default:
		errs = multierr.Append(errs, fmt.Errorf("authn.type %q: want none|tls", c.Authn.Type))
	}

	// Authz (FGA Check)
	if c.Authz.IAM.Addr == "" && mode == ModeProduction {
		errs = multierr.Append(errs, fmt.Errorf("authz.iam.addr: required in production mode"))
	}
	if c.Authz.Cache.Size < 0 {
		errs = multierr.Append(errs, fmt.Errorf("authz.cache.size must be >= 0, got %d", c.Authz.Cache.Size))
	}
	if c.Authz.Breakglass && mode == ModeProduction {
		errs = multierr.Append(errs, fmt.Errorf("authz.breakglass: forbidden in production mode (dev-only)"))
	}

	// Jobs.target-drain (KAC-159 Phase B drain runner). Interval должен быть > 0;
	// `0s` означало бы tight-loop, что нагрузит БД.
	if c.Jobs.TargetDrain.Interval <= 0 {
		errs = multierr.Append(errs, fmt.Errorf("jobs.target-drain.interval must be > 0, got %v", c.Jobs.TargetDrain.Interval))
	}

	// InternalLifecycle.MaxStreams (KAC-157 D-13 stream). Должен быть > 0:
	// =0 означало бы «никакие streams не разрешены» → kacho-iam не сможет
	// подключиться → tuple-sync сломан.
	if c.InternalLifecycle.MaxStreams <= 0 {
		errs = multierr.Append(errs, fmt.Errorf("internal-lifecycle.max-streams must be > 0, got %d", c.InternalLifecycle.MaxStreams))
	}

	return errs
}

// validateEndpoint — `tcp://host:port` парсится как url, схема обязательна,
// host:port извлекается. Пустая строка → ошибка.
func validateEndpoint(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%s: required", field)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: parse %q: %w", field, raw, err)
	}
	if u.Scheme != "tcp" {
		return fmt.Errorf("%s: scheme %q (want tcp)", field, u.Scheme)
	}
	host := u.Host
	if host == "" {
		return fmt.Errorf("%s: empty host:port in %q", field, raw)
	}
	// crude port check — net.SplitHostPort returns error if no port present
	if !strings.Contains(host, ":") {
		return fmt.Errorf("%s: %q missing :port", field, raw)
	}
	return nil
}
