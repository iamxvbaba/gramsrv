package mtprotoedge

import (
	"context"
	"crypto/rsa"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tmap"
	"github.com/iamxvbaba/td/transport"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// legacyRPCHandler 把解密后的 canonical RPC 请求体路由到响应。
//
// b 是明文 RPC 请求（已剥离 MTProto 外壳）；返回的 bin.Encoder 会被包成 rpc_result。
// 返回 *tgerr.Error 时连接层将其转为 rpc_error 回发；其他 error 视为连接级故障。
//
// 它只保留给本包旧连接状态机的回归测试。生产 API RPC 必须走 LayerRPCHandler，
// 使 admission、request/result TypeRef 与 exact profile 不可绕过。
type legacyRPCHandler interface {
	Dispatch(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, error)
	// NegotiatedLayer returns the TL layer proven via invokeWithLayer for this
	// exact (auth_key_id, session_id). It must never infer from API ID, device
	// metadata, authorization rows, or another session on the same auth key.
	NegotiatedLayer(authKeyID [8]byte, sessionID int64) (int, bool)
}

// legacyRPCHandlerWithMethod returns the canonical innermost RPC method after the
// router has peeled invokeWithLayer/initConnection/invokeAfter wrappers. Egress
// scheduling must use this identity: the outer wrapper is not a useful signal
// for prioritizing updates convergence over catalog/media responses.
type legacyRPCHandlerWithMethod interface {
	DispatchWithMethod(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, string, error)
}

// LayerRPCHandler is the production API-RPC boundary. Admission is a separate
// allocation-bounded phase so the edge can freeze the connection profile,
// validate wrapper dependencies and establish exact request identity before
// flight/cache/scheduler ownership is acquired.
type LayerRPCHandler interface {
	AdmitLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error)
	AdmitUnprofiled(b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error)
	DispatchAdmitted(
		ctx context.Context,
		authKeyID [8]byte,
		sessionID int64,
		msgID int64,
		admissionSeq uint64,
		request tlprofile.Admission,
	) (tlprofile.Result, string, error)
}

// LayerRPCDefaultProfileAdmitter decodes with a recoverable inherited/default
// profile. Production handlers should implement it with the same sparse
// tlprofile dispatcher and semantic adapter registry used by AdmitLayer. The split keeps old
// test doubles source-compatible while allowing invokeWithLayer to correct even
// a previously explicit Conn profile.
type LayerRPCDefaultProfileAdmitter interface {
	AdmitDefaultLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error)
}

// LayerRPCSessionProfileResolver may restore an exact profile only when it was
// previously proven for this same (auth_key_id, session_id). Auth-key-wide
// device metadata is intentionally ineligible: a client upgrade can reuse its
// auth key while opening a new session at a newer Layer.
type LayerRPCSessionProfileResolver interface {
	NegotiatedSessionLayer(authKeyID [8]byte, sessionID int64) (int, bool)
}

// LayerRPCOrderedSessionProfileResolver restores both the selected Layer and
// the newest invokeWithLayer client msg_id which proved it. The cursor prevents
// an old cached request replay on a replacement physical connection from
// rolling the logical session back to an older profile.
type LayerRPCOrderedSessionProfileResolver interface {
	NegotiatedSessionLayerEvidence(authKeyID [8]byte, sessionID int64) (layer int, msgID int64, ok bool)
}

// LayerRPCInheritedAuthKeyProfileResolver resolves the best persisted
// auth-key-wide client Layer for a new session. Unlike exact same-session
// evidence this value is only an inherited default: a later invokeWithLayer may
// correct it. Implementations may resolve a temporary raw key through its bound
// permanent key, but must not infer from API ID or device strings.
type LayerRPCInheritedAuthKeyProfileResolver interface {
	ResolveInheritedAuthKeyLayer(ctx context.Context, rawAuthKeyID [8]byte) (layer int, found bool, err error)
}

// LayerRPCSessionProfileRegistry atomically freezes and invalidates the exact
// same-session profile. Its bounded retention may survive a TCP reconnect and
// admit a naked replay while the entry remains live. Expiry or capacity eviction
// loses only that proof and therefore fails closed until fresh invokeWithLayer;
// it never falls back to auth-key-wide metadata.
type LayerRPCSessionProfileRegistry interface {
	LayerRPCSessionProfileResolver
	FreezeNegotiatedSessionLayer(authKeyID [8]byte, sessionID int64, layer int) error
	ForgetNegotiatedSessionLayer(authKeyID [8]byte, sessionID int64)
	ForgetNegotiatedAuthKey(authKeyID [8]byte)
}

// LayerRPCOrderedSessionProfileRegistry linearizes explicit Layer evidence by
// MTProto client msg_id. applied is false for an older or identical duplicate;
// the same msg_id with another Layer must return ErrLayerProfileConflict (or an
// error wrapping it). Implementations advance the cursor even when Layer is
// unchanged.
type LayerRPCOrderedSessionProfileRegistry interface {
	LayerRPCOrderedSessionProfileResolver
	FreezeNegotiatedSessionLayerAt(authKeyID [8]byte, sessionID int64, layer int, msgID int64) (applied bool, err error)
}

// LayerRPCDurableSessionProfileResolver restores restart-safe raw Layer
// evidence. layer may be newer than this binary's generated codec universe;
// msgID remains the ordering authority in that case.
type LayerRPCDurableSessionProfileResolver interface {
	ResolveNegotiatedSessionLayerEvidence(
		ctx context.Context,
		rawAuthKeyID [8]byte,
		sessionID int64,
	) (layer int, msgID int64, found bool, err error)
}

// LayerRPCDurableSessionProfileAdvancer atomically advances exact-session and
// auth-key shared-default evidence. publishShared is true only when this exact
// observation still owns the durable shared default.
type LayerRPCDurableSessionProfileAdvancer interface {
	AdvanceNegotiatedSessionLayerEvidence(
		ctx context.Context,
		rawAuthKeyID [8]byte,
		sessionID int64,
		layer int,
		msgID int64,
	) (currentLayer int, currentMsgID int64, publishShared bool, err error)
}

type LayerRPCDurableSessionProfileDeleter interface {
	DeleteNegotiatedSessionLayerEvidence(
		ctx context.Context,
		rawAuthKeyID [8]byte,
		sessionID int64,
	) (deleted bool, err error)
}

