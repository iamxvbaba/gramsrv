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

// legacyRPCHandler routes a decrypted canonical RPC request body to a response.
//
// b is the plaintext RPC request (MTProto envelope already stripped); the returned
// bin.Encoder is wrapped into rpc_result. When *tgerr.Error is returned, the
// connection layer converts it to rpc_error and sends it back; other errors are
// treated as connection-level failures.
//
// It is retained only for regression tests of this package's old connection state
// machine. Production API RPCs must go through LayerRPCHandler, so that admission,
// the request/result TypeRef and the exact profile cannot be bypassed.
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
// execution-ledger/scheduler ownership is acquired.
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

// LayerRPCOptionsAdmitter extends the stable handler boundary with
// caller-owned admission capabilities. Implementations that do not expose it
// remain usable for requests that need only Limits.
type LayerRPCOptionsAdmitter interface {
	AdmitLayerWithOptions(profile tlprofile.Profile, b *bin.Buffer, options tlprofile.AdmissionOptions) (tlprofile.Admission, error)
	AdmitUnprofiledWithOptions(b *bin.Buffer, options tlprofile.AdmissionOptions) (tlprofile.Admission, error)
}

// LayerRPCDefaultProfileAdmitter decodes with a recoverable inherited/default
// profile. Production handlers should implement it with the same sparse
// tlprofile dispatcher and semantic adapter registry used by AdmitLayer. The split keeps old
// test doubles source-compatible while allowing invokeWithLayer to correct even
// a previously explicit Conn profile.
type LayerRPCDefaultProfileAdmitter interface {
	AdmitDefaultLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error)
}

// LayerRPCDefaultProfileOptionsAdmitter is the capability-aware form used when
// exact admission needs caller-owned resources such as bounded gzip expansion.
type LayerRPCDefaultProfileOptionsAdmitter interface {
	AdmitDefaultLayerWithOptions(profile tlprofile.Profile, b *bin.Buffer, options tlprofile.AdmissionOptions) (tlprofile.Admission, error)
}

