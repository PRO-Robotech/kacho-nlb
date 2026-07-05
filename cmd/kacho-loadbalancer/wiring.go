// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// wiring.go — cohesive composition-root sub-builders extracted from runServe.
// Each function is a faithful, side-effect-preserving slice of the original
// linear body: building the interceptor chains, registering the gRPC services,
// and assembling the supervised background workers. runServe stays the short
// orchestration sequence; resource-lifetime `defer`s and the errgroup/shutdown
// loop remain in runServe (moving them here would change their lifetime).
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"

	"google.golang.org/grpc"

	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"
	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"
	"github.com/PRO-Robotech/kacho-corelib/outbox/metrics"

	lbv1 "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/loadbalancer/v1"
	operationpb "github.com/PRO-Robotech/kacho-proto/gen/go/kacho/cloud/operation"

	announceapi "github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/announce"
	internallifecycle "github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/internal_lifecycle"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/listener"
	lbhandler "github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/loadbalancer"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/operation"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/api/targetgroup"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/config"
	"github.com/PRO-Robotech/kacho-nlb/internal/apps/kacho/jobs"
	iamclient "github.com/PRO-Robotech/kacho-nlb/internal/clients/iam"
	"github.com/PRO-Robotech/kacho-nlb/internal/domain"
	"github.com/PRO-Robotech/kacho-nlb/internal/fgaboot"
	kachopg "github.com/PRO-Robotech/kacho-nlb/internal/repo/kacho/pg"
)

// bgWorker — фоновый loop под супервизором (errgroup): неожиданный exit флипает
// readiness в shutting-down и триггерит graceful-shutdown (не fire-and-forget).
type bgWorker struct {
	name string
	run  func(context.Context) error
}

// buildInterceptorChains собирает unary/stream цепочки для public :9090 и
// internal :9091 listener'ов. Public: fgaboot boot-gate FIRST → cert-identity →
// trusted-principal → authz. Internal: тот же authN+authZ БЕЗ boot-gate (он
// охраняет только tenant-facing Create на public). anti-spoof:
// TrustedPrincipalExtract идёт ПОСЛЕ CertIdentityExtract (см. runServe doc).
func buildInterceptorChains(bootGate *bootgate.Gate, authzIntr *authz.Interceptor, forwarders []string) (
	publicUnary []grpc.UnaryServerInterceptor,
	publicStream []grpc.StreamServerInterceptor,
	internalUnary []grpc.UnaryServerInterceptor,
	internalStream []grpc.StreamServerInterceptor,
) {
	publicUnary = []grpc.UnaryServerInterceptor{
		fgaboot.GuardCreateUnary(bootGate),
		grpcsrv.UnaryCertIdentityExtract(),
		grpcsrv.UnaryTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarders...)),
		authzIntr.Unary(),
	}
	publicStream = []grpc.StreamServerInterceptor{
		grpcsrv.StreamCertIdentityExtract(),
		grpcsrv.StreamTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarders...)),
		authzIntr.Stream(),
	}
	internalUnary = []grpc.UnaryServerInterceptor{
		grpcsrv.UnaryCertIdentityExtract(),
		grpcsrv.UnaryTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarders...)),
		authzIntr.Unary(),
	}
	internalStream = []grpc.StreamServerInterceptor{
		grpcsrv.StreamCertIdentityExtract(),
		grpcsrv.StreamTrustedPrincipalExtract(grpcsrv.WithTrustedForwarders(forwarders...)),
		authzIntr.Stream(),
	}
	return publicUnary, publicStream, internalUnary, internalStream
}

// grpcWiring — зависимости регистрации gRPC-сервисов (composition root bundle).
type grpcWiring struct {
	repo    *kachopg.Repository
	opsRepo operations.Repo
	peers   *peerClients
	pool    *pgxpool.Pool
	cfg     *config.Config
	logger  *slog.Logger
}

