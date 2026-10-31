package mtprotoedge

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// ErrSessionNotFound means the target session currently has no active connection.
var ErrSessionNotFound = errors.New("session not found")

var (
	ErrSessionActivationSuperseded = errors.New("session activation superseded")
	ErrSessionActivationFence      = errors.New("session activation could not fence previous writer")
)

const (
	maxPendingPushesPerSession = 32
	// maxFlushAttempts / flushRetryBackoff: the bound on backoff retries after
	// c.Send fails while draining staged pushes (outbound congestion 5s timeout or
	// transient error). When the connection is truly dead serveConn Unregisters and
	// cleans up state, cutting losses early; here only bounded retries are done for
	// "connection alive but outbound temporarily congested". If retries are
	// exhausted and it still fails, the flag is set active and getDifference
	// fallback is accepted—avoiding an idle client (sending only pings, not
	// triggering a flag-set retry) being stuck inactive forever and silently losing
	// its stream.
	maxFlushAttempts  = 5
	flushRetryBackoff = 2 * time.Second
	// pendingPushMaxAge: when a session, after registration, takes too long to call
	// updates.getState (receivesUpdates stays false), this is the longest its staged
	// proactive pushes are retained. Beyond it the whole batch is dropped and no
	// more is hoarded—a normal TDesktop establishes the sync baseline with getState
	// within seconds after login; long-term not-ready is usually an aberrant/adversarial connection.
	//
	// Invariant: only durable updates (writing user_update_events) enter pending.
	// Transient updates (typing/presence, not writing the durable log) go through
	// PushToUserTransient* and are skipped directly when not ready, without
	// enqueuing, so when this queue is dropped by aging/overflow/exhausted retries
	// what is dropped is always durable entries—getDifference fills them in via
	// user_update_events, so dropping loses no data.
	pendingPushMaxAge          = 60 * time.Second
	defaultPendingPushMaxBytes = int64(256 << 20)
	// maxSessionsPerAuthKey: the cap on sessions allowed online simultaneously for a
	// single raw auth_key. telesrv is single-DC; all connections of one client (main
	// connection + concurrent downloads/uploads) share the same auth_key and each
	// uses an independent session_id, so this cap must exceed a real client's
	// per-device concurrent connection peak, otherwise it would wrongly kill active
	// downloads/the main connection:
	//   - TDesktop: kMaxMediaDcCount=0x10, at most 16 downloads + 16 uploads + 1 main ≈ 33 per DC;
	//   - DrKLO: DOWNLOAD_CONNECTIONS_COUNT=2 + UPLOAD_CONNECTIONS_COUNT=4 + main/push ≈ 10.
	// Adding reconnect churn (old sessions lingering within readTimeout) the peak is
	// about ~70, so 256 is set (~3.5x headroom). It only guards against the
	// pathology of "a single auth_key accumulating a huge number of connections"
	// (which would make CloseSessionsForRawAuthKey/pushToUser traversal degrade to
	// O(N)); over-limit eviction only drops connections of that same device's
	// credentials and does not harm other accounts.
	maxSessionsPerAuthKey = 256
	// maxChannelIndexPerSession: the cap on the number of channels a single session
	// registers in the channel routing indexes (interest / membership). membership
	// comes from real membership (a large account may have many) and interest is
	// directly controlled by the client; a loose upper bound is set for both to
	// prevent memory amplification, truncating and logging when exceeded.
	maxChannelIndexPerSession = 8192
	// Official clients short-poll at most ten opened channels per session. Keep the
	// server-side passive subscription index at the same hard bound.
	maxChannelSubscriptionsPerSession = 10
	defaultChannelSubscriptionTTL     = 75 * time.Second
	maxChannelSubscriptionTTL         = 2 * time.Minute
	// A claim is normally released by its rpc_result delivery callback or the
	// pending-update flush it starts. This lease only recovers the exceptional
	// path where result encoding is replaced before the callback can be attached.
	updatesActivationClaimTTL = time.Minute
)

// forceCloseBatchTimeout is one deadline for a whole revoke/replace/eviction batch. Conn.Close
// already bounds its inbound-RPC wait, but calling ForceClose serially would multiply that bound
// by the number of sessions. The batch helper starts every close concurrently and waits at most
// this one shared interval.
const forceCloseBatchTimeout = rpcCloseWaitTimeout

// maxForceCloseParallelism caps control-plane close goroutines even if a corrupted/runtime index
// hands a revoke path far more sessions than maxSessionsPerAuthKey. Every Conn's producer/RPC gate
// is closed synchronously before these workers start, so a stuck transport.Close cannot admit more
// memory while the bounded workers continue draining physical sockets in the background.
const maxForceCloseParallelism = 64

type queuedPush struct {
	t           proto.MessageType
	updates     *layerUpdatesFanout
	reservation *pendingPushReservation
	at          time.Time
}

type pendingPushReservation struct {
	budget *outboundTrackedBudget
	bytes  atomic.Int64
	refs   atomic.Int32

	mu       sync.Mutex
	profiles map[tlprofile.Profile]struct{}
}

func (r *pendingPushReservation) retain() {
	if r == nil {
		return
	}
	if refs := r.refs.Add(1); refs <= 1 {
		panic("mtprotoedge: retained released pending push reservation")
	}
}

func (r *pendingPushReservation) release() {
	if r == nil {
		return
	}
	refs := r.refs.Add(-1)
	if refs < 0 {
		panic("mtprotoedge: pending push reservation released more than retained")
	}
	if refs == 0 {
		r.budget.release(int(r.bytes.Load()))
	}
}

// reservePrepared accounts the profile-specific immutable body retained by the
// semantic pending fanout. Multiple queued sessions sharing this reservation
// and profile share both the bytes and this one budget charge.
func (r *pendingPushReservation) reservePrepared(profile tlprofile.Profile, bytes int) bool {
	if r == nil || bytes < 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.profiles[profile]; ok {
		return true
	}
	if !r.budget.reserve(bytes) {
		return false
	}
	if r.profiles == nil {
		r.profiles = make(map[tlprofile.Profile]struct{})
	}
	r.profiles[profile] = struct{}{}
	r.bytes.Add(int64(bytes))
	return true
}

type sessionKey struct {
	authKeyID [8]byte
	sessionID int64
}

type channelSubscription struct {
	userID    int64
	expiresAt int64
}

// SessionLifecycleObserver receives active connection lifecycle events.
type SessionLifecycleObserver interface {
	SessionOffline(rawAuthKeyID [8]byte, sessionID, userID int64, lastForUser bool)
}

// SessionDestructionObserver is an optional explicit control-plane lifecycle.
// It is separate from SessionOffline because a physical disconnect must retain
// logical-session replay metadata, while destroy_session must invalidate it.
type SessionDestructionObserver interface {
	SessionDestroyed(rawAuthKeyID [8]byte, sessionID int64)
}

func notifySessionDestroyed(observer SessionLifecycleObserver, authKeyID [8]byte, sessionID int64) {
	if destroyed, ok := observer.(SessionDestructionObserver); ok {
		destroyed.SessionDestroyed(authKeyID, sessionID)
	}
}

// SessionManager is the active-connection registry, supporting lookup and
// proactive push by session / auth-key / user.
//
// It only manages in-process runtime state, holding sendable active connections;
// protocol-recoverable facts are carried by the auth key, client reconnection and
// the durable updates/difference chain. All methods are concurrency-safe.
type SessionManager struct {
	mu        sync.RWMutex
	bySession map[sessionKey]*Conn
	// logicalSessions owns MTProto resend state independently of physical Conn
	// generations. It is bounded by the Server-wide tracked-body budget and a
	// short offline retention window; ACK and destroy release bodies immediately.
	logicalSessions map[sessionKey]*logicalSession
	// claims owns the provisional -> active gap. A claimant is intentionally
	// absent from every push/online index until its required session control frame
	// is on the wire and PublishActivation validates the same owner.
	claims                 map[sessionKey]*Conn
	claimsByAuth           map[[8]byte]map[int64]*Conn // raw authKeyID -> sessionID -> provisional claim
	byAuthKey              map[[8]byte]map[int64]*Conn // raw authKeyID → sessionID → Conn
	byBusinessAuthKey      map[[8]byte]map[sessionKey]*Conn
	byUser                 map[int64]map[sessionKey]*Conn
	byChannel              map[int64]map[sessionKey]int64 // channelID → session → userID, for temporary channel active-viewer pushes
	bySessionChannels      map[sessionKey]map[int64]struct{}
	bySubscribedChannel    map[int64]map[sessionKey]channelSubscription
	bySessionSubscriptions map[sessionKey]map[int64]int64
	byMemberChannel        map[int64]map[sessionKey]int64 // channelID → session → userID, for durable update pushes to online members
	bySessionMembers       map[sessionKey]map[int64]struct{}
	pending                map[sessionKey][]queuedPush // proactive pushes staged before updates-ready
	flushing               map[sessionKey]bool         // sessions whose staged pushes are draining on flag set; pushes keep entering pending until the drain completes, preserving order
	pendingBudget          *outboundTrackedBudget      // process-level cap on encoded bodies staged for not-ready sessions
	logicalSessionReleased func(sessionKey)
	updatesActivationSeq   uint64
	bootstrapProbeSeq      uint64

	lifecycle SessionLifecycleObserver
	log       *zap.Logger
}

// NewSessionManager creates an empty connection registry.
func NewSessionManager(log *zap.Logger) *SessionManager {
	if log == nil {
		log = zap.NewNop()
	}
	return &SessionManager{
		bySession:              make(map[sessionKey]*Conn),
		logicalSessions:        make(map[sessionKey]*logicalSession),
		claims:                 make(map[sessionKey]*Conn),
		claimsByAuth:           make(map[[8]byte]map[int64]*Conn),
		byAuthKey:              make(map[[8]byte]map[int64]*Conn),
		byBusinessAuthKey:      make(map[[8]byte]map[sessionKey]*Conn),
		byUser:                 make(map[int64]map[sessionKey]*Conn),
		byChannel:              make(map[int64]map[sessionKey]int64),
		bySessionChannels:      make(map[sessionKey]map[int64]struct{}),
		bySubscribedChannel:    make(map[int64]map[sessionKey]channelSubscription),
		bySessionSubscriptions: make(map[sessionKey]map[int64]int64),
		byMemberChannel:        make(map[int64]map[sessionKey]int64),
		bySessionMembers:       make(map[sessionKey]map[int64]struct{}),
		pending:                make(map[sessionKey][]queuedPush),
		flushing:               make(map[sessionKey]bool),
		pendingBudget:          newOutboundTrackedBudget(defaultPendingPushMaxBytes),
		log:                    log,
	}
}

// SetLifecycleObserver installs a best-effort active session lifecycle observer.
func (m *SessionManager) SetLifecycleObserver(observer SessionLifecycleObserver) {
	m.mu.Lock()
	m.lifecycle = observer
	m.mu.Unlock()
}

func (m *SessionManager) setLogicalSessionReleaseHook(hook func(sessionKey)) {
	m.mu.Lock()
	m.logicalSessionReleased = hook
	m.mu.Unlock()
}

// SeedInheritedLayerForRawAuthKey supplies an auth-key-wide default to every
// currently unknown active/provisional connection for rawAuthKeyID. Existing
// inherited or explicit state is left untouched; only ordered invokeWithLayer
// admission may correct a selected profile. The return value is the number of
// connections which transitioned from unknown to inherited.
func (m *SessionManager) SeedInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte, layer int) int {
	return m.applyInheritedLayerForRawAuthKey(rawAuthKeyID, layer, false)
}

// RefreshInheritedLayerForRawAuthKey is the auth.bindTempAuthKey identity-
// normalization path. It replaces unknown and inherited raw-temp-key shadows
// with the resolved permanent-key default, while preserving explicit evidence.
// Ordinary auth-key default publication must use SeedInheritedLayerForRawAuthKey
// so it cannot rewrite live sessions which already selected an inherited value.
func (m *SessionManager) RefreshInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte, layer int) int {
	return m.applyInheritedLayerForRawAuthKey(rawAuthKeyID, layer, true)
}

// ClearInheritedLayerForRawAuthKey removes a stale raw-key default after
// identity normalization obtains an authoritative unsupported/unknown
// permanent-key result. Only inherited state is cleared: explicit
// invokeWithLayer evidence belongs to the concrete logical session and remains
// authoritative until newer ordered evidence replaces it.
func (m *SessionManager) ClearInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte) int {
	if m == nil || rawAuthKeyID == ([8]byte{}) {
		return 0
	}
	m.mu.RLock()
	conns := make([]*Conn, 0, len(m.byAuthKey[rawAuthKeyID])+len(m.claimsByAuth[rawAuthKeyID]))
	seen := make(map[*Conn]struct{}, cap(conns))
	for _, group := range []map[int64]*Conn{m.byAuthKey[rawAuthKeyID], m.claimsByAuth[rawAuthKeyID]} {
		for _, c := range group {
			if c == nil {
				continue
			}
			if _, duplicate := seen[c]; duplicate {
				continue
			}
			seen[c] = struct{}{}
			conns = append(conns, c)
		}
	}
	m.mu.RUnlock()

	cleared := 0
	for _, c := range conns {
		if c.isRetired() {
			continue
		}
		if changed, err := c.clearInheritedLayerProfileState(); err == nil && changed {
			cleared++
		}
	}
	return cleared
}

