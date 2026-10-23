package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// PrivatePaidMessageCommissionPermille is the recipient's share of a paid
// private message, matching the stars_paid_message_commission_permille the
// clients are given.
const PrivatePaidMessageCommissionPermille int64 = 850

// debitPrivatePaidMessageTx charges the sender for one paid private message
// and credits the recipient's share. It runs inside the private-send
// transaction, so a failed send never keeps the money.
func debitPrivatePaidMessageTx(ctx context.Context, tx pgx.Tx, req *domain.SendPrivateTextRequest) error {
	stars := req.PaidMessageStars
	if stars <= 0 {
		return nil
	}
	var balance int64
	err := tx.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1 FOR UPDATE`, req.SenderUserID).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && balance < stars {
		return domain.ErrStarsInsufficient
	}
	if err != nil {
		return fmt.Errorf("lock paid message sender balance: %w", err)
	}
	if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance = balance - $2, updated_at = now() WHERE user_id = $1 RETURNING balance`,
		req.SenderUserID, stars).Scan(&balance); err != nil {
		return fmt.Errorf("debit paid message sender: %w", err)
	}
	req.PaidMessageSenderBalance = balance
	recipientPeer := domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}
	if err := insertStarsTxn(ctx, tx, req.SenderUserID, -stars, domain.StarsReasonPaidMessage, recipientPeer, req.Date, "", ""); err != nil {
		return err
	}
	credit := stars * PrivatePaidMessageCommissionPermille / 1000
	req.PaidMessageCredited = credit
	if credit <= 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO stars_balances (user_id, balance, granted, updated_at) VALUES ($1, $2, true, now())
ON CONFLICT (user_id) DO UPDATE SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()`,
		req.RecipientUserID, credit); err != nil {
		return fmt.Errorf("credit paid message recipient: %w", err)
	}
	senderPeer := domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}
	return insertStarsTxn(ctx, tx, req.RecipientUserID, credit, domain.StarsReasonPaidMessage, senderPeer, req.Date, "", "")
}

// recordPrivatePaidMessageTx stores the paid amount against the logical
// message once its id exists.
func recordPrivatePaidMessageTx(ctx context.Context, tx pgx.Tx, req domain.SendPrivateTextRequest, privateMessageID int64) error {
	if req.PaidMessageStars <= 0 || privateMessageID == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO private_paid_messages (payer_user_id, private_message_id, recipient_user_id, stars, credited, date)
VALUES ($2, $1, $3, $4, $5, $6)`,
		privateMessageID, req.SenderUserID, req.RecipientUserID, req.PaidMessageStars, req.PaidMessageCredited, req.Date); err != nil {
		return fmt.Errorf("record paid private message: %w", err)
	}
	return nil
}

