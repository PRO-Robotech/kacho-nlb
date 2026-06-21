// Package kacho — repo-leaf entities (per-resource DTO между domain и SQL-схемой
// kacho_nlb). Здесь живут *Record-структуры — «row из таблицы + DB-managed
// поля» (CreatedAt / UpdatedAt). Skill evgeniy §4 D.1 / §6 G.2 / §7 H.1.
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
//
// Skill evgeniy §4 D.1 / §7 H.1.
type LoadBalancerRecord struct {
	domain.LoadBalancer
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LoadBalancerFilter — фильтр для List load_balancers.
//
// ProjectID — обязателен для production-запросов (FGA scoping). Name — точное
// совпадение; Filter — YC-syntax `name="<value>"` (через corelib/filter.Parse).
type LoadBalancerFilter struct {
	ProjectID string
	Name      string
	Filter    string
	// AllowedIDs — per-object FGA allow-set (RBAC sub-phase D §11; iam ListObjects).
	// nil → фильтр не применяется (bypass / authz disabled). len==0 → пустой
	// результат (no-leak). len>0 → `WHERE id = ANY($allowed)` ВНУТРИ SQL ДО LIMIT,
	// чтобы keyset-пагинация была плотной по отфильтрованному набору (D-46).
	AllowedIDs []string
}
