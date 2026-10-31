package maintenance

import (
	"context"
	"time"

	"go.uber.org/zap"
)

// DispatchOutboxRetentionStore cleans up outbox dead tasks that failed permanently (retries abandoned).
type DispatchOutboxRetentionStore interface {
	DeleteFailed(ctx context.Context, olderThan time.Duration, limit int) (int, error)
}

// TempAuthKeyRetentionStore reclaims expired PFS temp auth keys (including unbound keys).
type TempAuthKeyRetentionStore interface {
	DeleteExpired(ctx context.Context, expiredBefore int64, limit int) (int, error)
}

// AuthKeySessionLayerRetentionStore reclaims expired short-lived Layer
// watermarks. Selector freshness, not retention timing, is the correctness
// gate; this worker only bounds durable storage.
type AuthKeySessionLayerRetentionStore interface {
	DeleteExpiredSessionLayers(ctx context.Context, limit int) (int, error)
}

// OrphanAuthKeyRetentionStore reclaims old handshake keys that never formed an
// authorization/temp binding. protected is a snapshot of the raw auth_key_ids actually in use
// by the current connection registry.
type OrphanAuthKeyRetentionStore interface {
	DeleteOrphaned(ctx context.Context, olderThan time.Duration, limit int, protected [][8]byte) (int, error)
}

type ActiveRawAuthKeyProvider interface {
	ActiveRawAuthKeyIDs() [][8]byte
}

// ActiveAuthKeyHeartbeatStore persists the liveness of raw auth keys still in use by this
// instance. With multiple instances, orphan GC cannot rely only on the current process's
// active snapshot; other instances' heartbeats advance last_used_at in the database so they
// are not misjudged as orphans.
type ActiveAuthKeyHeartbeatStore interface {
	TouchActiveRawAuthKeys(ctx context.Context, ids [][8]byte) error
}

// BotAPIUpdateRetentionStore reclaims dead rows of the Bot API getUpdates delivery queue
// (performance audit H1): rows that are confirmed and past the grace period + rows past
// retention by message date (official Bot API updates are kept at most 24h).
type BotAPIUpdateRetentionStore interface {
	DeleteDeliveredOrExpired(ctx context.Context, confirmedGrace, maxAge time.Duration, limit int) (int, error)
}

// UserUpdateEventRetentionStore reclaims only the prefix of account events explicitly
// confirmed by all current authorizations. It is not an ordinary TTL: when any authorization
// lacks state the confirmation watermark is 0, and events that device may still need must not
// be deleted.
type UserUpdateEventRetentionStore interface {
	DeleteConfirmedPrefix(ctx context.Context, olderThan time.Duration, limit int) (int, error)
}

// ChannelUpdateEventRetentionStore reclaims the consecutive prefix of channel durable
// updates past retention. The concrete store must delete events and advance the retained
// floor in the same transaction; clients below the floor recover via updates.getChannelDifference
// using the channelDifferenceTooLong snapshot.
type ChannelUpdateEventRetentionStore interface {
	DeleteExpiredChannelUpdateEvents(ctx context.Context, olderThan time.Duration, limit int) (int, error)
}

// LoginCodeDeliveryRetentionStore reclaims only compact idempotency receipts
// after their associated opaque code lifetime. It must not delete the message,
// durable update event, or outbox facts created by the delivery transaction.
type LoginCodeDeliveryRetentionStore interface {
	DeleteExpiredLoginCodeDeliveries(ctx context.Context, expiredBefore time.Time, limit int) (int, error)
}

type ClientTelemetryRetentionStore interface {
	DeleteExpiredClientTelemetry(ctx context.Context, olderThan time.Time, limit int) (int, error)
}

type AuthDeliveryReportRetentionStore interface {
	DeleteExpiredAuthDeliveryReports(ctx context.Context, olderThan time.Time, limit int) (int, error)
}

type ModerationRetentionStore interface {
	DeleteExpiredSponsoredMessageImpressions(ctx context.Context, olderThan time.Time, limit int) (int, error)
	DeleteExpiredModerationAppealLinks(ctx context.Context, olderThan time.Time, limit int) (int, error)
}

