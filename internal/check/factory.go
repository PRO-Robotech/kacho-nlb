// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package check

import (
	"errors"
	"log/slog"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/authz"

	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
)

// Options — параметры для NewInterceptor.
type Options struct {
	// ServiceName — для метрик/логов interceptor'а.
	ServiceName string

	// IAMCheck — peer-client `iam.CheckClient`. Nil + Breakglass=false → ErrIAMCheckNotConfigured.
	IAMCheck iamclient.CheckClient

	// Breakglass — если true, interceptor пропускает всех authenticated
	// principal'ов без Check + WARN (config-validation rejects в production
	// mode, см. config/validate.go).
	Breakglass bool

	// Logger — slog logger; nil → slog.Default.
	Logger *slog.Logger

	// CheckTimeout — таймаут на один Check (default 2s).
	CheckTimeout time.Duration

	// DenyRateLimitPerSec — per-Principal rate-limit на denied storm (default 100).
	DenyRateLimitPerSec float64

	// CacheTTL — TTL positive-кеша (default 5s, ≤10s).
	CacheTTL time.Duration

	// AllowSystemPrincipal — если true, system:bootstrap пропускается без
	// Check (для миграций / фоновых job'ов).
	AllowSystemPrincipal bool
}

// ErrIAMCheckNotConfigured — peer.iam.CheckClient = nil И Breakglass=false.
// Caller (composition root) сам решает: в production-mode — fatal; в dev — skip.
var ErrIAMCheckNotConfigured = errors.New("check: IAM CheckClient not configured and Breakglass=false")

// NewInterceptor — фабрика gRPC interceptor'а. Возвращает:
//   - (*authz.Interceptor, *authz.Cache, nil) — успех; caller wireы Unary/Stream
//     в gRPC server chain'ы и держит cache для ListenInvalidator'а;
//   - (nil, nil, ErrIAMCheckNotConfigured) — peer не задан и breakglass=false.
//
// Cache отдаётся **отдельно** (а не только через interceptor) чтобы caller
// мог передать тот же экземпляр в `authz.ListenInvalidator.Cache` (общий
// pg_notify-driven invalidation). См. cmd/kacho-loadbalancer/main.go wiring.
func NewInterceptor(opts Options) (*authz.Interceptor, *authz.Cache, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	cache := authz.NewCache(opts.CacheTTL)

	if opts.Breakglass {
		// Breakglass-mode: Client = nil, Cache всё равно создаём (defensive —
		// interceptor его проверяет nil-guard'ом, но без нагрузки на cache в
		// этом mode'е).
		return authz.NewInterceptor(authz.InterceptorOptions{
			ServiceName:          opts.ServiceName,
			Map:                  PermissionMap(),
			Client:               nil,
			Cache:                cache,
			Logger:               opts.Logger,
			Breakglass:           true,
			DenyRateLimitPerSec:  opts.DenyRateLimitPerSec,
			CheckTimeout:         opts.CheckTimeout,
			AllowSystemPrincipal: opts.AllowSystemPrincipal,
		}), cache, nil
	}

	if opts.IAMCheck == nil {
		return nil, nil, ErrIAMCheckNotConfigured
	}

	adapter := NewIAMCheckClient(opts.IAMCheck)
	return authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName:          opts.ServiceName,
		Map:                  PermissionMap(),
		Client:               adapter,
		Cache:                cache,
		Logger:               opts.Logger,
		Breakglass:           false,
		DenyRateLimitPerSec:  opts.DenyRateLimitPerSec,
		CheckTimeout:         opts.CheckTimeout,
		AllowSystemPrincipal: opts.AllowSystemPrincipal,
	}), cache, nil
}
