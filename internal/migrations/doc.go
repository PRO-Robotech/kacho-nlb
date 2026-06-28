// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package migrations — embedded SQL миграции kacho-nlb (схема `kacho_nlb`).
//
// Baseline: `0001_initial.sql` — squashed schema, helper-функции
// (`kacho_labels_valid`, `nlb_outbox_notify`, `lb_status_recompute`), tables
// (operations, load_balancers, listeners, target_groups, targets,
// attached_target_groups, nlb_outbox, nlb_watch_cursors), sequences, FK/CHECK/
// UNIQUE/partial-UNIQUE-NULLS-NOT-DISTINCT + triggers.
//
// FS потребляется `cmd/migrator/main.go` (goose up/down/status) и
// `cmd/kacho-loadbalancer/main.go` (на serve startup — health-check / version).
package migrations

import "embed"

// FS — embedded набор миграций (`*.sql`).
//
//go:embed *.sql
var FS embed.FS
