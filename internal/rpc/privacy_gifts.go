package rpc

import (
	"context"

	"telesrv/internal/domain"
)

// starGiftRecipientUnsaved evaluates privacyKeyStarGiftsAutoSave at the
// ownership-write boundary. The rule does not reject the gift: it decides
// whether an incoming user gift is displayed immediately (unsaved=false) or
// waits for the recipient's approval (unsaved=true).
func (r *Router) starGiftRecipientUnsaved(ctx context.Context, senderUserID int64, recipient domain.Peer) (bool, error) {
	if recipient.Type != domain.PeerTypeUser || recipient.ID == 0 ||
		senderUserID == 0 || senderUserID == recipient.ID || r.deps.Privacy == nil {
		return false, nil
	}
	allowed, err := r.deps.Privacy.CanSee(
		ctx,
		recipient.ID,
		senderUserID,
		domain.PrivacyKeyStarGiftsAutoSave,
	)
	if err != nil {
		return false, internalErr()
	}
	return !allowed, nil
}
