package mtprotoedge

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tlprofile"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/edgecontrol"
)

// Conn is a client connection for an identified session, holding all the
// context needed to send it encrypted messages. It is managed by SessionManager
// and shared by request responses and proactive pushes.
//
// Send is concurrency-safe: all outbound messages first enter the per-Conn
// outbound actor, which serially assigns msg_id/seq_no, encrypts and writes to
// the transport, avoiding MTProto ordering errors from interleaved concurrent
// RPC responses and pushes.
type outboundWriter interface {
	Send(context.Context, *bin.Buffer) error
}

type connLifecycle uint32

const (
	// The zero value is deliberately provisional so test/embedded Conn values start
	// outside every SessionManager index until they complete activation.
	connLifecycleProvisional connLifecycle = iota
	connLifecycleClaiming
	connLifecycleActive
	// retired is terminal and irreversible. A physical connection that lost an
	// activation claim must never become visible again, even if its read goroutine
	// was already between preflight and publish when a replacement arrived.
	connLifecycleRetired
)

var (
	// ErrLayerProfileUnsupported means the requested profile has no generated
	// exact codec. Profiles are never clamped to the nearest supported layer.
	ErrLayerProfileUnsupported = errors.New("unsupported exact layer profile")
	// ErrLayerProfileConflict means one admitted request carried contradictory
	// profile evidence. A later, well-formed invokeWithLayer is allowed to correct
	// the connection profile and therefore does not use this error.
	ErrLayerProfileConflict = errors.New("connection layer profile conflict")
	// ErrLayerProfileEpochExhausted is a defensive terminal guard. Reaching it
	// would require more than four billion effective layer corrections on one
	// physical connection, so wrapping the epoch and making stale pushes current
	// again is never safe.
	ErrLayerProfileEpochExhausted = errors.New("connection layer profile epoch exhausted")
)

