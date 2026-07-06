package mtprotoedge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
)

// ErrSessionNotFound 表示目标 session 当前无活跃连接。
var ErrSessionNotFound = errors.New("session not found")

// ErrSessionAmbiguous 表示仅用 session_id 无法唯一定位连接。
var ErrSessionAmbiguous = errors.New("session id is shared by multiple auth keys")

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
	pendingPushMaxAge = 60 * time.Second
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

type queuedPush struct {
	t   proto.MessageType
	msg bin.Encoder
	at  time.Time
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
// 它管理运行态的在线连接，与持久化的 store.SessionStore 互补：后者记录 session 数据，
// 前者持有可发送的活跃连接。所有方法并发安全。
type SessionManager struct {
	mu                sync.RWMutex
	bySession         map[sessionKey]*Conn
	bySessionID       map[int64]map[[8]byte]*Conn // sessionID → raw authKeyID → Conn，用于兼容旧 API 的唯一性检查
	byAuthKey         map[[8]byte]map[int64]*Conn // raw authKeyID → sessionID → Conn
	byBusinessAuthKey map[[8]byte]map[sessionKey]*Conn
	byUser            map[int64]map[sessionKey]*Conn
	byChannel         map[int64]map[sessionKey]int64 // channelID → session → userID，用于频道 active-viewer 临时推送
	bySessionChannels map[sessionKey]map[int64]struct{}
	byMemberChannel   map[int64]map[sessionKey]int64 // channelID → session → userID，用于已上线成员持久 update 推送
	bySessionMembers  map[sessionKey]map[int64]struct{}
	pending           map[sessionKey][]queuedPush // updates-ready 前暂存的主动推送
	flushing          map[sessionKey]bool         // 置位时暂存正在排空的 session；排空完成前推送继续进 pending 保序

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
		bySessionID:       make(map[int64]map[[8]byte]*Conn),
		byAuthKey:         make(map[[8]byte]map[int64]*Conn),
		byBusinessAuthKey: make(map[[8]byte]map[sessionKey]*Conn),
		byUser:            make(map[int64]map[sessionKey]*Conn),
		byChannel:         make(map[int64]map[sessionKey]int64),
		bySessionChannels: make(map[sessionKey]map[int64]struct{}),
		byMemberChannel:   make(map[int64]map[sessionKey]int64),
		bySessionMembers:  make(map[sessionKey]map[int64]struct{}),
		pending:           make(map[sessionKey][]queuedPush),
		flushing:          make(map[sessionKey]bool),
		log:               log,
	}
}

// SetLifecycleObserver installs a best-effort active session lifecycle observer.
func (m *SessionManager) SetLifecycleObserver(observer SessionLifecycleObserver) {
	m.mu.Lock()
	m.lifecycle = observer
	m.mu.Unlock()
}

