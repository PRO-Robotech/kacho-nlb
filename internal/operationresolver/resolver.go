// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package operationresolver — доменный resolver осиротевших LRO для kacho-nlb.
//
// Движок reconciler'а живёт в kacho-corelib/operations (сканирует таблицу
// operations по grace-окну, клеймит orphan'ы под FOR UPDATE SKIP LOCKED). Сам
// resolver — доменная часть в сервисе: он знает типы метаданных операций nlb
// (*lbv1.<Verb><Resource>Metadata) и сверяет осиротевшую операцию с
// committed-реальностью ресурса через read-порт repo.Get.
//
// Контракт диспетчеризации (writer-TX атомарна, частичных состояний нет):
//   - Create-метаданные: ресурс присутствует → Done(current как Response);
//     отсутствует → Interrupted.
//   - Update / lifecycle-метаданные (Start/Stop/Move/Attach/Detach/AddTargets/…
//     существование ресурса не меняют): присутствует → Done(current);
//     отсутствует → Interrupted.
//   - Delete-метаданные: отсутствует → Done(Empty); присутствует → Interrupted.
//   - неузнанный / nil тип метаданных → Skip (строка остаётся done=false, sweep
//     повторится);
//   - transient-ошибка чтения ресурса → (ResolverResult{}, err): движок
//     инкрементит reconcile_errors и пропускает orphan до следующего sweep'а.
//
// Resolver не делает re-drive (повторный запуск worker-fn) — он приводит статус
// операции в соответствие тому, что реально закоммичено.
package operationresolver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/PRO-Robotech/kacho-corelib/operations"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
)

// LoadBalancerReader / ListenerReader / TargetGroupReader — узкие read-порты трёх
// мутируемых ресурсов nlb. Get возвращает текущий proto-ресурс, либо
// domain.ErrNotFound (absent), либо transient-ошибку. Реализуются adapter'ами в
// composition root поверх Repository.
type LoadBalancerReader interface {
	Get(ctx context.Context, id string) (*lbv1.NetworkLoadBalancer, error)
}

type ListenerReader interface {
	Get(ctx context.Context, id string) (*lbv1.Listener, error)
}

type TargetGroupReader interface {
	Get(ctx context.Context, id string) (*lbv1.TargetGroup, error)
}

// Readers — набор read-портов, инжектируемый composition root'ом. Незаполненный
// (nil) порт → соответствующие orphan'ы пропускаются (Skip).
type Readers struct {
	LoadBalancer LoadBalancerReader
	Listener     ListenerReader
	TargetGroup  TargetGroupReader
}

// kind — категория операции, выводимая из типа метаданных.
type kind int

const (
	kindCreate kind = iota // present → Done(current); absent → Interrupted
	kindUpdate             // как Create (reconcile к committed-реальности, не re-apply)
	kindDelete             // absent → Done(Empty); present → Interrupted
)

// Resolver — доменный resolver nlb поверх узких read-портов репозиториев.
type Resolver struct {
	r   Readers
	log *slog.Logger
}

// Option — функциональная опция Resolver.
type Option func(*Resolver)

// WithLogger подключает структурированный логгер (диагностика resolve).
func WithLogger(l *slog.Logger) Option {
	return func(r *Resolver) {
		if l != nil {
			r.log = l
		}
	}
}

// New конструирует Resolver поверх набора read-портов.
func New(r Readers, opts ...Option) *Resolver {
	rs := &Resolver{r: r, log: slog.Default()}
	for _, o := range opts {
		o(rs)
	}
	return rs
}

