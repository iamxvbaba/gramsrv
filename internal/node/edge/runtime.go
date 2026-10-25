// Package edge wires the production MTProto Edge role.
package edge

import (
	"context"
	"crypto/rsa"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/config"
	"telesrv/internal/coreexec"
	"telesrv/internal/edgecontrol"
	"telesrv/internal/edgecontrol/redisbus"
	"telesrv/internal/edgecontrol/redisregistry"
	egresssvc "telesrv/internal/egress"
	"telesrv/internal/filedata"
	"telesrv/internal/identity"
	"telesrv/internal/mtprotoedge"
	"telesrv/internal/node/common"
	obsmetrics "telesrv/internal/observability/metrics"
	"telesrv/internal/rpc"
	"telesrv/internal/serverinfo"
	"telesrv/internal/store/redisstore"
)

// Run starts the production MTProto Edge entrypoint.
func Run(logger *zap.Logger, buildMeta common.BuildMetadata) error {
	cfg, err := config.LoadEdge()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return runProcess(logger, cfg, buildMeta)
}

func runProcess(logger *zap.Logger, cfg config.EdgeConfig, buildMeta common.BuildMetadata) error {
	if err := validateEdgeConfig(cfg); err != nil {
		return err
	}
	instanceID, err := config.RequireInstanceID(cfg.InstanceID)
	if err != nil {
		return fmt.Errorf("invalid instance id: %w", err)
	}
	if err := common.ConfigureProcessGlobals(cfg); err != nil {
		return err
	}
	rsaKey, err := mtprotoedge.LoadOrGenerateRSAKey(cfg.RSAKeyPath)
	if err != nil {
		return fmt.Errorf("server rsa key: %w", err)
	}
	fingerprint := exchange.PrivateKey{RSA: rsaKey}.Fingerprint()

	logger.Info("telesrv edge starting",
		zap.String("listen", cfg.ListenAddr),
		zap.Int("dc", cfg.DC),
		zap.String("default_country_code", cfg.DefaultCountryCode),
		zap.String("advertise", net.JoinHostPort(cfg.AdvertiseIP, strconv.Itoa(cfg.AdvertisePort))),
		zap.Int("tl_layer", tg.Layer),
		zap.String("git_commit", buildMeta.Commit),
		zap.String("git_branch", buildMeta.Branch),
		zap.String("git_tree_state", buildMeta.TreeState),
		zap.String("build_time", buildMeta.BuildTime),
		zap.String("go_version", buildMeta.GoVersion),
		zap.String("instance_id", instanceID),
		zap.String("rsa_key", cfg.RSAKeyPath),
		zap.Int64("rsa_fingerprint", fingerprint),
	)

	ctx, stop, metricRegistry := common.StartRuntimeSupport(cfg, logger)
	defer stop()
	return runWithConfig(ctx, cfg, logger, metricRegistry, rsaKey, instanceID, buildMeta, fingerprint)
}

