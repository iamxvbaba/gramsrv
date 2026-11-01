package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// This file implements the rpc callbacks for app/bots: closing out sessions after a token
// revoke, the durable non-PTS effects for bot commands / sticker sets, and @ChatBot's transient
// streaming-draft pushes. They are wired after the Router is created through
// botsService.SetRouterHooks / SetTextDraftPusher (see internal/node/core/runtime.go).

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

// BotLifecycleDeliveryEffects projects the created/deleted bot user as an
// absolute updateUser for the frozen owner. The store validates that the
// builder emits exactly that one owner-targeted effect.
func (r *Router) BotLifecycleDeliveryEffects(_ context.Context) store.DeliveryEffectsBuilder[store.BotLifecycleDeliverySnapshot] {
	return func(snapshot store.BotLifecycleDeliverySnapshot) ([]store.DeliveryEffect, error) {
		payload, err := encodeDeliveryUpdate(&tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: snapshot.Bot.ID}},
			// The builder runs while the BotStore aggregate lock/transaction is
			// open. Use the pure domain->TL projector: consulting BotInfo here
			// would self-deadlock in memory and miss the uncommitted PG row.
			Users: []tg.UserClass{tgUser(snapshot.Bot)},
			Date:  int(r.clock.Now().Unix()),
		})
		if err != nil {
			return nil, err
		}
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: snapshot.OwnerUserID, Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
}

// BotInfoDeliveryEffects is executed inside the bot-info aggregate after its
// owner/private-dialog viewer audience has been frozen.
func (r *Router) BotInfoDeliveryEffects(_ context.Context) store.DeliveryEffectsBuilder[store.UserAudienceDeliverySnapshot] {
	return r.UserAudienceDeliveryEffects
}

// BotCommandsDeliveryEffects projects one immutable updateBotCommands payload
// per viewer frozen by BotStore. BotFather/owner sessions are not necessarily
// viewers, so this absolute notification deliberately carries no session
// exclusion.
func (r *Router) BotCommandsDeliveryEffects(_ context.Context) store.DeliveryEffectsBuilder[store.BotCommandsDeliverySnapshot] {
	return func(snapshot store.BotCommandsDeliverySnapshot) ([]store.DeliveryEffect, error) {
		if len(snapshot.Audience) == 0 {
			return nil, nil
		}
		payload, err := encodeDeliveryUpdate(&tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateBotCommands{
				Peer:     &tg.PeerUser{UserID: snapshot.BotUserID},
				BotID:    snapshot.BotUserID,
				Commands: tgBotCommands(snapshot.Commands),
			}},
			Date: int(r.clock.Now().Unix()),
		})
		if err != nil {
			return nil, err
		}
		effects := make([]store.DeliveryEffect, len(snapshot.Audience))
		for i, viewerUserID := range snapshot.Audience {
			// All effects share one immutable byte snapshot until the store's
			// set-based INSERT encodes its parameter arrays; avoid cloning the
			// same commands payload once per viewer in the transaction hot path.
			effects[i] = store.DeliveryEffect{
				Kind: store.DeliveryEffectAbsolute, TargetUserID: viewerUserID,
				Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			}
		}
		return effects, nil
	}
}

// StickerSetDeliveryEffects is the TL projection boundary used by the built-in
// @Stickers service. The returned builder is executed by the media store while
// its mutation transaction is open; it never starts an asynchronous push.
func (r *Router) StickerSetDeliveryEffects(ctx context.Context, userID int64) store.DeliveryEffectsBuilder[store.StickerSetMutation] {
	if userID <= 0 || r.deps.DeliveryOutbox == nil {
		return nil
	}
	return r.stickerSetMutationDeliveryEffects(ctx, userID)
}

// InvalidateStickerSetCatalog runs synchronously after the media transaction
// commits. Keeping cache invalidation out of the effect builder prevents a
// concurrent read from repopulating the old catalog before commit.
func (r *Router) InvalidateStickerSetCatalog(kind domain.StickerSetKind) {
	r.invalidateStickerCatalog(kind)
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
