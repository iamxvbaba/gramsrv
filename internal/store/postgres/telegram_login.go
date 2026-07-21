package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type TelegramLoginStore struct {
	db sqlcgen.DBTX
}

func NewTelegramLoginStore(db sqlcgen.DBTX) *TelegramLoginStore {
	return &TelegramLoginStore{db: db}
}

type telegramLoginRowScanner interface {
	Scan(dest ...any) error
}

const telegramLoginClientColumns = `bot_user_id, client_id, client_secret_hash, secret_version, signing_algorithm, enabled, created_at, updated_at`

func scanTelegramLoginClient(row telegramLoginRowScanner) (domain.TelegramLoginClient, error) {
	var client domain.TelegramLoginClient
	var algorithm string
	if err := row.Scan(&client.BotUserID, &client.ClientID, &client.SecretHash, &client.SecretVersion, &algorithm, &client.Enabled, &client.CreatedAt, &client.UpdatedAt); err != nil {
		return domain.TelegramLoginClient{}, err
	}
	client.SigningAlgorithm = domain.TelegramLoginSigningAlgorithm(algorithm)
	return client, nil
}

func (s *TelegramLoginStore) CreateTelegramLoginClient(ctx context.Context, client domain.TelegramLoginClient) (domain.TelegramLoginClient, error) {
	if err := client.Validate(); err != nil {
		return domain.TelegramLoginClient{}, err
	}
	createdAt := client.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := client.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	client, err := scanTelegramLoginClient(s.db.QueryRow(ctx, `
INSERT INTO bot_login_clients (
  bot_user_id, client_id, client_secret_hash, secret_version, signing_algorithm,
  enabled, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING `+telegramLoginClientColumns,
		client.BotUserID, client.ClientID, client.SecretHash, client.SecretVersion,
		string(client.SigningAlgorithm), client.Enabled, createdAt, updatedAt))
	if err != nil {
		return domain.TelegramLoginClient{}, mapTelegramLoginWriteError("create telegram login client", err)
	}
	return client, nil
}