// LayerRPCReplayPreparer reapplies connection-local wrapper state for an
// already-executed exact request without consuming its one-shot business
// dispatch lease. The returned callback is safe to run only after a successful
// cached rpc_result reaches the replacement physical connection.
type LayerRPCReplayPreparer interface {
	PrepareAdmittedReplay(
		ctx context.Context,
		authKeyID [8]byte,
		sessionID int64,
		msgID int64,
		admissionSeq uint64,
		request tlprofile.Admission,
	) (afterSuccessfulDelivery func() error, err error)
}

// LayerRPCProfileEvidenceContext lets a generated/semantic handler carry the
// edge's MTProto msg_id freshness decision through its existing context-based
// dispatch API. fresh=false means the request remains fully request-bound: it
// may decode, execute and produce an exact result, but it must not publish
// mutable Layer/init/readiness/auth-bind state. Production Router implements
// this optional decorator; older handlers which have no such shared state stay
// source compatible.
type LayerRPCProfileEvidenceContext interface {
	WithLayerRPCProfileEvidenceFresh(ctx context.Context, fresh bool) context.Context
}

// LayerRPCAdmissionProfilePublisher advances the auth-key-wide inherited
// default for fresh explicit evidence. admissionSeq is allocated once by the
// edge's exact flight owner and globally orders different MTProto sessions;
// cached joins/replays never call this hook again.
type LayerRPCAdmissionProfilePublisher interface {
	PublishAdmittedLayerProfileEvidence(
		ctx context.Context,
		rawAuthKeyID [8]byte,
		sessionID int64,
		msgID int64,
		admissionSeq uint64,
		safeFloor uint64,
		layer int,
	) error
}

// RPCInitConnectionObserver records wrapper metadata when the edge aliases an
// initConnection reissue to an already-running request and therefore correctly
// skips a second business Dispatch.
type RPCInitConnectionObserver interface {
	ObserveInitConnection(
		ctx context.Context,
		authKeyID [8]byte,
		sessionID int64,
		layer, apiID int,
		deviceModel, systemVersion, appVersion, systemLangCode, langPack, langCode string,
	) error
}

// Options 配置 Server。
type Options struct {
	// Logger 日志器。默认 zap.NewNop()。
	Logger *zap.Logger
	// Codec 传输 codec 构造器。nil 表示自动探测（intermediate/abridged/full）。自定义
	// codec 必须是 gotd 内置四种 codec（可包 NoHeader），或实现 InboundFrameBudgetedCodec；
	// 无法在 payload 分配前预检长度的 codec 会 fail-closed。
	Codec func() transport.Codec
	// ObfuscatedTCP 先按 MTProto TCP obfuscation 解包，再自动探测 codec。
	// Telegram Desktop 的 tcpo_only endpoint 会走这个 64 字节前缀流程。
	ObfuscatedTCP bool
	// WebSocket 在同一个 listener 上接受 MTProto over WebSocket(/apiws*)。
	// 开启后仅在连接建立时读取前 4 字节做 HTTP/TCP 分流；MTProto TCP
	// 后续仍走原 ObfuscatedTCP + codec 热路径。
	WebSocket bool
	// WebSocketAllowedOrigins 是允许浏览器发起 WebSocket upgrade 的页面 origin。
	// 空列表表示只接受无 Origin 的非浏览器客户端；"*" 表示允许所有来源（仅调试）。
	WebSocketAllowedOrigins []string
	// ReadTimeout 单次读取超时。默认 5m。
	ReadTimeout time.Duration
	// HandshakeIdleTimeout 是连接「建立 session 前」（握手 + 首个加密消息之前）的读超时，
	// 比 ReadTimeout 短，用于快速回收握手后静默的半开 / 异常连接。默认 60s。
	HandshakeIdleTimeout time.Duration
	// HandshakeMaxDuration 是单次密钥交换（serverExchange）的总时长上界。HandshakeIdleTimeout
	// 只约束「单次读 idle」，对一个持续发包的客户端无效——若客户端陷入「收到 ResPQ→nonce 失步
	// →重发 req_pq」的握手重启死循环（见 docs/client-compat-notes.md），无界的 serverExchange 会
	// 对每个 req_pq 盲回 ResPQ、永不收敛地空转刷日志/占 CPU。本上界给整个握手设总预算，超时即
	// 放弃并断开，客户端重连发起全新握手（无残留相位差）即恢复。正常握手 <1s。默认 20s。
	HandshakeMaxDuration time.Duration
	// WriteTimeout 单次写入超时。默认 30s。
	WriteTimeout time.Duration
	// MaxConnections 是进程接受的 raw 物理连接总上限，覆盖 codec sniff、握手和
	// 已认证连接的完整生命周期。默认 200000；负数表示不限制。
	MaxConnections int
	// MaxConnectionsPerIP 是单 remote IP 的 raw 物理连接上限。默认 4096，
	// 为共享 NAT 与 TDesktop 多候选连接保留足够突发；负数表示不限制。
	MaxConnectionsPerIP int
	// MaxConcurrentHandshakes 是同时执行 auth_key_id=0 RSA/DH exchange 的上限。
	// 达限时已完成 transport framing 的连接收到 -429 后断开。默认 256；负数表示不限制。
	MaxConcurrentHandshakes int
	// RPCMaxInflight 是单连接同时处理的 RPC 上限。默认 32。
	RPCMaxInflight int
	// RPCQueueSize 是单连接等待处理的 RPC 队列长度。默认 64；队列按首条请求懒分配。
	RPCQueueSize int
	// RPCTimeout 是单个 RPC 在连接层的最大处理时长。默认 30s。
	// 超时从 Copy 前预算/入队开始计算，排队时间包含在内。
	RPCTimeout time.Duration
	// RPCGlobalWorkers 是 Server 共享 inbound RPC worker 数。默认 256。
	RPCGlobalWorkers int
	// RPCGlobalMaxTasks 是全进程已预留、排队和执行中的 RPC 条数上限。默认 8192。
	RPCGlobalMaxTasks int
	// RPCGlobalMaxBytes 是上述 RPC 的进程级 memory charge 预算。legacy charge
	// 等于 copied body；exact charge 是 typed decode 前的保守 materialization
	// 上界，因此该配置不表示可并发接收 512 MiB wire body。默认 512 MiB。
	RPCGlobalMaxBytes int64
	// RPCResultCache* limits bound pending ownership and completed rpc_result
	// replay state across the full 331-second duplicate horizon. Every owner is
	// charged simultaneously at global, raw-auth and session scopes. Defaults:
	// global 262144/64 MiB, auth 32768/32 MiB, session 16384/16 MiB.
	RPCResultCacheMaxEntries        int
	RPCResultCacheMaxBytes          int64
	RPCResultCacheAuthMaxEntries    int
	RPCResultCacheAuthMaxBytes      int64
	RPCResultCacheSessionMaxEntries int
	RPCResultCacheSessionMaxBytes   int64
	// RPCResultPendingPerAuth is an additional active-owner bound, independent
	// from the retained entry limits and RPCGlobalMaxTasks. Default 2048.
	RPCResultPendingPerAuth int
	// InboundFrameGlobalMaxBytes 是所有物理连接当前正在处理的 transport wire buffer
	// 与最大解密 plaintext buffer 的总预算。长度前缀读取后、payload 分配前预留，默认
	// 512 MiB；非正值使用默认值。
	InboundFrameGlobalMaxBytes int64
	// OutboundQueueSize / OutboundControlQueueSize 是每连接普通与控制 mailbox 容量。
	// 默认 128/32；控制队列在 actor 中保持严格优先。
	OutboundQueueSize        int
	OutboundControlQueueSize int
	// OutboundTrackedGlobalMaxBytes 是所有连接为 msg_resend_req 保留的 RPC/update body
	// 总预算。默认 512 MiB；编码后的 MTProto service frame 与控制向量另用 64 MiB
	// control budget（包括需 resend tracking 的 new_session_created 等），避免 body 压力
	// 阻断连接维持消息。可靠响应无法 tracking 时终止该连接，durable best-effort update
	// 则只丢在线加速并由 difference 恢复。
	OutboundTrackedGlobalMaxBytes int64
	// OutboundWriteGlobalMaxBytes bounds concurrent encrypted wire/codec/obfuscation scratch.
	// Scratch is shared and pooled across connections; default 512 MiB.
	OutboundWriteGlobalMaxBytes int64

	// DC 是本 server 的 DC ID。默认 2。
	DC int
	// StrictDC turns on exact DC-ID validation for the permanent-key exchange
	// (default off = lenient). telesrv is always a single physical backend —
	// there is no real multi-DC federation behind it — but self-hosted client
	// forks commonly run in "single-server backend" mode, where dc_id 1..5 all
	// alias to this one server so that any old data referencing a specific
	// dc_id still resolves correctly. When a client adds a new local account it
	// picks its own starting dc_id (its usual multi-DC load-spreading
	// behavior, unrelated to which physical server it's actually talking to)
	// — that choice is not guaranteed to equal our configured DC. Strict
	// validation would reject those accounts with "-444 wrong dc_id" even
	// though they are connecting to the right (and only) server; dc_id is a
	// client-side routing label here, not part of key derivation, so
	// accepting the mismatch does not weaken the exchange. The switch exists
	// for a hypothetical future real multi-DC deployment.
	StrictDC bool
	// RSAKey 是 server RSA 私钥，用于密钥交换。nil 时无法完成握手。
	RSAKey *rsa.PrivateKey
	// AuthKeys 持久化 auth key。默认内存实现。
	AuthKeys store.AuthKeyStore
	// ActiveSessions 管理活跃连接。默认新建；传入时可让 RPC 层共享同一注册表。
	ActiveSessions *SessionManager
	// legacyRPC is an internal test hook for the pre-exact RPC state machine.
	// It is deliberately unexported so production callers cannot bypass
	// generated Layer admission by configuring the canonical-only route.
	legacyRPC legacyRPCHandler
	// LayerRPC is the generated exact-profile production path. When configured,
	// every API request must complete admission before flight/cache scheduling.
	LayerRPC LayerRPCHandler
	// Metrics 接收连接层指标。默认 NopMetrics。
	Metrics Metrics
	// OnServing is called after the connection intake loops have been installed.
	// It is a platform-neutral observation hook; all slow initialization must
	// finish before ListenAndServe is entered.
	OnServing func(net.Addr)
	// Clock 用于消息 ID 与时间戳。默认 clock.System。
	Clock clock.Clock
	// Rand 随机源。默认 crypto.DefaultRand()。
	Rand io.Reader
}

