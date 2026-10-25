package store

import (
	"context"

	"telesrv/internal/domain"
)

// PasswordStore 持久化账号 2FA/SRP 配置。
type PasswordStore interface {
	GetByUser(ctx context.Context, userID int64) (domain.PasswordSettings, bool, error)
	LoginEmailOwner(ctx context.Context, email string) (int64, bool, error)
	Save(ctx context.Context, userID int64, settings domain.PasswordSettings) error
	Mutate(ctx context.Context, userID int64, apply func(*domain.PasswordSettings) error) error
}

// TmpPasswordStore persists account.getTmpPassword grants, at most one per
// permanent auth key: PutTmpPassword replaces that authorization's previous
// grant. Durable implementations delete the grant with its authorization and
// return ErrAuthorizationStateChanged when the authorization no longer exists.
type TmpPasswordStore interface {
	PutTmpPassword(ctx context.Context, grant domain.TmpPasswordGrant) error
	TmpPasswordByAuthKey(ctx context.Context, authKeyID [8]byte) (domain.TmpPasswordGrant, bool, error)
}

// AccountReactionSettingsStore persists account-level reaction preferences.
type AccountReactionSettingsStore interface {
	GetReactionSettings(ctx context.Context, userID int64) (domain.AccountReactionSettings, bool, error)
	// UpdateReactionSettings merges a patch under the owner lock and commits its
	// delivery effects atomically. Only a notify-only patch, which has no update
	// constructor, may pass nil effects; there is no fake Update for it.
	UpdateReactionSettings(ctx context.Context, userID int64, patch domain.AccountReactionSettingsPatch, effects DeliveryEffectsBuilder[domain.AccountReactionSettings]) (domain.AccountReactionSettings, error)
}

// AccountSettingsStore persists account-level singleton settings (global privacy,
// account TTL, sensitive-content toggle, contact-signup notification).
type AccountSettingsStore interface {
	GetAccountSettings(ctx context.Context, userID int64) (domain.AccountSettings, bool, error)
	SaveAccountSettings(ctx context.Context, userID int64, settings domain.AccountSettings) error
}

// AccountSettingsBatchStore is the cold-load boundary for the per-user account
// settings read model. Production stores implement it so a 100-user
// users.getRequirementsToContact request never becomes 100 SQL queries.
type AccountSettingsBatchStore interface {
	GetAccountSettingsBatch(ctx context.Context, userIDs []int64) (map[int64]domain.AccountSettings, error)
}

// NotifySettingsStore persists per-scope notification settings (specific peer /
// forum topic / the three category defaults: users, chats, broadcasts).
type NotifySettingsStore interface {
	// GetForumTopicNotifySettings loads one bounded topic page for one owner and peer.
	GetForumTopicNotifySettings(ctx context.Context, ownerUserID int64, peer domain.Peer, topicIDs []int) (map[int]domain.PeerNotifySettings, error)
	GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, bool, error)
	SaveNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error
	SaveNotifySettingsWithDelivery(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings, delivery DeliveryOutboxEnqueue) error
	ResetNotifySettingsWithDelivery(ctx context.Context, ownerUserID int64, effects DeliveryEffectsBuilder[NotifySettingsResetSnapshot]) error
	// GetPeerNotifySettings batch-loads whole-peer (topic 0) settings for the dialog list.
	GetPeerNotifySettings(ctx context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error)
	// AllPeerNotifySettings loads every whole-peer (topic 0) setting for one owner in a single
	// owner-scoped indexed query, for the per-user notify cache (dialog list + Full overlays).
	AllPeerNotifySettings(ctx context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error)
	// ListNotifyExceptions lists all per-peer non-default settings (account.getNotifyExceptions).
	ListNotifyExceptions(ctx context.Context, ownerUserID int64) ([]domain.NotifyException, error)
}

type NotifySettingsResetSnapshot struct {
	OwnerUserID int64
	Scopes      []domain.NotifyScope
}

func ValidateNotifySettingsResetDeliveryEffects(snapshot NotifySettingsResetSnapshot, effects []DeliveryEffect) error {
	if snapshot.OwnerUserID <= 0 || len(effects) != 1 {
		return ErrDeliveryOutboxRequired
	}
	effect := effects[0]
	if err := effect.Validate(); err != nil {
		return err
	}
	if effect.Kind != DeliveryEffectAbsolute || effect.TargetUserID != snapshot.OwnerUserID ||
		effect.ExcludeAuthKeyID != ([8]byte{}) || effect.ExcludeSessionID != 0 ||
		effect.RecoveryPolicy != OutboxRecoveryAbsoluteReload {
		return ErrDeliveryOutboxRequired
	}
	return nil
}

