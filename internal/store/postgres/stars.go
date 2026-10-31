package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// StarsStore implements store.StarsStore with PostgreSQL (the local Stars ledger).
// Debit/credit/grant each complete in a single transaction: the balance and the ledger never drift.
type StarsStore struct {
	db sqlcgen.DBTX
}

// NewStarsStore creates a StarsStore on top of a pgx pool (or transaction).
func NewStarsStore(db sqlcgen.DBTX) *StarsStore {
	return &StarsStore{db: db}
}

func (s *StarsStore) GetBalance(ctx context.Context, userID int64) (domain.StarsBalance, error) {
	if userID == 0 {
		return domain.StarsBalance{}, nil
	}
	bal := domain.StarsBalance{UserID: userID}
	err := s.db.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id = $1`, userID).
		Scan(&bal.Balance, &bal.Granted)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StarsBalance{UserID: userID}, nil
		}
		return domain.StarsBalance{}, fmt.Errorf("get stars balance: %w", err)
	}
	return bal, nil
}

func (s *StarsStore) EnsureGrant(ctx context.Context, userID, amount int64, date int) (domain.StarsBalance, bool, error) {
	if userID == 0 {
		return domain.StarsBalance{}, false, nil
	}
	if amount <= 0 {
		// A zero grant amount only ensures a row exists with granted=true (idempotently disables the grant).
		bal, err := s.GetBalance(ctx, userID)
		return bal, false, err
	}
	out := domain.StarsBalance{UserID: userID}
	applied := false
	err := withTx(ctx, s.db, "ensure stars grant", func(tx pgx.Tx) error {
		var balance int64
		var granted bool
		err := tx.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id = $1 FOR UPDATE`, userID).
			Scan(&balance, &granted)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx, `INSERT INTO stars_balances (user_id, balance, granted, updated_at) VALUES ($1, $2, true, now())`,
				userID, amount); err != nil {
				return fmt.Errorf("insert stars balance grant: %w", err)
			}
			out.Balance, out.Granted, applied = amount, true, true
		case err != nil:
			return fmt.Errorf("select stars balance for grant: %w", err)
		case granted:
			out.Balance, out.Granted = balance, true
			return nil
		default:
			if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance = balance + $2, granted = true, updated_at = now() WHERE user_id = $1 RETURNING balance`,
				userID, amount).Scan(&out.Balance); err != nil {
				return fmt.Errorf("update stars balance grant: %w", err)
			}
			out.Granted, applied = true, true
		}
		return insertStarsTxn(ctx, tx, userID, amount, domain.StarsReasonGrant, domain.Peer{}, date, "", "")
	})
	if err != nil {
		return domain.StarsBalance{}, false, err
	}
	return out, applied, nil
}

func (s *StarsStore) Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error) {
	if userID == 0 || amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	out := domain.StarsBalance{UserID: userID}
	err := withTx(ctx, s.db, "credit stars", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id, balance, updated_at) VALUES ($1, $2, now())
ON CONFLICT (user_id) DO UPDATE SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()
RETURNING balance, granted`, userID, amount).Scan(&out.Balance, &out.Granted); err != nil {
			return fmt.Errorf("credit stars balance: %w", err)
		}
		return insertStarsTxn(ctx, tx, userID, amount, reason, peer, date, title, desc)
	})
	if err != nil {
		return domain.StarsBalance{}, err
	}
	return out, nil
}

func (s *StarsStore) Debit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) (domain.StarsBalance, error) {
	if userID == 0 || amount <= 0 {
		return domain.StarsBalance{}, domain.ErrStarsInvalidAmount
	}
	out := domain.StarsBalance{UserID: userID, Granted: true}
	err := withTx(ctx, s.db, "debit stars", func(tx pgx.Tx) error {
		var balance int64
		var granted bool
		err := tx.QueryRow(ctx, `SELECT balance, granted FROM stars_balances WHERE user_id = $1 FOR UPDATE`, userID).
			Scan(&balance, &granted)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && balance < amount) {
			return domain.ErrStarsInsufficient
		}
		if err != nil {
			return fmt.Errorf("select stars balance for debit: %w", err)
		}
		if err := tx.QueryRow(ctx, `UPDATE stars_balances SET balance = balance - $2, updated_at = now() WHERE user_id = $1 RETURNING balance`,
			userID, amount).Scan(&out.Balance); err != nil {
			return fmt.Errorf("update stars balance debit: %w", err)
		}
		out.Granted = granted
		return insertStarsTxn(ctx, tx, userID, -amount, reason, peer, date, title, desc)
	})
	if err != nil {
		return domain.StarsBalance{}, err
	}
	return out, nil
}