func runWithConfig(
	ctx context.Context,
	cfg config.EdgeConfig,
	logger *zap.Logger,
	metricRegistry *obsmetrics.Registry,
	rsaKey *rsa.PrivateKey,
	instanceID string,
	buildMeta common.BuildMetadata,
	fingerprint int64,
) error {
	rdb, err := redisstore.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { _ = rdb.Close() }()
	metricRegistry.AddGaugeProvider(func() []obsmetrics.GaugeSample {
		stat := rdb.PoolStats()
		return []obsmetrics.GaugeSample{
			{Name: "telesrv_redis_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "total"}}, Value: float64(stat.TotalConns)},
			{Name: "telesrv_redis_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "idle"}}, Value: float64(stat.IdleConns)},
			{Name: "telesrv_redis_pool_pending_requests", Value: float64(stat.PendingRequests)},
			{Name: "telesrv_redis_pool_hits", Value: float64(stat.Hits)},
			{Name: "telesrv_redis_pool_misses", Value: float64(stat.Misses)},
			{Name: "telesrv_redis_pool_timeouts", Value: float64(stat.Timeouts)},
			{Name: "telesrv_redis_pool_wait_count", Value: float64(stat.WaitCount)},
			{Name: "telesrv_redis_pool_wait_seconds", Value: time.Duration(stat.WaitDurationNs).Seconds()},
		}
	})

	activeSessions := mtprotoedge.NewSessionManager(logger.Named("mtprotoedge").Named("sessions"))
	localEdgeControl, ok := edgecontrol.NewLocal(activeSessions).(edgecontrol.FullController)
	if !ok {
		return fmt.Errorf("active session manager does not implement full edge control contract")
	}
	edgeLocationRegistry := redisregistry.New(rdb)
	edgeCommandBus := redisbus.New(rdb)
	layerAuthority := &requiredAuthKeySessionLayerStore{}
	codecRouter := rpc.New(rpc.Config{
		DC:                            cfg.DC,
		DefaultCountryCode:            cfg.DefaultCountryCode,
		IP:                            cfg.AdvertiseIP,
		Port:                          cfg.AdvertisePort,
		InstanceID:                    instanceID,
		OutboundPushTimeout:           cfg.OutboundPushTimeout,
		PublicBaseURL:                 cfg.PublicBaseURL,
		UpdatePublicURL:               cfg.UpdatePublicURL,
		PublicAppScheme:               cfg.PublicAppScheme,
		PublicAppLinkBase:             cfg.PublicAppLinkBase,
		TempKeyResolveCacheTTL:        cfg.TempKeyResolveCacheTTL,
		TempKeyResolveCacheMaxEntries: cfg.TempKeyResolveCacheMaxEntries,
		AuthUserCacheTTL:              cfg.AuthUserCacheTTL,
	}, rpc.Deps{
		Sessions:             localEdgeControl,
		Metrics:              metricRegistry,
		AuthKeySessionLayers: layerAuthority,
	}, logger.Named("rpc").Named("edge-shell"), clock.System)
	coreExecGRPCTargets, err := coreexec.ParseGRPCTargetsForResolver(cfg.CoreExecGRPCTargets, cfg.CoreExecGRPCResolver)
	if err != nil {
		return fmt.Errorf("parse TELESRV_CORE_EXEC_GRPC_TARGETS: %w", err)
	}
	coreExecutor, conn, err := coreexec.DialGRPCRemote(ctx, codecRouter, coreexec.GRPCClientConfig{
		Targets:        coreExecGRPCTargets,
		ResolverKind:   cfg.CoreExecGRPCResolver,
		Token:          cfg.CoreExecToken,
		InstanceID:     instanceID,
		Logger:         logger.Named("coreexec").Named("grpc").Named("client"),
		TLSCAFile:      cfg.CoreExecGRPCTLSCAFile,
		TLSServerName:  cfg.CoreExecGRPCTLSServerName,
		TLSCertFile:    cfg.CoreExecGRPCTLSClientCertFile,
		TLSKeyFile:     cfg.CoreExecGRPCTLSClientKeyFile,
		RequestTimeout: cfg.CoreExecGRPCRequestTimeout,
		Metrics:        metricRegistry,
	})
	if err != nil {
		return fmt.Errorf("connect coreexec grpc: %w", err)
	}
	defer func() { _ = conn.Close() }()
	common.RegisterCoreExecPendingAdmissionMetrics(metricRegistry, "grpc", coreExecutor)
	logger.Info("coreexec grpc health check passed",
		zap.String("resolver", cfg.CoreExecGRPCResolver),
		zap.Strings("targets", coreExecGRPCTargets))
	lifecycleReporter, err := newEdgeSessionLifecycleReporter(
		coreExecutor,
		logger.Named("coreexec").Named("session-lifecycle"),
	)
	if err != nil {
		return fmt.Errorf("create session lifecycle reporter: %w", err)
	}
	lifecycleObserver, err := newEdgeSessionLifecycleObserver(
		codecRouter,
		lifecycleReporter,
		logger.Named("mtprotoedge").Named("session-lifecycle"),
	)
	if err != nil {
		return fmt.Errorf("create session lifecycle observer: %w", err)
	}
	activeSessions.SetLifecycleObserver(lifecycleObserver)
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	go lifecycleReporter.Run(lifecycleCtx)
	defer func() {
		cancelLifecycle()
		waitCtx, cancel := context.WithTimeout(context.Background(), edgeSessionOfflineDrainTimeout+time.Second)
		defer cancel()
		if err := lifecycleReporter.Wait(waitCtx); err != nil {
			logger.Error("session lifecycle reporter did not stop cleanly", zap.Error(err))
		}
	}()
	metricRegistry.AddGaugeProvider(func() []obsmetrics.GaugeSample {
		return []obsmetrics.GaugeSample{{
			Name:  "telesrv_coreexec_session_offline_pending",
			Value: float64(lifecycleReporter.Pending()),
		}}
	})
	authKeyStore := coreexec.NewCachedAuthKeyStore(coreExecutor, coreexec.AuthKeyCacheConfig{
		MaxEntries: cfg.AuthKeyCacheMaxEntries,
		TTL:        cfg.AuthKeyCacheTTL,
		Logger:     logger.Named("coreexec").Named("auth-key-cache"),
	})
	layerIdentitySource, err := newEdgeAuthKeyLayerIdentitySource(authKeyStore, coreExecutor, rdb)
	if err != nil {
		return fmt.Errorf("initialize Edge Layer identity source: %w", err)
	}
	authKeySessionLayers, err := redisstore.NewAuthKeySessionLayerStore(rdb, layerIdentitySource)
	if err != nil {
		return fmt.Errorf("initialize Edge Layer authority: %w", err)
	}
	if err := layerAuthority.Bind(authKeySessionLayers); err != nil {
		return fmt.Errorf("bind Edge Layer authority: %w", err)
	}

	fileDataGRPCTargets, err := filedata.ParseGRPCTargetsForResolver(cfg.FileGRPCTargets, cfg.FileGRPCResolver)
	if err != nil {
		return fmt.Errorf("parse TELESRV_FILE_GRPC_TARGETS: %w", err)
	}
	fileDataRemote, fileDataConn, err := filedata.DialGRPCRemote(ctx, filedata.GRPCClientConfig{
		Targets:        fileDataGRPCTargets,
		ResolverKind:   cfg.FileGRPCResolver,
		Token:          cfg.FileToken,
		Logger:         logger.Named("filedata").Named("grpc").Named("client"),
		TLSCAFile:      cfg.FileGRPCTLSCAFile,
		TLSServerName:  cfg.FileGRPCTLSServerName,
		TLSCertFile:    cfg.FileGRPCTLSClientCertFile,
		TLSKeyFile:     cfg.FileGRPCTLSClientKeyFile,
		RequestTimeout: cfg.FileGRPCRequestTimeout,
	})
	if err != nil {
		return fmt.Errorf("connect filedata grpc: %w", err)
	}
	defer func() { _ = fileDataConn.Close() }()
	layerRPC := mtprotoedge.NewFileDataLayerRPC(coreExecutor, fileDataRemote, logger.Named("mtprotoedge").Named("filedata"))
	logger.Info("filedata grpc health check passed",
		zap.String("resolver", cfg.FileGRPCResolver),
		zap.Strings("targets", fileDataGRPCTargets),
		zap.String("backend", fileDataRemote.Name()))

	authInvalidationBroker := redisstore.NewAuthInvalidationBroker(rdb)
	egressDeliveryGRPCTargets, err := egresssvc.ParseGRPCDeliveryTargetsForResolver(cfg.EgressDeliveryGRPCTargets, cfg.EgressDeliveryGRPCResolver)
	if err != nil {
		return fmt.Errorf("parse TELESRV_EGRESS_DELIVERY_GRPC_TARGETS: %w", err)
	}
	egressDeliveryRemote, egressDeliveryConn, err := egresssvc.DialGRPCDeliveryRemote(ctx, egresssvc.GRPCDeliveryClientConfig{
		Targets:        egressDeliveryGRPCTargets,
		ResolverKind:   cfg.EgressDeliveryGRPCResolver,
		Token:          cfg.EgressDeliveryToken,
		Logger:         logger.Named("egress").Named("delivery").Named("grpc").Named("client"),
		TLSCAFile:      cfg.EgressDeliveryGRPCTLSCAFile,
		TLSServerName:  cfg.EgressDeliveryGRPCTLSServerName,
		TLSCertFile:    cfg.EgressDeliveryGRPCTLSClientCertFile,
		TLSKeyFile:     cfg.EgressDeliveryGRPCTLSClientKeyFile,
		RequestTimeout: cfg.EgressDeliveryGRPCRequestTimeout,
	})
	if err != nil {
		return fmt.Errorf("connect egress delivery grpc: %w", err)
	}
	defer func() { _ = egressDeliveryConn.Close() }()
	physicalReceiptReporter := newPhysicalReceiptReporter(egressDeliveryRemote, cfg.EgressDeliveryGRPCRequestTimeout, logger.Named("egress").Named("physical-receipt-reporter"))
	// Reporter lifecycle is closed explicitly after the delivery executor. Do
	// not let the shared serve context stop it before executor shutdown commits
	// every pre-reserved receipt slot.
	go physicalReceiptReporter.Run(context.Background())
	defer func() {
		physicalReceiptReporter.Close()
		physicalReceiptReporter.Wait()
	}()
	if err := activeSessions.SetPhysicalReceiptSink(physicalReceiptReporter); err != nil {
		return fmt.Errorf("install physical receipt sink: %w", err)
	}
	defer activeSessions.CloseDeliveryExecutor()

	clientAckReporter := newDeliveryClientAckReporter(egressDeliveryRemote, instanceID, cfg.EgressDeliveryGRPCRequestTimeout, logger.Named("egress").Named("client-ack-reporter"))
	go clientAckReporter.Run(context.Background())
	defer func() {
		clientAckReporter.Close()
		clientAckReporter.Wait()
	}()
	logger.Info("egress delivery grpc health check passed",
		zap.String("resolver", cfg.EgressDeliveryGRPCResolver),
		zap.Strings("targets", egressDeliveryGRPCTargets))

	logger.Info("edge location registry enabled",
		zap.String("instance_id", instanceID),
		zap.Duration("ttl", cfg.EdgeLocationTTL),
		zap.Duration("lease_renew_interval", cfg.EdgeLocationHeartbeatInterval),
	)
	if err := activeSessions.StartLocationRegistry(ctx, edgeLocationRegistry, instanceID, cfg.EdgeLocationTTL, cfg.EdgeLocationHeartbeatInterval); err != nil {
		return fmt.Errorf("start edge location registry: %w", err)
	}
	go edgecontrol.RunDeliveryBatchSubscriber(ctx, edgeCommandBus, instanceID, activeSessions)
	go edgecontrol.RunSessionControlSubscriber(ctx, edgeCommandBus, instanceID, localEdgeControl)
	go authKeyStore.RunAuthKeyInvalidationSubscriber(ctx, authInvalidationBroker, instanceID, activeSessions, logger.Named("coreexec").Named("auth-key-invalidation"))
	go activeSessions.RunPendingSweeper(ctx, time.Minute)

	srv := mtprotoedge.New(mtprotoedge.Options{
		Logger:                         logger.Named("mtprotoedge"),
		DC:                             cfg.DC,
		StrictDC:                       cfg.StrictDCCheck,
		RSAKey:                         rsaKey,
		LayerRPC:                       layerRPC,
		AuthKeys:                       authKeyStore,
		ActiveSessions:                 activeSessions,
		Metrics:                        metricRegistry,
		ObfuscatedTCP:                  true,
		WebSocket:                      cfg.WebSocketEnable,
		WebSocketAllowedOrigins:        cfg.WebSocketAllowedOrigins,
		SamePortHTTP:                   serverinfo.Handler(cfg.DC, rsaKey, identity.NewStore(cfg.IdentityDir)),
		MaxConnections:                 cfg.MTProtoMaxConnections,
		MaxConnectionsPerIP:            cfg.MTProtoMaxConnectionsPerIP,
		MaxConcurrentHandshakes:        cfg.MTProtoMaxConcurrentHandshakes,
		RPCMaxInflight:                 cfg.MTProtoRPCMaxInflight,
		RPCQueueSize:                   cfg.MTProtoRPCQueueSize,
		RPCTimeout:                     cfg.MTProtoRPCTimeout,
		RPCGlobalWorkers:               cfg.MTProtoRPCGlobalWorkers,
		RPCGlobalMaxTasks:              cfg.MTProtoRPCGlobalMaxTasks,
		RPCGlobalMaxBytes:              cfg.MTProtoRPCGlobalMaxBytes,
		RPCDeliveryHookWorkers:         cfg.MTProtoRPCDeliveryHookWorkers,
		RPCDeliveryHookMaxPending:      cfg.MTProtoRPCDeliveryHookMaxPending,
		RPCExecutionMaxEntries:         cfg.MTProtoRPCExecutionMaxEntries,
		RPCExecutionAuthMaxEntries:     cfg.MTProtoRPCExecutionAuthMaxEntries,
		RPCExecutionSessionMaxEntries:  cfg.MTProtoRPCExecutionSessionMaxEntries,
		RPCExecutionPendingPerAuth:     cfg.MTProtoRPCExecutionPendingPerAuth,
		InboundFrameGlobalMaxBytes:     cfg.MTProtoInboundFrameGlobalMaxBytes,
		OutboundQueueSize:              cfg.MTProtoOutboundQueueSize,
		OutboundControlQueueSize:       cfg.MTProtoOutboundControlQueueSize,
		OutboundTrackedGlobalMaxBytes:  cfg.MTProtoOutboundTrackedGlobalMaxBytes,
		OutboundCriticalGlobalMaxBytes: cfg.MTProtoOutboundCriticalGlobalMaxBytes,
		OutboundWriteGlobalMaxBytes:    cfg.MTProtoOutboundWriteGlobalMaxBytes,
		DeliveryClientAcked:            clientAckReporter.Observe,
		OnServing: func(_ net.Addr) {
			logger.Info("telesrv edge ready",
				zap.String("listen", cfg.ListenAddr),
				zap.String("advertise", net.JoinHostPort(cfg.AdvertiseIP, strconv.Itoa(cfg.AdvertisePort))),
				zap.Strings("core_exec_grpc_targets", coreExecGRPCTargets),
				zap.String("core_exec_grpc_resolver", cfg.CoreExecGRPCResolver),
				zap.Strings("file_grpc_targets", fileDataGRPCTargets),
				zap.String("file_grpc_resolver", cfg.FileGRPCResolver),
				zap.Strings("egress_delivery_grpc_targets", egressDeliveryGRPCTargets),
				zap.String("egress_delivery_grpc_resolver", cfg.EgressDeliveryGRPCResolver),
				zap.Int("pid", os.Getpid()),
				zap.String("instance_id", instanceID),
				zap.String("git_commit", buildMeta.Commit),
				zap.Int64("rsa_fingerprint", fingerprint),
			)
		},
	})
	metricRegistry.AddGaugeProvider(func() []obsmetrics.GaugeSample {
		return common.MTProtoRuntimeGaugeSamples(srv.RuntimeSnapshot())
	})
	return serveUntilLocationRegistryFatal(ctx, activeSessions.LocationRegistryFatal(), func(serveCtx context.Context) error {
		return srv.ListenAndServe(serveCtx, cfg.ListenAddr)
	})
}

func serveUntilLocationRegistryFatal(ctx context.Context, fatal <-chan error, serve func(context.Context) error) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	fatalResult := make(chan error, 1)
	go func() {
		select {
		case err := <-fatal:
			if err == nil {
				return
			}
			fatalResult <- err
			cancel()
		case <-serveCtx.Done():
		}
	}()

	serveErr := serve(serveCtx)
	select {
	case fatalErr := <-fatalResult:
		return fmt.Errorf("edge location registry fatal: %w", fatalErr)
	default:
		return serveErr
	}
}

func validateEdgeConfig(cfg config.EdgeConfig) error {
	if strings.TrimSpace(cfg.CoreExecGRPCTargets) == "" {
		return fmt.Errorf("TELESRV_CORE_EXEC_GRPC_TARGETS is required by cmd/telesrv-edge")
	}
	if _, err := coreexec.ParseGRPCTargetsForResolver(cfg.CoreExecGRPCTargets, cfg.CoreExecGRPCResolver); err != nil {
		return fmt.Errorf("parse TELESRV_CORE_EXEC_GRPC_TARGETS: %w", err)
	}
	if strings.TrimSpace(cfg.CoreExecToken) == "" {
		return fmt.Errorf("TELESRV_CORE_EXEC_TOKEN is required by cmd/telesrv-core and cmd/telesrv-edge")
	}
	if strings.TrimSpace(cfg.FileGRPCTargets) == "" {
		return fmt.Errorf("TELESRV_FILE_GRPC_TARGETS is required by cmd/telesrv-edge")
	}
	if _, err := filedata.ParseGRPCTargetsForResolver(cfg.FileGRPCTargets, cfg.FileGRPCResolver); err != nil {
		return fmt.Errorf("parse TELESRV_FILE_GRPC_TARGETS: %w", err)
	}
	if strings.TrimSpace(cfg.FileToken) == "" {
		return fmt.Errorf("TELESRV_FILE_TOKEN is required by cmd/telesrv-edge and cmd/telesrv-file")
	}
	if strings.TrimSpace(cfg.EgressDeliveryGRPCTargets) == "" {
		return fmt.Errorf("TELESRV_EGRESS_DELIVERY_GRPC_TARGETS is required by cmd/telesrv-edge")
	}
	if _, err := egresssvc.ParseGRPCDeliveryTargetsForResolver(cfg.EgressDeliveryGRPCTargets, cfg.EgressDeliveryGRPCResolver); err != nil {
		return fmt.Errorf("parse TELESRV_EGRESS_DELIVERY_GRPC_TARGETS: %w", err)
	}
	if strings.TrimSpace(cfg.EgressDeliveryToken) == "" {
		return fmt.Errorf("TELESRV_EGRESS_DELIVERY_TOKEN is required by cmd/telesrv-edge and cmd/telesrv-egress")
	}
	return nil
}
