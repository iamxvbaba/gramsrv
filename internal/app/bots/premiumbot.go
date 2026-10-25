package bots

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

const premiumBotHelpText = `Покупка Telegram Premium за Stars.

/premium - доступные тарифы
/gift - подарить Premium другому пользователю
/status - проверить срок действия Premium
/history - последние покупки и подарки
/terms - условия покупки
/deposit - получить личный адрес для крипто-депозита
/help - показать это сообщение

Цена и длительность загружаются из каталога сервера. Перед оплатой цена
проверяется повторно, а списание Stars выполняется атомарно.`

const premiumBotHelpTextEN = `Buy Telegram Premium with Stars.

/premium - available plans
/gift - gift Premium to another user
/status - check your Premium expiry
/history - recent purchases and gifts
/terms - purchase terms
/deposit - get your personal crypto deposit address
/help - show this message

Prices and durations come from the server catalog. The price is checked again
before payment, and Stars are debited atomically.`

type premiumBotLocale string

const (
	premiumBotLocaleRU premiumBotLocale = "ru"
	premiumBotLocaleEN premiumBotLocale = "en"
)

func premiumLocaleFromSession(session domain.ClientSessionMetadata) premiumBotLocale {
	if session.PreferredLanguage() == "ru" {
		return premiumBotLocaleRU
	}
	if session.PreferredLanguage() == "" {
		// Preserve the historical default for legacy/internal calls without
		// initConnection metadata. Real Telegram sessions carry lang_code.
		return premiumBotLocaleRU
	}
	return premiumBotLocaleEN
}

func premiumLocaleArg(locales []premiumBotLocale) premiumBotLocale {
	if len(locales) > 0 && locales[0] != "" {
		return locales[0]
	}
	return premiumBotLocaleRU
}

// NotifyDonationCredited satisfies donations.CreditNotifier: it's called
// once per deposit the crypto donations watcher just credited (see
// cmd/telesrv, which wires donationsService.SetNotifier(botsService)), and
// sends the donor a @premiumbot message saying so. Without this wired up,
// crediting still happens exactly the same -- the user just finds out only
// by checking /deposit or their Stars balance themselves, never proactively.
func (s *Service) NotifyDonationCredited(ctx context.Context, notice domain.DonationCreditNotice) {
	if s == nil || notice.UserID <= 0 || notice.Deposit.StarsCredited <= 0 {
		return
	}
	mu := s.serviceBotReplyLock(domain.PremiumBotUserID, notice.UserID)
	mu.Lock()
	defer mu.Unlock()
	amount := formatAssetAmount(notice.Deposit.AmountRaw, notice.AssetDecimals)
	text := fmt.Sprintf("Your deposit of %s %s on %s was confirmed and credited: +%d %s.",
		amount, notice.AssetSymbol, notice.ChainName, notice.Deposit.StarsCredited, branding.StarsName())
	s.sendServiceBotReply(ctx, domain.PremiumBotUserID, notice.UserID, botReply{Text: text})
}

// formatAssetAmount renders a raw smallest-unit amount (wei, or an ERC-20's
// base units) as a human string with the asset's actual decimal point.
// Deliberately duplicated from (rather than imported off)
// app/donations.FormatAssetAmount: bots depends on donations only through
// the narrow donationsSource/CreditNotifier ports, matching every other
// cross-service dependency in this file, never the concrete package.
func formatAssetAmount(amountRaw string, decimals int) string {
	if decimals <= 0 || len(amountRaw) == 0 {
		return amountRaw
	}
	neg := strings.HasPrefix(amountRaw, "-")
	digits := strings.TrimPrefix(amountRaw, "-")
	for len(digits) <= decimals {
		digits = "0" + digits
	}
	intPart, fracPart := digits[:len(digits)-decimals], strings.TrimRight(digits[len(digits)-decimals:], "0")
	out := intPart
	if fracPart != "" {
		out += "." + fracPart
	}
	if neg {
		out = "-" + out
	}
	return out
}

func (locale premiumBotLocale) text(ru, en string) string {
	if locale == premiumBotLocaleRU {
		return ru
	}
	return en
}

func (locale premiumBotLocale) help() string {
	return locale.text(premiumBotHelpText, premiumBotHelpTextEN)
}

