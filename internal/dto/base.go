// Package dto — table-driven generic-based DTO трансферы (evgeniy §3.C).
//
// Структура:
//   - dto/base.go (этот файл): generic Interface, RegTransfer / FindTransfer,
//     Fn / Fn2Face helper, FromTo + DTO[F,T] pair + Transfer entry-point с
//     type-set generic constraint.
//   - dto/type2pb/*.go: реализации Interface[<src>, <pb>] + init()-регистрация.
//
// Use-case в caller-site:
//
//	var dst *lbv1.NetworkLoadBalancer
//	if err := dto.Transfer(dto.FromTo(rec, &dst)); err != nil { ... }
//	return anypb.New(dst)
//
// Registry — process-level map[reflect.Type]any, заполняется через init() в
// каждом transfer-файле. Type-set constraint Transferrable закрывает множество
// допустимых пар compile-time — попытка вызвать Transfer на незарегистрированной
// паре не компилируется (а не падает в runtime).
package dto

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationv1 "github.com/PRO-Robotech/kacho-corelib/proto/gen/go/kacho/cloud/operation"
	kachorepo "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Interface — generic transfer-функтор F → T (evgeniy §3.C1).
//
// Реализация живёт в подпакете dto/type2pb/ и регистрируется в реестре через
// RegTransfer в init().
type Interface[F any, T any] interface {
	Transfer(F) (T, error)
}

// Fn — adapter: обычная Go-функция как Interface. Удобство для регистрации
// без объявления отдельной struct-pair'ы под каждое маппинг-методом.
type Fn[F any, T any] func(F) (T, error)

// Transfer — реализация Interface для Fn.
func (f Fn[F, T]) Transfer(src F) (T, error) { return f(src) }

// Fn2Face оборачивает функцию в Interface — синтаксический helper для init():
//
//	dto.RegTransfer(dto.Fn2Face(networkLoadBalancer{}.toPb))
func Fn2Face[F any, T any](fn func(F) (T, error)) Interface[F, T] { return Fn[F, T](fn) }

// ---- Registry ----------------------------------------------------------------

// tag — type-level marker для индексирования реестра по паре (F, T) через
// reflect.TypeFor. Сам value никогда не существует, нужен только для типа.
type tag[_ any, _ any] struct{}

var (
	regMu        sync.RWMutex
	transfersReg = map[reflect.Type]any{}
)

// RegTransfer регистрирует трансфер F → T под ключом reflect.TypeFor[tag[F,T]].
// Дубликат регистрации (та же пара (F,T)) — panic в init().
func RegTransfer[F any, T any](impl Interface[F, T]) {
	key := reflect.TypeFor[tag[F, T]]()
	regMu.Lock()
	defer regMu.Unlock()
	if _, ok := transfersReg[key]; ok {
		panic(fmt.Sprintf("dto: duplicate transfer registration for %s", key.String()))
	}
	transfersReg[key] = impl
}

// FindTransfer — публичный lookup. Используется в тестах и handler'ах, которые
// сами хотят вызвать Transfer без DTO[F,T]-pair'ы (например inline-вызов
// timestamp-transfer'а из чужого toPb-метода).
func FindTransfer[F any, T any]() (Interface[F, T], bool) {
	return findTransfer[F, T]()
}

func findTransfer[F any, T any]() (Interface[F, T], bool) {
	key := reflect.TypeFor[tag[F, T]]()
	regMu.RLock()
	defer regMu.RUnlock()
	v, ok := transfersReg[key]
	if !ok {
		return nil, false
	}
	impl, ok := v.(Interface[F, T])
	if !ok {
		return nil, false
	}
	return impl, true
}

// ---- DTO entry-point (Transfer + FromTo) -------------------------------------

// DTO — pair-объект, который собирает FromTo(): хранит src + указатель на dst
// и реализует Perform() через registry-lookup. Поле dst — pointer-to-T, чтобы
// caller получил результат через свой собственный nil-pointer.
type DTO[F any, T any] struct {
	src F
	dst *T
}

// Perform выполняет лукап Interface[F,T] и пишет результат в *dto.dst.
// Ошибки: «no transfer registered» если пары нет; пробрасывает ошибку реализации.
func (d *DTO[F, T]) Perform() error {
	impl, ok := findTransfer[F, T]()
	if !ok {
		var f F
		var t T
		return fmt.Errorf("dto: no transfer registered for %T → %T", f, t)
	}
	res, err := impl.Transfer(d.src)
	if err != nil {
		return err
	}
	*d.dst = res
	return nil
}

// FromTo — конструктор DTO. Применяется в caller-site:
//
//	dto.Transfer(dto.FromTo(rec, &dst))
//
// Возвращает *DTO[F,T] — пара указатель для Transfer, чьё имплицитное поведение
// видно компилятором через type-set constraint (см. Transfer).
func FromTo[F any, T any](src F, dst *T) *DTO[F, T] {
	return &DTO[F, T]{src: src, dst: dst}
}

// Transferrable — закрытый sum-type generic constraint для Transfer():
// принимает только те *DTO[F,T] пары, которые ЯВНО зарегистрированы в
// type-set ниже. Compile-time гарантия: попытка вызвать
// `dto.Transfer(dto.FromTo(someUnregisteredSrc, &dst))` с парой (F,T), не
// перечисленной в union — провалится в compile-time.
//
// Skill evgeniy §3 C.5: type-set generic-constraint над union допустимых пар.
// При добавлении нового ресурса в DTO-реестр требуется одновременно
// (а) новой пары в union ниже, (б) нового init() с RegTransfer в
// `internal/dto/type2pb/`. Без обоих — код не компилируется.
type Transferrable interface {
	Perform() error

	*DTO[time.Time, *timestamppb.Timestamp] |
		*DTO[kachorepo.LoadBalancerRecord, *lbv1.NetworkLoadBalancer] |
		*DTO[kachorepo.ListenerRecord, *lbv1.Listener] |
		*DTO[kachorepo.TargetGroupRecord, *lbv1.TargetGroup] |
		*DTO[kachorepo.TargetRecord, *lbv1.Target] |
		*DTO[*operationv1.Operation, *operationv1.Operation]
}

// Transfer запускает Perform() на dto. Единственная публичная entry-point.
func Transfer[V Transferrable](dto V) error {
	return dto.Perform()
}
