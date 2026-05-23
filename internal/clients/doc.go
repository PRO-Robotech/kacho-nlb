// Package clients — gRPC-клиенты к peer-сервисам.
//
// TODO(KAC-151): по одному файлу на peer.
//   - vpc_client.go — InternalAddressService / AddressService / SubnetService /
//     NetworkInterfaceService (validation NIC / IPAM aux).
//   - compute_client.go — InstanceService / RegionService (target resolve / region
//     existence).
//   - iam_client.go — ProjectService / InternalIAMService (project existence + Check
//     + WriteCreatorTuple).
//
// Каждый клиент реализует port-интерфейс из соответствующего use-case-пакета.
// Wiring — в cmd/kacho-loadbalancer/main.go (composition root); domain/service
// о существовании конкретных gRPC-stub'ов не знают (workspace CLAUDE.md
// "Чистая архитектура").
package clients
