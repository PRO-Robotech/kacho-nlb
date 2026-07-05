// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package authzfilter — per-object filtered List для kacho-nlb (RBAC).
//
// Каждый публичный List<Resource> handler/use-case прогоняет id-set ресурса
// через iam.AuthorizeService.ListObjects(subject, action, "lb_*") и отдаёт
// ПЕРЕСЕЧЕНИЕ (только доступные объекты). read==enforce: та же FGA relation
// (viewer), что и per-RPC Check для Get; fail-closed: iam недоступен → Unavailable
// (НЕ нефильтрованный список — no-leak, security.md).
//
// Зеркало kacho-compute `internal/authzfilter` (живой reference consumer-паттерна);
// subject извлекается из ctx через operations.PrincipalFromContext (nlb-конвенция),
// не из raw-metadata.
package authzfilter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	iamv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/iam/v1"
)

// Decision — результат фильтра для одного ListObjects-вызова.
//
//   - BypassAll=true: фильтр не применяется (admin / wildcard-grant / fail-open /
//     disabled). repo.List возвращает project-scoped строки как есть. Пустой
//     (system) subject НЕ даёт BypassAll — enabled-фильтр fail-close'ит его
//     (Unauthenticated), см. Resolve (audit SEC-high #1).
//   - Empty=true: subject ничего не разрешено в этом resource_type — use-case
//     возвращает пустой response без обращения к repo (no-leak).
//   - AllowedIDs: explicit-список id, к которым subject имеет access; use-case
//     прокидывает в repo как `WHERE id = ANY($allowed)` ДО LIMIT (pagination-after-filter).
type Decision struct {
	BypassAll  bool
	Empty      bool
	AllowedIDs []string
	// FromCache — true если ответ из cache (observability/tests).
	FromCache bool
	// FailOpen — true если решение принято в degraded-mode (FGA error + fail-open).
	FailOpen bool
}

// IsBypass — true если фильтрация не применяется.
func (d Decision) IsBypass() bool { return d.BypassAll }

// IsEmpty — true если allow-list пуст.
func (d Decision) IsEmpty() bool { return d.Empty }

// IDs — отсортированный allow-list (детерминированный порядок для стабильной пагинации).
func (d Decision) IDs() []string { return d.AllowedIDs }

// Filter — port интерфейс. Реализация — FGAFilter (через iam.ListObjects) либо
// BypassFilter (public-catalog / FGA disabled). nil-Filter трактуется caller'ом
// как bypass (use-case проверяет filter == nil).
type Filter interface {
	// ListAllowedIDs возвращает Decision для (subject, resourceType, action).
	// resourceType — FGA object type ("lb_network_load_balancer",...).
	// action — semantic permission ("loadbalancer.networkLoadBalancers.list",...) —
	// iam-сервер мапит на FGA relation (viewer). subject — FGA subject
	// ("user:usr_..." / "service_account:sa_...").
	ListAllowedIDs(ctx context.Context, subject, resourceType, action string) (Decision, error)
}

// BypassFilter — заглушка, всегда BypassAll=true (для тестов / явного отключения).
type BypassFilter struct{}

// ListAllowedIDs возвращает BypassAll=true.
func (BypassFilter) ListAllowedIDs(_ context.Context, _, _, _ string) (Decision, error) {
	return Decision{BypassAll: true}, nil
}

// Config — параметры FGAFilter.
type Config struct {
	// Enabled — master-switch. false → ListAllowedIDs возвращает BypassAll=true
	// (no-op). Для dev-кластера / graceful start без iam.
	Enabled bool
	// Timeout — per-request deadline к iam.ListObjects.
	Timeout time.Duration
	// CacheTTL — TTL одной записи в in-process decision cache.
	CacheTTL time.Duration
	// CacheMaxEntries — bound для cache + MaxResults cap к iam.ListObjects.
	CacheMaxEntries int
	// FailOpen — на FGA error: true → BypassAll=true + audit-warn; false → Unavailable
	// (default, fail-closed per security.md).
	FailOpen bool
}

// DefaultConfig — sane defaults: filter включён, 500ms timeout, 5s TTL, 10000 entries, fail-closed.
func DefaultConfig() Config {
	return Config{
		Enabled:         true,
		Timeout:         500 * time.Millisecond,
		CacheTTL:        5 * time.Second,
		CacheMaxEntries: 10000,
		FailOpen:        false,
	}
}

// AuthorizeClient — узкий интерфейс к iam.AuthorizeService (тестируемость).
// Реализуется *grpcAuthorizeClient (production) либо mock (unit-tests). Сигнатура
// совпадает с generated AuthorizeServiceClient.ListObjects → тонкий pass-through.
type AuthorizeClient interface {
	ListObjects(ctx context.Context, in *iamv1.ListObjectsRequest, opts ...grpc.CallOption) (*iamv1.ListObjectsResponse, error)
}

