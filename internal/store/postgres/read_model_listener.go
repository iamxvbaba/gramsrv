package postgres

import (
	"context"
	"encoding/json"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const readModelChangeNotifyChannel = "telesrv_read_model_changed"

const privacyReadModelWarmTimeout = 5 * time.Second

// ReadModelCacheSet is the set of in-process projection caches that read_model_versions notifications can invalidate.
// When adding a new read model, wire its cache in here to reuse the same LISTEN connection.
type ReadModelCacheSet struct {
	ReadModelVersions   store.ReadModelVersionCache
	ChannelRows         *ChannelRowCache
	ChannelTopMessages  *ChannelTopMessageCache
	CommunityCatalog    *CommunityCatalogCache
	ChannelMembers      *ChannelMemberCache
	ChannelDialogs      *ChannelDialogCache
	ChannelDifferences  *ChannelDifferenceBaseCache
	ChannelBoosts       *ChannelBoostCache
	Contacts            ContactReadModelCache
	Dialogs             DialogReadModelCache
	Privacy             PrivacyReadModelCache
	ProfilePhotos       ProfilePhotoReadModelCache
	Stories             StoryReadModelCache
	ChannelFullBots     ChannelFullBotReadModelCache
	ChannelBotMembers   ChannelBotMemberReadModelCache
	ChannelMediaCounts  ChannelMediaCountReadModelCache
	PrivateMediaCounts  PrivateMediaCountReadModelCache
	RPCProjections      RPCProjectionReadModelCache
	PeerIdentities      PeerIdentityReadModelCache
	BaseUsers           BaseUserCache
	BotProfiles         BotProfileReadModelCache
	StarGifts           StarGiftCatalogCache
	AccountSettings     AccountSettingsReadModelCache
	AccountFreezes      AccountFreezeReadModelCache
	CollectiblePhones   CollectiblePhoneReadModelCache
	UserProjectionFacts UserProjectionFactReadModelCache
	BusinessAutomation  BusinessAutomationReadModelCache
}

type UserProjectionFactReadModelCache interface {
	InvalidateAccountFreezeFact(userID int64)
	InvalidateCollectiblePhoneFact(userID int64)
	FlushUserProjectionFactReadModel()
}

type BusinessAutomationReadModelCache interface {
	InvalidateBusinessAutomationReadModel(context.Context, int64) error
	FlushBusinessAutomationReadModel()
}

type StarGiftCatalogCache interface {
	InvalidateStarGiftCatalog()
	FlushStarGiftCatalog()
}

type AccountSettingsReadModelCache interface {
	InvalidateAccountSettingsReadModel(userID int64)
	FlushAccountSettingsReadModel()
}

type AccountSettingsReadModelWarmer interface {
	WarmAccountSettingsReadModel(context.Context, int64) error
}

type AccountFreezeReadModelCache interface {
	InvalidateAccountFreezeReadModel(userID int64)
	FlushAccountFreezeReadModel()
}

type CollectiblePhoneReadModelCache interface {
	InvalidateCollectiblePhoneReadModel(userID int64)
	FlushCollectiblePhoneReadModel()
}

type PeerIdentityReadModelCache interface {
	InvalidatePeerIdentityReadModel(domain.Peer)
	FlushPeerIdentityReadModel()
}

// BaseUserCache is the cross-process-shared user:base cache (Redis). user_base/user_deleted
// read-model events must delete the corresponding user key, otherwise after an RPC projection
// invalidation the entry is rebuilt from a stale user:base (the invalidation cancels itself out).
// It does not take part in reconnect flush: Redis is shared across instances, so clearing the whole
// store would be wrong; missed notifications are covered by its own TTL fallback.
type BaseUserCache interface {
	Delete(ctx context.Context, ids []int64) error
}

// BotProfileReadModelCache is the bots service's in-process bot profile cache. Every bot write bumps
// bot_info_version and thereby fires a user_base event, which cross-instance invalidates the bot
// profile (commands/menu/about and other in-memory-only fields; a 24h TTL is too long to rely on alone).
type BotProfileReadModelCache interface {
	InvalidateBotProfileReadModel(userID int64)
	FlushBotProfileReadModel()
}

type ContactReadModelCache interface {
	InvalidateViewers(...int64)
	FlushReadModelCache()
}

type DialogReadModelCache interface {
	InvalidateDialog(ownerUserID int64, peer domain.Peer)
	FlushReadModelCache()
}

type DialogOwnerReadModelCache interface {
	InvalidateDialogOwner(ownerUserID int64)
}

type ChannelDialogListReadModelCache interface {
	InvalidateDialogListsForChannel(channelID int64)
}

// ContactReadModelCaches fans out invalidation to multiple contact read-model caches.
type ContactReadModelCaches []ContactReadModelCache

func (c ContactReadModelCaches) InvalidateViewers(ids ...int64) {
	for _, cache := range c {
		if cache != nil {
			cache.InvalidateViewers(ids...)
		}
	}
}

func (c ContactReadModelCaches) FlushReadModelCache() {
	for _, cache := range c {
		if cache != nil {
			cache.FlushReadModelCache()
		}
	}
}

type PrivacyReadModelCache interface {
	InvalidateOwners(...int64)
	FlushReadModelCache()
}

// PrivacyReadModelWarmer lets the low-frequency change stream rebuild owner
// snapshots after invalidation, so the next user projection does not own a
// synchronous database miss. It is optional; caches without it remain
// cache-aside and only receive invalidation.
type PrivacyReadModelWarmer interface {
	WarmOwners(context.Context, ...int64) error
}

type PrivacyViewerFactsReadModelCache interface {
	InvalidateViewerFacts(...int64)
}

type PrivacyMembershipReadModelCache interface {
	InvalidateMembership(channelID, userID int64)
	InvalidateChannelMemberships(channelID int64)
}

type ProfilePhotoReadModelCache interface {
	InvalidateOwner(domain.PeerType, int64)
	FlushReadModelCache()
}

type StoryReadModelCache interface {
	InvalidateStoryReadModelViewers(...int64)
	InvalidateStoryReadModelPeer(domain.Peer)
	FlushStoryReadModelCache()
}

type ChannelFullBotReadModelCache interface {
	InvalidateChannelFullBotInfoReadModel(channelID int64)
	FlushChannelFullBotInfoReadModel()
}

type ChannelBotMemberReadModelCache interface {
	InvalidateActiveBotMemberIDsReadModel(channelID int64)
	FlushActiveBotMemberIDsReadModel()
}

type ChannelMediaCountReadModelCache interface {
	InvalidateChannelMediaCountReadModel(channelID int64)
	InvalidateChannelMediaCountReadModelForViewer(userID, channelID int64)
	FlushChannelMediaCountReadModel()
}

type PrivateMediaCountReadModelCache interface {
	InvalidatePrivateMediaCountReadModel(userID, peerID int64)
	FlushPrivateMediaCountReadModel()
}

type RPCProjectionReadModelCache interface {
	InvalidateRPCProjectionReadModelForViewer(viewerUserID int64)
	InvalidateRPCProjectionReadModelForContactOwner(ownerUserID int64)
	InvalidateRPCProjectionReadModelForUser(userID int64)
	InvalidateRPCProjectionReadModelForPeer(ownerUserID int64, peer domain.Peer)
	InvalidateRPCProjectionReadModelForChannel(channelID int64)
	FlushRPCProjectionReadModel()
}

type readModelChangePayload struct {
	Model       string `json:"model"`
	OwnerUserID int64  `json:"owner_user_id"`
	PeerType    string `json:"peer_type"`
	PeerID      int64  `json:"peer_id"`
	Version     int64  `json:"version"`
	Hash        int64  `json:"hash"`
}

// ReadModelChangeListener consumes the unified read model invalidation channel and drives in-process snapshot invalidation.
type ReadModelChangeListener struct {
	dsn    string
	caches ReadModelCacheSet
	log    *zap.Logger
}

func NewReadModelChangeListener(dsn string, caches ReadModelCacheSet, log *zap.Logger) *ReadModelChangeListener {
	if log == nil {
		log = zap.NewNop()
	}
	return &ReadModelChangeListener{dsn: dsn, caches: caches, log: log}
}

func (l *ReadModelChangeListener) Run(ctx context.Context) {
	if l == nil || l.empty() {
		return
	}
	backoff := channelListenerInitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		err := l.listenAndConsume(ctx)
		if ctx.Err() != nil {
			return
		}
		l.log.Warn("read model change listener disconnected; reconnecting",
			zap.Error(err), zap.Duration("backoff", backoff))
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff *= 2
		if backoff > channelListenerMaxBackoff {
			backoff = channelListenerMaxBackoff
		}
	}
}

