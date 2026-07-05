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
// слой не импортировал leaf-пакет `repo/kacho` напрямую под другим именем.
type Repo = kachorepo.Repository

// ProjectClient — Get(projectID) → *iamclient.Project. sync-precheck `project_id`
// в Create/Move (NotFound → InvalidArgument; недоступен → Unavailable).
type ProjectClient = iamclient.ProjectClient

// CheckClient — per-object FGA authorization gate (iam.InternalIAMService.Check).
// Move использует его для авторизации caller'а на DESTINATION project (`editor on
// project:<dst>`) — per-RPC interceptor проверяет только source-ресурс, поэтому
// dst-authz — задача handler'а (audit SEC-high #2). nil → check пропускается
// (dev/unwired; breakglass также обходит source-check).
type CheckClient = iamclient.CheckClient

// RegionClient — Get(regionID) → *geoclient.Region. sync-precheck `region_id`
// через geo.RegionService.Get (ребро nlb→geo).
type RegionClient = geoclient.RegionClient

// ZoneClient — ListZoneIDsInRegion(regionID) → зоны региона. sync-precheck
// disabled_announce_zones (зоны ∈ регион, не все зоны) и деривация underlying-зоны
// public-VIP (EXTERNAL auto) через geo.ZoneService.List (ребро nlb→geo).
type ZoneClient = geoclient.ZoneClient

// InternalAddressClient — VIP-lifecycle port над vpc InternalAddressService:
// per-family auto-аллокация (AllocateInternalIP/IPv6 из подсети, AllocateExternalIP/
// IPv6 — платформенный public), link существующего Address (AttachExisting) и
// release (FreeIP/ClearReference) в compensation/Delete. Concrete
// `*vpcclient.internalAddressClient` удовлетворяет интерфейс структурно.
type InternalAddressClient interface {
	AllocateInternalIP(ctx context.Context, req vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error)
	AllocateInternalIPv6(ctx context.Context, req vpcclient.AllocateInternalIPRequest) (*vpcclient.AllocateResponse, error)
	AllocateExternalIP(ctx context.Context, req vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error)
	AllocateExternalIPv6(ctx context.Context, req vpcclient.AllocateExternalIPRequest) (*vpcclient.AllocateResponse, error)
	AttachExisting(ctx context.Context, req vpcclient.AttachExistingRequest) (*vpcclient.AllocateResponse, error)
	FreeIP(ctx context.Context, addressID string) error
	ClearReference(ctx context.Context, addressID string) error
}

// SubnetClient — Get(subnetID) → *vpcclient.Subnet. sync-precheck placement подсети
// (== placement LB) + derived network_id (dualstack same-network) через
// vpc.SubnetService.Get. not-found → InvalidArgument; недоступен → Unavailable.
type SubnetClient = vpcclient.SubnetClient

// AddressClient — Get(addressID) → *vpcclient.Address (публичный vpc
// AddressService.Get, authz-gated `v_get`). sync-precheck link-источника: адрес
// резолвится под tenant-identity, проверяются kind/family/ownership/placement.
// Анти-oracle: несоответствие/no-access → generic InvalidArgument.
type AddressClient = vpcclient.AddressClient