type Conn struct {
	transport transport.Conn
	// transportLease owns exactly one generation of the physical transport.
	// It is nil only for directly constructed test/embedded Conns that retain
	// the legacy raw-transport close fallback.
	transportLease *physicalTransportLease
	writer         outboundWriter
	cipher         crypto.Cipher
	msgID          *proto.MessageIDGen
	writeTimeout   time.Duration
	metrics        Metrics
	// now shares the Server protocol clock with inbound expiry admission. Tests may
	// advance it without sleeping; construction-only Conns fall back to time.Now.
	now func() time.Time

	authKeyID [8]byte
	// authKeyHex is the hex cache of authKeyID: every RPC's structured log
	// carries it, so it is computed once at connection setup to avoid repeated
	// hex encoding allocations on the hot path.
	authKeyHex string
	// authKeyExpiresAt=0 means a permanent key; a positive value is the absolute
	// protocol expiry time determined at handshake for a temporary/media-temporary
	// key; -1 is the legacy-unknown sentinel used only for migration (the edge
	// rejects it with -404 before creating the Conn). Immutable after Conn creation.
	authKeyExpiresAt int
	sessionID        int64
	salt             int64
	key              crypto.AuthKey

	outbound        chan *outboundOp
	outboundControl chan *outboundOp
	// Critical RPC results (session/difference convergence) and large bulk
	// responses have independent bounded lanes. The actor remains the sole writer.
	outboundCritical chan *outboundOp
	outboundBulk     chan *outboundOp
	outboundStop     chan struct{}
	outboundDone     chan struct{}
	outboundClose    sync.Once
	// outboundEnqueueMu orders producer registration against terminal close. Close
	// flips closing under this lock before waiting, so no WaitGroup Add can race Wait.
	outboundEnqueueMu sync.Mutex
	outboundEnqueueWG sync.WaitGroup
	outboundClosing   bool
	// Queue backing is intentionally small and bounded per Conn; control has a separate queue
	// and strict actor priority. Server-created connections share outboundTrackedBudget.
	outboundQueueSize        int
	outboundControlQueueSize int
	outboundTrackedBudget    *outboundTrackedBudget
	outboundBudgetOnce       sync.Once
	// Encoded MTProto service frames and control vectors use independent headroom: pong,
	// new_session_created, bad_msg and msgs_ack must remain admissible when the body budget is
	// full. Content-related control frames keep this budget while pending for resend.
	outboundControlTrackedBudget  *outboundTrackedBudget
	outboundControlBudgetOnce     sync.Once
	outboundCriticalTrackedBudget *outboundTrackedBudget
	outboundCriticalBudgetOnce    sync.Once
	outboundScratchPool           *outboundScratchPool
	outboundScratchOnce           sync.Once
	outboundOpPool                *outboundOpPool
	outboundReplayBodyPool        *outboundReplayBodyPool
	// outboundState outlives this physical Conn generation. A replacement
	// physical connection for the same auth key/session reuses it.
	outboundState *outboundState
	// lifecycle is the sole monotonic activation/retirement state machine.
	// retired never transitions back to claiming/active; one atomic state avoids
	// contradictory activation and shutdown observations.
	lifecycle      atomic.Uint32
	transportClose sync.Once

	rpcScheduler *inboundRPCScheduler
	// rpcDeliveryHooks belongs to the owning Server. Directly constructed test
	// Conns leave it nil and use the package test fallback.
	rpcDeliveryHooks *rpcDeliveryHookExecutor
	rpcCancel        context.CancelFunc
	rpcClose         sync.Once
	rpcMu            sync.Mutex
	rpcWG            sync.WaitGroup
	// rpcReservationWG tracks the short window from pre-Copy budget to
	// commit/abort, so that when Close returns the global/per-connection budget
	// has either been returned or handed off to an explicit queued/running task.
	rpcReservationWG sync.WaitGroup
	rpcTimeout       time.Duration
	rpcQueue         []inboundRPC
	rpcQueueSize     int
	rpcReserved      int
	rpcRunning       int
	rpcReady         bool
	rpcClosed        bool
	// rpcReplayRestores is a per-physical-connection ordering barrier. An exact
	// replayed/rewrapped init request has already executed its business handler,
	// but its wrapper/client/readiness state becomes authoritative only after the
	// replacement rpc_result is physically written. Queued naked RPCs remain
	// admitted and budgeted, but are not scheduler-runnable until every such
	// restore finishes or the connection is fenced.
	rpcReplayRestores int
	// Rewrap aliasing never delays execution. initialized stops collecting
	// candidates after the first valid init wrapper on this physical generation.
	rpcRewrapInitialized atomic.Bool
	// layerRPCAdmissionTraceLogged bounds production INFO diagnostics to the
	// first non-unknown exact-admission rejection on this physical generation.
	// Repeated malformed requests remain visible at Debug without turning an
	// authenticated reconnect/session into an unbounded INFO log source.
	layerRPCAdmissionTraceLogged atomic.Bool
	// rpcResultAcked is invoked by the sole outbound actor after it resolves an
	// acknowledged server frame back to the rpc_result request msg_id.
	rpcResultAcked func(*Conn, int64)
	// deliveryClientAcked is invoked by the sole outbound actor after a client
	// msgs_ack resolves to a v3 durable delivery frame.
	deliveryClientAcked func(*Conn, edgecontrol.DeliveryTracking, int64)
	// inflightRPCBytes tracks the memory charge of reserved/queued/executing
	// inbound RPCs; legacy equals the copied body, exact is the conservative
	// inflated value of typed materialization. Together with
	// maxInflightRPCBytes it puts a memory budget on the RPC queue (not just a count).
	inflightRPCBytes atomic.Int64
	// A single connection only keeps a concurrency quota; the actual worker comes
	// from the Server's shared pool, avoiding a reserved goroutine per connection.
	rpcRootCtx     context.Context
	rpcMaxInflight int
	// remoteAddr is the normalized IP literal of the MTProto connection's peer (from
	// net.Conn.RemoteAddr, with port and zone stripped and IPv4-mapped addresses restored to IPv4);
	// it is written to authorizations.ip when a device authorization is bound, so the admin panel
	// shows the login IP. Non-IP peers (such as a unix socket) are not recorded.
	remoteAddr string

	// sentContentMessages is retained only for standalone construction tests.
	// Server connections allocate seq_no from logical-session outboundState.
	sentContentMessages int32
	// outboundRand is accessed only by the outbound actor: a buffered pre-read of
	// the cipher's random source, amortizing the getrandom syscall for each
	// frame's padding down to about one per ~1KiB.
	outboundRand *bufio.Reader

	identityMu              sync.RWMutex
	businessAuthKeyID       [8]byte
	businessAuthKeyHex      string
	businessAuthKeyResolved bool
	userID                  atomic.Int64
	userIDResolved          atomic.Bool
	// deliveryIdentityGeneration fences durable snapshots across an in-place
	// logout/login or business-auth-key rebind. The Conn pointer already names a
	// physical generation; this counter names the account identity generation
	// within that physical connection.
	deliveryIdentityGeneration atomic.Uint64
	receivesUpdates            atomic.Bool
	channelWatermarkMu         sync.Mutex
	channelPayloadPts          map[int64]int
	channelNudgePts            map[int64]int
	// membershipsSynced indicates that this connection's channel membership push
	// route (byMemberChannel) has been established successfully. Together with
	// receivesUpdates it forms "session fully ready": on membership sync failure
	// it stays false, letting the flag short-circuit admission and the next RPC
	// retry the sync, avoiding a session that is flagged ready but lacks channel
	// routes silently missing supergroup pushes.
	membershipsSynced atomic.Bool
	// updatesActivationToken/At are protected by SessionManager.mu. They make
	// readiness activation single-flight per physical connection generation;
	// an old delivery callback can only release the exact token it acquired.
	updatesActivationToken uint64
	updatesActivationAt    time.Time
	// bootstrapProbeToken/At/bootstrapProbed are protected by SessionManager.mu.
	// A delivered updates baseline performs the durable bootstrap-job probe once
	// per physical connection generation. A failed callback releases the token;
	// a replacement Conn starts with a fresh zero value and probes again.
	bootstrapProbeToken uint64
	bootstrapProbeAt    time.Time
	bootstrapProbed     bool
	// membershipGen is the revision number of this connection's channel membership index: every
	// incremental revision (Add/Remove for join/leave/kick, a full clear on identity switch/logout)
	// increments it. A full sync samples it before reading the persisted member list and compares it
	// when applying, detecting the lost-update race of "an incremental revision happened inside the
	// read window" (a paged membership commit then takes the merge path and keeps retrying until ready).
	membershipGen atomic.Int64
	// createdAt is the connection setup time, used to evict the genuinely oldest
	// connection when the session count for the same auth_key hits its cap.
	createdAt time.Time
	// layerProfileState atomically packs profile, provenance and epoch. A profile
	// inherited from auth-key metadata is only a default; a later well-formed
	// invokeWithLayer may correct it. The epoch fences proactive updates prepared
	// before that correction without invalidating request-bound RPC results.
	layerProfileMu sync.RWMutex
	// layerProfileEvidenceMsgID is protected by layerProfileMu. Zero means the
	// selected profile came from inherited/legacy recovery and therefore has no
	// ordered client-message cursor yet. Positive values are the newest accepted
	// invokeWithLayer message for this exact MTProto session.
	layerProfileEvidenceMsgID int64
	// layerProfileEvidenceLayer retains the raw negotiated Layer even when this
	// binary has no generated codec for it. In that case the packed profile stays
	// Unknown, but msg_id ordering can still admit a newer supported correction
	// and reject an older/same-id rollback.
	layerProfileEvidenceLayer int
	layerProfileState         atomic.Uint64
	// clientLayer is the package-internal mirror used only by legacy state-machine
	// regression tests. Production application RPC/result/update encoding uses
	// the structured profile state. Zero is unknown and must fail closed if a legacy
	// application value reaches an outbound boundary.
	clientLayer atomic.Int32
}