// botAPIConfirmedGrace is the deletion grace for confirmed Bot API update rows: rows below
// the confirmation watermark are no longer read by getUpdates (fromID is always > confirmed);
// the grace only defends against offset rollback debugging, and the reclamation goal is to
// clear the backlog.
const botAPIConfirmedGrace = 15 * time.Minute

// tempAuthKeyExpiryGrace is only a physical database reclamation grace. The MTProto edge
// stops inbound RPC, active pushes, and retransmissions and disconnects at expires_at;
// ResolveAuthKey does not tolerate expired keys. Deleting a day late absorbs client
// rotation/diagnostics windows and does not extend the protocol validity.
const tempAuthKeyExpiryGrace = 24 * time.Hour

const (
	// A terminal failed outbox only provides short-term diagnostic isolation; it is not a
	// durable update log. Deleting the task immediately releases the same account's next pts
	// through the head trigger, while user_update_events are retained and any missed online
	// pushes are compensated by the normal difference path.
	defaultOutboxPoisonRetention = time.Minute
	defaultOutboxPoisonInterval  = 15 * time.Second
)

// RetentionWorker periodically reclaims dead data from storage.
//
// Note: TDesktop does not support account-level updates.differenceTooLong (api_updates.cpp
// only logs the response and does not clear requesting, permanently locking up the update
// engine), so user_update_events must never be hard-trimmed by ordinary TTL. This worker only
// allows the store to delete the consecutive safe prefix that "all current authorized devices
// have explicitly confirmed"; any lagging device or one lacking state pushes the floor back
// to 0. When a client happens to carry back an old pts from before confirmation, the updates
// service advances via the normal differenceSlice checkpoint and does not send
// differenceTooLong.
type RetentionWorker struct {
	outbox                      DispatchOutboxRetentionStore
	tempKeys                    TempAuthKeyRetentionStore // may be nil (does not reclaim temp key bindings)
	authKeySessionLayers        AuthKeySessionLayerRetentionStore
	botAPIUpdates               BotAPIUpdateRetentionStore // may be nil (does not reclaim the Bot API queue)
	userUpdates                 UserUpdateEventRetentionStore
	channelUpdates              ChannelUpdateEventRetentionStore
	loginCodeDeliveries         LoginCodeDeliveryRetentionStore
	clientTelemetry             ClientTelemetryRetentionStore
	authDeliveryReports         AuthDeliveryReportRetentionStore
	moderation                  ModerationRetentionStore
	orphanAuthKeys              OrphanAuthKeyRetentionStore
	activeAuthKeys              ActiveRawAuthKeyProvider
	activeAuthKeyHeartbeat      ActiveAuthKeyHeartbeatStore
	logger                      *zap.Logger
	retention                   time.Duration
	botAPIRetention             time.Duration
	orphanRetention             time.Duration
	clientTelemetryRetention    time.Duration
	authDeliveryReportRetention time.Duration
	outboxPoisonRetention       time.Duration
	outboxPoisonInterval        time.Duration
	interval                    time.Duration
	batch                       int
}

func NewRetentionWorker(outbox DispatchOutboxRetentionStore, tempKeys TempAuthKeyRetentionStore, logger *zap.Logger, retention, interval time.Duration, batch int) *RetentionWorker {
	if logger == nil {
		logger = zap.NewNop()
	}
	if retention <= 0 {
		retention = 168 * time.Hour
	}
	if interval <= 0 {
		interval = time.Hour
	}
	if batch <= 0 {
		batch = 10000
	}
	return &RetentionWorker{
		outbox:                outbox,
		tempKeys:              tempKeys,
		logger:                logger,
		retention:             retention,
		outboxPoisonRetention: defaultOutboxPoisonRetention,
		outboxPoisonInterval:  defaultOutboxPoisonInterval,
		interval:              interval,
		batch:                 batch,
	}
}

// WithDispatchOutboxPoisonPolicy configures an independent short isolation and cleanup
// period for a terminal failed head. This period cannot reuse the durable update's weekly
// retention, otherwise one deterministic construction error would freeze that user's entire
// online pts lane. Values <=0 fall back to the safe defaults of 1m/15s respectively.
func (w *RetentionWorker) WithDispatchOutboxPoisonPolicy(retention, interval time.Duration) *RetentionWorker {
	if retention <= 0 {
		retention = defaultOutboxPoisonRetention
	}
	if interval <= 0 {
		interval = defaultOutboxPoisonInterval
	}
	w.outboxPoisonRetention = retention
	w.outboxPoisonInterval = interval
	return w
}

