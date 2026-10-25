package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// Bot Stars revenue wallet.
//
// A bot's wallet is a distinct bucket from the bot user identity's personal
// Stars balance (`stars_balances`). The split is deliberate and mirrors the
// channel ledger: `payments.getStarsStatus` reports what a principal may spend,
// while `payments.getStarsRevenueStats` reports what a peer has earned. Mixing
// them would show an operator their own spending as revenue.
//
// Balance rows are never seeded with a starting grant. A bot earns stars only
// by settling an actual invoice, which is what CreditBotStarsWallet records.

// BotStarsBalance reads the current wallet balance. An unknown bot reads zero:
// absence of a row means the bot has never been paid.
func (s *StarGiftLifecycleStore) BotStarsBalance(ctx context.Context, botUserID int64) (int64, error) {
	if botUserID <= 0 {
		return 0, domain.ErrStarGiftOwnerInvalid
	}
	var balance int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM bot_stars_balances WHERE bot_user_id=$1),0)`, botUserID).Scan(&balance)
	return balance, err
}

// BotStarsOverallRevenue sums only positive entries so lifetime revenue stays
// monotonic: a refund or a later spend must never lower it.
func (s *StarGiftLifecycleStore) BotStarsOverallRevenue(ctx context.Context, botUserID int64) (int64, error) {
	if botUserID <= 0 {
		return 0, domain.ErrStarGiftOwnerInvalid
	}
	var revenue int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE(sum(amount) FILTER (WHERE amount>0),0)
FROM bot_stars_transactions WHERE bot_user_id=$1`, botUserID).Scan(&revenue)
	return revenue, err
}

// BotStarsTransactions returns one bounded page of wallet history together with
// the current balance, using the same keyset cursor as every other ledger.
func (s *StarGiftLifecycleStore) BotStarsTransactions(ctx context.Context, botUserID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error) {
	if botUserID <= 0 {
		return domain.StarsTransactionPage{}, domain.ErrStarGiftOwnerInvalid
	}
	query, err := domain.NormalizeStarsTransactionQuery(query)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	where, order, args := starsTransactionQueryParts("bot_user_id", "amount", botUserID, query)
	rows, err := s.db.Query(ctx, `SELECT id,bot_user_id,COALESCE(peer_type,''),COALESCE(peer_id,0),amount,date,reason
FROM bot_stars_transactions WHERE `+where+` ORDER BY id `+order+` LIMIT $2`, args...)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	defer rows.Close()
	items := make([]domain.StarsTransaction, 0, query.Limit+1)
	for rows.Next() {
		var item domain.StarsTransaction
		var peerType string
		if err := rows.Scan(&item.ID, &item.UserID, &peerType, &item.Peer.ID, &item.Amount, &item.Date, &item.Reason); err != nil {
			return domain.StarsTransactionPage{}, err
		}
		item.Peer.Type = domain.PeerType(peerType)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.StarsTransactionPage{}, err
	}
	page := domain.StarsTransactionPage{Transactions: items}
	if len(items) > query.Limit {
		items = items[:query.Limit]
		page.NextOffset = domain.EncodeStarsCursor(items[len(items)-1].ID)
	}
	page.Transactions = items
	page.Balance, err = s.BotStarsBalance(ctx, botUserID)
	return page, err
}