func (o *Options) setDefaults() {
	if o.Logger == nil {
		o.Logger = zap.NewNop()
	}
	if o.ReadTimeout == 0 {
		o.ReadTimeout = 5 * time.Minute
	}
	if o.HandshakeIdleTimeout == 0 {
		o.HandshakeIdleTimeout = 60 * time.Second
	}
	if o.HandshakeMaxDuration == 0 {
		o.HandshakeMaxDuration = 20 * time.Second
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 30 * time.Second
	}
	if o.MaxConnections == 0 {
		o.MaxConnections = defaultMaxConnections
	}
	if o.MaxConnectionsPerIP == 0 {
		o.MaxConnectionsPerIP = defaultMaxConnectionsPerIP
	}
	if o.MaxConcurrentHandshakes == 0 {
		o.MaxConcurrentHandshakes = defaultMaxConcurrentHandshakes
	}
	if o.RPCMaxInflight <= 0 {
		o.RPCMaxInflight = 32
	}
	if o.RPCQueueSize <= 0 {
		o.RPCQueueSize = 64
	}
	if o.RPCTimeout == 0 {
		o.RPCTimeout = 30 * time.Second
	}
	if o.RPCGlobalWorkers <= 0 {
		o.RPCGlobalWorkers = 256
	}
	if o.RPCGlobalMaxTasks <= 0 {
		o.RPCGlobalMaxTasks = rpcResultFlightDefaultMaxPending
	}
	if o.RPCGlobalMaxBytes <= 0 {
		o.RPCGlobalMaxBytes = 512 << 20
	}
	if o.RPCResultCacheMaxEntries == 0 {
		o.RPCResultCacheMaxEntries = rpcResultCacheMaxEntries
	}
	if o.RPCResultCacheMaxBytes == 0 {
		o.RPCResultCacheMaxBytes = rpcResultCacheMaxBytes
	}
	if o.RPCResultCacheAuthMaxEntries == 0 {
		o.RPCResultCacheAuthMaxEntries = rpcResultCacheAuthMaxEntries
	}
	if o.RPCResultCacheAuthMaxBytes == 0 {
		o.RPCResultCacheAuthMaxBytes = rpcResultCacheAuthMaxBytes
	}
	if o.RPCResultCacheSessionMaxEntries == 0 {
		o.RPCResultCacheSessionMaxEntries = rpcResultCacheSessionMaxEntries
	}
	if o.RPCResultCacheSessionMaxBytes == 0 {
		o.RPCResultCacheSessionMaxBytes = rpcResultCacheSessionMaxBytes
	}
	if o.RPCResultPendingPerAuth == 0 {
		o.RPCResultPendingPerAuth = rpcResultFlightMaxPendingPerAuth
		if o.RPCResultPendingPerAuth > o.RPCGlobalMaxTasks {
			o.RPCResultPendingPerAuth = o.RPCGlobalMaxTasks
		}
	}
	if o.InboundFrameGlobalMaxBytes <= 0 {
		o.InboundFrameGlobalMaxBytes = defaultInboundFrameGlobalMaxBytes
	}
	if o.OutboundQueueSize <= 0 {
		o.OutboundQueueSize = defaultOutboundQueueSize
	}
	if o.OutboundControlQueueSize <= 0 {
		o.OutboundControlQueueSize = defaultOutboundControlQueueSize
	}
	if o.OutboundTrackedGlobalMaxBytes <= 0 {
		o.OutboundTrackedGlobalMaxBytes = defaultOutboundTrackedMaxBytes
	}
	if o.OutboundWriteGlobalMaxBytes <= 0 {
		o.OutboundWriteGlobalMaxBytes = defaultOutboundWriteMaxBytes
	}
	if o.DC == 0 {
		o.DC = 2
	}
	if o.AuthKeys == nil {
		o.AuthKeys = memory.NewAuthKeyStore()
	}
	if o.Metrics == nil {
		o.Metrics = NopMetrics{}
	}
	if o.Clock == nil {
		o.Clock = clock.System
	}
	if o.Rand == nil {
		o.Rand = crypto.DefaultRand()
	}
}

