// Package pg — PostgreSQL-реализация репозитория kacho-nlb.
//
// Использует pgx/v5 + sqlc (handwritten queries в pg/queries/ + generated в pg/sqlc/);
// порты-интерфейсы определены в internal/repo/kacho/.
//
// TODO(KAC-150): реализация Repository / Reader / Writer + Insert/Update/Delete
// + outbox emit + Operations CRUD + LISTEN/NOTIFY dedicated-conn для drainer'а.
package pg