func (c *Conn) lifecycleState() connLifecycle {
	if c == nil {
		return connLifecycleRetired
	}
	return connLifecycle(c.lifecycle.Load())
}

func (c *Conn) isRetired() bool {
	return c == nil || c.lifecycleState() == connLifecycleRetired
}

// retire irreversibly fences the logical connection. The caller that wins the
// transition may additionally own one-shot physical cleanup.
func (c *Conn) retire() bool {
	if c == nil {
		return false
	}
	for {
		state := c.lifecycle.Load()
		if connLifecycle(state) == connLifecycleRetired {
			return false
		}
		if c.lifecycle.CompareAndSwap(state, uint32(connLifecycleRetired)) {
			return true
		}
	}
}

func (c *Conn) beginActivationClaim() bool {
	if c == nil || !c.isPhysicalTransportCurrentOpen() {
		return false
	}
	if !c.lifecycle.CompareAndSwap(uint32(connLifecycleProvisional), uint32(connLifecycleClaiming)) {
		return false
	}
	// Physical close can win after the pre-check but before the lifecycle CAS.
	// Do not let a doomed claimant enter SessionManager and retire a healthy old
	// owner for the same logical session.
	if c.lifecycleState() != connLifecycleClaiming || !c.isPhysicalTransportCurrentOpen() {
		c.retire()
		return false
	}
	return true
}

