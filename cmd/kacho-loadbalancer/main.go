// Command kacho-loadbalancer — API-сервер kacho-nlb (gRPC public :9090 + internal :9091
// + REST через grpc-gateway). Composition root (workspace CLAUDE.md "Чистая архитектура"):
// единственное место, где собираются adapter'ы (pgxpool, gRPC clients, FGA check-client)
// и пробрасываются в handler-слой.
//
// Поддерживает один subcommand `serve`. Миграции — в отдельном binary `cmd/migrator`
// (evgeniy §9.K.1: один CLI use-case = один binary, никакого `switch os.Args[1]`).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "kacho-loadbalancer",
		Short: "kacho-nlb API server (L4 NLB control-plane)",
	}
	root.AddCommand(newServeCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newServeCmd запускает API-сервер kacho-nlb.
//
// TODO(KAC-147): wiring composition root:
//   - load viper config (internal/apps/kacho/config).
//   - open pgxpool, run goose.Up против embedded internal/migrations.
//   - construct repository (internal/repo/kacho/pg).
//   - construct peer-clients (internal/clients): vpc, compute, iam.
//   - register handlers (internal/apps/kacho/api/*) — gRPC server.
//   - wire FGA Check interceptor (internal/check) + permission map.
//   - start parallel public/internal gRPC servers + shutdown waiter
//     через H-BF/corlib/pkg/parallel.ExecAbstract (evgeniy §9.K.4).
//   - start outbox drainer + fga-tuple-writer + target_drain_runner jobs
//     (internal/apps/kacho/jobs).
func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "run gRPC + REST API server",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO(KAC-147): implement serve composition root.
			return fmt.Errorf("kacho-loadbalancer serve: not yet implemented (KAC-147)")
		},
	}
}