const premiumGiftPeerButtonID = 7101

const (
	premiumCallbackPrefix  = "premium:"
	premiumCallbackPlans   = premiumCallbackPrefix + "plans"
	premiumCallbackGift    = premiumCallbackPrefix + "gift"
	premiumCallbackStatus  = premiumCallbackPrefix + "status"
	premiumCallbackHistory = premiumCallbackPrefix + "history"
	premiumCallbackTerms   = premiumCallbackPrefix + "terms"
	premiumCallbackBuy     = premiumCallbackPrefix + "buy:"
	premiumCallbackGiftBuy = premiumCallbackPrefix + "gift-buy:"
)

func (s *Service) respondAsPremium(botUserID, userID int64, msg domain.Message, session domain.ClientSessionMetadata) {
	mu := s.serviceBotReplyLock(botUserID, userID)
	mu.Lock()
	defer mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	locale := premiumLocaleFromSession(session)
	s.sendServiceBotReply(ctx, botUserID, userID, s.handlePremiumMessage(ctx, userID, msg, locale))
}

func (s *Service) handlePremiumMessage(ctx context.Context, userID int64, msg domain.Message, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	if msg.Media != nil && msg.Media.ServiceAction != nil &&
		msg.Media.ServiceAction.Kind == domain.MessageServiceActionRequestedPeer {
		action := msg.Media.ServiceAction.RequestedPeer
		if action == nil || action.ButtonID != premiumGiftPeerButtonID ||
			len(action.Peers) != 1 || action.Peers[0].Type != domain.PeerTypeUser {
			return s.premiumBotGiftError(locale.text(
				"Выберите ровно одного обычного пользователя кнопкой «Выбрать получателя».",
				"Select exactly one regular user with the “Choose recipient” button."), locale)
		}
		return s.premiumBotGiftPlans(ctx, userID, action.Peers[0].ID, locale)
	}
	return s.handlePremium(ctx, userID, msg.Body, locale)
}

func (s *Service) handlePremium(ctx context.Context, userID int64, body string, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	command, argument := premiumBotCommand(body)
	switch command {
	case "start":
		if strings.HasPrefix(argument, "buy_") {
			months, err := strconv.Atoi(strings.TrimPrefix(argument, "buy_"))
			if err == nil {
				return s.premiumBotInvoice(ctx, userID, months, locale)
			}
		}
		switch argument {
		case "premium":
			return s.premiumBotPlans(ctx, locale.text("Выберите тариф Premium:", "Choose a Premium plan:"), locale)
		case "gift":
			return s.premiumBotGift(locale)
		case "status":
			return s.premiumBotStatus(ctx, userID, locale)
		case "history":
			return s.premiumBotHistory(ctx, userID, locale)
		case "terms":
			return premiumBotTerms(locale)
		default:
			// settings/profile/business/deeplink and feature identifiers are
			// navigation hints only. They never select a price or recipient.
			return s.premiumBotHome(locale.text("Добро пожаловать в магазин Premium.", "Welcome to the Premium store."), locale)
		}
	case "premium", "buy":
		if months, err := strconv.Atoi(argument); err == nil && months > 0 {
			return s.premiumBotInvoice(ctx, userID, months, locale)
		}
		return s.premiumBotPlans(ctx, locale.text("Выберите тариф Premium:", "Choose a Premium plan:"), locale)
	case "status":
		return s.premiumBotStatus(ctx, userID, locale)
	case "gift":
		return s.handlePremiumGiftCommand(ctx, userID, argument, locale)
	case "history":
		return s.premiumBotHistory(ctx, userID, locale)
	case "terms":
		return premiumBotTerms(locale)
	case "help", "":
		return s.premiumBotHome(locale.help(), locale)
	case "deposit":
		return s.premiumBotDepositReply(ctx, userID)
	default:
		return s.premiumBotHome(locale.text("Неизвестная команда.", "Unknown command.")+"\n\n"+locale.help(), locale)
	}
}

func premiumBotCommand(body string) (command, argument string) {
	fields := strings.Fields(strings.TrimSpace(body))
	if len(fields) == 0 {
		return "", ""
	}
	head := strings.TrimPrefix(fields[0], "/")
	if at := strings.IndexByte(head, '@'); at >= 0 {
		head = head[:at]
	}
	if len(fields) > 1 {
		argument = strings.Join(fields[1:], " ")
	}
	return strings.ToLower(head), argument
}