func (c *Conn) publishActivation() bool {
	if c == nil || !c.isPhysicalTransportCurrentOpen() {
		return false
	}
	if !c.lifecycle.CompareAndSwap(uint32(connLifecycleClaiming), uint32(connLifecycleActive)) {
		return false
	}
	// A concurrent transport failure can retire the Conn between the first
	// physical check and the CAS. Never let that intermediate active value escape.
	if c.lifecycleState() != connLifecycleActive || !c.isPhysicalTransportCurrentOpen() {
		c.retire()
		return false
	}
	return true
}

func (c *Conn) isActive() bool {
	return c != nil && c.lifecycleState() == connLifecycleActive && c.isPhysicalTransportCurrentOpen()
}

// transferTransportOwnership hands this Conn's physical socket to the next
// logical generation. The caller must have fenced and drained the old writer.
func (c *Conn) transferTransportOwnership() (*physicalTransportLease, bool) {
	if c == nil || c.transportLease == nil {
		return nil, false
	}
	return c.transportLease.Transfer()
}

func (c *Conn) isPhysicalTransportCurrentOpen() bool {
	return c != nil && (c.transportLease == nil || c.transportLease.IsCurrentOpen())
}

// LayerProfile returns the exact TL profile currently selected for this
// connection. ok is false until admission or an inherited auth-key default
// supplies a supported generated profile.
func (c *Conn) LayerProfile() (profile tlprofile.Profile, ok bool) {
	state := c.LayerProfileState()
	return state.Profile, state.Origin != LayerProfileUnknown
}

// FreezeLayerProfile records explicit protocol evidence observed during ordered
// admission. Repeating the same value is idempotent. A later well-formed
// invokeWithLayer may replace either an inherited default or older explicit
// evidence; already-admitted requests retain their own immutable profile.
func (c *Conn) FreezeLayerProfile(profile tlprofile.Profile) error {
	_, err := c.setLayerProfile(profile, LayerProfileExplicit, true)
	return err
}

// FreezeLayerProfileAt applies explicit protocol evidence in client msg_id
// order. A duplicate older than the last accepted evidence is inert; the same
// msg_id carrying another Layer is a protocol conflict. Advancing the evidence
// cursor at an unchanged Layer does not rotate the outbound epoch because the
// wire profile itself did not change.
func (c *Conn) FreezeLayerProfileAt(profile tlprofile.Profile, msgID int64) (bool, error) {
	return c.freezeLayerProfileAt(profile, msgID)
}

// SeedLayerProfile restores explicit evidence previously proven for this exact
// logical session. It is kept as the compatible same-session restore API;
// auth-key-wide metadata must use SeedInheritedLayerProfile instead.
func (c *Conn) SeedLayerProfile(profile tlprofile.Profile) error {
	_, err := c.setLayerProfile(profile, LayerProfileExplicit, true)
	return err
}

// SeedInheritedLayerProfile installs an auth-key-wide default only while the
// connection is still unknown. It never overwrites explicit evidence or an
// already selected inherited default; client protocol evidence owns correction.
func (c *Conn) SeedInheritedLayerProfile(profile tlprofile.Profile) error {
	_, err := c.setLayerProfile(profile, LayerProfileInherited, false)
	return err
}

