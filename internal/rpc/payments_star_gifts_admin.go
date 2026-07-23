package rpc

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

type adminUniqueStarGiftGranter interface {
	GrantUnique(context.Context, domain.AdminStarGiftGrant) (domain.AdminStarGiftGrantResult, error)
}

// AdminGrantStarGift delivers a catalog gift to a recipient peer on behalf of
// grant.SenderID without charging any Stars. It powers the admin console "Give
// gift" action: the gift is loaded from the catalog and delivered through the
// exact same path a paid send uses (messageActionStarGift service message for
// users, saved-gift + admin log for channels), only the Stars debit is skipped.
//
// SenderID must be zero or the official system account (777000). When Upgrade
// is true, the store assigns a genuine collectible directly in the same
// transaction as its service message and durable updates. The optional
// ModelAttributeID / PatternAttributeID / BackdropAttributeID pin specific
// collectible facts (0 => random; number is always sequential). Upgraded
// delivery is supported for user recipients only.
func (r *Router) AdminGrantStarGift(ctx context.Context, grant domain.AdminStarGiftGrant) error {
	senderID := grant.SenderID
	if senderID <= 0 {
		senderID = domain.OfficialSystemUserID
	}
	if senderID != domain.OfficialSystemUserID {
		return fmt.Errorf("gift sender must be the official system account")
	}
	if grant.GiftID <= 0 {
		return fmt.Errorf("gift_id is required")
	}
	if grant.Recipient.ID <= 0 {
		return fmt.Errorf("recipient is required")
	}
	if r.deps.Gifts == nil {
		return fmt.Errorf("gifts dependency is not configured")
	}
	gift, ok, err := r.deps.Gifts.GiftByID(ctx, grant.GiftID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("gift %d not found", grant.GiftID)
	}
	if grant.Upgrade {
		return r.adminGrantUpgradedStarGift(ctx, senderID, gift, grant)
	}
	switch grant.Recipient.Type {
	case domain.PeerTypeUser:
		_, _, err = r.sendStarGiftToUser(ctx, senderID, grant.Recipient.ID, gift, grant.HideName, grant.Message, 0)
		return err
	case domain.PeerTypeChannel:
		_, _, err = r.sendStarGiftToChannel(ctx, senderID, grant.Recipient.ID, gift, grant.HideName, grant.Message, 0)
		return err
	default:
		return fmt.Errorf("unsupported recipient peer type %q", grant.Recipient.Type)
	}
}

// adminGrantUpgradedStarGift assigns a collectible through the atomic store
// boundary, so a failure cannot leave a regular gift, partial issuance, pts or
// outbox event behind.
func (r *Router) adminGrantUpgradedStarGift(ctx context.Context, senderID int64, gift domain.StarGift, grant domain.AdminStarGiftGrant) error {
	if grant.Recipient.Type != domain.PeerTypeUser {
		return fmt.Errorf("upgraded gift delivery is supported for user recipients only")
	}
	preview, found, err := r.deps.Gifts.CollectiblePreview(ctx, gift.ID)
	if err != nil {
		return err
	}
	if !found || preview.UpgradeStars <= 0 {
		return fmt.Errorf("gift %d has no published collectible upgrade", gift.ID)
	}
	if preview.Issued >= preview.SupplyTotal {
		return fmt.Errorf("gift %d collectible supply is exhausted", gift.ID)
	}
	granter, ok := r.deps.Gifts.(adminUniqueStarGiftGranter)
	if !ok {
		return fmt.Errorf("atomic collectible grant is not configured")
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, senderID, grant.Recipient.ID)
	if err != nil {
		return err
	}
	grant.SenderID = senderID
	grant.Date = int(r.clock.Now().Unix())
	grant.RecipientBlocked = recipientBlocked
	if _, err := granter.GrantUnique(ctx, grant); err != nil {
		return err
	}
	r.invalidateStarGiftOwnerProjection(grant.Recipient)
	return nil
}
