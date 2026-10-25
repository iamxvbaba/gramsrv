package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// BotAPIAvailableGifts implements the HTTP Bot API getAvailableGifts method on
// top of the same catalog MTProto payments.getStarGifts serves, so a bot picking
// a gift id over HTTP picks an id the MTProto checkout would accept.
//
// Availability itself is not filtered here: the enabled catalog is returned
// as-is, including sold-out and not-yet-released gifts, because remaining_count
// is the signal a bot needs to tell "sold out" from "gone". The purchase
// transaction behind sendGift is where availability is enforced under a lock.
func (r *Router) BotAPIAvailableGifts(ctx context.Context) ([]domain.StarGift, error) {
	if r == nil || r.deps.Gifts == nil {
		return nil, errors.New("STAR_GIFT_UNAVAILABLE")
	}
	return r.deps.Gifts.Catalog(ctx)
}

// starGiftPurchaseFormTTLSeconds 固定表单有效期。StarGiftPurchaseForm 的校验要求
// ExpiresAt 恰为 IssuedAt+600（PostgreSQL 商店和内存适配器都强制这一点），所以这里
// 用同一个常量而不是散落的字面量 600。
const starGiftPurchaseFormTTLSeconds = 600

// botAPIStarGiftCommandKey 把 Bot API 的 Idempotency-Key/request_id 映射成星礼物付款
// 命令表的 command_key。前缀带 botID，因此两个 bot 使用同一个用户提供的键也不会互相
// 冲突；键只接受 Bot API 已校验过的字符集，长度上限 256 在 PurchaseStarGift 里再次
// 校验。
func botAPIStarGiftCommandKey(botID int64, requestID string) string {
	return fmt.Sprintf("botapi-stargift:%d:%s", botID, strings.TrimSpace(requestID))
}

