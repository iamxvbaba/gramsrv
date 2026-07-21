package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// TelegramLoginStore is the single durable boundary shared by the HTTP OIDC
// adapter, MTProto URL-authorization RPCs and account Web-authorization RPCs.
// Implementations must use compare-and-set transitions and must not treat an
// in-memory cache as the source of truth.
type TelegramLoginStore interface {
	CreateTelegramLoginClient(ctx context.Context, client domain.TelegramLoginClient) (domain.TelegramLoginClient, error)
	UpsertTelegramLoginClient(ctx context.Context, client domain.TelegramLoginClient) (domain.TelegramLoginClient, error)
	GetTelegramLoginClient(ctx context.Context, clientID string) (domain.TelegramLoginClient, bool, error)
	GetTelegramLoginClientByBot(ctx context.Context, botUserID int64) (domain.TelegramLoginClient, bool, error)
	RotateTelegramLoginClientSecret(ctx context.Context, botUserID, expectedVersion int64, secretHash []byte, now time.Time) (domain.TelegramLoginClient, error)
	SetTelegramLoginClientSigningAlgorithm(ctx context.Context, botUserID int64, algorithm domain.TelegramLoginSigningAlgorithm, now time.Time) (domain.TelegramLoginClient, error)
	SetTelegramLoginClientEnabled(ctx context.Context, botUserID int64, enabled bool, now time.Time) error

	AddTelegramLoginAllowedURL(ctx context.Context, allowed domain.TelegramLoginAllowedURL) (domain.TelegramLoginAllowedURL, error)
	DeleteTelegramLoginAllowedURL(ctx context.Context, botUserID int64, kind domain.TelegramLoginAllowedURLKind, normalizedURL string) (bool, error)
	ListTelegramLoginAllowedURLs(ctx context.Context, botUserID int64) ([]domain.TelegramLoginAllowedURL, error)
	IsTelegramLoginURLAllowed(ctx context.Context, botUserID int64, kind domain.TelegramLoginAllowedURLKind, normalizedURL string) (bool, error)

	UpsertTelegramLoginNativeApp(ctx context.Context, app domain.TelegramLoginNativeApp) (domain.TelegramLoginNativeApp, error)
	DeleteTelegramLoginNativeApp(ctx context.Context, botUserID, appID int64) (bool, error)
	ListTelegramLoginNativeApps(ctx context.Context, botUserID int64) ([]domain.TelegramLoginNativeApp, error)

	CreateTelegramLoginRequest(ctx context.Context, request domain.TelegramLoginRequest) (domain.TelegramLoginRequest, error)
	GetTelegramLoginRequest(ctx context.Context, requestID int64) (domain.TelegramLoginRequest, bool, error)
	GetTelegramLoginRequestByTokenHash(ctx context.Context, tokenHash []byte) (domain.TelegramLoginRequest, bool, error)
	GetTelegramLoginRequestByBrowserTokenHash(ctx context.Context, tokenHash []byte) (domain.TelegramLoginRequest, bool, error)
	ApproveTelegramLoginRequest(ctx context.Context, approval domain.TelegramLoginApproval, webAuthorizationHash int64) (domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error)
	DeclineTelegramLoginRequest(ctx context.Context, requestID, userID int64, now time.Time) (domain.TelegramLoginRequest, error)

	PutTelegramLoginAuthorizationCode(ctx context.Context, code domain.TelegramLoginAuthorizationCode) (domain.TelegramLoginAuthorizationCode, error)
	GetTelegramLoginAuthorizationCodeByRequest(ctx context.Context, requestID int64) (domain.TelegramLoginAuthorizationCode, bool, error)
	GetTelegramLoginAuthorizationCodeByHash(ctx context.Context, codeHash []byte) (domain.TelegramLoginAuthorizationCode, bool, error)
	ConsumeTelegramLoginAuthorizationCode(ctx context.Context, exchange domain.TelegramLoginCodeExchange) (domain.TelegramLoginAuthorizationCode, domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error)
	ConsumeTelegramLoginDirectToken(ctx context.Context, tokenHash []byte, origin string, now time.Time) (domain.TelegramLoginAuthorizationCode, domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error)

	ListTelegramLoginWebAuthorizations(ctx context.Context, userID int64) ([]domain.TelegramLoginWebAuthorization, error)
	RevokeTelegramLoginWebAuthorization(ctx context.Context, userID, hash int64, now time.Time) (bool, error)
	RevokeAllTelegramLoginWebAuthorizations(ctx context.Context, userID int64, now time.Time) (int64, error)
	DeleteExpiredTelegramLoginArtifacts(ctx context.Context, before time.Time, limit int) (int64, error)
}