func (s *TelegramLoginStore) UpsertTelegramLoginClient(ctx context.Context, client domain.TelegramLoginClient) (domain.TelegramLoginClient, error) {
	if err := client.Validate(); err != nil {
		return domain.TelegramLoginClient{}, err
	}
	createdAt := client.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	updatedAt := client.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO bot_login_clients (
  bot_user_id, client_id, client_secret_hash, secret_version, signing_algorithm,
  enabled, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (bot_user_id) DO UPDATE SET
  client_id = EXCLUDED.client_id,
  client_secret_hash = EXCLUDED.client_secret_hash,
  secret_version = EXCLUDED.secret_version,
  signing_algorithm = EXCLUDED.signing_algorithm,
  enabled = EXCLUDED.enabled,
  updated_at = EXCLUDED.updated_at
RETURNING `+telegramLoginClientColumns,
		client.BotUserID, client.ClientID, client.SecretHash, client.SecretVersion,
		string(client.SigningAlgorithm), client.Enabled, createdAt, updatedAt)
	out, err := scanTelegramLoginClient(row)
	if err != nil {
		return domain.TelegramLoginClient{}, mapTelegramLoginWriteError("upsert telegram login client", err)
	}
	return out, nil
}

func (s *TelegramLoginStore) GetTelegramLoginClient(ctx context.Context, clientID string) (domain.TelegramLoginClient, bool, error) {
	client, err := scanTelegramLoginClient(s.db.QueryRow(ctx, `SELECT `+telegramLoginClientColumns+` FROM bot_login_clients WHERE client_id = $1`, clientID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginClient{}, false, nil
	}
	if err != nil {
		return domain.TelegramLoginClient{}, false, fmt.Errorf("get telegram login client: %w", err)
	}
	return client, true, nil
}

func (s *TelegramLoginStore) GetTelegramLoginClientByBot(ctx context.Context, botUserID int64) (domain.TelegramLoginClient, bool, error) {
	client, err := scanTelegramLoginClient(s.db.QueryRow(ctx, `SELECT `+telegramLoginClientColumns+` FROM bot_login_clients WHERE bot_user_id = $1`, botUserID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginClient{}, false, nil
	}
	if err != nil {
		return domain.TelegramLoginClient{}, false, fmt.Errorf("get telegram login client by bot: %w", err)
	}
	return client, true, nil
}

func (s *TelegramLoginStore) RotateTelegramLoginClientSecret(ctx context.Context, botUserID, expectedVersion int64, secretHash []byte, now time.Time) (domain.TelegramLoginClient, error) {
	if botUserID <= 0 || expectedVersion <= 0 || len(secretHash) != 32 {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	client, err := scanTelegramLoginClient(s.db.QueryRow(ctx, `
UPDATE bot_login_clients
SET client_secret_hash = $3, secret_version = secret_version + 1, updated_at = $4
WHERE bot_user_id = $1 AND secret_version = $2
RETURNING `+telegramLoginClientColumns, botUserID, expectedVersion, secretHash, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginRequestConflict
	}
	if err != nil {
		return domain.TelegramLoginClient{}, fmt.Errorf("rotate telegram login client secret: %w", err)
	}
	return client, nil
}

func (s *TelegramLoginStore) SetTelegramLoginClientSigningAlgorithm(ctx context.Context, botUserID int64, algorithm domain.TelegramLoginSigningAlgorithm, now time.Time) (domain.TelegramLoginClient, error) {
	if botUserID <= 0 || !algorithm.Valid() {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	client, err := scanTelegramLoginClient(s.db.QueryRow(ctx, `
UPDATE bot_login_clients SET signing_algorithm = $2, updated_at = $3
WHERE bot_user_id = $1
RETURNING `+telegramLoginClientColumns, botUserID, string(algorithm), now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginClient{}, domain.ErrTelegramLoginClientInvalid
	}
	if err != nil {
		return domain.TelegramLoginClient{}, fmt.Errorf("set telegram login client signing algorithm: %w", err)
	}
	return client, nil
}

func (s *TelegramLoginStore) SetTelegramLoginClientEnabled(ctx context.Context, botUserID int64, enabled bool, now time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE bot_login_clients SET enabled = $2, updated_at = $3 WHERE bot_user_id = $1`, botUserID, enabled, now)
	if err != nil {
		return fmt.Errorf("set telegram login client enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrTelegramLoginClientInvalid
	}
	return nil
}

func scanTelegramLoginAllowedURL(row telegramLoginRowScanner) (domain.TelegramLoginAllowedURL, error) {
	var allowed domain.TelegramLoginAllowedURL
	var kind string
	if err := row.Scan(&allowed.ID, &allowed.BotUserID, &kind, &allowed.NormalizedURL, &allowed.CreatedAt); err != nil {
		return domain.TelegramLoginAllowedURL{}, err
	}
	allowed.Kind = domain.TelegramLoginAllowedURLKind(kind)
	return allowed, nil
}

func (s *TelegramLoginStore) AddTelegramLoginAllowedURL(ctx context.Context, allowed domain.TelegramLoginAllowedURL) (domain.TelegramLoginAllowedURL, error) {
	if allowed.BotUserID <= 0 || allowed.NormalizedURL == "" || (allowed.Kind != domain.TelegramLoginAllowedWebOrigin && allowed.Kind != domain.TelegramLoginAllowedRedirectURI) {
		return domain.TelegramLoginAllowedURL{}, domain.ErrTelegramLoginURLInvalid
	}
	createdAt := allowed.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO bot_login_allowed_urls (bot_user_id, kind, normalized_url, created_at)
VALUES ($1,$2,$3,$4)
ON CONFLICT (bot_user_id, kind, normalized_url) DO UPDATE
SET normalized_url = EXCLUDED.normalized_url
RETURNING id, bot_user_id, kind, normalized_url, created_at`,
		allowed.BotUserID, string(allowed.Kind), allowed.NormalizedURL, createdAt)
	out, err := scanTelegramLoginAllowedURL(row)
	if err != nil {
		return domain.TelegramLoginAllowedURL{}, mapTelegramLoginWriteError("add telegram login allowed url", err)
	}
	return out, nil
}

func (s *TelegramLoginStore) DeleteTelegramLoginAllowedURL(ctx context.Context, botUserID int64, kind domain.TelegramLoginAllowedURLKind, normalizedURL string) (bool, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return false, fmt.Errorf("delete telegram login allowed url: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("delete telegram login allowed url: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Approval and code consumption lock this same client row before their
	// final allow-list recheck. Configuration removal therefore serializes
	// with those transitions across every server instance.
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM bot_login_clients WHERE bot_user_id = $1 FOR UPDATE`, botUserID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, domain.ErrTelegramLoginClientInvalid
		}
		return false, fmt.Errorf("delete telegram login allowed url: lock client: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = $2 AND normalized_url = $3`, botUserID, string(kind), normalizedURL)
	if err != nil {
		return false, fmt.Errorf("delete telegram login allowed url: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("delete telegram login allowed url: commit: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *TelegramLoginStore) ListTelegramLoginAllowedURLs(ctx context.Context, botUserID int64) ([]domain.TelegramLoginAllowedURL, error) {
	rows, err := s.db.Query(ctx, `SELECT id, bot_user_id, kind, normalized_url, created_at FROM bot_login_allowed_urls WHERE bot_user_id = $1 ORDER BY kind, id`, botUserID)
	if err != nil {
		return nil, fmt.Errorf("list telegram login allowed urls: %w", err)
	}
	defer rows.Close()
	out := make([]domain.TelegramLoginAllowedURL, 0)
	for rows.Next() {
		allowed, err := scanTelegramLoginAllowedURL(rows)
		if err != nil {
			return nil, fmt.Errorf("scan telegram login allowed url: %w", err)
		}
		out = append(out, allowed)
	}
	return out, rows.Err()
}

func (s *TelegramLoginStore) IsTelegramLoginURLAllowed(ctx context.Context, botUserID int64, kind domain.TelegramLoginAllowedURLKind, normalizedURL string) (bool, error) {
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = $2 AND normalized_url = $3)`, botUserID, string(kind), normalizedURL).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("check telegram login allowed url: %w", err)
	}
	return allowed, nil
}

func scanTelegramLoginNativeApp(row telegramLoginRowScanner) (domain.TelegramLoginNativeApp, error) {
	var app domain.TelegramLoginNativeApp
	var platform string
	if err := row.Scan(&app.ID, &app.BotUserID, &platform, &app.ApplicationID, &app.VerificationID, &app.CallbackURI, &app.VerifiedDisplayName, &app.Enabled, &app.CreatedAt, &app.UpdatedAt); err != nil {
		return domain.TelegramLoginNativeApp{}, err
	}
	app.Platform = domain.TelegramLoginNativePlatform(platform)
	return app, nil
}

const telegramLoginNativeAppColumns = `id, bot_user_id, platform, application_id, verification_id, callback_uri, verified_display_name, enabled, created_at, updated_at`

func (s *TelegramLoginStore) UpsertTelegramLoginNativeApp(ctx context.Context, app domain.TelegramLoginNativeApp) (domain.TelegramLoginNativeApp, error) {
	if err := app.Validate(); err != nil {
		return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginClientInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.TelegramLoginNativeApp{}, fmt.Errorf("upsert telegram login native app: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.TelegramLoginNativeApp{}, fmt.Errorf("upsert telegram login native app: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM bot_login_clients WHERE bot_user_id = $1 FOR UPDATE`, app.BotUserID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginClientInvalid
		}
		return domain.TelegramLoginNativeApp{}, fmt.Errorf("upsert telegram login native app: lock client: %w", err)
	}
	if app.ID == 0 {
		var current int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM bot_login_native_apps WHERE bot_user_id = $1`, app.BotUserID).Scan(&current); err != nil {
			return domain.TelegramLoginNativeApp{}, fmt.Errorf("upsert telegram login native app: capacity: %w", err)
		}
		if current >= domain.MaxTelegramLoginNativeApps {
			// A duplicate configuration remains an idempotent update at capacity.
			var duplicate bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bot_login_native_apps WHERE bot_user_id=$1 AND platform=$2 AND application_id=$3 AND verification_id=$4)`, app.BotUserID, string(app.Platform), app.ApplicationID, app.VerificationID).Scan(&duplicate); err != nil {
				return domain.TelegramLoginNativeApp{}, fmt.Errorf("upsert telegram login native app: duplicate check: %w", err)
			}
			if !duplicate {
				return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginRequestInvalid
			}
		}
	}
	var row telegramLoginRowScanner
	if app.ID == 0 {
		row = tx.QueryRow(ctx, `
INSERT INTO bot_login_native_apps (
  bot_user_id, platform, application_id, verification_id, callback_uri,
  verified_display_name, enabled, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (bot_user_id, platform, application_id, verification_id) DO UPDATE SET
  callback_uri = EXCLUDED.callback_uri,
  verified_display_name = EXCLUDED.verified_display_name,
  enabled = EXCLUDED.enabled,
  updated_at = EXCLUDED.updated_at
RETURNING `+telegramLoginNativeAppColumns,
			app.BotUserID, string(app.Platform), app.ApplicationID, app.VerificationID,
			app.CallbackURI, app.VerifiedDisplayName, app.Enabled, app.CreatedAt, app.UpdatedAt)
	} else {
		row = tx.QueryRow(ctx, `
UPDATE bot_login_native_apps SET
  platform = $3, application_id = $4, verification_id = $5,
  callback_uri = $6, verified_display_name = $7, enabled = $8, updated_at = $9
WHERE id = $1 AND bot_user_id = $2
RETURNING `+telegramLoginNativeAppColumns,
			app.ID, app.BotUserID, string(app.Platform), app.ApplicationID, app.VerificationID,
			app.CallbackURI, app.VerifiedDisplayName, app.Enabled, app.UpdatedAt)
	}
	out, err := scanTelegramLoginNativeApp(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginNativeApp{}, domain.ErrTelegramLoginClientInvalid
	}
	if err != nil {
		return domain.TelegramLoginNativeApp{}, mapTelegramLoginWriteError("upsert telegram login native app", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TelegramLoginNativeApp{}, fmt.Errorf("upsert telegram login native app: commit: %w", err)
	}
	return out, nil
}

