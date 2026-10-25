package memory

import (
	"context"
	"errors"
	"sync"

	"telesrv/internal/domain"
)

// In-memory bot invoices. Mirrors the postgres semantics that matter to callers:
// a message resolves to one invoice, settlement is a one-shot compare-and-set, and
// a charge id refunds at most once.

type botInvoiceKey struct {
	botUserID int64
	chatID    int64
	messageID int
}

type botInvoiceLedger struct {
	mu       sync.Mutex
	nextID   int64
	byMsg    map[botInvoiceKey]domain.BotInvoice
	byCharge map[string]botInvoiceKey
}

func newBotInvoiceLedger() *botInvoiceLedger {
	return &botInvoiceLedger{
		byMsg:    make(map[botInvoiceKey]domain.BotInvoice),
		byCharge: make(map[string]botInvoiceKey),
	}
}

func (l *botInvoiceLedger) create(invoice domain.BotInvoice) (domain.BotInvoice, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := botInvoiceKey{invoice.BotUserID, invoice.ChatID, invoice.MessageID}
	if _, exists := l.byMsg[key]; exists {
		return domain.BotInvoice{}, domain.ErrBotInvoiceSettled
	}
	l.nextID++
	invoice.ID = l.nextID
	l.byMsg[key] = invoice
	return invoice, nil
}

func (l *botInvoiceLedger) byMessage(botUserID, chatID int64, messageID int) (domain.BotInvoice, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	invoice, found := l.byMsg[botInvoiceKey{botUserID, chatID, messageID}]
	return invoice, found
}

func (l *botInvoiceLedger) byChargeID(chargeID string) (domain.BotInvoice, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key, found := l.byCharge[chargeID]
	if !found {
		return domain.BotInvoice{}, domain.ErrBotInvoiceNotFound
	}
	return l.byMsg[key], nil
}

func (s *BotStore) CreateBotInvoice(_ context.Context, invoice domain.BotInvoice) (domain.BotInvoice, error) {
	if !invoice.Valid() {
		return domain.BotInvoice{}, domain.ErrBotInvoiceInvalid
	}
	return s.invoices.create(invoice)
}

func (s *BotStore) BotInvoiceByMessage(_ context.Context, botUserID, chatID int64, messageID int) (domain.BotInvoice, bool, error) {
	if botUserID <= 0 || chatID == 0 || messageID <= 0 {
		return domain.BotInvoice{}, false, domain.ErrBotInvoiceInvalid
	}
	invoice, found := s.invoices.byMessage(botUserID, chatID, messageID)
	return invoice, found, nil
}

func (s *BotStore) BotInvoiceByCharge(_ context.Context, chargeID string) (domain.BotInvoice, bool, error) {
	if chargeID == "" {
		return domain.BotInvoice{}, false, domain.ErrBotInvoiceInvalid
	}
	invoice, err := s.invoices.byChargeID(chargeID)
	if errors.Is(err, domain.ErrBotInvoiceNotFound) {
		return domain.BotInvoice{}, false, domain.ErrBotInvoiceNotFound
	}
	if err != nil {
		return domain.BotInvoice{}, false, err
	}
	return invoice, true, nil
}