func (l *ReadModelChangeListener) listenAndConsume(ctx context.Context) error {
	conn, err := connectPostgresAdmitted(ctx, l.dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	if _, err := conn.Exec(ctx, "LISTEN "+readModelChangeNotifyChannel); err != nil {
		return err
	}
	l.flush("listen_ready")
	l.log.Info("read model change listener ready", zap.String("notify_channel", readModelChangeNotifyChannel))

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if notification == nil {
			continue
		}
		l.handlePayload(notification.Payload)
	}
}

func (l *ReadModelChangeListener) empty() bool {
	return l.caches.ReadModelVersions == nil &&
		l.caches.ChannelRows == nil &&
		l.caches.ChannelTopMessages == nil &&
		l.caches.CommunityCatalog == nil &&
		l.caches.ChannelMembers == nil &&
		l.caches.ChannelDialogs == nil &&
		l.caches.ChannelDifferences == nil &&
		l.caches.ChannelBoosts == nil &&
		l.caches.Contacts == nil &&
		l.caches.Dialogs == nil &&
		l.caches.Privacy == nil &&
		l.caches.ProfilePhotos == nil &&
		l.caches.Stories == nil &&
		l.caches.ChannelFullBots == nil &&
		l.caches.ChannelBotMembers == nil &&
		l.caches.ChannelMediaCounts == nil &&
		l.caches.PrivateMediaCounts == nil &&
		l.caches.RPCProjections == nil &&
		l.caches.PeerIdentities == nil &&
		l.caches.BaseUsers == nil &&
		l.caches.BotProfiles == nil &&
		l.caches.StarGifts == nil &&
		l.caches.AccountSettings == nil &&
		l.caches.AccountFreezes == nil &&
		l.caches.CollectiblePhones == nil &&
		l.caches.UserProjectionFacts == nil &&
		l.caches.BusinessAutomation == nil
}

