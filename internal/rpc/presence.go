package rpc

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const userOnlineTTL = 5 * time.Minute
const presenceDialogFanoutCandidateLimit = 512

// onlineNotifyRefreshInterval 是「每 RPC 上线广播」的去重间隔：同一 session 在此间隔内的在线续期
// 不再向对端重复广播在线态。TDesktop 的在线 TTL 为 5min，客户端 account.updateStatus 周期为分钟级；
// 普通 RPC 只需刷新内存水位，presence fan-out 以分钟级节流即可保持 UI 新鲜。
const onlineNotifyRefreshInterval = 2 * time.Minute

type presencePersistMode uint8

const (
	presencePersistSync presencePersistMode = iota
	presencePersistAsync
)

// offlineAnnounceGrace 是最后一个连接断开后广播 offline 的去抖宽限。
// 移动端重连/换 session 都是「先断旧再建新」，瞬时无连接会对全部好友广播一轮
// offline→online 抖动；宽限期内重新上线则跳过广播。被动查询不受去抖影响：
// presence 条目断连即清，查询回落到持久化 last_seen，语义一致。
// var 而非 const：测试需要缩短等待。
var offlineAnnounceGrace = 8 * time.Second

type presenceSessionKey struct {
	rawAuthKeyID [8]byte
	sessionID    int64
}

type presenceSessionState struct {
	userID int64
	status domain.UserStatus
}

type presenceTracker struct {
	mu        sync.RWMutex
	bySession map[presenceSessionKey]presenceSessionState
	byUser    map[int64]map[presenceSessionKey]domain.UserStatus
	// offlineTimers 跟踪每个 user 挂起的 offline 广播去抖定时器，使重新上线能取消它，
	// 避免断连风暴下 O(N) 个裸 time.AfterFunc 在 runtime timer heap 长期堆积。
	offlineTimers map[int64]*time.Timer
}

func newPresenceTracker() *presenceTracker {
	return &presenceTracker{
		bySession:     make(map[presenceSessionKey]presenceSessionState),
		byUser:        make(map[int64]map[presenceSessionKey]domain.UserStatus),
		offlineTimers: make(map[int64]*time.Timer),
	}
}

// armOfflineTimer 安排（或重置）某 user 的 offline 广播去抖定时器。定时器触发时先把自己
// 从 map 移除再执行 fire（fire 内部会查在线态做最终去抖），全程不持 p.mu 调用 fire。
func (p *presenceTracker) armOfflineTimer(userID int64, d time.Duration, fire func()) {
	if p == nil || userID == 0 {
		return
	}
	p.mu.Lock()
	if p.offlineTimers == nil {
		p.offlineTimers = make(map[int64]*time.Timer)
	}
	if old := p.offlineTimers[userID]; old != nil {
		old.Stop()
	}
	p.offlineTimers[userID] = time.AfterFunc(d, func() {
		p.mu.Lock()
		delete(p.offlineTimers, userID)
		p.mu.Unlock()
		fire()
	})
	p.mu.Unlock()
}

// cancelOfflineTimer 取消某 user 挂起的 offline 广播去抖定时器（重新上线时调用）。
func (p *presenceTracker) cancelOfflineTimer(userID int64) {
	if p == nil || userID == 0 {
		return
	}
	p.mu.Lock()
	if t := p.offlineTimers[userID]; t != nil {
		t.Stop()
		delete(p.offlineTimers, userID)
	}
	p.mu.Unlock()
}

// setSessionStatus 记录 session 的在线态，并返回是否需要向对端「广播在线」（notify）。
// notify 仅在「本 session 此前已在线、本次仍是在线续期、且距上次广播未超 onlineNotifyRefreshInterval」
// 时为 false——即每 RPC 的重复在线续期被去重。首次上线/离线/转在线/超刷新间隔一律 true。
// 整个判定在 p.mu 内原子完成：开群洪峰下并发首帧只有第一个返回 true，其余看到刚写入的新鲜态返回 false。
func (p *presenceTracker) setSessionStatus(key presenceSessionKey, userID int64, status domain.UserStatus) bool {
	if p == nil || userID == 0 {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	old, hadOld := p.bySession[key]
	if hadOld {
		p.removeSessionLocked(key, old.userID)
	}
	p.bySession[key] = presenceSessionState{userID: userID, status: status}
	sessions := p.byUser[userID]
	if sessions == nil {
		sessions = make(map[presenceSessionKey]domain.UserStatus)
		p.byUser[userID] = sessions
	}
	sessions[key] = status
	if hadOld && old.userID == userID &&
		old.status.Kind == domain.UserStatusOnline && status.Kind == domain.UserStatusOnline &&
		old.status.Expires-status.WasOnline > int((userOnlineTTL-onlineNotifyRefreshInterval)/time.Second) {
		return false
	}
	return true
}

func (p *presenceTracker) clearSession(key presenceSessionKey) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.bySession[key]; ok {
		p.removeSessionLocked(key, old.userID)
	}
}

