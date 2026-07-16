package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const readModelChangeNotifyChannel = "telesrv_read_model_changed"

// ReadModelCacheSet 是 read_model_versions 通知可失效的进程内投影缓存集合。
// 后续新增 read model 时，把缓存接到这里即可复用同一条 LISTEN 连接。
type ReadModelCacheSet struct {
	ReadModelVersions  store.ReadModelVersionCache
	ChannelRows        *ChannelRowCache
	ChannelMembers     *ChannelMemberCache
	ChannelDialogs     *ChannelDialogCache
	ChannelBoosts      *ChannelBoostCache
	Contacts           ContactReadModelCache
	Dialogs            DialogReadModelCache
	Privacy            PrivacyReadModelCache
	ProfilePhotos      ProfilePhotoReadModelCache
	Stories            StoryReadModelCache
	ChannelFullBots    ChannelFullBotReadModelCache
	ChannelBotMembers  ChannelBotMemberReadModelCache
	ChannelMediaCounts ChannelMediaCountReadModelCache
	PrivateMediaCounts PrivateMediaCountReadModelCache
	RPCProjections     RPCProjectionReadModelCache
	BaseUsers          BaseUserCache
	BotProfiles        BotProfileReadModelCache
	StarGifts          StarGiftCatalogCache
}

type StarGiftCatalogCache interface {
	InvalidateStarGiftCatalog()
	FlushStarGiftCatalog()
}

// BaseUserCache 是跨进程共享的 user:base 缓存(Redis)。user_base read-model 事件必须删除
// 对应 user 键，否则 RPC 投影失效后会从陈旧的 user:base 重建(失效被未失效的源自我抵消)。
// 不参与重连 flush：Redis 是跨实例共享的，整库清空是错的，漏掉的通知靠其自身 TTL 兜底。
type BaseUserCache interface {
	Delete(ctx context.Context, ids []int64) error
}

// BotProfileReadModelCache 是 bots 服务的进程内 bot 资料缓存。每次 bot 写都会 bump
// bot_info_version 从而触发 user_base 事件，据此跨实例失效 bot 资料(命令/菜单/简介等
// 仅存内存的字段，TTL 24h 太长不能只靠它)。
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

// ReadModelChangeListener 消费统一 read model invalidation 通道，驱动进程内 snapshot 失效。
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
	conn, err := pgx.Connect(ctx, l.dsn)
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
		l.caches.ChannelMembers == nil &&
		l.caches.ChannelDialogs == nil &&
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
		l.caches.BaseUsers == nil &&
		l.caches.BotProfiles == nil &&
		l.caches.StarGifts == nil
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
	if l.caches.ChannelMembers != nil {
		l.caches.ChannelMembers.flush()
		flushed = append(flushed, "channel_members")
	}
	if l.caches.ChannelDialogs != nil {
		l.caches.ChannelDialogs.flush()
		flushed = append(flushed, "channel_dialogs")
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
	if l.caches.BotProfiles != nil {
		l.caches.BotProfiles.FlushBotProfileReadModel()
		flushed = append(flushed, "bot_profiles")
	}
	if l.caches.StarGifts != nil {
		l.caches.StarGifts.FlushStarGiftCatalog()
		flushed = append(flushed, "star_gifts")
	}
	// 注意：BaseUsers(Redis) 刻意不在重连时 flush——它是跨实例共享缓存，整库清空会误伤
	// 其它实例；漏掉的通知由其 5min TTL 兜底。
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
	case "star_gift_catalog":
		if l.caches.StarGifts != nil {
			l.caches.StarGifts.InvalidateStarGiftCatalog()
		}
	case "user_base":
		if evt.PeerType == "user" && evt.PeerID != 0 {
			if l.caches.RPCProjections != nil {
				l.caches.RPCProjections.InvalidateRPCProjectionReadModelForUser(evt.PeerID)
			}
			// 跨实例失效 Redis user:base：否则本实例的 RPC 投影失效后会从其它实例仍陈旧的
			// (其实是本实例自己的) user:base 重建，等于没失效；这也覆盖了 system-user upsert。
			if l.caches.BaseUsers != nil {
				if err := l.caches.BaseUsers.Delete(context.Background(), []int64{evt.PeerID}); err != nil {
					l.log.Warn("invalidate base user cache on user_base event",
						zap.Int64("user_id", evt.PeerID), zap.Error(err))
				}
			}
			// bot 写 bump bot_info_version 也走 user_base：跨实例失效 bot 资料缓存。
			if l.caches.BotProfiles != nil {
				l.caches.BotProfiles.InvalidateBotProfileReadModel(evt.PeerID)
			}
		}
	case "bot_full":
		// bot 资料(name/about/description/commands/menu_button)变更经 bot_info_version
		// bump 触发(迁移 0013)。channelFullBotInfoCache 按 (viewer,channel) 键、无法按 botID
		// 精确定位某 bot 涉及的频道,故整体 flush(bot 改资料频率低,代价是偶发全量重建)。
		// 这覆盖了 user_base 分支漏失效的群信息页 bot_info 陈旧:本地 BotFather 改资料路径
		// 与跨实例 bots.* RPC 两条更新路径都经此失效。
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
			l.caches.RPCProjections.InvalidateRPCProjectionReadModelForViewer(evt.OwnerUserID)
		}
	case "story_peer":
		// stories / story_hidden_peers 写(0135 触发器)→ 按 owner peer 失效该 peer 的
		// 故事投影(ring/hidden/置顶可用性/置顶分页),实现跨实例失效。
		if peerType, ok := readModelPeerType(evt.PeerType); ok && evt.PeerID != 0 && l.caches.Stories != nil {
			l.caches.Stories.InvalidateStoryReadModelPeer(domain.Peer{Type: peerType, ID: evt.PeerID})
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
			if l.caches.ChannelRows != nil {
				l.caches.ChannelRows.delete(evt.PeerID)
			}
			if l.caches.ChannelMembers != nil {
				l.caches.ChannelMembers.deleteChannel(evt.PeerID)
			}
			if l.caches.ChannelDialogs != nil {
				l.caches.ChannelDialogs.deleteChannel(evt.PeerID)
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
