package store

import (
	"context"

	"telesrv/internal/domain"
)

// BotVerificationStore owns third-party verification: the icon catalogue, verifier
// status, the granted marks and the application queue in front of them.
//
// Reads on the projection path (PeerVerification / PeerVerificationBatch) are on
// every peer serialisation, so they must be cheap. The wire model carries one
// BotVerification, and the store therefore permits exactly one mark per peer.
type BotVerificationStore interface {
	// --- icon catalogue ---

	// UpsertVerificationIcon adds or updates a catalogue entry by document id.
	UpsertVerificationIcon(ctx context.Context, icon domain.VerificationIcon) (domain.VerificationIcon, error)
	// SetVerificationIconActive retires or restores an entry. Marks already granted
	// with it keep rendering: the icon id is denormalised onto the mark.
	SetVerificationIconActive(ctx context.Context, iconID int64, active bool) (domain.VerificationIcon, error)
	// VerificationIcon reads one entry by id.
	VerificationIcon(ctx context.Context, iconID int64) (domain.VerificationIcon, error)
	// VerificationIconByDocument reads one entry by its custom emoji document id.
	VerificationIconByDocument(ctx context.Context, documentID int64) (domain.VerificationIcon, error)
	// ListVerificationIcons lists the catalogue, newest first.
	ListVerificationIcons(ctx context.Context, activeOnly bool, limit int) ([]domain.VerificationIcon, error)

	// --- verifier status ---

	// UpsertBotVerifierSettings grants or updates verifier status. Optimistic
	// locking on the stored version keeps two operators from clobbering each other.
	UpsertBotVerifierSettings(ctx context.Context, settings domain.BotVerifierSettings) (domain.BotVerifierSettings, error)
	// SetBotVerifierEnabled flips the operator kill switch. Existing marks stay,
	// but the verifier can grant nothing new and its settings stop being projected.
	SetBotVerifierEnabled(ctx context.Context, botID int64, enabled bool) (domain.BotVerifierSettings, error)
	// DeleteBotVerifierSettings removes verifier status; its marks cascade away with
	// it, because a mark whose verifier no longer exists has nothing to render.
	DeleteBotVerifierSettings(ctx context.Context, botID int64) (bool, error)
	// BotVerifierSettings reads one verifier's status, enabled or not.
	BotVerifierSettings(ctx context.Context, botID int64) (domain.BotVerifierSettings, error)
	// BotVerifierSettingsBatch resolves several bots in one round trip for the
	// botInfo projection; bots without verifier status are absent from the map.
	BotVerifierSettingsBatch(ctx context.Context, botIDs []int64) (map[int64]domain.BotVerifierSettings, error)
	// ListBotVerifiers lists verifier bots for the admin panel.
	ListBotVerifiers(ctx context.Context, enabledOnly bool, limit int) ([]domain.BotVerifierSettings, error)

	// --- granted marks ---

	// GrantCustomVerification creates or updates the peer's mark. A different
	// verifier replaces the current mark rather than leaving hidden fallback state.
	// The icon is taken from the verifier's settings at grant time and the caller
	// has already resolved the description through
	// domain.BotVerifierSettings.DescriptionFor.
	GrantCustomVerification(ctx context.Context, mark domain.CustomVerification) (domain.CustomVerification, bool, error)
	// RevokeCustomVerification removes this verifier's mark from the peer and
	// reports whether anything was removed, so a repeated revoke is a no-op.
	RevokeCustomVerification(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error)
	// CustomVerification reads one verifier's mark on a peer.
	CustomVerification(ctx context.Context, verifierBotID int64, peer domain.Peer) (domain.CustomVerification, error)
	// PeerVerification returns the peer's single mark. Missing marks report
	// domain.ErrCustomVerificationNotFound.
	PeerVerification(ctx context.Context, peer domain.Peer) (domain.CustomVerification, error)
	// PeerVerificationBatch resolves the projection for many peers at once. This is
	// the call on the hot serialisation path, so peers without a mark are simply
	// absent instead of erroring.
	PeerVerificationBatch(ctx context.Context, peers []domain.Peer) (map[domain.Peer]domain.CustomVerification, error)
	// CountCustomVerifications reports how many peers a verifier has marked, for the
	// per-verifier bound.
	CountCustomVerifications(ctx context.Context, verifierBotID int64) (int, error)
	// ListCustomVerifications is the admin listing query with keyset paging.
	ListCustomVerifications(ctx context.Context, filter domain.CustomVerificationFilter) ([]domain.CustomVerification, error)

	// --- application queue ---

	// CreateCustomVerificationRequest files an application. A pending application on
	// the same (verifier, peer) reports domain.ErrCustomVerificationRequestExists.
	CreateCustomVerificationRequest(ctx context.Context, req domain.CustomVerificationRequest) (domain.CustomVerificationRequest, error)
	// DecideCustomVerificationRequest moves an application through its status
	// machine. approve=true grants the mark in the same transaction through the
	// supplied callback, so an approved application can never exist without its
	// mark; revoke removes it the same way.
	DecideCustomVerificationRequest(ctx context.Context, requestID int64, version int64, status domain.CustomVerificationRequestStatus, decidedBy, reason, note string, apply func(ctx context.Context, req domain.CustomVerificationRequest) error) (domain.CustomVerificationRequest, bool, error)
	// CustomVerificationRequest reads one application.
	CustomVerificationRequest(ctx context.Context, requestID int64) (domain.CustomVerificationRequest, error)
	// PendingCustomVerificationRequest returns the live application for a
	// (verifier, peer) pair, if any.
	PendingCustomVerificationRequest(ctx context.Context, verifierBotID int64, peer domain.Peer) (domain.CustomVerificationRequest, error)
	// ListCustomVerificationRequests is the review-queue query with keyset paging.
	ListCustomVerificationRequests(ctx context.Context, filter domain.CustomVerificationRequestFilter) ([]domain.CustomVerificationRequest, error)
	// CustomVerificationRequestsForApplicant returns an applicant's own history for
	// the verifier bot's /status command.
	CustomVerificationRequestsForApplicant(ctx context.Context, applicantUserID int64, limit int) ([]domain.CustomVerificationRequest, error)
	// CustomVerificationRequestCounts is the queue summary by status.
	CustomVerificationRequestCounts(ctx context.Context) (map[domain.CustomVerificationRequestStatus]int64, error)
}
