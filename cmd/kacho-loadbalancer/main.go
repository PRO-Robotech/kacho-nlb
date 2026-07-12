// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Command kacho-loadbalancer — API-сервер kacho-nlb (gRPC public :9090 +
// internal :9091). Composition root (workspace CLAUDE.md «Чистая архитектура»):
// единственное место, где собираются adapter'ы (pgxpool, gRPC clients, FGA
// check-client) и пробрасываются в handler-слой.
//
// Поддерживает один subcommand `serve`. Миграции — в отдельном binary
// `cmd/migrator` (один CLI use-case = один binary).
//
// Параллельные серверы (public + internal + shutdown waiter) — через
// `H-BF/corlib/pkg/parallel.ExecAbstract`: первая
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

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	coredb "github.com/PRO-Robotech/kacho-corelib/db"
	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/observability"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"
	"github.com/PRO-Robotech/kacho-corelib/outbox/metrics"

	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
	"github.com/PRO-Robotech/kacho-nlb/internal/authzfilter"
	"github.com/PRO-Robotech/kacho-nlb/internal/check"
	"github.com/PRO-Robotech/kacho-nlb/internal/clients"
	computeclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/compute"
	geoclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/geo"
	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	vpcclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/vpc"
	"github.com/PRO-Robotech/kacho-nlb/internal/observability/health"
	nlbmetrics "github.com/PRO-Robotech/kacho-nlb/internal/observability/metrics"
	// dto/type2pb init регистрирует все DTO трансферы (domain ↔ proto) в реестре.
	// Импортируется здесь (composition root), чтобы registry был полон до старта
	// gRPC server'ов; handler'ы вызывают dto.Transfer и предполагают, что
	// каждая зарегистрированная пара уже в map'е.
	_ "github.com/PRO-Robotech/kacho-nlb/internal/dto/type2pb"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// peerClients — composition root bundle типизированных адаптеров к peer-сервисам.
