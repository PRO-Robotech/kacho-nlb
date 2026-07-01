// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package loadbalancer

import (
	"context"

	geoclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/geo"
	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// Port-интерфейсы use-case-слоя NetworkLoadBalancer (Clean Architecture).
//
// Use-case'ы внутри пакета зависят ТОЛЬКО от этих port-ов; конкретные реализации
// (pgx-Repository, gRPC-typed-clients, FGA writer) инжектируются в composition
// root (`cmd/kacho-loadbalancer/main.go`). Тесты подменяют port'ы на ручные
// двойники (см. *_mock_test.go в этом же пакете).

// Repo — корневой CQRS-Repository kacho-nlb. Сохранён как алиас, чтобы handler-
// слой не импортировал leaf-пакет `repo/kacho` напрямую под другим именем —
// весь приём идёт через type Repo (читаемая локальная переменная для use-case'ов).
type Repo = kachorepo.Repository

// ProjectClient — Get(projectID) → *iamclient.Project. Используется sync-precheck
// в Create/Move use-case'ах для валидации `project_id` (`InvalidArgument` если
// peer вернул NotFound; `Unavailable` если peer недоступен).
type ProjectClient = iamclient.ProjectClient

// RegionClient — Get(regionID) → *geoclient.Region. Используется sync-precheck
// в Create use-case'е для валидации `region_id` через geo.RegionService.Get
// (kacho-geo; ребро nlb→geo заменило nlb→compute «ради region»).
type RegionClient = geoclient.RegionClient

// NetworkClient — Get(networkID) → *vpcclient.Network. Используется sync-precheck
// в Create use-case'е для валидации `network_id` INTERNAL-LB через
// vpc.NetworkService.Get (ребро nlb→vpc): not-found → `InvalidArgument`, peer
// недоступен → `Unavailable` (fail-closed для мутации).
type NetworkClient = vpcclient.NetworkClient

// InternalAddressClient — узкий VIP-lifecycle port над vpc InternalAddressService:
// per-family auto-аллокация internal VIP из REGIONAL-подсети (AllocateInternalIP /
// AllocateInternalIPv6), BYO-привязка принесённого Address (AttachExisting) и
// release (FreeIP/ClearReference) в compensation/Delete. Используется per-family
// fan-out сагой Create и Delete. Concrete `*vpcclient.internalAddressClient`
// удовлетворяет этот интерфейс структурно.
type InternalAddressClient interface {
	AllocateInternalIP(ctx context.Context, req vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error)
	AllocateInternalIPv6(ctx context.Context, req vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error)
	AttachExisting(ctx context.Context, req vpcclient.AttachExistingRequest) (*vpcclient.AllocateResponse, error)
	FreeIP(ctx context.Context, addressID string, owner vpcclient.AddressOwner) error
	ClearReference(ctx context.Context, addressID string, owner vpcclient.AddressOwner) error
}

// SubnetClient — Get(subnetID) → *vpcclient.Subnet. Используется sync-precheck в
// Create use-case'е: VIP LoadBalancer'а аллоцируется только из REGIONAL-подсети
// (placement_type=REGIONAL). not-found → `InvalidArgument`, ZONAL-подсеть →
// `InvalidArgument`, peer недоступен → `Unavailable` (fail-closed для мутации).
type SubnetClient = vpcclient.SubnetClient

// AddressClient — Get(addressID) → *vpcclient.Address (публичный vpc
// AddressService.Get, authz-gated `v_get`). Используется sync-precheck BYO-VIP в
// Create use-case'е: адрес резолвится под tenant-identity (auth.PropagateOutgoing),
// проверяется принадлежность проекту LB и совпадение семейства. Cross-domain
// ownership-валидация через owner-read API (data-integrity §2): восстанавливает
// project/family-guard, которого больше нет server-side у vpc.SetAddressReference.
type AddressClient = vpcclient.AddressClient

// SecurityGroupClient — Get(sgID) → *vpcclient.SecurityGroup. Используется
// sync-precheck в Create/Update use-case'ах для валидации `security_group_ids`
// INTERNAL-LB через vpc.SecurityGroupService.Get (ребро nlb→vpc): not-found или
// SG чужой сети → `InvalidArgument`, peer недоступен → `Unavailable` (fail-closed
// для мутации).
type SecurityGroupClient = vpcclient.SecurityGroupClient

// Logger — узкий port логгера; вся работа use-case'ов и worker'ов идёт через
// этот интерфейс — concrete *slog.Logger удовлетворяет его автоматически.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	Debug(msg string, args ...any)
}
