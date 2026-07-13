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

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
)

// ErrSessionNotFound 表示目标 session 当前无活跃连接。
var ErrSessionNotFound = errors.New("session not found")

var (
	ErrSessionActivationSuperseded = errors.New("session activation superseded")
	ErrSessionActivationFence      = errors.New("session activation could not fence previous writer")
)

const (
	maxPendingPushesPerSession = 32
	// maxFlushAttempts / flushRetryBackoff：排空暂存推送时 c.Send 失败（出站拥塞 5s 超时
	// 或瞬时错误）后的退避重试上界。连接真死时 serveConn 会 Unregister 清理状态、提前止损；
	// 这里只为「连接存活但出站暂时拥塞」做有限重试。用尽仍失败则置位激活并接受 getDifference
	// 兜底——避免 idle 客户端（只发 ping、不触发置位重试）永久停在未激活态而静默断流。
	maxFlushAttempts  = 5
	flushRetryBackoff = 2 * time.Second
	// pendingPushMaxAge：session 注册后迟迟不调 updates.getState（receivesUpdates 恒 false）时，
	// 其暂存的主动推送最长保留时长。超过即丢整批并不再囤——正常 TDesktop 登录后秒级就会
	// getState 建立同步基线；长期不 ready 多为异常/对抗连接。
	//
	// 不变量：只有 durable update（写 user_update_events）才会进 pending。transient update
	// （typing/presence，不写 durable log）经 PushToUserTransient* 在未就绪时直接跳过、不入队，
	// 因此本队列被老化/溢出/重试耗尽丢弃时，丢的一定是 durable 条目——getDifference 以
	// user_update_events 兜底补齐，丢弃不丢数据。
	pendingPushMaxAge          = 60 * time.Second
	defaultPendingPushMaxBytes = int64(256 << 20)
	// maxSessionsPerAuthKey：单个 raw auth_key 允许同时在线的 session 上限。telesrv 单 DC，
	// 一个客户端的全部连接（主连接 + 并发下载/上传）共享同一 auth_key、各用独立 session_id，
	// 故此上限须高于真实客户端单设备的并发连接峰值，否则会误杀活跃下载/主连接：
	//   - TDesktop：kMaxMediaDcCount=0x10，单 DC 最多 16 路下载 + 16 路上传 + 1 主 ≈ 33；
	//   - DrKLO：DOWNLOAD_CONNECTIONS_COUNT=2 + UPLOAD_CONNECTIONS_COUNT=4 + 主/push ≈ 10。
	// 叠加重连 churn（旧 session 在 readTimeout 内滞留）峰值约 ~70，故设 256（~3.5x 余量）。
	// 它只防「单 auth_key 累积海量连接」的病态（使 CloseSessionsForRawAuthKey/pushToUser 遍历
	// 退化 O(N)），超限驱逐的也只是同一设备凭据自身的连接，不会误伤别的账号。
	maxSessionsPerAuthKey = 256
	// maxChannelIndexPerSession：单 session 在 channel 路由索引（interest / membership）中
	// 登记的 channel 数上限。membership 源于真实成员关系（大账号可能很多），interest 受客户端
	// 直接控制；两者都设一个宽松上界防内存放大，超出即截断并记日志。
	maxChannelIndexPerSession = 8192
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
	encoded     *encodedOutboundMessage
	reservation *pendingPushReservation
	at          time.Time
}

type pendingPushReservation struct {
	budget *outboundTrackedBudget
	bytes  int
	refs   atomic.Int32
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
		r.budget.release(r.bytes)
	}
}

type sessionKey struct {
	authKeyID [8]byte
	sessionID int64
}

// SessionLifecycleObserver receives active connection lifecycle events.
type SessionLifecycleObserver interface {
	SessionOffline(rawAuthKeyID [8]byte, sessionID, userID int64, lastForUser bool)
}