func (s *StarsStore) ListTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error) {
	if userID == 0 {
		return domain.StarsTransactionPage{}, nil
	}
	query, err := domain.NormalizeStarsTransactionQuery(query)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	bal, err := s.GetBalance(ctx, userID)
	if err != nil {
		return domain.StarsTransactionPage{}, err
	}
	page := domain.StarsTransactionPage{Balance: bal.Balance}

	// keyset: the direction filter precedes LIMIT, and one extra row is fetched to probe whether the same view has a next page.
	where, order, args := starsTransactionQueryParts("user_id", "amount", userID, query)
	rows, err := s.db.Query(ctx, `
SELECT id, peer_type, peer_id, amount, reason, title, description, date,
       COALESCE(premium_payment_intent_id, 0), COALESCE(premium_recipient_user_id, 0),
       COALESCE(premium_months, 0)
FROM stars_transactions
WHERE `+where+`
ORDER BY id `+order+`
LIMIT $2`, args...)
	if err != nil {
		return domain.StarsTransactionPage{}, fmt.Errorf("list stars transactions: %w", err)
	}
	defer rows.Close()
	txns := make([]domain.StarsTransaction, 0, query.Limit+1)
	for rows.Next() {
		var (
			t        domain.StarsTransaction
			peerType string
			peerID   int64
			reason   string
		)
		if err := rows.Scan(&t.ID, &peerType, &peerID, &t.Amount, &reason, &t.Title, &t.Description, &t.Date,
			&t.PaymentID, &t.RecipientUserID, &t.PremiumMonths); err != nil {
			return domain.StarsTransactionPage{}, fmt.Errorf("scan stars transaction: %w", err)
		}
		t.UserID = userID
		t.Reason = domain.StarsTransactionReason(reason)
		if peerType != "" {
			t.Peer = domain.Peer{Type: domain.PeerType(peerType), ID: peerID}
		}
		txns = append(txns, t)
	}
	if err := rows.Err(); err != nil {
		return domain.StarsTransactionPage{}, fmt.Errorf("iterate stars transactions: %w", err)
	}
	if len(txns) > query.Limit {
		txns = txns[:query.Limit]
		page.NextOffset = domain.EncodeStarsCursor(txns[len(txns)-1].ID)
	}
	page.Transactions = txns
	return page, nil
}

// chargePaidMessageStarsTx debits a private paid message from the sender and
// credits the recipient the same 85% share as channel paid messages, inside the
// caller's send transaction, with a paid_message ledger row on each side.
func chargePaidMessageStarsTx(ctx context.Context, tx pgx.Tx, senderUserID, recipientUserID, amount int64) (domain.PaidStarsCharge, error) {
	if senderUserID == 0 || recipientUserID == 0 || senderUserID == recipientUserID || amount <= 0 {
		return domain.PaidStarsCharge{}, domain.ErrStarsInvalidAmount
	}
	date := int(time.Now().Unix())
	var balance int64
	err := tx.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1 FOR UPDATE`, senderUserID).Scan(&balance)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && balance < amount) {
		return domain.PaidStarsCharge{}, domain.ErrStarsInsufficient
	}
	if err != nil {
		return domain.PaidStarsCharge{}, fmt.Errorf("select stars balance for paid message: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE stars_balances SET balance = balance - $2, updated_at = now() WHERE user_id = $1`, senderUserID, amount); err != nil {
		return domain.PaidStarsCharge{}, fmt.Errorf("debit stars balance for paid message: %w", err)
	}
	if err := insertStarsTxn(ctx, tx, senderUserID, -amount, domain.StarsReasonPaidMessage, domain.Peer{Type: domain.PeerTypeUser, ID: recipientUserID}, date, "Paid message", ""); err != nil {
		return domain.PaidStarsCharge{}, err
	}
	credited := amount * paidMessageChannelCommissionPermille / 1000
	var recipientBalance int64
	if err := tx.QueryRow(ctx, `
INSERT INTO stars_balances (user_id, balance, updated_at) VALUES ($1, $2, now())
ON CONFLICT (user_id) DO UPDATE SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()
RETURNING balance`, recipientUserID, credited).Scan(&recipientBalance); err != nil {
		return domain.PaidStarsCharge{}, fmt.Errorf("credit stars balance for paid message: %w", err)
	}
	if credited > 0 {
		if err := insertStarsTxn(ctx, tx, recipientUserID, credited, domain.StarsReasonPaidMessage, domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID}, date, "Paid message", ""); err != nil {
			return domain.PaidStarsCharge{}, err
		}
	}
	return domain.PaidStarsCharge{Amount: amount, SenderBalance: balance - amount, RecipientBalance: recipientBalance}, nil
}

