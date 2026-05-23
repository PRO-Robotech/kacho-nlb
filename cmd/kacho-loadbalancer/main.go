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

	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/operation"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients"
	// dto/type2pb init() регистрирует все DTO трансферы (domain ↔ proto) в реестре.
	// Импортируется здесь (composition root), чтобы registry был полон до старта
	// gRPC server'ов; handler'ы вызывают dto.Transfer(...) и предполагают, что
	// каждая зарегистрированная пара уже в map'е.
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
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

	// pgxpool. MaxConns / ConnLifetime tuning из cfg.Repository.Postgres
	// расширяется через coredb.NewPool по мере необходимости (см. kacho-corelib/db).
	pool, err := coredb.NewPool(ctx, cfg.Repository.Postgres.URL)
	if err != nil {
		return fmt.Errorf("open pgxpool: %w", err)
	}
	defer pool.Close()

	// CQRS-Repository (KAC-149). master = slave (single-pool dev). Use-case'ы,
	// зарегистрированные на handler-слое в следующих Wave'ах, получают этот
	// repo через port-интерфейсы (`internal/repo/kacho.Repository`).
	repo := kachopg.New(pool, nil)
	defer repo.Close()

	// Operations LRO repo (общая таблица operations в kacho_nlb schema).
	// Используется всеми use-case'ами мутирующих RPC через worker'ы
	// `operations.Run(ctx, opsRepo, opID, fn)` (kacho-corelib pattern) и
	// напрямую — OperationService.Get/Cancel (см. ниже).
	opsRepo := operations.NewRepo(pool, "kacho_nlb")

	// Peer-gRPC clients (corlib client-builder; evgeniy §K.6).
	peerConns, err := dialPeers(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("dial peers: %w", err)
	}
	defer closeAll(peerConns, logger)

	// gRPC servers (public :9090 + internal :9091).
	// OperationService зарегистрирован здесь как полный end-to-end путь (KAC-155).
	// Per-resource handler'ы (load_balancer / listener / target_group) подключаются
	// в Wave 6+ (KAC-151..154, KAC-156..158).
	publicSrv := grpcsrv.NewServer()
	internalSrv := grpcsrv.NewServer()
	// Sentinel-use — repo потребят handler'ы в Wave 6/7 (NLB / Listener / TG use-cases).
	// Композиционный root уже владеет и закрывает его через defer выше.
	_ = repo

	// OperationService (kacho.cloud.operation.OperationService): Get + Cancel.
	// Public per proto annotation `(kacho.iam.authz.v1.permission) = "<exempt>"`
	// — оба RPC доступны всем авторизованным subject'ам без per-RPC FGA Check.
	// List-операций НЕТ на этом сервисе — per-resource history exposed через
	// `<Resource>Service.ListOperations` (см. internal/apps/kacho/api/operation/handler.go).
	operationpb.RegisterOperationServiceServer(publicSrv, operation.NewHandler(opsRepo))

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
// Соединения открываются только если соответствующий addr задан в config;
// иначе пропускается (graceful dev-startup без peer-сервисов). Use-case'ы,
// которым нужен peer-client, получают conn через port-интерфейс
// (`internal/apps/kacho/api/<resource>/ports.go`).
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