// SeedInheritedLayerForBusinessAuthKey supplies a canonical permanent-key
// default to every live raw physical key already normalized to that business
// identity. This covers multiple temporary/PFS keys for one authorization;
// explicit and previously-selected inherited session profiles remain stable.
func (m *SessionManager) SeedInheritedLayerForBusinessAuthKey(businessAuthKeyID [8]byte, layer int) int {
	if m == nil || businessAuthKeyID == ([8]byte{}) {
		return 0
	}
	profile, ok := tlprofile.ResolveProfile(layer)
	if !ok {
		return 0
	}
	m.mu.RLock()
	group := m.byBusinessAuthKey[businessAuthKeyID]
	conns := make([]*Conn, 0, len(group))
	seen := make(map[*Conn]struct{}, len(group))
	for _, c := range group {
		if c == nil {
			continue
		}
		if _, duplicate := seen[c]; duplicate {
			continue
		}
		seen[c] = struct{}{}
		conns = append(conns, c)
	}
	m.mu.RUnlock()

	seeded := 0
	for _, c := range conns {
		if c.isRetired() {
			continue
		}
		if current, resolved := c.BusinessAuthKeyID(); !resolved || current != businessAuthKeyID {
			continue
		}
		if changed, err := c.setLayerProfile(profile, LayerProfileInherited, false); err == nil && changed {
			seeded++
		}
	}
	return seeded
}

func (m *SessionManager) applyInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte, layer int, refresh bool) int {
	if m == nil {
		return 0
	}
	profile, ok := tlprofile.ResolveProfile(layer)
	if !ok {
		return 0
	}
	m.mu.RLock()
	conns := make([]*Conn, 0, len(m.byAuthKey[rawAuthKeyID])+len(m.claimsByAuth[rawAuthKeyID]))
	seen := make(map[*Conn]struct{}, cap(conns))
	for _, group := range []map[int64]*Conn{m.byAuthKey[rawAuthKeyID], m.claimsByAuth[rawAuthKeyID]} {
		for _, c := range group {
			if c == nil {
				continue
			}
			if _, duplicate := seen[c]; duplicate {
				continue
			}
			seen[c] = struct{}{}
			conns = append(conns, c)
		}
	}
	m.mu.RUnlock()

	seeded := 0
	for _, c := range conns {
		if c.isRetired() {
			continue
		}
		var (
			changed bool
			err     error
		)
		if refresh {
			changed, err = c.refreshInheritedLayerProfile(profile)
		} else {
			changed, err = c.setLayerProfile(profile, LayerProfileInherited, false)
		}
		if err == nil && changed {
			seeded++
		}
	}
	return seeded
}

// ApplyOrderedLayerProfileForSession converges every physical generation
// currently active or claiming the same logical MTProto session. Per-Conn
// msg_id watermarks make broadcasts commutative: even if profile 300 reaches a
// Conn before a delayed profile 200 broadcast, 200 is inert and final state is
// the exact registry's maximum accepted evidence.
func (m *SessionManager) ApplyOrderedLayerProfileForSession(
	primary *Conn,
	rawAuthKeyID [8]byte,
	sessionID int64,
	profile tlprofile.Profile,
	msgID int64,
) (int, error) {
	if err := validateLayerProfile(profile); err != nil {
		return 0, err
	}
	return m.ApplyOrderedRawLayerForSession(primary, rawAuthKeyID, sessionID, int(profile), msgID)
}

// ApplyOrderedRawLayerForSession also carries future Layers unknown to this
// binary. Their raw watermark converges across physical generations while each
// Conn remains codec-unknown until a newer supported selector is admitted.
func (m *SessionManager) ApplyOrderedRawLayerForSession(
	primary *Conn,
	rawAuthKeyID [8]byte,
	sessionID int64,
	layer int,
	msgID int64,
) (int, error) {
	if msgID <= 0 {
		return 0, fmt.Errorf("invalid ordered session layer msg_id %d", msgID)
	}
	if layer <= 0 {
		return 0, fmt.Errorf("invalid ordered session layer %d", layer)
	}
	conns := make([]*Conn, 0, 3)
	seen := make(map[*Conn]struct{}, 3)
	if primary != nil {
		seen[primary] = struct{}{}
		conns = append(conns, primary)
	}
	if m != nil {
		key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
		m.mu.RLock()
		for _, c := range []*Conn{m.bySession[key], m.claims[key]} {
			if c == nil {
				continue
			}
			if _, duplicate := seen[c]; duplicate {
				continue
			}
			seen[c] = struct{}{}
			conns = append(conns, c)
		}
		m.mu.RUnlock()
	}

	applied := 0
	for _, c := range conns {
		if c == nil || c.isRetired() {
			continue
		}
		changed, err := c.freezeRawLayerProfileAt(layer, msgID)
		if err != nil {
			return applied, err
		}
		if changed {
			applied++
		}
	}
	return applied, nil
}

// ExplicitLayerEvidenceForAuthKey exposes live exact-session truth to
// auth.bindTempAuthKey. Router's bounded exact registry may expire while a Conn
// remains active; bind must not replace that explicit profile with a permanent
// key's inherited default merely because the execution-receipt TTL elapsed.
func (m *SessionManager) ExplicitLayerEvidenceForAuthKey(rawAuthKeyID [8]byte, sessionID int64) (layer int, msgID int64, ok bool) {
	if m == nil || rawAuthKeyID == ([8]byte{}) || sessionID == 0 {
		return 0, 0, false
	}
	key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
	m.mu.RLock()
	conns := []*Conn{m.bySession[key], m.claims[key]}
	m.mu.RUnlock()
	seen := make(map[*Conn]struct{}, len(conns))
	for _, c := range conns {
		if c == nil || c.isRetired() {
			continue
		}
		if _, duplicate := seen[c]; duplicate {
			continue
		}
		seen[c] = struct{}{}
		state, evidenceMsgID := c.layerProfileEvidenceState()
		if c.isRetired() || state.Origin != LayerProfileExplicit {
			continue
		}
		profile, supported := tlprofile.ResolveProfile(int(state.Profile))
		if !supported || profile != state.Profile {
			continue
		}
		if !ok || evidenceMsgID > msgID {
			layer, msgID, ok = int(profile), evidenceMsgID, true
			continue
		}
		if evidenceMsgID == msgID && layer != int(profile) {
			// This state contradicts the per-session msg_id ordering invariant;
			// do not let bind choose either physical generation arbitrarily.
			return 0, 0, false
		}
	}
	return layer, msgID, ok
}

// SetClientLayerForAuthKey implements rpc.ClientLayerBinder without weakening
// ordered evidence. It is a legacy/readiness safety net: only an unknown exact
// Conn receives the value as inherited state. Explicit or already-selected
// inherited profiles are owned by the edge's msg_id-ordered path.
func (m *SessionManager) SetClientLayerForAuthKey(rawAuthKeyID [8]byte, sessionID int64, layer int) {
	if m == nil {
		return
	}
	profile, ok := tlprofile.ResolveProfile(layer)
	if !ok {
		return
	}
	key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
	m.mu.RLock()
	conns := []*Conn{m.bySession[key], m.claims[key]}
	m.mu.RUnlock()
	seen := make(map[*Conn]struct{}, len(conns))
	for _, c := range conns {
		if c == nil || c.isRetired() {
			continue
		}
		if _, duplicate := seen[c]; duplicate {
			continue
		}
		seen[c] = struct{}{}
		_, _ = c.setLayerProfile(profile, LayerProfileInherited, false)
	}
}

// BeginActivation atomically claims auth_key_id + session_id without publishing the
// new Conn. Under the manager lock it irreversibly fences every previous owner,
// removes active indexes and closes producer/RPC admission gates. Physical close and
// outbound-actor convergence happen outside the lock; the caller may send the
// required new_session_created frame only after this method returns nil.
func (m *SessionManager) BeginActivation(c *Conn) error {
	if c == nil || !c.beginActivationClaim() {
		return ErrSessionActivationSuperseded
	}

	key := connSessionKey(c)
	retired := make([]*Conn, 0, 2)
	m.mu.Lock()
	if !c.isPhysicalTransportCurrentOpen() || c.lifecycleState() != connLifecycleClaiming {
		c.beginTerminalShutdown()
		m.mu.Unlock()
		return ErrConnClosed
	}
	if oldClaim := m.claims[key]; oldClaim != nil && oldClaim != c {
		m.retireClaimLocked(key, oldClaim, false)
		retired = append(retired, oldClaim)
	}
	if old := m.bySession[key]; old != nil && old != c {
		m.retireConnLocked(old, false)
		retired = append(retired, old)
	}

	// Claims reserve a cap slot just like published sessions. Otherwise many
	// concurrent handshakes could all pass the old byAuthKey-only check and publish
	// beyond maxSessionsPerAuthKey.
	for len(m.byAuthKey[c.authKeyID])+m.claimCountForAuthLocked(c.authKeyID) >= maxSessionsPerAuthKey {
		victimKey, victim, isClaim := m.oldestAuthOwnerLocked(c.authKeyID, c)
		if victim == nil {
			break
		}
		if isClaim {
			m.retireClaimLocked(victimKey, victim, true)
		} else {
			m.retireConnLocked(victim, true)
		}
		retired = append(retired, victim)
		m.log.Debug("Evicted oldest session activation for auth key at cap",
			zap.String("auth_key_id", sessionKeyLog(c.authKeyID)),
			zap.Int("cap", maxSessionsPerAuthKey),
		)
	}
	m.addClaimLocked(key, c)
	m.mu.Unlock()

	// Do not wait for old business handlers: their root context/admission gate is
	// already canceled. We only need the old physical writer and outbound actor to
	// converge before the new Conn is allowed to write the session barrier.
	if !closeConnBatch(retired, forceCloseBatchTimeout, false) {
		m.AbortActivation(c)
		return ErrSessionActivationFence
	}
	return nil
}

// PublishActivation makes the current claim visible to push/online lookups. It is
// deliberately a separate operation from BeginActivation so required protocol
// control can be written while the Conn remains provisional and unindexed.
func (m *SessionManager) PublishActivation(c *Conn) error {
	if c == nil {
		return ErrSessionActivationSuperseded
	}
	key := connSessionKey(c)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claims[key] != c {
		return ErrSessionActivationSuperseded
	}
	if c.lifecycleState() != connLifecycleClaiming {
		m.removeClaimLocked(key, c)
		return ErrConnClosed
	}
	if old := m.bySession[key]; old != nil && old != c {
		// An active owner without this claim can only be a stale/unsafe publisher.
		// Never reverse-replace it from here; the caller must reconnect and claim again.
		m.removeClaimLocked(key, c)
		c.beginTerminalShutdown()
		return ErrSessionActivationSuperseded
	}
	if !c.publishActivation() {
		m.removeClaimLocked(key, c)
		return ErrConnClosed
	}
	m.removeClaimLocked(key, c)
	m.bySession[key] = c
	addConnIndex(m.byAuthKey, c.authKeyID, c.sessionID, c)
	if businessAuthKeyID, resolved := c.BusinessAuthKeyID(); resolved {
		addBusinessAuthKeyIndex(m.byBusinessAuthKey, businessAuthKeyID, key, c)
	}
	if uid := c.userID.Load(); uid != 0 {
		c.userIDResolved.Store(true)
		addUserIndex(m.byUser, uid, key, c)
	}
	m.log.Debug("Session activated",
		zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
		zap.Int64("session_id", c.sessionID),
		zap.Int("online", len(m.bySession)),
	)
	return nil
}

// AbortActivation removes a claim only when c still owns it, then terminally
// retires the provisional Conn. A superseded caller cannot delete the newer claim.
func (m *SessionManager) AbortActivation(c *Conn) {
	if c == nil {
		return
	}
	key := connSessionKey(c)
	owned := false
	m.mu.Lock()
	if m.claims[key] == c {
		c.beginTerminalShutdown()
		m.removeClaimLocked(key, c)
		if m.bySession[key] == nil {
			m.deletePendingLocked(key)
			delete(m.flushing, key)
		}
		m.markLogicalSessionOfflineLocked(key, time.Now())
		owned = true
	}
	m.mu.Unlock()
	if owned {
		_ = closeConnBatch([]*Conn{c}, forceCloseBatchTimeout, false)
	}
}

