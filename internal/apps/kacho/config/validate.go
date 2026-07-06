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
// игнорируется. Пустая/неуказанная строка → ModeProduction — fail-closed
// (security.md; RegisterDefaults тоже дефолтит `production`). dev — ЯВНЫЙ
// opt-in; пустой `mode: ""` не должен молча включать relaxed-режим.
func ParseMode(s string) (ModeEnum, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "dev", "development":
		return ModeDev, nil
	case "", "production", "prod":
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
	if c.Authz.Breakglass && mode == ModeProduction {
		errs = multierr.Append(errs, fmt.Errorf("authz.breakglass: forbidden in production mode (dev-only)"))
	}

	// Production transport fail-closed (security.md «AuthN+AuthZ ВЕЗДЕ»): plaintext
	// listener и insecure peer-вызовы в проде запрещены — boot отвергает insecure
	// prod-конфиг (не silent insecure-fallback).
	if mode == ModeProduction {
		// Server listener transport-security: ТОЛЬКО реальный server-cred
		// grpcsrv.TLSServerCreds(cfg.MTLS.Server) (composition root,
		// cmd/kacho-loadbalancer/main.go). authn.type=tls — МЁРТВОЕ значение: cfg.Authn
		// НЕ проброшен ни в один транспорт (grep: читается только здесь, в validate.go),
		// поэтому он НЕ настраивает TLS на listener'ах. Прежний gate принимал его как
		// «one-way TLS+JWT» и пропускал prod-конфиг с mtls.server.enable=false → boot
		// поднимал plaintext gRPC на public И internal :9091, доверяя client-asserted
		// principal без mTLS (CWE-319/CWE-290). Fail-closed, паритет с
		// kacho-vpc/kacho-compute (server-mTLS — обязателен, gate только на реальном
		// mTLS-настройке):
		//   1) dead authn.type=tls отвергается явно (оператор не примет его за transport-
		//      security и не оставит listener plaintext);
		//   2) mtls.server.enable=true обязателен (единственный источник server-creds).
		if strings.EqualFold(strings.TrimSpace(c.Authn.Type), "tls") {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: authn.type=tls is a dead/unwired transport setting — it configures no server TLS "+
					"(listeners would run plaintext); remove it and set mtls.server.enable=true for real server transport security"))
		}
		if !c.MTLS.Server.Enable {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: insecure server transport — set mtls.server.enable=true (plaintext listener forbidden)"))
		}
		// nlb→iam authz edge (per-RPC InternalIAMService.Check, internal :9091) обязан
		// быть mTLS: иначе Check идёт по plaintext и подделанная identity не отсекается.
		if !c.MTLS.IAMRegister.Enable {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: nlb→iam authz edge must be mTLS — set mtls.iam-register.enable=true (insecure Check edge forbidden)"))
		}
		// Остальные cross-service рёбра (vpc / compute / geo / iam-project) обязаны
		// иметь transport-security когда сконфигурированы: mTLS (mtls.<edge>.enable)
		// ЛИБО one-way TLS (<edge>.tls). Без этого dialOne падает в insecure gRPC
		// (buildCreds → insecure.NewCredentials), и on-path attacker читает/подменяет
		// IPAM-аллокацию (VIP), instance-resolve, region-валидацию — integrity/
		// defense-in-depth (CWE-319). Прежде проверялись только server
		// + iam-register. Проверяем только СКОНФИГУРИРОВАННЫЕ рёбра (addr задан),
		// чтобы dev-подобные частичные prod-конфиги без некоторых peer'ов не ломались.
		for _, e := range c.peerEdges() {
			if e.addr != "" && !e.secure {
				errs = multierr.Append(errs, fmt.Errorf(
					"production mode: insecure peer transport on %s edge — set mtls.%s.enable=true or %s.tls=true (plaintext peer dial forbidden)",
					e.name, e.mtlsKey, e.tlsKey))
			}
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
		// и FGA Check оценивает подделанный subject. В проде mtls.server.enable
		// обязателен (проверено выше — единственная реальная server-transport-security),
		// поэтому allow-list обязан быть непустым (перечисляет SAN доверенного
		// форвардера — api-gateway). Guard оставлен gated на mtls.server.Enable: если он
		// off, ошибка insecure-server-transport уже поднята выше — не дублируем.
		if c.MTLS.Server.Enable && len(c.Authz.TrustedForwarderSANs) == 0 {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: authz.trusted-forwarder-sans must be non-empty when mtls.server.enable=true "+
					"(empty allow-list trusts any mTLS-verified peer to forward the end-user principal — impersonation vector)"))
		}
		// Postgres transport fail-closed (security.md «mTLS/TLS ВЕЗДЕ», CWE-319).
		// Peer-рёбра проверяются выше; DB-соединение — тот же
		// периметр: `sslmode=disable`/`allow`/`prefer` (или отсутствие sslmode,
		// libpq-default 'prefer') допускает plaintext-канал, по которому
		// DB-пароль (KACHO_NLB_DB_PASSWORD) и tenant-данные (VIP/listener/target)
		// идут в открытую. В проде допустимы только `require`/`verify-ca`/
		// `verify-full`. Проверяем лишь при непустом URL (пустой ловится выше).
		if u := strings.TrimSpace(c.Repository.Postgres.URL); u != "" && !postgresSSLSecure(u) {
			errs = multierr.Append(errs, fmt.Errorf(
				"production mode: insecure Postgres transport — repository.postgres.url sslmode must be require|verify-ca|verify-full "+
					"(disable/allow/prefer or unset permits a plaintext DB connection; forbidden)"))
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

// peerEdge — проекция одного cross-service ребра для transport-fail-closed gate:
// имя (для сообщения), резолвнутый addr, флаг «защищено» (mTLS ЛИБО one-way TLS),
// и config-ключи для actionable-текста. Зеркалит peerDialSpecs в composition root
// (cmd/kacho-loadbalancer/main.go) — там же строятся реальные conn'ы.
type peerEdge struct {
	name    string
	addr    string
	secure  bool
	mtlsKey string
	tlsKey  string
}

// peerEdges — таблица cross-service рёбер для production transport-gate. addr =
// firstNonEmpty(Addr, InternalAddr) (single-addr dev-config тоже покрывается).
// secure = mtls.<edge>.enable || <edge>.tls. iam-register (authz Check edge)
// проверяется отдельно строгим mTLS-требованием выше; здесь — публичное
// iam-project (ProjectService.Get) ребро.
func (c Config) peerEdges() []peerEdge {
	firstNonEmpty := func(a, b string) string {
		if strings.TrimSpace(a) != "" {
			return strings.TrimSpace(a)
		}
		return strings.TrimSpace(b)
	}
	return []peerEdge{
		{
			name:    "vpc",
			addr:    firstNonEmpty(c.ExtAPI.VPC.Addr, c.ExtAPI.VPC.InternalAddr),
			secure:  c.MTLS.VPC.Enable || c.ExtAPI.VPC.TLS,
			mtlsKey: "vpc", tlsKey: "vpc",
		},
		{
			name:    "compute",
			addr:    firstNonEmpty(c.ExtAPI.Compute.Addr, c.ExtAPI.Compute.InternalAddr),
			secure:  c.MTLS.Compute.Enable || c.ExtAPI.Compute.TLS,
			mtlsKey: "compute", tlsKey: "compute",
		},
		{
			name:    "geo",
			addr:    firstNonEmpty(c.ExtAPI.Geo.Addr, c.ExtAPI.Geo.InternalAddr),
			secure:  c.MTLS.Geo.Enable || c.ExtAPI.Geo.TLS,
			mtlsKey: "geo", tlsKey: "geo",
		},
		{
			name:    "iam-project",
			addr:    firstNonEmpty(c.ExtAPI.IAM.Addr, c.ExtAPI.IAM.InternalAddr),
			secure:  c.MTLS.IAMProject.Enable || c.ExtAPI.IAM.TLS,
			mtlsKey: "iam-project", tlsKey: "iam",
		},
	}
}

// postgresSSLSecure — true, если DSN несёт защищённый sslmode
// (`require`/`verify-ca`/`verify-full`). `disable`/`allow`/`prefer` и
// отсутствие sslmode (libpq-default 'prefer', plaintext-fallback) → false.
// Парсит query-param у URL-DSN (`postgres://…?sslmode=…`); при неудаче парса —
// keyword-DSN-fallback через регистронезависимый скан `sslmode=<value>`.
func postgresSSLSecure(dsn string) bool {
	secure := map[string]struct{}{
		"require": {}, "verify-ca": {}, "verify-full": {},
	}
	mode := ""
	if u, err := url.Parse(dsn); err == nil && u.Query().Has("sslmode") {
		mode = u.Query().Get("sslmode")
	} else {
		// keyword-form fallback (`host=… sslmode=require`) — грубый скан.
		for _, tok := range strings.Fields(dsn) {
			if kv := strings.SplitN(tok, "=", 2); len(kv) == 2 && strings.EqualFold(strings.TrimSpace(kv[0]), "sslmode") {
				mode = kv[1]
				break
			}
		}
	}
	_, ok := secure[strings.ToLower(strings.TrimSpace(mode))]
	return ok
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
