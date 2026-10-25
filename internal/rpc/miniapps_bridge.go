package rpc

import (
	"context"
	"errors"

	"telesrv/internal/domain"
)

var errMiniAppServiceUnavailable = errors.New("mini app service is not configured")

// The public Web BotFather and Stickers Mini Apps (internal/web) write through
// these bridges so their mutations take exactly the durable delivery paths of
// the MTProto RPCs: bot creation commits its updateUser fanout, bot info its
// audience delivery and a sticker set the owner's updateStickerSets.

// MiniAppCreateBot creates a bot owned by ownerUserID, as /newbot does.
func (r *Router) MiniAppCreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error) {
	if r == nil || r.deps.Bots == nil {
		return domain.User{}, "", errMiniAppServiceUnavailable
	}
	return r.deps.Bots.CreateBotWithDelivery(ctx, ownerUserID, name, username, r.BotLifecycleDeliveryEffects(ctx))
}

// MiniAppSetBotInfo updates a bot's profile text, as bots.setBotInfo does.
func (r *Router) MiniAppSetBotInfo(ctx context.Context, botUserID int64, upd domain.BotInfoUpdate) (int, error) {
	if r == nil || r.deps.Bots == nil {
		return 0, errMiniAppServiceUnavailable
	}
	return r.deps.Bots.SetBotInfoWithDelivery(ctx, botUserID, upd, r.BotInfoDeliveryEffects(ctx))
}

// MiniAppCreateStickerSet creates and installs a user's sticker set, as
// stickers.createStickerSet does.
func (r *Router) MiniAppCreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error) {
	if r == nil || r.deps.Files == nil {
		return domain.StickerSet{}, nil, errMiniAppServiceUnavailable
	}
	if err := r.requireAccountDelivery(req.CreatorUserID, "miniapps.createStickerSet"); err != nil {
		return domain.StickerSet{}, nil, err
	}
	set, docs, err := r.deps.Files.CreateInstalledStickerSet(ctx, req, r.stickerSetMutationDeliveryEffects(ctx, req.CreatorUserID))
	if err != nil {
		return domain.StickerSet{}, nil, err
	}
	r.invalidateStickerCatalog(userStickerSetKind(set))
	return set, docs, nil
}