// WithBotAPIUpdateRetention enables bot_api_updates queue reclamation; when retention <=0 the official-semantics default of 24h is used.
func (w *RetentionWorker) WithBotAPIUpdateRetention(store BotAPIUpdateRetentionStore, retention time.Duration) *RetentionWorker {
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	w.botAPIUpdates = store
	w.botAPIRetention = retention
	return w
}

// WithUserUpdateRetention enables common-confirmation safe-prefix reclamation for account
// updates. TDesktop does not support account differenceTooLong, so the concrete store must
// guarantee that the unconfirmed prefix is never deleted.
func (w *RetentionWorker) WithUserUpdateRetention(store UserUpdateEventRetentionStore) *RetentionWorker {
	w.userUpdates = store
	return w
}

// WithChannelUpdateRetention enables bounded TTL reclamation of channel durable updates;
// it reuses the worker's retention/interval/batch, and the store's retained floor guarantees
// that old pts do not read a silent hole.
func (w *RetentionWorker) WithChannelUpdateRetention(store ChannelUpdateEventRetentionStore) *RetentionWorker {
	w.channelUpdates = store
	return w
}

// WithLoginCodeDeliveryRetention enables bounded seek cleanup for compact
// phone_code_hash receipts. Each row carries its own expiry derived from the
// code TTL, so this cleanup intentionally does not reuse update-log retention.
func (w *RetentionWorker) WithLoginCodeDeliveryRetention(store LoginCodeDeliveryRetentionStore) *RetentionWorker {
	w.loginCodeDeliveries = store
	return w
}

// WithAuthKeySessionLayerRetention enables bounded seek cleanup for expired
// per-session Layer evidence.
func (w *RetentionWorker) WithAuthKeySessionLayerRetention(store AuthKeySessionLayerRetentionStore) *RetentionWorker {
	w.authKeySessionLayers = store
	return w
}

func (w *RetentionWorker) WithClientTelemetryRetention(store ClientTelemetryRetentionStore, retention time.Duration) *RetentionWorker {
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	w.clientTelemetry = store
	w.clientTelemetryRetention = retention
	return w
}

func (w *RetentionWorker) WithAuthDeliveryReportRetention(store AuthDeliveryReportRetentionStore, retention time.Duration) *RetentionWorker {
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	w.authDeliveryReports = store
	w.authDeliveryReportRetention = retention
	return w
}

func (w *RetentionWorker) WithModerationRetention(store ModerationRetentionStore) *RetentionWorker {
	w.moderation = store
	return w
}

// WithOrphanAuthKeyRetention enables bounded reclamation of unauthorized handshake keys.
// active must provide raw keys, not temp→perm business keys; otherwise unauthenticated or
// PFS connections would be misjudged as orphans.
func (w *RetentionWorker) WithOrphanAuthKeyRetention(store OrphanAuthKeyRetentionStore, active ActiveRawAuthKeyProvider, retention time.Duration) *RetentionWorker {
	w.orphanAuthKeys = store
	w.activeAuthKeys = active
	w.activeAuthKeyHeartbeat, _ = store.(ActiveAuthKeyHeartbeatStore)
	w.orphanRetention = retention
	return w
}

func (w *RetentionWorker) Run(ctx context.Context) {
	w.runOnce(ctx)
	retentionTicker := time.NewTicker(w.interval)
	defer retentionTicker.Stop()
	poisonTicker := time.NewTicker(w.outboxPoisonInterval)
	defer poisonTicker.Stop()
	var (
		heartbeatTicker *time.Ticker
		heartbeatC      <-chan time.Time
	)
	if interval := w.orphanHeartbeatInterval(); interval > 0 {
		heartbeatTicker = time.NewTicker(interval)
		heartbeatC = heartbeatTicker.C
		defer heartbeatTicker.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-retentionTicker.C:
			w.runRetentionOnce(ctx)
		case <-poisonTicker.C:
			w.runOutboxPoisonOnce(ctx)
		case <-heartbeatC:
			w.heartbeatActiveAuthKeys(ctx)
		}
	}
}

