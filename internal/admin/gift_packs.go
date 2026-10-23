package admin

import (
	"context"
	"hash/fnv"

	"telesrv/internal/domain"
)

// Granted Stars and Premium arrive the way Telegram delivers a gift: a
// "pack" service message (messageActionGiftStars / messageActionGiftPremium)
// from the system notifications account, not just a silent ledger change.
// No money changed hands, so the price shown is in Stars (XTR).

type privateTextSender interface {
	SendPrivateText(ctx context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error)
}

// giftPackRandomID derives random_id from the command, so a replayed command
// is deduplicated by the send pipeline instead of stacking a second pack.
func giftPackRandomID(kind, commandID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(kind + ":" + commandID))
	id := int64(h.Sum64() & 0x7fffffffffffffff)
	if id == 0 {
		id = 1
	}
	return id
}

func (s *Service) sendGiftPack(ctx context.Context, recipientUserID int64, kind, commandID string, action *domain.MessageServiceAction) error {
	sender, ok := s.messages.(privateTextSender)
	if s.messages == nil || !ok || recipientUserID <= 0 {
		return nil
	}
	_, err := sender.SendPrivateText(ctx, domain.OfficialSystemUserID, domain.SendPrivateTextRequest{
		SenderUserID: domain.OfficialSystemUserID, RecipientUserID: recipientUserID,
		RandomID: giftPackRandomID(kind, commandID), Date: int(s.now().Unix()),
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: action},
	})
	return err
}

// sendStarsGiftPack posts the Stars pack. The credit already happened; a
// failed notice is reported in the command details, never rolled back.
func (s *Service) sendStarsGiftPack(ctx context.Context, recipientUserID, stars int64, commandID string, balanceAfter int64) error {
	if stars <= 0 {
		return nil
	}
	return s.sendGiftPack(ctx, recipientUserID, "stars", commandID, &domain.MessageServiceAction{
		Kind: domain.MessageServiceActionGiftStars,
		GiftStars: &domain.MessageGiftStarsAction{Currency: "XTR", Amount: stars, Stars: stars,
			TransactionID: "admin-stars-grant:" + commandID, BalanceAfter: balanceAfter},
	})
}

// sendPremiumGiftPack posts the Premium gift. amountStars is the plan's
// price; without a plan it falls back to one Star per day so the client,
// which requires a positive amount, still renders the gift.
func (s *Service) sendPremiumGiftPack(ctx context.Context, recipientUserID int64, days int, amountStars int64, commandID string) error {
	if days <= 0 {
		return nil
	}
	if amountStars <= 0 {
		amountStars = int64(days)
	}
	return s.sendGiftPack(ctx, recipientUserID, "premium", commandID, &domain.MessageServiceAction{
		Kind:        domain.MessageServiceActionGiftPremium,
		GiftPremium: &domain.MessageGiftPremiumAction{Currency: "XTR", Amount: amountStars, Days: days},
	})
}