func validateRPCResultCacheOptions(o Options) error {
	if o.RPCResultCacheMaxEntries <= 0 || o.RPCResultCacheAuthMaxEntries <= 0 || o.RPCResultCacheSessionMaxEntries <= 0 {
		return fmt.Errorf("rpc_result cache entry limits must be positive")
	}
	if o.RPCResultCacheMaxEntries < o.RPCResultCacheAuthMaxEntries ||
		o.RPCResultCacheAuthMaxEntries < o.RPCResultCacheSessionMaxEntries {
		return fmt.Errorf("rpc_result cache entry hierarchy must satisfy global >= auth >= session: %d/%d/%d",
			o.RPCResultCacheMaxEntries, o.RPCResultCacheAuthMaxEntries, o.RPCResultCacheSessionMaxEntries)
	}
	if o.RPCResultCacheMaxBytes < int64(maxOutboundBodyBytes) ||
		o.RPCResultCacheAuthMaxBytes < int64(maxOutboundBodyBytes) ||
		o.RPCResultCacheSessionMaxBytes < int64(maxOutboundBodyBytes) {
		return fmt.Errorf("rpc_result cache byte limits must each be at least max outbound body %d: %d/%d/%d",
			maxOutboundBodyBytes, o.RPCResultCacheMaxBytes, o.RPCResultCacheAuthMaxBytes, o.RPCResultCacheSessionMaxBytes)
	}
	if o.RPCResultCacheMaxBytes < o.RPCResultCacheAuthMaxBytes ||
		o.RPCResultCacheAuthMaxBytes < o.RPCResultCacheSessionMaxBytes {
		return fmt.Errorf("rpc_result cache byte hierarchy must satisfy global >= auth >= session: %d/%d/%d",
			o.RPCResultCacheMaxBytes, o.RPCResultCacheAuthMaxBytes, o.RPCResultCacheSessionMaxBytes)
	}
	if o.RPCResultPendingPerAuth <= 0 || o.RPCResultPendingPerAuth > o.RPCGlobalMaxTasks ||
		o.RPCResultPendingPerAuth > o.RPCResultCacheAuthMaxEntries {
		return fmt.Errorf("rpc_result per-auth pending limit %d must be positive and <= global pending %d and auth entries %d",
			o.RPCResultPendingPerAuth, o.RPCGlobalMaxTasks, o.RPCResultCacheAuthMaxEntries)
	}
	return nil
}

// Server 是 MTProto 连接层（mtprotoedge）。
//
// 职责见 doc.go。它把原始 TCP 字节流转换为「已解密、已识别 session 的 RPC 请求」：
// 接受连接、协商 codec、完成密钥交换、解密并分发加密消息到 RPC 路由，处理服务消息，
// 并把活跃连接注册到 SessionManager 以支持主动推送（updates 等）。不含业务逻辑。
type Server struct {
	log                      *zap.Logger
	codec                    func() transport.Codec
	obfuscated               bool
	websocket                bool
	websocketOrigins         []string
	readTimeout              time.Duration
	handshakeTimeout         time.Duration
	handshakeMaxDur          time.Duration
	writeTimeout             time.Duration
	rpcInflight              int
	rpcQueueSize             int
	rpcTimeout               time.Duration
	rpcScheduler             *inboundRPCScheduler
	frameBudget              *inboundFrameBudget
	outboundQueueSize        int
	outboundControlQueueSize int
	outboundTrackedBudget    *outboundTrackedBudget
	outboundControlBudget    *outboundTrackedBudget
	outboundScratchPool      *outboundScratchPool

	dc        int
	strictDC  bool
	key       exchange.PrivateKey
	authKeys  store.AuthKeyStore
	conns     *SessionManager
	rpc       legacyRPCHandler
	layerRPC  LayerRPCHandler
	metrics   Metrics
	onServing func(net.Addr)
	cipher    crypto.Cipher
	clock     clock.Clock
	rand      io.Reader
	types     *tmap.Map
	admission *admissionController

	rpcResults *rpcResultCache
	rpcRewrap  *rpcRewrapRegistry

	// onFrame 是测试钩子：收到一帧时回调其字节数；生产为 nil。
	onFrame func(n int)
}

