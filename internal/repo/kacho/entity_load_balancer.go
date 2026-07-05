// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package kacho — repo-leaf entities (per-resource DTO между domain и SQL-схемой
// kacho_nlb). Здесь живут *Record-структуры — «row из таблицы + DB-managed
// поля» (CreatedAt / UpdatedAt).
//
// Dependency rule:
//
//	dto/type2pb → repo/kacho → domain
//	apps/kacho/api/<res>/{handler,…} → repo/kacho → domain
//	repo/kacho/pg → repo/kacho → domain
//	cmd/kacho-loadbalancer/main.go → repo/kacho (composition root)
package kacho

import (
	"time"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// LoadBalancerRecord — repo-entity для NetworkLoadBalancer. domain.LoadBalancer
// + DB-managed CreatedAt / UpdatedAt. Service-слой получает *LoadBalancerRecord
// из репозитория и пробрасывает в DTO / handler.
type LoadBalancerRecord struct {
	domain.LoadBalancer
	CreatedAt time.Time
	UpdatedAt time.Time
	// Xmin — Postgres system-column `xmin::text` snapshot for optimistic
	// concurrency control on read-modify-write Update (no version column). Get
	// captures it; Update issues `WHERE xmin::text=$exp` so a concurrent write
	// since the read → 0 rows → FailedPrecondition (see data-integrity.md OCC).
	// Empty for manually-constructed (non-DB) records.
	Xmin string
}

// LoadBalancerFilter — фильтр для List load_balancers.
//
// ProjectID — обязателен для production-запросов (FGA scoping). Name — точное
// совпадение; Filter — синтаксис `name="<value>"` (через corelib/filter.Parse).
type LoadBalancerFilter struct {
	ProjectID string
	Name      string
	Filter    string
	// AllowedIDs — per-object FGA allow-set (RBAC; iam ListObjects).
	// nil → фильтр не применяется (bypass / authz disabled). len==0 → пустой
	// результат (no-leak). len>0 → `WHERE id = ANY($allowed)` ВНУТРИ SQL ДО LIMIT,
	// чтобы keyset-пагинация была плотной по отфильтрованному набору.
	AllowedIDs []string
}
