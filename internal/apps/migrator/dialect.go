// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package migrator — бизнес-логика отдельного бинаря cmd/migrator
// . До миграции kacho-nlb запускаются
// исключительно через этот отдельный binary; никакого `switch os.Args[1]` в
// kacho-loadbalancer.
//
// dialect.go определяет ключевую абстракцию пакета — интерфейс [Dialect].
// Единственная поддерживаемая БД — PostgreSQL (`postgres.go`); [ResolveDialect]
// выбирает реализацию по имени из CLI/конфига (неизвестное имя → ошибка). Это
// держит per-dialect tweaks за интерфейсом, без if-ветвей внутри общего Runner'а.
package migrator

import (
	"context"
	"fmt"
	"io"
	"io/fs"
)

// Dialect — абстракция SQL-диалекта для миграций.
//
// Реализация:
//   - [postgresDialect] (`postgres.go`) — production, через goose + pgx driver.
type Dialect interface {
	// Up применяет миграции вверх. target=="" → до самой последней; иначе
	// до версии target (включительно).
	Up(ctx context.Context, dsn string, fsys fs.FS, dir string, target string) error

	// Down откатывает миграцию(и). target=="" → одна последняя; иначе до
	// версии target (включительно).
	Down(ctx context.Context, dsn string, fsys fs.FS, dir string, target string) error

	// Status печатает применённые/неприменённые миграции (через goose-logger).
	Status(ctx context.Context, dsn string, fsys fs.FS, dir string, out io.Writer) error

	// Create создаёт пустой.sql-файл миграции на физическом диске (embed.FS
	// read-only). physDir — directory относительно cwd; name — суффикс.
	Create(physDir, name string) error

	// Spec — описательная метадата для CLI / help / тестов.
	Spec() DialectSpec
}

// DialectSpec — описательная метадата диалекта (CLI имя + goose-dialect +
// sql driver-имя). Runtime-behaviour живёт в реализации Dialect-interface'а.
type DialectSpec struct {
	Name         string // CLI имя: postgres, cockroach,...
	GooseDialect string // goose.SetDialect argument
	SQLDriver    string // sql.Open driver-имя ("pgx")
}

// SpecPostgres — spec единственного поддерживаемого диалекта.
var SpecPostgres = DialectSpec{
	Name:         "postgres",
	GooseDialect: "postgres",
	SQLDriver:    "pgx",
}

// ResolveDialect выбирает реализацию [Dialect] по имени из CLI/конфига.
// postgres — единственный поддерживаемый диалект; любое другое имя → ошибка
// (потребляется cmd/migrator: `--dialect <name>`).
func ResolveDialect(name string) (Dialect, error) {
	if name != SpecPostgres.Name {
		return nil, fmt.Errorf("unknown dialect %q (supported: %s)", name, SpecPostgres.Name)
	}
	return newPostgresDialect(), nil
}
