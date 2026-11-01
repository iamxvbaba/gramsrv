package maintenance

import (
	"context"
	"time"

	"go.uber.org/zap"
)

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

// ActiveRawAuthKeySnapshotProvider exposes the distributed active raw-key
// snapshot. It can fail; orphan auth-key GC must stop for that pass rather than
// delete against an incomplete view.
type ActiveRawAuthKeySnapshotProvider interface {
	ActiveRawAuthKeySnapshot(ctx context.Context) ([][8]byte, error)
}

// ActiveAuthKeyHeartbeatStore persists the liveness of raw auth keys still in use. With several
// instances, orphan GC cannot look only at one process's active snapshot; the distributed snapshot
// protects candidates first, then the heartbeat advances last_used_at in the database, leaving
// durable evidence for later reclaim passes.
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
	activeAuthKeySnapshot       ActiveRawAuthKeySnapshotProvider
	activeAuthKeyHeartbeat      ActiveAuthKeyHeartbeatStore
	logger                      *zap.Logger
	retention                   time.Duration
	botAPIRetention             time.Duration
	orphanRetention             time.Duration
	clientTelemetryRetention    time.Duration
	authDeliveryReportRetention time.Duration
	interval                    time.Duration
	batch                       int
}

func NewRetentionWorker(tempKeys TempAuthKeyRetentionStore, logger *zap.Logger, retention, interval time.Duration, batch int) *RetentionWorker {
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
		tempKeys:  tempKeys,
		logger:    logger,
		retention: retention,
		interval:  interval,
		batch:     batch,
	}
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
func (w *RetentionWorker) WithOrphanAuthKeyRetention(store OrphanAuthKeyRetentionStore, active ActiveRawAuthKeySnapshotProvider, retention time.Duration) *RetentionWorker {
	w.orphanAuthKeys = store
	w.activeAuthKeySnapshot = active
	w.activeAuthKeyHeartbeat, _ = store.(ActiveAuthKeyHeartbeatStore)
	w.orphanRetention = retention
	return w
}

func (w *RetentionWorker) Run(ctx context.Context) {
	w.runOnce(ctx)
	retentionTicker := time.NewTicker(w.interval)
	defer retentionTicker.Stop()
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
		case <-heartbeatC:
			w.heartbeatActiveAuthKeys(ctx)
		}
	}
}

func (w *RetentionWorker) runOnce(ctx context.Context) {
	w.runRetentionOnce(ctx)
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
		protected, ok := w.activeRawAuthKeySnapshot(ctx)
		if !ok {
			// Fail safe: without a complete distributed active-key view, orphan
			// deletion cannot distinguish dead handshakes from live Edge sessions.
		} else if !w.touchActiveAuthKeys(ctx, protected) {
			// Fail safe: if this instance cannot publish the active set, deleting against a
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
	if w.activeAuthKeyHeartbeat == nil || w.activeAuthKeySnapshot == nil || w.orphanRetention <= 0 {
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
	if w.activeAuthKeySnapshot == nil {
		return
	}
	protected, ok := w.activeRawAuthKeySnapshot(ctx)
	if !ok {
		return
	}
	w.touchActiveAuthKeys(ctx, protected)
}

// touchActiveAuthKeys requires the durable database heartbeat boundary. Orphan
// deletion is unsafe without both the distributed snapshot and that heartbeat.
func (w *RetentionWorker) touchActiveAuthKeys(ctx context.Context, protected [][8]byte) bool {
	if w.activeAuthKeyHeartbeat == nil {
		w.logger.Error("active raw auth key heartbeat missing; skipping orphan GC this round",
			zap.String("signal", "active_raw_auth_key_heartbeat_missing"),
		)
		return false
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

func (w *RetentionWorker) activeRawAuthKeySnapshot(ctx context.Context) ([][8]byte, bool) {
	if w.activeAuthKeySnapshot == nil {
		w.logger.Error("active raw auth key snapshot missing; skipping orphan GC this round",
			zap.String("signal", "active_raw_auth_key_snapshot_missing"),
		)
		return nil, false
	}
	protected, err := w.activeAuthKeySnapshot.ActiveRawAuthKeySnapshot(ctx)
	if err != nil {
		w.logger.Error("failed to read active raw auth key snapshot; skipping orphan GC this round",
			zap.String("signal", "active_raw_auth_key_snapshot_failed"),
			zap.Error(err),
		)
		return nil, false
	}
	return compactRawAuthKeyIDs(protected), true
}

func compactRawAuthKeyIDs(ids [][8]byte) [][8]byte {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[[8]byte]struct{}, len(ids))
	out := make([][8]byte, 0, len(ids))
	for _, id := range ids {
		if id == ([8]byte{}) {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