// New 创建 Server。
func New(opts Options) *Server {
	opts.setDefaults()
	if err := validateRPCResultCacheOptions(opts); err != nil {
		panic(fmt.Sprintf("mtprotoedge: invalid result-cache options: %v", err))
	}
	conns := opts.ActiveSessions
	if conns == nil {
		conns = NewSessionManager(opts.Logger.Named("sessions"))
	}
	return &Server{
		log:                      opts.Logger,
		codec:                    opts.Codec,
		obfuscated:               opts.ObfuscatedTCP,
		websocket:                opts.WebSocket,
		websocketOrigins:         append([]string(nil), opts.WebSocketAllowedOrigins...),
		readTimeout:              opts.ReadTimeout,
		handshakeTimeout:         opts.HandshakeIdleTimeout,
		handshakeMaxDur:          opts.HandshakeMaxDuration,
		writeTimeout:             opts.WriteTimeout,
		rpcInflight:              opts.RPCMaxInflight,
		rpcQueueSize:             opts.RPCQueueSize,
		rpcTimeout:               opts.RPCTimeout,
		rpcScheduler:             newInboundRPCScheduler(opts.RPCGlobalWorkers, opts.RPCGlobalMaxTasks, opts.RPCGlobalMaxBytes),
		frameBudget:              newInboundFrameBudget(opts.InboundFrameGlobalMaxBytes),
		outboundQueueSize:        opts.OutboundQueueSize,
		outboundControlQueueSize: opts.OutboundControlQueueSize,
		outboundTrackedBudget:    newOutboundTrackedBudget(opts.OutboundTrackedGlobalMaxBytes),
		outboundControlBudget:    newOutboundTrackedBudget(defaultOutboundControlMaxBytes),
		outboundScratchPool:      newOutboundScratchPool(opts.OutboundWriteGlobalMaxBytes),
		dc:                       opts.DC,
		strictDC:                 opts.StrictDC,
		key:                      exchange.PrivateKey{RSA: opts.RSAKey},
		authKeys:                 opts.AuthKeys,
		conns:                    conns,
		rpc:                      opts.legacyRPC,
		layerRPC:                 opts.LayerRPC,
		metrics:                  opts.Metrics,
		onServing:                opts.OnServing,
		cipher:                   crypto.NewServerCipher(opts.Rand),
		clock:                    opts.Clock,
		rand:                     opts.Rand,
		types:                    tmap.New(tg.TypesMap(), mt.TypesMap(), proto.TypesMap()),
		rpcResults: newRPCResultCacheWithFairCapacity(opts.Clock.Now, rpcResultCacheCapacity{
			maxPending:        opts.RPCGlobalMaxTasks,
			maxPendingPerAuth: opts.RPCResultPendingPerAuth,
			globalMaxBytes:    opts.RPCResultCacheMaxBytes,
			globalMaxEntries:  opts.RPCResultCacheMaxEntries,
			authMaxBytes:      opts.RPCResultCacheAuthMaxBytes,
			authMaxEntries:    opts.RPCResultCacheAuthMaxEntries,
			sessionMaxBytes:   opts.RPCResultCacheSessionMaxBytes,
			sessionMaxEntries: opts.RPCResultCacheSessionMaxEntries,
		}),
		rpcRewrap: newRPCRewrapRegistry(opts.RPCGlobalMaxTasks),
		admission: newAdmissionController(opts.MaxConnections, opts.MaxConnectionsPerIP, opts.MaxConcurrentHandshakes),
	}
}

// ListenAndServe binds the public MTProto socket and immediately enters Serve.
// Keeping listener ownership at the connection edge prevents callers from
// exposing a TCP port and then performing slow seed/cache initialization while
// clients are already completing handshakes into an unread accept backlog.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %q: %w", addr, err)
	}
	return s.Serve(ctx, ln)
}

// newConn 基于一次解密结果创建一个可发送的连接对象。
func (s *Server) newConn(tc transport.Conn, key crypto.AuthKey, sessionID, salt int64) *Conn {
	if lease, ok := tc.(*physicalTransportLease); ok {
		return s.newConnWithLease(lease, key, sessionID, salt)
	}
	if tc != nil {
		_, lease := newPhysicalTransportOwner(tc)
		return s.newConnWithLease(lease, key, sessionID, salt)
	}
	// Preserve the nil transport used by construction-only tests.
	return s.buildConn(nil, nil, key, sessionID, salt)
}

// newConnWithLease attaches a logical Conn to an explicitly owned physical
// transport generation. Production session replacement must Transfer the old
// lease first; it must never wrap the same raw transport in a second owner.
func (s *Server) newConnWithLease(lease *physicalTransportLease, key crypto.AuthKey, sessionID, salt int64) *Conn {
	if lease == nil {
		panic("mtprotoedge: nil physical transport lease")
	}
	c := s.buildConn(lease, lease, key, sessionID, salt)
	lease.bindLogicalConn(c)
	return c
}

func (s *Server) buildConn(tc transport.Conn, lease *physicalTransportLease, key crypto.AuthKey, sessionID, salt int64) *Conn {
	c := &Conn{
		transport:                    tc,
		transportLease:               lease,
		writer:                       tc,
		cipher:                       s.cipher,
		msgID:                        proto.NewMessageIDGen(s.clock.Now),
		writeTimeout:                 s.writeTimeout,
		metrics:                      s.metrics,
		now:                          s.clock.Now,
		authKeyID:                    key.ID,
		authKeyHex:                   hex.EncodeToString(key.ID[:]),
		sessionID:                    sessionID,
		salt:                         salt,
		key:                          key,
		createdAt:                    s.clock.Now(),
		outboundQueueSize:            s.outboundQueueSize,
		outboundControlQueueSize:     s.outboundControlQueueSize,
		outboundTrackedBudget:        s.outboundTrackedBudget,
		outboundControlTrackedBudget: s.outboundControlBudget,
		outboundScratchPool:          s.outboundScratchPool,
		rpcResultAcked:               s.rpcRewrap.acknowledge,
	}
	c.startOutbound()
	c.startInboundRPCScheduler(s.rpcScheduler, s.rpcInflight, s.rpcQueueSize, s.rpcTimeout)
	return c
}

// Serve 在 ln 上运行 MTProto 连接循环，直到 ctx 取消或发生不可恢复错误。
// ctx 取消时优雅退出：关闭 listener 并等待在途连接处理结束。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	// 共享 worker 池只在 Server 真正 Serve 后允许消费，并在首条 RPC 到达时懒启动。
	// serveTCP/serveMixed 返回前会等待连接 goroutine 收敛，各 Conn 已先排空/取消任务；
	// 最后再停止全局池，避免关闭过程中留下无人消费但仍占预算的队列。
	s.rpcScheduler.start()
	defer s.rpcScheduler.stop(rpcCloseWaitTimeout)
	// 只在最外层 listener 包一次，确保 same-port mux 的 sniff/HTTP upgrade 也计入
	// raw admission，而不是等连接已经分流后才计数。
	ln = s.observeRawAccepts(s.admission.wrapListener(ln))
	if s.websocket {
		return s.serveMixed(ctx, ln)
	}
	return s.serveTCP(ctx, ln)
}

