// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package listener — gRPC handler + per-RPC UseCases for the
// kacho.cloud.loadbalancer.v1.ListenerService.
//
// Scope (26 scenarios):
//
//   - Get / List          — sync reads.
//   - Create              — async; VIP allocation: BYO `address_id` (atomic
//     SetReference CAS on existing vpc.Address) ИЛИ auto-alloc via
//     vpc.InternalAddressService.AllocateExternalIP/AllocateInternalIP.
//   - Update              — async; mutable fields only (name/description/labels/
//     default_target_group_id/proxy_protocol_v2). Immutable load_balancer_id/
//     protocol/port/ip_version/address_id rejected sync с текст ошибки по конвенции Kachō
//     `"<field> is immutable after Listener.Create"`.
//   - Delete              — async; free VIP back to pool (auto-alloc) либо
//     clear used_by (BYO); DELETE listener row; emit DELETED + LB UPDATED.
//   - ListOperations      — sync; per-resource history wrapper над
//     `kacho-corelib/operations.Repo.List(filter=resource_id)`.
//
// Architectural pillars (Clean Architecture):
//
//   - Handler — thin transport: parse request → call UseCase → dto.Transfer →
//     proto response. No business logic, no validation beyond `id is required`.
//   - Each UseCase = one file; receives port-interfaces (declared в ports.go)
//     через конструктор (composition root wires concrete adapters).
//   - DB writes + outbox emit live in one writer-TX (`kachorepo.RepositoryWriter`).
//   - Long-running ops via `operations.Run(callerCtx, opsRepo, opID, fn)` —
//     handler returns Operation immediately; worker propagates baggage values
//     (slog logger, principal) but не наследует caller deadline.
//
// Compensation saga:
//   - Create BYO   → SetReference CAS fails → worker returns InvalidArgument /
//     FailedPrecondition; no listener row written, no VIP to release.
//   - Create AUTO  → AllocateExternalIP/AllocateInternalIP succeeded but
//     subsequent INSERT failed → `defer compensation: FreeIP(addr_id)` on
//     return path. Best-effort, не 2PC; failure of compensation is logged but
//     does not change Operation error (caller already sees the original error).
//   - Delete       → FreeIP / ClearReference failure marks listener `FAILED`
//     in outbox + retains row with `status='DELETING'`; background
//     `jobs/free_ip_runner` reconciles the stuck row (release-by-address +
//     delete + finalize) on a later tick. Within this Delete returns
//     Unavailable если peer vpc недоступен; row остаётся в DELETING.
//
// FGA owner-hierarchy tuple emit (transactional-outbox, replaces the former
// best-effort direct FGA write —):
//   - creator tuple `<subject> #admin @lb_listener:<id>` (skipped if the principal
//     is system/unauthenticated) + parent-link tuple
//     `lb_network_load_balancer:<lb_id> #load_balancer @lb_listener:<id>` are
//     serialised into a `domain.FGARegisterIntent` and persisted via
//     `w.FGARegisterOutbox.Emit(fga.register, …)` in the SAME writer-tx as the
//     listener INSERT (one commit, no dual-write).
//   - the register-drainer (`cmd/kacho-loadbalancer/main.go`) later applies each
//     tuple through kacho-iam `InternalIAMService.RegisterResource` by mTLS;
//     IAM-down → intent stays durable and is retried (tuple is never lost).
//   - Delete emits the symmetric `fga.unregister` intent (parent-link) →
//     `UnregisterResource`.
//
// Test layout (test-first):
//   - *_test.go — unit (in-package), table-driven, fake-port adapters.
//   - integration_test.go — testcontainers Postgres; verifies UNIQUE race,
//     outbox emit and LB region/project denorm correctness.
package listener
