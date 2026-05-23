// Package dto — мост domain ↔ DB-row (DTO[domain → row] / DTO[row → domain]).
//
// Использует generic DTO Interface из internal/dto/base.go (evgeniy §3.C).
//
// TODO(KAC-150): per-resource transfers для loadbalancers / listeners / target_groups
// / targets / attached_target_groups + JSONB → HealthCheck.
package dto
