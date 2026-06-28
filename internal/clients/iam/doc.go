// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package iam — typed adapter-клиенты к kacho-iam (Clean Architecture
// outbound adapter).
//
// Содержит:
//
//   - ProjectClient           — kacho.iam.v1.ProjectService.Get; project
//     existence + account lookup для Listener.Create / TargetGroup.Create.
//   - CheckClient             — kacho.iam.v1.InternalIAMService.Check; per-RPC
//     FGA authorization gate (consumer — authz interceptor).
//   - RegisterResourceClient  — kacho.iam.v1.InternalIAMService.RegisterResource
//     / UnregisterResource (Internal-only :9091, FGA-proxy); narrow port
//     consumed by the register-drainer.
//   - NewRegisterApplier / DecodeFGARegisterIntent — drainer.Applier/Decoder
//     halves of the `kacho_nlb.fga_register_outbox` drainer. The Create/Delete
//     worker persists a FGARegisterIntent in the resource writer-tx; the drainer
//     reads each row and applies its owner-tuple set through kacho-iam by mTLS
//     (replaces the removed best-effort direct-FGA write — `internal/fgawrite` +
//     HierarchyWriter.WriteCreatorTuple-after-Commit).
//
// ProjectClient / CheckClient оборачивают gRPC-status в sentinel-ошибки из
// `internal/domain` (`domain.ErrNotFound` / `domain.ErrFailedPrecondition` /
// `domain.ErrUnavailable` / `domain.ErrInvalidArg`); the register-applier maps
// the gRPC reply onto the drainer's three-way classification (nil / ErrPermanent
// poison / transient retry). Service-слой работает только через port-интерфейсы
// и не знает о существовании конкретных gRPC-stub'ов.
package iam
