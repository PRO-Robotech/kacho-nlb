package listener

import (
	"context"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
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

// AddressClient — read-side vpc.AddressService consumer (BYO validation).
type AddressClient = vpcclient.AddressClient

// InternalAddressClient — write-side vpc.InternalAddressService consumer
// (auto-alloc + SetReference CAS + FreeIP / ClearReference compensation).
type InternalAddressClient = vpcclient.InternalAddressClient

// SubnetClient — read-side vpc.SubnetService consumer (INTERNAL Listener
// subnet validation, same project + denormalised region resolve).
type SubnetClient = vpcclient.SubnetClient

// HierarchyWriter — iam.InternalIAMService.WriteCreatorTuple wrapper for
// D-11 sync hierarchy tuple emit after Listener row commit.
type HierarchyWriter = iamclient.HierarchyWriter

// addressOwner — package-internal helper для построения VPC owner tuple
// (`{Kind:"nlb_listener", ID:<listener-id>}`). Используется при alloc,
// SetReference, FreeIP, ClearReference.
func addressOwner(listenerID string) vpcclient.AddressOwner {
	return vpcclient.AddressOwner{
		Kind: addressOwnerKindNLBListener,
		ID:   listenerID,
	}
}

// addressOwnerKindNLBListener — Reference.kind для NLB Listener в vpc.Address
// `used_by` (design §4.2 «owner="nlb_listener:<id>"`).
const addressOwnerKindNLBListener = "nlb_listener"

// fgaObjectTypeListener / fgaObjectTypeLoadBalancer — FGA object type strings
// (design §6.1).
const (
	fgaObjectTypeListener     = "nlb_listener"
	fgaObjectTypeLoadBalancer = "nlb_load_balancer"
)

// outboxResourceTypeListener / outboxResourceTypeLoadBalancer — resource_type
// в `nlb_outbox` (design §3.9; ограничено CHECK CONSTRAINT в миграции 0001).
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

// fgaRelationOwner / fgaRelationLoadBalancer — predicate FGA-relations
// для creator / parent tuples (design §6.1 — Listener inherits from LB).
const (
	fgaRelationOwner        = "owner"
	fgaRelationLoadBalancer = "load_balancer"
)

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

// SubjectFromContext — см. permissionsCtxAccessor.
func (principalSubjectAccessor) SubjectFromContext(ctx context.Context) string {
	p := operations.PrincipalFromContext(ctx)
	if p.Type == "" || p.ID == "" || p.Type == "system" {
		return ""
	}
	return p.Type + ":" + p.ID
}