// Unregister unregisters a connection (only if it is still the same object
// registered currently, to avoid wrongly deleting a new connection after
// reconnecting). The observer is also called back for logged-out connections
// (userID=0): the business layer uses it to clean up per-session cache entries,
// otherwise the metadata of a logged-out connection could only be evicted at the
// capacity cap.
func (m *SessionManager) Unregister(c *Conn) {
	if c == nil {
		return
	}
	// Close admission/outbound producer gates before removing indexes or invoking
	// a lifecycle observer. An observer may block, but no old RPC/push may continue
	// to write or enqueue work during that interval.
	c.beginTerminalShutdown()
	m.mu.Lock()
	var (
		observer    SessionLifecycleObserver
		offlineUser int64
		lastForUser bool
	)
	key := connSessionKey(c)
	if m.claims[key] == c {
		m.removeClaimLocked(key, c)
		m.deletePendingLocked(key)
		delete(m.flushing, key)
	}
	if cur, ok := m.bySession[key]; ok && cur == c {
		offlineUser = m.removeLocked(c, true)
		if offlineUser != 0 {
			lastForUser = len(m.byUser[offlineUser]) == 0
		}
		observer = m.lifecycle
		m.log.Debug("Session unregistered",
			zap.String("auth_key_id", sessionKeyLog(c.authKeyID)),
			zap.Int64("session_id", c.sessionID),
			zap.Int("online", len(m.bySession)),
		)
	}
	m.markLogicalSessionOfflineLocked(key, time.Now())
	m.mu.Unlock()
	if observer != nil {
		observer.SessionOffline(c.authKeyID, c.sessionID, offlineUser, lastForUser)
	}
}

// DestroySessionForAuthKey precisely removes a session under a raw auth_key_id.
func (m *SessionManager) DestroySessionForAuthKey(authKeyID [8]byte, sessionID int64) bool {
	m.mu.Lock()
	observer := m.lifecycle
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	if !ok {
		if claim := m.claims[key]; claim != nil {
			m.retireClaimLocked(key, claim, true)
			outbound := m.destroyLogicalSessionLocked(key)
			m.mu.Unlock()
			if !forceCloseConnBatch([]*Conn{claim}, forceCloseBatchTimeout) {
				m.log.Warn("Claimed session close exceeded shared deadline",
					zap.String("auth_key_id", sessionKeyLog(authKeyID)),
					zap.Int64("session_id", sessionID),
				)
			}
			if outbound != nil {
				m.releaseLogicalSession(key, outbound)
			}
			notifySessionDestroyed(observer, authKeyID, sessionID)
			return true
		}
		m.deletePendingLocked(key)
		outbound := m.destroyLogicalSessionLocked(key)
		m.mu.Unlock()
		if outbound != nil {
			m.releaseLogicalSession(key, outbound)
		}
		notifySessionDestroyed(observer, authKeyID, sessionID)
		return false
	}
	offlineUser := m.retireConnLocked(c, true)
	outbound := m.destroyLogicalSessionLocked(key)
	lastForUser := offlineUser != 0 && len(m.byUser[offlineUser]) == 0
	m.log.Debug("Session destroyed",
		zap.String("auth_key_id", sessionKeyLog(authKeyID)),
		zap.Int64("session_id", sessionID),
		zap.Int("online", len(m.bySession)),
	)
	m.mu.Unlock()
	if !forceCloseConnBatch([]*Conn{c}, forceCloseBatchTimeout) {
		m.log.Warn("Destroyed session close exceeded shared deadline",
			zap.String("auth_key_id", sessionKeyLog(authKeyID)),
			zap.Int64("session_id", sessionID),
		)
	}
	if outbound != nil {
		m.releaseLogicalSession(key, outbound)
	}
	if observer != nil && offlineUser != 0 {
		observer.SessionOffline(authKeyID, sessionID, offlineUser, lastForUser)
	}
	notifySessionDestroyed(observer, authKeyID, sessionID)
	return true
}

// BindUserForAuthKey caches the authorized user for the given raw auth_key_id + session_id.
func (m *SessionManager) BindUserForAuthKey(authKeyID [8]byte, sessionID, userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	if !ok {
		return
	}
	m.bindUserLocked(c, key, userID)
}

func (m *SessionManager) bindUserLocked(c *Conn, key sessionKey, userID int64) {
	if old := c.userID.Swap(userID); old != 0 {
		removeUserIndex(m.byUser, old, key)
		if old != userID {
			m.clearSessionChannelIndexesLocked(c, key)
			c.membershipsSynced.Store(false)
			m.clearUpdatesActivationLocked(c)
			m.clearBootstrapProbeLocked(c)
			// An identity change discards staged pushes: they belong to the previous
			// account, and flushing them to the new account would be a cross-account
			// leak. It also cancels an in-progress drain (runFlush has an additional
			// owner check as a per-batch fallback).
			m.deletePendingLocked(key)
			delete(m.flushing, key)
		}
	}
	c.userIDResolved.Store(true)
	if userID != 0 {
		addUserIndex(m.byUser, userID, key, c)
	} else {
		m.clearSessionChannelIndexesLocked(c, key)
		c.membershipsSynced.Store(false)
		m.clearUpdatesActivationLocked(c)
		m.clearBootstrapProbeLocked(c)
		m.deletePendingLocked(key)
		delete(m.flushing, key)
	}
}

// UserIDResolvedForAuthKey returns the user_id cache state for the given raw auth_key_id + session_id.
func (m *SessionManager) UserIDResolvedForAuthKey(authKeyID [8]byte, sessionID int64) (int64, bool) {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: authKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return c.UserIDResolved()
}

// BindAuthKeyForSession caches the business auth_key_id for the given raw auth_key_id + session_id.
func (m *SessionManager) BindAuthKeyForSession(rawAuthKeyID [8]byte, sessionID int64, authKeyID [8]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	if !ok {
		return
	}
	m.bindAuthKeyLocked(c, key, authKeyID)
}

// BindAuthKeyForRawAuthKey binds all active sessions of the same raw temporary key
// to the canonical permanent key. Android/TDesktop concurrently create multiple
// sessions on one temp key; the bind happens on only one of them, and the other
// sessions must not keep treating the raw temp as the business key.
func (m *SessionManager) BindAuthKeyForRawAuthKey(rawAuthKeyID [8]byte, authKeyID [8]byte) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	bound := 0
	for sessionID, c := range m.byAuthKey[rawAuthKeyID] {
		if c == nil {
			continue
		}
		key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
		if m.bySession[key] != c {
			continue
		}
		m.bindAuthKeyLocked(c, key, authKeyID)
		bound++
	}
	return bound
}

func (m *SessionManager) bindAuthKeyLocked(c *Conn, key sessionKey, authKeyID [8]byte) {
	oldAuthKeyID, resolved := c.BusinessAuthKeyID()
	changed := !resolved || oldAuthKeyID != authKeyID
	oldUserID := c.userID.Load()
	if resolved {
		removeBusinessAuthKeyIndex(m.byBusinessAuthKey, oldAuthKeyID, key)
	}
	c.SetBusinessAuthKeyID(authKeyID)
	m.bindLogicalSessionAuthKeyLocked(key, authKeyID)
	addBusinessAuthKeyIndex(m.byBusinessAuthKey, authKeyID, key, c)
	if changed {
		if oldUserID != 0 {
			removeUserIndex(m.byUser, oldUserID, key)
		}
		m.clearSessionChannelIndexesLocked(c, key)
		c.membershipsSynced.Store(false)
		m.clearUpdatesActivationLocked(c)
		m.clearBootstrapProbeLocked(c)
		m.deletePendingLocked(key)
		delete(m.flushing, key)
		c.userID.Store(0)
		c.userIDResolved.Store(false)
	}
}

// AuthKeyIDForSession returns the cached business auth_key_id for the given raw auth_key_id + session_id.
func (m *SessionManager) AuthKeyIDForSession(rawAuthKeyID [8]byte, sessionID int64) ([8]byte, bool) {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return [8]byte{}, false
	}
	return c.BusinessAuthKeyID()
}

// AuthKeyExpiresAtForSession returns the handshake protocol expiry of the raw key; 0 means permanent.
func (m *SessionManager) AuthKeyExpiresAtForSession(rawAuthKeyID [8]byte, sessionID int64) (int, bool) {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return c.AuthKeyExpiresAt(), true
}

// CloseSessionsForBusinessAuthKey forcibly disconnects all active connections of
// the given business auth_key, used for authorization revocation (a kicked
// device): outbound pushes are encrypted with the key held by the connection and
// do not recheck the key store, so without disconnecting, a revoked device would
// keep receiving pushes until it disconnects naturally; also, a perm-key
// connection's authorization cache is only rechecked on disconnect and reconnect.
// Here the underlying transport must be closed so the WebSocket/TCP peer sees the
// disconnect immediately, rather than only removing it from the online index.
func (m *SessionManager) CloseSessionsForBusinessAuthKey(authKeyID [8]byte) int {
	type offlineEvent struct {
		key    sessionKey
		userID int64
		last   bool
	}
	m.mu.Lock()
	var conns []*Conn
	var events []offlineEvent
	var logicalRelease []*logicalSession
	for key, c := range m.businessAuthKeyCandidatesLocked(authKeyID) {
		if !connUsesBusinessAuthKey(c, authKeyID) {
			continue
		}
		uid := m.retireConnLocked(c, true)
		conns = append(conns, c)
		events = append(events, offlineEvent{key: key, userID: uid, last: uid != 0 && len(m.byUser[uid]) == 0})
	}
	for key, c := range m.claims {
		if !connUsesBusinessAuthKey(c, authKeyID) {
			continue
		}
		m.retireClaimLocked(key, c, true)
		conns = append(conns, c)
	}
	for key, logical := range m.logicalSessions {
		if logical == nil || (key.authKeyID != authKeyID &&
			(!logical.businessAuthResolved || logical.businessAuthKeyID != authKeyID)) {
			continue
		}
		delete(m.logicalSessions, key)
		logicalRelease = append(logicalRelease, logical)
	}
	observer := m.lifecycle
	if len(conns) > 0 {
		m.log.Debug("Force close sessions for revoked auth key",
			zap.String("auth_key_id", sessionKeyLog(authKeyID)),
			zap.Int("closed", len(conns)),
		)
	}
	m.mu.Unlock()
	if !forceCloseConnBatch(conns, forceCloseBatchTimeout) {
		m.log.Warn("Revoked auth-key session close exceeded shared deadline",
			zap.String("auth_key_id", sessionKeyLog(authKeyID)),
			zap.Int("sessions", len(conns)),
		)
	}
	for _, logical := range logicalRelease {
		m.releaseLogicalSession(logical.key, logical.outbound)
	}
	if observer != nil {
		for _, e := range events {
			observer.SessionOffline(e.key.authKeyID, e.key.sessionID, e.userID, e.last)
		}
	}
	return len(conns)
}

// CloseSessionsForRawAuthKeyExcept forcibly disconnects the active connections of
// the given raw auth_key, optionally excluding one session by session ID. This
// interface is used by the business layer for authorization revocation; wire-level
// destroy_auth_key must instead use exact Conn exclusion, to avoid a same-session
// replacement being wrongly let through.
func (m *SessionManager) CloseSessionsForRawAuthKeyExcept(authKeyID [8]byte, exceptSessionID int64) int {
	return m.closeSessionsForRawAuthKey(authKeyID, func(sessionID int64, _ *Conn) bool {
		return sessionID == exceptSessionID
	})
}

// CloseSessionsForRawAuthKeyExceptConn closes every active/claiming owner for a raw
// auth key except the exact Conn executing destroy_auth_key. A session ID is not an
// identity: a concurrent replacement may already own the same logical session while
// the retired request handler finishes deletion.
func (m *SessionManager) CloseSessionsForRawAuthKeyExceptConn(authKeyID [8]byte, except *Conn) int {
	return m.closeSessionsForRawAuthKey(authKeyID, func(_ int64, c *Conn) bool {
		return c == except
	})
}

func (m *SessionManager) closeSessionsForRawAuthKey(authKeyID [8]byte, skip func(int64, *Conn) bool) int {
	type offlineEvent struct {
		key    sessionKey
		userID int64
		last   bool
	}
	m.mu.Lock()
	var conns []*Conn
	var events []offlineEvent
	for sessionID, c := range m.byAuthKey[authKeyID] {
		if skip != nil && skip(sessionID, c) {
			continue
		}
		key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
		uid := m.retireConnLocked(c, true)
		conns = append(conns, c)
		events = append(events, offlineEvent{key: key, userID: uid, last: uid != 0 && len(m.byUser[uid]) == 0})
	}
	for sessionID, c := range m.claimsByAuth[authKeyID] {
		if skip != nil && skip(sessionID, c) {
			continue
		}
		key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
		m.retireClaimLocked(key, c, true)
		conns = append(conns, c)
	}
	observer := m.lifecycle
	m.mu.Unlock()
	if !forceCloseConnBatch(conns, forceCloseBatchTimeout) {
		m.log.Warn("Raw auth-key session close exceeded shared deadline",
			zap.String("auth_key_id", sessionKeyLog(authKeyID)),
			zap.Int("sessions", len(conns)),
		)
	}
	if observer != nil {
		for _, e := range events {
			observer.SessionOffline(e.key.authKeyID, e.key.sessionID, e.userID, e.last)
		}
	}
	return len(conns)
}