// SessionManager 是活跃连接注册表，支持按 session / auth-key / user 查找并主动 push。
//
// 它只管理进程内运行态，持有可发送的活跃连接；协议可恢复事实由 auth key、客户端重连
// 和 durable updates/difference 链路承担。所有方法并发安全。
type SessionManager struct {
	mu        sync.RWMutex
	bySession map[sessionKey]*Conn
	// claims owns the provisional -> active gap. A claimant is intentionally
	// absent from every push/online index until its required session control frame
	// is on the wire and PublishActivation validates the same owner.
	claims            map[sessionKey]*Conn
	claimsByAuth      map[[8]byte]map[int64]*Conn // raw authKeyID -> sessionID -> provisional claim
	byAuthKey         map[[8]byte]map[int64]*Conn // raw authKeyID → sessionID → Conn
	byBusinessAuthKey map[[8]byte]map[sessionKey]*Conn
	byUser            map[int64]map[sessionKey]*Conn
	byChannel         map[int64]map[sessionKey]int64 // channelID → session → userID，用于频道 active-viewer 临时推送
	bySessionChannels map[sessionKey]map[int64]struct{}
	byMemberChannel   map[int64]map[sessionKey]int64 // channelID → session → userID，用于已上线成员持久 update 推送
	bySessionMembers  map[sessionKey]map[int64]struct{}
	pending           map[sessionKey][]queuedPush // updates-ready 前暂存的主动推送
	flushing          map[sessionKey]bool         // 置位时暂存正在排空的 session；排空完成前推送继续进 pending 保序
	pendingBudget     *outboundTrackedBudget      // 未就绪 session 暂存 encoded body 的进程级上限

	lifecycle SessionLifecycleObserver
	log       *zap.Logger
}

// NewSessionManager 创建空的连接注册表。
func NewSessionManager(log *zap.Logger) *SessionManager {
	if log == nil {
		log = zap.NewNop()
	}
	return &SessionManager{
		bySession:         make(map[sessionKey]*Conn),
		claims:            make(map[sessionKey]*Conn),
		claimsByAuth:      make(map[[8]byte]map[int64]*Conn),
		byAuthKey:         make(map[[8]byte]map[int64]*Conn),
		byBusinessAuthKey: make(map[[8]byte]map[sessionKey]*Conn),
		byUser:            make(map[int64]map[sessionKey]*Conn),
		byChannel:         make(map[int64]map[sessionKey]int64),
		bySessionChannels: make(map[sessionKey]map[int64]struct{}),
		byMemberChannel:   make(map[int64]map[sessionKey]int64),
		bySessionMembers:  make(map[sessionKey]map[int64]struct{}),
		pending:           make(map[sessionKey][]queuedPush),
		flushing:          make(map[sessionKey]bool),
		pendingBudget:     newOutboundTrackedBudget(defaultPendingPushMaxBytes),
		log:               log,
	}
}

// SetLifecycleObserver installs a best-effort active session lifecycle observer.
func (m *SessionManager) SetLifecycleObserver(observer SessionLifecycleObserver) {
	m.mu.Lock()
	m.lifecycle = observer
	m.mu.Unlock()
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
		owned = true
	}
	m.mu.Unlock()
	if owned {
		_ = closeConnBatch([]*Conn{c}, forceCloseBatchTimeout, false)
	}
}

// Unregister 注销一个连接（仅当它仍是当前注册的同一对象，避免误删重连后的新连接）。
// 观察者对未登录连接（userID=0）也回调：业务层据此清理按 session 维度的缓存条目，
// 否则未登录连接的元数据只能等容量上限驱逐。
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
	m.mu.Unlock()
	if observer != nil {
		observer.SessionOffline(c.authKeyID, c.sessionID, offlineUser, lastForUser)
	}
}

// DestroySessionForAuthKey 精确移除某个 raw auth_key_id 下的 session。
func (m *SessionManager) DestroySessionForAuthKey(authKeyID [8]byte, sessionID int64) bool {
	m.mu.Lock()
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	if !ok {
		if claim := m.claims[key]; claim != nil {
			m.retireClaimLocked(key, claim, true)
			m.mu.Unlock()
			if !forceCloseConnBatch([]*Conn{claim}, forceCloseBatchTimeout) {
				m.log.Warn("Claimed session close exceeded shared deadline",
					zap.String("auth_key_id", sessionKeyLog(authKeyID)),
					zap.Int64("session_id", sessionID),
				)
			}
			return true
		}
		m.deletePendingLocked(key)
		m.mu.Unlock()
		return false
	}
	offlineUser := m.retireConnLocked(c, true)
	lastForUser := offlineUser != 0 && len(m.byUser[offlineUser]) == 0
	observer := m.lifecycle
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
	if observer != nil && offlineUser != 0 {
		observer.SessionOffline(authKeyID, sessionID, offlineUser, lastForUser)
	}
	return true
}

// BindUserForAuthKey 缓存指定 raw auth_key_id + session_id 的授权用户。
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
			m.clearChannelInterestsLocked(key)
			m.clearChannelMembershipsLocked(c, key)
			c.membershipsSynced.Store(false)
			// 身份变化即丢弃暂存推送：它们属于前一个账号，flush 给新账号是跨账号泄露。
			// 同时取消进行中的排空（runFlush 还另有 owner 校验做批内兜底）。
			m.deletePendingLocked(key)
			delete(m.flushing, key)
		}
	}
	c.userIDResolved.Store(true)
	if userID != 0 {
		addUserIndex(m.byUser, userID, key, c)
	} else {
		m.clearChannelInterestsLocked(key)
		m.clearChannelMembershipsLocked(c, key)
		c.membershipsSynced.Store(false)
		m.deletePendingLocked(key)
		delete(m.flushing, key)
	}
}

