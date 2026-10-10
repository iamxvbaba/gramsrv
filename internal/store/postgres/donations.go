package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type DonationStore struct {
	db sqlcgen.DBTX
}

func NewDonationStore(db sqlcgen.DBTX) *DonationStore {
	return &DonationStore{db: db}
}

func (s *DonationStore) WalletSeed(ctx context.Context) ([]byte, []byte, bool, error) {
	var seed, nonce []byte
	err := s.db.QueryRow(ctx, `SELECT encrypted_seed, nonce FROM donation_wallet WHERE id = 1`).Scan(&seed, &nonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("load donation wallet seed: %w", err)
	}
	return seed, nonce, true, nil
}

func (s *DonationStore) CreateWalletSeed(ctx context.Context, encryptedSeed, nonce []byte) error {
	_, err := s.db.Exec(ctx, `INSERT INTO donation_wallet (id, encrypted_seed, nonce) VALUES (1, $1, $2)`, encryptedSeed, nonce)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrDonationWalletAlreadyExists
		}
		return fmt.Errorf("create donation wallet seed: %w", err)
	}
	return nil
}

func (s *DonationStore) DonationAddressForUser(ctx context.Context, userID int64) (domain.DonationAddress, bool, error) {
	var out domain.DonationAddress
	err := s.db.QueryRow(ctx, `SELECT user_id, derivation_index, address, created_at
FROM donation_addresses WHERE user_id = $1`, userID).
		Scan(&out.UserID, &out.DerivationIndex, &out.Address, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DonationAddress{}, false, nil
	}
	if err != nil {
		return domain.DonationAddress{}, false, fmt.Errorf("load donation address: %w", err)
	}
	return out, true, nil
}

func (s *DonationStore) DonationUserByAddress(ctx context.Context, address string) (int64, bool, error) {
	var userID int64
	err := s.db.QueryRow(ctx, `SELECT user_id FROM donation_addresses WHERE address = $1`, address).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("look up donation address owner: %w", err)
	}
	return userID, true, nil
}

func (s *DonationStore) NextDonationAddressIndex(ctx context.Context) (int64, error) {
	var index int64
	if err := s.db.QueryRow(ctx, `SELECT nextval('donation_address_index_seq')`).Scan(&index); err != nil {
		return 0, fmt.Errorf("reserve donation address index: %w", err)
	}
	return index, nil
}

func (s *DonationStore) InsertDonationAddress(ctx context.Context, userID, index int64, address string) (domain.DonationAddress, error) {
	var out domain.DonationAddress
	err := s.db.QueryRow(ctx, `
INSERT INTO donation_addresses (user_id, derivation_index, address) VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO UPDATE SET user_id = donation_addresses.user_id
RETURNING user_id, derivation_index, address, created_at`, userID, index, address).
		Scan(&out.UserID, &out.DerivationIndex, &out.Address, &out.CreatedAt)
	if err != nil {
		return domain.DonationAddress{}, fmt.Errorf("insert donation address: %w", err)
	}
	return out, nil
}

func (s *DonationStore) EnabledDonationChains(ctx context.Context) ([]domain.DonationChain, error) {
	rows, err := s.db.Query(ctx, `SELECT chain_key, name, chain_id, rpc_url, ws_url, native_symbol, native_decimals,
confirmations_required, price_feed_address, manual_usd_rate_micros, enabled
FROM donation_chains WHERE enabled ORDER BY chain_key`)
	if err != nil {
		return nil, fmt.Errorf("list enabled donation chains: %w", err)
	}
	defer rows.Close()
	return scanDonationChains(rows)
}

func (s *DonationStore) DonationChain(ctx context.Context, chainKey string) (domain.DonationChain, bool, error) {
	var out domain.DonationChain
	err := s.db.QueryRow(ctx, `SELECT chain_key, name, chain_id, rpc_url, ws_url, native_symbol, native_decimals,
confirmations_required, price_feed_address, manual_usd_rate_micros, enabled
FROM donation_chains WHERE chain_key = $1`, chainKey).Scan(
		&out.Key, &out.Name, &out.ChainID, &out.RPCURL, &out.WSURL, &out.NativeSymbol, &out.NativeDecimals,
		&out.ConfirmationsRequired, &out.PriceFeedAddress, &out.ManualUSDRateMicros, &out.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DonationChain{}, false, nil
	}
	if err != nil {
		return domain.DonationChain{}, false, fmt.Errorf("load donation chain: %w", err)
	}
	return out, true, nil
}

