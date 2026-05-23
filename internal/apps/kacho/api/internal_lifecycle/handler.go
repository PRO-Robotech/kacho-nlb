// Package internal_lifecycle — Internal endpoint InternalResourceLifecycleService
// (server-stream Subscribe для D-13 — потребитель kacho-iam, не наружу).
//
// Подписка iam на lifecycle-events ресурсов nlb_load_balancer / nlb_listener /
// nlb_target_group (CREATED / UPDATED / DELETED) для синхронизации FGA tuples.
// Слушает `nlb_outbox` LISTEN/NOTIFY на dedicated pgx.Conn (workspace §запрет 7).
//
// Internal endpoint — порт 9091, НЕ маршрутизируется через api-gateway external
// TLS endpoint (workspace §запрет 6).
//
// TODO(KAC-166): реализация через corelib/resourcelifecycle.
package internal_lifecycle