// CreditBotStarsWallet applies one settled invoice atomically: the balance row,
// the transaction entry and the idempotency receipt are written in one
// transaction, so a crash between them can never mint a partial credit.
//
// A replayed InvoiceKey is not an error. The stored receipt is returned with
// credited=false so payments.sendPaymentForm stays safe to retry after a
// network timeout.
func (s *StarGiftLifecycleStore) CreditBotStarsWallet(ctx context.Context, credit domain.BotStarsCredit) (int64, bool, error) {
	if !credit.Valid() {
		return 0, false, domain.ErrStarGiftLifecycleInvalid
	}
	var balance int64
	credited := false
	err := withTx(ctx, s.db, "credit bot stars wallet", func(tx pgx.Tx) error {
		stored, ok, err := botStarsReceipt(ctx, tx, credit.InvoiceKey)
		if err != nil {
			return err
		}
		if ok {
			balance = stored
			return nil
		}
		balance, err = s.applyBotStarsWalletTx(ctx, tx, credit)
		if err != nil {
			return err
		}
		credited = true
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return balance, credited, nil
}

// applyBotStarsWalletTx moves the wallet by the signed amount, appends the
// transaction entry and stores the idempotency receipt.
//
// The credit path upserts, and the debit path locks the row with FOR UPDATE, so
// two concurrent debits serialize and the balance>=0 CHECK rejects an overdraft
// instead of creating negative funds.
func (s *StarGiftLifecycleStore) applyBotStarsWalletTx(ctx context.Context, tx pgx.Tx, credit domain.BotStarsCredit) (int64, error) {
	var balance int64
	var err error
	if credit.Amount > 0 {
		balance, err = botStarsUpsertBalance(ctx, tx, credit.BotUserID, credit.Amount)
	} else {
		if err = tx.QueryRow(ctx, `SELECT balance FROM bot_stars_balances WHERE bot_user_id=$1 FOR UPDATE`,
			credit.BotUserID).Scan(&balance); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, domain.ErrStarsInsufficient
			}
			return 0, err
		}
		balance += credit.Amount
		if balance < 0 {
			return 0, domain.ErrStarsInsufficient
		}
		if _, err = tx.Exec(ctx, `UPDATE bot_stars_balances SET balance=$2,updated_at=now() WHERE bot_user_id=$1`,
			credit.BotUserID, balance); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_stars_transactions
		(bot_user_id,actor_user_id,amount,reason,peer_type,peer_id,invoice_key,date)
		VALUES($1,$2,$3,$4,'user',$5,$6,$7)`,
		credit.BotUserID, credit.PayerUserID, credit.Amount, string(credit.Reason),
		credit.PayerUserID, credit.InvoiceKey, credit.Date); err != nil {
		return 0, err
	}
	// The receipt stores the absolute moved amount: the sign belongs to the
	// transaction row, and the CHECK requires a positive magnitude.
	moved := credit.Amount
	if moved < 0 {
		moved = -moved
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bot_stars_payments
		(invoice_key,bot_user_id,payer_user_id,amount,balance_after,settled_at)
		VALUES($1,$2,$3,$4,$5,$6)`,
		credit.InvoiceKey, credit.BotUserID, credit.PayerUserID, moved, balance, credit.Date); err != nil {
		return 0, err
	}
	return balance, nil
}

// botStarsReceipt returns the stored post-credit balance for an
// already-settled invoice key, so a retry can be answered from the receipt
// without touching the mutable balance row.
// BotStarsRevenueSeries groups the wallet journal into UTC days over the
// inclusive window. Credits and debits are reported separately so the chart
// can stack income against spend the way the official earn screen does,
// instead of netting the two and hiding gross revenue.
func (s *StarGiftLifecycleStore) BotStarsRevenueSeries(ctx context.Context, botUserID int64, fromUnix, toUnix int) ([]domain.BotStarsRevenuePoint, error) {
	if botUserID <= 0 {
		return nil, domain.ErrStarGiftOwnerInvalid
	}
	if fromUnix <= 0 || toUnix < fromUnix {
		return nil, domain.ErrStarGiftLifecycleInvalid
	}
	rows, err := s.db.Query(ctx, `SELECT (date/86400)*86400 AS day_start,
		COALESCE(sum(amount) FILTER (WHERE amount>0),0) AS earned,
		COALESCE(-sum(amount) FILTER (WHERE amount<0),0) AS spent
FROM bot_stars_transactions
WHERE bot_user_id=$1 AND date>=$2 AND date<=$3
GROUP BY day_start ORDER BY day_start`, botUserID, fromUnix, toUnix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]domain.BotStarsRevenuePoint, 0, 32)
	for rows.Next() {
		var point domain.BotStarsRevenuePoint
		if err := rows.Scan(&point.DayStart, &point.Amount, &point.Spent); err != nil {
			return nil, err
		}
		if point.Valid() {
			points = append(points, point)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, nil
}

func botStarsReceipt(ctx context.Context, tx pgx.Tx, invoiceKey string) (balance int64, found bool, err error) {
	err = tx.QueryRow(ctx, `SELECT balance_after FROM bot_stars_payments WHERE invoice_key=$1`,
		invoiceKey).Scan(&balance)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return balance, true, nil
}

// botStarsUpsertBalance applies a signed delta and returns the new balance.
// The CHECK constraint is the final guard against a negative wallet.
func botStarsUpsertBalance(ctx context.Context, tx pgx.Tx, botUserID, delta int64) (int64, error) {
	var balance int64
	err := tx.QueryRow(ctx, `INSERT INTO bot_stars_balances(bot_user_id,balance) VALUES($1,$2)
		ON CONFLICT(bot_user_id) DO UPDATE SET balance=bot_stars_balances.balance+EXCLUDED.balance,updated_at=now()
		RETURNING balance`, botUserID, delta).Scan(&balance)
	return balance, err
}