func (p *presenceTracker) removeSessionLocked(key presenceSessionKey, userID int64) {
	delete(p.bySession, key)
	sessions := p.byUser[userID]
	delete(sessions, key)
	if len(sessions) == 0 {
		delete(p.byUser, userID)
	}
}

// expireOnline 把已过期的 online 条目原地降级为 offline，返回受影响用户的去重列表。
func (p *presenceTracker) expireOnline(now int) []int64 {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var users []int64
	seen := map[int64]struct{}{}
	for key, st := range p.bySession {
		if st.status.Kind != domain.UserStatusOnline || st.status.Expires > now {
			continue
		}
		offline := normalizePresenceStatus(st.status, now)
		p.bySession[key] = presenceSessionState{userID: st.userID, status: offline}
		if sessions := p.byUser[st.userID]; sessions != nil {
			sessions[key] = offline
		}
		if _, dup := seen[st.userID]; !dup {
			seen[st.userID] = struct{}{}
			users = append(users, st.userID)
		}
	}
	return users
}

func (p *presenceTracker) statusFor(userID int64, now int) (domain.UserStatus, bool) {
	if p == nil || userID == 0 {
		return domain.UserStatus{}, false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	sessions := p.byUser[userID]
	if len(sessions) == 0 {
		return domain.UserStatus{}, false
	}
	known := false
	var bestOnline domain.UserStatus
	var bestOffline domain.UserStatus
	for _, status := range sessions {
		status = normalizePresenceStatus(status, now)
		switch status.Kind {
		case domain.UserStatusOnline:
			if bestOnline.Expires == 0 || status.Expires > bestOnline.Expires {
				bestOnline = status
			}
			known = true
		case domain.UserStatusOffline:
			if bestOffline.WasOnline == 0 || status.WasOnline > bestOffline.WasOnline {
				bestOffline = status
			}
			known = true
		case domain.UserStatusRecently, domain.UserStatusLastWeek, domain.UserStatusLastMonth, domain.UserStatusEmpty:
			if !known {
				bestOffline = status
				known = true
			}
		}
	}
	if bestOnline.Kind == domain.UserStatusOnline {
		return bestOnline, true
	}
	if known {
		return bestOffline, true
	}
	return domain.UserStatus{}, false
}

func normalizePresenceStatus(status domain.UserStatus, now int) domain.UserStatus {
	if status.Kind == domain.UserStatusOnline && status.Expires <= now {
		wasOnline := status.WasOnline
		if wasOnline == 0 || wasOnline > status.Expires {
			wasOnline = status.Expires
		}
		return domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: wasOnline}
	}
	return status
}

func (r *Router) setPresenceFromContext(ctx context.Context, userID int64, offline bool, persistMode presencePersistMode) (domain.UserStatus, bool) {
	now := int(r.clock.Now().Unix())
	status := domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: now}
	if !offline {
		status = domain.UserStatus{
			Kind:      domain.UserStatusOnline,
			Expires:   now + int(userOnlineTTL/time.Second),
			WasOnline: now,
		}
		// 重新上线：取消可能挂起的 offline 去抖广播定时器。
		r.presence.cancelOfflineTimer(userID)
	}
	notify := true
	if key, ok := presenceSessionKeyFromContext(ctx); ok {
		notify = r.presence.setSessionStatus(key, userID, status)
	}
	// 在线续期（!offline）去抖；显式 offline 是权威写，强制落库。
	if persistMode == presencePersistAsync {
		r.persistLastSeenAsync(ctx, userID, now, !offline)
	} else {
		r.persistLastSeen(ctx, userID, now, !offline)
	}
	return r.userPresenceStatusForUser(domain.User{ID: userID, LastSeenAt: now}), notify
}