func (s *Server) serveTCP(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.log.Info("Serving", zap.String("addr", ln.Addr().String()), zap.Int("dc", s.dc), zap.Bool("obfuscated_tcp", s.obfuscated))
	defer s.log.Info("Stopped")
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.acceptLoop(ctx, ln, s.obfuscated)
	}()
	s.signalServing(ln.Addr())
	return <-errCh
}

func (s *Server) serveMixed(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 嗅探(读首 4 字节做 HTTP/TCP 分流)的读超时必须对齐「建立 session 前」的读超时
	// s.handshakeTimeout(默认 60s)——与 serveDetectedConn/serveConn 的 pre-session 读
	// deadline 完全一致。一条尚未发出首帧的连接正处于「pre-session idle」状态：合法 MTProto
	// 客户端(如 DrKLO)会预开「暖」连接、在有请求前并不立即发送 obfuscated2 init。此前用
	// minDuration(5s,...) 把嗅探压到 5s，比非 mux 路径激进 12 倍，会把这些暖连接在 5s 误杀，
	// 触发客户端 6s 重连风暴并误判「后端不健康」回退到外部 DNS。per-conn goroutine 模型已消解
	// slow-loris 接入饥饿，故嗅探用满 handshakeTimeout 是安全的。
	mux := newSamePortMux(ln, s.handshakeTimeout, s.observeConnectionIntake)
	wsRawLn, wsHandler := transport.WebsocketListener(ln.Addr())
	wsLn := newTransportPacketMessageListener(wsRawLn)

	httpServer := &http.Server{
		Handler:           websocketRouteHandler(wsHandler, s.websocketOrigins),
		ReadHeaderTimeout: minDuration(10*time.Second, s.handshakeTimeout),
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	s.log.Info("Serving",
		zap.String("addr", ln.Addr().String()),
		zap.Int("dc", s.dc),
		zap.Bool("obfuscated_tcp", s.obfuscated),
		zap.Bool("websocket", true),
		zap.Strings("websocket_origins", s.websocketOrigins),
	)
	defer s.log.Info("Stopped")

	stopAll := func() {
		cancel()
		_ = mux.Close()
		_ = httpServer.Close()
		_ = wsLn.Close()
	}
	go func() {
		<-ctx.Done()
		stopAll()
	}()

	errCh := make(chan error, 4)
	var wg sync.WaitGroup
	wg.Add(4)
	// 分流器：窥探前 4 字节把 HTTP(WebSocket 升级) 与裸 MTProto TCP 拆开。
	go func() {
		defer wg.Done()
		errCh <- mux.Serve(ctx)
	}()
	// 裸 MTProto TCP：每条连接在自己的 goroutine 里完成去混淆 + codec 探测。
	go func() {
		defer wg.Done()
		errCh <- s.acceptLoop(ctx, mux.TCP(), s.obfuscated)
	}()
	// WebSocket：gotd 升级处理器已剥离 obfuscated2 并补回 codec tag，这里只需探测 codec。
	go func() {
		defer wg.Done()
		errCh <- s.acceptLoopTransport(ctx, wsLn, false, "websocket")
	}()
	go func() {
		defer wg.Done()
		if err := httpServer.Serve(mux.HTTP()); err != nil {
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				errCh <- nil
				return
			}
			errCh <- fmt.Errorf("websocket http serve: %w", err)
			return
		}
		errCh <- nil
	}()
	s.signalServing(ln.Addr())

	// The four services form one lifecycle: even a clean/closed-listener return from any one
	// component means the remaining three can no longer make forward progress as a complete
	// same-port server. Stop them immediately, then collect their terminal results.
	var firstErr error
	if err := <-errCh; err != nil {
		firstErr = err
	}
	stopAll()
	for i := 1; i < 4; i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	wg.Wait()
	return firstErr
}

// acceptLoop 接受裸连接，并为每条连接单独起 goroutine 完成「去混淆 + codec 探测 +
// serveConn」。探测在 accept 循环之外、带握手超时进行——慢/半开/坏 init 的客户端只占用
// 自己的 goroutine，绝不阻塞其他连接的接入；单条连接的握手失败也只关闭该连接，不会拖垮
// 整个监听循环。obfuscated 为 true 时先走 obfuscated2 去混淆（裸 MTProto TCP）；WebSocket
// 连接传 false（gotd 升级处理器已完成去混淆）。
func (s *Server) acceptLoop(ctx context.Context, ln net.Listener, obfuscated bool) error {
	return s.acceptLoopTransport(ctx, ln, obfuscated, intakeTransport(obfuscated))
}

func (s *Server) acceptLoopTransport(ctx context.Context, ln net.Listener, obfuscated bool, transportName string) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		// A permanent Accept error is itself a terminal lifecycle event. Cancel accepted
		// connections and close the listener before waiting; otherwise a live connection can
		// keep the WaitGroup blocked forever and prevent the accept error from being returned.
		cancel()
		_ = ln.Close()
		wg.Wait()
	}()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var tempDelay time.Duration
	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if isTemporaryAcceptError(err) {
				tempDelay = nextAcceptRetryDelay(tempDelay)
				s.log.Debug("Temporary accept error; retrying", zap.Duration("backoff", tempDelay), zap.Error(err))
				if !waitAcceptRetry(ctx, tempDelay) {
					return nil
				}
				continue
			}
			return fmt.Errorf("accept: %w", err)
		}
		tempDelay = 0

		wg.Add(1)
		go func() {
			defer wg.Done()
			s.observeConnectionIntake(connectionIntakeEvent{
				stage:     "transport_dispatch",
				outcome:   "accepted",
				transport: transportName,
				remote:    connRemote(raw),
				local:     connLocal(raw),
			})
			s.serveDetectedConn(ctx, raw, obfuscated, transportName)
		}()
	}
}

