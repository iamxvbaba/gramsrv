package config

import (
	"time"

	"telesrv/internal/branding"
)

type EdgeConfigYAML struct {
	Version    int `yaml:"version"`
	CommonYAML `yaml:",inline"`
	Edge       EdgeYAML       `yaml:"edge"`
	CoreExec   GRPCClientYAML `yaml:"core_exec"`
	FileData   GRPCClientYAML `yaml:"file_data"`
	Egress     EdgeEgressYAML `yaml:"egress"`
	// Identity lets Edge publish the operator-edited name, description and
	// icon next to its RSA key at /owpengram/server-info (read-only; Admin
	// stays the only writer).
	Identity IdentityYAML `yaml:"identity"`
}

type EdgeYAML struct {
	Listen       string           `yaml:"listen"`
	RSAKey       string           `yaml:"rsa_key"`
	WebSocket    WebSocketYAML    `yaml:"websocket"`
	MTProto      MTProtoYAML      `yaml:"mtproto"`
	Location     EdgeLocationYAML `yaml:"location"`
	AuthKeyCache CacheYAML        `yaml:"auth_key_cache"`
	TempKeyCache CacheYAML        `yaml:"temp_key_cache"`
}

type WebSocketYAML struct {
	Enabled        *bool    `yaml:"enabled"`
	AllowedOrigins []string `yaml:"allowed_origins"`
}

type MTProtoYAML struct {
	MaxConnections              *int   `yaml:"max_connections"`
	MaxConnectionsPerIP         *int   `yaml:"max_connections_per_ip"`
	MaxConcurrentHandshakes     *int   `yaml:"max_concurrent_handshakes"`
	RPCMaxInflight              *int   `yaml:"rpc_max_inflight"`
	RPCQueueSize                *int   `yaml:"rpc_queue_size"`
	RPCTimeout                  string `yaml:"rpc_timeout"`
	RPCGlobalWorkers            *int   `yaml:"rpc_global_workers"`
	RPCGlobalMaxTasks           *int   `yaml:"rpc_global_max_tasks"`
	RPCGlobalMaxBytes           *int64 `yaml:"rpc_global_max_bytes"`
	RPCExecutionMaxEntries      *int   `yaml:"rpc_execution_max_entries"`
	RPCExecutionAuthMaxEntries  *int   `yaml:"rpc_execution_auth_max_entries"`
	RPCExecutionSessionMaxEntry *int   `yaml:"rpc_execution_session_max_entries"`
	RPCExecutionPendingPerAuth  *int   `yaml:"rpc_execution_pending_per_auth"`
	InboundFrameGlobalMaxBytes  *int64 `yaml:"inbound_frame_global_max_bytes"`
	OutboundQueueSize           *int   `yaml:"outbound_queue_size"`
	OutboundControlQueueSize    *int   `yaml:"outbound_control_queue_size"`
	OutboundTrackedGlobalBytes  *int64 `yaml:"outbound_tracked_global_max_bytes"`
	OutboundCriticalGlobalBytes *int64 `yaml:"outbound_critical_global_max_bytes"`
	OutboundWriteGlobalBytes    *int64 `yaml:"outbound_write_global_max_bytes"`
}

type EdgeLocationYAML struct {
	TTL               string `yaml:"ttl"`
	HeartbeatInterval string `yaml:"heartbeat_interval"`
}

type CacheYAML struct {
	MaxEntries *int   `yaml:"max_entries"`
	TTL        string `yaml:"ttl"`
}

type EdgeEgressYAML struct {
	Delivery GRPCClientYAML `yaml:"delivery"`
}