// UserIDResolvedForAuthKey 返回指定 raw auth_key_id + session_id 的 user_id 缓存状态。
func (m *SessionManager) UserIDResolvedForAuthKey(authKeyID [8]byte, sessionID int64) (int64, bool) {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: authKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return c.UserIDResolved()
}

// BindAuthKeyForSession 缓存指定 raw auth_key_id + session_id 的业务 auth_key_id。
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

// BindAuthKeyForRawAuthKey 把同一 raw temporary key 的全部活跃 session 绑定到
// canonical permanent key。Android/TDesktop 会在一个 temp key 上并发创建多个
// session；bind 只发生在其中一个 session，其他 session 不能继续把 raw temp 当业务 key。
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
	addBusinessAuthKeyIndex(m.byBusinessAuthKey, authKeyID, key, c)
	if changed {
		if oldUserID != 0 {
			removeUserIndex(m.byUser, oldUserID, key)
		}
		m.clearChannelInterestsLocked(key)
		m.clearChannelMembershipsLocked(c, key)
		c.membershipsSynced.Store(false)
		m.deletePendingLocked(key)
		delete(m.flushing, key)
		c.userID.Store(0)
		c.userIDResolved.Store(false)
	}
}

// AuthKeyIDForSession 返回指定 raw auth_key_id + session_id 缓存的业务 auth_key_id。
func (m *SessionManager) AuthKeyIDForSession(rawAuthKeyID [8]byte, sessionID int64) ([8]byte, bool) {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return [8]byte{}, false
	}
	return c.BusinessAuthKeyID()
}

// AuthKeyExpiresAtForSession 返回 raw key 的握手协议失效时间；0 表示 permanent。
func (m *SessionManager) AuthKeyExpiresAtForSession(rawAuthKeyID [8]byte, sessionID int64) (int, bool) {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: rawAuthKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return 0, false
	}
	return c.AuthKeyExpiresAt(), true
}

// CloseSessionsForBusinessAuthKey 强制断开指定业务 auth_key 的全部活跃连接，
// 供授权撤销（被踢设备）使用：出站推送用连接持有的密钥加密、不回查密钥库，
// 不断开的话被撤销的设备会继续收到推送直至自然断线；perm-key 连接的授权
// 缓存也只有断开重连才会重新回查授权表。这里必须关闭底层 transport，
// 让 WebSocket/TCP 对端马上看到断线，而不是只从在线索引摘除。
func (m *SessionManager) CloseSessionsForBusinessAuthKey(authKeyID [8]byte) int {
	type offlineEvent struct {
		key    sessionKey
		userID int64
		last   bool
	}
	m.mu.Lock()
	var conns []*Conn
	var events []offlineEvent
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
	if observer != nil {
		for _, e := range events {
			observer.SessionOffline(e.key.authKeyID, e.key.sessionID, e.userID, e.last)
		}
	}
	return len(conns)
}

// CloseSessionsForRawAuthKeyExcept 强制断开指定 raw auth_key 的活跃连接，可按
// session ID 排除一个 session。该接口供业务层授权撤销使用；wire-level
// destroy_auth_key 必须改用精确 Conn 排除，避免同 session replacement 被误放过。
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

// UnbindAuthKey 清理某业务 auth_key 下所有活跃连接的登录用户缓存。
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
		m.clearChannelInterestsLocked(key)
		m.clearChannelMembershipsLocked(c, key)
		c.membershipsSynced.Store(false)
		// 授权解除后暂存推送属于已登出的账号，不能等下一个登录者置位时 flush 出去。
		m.deletePendingLocked(key)
		delete(m.flushing, key)
		c.userIDResolved.Store(true)
		count++
	}
	return count
}