// BotAPISendStarGift 实现 HTTP Bot API 的 sendGift：机器人用自身 Stars 余额购买并送出
// 星礼物，走的是和 MTProto payments.sendStarsForm 完全相同的目录校验、支付表单、
// 星礼物账本与权益事务。
//
// userID 与 chatID 是官方那对互斥的收礼人选择器，chat_id 的编码复用全站唯一的
// botAPIPeerFromChatID，避免 Bot API 层自己再抄一份 -100 偏移规则。
//
// 幂等语义与 giftPremiumSubscription 一致：同一个 request_id 重试同一个请求返回 true
// 且不再扣款；同一个键配不同的收礼人/礼物/升级/文本则报 IDEMPOTENCY_KEY_INVALID。
// 无键时处理器会生成随机键，所以每次调用都重新扣款——这与官方「无幂等」行为一致，
// 也不会把两个不同的请求误判为重放。
func (r *Router) BotAPISendStarGift(
	ctx context.Context,
	botID int64,
	giftID int64,
	userID int64,
	chatID int64,
	payForUpgrade bool,
	message domain.PremiumGiftMessage,
	requestID string,
) (bool, error) {
	if r == nil || r.deps.Gifts == nil || r.deps.Users == nil {
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", nil)
	}
	bot, found, err := r.deps.Users.ByID(ctx, botID, botID)
	if err != nil {
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	}
	if !found || !bot.Bot {
		return false, r.botAPIStarGiftErr(botID, "BOT_INVALID", nil)
	}
	// user_id 与 chat_id 必须恰好给一个（Bot API 层已校验，这里再兜一次底线）：
	// 两个都空就没有收礼人，两个都满就会把 chat_id 悄悄解释成收礼对象。
	to, valid := botAPIStarGiftPeer(userID, chatID)
	if !valid {
		return false, r.botAPIStarGiftErr(botID, "CHAT_ID_INVALID", nil)
	}
	if err := r.botAPIStarGiftRecipient(ctx, bot.ID, to); err != nil {
		return false, err
	}
	if !(domain.PremiumGiftMessage{Text: message.Text, Entities: message.Entities}).Valid() {
		return false, r.botAPIStarGiftErr(botID, "ENTITY_BOUNDS_INVALID", nil)
	}
	gift, found, err := r.deps.Gifts.GiftByID(ctx, giftID)
	if err != nil {
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	}
	if !found {
		return false, r.botAPIStarGiftErr(botID, "GIFT_NOT_AVAILABLE", nil)
	}
	now := int(r.clock.Now().Unix())
	switch {
	case gift.SoldOut, gift.Limited && gift.AvailabilityRemains <= 0,
		gift.LockedUntilDate > now, gift.Auction:
		// 这四种情况在 MTProto 侧走 payments.checkCanSendGift 的 fail 分支；这里
		// 统一成同一个错误，避免向机器人泄露「已售罄」与「尚未发布」的区别。
		return false, r.botAPIStarGiftErr(botID, "GIFT_NOT_AVAILABLE", nil)
	case gift.SupportOnly && !r.viewerSupport(ctx, bot.ID):
		return false, r.botAPIStarGiftErr(botID, "GIFT_NOT_AVAILABLE", nil)
	}
	// 机器人自己没有 Telegram Premium，所以 RequirePremium 礼物对 bot 基本不可用。
	// 这里传真实的会员状态，而不是硬编码 true 来绕过 prepareStarGiftPurchase 的
	// 同一道校验：PremiumGiftMessage 路径（giftPremiumSubscription）才是官方给
	// 机器人的开通方式。
	buyerPremium := r.viewerPremium(ctx, bot.ID)
	if gift.RequirePremium && !buyerPremium {
		return false, r.botAPIStarGiftErr(botID, "PREMIUM_ACCOUNT_REQUIRED", nil)
	}
	upgradeStars := int64(0)
	if payForUpgrade {
		if gift.UpgradeStars <= 0 || gift.UpgradeIssued >= gift.UpgradeTotal {
			return false, r.botAPIStarGiftErr(botID, "GIFT_UPGRADE_UNAVAILABLE", nil)
		}
		upgradeStars = gift.UpgradeStars
	}
	purchase := domain.StarGiftPurchaseRequest{
		BuyerUserID:  bot.ID,
		BuyerPremium: buyerPremium,
		// 机器人的 Stars 是它结算发票赚来的钱包余额（bot_stars_balances），不是它的
		// 个人余额：机器人这个用户身份在 stars_balances 里根本没有行。不标记的话，
		// 扣款会走个人账本并永远返回余额不足，钱包里的收入看起来像不存在。
		BuyerIsBot:      true,
		To:              to,
		GiftID:          gift.ID,
		RevisionID:      gift.RevisionID,
		IncludeUpgrade:  payForUpgrade,
		Message:         message.Text,
		MessageEntities: message.Entities,
		ChargeStars:     gift.Stars + upgradeStars,
		CommandKey:      botAPIStarGiftCommandKey(bot.ID, requestID),
		Date:            now,
		// OriginAuthKeyID 让账本把这笔钱记成 Bot API 来源，而不是某个 MTProto
		// 会话：机器人的 Stars 支出不应被算进任何用户的会话统计。
		// 这里不设 OriginAuthKeyID/OriginSessionID：这两个字段是 MTProto 会话身份，
		// 用来把 dispatch 排除对精确的发起会话（避免回声）。Bot API 没有 MTProto 会话，
		// 只填 auth key 而留 session 为 0 会构成「半对」，enqueueDispatch 直接以
		// errInvalidDispatchOutboxExclusionPair 拒绝整笔事务——sendGift 因此恒定 500。
		// 机器人的消息投递走 Bot API 队列，本来也不需要排除任何 MTProto 会话。
		// Bot API 来源的记账由 BuyerIsBot（钱包路由）承担，与这两个字段无关。
	}
	if to.Type == domain.PeerTypeUser {
		purchase.RecipientBlocked, err = r.peerBlocksUser(ctx, bot.ID, to.ID)
		if err != nil {
			return false, err
		}
		purchase.RecipientUnsaved, err = r.starGiftRecipientUnsaved(ctx, bot.ID, to)
		if err != nil {
			return false, err
		}
	}
	// 重放检查必须在签发表单之前：Bot API 的重试每次都会拿到一张新的 form_id，
	// 而 PurchaseStarGift 内部的重放比对要求 form_id 相同，所以在签发之后才查，
	// 重试会被当成 amount/command 不匹配而报错。
	if settled, found, err := r.deps.Gifts.SettledStarGiftPurchase(ctx, purchase); err != nil {
		if errors.Is(err, domain.ErrStarGiftIdempotencyConflict) {
			return false, r.botAPIStarGiftErr(botID, "IDEMPOTENCY_KEY_INVALID", nil)
		}
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	} else if found {
		if settled.Duplicate {
			r.invalidateStarGiftOwner(to)
		}
		return true, nil
	}
	form, err := r.deps.Gifts.IssuePurchaseForm(ctx, domain.StarGiftPurchaseForm{
		BuyerUserID:     bot.ID,
		To:              to,
		GiftID:          gift.ID,
		RevisionID:      gift.RevisionID,
		IncludeUpgrade:  payForUpgrade,
		Message:         message.Text,
		MessageEntities: message.Entities,
		ChargeStars:     purchase.ChargeStars,
		IssuedAt:        now,
		ExpiresAt:       now + starGiftPurchaseFormTTLSeconds,
	})
	if err != nil {
		return false, r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	}
	purchase.FormID = form.FormID
	// v2: the purchase commits the buyer balance refresh (for a bot, its wallet)
	// as a durable delivery in the same transaction.
	if _, err := r.deps.Gifts.PurchaseWithDelivery(ctx, purchase, r.starGiftPurchaseDeliveryEffects(purchase)); err != nil {
		if errors.Is(err, domain.ErrStarGiftIdempotencyConflict) {
			return false, r.botAPIStarGiftErr(botID, "IDEMPOTENCY_KEY_INVALID", nil)
		}
		// 并发同键重试时，两个事务里只有一个能拿到 command_key；输的那个会先撞上
		// form_id 比对失败。此时付款其实已经成功，重新读一次命令表，把已经完成的
		// 发送如实报告成成功，而不是让机器人以为钱白花了。
		if _, found, replayErr := r.deps.Gifts.SettledStarGiftPurchase(ctx, purchase); replayErr == nil && found {
			r.invalidateStarGiftOwner(to)
			return true, nil
		}
		return false, r.botAPIStarGiftPurchaseErr(bot.ID, err)
	}
	r.invalidateStarGiftOwner(to)
	return true, nil
}

