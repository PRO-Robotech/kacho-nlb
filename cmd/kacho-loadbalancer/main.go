// Command kacho-loadbalancer — API-сервер kacho-nlb (gRPC public :9090 +
// internal :9091). Composition root (workspace CLAUDE.md «Чистая архитектура»):
// единственное место, где собираются adapter'ы (pgxpool, gRPC clients, FGA
// check-client) и пробрасываются в handler-слой.
//
// Поддерживает один subcommand `serve`. Миграции — в отдельном binary
// `cmd/migrator` (evgeniy §9 K.1: один CLI use-case = один binary).
//
// Параллельные серверы (public + internal + shutdown waiter) — через
// `H-BF/corlib/pkg/parallel.ExecAbstract` (evgeniy §9 K.4 / K.5): первая
// ошибка / SIGTERM триггерит ctx cancel → GracefulStop обоих серверов.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/H-BF/corlib/pkg/parallel"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/PRO-Robotech/kacho-corelib/operations"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/jobs"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients"
)

func main() {
	root := &cobra.Command{
		Use:          "kacho-loadbalancer",
		Short:        "kacho-nlb API server (L4 NLB control-plane)",
		SilenceUsage: true,
	}
	root.AddCommand(newServeCmd())
	if err := root.Execute(); err != nil {
		// cobra сама печатает текст ошибки; нам остаётся exit-code.
		os.Exit(1)
	}
}

// newServeCmd запускает API-сервер kacho-nlb. Принимает --config path к YAML
// (опционально; defaults + ENV сами по себе достаточны для dev). Под капотом —
// composition root: config.Load → slog → pgxpool → ops repo → peer-clients
// stubs → public+internal gRPC servers → parallel.ExecAbstract.
func newServeCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "run gRPC public/internal servers",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "",
		"path to kacho-nlb config.yaml (optional; defaults + ENV used if empty)")
	return cmd
}

// runServe — собственно serve composition root.
func runServe(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := observability.NewSlogger(os.Stdout)
	slog.SetDefault(logger)
	logger.Info("kacho-loadbalancer starting",
		"mode", cfg.Mode().String(),
		"endpoint", cfg.APIServer.Endpoint,
		"internal_endpoint", cfg.APIServer.InternalEndpoint,
	)

	// Context: SIGTERM / SIGINT триггерит cancel → graceful stop через
	// shutdown-task в parallel.ExecAbstract.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// pgxpool. TODO(KAC-149): MaxConns / ConnLifetime из cfg.Repository.Postgres
	// — coredb.NewPool сейчас принимает только DSN; расширить когда понадобится.
	pool, err := coredb.NewPool(ctx, cfg.Repository.Postgres.URL)
	if err != nil {
		return fmt.Errorf("open pgxpool: %w", err)
	}
	defer pool.Close()

	// Operations LRO repo (общая таблица operations в kacho_nlb schema).
	opsRepo := operations.NewRepo(pool, "kacho_nlb")
	_ = opsRepo // TODO(KAC-150): передать в use-cases при их wiring'е.

	// Peer-gRPC clients (corlib client-builder; evgeniy §K.6).
	// TODO(KAC-151): после реализации vpc_client / compute_client / iam_client
	// — здесь создаются типизированные клиенты, реализующие port-интерфейсы
	// из internal/apps/kacho/api/<resource>/.
	peerConns, err := dialPeers(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("dial peers: %w", err)
	}
	defer closeAll(peerConns, logger)

	// gRPC servers (public :9090 + internal :9091). TODO(KAC-152/KAC-149):
	// зарегистрировать здесь handler'ы из internal/apps/kacho/api/*.
	publicSrv := grpcsrv.NewServer()
	internalSrv := grpcsrv.NewServer()

	publicListener, err := listenEndpoint(cfg.APIServer.Endpoint)
	if err != nil {
		return fmt.Errorf("listen public %q: %w", cfg.APIServer.Endpoint, err)
	}
	internalListener, err := listenEndpoint(cfg.APIServer.InternalEndpoint)
	if err != nil {
		_ = publicListener.Close()
		return fmt.Errorf("listen internal %q: %w", cfg.APIServer.InternalEndpoint, err)
	}
	logger.Info("kacho-loadbalancer listening",
		"public", publicListener.Addr().String(),
		"internal", internalListener.Addr().String(),
	)

	// Background jobs (KAC-159): Phase B 2-phase target drain-runner.
	// Запускается параллельно с gRPC-серверами как 4-й task. Cancel ctx →
	// штатное завершение Run() → task возвращает nil.
	drainRunner := jobs.NewTargetDrainRunner(pool, logger, cfg.Jobs.TargetDrain.Interval)

	// Параллельные tasks через corlib parallel.ExecAbstract (evgeniy §K.4):
	//   - public gRPC server
	//   - internal gRPC server
	//   - shutdown waiter (SIGTERM/SIGINT/ctx.Done → triggerShutdown)
	//
	// Failure isolation (§K.5): первая ошибка → triggerShutdown → ctx cancel
	// → GracefulStop обоих серверов. sync.Once защищает от двойного Stop
	// (SIGTERM пришёл одновременно с crash internal'а).
	var shutdownOnce sync.Once
	triggerShutdown := func() {
		shutdownOnce.Do(func() {
			gracefulCtx, gracefulCancel := context.WithTimeout(
				context.Background(), cfg.APIServer.GracefulShutdown)
			defer gracefulCancel()

			done := make(chan struct{})
			go func() {
				internalSrv.GracefulStop()
				publicSrv.GracefulStop()
				close(done)
			}()
			select {
			case <-done:
			case <-gracefulCtx.Done():
				logger.Warn("graceful stop timeout — forcing Stop",
					"timeout", cfg.APIServer.GracefulShutdown)
				internalSrv.Stop()
				publicSrv.Stop()
			}
		})
	}

	tasks := []func() error{
		// task 0 — public gRPC
		func() error {
			err := publicSrv.Serve(publicListener)
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				triggerShutdown()
				return fmt.Errorf("public grpc: %w", err)
			}
			return nil
		},
		// task 1 — internal gRPC
		func() error {
			err := internalSrv.Serve(internalListener)
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				triggerShutdown()
				return fmt.Errorf("internal grpc: %w", err)
			}
			return nil
		},
		// task 2 — shutdown waiter
		func() error {
			<-ctx.Done()
			logger.Info("shutdown signal received", "cause", ctx.Err())
			triggerShutdown()
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer drainCancel()
			if werr := operations.Wait(drainCtx); werr != nil {
				logger.Warn("operations workers did not finish in time",
					"err", werr, "active", operations.Active())
			}
			return nil
		},
		// task 3 — Phase B 2-phase target drain-runner (KAC-159).
		// Tick-loop по `cfg.Jobs.TargetDrain.Interval`; ctx cancel → штатный
		// exit; transient errors → log + continue (не abort task'а).
		func() error {
			return drainRunner.Run(ctx)
		},
	}
	// maxConcurrency = len(tasks)-1: основной goroutine исполняет task[0],
	// + дополнительные goroutine'ы для остальных. См. parallel.ExecAbstract doc.
	if err := parallel.ExecAbstract(len(tasks), int32(len(tasks)-1), func(i int) error {
		return tasks[i]()
	}); err != nil {
		return err
	}
	logger.Info("kacho-loadbalancer stopped cleanly")
	return nil
}

