package postgres

import (
	"context"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
)

func starGiftPurchaseTestEffects(result domain.StarGiftPurchaseResult) ([]storepkg.DeliveryEffect, error) {
	return []storepkg.DeliveryEffect{storepkg.AbsoluteDeliveryEffect(storepkg.DeliveryOutboxEnqueue{
		TargetUserID:   result.Balance.UserID,
		Payload:        []byte("star-gift-balance-test"),
		RecoveryPolicy: storepkg.OutboxRecoveryAbsoluteReload,
	})}, nil
}

func starGiftConvertTestEffects(result domain.StarGiftConvertResult) ([]storepkg.DeliveryEffect, error) {
	if result.Saved.Owner.Type == domain.PeerTypeChannel {
		return nil, nil
	}
	if result.Saved.ConvertStars <= 0 {
		return nil, nil
	}
	return []storepkg.DeliveryEffect{storepkg.AbsoluteDeliveryEffect(storepkg.DeliveryOutboxEnqueue{
		TargetUserID:   result.Saved.Owner.ID,
		Payload:        []byte("star-gift-convert-balance-test"),
		RecoveryPolicy: storepkg.OutboxRecoveryAbsoluteReload,
	})}, nil
}

func purchaseStarGiftForTest(ctx context.Context, lifecycle *StarGiftLifecycleStore, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, error) {
	return lifecycle.PurchaseStarGiftWithDelivery(ctx, req, starGiftPurchaseTestEffects)
}

func convertStarGiftForTest(ctx context.Context, lifecycle *StarGiftLifecycleStore, req domain.StarGiftConvertRequest) (domain.StarGiftConvertResult, error) {
	return lifecycle.ConvertStarGiftWithDelivery(ctx, req, starGiftConvertTestEffects)
}
