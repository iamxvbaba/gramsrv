package store

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// BotStore 持久化 bot 账号元数据（bots 表）与内置 bot 对话状态（bot_chat_states 表）。
//
// CreateBotAccountWithDelivery 必须原子地创建 users 行（is_bot=true,
// bot_info_version=1, phone 空）、bots 行与 owner absolute delivery；username
// 冲突返回 domain.ErrUsernameOccupied。不存在 mutation-only 创建入口。
type BotStore interface {
	CreateBotAccountWithDelivery(ctx context.Context, user domain.User, profile domain.BotProfile, effects DeliveryEffectsBuilder[BotLifecycleDeliverySnapshot]) (domain.User, domain.BotProfile, error)
	DeleteBotAccountWithDelivery(ctx context.Context, botUserID int64, effects DeliveryEffectsBuilder[BotLifecycleDeliverySnapshot]) (domain.User, error)
	GetBot(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error)
	GetBotInfo(ctx context.Context, botUserID int64, langCode string) (domain.BotInfoValues, bool, error)
	ListBotsByOwner(ctx context.Context, ownerUserID int64) ([]domain.BotProfile, error)
	CountBotsByOwner(ctx context.Context, ownerUserID int64) (int, error)
	// UpdateBotTokenSecret 写入新 token 随机段；旧 token 立即失效（无缓存层）。
	UpdateBotTokenSecret(ctx context.Context, botUserID int64, secret string) error

	// 以下 P2 元数据写入：每个都在单事务内同时 bump 该 bot 对应 users 行的
	// bot_info_version（客户端据此感知变更并重拉 getFullUser），返回 bump 后的新
	// version。UpdateBotInfoWithDelivery 跨 users（first_name/about）与 bots（description）两表，
	// 因此放在同一原子方法里。
	UpdateBotCommandsWithDelivery(ctx context.Context, botUserID int64, commands []domain.BotCommand, effects DeliveryEffectsBuilder[BotCommandsDeliverySnapshot]) (newVersion int, changed bool, err error)
	UpdateBotInfoWithDelivery(ctx context.Context, botUserID int64, upd domain.BotInfoUpdate, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (newVersion int, changed bool, err error)
	UpdateBotMenuButton(ctx context.Context, botUserID int64, button domain.BotMenuButton) (newVersion int, err error)
	SetBotInlinePlaceholder(ctx context.Context, botUserID int64, placeholder string) (newVersion int, err error)
	SetBotInlineGeo(ctx context.Context, botUserID int64, inlineGeo bool) (newVersion int, err error)
	SetBotNochats(ctx context.Context, botUserID int64, nochats bool) (newVersion int, err error)
	SetBotChatHistory(ctx context.Context, botUserID int64, chatHistory bool) (newVersion int, err error)
	CanBotSendMessage(ctx context.Context, botUserID, userID int64) (bool, error)
	AllowBotSendMessage(ctx context.Context, botUserID, userID int64, fromRequest bool) (created bool, err error)

	UpsertBotApp(ctx context.Context, app domain.BotApp) (domain.BotApp, int, error)
	GetBotAppByID(ctx context.Context, appID, accessHash int64) (domain.BotApp, bool, error)
	GetBotAppByShortName(ctx context.Context, botUserID int64, shortName string) (domain.BotApp, bool, error)
	GetMainBotApp(ctx context.Context, botUserID int64) (domain.BotApp, bool, error)
	ListBotApps(ctx context.Context, botUserID int64) ([]domain.BotApp, error)
	GetBotAppSettings(ctx context.Context, botUserID int64) (domain.BotAppSettings, bool, error)
	UpsertBotAppSettings(ctx context.Context, botUserID int64, settings domain.BotAppSettings) (int, error)
	ListBotAppPreviewMedia(ctx context.Context, botUserID, appID int64) ([]domain.BotAppPreviewMedia, error)
	UpsertBotAppPreviewMedia(ctx context.Context, media domain.BotAppPreviewMedia) (domain.BotAppPreviewMedia, int, error)
	DeleteBotAppPreviewMedia(ctx context.Context, botUserID, appID, mediaID int64) (int, error)
	ReorderBotAppPreviewMedia(ctx context.Context, botUserID, appID int64, mediaIDs []int64) (int, error)

	UpsertAttachMenuBot(ctx context.Context, bot domain.BotAttachMenuBot) (int, error)

	// Bot invoices. Creation is called inside the invoice message transaction.
	CreateBotInvoice(ctx context.Context, invoice domain.BotInvoice) (domain.BotInvoice, error)
	// BotInvoiceByMessage resolves the invoice behind an inputInvoiceMessage.
	BotInvoiceByMessage(ctx context.Context, botUserID, chatID int64, messageID int) (domain.BotInvoice, bool, error)

	// BotInvoiceByCharge resolves an invoice by telegram_payment_charge_id without
	// changing it, so the caller can check ownership before acting.
	BotInvoiceByCharge(ctx context.Context, chargeID string) (domain.BotInvoice, bool, error)

	GetAttachMenuBot(ctx context.Context, botUserID int64) (domain.BotAttachMenuBot, bool, error)
	ListAttachMenuBots(ctx context.Context) ([]domain.BotAttachMenuBot, error)
	GetAttachMenuState(ctx context.Context, userID, botUserID int64) (domain.BotAttachMenuState, bool, error)
	SetAttachMenuState(ctx context.Context, state domain.BotAttachMenuState, effects DeliveryEffectsBuilder[domain.BotAttachMenuState]) (domain.BotAttachMenuState, error)

	SaveRequestedWebViewButton(ctx context.Context, button domain.BotRequestedWebViewButton) error
	GetRequestedWebViewButton(ctx context.Context, botUserID, userID int64, webAppReqID string) (domain.BotRequestedWebViewButton, bool, error)
	DeleteRequestedWebViewButton(ctx context.Context, botUserID, userID int64, webAppReqID string) error

	SetBotEmojiStatusPermission(ctx context.Context, botUserID, userID int64, allowed bool) error
	BotEmojiStatusPermission(ctx context.Context, botUserID, userID int64) (bool, error)

	PutWebViewCustomMethodQuery(ctx context.Context, query domain.BotWebViewCustomMethodQuery) error

	GetBotChatState(ctx context.Context, botUserID, userID int64) (domain.BotChatState, bool, error)
	UpsertBotChatState(ctx context.Context, state domain.BotChatState) error
	DeleteBotChatState(ctx context.Context, botUserID, userID int64) error
}

// BotLifecycleDeliverySnapshot freezes the bot tombstone/live projection and
// its owning account. Bot accounts have no independent recipient lane before
// login, so create/delete is observed by exactly the owner account.
type BotLifecycleDeliverySnapshot struct {
	Bot         domain.User
	OwnerUserID int64
	Deleted     bool
}

func ValidateBotLifecycleDeliveryEffects(snapshot BotLifecycleDeliverySnapshot, effects []DeliveryEffect) error {
	if snapshot.Bot.ID <= 0 || snapshot.OwnerUserID <= 0 || snapshot.Bot.Deleted != snapshot.Deleted {
		return fmt.Errorf("bot lifecycle delivery snapshot is invalid")
	}
	if len(effects) != 1 {
		return fmt.Errorf("bot lifecycle delivery requires exactly one owner effect")
	}
	effect := effects[0]
	if err := effect.Validate(); err != nil {
		return fmt.Errorf("bot lifecycle delivery effect: %w", err)
	}
	if effect.Kind != DeliveryEffectAbsolute || effect.TargetUserID != snapshot.OwnerUserID {
		return fmt.Errorf("bot lifecycle delivery effect has unexpected target")
	}
	if effect.ExcludeAuthKeyID != ([8]byte{}) || effect.ExcludeSessionID != 0 {
		return fmt.Errorf("bot lifecycle delivery must not exclude a session")
	}
	return nil
}

// MaxBotCommandsDeliveryAudience is a correctness and transaction-size
// boundary, not a pagination limit. A bot with more private-dialog viewers must
// fail the command mutation so no viewer is silently omitted from the durable
// non-PTS notification set.
const MaxBotCommandsDeliveryAudience = 4096

// MaxBotInfoDeliveryAudience bounds owner + private-dialog viewers frozen by a
// bot-info aggregate. It is a correctness boundary, never a pagination limit.
const MaxBotInfoDeliveryAudience = 4096

// BotCommandsDeliverySnapshot is frozen from the authoritative private-dialog
// read model while the bot aggregate transaction owns the bot user's write
// lock. Commands and Audience are immutable inputs to the RPC/TL projector.
type BotCommandsDeliverySnapshot struct {
	BotUserID int64
	Commands  []domain.BotCommand
	Audience  []int64
	Version   int
}

// ValidateBotCommandsDeliveryEffects requires an exact, duplicate-free
// one-row-per-viewer absolute delivery set. An empty audience is valid only
// with zero effects.
func ValidateBotCommandsDeliveryEffects(snapshot BotCommandsDeliverySnapshot, effects []DeliveryEffect) error {
	if snapshot.BotUserID <= 0 || snapshot.Version <= 0 {
		return fmt.Errorf("bot commands delivery snapshot is invalid")
	}
	if len(snapshot.Audience) > MaxBotCommandsDeliveryAudience {
		return fmt.Errorf("bot commands delivery audience exceeds %d", MaxBotCommandsDeliveryAudience)
	}
	if len(effects) != len(snapshot.Audience) {
		return fmt.Errorf("bot commands delivery requires one effect per frozen viewer")
	}
	want := make(map[int64]struct{}, len(snapshot.Audience))
	for _, viewerID := range snapshot.Audience {
		if viewerID <= 0 || viewerID == snapshot.BotUserID {
			return fmt.Errorf("bot commands delivery contains invalid viewer")
		}
		if _, duplicate := want[viewerID]; duplicate {
			return fmt.Errorf("bot commands delivery contains duplicate viewer %d", viewerID)
		}
		want[viewerID] = struct{}{}
	}
	seen := make(map[int64]struct{}, len(effects))
	for i := range effects {
		effect := effects[i]
		if err := effect.Validate(); err != nil {
			return fmt.Errorf("bot commands delivery effect %d: %w", i, err)
		}
		if effect.Kind != DeliveryEffectAbsolute {
			return fmt.Errorf("bot commands delivery effect %d is not absolute", i)
		}
		if effect.ExcludeAuthKeyID != ([8]byte{}) || effect.ExcludeSessionID != 0 {
			return fmt.Errorf("bot commands delivery effect %d must not exclude a session", i)
		}
		if _, ok := want[effect.TargetUserID]; !ok {
			return fmt.Errorf("bot commands delivery effect %d has unexpected target", i)
		}
		if _, duplicate := seen[effect.TargetUserID]; duplicate {
			return fmt.Errorf("bot commands delivery target %d is duplicated", effect.TargetUserID)
		}
		seen[effect.TargetUserID] = struct{}{}
	}
	return nil
}
