package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
)

// Repository — pgxpool-impl корневого CQRS-контракта (kacho.Repository).
//
// Skill evgeniy §6 G.4: Reader идёт на slave-реплику, Writer — на master.
// Если slavePool не настроен — Reader открывает read-only TX на master-pool'е
// (fallback, текущее dev/prod-поведение). Когда реальная реплика появится —
// composition root передаёт второй pool, переключение прозрачно для use-case'ов.
type Repository struct {
	master *pgxpool.Pool
	slave  *pgxpool.Pool
}

// New собирает Repository поверх master- и опц. slave-pool'ов.
//
//   - masterPool — RW pgxpool на primary; используется Writer + Reader-fallback.
//   - slavePool  — RO pgxpool на streaming-replica; если nil → Reader идёт на
//     master (fallback).
//
// Pools создаются в composition root (kacho-corelib/db.NewPool).
func New(masterPool, slavePool *pgxpool.Pool) *Repository {
	if slavePool == nil {
		slavePool = masterPool
	}
	return &Repository{master: masterPool, slave: slavePool}
}

// Reader открывает read-only TX (read-committed) на slave-pool'е (или master
// fallback). Возвращённый reader обязан быть закрыт через Close() — это
// rollback'ит TX и возвращает соединение в пул.
func (r *Repository) Reader(ctx context.Context) (kacho.RepositoryReader, error) {
	tx, err := r.slave.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	return &readerImpl{tx: tx}, nil
}

// Writer открывает RW TX на master-pool'е. Caller обязан вызвать либо Commit(),
// либо Abort() (Abort идемпотентен — безопасно через defer сразу после открытия).
func (r *Repository) Writer(ctx context.Context) (kacho.RepositoryWriter, error) {
	tx, err := r.master.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	return &writerImpl{tx: tx}, nil
}

// Close — no-op (pool'ы управляются composition root, не репозиторием).
// Метод есть в Repository-интерфейсе чтобы тестовый код мог .Close() мокать
// без reach'а в pool.
func (r *Repository) Close() {}

// readerImpl — read-only TX state.
type readerImpl struct {
	tx     pgx.Tx
	closed bool
}

func (r *readerImpl) LoadBalancers() kacho.LoadBalancerReaderIface {
	return &loadBalancerReader{tx: r.tx}
}

func (r *readerImpl) Listeners() kacho.ListenerReaderIface {
	return &listenerReader{tx: r.tx}
}

func (r *readerImpl) TargetGroups() kacho.TargetGroupReaderIface {
	return &targetGroupReader{tx: r.tx}
}

func (r *readerImpl) AttachedTargetGroups() kacho.AttachedTargetGroupReaderIface {
	return &attachedTGReader{tx: r.tx}
}

// Close rollback'ит read-TX (read-only TX — rollback не имеет side-effects).
// Идемпотентно. Игнорирует pgx.ErrTxClosed.
func (r *readerImpl) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if err := r.tx.Rollback(context.Background()); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return err
	}
	return nil
}

// writerImpl — RW TX state.
type writerImpl struct {
	tx        pgx.Tx
	finalised bool // true после Commit() или Abort() — защита от double-finalize
}

// Writer-side returns: G.2 — writer видит свои writes (reader-методы — поверх
// той же pgx.Tx).
func (w *writerImpl) LoadBalancers() kacho.LoadBalancerWriterIface {
	return &loadBalancerWriter{
		loadBalancerReader: loadBalancerReader{tx: w.tx},
	}
}

func (w *writerImpl) Listeners() kacho.ListenerWriterIface {
	return &listenerWriter{
		listenerReader: listenerReader{tx: w.tx},
	}
}

func (w *writerImpl) TargetGroups() kacho.TargetGroupWriterIface {
	return &targetGroupWriter{
		targetGroupReader: targetGroupReader{tx: w.tx},
	}
}

func (w *writerImpl) AttachedTargetGroups() kacho.AttachedTargetGroupWriterIface {
	return &attachedTGWriter{
		attachedTGReader: attachedTGReader{tx: w.tx},
	}
}

// Outbox — emit события в `nlb_outbox` в той же tx-области writer'а.
// DML + outbox-emit атомарны (skill evgeniy §6 G.5).
func (w *writerImpl) Outbox() kacho.OutboxEmitter {
	return &outboxEmitter{tx: w.tx}
}

// Commit финализирует write-TX. После Commit вызов Abort — no-op.
func (w *writerImpl) Commit() error {
	if w.finalised {
		return nil
	}
	w.finalised = true
	return w.tx.Commit(context.Background())
}

// Abort откатывает TX. Идемпотентен — после Commit no-op, можно ставить через
// defer сразу после открытия writer'а.
func (w *writerImpl) Abort() {
	if w.finalised {
		return
	}
	w.finalised = true
	_ = w.tx.Rollback(context.Background())
}