// Register 注册一个活跃连接。若同 raw auth_key_id + session_id 已存在（重连），旧连接被替换并移除索引。
func (m *SessionManager) Register(c *Conn) {
	m.mu.Lock()

	key := connSessionKey(c)
	var replaced *Conn
	var evicted *Conn
	if old, ok := m.bySession[key]; ok && old != c {
		replaced = old
		m.removeLocked(old, false)
	} else if existing := m.byAuthKey[c.authKeyID]; len(existing) >= maxSessionsPerAuthKey {
		// 同 raw auth_key 的 session 数达上限且本次是新 session：驱逐建连最早的 session 让位，
		// 防对抗客户端用海量 session_id 撑爆索引。驱逐对象与新连接同属一个设备凭据，
		// 触顶基本是该凭据自身异常；选最旧而非 map 随机，避免误杀刚建立的活跃下载/主连接。
		// 被驱逐连接的 serveConn 会在下一帧因 actor 已关而退出。O(cap) 扫描仅在触顶时发生。
		for _, ec := range existing {
			if evicted == nil || ec.createdAt.Before(evicted.createdAt) {
				evicted = ec
			}
		}
		m.removeLocked(evicted, true)
		m.log.Debug("Evicted oldest session for auth key at cap",
			zap.String("auth_key_id", sessionKeyLog(c.authKeyID)),
			zap.Int("cap", maxSessionsPerAuthKey),
		)
	}
	m.bySession[key] = c
	addSessionIDIndex(m.bySessionID, c.sessionID, c.authKeyID, c)
	addConnIndex(m.byAuthKey, c.authKeyID, c.sessionID, c)
	if businessAuthKeyID, resolved := c.BusinessAuthKeyID(); resolved {
		addBusinessAuthKeyIndex(m.byBusinessAuthKey, businessAuthKeyID, key, c)
	}
	if uid := c.userID.Load(); uid != 0 {
		c.userIDResolved.Store(true)
		addUserIndex(m.byUser, uid, key, c)
	}
	m.log.Debug("Session registered",
		zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
		zap.Int64("session_id", c.sessionID),
		zap.Int("online", len(m.bySession)),
	)
	m.mu.Unlock()

	if replaced != nil {
		replaced.Close()
	}
	if evicted != nil {
		evicted.Close()
	}
}

// Unregister 注销一个连接（仅当它仍是当前注册的同一对象，避免误删重连后的新连接）。
// 观察者对未登录连接（userID=0）也回调：业务层据此清理按 session 维度的缓存条目，
// 否则未登录连接的元数据只能等容量上限驱逐。
func (m *SessionManager) Unregister(c *Conn) {
	m.mu.Lock()
	var (
		observer    SessionLifecycleObserver
		offlineUser int64
		lastForUser bool
	)
	if cur, ok := m.bySession[connSessionKey(c)]; ok && cur == c {
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

// DestroySession 移除指定 session 的运行态索引，供 MTProto destroy_session 使用。
func (m *SessionManager) DestroySession(sessionID int64) bool {
	m.mu.Lock()
	c, key, ok, ambiguous := m.uniqueSessionLocked(sessionID)
	if ambiguous || !ok {
		if !ambiguous {
			m.dropPendingBySessionLocked(sessionID)
		}
		m.mu.Unlock()
		return false
	}
	offlineUser := m.removeLocked(c, true)
	lastForUser := offlineUser != 0 && len(m.byUser[offlineUser]) == 0
	observer := m.lifecycle
	m.log.Debug("Session destroyed",
		zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
		zap.Int64("session_id", sessionID),
		zap.Int("online", len(m.bySession)),
	)
	m.mu.Unlock()
	c.Close()
	if observer != nil && offlineUser != 0 {
		observer.SessionOffline(key.authKeyID, sessionID, offlineUser, lastForUser)
	}
	return true
}

// DestroySessionForAuthKey 精确移除某个 raw auth_key_id 下的 session。
func (m *SessionManager) DestroySessionForAuthKey(authKeyID [8]byte, sessionID int64) bool {
	m.mu.Lock()
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	if !ok {
		delete(m.pending, key)
		m.mu.Unlock()
		return false
	}
	offlineUser := m.removeLocked(c, true)
	lastForUser := offlineUser != 0 && len(m.byUser[offlineUser]) == 0
	observer := m.lifecycle
	m.log.Debug("Session destroyed",
		zap.String("auth_key_id", sessionKeyLog(authKeyID)),
		zap.Int64("session_id", sessionID),
		zap.Int("online", len(m.bySession)),
	)
	m.mu.Unlock()
	c.Close()
	if observer != nil && offlineUser != 0 {
		observer.SessionOffline(authKeyID, sessionID, offlineUser, lastForUser)
	}
	return true
}

// BindUser 缓存 session 的授权用户。userID=0 表示当前 auth_key 已确认未登录。
// 登录后绑定非 0 userID，使其可经 PushToUser 收到推送。
func (m *SessionManager) BindUser(sessionID, userID int64) {
	m.mu.Lock()
	c, key, ok, ambiguous := m.uniqueSessionLocked(sessionID)
	if ambiguous || !ok {
		if ambiguous {
			m.log.Warn("Skip BindUser for ambiguous session_id", zap.Int64("session_id", sessionID))
		}
		m.mu.Unlock()
		return
	}
	m.bindUserLocked(c, key, userID)
	m.mu.Unlock()
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
			delete(m.pending, key)
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
		delete(m.pending, key)
		delete(m.flushing, key)
	}
}

// UserID 返回 session 当前缓存的登录用户 id。未绑定或离线时 ok=false。
func (m *SessionManager) UserID(sessionID int64) (int64, bool) {
	m.mu.RLock()
	c, _, ok, ambiguous := m.uniqueSessionLocked(sessionID)
	m.mu.RUnlock()
	if ambiguous || !ok {
		return 0, false
	}
	userID := c.userID.Load()
	if userID == 0 {
		return 0, false
	}
	return userID, true
}

// UserIDForAuthKey 返回指定 raw auth_key_id + session_id 当前缓存的登录用户 id。
func (m *SessionManager) UserIDForAuthKey(authKeyID [8]byte, sessionID int64) (int64, bool) {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: authKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return 0, false
	}
	userID := c.userID.Load()
	if userID == 0 {
		return 0, false
	}
	return userID, true
}

