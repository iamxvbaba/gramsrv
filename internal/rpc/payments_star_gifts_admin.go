package rpc

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
)

// AdminGrantStarGift delivers a catalog gift to a recipient peer on behalf of
// grant.SenderID without charging any Stars. It powers the admin console "Give
// gift" action: the gift is loaded from the catalog and delivered through the
// exact same path a paid send uses (messageActionStarGift service message for
// users, saved-gift + admin log for channels), only the Stars debit is skipped.
//
// When SenderID is zero the official system account (777000, the telesrv
// service account) is used as the sender. When Upgrade is true the granted gift
// is immediately upgraded to a genuine collectible (unique) gift. The optional
// ModelAttributeID / PatternAttributeID / BackdropAttributeID / Num pin specific
// collectible facts (0 => random model/pattern/backdrop, auto sequential
// number); the DB constraints remain the source of truth. Upgraded delivery is
// supported for user recipients only.
func (r *Router) AdminGrantStarGift(ctx context.Context, grant domain.AdminStarGiftGrant) error {
	senderID := grant.SenderID
	if senderID <= 0 {
		senderID = domain.OfficialSystemUserID
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

// adminGrantUpgradedStarGift grants a base gift carrying a prepaid upgrade
// entitlement and then mints the collectible via the standard zero-charge
// prepaid upgrade path, so the recipient ends up owning a real unique gift with
// the requested (or random) attributes and number.
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
	// Grant the base gift with a prepaid-upgrade entitlement so the upgrade
	// below runs on the zero-charge RequirePrepaid path.
	ref, _, err := r.sendStarGiftToUser(ctx, senderID, grant.Recipient.ID, gift, grant.HideName, grant.Message, preview.UpgradeStars)
	if err != nil {
		return err
	}
	commandKey := fmt.Sprintf("admin-grant-upgrade:%d:%d:%d", grant.Recipient.ID, gift.ID, ref.MsgID)
	if _, err := r.deps.Gifts.Upgrade(ctx, domain.StarGiftUpgradeRequest{
		UserID:              grant.Recipient.ID,
		Ref:                 ref,
		RequirePrepaid:      true,
		CommandKey:          commandKey,
		Date:                int(r.clock.Now().Unix()),
		ModelAttributeID:    grant.ModelAttributeID,
		PatternAttributeID:  grant.PatternAttributeID,
		BackdropAttributeID: grant.BackdropAttributeID,
		Num:                 grant.Num,
	}); err != nil {
		return err
	}
	r.invalidateStarGiftOwnerProjection(grant.Recipient)
	return nil
}
