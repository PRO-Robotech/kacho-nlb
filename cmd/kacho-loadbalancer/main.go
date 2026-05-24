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
	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"

	internallifecycle "github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/internal_lifecycle"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/listener"
	lbhandler "github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/loadbalancer"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/operation"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/targetgroup"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/jobs"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients"
	computeclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	// dto/type2pb init() регистрирует все DTO трансферы (domain ↔ proto) в реестре.
	// Импортируется здесь (composition root), чтобы registry был полон до старта
	// gRPC server'ов; handler'ы вызывают dto.Transfer(...) и предполагают, что
	// каждая зарегистрированная пара уже в map'е.
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// peerClients — composition root bundle типизированных адаптеров к peer-сервисам.
// Use-case'ы Wave 6/7 принимают эти port-интерфейсы через конструкторы (Clean
// Architecture: composition root — единственное место, где известны
// конкретные реализации).
type peerClients struct {
	// IAM
	Project   iamclient.ProjectClient
	Check     iamclient.CheckClient
	Hierarchy iamclient.HierarchyWriter
	// Compute
	Region   computeclient.RegionClient
	Instance computeclient.InstanceClient
	// VPC
	Subnet           vpcclient.SubnetClient
	NetworkInterface vpcclient.NetworkInterfaceClient
	Address          vpcclient.AddressClient
	InternalAddress  vpcclient.InternalAddressClient
}

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
	// Возвращается bundle conn'ов + типизированных adapter'ов; conn'ы
	// закрываются по defer ниже. Use-case'ы Wave 6/7 получают clients
	// через port-интерфейсы (`internal/apps/kacho/api/<resource>/ports.go`).
	peerConns, peers, err := dialPeers(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("dial peers: %w", err)
	}
	defer closeAll(peerConns, logger)
	// peers — типизированные clients потребляются handler'ами (NLB / Listener
	// — wired ниже; TG handler — Wave 7). Композиционный root владеет gRPC-conn'ами
	// и закрывает их через defer выше — peers держит ссылки на stub'ы поверх этих
	// conn'ов, отдельного Close() не требуется.

	// gRPC servers (public :9090 + internal :9091).
	// OperationService зарегистрирован здесь как полный end-to-end путь (KAC-155).
	// Per-resource handler'ы (load_balancer / listener / target_group) подключаются
	// в Wave 6+ (KAC-151..154, KAC-156..158).
	publicSrv := grpcsrv.NewServer()
	internalSrv := grpcsrv.NewServer()

	// OperationService (kacho.cloud.operation.OperationService): Get + Cancel.
	// Public per proto annotation `(kacho.iam.authz.v1.permission) = "<exempt>"`
	// — оба RPC доступны всем авторизованным subject'ам без per-RPC FGA Check.
	// List-операций НЕТ на этом сервисе — per-resource history exposed через
	// `<Resource>Service.ListOperations` (см. internal/apps/kacho/api/operation/handler.go).
	operationpb.RegisterOperationServiceServer(publicSrv, operation.NewHandler(opsRepo))

	// NetworkLoadBalancerService (KAC-151). 12 публичных RPC: Get/List/Create/
	// Update/Delete/Start/Stop/Move/AttachTargetGroup/DetachTargetGroup/
	// GetTargetStates/ListOperations. Зарегистрирован ТОЛЬКО на publicSrv —
	// workspace CLAUDE.md «Запреты» #6 (Internal.* живут на internalSrv).
	lbHandler := lbhandler.NewHandler(
		repo, opsRepo,
		peers.Project, peers.Region, peers.Hierarchy,
		logger,
	)
	lbv1.RegisterNetworkLoadBalancerServiceServer(publicSrv, lbHandler)

	// ListenerService (KAC-152): Get/List/Create/Update/Delete/ListOperations.
	// Peer-clients (vpc Address / InternalAddress / Subnet, iam HierarchyWriter)
	// допускают nil — Create/Delete вернут Unavailable если peer не сконфигурирован.
	lbv1.RegisterListenerServiceServer(publicSrv, listener.NewHandler(
		repo,
		opsRepo,
		peers.Address,
		peers.InternalAddress,
		peers.Subnet,
		peers.Hierarchy,
		logger,
	))

	// TargetGroupService (KAC-153 + KAC-154): 9 публичных RPC: Get/List/Create/
	// Update/Delete/Move/AddTargets/RemoveTargets/ListOperations. Phase B drain
	// (DELETE expired DRAINING targets) выполняется отдельным background-runner'ом
	// (см. drainRunner ниже). Зарегистрирован ТОЛЬКО на publicSrv.
	tgHandler := targetgroup.NewHandler(
		repo, opsRepo,
		peers.Project, peers.Region,
		peers.Instance, peers.NetworkInterface, peers.Subnet,
		peers.Hierarchy,
		logger,
	)
	lbv1.RegisterTargetGroupServiceServer(publicSrv, tgHandler)

	// InternalResourceLifecycleService (KAC-157). Server-stream Subscribe(req)
	// для D-13 (kacho-iam consumer FGA tuple-sync). Зарегистрирован ТОЛЬКО на
	// internalSrv — workspace CLAUDE.md «Запреты» #6 (Internal.* НЕ маршрутизируется
	// через api-gateway на external TLS endpoint). Кросс-репо edge:
	// `iam → nlb.InternalResourceLifecycleService.Subscribe` (см. nlb CLAUDE.md §2).
	//
	// dsn — используется handler'ом для dedicated pgx.Conn (LISTEN/NOTIFY вне
	// pool'а); MaxStreams ограничивает concurrent streams (защита pgxpool от
	// исчерпания одним buggy/looping kacho-iam pod'ом).
	lifecycleHandler := internallifecycle.NewHandler(
		cfg.Repository.Postgres.URL,
		cfg.InternalLifecycle.MaxStreams,
		logger,
	)
	lbv1.RegisterInternalResourceLifecycleServiceServer(internalSrv, lifecycleHandler)

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

