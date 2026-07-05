// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// validate.go — Mode enum + Config.Validate.
//
//   - `Mode` enum заменяет `bool productionMode`  — `cfg.Mode`
//     (общий режим работы), а не `cfg.AuthMode`.
//   - Validate-логика — в config-пакете, не в main.
package config

import (
	"fmt"
	"net/url"
	"strings"

	"go.uber.org/multierr"
)

// ModeEnum — общий режим работы сервиса (bool → enum).
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
// `viper.Unmarshal` в `Load`.
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

	// Production transport fail-closed (security.md «AuthN+AuthZ ВЕЗДЕ»): plaintext
	// listener и insecure peer-вызовы в проде запрещены — boot отвергает insecure
	// prod-конфиг (не silent insecure-fallback).
	if mode == ModeProduction {
		// Server listener: mutual TLS (mtls.server) ЛИБО one-way TLS+JWT (authn.type=tls).
		serverSecure := c.MTLS.Server.Enable || strings.EqualFold(strings.TrimSpace(c.Authn.Type), "tls")
		if !serverSecure {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: insecure server transport — set mtls.server.enable=true or authn.type=tls (plaintext listener forbidden)"))
		}
		// nlb→iam authz edge (per-RPC InternalIAMService.Check, internal :9091) обязан
		// быть mTLS: иначе Check идёт по plaintext и подделанная identity не отсекается.
		if !c.MTLS.IAMRegister.Enable {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: nlb→iam authz edge must be mTLS — set mtls.iam-register.enable=true (insecure Check edge forbidden)"))
		}
		// Per-object List authorization fail-closed (security.md, defense-in-depth
		// parity с breakglass-gate). list-filter — единственный authz-слой для
		// ScopeFiltered List RPC (interceptor их пропускает); отключение или
		// fail-open превращает List в нефильтрованный passthrough → cross-tenant
		// enumeration. В проде: enabled обязателен, fail-open запрещён.
		if !c.Authz.ListFilter.Enabled {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: authz.list-filter.enabled must be true (per-object List authorization required; disabling it enables cross-tenant enumeration)"))
		}
		if c.Authz.ListFilter.FailOpen {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: authz.list-filter.fail-open forbidden (fail-closed only; fail-open returns unfiltered results during IAM outage)"))
		}
		// Trusted-forwarder allow-list (anti-impersonation). Пустой allow-list в
		// grpcsrv.WithTrustedForwarders означает «доверять форвардинг
		// x-kacho-principal-* ЛЮБОМУ mTLS-verified peer'у» (back-compat trust-all).
		// В общем mTLS-mesh (все воркеры под одним internal-CA) это confused-deputy:
		// любой сервис с валидным клиентским cert'ом форжит произвольного principal'а,
		// и FGA Check оценивает подделанный subject. В проде с mTLS-server allow-list
		// обязан быть непустым (перечисляет SAN доверенного форвардера — api-gateway).
		// При one-way TLS (mtls.server off) ни один peer не verified → forwarded
		// principal снимается с любого peer'а → SystemPrincipal → fail-closed, поэтому
		// allow-list там не требуется.
		if c.MTLS.Server.Enable && len(c.Authz.TrustedForwarderSANs) == 0 {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: authz.trusted-forwarder-sans must be non-empty when mtls.server.enable=true "+
					"(empty allow-list trusts any mTLS-verified peer to forward the end-user principal — impersonation vector)"))
		}
	}

	// Jobs.target-drain (фаза B drain runner). Interval должен быть > 0;
	// `0s` означало бы tight-loop, что нагрузит БД.
	if c.Jobs.TargetDrain.Interval <= 0 {
		errs = multierr.Append(errs, fmt.Errorf("jobs.target-drain.interval must be > 0, got %v", c.Jobs.TargetDrain.Interval))
	}

	// Jobs.free-ip (reconciler застрявших листенеров). Interval > 0 (иначе
	// tight-loop); age-threshold > 0 (иначе reconciler схватит свежий in-flight
	// create/delete и удалит легитимную in-progress строку — гонка).
	if c.Jobs.FreeIP.Interval <= 0 {
		errs = multierr.Append(errs, fmt.Errorf("jobs.free-ip.interval must be > 0, got %v", c.Jobs.FreeIP.Interval))
	}
	if c.Jobs.FreeIP.AgeThreshold <= 0 {
		errs = multierr.Append(errs, fmt.Errorf("jobs.free-ip.age-threshold must be > 0, got %v", c.Jobs.FreeIP.AgeThreshold))
	}

	// InternalLifecycle.MaxStreams (stream). Должен быть > 0:
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
