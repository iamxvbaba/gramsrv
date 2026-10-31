package store

import (
	"context"

	"telesrv/internal/domain"
)

// BotStore persists bot account metadata (bots table) and built-in bot chat state (bot_chat_states table).
//
// CreateBotAccount must atomically create the users row (is_bot=true, bot_info_version=1, phone empty)
// and the bots row; a username conflict returns domain.ErrUsernameOccupied.
type BotStore interface {
	CreateBotAccount(ctx context.Context, user domain.User, profile domain.BotProfile) (domain.User, domain.BotProfile, error)
	GetBot(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error)
	ListBotsByOwner(ctx context.Context, ownerUserID int64) ([]domain.BotProfile, error)
	CountBotsByOwner(ctx context.Context, ownerUserID int64) (int, error)
	// UpdateBotTokenSecret writes a new random token segment; the old token is invalidated immediately (no cache layer).
	UpdateBotTokenSecret(ctx context.Context, botUserID int64, secret string) error

	// The following P2 metadata writes: each one bumps the bot's corresponding users row
	// bot_info_version within a single transaction (clients notice the change this way and
	// re-fetch getFullUser) and returns the new bumped version. UpdateBotInfo spans both the
	// users (first_name/about) and bots (description) tables, so it lives in one atomic method.
	UpdateBotCommands(ctx context.Context, botUserID int64, commands []domain.BotCommand) (newVersion int, err error)
	UpdateBotInfo(ctx context.Context, botUserID int64, upd domain.BotInfoUpdate) (newVersion int, err error)
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

	// Bot invoices. CreateBotInvoice records the sale right after the invoice
	// message is stored, so the payment form can resolve the price later.
	CreateBotInvoice(ctx context.Context, invoice domain.BotInvoice) (domain.BotInvoice, error)
	// BotInvoiceByMessage resolves the invoice behind an inputInvoiceMessage.
	BotInvoiceByMessage(ctx context.Context, botUserID, chatID int64, messageID int) (domain.BotInvoice, bool, error)

	// BotInvoiceByCharge resolves an invoice by telegram_payment_charge_id without
	// changing it, so the caller can check ownership before acting.
	BotInvoiceByCharge(ctx context.Context, chargeID string) (domain.BotInvoice, bool, error)

	GetAttachMenuBot(ctx context.Context, botUserID int64) (domain.BotAttachMenuBot, bool, error)
	ListAttachMenuBots(ctx context.Context) ([]domain.BotAttachMenuBot, error)
	GetAttachMenuState(ctx context.Context, userID, botUserID int64) (domain.BotAttachMenuState, bool, error)
	SetAttachMenuState(ctx context.Context, state domain.BotAttachMenuState) (domain.BotAttachMenuState, error)

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
