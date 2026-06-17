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
	"google.golang.org/grpc/credentials"

	"github.com/PRO-Robotech/kacho-corelib/authz"
	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	internallifecycle "github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/internal_lifecycle"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/listener"
	lbhandler "github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/loadbalancer"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/operation"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/targetgroup"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/jobs"
	"github.com/PRO-Robotech/kacho-nlb/internal/check"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients"
	computeclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	geoclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/geo"
	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
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
	Project  iamclient.ProjectClient
	Check    iamclient.CheckClient
	Register iamclient.RegisterResourceClient // SEC-D FGA-proxy (register-drainer)
	// Geo (Region-валидация — ребро nlb→geo, epic kacho-geo S4)
	Region geoclient.RegionClient
	// Compute (Instance-resolve — НЕ geography, ребро nlb→compute остаётся)
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

	// FGA Check interceptor (KAC-156). Per-RPC Check через
	// `iam.InternalIAMService.Check` + positive-cache (TTL 5s) +
	// pg_notify-driven invalidation (kacho_iam_subjects).
	//
	// В dev-mode (без peer.iam) caller передаёт Breakglass=true в config'е —
	// взамен Check'ов выдаёт WARN allow для аутентифицированных principal'ов.
	// Anonymous всё равно denied (KAC-122 CRIT-6/7 fix). Production-mode
	// config-validation rejects breakglass=true (см. config/validate.go).
	//
	// authzCache отдаётся отдельно (а не только через interceptor) чтобы
	// ListenInvalidator (task 4 ниже) делил с interceptor'ом один экземпляр —
	// pg_notify-driven invalidations применяются к тому же cache.
	authzIntr, authzCache, err := check.NewInterceptor(check.Options{
		ServiceName:         "kacho-nlb",
		IAMCheck:            peers.Check,
		Breakglass:          cfg.Authz.Breakglass,
		Logger:              logger,
		CheckTimeout:        cfg.Authz.IAM.RequestTimeout,
		CacheTTL:            cfg.Authz.Cache.TTL,
		CacheSize:           cfg.Authz.Cache.Size,
		DenyRateLimitPerSec: 100, // I10 default; tunable в будущем config-поле.
	})
	if err != nil {
		return fmt.Errorf("build authz interceptor: %w", err)
	}

	// gRPC servers (public :9090 + internal :9091).
	// OperationService зарегистрирован здесь как полный end-to-end путь (KAC-155).
	// Per-resource handler'ы (load_balancer / listener / target_group) подключаются
	// в Wave 6+ (KAC-151..154, KAC-156..158).
	//
	// authzIntr applied to BOTH public + internal listeners — внешние clients
	// (через api-gateway) и cluster-internal callers (admin-tooling / другие
	// kacho-services) идут через тот же permission_map. Internal RPC
	// (InternalResourceLifecycleService) автоматически распознаются
	// interceptor'ом по methodIsInternal heuristic и пропускаются
	// (DecisionInternal). См. authz/types.go::methodIsInternal.
	// KAC-178 §2 (W1.4 mirror of vpc/compute): grpcsrv.UnaryPrincipalExtract
	// ОБЯЗАН быть ПЕРВЫМ в public chain — без него operations.PrincipalFromContext
	// возвращает SystemPrincipal() = user:bootstrap для каждого request'а,
	// независимо от того, что api-gateway форвардит x-kacho-principal-* через
	// gRPC metadata. Operation handler пишет "anonymous"/empty principal в DB.
	// SEC-D S3: opt-in mTLS server-creds (SEC-B). enable=false (default) →
	// insecure (dev backward-compat). enable=true → RequireAndVerifyClientCert
	// (server-cert + client-CA). Applied to BOTH listeners (one server-cert).
	serverCreds, err := grpcsrv.TLSServerCreds(cfg.MTLS.Server)
	if err != nil {
		return fmt.Errorf("build server TLS creds: %w", err)
	}
	publicSrv := grpcsrv.NewServer(
		serverCreds,
		grpc.ChainUnaryInterceptor(grpcsrv.UnaryPrincipalExtract(), authzIntr.Unary()),
		grpc.ChainStreamInterceptor(grpcsrv.StreamPrincipalExtract(), authzIntr.Stream()),
	)
	internalSrv := grpcsrv.NewServer(
		serverCreds,
		grpc.ChainUnaryInterceptor(grpcsrv.UnaryPrincipalExtract(), authzIntr.Unary()),
		grpc.ChainStreamInterceptor(grpcsrv.StreamPrincipalExtract(), authzIntr.Stream()),
	)

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
		peers.Project, peers.Region,
		logger,
	)
	lbv1.RegisterNetworkLoadBalancerServiceServer(publicSrv, lbHandler)

	// ListenerService (KAC-152): Get/List/Create/Update/Delete/ListOperations.
	// Peer-clients (vpc Address / InternalAddress / Subnet)
	// допускают nil — Create/Delete вернут Unavailable если peer не сконфигурирован.
	lbv1.RegisterListenerServiceServer(publicSrv, listener.NewHandler(
		repo,
		opsRepo,
		peers.Address,
		peers.InternalAddress,
		peers.Subnet,
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
		// task 4 — FGA Check cache invalidator (KAC-156). Слушает
		// `kacho_iam_subjects` на iam-DB через DEDICATED pgx-conn (LISTEN/NOTIFY
		// требует non-pooled conn — godzila §16). На каждый NOTIFY → invalidate
		// cache по subject_id. На conn-drop → exponential backoff + conservative
		// InvalidateAll (защита от пропущенных NOTIFY в окне disconnect'а).
		//
		// Включается ТОЛЬКО если cfg.Authz.ListenInvalidator.Enable=true И
		// IAMDirectDSN задан (нам нужен прямой DSN к kacho_iam Postgres — это
		// cross-DB connection, поэтому отдельный config-field, не Repository.URL).
		// Иначе — no-op task (cache живёт TTL-only invalidation).
		func() error {
			if !cfg.Authz.ListenInvalidator.Enable || strings.TrimSpace(cfg.Authz.ListenInvalidator.IAMDirectDSN) == "" {
				logger.Info("authz_listen_invalidator_disabled",
					"enable", cfg.Authz.ListenInvalidator.Enable,
					"dsn_configured", cfg.Authz.ListenInvalidator.IAMDirectDSN != "")
				<-ctx.Done()
				return nil
			}
			channel := cfg.Authz.ListenInvalidator.Channel
			if channel == "" {
				channel = "kacho_iam_subjects"
			}
			inv := &authz.ListenInvalidator{
				ConnString: cfg.Authz.ListenInvalidator.IAMDirectDSN,
				Channel:    channel,
				Cache:      authzCache,
				Logger:     logger,
			}
			return inv.Run(ctx)
		},
		// task 5 — SEC-D FGA register-drainer (epic §3.1 Вариант A). corelib
		// outbox/drainer on `kacho_nlb.fga_register_outbox`; FOR UPDATE SKIP
		// LOCKED claim → exactly-once across replicas. Each row → kacho-iam
		// InternalIAMService.RegisterResource / UnregisterResource (mTLS when
		// cfg.MTLS.IAMRegister.Enable). IAM Unavailable → retry with backoff,
		// intent stays durable (SEC-D-11). enable=false → no-op task.
		// OQ-SEC-D-5: default-on (без drainer'а ресурсы не получат owner-tuple).
		func() error {
			if !cfg.FGA.RegisterDrainer.Enable {
				logger.Info("fga_register_drainer_disabled")
				<-ctx.Done()
				return nil
			}
			if peers.Register == nil {
				// No iam peer configured: intents accumulate durably; without an
				// applier the drainer would only poison. Log + idle (intent not
				// lost — applied once iam is wired and drainer restarts).
				logger.Warn("fga_register_drainer_idle_no_iam_peer")
				<-ctx.Done()
				return nil
			}
			d, derr := drainer.New[domain.FGARegisterIntent](
				pool,
				drainer.Config{
					Table:        "kacho_nlb.fga_register_outbox",
					Channel:      "kacho_nlb_fga_register_outbox",
					BatchSize:    cfg.FGA.RegisterDrainer.BatchSize,
					PollFallback: cfg.FGA.RegisterDrainer.PollFallback,
					MaxAttempts:  cfg.FGA.RegisterDrainer.MaxAttempts,
					BackoffMin:   cfg.FGA.RegisterDrainer.BackoffMin,
					BackoffMax:   cfg.FGA.RegisterDrainer.BackoffMax,
				},
				iamclient.DecodeFGARegisterIntent,
				iamclient.NewRegisterApplier(peers.Register),
				logger,
			)
			if derr != nil {
				return fmt.Errorf("build fga register-drainer: %w", derr)
			}
			logger.Info("fga_register_drainer_started",
				"mtls", cfg.MTLS.IAMRegister.Enable)
			return d.Run(ctx)
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

// peerDialSpec — декларативная единица wiring одного peer-conn'а: имя, dial-addr,
// legacy system-trust TLS-bool и per-edge SEC-B mTLS-config (grpcclient.TLSClient).
// mtls имеет приоритет над tls в dialOne (см. dialPeers): при mtls.Enable=true dial
// предъявляет client-cert и верифицирует server по CA + server_name.
//
// Вынесен из dialPeers как чистая (без side-effect'ов) проекция wiring'а — это
// единственный testable seam, фиксирующий контракт «каждое cross-service ребро
// предъявляет СВОИ per-edge mTLS-creds» (SEC-M: nlb→vpc / nlb→compute зеркалят
// nlb→iam mtls.iam-register). Регрессия к zero-value (insecure) TLSClient на vpc/
// compute ребре ловится тестом peerDialSpecs (cmd/.../dialpeers_mtls_test.go).
type peerDialSpec struct {
	name string               // лог-имя ребра (iam-public / vpc-internal / compute / …)
	addr string               // host:port (уже резолвнутый firstNonEmpty)
	tls  bool                 // legacy system-trust TLS (перебивается mtls при Enable)
	mtls grpcclient.TLSClient // per-edge SEC-B client-cert config (приоритет над tls)
}

// peerDialSpecs строит таблицу peer-conn'ов из config'а. Чистая функция:
// никаких dial'ов / I/O — только маппинг cfg → []peerDialSpec. Порядок conn'ов:
//   - iam-public  (:9090, ProjectService.Get)        ← cfg.MTLS.IAMProject
//   - iam-internal(:9091, Check + Register)           ← cfg.MTLS.IAMRegister
//   - geo         (:9090, RegionService.Get)          ← cfg.MTLS.Geo
//   - compute     (:9090, InstanceService.Get)        ← cfg.MTLS.Compute
//   - vpc-public  (:9090, Address/Subnet/NIC)         ← cfg.MTLS.VPC
//   - vpc-internal(:9091, InternalAddressService)     ← cfg.MTLS.VPC
//
// Per-listener split для iam (iam-public≠iam-internal по ServerName) обязателен под
// SEC-H RequireAndVerifyClientCert (I6 / latent-bug D-04). vpc-public и vpc-internal
// дилят один Service `vpc` (SAN serverHosts=[vpc] покрывает оба порта) → общий
// cfg.MTLS.VPC. Адрес каждого ребра — firstNonEmpty(public, internal) и наоборот,
// чтобы single-addr dev-config продолжал работать.
func peerDialSpecs(cfg *config.Config) []peerDialSpec {
	return []peerDialSpec{
		{
			name: "iam-public",
			addr: firstNonEmpty(cfg.ExtAPI.IAM.Addr, cfg.ExtAPI.IAM.InternalAddr),
			tls:  cfg.ExtAPI.IAM.TLS,
			mtls: cfg.MTLS.IAMProject,
		},
		{
			name: "iam-internal",
			addr: firstNonEmpty(cfg.ExtAPI.IAM.InternalAddr, cfg.ExtAPI.IAM.Addr),
			tls:  cfg.ExtAPI.IAM.TLS,
			mtls: cfg.MTLS.IAMRegister,
		},
		{
			name: "geo",
			addr: firstNonEmpty(cfg.ExtAPI.Geo.Addr, cfg.ExtAPI.Geo.InternalAddr),
			tls:  cfg.ExtAPI.Geo.TLS,
			mtls: cfg.MTLS.Geo,
		},
		{
			name: "compute",
			addr: firstNonEmpty(cfg.ExtAPI.Compute.Addr, cfg.ExtAPI.Compute.InternalAddr),
			tls:  cfg.ExtAPI.Compute.TLS,
			mtls: cfg.MTLS.Compute,
		},
		{
			name: "vpc-public",
			addr: firstNonEmpty(cfg.ExtAPI.VPC.Addr, cfg.ExtAPI.VPC.InternalAddr),
			tls:  cfg.ExtAPI.VPC.TLS,
			mtls: cfg.MTLS.VPC,
		},
		{
			name: "vpc-internal",
			addr: firstNonEmpty(cfg.ExtAPI.VPC.InternalAddr, cfg.ExtAPI.VPC.Addr),
			tls:  cfg.ExtAPI.VPC.TLS,
			mtls: cfg.MTLS.VPC,
		},
	}
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
//     RegisterResource, UnregisterResource} — только на internal. Используем
//     internal listener.
//   - kacho-geo: один conn на public Addr — RegionService.Get (region-валидация,
//     epic kacho-geo S4). Geography выделена из compute в leaf-сервис geo.
//   - kacho-compute: один conn на public Addr — InstanceService.Get
//     (instance-resolve; НЕ geography).
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
	// dialOne opens one peer conn. mtls (per-edge SEC-B grpcclient.TLSClient)
	// takes precedence over the legacy `useTLS` system-trust bool: when
	// mtls.Enable=true the dial presents a client-cert and verifies the server
	// against the configured CA + server_name. mtls.Enable=false → insecure /
	// legacy TLS (dev backward-compat, эпик §5). A mTLS cred-build error is
	// fail-closed (no silent insecure downgrade, SEC-D-20).
	dialOne := func(name, addr string, useTLS bool, mtls grpcclient.TLSClient) (clients.Conn, error) {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			logger.Info("peer not configured — skip", "peer", name)
			return nil, nil
		}
		var mtlsCreds credentials.TransportCredentials
		if mtls.Enable {
			c, cerr := grpcclient.TLSClientTransportCreds(mtls)
			if cerr != nil {
				return nil, fmt.Errorf("build mTLS creds for %s: %w", name, cerr)
			}
			mtlsCreds = c
		}
		cc, err := clients.Build(ctx, clients.BuildOptions{
			Endpoint:    addr,
			TLS:         useTLS,
			MTLSCreds:   mtlsCreds,
			DialTimeout: peerDialDuration(cfg),
		})
		if err != nil {
			return nil, fmt.Errorf("dial %s @ %q: %w", name, addr, err)
		}
		conns = append(conns, cc)
		logger.Info("peer connected", "peer", name, "addr", addr, "tls", useTLS, "mtls", mtls.Enable)
		return cc, nil
	}

	peers := &peerClients{}

	// Dial every peer-conn from the declarative spec table (peerDialSpecs). The
	// table is the single source of truth for per-edge mTLS wiring: each spec
	// carries its OWN grpcclient.TLSClient (iam-public→IAMProject, iam-internal→
	// IAMRegister, compute→Compute, vpc-public/internal→VPC). dialOne applies the
	// mtls precedence (Enable=true → client-cert + server verify; fail-closed on
	// cred-build error). conns are appended for defer'd Close in the composition
	// root. A dial error closes everything opened so far and propagates.
	//
	// Топология (см. peerDialSpecs doc + workspace CLAUDE.md «Запреты» #6):
	//   - kacho-iam: два conn'а ПЕР-LISTENER. PUBLIC (:9090) — ProjectService.Get;
	//     INTERNAL (:9091) — InternalIAMService.{Check,RegisterResource,Unregister}.
	//     Раздельные mTLS-поля (IAMProject vs IAMRegister) обязательны: единый
	//     ServerName не корректен для обоих listener'ов под SEC-H
	//     RequireAndVerifyClientCert (I6, latent-bug D-04). KAC-178 §2: до split'а
	//     оба шли на INTERNAL → ProjectService Unimplemented ("project lookup failed").
	//   - kacho-geo: один conn (:9090) — RegionService.Get (region-валидация).
	//   - kacho-compute: один conn (:9090) — InstanceService.Get (instance-resolve).
	//   - kacho-vpc: два conn'а — public (Address/Subnet/NIC/Operation) + internal
	//     (InternalAddressService). Оба предъявляют cfg.MTLS.VPC (vpc Service `vpc`,
	//     SAN serverHosts=[vpc] покрывает оба порта).
	dialedConns := make(map[string]clients.Conn, 6)
	for _, spec := range peerDialSpecs(cfg) {
		cc, derr := dialOne(spec.name, spec.addr, spec.tls, spec.mtls)
		if derr != nil {
			closeAll(conns, logger)
			return nil, nil, derr
		}
		dialedConns[spec.name] = cc
	}

	iamPublicConn := dialedConns["iam-public"]
	iamInternalConn := dialedConns["iam-internal"]
	geoConn := dialedConns["geo"]
	computeConn := dialedConns["compute"]
	vpcPublicConn := dialedConns["vpc-public"]
	vpcInternalConn := dialedConns["vpc-internal"]

	if iamPublicConn != nil {
		peers.Project = iamclient.NewProjectClient(iamPublicConn)
	}
	if iamInternalConn != nil {
		peers.Check = iamclient.NewCheckClient(iamInternalConn)
		// SEC-D FGA-proxy: register-drainer applies owner-tuple intents through
		// InternalIAMService.RegisterResource / UnregisterResource (Internal-only
		// :9091). Replaces the former direct WriteCreatorTuple (Issue N5).
		peers.Register = iamclient.NewRegisterResourceClient(iamInternalConn)
	}
	// SEC-I: report the per-listener mTLS state of the iam read/authz edges
	// (mirror of the register-drainer fga_register_drainer_started "mtls" log).
	// iam-project (:9090, ProjectService.Get) and iam-internal (:9091, Check) are
	// the read/authz edges; each enables independently with its own ServerName.
	logger.Info("iam_read_authz_mtls",
		"project_mtls", cfg.MTLS.IAMProject.Enable,
		"project_server_name", cfg.MTLS.IAMProject.ServerName,
		"authz_mtls", cfg.MTLS.IAMRegister.Enable,
		"authz_server_name", cfg.MTLS.IAMRegister.ServerName)

	// kacho-geo — один conn на public listener (RegionService.Get — публичный
	// read-only Geography-справочник). Ребро nlb→geo (epic kacho-geo S4) заменило
	// прежнюю region-валидацию через nlb→compute.
	if geoConn != nil {
		peers.Region = geoclient.NewRegionClient(geoConn)
	}

	// kacho-compute — один conn на public listener (InstanceService.Get —
	// instance-resolve для TargetGroup-таргетов; НЕ geography).
	if computeConn != nil {
		peers.Instance = computeclient.NewInstanceClient(computeConn)
	}

	// kacho-vpc — public (Address/Subnet/NIC/Operation) + internal (InternalAddressService).
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