// forceCloseConnBatch closes every producer/RPC gate first, then closes physical transports with a
// bounded worker set. Physical close and actor/RPC convergence share one batch deadline; the wait is
// never multiplied by the number of sessions. Workers may finish physical closes after the caller's
// deadline, but no timed-out Conn can enqueue more work in that interval. Nil/duplicate entries are
// removed so Register's replacement/eviction slots cannot close the same Conn twice.
func forceCloseConnBatch(conns []*Conn, timeout time.Duration) bool {
	return closeConnBatch(conns, timeout, true)
}

// closeConnBatch always converges physical writers/outbound actors. waitInbound
// is reserved for destructive control-plane operations; activation takeover sets
// it false so a canceled business handler that ignores its context cannot stall a
// healthy replacement. Its admission and response writer are already terminal.
func closeConnBatch(conns []*Conn, timeout time.Duration, waitInbound bool) bool {
	if len(conns) == 0 {
		return true
	}
	unique := make([]*Conn, 0, len(conns))
	seen := make(map[*Conn]struct{}, len(conns))
	for _, c := range conns {
		if c == nil {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		unique = append(unique, c)
	}
	if len(unique) == 0 {
		return true
	}

	// This phase is non-blocking and must precede transport.Close: it is the safety boundary if
	// an implementation of transport.Conn.Close itself blocks past the batch deadline.
	for _, c := range unique {
		c.beginTerminalShutdown()
	}

	workers := min(len(unique), maxForceCloseParallelism)
	jobs := make(chan *Conn, len(unique))
	for _, c := range unique {
		jobs <- c
	}
	close(jobs)
	var closeWG sync.WaitGroup
	closeWG.Add(workers)
	for range workers {
		go func() {
			defer closeWG.Done()
			for c := range jobs {
				c.closeTransport()
			}
		}()
	}
	physicalDone := make(chan struct{})
	go func() {
		closeWG.Wait()
		close(physicalDone)
	}()

	if timeout <= 0 {
		return false
	}
	deadline := time.Now().Add(timeout)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-physicalDone:
	case <-timer.C:
		return false
	}

	// All physical close calls returned. Wait for memory-owning actor/RPC work using the same
	// deadline; the first genuinely stuck Conn consumes the remaining allowance, not a fresh 5s.
	for _, c := range unique {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		if waitInbound && c.rpcScheduler != nil && !c.waitInboundShutdown(remaining) {
			return false
		}
		if c.outboundDone == nil {
			continue
		}
		remaining = time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		wait := time.NewTimer(remaining)
		select {
		case <-c.outboundDone:
			wait.Stop()
		case <-wait.C:
			return false
		}
	}
	return true
}

// UnbindAuthKey clears the logged-in user cache of all active connections under a business auth_key.
func (m *SessionManager) UnbindAuthKey(authKeyID [8]byte) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for key, c := range m.businessAuthKeyCandidatesLocked(authKeyID) {
		if !connUsesBusinessAuthKey(c, authKeyID) {
			continue
		}
		if old := c.userID.Swap(0); old != 0 {
			removeUserIndex(m.byUser, old, key)
		}
		m.clearSessionChannelIndexesLocked(c, key)
		c.membershipsSynced.Store(false)
		m.clearUpdatesActivationLocked(c)
		m.clearBootstrapProbeLocked(c)
		// After unbinding, staged pushes belong to the logged-out account and must not
		// be flushed out when the next login sets the flag.
		m.deletePendingLocked(key)
		delete(m.flushing, key)
		c.userIDResolved.Store(true)
		count++
	}
	return count
}

// setReceivesUpdatesLocked is the common core of setting/resetting the flag; the
// caller must hold m.mu.
// When setting the flag and there is staged data, receivesUpdates is not set
// immediately: instead flushing is marked and the userID owning that staged batch
// is returned, to be drained by runFlush and set atomically afterwards. Meanwhile
// newly arriving pushes keep entering pending, preserving the relative order of
// staged and live pushes (otherwise a live direct send could land before staged
// entries with earlier pts). The returned owner lets runFlush detect an identity
// switch during the drain (logout/account switch) and discard the remaining staged
// data of the old account without sending it to the new account.
func (m *SessionManager) setReceivesUpdatesLocked(c *Conn, key sessionKey, receives bool) (int64, bool) {
	if !receives {
		c.receivesUpdates.Store(false)
		m.clearSessionChannelIndexesLocked(c, key)
		c.membershipsSynced.Store(false)
		m.clearUpdatesActivationLocked(c)
		// Cancel an in-progress drain activation: runFlush rechecks this flag before
		// setting, and if the flag has been deleted it abandons the set, avoiding
		// flipping a switch just set to false back to true.
		delete(m.flushing, key)
		return 0, false
	}
	if _, ok := c.LayerProfile(); !ok {
		// A successful wire-invariant bootstrap RPC is not evidence that this
		// physical session can decode proactive updates. Keep durable updates
		// pending until generated exact admission freezes a real profile; do not
		// start a flush which would fail layer binding and retire a healthy socket.
		c.receivesUpdates.Store(false)
		m.clearSessionChannelIndexesLocked(c, key)
		c.membershipsSynced.Store(false)
		m.clearUpdatesActivationLocked(c)
		delete(m.flushing, key)
		return 0, false
	}
	if c.receivesUpdates.Load() || m.flushing[key] {
		// Already ready, or a drain goroutine is already running (it will take the
		// newly staged pushes itself and set the flag when it completes).
		if c.receivesUpdates.Load() {
			m.clearUpdatesActivationLocked(c)
		}
		return 0, false
	}
	if len(m.pending[key]) == 0 {
		c.receivesUpdates.Store(true)
		return 0, false
	}
	m.flushing[key] = true
	return c.userID.Load(), true
}

