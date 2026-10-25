package botapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// sendInvoice implements the Bot API sendInvoice method for a Stars invoice.
//
// Only currency="XTR" is accepted: telesrv models no fiat checkout, so an
// invoice in a real currency has nothing to settle against and is rejected
// rather than stored as an unpayable message.
func (h *handler) sendInvoice(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.invoices == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(values["chat_id"]), 10, 64)
	if err != nil || chatID == 0 {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ID_INVALID")
		return
	}
	currency := strings.TrimSpace(values["currency"])
	if currency == "" {
		currency = domain.PremiumCurrencyStars
	}
	if currency != domain.PremiumCurrencyStars {
		writeAPIError(w, http.StatusBadRequest, "CURRENCY_INVALID")
		return
	}
	// Telegram sends the XTR price either as a scalar `price` or as a one-entry
	// `prices` array, depending on the library.
	amount, err := invoiceAmount(values["price"], values["prices"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	msg, err := h.invoices.BotAPISendInvoice(r.Context(), botID, chatID,
		values["title"], values["description"], values["payload"], amount)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	users := []domain.User(nil)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	writeAPIOK(w, apiMessage(msg, users))
}

// refundStarPayment implements the Bot API refundStarPayment method. It reverses
// the exact Stars the charge credited, in the same transaction order as the
// original charge: the buyer is refunded and the bot wallet is debited.
//
// user_id is the payer per the Bot API contract. It is checked against the payer
// recorded on the invoice rather than trusted, so a bot cannot redirect its own
// refund into another account.
func (h *handler) refundStarPayment(w http.ResponseWriter, r *http.Request, botID int64) {
	if h.invoices == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(values["user_id"]), 10, 64)
	if err != nil || userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "USER_ID_INVALID")
		return
	}
	refunded, err := h.invoices.BotAPIRefundStarPayment(r.Context(), botID, userID, values["telegram_payment_charge_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, refunded)
}

// invoiceAmount reads the Stars total from either the scalar `price` field or
// the single-entry `prices` array. Anything else is a shape the gateway cannot
// price unambiguously, so it is refused instead of guessed.
func invoiceAmount(priceRaw, pricesRaw string) (int64, error) {
	if raw := strings.TrimSpace(priceRaw); raw != "" {
		amount, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || amount <= 0 {
			return 0, errInvoiceAmount
		}
		return amount, nil
	}
	raw := strings.TrimSpace(pricesRaw)
	if raw == "" {
		return 0, errInvoiceAmount
	}
	var prices []struct {
		Label  string `json:"label"`
		Amount int64  `json:"amount"`
	}
	if err := json.Unmarshal([]byte(raw), &prices); err != nil || len(prices) != 1 || prices[0].Amount <= 0 {
		return 0, errInvoiceAmount
	}
	return prices[0].Amount, nil
}

var errInvoiceAmount = invoiceAmountError{}

type invoiceAmountError struct{}

func (invoiceAmountError) Error() string { return "INVOICE_AMOUNT_INVALID" }