func (s *DonationStore) UpdateDonationChainConfig(ctx context.Context, upd domain.DonationChainConfigUpdate) (domain.DonationChain, error) {
	var out domain.DonationChain
	err := s.db.QueryRow(ctx, `UPDATE donation_chains SET
    rpc_url = $2, ws_url = $3, confirmations_required = $4, price_feed_address = $5,
    manual_usd_rate_micros = $6, enabled = $7
WHERE chain_key = $1
RETURNING chain_key, name, chain_id, rpc_url, ws_url, native_symbol, native_decimals,
    confirmations_required, price_feed_address, manual_usd_rate_micros, enabled`,
		upd.ChainKey, upd.RPCURL, upd.WSURL, upd.ConfirmationsRequired, upd.PriceFeedAddress,
		upd.ManualUSDRateMicros, upd.Enabled).Scan(
		&out.Key, &out.Name, &out.ChainID, &out.RPCURL, &out.WSURL, &out.NativeSymbol, &out.NativeDecimals,
		&out.ConfirmationsRequired, &out.PriceFeedAddress, &out.ManualUSDRateMicros, &out.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DonationChain{}, domain.ErrDonationChainNotFound
	}
	if err != nil {
		return domain.DonationChain{}, fmt.Errorf("update donation chain config: %w", err)
	}
	return out, nil
}