// runFlush sends the staged pushes directly to the connection in order, setting
// receivesUpdates only after draining (including those added during the drain).
// The direct send uses c.Send to bypass the ready check—at this point it is
// necessarily not ready, and going through PushToSessionForAuthKey would stage it
// again into an infinite loop. Three kinds of termination:
//   - identity switch (logout/account switch causing c.userID != owner): discard the remaining staged and requeued data without sending to the new account;
//   - send failure: requeue the remainder and back off and retry; if attempts are exhausted, set active and fall back to getDifference, so an idle client does not stay inactive forever;
//   - drain complete: atomically set receivesUpdates.
func (m *SessionManager) runFlush(c *Conn, key sessionKey, owner int64, attempt int) {
	for {
		m.mu.Lock()
		if cur, ok := m.bySession[key]; !ok || cur != c || !m.flushing[key] {
			// The connection has changed generation (removeLocked has already cleared
			// flushing) or the activation was canceled (SetReceivesUpdates(false)).
			m.mu.Unlock()
			return
		}
		if c.userID.Load() != owner {
			// A logout/account switch happened during the drain: the remaining staged
			// data belongs to the old account, must be discarded and must not be sent to the new account.
			m.deletePendingLocked(key)
			delete(m.flushing, key)
			m.clearUpdatesActivationLocked(c)
			m.mu.Unlock()
			return
		}
		batch := m.takePendingLocked(key, true)
		if len(batch) == 0 {
			c.receivesUpdates.Store(true)
			delete(m.flushing, key)
			m.clearUpdatesActivationLocked(c)
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		for i, item := range batch {
			// Recheck identity before each send: after a logout/account switch the
			// remaining entries of the batch must not keep being sent to a connection
			// that has changed hands.
			if c.userID.Load() != owner {
				m.mu.Lock()
				m.deletePendingLocked(key)
				delete(m.flushing, key)
				m.clearUpdatesActivationLocked(c)
				m.mu.Unlock()
				releaseQueuedPushes(batch[i:])
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// Pending entries are durable account updates. Shared body-budget pressure is not
			// evidence that this socket is corrupt, so use the non-terminal enqueue path; after
			// bounded retries, getDifference is the authoritative recovery path.
			encoded, err := item.updates.prepareForConn(ctx, c)
			if err == nil {
				if encoded == nil || encoded.layer == nil {
					err = errors.New("pending exact updates lost layer binding")
				} else if !item.reservation.reservePrepared(encoded.layer.profile, len(encoded.body)) {
					item.updates.discardPrepared(encoded.layer.profile, encoded)
					err = ErrOutboundTrackedBudget
				}
			}
			if err == nil {
				err = c.SendBestEffortEncoded(ctx, item.t, encoded, 5*time.Second)
			}
			cancel()
			if err == nil {
				item.release()
				continue
			}
			if isOutboundStaleLayerEpoch(err) {
				// This durable online accelerator was prepared before a client layer
				// correction. Drop only the stale item; difference remains authoritative.
				item.release()
				continue
			}
			if isOutboundLayerProfileError(err) {
				// updates-ready without an exact profile, or a mismatched final
				// body, violates the physical-connection layer invariant. Never
				// guess canonical bytes; retire this writer and let durable
				// difference recover after a correctly negotiated reconnect.
				c.dropSlowConsumer()
				releaseQueuedPushes(batch[i:])
				return
			}
			m.mu.Lock()
			if cur, ok := m.bySession[key]; !ok || cur != c || !m.flushing[key] || c.userID.Load() != owner {
				// Connection generation change/cancellation/hand-over: the remaining
				// batch does not belong to the current connection's current account, discard it.
				if c.userID.Load() != owner {
					m.deletePendingLocked(key)
					delete(m.flushing, key)
					m.clearUpdatesActivationLocked(c)
				}
				m.mu.Unlock()
				releaseQueuedPushes(batch[i:])
				return
			}
			rest := append(append([]queuedPush(nil), batch[i:]...), m.pending[key]...)
			if len(rest) > maxPendingPushesPerSession {
				// Consistent with queueLocked's overflow policy: drop the oldest and
				// keep the newest, concentrating the pts gap at the very front, so the
				// flush's first entry triggers the client's gap detection and the
				// recovery path is shortest.
				dropped := len(rest) - maxPendingPushesPerSession
				releaseQueuedPushes(rest[:dropped])
				rest = rest[dropped:]
			}
			m.pending[key] = rest
			if attempt+1 >= maxFlushAttempts {
				// Retries exhausted: set active to avoid an idle client losing its stream
				// forever; the durable updates among the remaining staged data are filled
				// in by getDifference triggered by the client's later pts gap.
				c.receivesUpdates.Store(true)
				m.deletePendingLocked(key)
				delete(m.flushing, key)
				m.clearUpdatesActivationLocked(c)
				m.mu.Unlock()
				m.log.Debug("Flush gave up after retries; activated with getDifference fallback",
					zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
					zap.Int64("session_id", key.sessionID),
					zap.Int("requeued", len(rest)),
				)
				return
			}
			m.mu.Unlock()
			m.log.Debug("Flush pending push failed; backoff retry",
				zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
				zap.Int64("session_id", key.sessionID),
				zap.Int("attempt", attempt+1),
				zap.Int("requeued", len(rest)),
				zap.Error(err),
			)
			time.AfterFunc(flushRetryBackoff*time.Duration(attempt+1), func() {
				m.runFlush(c, key, owner, attempt+1)
			})
			return
		}
		// This batch is done; loop back to re-take staged pushes added during the drain.
	}
}

// ReceivesUpdatesForAuthKey reports whether the connection for the given raw
// auth_key_id + session_id is fully ready: it both receives proactive updates and
// has successfully established the channel membership push route. Returns false
// when there is no active connection. Returning false lets the RPC-driven flag set
// short-circuit through and the next RPC retry the membership sync—otherwise a
// session whose sync failed would silently miss supergroup pushes in the state of
// "flagged ready but byMemberChannel missing".
func (m *SessionManager) ReceivesUpdatesForAuthKey(authKeyID [8]byte, sessionID int64) bool {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: authKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	_, hasProfile := c.LayerProfile()
	return hasProfile && c.receivesUpdates.Load() && c.membershipsSynced.Load()
}

// BeginSessionUpdatesActivation claims the readiness transition for the
// current physical connection. Ordinary startup RPCs race here before they
// register delivery hooks, so at most one of them can enqueue the expensive
// channel-membership synchronization. Cursor commits remain request-owned.
func (m *SessionManager) BeginSessionUpdatesActivation(authKeyID [8]byte, sessionID int64) (uint64, bool) {
	if m == nil {
		return 0, false
	}
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.bySession[key]
	if c == nil || c.isRetired() {
		return 0, false
	}
	if _, hasProfile := c.LayerProfile(); hasProfile && c.receivesUpdates.Load() && c.membershipsSynced.Load() {
		return 0, false
	}
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if c.updatesActivationToken != 0 {
		// A pending FIFO flush owns the activation until it reaches a terminal
		// outcome. Never lease-steal while that ordered delivery is in progress.
		if m.flushing[key] || now.Sub(c.updatesActivationAt) < updatesActivationClaimTTL {
			return 0, false
		}
	}
	m.updatesActivationSeq++
	if m.updatesActivationSeq == 0 {
		m.updatesActivationSeq++
	}
	c.updatesActivationToken = m.updatesActivationSeq
	c.updatesActivationAt = now
	return c.updatesActivationToken, true
}

// EndSessionUpdatesActivation releases only the token owned by the caller and
// only on the same current physical Conn. If SetReceivesUpdates started an
// ordered pending flush, that flush retains and releases the claim itself.
func (m *SessionManager) EndSessionUpdatesActivation(authKeyID [8]byte, sessionID int64, token uint64) {
	if m == nil || token == 0 {
		return
	}
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.bySession[key]
	if c == nil || c.updatesActivationToken != token || m.flushing[key] {
		return
	}
	m.clearUpdatesActivationLocked(c)
}

// BeginSessionBootstrapProbe claims the first durable bootstrap-job lookup for
// the current physical connection generation. Unlike updates activation, this
// is completed only by a delivered getState/getDifference baseline.
func (m *SessionManager) BeginSessionBootstrapProbe(authKeyID [8]byte, sessionID int64) (uint64, bool) {
	if m == nil {
		return 0, false
	}
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.bySession[key]
	if c == nil || c.isRetired() || c.bootstrapProbed || c.bootstrapProbeToken != 0 {
		return 0, false
	}
	m.bootstrapProbeSeq++
	if m.bootstrapProbeSeq == 0 {
		m.bootstrapProbeSeq++
	}
	c.bootstrapProbeToken = m.bootstrapProbeSeq
	return c.bootstrapProbeToken, true
}

// EndSessionBootstrapProbe completes or releases only the token on the same
// current Conn. A delayed callback from a replaced connection cannot mutate the
// replacement's one-shot state.
func (m *SessionManager) EndSessionBootstrapProbe(authKeyID [8]byte, sessionID int64, token uint64, success bool) {
	if m == nil || token == 0 {
		return
	}
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.bySession[key]
	if c == nil || c.bootstrapProbeToken != token {
		return
	}
	c.bootstrapProbeToken = 0
	if success {
		c.bootstrapProbed = true
	}
}

func (m *SessionManager) clearUpdatesActivationLocked(c *Conn) {
	if c == nil {
		return
	}
	c.updatesActivationToken = 0
	c.updatesActivationAt = time.Time{}
}

func (m *SessionManager) clearBootstrapProbeLocked(c *Conn) {
	if c == nil {
		return
	}
	c.bootstrapProbeToken = 0
	c.bootstrapProbed = false
}

// SetReceivesUpdatesForAuthKey marks whether the given raw auth_key_id + session_id receives proactive updates.
func (m *SessionManager) SetReceivesUpdatesForAuthKey(authKeyID [8]byte, sessionID int64, receives bool) {
	m.mu.Lock()
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	if !ok {
		m.mu.Unlock()
		return
	}
	owner, start := m.setReceivesUpdatesLocked(c, key, receives)
	m.mu.Unlock()

	if start {
		go m.runFlush(c, key, owner, 0)
	}
}

// PushToSessionForAuthKey pushes a message to the given raw auth_key_id + session_id.
func (m *SessionManager) PushToSessionForAuthKey(ctx context.Context, authKeyID [8]byte, sessionID int64, t proto.MessageType, msg tg.UpdatesClass) error {
	m.mu.RLock()
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	if !ok {
		m.mu.RUnlock()
		return ErrSessionNotFound
	}
	ready := c.receivesUpdates.Load()
	m.mu.RUnlock()
	if ready {
		updates, err := newLayerUpdatesFanoutContext(ctx, msg)
		if err != nil {
			return err
		}
		encoded, err := updates.prepareForConn(ctx, c)
		if err != nil {
			return err
		}
		return c.SendEncoded(ctx, t, encoded)
	}
	return m.queueOrSendPrepared(ctx, key, t, msg)
}

func (m *SessionManager) queueOrSendPrepared(ctx context.Context, key sessionKey, t proto.MessageType, msg tg.UpdatesClass) error {
	getUpdates := onceLayerUpdatesFanout(ctx, msg)
	updates, reservation, err := m.preparePendingPush(getUpdates)
	if err != nil {
		return err
	}
	defer reservation.release()

	m.mu.Lock()
	c, ok := m.bySession[key]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	if !c.receivesUpdates.Load() {
		_ = m.queuePreparedLocked(key, t, updates, reservation)
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	encoded, err := updates.prepareForConn(ctx, c)
	if err != nil {
		return err
	}
	return c.SendEncoded(ctx, t, encoded)
}

// PushToSessionForAuthKeyImmediate immediately pushes a message to the given raw
// auth_key_id + session_id.
//
// It does not wait for the session to become updates-ready and does not write the
// pending queue. It is only used for pre-login handshake signals (such as
// updateLoginToken): such messages are themselves the trigger for the client to
// continue completing login, and going through the ordinary durable update queue
// would stall at the stage where the client has not yet called updates.getState.
func (m *SessionManager) PushToSessionForAuthKeyImmediate(ctx context.Context, authKeyID [8]byte, sessionID int64, t proto.MessageType, msg tg.UpdatesClass) error {
	m.mu.RLock()
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	m.mu.RUnlock()
	if !ok {
		return ErrSessionNotFound
	}
	updates, err := newLayerUpdatesFanoutContext(ctx, msg)
	if err != nil {
		return err
	}
	encoded, err := updates.prepareForConn(ctx, c)
	if err != nil {
		return err
	}
	return c.SendBestEffortEncoded(ctx, t, encoded, 2*time.Second)
}

// PushToUserExceptAuthKeySession pushes to all active connections of a user, skipping the given raw auth_key + session.
func (m *SessionManager) PushToUserExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass) (int, error) {
	return m.pushToUser(ctx, userID, &excludeAuthKeyID, excludeSessionID, t, msg)
}

// PushToUserAuthKey delivers msg specifically to the ready connection bound to the
// concrete device businessAuthKeyID and belonging to userID (the anchor for
// device-level secret-chat delivery). The index goes through byBusinessAuthKey
// (via businessAuthKeyCandidatesLocked, compatible with temp-key/PFS connections),
// not byAuthKey (the raw index would miss temp-key devices). Not-ready connections
// are skipped and do not enter pending—secret-chat messages are durable in the qts
// queue, and offline devices catch up via getDifference (online push is just an
// accelerator). The c.userID recheck prevents cross-account leaks.
func (m *SessionManager) PushToUserAuthKey(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg tg.UpdatesClass) (int, error) {
	// Secret-chat qts is the durable source of truth, so online delivery is an accelerator just
	// like account pts fan-out.  Do not synchronously wait for every PFS/raw connection's socket.
	return m.pushToBusinessAuthKeyBestEffort(ctx, userID, businessAuthKeyID, 0, t, msg, 2*time.Second)
}

// PushToUserAuthKeyTransient is the transient (typing) best-effort version of PushToUserAuthKey.
func (m *SessionManager) PushToUserAuthKeyTransient(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	return m.pushToBusinessAuthKeyBestEffort(ctx, userID, businessAuthKeyID, 0, t, msg, timeout)
}

func (m *SessionManager) PushToUserAuthKeyTransientCompatible(ctx context.Context, userID int64, businessAuthKeyID [8]byte, semantic tlprofile.SemanticID, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	return m.pushToBusinessAuthKeyBestEffort(ctx, userID, businessAuthKeyID, semantic, t, msg, timeout)
}

// PushToUserExceptBusinessAuthKey delivers the update to the account's other
// devices, precisely excluding all raw/temp/PFS connections under the same
// permanent business auth key. The secret-chat accept uses it to make the device
// that lost the race converge to discarded, while ensuring the winning device's
// other connections do not wrongly delete the just-established secret chat.
func (m *SessionManager) PushToUserExceptBusinessAuthKey(ctx context.Context, userID int64, excludeBusinessAuthKeyID [8]byte, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	getUpdates := onceLayerUpdatesFanout(ctx, msg)
	return m.pushToUserWithSender(ctx, userID, nil, 0, &excludeBusinessAuthKeyID, 0, t, getUpdates, false, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		updates, err := getUpdates()
		if err != nil {
			return err
		}
		encoded, err := updates.prepareForConn(ctx, c)
		if err != nil {
			return err
		}
		return c.SendBestEffortEncoded(ctx, t, encoded, timeout)
	})
}

func (m *SessionManager) pushToBusinessAuthKeyBestEffort(ctx context.Context, userID int64, businessAuthKeyID [8]byte, semantic tlprofile.SemanticID, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	if ctx != nil && ctx.Err() != nil {
		return 0, ctx.Err()
	}
	sendCtx := context.Background()
	if ctx != nil {
		sendCtx = context.WithoutCancel(ctx)
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
			deadline = ctxDeadline
		}
	}
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithDeadline(sendCtx, deadline)
		defer cancel()
	}
	getUpdates := onceLayerUpdatesFanout(sendCtx, msg)
	return m.pushToBusinessAuthKey(ctx, userID, businessAuthKeyID, semantic, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		updates, err := getUpdates()
		if err != nil {
			return err
		}
		encoded, err := updates.prepareForConn(sendCtx, c)
		if err != nil {
			return err
		}
		remaining := timeout
		if !deadline.IsZero() {
			remaining = time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
		}
		return c.SendBestEffortEncoded(sendCtx, t, encoded, remaining)
	})
}

func (m *SessionManager) pushToBusinessAuthKey(ctx context.Context, userID int64, businessAuthKeyID [8]byte, semantic tlprofile.SemanticID, send func(*Conn) error) (int, error) {
	m.mu.Lock()
	candidates := m.businessAuthKeyCandidatesLocked(businessAuthKeyID)
	conns := make([]*Conn, 0, len(candidates))
	for _, c := range candidates {
		if c.userID.Load() != userID {
			continue
		}
		if !c.receivesUpdates.Load() {
			// Not ready: secret-chat messages are caught up via getDifference and typing
			// is dropped directly—neither enters pending.
			continue
		}
		if !sessionSupportsSemantic(c, semantic) {
			continue
		}
		conns = append(conns, c)
	}
	m.mu.Unlock()
	var firstErr error
	sent := 0
	for _, c := range conns {
		// Recheck identity before sending outside the lock, preventing a cross-account
		// leak from a concurrent rebind after collection.
		if c.userID.Load() != userID {
			continue
		}
		if err := send(c); err != nil {
			if isOutboundStaleLayerEpoch(err) {
				// A concurrent correction invalidated this prepared push, not the
				// connection. Durable qts/difference remains the source of truth.
				continue
			}
			if isOutboundLayerProfileError(err) {
				c.dropSlowConsumer()
				continue
			}
			if errors.Is(err, ErrOutboundTrackedBudget) {
				// Shared process pressure is not evidence that this particular socket is
				// slow. Skip this online accelerator; durable qts/difference is the truth.
				continue
			}
			if errors.Is(err, ErrOutboundQueueFull) {
				c.dropSlowConsumer()
				continue
			}
			if errors.Is(err, ErrConnClosed) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sent++
	}
	return sent, firstErr
}

func (m *SessionManager) pushToUser(ctx context.Context, userID int64, excludeAuthKeyID *[8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass) (int, error) {
	getUpdates := onceLayerUpdatesFanout(ctx, msg)
	return m.pushToUserWithSender(ctx, userID, excludeAuthKeyID, excludeSessionID, nil, 0, t, getUpdates, true, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		updates, err := getUpdates()
		if err != nil {
			return err
		}
		encoded, err := updates.prepareForConn(ctx, c)
		if err != nil {
			return err
		}
		return c.SendEncoded(ctx, t, encoded)
	})
}

// PushToUserTransientExceptAuthKeySession pushes transient (short-lived, not
// writing the durable log) updates such as typing / presence. The key difference
// from an ordinary push: when a session is not ready (receivesUpdates=false) that
// connection is skipped directly and does not enter pending—transient data cannot
// be caught up by getDifference, and once ready is rebuilt from the getState
// snapshot / the next state change; hoarding stale transient data is both
// meaningless and would be wrongly treated as a "durable fallback" discard by
// pending's aging/overflow/exhausted retries. It uses a best-effort send and does
// not block the caller.
func (m *SessionManager) PushToUserTransientExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	getUpdates := onceLayerUpdatesFanout(ctx, msg)
	return m.pushToUserWithSender(ctx, userID, &excludeAuthKeyID, excludeSessionID, nil, 0, t, getUpdates, false, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		updates, err := getUpdates()
		if err != nil {
			return err
		}
		encoded, err := updates.prepareForConn(ctx, c)
		if err != nil {
			return err
		}
		return c.SendBestEffortEncoded(ctx, t, encoded, timeout)
	})
}

func (m *SessionManager) PushToUserTransientCompatible(ctx context.Context, userID int64, semantic tlprofile.SemanticID, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	getUpdates := onceLayerUpdatesFanout(ctx, msg)
	return m.pushToUserWithSender(ctx, userID, nil, 0, nil, semantic, t, getUpdates, false, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		updates, err := getUpdates()
		if err != nil {
			return err
		}
		encoded, err := updates.prepareForConn(ctx, c)
		if err != nil {
			return err
		}
		return c.SendBestEffortEncoded(ctx, t, encoded, timeout)
	})
}

