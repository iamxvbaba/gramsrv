package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/admin"
)

const (
	panelInviteMinTTLMinutes = 5
	panelInviteMaxTTLMinutes = 30 * 24 * 60
)

var (
	errPanelInvitePermissions = errors.New("choose at least one permission")
	errPanelInviteTTL         = errors.New("expiry must be between 5 minutes and 30 days")
	errPanelInviteNotFound    = errors.New("panel invite not found")
	errPanelInviteRevoked     = errors.New("panel invite is already revoked")
)

// panelInvite is one temporary operator row. The account itself lives in
// admin_console_users; this is the invite-specific metadata that makes it
// temporary and gives it a TOTP secret of its own.
type panelInvite struct {
	UserID      int64      `json:"user_id"`
	Username    string     `json:"username"`
	Label       string     `json:"label"`
	Permissions []string   `json:"permissions"`
	Enabled     bool       `json:"enabled"`
	ExpiresAt   time.Time  `json:"expires_at"`
	SingleUse   bool       `json:"single_use"`
	UsedAt      *time.Time `json:"used_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CreatedBy   string     `json:"created_by"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// panelInviteCredentials is returned exactly once, in the HTTP response to the
// confirming command. It must never be placed in admin.CommandResult.Details:
// that struct is persisted in admin_commands and admin_audit_logs.
type panelInviteCredentials struct {
	UserID      int64     `json:"user_id"`
	Username    string    `json:"username"`
	Password    string    `json:"password"`
	Label       string    `json:"label"`
	Permissions []string  `json:"permissions"`
	ExpiresAt   time.Time `json:"expires_at"`
	SingleUse   bool      `json:"single_use"`
	TOTPSecret  string    `json:"totp_secret"`
	OTPAuthURI  string    `json:"otpauth_uri"`
	QRPNGBase64 string    `json:"qr_png_base64"`
}

type panelInviteActionRequest struct {
	CommandID   string   `json:"command_id"`
	Reason      string   `json:"reason"`
	Confirm     bool     `json:"confirm"`
	UserID      int64    `json:"user_id"`
	Username    string   `json:"username"`
	Password    string   `json:"password"`
	Label       string   `json:"label"`
	Permissions []string `json:"permissions"`
	TTLMinutes  int      `json:"ttl_minutes"`
	SingleUse   *bool    `json:"single_use"`
}

// panelInviteCreateResponse flattens the audited command result and adds the
// one-time credentials the operator needs to hand over. The embedded result is
// what gets stored in the audit trail; Invite is response-only.
type panelInviteCreateResponse struct {
	admin.CommandResult
	Invite *panelInviteCredentials `json:"invite,omitempty"`
}

func (s *readStore) ListPanelInvites(ctx context.Context) ([]panelInvite, error) {
	rows, err := s.pool.Query(ctx, `
SELECT u.id, u.username, i.label, u.permissions, u.enabled, i.expires_at,
       i.single_use, i.used_at, i.revoked_at, i.created_at, i.created_by, u.last_login_at
FROM admin_console_invites i
JOIN admin_console_users u ON u.id = i.user_id
ORDER BY i.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list panel invites: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	out := []panelInvite{}
	for rows.Next() {
		var inv panelInvite
		if err := rows.Scan(&inv.UserID, &inv.Username, &inv.Label, &inv.Permissions,
			&inv.Enabled, &inv.ExpiresAt, &inv.SingleUse, &inv.UsedAt, &inv.RevokedAt,
			&inv.CreatedAt, &inv.CreatedBy, &inv.LastLoginAt); err != nil {
			return nil, fmt.Errorf("scan panel invite: %w", err)
		}
		if inv.Permissions == nil {
			inv.Permissions = []string{}
		}
		switch {
		case inv.RevokedAt != nil:
			inv.Status = "revoked"
		case !now.Before(inv.ExpiresAt):
			inv.Status = "expired"
		case inv.SingleUse && inv.UsedAt != nil:
			inv.Status = "used"
		case !inv.Enabled:
			inv.Status = "disabled"
		default:
			inv.Status = "active"
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate panel invites: %w", err)
	}
	return out, nil
}

func (s *readStore) PanelInviteTOTP(ctx context.Context, userID int64) (string, string, error) {
	var username, secret string
	err := s.pool.QueryRow(ctx, `
SELECT u.username, i.totp_secret
FROM admin_console_invites i
JOIN admin_console_users u ON u.id = i.user_id
WHERE i.user_id = $1`, userID).Scan(&username, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errPanelInviteNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("load panel invite totp: %w", err)
	}
	return username, secret, nil
}

// ConsumePanelInvite burns a single-use invite exactly once. The UPDATE is the
// concurrency guard: two simultaneous logins with the same invite produce one
// row affected and one rejection.
func (s *readStore) ConsumePanelInvite(ctx context.Context, userID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE admin_console_invites
SET used_at = now()
WHERE user_id = $1
  AND single_use
  AND used_at IS NULL
  AND revoked_at IS NULL
  AND expires_at > now()`, userID)
	if err != nil {
		return false, fmt.Errorf("consume panel invite: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// verifyLoginTOTP uses the invite's own secret when it has one. This is what
// keeps an invitee from ever needing the break-glass TOTP secret, and it keeps
// the replay guard per account: the verifier is cached for the process
// lifetime, exactly like the global one.
func (s *server) verifyLoginTOTP(identity loginIdentity, code string, now time.Time) bool {
	if strings.TrimSpace(identity.totpSecret) != "" {
		s.totpMu.Lock()
		defer s.totpMu.Unlock()
		if s.totpUsers == nil {
			s.totpUsers = make(map[int64]*totpVerifier)
		}
		verifier, ok := s.totpUsers[identity.userID]
		if !ok {
			created, err := newTOTPVerifier(identity.totpSecret)
			if err != nil {
				return false
			}
			verifier = created
			s.totpUsers[identity.userID] = verifier
		}
		return verifier.Verify(code, now)
	}
	return s.totp == nil || s.totp.Verify(code, now)
}

func (s *server) handleListPanelInvitesAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read store is not configured")
		return
	}
	rows, err := s.read.ListPanelInvites(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":                  rows,
		"available_permissions": assignablePermissions(),
	})
}

func (s *server) handlePanelInviteQRAPI(w http.ResponseWriter, r *http.Request) {
	if s.read == nil {
		http.NotFound(w, r)
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || userID <= 0 {
		http.NotFound(w, r)
		return
	}
	username, secret, err := s.read.PanelInviteTOTP(r.Context(), userID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	png, err := panelInviteQRPNG(panelInviteTOTPURI(username, secret))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(png)
}

func (s *server) handleCreatePanelInviteAPI(w http.ResponseWriter, r *http.Request) {
	var body panelInviteActionRequest
	if err := decodeJSON(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "panel-invite-create")
	username := strings.TrimSpace(body.Username)
	label := strings.TrimSpace(body.Label)
	permissions := normalisePermissions(body.Permissions)
	singleUse := body.SingleUse == nil || *body.SingleUse
	ttlMinutes := body.TTLMinutes
	params := map[string]any{
		"username":    username,
		"label":       label,
		"permissions": permissions,
		"ttl_minutes": ttlMinutes,
		"single_use":  singleUse,
	}
	var credentials *panelInviteCredentials

	fn := func(ctx context.Context, tx pgx.Tx) (admin.CommandResult, error) {
		if err := validateAdminUsername(username); err != nil {
			return admin.CommandResult{}, err
		}
		if strings.EqualFold(username, breakGlassUsername) {
			return admin.CommandResult{}, errUsernameReserved
		}
		if err := validateAdminPassword(body.Password); err != nil {
			return admin.CommandResult{}, err
		}
		if len(permissions) == 0 {
			return admin.CommandResult{}, errPanelInvitePermissions
		}
		if err := validatePermissions(permissions); err != nil {
			return admin.CommandResult{}, err
		}
		if ttlMinutes < panelInviteMinTTLMinutes || ttlMinutes > panelInviteMaxTTLMinutes {
			return admin.CommandResult{}, errPanelInviteTTL
		}
		var taken bool
		if err := tx.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM admin_console_users WHERE lower(username) = lower($1))`,
			username).Scan(&taken); err != nil {
			return admin.CommandResult{}, fmt.Errorf("check username: %w", err)
		}
		if taken {
			return admin.CommandResult{}, errAdminUsernameTaken
		}
		expiresAt := time.Now().Add(time.Duration(ttlMinutes) * time.Minute)
		if meta.DryRun {
			return admin.CommandResult{
				Status: "ok",
				DryRun: true,
				Message: fmt.Sprintf("Would create a temporary operator %q with %d permission(s), valid until %s.",
					username, len(permissions), expiresAt.UTC().Format(time.RFC3339)),
				Details: params,
			}, nil
		}

		secret, err := newPanelInviteTOTPSecret()
		if err != nil {
			return admin.CommandResult{}, err
		}
		user, err := createAdminConsoleUserOn(ctx, tx, username, body.Password, permissions, true)
		if err != nil {
			return admin.CommandResult{}, err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO admin_console_invites (user_id, totp_secret, label, created_by, single_use, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)`,
			user.ID, secret, label, meta.Actor, singleUse, expiresAt); err != nil {
			return admin.CommandResult{}, fmt.Errorf("create panel invite: %w", err)
		}

		uri := panelInviteTOTPURI(user.Username, secret)
		qr, err := panelInviteQRPNG(uri)
		if err != nil {
			return admin.CommandResult{}, err
		}
		credentials = &panelInviteCredentials{
			UserID:      user.ID,
			Username:    user.Username,
			Password:    body.Password,
			Label:       label,
			Permissions: user.Permissions,
			ExpiresAt:   expiresAt,
			SingleUse:   singleUse,
			TOTPSecret:  secret,
			OTPAuthURI:  uri,
			QRPNGBase64: base64.StdEncoding.EncodeToString(qr),
		}
		return admin.CommandResult{
			Status:  "ok",
			Message: fmt.Sprintf("Issued temporary access for %q.", user.Username),
			Details: map[string]any{
				"user_id":     user.ID,
				"username":    user.Username,
				"permissions": user.Permissions,
				"expires_at":  expiresAt,
				"single_use":  singleUse,
			},
		}, nil
	}

	result, err := s.runOperatorCommand(r.Context(), meta, "create-panel-invite", params, body.Password, fn)
	if err != nil {
		writeCommandResultAPI(w, result, err)
		return
	}
	if credentials == nil && !result.DryRun && result.Status == "completed" {
		// The command was replayed, and the one-time secret was deliberately
		// never persisted. There is nothing safe to hand back; a new invite is
		// the only way to get a secret again.
		result.Message = "access was already issued; its one-time secret cannot be replayed. Revoke it and issue a new one."
		result.Error = "INVITE_SECRET_NOT_REPLAYABLE"
		writeJSON(w, http.StatusConflict, result)
		return
	}
	writeJSON(w, http.StatusOK, panelInviteCreateResponse{CommandResult: result, Invite: credentials})
}

func (s *server) handleRevokePanelInviteAPI(w http.ResponseWriter, r *http.Request) {
	var body panelInviteActionRequest
	if err := decodeJSON(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.UserID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	meta := s.commandMetaFromAPI(r, body.CommandID, body.Reason, body.Confirm, "panel-invite-revoke")
	params := map[string]any{"user_id": body.UserID}

	fn := func(ctx context.Context, tx pgx.Tx) (admin.CommandResult, error) {
		var username string
		var revokedAt *time.Time
		err := tx.QueryRow(ctx, `
SELECT u.username, i.revoked_at
FROM admin_console_invites i
JOIN admin_console_users u ON u.id = i.user_id
WHERE i.user_id = $1`, body.UserID).Scan(&username, &revokedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return admin.CommandResult{}, errPanelInviteNotFound
		}
		if err != nil {
			return admin.CommandResult{}, fmt.Errorf("load panel invite: %w", err)
		}
		if revokedAt != nil {
			return admin.CommandResult{}, errPanelInviteRevoked
		}
		if meta.DryRun {
			return admin.CommandResult{
				Status:  "ok",
				DryRun:  true,
				Message: fmt.Sprintf("Would revoke access for %q.", username),
				Details: params,
			}, nil
		}
		if _, err := tx.Exec(ctx, `
UPDATE admin_console_invites SET revoked_at = now() WHERE user_id = $1`, body.UserID); err != nil {
			return admin.CommandResult{}, fmt.Errorf("revoke panel invite: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE admin_console_users SET enabled = false, updated_at = now() WHERE id = $1`, body.UserID); err != nil {
			return admin.CommandResult{}, fmt.Errorf("disable invited operator: %w", err)
		}
		return admin.CommandResult{
			Status:  "ok",
			Message: fmt.Sprintf("Revoked access for %q.", username),
			Details: params,
		}, nil
	}

	result, err := s.runOperatorCommand(r.Context(), meta, "revoke-panel-invite", params, "", fn)
	writeCommandResultAPI(w, result, err)
}

func newPanelInviteTOTPSecret() (string, error) {
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate totp secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]), nil
}

func panelInviteTOTPURI(username, secret string) string {
	label := "XiroGram%20Admin:" + url.PathEscape(username)
	return fmt.Sprintf(
		"otpauth://totp/%s?secret=%s&issuer=XiroGram%%20Admin&period=30&digits=6",
		label, secret,
	)
}

func panelInviteQRPNG(uri string) ([]byte, error) {
	out, err := exec.Command("qrencode", "-o", "-", "-s", "8", "-m", "2", uri).Output()
	if err != nil {
		return nil, fmt.Errorf("generate totp qr: %w", err)
	}
	return out, nil
}

func (s *server) sweepExpiredInvites(ctx context.Context) {
	s.disableExpiredInvites(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.disableExpiredInvites(ctx)
		}
	}
}

func (s *server) disableExpiredInvites(ctx context.Context) {
	if s.read == nil || s.read.pool == nil {
		return
	}
	_, _ = s.read.pool.Exec(ctx, `
UPDATE admin_console_users u
SET enabled = false, updated_at = now()
WHERE u.enabled
  AND EXISTS (
    SELECT 1 FROM admin_console_invites i
    WHERE i.user_id = u.id AND i.expires_at <= now()
  )`)
}
