// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package outbox — doc-only stub-пакет.
//
// Port-интерфейс OutboxEmitter живёт в leaf-пакете `internal/repo/kacho`
// (см. iface.go и outbox_emitter.go). Trigger `nlb_outbox_notify_trg`
// шлёт `pg_notify('nlb_outbox', sequence_no::text)` на каждый INSERT.
package outbox
