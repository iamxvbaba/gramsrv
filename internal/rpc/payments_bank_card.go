package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// bankCardData implements payments.getBankCardData: after stripping the grouping
// spaces and hyphens it must be 13-19 digits and pass the Luhn check, otherwise
// it returns the official BANK_CARD_NUMBER_INVALID. title is the card scheme that
// TDLib distinguishes when recognizing a card number (domain.BankCardScheme),
// left empty rather than invented when it cannot be determined. telesrv is not
// integrated with any issuer, so open_urls is always empty; the client still
// offers to copy the card number (CopyCardNumber in DrKLO
// ChatActivity.didPressBankCard).
func bankCardData(number string) (*tg.PaymentsBankCardData, error) {
	digits, ok := domain.BankCardDigits(number)
	if !ok || len(digits) < domain.MinBankCardDigits || len(digits) > 19 || !domain.BankCardLuhnValid(digits) {
		return nil, tgerr.New(400, "BANK_CARD_NUMBER_INVALID")
	}
	return &tg.PaymentsBankCardData{Title: domain.BankCardScheme(digits), OpenURLs: []tg.BankCardOpenURL{}}, nil
}
