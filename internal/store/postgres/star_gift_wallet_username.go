package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// WalletForUsername answers the only question a Telegram identity can answer:
// which wallet this server can prove for it. Every source is a flashfragment
// (OpenFragment shared-db) record — the claim payment, the username mint, the
// verified wallet of the collectible username owner, or a TON Connect
// connection — so the typed name itself is never turned into an address.
func (s *StarGiftClaimStore) WalletForUsername(
	ctx context.Context,
	username string) (address, source string, found bool, err error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return "", "", false, nil
	}
	queries := []struct {
		label string
		sql   string
	}{
		{
			label: "fragment username claim",
			sql: `SELECT wallet FROM openfragment_username_claims
WHERE lower(username) = $1
LIMIT 1`,
		},
		{
			label: "collectible username mint",
			sql: `SELECT wallet FROM openfragment_username_mints
WHERE lower(username) = $1 AND wallet <> ''
ORDER BY (status = 'minted') DESC, created_at DESC
LIMIT 1`,
		},
		{
			label: "collectible username owner wallet",
			sql: `SELECT c.address_raw
FROM collectible_usernames cu
JOIN openfragment_wallet_connections c
  ON c.network = 'mainnet' AND c.user_id = cu.owner_peer_id
WHERE cu.username_lower = $1 AND cu.owner_peer_type = 'user'
ORDER BY c.last_seen_at DESC
LIMIT 1`,
		},
		{
			label: "ton connect",
			sql: `SELECT c.address_raw
FROM users u
JOIN openfragment_wallet_connections c ON c.user_id = u.id
WHERE lower(u.username) = $1 AND c.network = 'mainnet'
ORDER BY c.last_seen_at DESC
LIMIT 1`,
		},
	}
	for _, query := range queries {
		err = s.db.QueryRow(ctx, query.sql, username).Scan(&address)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", "", false, fmt.Errorf("wallet for username %s: %w", username, err)
		}
		return address, query.label, true, nil
	}
	return "", "", false, nil
}