func (r *Router) announceSessionOnline(ctx context.Context, userID int64) {
	if userID == 0 {
		return
	}
	// bot 不参与 presence：官方 bot 无 status，从不产生 updateUserStatus，也不写
	// last_seen。bot 登录（importBotAuthorization）经此路径，必须整体短路——否则
	// 与 bot 有私聊的用户会收到协议中不存在的 bot 在线/离线广播，bot 也不登记
	// presence 条目，sweeper 因此天然不会遇到 bot。
	if r.userIsBot(ctx, userID) {
		return
	}
	status, notify := r.setPresenceFromContext(ctx, userID, false, presencePersistAsync)
	if !notify {
		// 本 session 已在线且在刷新间隔内：跳过「向对端广播在线 + 拉对端在线态给本 session」。
		// setPresenceFromContext 已每 RPC 续期在线水位；这里省掉开群洪峰下每 RPC 的扇出 + 推送
		// （实测每 RPC ~70ms，是去分区后开群尾延迟/CPU 的主要残余）。
		return
	}
	r.pushSessionOnlineAsync(ctx, userID, status)
}

// userIsBot 报告 userID 是否为 bot 账号（presence 广播豁免用）。仅在连接生命周期
// 事件（登录/断线/登出）调用，频率低；命中 redis UserCache。
func (r *Router) userIsBot(ctx context.Context, userID int64) bool {
	if userID == 0 || r.deps.Users == nil {
		return false
	}
	// bot 标志不可变 → 永久 in-process 缓存，避免 announceSessionOnline 每 RPC 都发一次重投影
	// Users.ByID。仅缓存已存在的用户结果。命中后纯内存。
	if v, ok := r.botStatus.Load(userID); ok {
		return v.(bool)
	}
	// 冷启动洪峰下用 singleflight 合并并发首查：~50 个并发首帧只打 1 次 PG（其余共享），
	// 避免 Users.ByID 重投影 herd（曾让首帧 user_resolve 飙到 ~1.1s）。
	v, _, _ := r.authUserSF.Do("bot:"+strconv.FormatInt(userID, 10), func() (any, error) {
		if cached, ok := r.botStatus.Load(userID); ok {
			return cached.(bool), nil
		}
		u, found, err := r.deps.Users.ByID(ctx, userID, userID)
		if err != nil || !found {
			return false, nil
		}
		r.botStatus.Store(userID, u.Bot)
		return u.Bot, nil
	})
	return v.(bool)
}

func (r *Router) userPresenceStatus(userID int64) domain.UserStatus {
	return r.userPresenceStatusForUser(domain.User{ID: userID})
}

func (r *Router) userPresenceStatusForUser(u domain.User) domain.UserStatus {
	userID := u.ID
	if userID == 0 {
		return domain.UserStatus{Kind: domain.UserStatusRecently}
	}
	// The app projector marks a privacy-hidden exact timestamp with a coarse
	// status and clears LastSeenAt. Never overlay the process presence tracker
	// after that boundary, or every users/dialogs/history response would undo
	// StatusTimestamp privacy.
	switch u.Status.Kind {
	case domain.UserStatusRecently,
		domain.UserStatusLastWeek,
		domain.UserStatusLastMonth,
		domain.UserStatusEmpty:
		return u.Status
	}
	now := int(r.clock.Now().Unix())
	if status, ok := r.presence.statusFor(userID, now); ok {
		return status
	}
	if provider, ok := r.deps.Sessions.(OnlineUserProvider); ok && provider.IsUserOnline(userID) {
		return domain.UserStatus{
			Kind:      domain.UserStatusOnline,
			Expires:   now + int(userOnlineTTL/time.Second),
			WasOnline: now,
		}
	}
	if u.LastSeenAt > 0 {
		return domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: u.LastSeenAt}
	}
	return domain.UserStatus{Kind: domain.UserStatusRecently}
}

type userLastSeenUpdater interface {
	UpdateLastSeen(ctx context.Context, userID int64, lastSeenAt int) error
}