// enrichPrivatePaidMessageStars fills Message.PaidMessageStars for read
// paths. A private message is keyed by its sender and per-sender id.
func enrichPrivatePaidMessageStars(ctx context.Context, db sqlcgen.DBTX, messages []domain.Message) error {
	senders := make([]int64, 0, len(messages))
	ids := make([]int64, 0, len(messages))
	for _, m := range messages {
		if m.UID != 0 && m.From.Type == domain.PeerTypeUser && m.From.ID != 0 {
			senders = append(senders, m.From.ID)
			ids = append(ids, m.UID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := db.Query(ctx, `
SELECT p.payer_user_id, p.private_message_id, p.stars
FROM unnest($1::bigint[], $2::bigint[]) AS w(sender_id, message_id)
JOIN private_paid_messages p ON p.payer_user_id = w.sender_id AND p.private_message_id = w.message_id`, senders, ids)
	if err != nil {
		return fmt.Errorf("load paid private messages: %w", err)
	}
	defer rows.Close()
	type key struct{ sender, id int64 }
	paid := make(map[key]int64)
	for rows.Next() {
		var k key
		var stars int64
		if err := rows.Scan(&k.sender, &k.id, &stars); err != nil {
			return err
		}
		paid[k] = stars
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(paid) == 0 {
		return nil
	}
	for i := range messages {
		if stars, ok := paid[key{messages[i].From.ID, messages[i].UID}]; ok {
			messages[i].PaidMessageStars = stars
		}
	}
	return nil
}

// PaidMessagesStore answers account.getPaidMessagesRevenue and performs the
// refund of account.toggleNoPaidMessagesException.
type PaidMessagesStore struct {
	db sqlcgen.DBTX
}

func NewPaidMessagesStore(db sqlcgen.DBTX) *PaidMessagesStore {
	return &PaidMessagesStore{db: db}
}

// PaidMessagesRevenue is the total, not refunded, payer spent on recipient.
func (s *PaidMessagesStore) PaidMessagesRevenue(ctx context.Context, recipientUserID, payerUserID int64) (int64, error) {
	var total int64
	err := s.db.QueryRow(ctx, `
SELECT COALESCE(sum(stars), 0) FROM private_paid_messages
WHERE recipient_user_id = $1 AND payer_user_id = $2 AND refunded_at IS NULL`, recipientUserID, payerUserID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("paid messages revenue: %w", err)
	}
	return total, nil
}

// RefundPaidMessages returns every unrefunded Star payer spent on recipient.
// The payer gets the full amount back; the recipient gives back what it was
// credited, as far as its balance allows.
func (s *PaidMessagesStore) RefundPaidMessages(ctx context.Context, recipientUserID, payerUserID int64, date int) (domain.PaidMessagesRefund, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.PaidMessagesRefund{}, fmt.Errorf("refund paid messages: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.PaidMessagesRefund{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockUsersForUpdate(ctx, tx, recipientUserID, payerUserID); err != nil {
		return domain.PaidMessagesRefund{}, err
	}
	var out domain.PaidMessagesRefund
	var credited int64
	if err := tx.QueryRow(ctx, `
WITH refunded AS (
    UPDATE private_paid_messages SET refunded_at = $3
    WHERE recipient_user_id = $1 AND payer_user_id = $2 AND refunded_at IS NULL
    RETURNING stars, credited
)
SELECT count(*), COALESCE(sum(stars), 0), COALESCE(sum(credited), 0) FROM refunded`,
		recipientUserID, payerUserID, date).Scan(&out.Count, &out.Stars, &credited); err != nil {
		return domain.PaidMessagesRefund{}, fmt.Errorf("mark paid messages refunded: %w", err)
	}
	if out.Count == 0 {
		return out, tx.Commit(ctx)
	}
	var recipientBalance int64
	err = tx.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1 FOR UPDATE`, recipientUserID).Scan(&recipientBalance)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.PaidMessagesRefund{}, err
	}
	takeBack := min(credited, max(recipientBalance, 0))
	if takeBack > 0 {
		if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance = balance - $2, updated_at = now() WHERE user_id = $1 RETURNING balance`,
			recipientUserID, takeBack).Scan(&out.RecipientBalance); err != nil {
			return domain.PaidMessagesRefund{}, err
		}
		if err := insertStarsTxn(ctx, tx, recipientUserID, -takeBack, domain.StarsReasonPaidMessage,
			domain.Peer{Type: domain.PeerTypeUser, ID: payerUserID}, date, "Refund", ""); err != nil {
			return domain.PaidMessagesRefund{}, err
		}
	} else {
		out.RecipientBalance = recipientBalance
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id, balance, granted, updated_at) VALUES ($1, $2, true, now())
ON CONFLICT (user_id) DO UPDATE SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()
RETURNING balance`, payerUserID, out.Stars).Scan(&out.PayerBalance); err != nil {
		return domain.PaidMessagesRefund{}, err
	}
	if err := insertStarsTxn(ctx, tx, payerUserID, out.Stars, domain.StarsReasonPaidMessage,
		domain.Peer{Type: domain.PeerTypeUser, ID: recipientUserID}, date, "Refund", ""); err != nil {
		return domain.PaidMessagesRefund{}, err
	}
	return out, tx.Commit(ctx)
}

// RecipientMessagedFirst reports whether recipient has ever written to
// sender: then sender may answer for free.
func (s *PaidMessagesStore) RecipientMessagedFirst(ctx context.Context, recipientUserID, senderUserID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM private_messages WHERE sender_user_id = $1 AND recipient_user_id = $2)`,
		recipientUserID, senderUserID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("recipient messaged first: %w", err)
	}
	return exists, nil
}

// chargeSupergroupPaidMessageTx charges a supergroup member for one message
// when the group charges for messages. Administrators, service messages and
// the group's own anonymous posts are free. The group gets the commission
// share on its Stars balance, like a paid channel direct message.
func chargeSupergroupPaidMessageTx(ctx context.Context, tx pgx.Tx, channel domain.Channel, member domain.ChannelMember, req domain.SendChannelMessageRequest) (int64, *domain.StarsBalance, error) {
	price := channel.SendPaidMessagesStars
	if price <= 0 || !channel.Megagroup || channel.Monoforum || req.Action != nil ||
		member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
		return 0, nil, nil
	}
	if req.SendAs != nil && req.SendAs.Type == domain.PeerTypeChannel && req.SendAs.ID == channel.ID {
		return 0, nil, nil
	}
	if req.AllowPaidStars < price {
		return 0, nil, &domain.StarsPaymentRequiredError{Stars: price}
	}
	balance := domain.StarsBalance{UserID: req.UserID}
	err := tx.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id = $1 FOR UPDATE`, req.UserID).
		Scan(&balance.Balance, &balance.Granted)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && balance.Balance < price {
		return 0, nil, domain.ErrStarsInsufficient
	}
	if err != nil {
		return 0, nil, fmt.Errorf("lock supergroup paid message balance: %w", err)
	}
	if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance = balance - $2, updated_at = now() WHERE user_id = $1 RETURNING balance`,
		req.UserID, price).Scan(&balance.Balance); err != nil {
		return 0, nil, fmt.Errorf("debit supergroup paid message: %w", err)
	}
	group := domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}
	if err := insertStarsTxn(ctx, tx, req.UserID, -price, domain.StarsReasonPaidMessage, group, req.Date, "", ""); err != nil {
		return 0, nil, err
	}
	if credit := price * PrivatePaidMessageCommissionPermille / 1000; credit > 0 {
		if _, err := tx.Exec(ctx, `
INSERT INTO channel_stars_balances (channel_id, balance) VALUES ($1, $2)
ON CONFLICT (channel_id) DO UPDATE SET balance = channel_stars_balances.balance + EXCLUDED.balance, updated_at = now()`,
			channel.ID, credit); err != nil {
			return 0, nil, fmt.Errorf("credit supergroup paid message: %w", err)
		}
	}
	return price, &balance, nil
}