func scanDonationChains(rows pgx.Rows) ([]domain.DonationChain, error) {
	out := make([]domain.DonationChain, 0)
	for rows.Next() {
		var c domain.DonationChain
		if err := rows.Scan(&c.Key, &c.Name, &c.ChainID, &c.RPCURL, &c.WSURL, &c.NativeSymbol, &c.NativeDecimals,
			&c.ConfirmationsRequired, &c.PriceFeedAddress, &c.ManualUSDRateMicros, &c.Enabled); err != nil {
			return nil, fmt.Errorf("scan donation chain: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate donation chains: %w", err)
	}
	return out, nil
}

func (s *DonationStore) DonationTokens(ctx context.Context, chainKey string) ([]domain.DonationToken, error) {
	rows, err := s.db.Query(ctx, `SELECT chain_key, symbol, contract_address, decimals
FROM donation_tokens WHERE chain_key = $1 ORDER BY symbol`, chainKey)
	if err != nil {
		return nil, fmt.Errorf("list donation tokens: %w", err)
	}
	defer rows.Close()
	out := make([]domain.DonationToken, 0)
	for rows.Next() {
		var t domain.DonationToken
		if err := rows.Scan(&t.ChainKey, &t.Symbol, &t.ContractAddress, &t.Decimals); err != nil {
			return nil, fmt.Errorf("scan donation token: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate donation tokens: %w", err)
	}
	return out, nil
}

func (s *DonationStore) DonationChainCursor(ctx context.Context, chainKey string) (int64, error) {
	var block int64
	err := s.db.QueryRow(ctx, `SELECT last_scanned_block FROM donation_chain_cursors WHERE chain_key = $1`, chainKey).Scan(&block)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load donation chain cursor: %w", err)
	}
	return block, nil
}

func (s *DonationStore) SetDonationChainCursor(ctx context.Context, chainKey string, block int64) error {
	_, err := s.db.Exec(ctx, `
INSERT INTO donation_chain_cursors (chain_key, last_scanned_block, updated_at) VALUES ($1, $2, now())
ON CONFLICT (chain_key) DO UPDATE SET last_scanned_block = EXCLUDED.last_scanned_block, updated_at = now()`,
		chainKey, block)
	if err != nil {
		return fmt.Errorf("set donation chain cursor: %w", err)
	}
	return nil
}

// donationDepositScanner is the common surface of pgx.Row and pgx.Rows, so
// one scan routine serves every deposit query below regardless of whether
// it reads one row or iterates many.
type donationDepositScanner interface {
	Scan(dest ...any) error
}

// donationDepositColumns is the SELECT list every deposit query below uses,
// in the exact order scanDonationDeposit expects.
const donationDepositColumns = `id, user_id, chain_key, token_symbol, tx_hash, log_index, block_number, amount_raw::text,
       usd_value_micros, stars_credited, status, confirmations, detected_at, credited_at`

// scanDonationDeposit reads one donationDepositColumns row. credited_at is
// nullable (a deposit that hasn't been credited yet), so it scans through
// pgtype.Timestamptz rather than directly into time.Time.
func scanDonationDeposit(row donationDepositScanner) (domain.DonationDeposit, error) {
	var d domain.DonationDeposit
	var status string
	var creditedAt pgtype.Timestamptz
	if err := row.Scan(&d.ID, &d.UserID, &d.ChainKey, &d.TokenSymbol, &d.TxHash, &d.LogIndex, &d.BlockNumber,
		&d.AmountRaw, &d.USDValueMicros, &d.StarsCredited, &status, &d.Confirmations, &d.DetectedAt, &creditedAt); err != nil {
		return domain.DonationDeposit{}, err
	}
	d.Status = domain.DonationDepositStatus(status)
	if creditedAt.Valid {
		d.CreditedAt = creditedAt.Time
	}
	return d, nil
}

func scanDonationDeposits(rows pgx.Rows) ([]domain.DonationDeposit, error) {
	out := make([]domain.DonationDeposit, 0)
	for rows.Next() {
		d, err := scanDonationDeposit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan donation deposit: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate donation deposits: %w", err)
	}
	return out, nil
}

func (s *DonationStore) RecordDonationDeposit(ctx context.Context, deposit domain.DonationDeposit) (domain.DonationDeposit, bool, error) {
	row := s.db.QueryRow(ctx, `
WITH inserted AS (
    INSERT INTO donation_deposits
        (user_id, chain_key, token_symbol, tx_hash, log_index, block_number, amount_raw, status, confirmations, detected_at)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
    ON CONFLICT (chain_key, tx_hash, log_index) DO NOTHING
    RETURNING *
)
SELECT `+donationDepositColumns+`, true AS created
FROM inserted
UNION ALL
SELECT `+donationDepositColumns+`, false AS created
FROM donation_deposits WHERE chain_key = $2 AND tx_hash = $4 AND log_index = $5
AND NOT EXISTS (SELECT 1 FROM inserted)
LIMIT 1`,
		deposit.UserID, deposit.ChainKey, deposit.TokenSymbol, deposit.TxHash, deposit.LogIndex,
		deposit.BlockNumber, deposit.AmountRaw, string(deposit.Status), deposit.Confirmations)
	var d domain.DonationDeposit
	var status string
	var creditedAt pgtype.Timestamptz
	var created bool
	if err := row.Scan(&d.ID, &d.UserID, &d.ChainKey, &d.TokenSymbol, &d.TxHash, &d.LogIndex, &d.BlockNumber,
		&d.AmountRaw, &d.USDValueMicros, &d.StarsCredited, &status, &d.Confirmations, &d.DetectedAt, &creditedAt, &created); err != nil {
		return domain.DonationDeposit{}, false, fmt.Errorf("record donation deposit: %w", err)
	}
	d.Status = domain.DonationDepositStatus(status)
	if creditedAt.Valid {
		d.CreditedAt = creditedAt.Time
	}
	return d, created, nil
}

func (s *DonationStore) UpdateDonationDepositConfirmations(ctx context.Context, chainKey, txHash string, logIndex int, confirmations int) (domain.DonationDeposit, error) {
	row := s.db.QueryRow(ctx, `
UPDATE donation_deposits SET
    confirmations = $4,
    status = CASE WHEN status = 'pending' AND $4 >= (
        SELECT confirmations_required FROM donation_chains WHERE chain_key = donation_deposits.chain_key
    ) THEN 'confirmed' ELSE status END
WHERE chain_key = $1 AND tx_hash = $2 AND log_index = $3 AND status = 'pending'
RETURNING `+donationDepositColumns, chainKey, txHash, logIndex, confirmations)
	out, err := scanDonationDeposit(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DonationDeposit{}, nil // already past pending, or never existed; not an error
	}
	if err != nil {
		return domain.DonationDeposit{}, fmt.Errorf("update donation deposit confirmations: %w", err)
	}
	return out, nil
}

func (s *DonationStore) PendingDonationDeposits(ctx context.Context, chainKey string) ([]domain.DonationDeposit, error) {
	rows, err := s.db.Query(ctx, `SELECT `+donationDepositColumns+`
FROM donation_deposits WHERE chain_key = $1 AND status = 'pending' ORDER BY id`, chainKey)
	if err != nil {
		return nil, fmt.Errorf("list pending donation deposits: %w", err)
	}
	defer rows.Close()
	return scanDonationDeposits(rows)
}

func (s *DonationStore) ConfirmedUncreditedDeposits(ctx context.Context, chainKey string, limit int) ([]domain.DonationDeposit, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT `+donationDepositColumns+`
FROM donation_deposits WHERE chain_key = $1 AND status = 'confirmed' ORDER BY id LIMIT $2`, chainKey, limit)
	if err != nil {
		return nil, fmt.Errorf("list confirmed donation deposits: %w", err)
	}
	defer rows.Close()
	return scanDonationDeposits(rows)
}

// CreditDonationDeposit is the only place a deposit's stars_credited is ever
// written, guarded by the WHERE status='confirmed' below: a deposit already
// credited (or somehow orphaned) matches zero rows, so a duplicate watcher
// pass or a retried call can never double-credit.
func (s *DonationStore) CreditDonationDeposit(ctx context.Context, depositID int64, usdValueMicros, stars int64, date int) (domain.DonationDeposit, bool, error) {
	if depositID <= 0 || stars <= 0 {
		return domain.DonationDeposit{}, false, domain.ErrDonationDepositInvalid
	}
	var credited bool
	err := withTx(ctx, s.db, "credit donation deposit", func(tx pgx.Tx) error {
		var userID int64
		var chainKey, tokenSymbol string
		err := tx.QueryRow(ctx, `
UPDATE donation_deposits SET status = 'credited', usd_value_micros = $2, stars_credited = $3, credited_at = now()
WHERE id = $1 AND status = 'confirmed'
RETURNING user_id, chain_key, token_symbol`, depositID, usdValueMicros, stars).Scan(&userID, &chainKey, &tokenSymbol)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // not confirmed (already credited, or never got there) -- not an error, just nothing to do.
		}
		if err != nil {
			return fmt.Errorf("mark donation deposit credited: %w", err)
		}
		symbol := tokenSymbol
		if symbol == "" {
			if chain, found, err := (&DonationStore{db: tx}).DonationChain(ctx, chainKey); err == nil && found {
				symbol = chain.NativeSymbol
			}
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO stars_balances (user_id, balance, updated_at) VALUES ($1, $2, now())
ON CONFLICT (user_id) DO UPDATE SET balance = stars_balances.balance + EXCLUDED.balance, updated_at = now()`,
			userID, stars); err != nil {
			return fmt.Errorf("credit stars balance for donation: %w", err)
		}
		if err := insertStarsTxn(ctx, tx, userID, stars, domain.StarsReasonDonationDeposit, domain.Peer{}, date,
			"Crypto donation", fmt.Sprintf("%s deposit on %s", symbol, chainKey)); err != nil {
			return err
		}
		credited = true
		return nil
	})
	if err != nil {
		return domain.DonationDeposit{}, false, err
	}
	if !credited {
		return domain.DonationDeposit{}, false, nil
	}
	return domain.DonationDeposit{ID: depositID, Status: domain.DonationDepositCredited, USDValueMicros: usdValueMicros, StarsCredited: stars}, true, nil
}

func (s *DonationStore) UserDonationDeposits(ctx context.Context, userID int64, limit int) ([]domain.DonationDeposit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT `+donationDepositColumns+`
FROM donation_deposits WHERE user_id = $1 ORDER BY detected_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list user donation deposits: %w", err)
	}
	defer rows.Close()
	return scanDonationDeposits(rows)
}