func (m *SessionManager) PushToUserExceptAuthKeySessionBestEffort(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	return m.pushToUserBestEffort(ctx, userID, &excludeAuthKeyID, excludeSessionID, t, msg, timeout)
}

func (m *SessionManager) pushToUserBestEffort(ctx context.Context, userID int64, excludeAuthKeyID *[8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	if ctx != nil && ctx.Err() != nil {
		return 0, ctx.Err()
	}
	sendCtx := context.Background()
	if ctx != nil {
		sendCtx = context.WithoutCancel(ctx)
	}
	// timeout is the wait budget for the whole fan-out, not one per session. Healthy
	// connections always take SendBestEffortEncoded's non-blocking fast path first;
	// after the budget is exhausted remaining=0, the fast path is still attempted,
	// but no more serial waiting is done for subsequent slow connections.
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
			deadline = ctxDeadline
		}
	}
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		sendCtx, cancel = context.WithDeadline(sendCtx, deadline)
		defer cancel()
	}
	getUpdates := onceLayerUpdatesFanout(sendCtx, msg)
	return m.pushToUserWithSender(ctx, userID, excludeAuthKeyID, excludeSessionID, nil, 0, t, getUpdates, true, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		updates, err := getUpdates()
		if err != nil {
			return err
		}
		encoded, err := updates.prepareForConn(sendCtx, c)
		if err != nil {
			return err
		}
		remaining := timeout
		if !deadline.IsZero() {
			remaining = time.Until(deadline)
			if remaining < 0 {
				remaining = 0
			}
		}
		return c.SendBestEffortEncoded(sendCtx, t, encoded, remaining)
	})
}

func newLayerUpdatesFanoutContext(ctx context.Context, msg tg.UpdatesClass) (*layerUpdatesFanout, error) {
	var updates *layerUpdatesFanout
	err := withOutboundEncodeSlot(ctx, nil, func() error {
		var err error
		updates, err = newLayerUpdatesFanout(msg)
		return err
	})
	return updates, err
}

func onceLayerUpdatesFanout(ctx context.Context, msg tg.UpdatesClass) func() (*layerUpdatesFanout, error) {
	var (
		once    sync.Once
		updates *layerUpdatesFanout
		err     error
	)
	return func() (*layerUpdatesFanout, error) {
		once.Do(func() {
			updates, err = newLayerUpdatesFanoutContext(ctx, msg)
		})
		return updates, err
	}
}

func (m *SessionManager) pushToUserWithSender(ctx context.Context, userID int64, excludeAuthKeyID *[8]byte, excludeSessionID int64, excludeBusinessAuthKeyID *[8]byte, semantic tlprofile.SemanticID, t proto.MessageType, getUpdates func() (*layerUpdatesFanout, error), queueWhenNotReady bool, send func(*Conn) error) (int, error) {
	// push fan-out is one of the connection layer's hottest paths: debug log field
	// construction (including auth_key hex formatting) is evaluated even when debug
	// is off, so check the level once and log on demand.
	debug := m.log.Core().Enabled(zapcore.DebugLevel)
	// Fast path: in steady state all target connections are ready (or transient
	// directly skips the not-ready ones), and collecting connections is read-only
	// and does not write, so a shared read lock suffices throughout, avoiding taking
	// an exclusive write lock to serialize the whole registry on every push. Only
	// when a durable push hits a not-ready connection (needing to write pending)
	// does it fall back to a write lock and rescan.
	m.mu.RLock()
	total := len(m.byUser[userID])
	conns := make([]*Conn, 0, total)
	queued := 0
	dropped := 0
	excluded := 0
	skipped := 0
	needQueue := false
	for _, c := range m.byUser[userID] {
		if shouldExcludeSession(c, excludeAuthKeyID, excludeSessionID) || shouldExcludeBusinessAuthKey(c, excludeBusinessAuthKeyID) {
			excluded++
			continue
		}
		if !sessionSupportsSemantic(c, semantic) {
			skipped++
			continue
		}
		if !c.receivesUpdates.Load() {
			if !queueWhenNotReady {
				// transient (typing/presence): dropped if not ready, not entering pending.
				// These updates do not write the durable log and cannot be caught up by
				// getDifference; once ready they are rebuilt from the getState
				// snapshot/next state change.
				skipped++
				continue
			}
			needQueue = true
			break
		}
		conns = append(conns, c)
	}
	m.mu.RUnlock()
	if needQueue {
		// TL encoding and the process-wide pending-byte reservation may be expensive or
		// briefly block on the global encode gate. Do both before taking SessionManager.mu,
		// then share the immutable body across every not-ready session found by the re-scan.
		pendingUpdates, pendingReservation, pendingErr := m.preparePendingPush(getUpdates)
		// Full rescan under the write lock (state may change between releasing the read
		// lock and here; the rescan result is authoritative).
		conns = conns[:0]
		queued, dropped, excluded, skipped = 0, 0, 0, 0
		m.mu.Lock()
		total = len(m.byUser[userID])
		for key, c := range m.byUser[userID] {
			if shouldExcludeSession(c, excludeAuthKeyID, excludeSessionID) || shouldExcludeBusinessAuthKey(c, excludeBusinessAuthKeyID) {
				excluded++
				continue
			}
			if !sessionSupportsSemantic(c, semantic) {
				skipped++
				continue
			}
			if !c.receivesUpdates.Load() {
				if !queueWhenNotReady {
					skipped++
					continue
				}
				if pendingErr == nil && m.queuePreparedLocked(key, t, pendingUpdates, pendingReservation) {
					queued++
					if debug {
						m.log.Debug("Push queued (session not updates-ready)",
							zap.Int64("user_id", userID),
							zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
							zap.Int64("session_id", key.sessionID),
						)
					}
				} else {
					dropped++
					if debug {
						m.log.Debug("Push dropped (stale pending; durable log covers)",
							zap.Int64("user_id", userID),
							zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
							zap.Int64("session_id", key.sessionID),
						)
					}
				}
				continue
			}
			conns = append(conns, c)
		}
		m.mu.Unlock()
		if pendingReservation != nil {
			pendingReservation.release() // drop producer ref; queued entries own the body now.
		}
		if pendingErr != nil && debug {
			m.log.Debug("Drop pending pushes outside byte budget",
				zap.Int64("user_id", userID),
				zap.Error(pendingErr),
			)
		}
	}

	var firstErr error
	sent := 0
	for _, c := range conns {
		// Recheck identity before sending outside the lock: between collecting conns
		// and now, a connection may have been concurrently rebound (logout/account
		// switch, bindUserLocked's c.userID.Swap). Without the recheck, an update
		// belonging to userID would be delivered to a connection that has changed
		// hands, constituting a cross-account leak. Consistent with the same defense
		// in AddUserChannelMembership.
		if c.userID.Load() != userID {
			continue
		}
		if shouldExcludeBusinessAuthKey(c, excludeBusinessAuthKeyID) {
			continue
		}
		if err := send(c); err != nil {
			if isOutboundStaleLayerEpoch(err) {
				// Do not classify profile correction as slow-consumer evidence.
				dropped++
				continue
			}
			if isOutboundLayerProfileError(err) {
				c.dropSlowConsumer()
				dropped++
				continue
			}
			if errors.Is(err, ErrOutboundTrackedBudget) {
				// Do not turn pressure owned by other sockets into a reconnect storm on
				// healthy recipients. The durable event remains recoverable by difference.
				dropped++
				continue
			}
			// For durable/best-effort fan-out, a full queue means this socket has become
			// a slow consumer. Remove it immediately and treat it as offline: its error
			// must not cause a wholesale retry of outbox rows already delivered to
			// healthy sessions. This session's durable gap is recovered by getDifference.
			if errors.Is(err, ErrOutboundQueueFull) {
				c.dropSlowConsumer()
				if debug {
					m.log.Debug("Drop slow outbound consumer",
						zap.Int64("user_id", userID),
						zap.String("auth_key_id", sessionKeyLog(c.authKeyID)),
						zap.Int64("session_id", c.sessionID),
					)
				}
				continue
			}
			if errors.Is(err, ErrConnClosed) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			if debug {
				m.log.Debug("Push to conn failed",
					zap.Int64("user_id", userID),
					zap.String("auth_key_id", sessionKeyLog(c.authKeyID)),
					zap.Int64("session_id", c.sessionID),
					zap.Error(err),
				)
			}
			continue
		}
		sent++
		if debug {
			m.log.Debug("Push to conn ok",
				zap.Int64("user_id", userID),
				zap.String("auth_key_id", sessionKeyLog(c.authKeyID)),
				zap.Int64("session_id", c.sessionID),
			)
		}
	}
	if debug {
		if total == 0 {
			m.log.Debug("Push to user: no active conns", zap.Int64("user_id", userID))
		} else if excluded > 0 || queued > 0 || dropped > 0 || skipped > 0 || sent < len(conns) {
			m.log.Debug("Push to user summary",
				zap.Int64("user_id", userID),
				zap.Int("conns", total),
				zap.Int("sent", sent),
				zap.Int("queued", queued),
				zap.Int("dropped", dropped),
				zap.Int("skipped_transient", skipped),
				zap.Int("excluded", excluded),
			)
		}
	}
	return sent + queued, firstErr
}

// ActiveRawAuthKeyIDs returns a deduplicated snapshot of the raw auth_key_ids
// actually used by current physical connections. maintenance uses it to protect
// long-lived connections that "have built a key but not yet logged in" from being
// deleted by the orphan GC; business/temp→perm keys cannot substitute for this, or
// active temp connections could still be wrongly deleted.
func (m *SessionManager) ActiveRawAuthKeyIDs() [][8]byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[[8]byte]struct{}, len(m.bySession))
	out := make([][8]byte, 0, len(m.byAuthKey))
	for key := range m.bySession {
		if _, ok := seen[key.authKeyID]; ok {
			continue
		}
		seen[key.authKeyID] = struct{}{}
		out = append(out, key.authKeyID)
	}
	return out
}

// IsUserOnline returns whether userID has at least one active connection.
func (m *SessionManager) IsUserOnline(userID int64) bool {
	if userID == 0 {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.byUser[userID]) > 0
}

