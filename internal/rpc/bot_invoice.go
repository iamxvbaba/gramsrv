package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Bot invoice settlement.
//
// payments.sendPaymentForm in this layer carries no purpose, so for
// inputInvoiceMessage the client never sends a price: the server resolves the
// amount it advertised from the stored invoice. That is why the row is the
// settlement authority rather than a convenience cache, and why settling is
// driven from the message id alone.

// BotAPISendInvoice implements the Bot API sendInvoice method: it stores an
// invoice message and records the price the server advertised.
//
// The invoice and both message boxes commit together. The invoice key includes
// the buyer because private message ids are scoped to the buyer's box.
func (r *Router) BotAPISendInvoice(
	ctx context.Context,
	botID, chatID int64,
	title, description, payload string,
	amount int64,
) (domain.Message, error) {
	if r == nil || r.deps.Messages == nil || r.deps.Bots == nil || botID == 0 {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	bot, found, err := r.deps.Users.ByID(ctx, botID, botID)
	if err != nil {
		return domain.Message{}, err
	}
	if !found || !bot.Bot {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	if amount <= 0 {
		return domain.Message{}, errors.New("AMOUNT_INVALID")
	}
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > domain.BotInvoiceMaxTitle {
		return domain.Message{}, errors.New("TITLE_INVALID")
	}
	if utf8.RuneCountInString(description) > domain.BotInvoiceMaxDescription {
		return domain.Message{}, errors.New("DESCRIPTION_INVALID")
	}
	if len(payload) > domain.BotInvoiceMaxPayload {
		return domain.Message{}, errors.New("PAYLOAD_INVALID")
	}
	// telesrv models no fiat checkout, so an invoice can only be settled in XTR.
	// Reject anything else here instead of storing a price nothing can pay.
	invoice := &domain.Invoice{
		Title: title, Description: description, AmountStars: amount, StartParam: payload,
	}
	if !invoice.Valid() {
		return domain.Message{}, errors.New("INVOICE_INVALID")
	}
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindInvoice, ProductInvoice: invoice}
	// The pay button is not part of the invoice media: it is a keyboardButtonBuy
	// in the message reply markup, which the client renders and dispatches through
	// the same handler as a callback. The built-in Premium bot attaches one for the
	// same reason; without it the card renders but nothing is payable.
	markup := &domain.MessageReplyMarkup{
		Type: domain.MessageReplyMarkupInline,
		Inline: [][]domain.MarkupButton{{{
			Type:  domain.MarkupButtonBuy,
			Text:  fmt.Sprintf("Оплатить %d Stars", amount),
			Style: domain.MarkupButtonStyleSuccess,
		}}},
	}

	now := int(r.clock.Now().Unix())
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	if peer.Type != domain.PeerTypeUser {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	payments, ok := r.deps.Bots.(store.BotInvoicePayments)
	if !ok {
		return domain.Message{}, notImplementedErr()
	}
	recipient, found, err := r.deps.Users.ByID(ctx, botID, peer.ID)
	if err != nil {
		return domain.Message{}, err
	}
	if !found || recipient.Deleted {
		return domain.Message{}, errors.New("USER_ID_INVALID")
	}
	result, err := payments.CreateBotInvoiceMessage(ctx, domain.BotInvoice{
		BotUserID: botID, ChatID: peer.ID, Title: title, Description: description, Amount: amount, Currency: domain.PremiumCurrencyStars, Payload: payload, Date: now,
	}, domain.SendPrivateTextRequest{SenderUserID: botID, RecipientUserID: peer.ID, RandomID: randomNonZeroInt64(), Media: media, ReplyMarkup: markup, Date: now})
	if err != nil {
		return domain.Message{}, invoiceStoreErr(err)
	}
	sent := result.SenderMessage

	return sent, nil
}

func invoiceStoreErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrBotInvoiceSettled):
		return errors.New("INVOICE_ALREADY_EXISTS")
	case errors.Is(err, domain.ErrBotInvoiceInvalid):
		return errors.New("INVOICE_INVALID")
	default:
		return err
	}
}