func (s *TelegramLoginStore) DeleteTelegramLoginNativeApp(ctx context.Context, botUserID, appID int64) (bool, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return false, fmt.Errorf("delete telegram login native app: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("delete telegram login native app: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM bot_login_clients WHERE bot_user_id = $1 FOR UPDATE`, botUserID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, domain.ErrTelegramLoginClientInvalid
		}
		return false, fmt.Errorf("delete telegram login native app: lock client: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM bot_login_native_apps WHERE id = $1 AND bot_user_id = $2`, appID, botUserID)
	if err != nil {
		return false, fmt.Errorf("delete telegram login native app: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("delete telegram login native app: commit: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *TelegramLoginStore) ListTelegramLoginNativeApps(ctx context.Context, botUserID int64) ([]domain.TelegramLoginNativeApp, error) {
	rows, err := s.db.Query(ctx, `SELECT `+telegramLoginNativeAppColumns+` FROM bot_login_native_apps WHERE bot_user_id = $1 ORDER BY id LIMIT $2`, botUserID, domain.MaxTelegramLoginNativeApps)
	if err != nil {
		return nil, fmt.Errorf("list telegram login native apps: %w", err)
	}
	defer rows.Close()
	out := make([]domain.TelegramLoginNativeApp, 0)
	for rows.Next() {
		app, err := scanTelegramLoginNativeApp(rows)
		if err != nil {
			return nil, fmt.Errorf("scan telegram login native app: %w", err)
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

const telegramLoginRequestColumns = `
id, request_token_hash, browser_token_hash, bot_user_id, client_id,
signing_algorithm, source, response_type, redirect_uri, origin, domain,
requested_scopes, oauth_state, nonce, code_challenge, code_challenge_method,
browser, platform, ip, region, in_app_origin, is_app, verified_app_name,
match_codes, match_code, match_codes_first, user_id_hint,
peer_type, peer_id, message_id, button_id,
status, authorized_user_id, profile_name, given_name, family_name,
preferred_username, picture, phone_number, write_allowed, phone_shared,
created_at, expires_at, approved_at, declined_at`

func telegramLoginScopeStrings(scopes []domain.TelegramLoginScope) []string {
	out := make([]string, len(scopes))
	for i, scope := range scopes {
		out[i] = string(scope)
	}
	return out
}

func telegramLoginScopes(values []string) []domain.TelegramLoginScope {
	out := make([]domain.TelegramLoginScope, len(values))
	for i, value := range values {
		out[i] = domain.TelegramLoginScope(value)
	}
	return out
}

func scanTelegramLoginRequest(row telegramLoginRowScanner) (domain.TelegramLoginRequest, error) {
	var request domain.TelegramLoginRequest
	var algorithm, source, status, peerType string
	var scopes []string
	var authorizedUserID sql.NullInt64
	var approvedAt, declinedAt sql.NullTime
	var messageID, buttonID int32
	if err := row.Scan(
		&request.ID, &request.RequestTokenHash, &request.BrowserTokenHash, &request.BotUserID, &request.ClientID,
		&algorithm, &source, &request.ResponseType, &request.RedirectURI, &request.Origin, &request.Domain,
		&scopes, &request.State, &request.Nonce, &request.CodeChallenge, &request.CodeChallengeMethod,
		&request.Browser, &request.Platform, &request.IP, &request.Region, &request.InAppOrigin, &request.IsApp, &request.VerifiedAppName,
		&request.MatchCodes, &request.MatchCode, &request.MatchCodesFirst, &request.UserIDHint,
		&peerType, &request.PeerID, &messageID, &buttonID,
		&status, &authorizedUserID, &request.ProfileName, &request.GivenName, &request.FamilyName,
		&request.PreferredUsername, &request.Picture, &request.PhoneNumber, &request.WriteAllowed, &request.PhoneShared,
		&request.CreatedAt, &request.ExpiresAt, &approvedAt, &declinedAt,
	); err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	request.SigningAlgorithm = domain.TelegramLoginSigningAlgorithm(algorithm)
	request.Source = domain.TelegramLoginRequestSource(source)
	request.Scopes = telegramLoginScopes(scopes)
	request.PeerType = domain.PeerType(peerType)
	request.MessageID = int(messageID)
	request.ButtonID = int(buttonID)
	request.Status = domain.TelegramLoginRequestState(status)
	if authorizedUserID.Valid {
		request.AuthorizedUserID = authorizedUserID.Int64
	}
	if approvedAt.Valid {
		request.ApprovedAt = approvedAt.Time
	}
	if declinedAt.Valid {
		request.DeclinedAt = declinedAt.Time
	}
	return request, nil
}

func (s *TelegramLoginStore) CreateTelegramLoginRequest(ctx context.Context, request domain.TelegramLoginRequest) (domain.TelegramLoginRequest, error) {
	if err := request.Validate(); err != nil {
		return domain.TelegramLoginRequest{}, err
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO telegram_login_requests (
  request_token_hash, browser_token_hash, bot_user_id, client_id,
  signing_algorithm, source, response_type, redirect_uri, origin, domain,
  requested_scopes, oauth_state, nonce, code_challenge, code_challenge_method,
  browser, platform, ip, region, in_app_origin, is_app, verified_app_name,
  match_codes, match_code, match_codes_first, user_id_hint,
  peer_type, peer_id, message_id, button_id, status, created_at, expires_at
)
SELECT
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
  $21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33
FROM bot_login_clients c
WHERE c.bot_user_id = $3 AND c.client_id = $4 AND c.enabled
  AND c.signing_algorithm = $5
RETURNING `+telegramLoginRequestColumns,
		request.RequestTokenHash, request.BrowserTokenHash, request.BotUserID, request.ClientID,
		string(request.SigningAlgorithm), string(request.Source), request.ResponseType, request.RedirectURI, request.Origin, request.Domain,
		telegramLoginScopeStrings(request.Scopes), request.State, request.Nonce, request.CodeChallenge, request.CodeChallengeMethod,
		request.Browser, request.Platform, request.IP, request.Region, request.InAppOrigin, request.IsApp, request.VerifiedAppName,
		request.MatchCodes, request.MatchCode, request.MatchCodesFirst, request.UserIDHint,
		string(request.PeerType), request.PeerID, request.MessageID, request.ButtonID, string(request.Status), request.CreatedAt, request.ExpiresAt)
	out, err := scanTelegramLoginRequest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginClientDisabled
	}
	if err != nil {
		return domain.TelegramLoginRequest{}, mapTelegramLoginWriteError("create telegram login request", err)
	}
	return out, nil
}

func (s *TelegramLoginStore) GetTelegramLoginRequest(ctx context.Context, requestID int64) (domain.TelegramLoginRequest, bool, error) {
	request, err := scanTelegramLoginRequest(s.db.QueryRow(ctx, `SELECT `+telegramLoginRequestColumns+` FROM telegram_login_requests WHERE id = $1`, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginRequest{}, false, nil
	}
	if err != nil {
		return domain.TelegramLoginRequest{}, false, fmt.Errorf("get telegram login request: %w", err)
	}
	return request, true, nil
}

func (s *TelegramLoginStore) GetTelegramLoginRequestByTokenHash(ctx context.Context, tokenHash []byte) (domain.TelegramLoginRequest, bool, error) {
	request, err := scanTelegramLoginRequest(s.db.QueryRow(ctx, `SELECT `+telegramLoginRequestColumns+` FROM telegram_login_requests WHERE request_token_hash = $1`, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginRequest{}, false, nil
	}
	if err != nil {
		return domain.TelegramLoginRequest{}, false, fmt.Errorf("get telegram login request by token: %w", err)
	}
	return request, true, nil
}

func (s *TelegramLoginStore) GetTelegramLoginRequestByBrowserTokenHash(ctx context.Context, tokenHash []byte) (domain.TelegramLoginRequest, bool, error) {
	request, err := scanTelegramLoginRequest(s.db.QueryRow(ctx, `SELECT `+telegramLoginRequestColumns+` FROM telegram_login_requests WHERE browser_token_hash = $1`, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginRequest{}, false, nil
	}
	if err != nil {
		return domain.TelegramLoginRequest{}, false, fmt.Errorf("get telegram login request by browser token: %w", err)
	}
	return request, true, nil
}

func (s *TelegramLoginStore) ApproveTelegramLoginRequest(ctx context.Context, approval domain.TelegramLoginApproval, webAuthorizationHash int64) (domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error) {
	if approval.RequestID <= 0 || approval.Identity.UserID <= 0 || webAuthorizationHash == 0 || approval.ApprovedAt.IsZero() {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	request, err := scanTelegramLoginRequest(tx.QueryRow(ctx, `SELECT `+telegramLoginRequestColumns+` FROM telegram_login_requests WHERE id = $1 FOR UPDATE`, approval.RequestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestInvalid
	}
	if err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: lock: %w", err)
	}
	if request.Status != domain.TelegramLoginRequestPending {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestConflict
	}
	if !approval.ApprovedAt.Before(request.ExpiresAt) {
		if _, err := tx.Exec(ctx, `UPDATE telegram_login_requests SET status = 'expired' WHERE id = $1 AND status = 'pending'`, request.ID); err != nil {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: mark expired: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: commit expiry: %w", err)
		}
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestExpired
	}
	var clientEnabled, redirectAllowed, originAllowed bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM bot_login_clients WHERE bot_user_id = $1 AND client_id = $2 AND signing_algorithm = $3 FOR UPDATE`, request.BotUserID, request.ClientID, string(request.SigningAlgorithm)).Scan(&clientEnabled); err != nil || !clientEnabled {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: client: %w", err)
		}
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginClientDisabled
	}
	redirectAllowed = request.ResponseType != "code"
	if request.ResponseType == "code" {
		if err := tx.QueryRow(ctx, `SELECT
  EXISTS(SELECT 1 FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = 'redirect_uri' AND normalized_url = $2)
  OR ($3 = 'native' AND EXISTS(SELECT 1 FROM bot_login_native_apps WHERE bot_user_id = $1 AND callback_uri = $2 AND enabled))`, request.BotUserID, request.RedirectURI, string(request.Source)).Scan(&redirectAllowed); err != nil {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: redirect: %w", err)
		}
		if !redirectAllowed {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRedirectNotAllowed
		}
	} else if request.ResponseType == "post_message" || request.ResponseType == "legacy_url" {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = 'web_origin' AND normalized_url = $2)`, request.BotUserID, request.Origin).Scan(&originAllowed); err != nil {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: post-message origin: %w", err)
		}
		if !originAllowed {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
		}
	} else {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginRequestInvalid
	}
	originAllowed = request.InAppOrigin == ""
	if !originAllowed {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = 'web_origin' AND normalized_url = $2)`, request.BotUserID, request.InAppOrigin).Scan(&originAllowed); err != nil {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: origin: %w", err)
		}
	}
	if !originAllowed {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginOriginNotAllowed
	}
	if len(request.MatchCodes) > 0 && approval.MatchCode != request.MatchCode {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginMatchCodeInvalid
	}
	scopes, err := grantedTelegramLoginScopesPG(request, approval)
	if err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, err
	}
	identity, err := approval.Identity.Sanitized(request.Requests(domain.TelegramLoginScopeProfile), approval.PhoneShared)
	if err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, err
	}
	// Serialize the per-user capacity check across requests and server
	// instances. This prevents unbounded account.getWebAuthorizations payloads.
	const telegramLoginAuthorizationLockNamespace int64 = 0x544c000000000000
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, identity.UserID^telegramLoginAuthorizationLockNamespace); err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: authorization capacity lock: %w", err)
	}
	var activeAuthorizations int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM web_authorizations WHERE user_id = $1 AND revoked_at IS NULL`, identity.UserID).Scan(&activeAuthorizations); err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: authorization capacity: %w", err)
	}
	if activeAuthorizations >= domain.MaxTelegramLoginWebAuthorizations {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginAuthorizationsTooMany
	}
	if approval.WriteAllowed {
		if _, err := tx.Exec(ctx, `