// LoadEdge reads the Edge role YAML config and returns the runtime snapshot used by the Edge process.
func LoadEdge() (EdgeConfig, error) {
	cfg, err := loadRoleYAML(roleEdge, func(b *envBuilder, y EdgeConfigYAML) error {
		if err := requireYAMLVersion(y.Version); err != nil {
			return err
		}
		if err := applyCommonYAML(b, y.CommonYAML); err != nil {
			return err
		}
		b.setString("TELESRV_LISTEN", y.Edge.Listen)
		b.setString("TELESRV_RSA_KEY", y.Edge.RSAKey)
		applyIdentityYAML(b, y.Identity)
		b.setBool("TELESRV_WEBSOCKET_ENABLE", y.Edge.WebSocket.Enabled)
		b.setList("TELESRV_WEBSOCKET_ALLOWED_ORIGINS", y.Edge.WebSocket.AllowedOrigins)
		b.setInt("TELESRV_MTPROTO_MAX_CONNECTIONS", y.Edge.MTProto.MaxConnections)
		b.setInt("TELESRV_MTPROTO_MAX_CONNECTIONS_PER_IP", y.Edge.MTProto.MaxConnectionsPerIP)
		b.setInt("TELESRV_MTPROTO_MAX_CONCURRENT_HANDSHAKES", y.Edge.MTProto.MaxConcurrentHandshakes)
		b.setInt("TELESRV_MTPROTO_RPC_MAX_INFLIGHT", y.Edge.MTProto.RPCMaxInflight)
		b.setInt("TELESRV_MTPROTO_RPC_QUEUE_SIZE", y.Edge.MTProto.RPCQueueSize)
		if err := b.setDuration("TELESRV_MTPROTO_RPC_TIMEOUT", y.Edge.MTProto.RPCTimeout); err != nil {
			return err
		}
		b.setInt("TELESRV_MTPROTO_RPC_GLOBAL_WORKERS", y.Edge.MTProto.RPCGlobalWorkers)
		b.setInt("TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS", y.Edge.MTProto.RPCGlobalMaxTasks)
		b.setInt64("TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES", y.Edge.MTProto.RPCGlobalMaxBytes)
		b.setInt("TELESRV_MTPROTO_RPC_EXECUTION_MAX_ENTRIES", y.Edge.MTProto.RPCExecutionMaxEntries)
		b.setInt("TELESRV_MTPROTO_RPC_EXECUTION_AUTH_MAX_ENTRIES", y.Edge.MTProto.RPCExecutionAuthMaxEntries)
		b.setInt("TELESRV_MTPROTO_RPC_EXECUTION_SESSION_MAX_ENTRIES", y.Edge.MTProto.RPCExecutionSessionMaxEntry)
		b.setInt("TELESRV_MTPROTO_RPC_EXECUTION_PENDING_PER_AUTH", y.Edge.MTProto.RPCExecutionPendingPerAuth)
		b.setInt64("TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES", y.Edge.MTProto.InboundFrameGlobalMaxBytes)
		b.setInt("TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE", y.Edge.MTProto.OutboundQueueSize)
		b.setInt("TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE", y.Edge.MTProto.OutboundControlQueueSize)
		b.setInt64("TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES", y.Edge.MTProto.OutboundTrackedGlobalBytes)
		b.setInt64("TELESRV_MTPROTO_OUTBOUND_CRITICAL_GLOBAL_MAX_BYTES", y.Edge.MTProto.OutboundCriticalGlobalBytes)
		b.setInt64("TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES", y.Edge.MTProto.OutboundWriteGlobalBytes)
		if err := b.setDuration("TELESRV_EDGE_LOCATION_TTL", y.Edge.Location.TTL); err != nil {
			return err
		}
		if err := b.setDuration("TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL", y.Edge.Location.HeartbeatInterval); err != nil {
			return err
		}
		b.setInt("TELESRV_AUTH_KEY_CACHE_MAX_ENTRIES", y.Edge.AuthKeyCache.MaxEntries)
		if err := b.setDuration("TELESRV_AUTH_KEY_CACHE_TTL", y.Edge.AuthKeyCache.TTL); err != nil {
			return err
		}
		b.setInt("TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES", y.Edge.TempKeyCache.MaxEntries)
		if err := b.setDuration("TELESRV_TEMP_KEY_CACHE_TTL", y.Edge.TempKeyCache.TTL); err != nil {
			return err
		}
		if err := applyCoreExecClientYAML(b, y.CoreExec); err != nil {
			return err
		}
		if err := applyFileClientYAML(b, y.FileData); err != nil {
			return err
		}
		return applyEgressDeliveryClientYAML(b, y.Egress.Delivery)
	})
	if err != nil {
		return EdgeConfig{}, err
	}
	return edgeConfigFromConfig(cfg), nil
}