// setReceivesUpdatesLocked 是置位/复位的共同内核，调用方须持有 m.mu。
// 置位且有暂存时不立即置 receivesUpdates：标记 flushing 并返回该批暂存所属的 userID，
// 交由 runFlush 排空后原子置位，期间新到推送继续进 pending，保证暂存与实时推送的
// 相对顺序（否则实时直发可能先于更早 pts 的暂存条目落线）。返回的 owner 让 runFlush
// 能识别排空期间的身份切换（登出/换号），丢弃属于旧账号的剩余暂存而不发给新账号。
func (m *SessionManager) setReceivesUpdatesLocked(c *Conn, key sessionKey, receives bool) (int64, bool) {
	if !receives {
		c.receivesUpdates.Store(false)
		m.clearChannelInterestsLocked(key)
		m.clearChannelMembershipsLocked(c, key)
		c.membershipsSynced.Store(false)
		// 取消进行中的排空激活：runFlush 在置位前会复查该标志，标志已删则放弃置位，
		// 避免把刚置 false 的开关翻回 true。
		delete(m.flushing, key)
		return 0, false
	}
	if c.receivesUpdates.Load() || m.flushing[key] {
		// 已就绪，或已有排空协程在跑（完成时会自行取走新增暂存并置位）。
		return 0, false
	}
	if len(m.pending[key]) == 0 {
		c.receivesUpdates.Store(true)
		return 0, false
	}
	m.flushing[key] = true
	return c.userID.Load(), true
}