INSERT INTO bot_user_permissions (bot_user_id, user_id, from_request)
VALUES ($1,$2,true)
ON CONFLICT (bot_user_id, user_id) DO UPDATE SET
  from_request = true, updated_at = now()`, request.BotUserID, identity.UserID); err != nil {
			return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: grant bot access: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE telegram_login_requests SET
  status = 'approved', authorized_user_id = $2, write_allowed = $3,
  phone_shared = $4, approved_at = $5, profile_name = $6, given_name = $7,
  family_name = $8, preferred_username = $9, picture = $10, phone_number = $11
WHERE id = $1`, request.ID, identity.UserID, approval.WriteAllowed, approval.PhoneShared, approval.ApprovedAt,
		identity.Name, identity.GivenName, identity.FamilyName, identity.PreferredUsername, identity.Picture, identity.PhoneNumber); err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: update request: %w", err)
	}
	web := domain.TelegramLoginWebAuthorization{
		Hash: webAuthorizationHash, RequestID: request.ID, UserID: identity.UserID, BotUserID: request.BotUserID,
		Domain: request.Domain, Browser: request.Browser, Platform: request.Platform, IP: request.IP, Region: request.Region,
		Scopes: scopes, PhoneShared: approval.PhoneShared, BotAccessGranted: approval.WriteAllowed,
		CreatedAt: approval.ApprovedAt, LastActiveAt: approval.ApprovedAt,
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO web_authorizations (
  hash, request_id, user_id, bot_user_id, domain, browser, platform, ip, region,
  granted_scopes, phone_shared, bot_access_granted, created_at, last_active_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		web.Hash, web.RequestID, web.UserID, web.BotUserID, web.Domain, web.Browser, web.Platform, web.IP, web.Region,
		telegramLoginScopeStrings(web.Scopes), web.PhoneShared, web.BotAccessGranted, web.CreatedAt, web.LastActiveAt); err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, mapTelegramLoginWriteError("approve telegram login request: insert web authorization", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("approve telegram login request: commit: %w", err)
	}
	request.Status = domain.TelegramLoginRequestApproved
	request.AuthorizedUserID = identity.UserID
	request.ProfileName = identity.Name
	request.GivenName = identity.GivenName
	request.FamilyName = identity.FamilyName
	request.PreferredUsername = identity.PreferredUsername
	request.Picture = identity.Picture
	request.PhoneNumber = identity.PhoneNumber
	request.WriteAllowed = approval.WriteAllowed
	request.PhoneShared = approval.PhoneShared
	request.ApprovedAt = approval.ApprovedAt
	return request, web, nil
}