// CountCreditAllUsers counts the candidate accounts for a bulk credit (real users: is_bot=false and not a system account).
func (s *StarsStore) CountCreditAllUsers(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRow(ctx, `
SELECT count(*)
FROM users
WHERE is_bot = false AND id <> ALL($1::bigint[])`, domain.SystemUserIDs()).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count stars credit-all users: %w", err)
	}
	return count, nil
}

// CreditAll bulk-credits amount to all real users in a single transaction (the admin's "send to everyone").
// For each affected account: a new row is inserted with balance=amount, an existing row's balance is
// accumulated, both setting granted=true -- the bulk credit is itself a grant, and setting granted
// avoids the lazy first-read starting grant stacking another one on top. Each user then gets a ledger
// row; the balance and the ledger complete atomically in the same transaction.
func (s *StarsStore) CreditAll(ctx context.Context, amount int64, reason domain.StarsTransactionReason, date int, title, desc string) (int64, error) {
	if amount <= 0 {
		return 0, domain.ErrStarsInvalidAmount
	}
	affected := int64(0)
	err := withTx(ctx, s.db, "credit all stars", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
WITH credited AS (
	INSERT INTO stars_balances (user_id, balance, granted, updated_at)
	SELECT id, $1::bigint, true, now()
	FROM users
	WHERE is_bot = false AND id <> ALL($2::bigint[])
	ON CONFLICT (user_id) DO UPDATE SET
		balance = stars_balances.balance + EXCLUDED.balance,
		granted = true,
		updated_at = now()
	RETURNING user_id
),
ledger AS (
	INSERT INTO stars_transactions (user_id, peer_type, peer_id, amount, reason, title, description, date)
	SELECT user_id, '', 0, $1::bigint, $3::text, $4::text, $5::text, $6::int
	FROM credited
	RETURNING 1
)
SELECT count(*) FROM ledger`, amount, domain.SystemUserIDs(), string(reason), title, desc, date).Scan(&affected); err != nil {
			return fmt.Errorf("credit all stars: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return affected, nil
}

// starsTransactionQueryParts centralizes the sign predicate and keyset
// direction for personal/channel Stars and TON ledgers. Column names are only
// package-owned constants; client values remain bind parameters.
func starsTransactionQueryParts(ownerColumn, amountColumn string, ownerID int64, query domain.StarsTransactionQuery) (string, string, []any) {
	where := ownerColumn + "=$1"
	switch query.Direction {
	case domain.StarsTransactionDirectionIncoming:
		where += " AND " + amountColumn + ">0"
	case domain.StarsTransactionDirectionOutgoing:
		where += " AND " + amountColumn + "<0"
	}
	order, comparator := "DESC", "<"
	if query.Ascending {
		order, comparator = "ASC", ">"
	}
	args := []any{ownerID, query.Limit + 1}
	if cursor, ok := domain.DecodeStarsCursor(query.Offset); ok {
		where += " AND id" + comparator + "$3"
		args = append(args, cursor)
	}
	return where, order, args
}

// insertStarsTxn writes one ledger row inside a transaction (amount is signed).
func insertStarsTxn(ctx context.Context, tx pgx.Tx, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, date int, title, desc string) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO stars_transactions (user_id, peer_type, peer_id, amount, reason, title, description, date)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		userID, string(peer.Type), peer.ID, amount, string(reason), title, desc, date); err != nil {
		return fmt.Errorf("insert stars transaction: %w", err)
	}
	return nil
}