// botInvoiceForMessage resolves the invoice behind an inputInvoiceMessage peer
// and message id, for both the form and the settlement paths.
func (r *Router) botInvoiceForMessage(ctx context.Context, viewerID int64, peer tg.InputPeerClass, msgID int) (domain.BotInvoice, error) {
	if msgID <= 0 {
		return domain.BotInvoice{}, tgerr.New(400, "MESSAGE_ID_INVALID")
	}
	owner, err := r.checkedDomainPeerFromInputPeer(ctx, viewerID, peer)
	if err != nil {
		return domain.BotInvoice{}, err
	}
	if owner.Type != domain.PeerTypeUser {
		return domain.BotInvoice{}, errBotInvoiceNotFound
	}
	// Private message identifiers are scoped to the buyer's box, not the bot's.
	chatKey := viewerID
	botUserID := owner.ID

	invoice, found, err := r.deps.Bots.BotInvoiceByMessage(ctx, botUserID, chatKey, msgID)
	if err != nil {
		return domain.BotInvoice{}, err
	}
	if !found {
		// A miss here means the client referenced a message this server never
		// recorded, or keyed the chat differently. Log the exact lookup so the
		// next failure is diagnosable instead of collapsing into INVOICE_INVALID.
		if r.log != nil {
			r.log.Warn("bot invoice lookup miss",
				zap.Int64("bot_user_id", botUserID),
				zap.Int64("chat_key", chatKey),
				zap.Int("msg_id", msgID),
				zap.Int64("viewer_id", viewerID),
				zap.String("peer_type", string(owner.Type)),
			)
		}
		// Not a bot invoice: the caller falls back to the Premium handler, which
		// owns the built-in bot's own invoices.
		return domain.BotInvoice{}, errBotInvoiceNotFound
	}
	return invoice, nil
}

// errBotInvoiceNotFound marks "this message is not a bot invoice" so the
// getPaymentForm dispatch can fall through instead of failing the request.
var errBotInvoiceNotFound = errors.New("bot invoice not found")

func isInvoiceLookupMiss(err error) bool { return errors.Is(err, errBotInvoiceNotFound) }

// botInvoicePaymentForm answers payments.getPaymentForm for a bot invoice.
//
// The shape must be paymentFormStars: TDesktop match()es on the constructor,
// and returning the generic paymentForm would make it open a card WebView
// instead of spending the buyer's existing Stars.
func (r *Router) botInvoicePaymentForm(ctx context.Context, viewerID int64, peer tg.InputPeerClass, msgID int) (tg.PaymentsPaymentFormClass, error) {
	invoice, err := r.botInvoiceForMessage(ctx, viewerID, peer, msgID)
	if err != nil {
		return nil, err
	}
	if invoice.Paid {
		return nil, tgerr.New(400, "INVOICE_ALREADY_PAID")
	}
	if invoice.Refunded {
		return nil, tgerr.New(400, "INVOICE_REFUNDED")
	}
	bot, found, err := r.deps.Users.ByID(ctx, viewerID, invoice.BotUserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, tgerr.New(400, "INVOICE_INVALID")
	}
	users := tgUsersForViewer(viewerID, []domain.User{bot})
	formID := botInvoiceFormID(invoice)
	return &tg.PaymentsPaymentFormStars{
		FormID:      formID,
		BotID:       invoice.BotUserID,
		Title:       invoice.Title,
		Description: invoice.Description,
		Invoice: tg.Invoice{
			Currency: domain.PremiumCurrencyStars,
			Prices: []tg.LabeledPrice{{
				Label:  invoice.Title,
				Amount: invoice.Amount,
			}},
		},
		Users: users,
	}, nil
}

// botInvoiceFormID derives a stable, non-zero form id from the invoice so a
// repeated getPaymentForm returns the same id the settle path will accept.
func botInvoiceFormID(invoice domain.BotInvoice) int64 {
	return botInvoiceFormIDBase + invoice.ID
}

// botInvoiceFormIDBase keeps generated form ids clear of the real Telegram
// namespace and non-zero, which the client treats as an invalid form.
const botInvoiceFormIDBase int64 = 0x424F5400

// botInvoiceFromFormID reverses botInvoiceFormID, rejecting anything that did not
// come from this generator.
func botInvoiceFromFormID(formID int64) (int64, bool) {
	id := formID - botInvoiceFormIDBase
	if id <= 0 {
		return 0, false
	}
	return id, true
}