type EdgeConfig struct {
	DebugAddr string

	Branding           branding.Config
	PremiumBotUsername string
	PremiumBotUserID   int64

	ListenAddr                            string
	WebSocketEnable                       bool
	WebSocketAllowedOrigins               []string
	AdvertiseIP                           string
	AdvertisePort                         int
	RSAKeyPath                            string
	IdentityDir                           string
	DC                                    int
	DefaultCountryCode                    string
	StrictDCCheck                         bool
	MTProtoMaxConnections                 int
	MTProtoMaxConnectionsPerIP            int
	MTProtoMaxConcurrentHandshakes        int
	MTProtoRPCMaxInflight                 int
	MTProtoRPCQueueSize                   int
	MTProtoRPCTimeout                     time.Duration
	MTProtoRPCGlobalWorkers               int
	MTProtoRPCGlobalMaxTasks              int
	MTProtoRPCGlobalMaxBytes              int64
	MTProtoRPCDeliveryHookWorkers         int
	MTProtoRPCDeliveryHookMaxPending      int
	MTProtoRPCExecutionMaxEntries         int
	MTProtoRPCExecutionAuthMaxEntries     int
	MTProtoRPCExecutionSessionMaxEntries  int
	MTProtoRPCExecutionPendingPerAuth     int
	MTProtoInboundFrameGlobalMaxBytes     int64
	MTProtoOutboundQueueSize              int
	MTProtoOutboundControlQueueSize       int
	MTProtoOutboundTrackedGlobalMaxBytes  int64
	MTProtoOutboundCriticalGlobalMaxBytes int64
	MTProtoOutboundWriteGlobalMaxBytes    int64

	RedisAddr                     string
	RedisPassword                 string
	RedisDB                       int
	InstanceID                    string
	EdgeLocationTTL               time.Duration
	EdgeLocationHeartbeatInterval time.Duration

	CoreExecGRPCResolver          string
	CoreExecGRPCTargets           string
	CoreExecGRPCRequestTimeout    time.Duration
	CoreExecGRPCTLSCAFile         string
	CoreExecGRPCTLSServerName     string
	CoreExecGRPCTLSClientCertFile string
	CoreExecGRPCTLSClientKeyFile  string
	CoreExecToken                 string

	EgressDeliveryGRPCResolver          string
	EgressDeliveryGRPCTargets           string
	EgressDeliveryGRPCRequestTimeout    time.Duration
	EgressDeliveryGRPCTLSCAFile         string
	EgressDeliveryGRPCTLSServerName     string
	EgressDeliveryGRPCTLSClientCertFile string
	EgressDeliveryGRPCTLSClientKeyFile  string
	EgressDeliveryToken                 string

	FileGRPCTargets           string
	FileGRPCResolver          string
	FileGRPCRequestTimeout    time.Duration
	FileGRPCTLSCAFile         string
	FileGRPCTLSServerName     string
	FileGRPCTLSClientCertFile string
	FileGRPCTLSClientKeyFile  string
	FileToken                 string

	PublicBaseURL                 string
	UpdatePublicURL               string
	PublicAppScheme               string
	PublicAppLinkBase             string
	TempKeyResolveCacheMaxEntries int
	TempKeyResolveCacheTTL        time.Duration
	AuthUserCacheTTL              time.Duration
	AuthKeyCacheMaxEntries        int
	AuthKeyCacheTTL               time.Duration
	OutboundPushTimeout           time.Duration
}

func (c EdgeConfig) ProcessGlobals() ProcessGlobals {
	return ProcessGlobals{Branding: c.Branding, PremiumBotUsername: c.PremiumBotUsername, PremiumBotUserID: c.PremiumBotUserID}
}

func (c EdgeConfig) DebugAddress() string {
	return c.DebugAddr
}

