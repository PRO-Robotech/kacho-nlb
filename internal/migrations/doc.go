// Package migrations — embedded SQL миграции kacho-nlb (схема `kacho_nlb`).
//
// TODO(KAC-148): добавить
//   - 0001_initial.sql — baseline (squashed): operations / loadbalancers / listeners
//     / target_groups / targets / attached_target_groups / nlb_outbox + индексы + FK
//     + DB-CHECK same-region + partial UNIQUE NULLS NOT DISTINCT на targets identity +
//     UNIQUE (region_id, allocated_address, port, protocol) WHERE status!='DELETING' на listeners +
//     trigger nlb_outbox_notify_trg + trigger lb_status_recompute.
//   - embed.FS handle (FS variable) — потребляется cmd/migrator/main.go и
//     cmd/kacho-loadbalancer/main.go (на serve startup).
package migrations

import "embed"

// FS — embedded набор миграций. TODO(KAC-148): добавить //go:embed *.sql
// после появления 0001_initial.sql.
var FS embed.FS
