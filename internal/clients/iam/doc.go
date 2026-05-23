// Package iam — typed adapter-клиенты к kacho-iam (Clean Architecture
// outbound adapter, KAC-151).
//
// Содержит:
//
//   - ProjectClient    — kacho.iam.v1.ProjectService.Get; project existence +
//     account lookup для Listener.Create / TargetGroup.Create.
//   - CheckClient      — kacho.iam.v1.InternalIAMService.Check; per-RPC FGA
//     authorization gate (consumer — Wave 8 authz interceptor).
//   - HierarchyWriter  — kacho.iam.v1.InternalIAMService.WriteCreatorTuple +
//     D-11 sync hierarchy tuple write перед Operation.Commit (Listener /
//     TargetGroup / NetworkLoadBalancer create workers).
//
// Все три adapter'а оборачивают gRPC-status в sentinel-ошибки из
// `internal/domain` (`domain.ErrNotFound` / `domain.ErrFailedPrecondition` /
// `domain.ErrUnavailable` / `domain.ErrInvalidArg`) — service-слой работает
// только через port-интерфейсы и не знает о существовании конкретных
// gRPC-stub'ов.
package iam
