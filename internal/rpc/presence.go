package rpc

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const userOnlineTTL = 5 * time.Minute
const presenceDialogFanoutCandidateLimit = 512

// onlineNotifyRefreshInterval is the dedup interval for the "broadcast online on
// every RPC" behavior: an online renewal for the same session within this interval
// no longer re-broadcasts the online state to peers. TDesktop's online TTL is 5min
// and the client's account.updateStatus period is on the order of minutes; an
// ordinary RPC only needs to refresh the in-memory watermark, so throttling the
// presence fan-out to the minute level keeps the UI fresh.
const onlineNotifyRefreshInterval = 2 * time.Minute

type presencePersistMode uint8

const (
	presencePersistSync presencePersistMode = iota
	presencePersistAsync
)

// offlineAnnounceGrace is the debounce grace before broadcasting offline after the
// last connection drops. Mobile reconnects / session switches are all "tear down
// the old, then build the new", and a momentary absence of connections would
// broadcast a round of offline->online churn to all friends; coming back online
// within the grace period skips the broadcast. Passive queries are unaffected by
// the debounce: a presence entry is cleared as soon as the connection drops, and
// the query falls back to the persisted last_seen, so the semantics are unchanged.
// A var rather than a const: tests need to shorten the wait.
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
	// offlineTimers tracks each user's pending offline-broadcast debounce timer so
	// going back online can cancel it, avoiding O(N) bare time.AfterFunc timers
	// piling up in the runtime timer heap during a disconnect storm.
	offlineTimers         map[int64]*time.Timer
	offlineRunning        int64
	lastSeenDirectRunning int64
}

func newPresenceTracker() *presenceTracker {
	return &presenceTracker{
		bySession:     make(map[presenceSessionKey]presenceSessionState),
		byUser:        make(map[int64]map[presenceSessionKey]domain.UserStatus),
		offlineTimers: make(map[int64]*time.Timer),
	}
}

// armOfflineTimer replaces the user's pending grace timer. Stop cannot join
// an already-started AfterFunc; the callback must still prove timer ownership.
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
	var timer *time.Timer
	timer = time.AfterFunc(d, func() {
		p.mu.Lock()
		if p.offlineTimers[userID] != timer {
			p.mu.Unlock()
			return
		}
		delete(p.offlineTimers, userID)
		p.offlineRunning++
		p.mu.Unlock()
		defer func() {
			p.mu.Lock()
			p.offlineRunning--
			p.mu.Unlock()
		}()
		fire()
	})
	p.offlineTimers[userID] = timer
	p.mu.Unlock()
}

// cancelOfflineTimer cancels a user's pending offline-broadcast debounce timer
// (called when going back online).
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

// setSessionStatus records the session's online state and returns whether the peer
// needs to hear about it ("broadcast online", notify). notify is false only when
// "this session was already online, is still renewing online, and the time since
// the last broadcast is under onlineNotifyRefreshInterval" - i.e. repeated online
// renewals on every RPC are deduplicated. First online/offline/switch-to-online/
// interval exceeded are always true. The whole decision is done atomically inside
// p.mu: on a burst of channel creation only the first concurrent frame returns
// true, and the rest see the freshly written state and return false.
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