// FGAFilter — продакшен-реализация Filter поверх iam.AuthorizeService.ListObjects
// с in-memory TTL-кешем.
type FGAFilter struct {
	cli AuthorizeClient
	cfg Config

	// now — источник времени для TTL-логики кеша. Инъектируется (по умолчанию
	// time.Now), чтобы TTL-тесты были детерминированными и не зависели от
	// wall-clock/time.Sleep (flaky под -race/GC/CPU-throttle).
	now func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	decision Decision
	expires  time.Time
}

// NewFGAFilter создаёт фильтр. cli == nil → всегда BypassAll (graceful start без iam).
func NewFGAFilter(cli AuthorizeClient, cfg Config) *FGAFilter {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 500 * time.Millisecond
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Second
	}
	if cfg.CacheMaxEntries <= 0 {
		cfg.CacheMaxEntries = 10000
	}
	return &FGAFilter{
		cli:   cli,
		cfg:   cfg,
		now:   time.Now,
		cache: make(map[string]cacheEntry, cfg.CacheMaxEntries),
	}
}

// nowFn — safe accessor для инъектируемых часов (fallback на time.Now, если
// FGAFilter сконструирован не через NewFGAFilter).
func (f *FGAFilter) nowFn() time.Time {
	if f.now != nil {
		return f.now()
	}
	return time.Now()
}

// ListAllowedIDs — основной entry-point.
func (f *FGAFilter) ListAllowedIDs(ctx context.Context, subject, resourceType, action string) (Decision, error) {
	if !f.cfg.Enabled || f.cli == nil {
		return Decision{BypassAll: true}, nil
	}
	if subject == "" {
		// Без identity — caller (use-case) трактует "" как system/bypass ДО вызова;
		// сюда subject="" попасть не должен, но fail-closed на всякий случай.
		return Decision{}, status.Error(codes.Unauthenticated, "list filter: subject required")
	}
	if resourceType == "" || action == "" {
		return Decision{}, fmt.Errorf("authzfilter: resourceType and action required")
	}

	key := cacheKey(subject, resourceType, action)
	if d, ok := f.getCache(key); ok {
		d.FromCache = true
		return d, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
	defer cancel()

	resp, err := f.cli.ListObjects(callCtx, &iamv1.ListObjectsRequest{
		Subject:      subject,
		ResourceType: resourceType,
		Action:       action,
		MaxResults:   int64(f.cfg.CacheMaxEntries),
	})
	if err != nil {
		return f.handleErr(err)
	}

	// wildcard_grant → subject имеет unbounded reach над типом → bypass
	// (resource_ids пуст на сервере при wildcard, поэтому НЕ трактуем как Empty).
	if resp.GetWildcardGrant() {
		d := Decision{BypassAll: true}
		f.putCache(key, d)
		return d, nil
	}

	ids := append([]string(nil), resp.GetResourceIds()...)
	sort.Strings(ids) // детерминированный порядок для стабильной пагинации

	d := Decision{
		AllowedIDs: ids,
		Empty:      len(ids) == 0,
	}
	f.putCache(key, d)
	return d, nil
}

// handleErr — reaction по fail-open / fail-closed.
func (f *FGAFilter) handleErr(err error) (Decision, error) {
	if f.cfg.FailOpen {
		return Decision{BypassAll: true, FailOpen: true}, nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Decision{}, status.Errorf(codes.Unavailable, "list filter: iam.ListObjects deadline exceeded after %s", f.cfg.Timeout)
	}
	if s, ok := status.FromError(err); ok && s.Code() != codes.OK && s.Code() != codes.Unknown {
		return Decision{}, status.Errorf(codes.Unavailable, "list filter: iam.ListObjects %s: %s", s.Code(), s.Message())
	}
	return Decision{}, status.Errorf(codes.Unavailable, "list filter: iam.ListObjects: %v", err)
}

func cacheKey(subject, resourceType, action string) string {
	return subject + "|" + resourceType + "|" + action
}

func (f *FGAFilter) getCache(key string) (Decision, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.cache[key]
	if !ok {
		return Decision{}, false
	}
	if f.nowFn().After(e.expires) {
		delete(f.cache, key)
		return Decision{}, false
	}
	d := e.decision
	if len(d.AllowedIDs) > 0 {
		idsCopy := make([]string, len(d.AllowedIDs))
		copy(idsCopy, d.AllowedIDs)
		d.AllowedIDs = idsCopy
	}
	return d, true
}

func (f *FGAFilter) putCache(key string, d Decision) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cache) >= f.cfg.CacheMaxEntries {
		for k := range f.cache {
			delete(f.cache, k)
			break
		}
	}
	f.cache[key] = cacheEntry{decision: d, expires: f.nowFn().Add(f.cfg.CacheTTL)}
}

// Size — текущий размер cache (observability/tests).
func (f *FGAFilter) Size() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cache)
}

// Invalidate — удаляет записи subject'а из cache (LISTEN/NOTIFY-driven inval).
func (f *FGAFilter) Invalidate(subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := subject + "|"
	for k := range f.cache {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(f.cache, k)
		}
	}
}
