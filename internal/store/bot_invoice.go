package store

import (
	"context"
	"telesrv/internal/domain"
)

// BotInvoicePayments commits invoice state, both ledgers, messages and durable delivery together.
// There is no in-memory or multi-store fallback.
type BotInvoicePayments interface {
	CreateBotInvoiceMessage(context.Context, domain.BotInvoice, domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error)
	PayBotInvoice(context.Context, int64, int64, int) (domain.SendPrivateTextResult, error)
	RefundBotInvoice(context.Context, int64, int64, string, int) error
}