// Use-case'ы принимают эти port-интерфейсы через конструкторы (Clean
// Architecture: composition root — единственное место, где известны
// конкретные реализации).
type peerClients struct {
	// IAM
	Project  iamclient.ProjectClient
	Check    iamclient.CheckClient
	Register iamclient.RegisterResourceClient // FGA-proxy (register-drainer)
	// Geo (Region/Zone-валидация — ребро nlb→geo, kacho-geo)
	Region geoclient.RegionClient
	Zone   geoclient.ZoneClient
	// Compute (Instance-resolve — НЕ geography, ребро nlb→compute остаётся)
	Instance computeclient.InstanceClient
	// VPC
	Subnet           vpcclient.SubnetClient
	NetworkInterface vpcclient.NetworkInterfaceClient
	Address          vpcclient.AddressClient
	InternalAddress  vpcclient.InternalAddressClient
	// ListFilter — per-object filtered List (RBAC; iam
	// AuthorizeService.ListObjects). nil → use-case'ы делают unfiltered passthrough.
	ListFilter authzfilter.Filter
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

	// CQRS-Repository. master = slave (single-pool dev). Use-case'ы,
	// зарегистрированные на handler-слое в следующих 'ах, получают этот
	// repo через port-интерфейсы (`internal/repo/kacho.Repository`).
	repo := kachopg.New(pool, nil)
	defer repo.Close()

	// Operations LRO repo (общая таблица operations в kacho_nlb schema).
	// Используется всеми use-case'ами мутирующих RPC через worker'ы
	// `operations.Run(ctx, opsRepo, opID, fn)` (kacho-corelib pattern) и
	// напрямую — OperationService.Get/Cancel (см. ниже).
	opsRepo := operations.NewRepo(pool, "kacho_nlb")

	// Peer-gRPC clients (corlib client-builder).
	// Возвращается bundle conn'ов + типизированных adapter'ов; conn'ы
	// закрываются по defer ниже. Use-case'ы получают clients
	// через port-интерфейсы (`internal/apps/kacho/api/<resource>/ports.go`).
	peerConns, peers, err := dialPeers(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("dial peers: %w", err)
	}
	defer closeAll(peerConns, logger)
	// peers — типизированные clients потребляются handler'ами (NLB / Listener
	// wired ниже; TG handler —). Композиционный root владеет gRPC-conn'ами
	// и закрывает их через defer выше — peers держит ссылки на stub'ы поверх этих
	// conn'ов, отдельного Close не требуется.

	// FGA Check interceptor. Per-RPC Check через
	// `iam.InternalIAMService.Check` + positive-cache (TTL 5s) +
	// pg_notify-driven invalidation (kacho_iam_subjects).
	//
	// В dev-mode (без peer.iam) caller передаёт Breakglass=true в config'е —
	// взамен Check'ов выдаёт WARN allow для аутентифицированных principal'ов.
	// Anonymous всё равно denied (CRIT-6/7 fix). Production-mode
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
		DenyRateLimitPerSec: 100, // I10 default; tunable в будущем config-поле.
	})
	if err != nil {
		return fmt.Errorf("build authz interceptor: %w", err)
	}

	// Fail-closed boot-gate: when KACHO_NLB_REQUIRE_IAM=true, mutating Create is
	// refused (UNAVAILABLE) and readiness is NotReady until the register-drainer is
	// IAM-connected. Starts NOT connected; SetConnected(true) fires once the drainer
	// is wired with a real iam peer (below).
	bootGate := bootgate.New(bootgate.Config{RequireIAM: cfg.FGA.RequireIAM, Service: "kacho-nlb"})

	// Prometheus observability adapter: приватный реестр, питает outbox-recorder,
	// LRO-worker/reconciler recorder и diagnostic /metrics. Заменяет in-memory
	// MemRecorder (метрики не экспортировались наружу) и NopRecorder LRO-worker'а.
	metricsAdapter := nlbmetrics.New(buildVersion, buildCommit)
	var outboxRec metrics.Recorder = metricsAdapter
	var lroRec operations.Recorder = metricsAdapter

	// LRO worker (default-registry) поднимается ДО приёма трафика: ConfigureDefault
	// подключает Prometheus-Recorder (live terminal-write/inflight метрики — раньше
	// NopRecorder), Start делает Ready=true без единой мутации (нет
	// readiness-deadlock «NotReady → нет Run → worker не стартует»).
	if err := startLROWorker(lroRec, logger); err != nil {
		return fmt.Errorf("start LRO worker: %w", err)
	}

	// Supervised background loop'ы (errgroup): LRO-reconciler, target-drain, free-ip,
	// authz-invalidator, fga-register-drainer + outbox-backstop, vip-origin. Собираются
	// здесь (drainer/backstop-ресурсы + bootGate.SetConnected как side-effect), но
	// запускаются errgroup'ом перед Serve. vipOriginGate нужен для readiness ниже.
	background, vipOriginGate, err := assembleBackgroundWorkers(ctx, backgroundDeps{
		pool:            pool,
		repo:            repo,
		lroRec:          lroRec,
		outboxRec:       outboxRec,
		bootGate:        bootGate,
		authzCache:      authzCache,
		peers:           peers,
		cfg:             cfg,
		logger:          logger,
		freeIPPoisonObs: metricsAdapter.IncFreeIPPoisoned,
	})
	if err != nil {
		return err
	}

	// gRPC servers (public :9090 + internal :9091).
	// OperationService зарегистрирован здесь как полный end-to-end путь.
	// Per-resource handler'ы (load_balancer / listener / target_group) подключаются
	// в + (..154,..158).
	//
	// authzIntr applied to BOTH public + internal listeners — внешние clients
	// (через api-gateway) и cluster-internal callers (admin-tooling / другие
	// kacho-services) идут через тот же permission_map. Internal RPC
	// (InternalResourceLifecycleService.Subscribe) явно замаплен в PermissionMap на
	// cluster-floor `system_viewer` — internal-листенер гоняет реальный Check, как
	// public (security.md «authN+authZ на ОБОИХ listener'ах»; «Internal = trusted» —
	// запрещённое допущение).
	//
	// principal — единственный subject per-RPC FGA Check. На ОБОИХ листенерах он
	// привязан к транспорту через trust-aware связку (anti-spoof):
	//   1. UnaryCertIdentityExtract — извлекает module-identity SAN из verified
	//      mTLS client-cert'а и помечает peer'а verified/unverified; insecure
	//      dev-listener (mTLS off) → no-op (back-compat).
	//   2. UnaryTrustedPrincipalExtract(WithTrustedForwarders(<gateway-SAN>)) —
	//      выставляет x-kacho-principal-* downstream (operations.PrincipalFromContext
	//      → subject Check'а + Operation.created_by) ТОЛЬКО когда peer mTLS-verified И
	//      (если allow-list задан) его SAN — доверенный форвардер (api-gateway). На
	//      недоверенном peer'е forwarded principal снимается → SystemPrincipal →
	//      Check fail-closed. MUST идти ПОСЛЕ CertIdentityExtract.
	// Прежняя grpcsrv.UnaryPrincipalExtract доверяла x-kacho-principal-* любого
	// peer'а безусловно (spoof: peer без cert'а форжил чужого principal'а).
	//
	// opt-in mTLS server-creds. enable=false (default) → insecure
	// (dev backward-compat). enable=true → RequireAndVerifyClientCert (server-cert +
	// client-CA). Applied to BOTH listeners (one server-cert).
	serverCreds, err := grpcsrv.TLSServerCreds(cfg.MTLS.Server)
	if err != nil {
		return fmt.Errorf("build server TLS creds: %w", err)
	}

	// boot-gate: fgaboot.GuardCreateUnary FIRST on the public chain —
	// a mutating tenant-resource Create is refused (UNAVAILABLE) when require-iam is
	// armed and the register-drainer is not IAM-connected, so no resource is created
	// without a deliverable owner-tuple intent. Read RPCs are untouched.
	publicUnary, publicStream, internalUnary, internalStream := buildInterceptorChains(
		bootGate, authzIntr, cfg.Authz.TrustedForwarderSANs)
	publicSrv := grpcsrv.NewServer(
		serverCreds,
		grpc.ChainUnaryInterceptor(publicUnary...),
		grpc.ChainStreamInterceptor(publicStream...),
	)
	internalSrv := grpcsrv.NewServer(
		serverCreds,
		grpc.ChainUnaryInterceptor(internalUnary...),
		grpc.ChainStreamInterceptor(internalStream...),
	)

	// Регистрация всех per-resource handler'ов на public/internal серверах
	// (Internal-vs-external инвариант: Internal.* — только на internalSrv). См.
	// registerGRPCServices (wiring.go) — per-service распределение/doc'и там же.
	registerGRPCServices(publicSrv, internalSrv, grpcWiring{
		repo:    repo,
		opsRepo: opsRepo,
		peers:   peers,
		pool:    pool,
		cfg:     cfg,
		logger:  logger,
	})

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

	// Dependency-aware readiness: /readyz отражает здоровье database / register-
	// drainer (= IAM-достижимость в nlb) / lro-worker / vip-origin-reconcile;
	// /healthz — только живость процесса (защита от restart-storm). Результат
	// зеркалится в dependency_up Prometheus-gauge.
	healthAgg := health.New(
		buildReadinessCheckers(pool, bootGate, vipOriginGate),
		health.WithResultObserver(metricsAdapter.SetDependencyUp),
	)
	// Diagnostic HTTP-listener (cluster-internal): /metrics + /healthz + /readyz.
	// metrics.enable=false ИЛИ пустой metrics.address → не поднимается (back-compat).
	diagAddr := ""
	if cfg.Metrics.Enable {
		diagAddr = cfg.Metrics.Address
	}
	diagTask, diagShutdown, err := startDiagnosticListener(diagAddr, metricsAdapter, healthAgg, logger)
	if err != nil {
		return fmt.Errorf("start diagnostic listener: %w", err)
	}

	// Единый shutdown-триггер (sync.Once): флипает readiness в shutting_down
	// (kubelet перестаёт слать трафик ДО GracefulStop), отменяет ctx (фоновые
	// loop'ы выходят), гасит оба gRPC-сервера с таймаутом. Вызывается из
	// shutdown-waiter (SIGTERM), из краша любого supervised-task'а и из
	// superviseBackground при неожиданном exit'е.
	var shutdownOnce sync.Once
	shutdownCh := make(chan struct{})
	triggerShutdown := func() {
		shutdownOnce.Do(func() {
			healthAgg.SetShuttingDown()
			close(shutdownCh)
			cancel()
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

	var g errgroup.Group
	// Фоновые loop'ы под супервизором: неожиданный exit (ctx ещё жив) флипает
	// readiness и триггерит shutdown; штатный возврат после ctx-cancel → nil.
	for _, bg := range background {
		g.Go(func() error {
			return superviseBackground(ctx, bg.name, bg.run, triggerShutdown, logger)
		})
	}
	// Diagnostic HTTP-listener (когда поднят).
	if diagTask != nil {
		g.Go(func() error {
			if derr := diagTask(); derr != nil {
				logger.Error("diagnostic listener stopped", "err", derr)
				triggerShutdown()
				return fmt.Errorf("diagnostic listener: %w", derr)
			}
			return nil
		})
	}
	// internal gRPC server.
	g.Go(func() error {
		if serr := internalSrv.Serve(internalListener); serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			logger.Error("internal grpc server stopped", "err", serr)
			triggerShutdown()
			return fmt.Errorf("internal grpc: %w", serr)
		}
		return nil
	})
	// public gRPC server.
	g.Go(func() error {
		if serr := publicSrv.Serve(publicListener); serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			triggerShutdown()
			return fmt.Errorf("public grpc: %w", serr)
		}
		return nil
	})
	// shutdown-waiter: SIGTERM/SIGINT (ctx) ИЛИ краш любого task'а (shutdownCh) →
	// triggerShutdown → дрейн LRO worker'ов → гашение diagnostic-listener'а
	// последним (probe-flip /readyz→503 успевает отработать до закрытия порта).
	g.Go(func() error {
		select {
		case <-ctx.Done():
		case <-shutdownCh:
		}
		logger.Info("shutdown signal received")
		triggerShutdown()
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer drainCancel()
		if werr := operations.Wait(drainCtx); werr != nil {
			logger.Warn("operations workers did not finish in time",
				"err", werr, "active", operations.Active())
		}
		diagShutdown(drainCtx)
		return nil
	})

	if err := g.Wait(); err != nil {
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
// legacy system-trust TLS-bool и per-edge mTLS-config (grpcclient.TLSClient).
// mtls имеет приоритет над tls в dialOne (см. dialPeers): при mtls.Enable=true dial
// предъявляет client-cert и верифицирует server по CA + server_name.
//
// Вынесен из dialPeers как чистая (без side-effect'ов) проекция wiring'а — это
// единственный testable seam, фиксирующий контракт «каждое cross-service ребро
// предъявляет СВОИ per-edge mTLS-creds» (nlb→vpc / nlb→compute зеркалят
// nlb→iam mtls.iam-register). Регрессия к zero-value (insecure) TLSClient на vpc/
// compute ребре ловится тестом peerDialSpecs (cmd/.../dialpeers_mtls_test.go).
type peerDialSpec struct {
	name string               // лог-имя ребра (iam-public / vpc-internal / compute / …)
	addr string               // host:port (уже резолвнутый firstNonEmpty)
	tls  bool                 // legacy system-trust TLS (перебивается mtls при Enable)
	mtls grpcclient.TLSClient // per-edge client-cert config (приоритет над tls)
}

// peerDialSpecs строит таблицу peer-conn'ов из config'а. Чистая функция:
// никаких dial'ов / I/O — только маппинг cfg → peerDialSpec. Порядок conn'ов:
//   - iam-public  (9090, ProjectService.Get)        ← cfg.MTLS.IAMProject
//   - iam-internal(9091, Check + Register)           ← cfg.MTLS.IAMRegister
//   - geo         (9090, RegionService.Get)          ← cfg.MTLS.Geo
//   - compute     (9090, InstanceService.Get)        ← cfg.MTLS.Compute
//   - vpc-public  (9090, Address/Subnet/NIC)         ← cfg.MTLS.VPC
//   - vpc-internal(9091, InternalAddressService)     ← cfg.MTLS.VPC
//
// Per-listener split для iam (iam-public≠iam-internal по ServerName) обязателен под
// RequireAndVerifyClientCert (latent-bug). vpc-public и vpc-internal
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
//   - clients.Conn — для defer'нутого Close в composition root.
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
//
// kacho-geo). Geography выделена из compute в leaf-сервис geo.
//   - kacho-compute: один conn на public Addr — InstanceService.Get
//     (instance-resolve; НЕ geography).
//   - kacho-vpc: ДВА conn'а. public (Addr) — AddressService / OperationService;
//     internal (InternalAddr) — InternalAddressService.{Set,Clear}Reference,
//     SubnetService / NetworkInterfaceService живут на public, но edge consumer
//     (NLB) использует public Addr для них тоже.
//
// Internal-vs-external инвариант: Internal.* НЕ публикуется на external
// TLS endpoint.
func dialPeers(
	ctx context.Context, cfg *config.Config, logger *slog.Logger,
) ([]clients.Conn, *peerClients, error) {
	var conns []clients.Conn
	// dialOne opens one peer conn. mtls (per-edge grpcclient.TLSClient)
	// takes precedence over the legacy `useTLS` system-trust bool: when
	// mtls.Enable=true the dial presents a client-cert and verifies the server
	// against the configured CA + server_name. mtls.Enable=false → insecure /
	// legacy TLS (dev backward-compat). A mTLS cred-build error is
	// fail-closed (no silent insecure downgrade).
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
	// Топология (см. peerDialSpecs doc):
	//   - kacho-iam: два conn'а ПЕР-LISTENER. PUBLIC (9090) — ProjectService.Get;
	//     INTERNAL (9091) — InternalIAMService.{Check,RegisterResource,Unregister}.
	//     Раздельные mTLS-поля (IAMProject vs IAMRegister) обязательны: единый
	//     ServerName не корректен для обоих listener'ов под
	//     RequireAndVerifyClientCert (latent-bug). До split'а
	//     оба шли на INTERNAL → ProjectService Unimplemented ("project lookup failed").
	//   - kacho-geo: один conn (9090) — RegionService.Get (region-валидация).
	//   - kacho-compute: один conn (9090) — InstanceService.Get (instance-resolve).
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
		// Same per-call timeout source as the authz interceptor
		// (check.Options.CheckTimeout below) — handler-side direct Check
		// calls (attach_target_group.go, move.go) run OUTSIDE the
		// interceptor's own bounded ctx, so the client must bound itself.
		peers.Check = iamclient.NewCheckClientWithTimeout(iamInternalConn, cfg.Authz.IAM.RequestTimeout)
		// FGA-proxy: register-drainer applies owner-tuple intents through
		// InternalIAMService.RegisterResource / UnregisterResource (Internal-only
		// :9091). Replaces the former direct WriteCreatorTuple (Issue N5).
		peers.Register = iamclient.NewRegisterResourceClient(iamInternalConn)
	}
	// report the per-listener mTLS state of the iam read/authz edges
	// (mirror of the register-drainer fga_register_drainer_started "mtls" log).
	// iam-project (9090, ProjectService.Get) and iam-internal (9091, Check) are
	// the read/authz edges; each enables independently with its own ServerName.
	logger.Info("iam_read_authz_mtls",
		"project_mtls", cfg.MTLS.IAMProject.Enable,
		"project_server_name", cfg.MTLS.IAMProject.ServerName,
		"authz_mtls", cfg.MTLS.IAMRegister.Enable,
		"authz_server_name", cfg.MTLS.IAMRegister.ServerName)

	// RBAC (issue): per-object filtered List. Каждый публичный
	// List<Resource> прогоняет id-set через iam.AuthorizeService.ListObjects(subject,
	// action, "lb_*") и отдаёт пересечение (только доступные объекты), read==enforce
	// (relation viewer — та же, что per-RPC Check на Get), fail-closed. nil →
	// use-case'ы получают unfiltered passthrough (disabled / нет iam conn).
	// AuthorizeService.ListObjects теперь зарегистрирован и на iam INTERNAL listener
	// (9091) — service→service per-object list-filter ходит по тому же mTLS-edge, что
	// InternalIAMService.Check (reuse iamInternalConn; mTLS — mtls.iam-register). :9091
	// энфорсит CallerPolicy (verified module-cert), аноним fail-closed — authN+authZ на
	// каждом вызове (НЕ public :9090, где сервис→сервис без JWT отклонился бы).
	peers.ListFilter = buildListFilter(cfg, iamInternalConn, logger)

	// kacho-geo — один conn на public listener (RegionService.Get — публичный
	// read-only Geography-справочник). Ребро nlb→geo (kacho-geo) заменило
	// прежнюю region-валидацию через nlb→compute.
	if geoConn != nil {
		peers.Region = geoclient.NewRegionClient(geoConn)
		peers.Zone = geoclient.NewZoneClient(geoConn)
	}

	// kacho-compute — один conn на public listener (InstanceService.Get —
	// instance-resolve для TargetGroup-таргетов; НЕ geography).
	if computeConn != nil {
		peers.Instance = computeclient.NewInstanceClient(computeConn)
	}

	// kacho-vpc — public (Address/Subnet/NIC/Operation) + internal (InternalAddressService).
	if vpcPublicConn != nil {
		// Subnet-adapter несёт zone→region резолвер (geo) для заполнения
		// denormalised Subnet.RegionID у ZONAL-подсети — placement-coherence
		// region-precheck (ребро nlb→geo). geoConn nil → nil resolver → RegionID
		// ZONAL пуст (region-precheck пропускается, REGIONAL всё равно заполняется).
		var zoneRegion vpcclient.ZoneRegionResolver
		if geoConn != nil {
			zoneRegion = geoclient.NewZoneRegionClient(geoConn)
		}
		peers.Subnet = vpcclient.NewSubnetClientWithZoneRegion(vpcPublicConn, zoneRegion)
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

// buildListFilter собирает per-object List-filter (RBAC).
// Возвращает nil (→ use-case'ы делают unfiltered project-scoped
// passthrough), если list-filter выключен в конфиге ИЛИ iam conn недоступен
// (graceful start без iam). Иначе — FGAFilter поверх iam.AuthorizeService.ListObjects
// (conn — iamPublicConn, тот же, которым nlb зовёт ProjectService.Get; mTLS — через
// mtls.iam-project). read==enforce (relation viewer), fail-closed (FailOpen=false).
func buildListFilter(cfg *config.Config, iamConn clients.Conn, logger *slog.Logger) authzfilter.Filter {
	lf := cfg.Authz.ListFilter
	if !lf.Enabled || iamConn == nil {
		logger.Info("list_filter_disabled",
			"enabled", lf.Enabled, "iam_conn", iamConn != nil)
		return nil
	}
	fcfg := authzfilter.Config{
		Enabled:         true,
		Timeout:         lf.Timeout,
		CacheTTL:        lf.CacheTTL,
		CacheMaxEntries: lf.CacheMaxEntries,
		FailOpen:        lf.FailOpen,
	}
	logger.Info("list_filter_enabled",
		"timeout", lf.Timeout, "cache_ttl", lf.CacheTTL,
		"cache_max_entries", lf.CacheMaxEntries, "fail_open", lf.FailOpen,
		"iam_authz_mtls", cfg.MTLS.IAMProject.Enable)
	return authzfilter.NewFGAFilter(authzfilter.NewIAMAuthorizeClient(iamConn), fcfg)
}