func (l *ReadModelChangeListener) flush(reasons ...string) {
	reason := "manual"
	if len(reasons) > 0 && reasons[0] != "" {
		reason = reasons[0]
	}
	flushed := make([]string, 0, 12)
	if l.caches.ReadModelVersions != nil {
		l.caches.ReadModelVersions.FlushReadModelCache()
		flushed = append(flushed, "read_model_versions")
	}
	if l.caches.ChannelRows != nil {
		l.caches.ChannelRows.flush()
		flushed = append(flushed, "channel_rows")
	}
	if l.caches.ChannelTopMessages != nil {
		l.caches.ChannelTopMessages.flush()
		flushed = append(flushed, "channel_top_messages")
	}
	if l.caches.CommunityCatalog != nil {
		l.caches.CommunityCatalog.flush()
		flushed = append(flushed, "community_catalog")
	}
	if l.caches.ChannelMembers != nil {
		l.caches.ChannelMembers.flush()
		flushed = append(flushed, "channel_members")
	}
	if l.caches.ChannelDialogs != nil {
		l.caches.ChannelDialogs.flush()
		flushed = append(flushed, "channel_dialogs")
	}
	if l.caches.ChannelDifferences != nil {
		l.caches.ChannelDifferences.flush()
		flushed = append(flushed, "channel_differences")
	}
	if l.caches.ChannelBoosts != nil {
		l.caches.ChannelBoosts.flush()
		flushed = append(flushed, "channel_boosts")
	}
	if l.caches.Contacts != nil {
		l.caches.Contacts.FlushReadModelCache()
		flushed = append(flushed, "contacts")
	}
	if l.caches.Dialogs != nil {
		l.caches.Dialogs.FlushReadModelCache()
		flushed = append(flushed, "dialogs")
	}
	if l.caches.Privacy != nil {
		l.caches.Privacy.FlushReadModelCache()
		flushed = append(flushed, "privacy")
	}
	if l.caches.ProfilePhotos != nil {
		l.caches.ProfilePhotos.FlushReadModelCache()
		flushed = append(flushed, "profile_photos")
	}
	if l.caches.Stories != nil {
		l.caches.Stories.FlushStoryReadModelCache()
		flushed = append(flushed, "stories")
	}
	if l.caches.ChannelFullBots != nil {
		l.caches.ChannelFullBots.FlushChannelFullBotInfoReadModel()
		flushed = append(flushed, "channel_full_bots")
	}
	if l.caches.ChannelBotMembers != nil {
		l.caches.ChannelBotMembers.FlushActiveBotMemberIDsReadModel()
		flushed = append(flushed, "channel_bot_members")
	}
	if l.caches.ChannelMediaCounts != nil {
		l.caches.ChannelMediaCounts.FlushChannelMediaCountReadModel()
		flushed = append(flushed, "channel_media_counts")
	}
	if l.caches.PrivateMediaCounts != nil {
		l.caches.PrivateMediaCounts.FlushPrivateMediaCountReadModel()
		flushed = append(flushed, "private_media_counts")
	}
	if l.caches.RPCProjections != nil {
		l.caches.RPCProjections.FlushRPCProjectionReadModel()
		flushed = append(flushed, "rpc_projections")
	}
	if l.caches.PeerIdentities != nil {
		l.caches.PeerIdentities.FlushPeerIdentityReadModel()
		flushed = append(flushed, "peer_identities")
	}
	if l.caches.BotProfiles != nil {
		l.caches.BotProfiles.FlushBotProfileReadModel()
		flushed = append(flushed, "bot_profiles")
	}
	if l.caches.StarGifts != nil {
		l.caches.StarGifts.FlushStarGiftCatalog()
		flushed = append(flushed, "star_gifts")
	}
	if l.caches.AccountSettings != nil {
		l.caches.AccountSettings.FlushAccountSettingsReadModel()
		flushed = append(flushed, "account_settings")
	}
	if l.caches.AccountFreezes != nil {
		l.caches.AccountFreezes.FlushAccountFreezeReadModel()
		flushed = append(flushed, "account_freezes")
	}
	if l.caches.CollectiblePhones != nil {
		l.caches.CollectiblePhones.FlushCollectiblePhoneReadModel()
		flushed = append(flushed, "collectible_phones")
	}
	if l.caches.UserProjectionFacts != nil {
		l.caches.UserProjectionFacts.FlushUserProjectionFactReadModel()
		flushed = append(flushed, "user_projection_facts")
	}
	if l.caches.BusinessAutomation != nil {
		l.caches.BusinessAutomation.FlushBusinessAutomationReadModel()
		flushed = append(flushed, "business_automation")
	}
	// Note: BaseUsers (Redis) is deliberately not flushed on reconnect -- it is a cross-instance shared
	// cache, and clearing the whole store would hurt other instances; missed notifications fall back to its 5min TTL.
	l.log.Info("read model caches flushed",
		zap.String("reason", reason),
		zap.Int("cache_groups", len(flushed)),
		zap.Strings("caches", flushed))
}

