// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package vpc — typed adapter-клиенты к kacho-vpc (Clean Architecture
// outbound adapter).
//
// kacho-vpc — owner Network/Subnet/SecurityGroup/Address/NetworkInterface.
// kacho-nlb валидирует:
//
//   - Listener.subnet_id (INTERNAL listener) → vpc.SubnetService.Get +
//     IP-in-CIDR check для manual ip_ref.
//   - Listener.address_id (BYO) → vpc.AddressService.Get + atomic CAS
//     SetReference / ClearReference на Listener.Create / Listener.Delete.
//   - Target.nic_id → vpc.NetworkInterfaceService.Get; PrimaryV4Address resolve.
//   - Listener.Create auto-alloc VIP → vpc.InternalAddressService.{AllocateExternalIP,
//     AllocateInternalIP}; Listener.Delete FreeIP (idempotent через
//     vpc.AddressService.Delete).
//
// Все adapter'ы возвращают sentinel-ошибки из `internal/domain`; service-слой
// работает только через port-интерфейсы и не знает о gRPC-stub'ах.
package vpc
