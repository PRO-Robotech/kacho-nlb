// Command migrator — отдельный binary мигратора схемы kacho-nlb.
//
// evgeniy §9.K.1+§9.K.3: отдельный CLI use-case = отдельный binary; миграции — НЕ
// subcommand основного сервиса. Поддерживает разные dialect'ы (--dialect=postgres
// сейчас единственный, но интерфейс расширяемый под cockroach / другие БД).
//
// Subcommands: up | down | status | create.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	flagDialect string
	flagDSN     string
)

func main() {
	root := &cobra.Command{
		Use:   "migrator",
		Short: "kacho-nlb schema migrator",
	}
	root.PersistentFlags().StringVar(&flagDialect, "dialect", "postgres",
		"DB dialect (postgres only for now)")
	root.PersistentFlags().StringVar(&flagDSN, "dsn", "",
		"connection string (or env KACHO_NLB_REPOSITORY__POSTGRES__URL)")

	root.AddCommand(newUpCmd(), newDownCmd(), newStatusCmd(), newCreateCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// TODO(KAC-148): реализовать migrator через goose + embed.FS (internal/migrations).
// Schema = `kacho_nlb`; baseline миграция `0001_initial.sql` создаётся в KAC-148.

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "apply all pending migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("migrator up: not yet implemented (KAC-148)")
		},
	}
}

func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "down",
		Short: "roll back one migration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("migrator down: not yet implemented (KAC-148)")
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "show applied/pending migration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("migrator status: not yet implemented (KAC-148)")
		},
	}
}

func newCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create [name]",
		Short: "scaffold a new migration file (timestamped)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("migrator create: not yet implemented (KAC-148)")
		},
	}
}