// serveDetectedConn 把一条裸连接提升为 transport.Conn（去混淆 + codec 探测）后运行 MTProto
// 连接循环。提升过程的读取放在本 goroutine、且受握手读超时约束，而非塞在 accept 循环里，
// 这样慢连接不会阻塞其他连接接入，去混淆/codec 握手本身也有时间上界。
func (s *Server) serveDetectedConn(ctx context.Context, raw net.Conn, obfuscated bool, transportName string) {
	started := time.Now()
	remote, local := connRemote(raw), connLocal(raw)
	// 握手读超时只覆盖去混淆 + codec 探测这一小段；用真实墙钟时间（SetReadDeadline 语义），
	// 不走可能被测试注入的逻辑 clock。
	if err := raw.SetReadDeadline(time.Now().Add(s.handshakeTimeout)); err != nil {
		_ = raw.Close()
		return
	}

	// 探测阶段若 ctx 取消，主动关闭 raw 解除阻塞读取——否则去混淆读会一直挂到握手超时，
	// 把半开连接拖进优雅退出的等待里。探测结束即停掉该 watcher，连接服务期由 serveConn
	// 自己的 ctx watcher 接管。
	promoted := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = raw.Close()
		case <-promoted:
		}
	}()

	conn, err := s.promoteConn(raw, obfuscated)
	close(promoted)
	if err != nil {
		outcome := "error"
		if isClientDisconnect(err) {
			outcome = "client_disconnect"
		}
		s.observeConnectionIntake(connectionIntakeEvent{
			stage:     "transport_promote",
			outcome:   outcome,
			transport: transportName,
			remote:    remote,
			local:     local,
			duration:  time.Since(started),
			err:       err,
		})
		_ = raw.Close()
		return
	}
	s.observeConnectionIntake(connectionIntakeEvent{
		stage:     "transport_promote",
		outcome:   "ready",
		transport: transportName,
		remote:    remote,
		local:     local,
		duration:  time.Since(started),
	})
	// 探测完成，撤掉握手读超时；后续每帧读写由 serveConn / 传输层各自管理超时。
	if err := raw.SetReadDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return
	}
	if err := s.serveConn(ctx, conn, remote, local); err != nil && !isClientDisconnect(err) {
		s.log.Info("Connection closed with error", zap.String("remote_addr", remote), zap.String("local_addr", local), zap.Error(err))
	}
}

// promoteConn 复用与 listener 组合完全一致的「obfuscated2 去混淆 + codec 探测」管线，但针对
// 单条连接，使其可在 accept 循环之外执行。obfuscated 对 WebSocket 连接必须为 false（gotd
// 升级处理器已剥离 obfuscated2 并补回 codec tag）。
func (s *Server) promoteConn(raw net.Conn, obfuscated bool) (transport.Conn, error) {
	var ln net.Listener = newSingleConnListener(raw)
	if obfuscated {
		ln = transport.ObfuscatedListener(ln)
	}
	return newCompatTransportListener(s.codec, ln, s.frameBudget).Accept()
}

