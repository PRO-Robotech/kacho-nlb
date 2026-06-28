// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// runner.go — высокоуровневая обёртка над Dialect. Один экземпляр на жизнь
// процесса; concurrent использование не предполагается (cobra гоняет одну
// команду за раз).
package migrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

// Config — параметры одного запуска runner'а. Заполняется cmd/migrator/main.go
// из cobra-флагов / ENV / kacho-nlb config (via internal/apps/kacho/config).
type Config struct {
	Dialect       Dialect
	DSN           string
	FS            fs.FS  // embed.FS с миграциями (internal/migrations.FS)
	MigrationsDir string // путь внутри FS; для корня embed — "."
}

// Validate проверяет минимально необходимые поля до обращения к диалекту.
func (c Config) Validate() error {
	if c.Dialect == nil {
		return errors.New("dialect is not set")
	}
	if c.Dialect.Spec().Name == "" {
		return errors.New("dialect spec.Name is empty")
	}
	if c.DSN == "" {
		return errors.New("dsn is empty (set --dsn / KACHO_NLB_REPOSITORY__POSTGRES__URL / config repository.postgres.url)")
	}
	if c.FS == nil {
		return errors.New("migrations FS is nil")
	}
	if c.MigrationsDir == "" {
		return errors.New("migrations dir is empty")
	}
	return nil
}

// Runner — собранная конфигурация миграции. Создаётся через [New].
type Runner struct {
	cfg Config
}

// New собирает Runner; cfg валидируется здесь же.
func New(cfg Config) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg}, nil
}

func (r *Runner) Up(target string) error {
	return r.cfg.Dialect.Up(context.Background(), r.cfg.DSN, r.cfg.FS, r.cfg.MigrationsDir, target)
}

func (r *Runner) Down(target string) error {
	return r.cfg.Dialect.Down(context.Background(), r.cfg.DSN, r.cfg.FS, r.cfg.MigrationsDir, target)
}

func (r *Runner) Status(out io.Writer) error {
	return r.cfg.Dialect.Status(context.Background(), r.cfg.DSN, r.cfg.FS, r.cfg.MigrationsDir, out)
}

func (r *Runner) Create(physDir, name string) error {
	return r.cfg.Dialect.Create(physDir, name)
}

// parseTargetVersion — goose использует int64 для версии (timestamp или
// 4-digit prefix файла). Принимаем строку с CLI, чтобы пользователь мог
// написать "0001" как в имени файла; конвертация — fmt.Sscanf (устойчив
// к leading zeros).
func parseTargetVersion(s string) (int64, error) {
	var v int64
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0, fmt.Errorf("parse target version %q: %w", s, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("target version must be non-negative, got %d", v)
	}
	return v, nil
}