// OnlineUserIDsForCandidates filters an explicit candidate set against the
// active user index. It avoids exporting or sorting the whole online map.
func (m *SessionManager) OnlineUserIDsForCandidates(candidateUserIDs []int64, limit int) []int64 {
	if len(candidateUserIDs) == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]int64, 0, min(len(candidateUserIDs), positiveLimitOrLen(limit, len(candidateUserIDs))))
	seen := make(map[int64]struct{}, len(candidateUserIDs))
	for _, userID := range candidateUserIDs {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		if len(m.byUser[userID]) == 0 {
			continue
		}
		out = append(out, userID)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// TrackChannelInterest replaces the channel viewer set for one live session.
// Realtime transient fan-out uses this as the current active-viewer candidate
// set; durable channel updates use the broader membership index instead.
func (m *SessionManager) TrackChannelInterest(rawAuthKeyID [8]byte, sessionID, userID int64, channelIDs []int64) {
	if userID == 0 {
		return
	}
	key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.bySession[key]
	if !ok || c.userID.Load() != userID {
		return
	}
	m.clearChannelInterestsLocked(key)
	if len(channelIDs) == 0 {
		return
	}
	m.trackChannelIndexLocked(m.byChannel, m.bySessionChannels, key, userID, channelIDs)
}

// ClearChannelInterest removes the active-viewer channel set for one live
// session while leaving its joined-channel membership index intact.
func (m *SessionManager) ClearChannelInterest(rawAuthKeyID [8]byte, sessionID, userID int64) {
	if userID == 0 {
		return
	}
	key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.bySession[key]
	if !ok || c.userID.Load() != userID {
		return
	}
	m.clearChannelInterestsLocked(key)
}

// OnlineChannelUserIDs returns users with active sessions that have recently
// proven current interest in channelID. The result is intentionally unsorted and bounded.
func (m *SessionManager) OnlineChannelUserIDs(channelID int64, limit int) []int64 {
	return m.onlineChannelUsers(m.byChannel, channelID, limit)
}

// RefreshChannelSubscription refreshes one public-channel passive-update
// subscription without replacing the other short-polled channels of the same
// session. The index is runtime-only and bounded to the official client limit.
func (m *SessionManager) RefreshChannelSubscription(rawAuthKeyID [8]byte, sessionID, userID, channelID int64, ttl time.Duration) {
	if userID == 0 || channelID == 0 {
		return
	}
	if ttl <= 0 {
		ttl = defaultChannelSubscriptionTTL
	} else if ttl > maxChannelSubscriptionTTL {
		ttl = maxChannelSubscriptionTTL
	}
	key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
	now := time.Now().UnixNano()
	expiresAt := now + int64(ttl)
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.bySession[key]
	if !ok || c.userID.Load() != userID {
		return
	}
	m.pruneSessionSubscriptionsLocked(key, now)
	channels := m.bySessionSubscriptions[key]
	if channels == nil {
		channels = make(map[int64]int64, 1)
		m.bySessionSubscriptions[key] = channels
	}
	if _, exists := channels[channelID]; !exists && len(channels) >= maxChannelSubscriptionsPerSession {
		m.log.Warn("Channel passive subscription ignored at per-session cap",
			zap.String("auth_key_id", sessionKeyLog(rawAuthKeyID)),
			zap.Int64("session_id", sessionID),
			zap.Int64("channel_id", channelID),
			zap.Int("cap", maxChannelSubscriptionsPerSession))
		return
	}
	channels[channelID] = expiresAt
	sessions := m.bySubscribedChannel[channelID]
	if sessions == nil {
		sessions = make(map[sessionKey]channelSubscription)
		m.bySubscribedChannel[channelID] = sessions
	}
	sessions[key] = channelSubscription{userID: userID, expiresAt: expiresAt}
}

// OnlineChannelSubscriberUserIDs returns users for which at least one live
// session still holds an unexpired short-poll subscription. The user is
// deduplicated because passive updates are subsequently pushed account-wide.
func (m *SessionManager) OnlineChannelSubscriberUserIDs(channelID int64, limit int) []int64 {
	return m.onlineChannelSubscriberUserIDsExcluding(channelID, nil, limit)
}

func (m *SessionManager) OnlineChannelSubscriberUserIDsExcluding(channelID int64, exclude map[int64]struct{}, limit int) []int64 {
	return m.onlineChannelSubscriberUserIDsExcluding(channelID, exclude, limit)
}

func (m *SessionManager) onlineChannelSubscriberUserIDsExcluding(channelID int64, exclude map[int64]struct{}, limit int) []int64 {
	if channelID == 0 {
		return nil
	}
	now := time.Now().UnixNano()
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := m.bySubscribedChannel[channelID]
	if len(sessions) == 0 {
		return nil
	}
	out := make([]int64, 0, positiveLimitOrLen(limit, len(sessions)))
	seen := make(map[int64]struct{}, positiveLimitOrLen(limit, len(sessions)))
	for key, subscription := range sessions {
		if subscription.expiresAt <= now {
			m.removeChannelSubscriptionLocked(key, channelID)
			continue
		}
		if subscription.userID == 0 {
			continue
		}
		if _, ok := exclude[subscription.userID]; ok {
			continue
		}
		c, ok := m.bySession[key]
		if !ok || c.userID.Load() != subscription.userID {
			m.removeChannelSubscriptionLocked(key, channelID)
			continue
		}
		if _, ok := seen[subscription.userID]; ok {
			continue
		}
		seen[subscription.userID] = struct{}{}
		out = append(out, subscription.userID)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// ChannelMembershipGeneration returns the membership index revision number of that
// session. The full-sync party must sample it *before* reading the persistent
// membership list and carry it back for comparison via
// SetSessionChannelMemberships; when the session is offline it returns 0 (a later
// Set also gives up because it cannot find the connection).
func (m *SessionManager) ChannelMembershipGeneration(rawAuthKeyID [8]byte, sessionID int64) int64 {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	return c.membershipGen.Load()
}

// SetSessionChannelMemberships replaces the joined-channel index for one
// updates-ready session. This index is broader than TrackChannelInterest and is
// used for durable channel updates such as new/edit/delete message.
//
// expectedGen is the revision number the caller sampled via
// ChannelMembershipGeneration before reading the persistent membership list. If the
// revision has changed on commit (an incremental revision from join/leave/kick or a
// wholesale clear happened inside the read window), a full replacement would
// overwrite the in-window increment—in that case it switches to a union merge
// (retaining incremental Adds; stale entries merged back are caught by the
// pre-fan-out PG active recheck) and keeps membershipsSynced=false, so the next RPC
// runs a fresh full sync to converge.
func (m *SessionManager) SetSessionChannelMemberships(rawAuthKeyID [8]byte, sessionID, userID int64, channelIDs []int64, expectedGen int64) {
	key := sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.bySession[key]
	if !ok {
		return
	}
	if userID == 0 || c.userID.Load() != userID {
		m.clearChannelMembershipsLocked(c, key)
		c.membershipsSynced.Store(false)
		return
	}
	if c.membershipGen.Load() != expectedGen {
		c.membershipsSynced.Store(false)
		m.trackChannelIndexLocked(m.byMemberChannel, m.bySessionMembers, key, userID, channelIDs)
		m.log.Debug("Channel membership sync raced with incremental updates; merged and kept unsynced",
			zap.String("auth_key_id", sessionKeyLog(rawAuthKeyID)),
			zap.Int64("session_id", sessionID),
		)
		return
	}
	m.clearChannelMembershipsLocked(c, key)
	c.membershipsSynced.Store(false)
	m.trackChannelIndexLocked(m.byMemberChannel, m.bySessionMembers, key, userID, channelIDs)
	c.membershipsSynced.Store(true)
}

// AddUserChannelMembership adds channelID to every live session for userID.
// It is called after successful join/invite approval paths.
func (m *SessionManager) AddUserChannelMembership(userID, channelID int64) {
	if userID == 0 || channelID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, c := range m.byUser[userID] {
		if c == nil || c.userID.Load() != userID {
			continue
		}
		c.membershipGen.Add(1)
		m.trackChannelIndexLocked(m.byMemberChannel, m.bySessionMembers, key, userID, []int64{channelID})
	}
}

// RemoveUserChannelMembership removes channelID from every live session for userID.
// It is called after leave/kick/ban/delete paths.
func (m *SessionManager) RemoveUserChannelMembership(userID, channelID int64) {
	if userID == 0 || channelID == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, c := range m.byUser[userID] {
		if c != nil {
			c.membershipGen.Add(1)
		}
		m.removeChannelIndexLocked(m.byMemberChannel, m.bySessionMembers, key, channelID)
	}
}

// OnlineChannelMemberUserIDs returns users with active sessions that are indexed
// as joined members of channelID. The result is intentionally unsorted; callers
// still verify business membership before pushing.
func (m *SessionManager) OnlineChannelMemberUserIDs(channelID int64, limit int) []int64 {
	return m.onlineChannelUsers(m.byMemberChannel, channelID, limit)
}

// OnlineChannelMemberUserIDsExcluding returns the user ids of a channel's online
// members that are not in the exclude set, used for the UpdateChannelTooLong nudge
// when online members exceed the cap (P0-8): the full payload has already been
// delivered to the exclude set (in-cap members), and the remaining online members
// only get a cheap nudge prompting their getChannelDifference. A single RLock
// snapshot; the caller builds exclude from "recipients that already got the full
// payload", so the same user does not receive both the payload and the nudge—this
// naturally avoids boundary double-send/missed-send between two independent cap
// calls (design §8-D3/D32). limit prevents an unbounded nudge storm.
// No PG active recheck is done: byMemberChannel is already maintained on
// join/leave/kick; the nudge is cheap and idempotent, and extra nudges to members
// who just left are harmless (their getChannelDifference has its own access check).
func (m *SessionManager) OnlineChannelMemberUserIDsExcluding(channelID int64, exclude map[int64]struct{}, limit int) []int64 {
	if channelID == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	sessions := m.byMemberChannel[channelID]
	if len(sessions) == 0 {
		return nil
	}
	out := make([]int64, 0, positiveLimitOrLen(limit, len(sessions)))
	seen := make(map[int64]struct{}, len(sessions))
	for key, userID := range sessions {
		if userID == 0 {
			continue
		}
		if _, ok := exclude[userID]; ok {
			continue
		}
		if _, ok := m.bySession[key]; !ok {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		out = append(out, userID)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// OnlineChannelIDsSnapshot returns every channel with at least one live joined-member session or
// unexpired passive subscriber in strictly ascending order. The global SessionManager lock is held
// only while copying map keys and pruning expired subscription entries;
// sorting and all recovery database work happen after unlock. The fixed saturation-recovery actor
// is the sole caller, so its exceptional-path temporary memory is one int64 slice (peak about 8*C
// bytes) rather than repeated O(C) scans under the connection/membership lock.
func (m *SessionManager) OnlineChannelIDsSnapshot() []int64 {
	m.mu.Lock()
	now := time.Now().UnixNano()
	seen := make(map[int64]struct{}, len(m.byMemberChannel)+len(m.bySubscribedChannel))
	out := make([]int64, 0, len(m.byMemberChannel)+len(m.bySubscribedChannel))
	for channelID, sessions := range m.byMemberChannel {
		if channelID <= 0 || len(sessions) == 0 {
			continue
		}
		live := false
		for key := range sessions {
			if _, ok := m.bySession[key]; ok {
				live = true
				break
			}
		}
		if !live {
			continue
		}
		seen[channelID] = struct{}{}
		out = append(out, channelID)
	}
	for channelID, sessions := range m.bySubscribedChannel {
		if channelID <= 0 || len(sessions) == 0 {
			continue
		}
		live := false
		for key, subscription := range sessions {
			if subscription.expiresAt <= now {
				m.removeChannelSubscriptionLocked(key, channelID)
				continue
			}
			c, ok := m.bySession[key]
			if !ok || c.userID.Load() != subscription.userID {
				m.removeChannelSubscriptionLocked(key, channelID)
				continue
			}
			live = true
		}
		if !live {
			continue
		}
		if _, exists := seen[channelID]; exists {
			continue
		}
		out = append(out, channelID)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (m *SessionManager) onlineChannelUsers(index map[int64]map[sessionKey]int64, channelID int64, limit int) []int64 {
	if channelID == 0 {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	sessions := index[channelID]
	if len(sessions) == 0 {
		return nil
	}
	out := make([]int64, 0, positiveLimitOrLen(limit, len(sessions)))
	seen := make(map[int64]struct{}, len(sessions))
	for key, userID := range sessions {
		if userID == 0 {
			continue
		}
		if _, ok := m.bySession[key]; !ok {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		out = append(out, userID)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (m *SessionManager) removeLocked(c *Conn, dropPending bool) int64 {
	key := connSessionKey(c)
	if m.bySession[key] != c {
		return 0
	}
	delete(m.bySession, key)
	removeConnIndex(m.byAuthKey, c.authKeyID, c.sessionID)
	if businessAuthKeyID, resolved := c.BusinessAuthKeyID(); resolved {
		removeBusinessAuthKeyIndex(m.byBusinessAuthKey, businessAuthKeyID, key)
	}
	uid := c.userID.Load()
	if uid != 0 {
		removeUserIndex(m.byUser, uid, key)
	}
	m.clearSessionChannelIndexesLocked(c, key)
	m.clearUpdatesActivationLocked(c)
	if dropPending {
		m.deletePendingLocked(key)
	}
	delete(m.flushing, key)
	return uid
}

// retireConnLocked closes every admission/producer gate before the Conn leaves
// manager indexes. Callers may close the physical transport and wait outside m.mu,
// but no pointer collected by an earlier fan-out can enqueue after this returns.
func (m *SessionManager) retireConnLocked(c *Conn, dropPending bool) int64 {
	if c == nil {
		return 0
	}
	c.beginTerminalShutdown()
	return m.removeLocked(c, dropPending)
}

func (m *SessionManager) retireClaimLocked(key sessionKey, c *Conn, dropPending bool) {
	if c == nil || m.claims[key] != c {
		return
	}
	c.beginTerminalShutdown()
	m.removeClaimLocked(key, c)
	if dropPending && m.bySession[key] == nil {
		m.deletePendingLocked(key)
	}
	delete(m.flushing, key)
}

func (m *SessionManager) claimCountForAuthLocked(authKeyID [8]byte) int {
	return len(m.claimsByAuth[authKeyID])
}

func (m *SessionManager) oldestAuthOwnerLocked(authKeyID [8]byte, exclude *Conn) (sessionKey, *Conn, bool) {
	var (
		oldestKey sessionKey
		oldest    *Conn
		isClaim   bool
	)
	for sessionID, candidate := range m.byAuthKey[authKeyID] {
		if candidate == nil || candidate == exclude {
			continue
		}
		if oldest == nil || candidate.createdAt.Before(oldest.createdAt) {
			oldestKey = sessionKey{authKeyID: authKeyID, sessionID: sessionID}
			oldest = candidate
			isClaim = false
		}
	}
	for sessionID, candidate := range m.claimsByAuth[authKeyID] {
		if candidate == nil || candidate == exclude {
			continue
		}
		if oldest == nil || candidate.createdAt.Before(oldest.createdAt) {
			oldestKey = sessionKey{authKeyID: authKeyID, sessionID: sessionID}
			oldest = candidate
			isClaim = true
		}
	}
	return oldestKey, oldest, isClaim
}

func (m *SessionManager) addClaimLocked(key sessionKey, c *Conn) {
	m.claims[key] = c
	addConnIndex(m.claimsByAuth, key.authKeyID, key.sessionID, c)
}

func (m *SessionManager) removeClaimLocked(key sessionKey, c *Conn) {
	if m.claims[key] != c {
		return
	}
	delete(m.claims, key)
	removeConnIndex(m.claimsByAuth, key.authKeyID, key.sessionID)
}

func (m *SessionManager) businessAuthKeyCandidatesLocked(authKeyID [8]byte) map[sessionKey]*Conn {
	out := make(map[sessionKey]*Conn, len(m.byBusinessAuthKey[authKeyID])+len(m.byAuthKey[authKeyID]))
	for key, c := range m.byBusinessAuthKey[authKeyID] {
		if cur := m.bySession[key]; cur == c {
			out[key] = c
		}
	}
	for sessionID, c := range m.byAuthKey[authKeyID] {
		key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
		if cur := m.bySession[key]; cur == c {
			out[key] = c
		}
	}
	return out
}

func (m *SessionManager) clearChannelInterestsLocked(key sessionKey) {
	m.clearChannelIndexLocked(m.byChannel, m.bySessionChannels, key)
}

func (m *SessionManager) clearChannelSubscriptionsLocked(key sessionKey) {
	channels := m.bySessionSubscriptions[key]
	if len(channels) == 0 {
		delete(m.bySessionSubscriptions, key)
		return
	}
	for channelID := range channels {
		sessions := m.bySubscribedChannel[channelID]
		delete(sessions, key)
		if len(sessions) == 0 {
			delete(m.bySubscribedChannel, channelID)
		}
	}
	delete(m.bySessionSubscriptions, key)
}

func (m *SessionManager) pruneSessionSubscriptionsLocked(key sessionKey, now int64) {
	channels := m.bySessionSubscriptions[key]
	for channelID, expiresAt := range channels {
		if expiresAt <= now {
			m.removeChannelSubscriptionLocked(key, channelID)
		}
	}
}

func (m *SessionManager) removeChannelSubscriptionLocked(key sessionKey, channelID int64) {
	channels := m.bySessionSubscriptions[key]
	delete(channels, channelID)
	if len(channels) == 0 {
		delete(m.bySessionSubscriptions, key)
	}
	sessions := m.bySubscribedChannel[channelID]
	delete(sessions, key)
	if len(sessions) == 0 {
		delete(m.bySubscribedChannel, channelID)
	}
}

func (m *SessionManager) clearSessionChannelIndexesLocked(c *Conn, key sessionKey) {
	m.clearChannelInterestsLocked(key)
	m.clearChannelSubscriptionsLocked(key)
	m.clearChannelMembershipsLocked(c, key)
}

// clearChannelMembershipsLocked wholesale clears a connection's membership index
// and increments its revision number, so an in-flight full sync
// (SetSessionChannelMemberships) can detect the clear and abandon a stale replacement.
func (m *SessionManager) clearChannelMembershipsLocked(c *Conn, key sessionKey) {
	c.membershipGen.Add(1)
	m.clearChannelIndexLocked(m.byMemberChannel, m.bySessionMembers, key)
}

func (m *SessionManager) trackChannelIndexLocked(index map[int64]map[sessionKey]int64, reverse map[sessionKey]map[int64]struct{}, key sessionKey, userID int64, channelIDs []int64) {
	channels := reverse[key]
	if channels == nil {
		channels = make(map[int64]struct{}, len(channelIDs))
		reverse[key] = channels
	}
	truncated := 0
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		if _, exists := channels[channelID]; !exists && len(channels) >= maxChannelIndexPerSession {
			// Per-session cap reached: drop the excess channel registrations (this only
			// affects that channel's live/member push routing; durable updates are still
			// caught by getDifference/getChannelDifference).
			truncated++
			continue
		}
		channels[channelID] = struct{}{}
		sessions := index[channelID]
		if sessions == nil {
			sessions = make(map[sessionKey]int64)
			index[channelID] = sessions
		}
		sessions[key] = userID
	}
	if truncated > 0 {
		m.log.Warn("Channel index truncated for session at per-session cap",
			zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
			zap.Int64("session_id", key.sessionID),
			zap.Int("cap", maxChannelIndexPerSession),
			zap.Int("truncated", truncated),
		)
	}
}

func (m *SessionManager) clearChannelIndexLocked(index map[int64]map[sessionKey]int64, reverse map[sessionKey]map[int64]struct{}, key sessionKey) {
	channels := reverse[key]
	if len(channels) == 0 {
		delete(reverse, key)
		return
	}
	for channelID := range channels {
		sessions := index[channelID]
		delete(sessions, key)
		if len(sessions) == 0 {
			delete(index, channelID)
		}
	}
	delete(reverse, key)
}

func (m *SessionManager) removeChannelIndexLocked(index map[int64]map[sessionKey]int64, reverse map[sessionKey]map[int64]struct{}, key sessionKey, channelID int64) {
	channels := reverse[key]
	delete(channels, channelID)
	if len(channels) == 0 {
		delete(reverse, key)
	}
	sessions := index[channelID]
	delete(sessions, key)
	if len(sessions) == 0 {
		delete(index, channelID)
	}
}

func positiveLimitOrLen(limit, length int) int {
	if limit > 0 && limit < length {
		return limit
	}
	return length
}

func (m *SessionManager) takePendingLocked(key sessionKey, ready bool) []queuedPush {
	if !ready || len(m.pending[key]) == 0 {
		return nil
	}
	q := m.pending[key]
	delete(m.pending, key)
	// Filter out over-age entries on take: staging only bridges the "registered to
	// ready" window, so outdated updates (including transient ones) hoarded during a
	// long not-ready period should not be delivered as-is minutes later; durable
	// events are caught by user_update_events + getDifference, so dropping loses no data.
	now := time.Now()
	pending := make([]queuedPush, 0, len(q))
	dropped := 0
	for i := range q {
		item := q[i]
		q[i] = queuedPush{}
		if now.Sub(item.at) > pendingPushMaxAge {
			item.release()
			dropped++
			continue
		}
		pending = append(pending, item)
	}
	if dropped > 0 {
		m.log.Debug("Drop stale pending pushes on take",
			zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
			zap.Int64("session_id", key.sessionID),
			zap.Int("dropped", dropped),
		)
	}
	return pending
}

// preparePendingPush reserves the one frozen canonical semantic snapshot. Exact
// wire bytes are deliberately not retained here: they are prepared only when a
// target physical connection has a frozen profile, then cached once per profile
// by layerUpdatesFanout.
func (m *SessionManager) preparePendingPush(getUpdates func() (*layerUpdatesFanout, error)) (*layerUpdatesFanout, *pendingPushReservation, error) {
	updates, err := getUpdates()
	if err != nil {
		return nil, nil, err
	}
	if updates == nil {
		return nil, nil, errors.New("nil pending layer updates")
	}
	bytes := updates.canonicalSize()
	if bytes > maxOutboundBodyBytes {
		return nil, nil, fmt.Errorf("%w: body=%d limit=%d", ErrOutboundMessageTooLarge, bytes, maxOutboundBodyBytes)
	}
	if !m.pendingBudget.reserve(bytes) {
		return nil, nil, ErrOutboundTrackedBudget
	}
	reservation := &pendingPushReservation{budget: m.pendingBudget}
	reservation.bytes.Store(int64(bytes))
	reservation.refs.Store(1) // producer ownership; queue entries retain below.
	return updates, reservation, nil
}

// queuePreparedLocked stages a frozen proactive push and returns whether it was
// actually enqueued. The caller must hold the reservation's producer ref outside
// the lock and release it after all enqueues complete.
func (m *SessionManager) queuePreparedLocked(key sessionKey, t proto.MessageType, updates *layerUpdatesFanout, reservation *pendingPushReservation) bool {
	q := m.pending[key]
	// Expiry protection: when the earliest staged entry has exceeded
	// pendingPushMaxAge (the session has long been not ready), drop the whole batch
	// and stop hoarding this one, logging a trace. This avoids a connection that
	// "never calls getState after login" occupying pending memory long term.
	if len(q) > 0 && time.Since(q[0].at) > pendingPushMaxAge {
		m.log.Debug("Drop stale pending pushes (session not ready in time)",
			zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
			zap.Int64("session_id", key.sessionID),
			zap.Int("dropped", len(q)),
		)
		m.deletePendingLocked(key)
		return false
	}
	if updates == nil || reservation == nil {
		return false
	}
	reservation.retain()
	push := queuedPush{
		t:           t,
		updates:     updates,
		reservation: reservation,
		at:          time.Now(),
	}
	if len(q) >= maxPendingPushesPerSession {
		q[0].release()
		copy(q, q[1:])
		q[len(q)-1] = push
		m.pending[key] = q
		return true
	}
	m.pending[key] = append(q, push)
	return true
}

// RunPendingSweeper periodically reclaims long-lingering pending staged data:
// passive aging (queueLocked/takePendingLocked) only triggers on "a new push" or
// "taken when ready", and cannot reclaim the over-age pending of a connection that
// "has registered but never calls getState, happens to have no new push, and does
// not disconnect either (kept alive by pings)". This sweeper provides an active
// fallback, consistent with the pendingPushMaxAge threshold, dropping only
// over-age whole batches and not touching a session that is draining (flushing).
func (m *SessionManager) RunPendingSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		m.sweepStalePending()
		m.sweepLogicalSessions(time.Now())
	}
}

func (m *SessionManager) sweepStalePending() {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	dropped := 0
	for key, q := range m.pending {
		if m.flushing[key] {
			// The drain goroutine owns this batch; reclamation is left to runFlush to avoid racing with it.
			continue
		}
		if len(q) == 0 || now.Sub(q[0].at) <= pendingPushMaxAge {
			continue
		}
		m.deletePendingLocked(key)
		dropped++
	}
	if dropped > 0 {
		m.log.Debug("Swept stale pending sessions", zap.Int("dropped_sessions", dropped))
	}
}

func (q *queuedPush) release() {
	if q == nil {
		return
	}
	reservation := q.reservation
	*q = queuedPush{}
	reservation.release()
}

func releaseQueuedPushes(q []queuedPush) {
	for i := range q {
		q[i].release()
	}
}

func (m *SessionManager) deletePendingLocked(key sessionKey) {
	q := m.pending[key]
	delete(m.pending, key)
	releaseQueuedPushes(q)
}

func addConnIndex[K comparable](idx map[K]map[int64]*Conn, key K, sessionID int64, c *Conn) {
	set := idx[key]
	if set == nil {
		set = make(map[int64]*Conn)
		idx[key] = set
	}
	set[sessionID] = c
}

func removeConnIndex[K comparable](idx map[K]map[int64]*Conn, key K, sessionID int64) {
	if set := idx[key]; set != nil {
		delete(set, sessionID)
		if len(set) == 0 {
			delete(idx, key)
		}
	}
}

func addBusinessAuthKeyIndex(idx map[[8]byte]map[sessionKey]*Conn, authKeyID [8]byte, key sessionKey, c *Conn) {
	set := idx[authKeyID]
	if set == nil {
		set = make(map[sessionKey]*Conn)
		idx[authKeyID] = set
	}
	set[key] = c
}

func removeBusinessAuthKeyIndex(idx map[[8]byte]map[sessionKey]*Conn, authKeyID [8]byte, key sessionKey) {
	if set := idx[authKeyID]; set != nil {
		delete(set, key)
		if len(set) == 0 {
			delete(idx, authKeyID)
		}
	}
}

func addUserIndex(idx map[int64]map[sessionKey]*Conn, userID int64, key sessionKey, c *Conn) {
	set := idx[userID]
	if set == nil {
		set = make(map[sessionKey]*Conn)
		idx[userID] = set
	}
	set[key] = c
}

func removeUserIndex(idx map[int64]map[sessionKey]*Conn, userID int64, key sessionKey) {
	if set := idx[userID]; set != nil {
		delete(set, key)
		if len(set) == 0 {
			delete(idx, userID)
		}
	}
}

func connSessionKey(c *Conn) sessionKey {
	return sessionKey{authKeyID: c.authKeyID, sessionID: c.sessionID}
}

func connUsesBusinessAuthKey(c *Conn, authKeyID [8]byte) bool {
	id, resolved := c.BusinessAuthKeyID()
	if resolved {
		return id == authKeyID
	}
	return c.authKeyID == authKeyID
}

func shouldExcludeSession(c *Conn, excludeAuthKeyID *[8]byte, excludeSessionID int64) bool {
	if excludeSessionID == 0 {
		return false
	}
	if c.sessionID != excludeSessionID {
		return false
	}
	if excludeAuthKeyID == nil || *excludeAuthKeyID == ([8]byte{}) {
		return true
	}
	return c.authKeyID == *excludeAuthKeyID
}

func shouldExcludeBusinessAuthKey(c *Conn, excludeBusinessAuthKeyID *[8]byte) bool {
	if c == nil || excludeBusinessAuthKeyID == nil || *excludeBusinessAuthKeyID == ([8]byte{}) {
		return false
	}
	return connUsesBusinessAuthKey(c, *excludeBusinessAuthKeyID)
}

func sessionSupportsSemantic(c *Conn, semantic tlprofile.SemanticID) bool {
	if semantic == 0 {
		return true
	}
	if c == nil {
		return false
	}
	state := c.LayerProfileState()
	if state.Origin == LayerProfileUnknown {
		return false
	}
	_, ok := tlprofile.WireID(state.Profile, semantic)
	return ok
}

func sessionKeyLog(id [8]byte) string {
	return fmt.Sprintf("%x", id)
}