func (w *RetentionWorker) runOnce(ctx context.Context) {
	w.runOutboxPoisonOnce(ctx)
	w.runRetentionOnce(ctx)
}

func (w *RetentionWorker) runOutboxPoisonOnce(ctx context.Context) {
	if w.outbox == nil {
		return
	}
	outboxDeleted, err := w.outbox.DeleteFailed(ctx, w.outboxPoisonRetention, w.batch)
	if err != nil {
		w.logger.Error("failed to clean up terminal failed dispatch_outbox",
			zap.String("signal", "dispatch_outbox_poison_cleanup_failed"),
			zap.Duration("quarantine", w.outboxPoisonRetention),
			zap.Error(err),
		)
	} else if outboxDeleted > 0 {
		// The Error-level structured signal is deliberately kept: a terminal failure means a
		// deterministic encoding, missing event, or other non-retryable fault. Deleting the task
		// only unfreezes the online lane and does not delete the durable event.
		w.logger.Error("terminal failed dispatch_outbox isolation ended and user lane released",
			zap.String("signal", "dispatch_outbox_poison_released"),
			zap.Int("deleted", outboxDeleted),
			zap.Duration("quarantine", w.outboxPoisonRetention),
		)
	}
}

func (w *RetentionWorker) runRetentionOnce(ctx context.Context) {
	if w.authKeySessionLayers != nil {
		deleted, err := w.authKeySessionLayers.DeleteExpiredSessionLayers(ctx, w.batch)
		if err != nil {
			w.logger.Warn("failed to reclaim expired auth-key session Layer evidence", zap.Error(err))
		} else if deleted > 0 {
			w.logger.Info("reclaimed expired auth-key session Layer evidence", zap.Int("deleted", deleted))
		}
	}
	if w.loginCodeDeliveries != nil {
		deleted, err := w.loginCodeDeliveries.DeleteExpiredLoginCodeDeliveries(ctx, time.Now(), w.batch)
		if err != nil {
			w.logger.Warn("failed to reclaim expired login-code delivery receipts", zap.Error(err))
		} else if deleted > 0 {
			w.logger.Info("reclaimed expired login-code delivery receipts", zap.Int("deleted", deleted))
		}
	}
	if w.clientTelemetry != nil {
		deleted, err := w.clientTelemetry.DeleteExpiredClientTelemetry(
			ctx, time.Now().Add(-w.clientTelemetryRetention), w.batch,
		)
		if err != nil {
			w.logger.Warn("failed to reclaim expired client telemetry", zap.Error(err))
		} else if deleted > 0 {
			w.logger.Info("reclaimed expired client telemetry", zap.Int("deleted", deleted))
		}
	}
	if w.authDeliveryReports != nil {
		deleted, err := w.authDeliveryReports.DeleteExpiredAuthDeliveryReports(
			ctx, time.Now().Add(-w.authDeliveryReportRetention), w.batch,
		)
		if err != nil {
			w.logger.Warn("failed to reclaim expired verification-code delivery diagnostics", zap.Error(err))
		} else if deleted > 0 {
			w.logger.Info("reclaimed expired verification-code delivery diagnostics", zap.Int("deleted", deleted))
		}
	}
	if w.moderation != nil {
		now := time.Now()
		impressions, err := w.moderation.DeleteExpiredSponsoredMessageImpressions(
			ctx, now, w.batch,
		)
		if err != nil {
			w.logger.Warn("failed to reclaim expired sponsored impressions", zap.Error(err))
		} else if impressions > 0 {
			w.logger.Info("reclaimed expired sponsored impressions", zap.Int("deleted", impressions))
		}
		links, err := w.moderation.DeleteExpiredModerationAppealLinks(
			ctx, now, w.batch,
		)
		if err != nil {
			w.logger.Warn("failed to reclaim expired moderation appeal links", zap.Error(err))
		} else if links > 0 {
			w.logger.Info("reclaimed expired moderation appeal links", zap.Int("deleted", links))
		}
	}
	if w.tempKeys != nil {
		expiredBefore := time.Now().Add(-tempAuthKeyExpiryGrace).Unix()
		tempDeleted, err := w.tempKeys.DeleteExpired(ctx, expiredBefore, w.batch)
		if err != nil {
			w.logger.Warn("failed to reclaim expired temp auth key bindings", zap.Error(err))
		} else if tempDeleted > 0 {
			w.logger.Info("reclaimed expired temp auth key bindings", zap.Int("deleted", tempDeleted))
		}
	}
	if w.orphanAuthKeys != nil && w.orphanRetention > 0 {
		var protected [][8]byte
		if w.activeAuthKeys != nil {
			protected = w.activeAuthKeys.ActiveRawAuthKeyIDs()
		}
		if !w.touchActiveAuthKeys(ctx, protected) {
			// Fail safe: if this instance cannot publish its own active set, deleting against a
			// stale database heartbeat could evict keys used by another instance too. Keep all
			// candidates for this pass and retry after the next heartbeat.
		} else {
			orphanDeleted, err := w.orphanAuthKeys.DeleteOrphaned(ctx, w.orphanRetention, w.batch, protected)
			if err != nil {
				w.logger.Warn("failed to reclaim unauthorized orphan auth keys", zap.Error(err))
			} else if orphanDeleted > 0 {
				w.logger.Info("reclaimed unauthorized orphan auth keys", zap.Int("deleted", orphanDeleted))
			}
		}
	}
	if w.botAPIUpdates != nil {
		botAPIDeleted, err := w.botAPIUpdates.DeleteDeliveredOrExpired(ctx, botAPIConfirmedGrace, w.botAPIRetention, w.batch)
		if err != nil {
			w.logger.Warn("failed to reclaim bot_api_updates queue", zap.Error(err))
		} else if botAPIDeleted > 0 {
			w.logger.Info("reclaimed bot_api_updates queue", zap.Int("deleted", botAPIDeleted))
		}
	}
	if w.userUpdates != nil {
		userDeleted, err := w.userUpdates.DeleteConfirmedPrefix(ctx, w.retention, w.batch)
		if err != nil {
			w.logger.Warn("failed to reclaim commonly confirmed user_update_events prefix", zap.Error(err))
		} else if userDeleted > 0 {
			w.logger.Info("reclaimed commonly confirmed user_update_events prefix", zap.Int("deleted", userDeleted))
		}
	}
	if w.channelUpdates != nil {
		channelDeleted, err := w.channelUpdates.DeleteExpiredChannelUpdateEvents(ctx, w.retention, w.batch)
		if err != nil {
			// The store isolates bad gaps per channel and continues this round; deleted may be
			// non-zero and must be logged at the same time — neither disguising a global pass as a
			// total failure nor swallowing invariant errors.
			w.logger.Warn("expired channel_update_events reclamation has isolated channels",
				zap.Int("deleted", channelDeleted),
				zap.Error(err),
			)
		} else if channelDeleted > 0 {
			w.logger.Info("reclaimed expired channel_update_events consecutive prefix", zap.Int("deleted", channelDeleted))
		}
	}
}