func (r *Router) reserveLastSeenPersist(userID int64, lastSeenAt int, debounce bool) (userLastSeenUpdater, bool) {
	if userID == 0 || lastSeenAt <= 0 || r.deps.Users == nil {
		return nil, false
	}
	updater, ok := r.deps.Users.(userLastSeenUpdater)
	if !ok {
		return nil, false
	}
	now := r.clock.Now().Unix()
	if debounce {
		if last, ok := r.lastSeenPersist.Load(userID); ok {
			if lastUnix, ok := last.(int64); ok && now-lastUnix < int64(lastSeenPersistDebounce/time.Second) {
				return nil, false
			}
		}
	}
	r.lastSeenPersist.Store(userID, now)
	return updater, true
}

// persistLastSeen 落库 last_seen。debounce=true 时（在线高频续期）数秒内只落一次 DB；
// debounce=false 是权威写（显式 offline / 断连），从不跳过。
func (r *Router) persistLastSeen(ctx context.Context, userID int64, lastSeenAt int, debounce bool) {
	updater, ok := r.reserveLastSeenPersist(userID, lastSeenAt, debounce)
	if !ok {
		return
	}
	if err := updater.UpdateLastSeen(ctx, userID, lastSeenAt); err != nil {
		r.log.Warn("Update user last seen failed", zap.Int64("user_id", userID), zap.Int("last_seen_at", lastSeenAt), zap.Error(err))
	}
}

func (r *Router) persistLastSeenAsync(ctx context.Context, userID int64, lastSeenAt int, debounce bool) {
	updater, ok := r.reserveLastSeenPersist(userID, lastSeenAt, debounce)
	if !ok {
		return
	}
	bgCtx, cancel := r.presenceBackgroundContext(ctx, 10*time.Second)
	go func() {
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Error("Update user last seen panicked", zap.Int64("user_id", userID), zap.Any("panic", rec))
			}
		}()
		if err := updater.UpdateLastSeen(bgCtx, userID, lastSeenAt); err != nil {
			r.log.Warn("Update user last seen failed", zap.Int64("user_id", userID), zap.Int("last_seen_at", lastSeenAt), zap.Error(err))
		}
	}()
}

func (r *Router) pushSessionOnlineAsync(ctx context.Context, userID int64, status domain.UserStatus) {
	pushCtx, cancel := r.presenceBackgroundContext(ctx, 10*time.Second)
	go func() {
		defer cancel()
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Error("push session online panicked", zap.Int64("user_id", userID), zap.Any("panic", rec))
			}
		}()
		r.pushUserStatus(pushCtx, userID, status)
		r.pushOnlinePeerStatusesToCurrentSession(pushCtx, userID)
	}()
}

func (r *Router) presenceBackgroundContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	bg, cancel := context.WithTimeout(context.Background(), timeout)
	if rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx); ok {
		bg = WithRawAuthKeyID(bg, rawAuthKeyID)
	}
	if authKeyID, ok := AuthKeyIDFrom(ctx); ok {
		bg = WithAuthKeyID(bg, authKeyID)
	}
	if sessionID, ok := SessionIDFrom(ctx); ok {
		bg = WithSessionID(bg, sessionID)
	}
	if userID, ok := UserIDFrom(ctx); ok {
		bg = WithUserID(bg, userID)
	}
	if layer := LayerFrom(ctx); layer != 0 {
		bg = WithLayer(bg, layer)
	}
	if info, ok := ClientInfoFrom(ctx); ok {
		bg = WithClientInfo(bg, info)
	}
	if invokeWithoutUpdatesFrom(ctx) {
		bg = withInvokeWithoutUpdates(bg)
	}
	return bg, cancel
}

// SessionOffline is called by mtprotoedge when an active connection disappears.
// Business-side effects stay here: mtprotoedge only reports lifecycle facts.
//
// 同步路径只做内存清理（presence/clientInfo 条目）；持久化与 offline 广播经
// 去抖宽限后在后台带超时执行——连接关闭发生在 serveConn 退出路径上，同步做
// DB 写与逐 peer 查询会在断连风暴时放大 DB 压力并拖住 goroutine 退出。
func (r *Router) SessionOffline(rawAuthKeyID [8]byte, sessionID, userID int64, lastForUser bool) {
	r.forgetClientSessionInfo(rawAuthKeyID, sessionID)
	if userID == 0 {
		return
	}
	key := presenceSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	r.presence.clearSession(key)
	if !lastForUser {
		return
	}
	disconnectedAt := int(r.clock.Now().Unix())
	// 用可跟踪的定时器，重新上线时能取消（见 cancelOfflineTimer），避免断连风暴堆积。
	r.presence.armOfflineTimer(userID, offlineAnnounceGrace, func() {
		r.announceUserOfflineIfStillGone(rawAuthKeyID, sessionID, userID, disconnectedAt)
	})
}

