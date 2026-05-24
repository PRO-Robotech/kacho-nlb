package targetgroup

import (
	"github.com/PRO-Robotech/kacho-corelib/operations"

	computeclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// Port-интерфейсы use-case-слоя TargetGroupService (Clean Architecture / evgeniy §2.B).
//
// Use-case'ы внутри пакета зависят ТОЛЬКО от этих port-ов; конкретные реализации
// (pgx-Repository, gRPC-typed-clients, FGA writer) инжектируются в composition
// root (`cmd/kacho-loadbalancer/main.go`). Тесты подменяют port'ы на ручные
// двойники (см. fakes_test.go в этом же пакете).

// Repo — корневой CQRS-Repository kacho-nlb. Алиас на `kacho.Repository`, чтобы
// handler-слой не импортировал leaf-пакет под полным путём.
type Repo = kachorepo.Repository

// OpsRepo — async LRO repo (kacho-corelib operations).
type OpsRepo = operations.Repo

// ProjectClient — iam.ProjectService.Get adapter.
type ProjectClient = iamclient.ProjectClient

// RegionClient — compute.RegionService.Get adapter (zone-list).
type RegionClient = computeclient.RegionClient

// InstanceClient — compute.InstanceService.Get adapter. Используется
// AddTargets-worker'ом для per-target instance-resolve + region-validate.
type InstanceClient = computeclient.InstanceClient

// NetworkInterfaceClient — vpc.NetworkInterfaceService.Get adapter. Используется
// AddTargets-worker'ом для per-target nic-resolve.
type NetworkInterfaceClient = vpcclient.NetworkInterfaceClient

// SubnetClient — vpc.SubnetService.Get adapter. Используется AddTargets-worker'ом
// для ip_ref-target peer-validate (Subnet existence + IP-in-CIDR + region-match).
type SubnetClient = vpcclient.SubnetClient

// HierarchyWriter — iam.InternalIAMService.WriteCreatorTuple wrapper для D-11
// sync hierarchy tuple emit после Create / Move worker commit.
type HierarchyWriter = iamclient.HierarchyWriter

// FGA object-types / relations — moved to `internal/fgawrite` (single source of
// truth, kacho-nlb-wide). Use `fgawrite.ObjectTypeTargetGroup`,
// `fgawrite.RelationOwner`, `fgawrite.RelationProject`.
