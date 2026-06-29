// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package clients — gRPC-клиенты к peer-сервисам (Clean Architecture
// outbound adapters).
//
// Структура — по одному подпакету на peer-домен; в каждом — типизированный
// клиент-adapter, реализующий port-интерфейс из соответствующего use-case-пакета
// (workspace CLAUDE.md «Чистая архитектура»: domain/service не знают о существовании
// конкретных gRPC-stub'ов; wiring — в `cmd/kacho-loadbalancer/main.go`):
//
//   - iam     — ProjectClient / CheckClient / fga-register applier
//     (iam.ProjectService.Get + iam.InternalIAMService.{Check, RegisterResource, UnregisterResource}).
//     Project existence + per-RPC FGA Check + owner-tuple register/unregister via fga_register_outbox drainer.
//   - geo     — RegionClient (geo.RegionService.Get).
//     region_id validation (stateless pass-through, без кэша; kacho-geo).
//   - compute — InstanceClient (compute.InstanceService.Get).
//     Target.instance_id resolve (instance-resolve — НЕ geography).
//   - vpc     — SubnetClient / NetworkInterfaceClient / AddressClient /
//     InternalAddressClient (vpc.SubnetService.Get + NetworkInterfaceService.Get +
//     AddressService.{Get,Create,Delete} + InternalAddressService.{AllocateExternalIP,
//     AllocateInternalIP, FreeIP, SetReference, ClearReference}).
//     Listener.subnet validation + Target.nic_id resolve + VIP allocation/free.
//
// Конструктор каждого adapter'а принимает `grpc.ClientConnInterface`, что
// совместимо и с `*grpc.ClientConn`, и с corlib `ClientConn` из `builder.go`.
package clients
