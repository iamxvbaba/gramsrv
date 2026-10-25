package bots

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (s *Service) CreateBotInvoiceMessage(ctx context.Context, invoice domain.BotInvoice, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if s.blocker != nil {
		blocked, err := s.blocker.IsBlocked(ctx, invoice.ChatID, invoice.BotUserID)
		if err != nil {
			return domain.SendPrivateTextResult{}, err
		}
		if blocked {
			return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceInvalid
		}
	}
	st, ok := s.messages.(store.BotInvoicePayments)
	if !ok {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceInvalid
	}
	return st.CreateBotInvoiceMessage(ctx, invoice, req)
}
func (s *Service) PayBotInvoice(ctx context.Context, invoiceID, payerID int64, date int) (domain.SendPrivateTextResult, error) {
	st, ok := s.messages.(store.BotInvoicePayments)
	if !ok {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceInvalid
	}
	return st.PayBotInvoice(ctx, invoiceID, payerID, date)
}
func (s *Service) RefundBotInvoice(ctx context.Context, botID, payerID int64, chargeID string, date int) error {
	st, ok := s.messages.(store.BotInvoicePayments)
	if !ok {
		return domain.ErrBotInvoiceInvalid
	}
	return st.RefundBotInvoice(ctx, botID, payerID, chargeID, date)
}