// Resolve реализует operations.Resolver: по метаданным осиротевшей операции
// определяет терминальный исход, сверяясь с committed-реальностью ресурса.
func (rs *Resolver) Resolve(ctx context.Context, op operations.Operation) (operations.ResolverResult, error) {
	if op.Metadata == nil {
		return skip(), nil
	}
	msg, err := op.Metadata.UnmarshalNew()
	if err != nil {
		// Неизвестный / неразбираемый тип метаданных — не наша операция в этом
		// прогоне. Skip, а не ошибка: строка остаётся done=false.
		rs.log.Warn("operation resolver: undecodable metadata, skipping orphan",
			"op", op.ID, "type_url", op.Metadata.TypeUrl, "err", err)
		return skip(), nil
	}

	switch m := msg.(type) {
	// ---- NetworkLoadBalancer: Create / Update / Delete ----
	case *lbv1.CreateNetworkLoadBalancerMetadata:
		return resolveExistence(ctx, kindCreate, m.GetNetworkLoadBalancerId(), rs.r.LoadBalancer)
	case *lbv1.UpdateNetworkLoadBalancerMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetNetworkLoadBalancerId(), rs.r.LoadBalancer)
	case *lbv1.DeleteNetworkLoadBalancerMetadata:
		return resolveExistence(ctx, kindDelete, m.GetNetworkLoadBalancerId(), rs.r.LoadBalancer)

	// ---- NetworkLoadBalancer lifecycle (existence-preserving) → reconcile к current ----
	case *lbv1.StartNetworkLoadBalancerMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetNetworkLoadBalancerId(), rs.r.LoadBalancer)
	case *lbv1.StopNetworkLoadBalancerMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetNetworkLoadBalancerId(), rs.r.LoadBalancer)
	case *lbv1.MoveNetworkLoadBalancerMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetNetworkLoadBalancerId(), rs.r.LoadBalancer)
	case *lbv1.AttachNetworkLoadBalancerTargetGroupMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetNetworkLoadBalancerId(), rs.r.LoadBalancer)
	case *lbv1.DetachNetworkLoadBalancerTargetGroupMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetNetworkLoadBalancerId(), rs.r.LoadBalancer)

	// ---- Listener: Create / Update / Delete ----
	case *lbv1.CreateListenerMetadata:
		return resolveExistence(ctx, kindCreate, m.GetListenerId(), rs.r.Listener)
	case *lbv1.UpdateListenerMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetListenerId(), rs.r.Listener)
	case *lbv1.DeleteListenerMetadata:
		return resolveExistence(ctx, kindDelete, m.GetListenerId(), rs.r.Listener)

	// ---- TargetGroup: Create / Update / Delete + lifecycle ----
	case *lbv1.CreateTargetGroupMetadata:
		return resolveExistence(ctx, kindCreate, m.GetTargetGroupId(), rs.r.TargetGroup)
	case *lbv1.UpdateTargetGroupMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetTargetGroupId(), rs.r.TargetGroup)
	case *lbv1.DeleteTargetGroupMetadata:
		return resolveExistence(ctx, kindDelete, m.GetTargetGroupId(), rs.r.TargetGroup)
	case *lbv1.MoveTargetGroupMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetTargetGroupId(), rs.r.TargetGroup)
	case *lbv1.AddTargetsMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetTargetGroupId(), rs.r.TargetGroup)
	case *lbv1.RemoveTargetsMetadata:
		return resolveExistence(ctx, kindUpdate, m.GetTargetGroupId(), rs.r.TargetGroup)

	default:
		// Прочие (не-операционные / unwired) типы метаданных — не наши.
		return skip(), nil
	}
}

// resolveExistence — общая логика «существование ресурса → терминальный исход».
// reader.Get читает proto-ресурс (domain.ErrNotFound → отсутствует). Если reader
// не сконфигурирован (nil — dev/неполный wiring), orphan пропускается (Skip).
func resolveExistence[T proto.Message](
	ctx context.Context,
	k kind,
	id string,
	reader interface {
		Get(context.Context, string) (T, error)
	},
) (operations.ResolverResult, error) {
	if reader == nil {
		return skip(), nil
	}
	rec, err := reader.Get(ctx, id)
	present := false
	switch {
	case err == nil:
		present = true
	case errors.Is(err, domain.ErrNotFound):
		present = false
	default:
		// transient read-ошибка → движок инкрементит reconcile_errors, пропускает.
		return operations.ResolverResult{}, fmt.Errorf("operationresolver: get %q: %w", id, err)
	}

	if k == kindDelete {
		if present {
			return interrupted(), nil
		}
		return done(nil), nil // Empty-семантика
	}
	// Create / Update / lifecycle.
	if !present {
		return interrupted(), nil
	}
	resp, err := anypb.New(rec)
	if err != nil {
		return operations.ResolverResult{}, fmt.Errorf("operationresolver: marshal %q: %w", id, err)
	}
	return done(resp), nil
}

func skip() operations.ResolverResult {
	return operations.ResolverResult{Outcome: operations.OutcomeSkip}
}

func interrupted() operations.ResolverResult {
	return operations.ResolverResult{Outcome: operations.OutcomeInterrupted}
}

func done(resp *anypb.Any) operations.ResolverResult {
	return operations.ResolverResult{Outcome: operations.OutcomeDone, Response: resp}
}