// registerGRPCServices регистрирует все per-resource handler'ы на public :9090 /
// internal :9091 серверах (Internal-vs-external инвариант: Internal.* — только на
// internalSrv). Порядок и распределение по listener'ам идентичны прежнему inline-
// блоку runServe (см. per-service doc-комментарии).
func registerGRPCServices(publicSrv, internalSrv *grpc.Server, w grpcWiring) {
	// OperationService (public, exempt: op-id опакен, owner-scoped Get/Cancel).
	operationpb.RegisterOperationServiceServer(publicSrv, operation.NewHandler(w.opsRepo))

	// NetworkLoadBalancerService (public only).
	lbHandler := lbhandler.NewHandler(
		w.repo, w.opsRepo,
		w.peers.Project, w.peers.Check, w.peers.Region, w.peers.Zone,
		w.peers.Subnet, w.peers.Address, w.peers.InternalAddress,
		w.peers.ListFilter,
		w.logger,
	)
	lbv1.RegisterNetworkLoadBalancerServiceServer(publicSrv, lbHandler)

	// ListenerService (public only). InternalAddress нужен только для release
	// legacy-VIP в Delete (nil → Unavailable).
	lbv1.RegisterListenerServiceServer(publicSrv, listener.NewHandler(
		w.repo,
		w.opsRepo,
		w.peers.InternalAddress,
		w.peers.ListFilter,
		w.logger,
	))

	// TargetGroupService (public only). Фаза B drain — отдельный background-runner.
	tgHandler := targetgroup.NewHandler(
		w.repo, w.opsRepo,
		w.peers.Project, w.peers.Check, w.peers.Region,
		w.peers.Instance, w.peers.NetworkInterface, w.peers.Subnet,
		w.peers.ListFilter,
		w.logger,
	)
	lbv1.RegisterTargetGroupServiceServer(publicSrv, tgHandler)

	// InternalResourceLifecycleService (internal only) — FGA tuple-sync для kacho-iam.
	lifecycleHandler := internallifecycle.NewHandler(
		kachopg.NewLifecycleFeed(w.cfg.Repository.Postgres.URL),
		w.cfg.InternalLifecycle.MaxStreams,
		w.logger,
	)
	lbv1.RegisterInternalResourceLifecycleServiceServer(internalSrv, lifecycleHandler)

	// InternalLoadBalancerAnnounceService (internal only) — announce-state feedback.
	// Инфра-чувствительные данные (BGP/route/VRF/kernel/infra-id) не выходят на external.
	announceHandler := announceapi.NewHandler(kachopg.NewAnnounceStore(w.pool), w.logger)
	lbv1.RegisterInternalLoadBalancerAnnounceServiceServer(internalSrv, announceHandler)
}

// backgroundDeps — зависимости сборки supervised background-loop'ов.
type backgroundDeps struct {
	pool       *pgxpool.Pool
	repo       *kachopg.Repository
	lroRec     operations.Recorder
	outboxRec  metrics.Recorder
	bootGate   *bootgate.Gate
	authzCache *authz.Cache
	peers      *peerClients
	cfg        *config.Config
	logger     *slog.Logger
}