func (l *ReadModelChangeListener) handlePayload(payload string) {
	var evt readModelChangePayload
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		l.log.Debug("ignore malformed read model change payload", zap.String("payload", payload), zap.Error(err))
		return
	}
	l.handleChange(evt)
}

// HandleReadModelInvalidations applies Redis-transported exact keys through
// the same cache semantics as PostgreSQL notifications.
func (l *ReadModelChangeListener) HandleReadModelInvalidations(items []store.ReadModelInvalidation) {
	if l == nil {
		return
	}
	for _, item := range items {
		l.handleChange(readModelChangePayload{
			Model: item.Key.Model, OwnerUserID: item.Key.OwnerUserID,
			PeerType: string(item.Key.PeerType), PeerID: item.Key.PeerID,
			Version: item.Version, Hash: item.Hash,
		})
	}
}

// FlushRelayedReadModelCaches closes a Redis Pub/Sub disconnect window for the
// cache families transported by the durable PostgreSQL -> Redis relay.
func (l *ReadModelChangeListener) FlushRelayedReadModelCaches(reason string) {
	if l == nil {
		return
	}
	flushed := make([]string, 0, 4)
	if l.caches.ReadModelVersions != nil {
		l.caches.ReadModelVersions.FlushReadModelCache()
		flushed = append(flushed, "read_model_versions")
	}
	if l.caches.ChannelDialogs != nil {
		l.caches.ChannelDialogs.flush()
		flushed = append(flushed, "channel_dialogs")
	}
	if l.caches.Dialogs != nil {
		l.caches.Dialogs.FlushReadModelCache()
		flushed = append(flushed, "dialogs")
	}
	if l.caches.RPCProjections != nil {
		l.caches.RPCProjections.FlushRPCProjectionReadModel()
		flushed = append(flushed, "rpc_projections")
	}
	if l.caches.BusinessAutomation != nil {
		l.caches.BusinessAutomation.FlushBusinessAutomationReadModel()
		flushed = append(flushed, "business_automation")
	}
	l.log.Info("relayed read-model caches flushed",
		zap.String("reason", reason), zap.Strings("caches", flushed))
}

