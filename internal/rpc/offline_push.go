package rpc

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/push"
	"telesrv/internal/store"
)

// OfflinePusher delivers a push notification for a durable update that reached no
// live session. It is called from an outbox worker, so an implementation must
// return quickly and do any network work asynchronously.
type OfflinePusher interface {
	PushOfflineUpdate(ctx context.Context, userID int64, event domain.UpdateEvent, update *tg.Updates)
}

const (
	offlinePushWorkers         = 4
	offlinePushQueueSize       = 256
	defaultOfflinePushDebounce = 30 * time.Second
)

// OfflinePushBridge builds notifications from the already-enriched viewer
// updates, drops muted chats (sending a throttled MESSAGE_MUTED badge refresh
// instead), coalesces bursts per user, and sends from a small worker pool so a
// provider round-trip never blocks the outbox.
type OfflinePushBridge struct {
	tokens   store.PushTokenStore
	notify   store.NotifySettingsStore
	service  *push.Service
	clock    clock.Clock
	debounce time.Duration
	log      *zap.Logger

	jobs chan offlinePushJob
	mu   sync.Mutex
	last map[int64]time.Time
}

type offlinePushJob struct {
	userID int64
	notif  push.Notification
	muted  bool
}

// NewOfflinePushBridge builds the outbox push bridge. A nil clock/log falls back
// to the system clock / no-op logger; a non-positive debounce uses the default.
func NewOfflinePushBridge(tokens store.PushTokenStore, service *push.Service, debounce time.Duration, clk clock.Clock, log *zap.Logger) *OfflinePushBridge {
	if clk == nil {
		clk = clock.System
	}
	if log == nil {
		log = zap.NewNop()
	}
	if debounce <= 0 {
		debounce = defaultOfflinePushDebounce
	}
	return &OfflinePushBridge{
		tokens:   tokens,
		service:  service,
		clock:    clk,
		debounce: debounce,
		log:      log,
		jobs:     make(chan offlinePushJob, offlinePushQueueSize),
		last:     make(map[int64]time.Time),
	}
}

// WithNotifyStore enables mute awareness: a muted peer yields a throttled
// MESSAGE_MUTED badge refresh instead of a visible notification.
func (p *OfflinePushBridge) WithNotifyStore(notify store.NotifySettingsStore) *OfflinePushBridge {
	p.notify = notify
	return p
}

// PushOfflineUpdate decides whether an outbox event warrants a push, builds it,
// applies the per-user debounce and mute check, and enqueues it. It never blocks
// for the provider, but reads notify settings synchronously (bounded to one read
// per user per debounce window).
func (p *OfflinePushBridge) PushOfflineUpdate(ctx context.Context, userID int64, event domain.UpdateEvent, update *tg.Updates) {
	if p == nil || p.tokens == nil || p.service == nil || update == nil {
		return
	}
	if event.Type != domain.UpdateEventNewMessage {
		return
	}
	if !p.allow(userID) {
		return
	}
	target, ok := buildPushTarget(userID, update)
	if !ok {
		return
	}
	muted := p.isMuted(ctx, userID, target.peer, target.category)
	if muted {
		target.notif = mutedNotification(userID, target.notif)
	}
	select {
	case p.jobs <- offlinePushJob{userID: userID, notif: target.notif, muted: muted}:
	default:
		p.log.Debug("offline push queue full; dropping", zap.Int64("user_id", userID))
	}
}

// allow is the per-user coalescing window (one push per user per window).
func (p *OfflinePushBridge) allow(userID int64) bool {
	now := p.clock.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.last[userID]; ok && now.Sub(last) < p.debounce {
		return false
	}
	p.last[userID] = now
	return true
}

// isMuted reports whether the peer (or its category default) is muted now.
func (p *OfflinePushBridge) isMuted(ctx context.Context, userID int64, peer domain.Peer, category domain.NotifyScopeKind) bool {
	if p.notify == nil {
		return false
	}
	now := int(p.clock.Now().Unix())
	if peer.ID != 0 && peer.Type != "" {
		if settings, found, err := p.notify.GetNotifySettings(ctx, userID, domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer}); err == nil && found && mutedNow(settings, now) {
			return true
		}
	}
	if category != "" {
		if settings, found, err := p.notify.GetNotifySettings(ctx, userID, domain.NotifyScope{Kind: category}); err == nil && found && mutedNow(settings, now) {
			return true
		}
	}
	return false
}

func mutedNow(settings domain.PeerNotifySettings, now int) bool {
	return settings.MuteUntil != nil && *settings.MuteUntil > now
}

// mutedNotification replaces a visible notification with a MESSAGE_MUTED badge
// refresh (the clients fetch updates but show nothing).
func mutedNotification(userID int64, base push.Notification) push.Notification {
	return push.Notification{
		LocKey: "MESSAGE_MUTED",
		Custom: base.Custom,
		UserID: userID,
		Muted:  true,
	}
}

// Run starts the send workers and blocks until ctx is cancelled.
func (p *OfflinePushBridge) Run(ctx context.Context) {
	if p == nil || p.tokens == nil || p.service == nil {
		return
	}
	var wg sync.WaitGroup
	for i := 0; i < offlinePushWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runWorker(ctx)
		}()
	}
	wg.Wait()
}

func (p *OfflinePushBridge) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-p.jobs:
			p.deliver(ctx, job)
		}
	}
}

func (p *OfflinePushBridge) deliver(ctx context.Context, job offlinePushJob) {
	tokens, err := p.tokens.ListPushTokensForUser(ctx, job.userID)
	if err != nil {
		p.log.Warn("list push tokens", zap.Int64("user_id", job.userID), zap.Error(err))
		return
	}
	for _, token := range tokens {
		// A device that opted out of muted/silent pushes must not receive the
		// MESSAGE_MUTED badge refresh either.
		if job.muted && token.NoMuted {
			continue
		}
		if err := p.service.Send(ctx, token, job.notif); err != nil {
			if errors.Is(err, push.ErrTokenUnregistered) {
				p.log.Info("push token invalidated by provider",
					zap.Int64("user_id", job.userID), zap.Int("token_type", token.TokenType))
				if _, delErr := p.tokens.DeletePushTokenByValue(ctx, token.TokenType, token.Token); delErr != nil {
					p.log.Warn("delete invalidated push token", zap.Error(delErr))
				}
				continue
			}
			p.log.Debug("offline push send failed",
				zap.Int64("user_id", job.userID), zap.Int("token_type", token.TokenType), zap.Error(err))
			continue
		}
		p.log.Info("offline push sent",
			zap.Int64("user_id", job.userID),
			zap.Int("token_type", token.TokenType),
			zap.String("loc_key", job.notif.LocKey),
			zap.Bool("muted", job.muted),
		)
	}
}