// runFlush 把暂存推送按序直发到连接，排空（含排空期间新增）后才置位 receivesUpdates。
// 直发用 c.Send 绕过 ready 检查——此刻必然未就绪，走 PushToSessionForAuthKey 会被
// 重新暂存形成死循环。三类终止：
//   - 身份切换（登出/换号致 c.userID != owner）：丢弃剩余暂存与回排数据，不发给新账号；
//   - 发送失败：回排剩余并退避重试，attempt 用尽则置位激活、靠 getDifference 兜底，
//     避免 idle 客户端永久停在未激活态；
//   - 排空完毕：原子置位 receivesUpdates。
func (m *SessionManager) runFlush(c *Conn, key sessionKey, owner int64, attempt int) {
	for {
		m.mu.Lock()
		if cur, ok := m.bySession[key]; !ok || cur != c || !m.flushing[key] {
			// 连接已换代（removeLocked 已清 flushing）或激活被取消（SetReceivesUpdates(false)）。
			m.mu.Unlock()
			return
		}
		if c.userID.Load() != owner {
			// 排空期间发生登出/换号：剩余暂存属于旧账号，丢弃且不得发给新账号。
			m.deletePendingLocked(key)
			delete(m.flushing, key)
			m.mu.Unlock()
			return
		}
		batch := m.takePendingLocked(key, true)
		if len(batch) == 0 {
			c.receivesUpdates.Store(true)
			delete(m.flushing, key)
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		for i, item := range batch {
			// 每条发送前复查身份：登出/换号后 batch 的剩余条目不能继续发到已易主的连接。
			if c.userID.Load() != owner {
				m.mu.Lock()
				m.deletePendingLocked(key)
				delete(m.flushing, key)
				m.mu.Unlock()
				releaseQueuedPushes(batch[i:])
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// Pending entries are durable account updates. Shared body-budget pressure is not
			// evidence that this socket is corrupt, so use the non-terminal enqueue path; after
			// bounded retries, getDifference is the authoritative recovery path.
			err := c.SendBestEffortEncoded(ctx, item.t, item.encoded, 5*time.Second)
			cancel()
			if err == nil {
				item.release()
				continue
			}
			m.mu.Lock()
			if cur, ok := m.bySession[key]; !ok || cur != c || !m.flushing[key] || c.userID.Load() != owner {
				// 连接换代/取消/易主：剩余 batch 不属于当前连接当前账号，丢弃。
				if c.userID.Load() != owner {
					m.deletePendingLocked(key)
					delete(m.flushing, key)
				}
				m.mu.Unlock()
				releaseQueuedPushes(batch[i:])
				return
			}
			rest := append(append([]queuedPush(nil), batch[i:]...), m.pending[key]...)
			if len(rest) > maxPendingPushesPerSession {
				// 与 queueLocked 溢出策略一致：丢最旧留最新，让 pts 空洞集中在最前端，
				// flush 首条即触发客户端 gap 检测，恢复路径最短。
				dropped := len(rest) - maxPendingPushesPerSession
				releaseQueuedPushes(rest[:dropped])
				rest = rest[dropped:]
			}
			m.pending[key] = rest
			if attempt+1 >= maxFlushAttempts {
				// 重试用尽：置位激活避免 idle 客户端永久断流；剩余暂存中的 durable 更新
				// 由客户端后续 pts 空洞触发 getDifference 补齐。
				c.receivesUpdates.Store(true)
				m.deletePendingLocked(key)
				delete(m.flushing, key)
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
		// 本批发完，循环回去 re-take 排空期间新增的暂存。
	}
}

// ReceivesUpdatesForAuthKey 报告指定 raw auth_key_id + session_id 的连接是否已完全就绪：
// 既接收主动 updates，channel membership 推送路由也已成功建立。无活跃连接时返回 false。
// 返回 false 会让按 RPC 置位的短路放行，下一条 RPC 重试 membership 同步——
// 否则同步失败的 session 会以「已置位但 byMemberChannel 缺失」的状态静默漏收超级群推送。
func (m *SessionManager) ReceivesUpdatesForAuthKey(authKeyID [8]byte, sessionID int64) bool {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: authKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	return ok && c.receivesUpdates.Load() && c.membershipsSynced.Load()
}

// SetClientLayerForAuthKey 把协商的 TL layer 即时写到指定连接。由 rpc 层在
// invokeWithLayer 观测到新 layer 时（Dispatch 入口，早于鉴权门与 updates 就绪
// 置位）调用，使同一条请求触发的 pending flush 与并发 push 立即按正确 layer
// 降级，不必等连接层在 Dispatch 返回后的兜底刷新。
func (m *SessionManager) SetClientLayerForAuthKey(authKeyID [8]byte, sessionID int64, layer int) {
	if m == nil || layer <= 0 {
		return
	}
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: authKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if ok {
		c.SetClientLayer(layer)
	}
}

// SetReceivesUpdatesForAuthKey 标记指定 raw auth_key_id + session_id 是否接收主动 updates。
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

// PushToSessionForAuthKey 向指定 raw auth_key_id + session_id 推送一条消息。
func (m *SessionManager) PushToSessionForAuthKey(ctx context.Context, authKeyID [8]byte, sessionID int64, t proto.MessageType, msg bin.Encoder) error {
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
		return c.Send(ctx, t, msg)
	}
	return m.queueOrSendPrepared(ctx, key, t, msg)
}

func (m *SessionManager) queueOrSendPrepared(ctx context.Context, key sessionKey, t proto.MessageType, msg bin.Encoder) error {
	encoded, reservation, err := m.preparePendingPush(ctx, msg)
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
		_ = m.queuePreparedLocked(key, t, encoded, reservation)
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	return c.SendEncoded(ctx, t, encoded)
}

// PushToSessionForAuthKeyImmediate 向指定 raw auth_key_id + session_id 立即推送一条消息。
//
// 它不等待该 session 进入 updates-ready，也不写 pending 队列。仅用于登录前的握手信号
// （例如 updateLoginToken）：这类消息本身就是让客户端继续完成登录的触发器，若走普通
// durable update 队列会卡在客户端尚未调用 updates.getState 的阶段。
func (m *SessionManager) PushToSessionForAuthKeyImmediate(ctx context.Context, authKeyID [8]byte, sessionID int64, t proto.MessageType, msg bin.Encoder) error {
	m.mu.RLock()
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	m.mu.RUnlock()
	if !ok {
		return ErrSessionNotFound
	}
	return c.SendBestEffort(ctx, t, msg, 2*time.Second)
}

// PushToUserExceptAuthKeySession 向某 user 所有活跃连接推送，跳过指定 raw auth_key + session。
func (m *SessionManager) PushToUserExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	return m.pushToUser(ctx, userID, &excludeAuthKeyID, excludeSessionID, t, msg)
}

// PushToUserAuthKey 把 msg 定向投递给【绑定到 businessAuthKeyID 这台具体设备】且属于
// userID 的就绪连接（密聊设备级投递的锚点）。索引走 byBusinessAuthKey（经
// businessAuthKeyCandidatesLocked，兼容 temp-key/PFS 连接），不是 byAuthKey（raw 索引会
// 漏 temp-key 设备）。未就绪连接跳过、不进 pending——密聊消息 durable 在 qts 队列，
// 离线设备靠 getDifference 补回（在线推送只是加速器）。c.userID 复查防跨账号泄露。
func (m *SessionManager) PushToUserAuthKey(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg bin.Encoder) (int, error) {
	// Secret-chat qts is the durable source of truth, so online delivery is an accelerator just
	// like account pts fan-out.  Do not synchronously wait for every PFS/raw connection's socket.
	return m.pushToBusinessAuthKeyBestEffort(ctx, userID, businessAuthKeyID, t, msg, 2*time.Second)
}

// PushToUserAuthKeyTransient 是 PushToUserAuthKey 的 transient（typing）best-effort 版本。
func (m *SessionManager) PushToUserAuthKeyTransient(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	return m.pushToBusinessAuthKeyBestEffort(ctx, userID, businessAuthKeyID, t, msg, timeout)
}

func (m *SessionManager) pushToBusinessAuthKeyBestEffort(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	getEncoded := onceEncodedOutbound(ctx, msg)
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
			deadline = ctxDeadline
		}
	}
	return m.pushToBusinessAuthKey(ctx, userID, businessAuthKeyID, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		encoded, err := getEncoded()
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
		return c.SendBestEffortEncoded(ctx, t, encoded, remaining)
	})
}