func (s *Service) premiumBotHome(text string, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	button := func(label, data string) domain.MarkupButton {
		return domain.MarkupButton{
			Type: domain.MarkupButtonCallback,
			Text: label,
			Data: []byte(data),
		}
	}
	return botReply{
		Text: text,
		ReplyMarkup: &domain.MessageReplyMarkup{
			Type: domain.MessageReplyMarkupInline,
			Inline: [][]domain.MarkupButton{
				{button(locale.text("Купить Premium", "Buy Premium"), premiumCallbackPlans), button(locale.text("Подарить Premium", "Gift Premium"), premiumCallbackGift)},
				{button(locale.text("Мой Premium", "My Premium"), premiumCallbackStatus), button(locale.text("История покупок", "Purchase history"), premiumCallbackHistory)},
				{button(locale.text("Условия", "Terms"), premiumCallbackTerms)},
			},
		},
	}
}

func (s *Service) premiumBotPlans(ctx context.Context, heading string, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	if s.premium == nil {
		return botReply{Text: locale.text("Покупка Premium временно недоступна.", "Premium purchases are temporarily unavailable.")}
	}
	plans, err := s.premium.Plans(ctx)
	if err != nil || len(plans) == 0 {
		return botReply{Text: locale.text("Тарифы Premium временно недоступны.", "Premium plans are temporarily unavailable.")}
	}
	var text strings.Builder
	text.WriteString(heading)
	text.WriteString("\n\n")
	rows := make([][]domain.MarkupButton, 0, len(plans))
	for _, plan := range plans {
		if !plan.Valid() || !plan.Enabled {
			continue
		}
		fmt.Fprintf(&text, "%s - %d Stars\n", plan.Label, plan.AmountStars)
		rows = append(rows, []domain.MarkupButton{{
			Type: domain.MarkupButtonCallback,
			Text: fmt.Sprintf("%s - %d Stars", plan.Label, plan.AmountStars),
			Data: []byte(premiumCallbackBuy + strconv.Itoa(plan.Months)),
		}})
	}
	if len(rows) == 0 {
		return botReply{Text: locale.text("Тарифы Premium временно недоступны.", "Premium plans are temporarily unavailable.")}
	}
	return botReply{
		Text: strings.TrimSpace(text.String()),
		ReplyMarkup: &domain.MessageReplyMarkup{
			Type:   domain.MessageReplyMarkupInline,
			Inline: rows,
		},
	}
}

// premiumBotDepositReply answers /deposit with the user's permanent crypto
// deposit address (the same address on every chain listed) and which
// chains it actually works on right now. The address is assigned once, on
// first call, and reused forever after -- see donations.Service.AddressForUser.
//
// It returns a full botReply rather than plain text because this server has
// no markdown parser on the send path (see service_bot_entities.go): the
// address must be marked up as a domain.MessageEntityCode span, the same
// way tokenReply does for BotFather tokens, not wrapped in literal
// backticks the client would show verbatim.
func (s *Service) premiumBotDepositReply(ctx context.Context, userID int64) botReply {
	if s.donations == nil || !s.donations.Ready() {
		return botReply{Text: "Crypto deposits are not available right now."}
	}
	chains, err := s.donations.EnabledChains(ctx)
	if err != nil {
		return botReply{Text: "Could not load deposit info right now. Please try again later."}
	}
	var live []string
	for _, c := range chains {
		if c.Watchable() {
			live = append(live, fmt.Sprintf("%s (%s)", c.Name, c.NativeSymbol))
		}
	}
	if len(live) == 0 {
		return botReply{Text: "Crypto deposits are not available right now -- no chain is configured yet."}
	}
	address, err := s.donations.AddressForUser(ctx, userID)
	if err != nil || address == "" {
		return botReply{Text: "Could not assign your deposit address right now. Please try again later."}
	}
	head := "Your personal crypto deposit address:\n\n"
	tail := "\n\nThis address works on: " + strings.Join(live, ", ") + ".\n\n" +
		"Send ETH, USDT or USDC to it from any wallet. Once your deposit reaches enough confirmations, it's automatically converted to " + branding.StarsName() + " and credited to your balance -- no further action needed.\n\n" +
		"This is the same address every time you check /deposit -- do not send funds on a chain not listed above, they will not be credited."
	return botReply{
		Text: head + address + tail,
		Entities: []domain.MessageEntity{
			// address is ASCII (0x + 40 lowercase hex chars), so byte length
			// and UTF-16 length coincide -- same assumption tokenReply makes.
			{Type: domain.MessageEntityCode, Offset: len(head), Length: len(address)},
		},
	}
}

