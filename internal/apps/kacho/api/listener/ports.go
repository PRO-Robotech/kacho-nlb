// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package listener

import (
	"context"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// Port interfaces for the listener package (workspace CLAUDE.md «Чистая
// архитектура»): use-cases depend on these abstractions, not on concrete
// adapters. Adapters live in `internal/clients/*` and `internal/repo/kacho/pg`;
// composition root (`cmd/kacho-loadbalancer/main.go`) wires them в Handler.

// RepoFactory — opens read/write transactions over kacho-nlb DB.
// Aliased from `internal/repo/kacho.Repository` to keep package boundary clean.
type RepoFactory = kachorepo.Repository

// OperationsRepo — async LRO repo (shared `kacho-corelib/operations.Repo`).
// Aliased to local name so use-cases don't reach into corelib by full path.
type OperationsRepo = operations.Repo

// InternalAddressClient — write-side vpc.InternalAddressService consumer.
// VIP консолидирован на LoadBalancer, поэтому листенер сам адрес не аллоцирует;
// клиент остаётся только для release legacy-VIP в Delete (FreeIP / ClearReference)
// — pre-cut листенеры до hard-cut могли нести собственный address_id.
type InternalAddressClient = vpcclient.InternalAddressClient

// FGA owner-hierarchy / creator / parent-link tuple-регистрация — через
// transactional-outbox (FGARegisterOutbox emit в writer-tx + register-drainer →
// IAM), не прямым FGA-клиентом. FGA object-types / relations — `internal/domain`.

// FGA object-type strings live in `internal/domain` (single source of truth,
// kacho-nlb-wide): `domain.FGAObjectTypeListener` / `domain.FGAObjectTypeLoadBalancer`.

// outboxResourceTypeListener / outboxResourceTypeLoadBalancer — resource_type
// в `nlb_outbox` (ограничено CHECK CONSTRAINT в миграции 0001).
const (
	outboxResourceTypeListener     = "nlb_listener"
	outboxResourceTypeLoadBalancer = "nlb_load_balancer"
)

// Outbox action strings (CHECK constraint в nlb_outbox; см. миграцию 0001).
const (
	outboxActionCreated = "CREATED"
	outboxActionUpdated = "UPDATED"
	outboxActionDeleted = "DELETED"
	outboxActionFailed  = "FAILED"
)

// FGA relation strings live in `internal/domain`:
// `domain.FGARelationAdmin` / `domain.FGARelationLoadBalancer`.

// permissionsCtxAccessor — port для извлечения acting subject FGA-id из ctx.
// На E0 (без auth-interceptor) возвращает "" → creator tuple не пишется
// (best-effort). На E2+ — заполняется api-gateway auth-interceptor через
// `operations.WithPrincipal(ctx, p)`; адаптер тянет принципала и форматирует
// FGA-subject (`<type>:<id>`).
type permissionsCtxAccessor interface {
	SubjectFromContext(ctx context.Context) string
}

// principalSubjectAccessor — реализация на базе `operations.PrincipalFromContext`.
// Возвращает `<type>:<id>` если оба поля заполнены и тип != "system";
// иначе "" — anonymous/system, creator tuple не пишется.
type principalSubjectAccessor struct{}

// SubjectFromContext — см. permissionsCtxAccessor. Delegates to
// `domain.FGASubjectFromPrincipal` so the subject-string format stays in one
// place across LB/Listener/TG.
func (principalSubjectAccessor) SubjectFromContext(ctx context.Context) string {
	p := operations.PrincipalFromContext(ctx)
	return domain.FGASubjectFromPrincipal(p.Type, p.ID)
}