func (m *SessionManager) pushToBusinessAuthKey(ctx context.Context, userID int64, businessAuthKeyID [8]byte, send func(*Conn) error) (int, error) {
	m.mu.Lock()
	candidates := m.businessAuthKeyCandidatesLocked(businessAuthKeyID)
	conns := make([]*Conn, 0, len(candidates))
	for _, c := range candidates {
		if c.userID.Load() != userID {
			continue
		}
		if !c.receivesUpdates.Load() {
			// 未就绪：密聊消息靠 getDifference 补，typing 直接丢——都不进 pending。
			continue
		}
		conns = append(conns, c)
	}
	m.mu.Unlock()
	var firstErr error
	sent := 0
	for _, c := range conns {
		// 锁外发送前复查身份，防收集后并发换绑导致跨账号泄露。
		if c.userID.Load() != userID {
			continue
		}
		if err := send(c); err != nil {
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

func (m *SessionManager) pushToUser(ctx context.Context, userID int64, excludeAuthKeyID *[8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	getEncoded := onceEncodedOutbound(ctx, msg)
	return m.pushToUserWithSender(ctx, userID, excludeAuthKeyID, excludeSessionID, t, msg, true, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		encoded, err := getEncoded()
		if err != nil {
			return err
		}
		return c.SendEncoded(ctx, t, encoded)
	})
}

// PushToUserTransientExceptAuthKeySession 推送 transient（短命、不写 durable log）update，
// 如 typing / presence。与普通推送的关键区别：session 未就绪（receivesUpdates=false）时直接
// 跳过该连接、不进 pending——transient 数据 getDifference 无法补，就绪后由 getState 快照 /
// 下一次状态变化重建，囤积过期 transient 既无意义又会被 pending 的老化/溢出/重试耗尽误当
// 「durable 兜底」丢弃。走 best-effort 发送，不阻塞调用方。
func (m *SessionManager) PushToUserTransientExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	getEncoded := onceEncodedOutbound(ctx, msg)
	return m.pushToUserWithSender(ctx, userID, &excludeAuthKeyID, excludeSessionID, t, msg, false, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		encoded, err := getEncoded()
		if err != nil {
			return err
		}
		return c.SendBestEffortEncoded(ctx, t, encoded, timeout)
	})
}

func (m *SessionManager) PushToUserExceptAuthKeySessionBestEffort(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	return m.pushToUserBestEffort(ctx, userID, &excludeAuthKeyID, excludeSessionID, t, msg, timeout)
}

func (m *SessionManager) pushToUserBestEffort(ctx context.Context, userID int64, excludeAuthKeyID *[8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	getEncoded := onceEncodedOutbound(ctx, msg)
	// timeout 是整次 fan-out 的等待预算，不是每个 session 各自一份。健康连接始终先走
	// SendBestEffortEncoded 的非阻塞快路径；预算耗尽后 remaining=0，仍会尝试快路径，
	// 但不会再为后续慢连接串行等待。
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
			deadline = ctxDeadline
		}
	}
	return m.pushToUserWithSender(ctx, userID, excludeAuthKeyID, excludeSessionID, t, msg, true, func(c *Conn) error {
		if c.outbound == nil || c.outboundControl == nil {
			return ErrConnClosed
		}
		encoded, err := getEncoded()
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
		return c.SendBestEffortEncoded(ctx, t, encoded, remaining)
	})
}

func onceEncodedOutbound(ctx context.Context, msg bin.Encoder) func() (*encodedOutboundMessage, error) {
	var (
		encoded *encodedOutboundMessage
		err     error
	)
	return func() (*encodedOutboundMessage, error) {
		if encoded == nil && err == nil {
			encoded, err = encodeOutboundMessageContext(ctx, msg)
		}
		return encoded, err
	}
}