// serveConn 处理单个传输连接：读帧并按 auth_key_id 分流。
//
//   - auth_key_id == 0：未加密的密钥交换起始消息，执行握手并落地 auth key。
//   - auth_key_id 已注册：加密消息，解密、注册连接并分发到 RPC 路由。
//   - auth_key_id 未注册：回 AuthKeyNotFound，促使客户端重新握手。
//
// 连接建立 session 后注册到 SessionManager，结束时注销。
func (s *Server) serveConn(ctx context.Context, raw transport.Conn, remote, local string) (err error) {
	transportOwner, conn := newPhysicalTransportOwner(raw)
	s.metrics.ConnOpened()
	s.log.Debug("MTProto connection loop started", zap.String("remote_addr", remote), zap.String("local_addr", local))

	var current *Conn
	firstFrameStarted := time.Now()
	firstFrameSeen := false
	defer func() {
		// A successful Recv transfers the frame reservation to serveConn. Release it only after
		// this stack has stopped using b/plain; transport.Close may have raced us earlier and must
		// not return that memory budget prematurely.
		releaseInboundFrameOwnership(conn)
		// Publish the terminal/RPC-cancel gates before index removal or lifecycle
		// observers. Physical close then releases a writer already inside Send; the
		// final Close only waits for the now-fenced actors to converge.
		if current != nil {
			current.beginTerminalShutdown()
		}
		_ = transportOwner.CloseAny()
		if current != nil {
			s.conns.Unregister(current)
			current.Close()
		}
		s.metrics.ConnClosed()
		s.log.Debug("Connection closed", zap.String("remote_addr", remote), zap.String("local_addr", local), zap.Error(err))
	}()

	// ctx 取消或处理结束时关闭连接，解除 Recv 阻塞。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = transportOwner.CloseAny()
	}()

	cs := newConnState()
	var b bin.Buffer
	// plain 是本连接的复用明文缓冲：decryptClientFrame 把每帧解密进它，免去
	// per-frame 整帧明文分配；帧内 slice 在下一帧读取前有效（RPC body 已在 dispatch 拷贝）。
	var plain bin.Buffer
	var replay *bin.Buffer
	for {
		if replay != nil {
			b.ResetTo(replay.Buf)
			replay = nil
		} else {
			// 建立 session 前（current==nil，握手 + 首个加密消息之前）用较短的 handshakeTimeout
			// 快速回收静默的半开 / 异常连接；建立 session 后用 readTimeout（客户端有 ping 心跳）。
			timeout := s.readTimeout
			if current == nil || !current.isActive() {
				timeout = s.handshakeTimeout
			}
			if err := s.recv(ctx, conn, &b, timeout); err != nil {
				if !firstFrameSeen {
					outcome := "error"
					if isClientDisconnect(err) {
						outcome = "client_disconnect"
					}
					s.observeConnectionIntake(connectionIntakeEvent{
						stage: "first_frame", outcome: outcome, remote: remote, local: local,
						duration: time.Since(firstFrameStarted), err: err,
					})
				}
				return err
			}
			if !firstFrameSeen {
				firstFrameSeen = true
				s.observeConnectionIntake(connectionIntakeEvent{
					stage: "first_frame", outcome: "ready", remote: remote, local: local,
					duration: time.Since(firstFrameStarted), bytes: b.Len(),
				})
			}
			if s.onFrame != nil {
				s.onFrame(b.Len())
			}
		}

		authKeyID, err := peekAuthKeyID(&b)
		if err != nil {
			return fmt.Errorf("peek auth key id: %w", err)
		}

		if authKeyID == emptyAuthKeyID {
			// A physical socket may perform key exchange only before it owns an
			// encrypted logical session. Mixing the direct exchange writer with an
			// active outbound actor would bypass generation/write serialization.
			if current != nil {
				return errors.New("unencrypted exchange on established encrypted connection")
			}
			releaseHandshake, admitted := s.admission.tryAcquireHandshake()
			if !admitted {
				if err := s.sendProtoError(ctx, conn, codec.CodeTransportFlood); err != nil {
					return err
				}
				return nil
			}
			next, err := s.handleExchange(ctx, conn, &b)
			releaseHandshake()
			if err != nil {
				return err
			}
			replay = next
			// Exchange has finished consuming the original transport frame. Drop its
			// potentially near-16MiB backing immediately. A replay frame is the gotd
			// encrypted-frame copy and keeps the existing frame reservation until it is
			// dispatched; a completed handshake has no surviving frame and can release now.
			trimOversizedInboundBuffer(&b)
			if replay == nil {
				releaseInboundFrameOwnership(conn)
			} else {
				retainInboundFrameBackings(conn, replay)
			}
			continue
		}

		// 已建立连接复用缓存密钥走快路径（fetchedKey=nil）：避开每帧回查 AuthKeyStore——
		// 这是 mtprotoedge 层最热的库访问点。密钥材料创建后不可变；销毁(destroy_auth_key)/
		// 撤销由 SessionManager 主动 Close 连接保证失效，不依赖被动的“下一帧 -404”。
		// 尚未进入 SessionManager 的 bad-salt provisional 会在 handleEncrypted 建立 activation claim
		// 后精确复查一次，既把撤销与激活线性化，也不把 salt storm 放大成 PG 写风暴。
		// temporary key 的绝对 expiry 缓存在 Conn 上，逐帧只做内存比较；到期必须在
		// RPC 前返回 -404，让官方客户端仅轮换 temp key。绝不能落到 Router 后退化为
		// raw business identity，再以会触发整账号退出的 401 结束。
		if current != nil && current.authKeyID == authKeyID && authKeyProtocolUnavailable(current.authKeyExpiresAt, s.clock.Now()) {
			s.log.Info("Rejecting unavailable temporary or legacy auth key",
				zap.String("auth_key_id", current.authKeyHex),
				zap.Int("expires_at", current.authKeyExpiresAt),
			)
			if err := s.sendTerminalProtoError(ctx, current, codec.CodeAuthKeyNotFound); err != nil {
				return err
			}
			return nil
		}
		var fetchedKey *store.AuthKeyData
		if current == nil || current.authKeyID != authKeyID {
			d, found, err := s.authKeys.Get(ctx, authKeyID)
			if err != nil {
				return fmt.Errorf("lookup auth key: %w", err)
			}
			if !found {
				var sendErr error
				if current != nil {
					sendErr = s.sendTerminalProtoError(ctx, current, codec.CodeAuthKeyNotFound)
				} else {
					sendErr = s.sendProtoError(ctx, conn, codec.CodeAuthKeyNotFound)
				}
				if sendErr != nil {
					return sendErr
				}
				// -404 对 TDesktop 是 terminal key failure；继续保留 socket 只会允许
				// 同一客户端反复触发 AuthKeyStore 查询。回包一次后立即断开。
				return nil
			}
			if authKeyProtocolUnavailable(d.ExpiresAt, s.clock.Now()) {
				s.log.Info("Rejecting unavailable temporary or legacy auth key",
					zap.String("auth_key_id", hex.EncodeToString(d.ID[:])),
					zap.Int("expires_at", d.ExpiresAt),
				)
				var sendErr error
				if current != nil {
					sendErr = s.sendTerminalProtoError(ctx, current, codec.CodeAuthKeyNotFound)
				} else {
					sendErr = s.sendProtoError(ctx, conn, codec.CodeAuthKeyNotFound)
				}
				if sendErr != nil {
					return sendErr
				}
				return nil
			}
			fetchedKey = &d
		}

		current, err = s.handleEncrypted(ctx, conn, cs, current, fetchedKey, &b, &plain)
		if errors.Is(err, errActivationAuthKeyRejected) {
			// handleEncrypted writes -404 while its activation claim still owns the
			// physical writer, then its deferred abort removes/closes the claim.
			return nil
		}
		if err != nil {
			return err
		}
		trimOversizedInboundBuffer(&b)
		trimOversizedInboundBuffer(&plain)
		retainInboundFrameBackings(conn, &b, &plain)
	}
}

func authKeyProtocolUnavailable(expiresAt int, now time.Time) bool {
	// -1 is migration 0086's explicit legacy-unknown sentinel. Reject it once
	// instead of guessing permanent and allowing account authorization on a temp key.
	return expiresAt < 0 || (expiresAt > 0 && int64(expiresAt) <= now.Unix())
}

// maxRetainedConnBuffer keeps normal upload/download frames allocation-free while preventing one
// exceptional near-16MiB transport frame from pinning that capacity for the lifetime of a long
// connection. RPC bodies that outlive dispatch already own a budgeted Copy.
const maxRetainedConnBuffer = 2 << 20

func trimOversizedInboundBuffer(b *bin.Buffer) {
	if b != nil && cap(b.Buf) > maxRetainedConnBuffer {
		b.Buf = nil
	}
}

// deadlineReceiver 是可选的直管读超时接口：telesrv-owned compat transport 实现它，
// 让每帧读只做一次 SetReadDeadline，不再分配 per-frame context timer。ctx 取消仍由
// serveConn 的 watcher 关闭底层连接来解除阻塞读（与 ctx deadline 路径行为一致）。
type deadlineReceiver interface {
	RecvDeadline(deadline time.Time, b *bin.Buffer) error
}

func (s *Server) recv(ctx context.Context, conn transport.Conn, b *bin.Buffer, timeout time.Duration) error {
	b.Reset()
	if dr, ok := conn.(deadlineReceiver); ok {
		return dr.RecvDeadline(time.Now().Add(timeout), b)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return conn.Recv(ctx, b)
}

// isClientDisconnect 判断错误是否为正常的客户端断开/服务关闭，不应作为异常记录。
func isClientDisconnect(err error) bool {
	switch {
	case errors.Is(err, io.EOF),
		errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return true
	}
	var nerr *net.OpError
	if errors.As(err, &nerr) && (nerr.Op == "read" || nerr.Op == "write") {
		return true
	}
	return false
}