func (s *Service) premiumBotInvoice(ctx context.Context, userID int64, months int, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	if s.premium == nil {
		return botReply{Text: locale.text("Покупка Premium временно недоступна.", "Premium purchases are temporarily unavailable.")}
	}
	plan, err := s.premium.Plan(ctx, months)
	if err != nil || !plan.Valid() || !plan.Enabled {
		return s.premiumBotPlans(ctx, locale.text(
			"Этот тариф больше недоступен. Выберите актуальный тариф:",
			"This plan is no longer available. Choose a current plan:"), locale)
	}
	title := fmt.Sprintf("Telegram Premium - %s", plan.Label)
	invoice := &domain.PremiumInvoice{
		Kind:         domain.PremiumPurchaseSelf,
		Months:       plan.Months,
		DurationDays: plan.DurationDays,
		AmountStars:  plan.AmountStars,
		PlanVersion:  plan.Version,
		Title:        title,
		Description: fmt.Sprintf(locale.text(
			"%d дней Telegram Premium за %d Stars.",
			"%d days of Telegram Premium for %d Stars."), plan.DurationDays, plan.AmountStars),
		StartParam: fmt.Sprintf("buy_%d", plan.Months),
	}
	balanceText := locale.text("недоступен", "unavailable")
	if balance, balanceErr := s.premium.Balance(ctx, userID); balanceErr == nil {
		balanceText = fmt.Sprintf("%d Stars", balance.Balance)
	}
	return botReply{
		Text:  fmt.Sprintf(locale.text("%s\nТекущий баланс: %s.", "%s\nCurrent balance: %s."), title, balanceText),
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindInvoice, Invoice: invoice},
		ReplyMarkup: &domain.MessageReplyMarkup{
			Type: domain.MessageReplyMarkupInline,
			Inline: [][]domain.MarkupButton{{{
				Type:  domain.MarkupButtonBuy,
				Text:  fmt.Sprintf(locale.text("Оплатить %d Stars", "Pay %d Stars"), plan.AmountStars),
				Style: domain.MarkupButtonStyleSuccess,
			}}},
		},
	}
}

func (s *Service) premiumBotGift(locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	filter := &domain.BotRequestPeerFilter{UserIsBotSet: true, UserIsBot: false}
	return botReply{
		Text: locale.text("Выберите пользователя, которому хотите подарить Telegram Premium.", "Choose a user to receive Telegram Premium."),
		ReplyMarkup: &domain.MessageReplyMarkup{
			Type:        domain.MessageReplyMarkupKeyboard,
			Resize:      true,
			SingleUse:   true,
			Placeholder: locale.text("Выберите получателя Premium", "Choose a Premium recipient"),
			Keyboard: [][]domain.MarkupButton{{{
				Type:              domain.MarkupButtonRequestPeer,
				Text:              locale.text("Выбрать получателя", "Choose recipient"),
				ButtonID:          premiumGiftPeerButtonID,
				RequestPeerType:   "user",
				MaxQuantity:       1,
				NameRequested:     true,
				UsernameRequested: true,
				RequestPeerFilter: filter,
			}}},
		},
	}
}

func (s *Service) premiumBotGiftError(text string, locales ...premiumBotLocale) botReply {
	reply := s.premiumBotGift(locales...)
	reply.Text = text
	return reply
}