// announceUserOfflineIfStillGone 在去抖宽限到期后持久化 last_seen 并广播 offline；
// 宽限期内用户重新上线则整体跳过。WasOnline 取断连时刻，数据不受去抖延迟影响。
func (r *Router) announceUserOfflineIfStillGone(rawAuthKeyID [8]byte, sessionID, userID int64, disconnectedAt int) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("announce user offline panicked", zap.Int64("user_id", userID), zap.Any("panic", rec))
		}
	}()
	if provider, ok := r.deps.Sessions.(OnlineUserProvider); ok && provider.IsUserOnline(userID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = WithSessionID(WithRawAuthKeyID(ctx, rawAuthKeyID), sessionID)
	// bot 不广播 offline、不写 last_seen（与 announceSessionOnline 对称）。
	if r.userIsBot(ctx, userID) {
		return
	}
	status := domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: disconnectedAt}
	// 断连降级是权威 last_seen，强制落库（不去抖）。
	r.persistLastSeen(ctx, userID, disconnectedAt, false)
	r.pushUserStatus(ctx, userID, status)
}

// RunPresenceSweeper 周期把过期的 online presence 降级为 offline 并广播给在线
// 关注方。客户端正常每隔几分钟 updateStatus 续期；停止续期（后台挂起）的设备
// 到期后好友应主动收到 offline，而不是等到下次查询才看到状态变化。
func (r *Router) RunPresenceSweeper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		r.sweepExpiredPresence(ctx)
	}
}

func (r *Router) sweepExpiredPresence(ctx context.Context) {
	now := int(r.clock.Now().Unix())
	for _, userID := range r.presence.expireOnline(now) {
		status, ok := r.presence.statusFor(userID, now)
		if ok && status.Kind == domain.UserStatusOnline {
			continue // 该用户另有未过期的 online session
		}
		if !ok {
			status = domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: now}
		}
		pushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		r.pushUserStatus(pushCtx, userID, status)
		cancel()
	}
}

func presenceSessionKeyFromContext(ctx context.Context) (presenceSessionKey, bool) {
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return presenceSessionKey{}, false
	}
	if rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx); ok {
		return presenceSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}, true
	}
	if authKeyID, ok := AuthKeyIDFrom(ctx); ok {
		return presenceSessionKey{rawAuthKeyID: authKeyID, sessionID: sessionID}, true
	}
	return presenceSessionKey{sessionID: sessionID}, true
}

func (r *Router) withUserPresence(u domain.User) domain.User {
	if u.ID == 0 {
		return u
	}
	// bot 不参与 presence：官方 bot user 不带 status（客户端状态栏恒显 "bot"），
	// tg 转换层对 bot 也不会输出 Status。
	if u.Bot {
		return u
	}
	u.Status = r.userPresenceStatusForUser(u)
	return u
}

func (r *Router) withUsersPresence(users []domain.User) []domain.User {
	if len(users) == 0 {
		return users
	}
	out := append([]domain.User(nil), users...)
	for i := range out {
		out[i] = r.withUserPresence(out[i])
	}
	return out
}

func (r *Router) withDialogListPresence(ctx context.Context, viewerUserID int64, list domain.DialogList) domain.DialogList {
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, msg := range list.Messages {
		collectMessagePeerRefs(msg, 0, userIDs, channelIDs)
	}
	for _, msg := range list.ChannelMessages {
		collectChannelMessagePeerRefs(msg, msg.ChannelID, userIDs, channelIDs)
	}
	cache := newViewerPeerCache(r)
	list.Users = r.withUsersPresence(mergeDomainUsers(list.Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))...))
	list.Channels = mergeDomainChannels(list.Channels, cache.channelsForIDs(ctx, viewerUserID, mapKeys(channelIDs))...)
	return list
}

func (r *Router) withUserSearchPresence(res domain.UserSearchResult) domain.UserSearchResult {
	res.MyResults = r.withUsersPresence(res.MyResults)
	res.Results = r.withUsersPresence(res.Results)
	return res
}

