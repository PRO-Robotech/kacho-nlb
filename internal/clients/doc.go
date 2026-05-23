// Package clients — gRPC-клиенты к peer-сервисам (Clean Architecture
// outbound adapters).
//
// Структура — по одному подпакету на peer-домен; в каждом — типизированный
// клиент-adapter, реализующий port-интерфейс из соответствующего use-case-пакета
// (workspace CLAUDE.md «Чистая архитектура»: domain/service не знают о существовании
// конкретных gRPC-stub'ов; wiring — в `cmd/kacho-loadbalancer/main.go`):
//
//   - iam     — ProjectClient / CheckClient / HierarchyWriter
//     (iam.ProjectService.Get + iam.InternalIAMService.{Check, WriteCreatorTuple}).
//     Project existence + per-RPC FGA Check + D-11 sync creator-tuple write.
//   - compute — RegionClient / InstanceClient
//     (compute.RegionService.Get/ListZones + compute.InstanceService.Get).
//     Region/zone validation + Target.instance_id resolve.
//   - vpc     — SubnetClient / NetworkInterfaceClient / AddressClient /
//     InternalAddressClient (vpc.SubnetService.Get + NetworkInterfaceService.Get +
//     AddressService.{Get,Create,Delete} + InternalAddressService.{AllocateExternalIP,
//     AllocateInternalIP, FreeIP, SetReference, ClearReference}).
//     Listener.subnet validation + Target.nic_id resolve + VIP allocation/free.
//
// Конструктор каждого adapter'а принимает `grpc.ClientConnInterface`, что
// совместимо и с `*grpc.ClientConn`, и с corlib `ClientConn` из `builder.go`.
package clients