// UserIDResolved 返回 session 的 user_id 授权状态是否已经查过。
// resolved=true 且 userID=0 表示该 session 当前未登录。
func (m *SessionManager) UserIDResolved(sessionID int64) (int64, bool) {
	m.mu.RLock()
	c, _, ok, ambiguous := m.uniqueSessionLocked(sessionID)
	m.mu.RUnlock()
	if ambiguous || !ok {
		return 0, false
	}
	return c.UserIDResolved()
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

// BindAuthKey 缓存业务视角 auth_key_id（temp auth_key 解析后的 perm auth_key）。
func (m *SessionManager) BindAuthKey(sessionID int64, authKeyID [8]byte) {
	m.mu.Lock()
	c, key, ok, ambiguous := m.uniqueSessionLocked(sessionID)
	if ambiguous || !ok {
		if ambiguous {
			m.log.Warn("Skip BindAuthKey for ambiguous session_id", zap.Int64("session_id", sessionID))
		}
		m.mu.Unlock()
		return
	}
	m.bindAuthKeyLocked(c, key, authKeyID)
	m.mu.Unlock()
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
		delete(m.pending, key)
		delete(m.flushing, key)
		c.userID.Store(0)
		c.userIDResolved.Store(false)
	}
}

// AuthKeyID 返回 session 缓存的业务视角 auth_key_id。
// ok=false 表示该连接尚未完成 temp→perm 解析。
func (m *SessionManager) AuthKeyID(sessionID int64) ([8]byte, bool) {
	m.mu.RLock()
	c, _, ok, ambiguous := m.uniqueSessionLocked(sessionID)
	m.mu.RUnlock()
	if ambiguous || !ok {
		return [8]byte{}, false
	}
	return c.BusinessAuthKeyID()
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
		uid := m.removeLocked(c, true)
		conns = append(conns, c)
		events = append(events, offlineEvent{key: key, userID: uid, last: uid != 0 && len(m.byUser[uid]) == 0})
	}
	observer := m.lifecycle
	if len(conns) > 0 {
		m.log.Debug("Force close sessions for revoked auth key",
			zap.String("auth_key_id", sessionKeyLog(authKeyID)),
			zap.Int("closed", len(conns)),
		)
	}
	m.mu.Unlock()
	for _, c := range conns {
		c.ForceClose()
	}
	if observer != nil {
		for _, e := range events {
			observer.SessionOffline(e.key.authKeyID, e.key.sessionID, e.userID, e.last)
		}
	}
	return len(conns)
}

