// Package pg — pgxpool-implementation CQRS-Repository (skill evgeniy §6 G.1-G.7).
//
// Структура:
//
//	repository.go         — Repository / readerImpl / writerImpl
//	errors.go             — mapPgErr + sentinel mapping
//	outbox_emitter.go     — OutboxEmitter поверх pgx.Tx
//	load_balancer_repo.go — LoadBalancer Reader+Writer impl
//	listener_repo.go      — Listener Reader+Writer impl
//	target_group_repo.go  — TargetGroup + Target Reader+Writer impl
//	attached_tg_repo.go   — AttachedTargetGroup pivot Reader+Writer impl
//
// Все DML использует handwritten pgx (никаких ORM — workspace CLAUDE.md
// «Запреты» #3). Writer-методы НЕ открывают свою TX и НЕ emit'ят outbox —
// caller (use-case) вызывает `RepositoryWriter.Outbox().Emit(...)` после
// успешного DML; atomicity DML + outbox гарантируется одной pgx.Tx
// writer'а (skill evgeniy §6 G.5).
package pg