func (w *RetentionWorker) orphanHeartbeatInterval() time.Duration {
	if w.activeAuthKeyHeartbeat == nil || w.activeAuthKeys == nil || w.orphanRetention <= 0 {
		return 0
	}
	interval := w.orphanRetention / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	if w.interval > 0 && w.interval < interval {
		interval = w.interval
	}
	return interval
}

func (w *RetentionWorker) heartbeatActiveAuthKeys(ctx context.Context) {
	if w.activeAuthKeys == nil {
		return
	}
	w.touchActiveAuthKeys(ctx, w.activeAuthKeys.ActiveRawAuthKeyIDs())
}

// touchActiveAuthKeys returns false only when a configured durable heartbeat failed. A store that
// predates the optional heartbeat interface keeps single-instance behavior.
func (w *RetentionWorker) touchActiveAuthKeys(ctx context.Context, protected [][8]byte) bool {
	if w.activeAuthKeyHeartbeat == nil {
		return true
	}
	if err := w.activeAuthKeyHeartbeat.TouchActiveRawAuthKeys(ctx, protected); err != nil {
		w.logger.Error("failed to refresh active raw auth key heartbeat, skipping orphan GC this round",
			zap.String("signal", "auth_key_heartbeat_failed"),
			zap.Int("active_keys", len(protected)),
			zap.Error(err),
		)
		return false
	}
	return true
}