// botAPIStarGiftPeer 把官方那对互斥的 user_id/chat_id 解析成收礼 peer。
func botAPIStarGiftPeer(userID, chatID int64) (domain.Peer, bool) {
	if userID != 0 {
		return domain.Peer{Type: domain.PeerTypeUser, ID: userID}, true
	}
	if chatID == 0 {
		return domain.Peer{}, false
	}
	return botAPIPeerFromChatID(chatID)
}

// botAPIStarGiftRecipient 校验收礼目标。给用户送礼要排除机器人自己、其他机器人、
// 冻结（Deleted）账号和系统账号；给频道送礼必须显式校验机器人确实有该频道的访问权。
func (r *Router) botAPIStarGiftRecipient(ctx context.Context, botID int64, to domain.Peer) error {
	switch to.Type {
	case domain.PeerTypeUser:
		if to.ID <= 0 || to.ID == botID {
			return r.botAPIStarGiftErr(botID, "USER_ID_INVALID", nil)
		}
		recipient, found, err := r.deps.Users.ByID(ctx, botID, to.ID)
		if err != nil {
			return r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
		}
		if !found || recipient.ID <= 0 || recipient.Bot || recipient.Deleted || domain.IsSystemUserID(recipient.ID) {
			return r.botAPIStarGiftErr(botID, "USER_ID_INVALID", nil)
		}
		return nil
	case domain.PeerTypeChannel:
		if to.ID <= 0 || r.deps.Channels == nil {
			return r.botAPIStarGiftErr(botID, "CHAT_ID_INVALID", nil)
		}
		// 频道礼物走的是 appendStarGiftAdminLogTx + 通知，不经过普通发消息的权限
		// 校验，因此这里必须自己确认机器人能访问该频道；否则任何 bot 只要知道
		// channel_id 就能往陌生频道里刷礼物并扣自己的 Stars。
		if _, err := r.deps.Channels.ResolveChannel(ctx, botID, to.ID); err != nil {
			return r.botAPIStarGiftErr(botID, "CHAT_ID_INVALID", err)
		}
		return nil
	default:
		return r.botAPIStarGiftErr(botID, "CHAT_ID_INVALID", nil)
	}
}

// botAPIStarGiftPurchaseErr 把事务层的拒绝翻译成 Bot API 错误码。
func (r *Router) botAPIStarGiftPurchaseErr(botID int64, err error) error {
	switch {
	case errors.Is(err, domain.ErrStarsInsufficient):
		return r.botAPIStarGiftErr(botID, "BALANCE_TOO_LOW", nil)
	case errors.Is(err, domain.ErrPremiumRequired):
		return r.botAPIStarGiftErr(botID, "PREMIUM_ACCOUNT_REQUIRED", nil)
	case errors.Is(err, domain.ErrStarGiftCollectibleUnavailable),
		errors.Is(err, domain.ErrStarGiftCollectibleSoldOut):
		return r.botAPIStarGiftErr(botID, "GIFT_UPGRADE_UNAVAILABLE", nil)
	case errors.Is(err, domain.ErrStarGiftRecipientUnavailable):
		// 冻结账号无法接收任何礼物。官方没有对应错误码，用限制类错误最贴近语义。
		return r.botAPIStarGiftErr(botID, "USER_PRIVACY_RESTRICTED", nil)
	case errors.Is(err, domain.ErrStarGiftInvalid),
		errors.Is(err, domain.ErrStarGiftUnavailable),
		errors.Is(err, domain.ErrStarGiftFormExpired),
		errors.Is(err, domain.ErrStarGiftFormPurposeInvalid),
		errors.Is(err, domain.ErrStarGiftFormAmountMismatch):
		return r.botAPIStarGiftErr(botID, "GIFT_NOT_AVAILABLE", nil)
	case errors.Is(err, domain.ErrChannelInvalid),
		errors.Is(err, domain.ErrChannelPrivate),
		errors.Is(err, domain.ErrChannelUserBanned),
		errors.Is(err, domain.ErrChannelWriteForbidden),
		errors.Is(err, domain.ErrChannelAdminRequired):
		return r.botAPIStarGiftErr(botID, botAPIChannelSendErr(err).Error(), nil)
	default:
		return r.botAPIStarGiftErr(botID, "STAR_GIFT_UNAVAILABLE", err)
	}
}

// botAPIStarGiftErr 组装一个会被 apiErrorDescription 还原成 Bot API 错误码的错误。
// cause 非空时同时记日志：Bot API 只会把错误码回给机器人，内部原因会在这里丢掉。
func (r *Router) botAPIStarGiftErr(botID int64, code string, cause error) error {
	if cause != nil && r != nil && r.log != nil {
		r.log.Warn("bot api star gift request failed", zap.Int64("bot_user_id", botID),
			zap.String("code", code), zap.Error(cause))
	}
	return errors.New(code)
}