func edgeConfigFromConfig(c Config) EdgeConfig {
	return EdgeConfig{
		DebugAddr: c.DebugAddr, Branding: c.Branding, PremiumBotUsername: c.PremiumBotUsername, PremiumBotUserID: c.PremiumBotUserID,
		ListenAddr: c.ListenAddr, WebSocketEnable: c.WebSocketEnable, WebSocketAllowedOrigins: append([]string(nil), c.WebSocketAllowedOrigins...),
		AdvertiseIP: c.AdvertiseIP, AdvertisePort: c.AdvertisePort, RSAKeyPath: c.RSAKeyPath, IdentityDir: c.IdentityDir, DC: c.DC, DefaultCountryCode: c.DefaultCountryCode, StrictDCCheck: c.StrictDCCheck,
		MTProtoMaxConnections: c.MTProtoMaxConnections, MTProtoMaxConnectionsPerIP: c.MTProtoMaxConnectionsPerIP, MTProtoMaxConcurrentHandshakes: c.MTProtoMaxConcurrentHandshakes,
		MTProtoRPCMaxInflight: c.MTProtoRPCMaxInflight, MTProtoRPCQueueSize: c.MTProtoRPCQueueSize, MTProtoRPCTimeout: c.MTProtoRPCTimeout,
		MTProtoRPCGlobalWorkers: c.MTProtoRPCGlobalWorkers, MTProtoRPCGlobalMaxTasks: c.MTProtoRPCGlobalMaxTasks, MTProtoRPCGlobalMaxBytes: c.MTProtoRPCGlobalMaxBytes,
		MTProtoRPCDeliveryHookWorkers: c.MTProtoRPCDeliveryHookWorkers, MTProtoRPCDeliveryHookMaxPending: c.MTProtoRPCDeliveryHookMaxPending,
		MTProtoRPCExecutionMaxEntries: c.MTProtoRPCExecutionMaxEntries, MTProtoRPCExecutionAuthMaxEntries: c.MTProtoRPCExecutionAuthMaxEntries,
		MTProtoRPCExecutionSessionMaxEntries: c.MTProtoRPCExecutionSessionMaxEntries, MTProtoRPCExecutionPendingPerAuth: c.MTProtoRPCExecutionPendingPerAuth,
		MTProtoInboundFrameGlobalMaxBytes: c.MTProtoInboundFrameGlobalMaxBytes, MTProtoOutboundQueueSize: c.MTProtoOutboundQueueSize,
		MTProtoOutboundControlQueueSize: c.MTProtoOutboundControlQueueSize, MTProtoOutboundTrackedGlobalMaxBytes: c.MTProtoOutboundTrackedGlobalMaxBytes,
		MTProtoOutboundCriticalGlobalMaxBytes: c.MTProtoOutboundCriticalGlobalMaxBytes, MTProtoOutboundWriteGlobalMaxBytes: c.MTProtoOutboundWriteGlobalMaxBytes,
		RedisAddr: c.RedisAddr, RedisPassword: c.RedisPassword, RedisDB: c.RedisDB, InstanceID: c.InstanceID,
		EdgeLocationTTL: c.EdgeLocationTTL, EdgeLocationHeartbeatInterval: c.EdgeLocationHeartbeatInterval,
		CoreExecGRPCResolver: c.CoreExecGRPCResolver, CoreExecGRPCTargets: c.CoreExecGRPCTargets, CoreExecGRPCRequestTimeout: c.CoreExecGRPCRequestTimeout,
		CoreExecGRPCTLSCAFile: c.CoreExecGRPCTLSCAFile, CoreExecGRPCTLSServerName: c.CoreExecGRPCTLSServerName,
		CoreExecGRPCTLSClientCertFile: c.CoreExecGRPCTLSClientCertFile, CoreExecGRPCTLSClientKeyFile: c.CoreExecGRPCTLSClientKeyFile, CoreExecToken: c.CoreExecToken,
		EgressDeliveryGRPCResolver: c.EgressDeliveryGRPCResolver, EgressDeliveryGRPCTargets: c.EgressDeliveryGRPCTargets, EgressDeliveryGRPCRequestTimeout: c.EgressDeliveryGRPCRequestTimeout,
		EgressDeliveryGRPCTLSCAFile: c.EgressDeliveryGRPCTLSCAFile, EgressDeliveryGRPCTLSServerName: c.EgressDeliveryGRPCTLSServerName,
		EgressDeliveryGRPCTLSClientCertFile: c.EgressDeliveryGRPCTLSClientCertFile, EgressDeliveryGRPCTLSClientKeyFile: c.EgressDeliveryGRPCTLSClientKeyFile, EgressDeliveryToken: c.EgressDeliveryToken,
		FileGRPCTargets: c.FileGRPCTargets, FileGRPCResolver: c.FileGRPCResolver, FileGRPCRequestTimeout: c.FileGRPCRequestTimeout,
		FileGRPCTLSCAFile: c.FileGRPCTLSCAFile, FileGRPCTLSServerName: c.FileGRPCTLSServerName,
		FileGRPCTLSClientCertFile: c.FileGRPCTLSClientCertFile, FileGRPCTLSClientKeyFile: c.FileGRPCTLSClientKeyFile, FileToken: c.FileToken,
		PublicBaseURL: c.PublicBaseURL, UpdatePublicURL: c.UpdatePublicURL, PublicAppScheme: c.PublicAppScheme, PublicAppLinkBase: c.PublicAppLinkBase,
		TempKeyResolveCacheMaxEntries: c.TempKeyResolveCacheMaxEntries, TempKeyResolveCacheTTL: c.TempKeyResolveCacheTTL,
		AuthUserCacheTTL: c.AuthUserCacheTTL, AuthKeyCacheMaxEntries: c.AuthKeyCacheMaxEntries, AuthKeyCacheTTL: c.AuthKeyCacheTTL,
		OutboundPushTimeout: c.OutboundPushTimeout,
	}
}
