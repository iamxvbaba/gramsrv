package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminConsoleUser is one named panel operator. It deliberately never carries
// the password hash outside authentication: everything that renders or returns
// a user uses this shape, so a hash cannot leak into an API response by
// someone adding a field to a JSON struct.
type AdminConsoleUser struct {
	ID          int64      `json:"id"`
	Username    string     `json:"username"`
	Permissions []string   `json:"permissions"`
	Enabled     bool       `json:"enabled"`
	TokenEpoch  int32      `json:"token_epoch"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// adminConsoleCredential is the authentication-only view: the hash plus the
// few fields a login decision needs. Kept unexported and separate from
// AdminConsoleUser so the hash has exactly one reason to be read.
type adminConsoleCredential struct {
	ID           int64
	Username     string
	PasswordHash string
	Permissions  []string
	Enabled      bool
	TokenEpoch   int32
}

// errAdminUserNotFound is returned instead of pgx.ErrNoRows so callers can
// treat "no such operator" without importing pgx.
var errAdminUserNotFound = errors.New("admin console user not found")

const adminConsoleUserColumns = `id, username, permissions, enabled, token_epoch, created_at, updated_at, last_login_at`

// AdminConsoleCredentialByUsername loads the authentication view for a login
// attempt. The lookup is case-insensitive to match the unique index, so an
// operator cannot be shadowed by a differently-cased duplicate.
func (s *readStore) AdminConsoleCredentialByUsername(ctx context.Context, username string) (adminConsoleCredential, error) {
	var out adminConsoleCredential
	err := s.pool.QueryRow(ctx, `
SELECT id, username, password_hash, permissions, enabled, token_epoch
FROM admin_console_users
WHERE lower(username) = lower($1)`, strings.TrimSpace(username)).Scan(
		&out.ID, &out.Username, &out.PasswordHash, &out.Permissions, &out.Enabled, &out.TokenEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminConsoleCredential{}, errAdminUserNotFound
	}
	if err != nil {
		return adminConsoleCredential{}, fmt.Errorf("load admin console credential: %w", err)
	}
	return out, nil
}

// AdminConsoleSessionState re-reads the two things a live session depends on.
// requireAuthAPI calls it per request so that disabling an operator, editing
// their rights or changing their password takes effect immediately rather than
// whenever their signed cookie happens to expire.
func (s *readStore) AdminConsoleSessionState(ctx context.Context, id int64) (enabled bool, epoch int32, permissions []string, err error) {
	err = s.pool.QueryRow(ctx, `
SELECT enabled, token_epoch, permissions FROM admin_console_users WHERE id = $1`, id).
		Scan(&enabled, &epoch, &permissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, nil, errAdminUserNotFound
	}
	if err != nil {
		return false, 0, nil, fmt.Errorf("load admin console session state: %w", err)
	}
	return enabled, epoch, permissions, nil
}

// ListAdminConsoleUsers returns every operator, oldest first so the list reads
// like the order they were added.
func (s *readStore) ListAdminConsoleUsers(ctx context.Context) ([]AdminConsoleUser, error) {
	rows, err := s.pool.Query(ctx, `
SELECT `+adminConsoleUserColumns+` FROM admin_console_users ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list admin console users: %w", err)
	}
	defer rows.Close()

	out := []AdminConsoleUser{}
	for rows.Next() {
		var u AdminConsoleUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Permissions, &u.Enabled,
			&u.TokenEpoch, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt); err != nil {
			return nil, fmt.Errorf("scan admin console user: %w", err)
		}
		if u.Permissions == nil {
			u.Permissions = []string{}
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate admin console users: %w", err)
	}
	return out, nil
}

// CountEnabledAdminConsoleUsersWith reports how many enabled operators hold a
// given permission, counting the '*' wildcard as holding everything. It exists
// for the last-administrator guard: the panel refuses the edit that would
// leave nobody able to manage operators.
func (s *readStore) CountEnabledAdminConsoleUsersWith(ctx context.Context, permission string, excludeID int64) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `
SELECT count(*)::int FROM admin_console_users
WHERE enabled
  AND id <> $2
  AND (permissions @> ARRAY[$1]::text[] OR permissions @> ARRAY['*']::text[])`,
		permission, excludeID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count admin console users with permission: %w", err)
	}
	return n, nil
}

// touchAdminConsoleLogin stamps the login time on a named account, best
// effort. It is called after a successful login only, so a failure here must
// never fail the login itself.
func (s *readStore) touchAdminConsoleLogin(ctx context.Context, id int64) {
	if s == nil || s.pool == nil || id <= 0 {
		return
	}
	_, _ = s.pool.Exec(ctx, `
UPDATE admin_console_users SET last_login_at = now() WHERE id = $1`, id)
}
