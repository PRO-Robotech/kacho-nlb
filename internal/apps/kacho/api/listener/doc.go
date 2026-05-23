// Package listener — gRPC handler + per-RPC UseCases for the
// kacho.cloud.loadbalancer.v1.ListenerService.
//
// Scope (design §3.3 / acceptance §4 — 26 GWT-LST-* scenarios):
//
//   - Get / List          — sync reads.
//   - Create              — async; VIP allocation: BYO `address_id` (atomic
//     SetReference CAS on existing vpc.Address) ИЛИ auto-alloc via
//     vpc.InternalAddressService.AllocateExternalIP/AllocateInternalIP.
//   - Update              — async; mutable fields only (name/description/labels/
//     default_target_group_id/proxy_protocol_v2). Immutable load_balancer_id/
//     protocol/port/ip_version/address_id rejected sync с verbatim YC text
//     `"<field> is immutable after Listener.Create"`.
//   - Delete              — async; free VIP back to pool (auto-alloc) либо
//     clear used_by (BYO); DELETE listener row; emit DELETED + LB UPDATED.
//   - ListOperations      — sync; per-resource history wrapper над
//     `kacho-corelib/operations.Repo.List(filter=resource_id)`.
//
// Architectural pillars (workspace CLAUDE.md «Чистая архитектура» + evgeniy §2.B):
//
//   - Handler — thin transport: parse request → call UseCase → dto.Transfer →
//     proto response. No business logic, no validation beyond `id is required`.
//   - Each UseCase = one file; receives port-interfaces (declared в ports.go)
//     через конструктор (composition root wires concrete adapters).
//   - DB writes + outbox emit live in one writer-TX (`kachorepo.RepositoryWriter`,
//     evgeniy §6 G.5).
//   - Long-running ops via `operations.Run(callerCtx, opsRepo, opID, fn)` —
//     handler returns Operation immediately; worker propagates baggage values
//     (slog logger, principal) but не наследует caller deadline.
//
// Compensation saga (design §4.9 / GWT-LST-015):
//   - Create BYO   → SetReference CAS fails → worker returns InvalidArgument /
//     FailedPrecondition; no listener row written, no VIP to release.
//   - Create AUTO  → AllocateExternalIP/AllocateInternalIP succeeded but
//     subsequent INSERT failed → `defer compensation: FreeIP(addr_id)` on
//     return path. Best-effort, не 2PC; failure of compensation is logged but
//     does not change Operation error (caller already sees the original error).
//   - Delete       → FreeIP / ClearReference failure marks listener `FAILED`
//     in outbox + retains row with `status='DELETING'`; `jobs/free_ip_runner`
//     (Wave 9 follow-up) eventually retries. Within this Wave Delete returns
//     Unavailable если peer vpc недоступен; row остаётся в DELETING.
//
// FGA hierarchy tuple emit (D-11 sync hierarchy):
//   - `iam.HierarchyWriter.WriteCreatorTuple(subject, "owner", "nlb_listener:<id>")`
//     написан ПОСЛЕ commit'а listener row (best-effort, non-fatal — listener row
//     already durable; tuple write failure is logged for operator).
//   - Parent-link tuple `nlb_listener:<id>#load_balancer@nlb_load_balancer:<lb_id>`
//     написан тем же writer (acceptance §4.1 D-11 sync writer; LST-001 outcome:
//     "FGA tuple D-11 sync write before commit: ... via D-13 ... #load_balancer").
//
// Test layout (test-first per workspace CLAUDE.md §11):
//   - *_test.go — unit (in-package), table-driven, fake-port adapters.
//   - integration_test.go — testcontainers Postgres; verifies UNIQUE race
//     (LST-010 / LST-011) + outbox emit + LB region/project denorm correctness.
package listener
