// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Command migrator — отдельный binary мигратора схемы kacho-nlb.
//
// + /: отдельный CLI use-case = отдельный binary;
// миграции — НЕ subcommand основного сервиса. Диалект резолвится через
// [migrator.Dialect] interface (postgres — единственный поддерживаемый; интерфейс
// оставляет место под будущие диалекты без if-ветвей в Runner'е).
//
// Subcommands:
//
//	kacho-nlb-migrator up      [--target <version>]
//	kacho-nlb-migrator down    [--target <version>]
//	kacho-nlb-migrator status
//	kacho-nlb-migrator create  <name> [--dir <path>]
//
// Global flags:
//
//	--dialect oneof<postgres>                 (default postgres)
//	--dsn     <connection-string>             (или ENV / --config fallback)
//	--config  /etc/kacho-nlb/config.yaml      (если --dsn пуст — читает
//	                                          repository.postgres.url из YAML)
//
// Источник DSN — приоритет: --dsn > ENV `KACHO_NLB_REPOSITORY__POSTGRES__URL`
// > --config (config.Load). Так одна и та же `values.yaml` покрывает оба
// бинаря (kacho-loadbalancer + migrator) без дублирования.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // регистрирует "pgx" driver для sql.Open
	"github.com/spf13/cobra"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/migrator"
	"github.com/PRO-Robotech/kacho-nlb/internal/migrations"
)

const (
	defaultDialect       = "postgres"
	defaultMigrationsDir = "."
	// defaultPhysDir — куда `create` пишет новые миграции по умолчанию.
	// На внешнем диске (relative cwd); embed FS — read-only.
	defaultPhysDir = "internal/migrations"
)

// rootOptions — shared параметры всех subcommand'ов (persistent flags).
type rootOptions struct {
	dialect    string
	dsn        string
	configPath string
}

func main() {
	if err := newRootCmd(migrations.FS).Execute(); err != nil {
		os.Exit(1)
	}
}

// newRootCmd собирает дерево команд. Вынесено в отдельный конструктор,
// чтобы main_test.go мог инстанцировать без os.Exit. migrationsFS принимается
// параметром: в production — `internal/migrations.FS`, в тестах — пустая
// `fstest.MapFS{}` (проверяем только парсинг args).
func newRootCmd(migrationsFS fs.FS) *cobra.Command {
	opts := &rootOptions{}

	root := &cobra.Command{
		Use:          "kacho-nlb-migrator",
		Short:        "Database migrations runner for kacho-nlb (KAC-160)",
		Long:         "kacho-nlb-migrator — отдельный CLI для управления миграциями БД сервиса kacho-nlb.\nПостроено по pattern'у kacho-vpc/cmd/migrator (skill evgeniy §9 K.1–K.3).",
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&opts.dialect, "dialect", defaultDialect,
		"SQL dialect (postgres)")
	root.PersistentFlags().StringVar(&opts.dsn, "dsn", "",
		"database DSN; if empty — read ENV KACHO_NLB_REPOSITORY__POSTGRES__URL, then config.yaml")
	root.PersistentFlags().StringVar(&opts.configPath, "config", "",
		"path to kacho-nlb config.yaml (fallback DSN source)")

	root.AddCommand(
		newUpCmd(opts, migrationsFS),
		newDownCmd(opts, migrationsFS),
		newStatusCmd(opts, migrationsFS),
		newCreateCmd(opts, migrationsFS),
	)
	return root
}

func newUpCmd(opts *rootOptions, migrationsFS fs.FS) *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Apply migrations up to latest (or --target version)",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := buildRunner(opts, migrationsFS)
			if err != nil {
				return err
			}
			return r.Up(target)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "stop at this version (inclusive); default — latest")
	return cmd
}

func newDownCmd(opts *rootOptions, migrationsFS fs.FS) *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Rollback the most recent migration (or down to --target)",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := buildRunner(opts, migrationsFS)
			if err != nil {
				return err
			}
			return r.Down(target)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "rollback down to this version (inclusive); default — one step back")
	return cmd
}

func newStatusCmd(opts *rootOptions, migrationsFS fs.FS) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show migration status (applied / pending)",
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := buildRunner(opts, migrationsFS)
			if err != nil {
				return err
			}
			return r.Status(cmd.OutOrStdout())
		},
	}
}

func newCreateCmd(opts *rootOptions, migrationsFS fs.FS) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new empty SQL migration file (on disk, not in embed FS)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := buildRunner(opts, migrationsFS)
			if err != nil {
				return err
			}
			return r.Create(dir, args[0])
		},
	}
	cmd.Flags().StringVar(&dir, "dir", defaultPhysDir,
		"physical directory to place the new .sql file (cannot be embed FS)")
	return cmd
}

// buildRunner собирает migrator.Runner из persistent-флагов + ENV + config-fallback.
//
// DSN приоритет: --dsn > ENV KACHO_NLB_REPOSITORY__POSTGRES__URL > --config
// (config.Load → cfg.Repository.Postgres.URL). config.Load умеет тот же ENV-key,
// так что ENV-fallback гарантированно сработает даже если --config не задан.
func buildRunner(opts *rootOptions, migrationsFS fs.FS) (*migrator.Runner, error) {
	dialect, err := migrator.ResolveDialect(opts.dialect)
	if err != nil {
		return nil, err
	}

	dsn := strings.TrimSpace(opts.dsn)
	if dsn == "" {
		// config.Load сам ловит ENV KACHO_NLB_REPOSITORY__POSTGRES__URL,
		// поэтому отдельно os.Getenv не зовём — config — единый source.
		cfg, cerr := config.Load(opts.configPath)
		if cerr != nil {
			return nil, fmt.Errorf("dsn unset (--dsn) and config load failed: %w", cerr)
		}
		dsn = strings.TrimSpace(cfg.Repository.Postgres.URL)
		if dsn == "" {
			return nil, fmt.Errorf("dsn unset: --dsn / KACHO_NLB_REPOSITORY__POSTGRES__URL / repository.postgres.url all empty")
		}
	}

	return migrator.New(migrator.Config{
		Dialect:       dialect,
		DSN:           dsn,
		FS:            migrationsFS,
		MigrationsDir: defaultMigrationsDir,
	})
}
