// Package migrator — бизнес-логика отдельного бинаря cmd/migrator
// (skill evgeniy §9 K.1–K.3, AP-9). До KAC-148 миграции kacho-nlb запускаются
// исключительно через этот отдельный binary; никакого `switch os.Args[1]` в
// kacho-loadbalancer.
//
// dialect.go определяет ключевую абстракцию пакета — интерфейс [Dialect]
// (правило K.3: «migrator multi-dialect-ready»). Каждая поддерживаемая БД —
// отдельная реализация (`postgres.go`, в будущем `cockroach.go`); фабрика
// [NewDialect] выбирает реализацию по имени из CLI/конфига. Это позволяет
// per-dialect tweaks без if-ветвей внутри общего Runner'а.
//
// Источник pattern'а — `kacho-vpc/internal/apps/migrator/` (KAC-94/96).
package migrator

import (
	"context"
	"fmt"
	"io"
	"io/fs"
)

// Dialect — абстракция SQL-диалекта для миграций.
//
// Реализации (на момент KAC-160):
//   - [postgresDialect] (`postgres.go`) — production, через goose + pgx driver.
//
// Cockroach / другие диалекты добавляются как новые impl'ы; их регистрация
// — [RegisterDialect] либо init() в файле impl'а.
type Dialect interface {
	// Up применяет миграции вверх. target=="" → до самой последней; иначе
	// до версии target (включительно).
	Up(ctx context.Context, dsn string, fsys fs.FS, dir string, target string) error

	// Down откатывает миграцию(и). target=="" → одна последняя; иначе до
	// версии target (включительно).
	Down(ctx context.Context, dsn string, fsys fs.FS, dir string, target string) error

	// Status печатает применённые/неприменённые миграции (через goose-logger).
	Status(ctx context.Context, dsn string, fsys fs.FS, dir string, out io.Writer) error

	// Create создаёт пустой .sql-файл миграции на физическом диске (embed.FS
	// read-only). physDir — directory относительно cwd; name — суффикс.
	Create(physDir, name string) error

	// Spec — описательная метадата для CLI / help / тестов.
	Spec() DialectSpec
}

// DialectSpec — описательная метадата диалекта (CLI имя + goose-dialect +
// sql driver-имя). Runtime-behaviour живёт в реализации Dialect-interface'а.
type DialectSpec struct {
	Name         string // CLI имя: postgres, cockroach, ...
	GooseDialect string // goose.SetDialect argument
	SQLDriver    string // sql.Open driver-имя ("pgx")
}

// Built-in spec'и.
var (
	SpecPostgres = DialectSpec{
		Name:         "postgres",
		GooseDialect: "postgres",
		SQLDriver:    "pgx",
	}
)

type dialectFactory func() Dialect

var registry = map[string]dialectFactory{
	SpecPostgres.Name: func() Dialect { return newPostgresDialect() },
}

// NewDialect — фабрика; неизвестное имя → ошибка со списком поддерживаемых.
func NewDialect(name string) (Dialect, error) {
	factory, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q (supported: %v)", name, listDialects())
	}
	return factory(), nil
}

// ResolveDialect — alias для NewDialect (consistency с kacho-vpc).
func ResolveDialect(name string) (Dialect, error) { return NewDialect(name) }

// RegisterDialect — расширяет registry (для тестов / будущих диалектов).
func RegisterDialect(spec DialectSpec, factory func() Dialect) {
	if spec.Name == "" {
		panic("migrator.RegisterDialect: spec.Name is empty")
	}
	registry[spec.Name] = dialectFactory(factory)
}

func listDialects() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	return out
}