func (r *Router) tgUser(u domain.User) *tg.User {
	return r.withBotProfileFlags(context.Background(), tgUser(r.withUserPresence(u)))
}

func (r *Router) tgSelfUser(u domain.User) *tg.User {
	return r.withBotProfileFlags(context.Background(), tgSelfUser(r.withUserPresence(u)))
}

func (r *Router) tgUsers(users []domain.User) []tg.UserClass {
	out := tgUsers(r.withUsersPresence(users))
	r.withBotProfileFlagsForUsers(context.Background(), out)
	return out
}

// tgUsersForViewer 与 tgUsers 同样补 presence，但 viewer 自己的 user 走 self 分支
// （DrKLO 靠 user.self 判定 Saved Messages；self=false 会污染账号缓存）。
func (r *Router) tgUsersForViewer(viewerUserID int64, users []domain.User) []tg.UserClass {
	out := tgUsersForViewer(viewerUserID, r.withUsersPresence(users))
	r.withBotProfileFlagsForUsers(context.Background(), out)
	return out
}

func (r *Router) withBotProfileFlagsForUsers(ctx context.Context, users []tg.UserClass) {
	if r.deps.Bots == nil || len(users) == 0 {
		return
	}
	botUsers := make(map[int64][]*tg.User)
	ids := make([]int64, 0)
	for _, user := range users {
		u, ok := user.(*tg.User)
		if !ok || u == nil || !u.Bot || u.ID == 0 {
			continue
		}
		if _, ok := botUsers[u.ID]; !ok {
			ids = append(ids, u.ID)
		}
		botUsers[u.ID] = append(botUsers[u.ID], u)
	}
	if len(ids) == 0 {
		return
	}
	if batch, ok := r.deps.Bots.(botProfileBatchResolver); ok {
		if profiles, err := batch.BotInfos(ctx, ids); err == nil {
			for id, items := range botUsers {
				profile, found := profiles[id]
				if !found {
					continue
				}
				for _, u := range items {
					applyBotProfileFlags(u, profile)
				}
			}
			return
		}
	}
	for id, items := range botUsers {
		profile, found, err := r.deps.Bots.BotInfo(ctx, id)
		if err != nil || !found {
			continue
		}
		for _, u := range items {
			applyBotProfileFlags(u, profile)
		}
	}
}

func (r *Router) withBotProfileFlags(ctx context.Context, out *tg.User) *tg.User {
	if out == nil || !out.Bot || r.deps.Bots == nil {
		return out
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, out.ID)
	if err != nil || !found {
		return out
	}
	applyBotProfileFlags(out, profile)
	return out
}

func applyBotProfileFlags(out *tg.User, profile domain.BotProfile) {
	if out == nil {
		return
	}
	if profile.ChatHistory {
		out.SetBotChatHistory(true)
	}
	if profile.Nochats {
		out.SetBotNochats(true)
	}
	if profile.InlineGeo {
		out.SetBotInlineGeo(true)
	}
	if profile.InlinePlaceholder != "" {
		out.SetBotInlinePlaceholder(profile.InlinePlaceholder)
	}
	if profile.HasMainApp {
		out.SetBotHasMainApp(true)
	}
	if profile.HasAttachMenu {
		out.SetBotAttachMenu(true)
	}
}

func (r *Router) pushUserStatus(ctx context.Context, userID int64, status domain.UserStatus) {
	if userID == 0 {
		return
	}
	update := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUserStatus{
			UserID: userID,
			Status: tgUserStatus(status),
		}},
		Date: int(r.clock.Now().Unix()),
		Seq:  0,
	}
	// presence 是 transient（不写 durable log）：未就绪的 session 直接跳过、不进 pending。
	r.pushUserMessageTransient(ctx, userID, "push user status (own sessions)", update)
	// 接收者集 = 与本 user 有联系人/私聊关系且当前在线的对端。用「本 user 自己的
	// contacts + dialog 对端 ∩ 在线」一次性算出（onlineRelevantPeerIDs，2 次查询 + 内存过滤），
	// 而非遍历全部在线候选逐个 GetPeerDialogs（旧 onlinePrivateDialogPeerIDs 的 O(在线数) N+1，
	// 断连/sweeper 风暴下 O(M×512) 串行 PG）。私聊 dialog 双向建行，两种算法得到同一集合。
	recipients := r.onlineRelevantPeerIDs(ctx, userID)
	visible := r.statusTimestampVisibleToViewers(ctx, userID, recipients)
	for _, recipientID := range recipients {
		if recipientID == userID {
			continue
		}
		if !visible[recipientID] {
			continue
		}
		r.pushUserMessageTransient(ctx, recipientID, "push user status", update)
	}
}