// dropUser removes every session presence entry of a user and cancels the
// pending offline grace timer. Used when an account transitions into the frozen
// state: the frozen account must never render online toward any peer.
func (p *presenceTracker) dropUser(userID int64) {
	if p == nil || userID == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if sessions := p.byUser[userID]; len(sessions) > 0 {
		for key := range sessions {
			delete(p.bySession, key)
		}
		delete(p.byUser, userID)
	}
	if t := p.offlineTimers[userID]; t != nil {
		t.Stop()
		delete(p.offlineTimers, userID)
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

// expireOnline downgrades expired online entries to offline in place and returns
// the deduplicated list of affected users.
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

// isDeletedAccount reports whether the peer is an account tombstone. Deletion
// keeps every row for history, so this is an authoritative row read rather than
// an in-process mask like the frozen one.
func (r *Router) isDeletedAccount(userID int64) bool {
	if r == nil || userID == 0 || r.deps.Users == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u, found, err := r.deps.Users.ByID(ctx, userID, userID)
	if err != nil || !found {
		return false
	}
	return u.Deleted
}

// isFrozenPresenceUser reports whether userID is under an account freeze and
// must never render/broadcast online. Pure in-process mask check, O(1).
func (r *Router) isFrozenPresenceUser(userID int64) bool {
	if r == nil || userID == 0 {
		return false
	}
	_, frozen := r.frozenPresence.Load(userID)
	return frozen
}

// seedFrozenPresence backfills the freeze presence mask from the durable
// account-freeze fact. Session binds are the recovery hook after a restart:
// already-consumed notifications leave no dispatch to re-populate the mask, so
// the first connection of a frozen account must stop the online watermark here.
func (r *Router) seedFrozenPresence(ctx context.Context, userID int64) {
	if r == nil || userID == 0 || r.deps.AccountFreeze == nil {
		return
	}
	if r.isFrozenPresenceUser(userID) {
		return
	}
	freeze, found, err := r.deps.AccountFreeze.AccountFreeze(ctx, userID)
	if err != nil || !found || !freeze.Frozen {
		return
	}
	r.frozenPresence.Store(userID, struct{}{})
}

// applyFrozenPresence transitions the in-process presence mask and tracker on a
// freeze/unfreeze change. Freeze evicts the user's online presence entries and
// cancels the pending offline grace timer; unfreeze only re-enables presence.
// It never pushes: the instant admin hook separately broadcasts the one-time
// offline status, while the durable worker path only re-masks the in-process
// state so no later read/push can resurrect the online watermark.
func (r *Router) applyFrozenPresence(userID int64, frozen bool) {
	if r == nil || userID == 0 {
		return
	}
	if !frozen {
		r.frozenPresence.Delete(userID)
		return
	}
	r.frozenPresence.Store(userID, struct{}{})
	if r.presence != nil {
		r.presence.dropUser(userID)
	}
}

// announceFrozenEmptyStatus immediately broadcasts an empty status for the frozen
// account to its own sessions and relevant peers, so previously rendered online
// states are cleared rather than waiting for the next read.
func (r *Router) announceFrozenEmptyStatus(userID int64) {
	if r == nil || userID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.pushUserStatus(ctx, userID,
		domain.UserStatus{Kind: domain.UserStatusEmpty})
}

func (r *Router) setPresenceFromContext(ctx context.Context, userID int64, offline bool, persistMode presencePersistMode) (domain.UserStatus, bool) {
	// A frozen account never goes online: even if the client explicitly calls
	// updateStatus(online) or renews on reconnect, it is forced to offline, the
	// in-memory tracker does not re-register online, and the online state must not
	// be broadcast to peers.
	if !offline && r.isFrozenPresenceUser(userID) {
		offline = true
	}
	now := int(r.clock.Now().Unix())
	status := domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: now}
	if !offline {
		status = domain.UserStatus{
			Kind:      domain.UserStatusOnline,
			Expires:   now + int(userOnlineTTL/time.Second),
			WasOnline: now,
		}
		// Back online: cancel any pending offline-broadcast debounce timer.
		r.presence.cancelOfflineTimer(userID)
	}
	notify := true
	if key, ok := presenceSessionKeyFromContext(ctx); ok {
		notify = r.presence.setSessionStatus(key, userID, status)
	}
	// Online renewals (!offline) are debounced; an explicit offline is an
	// authoritative write and is always persisted. The frozen-account case is the
	// exception: a session renewal during the freeze must not refresh last_seen, or
	// users.last_seen_at would stay "just now" and any read that does not go through
	// the freeze projection would leak the recent-online state again.
	if r.isFrozenPresenceUser(userID) {
		// no-op: a frozen account does not record activity during the freeze
	} else if persistMode == presencePersistAsync {
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
	// Bots do not take part in presence: an official bot has no status, never
	// produces updateUserStatus and never writes last_seen. Bot login
	// (importBotAuthorization) goes through this path and must be short-circuited
	// entirely - otherwise users with a private chat with the bot would receive
	// bot online/offline broadcasts that do not exist in the protocol; the bot also
	// does not register a presence entry, so the sweeper naturally never encounters
	// a bot.
	if bot, known := r.userBotStatus(ctx, userID); !known || bot {
		return
	}
	r.seedFrozenPresence(ctx, userID)
	status, notify := r.setPresenceFromContext(ctx, userID, false, presencePersistAsync)
	if !notify {
		// This session is already online and within the refresh interval: skip both
		// "broadcast online to peers" and "pull peer online states to this session".
		// setPresenceFromContext already renews the online watermark on every RPC;
		// this drops the per-RPC fan-out + push during a burst of channel creation
		// (measured at ~70ms per RPC, the main residual tail latency/CPU cost after
		// the partition removal).
		return
	}
	r.pushSessionOnlineAsync(ctx, userID, status)
}

// userIsBot reports whether userID is a bot for business call sites that keep
// the historical bool contract. Presence uses userBotStatus directly so an
// unavailable classification cannot be guessed as a human account.
func (r *Router) userIsBot(ctx context.Context, userID int64) bool {
	bot, known := r.userBotStatus(ctx, userID)
	return known && bot
}

type botStatusResult struct {
	bot   bool
	known bool
}

func (r *Router) userBotStatus(ctx context.Context, userID int64) (bool, bool) {
	bot, known, err := r.lookupUserBotStatus(ctx, userID)
	if err != nil {
		return false, false
	}
	return bot, known
}

// lookupUserBotStatus is userBotStatus with the lookup error kept, for write
// paths where an unavailable classification must abort instead of being
// guessed as either a bot or a human account.
func (r *Router) lookupUserBotStatus(ctx context.Context, userID int64) (bool, bool, error) {
	if userID == 0 || r.deps.Users == nil {
		return false, false, nil
	}
	// The bot flag is immutable -> a permanent in-process cache, avoiding a
	// reprojecting Users.ByID on every announceSessionOnline RPC. Only the results
	// for users that exist are cached. A hit is purely in-memory.
	if v, ok := r.botStatus.Load(userID); ok {
		return v.(bool), true, nil
	}
	// During the cold-start surge, singleflight coalesces concurrent first lookups:
	// ~50 concurrent first frames hit PG only once (the rest share), avoiding a
	// Users.ByID reprojection herd (which once drove first-frame user_resolve up to
	// ~1.1s).
	v, err, _ := r.authUserSF.Do("bot:"+strconv.FormatInt(userID, 10), func() (any, error) {
		if cached, ok := r.botStatus.Load(userID); ok {
			return botStatusResult{bot: cached.(bool), known: true}, nil
		}
		if provider, ok := r.deps.Users.(BaseUserBotStatusProvider); ok {
			bot, found, err := provider.BotStatus(ctx, userID)
			if err != nil || !found {
				return botStatusResult{}, err
			}
			r.botStatus.Store(userID, bot)
			return botStatusResult{bot: bot, known: true}, nil
		}
		u, found, err := r.deps.Users.ByID(ctx, userID, userID)
		if err != nil || !found {
			return botStatusResult{}, err
		}
		r.botStatus.Store(userID, u.Bot)
		return botStatusResult{bot: u.Bot, known: true}, nil
	})
	if err != nil {
		return false, false, err
	}
	result := v.(botStatusResult)
	return result.bot, result.known, nil
}

func (r *Router) userPresenceStatus(userID int64) domain.UserStatus {
	return r.userPresenceStatusForUser(domain.User{ID: userID})
}

func (r *Router) userPresenceStatusForUser(u domain.User) domain.UserStatus {
	userID := u.ID
	if userID == 0 {
		return domain.UserStatus{Kind: domain.UserStatusRecently}
	}
	// A tombstone or frozen account never has presence. Without this guard a deleted
	// or frozen peer that reaches presence before the projection collapsed its status
	// would fall through to the tracker/last-seen branches and render "last seen recently"
	// right next to its own deleted presentation.
	if u.Deleted || r.isFrozenPresenceUser(userID) {
		return domain.UserStatus{Kind: domain.UserStatusEmpty}
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

// persistLastSeen persists last_seen. With debounce=true (high-frequency online
// renewals) it writes to the DB only once every few seconds; debounce=false is an
// authoritative write (explicit offline / disconnect) and is never skipped.
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
	if r.lastSeenBatch != nil {
		if err := r.lastSeenBatch.submit(store.UserLastSeenUpdate{UserID: userID, LastSeenAt: lastSeenAt}); err == nil {
			return
		} else {
			// Capacity/stopping is never a silent drop. The direct authoritative
			// write is an overload safety valve, not a configurable legacy mode.
			r.log.Error("presence last-seen batch admission failed; using authoritative direct write",
				zap.Int64("user_id", userID), zap.Error(err))
		}
	}
	r.persistReservedLastSeenAsync(ctx, updater, userID, lastSeenAt)
}

func (r *Router) persistReservedLastSeenAsync(
	ctx context.Context,
	updater userLastSeenUpdater,
	userID int64,
	lastSeenAt int,
) {
	bgCtx, cancel := r.presenceBackgroundContext(ctx, 10*time.Second)
	// Keep the direct overflow write visible after its parent callback returns.
	// The production batch normally handles this work; admission errors retain
	// their existing explicit log and authoritative write behavior.
	r.presence.changeDirectLastSeenRunning(1)
	go func() {
		defer cancel()
		defer r.presence.changeDirectLastSeenRunning(-1)
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

// RunPresenceLastSeenBatch owns the lifecycle batch worker. The worker stops
// accepting on cancellation and performs a bounded drain before returning.
func (r *Router) RunPresenceLastSeenBatch(ctx context.Context) {
	if r == nil || r.lastSeenBatch == nil {
		return
	}
	r.lastSeenBatch.Run(ctx)
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
// The synchronous path only does in-memory cleanup (presence/clientInfo entries);
// persistence and the offline broadcast run in the background with a timeout after
// the debounce grace - the connection close happens on the serveConn exit path, and
// doing the DB write and per-peer queries synchronously would amplify DB pressure
// during a disconnect storm and stall the goroutine's exit.
func (r *Router) SessionOffline(rawAuthKeyID [8]byte, sessionID, userID int64, lastForUser bool) {
	r.SessionOfflineAt(rawAuthKeyID, sessionID, userID, lastForUser, int(r.clock.Now().Unix()))
}

// SessionOfflineAt preserves the observed disconnect time and is idempotent
// for repeated physical-session departure reports.
func (r *Router) SessionOfflineAt(rawAuthKeyID [8]byte, sessionID, userID int64, lastForUser bool, disconnectedAt int) {
	r.forgetClientSessionInfo(rawAuthKeyID, sessionID)
	if userID == 0 {
		return
	}
	key := presenceSessionKey{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID}
	r.presence.clearSession(key)
	if !lastForUser {
		return
	}
	if disconnectedAt <= 0 {
		disconnectedAt = int(r.clock.Now().Unix())
	}
	// Use a trackable timer that can be cancelled when going back online (see
	// cancelOfflineTimer), avoiding a pile-up during a disconnect storm.
	r.presence.armOfflineTimer(userID, offlineAnnounceGrace, func() {
		r.announceUserOfflineIfStillGone(rawAuthKeyID, sessionID, userID, disconnectedAt)
	})
}

// announceUserOfflineIfStillGone persists last_seen and broadcasts offline after
// the debounce grace expires; if the user comes back online within the grace period
// it is skipped entirely. WasOnline takes the disconnect instant, so the data is
// unaffected by the debounce delay.
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
	// Bots do not broadcast offline and do not write last_seen (symmetric with
	// announceSessionOnline).
	if bot, known := r.userBotStatus(ctx, userID); !known || bot {
		return
	}
	// Account deletion revokes every session at once, so this disconnect fires
	// right behind it. Announcing "last seen <now>" here would immediately undo
	// the empty presence the tombstone just installed -- the account would pop
	// back as recently offline seconds after appearing deleted.
	if r.isDeletedAccount(userID) || r.isFrozenPresenceUser(userID) {
		return
	}
	status := domain.UserStatus{Kind: domain.UserStatusOffline, WasOnline: disconnectedAt}
	// The disconnect downgrade is an authoritative last_seen and is not debounced;
	// lifecycle writes are coalesced through a bounded batch, and WasOnline still
	// takes the real disconnect instant rather than the flush instant.
	r.persistLastSeenAsync(ctx, userID, disconnectedAt, false)
	r.pushUserStatus(ctx, userID, status)
}

// RunPresenceSweeper periodically downgrades expired online presence to offline and
// broadcasts it to online watchers. A client normally renews via updateStatus every
// few minutes; when a device stops renewing (suspended in the background), its
// friends should actively receive offline once it expires rather than seeing the
// status change only on the next query.
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
			continue // the user has another unexpired online session
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
	// Bots do not take part in presence: an official bot user carries no status (the
	// client status bar always shows "bot"), and the tg conversion layer does not
	// output Status for a bot either.
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
	// Dialog/application projections already contain the ordinary peer envelope.
	// Only resolve nested message references that are still absent (forward author,
	// via bot, poll voter, linked giveaway channel, and similar fields).
	for _, user := range list.Users {
		delete(userIDs, user.ID)
	}
	for _, channel := range list.Channels {
		delete(channelIDs, channel.ID)
	}
	for _, community := range list.Communities {
		delete(channelIDs, community.Community.ID)
		for _, user := range community.Users {
			delete(userIDs, user.ID)
		}
		for _, channel := range community.Channels {
			delete(channelIDs, channel.ID)
		}
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

// tgUser / tgSelfUser deliberately do NOT overlay the username registry or the
// bot verification icon: they are called inside per-id loops (users.getUsers), and a
// per-object read there would be exactly the N+1 the batch overlay exists to avoid.
// Single-object handlers pick both up from applyPeerReadModels at the response
// boundary.
func (r *Router) tgUser(u domain.User) *tg.User {
	return r.withBotProfileFlags(context.Background(), tgUser(r.withUserPresence(u)))
}

func (r *Router) tgSelfUser(u domain.User) *tg.User {
	return r.withBotProfileFlags(context.Background(), tgSelfUser(r.withUserPresence(u)))
}

func (r *Router) tgUsers(users []domain.User) []tg.UserClass {
	out := tgUsers(r.withUsersPresence(users))
	r.withBotProfileFlagsForUsers(context.Background(), out)
	r.applyPeerIdentitiesToPeerObjects(context.Background(), out, nil)
	return out
}

// tgUsersForViewer backfills presence like tgUsers, but the viewer's own user takes
// the self branch (DrKLO decides Saved Messages from user.self; self=false would
// pollute the account cache).
func (r *Router) tgUsersForViewer(viewerUserID int64, users []domain.User) []tg.UserClass {
	out := tgUsersForViewer(viewerUserID, r.withUsersPresence(users))
	r.withBotProfileFlagsForUsers(context.Background(), out)
	r.applyPeerIdentitiesToPeerObjects(context.Background(), out, nil)
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
	if status.Kind != domain.UserStatusEmpty && r.isFrozenPresenceUser(userID) {
		status = domain.UserStatus{Kind: domain.UserStatusEmpty}
	}
	update := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUserStatus{
			UserID: userID,
			Status: tgUserStatus(status),
		}},
		Date: int(r.clock.Now().Unix()),
		Seq:  0,
	}
	// presence is transient (does not write to the durable log): a session that is
	// not ready is skipped outright and does not enter pending.
	r.pushUserMessageTransient(ctx, userID, "push user status (own sessions)", update)
	// The recipient set = peers that have a contact/private-chat relationship with
	// this user and are currently online. It is computed in one shot from "this
	// user's own contacts + dialog peers ∩ online" (onlineRelevantPeerIDs, 2 queries
	// + in-memory filtering), rather than iterating over every online candidate and
	// calling GetPeerDialogs one by one (the old onlinePrivateDialogPeerIDs's
	// O(online count) N+1, or O(M×512) serial PG during a disconnect/sweeper storm).
	// A private-chat dialog is written as a row in both directions, so the two
	// algorithms yield the same set.
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
	if r.isDeletedAccount(ownerUserID) || r.isFrozenPresenceUser(ownerUserID) {
		return
	}
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

// presenceCandidateCacheTTL is the cache lifetime of the presence fan-out candidate
// set (contacts ∪ private-chat peers); lastSeenPersistDebounce is the debounce
// window for writing last_seen on online renewals.
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
	// The online filter runs live every time (it does not enter the cache), so the
	// cache only affects the "candidate scope", not the freshness of the online
	// decision.
	return provider.OnlineUserIDsForCandidates(candidates, 0)
}

// presenceFanoutCandidates returns the candidate peer set for presence fan-out
// (contacts ∪ private-chat peers, before the online filter), with a short TTL
// cache. A hit means zero DB queries; a miss goes through the original hydration
// path to compute it once and cache it. The candidate set changes slowly (adding a
// friend / starting a new private chat), and staleness of at most TTL seconds is
// acceptable for presence (best-effort, transient). The cache is written only when
// both queries returned without error, so a partial set caused by a transient error
// is not pinned for the TTL.
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
		provider, ok := r.deps.Dialogs.(interface {
			PrivateDialogPeerIDs(context.Context, int64, int) ([]int64, error)
		})
		if !ok {
			failed = true
		} else if ids, err := provider.PrivateDialogPeerIDs(ctx, userID, presenceDialogFanoutCandidateLimit); err != nil {
			failed = true
		} else {
			for _, id := range ids {
				add(id)
			}
		}
	}
	if !failed {
		r.presenceCandidateCache.Store(userID, &presenceCandidateEntry{ids: candidates, expireAt: now.Add(presenceCandidateCacheTTL)})
	}
	return candidates
}