// legacyClientLayer returns the test-only canonical-transcoder profile. Zero
// deliberately remains unknown; it must never be converted into an implicit
// canonical application profile.
func (c *Conn) legacyClientLayer() int { return int(c.clientLayer.Load()) }

// setLegacyClientLayer records the package-internal legacy mirror.
func (c *Conn) setLegacyClientLayer(layer int) { c.clientLayer.Store(int32(layer)) }

// AuthKeyID returns the connection's auth_key_id.
func (c *Conn) AuthKeyID() [8]byte { return c.authKeyID }

// AuthKeyExpiresAt returns the raw protocol key's expiry time; 0 means a permanent key.
func (c *Conn) AuthKeyExpiresAt() int { return c.authKeyExpiresAt }

func (c *Conn) authKeyProtocolUnavailableNow() bool {
	if c == nil {
		return true
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	return authKeyProtocolUnavailable(c.authKeyExpiresAt, now)
}

// BusinessAuthKeyID returns the auth_key_id from the business view.
//
// After a temp auth_key is bound it resolves to the perm auth_key; the second
// return value indicates whether this connection has completed resolution, and
// returns true even when the result equals the original auth_key_id, to avoid
// querying the binding table on every RPC.
func (c *Conn) BusinessAuthKeyID() ([8]byte, bool) {
	c.identityMu.RLock()
	defer c.identityMu.RUnlock()
	return c.businessAuthKeyID, c.businessAuthKeyResolved
}

// BusinessAuthKeyHex returns the hex cache of the business-view auth_key_id
// (used in every RPC log, avoiding repeated encoding).
func (c *Conn) BusinessAuthKeyHex() (string, bool) {
	c.identityMu.RLock()
	defer c.identityMu.RUnlock()
	return c.businessAuthKeyHex, c.businessAuthKeyResolved
}

// SetBusinessAuthKeyID caches the business-view auth_key_id.
func (c *Conn) SetBusinessAuthKeyID(id [8]byte) {
	c.identityMu.Lock()
	changed := !c.businessAuthKeyResolved || c.businessAuthKeyID != id
	if changed || c.businessAuthKeyHex == "" {
		c.businessAuthKeyHex = hex.EncodeToString(id[:])
	}
	c.businessAuthKeyID = id
	c.businessAuthKeyResolved = true
	c.identityMu.Unlock()
	if changed {
		c.deliveryIdentityGeneration.Add(1)
		c.userID.Store(0)
		c.userIDResolved.Store(false)
	}
}

// SessionID returns the connection's session_id.
func (c *Conn) SessionID() int64 { return c.sessionID }

// UserID returns the bound user id; 0 when not logged in.
func (c *Conn) UserID() int64 { return c.userID.Load() }

// UserIDResolved reports whether the user_id authorization state has been
// resolved for the current connection.
//
// resolved=true with userID=0 means this auth_key is not currently logged in;
// this way the many RPCs before login do not repeatedly query the authorization
// table, and a later successful login is overwritten with the real user by
// BindUser.
func (c *Conn) UserIDResolved() (userID int64, resolved bool) {
	return c.userID.Load(), c.userIDResolved.Load()
}

// ReceivesUpdates reports whether this connection receives proactively pushed updates.
func (c *Conn) ReceivesUpdates() bool { return c.receivesUpdates.Load() }

// SetReceivesUpdates sets whether this connection receives proactively pushed updates.
// After login the main connection is set to true once updates.getState/getDifference
// have established the sync baseline.
func (c *Conn) SetReceivesUpdates(v bool) { c.receivesUpdates.Store(v) }

// setRemoteAddrStr records the peer address of the MTProto connection (remote is host:port or a
// bare host). Only a normalized literal that parses as an IP is kept: CoreExec passes it to Core
// as is, and Core rejects any non-normalized IP, so the Edge must not forward non-IP values such
// as a unix socket path.
func (c *Conn) setRemoteAddrStr(remote string) {
	host := remote
	if h, _, err := net.SplitHostPort(remote); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return
	}
	c.remoteAddr = addr.WithZone("").Unmap().String()
}

// clientIP returns the connection's peer IP (host part), empty when unset.
func (c *Conn) clientIP() string { return c.remoteAddr }
