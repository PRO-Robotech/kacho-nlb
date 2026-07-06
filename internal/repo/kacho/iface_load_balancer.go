// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package kacho

import (
	"context"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// LoadBalancerReaderIface — read-операции NetworkLoadBalancer.
type LoadBalancerReaderIface interface {
	// Get возвращает LB по id. Pgx ErrNoRows → ErrNotFound с с фиксированным текстом
	// текст ошибки по конвенции Kachō `"NetworkLoadBalancer <id> not found"`.
	Get(ctx context.Context, id string) (*LoadBalancerRecord, error)

	// List — cursor-based pagination + filter. Возвращает page +
	// next-page-token (empty если страниц больше нет).
	List(ctx context.Context, f LoadBalancerFilter, p Pagination) ([]*LoadBalancerRecord, string, error)

	// ListByProject — short-circuit удобный wrapper над List для FGA-scoped
	// per-project listing. Идентичен List с ProjectID-фильтром.
	ListByProject(ctx context.Context, projectID string, p Pagination) ([]*LoadBalancerRecord, string, error)

	// HasListeners — `EXISTS` под FK-precheck в Delete UseCase.
	HasListeners(ctx context.Context, lbID string) (bool, error)

	// HasAttachedTargetGroups — `EXISTS` для Delete-precheck + AttachedTG
	// pre-Move check.
	HasAttachedTargetGroups(ctx context.Context, lbID string) (bool, error)
}

// LoadBalancerWriterIface — write-операции + read (writer видит свои writes).
//
// DML-методы НЕ открывают свою TX и НЕ emit'ят outbox — caller (use-case)
// вызывает `RepositoryWriter.Outbox.Emit` после успешного DML; atomicity
// DML + outbox гарантируется тем, что обе операции идут через одну pgx.Tx
// (writer-instance).
type LoadBalancerWriterIface interface {
	LoadBalancerReaderIface

	// Insert — INSERT load_balancers RETURNING полный record. UNIQUE-violation
	// на (project_id, name) → ErrAlreadyExists; CHECK-violation → ErrInvalidArg.
	Insert(ctx context.Context, lb *domain.LoadBalancer) (*LoadBalancerRecord, error)

	// Update — UPDATE load_balancers SET name/description/labels/...
	// (immutable: type, region_id, project_id). Status меняется отдельно
	// через SetStatusCAS. expectedXmin — OCC-snapshot из предшествующего Get
	// (record.Xmin); concurrent-modify между Get и Update → 0 rows →
	// ErrFailedPrecondition (защита от lost update на partial-mask Update).
	Update(ctx context.Context, lb *domain.LoadBalancer, expectedXmin string) (*LoadBalancerRecord, error)

	// AttachVIP — атомарный CAS-attach anycast-VIP одного семейства (IPV4/IPV6) к
	// LB-строке: UPDATE … SET address_<fam>=$, address_id_<fam>=$, vip_origin_<fam>=$
	// WHERE id=$ AND (address_<fam>='' OR address_<fam>=$new) RETURNING. 0 rows →
	// ErrFailedPrecondition (семейство уже несёт другой адрес; повтор того же —
	// идемпотентный no-op). per-region UNIQUE 23505 → generic ErrFailedPrecondition
	// (анти-oracle); status-aware CHECK 23514 → ErrInvalidArg (семейство не в
	// ip_families ДО persist). Single-VIP-per-LB на DB-уровне.
	AttachVIP(ctx context.Context, id string, family domain.IPVersion, address, addressID string, origin domain.VipOrigin) (*LoadBalancerRecord, error)

	// SetStatusCAS — atomic compare-and-swap на status-колонке. expected — ожидаемый
	// текущий статус (например `STOPPED`); newStatus — целевой. 0 affected
	// (CAS-miss или row absent) → ErrFailedPrecondition. Skill workspace
	// CLAUDE.md «Within-service refs — DB-уровень».
	SetStatusCAS(ctx context.Context, id string, expected, newStatus domain.LBStatus) (*LoadBalancerRecord, error)

	// MoveProject меняет project_id у LB и каскадно — у его листенеров (denorm
	// sync в одной TX). Возвращает обновлённый LB-record.
	MoveProject(ctx context.Context, id, newProjectID string) (*LoadBalancerRecord, error)

	// Delete — DELETE load_balancers WHERE id=$1. FK-violation (есть дети —
	// listeners / attached_target_groups) → ErrFailedPrecondition. row absent
	// → ErrNotFound. Безусловный — используется для compensation-rollback в
	// Create (там deletion_protection не может помешать откату).
	Delete(ctx context.Context, id string) error

	// DeleteIfUnprotected — atomic guarded delete для user-facing Delete-воркера:
	// DELETE ... WHERE id=$1 AND deletion_protection=false. Инвариант «защищённый
	// LB не удаляется» прибит на DB-уровне —
	// sync-precheck в use-case'е только UX; конкурентный Update(protection=true)
	// между precheck и apply здесь пресекается атомарно. 0 rows при существующем
	// LB → ErrFailedPrecondition; row absent → ErrNotFound; FK-violation → ErrFailedPrecondition.
	DeleteIfUnprotected(ctx context.Context, id string) error
}