// LayerRPCFlatBytesPayloadSizer is an optional, allocation-free admission
// capability for exact terminal requests whose generated object graph contains
// one already-bounded flat bytes payload. The handler may return ok only after
// proving the complete terminal wire shape and every semantic field cap it
// relies on. The edge still owns the multiplier, graph slack and all process /
// connection budgets; an absent or invalid hint falls back to the conservative
// generic graph charge.
type LayerRPCFlatBytesPayloadSizer interface {
	LayerRPCFlatBytesPayloadSize(wire []byte) (payloadBytes int, ok bool)
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
// an old retained request replay on a replacement physical connection from
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
// auth-key shared-default evidence. publishShared is true only when this call
// established a different durable profile generation which still owns the
// shared default; a same-generation msg_id high-water advance needs no second
// process-local default publication.
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
// replayed rpc_result reaches the replacement physical connection.
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
// joined/replayed requests never call this hook again.
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

// Options configures Server.
type Options struct {
	// Logger is the logger. Defaults to zap.NewNop().
	Logger *zap.Logger
	// Codec is the transport codec constructor. nil means auto-detect
	// (intermediate/abridged/full). A custom codec must be one of gotd's four
	// built-in codecs (optionally wrapping NoHeader), or implement
	// InboundFrameBudgetedCodec; a codec that cannot pre-check its length before
	// payload allocation fails closed.
	Codec func() transport.Codec
	// ObfuscatedTCP allows bare TCP to use MTProto transport obfuscation. When
	// enabled, the first 1/4/8 bytes of each physical connection are used to
	// automatically distinguish plaintext transport from 64-byte obfuscated2, then
	// the wire mode + codec are frozen onto the same bidirectional connection;
	// Telegram Desktop's tcpo_only and third-party clients without obfuscation can
	// share the same port.
	ObfuscatedTCP bool
	// WebSocket accepts MTProto over WebSocket (/apiws*) on the same listener.
	// When enabled, only the first 4 bytes are read at connection setup to split
	// HTTP/TCP; MTProto TCP then still follows the original TCP wire-mode + codec
	// detection path.
	WebSocket bool
	// WebSocketAllowedOrigins is the page origins allowed to initiate a WebSocket
	// upgrade from a browser. An empty list means only non-browser clients without
	// Origin are accepted; "*" allows all origins (debug only).
	WebSocketAllowedOrigins []string
	// SamePortHTTP handles non-/apiws* requests on the HTTP side of the same port
	// (for example the public server-info description, see internal/serverinfo).
	// The edge only routes and does not interpret the content; when nil these paths
	// all return 404. Reachable only when WebSocket is enabled (same-port splitting
	// in effect).
	SamePortHTTP http.Handler
	// ReadTimeout is the single-read timeout. Defaults to 5m.
	ReadTimeout time.Duration
	// HandshakeIdleTimeout is the read timeout before a connection "establishes a
	// session" (handshake + before the first encrypted message); it is shorter than
	// ReadTimeout, used to quickly reclaim half-open/aberrant connections that go
	// silent after the handshake. Defaults to 60s.
	HandshakeIdleTimeout time.Duration
	// HandshakeMaxDuration is the overall duration bound of a single key exchange
	// (serverExchange). HandshakeIdleTimeout only constrains a "single read idle"
	// and is useless against a client that keeps sending—if a client falls into a
	// "receive ResPQ→nonce desync→resend req_pq" handshake restart loop (see
	// docs/client-compat-notes.md), an unbounded serverExchange would blindly reply
	// ResPQ to every req_pq, spinning forever and flushing logs/burning CPU. This
	// bound sets an overall budget for the whole handshake, abandoning and closing
	// on timeout, and the client reconnecting with a fresh handshake (no residual
	// phase offset) recovers. A normal handshake is <1s. Defaults to 20s.
	HandshakeMaxDuration time.Duration
	// WriteTimeout is the single-write timeout. Defaults to 30s.
	WriteTimeout time.Duration
	// MaxConnections is the total cap on raw physical connections accepted by the
	// process, covering codec sniff, handshake and the full lifetime of
	// authenticated connections. Defaults to 200000; a negative value means no limit.
	MaxConnections int
	// MaxConnectionsPerIP is the raw physical connection cap per remote IP.
	// Defaults to 4096, leaving ample burst headroom for shared NAT and TDesktop
	// multi-candidate connections; a negative value means no limit.
	MaxConnectionsPerIP int
	// MaxConcurrentHandshakes is the cap on simultaneous auth_key_id=0 RSA/DH
	// exchanges. On reaching the cap, connections that have completed transport
	// framing receive -429 and are closed. Defaults to 256; a negative value means no limit.
	MaxConcurrentHandshakes int
	// RPCMaxInflight is the cap on RPCs processed simultaneously by one connection. Defaults to 32.
	RPCMaxInflight int
	// RPCQueueSize is the length of the per-connection queue of RPCs awaiting
	// processing. Defaults to 64; the queue is lazily allocated on the first request.
	RPCQueueSize int
	// RPCTimeout is the maximum processing duration of a single RPC at the
	// connection layer. Defaults to 30s. The timeout is counted from pre-Copy
	// budget/enqueue, including queue time.
	RPCTimeout time.Duration
	// RPCGlobalWorkers is the number of Server-shared inbound RPC workers. Defaults to 256.
	RPCGlobalWorkers int
	// RPCGlobalMaxTasks is the process-wide cap on reserved, queued and executing
	// RPCs. Defaults to 32768; request materialization is still separately hard
	// limited by RPCGlobalMaxBytes.
	RPCGlobalMaxTasks int
	// RPCGlobalMaxBytes is the process-level memory charge budget for the above
	// RPCs. The legacy charge equals the copied body; the exact charge is the
	// conservative materialization bound before typed decode, so this setting does
	// not mean 512 MiB of wire body can be received concurrently. Defaults to 512 MiB.
	RPCGlobalMaxBytes int64
	// RPCDeliveryHookWorkers bounds concurrent post-response correctness work
	// such as delivered-cursor commits and first-session readiness. The pending
	// limit separately covers reserved + queued + running hooks so a 10k startup
	// burst remains bounded without forcing socket writers to wait. Defaults are
	// 32 workers and 16,384 pending hooks.
	RPCDeliveryHookWorkers    int
	RPCDeliveryHookMaxPending int
	// RPCExecution*Entries bound in-flight owners and compact completed
	// receipts. Payload bytes are not charged here: the logical-session
	// outbox owns them under OutboundTrackedGlobalMaxBytes until ACK. ACK removes
	// the receipt immediately; 331 seconds is only the no-ACK safety horizon.
	RPCExecutionMaxEntries        int
	RPCExecutionAuthMaxEntries    int
	RPCExecutionSessionMaxEntries int
	// RPCExecutionPendingPerAuth is an additional active-owner bound, independent
	// from the retained entry limits and RPCGlobalMaxTasks. Default 2048.
	RPCExecutionPendingPerAuth int
	// InboundFrameGlobalMaxBytes is the total budget for the transport wire buffers
	// and the maximum decrypted plaintext buffer currently being processed across
	// all physical connections. Reserved after reading the length prefix and before
	// payload allocation; defaults to 512 MiB, and a non-positive value uses the default.
	InboundFrameGlobalMaxBytes int64
	// OutboundQueueSize / OutboundControlQueueSize are the per-connection ordinary
	// and control mailbox capacities. Default 128/32; the control queue keeps strict
	// priority in the actor.
	OutboundQueueSize        int
	OutboundControlQueueSize int
	// OutboundTrackedGlobalMaxBytes is the total budget for the RPC/update bodies
	// kept by all connections for msg_resend_req. Defaults to 512 MiB; encoded
	// MTProto service frames and control vectors use a separate 64 MiB control
	// budget (including new_session_created etc. that need resend tracking), so body
	// pressure does not block connection-maintenance messages. When a reliable
	// response cannot be tracked, that connection is terminated; a durable
	// best-effort update only drops the online acceleration and is recovered by difference.
	OutboundTrackedGlobalMaxBytes int64
	// OutboundCriticalGlobalMaxBytes is an independent retained-body reserve for
	// bootstrap/convergence RPC results. Bulk traffic cannot consume it.
	OutboundCriticalGlobalMaxBytes int64
	// OutboundWriteGlobalMaxBytes bounds concurrent encrypted wire/codec/obfuscation scratch.
	// Scratch is shared and pooled across connections; default 512 MiB.
	OutboundWriteGlobalMaxBytes int64

	// DC is this server's DC ID. Defaults to 2.
	DC int
	// StrictDC enables DC-label validation during key exchange. It is false by
	// default: this single physical backend accepts every wire int32 label for
	// permanent and temporary keys, and the label never changes auth-key
	// persistence, session identity, or business state. When enabled,
	// permanent labels must equal DC and temporary labels may equal +/-DC.
	// This diagnostic switch does not itself provide multi-DC isolation.
	StrictDC bool
	// RSAKey is the server RSA private key, used for the key exchange. When nil the handshake cannot complete.
	RSAKey *rsa.PrivateKey
	// AuthKeys persists auth keys. Defaults to the in-memory implementation.
	AuthKeys store.AuthKeyStore
	// ActiveSessions manages active connections. Defaults to a new one; passing one
	// lets the RPC layer share the same registry.
	ActiveSessions *SessionManager
	// legacyRPC is an internal test hook for the pre-exact RPC state machine.
	// It is deliberately unexported so production callers cannot bypass
	// generated Layer admission by configuring the canonical-only route.
	legacyRPC legacyRPCHandler
	// LayerRPC is the generated exact-profile production path. When configured,
	// every API request must complete admission before execution-ledger scheduling.
	LayerRPC LayerRPCHandler
	// Metrics receives connection-layer metrics. Defaults to NopMetrics.
	Metrics Metrics
	// OnServing is called after the connection intake loops have been installed.
	// It is a platform-neutral observation hook; all slow initialization must
	// finish before ListenAndServe is entered.
	OnServing func(net.Addr)
	// Clock is used for message IDs and timestamps. Defaults to clock.System.
	Clock clock.Clock
	// Rand is the random source. Defaults to crypto.DefaultRand().
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
	if o.RPCDeliveryHookWorkers <= 0 {
		o.RPCDeliveryHookWorkers = defaultRPCDeliveryHookWorkers
	}
	if o.RPCDeliveryHookMaxPending <= 0 {
		o.RPCDeliveryHookMaxPending = defaultRPCDeliveryHookMaxPending
	}
	if o.RPCExecutionMaxEntries == 0 {
		o.RPCExecutionMaxEntries = rpcExecutionMaxEntries
	}
	if o.RPCExecutionAuthMaxEntries == 0 {
		o.RPCExecutionAuthMaxEntries = rpcExecutionAuthMaxEntries
	}
	if o.RPCExecutionSessionMaxEntries == 0 {
		o.RPCExecutionSessionMaxEntries = rpcExecutionSessionMaxEntries
	}
	if o.RPCExecutionPendingPerAuth == 0 {
		o.RPCExecutionPendingPerAuth = rpcExecutionPendingPerAuth
		if o.RPCExecutionPendingPerAuth > o.RPCGlobalMaxTasks {
			o.RPCExecutionPendingPerAuth = o.RPCGlobalMaxTasks
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
	if o.OutboundCriticalGlobalMaxBytes <= 0 {
		o.OutboundCriticalGlobalMaxBytes = defaultOutboundCriticalMaxBytes
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

func validateRPCExecutionOptions(o Options) error {
	if o.RPCDeliveryHookWorkers <= 0 || o.RPCDeliveryHookMaxPending < o.RPCDeliveryHookWorkers {
		return fmt.Errorf("rpc delivery hook capacity must satisfy pending >= workers > 0: %d/%d",
			o.RPCDeliveryHookMaxPending, o.RPCDeliveryHookWorkers)
	}
	if o.RPCExecutionMaxEntries <= 0 || o.RPCExecutionAuthMaxEntries <= 0 || o.RPCExecutionSessionMaxEntries <= 0 {
		return fmt.Errorf("rpc execution ledger entry limits must be positive")
	}
	if o.RPCExecutionMaxEntries < o.RPCExecutionAuthMaxEntries ||
		o.RPCExecutionAuthMaxEntries < o.RPCExecutionSessionMaxEntries {
		return fmt.Errorf("rpc execution ledger entry hierarchy must satisfy global >= auth >= session: %d/%d/%d",
			o.RPCExecutionMaxEntries, o.RPCExecutionAuthMaxEntries, o.RPCExecutionSessionMaxEntries)
	}
	if o.RPCExecutionPendingPerAuth <= 0 || o.RPCExecutionPendingPerAuth > o.RPCGlobalMaxTasks ||
		o.RPCExecutionPendingPerAuth > o.RPCExecutionAuthMaxEntries {
		return fmt.Errorf("rpc execution per-auth pending limit %d must be positive and <= global pending %d and auth entries %d",
			o.RPCExecutionPendingPerAuth, o.RPCGlobalMaxTasks, o.RPCExecutionAuthMaxEntries)
	}
	return nil
}

// Server is the MTProto connection layer (mtprotoedge).
//
// See doc.go for its responsibilities. It turns the raw TCP byte stream into
// "decrypted RPC requests for an identified session": it accepts connections,
// negotiates the codec, completes the key exchange, decrypts encrypted messages
// and dispatches them to the RPC router, handles service messages, and registers
// active connections with SessionManager to support proactive pushes (updates
// etc.). It contains no business logic.
type Server struct {
	log                      *zap.Logger
	codec                    func() transport.Codec
	obfuscated               bool
	websocket                bool
	websocketOrigins         []string
	samePortHTTP             http.Handler
	readTimeout              time.Duration
	handshakeTimeout         time.Duration
	handshakeMaxDur          time.Duration
	writeTimeout             time.Duration
	rpcInflight              int
	rpcQueueSize             int
	rpcTimeout               time.Duration
	rpcScheduler             *inboundRPCScheduler
	bulkRPCScheduler         *bulkRPCScheduler
	rpcDeliveryHooks         *rpcDeliveryHookExecutor
	frameBudget              *inboundFrameBudget
	outboundQueueSize        int
	outboundControlQueueSize int
	outboundTrackedBudget    *outboundTrackedBudget
	outboundControlBudget    *outboundTrackedBudget
	outboundCriticalBudget   *outboundTrackedBudget
	outboundScratchPool      *outboundScratchPool
	outboundOpPool           *outboundOpPool
	outboundReplayBodyPool   *outboundReplayBodyPool

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

	rpcResults *rpcExecutionLedger
	rpcRewrap  *rpcRewrapRegistry

	// onFrame is a test hook: it is called back with a frame's byte count when one is received; nil in production.
	onFrame func(n int)
}

// New creates a Server.
func New(opts Options) *Server {
	opts.setDefaults()
	if err := validateRPCExecutionOptions(opts); err != nil {
		panic(fmt.Sprintf("mtprotoedge: invalid rpc execution options: %v", err))
	}
	conns := opts.ActiveSessions
	if conns == nil {
		conns = NewSessionManager(opts.Logger.Named("sessions"))
	}
	server := &Server{
		log:                      opts.Logger,
		codec:                    opts.Codec,
		obfuscated:               opts.ObfuscatedTCP,
		websocket:                opts.WebSocket,
		websocketOrigins:         append([]string(nil), opts.WebSocketAllowedOrigins...),
		samePortHTTP:             opts.SamePortHTTP,
		readTimeout:              opts.ReadTimeout,
		handshakeTimeout:         opts.HandshakeIdleTimeout,
		handshakeMaxDur:          opts.HandshakeMaxDuration,
		writeTimeout:             opts.WriteTimeout,
		rpcInflight:              opts.RPCMaxInflight,
		rpcQueueSize:             opts.RPCQueueSize,
		rpcTimeout:               opts.RPCTimeout,
		rpcScheduler:             newInboundRPCScheduler(opts.RPCGlobalWorkers, opts.RPCGlobalMaxTasks, opts.RPCGlobalMaxBytes),
		bulkRPCScheduler:         newBulkRPCScheduler(max(1, opts.RPCGlobalWorkers/2)),
		rpcDeliveryHooks:         newRPCDeliveryHookExecutor(opts.RPCDeliveryHookWorkers, opts.RPCDeliveryHookMaxPending),
		frameBudget:              newInboundFrameBudget(opts.InboundFrameGlobalMaxBytes),
		outboundQueueSize:        opts.OutboundQueueSize,
		outboundControlQueueSize: opts.OutboundControlQueueSize,
		outboundTrackedBudget:    newOutboundTrackedBudget(opts.OutboundTrackedGlobalMaxBytes),
		outboundControlBudget:    newOutboundTrackedBudget(defaultOutboundControlMaxBytes),
		outboundCriticalBudget:   newOutboundTrackedBudget(opts.OutboundCriticalGlobalMaxBytes),
		outboundScratchPool:      newOutboundScratchPool(opts.OutboundWriteGlobalMaxBytes),
		outboundOpPool:           newOutboundOpPool(defaultOutboundOpPoolSize),
		outboundReplayBodyPool:   newOutboundReplayBodyPool(defaultOutboundReplayBodyClasses),
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
		rpcResults: newRPCExecutionLedger(opts.Clock.Now, rpcExecutionLedgerCapacity{
			maxPending:        opts.RPCGlobalMaxTasks,
			maxPendingPerAuth: opts.RPCExecutionPendingPerAuth,
			globalMaxEntries:  opts.RPCExecutionMaxEntries,
			authMaxEntries:    opts.RPCExecutionAuthMaxEntries,
			sessionMaxEntries: opts.RPCExecutionSessionMaxEntries,
			replayStore:       conns,
		}),
		rpcRewrap: newRPCRewrapRegistry(opts.RPCGlobalMaxTasks),
		admission: newAdmissionController(opts.MaxConnections, opts.MaxConnectionsPerIP, opts.MaxConcurrentHandshakes),
	}
	conns.setLogicalSessionReleaseHook(func(key sessionKey) {
		server.rpcResults.forgetSession(key.authKeyID, key.sessionID)
	})
	return server
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

// newConn creates a sendable connection object based on one decryption result.
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
		transport:                     tc,
		transportLease:                lease,
		writer:                        tc,
		cipher:                        s.cipher,
		msgID:                         proto.NewMessageIDGen(s.clock.Now),
		writeTimeout:                  s.writeTimeout,
		metrics:                       s.metrics,
		now:                           s.clock.Now,
		authKeyID:                     key.ID,
		authKeyHex:                    hex.EncodeToString(key.ID[:]),
		sessionID:                     sessionID,
		salt:                          salt,
		key:                           key,
		createdAt:                     s.clock.Now(),
		outboundQueueSize:             s.outboundQueueSize,
		outboundControlQueueSize:      s.outboundControlQueueSize,
		outboundTrackedBudget:         s.outboundTrackedBudget,
		outboundControlTrackedBudget:  s.outboundControlBudget,
		outboundCriticalTrackedBudget: s.outboundCriticalBudget,
		outboundScratchPool:           s.outboundScratchPool,
		outboundOpPool:                s.outboundOpPool,
		outboundReplayBodyPool:        s.outboundReplayBodyPool,
		rpcDeliveryHooks:              s.rpcDeliveryHooks,
		rpcResultAcked: func(conn *Conn, reqMsgID int64) {
			// The sole outbound actor invokes this only after resolving a client
			// msgs_ack server msg_id through its tracked resend frame. The actor has
			// already removed the sole outbox frame; now delete its receipt and
			// retire any init-rewrap bookkeeping.
			s.rpcResults.Acknowledge(conn.authKeyID, conn.sessionID, reqMsgID)
			s.rpcRewrap.acknowledge(conn, reqMsgID)
		},
	}
	s.conns.attachLogicalSession(c, s.outboundTrackedBudget)
	c.startOutbound()
	c.startInboundRPCScheduler(s.rpcScheduler, s.rpcInflight, s.rpcQueueSize, s.rpcTimeout)
	return c
}

// Serve runs the MTProto connection loop on ln until ctx is canceled or an
// unrecoverable error occurs. On ctx cancellation it exits gracefully: it closes
// the listener and waits for in-flight connection handling to finish.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	// The shared worker pool may consume only after the Server actually Serves, and
	// starts lazily when the first RPC arrives. Before serveTCP/serveMixed return
	// they wait for connection goroutines to converge, and each Conn has already
	// drained/canceled its tasks; only then is the shared pool stopped and the
	// reserved delivery hooks drained, avoiding leftover queues that no one consumes
	// but that still occupy budget during shutdown.
	s.rpcScheduler.start()
	defer s.rpcDeliveryHooks.stop(rpcCloseWaitTimeout)
	defer s.conns.releaseAllLogicalSessions()
	defer s.rpcScheduler.stop(rpcCloseWaitTimeout)
	defer s.bulkRPCScheduler.close()
	// Wrap only once at the outermost listener, ensuring the same-port mux's
	// sniff/HTTP upgrade is also counted in raw admission, rather than counting only
	// after the connection has been split.
	ln = s.observeRawAccepts(s.admission.wrapListener(ln))
	if s.websocket {
		return s.serveMixed(ctx, ln)
	}
	return s.serveTCP(ctx, ln)
}

func (s *Server) serveTCP(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	s.log.Info("Serving",
		zap.String("addr", ln.Addr().String()),
		zap.Int("dc", s.dc),
		zap.String("tcp_transport_mode", intakeTransport(s.obfuscated)),
	)
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

	// The sniff read timeout (reading the first 4 bytes for HTTP/TCP splitting) must
	// align with the "before establishing a session" read timeout
	// s.handshakeTimeout (default 60s)—exactly matching serveDetectedConn/serveConn's
	// pre-session read deadline. A connection that has not yet sent its first frame
	// is in the "pre-session idle" state: legitimate MTProto clients (such as DrKLO)
	// pre-open "warm" connections and do not immediately send the obfuscated2 init
	// before there is a request. Previously minDuration(5s,...) forced the sniff down
	// to 5s, 12x more aggressive than the non-mux path, which wrongly killed these
	// warm connections at 5s and triggered a client 6s reconnect storm and a wrong
	// "backend unhealthy" fallback to external DNS. The per-conn goroutine model has
	// eliminated slow-loris intake starvation, so using the full handshakeTimeout for
	// the sniff is safe.
	mux := newSamePortMux(ln, s.handshakeTimeout, s.observeConnectionIntake)
	wsRawLn, wsHandler := transport.WebsocketListener(ln.Addr())
	wsLn := newTransportPacketMessageListener(wsRawLn)

	httpServer := &http.Server{
		Handler:           websocketRouteHandler(wsHandler, s.websocketOrigins, s.samePortHTTP),
		ReadHeaderTimeout: minDuration(10*time.Second, s.handshakeTimeout),
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	s.log.Info("Serving",
		zap.String("addr", ln.Addr().String()),
		zap.Int("dc", s.dc),
		zap.String("tcp_transport_mode", intakeTransport(s.obfuscated)),
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
	// Splitter: sniff the first 4 bytes to separate HTTP (WebSocket upgrade) from bare MTProto TCP.
	go func() {
		defer wg.Done()
		errCh <- mux.Serve(ctx)
	}()
	// Bare MTProto TCP: each connection completes wire mode + codec detection in its own goroutine.
	go func() {
		defer wg.Done()
		errCh <- s.acceptLoop(ctx, mux.TCP(), s.obfuscated)
	}()
	// WebSocket: the gotd upgrade handler has already stripped obfuscated2 and restored the codec tag, so here only the codec needs detecting.
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

// acceptLoop accepts bare connections and starts a separate goroutine per
// connection to complete "wire mode + codec detection + serveConn". Detection runs
// outside the accept loop under the handshake timeout—a slow/half-open/bad-init
// client only occupies its own goroutine and never blocks the intake of other
// connections; a single connection's handshake failure only closes that
// connection and does not bring down the whole listen loop. When obfuscated is
// true it automatically distinguishes plain from obfuscated2 (bare MTProto TCP);
// WebSocket connections pass false (the gotd upgrade handler has already
// de-obfuscated).
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

// serveDetectedConn promotes a bare connection to a transport.Conn (de-obfuscation
// + codec detection) and then runs the MTProto connection loop. The reads during
// promotion happen on this goroutine and are bounded by the handshake read
// timeout, rather than being stuffed into the accept loop, so a slow connection
// does not block other connections' intake and the de-obfuscation/codec handshake
// itself has a time bound.
func (s *Server) serveDetectedConn(ctx context.Context, raw net.Conn, obfuscated bool, transportName string) {
	started := time.Now()
	remote, local := connRemote(raw), connLocal(raw)
	// The handshake read timeout only covers the small wire-mode + codec detection
	// stretch; it uses real wall-clock time (SetReadDeadline semantics) and does not
	// go through a logical clock that tests may inject.
	if err := raw.SetReadDeadline(time.Now().Add(s.handshakeTimeout)); err != nil {
		_ = raw.Close()
		return
	}

	// If ctx is canceled during detection, actively close raw to unblock the read—otherwise
	// the de-obfuscation read would hang until the handshake timeout, dragging the
	// half-open connection into the graceful-shutdown wait. Detection end stops this
	// watcher, and the serving phase is taken over by serveConn's own ctx watcher.
	promoted := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = raw.Close()
		case <-promoted:
		}
	}()

	conn, detectedTransport, err := s.promoteConn(raw, obfuscated)
	if detectedTransport != "" {
		transportName = detectedTransport
	}
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
	// Detection is complete, so the handshake read timeout is removed; per-frame
	// reads and writes are then subject to the timeouts managed by serveConn / the transport layer.
	if err := raw.SetReadDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return
	}
	if err := s.serveConn(ctx, conn, remote, local); err != nil && !isClientDisconnect(err) {
		s.log.Info("Connection closed with error", zap.String("remote_addr", remote), zap.String("local_addr", local), zap.Error(err))
	}
}

// promoteConn performs one transport promotion for a single connection.
// obfuscated=true means bare TCP allows obfuscation and automatically distinguishes
// plain/obfuscated2; WebSocket must pass false because the gotd upgrade handler has
// already stripped obfuscated2 and restored the codec tag.
func (s *Server) promoteConn(raw net.Conn, obfuscated bool) (transport.Conn, string, error) {
	if obfuscated {
		return s.promoteMixedTCP(raw)
	}
	conn, err := newCompatTransportConn(s.codec, raw, s.frameBudget)
	return conn, "", err
}

// serveConn handles a single transport connection: it reads frames and splits by auth_key_id.
//
//   - auth_key_id == 0: an unencrypted key-exchange start message, performing the handshake and persisting the auth key.
//   - auth_key_id registered: an encrypted message, decrypted, registering the connection and dispatching to the RPC router.
//   - auth_key_id not registered: reply AuthKeyNotFound, prompting the client to redo the handshake.
//
// The connection is registered with SessionManager after establishing a session and
// unregistered when it ends.
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

	// Close the connection when ctx is canceled or handling ends, unblocking Recv.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = transportOwner.CloseAny()
	}()

	cs := newConnState()
	var b bin.Buffer
	// plain is this connection's reused plaintext buffer: decryptClientFrame
	// decrypts each frame into it, avoiding per-frame whole-plaintext allocation;
	// the in-frame slices are valid until the next frame is read (the RPC body is
	// already copied in dispatch).
	var plain bin.Buffer
	var replay *bin.Buffer
	for {
		if replay != nil {
			b.ResetTo(replay.Buf)
			replay = nil
		} else {
			// Before a session is established (current==nil, before the handshake +
			// first encrypted message) use the shorter handshakeTimeout to quickly
			// reclaim silent half-open/aberrant connections; after a session is
			// established use readTimeout (the client has a ping heartbeat).
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

		// An established connection reuses the cached key on the fast path
		// (fetchedKey=nil): avoiding a per-frame AuthKeyStore lookup—the hottest
		// library access point in the mtprotoedge layer. Key material is immutable
		// after creation; destruction (destroy_auth_key)/revocation is invalidated by
		// SessionManager actively Closing the connection, not relying on the passive
		// "next frame -404". A bad-salt provisional not yet in SessionManager is
		// precisely rechecked once after handleEncrypted establishes the activation
		// claim, which linearizes revocation with activation and also does not amplify
		// a salt storm into a PG write storm. A temporary key's absolute expiry is
		// cached on the Conn and only needs an in-memory comparison per frame; on
		// expiry it must return -404 before RPC, letting official clients rotate only
		// the temp key. It must never fall through to the Router and degrade to a raw
		// business identity, ending with a 401 that triggers a whole-account logout.
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
				// -404 is a terminal key failure for TDesktop; keeping the socket would
				// only let the same client repeatedly trigger AuthKeyStore lookups.
				// Reply once and then disconnect immediately.
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

		current, err = s.handleEncrypted(ctx, conn, cs, current, remote, fetchedKey, &b, &plain)
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

// deadlineReceiver is an optional direct read-timeout interface: the telesrv-owned
// compat transport implements it so each frame read only does one SetReadDeadline
// and no longer allocates a per-frame context timer. ctx cancellation is still
// handled by serveConn's watcher closing the underlying connection to unblock the
// read (matching the ctx-deadline path's behavior).
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

// isClientDisconnect determines whether an error is a normal client disconnect /
// service shutdown and should not be logged as an anomaly.
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
