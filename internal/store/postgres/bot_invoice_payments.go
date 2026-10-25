package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var _ store.BotInvoicePayments = (*MessageStore)(nil)

func (s *MessageStore) CreateBotInvoiceMessage(ctx context.Context, invoice domain.BotInvoice, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	if req.SenderUserID != invoice.BotUserID || req.RecipientUserID != invoice.ChatID || invoice.ChatID <= 0 || req.RecipientBlocked {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceInvalid
	}
	return s.sendPrivateTextWithHooks(ctx, req, privateSendTxHooks{after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
		invoice.MessageID = sent.RecipientMessage.ID
		_, err := NewBotStore(tx).CreateBotInvoice(ctx, invoice)
		return err
	}})
}

func (s *MessageStore) PayBotInvoice(ctx context.Context, invoiceID, payerID int64, date int) (domain.SendPrivateTextResult, error) {
	if invoiceID <= 0 || payerID <= 0 || date <= 0 {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceInvalid
	}
	invoice, err := scanBotInvoice(s.db.QueryRow(ctx, `SELECT `+botInvoiceColumns+` FROM bot_invoices WHERE id=$1 AND chat_id=$2`, invoiceID, payerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceNotFound
	}
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	if invoice.Refunded {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceRefunded
	}
	if invoice.Paid && invoice.PayerID != payerID {
		return domain.SendPrivateTextResult{}, domain.ErrBotInvoiceInvalid
	}
	chargeID := fmt.Sprintf("botinv-%d", invoice.ID)
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("bot-invoice-payment:%d:%d", invoice.ID, payerID)))
	req := domain.SendPrivateTextRequest{
		SenderUserID: payerID, RecipientUserID: invoice.BotUserID, RandomID: lifecycleCommandRandomID("bot-invoice-payment", payerID, chargeID), Date: date,
		IdempotencyFingerprint: fingerprint[:],
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionPayment, Payment: &domain.MessagePaymentAction{Currency: invoice.Currency, TotalAmount: invoice.Amount, Payload: invoice.Payload, ChargeID: chargeID, ProviderChargeID: chargeID, Title: invoice.Title, Description: invoice.Description}}},
	}
	return s.sendPrivateTextWithHooks(ctx, req, privateSendTxHooks{
		before: func(ctx context.Context, tx pgx.Tx, _ *domain.SendPrivateTextRequest) error {
			locked, err := scanBotInvoice(tx.QueryRow(ctx, `SELECT `+botInvoiceColumns+` FROM bot_invoices WHERE id=$1 FOR UPDATE`, invoiceID))
			if err != nil {
				return err
			}
			if locked.Paid || locked.Refunded || locked.ChatID != payerID {
				return domain.ErrBotInvoiceSettled
			}
			peer := domain.Peer{Type: domain.PeerTypeUser, ID: invoice.BotUserID}
			if _, err := NewStarsStore(tx).Debit(ctx, payerID, invoice.Amount, domain.StarsReasonBotInvoice, peer, date, invoice.Title, ""); err != nil {
				return err
			}
			lifecycle := &StarGiftLifecycleStore{db: tx}
			if _, err := lifecycle.applyBotStarsWalletTx(ctx, tx, domain.BotStarsCredit{BotUserID: invoice.BotUserID, PayerUserID: payerID, Amount: invoice.Amount, Reason: domain.StarsReasonBotInvoice, InvoiceKey: chargeID, Date: date}); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE bot_invoices SET paid=true,charge_id=$2,payer_user_id=$3,paid_at=$4 WHERE id=$1`, invoiceID, chargeID, payerID, date)
			return err
		},
		after: func(ctx context.Context, tx pgx.Tx, sent domain.SendPrivateTextResult) error {
			_, _, err := NewBotAPIUpdateStore(tx).EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
				BotUserID: invoice.BotUserID, Kind: domain.BotAPIUpdateMessage, Peer: sent.RecipientMessage.Peer, MessageID: sent.RecipientMessage.ID, SourcePts: sent.RecipientEvent.Pts, Date: date,
			})
			return err
		},
	})
}

func (s *MessageStore) RefundBotInvoice(ctx context.Context, botID, payerID int64, chargeID string, date int) error {
	if botID <= 0 || payerID <= 0 || chargeID == "" || date <= 0 {
		return domain.ErrBotInvoiceInvalid
	}
	return withTx(ctx, s.db, "refund bot invoice", func(tx pgx.Tx) error {
		if err := lockUsersForUpdate(ctx, tx, botID, payerID); err != nil {
			return err
		}
		invoice, err := scanBotInvoice(tx.QueryRow(ctx, `SELECT `+botInvoiceColumns+` FROM bot_invoices WHERE charge_id=$1 AND bot_user_id=$2 AND payer_user_id=$3 FOR UPDATE`, chargeID, botID, payerID))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrBotInvoiceNotFound
		}
		if err != nil {
			return err
		}
		if !invoice.Paid {
			return domain.ErrBotInvoiceInvalid
		}
		if invoice.Refunded {
			return nil
		}
		lifecycle := &StarGiftLifecycleStore{db: tx}
		if _, err := lifecycle.applyBotStarsWalletTx(ctx, tx, domain.BotStarsCredit{BotUserID: botID, PayerUserID: payerID, Amount: -invoice.Amount, Reason: domain.StarsReasonBotRefund, InvoiceKey: chargeID + "-refund", Date: date}); err != nil {
			return err
		}
		if _, err := NewStarsStore(tx).Credit(ctx, payerID, invoice.Amount, domain.StarsReasonBotRefund, domain.Peer{Type: domain.PeerTypeUser, ID: botID}, date, invoice.Title, ""); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE bot_invoices SET refunded=true WHERE id=$1`, invoice.ID)
		return err
	})
}