func (r *Router) pushOnlinePeerStatusesToCurrentSession(ctx context.Context, userID int64) {
	peerIDs := r.onlineRelevantPeerIDs(ctx, userID)
	if len(peerIDs) == 0 {
		return
	}
	updates := make([]tg.UpdateClass, 0, len(peerIDs))
	visible := r.statusTimestampVisibleToViewer(ctx, peerIDs, userID)
	for _, peerID := range peerIDs {
		if !visible[peerID] {
			continue
		}
		status := r.userPresenceStatus(peerID)
		if status.Kind != domain.UserStatusOnline {
			continue
		}
		updates = append(updates, &tg.UpdateUserStatus{
			UserID: peerID,
			Status: tgUserStatus(status),
		})
	}
	if len(updates) == 0 {
		return
	}
	r.pushCurrentSessionMessage(ctx, "push online peer statuses", &tg.Updates{
		Updates: updates,
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	})
}

type batchPrivacyEvaluator interface {
	CanSeeBatch(ctx context.Context, ownerUserIDs []int64, viewerUserID int64, keys []domain.PrivacyKey) (map[int64]map[domain.PrivacyKey]bool, error)
}

type matrixPrivacyEvaluator interface {
	CanSeeMatrix(ctx context.Context, ownerUserIDs, viewerUserIDs []int64, keys []domain.PrivacyKey) (map[int64]map[int64]map[domain.PrivacyKey]bool, error)
}

func (r *Router) statusTimestampVisibleToViewer(ctx context.Context, ownerUserIDs []int64, viewerUserID int64) map[int64]bool {
	out := make(map[int64]bool, len(ownerUserIDs))
	if r.deps.Privacy == nil {
		for _, ownerID := range ownerUserIDs {
			out[ownerID] = true
		}
		return out
	}
	if batch, ok := r.deps.Privacy.(batchPrivacyEvaluator); ok {
		visibility, err := batch.CanSeeBatch(ctx, ownerUserIDs, viewerUserID, []domain.PrivacyKey{domain.PrivacyKeyStatusTimestamp})
		if err != nil {
			r.log.Warn("evaluate status privacy batch", zap.Int64("viewer_user_id", viewerUserID), zap.Error(err))
			return out
		}
		for _, ownerID := range ownerUserIDs {
			out[ownerID] = visibility[ownerID][domain.PrivacyKeyStatusTimestamp]
		}
		return out
	}
	for _, ownerID := range ownerUserIDs {
		allowed, err := r.deps.Privacy.CanSee(ctx, ownerID, viewerUserID, domain.PrivacyKeyStatusTimestamp)
		if err == nil {
			out[ownerID] = allowed
		}
	}
	return out
}

func (r *Router) statusTimestampVisibleToViewers(ctx context.Context, ownerUserID int64, viewerUserIDs []int64) map[int64]bool {
	out := make(map[int64]bool, len(viewerUserIDs))
	if r.deps.Privacy == nil {
		for _, viewerID := range viewerUserIDs {
			out[viewerID] = true
		}
		return out
	}
	if matrix, ok := r.deps.Privacy.(matrixPrivacyEvaluator); ok {
		visibility, err := matrix.CanSeeMatrix(ctx, []int64{ownerUserID}, viewerUserIDs, []domain.PrivacyKey{domain.PrivacyKeyStatusTimestamp})
		if err != nil {
			r.log.Warn("evaluate status privacy matrix", zap.Int64("owner_user_id", ownerUserID), zap.Error(err))
			return out
		}
		for _, viewerID := range viewerUserIDs {
			out[viewerID] = visibility[ownerUserID][viewerID][domain.PrivacyKeyStatusTimestamp]
		}
		return out
	}
	for _, viewerID := range viewerUserIDs {
		allowed, err := r.deps.Privacy.CanSee(ctx, ownerUserID, viewerID, domain.PrivacyKeyStatusTimestamp)
		if err == nil {
			out[viewerID] = allowed
		}
	}
	return out
}