func (m *SessionManager) pushToUserWithSender(ctx context.Context, userID int64, excludeAuthKeyID *[8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, queueWhenNotReady bool, send func(*Conn) error) (int, error) {
	// push fan-out 是连接层最热路径之一：debug 日志的字段构造（含 auth_key hex 格式化）
	// 在关闭 debug 时也会求值，先查级别一次、按需记日志。
	debug := m.log.Core().Enabled(zapcore.DebugLevel)
	// 快路径：稳态下目标连接全部就绪（或 transient 直接跳过未就绪者），收集连接
	// 只读不写，全程共享读锁即可，避免每次 push 都拿独占写锁串行整个注册表。
	// 仅当 durable 推送遇到未就绪连接（需写 pending）时才回落写锁重扫。
	m.mu.RLock()
	total := len(m.byUser[userID])
	conns := make([]*Conn, 0, total)
	queued := 0
	dropped := 0
	excluded := 0
	skipped := 0
	needQueue := false
	for _, c := range m.byUser[userID] {
		if shouldExcludeSession(c, excludeAuthKeyID, excludeSessionID) {
			excluded++
			continue
		}
		if !c.receivesUpdates.Load() {
			if !queueWhenNotReady {
				// transient（typing/presence）：未就绪即丢，不进 pending。这些 update 不写
				// durable log，getDifference 无法补；就绪后由 getState 快照/下次状态变化重建。
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
		pendingEncoded, pendingReservation, pendingErr := m.preparePendingPush(ctx, msg)
		// 写锁下完整重扫（读锁释放到此之间状态可能变化，以重扫结果为准）。
		conns = conns[:0]
		queued, dropped, excluded, skipped = 0, 0, 0, 0
		m.mu.Lock()
		total = len(m.byUser[userID])
		for key, c := range m.byUser[userID] {
			if shouldExcludeSession(c, excludeAuthKeyID, excludeSessionID) {
				excluded++
				continue
			}
			if !c.receivesUpdates.Load() {
				if !queueWhenNotReady {
					skipped++
					continue
				}
				if pendingErr == nil && m.queuePreparedLocked(key, t, pendingEncoded, pendingReservation) {
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
		// 锁外发送前复查身份：收集 conns 到此刻之间，连接可能被并发换绑（登出/换号，
		// bindUserLocked 的 c.userID.Swap）。不复查会把本属于 userID 的 update 投递到
		// 已易主的连接，构成跨账号泄露。与 AddUserChannelMembership 的同款防御一致。
		if c.userID.Load() != userID {
			continue
		}
		if err := send(c); err != nil {
			if errors.Is(err, ErrOutboundTrackedBudget) {
				// Do not turn pressure owned by other sockets into a reconnect storm on
				// healthy recipients. The durable event remains recoverable by difference.
				dropped++
				continue
			}
			// 对 durable/best-effort fan-out，队列满意味着该 socket 已成为慢消费者。
			// 立即摘除并把它视为离线：不能让其错误把已经投递给健康 session 的 outbox
			// 行整体重试。该 session 的 durable gap 由 getDifference 恢复。
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

// ActiveRawAuthKeyIDs 返回当前物理连接实际使用的 raw auth_key_id 去重快照。
// maintenance 用它保护“已建 key 但尚未登录”的长连接不被 orphan GC 删除；不能用
// business/temp→perm key 替代，否则活跃 temp 连接仍可能误删。
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

// ChannelMembershipGeneration 返回该 session 的 membership 索引修订号。
// 全量同步方必须在读取持久成员列表【之前】采样，并经 SetSessionChannelMemberships
// 带回比对；session 不在线时返回 0（后续 Set 也会因查不到连接而放弃）。
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
// expectedGen 是调用方在读取持久成员列表前经 ChannelMembershipGeneration 采样的
// 修订号。若落地时修订号已变（读取窗口内发生了 join/leave/kick 的增量修订或整体
// 清除），全量替换会覆盖掉窗口内的增量——此时改走并集合并（保留增量 Add；合并回的
// stale 条目由 fan-out 前的 PG active 复核兜底），并保持 membershipsSynced=false，
// 让下一条 RPC 重新走全量同步收敛。
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

// OnlineChannelMemberUserIDsExcluding 返回频道在线成员中不在 exclude 集合内的 user id，
// 用于 >cap 在线成员的 UpdateChannelTooLong nudge（P0-8）：完整 payload 已投递给 exclude
// 集合（cap 内成员），其余在线成员只发廉价 nudge 促其 getChannelDifference。单次 RLock 快照；
// 由调用方用「已收完整 payload 的 recipients」构造 exclude，使同一 user 不会既收 payload 又收
// nudge——天然规避两次独立 cap 调用的边界双投/漏投（设计 §8-D3/D32）。limit 防一次无界 nudge 风暴。
// 不做 PG active 复核：byMemberChannel 已在 join/leave/kick 维护；nudge 廉价且幂等，对刚离开成员
// 的多余 nudge 无害（其 getChannelDifference 自带访问校验）。
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

// OnlineChannelIDsSnapshot returns every channel with at least one live joined-member session in
// strictly ascending order. The global SessionManager lock is held only while copying map keys;
// sorting and all recovery database work happen after unlock. The fixed saturation-recovery actor
// is the sole caller, so its exceptional-path temporary memory is one int64 slice (peak about 8*C
// bytes) rather than repeated O(C) scans under the connection/membership lock.
func (m *SessionManager) OnlineChannelIDsSnapshot() []int64 {
	m.mu.RLock()
	out := make([]int64, 0, len(m.byMemberChannel))
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
		out = append(out, channelID)
	}
	m.mu.RUnlock()
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
	m.clearChannelInterestsLocked(key)
	m.clearChannelMembershipsLocked(c, key)
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

// clearChannelMembershipsLocked 整体清除某连接的 membership 索引并递增其修订号，
// 使在飞的全量同步（SetSessionChannelMemberships）能检测到清除并放弃过期替换。
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
			// 达 per-session 上限：丢弃多出的 channel 登记（仅影响该 channel 的实时/成员
			// 推送路由，durable update 仍由 getDifference/getChannelDifference 兜底）。
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
	// 取出时过滤超龄条目：暂存只为弥合「注册到就绪」的窗口，迟迟未就绪期间
	// 囤下的过时 update（含 transient 类）不应在多分钟后原样下发；durable 事件
	// 由 user_update_events + getDifference 兜底，丢弃不丢数据。
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

// preparePendingPush encodes outside SessionManager.mu and reserves the one physical body before
// releasing the process-wide encode slot. Multiple not-ready sessions may then share this
// immutable body via reservation refs instead of encoding/copying it once per session.
func (m *SessionManager) preparePendingPush(ctx context.Context, msg bin.Encoder) (*encodedOutboundMessage, *pendingPushReservation, error) {
	var (
		encoded *encodedOutboundMessage
		bytes   int
	)
	err := withOutboundEncodeSlot(ctx, nil, func() error {
		var err error
		encoded, err = encodeOutboundMessageWithoutSlot(msg)
		if err != nil {
			return err
		}
		if encoded == nil {
			return errors.New("nil encoded pending push")
		}
		bytes = len(encoded.body)
		if bytes > maxOutboundBodyBytes {
			return fmt.Errorf("%w: body=%d limit=%d", ErrOutboundMessageTooLarge, bytes, maxOutboundBodyBytes)
		}
		if !m.pendingBudget.reserve(bytes) {
			return ErrOutboundTrackedBudget
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	reservation := &pendingPushReservation{budget: m.pendingBudget, bytes: bytes}
	reservation.refs.Store(1) // producer ownership; queue entries retain below.
	return encoded, reservation, nil
}

// queuePreparedLocked 暂存一条已编码的主动推送，返回是否实际入队。
// 调用方必须在锁外保持 reservation 的 producer ref，并在全部入队完成后 release。
func (m *SessionManager) queuePreparedLocked(key sessionKey, t proto.MessageType, encoded *encodedOutboundMessage, reservation *pendingPushReservation) bool {
	q := m.pending[key]
	// 过期保护：最早一条暂存已超过 pendingPushMaxAge（session 迟迟未 ready）时，丢整批并
	// 不再囤这条，记 trace。避免「登录后从不 getState」的连接长期占用 pending 内存。
	if len(q) > 0 && time.Since(q[0].at) > pendingPushMaxAge {
		m.log.Debug("Drop stale pending pushes (session not ready in time)",
			zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
			zap.Int64("session_id", key.sessionID),
			zap.Int("dropped", len(q)),
		)
		m.deletePendingLocked(key)
		return false
	}
	if encoded == nil || reservation == nil {
		return false
	}
	reservation.retain()
	push := queuedPush{
		t:           t,
		encoded:     encoded,
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

// RunPendingSweeper 周期回收长期滞留的 pending 暂存：被动老化（queueLocked/takePendingLocked）
// 只在「有新推送」或「就绪后取出」时触发，对「已注册但迟迟不调 getState、又恰好没有新推送、
// 也不断连（持续 ping 保活）」的连接无法回收其超龄 pending。本 sweeper 给出一个主动兜底，
// 与 pendingPushMaxAge 阈值一致，仅丢整批超龄、不触碰正在排空（flushing）的 session。
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
	}
}

func (m *SessionManager) sweepStalePending() {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	dropped := 0
	for key, q := range m.pending {
		if m.flushing[key] {
			// 排空协程拥有该批，回收交给 runFlush，避免与其竞态。
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

func sessionKeyLog(id [8]byte) string {
	return fmt.Sprintf("%x", id)
}