// dialPeers открывает gRPC connections к vpc/compute/iam через corlib client-builder
// и собирает типизированные adapter'ы (Clean Architecture outbound adapters).
//
// Возвращает:
//   - []clients.Conn — для defer'нутого Close() в composition root.
//   - *peerClients   — bundle типизированных port-интерфейсов для use-case'ов.
//
// Соединения открываются только если соответствующий addr задан в config;
// иначе adapter в peers остаётся nil (graceful dev-startup без peer-сервисов;
// use-case'ы при отсутствующем adapter'е возвращают Unavailable).
//
// Внутренняя топология:
//   - kacho-iam: один conn на InternalAddr — ProjectService.Get живёт и
//     на public, и (через scope-filter) на internal; InternalIAMService.{Check,
//     WriteCreatorTuple} — только на internal. Используем internal listener.
//   - kacho-compute: один conn на public Addr — RegionService / ZoneService /
//     InstanceService — публичные read RPC.
//   - kacho-vpc: ДВА conn'а. public (Addr) — AddressService / OperationService;
//     internal (InternalAddr) — InternalAddressService.{Set,Clear}Reference,
//     SubnetService / NetworkInterfaceService живут на public, но edge consumer
//     (NLB) использует public Addr для них тоже.
//
// См. workspace CLAUDE.md «Запреты» #6: Internal.* НЕ публикуется на external
// TLS endpoint.
func dialPeers(
	ctx context.Context, cfg *config.Config, logger *slog.Logger,
) ([]clients.Conn, *peerClients, error) {
	var conns []clients.Conn
	dialOne := func(name, addr string, useTLS bool) (clients.Conn, error) {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			logger.Info("peer not configured — skip", "peer", name)
			return nil, nil
		}
		cc, err := clients.Build(ctx, clients.BuildOptions{
			Endpoint:    addr,
			TLS:         useTLS,
			DialTimeout: peerDialDuration(cfg),
		})
		if err != nil {
			return nil, fmt.Errorf("dial %s @ %q: %w", name, addr, err)
		}
		conns = append(conns, cc)
		logger.Info("peer connected", "peer", name, "addr", addr, "tls", useTLS)
		return cc, nil
	}

	peers := &peerClients{}

	// kacho-iam — один conn на internal listener.
	iamAddr := firstNonEmpty(cfg.ExtAPI.IAM.InternalAddr, cfg.ExtAPI.IAM.Addr)
	iamConn, err := dialOne("iam", iamAddr, cfg.ExtAPI.IAM.TLS)
	if err != nil {
		closeAll(conns, logger)
		return nil, nil, err
	}
	if iamConn != nil {
		peers.Project = iamclient.NewProjectClient(iamConn)
		peers.Check = iamclient.NewCheckClient(iamConn)
		peers.Hierarchy = iamclient.NewHierarchyWriter(iamConn)
	}

	// kacho-compute — один conn на public listener (Region/Zone/Instance — публичные).
	computeAddr := firstNonEmpty(cfg.ExtAPI.Compute.Addr, cfg.ExtAPI.Compute.InternalAddr)
	computeConn, err := dialOne("compute", computeAddr, cfg.ExtAPI.Compute.TLS)
	if err != nil {
		closeAll(conns, logger)
		return nil, nil, err
	}
	if computeConn != nil {
		peers.Region = computeclient.NewRegionClient(computeConn)
		peers.Instance = computeclient.NewInstanceClient(computeConn)
	}

	// kacho-vpc — два conn'а: public (Address/Subnet/NIC/Operation) +
	// internal (InternalAddressService).
	vpcPublicAddr := firstNonEmpty(cfg.ExtAPI.VPC.Addr, cfg.ExtAPI.VPC.InternalAddr)
	vpcPublicConn, err := dialOne("vpc-public", vpcPublicAddr, cfg.ExtAPI.VPC.TLS)
	if err != nil {
		closeAll(conns, logger)
		return nil, nil, err
	}
	vpcInternalAddr := firstNonEmpty(cfg.ExtAPI.VPC.InternalAddr, cfg.ExtAPI.VPC.Addr)
	vpcInternalConn, err := dialOne("vpc-internal", vpcInternalAddr, cfg.ExtAPI.VPC.TLS)
	if err != nil {
		closeAll(conns, logger)
		return nil, nil, err
	}
	if vpcPublicConn != nil {
		peers.Subnet = vpcclient.NewSubnetClient(vpcPublicConn)
		peers.NetworkInterface = vpcclient.NewNetworkInterfaceClient(vpcPublicConn)
		peers.Address = vpcclient.NewAddressClient(vpcPublicConn)
	}
	if vpcPublicConn != nil && vpcInternalConn != nil {
		peers.InternalAddress = vpcclient.NewInternalAddressClient(vpcPublicConn, vpcInternalConn)
	}

	return conns, peers, nil
}

// firstNonEmpty — first non-empty string из аргументов; "" если все пусты.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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