func (l *ReadModelChangeListener) handleChange(evt readModelChangePayload) {
	if l.caches.ReadModelVersions != nil && evt.Model != "" {
		key := store.ReadModelKey{
			Model:       evt.Model,
			OwnerUserID: evt.OwnerUserID,
			PeerType:    domain.PeerType(evt.PeerType),
			PeerID:      evt.PeerID,
		}
		if updater, ok := l.caches.ReadModelVersions.(store.ReadModelVersionCacheUpdater); ok && evt.Hash != 0 {
			updater.UpdateReadModelHash(key, evt.Hash)
		} else {
			l.caches.ReadModelVersions.InvalidateReadModel(key)
		}
	}
	switch evt.Model {
	case "community_catalog":
		if l.caches.CommunityCatalog != nil {
			l.caches.CommunityCatalog.invalidate()
		}
	case "business_automation":
		if evt.OwnerUserID != 0 && l.caches.BusinessAutomation != nil {
			if err := l.caches.BusinessAutomation.InvalidateBusinessAutomationReadModel(context.Background(), evt.OwnerUserID); err != nil {
				l.log.Warn("invalidate business automation read model",
					zap.Int64("owner_user_id", evt.OwnerUserID), zap.Error(err))
			}
		}
	case "account_settings":
		if evt.OwnerUserID != 0 && l.caches.AccountSettings != nil {
			l.caches.AccountSettings.InvalidateAccountSettingsReadModel(evt.OwnerUserID)
			if warmer, ok := l.caches.AccountSettings.(AccountSettingsReadModelWarmer); ok {
				ctx, cancel := context.WithTimeout(context.Background(), privacyReadModelWarmTimeout)
				err := warmer.WarmAccountSettingsReadModel(ctx, evt.OwnerUserID)
				cancel()
				if err != nil {
					l.log.Warn("warm account settings read model after change",
						zap.Int64("owner_user_id", evt.OwnerUserID), zap.Error(err))
				}
			}
		}
	case "star_gift_catalog":
		if l.caches.StarGifts != nil {
			l.caches.StarGifts.InvalidateStarGiftCatalog()
		}
	case "user_deleted":
		if evt.PeerType == "user" && evt.PeerID != 0 {
			// Logical account deletion changes the target User for every viewer but
			// deliberately keeps contacts, dialogs and memberships intact.  A coarse
			// projection flush is bounded (the first event empties the caches; a batch
			// of later events is O(1)) and avoids four full-map scans per deleted user.
			if l.caches.RPCProjections != nil {
				l.caches.RPCProjections.FlushRPCProjectionReadModel()
			}
			if l.caches.BaseUsers != nil {
				if err := l.caches.BaseUsers.Delete(context.Background(), []int64{evt.PeerID}); err != nil {
					l.log.Warn("invalidate base user cache on user_deleted event",
						zap.Int64("user_id", evt.PeerID), zap.Error(err))
				}
			}
			if l.caches.UserProjectionFacts != nil {
				l.caches.UserProjectionFacts.InvalidateAccountFreezeFact(evt.PeerID)
				l.caches.UserProjectionFacts.InvalidateCollectiblePhoneFact(evt.PeerID)
			}
			if l.caches.PeerIdentities != nil {
				l.caches.PeerIdentities.InvalidatePeerIdentityReadModel(domain.Peer{Type: domain.PeerTypeUser, ID: evt.PeerID})
			}
		}
	case "user_base":
		if evt.PeerType == "user" && evt.PeerID != 0 {
			if l.caches.RPCProjections != nil {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForUser(evt.PeerID)
			}
			// Cross-instance invalidate the Redis user:base: otherwise this instance's RPC projection
			// invalidation would rebuild from another instance's still-stale (in fact this instance's own)
			// user:base, which is as good as no invalidation; this also covers system-user upserts.
			if l.caches.BaseUsers != nil {
				if err := l.caches.BaseUsers.Delete(context.Background(), []int64{evt.PeerID}); err != nil {
					l.log.Warn("invalidate base user cache on user_base event",
						zap.Int64("user_id", evt.PeerID), zap.Error(err))
				}
			}
			// A bot write bumps bot_info_version and also goes through user_base: cross-instance invalidate the bot profile cache.
			if l.caches.BotProfiles != nil {
				l.caches.BotProfiles.InvalidateBotProfileReadModel(evt.PeerID)
			}
			if cache, ok := l.caches.Privacy.(PrivacyViewerFactsReadModelCache); ok {
				cache.InvalidateViewerFacts(evt.PeerID)
			}
		}
	case "user_visibility":
		if evt.PeerType == "user" && evt.PeerID != 0 {
			if l.caches.UserProjectionFacts != nil {
				l.caches.UserProjectionFacts.InvalidateAccountFreezeFact(evt.PeerID)
			}
			if l.caches.AccountFreezes != nil {
				l.caches.AccountFreezes.InvalidateAccountFreezeReadModel(evt.PeerID)
			}
			if l.caches.RPCProjections != nil {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForUser(evt.PeerID)
			}
			if l.caches.Stories != nil {
				l.caches.Stories.InvalidateStoryReadModelPeer(domain.Peer{Type: domain.PeerTypeUser, ID: evt.PeerID})
			}
		}
	case "collectible_phone":
		if evt.PeerType == "user" && evt.PeerID != 0 {
			if l.caches.CollectiblePhones != nil {
				l.caches.CollectiblePhones.InvalidateCollectiblePhoneReadModel(evt.PeerID)
			}
			if l.caches.RPCProjections != nil {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForUser(evt.PeerID)
			}
		}
	case "user_collectible_phone":
		if evt.PeerType == "user" && evt.PeerID != 0 && l.caches.UserProjectionFacts != nil {
			l.caches.UserProjectionFacts.InvalidateCollectiblePhoneFact(evt.PeerID)
		}
	case "peer_identity":
		if evt.PeerID != 0 {
			if peerType, ok := readModelPeerType(evt.PeerType); ok && l.caches.PeerIdentities != nil {
				l.caches.PeerIdentities.InvalidatePeerIdentityReadModel(domain.Peer{Type: peerType, ID: evt.PeerID})
			}
			if l.caches.RPCProjections != nil {
				switch evt.PeerType {
				case "user":
					l.caches.RPCProjections.InvalidateRPCProjectionReadModelForUser(evt.PeerID)
				case "channel":
					l.caches.RPCProjections.InvalidateRPCProjectionReadModelForChannel(evt.PeerID)
				}
			}
		}
	case "bot_full":
		// A bot profile change (name/about/description/commands/menu_button) is triggered by a
		// bot_info_version bump (migration 0013). channelFullBotInfoCache is keyed by (viewer,channel)
		// and cannot pinpoint by botID which channels a bot is involved in, so it is flushed wholesale
		// (bot profile changes are infrequent; the cost is an occasional full rebuild).
		// This covers the group-info-page bot_info staleness missed by the user_base branch: both the
		// local BotFather profile-change path and the cross-instance bots.* RPC path are invalidated here.
		if l.caches.ChannelFullBots != nil {
			l.caches.ChannelFullBots.FlushChannelFullBotInfoReadModel()
		}
	case "contact_account", "contact_blocklist":
		if evt.OwnerUserID != 0 && l.caches.Contacts != nil {
			l.caches.Contacts.InvalidateViewers(evt.OwnerUserID)
		}
		if evt.OwnerUserID != 0 && l.caches.Stories != nil {
			l.caches.Stories.InvalidateStoryReadModelPeer(domain.Peer{Type: domain.PeerTypeUser, ID: evt.OwnerUserID})
		}
		if evt.OwnerUserID != 0 && l.caches.RPCProjections != nil {
			if evt.Model == "contact_account" {
				// contact_account is an owner-level aggregate event: direct contact
				// fields affect owner-as-viewer, while the inverse contact edge can
				// change how every viewer sees owner through privacy rules.
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForContactOwner(evt.OwnerUserID)
			} else {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForViewer(evt.OwnerUserID)
			}
		}
	case "story_peer":
		// Writes to stories / story_hidden_peers (0135 trigger) → invalidate that owner peer's story
		// projections (ring/hidden/pinned availability/pinned pages) by owner peer, across instances.
		if peerType, ok := readModelPeerType(evt.PeerType); ok && evt.PeerID != 0 && l.caches.Stories != nil {
			l.caches.Stories.InvalidateStoryReadModelPeer(domain.Peer{Type: peerType, ID: evt.PeerID})
		}
	case "story_hidden_list":
		if evt.OwnerUserID != 0 && evt.PeerType == "user" && evt.PeerID == evt.OwnerUserID && l.caches.Stories != nil {
			l.caches.Stories.InvalidateStoryReadModelViewers(evt.OwnerUserID)
		}
	case "privacy_rules":
		if evt.OwnerUserID != 0 && l.caches.Privacy != nil {
			l.caches.Privacy.InvalidateOwners(evt.OwnerUserID)
		}
		if evt.OwnerUserID != 0 && l.caches.Stories != nil {
			l.caches.Stories.InvalidateStoryReadModelViewers(evt.OwnerUserID)
		}
		if evt.OwnerUserID != 0 && l.caches.RPCProjections != nil {
			l.caches.RPCProjections.InvalidateRPCProjectionReadModelForUser(evt.OwnerUserID)
			l.caches.RPCProjections.InvalidateRPCProjectionReadModelForViewer(evt.OwnerUserID)
		}
		if warmer, ok := l.caches.Privacy.(PrivacyReadModelWarmer); ok && evt.OwnerUserID != 0 {
			ctx, cancel := context.WithTimeout(context.Background(), privacyReadModelWarmTimeout)
			err := warmer.WarmOwners(ctx, evt.OwnerUserID)
			cancel()
			if err != nil {
				l.log.Warn("warm privacy read model after change",
					zap.Int64("owner_user_id", evt.OwnerUserID), zap.Error(err))
			}
		}
	case "dialog_light":
		if peerType, ok := readModelPeerType(evt.PeerType); ok && evt.OwnerUserID != 0 && evt.PeerID != 0 {
			peer := domain.Peer{Type: peerType, ID: evt.PeerID}
			if l.caches.Dialogs != nil {
				l.caches.Dialogs.InvalidateDialog(evt.OwnerUserID, peer)
			}
			if peerType == domain.PeerTypeChannel && l.caches.ChannelDialogs != nil {
				l.caches.ChannelDialogs.delete(evt.OwnerUserID, evt.PeerID)
			}
			if l.caches.RPCProjections != nil {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForPeer(evt.OwnerUserID, peer)
			}
		}
	case "dialog_owner":
		if evt.OwnerUserID != 0 {
			if cache, ok := l.caches.Dialogs.(DialogOwnerReadModelCache); ok {
				cache.InvalidateDialogOwner(evt.OwnerUserID)
			}
		}
	case "profile_photo":
		if peerType, ok := readModelPeerType(evt.PeerType); ok && evt.PeerID != 0 && l.caches.ProfilePhotos != nil {
			l.caches.ProfilePhotos.InvalidateOwner(peerType, evt.PeerID)
		}
		if peerType, ok := readModelPeerType(evt.PeerType); ok && evt.PeerID != 0 && l.caches.RPCProjections != nil {
			if peerType == domain.PeerTypeUser {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForUser(evt.PeerID)
			} else if peerType == domain.PeerTypeChannel {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForChannel(evt.PeerID)
			}
		}
	case "channel_base":
		if evt.PeerType == "channel" && evt.PeerID != 0 {
			if cache, ok := l.caches.Dialogs.(ChannelDialogListReadModelCache); ok {
				cache.InvalidateDialogListsForChannel(evt.PeerID)
			}
			if l.caches.ChannelRows != nil {
				l.caches.ChannelRows.delete(evt.PeerID)
			}
			if l.caches.ChannelTopMessages != nil {
				l.caches.ChannelTopMessages.deleteChannel(evt.PeerID)
			}
			if l.caches.ChannelMembers != nil {
				l.caches.ChannelMembers.deleteChannel(evt.PeerID)
			}
			if l.caches.ChannelDialogs != nil {
				l.caches.ChannelDialogs.deleteChannel(evt.PeerID)
			}
			if l.caches.ChannelDifferences != nil {
				l.caches.ChannelDifferences.deleteChannel(evt.PeerID)
			}
			if l.caches.ChannelFullBots != nil {
				l.caches.ChannelFullBots.InvalidateChannelFullBotInfoReadModel(evt.PeerID)
			}
			if l.caches.ChannelBotMembers != nil {
				l.caches.ChannelBotMembers.InvalidateActiveBotMemberIDsReadModel(evt.PeerID)
			}
			if l.caches.RPCProjections != nil {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForChannel(evt.PeerID)
			}
			if cache, ok := l.caches.Privacy.(PrivacyMembershipReadModelCache); ok {
				cache.InvalidateChannelMemberships(evt.PeerID)
			}
		}
	case "channel_difference_base":
		if evt.PeerType == "channel" && evt.PeerID != 0 && l.caches.ChannelDifferences != nil {
			l.caches.ChannelDifferences.deleteChannel(evt.PeerID)
		}
	case "channel_media_counts":
		if evt.PeerType == "channel" && evt.PeerID != 0 && l.caches.ChannelMediaCounts != nil {
			l.caches.ChannelMediaCounts.InvalidateChannelMediaCountReadModel(evt.PeerID)
		}
	case "channel_member":
		if evt.PeerType == "channel" && evt.PeerID != 0 && evt.OwnerUserID != 0 {
			if l.caches.ChannelMembers != nil {
				l.caches.ChannelMembers.delete(evt.PeerID, evt.OwnerUserID)
			}
			if l.caches.ChannelMediaCounts != nil {
				l.caches.ChannelMediaCounts.InvalidateChannelMediaCountReadModelForViewer(evt.OwnerUserID, evt.PeerID)
			}
			if l.caches.ChannelDialogs != nil {
				l.caches.ChannelDialogs.delete(evt.OwnerUserID, evt.PeerID)
			}
			if l.caches.Dialogs != nil {
				l.caches.Dialogs.InvalidateDialog(evt.OwnerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: evt.PeerID})
			}
			if l.caches.ChannelFullBots != nil {
				l.caches.ChannelFullBots.InvalidateChannelFullBotInfoReadModel(evt.PeerID)
			}
			if l.caches.ChannelBotMembers != nil {
				l.caches.ChannelBotMembers.InvalidateActiveBotMemberIDsReadModel(evt.PeerID)
			}
			if l.caches.RPCProjections != nil {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForChannel(evt.PeerID)
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForPeer(evt.OwnerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: evt.PeerID})
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForUser(evt.OwnerUserID)
			}
			if cache, ok := l.caches.Privacy.(PrivacyMembershipReadModelCache); ok {
				cache.InvalidateMembership(evt.PeerID, evt.OwnerUserID)
			}
		}
	case "channel_self_boosts":
		if evt.PeerType == "channel" && evt.PeerID != 0 && l.caches.ChannelBoosts != nil {
			peer := domain.Peer{Type: domain.PeerTypeChannel, ID: evt.PeerID}
			if evt.OwnerUserID != 0 {
				l.caches.ChannelBoosts.delete(evt.OwnerUserID, peer)
			}
			l.caches.ChannelBoosts.deletePeerTotal(peer)
		}
		if evt.PeerType == "channel" && evt.PeerID != 0 && evt.OwnerUserID != 0 && l.caches.RPCProjections != nil {
			l.caches.RPCProjections.InvalidateRPCProjectionReadModelForPeer(evt.OwnerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: evt.PeerID})
		}
	case "channel_participants":
		if evt.PeerType == "channel" && evt.PeerID != 0 && l.caches.ChannelBotMembers != nil {
			l.caches.ChannelBotMembers.InvalidateActiveBotMemberIDsReadModel(evt.PeerID)
		}
		if evt.PeerType == "channel" && evt.PeerID != 0 && l.caches.RPCProjections != nil {
			l.caches.RPCProjections.InvalidateRPCProjectionReadModelForChannel(evt.PeerID)
		}
	case "private_media_counts":
		if evt.OwnerUserID != 0 && evt.PeerType == "user" && evt.PeerID != 0 && l.caches.PrivateMediaCounts != nil {
			l.caches.PrivateMediaCounts.InvalidatePrivateMediaCountReadModel(evt.OwnerUserID, evt.PeerID)
		}
	}
}

func readModelPeerType(value string) (domain.PeerType, bool) {
	switch domain.PeerType(value) {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		return domain.PeerType(value), true
	default:
		return "", false
	}
}
