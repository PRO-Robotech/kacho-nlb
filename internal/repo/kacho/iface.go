// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package kacho — корневой Repository интерфейс kacho-nlb (CQRS).
//
// Разделение Reader/Writer по TX-снапшоту:
//   - RepositoryReader открывает read-TX (в будущем — на slave-реплику).
//   - RepositoryWriter открывает write-TX на master; держит outbox-эмит в той же TX.
//   - UseCase сам управляет TX: Writer.Commit / Abort (defer-style).
//
// Реализации:
//   - internal/repo/kacho/pg/ — pgx5 + pgxpool на master/slave.
//
// Pagination — общий объект, разделяемый между всеми per-resource Reader'ами.
//
// Все CQRS port-интерфейсы per-resource (LoadBalancer / Listener / TargetGroup /
// AttachedTargetGroup / Outbox / FGARegister) живут ЗДЕСЬ, в этом leaf-пакете —
// а не в отдельных под-пакетах loadbalancer/ listener/ targetgroup/ outbox/.
// Причина — избежать import-cycle (dto/type2pb → repo/kacho → domain) и держать
// единую точку для port-контракта. Не заводите под-пакеты под repo-типы: новый
// Reader/Writer-метод добавляется в соответствующий iface_<resource>.go здесь.
package kacho

import "context"

// Pagination — постраничная навигация. Cursor-based: PageToken — opaque
// base64-encoded (created_at, id) snapshot, PageSize — page-size (0 →
// default 50; max 1000, см. corelib/validate.PageSize).
type Pagination struct {
	PageToken string
	PageSize  int64
}

// Repository — фабрика TX-aware Reader/Writer..
//
// Reader(ctx) открывает read-only TX на slave-pool'е (если настроен) либо на
// master (fallback). Caller обязан вызвать Close.
//
// Writer(ctx) открывает RW TX на master. Caller обязан вызвать либо Commit,
// либо Abort (Abort идемпотентен — безопасно через defer сразу после открытия).
type Repository interface {
	Reader(ctx context.Context) (RepositoryReader, error)
	Writer(ctx context.Context) (RepositoryWriter, error)
	Close()
}

// RepositoryReader — read-only TX-снапшот. Все per-resource reader'ы получают
// pgx.Tx через этот объект и читают одну и ту же snapshot-version.
type RepositoryReader interface {
	LoadBalancers() LoadBalancerReaderIface
	Listeners() ListenerReaderIface
	TargetGroups() TargetGroupReaderIface
	AttachedTargetGroups() AttachedTargetGroupReaderIface
	// Close завершает read-TX (rollback). Идемпотентно.
	Close() error
}

// RepositoryWriter — write-TX. Writer видит свои writes (writer extends
// reader). Outbox-emit живёт здесь же — DML + outbox atomicity гарантируется
// одной pgx.Tx.
type RepositoryWriter interface {
	LoadBalancers() LoadBalancerWriterIface
	Listeners() ListenerWriterIface
	TargetGroups() TargetGroupWriterIface
	AttachedTargetGroups() AttachedTargetGroupWriterIface
	// Outbox — emit события в `nlb_outbox` в той же tx-области writer'а.
	Outbox() OutboxEmitter
	// FGARegisterOutbox — emit FGA-register-intent в `fga_register_outbox` в
	// той же tx-области writer'а (transactional-outbox).
	FGARegisterOutbox() FGARegisterEmitter
	// Commit финализирует tx. После Commit вызов Abort — no-op.
	Commit() error
	// Abort откатывает tx. Идемпотентен — безопасно через `defer w.Abort`
	// сразу после открытия writer'а.
	Abort()
}
