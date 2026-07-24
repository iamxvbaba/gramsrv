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
}

// AccountReactionSettingsStore persists account-level reaction preferences.
type AccountReactionSettingsStore interface {
	GetReactionSettings(ctx context.Context, userID int64) (domain.AccountReactionSettings, bool, error)
	SaveReactionSettings(ctx context.Context, userID int64, settings domain.AccountReactionSettings) error
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
	GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, bool, error)
	SaveNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error
	ResetNotifySettings(ctx context.Context, ownerUserID int64) error
	// GetPeerNotifySettings batch-loads whole-peer (topic 0) settings for the dialog list.
	GetPeerNotifySettings(ctx context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error)
	// AllPeerNotifySettings loads every whole-peer (topic 0) setting for one owner in a single
	// owner-scoped indexed query, for the per-user notify cache (dialog list + Full overlays).
	AllPeerNotifySettings(ctx context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error)
	// ListNotifyExceptions lists all per-peer non-default settings (account.getNotifyExceptions).
	ListNotifyExceptions(ctx context.Context, ownerUserID int64) ([]domain.NotifyException, error)
}

// StickerCollectionStore persists per-user personal sticker/GIF collections
// (faved / recent / recent-attached stickers, saved GIFs).
type StickerCollectionStore interface {
	// SaveStickerCollectionItem adds (move-to-front, capped at max) or removes a document.
	SaveStickerCollectionItem(ctx context.Context, userID int64, kind domain.StickerCollectionKind, documentID int64, unsave bool, now, max int) error
	ListStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind, limit int) ([]domain.StickerCollectionItem, error)
	ClearStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind) error
}

// UserStickerSetStore persists per-user installed sticker set state.
// Sticker set metadata remains in MediaStore; this store only owns account-local
// installation/archive/order facts.
type UserStickerSetStore interface {
	InstallUserStickerSet(ctx context.Context, userID int64, setID int64, kind domain.StickerSetKind, archived bool, installedDate int) error
	UninstallUserStickerSet(ctx context.Context, userID int64, setID int64) error
	SetUserStickerSetArchived(ctx context.Context, userID int64, setID int64, archived bool, now int) error
	ReorderUserStickerSets(ctx context.Context, userID int64, kind domain.StickerSetKind, order []int64, now int) error
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
	GetBusinessProfile(ctx context.Context, userID int64) (domain.BusinessProfile, bool, error)
	SaveBusinessProfile(ctx context.Context, profile domain.BusinessProfile) error
	ListBusinessChatLinks(ctx context.Context, ownerUserID int64) ([]domain.BusinessChatLink, error)
	CreateBusinessChatLink(ctx context.Context, link domain.BusinessChatLink) (domain.BusinessChatLink, error)
	UpdateBusinessChatLink(ctx context.Context, ownerUserID int64, slug string, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error)
	DeleteBusinessChatLink(ctx context.Context, ownerUserID int64, slug string) (bool, error)
	ResolveBusinessChatLink(ctx context.Context, slug string, bumpViews bool) (domain.BusinessChatLink, bool, error)
	ListQuickReplies(ctx context.Context, ownerUserID int64, includeTopMessages bool) (domain.QuickReplyList, error)
	CheckQuickReplyShortcut(ctx context.Context, ownerUserID int64, shortcut string) (bool, error)
	SaveQuickReplyText(ctx context.Context, ownerUserID int64, shortcut string, msg domain.QuickReplyMessage) (domain.QuickReplyMutation, error)
	GetQuickReplyMessages(ctx context.Context, ownerUserID int64, shortcutID int, ids []int) (domain.QuickReplyMessages, error)
	RenameQuickReplyShortcut(ctx context.Context, ownerUserID int64, shortcutID int, shortcut string) (domain.QuickReplyMutation, error)
	ReorderQuickReplies(ctx context.Context, ownerUserID int64, order []int) (domain.QuickReplyMutation, error)
	DeleteQuickReplyShortcut(ctx context.Context, ownerUserID int64, shortcutID int) (domain.QuickReplyMutation, error)
	DeleteQuickReplyMessages(ctx context.Context, ownerUserID int64, shortcutID int, ids []int) (domain.QuickReplyMutation, error)
	ReserveBusinessAutomationDelivery(ctx context.Context, delivery domain.BusinessAutomationDelivery) (bool, error)
	LastBusinessAutomationDelivery(ctx context.Context, ownerUserID, peerUserID int64, kind domain.BusinessAutomationKind) (domain.BusinessAutomationDelivery, bool, error)
	GetConnectedBusinessBot(ctx context.Context, ownerUserID int64) (domain.ConnectedBusinessBot, bool, error)
	SaveConnectedBusinessBot(ctx context.Context, bot domain.ConnectedBusinessBot) (domain.ConnectedBusinessBot, error)
	DeleteConnectedBusinessBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error)
	SetConnectedBusinessBotPaused(ctx context.Context, ownerUserID, peerUserID int64, paused bool) (domain.ConnectedBusinessBotPeerState, error)
	DisableConnectedBusinessBotForPeer(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, error)
	GetConnectedBusinessBotPeerState(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, bool, error)
}