// StickerCollectionStore persists per-user personal sticker/GIF collections
// (faved / recent / recent-attached stickers, saved GIFs).
type StickerCollectionStore interface {
	MutateStickerCollection(ctx context.Context, mutation domain.StickerCollectionMutation, effects DeliveryEffectsBuilder[domain.StickerCollectionMutation]) error
	ListStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind, limit int) ([]domain.StickerCollectionItem, error)
}

// UserStickerSetStore persists per-user installed sticker set state.
// Sticker set metadata remains in MediaStore; this store only owns account-local
// installation/archive/order facts.
type UserStickerSetStore interface {
	MutateUserStickerSets(ctx context.Context, mutation domain.UserStickerSetMutation, effects DeliveryEffectsBuilder[domain.UserStickerSetMutation]) error
	ListUserStickerSets(ctx context.Context, userID int64, kind domain.StickerSetKind, archived *bool, offsetID int64, limit int) ([]domain.UserStickerSet, int, error)
}

// SavedMusicStore persists one account's ordered profile music list.
type SavedMusicStore interface {
	SaveMusic(ctx context.Context, req domain.SaveMusicRequest) error
	ListSavedMusicIDs(ctx context.Context, userID int64, limit int) ([]int64, error)
	ListSavedMusic(ctx context.Context, userID int64, offset, limit int) (domain.SavedMusicList, error)
	GetSavedMusicByIDs(ctx context.Context, userID int64, ids []int64) (domain.SavedMusicList, error)
}

// BusinessAutomationStore persists account-local Telegram Business settings.
type BusinessAutomationStore interface {
	HasBusinessAutomation(ctx context.Context, userID int64) (bool, error)
	GetBusinessProfile(ctx context.Context, userID int64) (domain.BusinessProfile, bool, error)
	SaveBusinessProfile(ctx context.Context, profile domain.BusinessProfile) error
	ListBusinessChatLinks(ctx context.Context, ownerUserID int64) ([]domain.BusinessChatLink, error)
	CreateBusinessChatLink(ctx context.Context, link domain.BusinessChatLink) (domain.BusinessChatLink, error)
	UpdateBusinessChatLink(ctx context.Context, ownerUserID int64, slug string, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error)
	DeleteBusinessChatLink(ctx context.Context, ownerUserID int64, slug string) (bool, error)
	ResolveBusinessChatLink(ctx context.Context, slug string, bumpViews bool) (domain.BusinessChatLink, bool, error)
	ListQuickReplies(ctx context.Context, ownerUserID int64, includeTopMessages bool) (domain.QuickReplyList, error)
	CheckQuickReplyShortcut(ctx context.Context, ownerUserID int64, shortcut string) (bool, error)
	GetQuickReplyMessages(ctx context.Context, ownerUserID int64, shortcutID int, ids []int) (domain.QuickReplyMessages, error)
	MutateQuickReplies(ctx context.Context, mutation QuickReplyAccountMutation, effects DeliveryEffectsBuilder[QuickReplyAccountMutationSnapshot]) (QuickReplyAccountMutationSnapshot, error)
	ReserveBusinessAutomationDelivery(ctx context.Context, delivery domain.BusinessAutomationDelivery) (bool, error)
	LastBusinessAutomationDelivery(ctx context.Context, ownerUserID, peerUserID int64, kind domain.BusinessAutomationKind) (domain.BusinessAutomationDelivery, bool, error)
	GetConnectedBusinessBot(ctx context.Context, ownerUserID int64) (domain.ConnectedBusinessBot, bool, error)
	SaveConnectedBusinessBot(ctx context.Context, bot domain.ConnectedBusinessBot) (domain.ConnectedBusinessBot, error)
	DeleteConnectedBusinessBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error)
	SetConnectedBusinessBotPaused(ctx context.Context, ownerUserID, peerUserID int64, paused bool) (domain.ConnectedBusinessBotPeerState, error)
	DisableConnectedBusinessBotForPeer(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, error)
	GetConnectedBusinessBotPeerState(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, bool, error)
}