func (s *Service) handlePremiumGiftCommand(ctx context.Context, userID int64, argument string, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	fields := strings.Fields(argument)
	if len(fields) == 0 {
		return s.premiumBotGift(locale)
	}
	recipientID, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || recipientID <= 0 {
		return s.premiumBotGiftError(locale.text("Получатель указан неверно. Выберите пользователя ещё раз.", "The recipient is invalid. Choose a user again."), locale)
	}
	if len(fields) == 1 {
		return s.premiumBotGiftPlans(ctx, userID, recipientID, locale)
	}
	months, err := strconv.Atoi(fields[1])
	if err != nil || months <= 0 {
		return s.premiumBotGiftPlans(ctx, userID, recipientID, locale)
	}
	message := strings.Join(fields[2:], " ")
	if len([]rune(message)) > domain.MaxPremiumGiftMessageRunes {
		return botReply{Text: fmt.Sprintf(locale.text(
			"Поздравление слишком длинное. Допустимо не более %d символов.",
			"The greeting is too long. The limit is %d characters."), domain.MaxPremiumGiftMessageRunes)}
	}
	return s.premiumBotGiftInvoice(ctx, userID, recipientID, months, message, locale)
}

func (s *Service) premiumBotGiftRecipient(
	ctx context.Context,
	buyerUserID, recipientUserID int64,
) (domain.User, bool) {
	if s == nil || s.users == nil || recipientUserID <= 0 || recipientUserID == buyerUserID ||
		domain.IsSystemUserID(recipientUserID) {
		return domain.User{}, false
	}
	user, found, err := s.users.ByID(ctx, recipientUserID)
	if err != nil || !found || user.Bot || user.Deleted {
		return domain.User{}, false
	}
	return user, true
}

func (s *Service) premiumBotGiftPlans(ctx context.Context, buyerUserID, recipientUserID int64, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	recipient, valid := s.premiumBotGiftRecipient(ctx, buyerUserID, recipientUserID)
	if !valid {
		return s.premiumBotGiftError(locale.text("Этому аккаунту нельзя подарить Premium. Выберите другого пользователя.", "Premium cannot be gifted to this account. Choose another user."), locale)
	}
	if s.premium == nil {
		return botReply{Text: locale.text("Подарки Premium временно недоступны.", "Premium gifts are temporarily unavailable.")}
	}
	plans, err := s.premium.Plans(ctx)
	if err != nil || len(plans) == 0 {
		return botReply{Text: locale.text("Тарифы Premium временно недоступны.", "Premium plans are temporarily unavailable.")}
	}
	name := strings.TrimSpace(recipient.FirstName + " " + recipient.LastName)
	if name == "" {
		name = fmt.Sprintf(locale.text("пользователь %d", "user %d"), recipient.ID)
	}
	rows := make([][]domain.MarkupButton, 0, len(plans))
	var text strings.Builder
	fmt.Fprintf(&text, locale.text("Подарить Telegram Premium пользователю %s:\n", "Gift Telegram Premium to %s:\n"), name)
	for _, plan := range plans {
		if !plan.Valid() || !plan.Enabled {
			continue
		}
		fmt.Fprintf(&text, "\n%s - %d Stars", plan.Label, plan.AmountStars)
		rows = append(rows, []domain.MarkupButton{{
			Type: domain.MarkupButtonCallback,
			Text: fmt.Sprintf("%s - %d Stars", plan.Label, plan.AmountStars),
			Data: []byte(fmt.Sprintf("%s%d:%d", premiumCallbackGiftBuy, recipient.ID, plan.Months)),
		}})
	}
	if len(rows) == 0 {
		return botReply{Text: locale.text("Тарифы Premium временно недоступны.", "Premium plans are temporarily unavailable.")}
	}
	return botReply{
		Text: text.String(),
		ReplyMarkup: &domain.MessageReplyMarkup{
			Type:   domain.MessageReplyMarkupInline,
			Inline: rows,
		},
	}
}