// pushStatusPrivacyRefresh immediately replaces stale exact statuses on other
// online accounts after StatusTimestamp changes. Allowed viewers receive the
// live value; denied viewers receive only a coarse bucket.
func (r *Router) pushStatusPrivacyRefresh(ctx context.Context, ownerUserID int64) {
	recipients := r.onlineRelevantPeerIDs(ctx, ownerUserID)
	visible := r.statusTimestampVisibleToViewers(ctx, ownerUserID, recipients)
	exact := r.userPresenceStatus(ownerUserID)
	if r.deps.Users != nil {
		if owner, found, err := r.deps.Users.ByID(ctx, ownerUserID, ownerUserID); err == nil && found {
			exact = r.userPresenceStatusForUser(owner)
		}
	}
	coarse := domain.ApproximateUserStatus(exact.WasOnline, int(r.clock.Now().Unix()))
	for _, recipientID := range recipients {
		if recipientID == 0 || recipientID == ownerUserID {
			continue
		}
		status := coarse
		if visible[recipientID] {
			status = exact
		}
		r.pushUserMessageTransient(ctx, recipientID, "push status privacy refresh", &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateUserStatus{UserID: ownerUserID, Status: tgUserStatus(status)}},
			Date:    int(r.clock.Now().Unix()),
			Seq:     0,
		})
	}
}

// presenceCandidateCacheTTL 是 presence fan-out 候选集（联系人 ∪ 私聊对端）的缓存有效期；
// lastSeenPersistDebounce 是在线续期写 last_seen 的去抖窗口。
const (
	presenceCandidateCacheTTL = 2 * time.Minute
	lastSeenPersistDebounce   = 25 * time.Second
)

type presenceCandidateEntry struct {
	ids      []int64
	expireAt time.Time
}

func (r *Router) onlineRelevantPeerIDs(ctx context.Context, userID int64) []int64 {
	if userID == 0 {
		return nil
	}
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return nil
	}
	candidates := r.presenceFanoutCandidates(ctx, userID)
	if len(candidates) == 0 {
		return nil
	}
	// online 过滤每次实时进行（不进缓存），所以缓存只影响「候选范围」，不影响在线判定时效。
	return provider.OnlineUserIDsForCandidates(candidates, 0)
}

// presenceFanoutCandidates 返回 presence fan-out 的候选 peer 集合（联系人 ∪ 私聊对端，online
// 过滤前），带短 TTL 缓存。命中时零 DB 查询；未命中走原 hydration 路径算一次并缓存。候选集
// 变动很慢（加好友 / 开新私聊），陈旧最多 TTL 秒生效，对 presence（best-effort、transient）可接受。
// 仅在两条查询都未报错时才写缓存，避免把瞬时错误导致的残缺集合钉住 TTL。
func (r *Router) presenceFanoutCandidates(ctx context.Context, userID int64) []int64 {
	now := r.clock.Now()
	if cached, ok := r.presenceCandidateCache.Load(userID); ok {
		if entry, ok := cached.(*presenceCandidateEntry); ok && entry.expireAt.After(now) {
			return entry.ids
		}
	}
	seen := map[int64]struct{}{}
	candidates := make([]int64, 0)
	add := func(id int64) {
		if id == 0 || id == userID {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		candidates = append(candidates, id)
	}
	failed := false
	if r.deps.Contacts != nil {
		ids, notModified, err := r.deps.Contacts.ContactIDs(ctx, userID, 0)
		if err != nil {
			failed = true
		} else if !notModified {
			for _, id := range ids {
				add(int64(id))
			}
		}
	}
	if r.deps.Dialogs != nil {
		list, err := r.deps.Dialogs.GetDialogs(ctx, userID, domain.DialogFilter{Limit: presenceDialogFanoutCandidateLimit})
		if err != nil {
			failed = true
		} else {
			for _, dialog := range list.Dialogs {
				if dialog.Peer.Type == domain.PeerTypeUser {
					add(dialog.Peer.ID)
				}
			}
		}
	}
	if !failed {
		r.presenceCandidateCache.Store(userID, &presenceCandidateEntry{ids: candidates, expireAt: now.Add(presenceCandidateCacheTTL)})
	}
	return candidates
}