// CloseSessionsForRawAuthKeyExcept 强制断开指定 raw auth_key 的活跃连接，可排除
// 一个 session（destroy_auth_key 的发起连接：响应要先送达，它的密钥已删，下一帧
// 自然失效）。出站推送不回查密钥库，必须主动断开底层 transport 才能让销毁立即生效。
func (m *SessionManager) CloseSessionsForRawAuthKeyExcept(authKeyID [8]byte, exceptSessionID int64) int {
	type offlineEvent struct {
		key    sessionKey
		userID int64
		last   bool
	}
	m.mu.Lock()
	var conns []*Conn
	var events []offlineEvent
	for sessionID, c := range m.byAuthKey[authKeyID] {
		if sessionID == exceptSessionID {
			continue
		}
		key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
		uid := m.removeLocked(c, true)
		conns = append(conns, c)
		events = append(events, offlineEvent{key: key, userID: uid, last: uid != 0 && len(m.byUser[uid]) == 0})
	}
	observer := m.lifecycle
	m.mu.Unlock()
	for _, c := range conns {
		c.ForceClose()
	}
	if observer != nil {
		for _, e := range events {
			observer.SessionOffline(e.key.authKeyID, e.key.sessionID, e.userID, e.last)
		}
	}
	return len(conns)
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
		delete(m.pending, key)
		delete(m.flushing, key)
		c.userIDResolved.Store(true)
		count++
	}
	return count
}