func (s *Service) premiumBotGiftInvoice(
	ctx context.Context,
	buyerUserID, recipientUserID int64,
	months int,
	greeting string,
	locales ...premiumBotLocale,
) botReply {
	locale := premiumLocaleArg(locales)
	recipient, valid := s.premiumBotGiftRecipient(ctx, buyerUserID, recipientUserID)
	if !valid {
		return s.premiumBotGiftError(locale.text("Этому аккаунту нельзя подарить Premium. Выберите другого пользователя.", "Premium cannot be gifted to this account. Choose another user."), locale)
	}
	if s.premium == nil {
		return botReply{Text: locale.text("Подарки Premium временно недоступны.", "Premium gifts are temporarily unavailable.")}
	}
	plan, err := s.premium.Plan(ctx, months)
	if err != nil || !plan.Valid() || !plan.Enabled {
		return s.premiumBotGiftPlans(ctx, buyerUserID, recipientUserID, locale)
	}
	name := strings.TrimSpace(recipient.FirstName + " " + recipient.LastName)
	if name == "" {
		name = fmt.Sprintf(locale.text("пользователь %d", "user %d"), recipient.ID)
	}
	title := fmt.Sprintf("Подарок Telegram Premium - %s", plan.Label)
	invoice := &domain.PremiumInvoice{
		Kind:            domain.PremiumPurchaseGift,
		RecipientUserID: recipient.ID,
		Months:          plan.Months,
		DurationDays:    plan.DurationDays,
		AmountStars:     plan.AmountStars,
		PlanVersion:     plan.Version,
		Title:           title,
		Description: fmt.Sprintf(locale.text(
			"%d дней Telegram Premium для %s за %d Stars.",
			"%d days of Telegram Premium for %s for %d Stars."), plan.DurationDays, name, plan.AmountStars),
		StartParam: fmt.Sprintf("gift_%d", plan.Months),
		Message:    domain.PremiumGiftMessage{Text: greeting},
	}
	balanceText := locale.text("недоступен", "unavailable")
	if balance, balanceErr := s.premium.Balance(ctx, buyerUserID); balanceErr == nil {
		balanceText = fmt.Sprintf("%d Stars", balance.Balance)
	}
	return botReply{
		Text: fmt.Sprintf(locale.text(
			"Подтвердите покупку тарифа %s для %s.\nЦена: %d Stars.\nТекущий баланс: %s.",
			"Confirm the %s plan for %s.\nPrice: %d Stars.\nCurrent balance: %s."), plan.Label, name, plan.AmountStars, balanceText),
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindInvoice, Invoice: invoice},
		ReplyMarkup: &domain.MessageReplyMarkup{
			Type: domain.MessageReplyMarkupInline,
			Inline: [][]domain.MarkupButton{{{
				Type:  domain.MarkupButtonBuy,
				Text:  fmt.Sprintf(locale.text("Оплатить %d Stars", "Pay %d Stars"), plan.AmountStars),
				Style: domain.MarkupButtonStyleSuccess,
			}}},
		},
	}
}

func (s *Service) premiumBotHistory(ctx context.Context, userID int64, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	if s.premium == nil {
		return botReply{Text: locale.text("История покупок Premium временно недоступна.", "Premium purchase history is temporarily unavailable.")}
	}
	items, err := s.premium.PurchaseHistory(ctx, userID, 10)
	if err != nil {
		return botReply{Text: locale.text("История покупок Premium временно недоступна.", "Premium purchase history is temporarily unavailable.")}
	}
	if len(items) == 0 {
		return botReply{Text: locale.text("У вас пока нет покупок или подарков Premium.", "You do not have any Premium purchases or gifts yet.")}
	}
	var text strings.Builder
	text.WriteString(locale.text("Последние покупки и подарки Premium:\n", "Recent Premium purchases and gifts:\n"))
	for _, item := range items {
		kind := locale.text("для себя", "for yourself")
		if item.UserID != userID {
			kind = fmt.Sprintf(locale.text("подарок пользователю %d", "gift to user %d"), item.UserID)
		} else if item.Source == domain.PremiumEntitlementGift && item.SourceUserID != userID {
			kind = fmt.Sprintf(locale.text("подарок от пользователя %d", "gift from user %d"), item.SourceUserID)
		}
		fmt.Fprintf(&text, locale.text("\n%d мес. - %s - %s - %s", "\n%d mo. - %s - %s - %s"), item.Months, kind, item.Status,
			time.Unix(int64(item.CreatedAt), 0).UTC().Format("2006-01-02"))
	}
	return botReply{Text: text.String()}
}

