package rpc

import (
	"context"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) applyTranslationDisabledToUserFull(ctx context.Context, viewerUserID, peerUserID int64, full *tg.UserFull) error {
	if full == nil || r.deps.Translation == nil || viewerUserID == 0 || peerUserID == 0 {
		return nil
	}
	disabled, err := r.deps.Translation.PeerDisabled(ctx, viewerUserID, domain.Peer{Type: domain.PeerTypeUser, ID: peerUserID})
	if err != nil {
		return internalErr()
	}
	full.SetTranslationsDisabled(disabled)
	return nil
}

func (r *Router) applyTranslationDisabledToChannelFull(ctx context.Context, viewerUserID, channelID int64, full *tg.ChannelFull) error {
	if full == nil || r.deps.Translation == nil || viewerUserID == 0 || channelID == 0 {
		return nil
	}
	disabled, err := r.deps.Translation.PeerDisabled(ctx, viewerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	if err != nil {
		return internalErr()
	}
	full.SetTranslationsDisabled(disabled)
	return nil
}