// SetReceivesUpdates 标记 session 是否已完成 updates 同步入口。
//
// TDesktop 登录后会先调用 updates.getState/getDifference 建立本地同步基线。
// 在此之前收到的主动 updates 先暂存，待 session 可接收后再异步下发。
func (m *SessionManager) SetReceivesUpdates(sessionID int64, receives bool) {
	m.mu.Lock()
	c, key, ok, ambiguous := m.uniqueSessionLocked(sessionID)
	if ambiguous || !ok {
		if ambiguous {
			m.log.Warn("Skip SetReceivesUpdates for ambiguous session_id", zap.Int64("session_id", sessionID))
		}
		m.mu.Unlock()
		return
	}
	owner, start := m.setReceivesUpdatesLocked(c, key, receives)
	m.mu.Unlock()

	if start {
		go m.runFlush(c, key, owner, 0)
	}
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
			delete(m.pending, key)
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
				delete(m.pending, key)
				delete(m.flushing, key)
				m.mu.Unlock()
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := c.Send(ctx, item.t, item.msg)
			cancel()
			if err == nil {
				continue
			}
			m.mu.Lock()
			if cur, ok := m.bySession[key]; !ok || cur != c || !m.flushing[key] || c.userID.Load() != owner {
				// 连接换代/取消/易主：剩余 batch 不属于当前连接当前账号，丢弃。
				if c.userID.Load() != owner {
					delete(m.pending, key)
					delete(m.flushing, key)
				}
				m.mu.Unlock()
				return
			}
			rest := append(append([]queuedPush(nil), batch[i:]...), m.pending[key]...)
			if len(rest) > maxPendingPushesPerSession {
				// 与 queueLocked 溢出策略一致：丢最旧留最新，让 pts 空洞集中在最前端，
				// flush 首条即触发客户端 gap 检测，恢复路径最短。
				rest = rest[len(rest)-maxPendingPushesPerSession:]
			}
			m.pending[key] = rest
			if attempt+1 >= maxFlushAttempts {
				// 重试用尽：置位激活避免 idle 客户端永久断流；剩余暂存中的 durable 更新
				// 由客户端后续 pts 空洞触发 getDifference 补齐。
				c.receivesUpdates.Store(true)
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

// PushToSession 向指定 session 推送一条消息。
func (m *SessionManager) PushToSession(ctx context.Context, sessionID int64, t proto.MessageType, msg bin.Encoder) error {
	m.mu.Lock()
	c, key, ok, ambiguous := m.uniqueSessionLocked(sessionID)
	if ambiguous {
		m.mu.Unlock()
		return ErrSessionAmbiguous
	}
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	if !c.receivesUpdates.Load() {
		m.queueLocked(key, t, msg)
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	return c.Send(ctx, t, msg)
}

// PushToSessionForAuthKey 向指定 raw auth_key_id + session_id 推送一条消息。
func (m *SessionManager) PushToSessionForAuthKey(ctx context.Context, authKeyID [8]byte, sessionID int64, t proto.MessageType, msg bin.Encoder) error {
	m.mu.Lock()
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	c, ok := m.bySession[key]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	if !c.receivesUpdates.Load() {
		m.queueLocked(key, t, msg)
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	return c.Send(ctx, t, msg)
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

// PushToUser 向某 user 所有活跃连接推送，返回已发送或已暂存的连接数。
// 发送在释放锁后进行，避免持锁阻塞于网络 IO。
func (m *SessionManager) PushToUser(ctx context.Context, userID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	return m.PushToUserExceptAuthKeySession(ctx, userID, [8]byte{}, 0, t, msg)
}

// PushToUserExceptSession 向某 user 所有活跃连接推送，但跳过指定 session。
// 未完成 updates 同步入口的 session 会先暂存，等 SetReceivesUpdates(true) 后再发。
func (m *SessionManager) PushToUserExceptSession(ctx context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	return m.pushToUser(ctx, userID, nil, excludeSessionID, t, msg)
}

// PushToUserExceptAuthKeySession 向某 user 所有活跃连接推送，跳过指定业务 auth_key + session。
func (m *SessionManager) PushToUserExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	return m.pushToUser(ctx, userID, &excludeAuthKeyID, excludeSessionID, t, msg)
}

// PushToUserAuthKey 把 msg 定向投递给【绑定到 businessAuthKeyID 这台具体设备】且属于
// userID 的就绪连接（密聊设备级投递的锚点）。索引走 byBusinessAuthKey（经
// businessAuthKeyCandidatesLocked，兼容 temp-key/PFS 连接），不是 byAuthKey（raw 索引会
// 漏 temp-key 设备）。未就绪连接跳过、不进 pending——密聊消息 durable 在 qts 队列，
// 离线设备靠 getDifference 补回（在线推送只是加速器）。c.userID 复查防跨账号泄露。
func (m *SessionManager) PushToUserAuthKey(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg bin.Encoder) (int, error) {
	getEncoded := onceEncodedOutbound(msg)
	return m.pushToBusinessAuthKey(ctx, userID, businessAuthKeyID, false, func(c *Conn) error {
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

// PushToUserAuthKeyTransient 是 PushToUserAuthKey 的 transient（typing）best-effort 版本。
func (m *SessionManager) PushToUserAuthKeyTransient(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	getEncoded := onceEncodedOutbound(msg)
	return m.pushToBusinessAuthKey(ctx, userID, businessAuthKeyID, true, func(c *Conn) error {
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

func (m *SessionManager) pushToBusinessAuthKey(ctx context.Context, userID int64, businessAuthKeyID [8]byte, transient bool, send func(*Conn) error) (int, error) {
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
	_ = transient
	var firstErr error
	sent := 0
	for _, c := range conns {
		// 锁外发送前复查身份，防收集后并发换绑导致跨账号泄露。
		if c.userID.Load() != userID {
			continue
		}
		if err := send(c); err != nil {
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
	getEncoded := onceEncodedOutbound(msg)
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
	getEncoded := onceEncodedOutbound(msg)
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

func (m *SessionManager) PushToUserExceptSessionBestEffort(ctx context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	return m.pushToUserBestEffort(ctx, userID, nil, excludeSessionID, t, msg, timeout)
}

func (m *SessionManager) PushToUserExceptAuthKeySessionBestEffort(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	return m.pushToUserBestEffort(ctx, userID, &excludeAuthKeyID, excludeSessionID, t, msg, timeout)
}

func (m *SessionManager) pushToUserBestEffort(ctx context.Context, userID int64, excludeAuthKeyID *[8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	getEncoded := onceEncodedOutbound(msg)
	return m.pushToUserWithSender(ctx, userID, excludeAuthKeyID, excludeSessionID, t, msg, true, func(c *Conn) error {
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

func onceEncodedOutbound(msg bin.Encoder) func() (*encodedOutboundMessage, error) {
	var (
		encoded *encodedOutboundMessage
		err     error
	)
	return func() (*encodedOutboundMessage, error) {
		if encoded == nil && err == nil {
			encoded, err = encodeOutboundMessage(msg)
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
				if m.queueLocked(key, t, msg) {
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

// Online 返回当前活跃连接数。
func (m *SessionManager) Online() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.bySession)
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
	delete(m.bySession, key)
	removeSessionIDIndex(m.bySessionID, c.sessionID, c.authKeyID)
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
		delete(m.pending, key)
	}
	delete(m.flushing, key)
	return uid
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
	for _, item := range q {
		if now.Sub(item.at) > pendingPushMaxAge {
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

// queueLocked 暂存一条主动推送，返回是否实际入队——stale 丢批分支会连同当前
// 这条一起丢弃，调用方据此区分 queued/dropped 计数，避免投递日志失真。
func (m *SessionManager) queueLocked(key sessionKey, t proto.MessageType, msg bin.Encoder) bool {
	q := m.pending[key]
	// 过期保护：最早一条暂存已超过 pendingPushMaxAge（session 迟迟未 ready）时，丢整批并
	// 不再囤这条，记 trace。避免「登录后从不 getState」的连接长期占用 pending 内存。
	if len(q) > 0 && time.Since(q[0].at) > pendingPushMaxAge {
		m.log.Debug("Drop stale pending pushes (session not ready in time)",
			zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
			zap.Int64("session_id", key.sessionID),
			zap.Int("dropped", len(q)),
		)
		delete(m.pending, key)
		return false
	}
	push := queuedPush{t: t, msg: msg, at: time.Now()}
	if len(q) >= maxPendingPushesPerSession {
		copy(q, q[1:])
		q[len(q)-1] = push
		m.pending[key] = q
		return true
	}
	m.pending[key] = append(q, push)
	return true
}

func (m *SessionManager) uniqueSessionLocked(sessionID int64) (*Conn, sessionKey, bool, bool) {
	set := m.bySessionID[sessionID]
	if len(set) == 0 {
		return nil, sessionKey{}, false, false
	}
	if len(set) > 1 {
		return nil, sessionKey{}, false, true
	}
	for authKeyID, c := range set {
		return c, sessionKey{authKeyID: authKeyID, sessionID: sessionID}, true, false
	}
	return nil, sessionKey{}, false, false
}

func (m *SessionManager) dropPendingBySessionLocked(sessionID int64) {
	for key := range m.pending {
		if key.sessionID == sessionID {
			delete(m.pending, key)
		}
	}
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
		delete(m.pending, key)
		dropped++
	}
	if dropped > 0 {
		m.log.Debug("Swept stale pending sessions", zap.Int("dropped_sessions", dropped))
	}
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

func addSessionIDIndex(idx map[int64]map[[8]byte]*Conn, sessionID int64, authKeyID [8]byte, c *Conn) {
	set := idx[sessionID]
	if set == nil {
		set = make(map[[8]byte]*Conn)
		idx[sessionID] = set
	}
	set[authKeyID] = c
}

func removeSessionIDIndex(idx map[int64]map[[8]byte]*Conn, sessionID int64, authKeyID [8]byte) {
	if set := idx[sessionID]; set != nil {
		delete(set, authKeyID)
		if len(set) == 0 {
			delete(idx, sessionID)
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
	return connUsesBusinessAuthKey(c, *excludeAuthKeyID)
}

func sessionKeyLog(id [8]byte) string {
	return fmt.Sprintf("%x", id)
}
