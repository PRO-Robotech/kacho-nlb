// Package kacho — корневой Repository интерфейс kacho-nlb (CQRS per evgeniy §6.G).
//
// Разделение Reader/Writer по TX-снапшоту:
//   - RepositoryReader открывает read-TX (в будущем — на slave-реплику).
//   - RepositoryWriter открывает write-TX на master; держит outbox-эмит в той же TX.
//   - UseCase сам управляет TX: Writer().Commit() / Abort() (defer-style).
//
// Реализации:
//   - internal/repo/kacho/pg/ — pgx5 + sqlc.
//
// TODO(KAC-150): полные интерфейсы Reader/Writer + per-resource ifaces + outbox.
package kacho

import "context"

// Repository — фабрика TX-aware Reader/Writer.
type Repository interface {
	Reader(ctx context.Context) (RepositoryReader, error)
	Writer(ctx context.Context) (RepositoryWriter, error)
	Close() error
}

// RepositoryReader — read-only TX-снапшот.
//
// TODO(KAC-150): методы per-resource (LoadBalancers(), Listeners(), TargetGroups(), Operations()).
type RepositoryReader interface {
	Close() error
}

// RepositoryWriter — write-TX. Все мутации + outbox-emit идут в одной TX,
// чтобы LISTEN/NOTIFY consumer'ы не видели событий о несуществующих строках.
//
// TODO(KAC-150): методы per-resource Writer + Outbox() + Operations().
type RepositoryWriter interface {
	RepositoryReader
	Commit() error
	Abort()
}