// assembleBackgroundWorkers строит полный набор фоновых loop'ов (LRO-reconciler,
// target-drain, free-ip, authz-listen-invalidator, fga-register-drainer + outbox-
// backstop, vip-origin-reconcile). Возвращает workers, vip-origin-gate (для
// readiness) и ошибку. Сами loop'ы НЕ запускаются здесь — их гоняет errgroup в
// runServe; функция лишь собирает slice + строит их ресурсы (drainer.New,
// backstop, bootGate.SetConnected). Порядок и side-effect'ы идентичны прежнему
// inline-блоку runServe.
func assembleBackgroundWorkers(ctx context.Context, d backgroundDeps) ([]bgWorker, *vipOriginReconcileGate, error) {
	var background []bgWorker

	// Durable LRO recovery: RecoverAll до трафика (в runServe), периодический Run —
	// backstop под супервизором.
	lroReconciler := startLRORecovery(ctx, d.pool, d.repo, d.lroRec, d.logger)
	background = append(background, bgWorker{"lro-reconciler", func(c context.Context) error {
		lroReconciler.Run(c)
		return nil
	}})

	// target drain-runner (фаза B): tick-loop по cfg.Jobs.TargetDrain.Interval.
	drainRunner := jobs.NewTargetDrainRunner(d.pool, d.logger, d.cfg.Jobs.TargetDrain.Interval)
	background = append(background, bgWorker{"target-drain", drainRunner.Run})

	// free_ip_runner: reconcile застрявших листенеров (multi-replica-safe). Требует
	// vpc internal-address client (release) — иначе не стартует (иначе утечка VIP).
	if d.peers.InternalAddress != nil {
		freeIPRunner := jobs.NewFreeIPRunner(d.pool, d.peers.InternalAddress, d.logger,
			d.cfg.Jobs.FreeIP.Interval, d.cfg.Jobs.FreeIP.AgeThreshold)
		background = append(background, bgWorker{"free-ip-runner", freeIPRunner.Run})
	} else {
		d.logger.Warn("free_ip_runner_disabled — no vpc internal-address client; stuck-listener VIP reconcile inactive")
	}

	// FGA Check cache invalidator: LISTEN kacho_iam_subjects на iam-DB через
	// dedicated pgx-conn. Включается ТОЛЬКО при enable=true И заданном IAMDirectDSN.
	if d.cfg.Authz.ListenInvalidator.Enable && strings.TrimSpace(d.cfg.Authz.ListenInvalidator.IAMDirectDSN) != "" {
		channel := d.cfg.Authz.ListenInvalidator.Channel
		if channel == "" {
			channel = "kacho_iam_subjects"
		}
		inv := &authz.ListenInvalidator{
			ConnString: d.cfg.Authz.ListenInvalidator.IAMDirectDSN,
			Channel:    channel,
			Cache:      d.authzCache,
			Logger:     d.logger,
		}
		background = append(background, bgWorker{"authz-listen-invalidator", inv.Run})
	} else {
		d.logger.Info("authz_listen_invalidator_disabled",
			"enable", d.cfg.Authz.ListenInvalidator.Enable,
			"dsn_configured", d.cfg.Authz.ListenInvalidator.IAMDirectDSN != "")
	}

	// FGA register-drainer: corelib outbox/drainer on kacho_nlb.fga_register_outbox
	// (FOR UPDATE SKIP LOCKED → exactly-once). Wired with a real iam peer → opens the
	// boot-gate + starts the reconciler/metrics backstop. Default-on.
	if d.cfg.FGA.RegisterDrainer.Enable && d.peers.Register != nil {
		dr, derr := drainer.New[domain.FGARegisterIntent](
			d.pool,
			drainer.Config{
				Table:        nlbFGAOutboxTable,
				Channel:      nlbFGAOutboxChannel,
				BatchSize:    d.cfg.FGA.RegisterDrainer.BatchSize,
				PollFallback: d.cfg.FGA.RegisterDrainer.PollFallback,
				MaxAttempts:  d.cfg.FGA.RegisterDrainer.MaxAttempts,
				BackoffMin:   d.cfg.FGA.RegisterDrainer.BackoffMin,
				BackoffMax:   d.cfg.FGA.RegisterDrainer.BackoffMax,
			},
			iamclient.DecodeFGARegisterIntent,
			iamclient.NewRegisterApplier(d.peers.Register),
			d.logger,
			// Each poisoned row bumps outbox_poisoned_total{table=…}.
			drainer.WithPoisonObserver[domain.FGARegisterIntent](func() {
				d.outboxRec.IncPoisoned(nlbFGAOutboxTable)
			}),
		)
		if derr != nil {
			return nil, nil, fmt.Errorf("build fga register-drainer: %w", derr)
		}
		background = append(background, bgWorker{"fga-register-drainer", dr.Run})
		// Drainer wired with a real iam peer → IAM-register delivery path is up:
		// open the boot-gate + start the reconciler/metrics backstop.
		d.bootGate.SetConnected(true)
		reconRun, colRun, berr := startBackstop(ctx, d.pool, d.outboxRec, d.logger)
		if berr != nil {
			return nil, nil, fmt.Errorf("start outbox backstop: %w", berr)
		}
		background = append(background,
			bgWorker{"fga-register-reconciler", reconRun},
			bgWorker{"outbox-metrics-collector", colRun},
		)
		d.logger.Info("fga_register_drainer_started", "mtls", d.cfg.MTLS.IAMRegister.Enable)
	} else {
		d.logger.Warn("fga_register_drainer_disabled_or_no_iam_peer — created resources will not get their per-resource FGA owner-tuple",
			"enable", d.cfg.FGA.RegisterDrainer.Enable, "iam_peer", d.peers.Register != nil)
	}

	// Boot-once backfill listeners.vip_origin: держит readiness not-ready (fail-closed)
	// до успешного завершения. Свежий стенд → no-op → readiness сразу ready.
	vipOriginGate := &vipOriginReconcileGate{}
	vipOriginReconciler := jobs.NewVIPOriginReconciler(
		jobs.NewPgVIPOriginStore(d.pool), d.peers.Address, d.logger)
	background = append(background, bgWorker{"vip-origin-reconcile", func(c context.Context) error {
		return runVIPOriginReconcile(c, vipOriginReconciler, vipOriginGate, d.logger)
	}})

	return background, vipOriginGate, nil
}
