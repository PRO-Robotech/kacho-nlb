// Package outbox — port-интерфейс outbox-эмита (LISTEN/NOTIFY на канале `nlb_outbox`).
//
// TODO(KAC-150): Emit(ctx, event) в той же TX, что и мутация ресурса; trigger
// `nlb_outbox_notify_trg` шлёт `pg_notify('nlb_outbox', sequence_no::text)`.
package outbox