func premiumBotTerms(locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	return botReply{Text: locale.text(`Premium продаётся за Telegram Stars по цене, указанной в форме оплаты.

Новая покупка продлевает текущий срок Premium и никогда не сокращает его. Каждое успешное списание записывается в журнал Stars и создаёт отдельное право Premium. Возврат выполняется отдельной компенсирующей операцией; исходная транзакция не удаляется.`, `Premium is sold for Telegram Stars at the price shown on the payment form.

A new purchase extends the current Premium term and never shortens it. Every successful debit is recorded in the Stars ledger and creates a separate Premium entitlement. Refunds use a separate compensating operation; the original transaction is retained.`)}
}

func (s *Service) premiumBotStatus(ctx context.Context, userID int64, locales ...premiumBotLocale) botReply {
	locale := premiumLocaleArg(locales)
	if s.premium == nil {
		return botReply{Text: locale.text("Статус Premium временно недоступен.", "Premium status is temporarily unavailable.")}
	}
	now := int(s.now().Unix())
	entitlements, err := s.premium.ActiveEntitlements(ctx, userID, now)
	if err != nil {
		return botReply{Text: locale.text("Статус Premium временно недоступен.", "Premium status is temporarily unavailable.")}
	}
	var expiresAt int
	for _, entitlement := range entitlements {
		if entitlement.Status == domain.PremiumEntitlementActive && entitlement.ExpiresAt > expiresAt {
			expiresAt = entitlement.ExpiresAt
		}
	}
	if expiresAt <= now {
		return s.premiumBotPlans(ctx, locale.text("Premium не активен. Выберите тариф:", "Premium is not active. Choose a plan:"), locale)
	}
	return botReply{Text: fmt.Sprintf(locale.text("Premium активен до %s UTC.", "Premium is active until %s UTC."),
		time.Unix(int64(expiresAt), 0).UTC().Format("2006-01-02 15:04"))}
}

func (s *Service) onPremiumCallback(
	ctx context.Context,
	query domain.BotCallbackQuery,
) (domain.BotCallbackAnswer, bool, error) {
	userID := query.UserID
	locale := premiumLocaleFromSession(query.ClientSession)
	mu := s.serviceBotReplyLock(query.BotUserID, userID)
	mu.Lock()
	defer mu.Unlock()

	data := string(query.Data)
	var reply botReply
	switch data {
	case premiumCallbackPlans:
		reply = s.premiumBotPlans(ctx, locale.text("Выберите тариф Premium:", "Choose a Premium plan:"), locale)
	case premiumCallbackGift:
		reply = s.premiumBotGift(locale)
	case premiumCallbackStatus:
		reply = s.premiumBotStatus(ctx, userID, locale)
	case premiumCallbackHistory:
		reply = s.premiumBotHistory(ctx, userID, locale)
	case premiumCallbackTerms:
		reply = premiumBotTerms(locale)
	default:
		switch {
		case strings.HasPrefix(data, premiumCallbackBuy):
			months, err := strconv.Atoi(strings.TrimPrefix(data, premiumCallbackBuy))
			if err != nil || months <= 0 {
				return premiumBotCallbackExpired(locale), true, nil
			}
			reply = s.premiumBotInvoice(ctx, userID, months, locale)
		case strings.HasPrefix(data, premiumCallbackGiftBuy):
			payload := strings.TrimPrefix(data, premiumCallbackGiftBuy)
			recipientText, monthsText, ok := strings.Cut(payload, ":")
			recipientID, recipientErr := strconv.ParseInt(recipientText, 10, 64)
			months, monthsErr := strconv.Atoi(monthsText)
			if !ok || recipientErr != nil || monthsErr != nil || recipientID <= 0 || months <= 0 {
				return premiumBotCallbackExpired(locale), true, nil
			}
			reply = s.premiumBotGiftInvoice(ctx, userID, recipientID, months, "", locale)
		default:
			return premiumBotCallbackExpired(locale), true, nil
		}
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	s.sendServiceBotReply(sendCtx, query.BotUserID, userID, reply)
	return domain.BotCallbackAnswer{}, true, nil
}

func premiumBotCallbackExpired(locales ...premiumBotLocale) domain.BotCallbackAnswer {
	locale := premiumLocaleArg(locales)
	return domain.BotCallbackAnswer{
		Alert:   true,
		Message: locale.text("Кнопка устарела. Отправьте /start и попробуйте ещё раз.", "This button has expired. Send /start and try again."),
	}
}
