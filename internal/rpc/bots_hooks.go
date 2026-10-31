package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// This file implements the rpc callbacks for app/bots: the session-invalidation loop after a
// token revoke, the online updateBotCommands push after a command change, the updateStickerSets
// online hint after an @Stickers publish, and the transient streaming-draft push for @ChatBot.
// Wired after Router creation via botsService.SetRouterHooks / SetTextDraftPusher (see
// cmd/telesrv/main.go).

// maxBotCommandsPushPeers caps the push fan-out of a single command change (the bot's recent
// dialog peers). Offline/long-tail users beyond it get the new commands on the next getFullUser
// via the bot_info_version bump.
const maxBotCommandsPushPeers = 100

// RevokeBotSessions revokes all of the bot's logged-in sessions: it deletes every authorization
// row and force-disconnects online connections (the same ordering as the
// account.resetAuthorization kick loop).
func (r *Router) RevokeBotSessions(ctx context.Context, botUserID int64) error {
	if r.deps.Auth == nil || botUserID == 0 {
		return nil
	}
	deleted, err := r.deps.Auth.ResetAuthorizations(ctx, botUserID, [8]byte{})
	if err != nil {
		return err
	}
	for _, a := range deleted {
		r.revokeAuthKeySessions(a.AuthKeyID)
		if err := r.clearAuthKeyState(ctx, a.AuthKeyID); err != nil {
			r.log.Warn("revoke bot sessions: clear auth key state",
				zap.Int64("bot_user_id", botUserID), zap.Error(err))
		}
	}
	if len(deleted) > 0 {
		r.log.Info("revoked bot sessions", zap.Int64("bot_user_id", botUserID), zap.Int("count", len(deleted)))
	}
	return nil
}

// PushBotCommandsChanged pushes updateBotCommands to users who have a private-chat dialog with
// the bot and are online (peer = the bot's user peer, matching TDesktop/DrKLO consumption
// semantics). updateBotCommands has no pts/qts and is not part of getDifference -- offline users
// are covered by the bot_info_version bump done alongside the write (re-fetching commands on the
// next getFullUser).
//
// fire-and-forget: runs in its own goroutine (detached from the already-returned setBotCommands
// RPC ctx), otherwise GetDialogs + up to maxBotCommandsPushPeers best-effort pushes would drag
// the RPC response to the sum of congestion timeouts and hold the BotFather stripe lock across
// users. The push is a pure notification, and losses are covered by the version bump, so
// guaranteed delivery is not required. The fan-out is bounded: only the first
// maxBotCommandsPushPeers user peers from the bot's dialog list, then filtered by the online
// snapshot; the over-cap portion falls back to the version bump (a deliberate trade-off).
func (r *Router) PushBotCommandsChanged(ctx context.Context, botUserID int64, commands []domain.BotCommand) {
	if r.deps.Dialogs == nil || r.deps.Sessions == nil || botUserID == 0 {
		return
	}
	// Copy the command slice: the caller (service) may reuse the underlying array.
	cmds := append([]domain.BotCommand(nil), commands...)
	go r.pushBotCommandsChanged(context.WithoutCancel(ctx), botUserID, cmds)
}

// PushStickerSetsChanged pushes updateStickerSets to a single user's online sessions. This update
// has no pts and is not part of getDifference; the authoritative installed state is already
// written to user_sticker_sets, and offline clients rebuild it on the next
// messages.getAllStickers/messages.getEmojiStickers.
func (r *Router) PushStickerSetsChanged(ctx context.Context, userID int64, kind domain.StickerSetKind) {
	if userID == 0 {
		return
	}
	r.invalidateStickerCatalog(kind)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.log.Error("push sticker sets panicked", zap.Int64("user_id", userID), zap.Any("panic", rec))
			}
		}()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		r.pushStickerSetsUpdate(ctx, userID, kind)
	}()
}

// PushBotTextDraft pushes the built-in service bot's streaming text draft. The draft is a
// TDesktop-specific transient typing action, writing no message/dialog/pts/outbox; the ultimately
// recoverable fact is still carried by the ordinary bot message stored afterwards.
func (r *Router) PushBotTextDraft(ctx context.Context, botUserID, userID, randomID int64, text string) {
	if botUserID == 0 || userID == 0 || randomID == 0 || text == "" {
		return
	}
	r.pushUserMessageTransient(context.WithoutCancel(ctx), userID, "push bot text draft", &tg.UpdateShort{
		Update: &tg.UpdateUserTyping{
			UserID: botUserID,
			Action: &tg.SendMessageTextDraftAction{
				RandomID: randomID,
				Text:     tg.TextWithEntities{Text: text},
			},
		},
		Date: int(r.clock.Now().Unix()),
	})
}

func (r *Router) pushBotCommandsChanged(ctx context.Context, botUserID int64, commands []domain.BotCommand) {
	defer func() {
		if rec := recover(); rec != nil {
			r.log.Error("push bot commands panicked", zap.Int64("bot_user_id", botUserID), zap.Any("panic", rec))
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := r.deps.Dialogs.GetDialogs(ctx, botUserID, domain.DialogFilter{Limit: maxBotCommandsPushPeers})
	if err != nil {
		r.log.Warn("push bot commands: list bot dialogs", zap.Int64("bot_user_id", botUserID), zap.Error(err))
		return
	}
	candidates := make([]int64, 0, len(list.Dialogs))
	for _, dialog := range list.Dialogs {
		if dialog.Peer.Type == domain.PeerTypeUser && dialog.Peer.ID != 0 && dialog.Peer.ID != botUserID {
			candidates = append(candidates, dialog.Peer.ID)
		}
	}
	if len(candidates) == 0 {
		return
	}
	if provider, ok := r.deps.Sessions.(OnlineUserProvider); ok {
		candidates = provider.OnlineUserIDsForCandidates(candidates, maxBotCommandsPushPeers)
	}
	if len(candidates) == 0 {
		return
	}
	update := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateBotCommands{
			Peer:     &tg.PeerUser{UserID: botUserID},
			BotID:    botUserID,
			Commands: tgBotCommands(commands),
		}},
		Date: int(r.clock.Now().Unix()),
	}
	for _, userID := range candidates {
		r.pushUserMessage(ctx, userID, "push bot commands", update)
	}
}