// listenEndpoint парсит `tcp://host:port` и открывает net.Listen.
func listenEndpoint(endpoint string) (net.Listener, error) {
	// config.Validate уже проверил scheme=tcp; здесь strip префикс.
	addr := strings.TrimPrefix(endpoint, "tcp://")
	return net.Listen("tcp", addr)
}

// dialPeers открывает gRPC connections к vpc/compute/iam через corlib client-builder.
//
// Текущая реализация — stub: соединения открываются только если соответствующий
// addr задан в config; иначе пропускается (graceful dev-startup без peer-сервисов).
// TODO(KAC-151): после реализации typed peer-clients (vpc_client.go etc.) здесь
// заворачиваются в port-интерфейсы и пробрасываются в use-cases.
//
// Внутреннее правило: NLB зовёт peer-сервисы по их **internal-addr** (:9091)
// напрямую через cluster-internal listener, **не** через api-gateway —
// см. design §1, edges section.
func dialPeers(ctx context.Context, cfg *config.Config, logger *slog.Logger) ([]clients.Conn, error) {
	var conns []clients.Conn
	dialOne := func(name, addr string, useTLS bool) error {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			logger.Info("peer not configured — skip", "peer", name)
			return nil
		}
		cc, err := clients.Build(ctx, clients.BuildOptions{
			Endpoint:    addr,
			TLS:         useTLS,
			DialTimeout: peerDialDuration(cfg),
		})
		if err != nil {
			return fmt.Errorf("dial %s @ %q: %w", name, addr, err)
		}
		conns = append(conns, cc)
		logger.Info("peer connected", "peer", name, "addr", addr, "tls", useTLS)
		return nil
	}

	// kacho-nlb зовёт *Internal*-сервисы peer'ов (см. design §1 edges) —
	// поэтому используется .InternalAddr. Если .InternalAddr пуст, fallback
	// на .Addr; в production это будет ошибкой validate.
	endpointOf := func(p config.ExtAPIEndpoint) string {
		if p.InternalAddr != "" {
			return p.InternalAddr
		}
		return p.Addr
	}
	if err := dialOne("vpc", endpointOf(cfg.ExtAPI.VPC), cfg.ExtAPI.VPC.TLS); err != nil {
		closeAll(conns, logger)
		return nil, err
	}
	if err := dialOne("compute", endpointOf(cfg.ExtAPI.Compute), cfg.ExtAPI.Compute.TLS); err != nil {
		closeAll(conns, logger)
		return nil, err
	}
	if err := dialOne("iam", endpointOf(cfg.ExtAPI.IAM), cfg.ExtAPI.IAM.TLS); err != nil {
		closeAll(conns, logger)
		return nil, err
	}
	return conns, nil
}

// peerDialDuration — берёт DialDuration из per-peer если задан, иначе
// общий DefDialDuration. Для NLB-stub'а пока используется единый default.
func peerDialDuration(cfg *config.Config) time.Duration {
	if cfg.ExtAPI.DefDialDuration > 0 {
		return cfg.ExtAPI.DefDialDuration
	}
	return 10 * time.Second
}

func closeAll(conns []clients.Conn, logger *slog.Logger) {
	for _, cc := range conns {
		if err := cc.Close(); err != nil {
			logger.Warn("close peer conn", "err", err)
		}
	}
}