func grantedTelegramLoginScopesPG(request domain.TelegramLoginRequest, approval domain.TelegramLoginApproval) ([]domain.TelegramLoginScope, error) {
	if approval.WriteAllowed && !request.Requests(domain.TelegramLoginScopeBotAccess) {
		return nil, domain.ErrTelegramLoginScopeInvalid
	}
	if approval.PhoneShared && !request.Requests(domain.TelegramLoginScopePhone) {
		return nil, domain.ErrTelegramLoginScopeInvalid
	}
	out := make([]domain.TelegramLoginScope, 0, len(request.Scopes))
	for _, scope := range request.Scopes {
		if scope == domain.TelegramLoginScopePhone && !approval.PhoneShared {
			continue
		}
		if scope == domain.TelegramLoginScopeBotAccess && !approval.WriteAllowed {
			continue
		}
		out = append(out, scope)
	}
	return out, nil
}

func (s *TelegramLoginStore) DeclineTelegramLoginRequest(ctx context.Context, requestID, userID int64, now time.Time) (domain.TelegramLoginRequest, error) {
	if requestID <= 0 || userID <= 0 || now.IsZero() {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.TelegramLoginRequest{}, fmt.Errorf("decline telegram login request: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.TelegramLoginRequest{}, fmt.Errorf("decline telegram login request: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	request, err := scanTelegramLoginRequest(tx.QueryRow(ctx, `SELECT `+telegramLoginRequestColumns+` FROM telegram_login_requests WHERE id = $1 FOR UPDATE`, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestInvalid
	}
	if err != nil {
		return domain.TelegramLoginRequest{}, fmt.Errorf("decline telegram login request: lock: %w", err)
	}
	if request.Status != domain.TelegramLoginRequestPending {
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestConflict
	}
	if !now.Before(request.ExpiresAt) {
		if _, err := tx.Exec(ctx, `UPDATE telegram_login_requests SET status = 'expired' WHERE id = $1`, request.ID); err != nil {
			return domain.TelegramLoginRequest{}, fmt.Errorf("decline telegram login request: mark expired: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.TelegramLoginRequest{}, fmt.Errorf("decline telegram login request: commit expiry: %w", err)
		}
		return domain.TelegramLoginRequest{}, domain.ErrTelegramLoginRequestExpired
	}
	if _, err := tx.Exec(ctx, `UPDATE telegram_login_requests SET status = 'declined', declined_at = $2 WHERE id = $1`, request.ID, now); err != nil {
		return domain.TelegramLoginRequest{}, fmt.Errorf("decline telegram login request: update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TelegramLoginRequest{}, fmt.Errorf("decline telegram login request: commit: %w", err)
	}
	request.Status = domain.TelegramLoginRequestDeclined
	request.DeclinedAt = now
	return request, nil
}

const telegramLoginCodeColumns = `id, request_id, code_hash, sealed_code, seal_nonce, seal_key_id, issued_at, expires_at, consumed_at`

func scanTelegramLoginAuthorizationCode(row telegramLoginRowScanner) (domain.TelegramLoginAuthorizationCode, error) {
	var code domain.TelegramLoginAuthorizationCode
	var consumedAt sql.NullTime
	if err := row.Scan(&code.ID, &code.RequestID, &code.CodeHash, &code.SealedCode, &code.SealNonce, &code.SealKeyID, &code.IssuedAt, &code.ExpiresAt, &consumedAt); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, err
	}
	if consumedAt.Valid {
		code.ConsumedAt = consumedAt.Time
	}
	return code, nil
}

func (s *TelegramLoginStore) PutTelegramLoginAuthorizationCode(ctx context.Context, code domain.TelegramLoginAuthorizationCode) (domain.TelegramLoginAuthorizationCode, error) {
	if code.RequestID <= 0 || len(code.CodeHash) != 32 || len(code.SealedCode) < 32 || len(code.SealNonce) < 12 || code.SealKeyID == "" || code.IssuedAt.IsZero() || !code.ExpiresAt.After(code.IssuedAt) {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginCodeInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	request, err := scanTelegramLoginRequest(tx.QueryRow(ctx, `SELECT `+telegramLoginRequestColumns+` FROM telegram_login_requests WHERE id = $1 FOR UPDATE`, code.RequestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRequestInvalid
	} else if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: lock request: %w", err)
	}
	if request.Status != domain.TelegramLoginRequestApproved {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRequestConflict
	}
	var clientEnabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM bot_login_clients WHERE bot_user_id = $1 AND client_id = $2 AND signing_algorithm = $3 FOR UPDATE`,
		request.BotUserID, request.ClientID, string(request.SigningAlgorithm)).Scan(&clientEnabled); err != nil || !clientEnabled {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: client: %w", err)
		}
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginClientDisabled
	}
	switch request.ResponseType {
	case "code":
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT
  EXISTS(SELECT 1 FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = 'redirect_uri' AND normalized_url = $2)
  OR ($3 = 'native' AND EXISTS(SELECT 1 FROM bot_login_native_apps WHERE bot_user_id = $1 AND callback_uri = $2 AND enabled))`,
			request.BotUserID, request.RedirectURI, string(request.Source)).Scan(&allowed); err != nil {
			return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: redirect: %w", err)
		}
		if !allowed {
			return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRedirectNotAllowed
		}
	case "post_message":
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = 'web_origin' AND normalized_url = $2)`,
			request.BotUserID, request.Origin).Scan(&allowed); err != nil {
			return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: origin: %w", err)
		}
		if !allowed {
			return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginOriginNotAllowed
		}
	default:
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRequestConflict
	}
	web, err := scanTelegramLoginWebAuthorization(tx.QueryRow(ctx, `SELECT `+telegramLoginWebAuthorizationColumns+` FROM web_authorizations WHERE request_id = $1 FOR UPDATE`, code.RequestID))
	if errors.Is(err, pgx.ErrNoRows) || !web.RevokedAt.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.ErrTelegramLoginRequestConflict
	}
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: web authorization: %w", err)
	}
	existing, err := scanTelegramLoginAuthorizationCode(tx.QueryRow(ctx, `SELECT `+telegramLoginCodeColumns+` FROM telegram_login_codes WHERE request_id = $1`, code.RequestID))
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: commit existing: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: read existing: %w", err)
	}
	created, err := scanTelegramLoginAuthorizationCode(tx.QueryRow(ctx, `
INSERT INTO telegram_login_codes (
  request_id, code_hash, sealed_code, seal_nonce, seal_key_id, issued_at, expires_at
) VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING `+telegramLoginCodeColumns,
		code.RequestID, code.CodeHash, code.SealedCode, code.SealNonce, code.SealKeyID, code.IssuedAt, code.ExpiresAt))
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, mapTelegramLoginWriteError("put telegram login code", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, fmt.Errorf("put telegram login code: commit: %w", err)
	}
	return created, nil
}

func (s *TelegramLoginStore) GetTelegramLoginAuthorizationCodeByRequest(ctx context.Context, requestID int64) (domain.TelegramLoginAuthorizationCode, bool, error) {
	code, err := scanTelegramLoginAuthorizationCode(s.db.QueryRow(ctx, `SELECT `+telegramLoginCodeColumns+` FROM telegram_login_codes WHERE request_id = $1`, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginAuthorizationCode{}, false, nil
	}
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, false, fmt.Errorf("get telegram login code by request: %w", err)
	}
	return code, true, nil
}

func (s *TelegramLoginStore) GetTelegramLoginAuthorizationCodeByHash(ctx context.Context, codeHash []byte) (domain.TelegramLoginAuthorizationCode, bool, error) {
	code, err := scanTelegramLoginAuthorizationCode(s.db.QueryRow(ctx, `SELECT `+telegramLoginCodeColumns+` FROM telegram_login_codes WHERE code_hash = $1`, codeHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginAuthorizationCode{}, false, nil
	}
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, false, fmt.Errorf("get telegram login code by hash: %w", err)
	}
	return code, true, nil
}

const telegramLoginWebAuthorizationColumns = `hash, request_id, user_id, bot_user_id, domain, browser, platform, ip, region, granted_scopes, phone_shared, bot_access_granted, created_at, last_active_at, revoked_at`

func scanTelegramLoginWebAuthorization(row telegramLoginRowScanner) (domain.TelegramLoginWebAuthorization, error) {
	var web domain.TelegramLoginWebAuthorization
	var scopes []string
	var revokedAt sql.NullTime
	if err := row.Scan(&web.Hash, &web.RequestID, &web.UserID, &web.BotUserID, &web.Domain, &web.Browser, &web.Platform, &web.IP, &web.Region, &scopes, &web.PhoneShared, &web.BotAccessGranted, &web.CreatedAt, &web.LastActiveAt, &revokedAt); err != nil {
		return domain.TelegramLoginWebAuthorization{}, err
	}
	web.Scopes = telegramLoginScopes(scopes)
	if revokedAt.Valid {
		web.RevokedAt = revokedAt.Time
	}
	return web, nil
}

func (s *TelegramLoginStore) ConsumeTelegramLoginAuthorizationCode(ctx context.Context, exchange domain.TelegramLoginCodeExchange) (domain.TelegramLoginAuthorizationCode, domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error) {
	if len(exchange.CodeHash) != 32 || exchange.ClientID == "" || exchange.ClientSecretVersion <= 0 || exchange.RedirectURI == "" || exchange.CodeChallenge == "" || exchange.Now.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	code, err := scanTelegramLoginAuthorizationCode(tx.QueryRow(ctx, `SELECT `+telegramLoginCodeColumns+` FROM telegram_login_codes WHERE code_hash = $1 FOR UPDATE`, exchange.CodeHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: lock code: %w", err)
	}
	if !code.ConsumedAt.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeConsumed
	}
	if !exchange.Now.Before(code.ExpiresAt) {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	request, err := scanTelegramLoginRequest(tx.QueryRow(ctx, `SELECT `+telegramLoginRequestColumns+` FROM telegram_login_requests WHERE id = $1 FOR UPDATE`, code.RequestID))
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: lock request: %w", err)
	}
	if request.Status != domain.TelegramLoginRequestApproved || request.ResponseType != "code" || request.ClientID != exchange.ClientID || request.RedirectURI != exchange.RedirectURI || request.CodeChallenge != exchange.CodeChallenge {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	var clientEnabled, redirectAllowed bool
	var secretVersion int64
	if err := tx.QueryRow(ctx, `SELECT enabled, secret_version FROM bot_login_clients WHERE client_id = $1 FOR UPDATE`, exchange.ClientID).Scan(&clientEnabled, &secretVersion); err != nil || !clientEnabled || secretVersion != exchange.ClientSecretVersion {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: client: %w", err)
		}
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	if err := tx.QueryRow(ctx, `SELECT
  EXISTS(SELECT 1 FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = 'redirect_uri' AND normalized_url = $2)
  OR ($3 = 'native' AND EXISTS(SELECT 1 FROM bot_login_native_apps WHERE bot_user_id = $1 AND callback_uri = $2 AND enabled))`, request.BotUserID, exchange.RedirectURI, string(request.Source)).Scan(&redirectAllowed); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: redirect: %w", err)
	}
	if !redirectAllowed {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	web, err := scanTelegramLoginWebAuthorization(tx.QueryRow(ctx, `SELECT `+telegramLoginWebAuthorizationColumns+` FROM web_authorizations WHERE request_id = $1 FOR UPDATE`, request.ID))
	if err != nil || !web.RevokedAt.IsZero() {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: web authorization: %w", err)
		}
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE telegram_login_codes SET consumed_at = $2 WHERE id = $1`, code.ID, exchange.Now); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: mark consumed: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE web_authorizations SET last_active_at = $2 WHERE hash = $1`, web.Hash, exchange.Now); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: touch web authorization: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login code: commit: %w", err)
	}
	code.ConsumedAt = exchange.Now
	web.LastActiveAt = exchange.Now
	return code, request, web, nil
}

func (s *TelegramLoginStore) ConsumeTelegramLoginDirectToken(ctx context.Context, tokenHash []byte, origin string, now time.Time) (domain.TelegramLoginAuthorizationCode, domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error) {
	if len(tokenHash) != 32 || origin == "" || now.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	code, err := scanTelegramLoginAuthorizationCode(tx.QueryRow(ctx, `SELECT `+telegramLoginCodeColumns+` FROM telegram_login_codes WHERE code_hash = $1 FOR UPDATE`, tokenHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: lock token: %w", err)
	}
	if !code.ConsumedAt.IsZero() {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeConsumed
	}
	if !now.Before(code.ExpiresAt) {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	request, err := scanTelegramLoginRequest(tx.QueryRow(ctx, `SELECT `+telegramLoginRequestColumns+` FROM telegram_login_requests WHERE id = $1 FOR UPDATE`, code.RequestID))
	if err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: lock request: %w", err)
	}
	if request.Status != domain.TelegramLoginRequestApproved || request.Source != domain.TelegramLoginRequestMiniApp ||
		request.ResponseType != "post_message" || request.Origin != origin || request.InAppOrigin != origin {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	var clientEnabled, originAllowed bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM bot_login_clients WHERE bot_user_id = $1 AND client_id = $2 FOR UPDATE`, request.BotUserID, request.ClientID).Scan(&clientEnabled); err != nil || !clientEnabled {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: client: %w", err)
		}
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bot_login_allowed_urls WHERE bot_user_id = $1 AND kind = 'web_origin' AND normalized_url = $2)`, request.BotUserID, origin).Scan(&originAllowed); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: origin: %w", err)
	}
	if !originAllowed {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	web, err := scanTelegramLoginWebAuthorization(tx.QueryRow(ctx, `SELECT `+telegramLoginWebAuthorizationColumns+` FROM web_authorizations WHERE request_id = $1 FOR UPDATE`, request.ID))
	if err != nil || !web.RevokedAt.IsZero() {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: web authorization: %w", err)
		}
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, domain.ErrTelegramLoginCodeInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE telegram_login_codes SET consumed_at = $2 WHERE id = $1`, code.ID, now); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: mark consumed: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE web_authorizations SET last_active_at = $2 WHERE hash = $1`, web.Hash, now); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: touch web authorization: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TelegramLoginAuthorizationCode{}, domain.TelegramLoginRequest{}, domain.TelegramLoginWebAuthorization{}, fmt.Errorf("consume telegram login direct token: commit: %w", err)
	}
	code.ConsumedAt = now
	web.LastActiveAt = now
	return code, request, web, nil
}

func (s *TelegramLoginStore) ListTelegramLoginWebAuthorizations(ctx context.Context, userID int64) ([]domain.TelegramLoginWebAuthorization, error) {
	rows, err := s.db.Query(ctx, `SELECT `+telegramLoginWebAuthorizationColumns+` FROM web_authorizations WHERE user_id = $1 AND revoked_at IS NULL ORDER BY last_active_at DESC, hash DESC LIMIT $2`, userID, domain.MaxTelegramLoginWebAuthorizations)
	if err != nil {
		return nil, fmt.Errorf("list telegram login web authorizations: %w", err)
	}
	defer rows.Close()
	out := make([]domain.TelegramLoginWebAuthorization, 0)
	for rows.Next() {
		web, err := scanTelegramLoginWebAuthorization(rows)
		if err != nil {
			return nil, fmt.Errorf("scan telegram login web authorization: %w", err)
		}
		out = append(out, web)
	}
	return out, rows.Err()
}

func (s *TelegramLoginStore) RevokeTelegramLoginWebAuthorization(ctx context.Context, userID, hash int64, now time.Time) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE web_authorizations SET revoked_at = $3 WHERE user_id = $1 AND hash = $2 AND revoked_at IS NULL`, userID, hash, now)
	if err != nil {
		return false, fmt.Errorf("revoke telegram login web authorization: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *TelegramLoginStore) RevokeAllTelegramLoginWebAuthorizations(ctx context.Context, userID int64, now time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE web_authorizations SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, now)
	if err != nil {
		return 0, fmt.Errorf("revoke all telegram login web authorizations: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *TelegramLoginStore) DeleteExpiredTelegramLoginArtifacts(ctx context.Context, before time.Time, limit int) (int64, error) {
	if limit <= 0 || limit > 1000 {
		return 0, domain.ErrTelegramLoginRequestInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return 0, fmt.Errorf("delete expired telegram login artifacts: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete expired telegram login artifacts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var deletedCodes int64
	if err := tx.QueryRow(ctx, `
WITH doomed AS (
  SELECT id FROM telegram_login_codes
  WHERE expires_at < $1 OR (consumed_at IS NOT NULL AND consumed_at < $1)
  ORDER BY expires_at, id
  LIMIT $2
  FOR UPDATE SKIP LOCKED
), deleted AS (
  DELETE FROM telegram_login_codes c USING doomed d WHERE c.id = d.id RETURNING c.id
)
SELECT count(*) FROM deleted`, before, limit).Scan(&deletedCodes); err != nil {
		return 0, fmt.Errorf("delete expired telegram login codes: %w", err)
	}
	remaining := int64(limit) - deletedCodes
	var deletedRequests int64
	if remaining > 0 {
		if err := tx.QueryRow(ctx, `
WITH doomed AS (
  SELECT r.id FROM telegram_login_requests r
  WHERE (r.status IN ('pending','declined','expired') AND r.expires_at < $1)
     OR (r.status = 'approved' AND r.approved_at < $1
         AND EXISTS (
           SELECT 1 FROM web_authorizations w
           WHERE w.request_id = r.id AND w.revoked_at < $1
         )
         AND NOT EXISTS (
           SELECT 1 FROM telegram_login_codes c WHERE c.request_id = r.id
         ))
  ORDER BY COALESCE(r.approved_at, r.expires_at), r.id
  LIMIT $2
  FOR UPDATE OF r SKIP LOCKED
), deleted AS (
  DELETE FROM telegram_login_requests r USING doomed d WHERE r.id = d.id RETURNING r.id
)
SELECT count(*) FROM deleted`, before, remaining).Scan(&deletedRequests); err != nil {
			return 0, fmt.Errorf("delete expired telegram login requests: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("delete expired telegram login artifacts: commit: %w", err)
	}
	return deletedCodes + deletedRequests, nil
}

func mapTelegramLoginWriteError(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgerrcode.UniqueViolation:
			return fmt.Errorf("%s: %w", op, domain.ErrTelegramLoginRequestConflict)
		case pgerrcode.ForeignKeyViolation, pgerrcode.CheckViolation:
			return fmt.Errorf("%s: %w", op, domain.ErrTelegramLoginRequestInvalid)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}
