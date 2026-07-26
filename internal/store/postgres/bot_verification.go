package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// BotVerificationStore is the PostgreSQL implementation of third-party bot
// verification (migration 0154): the icon catalogue, verifier status, the granted
// marks and the application queue in front of them.
//
// Four properties are load bearing and every method below exists to keep them:
//
//   - The projection is deterministic and cheap. PeerVerification and
//     PeerVerificationBatch run on every peer serialisation, so the batch form is
//     a single query for all peers (no N+1) and both forms resolve the same mark
//     when several verifiers marked one peer: ORDER BY id DESC, the tail of
//     custom_verifications_peer_idx. Without that ordering a peer would flicker
//     between icons across responses.
//   - A disabled verifier projects nothing. Both projection reads join
//     bot_verifier_settings and keep only enabled verifiers, which is the
//     operator kill switch: flipping enabled off darkens every badge that
//     verifier granted while the rows stay on disk, so flipping it back restores
//     them unchanged.
//   - "approved implies the mark exists". DecideCustomVerificationRequest changes
//     the application status and grants (or revokes) the mark in ONE transaction:
//     the caller-supplied apply callback runs inside it and must write through
//     VerificationTxFromContext, so a callback error rolls the decision back and
//     an approved application without its mark cannot exist.
//   - Exactly one decision per application and one settings writer at a time.
//     Every mutation is guarded by WHERE ... AND version = $n on top of a
//     SELECT ... FOR UPDATE, so the loser of a race gets
//     domain.ErrCustomVerificationVersionConflict instead of silently clobbering.
//
// Status transitions are never re-implemented in SQL: they all go through
// domain.CanTransitionCustomVerificationStatus. Re-issuing a decision that
// already holds is reported as changed=false without touching the row.
//
// Two deliberate non-responsibilities: the store does not check
// icon_document_id against the catalogue (0154 has no such foreign key, and the
// admin edge picks the icon from the catalogue before it gets here), and it does
// not refuse a grant by a disabled verifier. "May this bot verify?"
// (domain.ErrVerifierForbidden / BOT_VERIFIER_FORBIDDEN) is an RPC-edge decision
// made from the settings this store returns; the store only enforces what the
// schema encodes.
type BotVerificationStore struct {
	db sqlcgen.DBTX
}

// NewBotVerificationStore builds the store on a pgx pool or transaction.
func NewBotVerificationStore(db sqlcgen.DBTX) *BotVerificationStore {
	return &BotVerificationStore{db: db}
}

var _ store.BotVerificationStore = (*BotVerificationStore)(nil)

const (
	defaultBotVerificationListLimit = 50
	maxBotVerificationListLimit     = 200
	// The bounds below mirror the octet_length CHECKs of 0154. They are not
	// redundant with the domain Validate methods: the domain counts runes and the
	// columns count bytes, so multi-byte text can clear validation and still
	// violate a CHECK. Guarding here keeps that a domain error instead of an
	// opaque constraint violation from the driver, and keeps the two backends
	// answering identically.
	maxVerificationIconNameBytes          = 128
	maxVerifierCompanyBytes               = 128
	maxVerifierDescriptionBytes           = 1024
	maxVerifierGrantReasonBytes           = 1024
	maxCustomVerificationDescriptionBytes = 1024
	maxCustomVerificationTitleBytes       = 1024
	maxCustomVerificationUsernameBytes    = 64
	maxCustomVerificationReasonBytes      = 4096
	maxCustomVerificationDecidedByBytes   = 128
	maxCustomVerificationDecisionBytes    = 4096
	maxCustomVerificationNoteBytes        = 8192
	maxCustomVerificationCorrelationBytes = 128
)

// Constraint names the store maps onto domain errors. They are the schema's
// invariants, so a race that slips past a pre-check still reports the error the
// pre-check would have.
const (
	verificationIconDocumentConstraint    = "verification_icons_document_id_key"
	customVerificationOnceConstraint      = "custom_verifications_once"
	customVerificationRequestPendingIndex = "custom_verification_requests_pending_idx"
)

// Column projections shared by every reader of a table, in scan order.
const (
	verificationIconColumnList = `id, document_id, owner_bot_id, name, active,
       created_at, updated_at`

	botVerifierSettingsColumnList = `bot_id, icon_document_id, company_name,
       default_description, can_modify_custom_description, enabled, granted_by,
       grant_reason, created_at, updated_at, version`

	customVerificationColumnList = `id, verifier_bot_id, peer_type, peer_id,
       icon_document_id, description, granted_by_user_id, created_at, updated_at,
       version`

	customVerificationRequestColumnList = `id, verifier_bot_id, applicant_user_id,
       peer_type, peer_id, peer_title, peer_username, reason,
       requested_description, status, decided_by, decision_reason, internal_note,
       correlation_id, created_at, updated_at, approved_at, rejected_at, version`
)

// customVerificationJoinColumns is the mark projection qualified for the
// bot_verifier_settings join the kill switch needs, kept in sync with the plain
// list by construction rather than by hand.
var customVerificationJoinColumns = prefixBotVerificationColumns(customVerificationColumnList, "cv.")

func prefixBotVerificationColumns(list, alias string) string {
	parts := strings.Split(list, ",")
	for i, part := range parts {
		parts[i] = alias + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

// botVerificationNow is the single clock for the store. Timestamps are truncated
// to the timestamptz resolution so a value written here reads back identically.
func botVerificationNow() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// ---- icon catalogue ---------------------------------------------------------

// UpsertVerificationIcon adds or updates a catalogue entry.
//
// document_id is the identity of an entry, not icon.ID: the catalogue exists to
// name real custom emoji documents, and the operator addresses them by document.
// Active travels with the payload, so an editor that means to keep an entry
// retired has to say so; SetVerificationIconActive is the narrow path for
// flipping only that flag.
func (s *BotVerificationStore) UpsertVerificationIcon(ctx context.Context, icon domain.VerificationIcon) (domain.VerificationIcon, error) {
	if s == nil || s.db == nil {
		return domain.VerificationIcon{}, fmt.Errorf("bot verification store is not configured")
	}
	icon.Name = strings.TrimSpace(icon.Name)
	if err := icon.Validate(); err != nil {
		return domain.VerificationIcon{}, err
	}
	if len(icon.Name) > maxVerificationIconNameBytes {
		return domain.VerificationIcon{}, domain.ErrVerificationIconInvalid
	}
	now := botVerificationNow()
	stored, err := scanVerificationIcon(s.db.QueryRow(ctx, `
INSERT INTO verification_icons (
  document_id, owner_bot_id, name, active, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$5)
ON CONFLICT ON CONSTRAINT `+verificationIconDocumentConstraint+` DO UPDATE
SET owner_bot_id = EXCLUDED.owner_bot_id,
    name = EXCLUDED.name,
    active = EXCLUDED.active,
    updated_at = GREATEST(verification_icons.updated_at, EXCLUDED.updated_at)
RETURNING `+verificationIconColumnList,
		icon.DocumentID, icon.OwnerBotID, icon.Name, icon.Active, now,
	))
	if err != nil {
		return domain.VerificationIcon{}, fmt.Errorf("upsert verification icon: %w", err)
	}
	return stored, nil
}

// SetVerificationIconActive retires or restores an entry. Marks already granted
// with it keep rendering: the icon id is denormalised onto the mark.
func (s *BotVerificationStore) SetVerificationIconActive(ctx context.Context, iconID int64, active bool) (domain.VerificationIcon, error) {
	if s == nil || s.db == nil {
		return domain.VerificationIcon{}, fmt.Errorf("bot verification store is not configured")
	}
	if iconID <= 0 {
		return domain.VerificationIcon{}, domain.ErrVerificationIconNotFound
	}
	icon, err := scanVerificationIcon(s.db.QueryRow(ctx, `
UPDATE verification_icons
SET active = $2, updated_at = GREATEST(updated_at, $3)
WHERE id = $1
RETURNING `+verificationIconColumnList, iconID, active, botVerificationNow()))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationIcon{}, domain.ErrVerificationIconNotFound
	}
	if err != nil {
		return domain.VerificationIcon{}, fmt.Errorf("set verification icon active: %w", err)
	}
	return icon, nil
}

// VerificationIcon reads one entry by id.
func (s *BotVerificationStore) VerificationIcon(ctx context.Context, iconID int64) (domain.VerificationIcon, error) {
	if s == nil || s.db == nil {
		return domain.VerificationIcon{}, fmt.Errorf("bot verification store is not configured")
	}
	if iconID <= 0 {
		return domain.VerificationIcon{}, domain.ErrVerificationIconNotFound
	}
	icon, err := scanVerificationIcon(s.db.QueryRow(ctx, `
SELECT `+verificationIconColumnList+`
FROM verification_icons
WHERE id = $1`, iconID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationIcon{}, domain.ErrVerificationIconNotFound
	}
	if err != nil {
		return domain.VerificationIcon{}, fmt.Errorf("get verification icon: %w", err)
	}
	return icon, nil
}

// VerificationIconByDocument reads one entry by its custom emoji document id,
// which is how the admin edge resolves an icon a verifier already carries.
func (s *BotVerificationStore) VerificationIconByDocument(ctx context.Context, documentID int64) (domain.VerificationIcon, error) {
	if s == nil || s.db == nil {
		return domain.VerificationIcon{}, fmt.Errorf("bot verification store is not configured")
	}
	if documentID <= 0 {
		return domain.VerificationIcon{}, domain.ErrVerificationIconNotFound
	}
	icon, err := scanVerificationIcon(s.db.QueryRow(ctx, `
SELECT `+verificationIconColumnList+`
FROM verification_icons
WHERE document_id = $1`, documentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VerificationIcon{}, domain.ErrVerificationIconNotFound
	}
	if err != nil {
		return domain.VerificationIcon{}, fmt.Errorf("get verification icon by document: %w", err)
	}
	return icon, nil
}

// ListVerificationIcons lists the catalogue, newest first. The order is the tail
// of verification_icons_active_idx, so the activeOnly form is an index-only walk.
func (s *BotVerificationStore) ListVerificationIcons(ctx context.Context, activeOnly bool, limit int) ([]domain.VerificationIcon, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	limit = botVerificationLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT `+verificationIconColumnList+`
FROM verification_icons
WHERE NOT $1::boolean OR active
ORDER BY id DESC
LIMIT $2`, activeOnly, limit)
	if err != nil {
		return nil, fmt.Errorf("list verification icons: %w", err)
	}
	defer rows.Close()
	out := make([]domain.VerificationIcon, 0, limit)
	for rows.Next() {
		icon, err := scanVerificationIcon(rows)
		if err != nil {
			return nil, fmt.Errorf("scan verification icon: %w", err)
		}
		out = append(out, icon)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate verification icons: %w", err)
	}
	return out, nil
}

// ---- verifier status --------------------------------------------------------

// UpsertBotVerifierSettings grants or updates verifier status.
//
// settings.Version is the optimistic-locking expectation: 0 means "there is no
// verifier row yet" and a stored row then reports
// domain.ErrCustomVerificationVersionConflict, and a non-zero version must match
// the stored one. created_at is never rewritten, so the grant date survives every
// later edit.
func (s *BotVerificationStore) UpsertBotVerifierSettings(ctx context.Context, settings domain.BotVerifierSettings) (domain.BotVerifierSettings, error) {
	if s == nil || s.db == nil {
		return domain.BotVerifierSettings{}, fmt.Errorf("bot verification store is not configured")
	}
	settings.CompanyName = strings.TrimSpace(settings.CompanyName)
	settings.DefaultDescription = strings.TrimSpace(settings.DefaultDescription)
	settings.GrantedBy = strings.TrimSpace(settings.GrantedBy)
	settings.GrantReason = strings.TrimSpace(settings.GrantReason)
	if err := settings.Validate(); err != nil {
		return domain.BotVerifierSettings{}, err
	}
	if settings.Version < 0 || !botVerifierSettingsColumnsFit(settings) {
		return domain.BotVerifierSettings{}, domain.ErrVerifierSettingsInvalid
	}
	var stored domain.BotVerifierSettings
	err := withTx(ctx, s.db, "upsert bot verifier settings", func(tx pgx.Tx) error {
		current, err := lockBotVerifierSettingsTx(ctx, tx, settings.BotID)
		switch {
		case errors.Is(err, domain.ErrVerifierNotFound):
			if settings.Version != 0 {
				// The caller edits a row that is no longer there.
				return domain.ErrVerifierNotFound
			}
			now := botVerificationNow()
			inserted, err := scanBotVerifierSettings(tx.QueryRow(ctx, `
INSERT INTO bot_verifier_settings (
  bot_id, icon_document_id, company_name, default_description,
  can_modify_custom_description, enabled, granted_by, grant_reason,
  created_at, updated_at, version
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,1)
RETURNING `+botVerifierSettingsColumnList,
				settings.BotID, settings.IconDocumentID, settings.CompanyName,
				settings.DefaultDescription, settings.CanModifyCustomDescription,
				settings.Enabled, settings.GrantedBy, settings.GrantReason, now,
			))
			if err != nil {
				return fmt.Errorf("insert bot verifier settings: %w", err)
			}
			stored = inserted
			return nil
		case err != nil:
			return err
		}
		if settings.Version == 0 || settings.Version != current.Version {
			return domain.ErrCustomVerificationVersionConflict
		}
		updated, err := scanBotVerifierSettings(tx.QueryRow(ctx, `
UPDATE bot_verifier_settings
SET icon_document_id = $3,
    company_name = $4,
    default_description = $5,
    can_modify_custom_description = $6,
    enabled = $7,
    granted_by = $8,
    grant_reason = $9,
    version = version + 1,
    updated_at = GREATEST(updated_at, $10)
WHERE bot_id = $1 AND version = $2
RETURNING `+botVerifierSettingsColumnList,
			settings.BotID, settings.Version, settings.IconDocumentID,
			settings.CompanyName, settings.DefaultDescription,
			settings.CanModifyCustomDescription, settings.Enabled,
			settings.GrantedBy, settings.GrantReason, botVerificationNow(),
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrCustomVerificationVersionConflict
		}
		if err != nil {
			return fmt.Errorf("update bot verifier settings: %w", err)
		}
		stored = updated
		return nil
	})
	if err != nil {
		return domain.BotVerifierSettings{}, err
	}
	return stored, nil
}

// SetBotVerifierEnabled flips the operator kill switch. Existing marks stay on
// disk, but the verifier can grant nothing new and neither its settings nor its
// marks are projected, so flipping the switch back restores exactly what was
// there. Setting the flag to the value it already has is a no-op and does not
// burn a version.
func (s *BotVerificationStore) SetBotVerifierEnabled(ctx context.Context, botID int64, enabled bool) (domain.BotVerifierSettings, error) {
	if s == nil || s.db == nil {
		return domain.BotVerifierSettings{}, fmt.Errorf("bot verification store is not configured")
	}
	if botID <= 0 {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	var stored domain.BotVerifierSettings
	err := withTx(ctx, s.db, "set bot verifier enabled", func(tx pgx.Tx) error {
		current, err := lockBotVerifierSettingsTx(ctx, tx, botID)
		if err != nil {
			return err
		}
		if current.Enabled == enabled {
			stored = current
			return nil
		}
		updated, err := scanBotVerifierSettings(tx.QueryRow(ctx, `
UPDATE bot_verifier_settings
SET enabled = $2, version = version + 1, updated_at = GREATEST(updated_at, $3)
WHERE bot_id = $1
RETURNING `+botVerifierSettingsColumnList, botID, enabled, botVerificationNow()))
		if err != nil {
			return fmt.Errorf("set bot verifier enabled: %w", err)
		}
		stored = updated
		return nil
	})
	if err != nil {
		return domain.BotVerifierSettings{}, err
	}
	return stored, nil
}

// DeleteBotVerifierSettings removes verifier status. Its marks cascade away with
// it (custom_verifications.verifier_bot_id ON DELETE CASCADE), because a mark
// whose verifier no longer exists has nothing to render. Applications survive:
// they reference users, not the verifier row, and stay as history.
func (s *BotVerificationStore) DeleteBotVerifierSettings(ctx context.Context, botID int64) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("bot verification store is not configured")
	}
	if botID <= 0 {
		return false, domain.ErrVerifierNotFound
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM bot_verifier_settings WHERE bot_id = $1`, botID)
	if err != nil {
		return false, fmt.Errorf("delete bot verifier settings: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// BotVerifierSettings reads one verifier's status, enabled or not: the caller
// needs the disabled row too, to render the kill switch and to explain
// BOT_VERIFIER_FORBIDDEN.
func (s *BotVerificationStore) BotVerifierSettings(ctx context.Context, botID int64) (domain.BotVerifierSettings, error) {
	if s == nil || s.db == nil {
		return domain.BotVerifierSettings{}, fmt.Errorf("bot verification store is not configured")
	}
	if botID <= 0 {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	settings, err := scanBotVerifierSettings(s.db.QueryRow(ctx, `
SELECT `+botVerifierSettingsColumnList+`
FROM bot_verifier_settings
WHERE bot_id = $1`, botID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	if err != nil {
		return domain.BotVerifierSettings{}, fmt.Errorf("get bot verifier settings: %w", err)
	}
	return settings, nil
}

// BotVerifierSettingsBatch resolves several bots in one round trip for the
// botInfo projection; bots without verifier status are absent from the map.
// Disabled verifiers are returned like any other row -- the Enabled field says
// so, and the projection edge decides -- which mirrors ListBotVerifiers taking
// enabledOnly as an explicit argument instead of assuming it.
func (s *BotVerificationStore) BotVerifierSettingsBatch(ctx context.Context, botIDs []int64) (map[int64]domain.BotVerifierSettings, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	ids := make([]int64, 0, len(botIDs))
	seen := make(map[int64]struct{}, len(botIDs))
	for _, id := range botIDs {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	out := make(map[int64]domain.BotVerifierSettings, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT `+botVerifierSettingsColumnList+`
FROM bot_verifier_settings
WHERE bot_id = ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("batch bot verifier settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		settings, err := scanBotVerifierSettings(rows)
		if err != nil {
			return nil, fmt.Errorf("scan bot verifier settings: %w", err)
		}
		out[settings.BotID] = settings
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bot verifier settings: %w", err)
	}
	return out, nil
}

// ListBotVerifiers lists verifier bots for the admin panel, ordered the way
// bot_verifier_settings_enabled_idx is built.
func (s *BotVerificationStore) ListBotVerifiers(ctx context.Context, enabledOnly bool, limit int) ([]domain.BotVerifierSettings, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	limit = botVerificationLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT `+botVerifierSettingsColumnList+`
FROM bot_verifier_settings
WHERE NOT $1::boolean OR enabled
ORDER BY bot_id
LIMIT $2`, enabledOnly, limit)
	if err != nil {
		return nil, fmt.Errorf("list bot verifiers: %w", err)
	}
	defer rows.Close()
	out := make([]domain.BotVerifierSettings, 0, limit)
	for rows.Next() {
		settings, err := scanBotVerifierSettings(rows)
		if err != nil {
			return nil, fmt.Errorf("scan bot verifier: %w", err)
		}
		out = append(out, settings)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bot verifiers: %w", err)
	}
	return out, nil
}

// ---- granted marks ---------------------------------------------------------

// GrantCustomVerification creates or updates this verifier's mark on the peer.
//
// custom_verifications_once makes (verifier, peer) the identity of a mark, so a
// second grant by the same verifier is an update of the description and the icon
// reported with created=false rather than a second badge.
//
// The verifier row is locked first: it is both the existence check
// (domain.ErrVerifierNotFound) and the serialisation point that makes the
// per-verifier bound real. Two concurrent grants by one verifier queue up, so
// the count they check cannot go stale between the check and the insert and
// domain.MaxCustomVerificationsPerVerifier cannot be overshot.
//
// mark.IconDocumentID is denormalised from the verifier's settings when the
// caller leaves it unset, which is what "the icon is taken from the verifier at
// grant time" means; an explicit id is honoured, so re-issuing a historical mark
// keeps its original icon.
func (s *BotVerificationStore) GrantCustomVerification(ctx context.Context, mark domain.CustomVerification) (domain.CustomVerification, bool, error) {
	if s == nil || s.db == nil {
		return domain.CustomVerification{}, false, fmt.Errorf("bot verification store is not configured")
	}
	mark.Description = strings.TrimSpace(mark.Description)
	if mark.VerifierBotID <= 0 || !validBotVerificationPeer(mark.Peer) {
		return domain.CustomVerification{}, false, domain.ErrCustomVerificationTargetInvalid
	}
	if mark.GrantedByUserID < 0 {
		return domain.CustomVerification{}, false, domain.ErrCustomVerificationTargetInvalid
	}
	if len(mark.Description) > maxCustomVerificationDescriptionBytes {
		return domain.CustomVerification{}, false, domain.ErrCustomVerificationRequestInvalid
	}
	var stored domain.CustomVerification
	created := false
	err := withTx(ctx, s.db, "grant custom verification", func(tx pgx.Tx) error {
		settings, err := lockBotVerifierSettingsTx(ctx, tx, mark.VerifierBotID)
		if err != nil {
			return err
		}
		if mark.IconDocumentID <= 0 {
			mark.IconDocumentID = settings.IconDocumentID
		}
		if err := mark.Validate(); err != nil {
			return err
		}
		existed := true
		switch _, err := customVerificationTx(ctx, tx, mark.VerifierBotID, mark.Peer, true); {
		case err == nil:
		case errors.Is(err, domain.ErrCustomVerificationNotFound):
			existed = false
		default:
			return err
		}
		if !existed {
			count, err := countCustomVerificationsTx(ctx, tx, mark.VerifierBotID)
			if err != nil {
				return err
			}
			if count >= domain.MaxCustomVerificationsPerVerifier {
				return domain.ErrCustomVerificationLimit
			}
		}
		now := botVerificationNow()
		upserted, err := scanCustomVerification(tx.QueryRow(ctx, `
INSERT INTO custom_verifications (
  verifier_bot_id, peer_type, peer_id, icon_document_id, description,
  granted_by_user_id, created_at, updated_at, version
) VALUES ($1,$2,$3,$4,$5,$6,$7,$7,1)
ON CONFLICT ON CONSTRAINT `+customVerificationOnceConstraint+` DO UPDATE
SET icon_document_id = EXCLUDED.icon_document_id,
    description = EXCLUDED.description,
    granted_by_user_id = EXCLUDED.granted_by_user_id,
    version = custom_verifications.version + 1,
    updated_at = GREATEST(custom_verifications.updated_at, EXCLUDED.updated_at)
RETURNING `+customVerificationColumnList,
			mark.VerifierBotID, string(mark.Peer.Type), mark.Peer.ID,
			mark.IconDocumentID, mark.Description, mark.GrantedByUserID, now,
		))
		if err != nil {
			return fmt.Errorf("grant custom verification: %w", err)
		}
		stored = upserted
		created = !existed
		return nil
	})
	if err != nil {
		return domain.CustomVerification{}, false, err
	}
	return stored, created, nil
}

// RevokeCustomVerification removes this verifier's mark from the peer and
// reports whether anything was removed, so a repeated revoke is a no-op instead
// of an error. Only this verifier's mark goes: another verifier's mark on the
// same peer is none of its business.
func (s *BotVerificationStore) RevokeCustomVerification(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("bot verification store is not configured")
	}
	if verifierBotID <= 0 || !validBotVerificationPeer(peer) {
		return false, domain.ErrCustomVerificationTargetInvalid
	}
	tag, err := s.db.Exec(ctx, `
DELETE FROM custom_verifications
WHERE verifier_bot_id = $1 AND peer_type = $2 AND peer_id = $3`,
		verifierBotID, string(peer.Type), peer.ID)
	if err != nil {
		return false, fmt.Errorf("revoke custom verification: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// CustomVerification reads one verifier's mark on a peer, whether or not that
// verifier is currently enabled: this is the bookkeeping read, not the
// projection.
func (s *BotVerificationStore) CustomVerification(ctx context.Context, verifierBotID int64, peer domain.Peer) (domain.CustomVerification, error) {
	if s == nil || s.db == nil {
		return domain.CustomVerification{}, fmt.Errorf("bot verification store is not configured")
	}
	if verifierBotID <= 0 || !validBotVerificationPeer(peer) {
		return domain.CustomVerification{}, domain.ErrCustomVerificationNotFound
	}
	return customVerificationTx(ctx, s.db, verifierBotID, peer, false)
}

// PeerVerification returns the mark a peer is rendered with.
//
// Two things make this deterministic: only enabled verifiers count (the join),
// and the newest mark wins (ORDER BY cv.id DESC). Both are the reason a peer
// marked by several verifiers shows the same icon in every response instead of
// flickering between them, and the ordering is the tail of
// custom_verifications_peer_idx so the pick costs one index descent.
func (s *BotVerificationStore) PeerVerification(ctx context.Context, peer domain.Peer) (domain.CustomVerification, error) {
	if s == nil || s.db == nil {
		return domain.CustomVerification{}, fmt.Errorf("bot verification store is not configured")
	}
	if !validBotVerificationPeer(peer) {
		return domain.CustomVerification{}, domain.ErrCustomVerificationNotFound
	}
	mark, err := scanCustomVerification(s.db.QueryRow(ctx, `
SELECT `+customVerificationJoinColumns+`
FROM custom_verifications cv
JOIN bot_verifier_settings s ON s.bot_id = cv.verifier_bot_id AND s.enabled
WHERE cv.peer_type = $1 AND cv.peer_id = $2
ORDER BY cv.id DESC
LIMIT 1`, string(peer.Type), peer.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CustomVerification{}, domain.ErrCustomVerificationNotFound
	}
	if err != nil {
		return domain.CustomVerification{}, fmt.Errorf("get peer verification: %w", err)
	}
	return mark, nil
}

// PeerVerificationBatch resolves the projection for many peers at once.
//
// This is the call on the hot serialisation path, so it is ONE query for the
// whole batch: DISTINCT ON (peer_type, peer_id) with ORDER BY ... cv.id DESC
// picks the same newest-enabled-mark per peer that PeerVerification would, and
// peers without a mark are simply absent instead of erroring. Sending N queries
// here would put a per-peer round trip on every dialog list.
func (s *BotVerificationStore) PeerVerificationBatch(ctx context.Context, peers []domain.Peer) (map[domain.Peer]domain.CustomVerification, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	types, ids := botVerificationPeerArrays(peers)
	out := make(map[domain.Peer]domain.CustomVerification, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT DISTINCT ON (cv.peer_type, cv.peer_id) `+customVerificationJoinColumns+`
FROM custom_verifications cv
JOIN bot_verifier_settings s ON s.bot_id = cv.verifier_bot_id AND s.enabled
WHERE (cv.peer_type, cv.peer_id) IN (SELECT * FROM unnest($1::text[], $2::bigint[]))
ORDER BY cv.peer_type, cv.peer_id, cv.id DESC`, types, ids)
	if err != nil {
		return nil, fmt.Errorf("batch peer verification: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		mark, err := scanCustomVerification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan peer verification: %w", err)
		}
		out[mark.Peer] = mark
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate peer verifications: %w", err)
	}
	return out, nil
}

// CountCustomVerifications reports how many peers a verifier has marked, for the
// per-verifier bound. Disabled verifiers still count their marks: the switch
// hides badges, it does not free quota.
func (s *BotVerificationStore) CountCustomVerifications(ctx context.Context, verifierBotID int64) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("bot verification store is not configured")
	}
	if verifierBotID <= 0 {
		return 0, domain.ErrCustomVerificationTargetInvalid
	}
	return countCustomVerificationsTx(ctx, s.db, verifierBotID)
}

// ListCustomVerifications is the admin listing query with keyset paging.
//
// Paging is keyset over id DESC (filter.BeforeID carries the last row of the
// previous page), which is the tail of custom_verifications_verifier_idx and of
// custom_verifications_peer_idx. Query matches a mark id or a peer id when it is
// numeric and otherwise matches the description case-insensitively, the only
// text a mark carries.
func (s *BotVerificationStore) ListCustomVerifications(ctx context.Context, filter domain.CustomVerificationFilter) ([]domain.CustomVerification, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	if filter.PeerType != "" && !botVerificationPeerType(filter.PeerType) {
		return nil, domain.ErrCustomVerificationTargetInvalid
	}
	limit := botVerificationLimit(filter.Limit)
	numeric, isNumeric, needle := parseBotVerificationQuery(filter.Query)
	rows, err := s.db.Query(ctx, `
SELECT `+customVerificationColumnList+`
FROM custom_verifications
WHERE ($1 = 0 OR verifier_bot_id = $1)
  AND ($2 = '' OR peer_type = $2)
  AND ($3 = 0 OR peer_id = $3)
  AND ($4 = 0 OR id < $4)
  AND (
    NOT $5::boolean
    OR ($6::boolean AND (id = $7::bigint OR peer_id = $7::bigint))
    OR (NOT $6::boolean AND lower(description) LIKE '%' || $8::text || '%')
  )
ORDER BY id DESC
LIMIT $9`,
		filter.VerifierBotID, string(filter.PeerType), filter.PeerID,
		filter.BeforeID, isNumeric || needle != "", isNumeric, numeric,
		escapeLike(needle), limit)
	if err != nil {
		return nil, fmt.Errorf("list custom verifications: %w", err)
	}
	defer rows.Close()
	out := make([]domain.CustomVerification, 0, limit)
	for rows.Next() {
		mark, err := scanCustomVerification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan custom verification: %w", err)
		}
		out = append(out, mark)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate custom verifications: %w", err)
	}
	return out, nil
}

// ---- application queue -----------------------------------------------------

// CreateCustomVerificationRequest files an application.
//
// A filed application is pending by definition, so the status is forced and any
// decision field the caller pre-filled is dropped: only
// DecideCustomVerificationRequest may write those.
// custom_verification_requests_pending_idx allows one live application per
// (verifier, peer), and a second one reports
// domain.ErrCustomVerificationRequestExists -- two pending rows would let two
// decisions race for one mark.
func (s *BotVerificationStore) CreateCustomVerificationRequest(ctx context.Context, req domain.CustomVerificationRequest) (domain.CustomVerificationRequest, error) {
	if s == nil || s.db == nil {
		return domain.CustomVerificationRequest{}, fmt.Errorf("bot verification store is not configured")
	}
	req = normalizeCustomVerificationRequest(req)
	if req.Status == "" {
		req.Status = domain.CustomVerificationPending
	}
	if req.Status != domain.CustomVerificationPending {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestInvalid
	}
	req.DecidedBy = ""
	req.DecisionReason = ""
	if err := req.Validate(); err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	if err := validateCustomVerificationRequestColumns(req); err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	now := botVerificationNow()
	stored, err := scanCustomVerificationRequest(s.db.QueryRow(ctx, `
INSERT INTO custom_verification_requests (
  verifier_bot_id, applicant_user_id, peer_type, peer_id, peer_title,
  peer_username, reason, requested_description, status, internal_note,
  correlation_id, created_at, updated_at, version
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'pending',$9,$10,$11,$11,1)
RETURNING `+customVerificationRequestColumnList,
		req.VerifierBotID, req.ApplicantUserID, string(req.Peer.Type), req.Peer.ID,
		req.PeerTitle, req.PeerUsername, req.Reason, req.RequestedDescription,
		req.InternalNote, req.CorrelationID, now,
	))
	if err != nil {
		if isUniqueConstraint(err, customVerificationRequestPendingIndex) {
			return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestExists
		}
		return domain.CustomVerificationRequest{}, fmt.Errorf("insert custom verification request: %w", err)
	}
	return stored, nil
}

// DecideCustomVerificationRequest moves an application through its status
// machine and keeps the mark in step with it.
//
// The whole decision is one transaction: the status change, the decision
// metadata, the approved_at/rejected_at stamps and the apply callback that
// grants (status approved) or removes (status revoked) the mark. apply is handed
// a context carrying this transaction, so it must write through
// VerificationTxFromContext -- for example
// postgres.NewBotVerificationStore(tx).GrantCustomVerification. A callback that
// fails rolls the status change back with it, which is why "approved without a
// mark" is not a reachable state; a callback that ignores the handle would write
// on its own connection and survive that rollback.
//
// Order of checks is the deterministic part of two reviewers acting at once: the
// version is compared first, so the loser always sees
// domain.ErrCustomVerificationVersionConflict. A caller re-issuing a decision
// that already holds gets the request back with changed=false, no second stamp
// and no second apply -- the callback is not idempotent by assumption.
//
// The transition itself is domain.CanTransitionCustomVerificationStatus, never
// re-implemented here, and the resulting row is validated with
// domain.CustomVerificationRequest.Validate, which is what rejects a rejection
// with no reason (domain.ErrVerificationReasonRequired).
func (s *BotVerificationStore) DecideCustomVerificationRequest(ctx context.Context, requestID int64, version int64, status domain.CustomVerificationRequestStatus, decidedBy, reason, note string, apply func(ctx context.Context, req domain.CustomVerificationRequest) error) (domain.CustomVerificationRequest, bool, error) {
	if s == nil || s.db == nil {
		return domain.CustomVerificationRequest{}, false, fmt.Errorf("bot verification store is not configured")
	}
	decidedBy = strings.TrimSpace(decidedBy)
	reason = strings.TrimSpace(reason)
	note = strings.TrimSpace(note)
	if requestID <= 0 || version <= 0 {
		return domain.CustomVerificationRequest{}, false, domain.ErrCustomVerificationRequestInvalid
	}
	if !status.Valid() || status == domain.CustomVerificationPending {
		// Pending is where an application starts, not a decision anybody makes.
		return domain.CustomVerificationRequest{}, false, domain.ErrCustomVerificationRequestInvalid
	}
	if customVerificationDecisionNeedsApply(status) && apply == nil {
		// Deciding without a way to move the mark is exactly the state this store
		// exists to make impossible.
		return domain.CustomVerificationRequest{}, false, fmt.Errorf("custom verification decision %q requires an apply callback", status)
	}
	var stored domain.CustomVerificationRequest
	changed := false
	err := withTx(ctx, s.db, "decide custom verification request", func(tx pgx.Tx) error {
		current, err := lockCustomVerificationRequestTx(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if current.Version != version {
			return domain.ErrCustomVerificationVersionConflict
		}
		if current.Status == status {
			// Already decided this way: keep the record and report that nothing
			// moved, so a retried decision cannot apply the mark twice.
			stored = current
			return nil
		}
		if !domain.CanTransitionCustomVerificationStatus(current.Status, status) {
			return domain.ErrCustomVerificationRequestInvalid
		}
		now := botVerificationNow()
		next := customVerificationDecisionState(current, status, decidedBy, reason, note, now)
		if err := next.Validate(); err != nil {
			return err
		}
		if err := validateCustomVerificationRequestColumns(next); err != nil {
			return err
		}
		updated, err := scanCustomVerificationRequest(tx.QueryRow(ctx, `
UPDATE custom_verification_requests
SET status = $3,
    decided_by = $4,
    decision_reason = $5,
    internal_note = $6,
    approved_at = $7,
    rejected_at = $8,
    version = version + 1,
    updated_at = GREATEST(updated_at, $9)
WHERE id = $1 AND version = $2
RETURNING `+customVerificationRequestColumnList,
			requestID, version, string(next.Status), next.DecidedBy,
			next.DecisionReason, next.InternalNote,
			botVerificationTimeArg(next.ApprovedAt),
			botVerificationTimeArg(next.RejectedAt), now,
		))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrCustomVerificationVersionConflict
		}
		if err != nil {
			return fmt.Errorf("decide custom verification request: %w", err)
		}
		if customVerificationDecisionNeedsApply(status) {
			if err := apply(verificationTxContext(ctx, tx), updated); err != nil {
				return err
			}
		}
		stored = updated
		changed = true
		return nil
	})
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	return stored, changed, nil
}

// CustomVerificationRequest reads one application.
func (s *BotVerificationStore) CustomVerificationRequest(ctx context.Context, requestID int64) (domain.CustomVerificationRequest, error) {
	if s == nil || s.db == nil {
		return domain.CustomVerificationRequest{}, fmt.Errorf("bot verification store is not configured")
	}
	if requestID <= 0 {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	req, err := scanCustomVerificationRequest(s.db.QueryRow(ctx, `
SELECT `+customVerificationRequestColumnList+`
FROM custom_verification_requests
WHERE id = $1`, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	if err != nil {
		return domain.CustomVerificationRequest{}, fmt.Errorf("get custom verification request: %w", err)
	}
	return req, nil
}

// PendingCustomVerificationRequest returns the live application for a
// (verifier, peer) pair. The partial unique index guarantees there is at most
// one, so no ordering is needed to pick it.
func (s *BotVerificationStore) PendingCustomVerificationRequest(ctx context.Context, verifierBotID int64, peer domain.Peer) (domain.CustomVerificationRequest, error) {
	if s == nil || s.db == nil {
		return domain.CustomVerificationRequest{}, fmt.Errorf("bot verification store is not configured")
	}
	if verifierBotID <= 0 || !validBotVerificationPeer(peer) {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	req, err := scanCustomVerificationRequest(s.db.QueryRow(ctx, `
SELECT `+customVerificationRequestColumnList+`
FROM custom_verification_requests
WHERE verifier_bot_id = $1 AND peer_type = $2 AND peer_id = $3
  AND status = 'pending'
LIMIT 1`, verifierBotID, string(peer.Type), peer.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	if err != nil {
		return domain.CustomVerificationRequest{}, fmt.Errorf("get pending custom verification request: %w", err)
	}
	return req, nil
}

// ListCustomVerificationRequests is the review-queue query with keyset paging.
//
// Paging is keyset over id DESC: the filter carries no timestamp cursor, and ids
// are monotonic, so id DESC is the same page order as created_at DESC without
// the ambiguity a mixed cursor would have. Query matches an application id or a
// peer id when it is numeric and otherwise prefix-matches the lowercased
// username snapshot, the same split the official verification queue uses.
func (s *BotVerificationStore) ListCustomVerificationRequests(ctx context.Context, filter domain.CustomVerificationRequestFilter) ([]domain.CustomVerificationRequest, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	statuses := make([]string, 0, len(filter.Statuses))
	for _, status := range filter.Statuses {
		if !status.Valid() {
			return nil, domain.ErrCustomVerificationRequestInvalid
		}
		statuses = append(statuses, string(status))
	}
	if filter.PeerType != "" && !botVerificationPeerType(filter.PeerType) {
		return nil, domain.ErrCustomVerificationTargetInvalid
	}
	limit := botVerificationLimit(filter.Limit)
	numeric, isNumeric, prefix := parseBotVerificationQuery(filter.Query)
	rows, err := s.db.Query(ctx, `
SELECT `+customVerificationRequestColumnList+`
FROM custom_verification_requests
WHERE (cardinality($1::text[]) = 0 OR status = ANY($1::text[]))
  AND ($2 = 0 OR verifier_bot_id = $2)
  AND ($3 = '' OR peer_type = $3)
  AND ($4 = 0 OR id < $4)
  AND (
    NOT $5::boolean
    OR ($6::boolean AND (id = $7::bigint OR peer_id = $7::bigint))
    OR (
      NOT $6::boolean
      AND peer_username <> ''
      AND lower(peer_username) LIKE $8::text || '%'
    )
  )
ORDER BY id DESC
LIMIT $9`,
		statuses, filter.VerifierBotID, string(filter.PeerType), filter.BeforeID,
		isNumeric || prefix != "", isNumeric, numeric, escapeLike(prefix), limit)
	if err != nil {
		return nil, fmt.Errorf("list custom verification requests: %w", err)
	}
	defer rows.Close()
	out := make([]domain.CustomVerificationRequest, 0, limit)
	for rows.Next() {
		req, err := scanCustomVerificationRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan custom verification request: %w", err)
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate custom verification requests: %w", err)
	}
	return out, nil
}

// CustomVerificationRequestsForApplicant returns an applicant's own history,
// newest first, for the verifier bot's /status command.
func (s *BotVerificationStore) CustomVerificationRequestsForApplicant(ctx context.Context, applicantUserID int64, limit int) ([]domain.CustomVerificationRequest, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	if applicantUserID <= 0 {
		return nil, domain.ErrCustomVerificationRequestInvalid
	}
	limit = botVerificationLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT `+customVerificationRequestColumnList+`
FROM custom_verification_requests
WHERE applicant_user_id = $1
ORDER BY id DESC
LIMIT $2`, applicantUserID, limit)
	if err != nil {
		return nil, fmt.Errorf("list applicant custom verification requests: %w", err)
	}
	defer rows.Close()
	out := make([]domain.CustomVerificationRequest, 0, limit)
	for rows.Next() {
		req, err := scanCustomVerificationRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan applicant custom verification request: %w", err)
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applicant custom verification requests: %w", err)
	}
	return out, nil
}

// CustomVerificationRequestCounts is the queue summary by status. Statuses
// nobody is in are absent rather than zero, which a map read cannot tell apart
// anyway.
func (s *BotVerificationStore) CustomVerificationRequestCounts(ctx context.Context) (map[domain.CustomVerificationRequestStatus]int64, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	rows, err := s.db.Query(ctx, `
SELECT status, count(*)
FROM custom_verification_requests
GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count custom verification requests: %w", err)
	}
	defer rows.Close()
	out := make(map[domain.CustomVerificationRequestStatus]int64, 4)
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan custom verification request count: %w", err)
		}
		out[domain.CustomVerificationRequestStatus(status)] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate custom verification request counts: %w", err)
	}
	return out, nil
}

// ---- helpers ----------------------------------------------------------------

func scanVerificationIcon(row pgx.Row) (domain.VerificationIcon, error) {
	var icon domain.VerificationIcon
	if err := row.Scan(&icon.ID, &icon.DocumentID, &icon.OwnerBotID, &icon.Name,
		&icon.Active, &icon.CreatedAt, &icon.UpdatedAt); err != nil {
		return domain.VerificationIcon{}, err
	}
	icon.CreatedAt = icon.CreatedAt.UTC()
	icon.UpdatedAt = icon.UpdatedAt.UTC()
	return icon, nil
}

func scanBotVerifierSettings(row pgx.Row) (domain.BotVerifierSettings, error) {
	var settings domain.BotVerifierSettings
	if err := row.Scan(&settings.BotID, &settings.IconDocumentID,
		&settings.CompanyName, &settings.DefaultDescription,
		&settings.CanModifyCustomDescription, &settings.Enabled,
		&settings.GrantedBy, &settings.GrantReason, &settings.CreatedAt,
		&settings.UpdatedAt, &settings.Version); err != nil {
		return domain.BotVerifierSettings{}, err
	}
	settings.CreatedAt = settings.CreatedAt.UTC()
	settings.UpdatedAt = settings.UpdatedAt.UTC()
	return settings, nil
}

// lockBotVerifierSettingsTx reads the verifier row for mutation. It is both the
// existence check and the serialisation point every grant by that verifier goes
// through, which is what makes the per-verifier bound hold under concurrency.
func lockBotVerifierSettingsTx(ctx context.Context, tx pgx.Tx, botID int64) (domain.BotVerifierSettings, error) {
	settings, err := scanBotVerifierSettings(tx.QueryRow(ctx, `
SELECT `+botVerifierSettingsColumnList+`
FROM bot_verifier_settings
WHERE bot_id = $1
FOR UPDATE`, botID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	if err != nil {
		return domain.BotVerifierSettings{}, fmt.Errorf("lock bot verifier settings: %w", err)
	}
	return settings, nil
}

// customVerificationRow adapts the mark projection onto the domain type: the
// peer arrives as (text, bigint) rather than as a domain.Peer.
type customVerificationRow struct {
	mark     domain.CustomVerification
	peerType string
}

func (r *customVerificationRow) dest() []any {
	return []any{
		&r.mark.ID, &r.mark.VerifierBotID, &r.peerType, &r.mark.Peer.ID,
		&r.mark.IconDocumentID, &r.mark.Description, &r.mark.GrantedByUserID,
		&r.mark.CreatedAt, &r.mark.UpdatedAt, &r.mark.Version,
	}
}

func (r *customVerificationRow) value() domain.CustomVerification {
	mark := r.mark
	mark.Peer.Type = domain.PeerType(r.peerType)
	mark.CreatedAt = mark.CreatedAt.UTC()
	mark.UpdatedAt = mark.UpdatedAt.UTC()
	return mark
}

func scanCustomVerification(row pgx.Row) (domain.CustomVerification, error) {
	var r customVerificationRow
	if err := row.Scan(r.dest()...); err != nil {
		return domain.CustomVerification{}, err
	}
	return r.value(), nil
}

// customVerificationTx reads one verifier's mark on a peer, optionally locking it
// so a concurrent grant of the same pair serialises behind this one.
func customVerificationTx(ctx context.Context, db sqlcgen.DBTX, verifierBotID int64, peer domain.Peer, forUpdate bool) (domain.CustomVerification, error) {
	query := `
SELECT ` + customVerificationColumnList + `
FROM custom_verifications
WHERE verifier_bot_id = $1 AND peer_type = $2 AND peer_id = $3`
	if forUpdate {
		query += `
FOR UPDATE`
	}
	mark, err := scanCustomVerification(db.QueryRow(ctx, query,
		verifierBotID, string(peer.Type), peer.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CustomVerification{}, domain.ErrCustomVerificationNotFound
	}
	if err != nil {
		return domain.CustomVerification{}, fmt.Errorf("get custom verification: %w", err)
	}
	return mark, nil
}

func countCustomVerificationsTx(ctx context.Context, db sqlcgen.DBTX, verifierBotID int64) (int, error) {
	var count int
	if err := db.QueryRow(ctx, `
SELECT count(*) FROM custom_verifications WHERE verifier_bot_id = $1`,
		verifierBotID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count custom verifications: %w", err)
	}
	return count, nil
}

// customVerificationRequestRow adapts the application projection: the enum
// columns arrive as text and approved_at / rejected_at are nullable.
type customVerificationRequestRow struct {
	req        domain.CustomVerificationRequest
	peerType   string
	status     string
	approvedAt *time.Time
	rejectedAt *time.Time
}

func (r *customVerificationRequestRow) dest() []any {
	return []any{
		&r.req.ID, &r.req.VerifierBotID, &r.req.ApplicantUserID, &r.peerType,
		&r.req.Peer.ID, &r.req.PeerTitle, &r.req.PeerUsername, &r.req.Reason,
		&r.req.RequestedDescription, &r.status, &r.req.DecidedBy,
		&r.req.DecisionReason, &r.req.InternalNote, &r.req.CorrelationID,
		&r.req.CreatedAt, &r.req.UpdatedAt, &r.approvedAt, &r.rejectedAt,
		&r.req.Version,
	}
}

func (r *customVerificationRequestRow) value() domain.CustomVerificationRequest {
	req := r.req
	req.Peer.Type = domain.PeerType(r.peerType)
	req.Status = domain.CustomVerificationRequestStatus(r.status)
	req.CreatedAt = req.CreatedAt.UTC()
	req.UpdatedAt = req.UpdatedAt.UTC()
	if r.approvedAt != nil {
		req.ApprovedAt = r.approvedAt.UTC()
	}
	if r.rejectedAt != nil {
		req.RejectedAt = r.rejectedAt.UTC()
	}
	return req
}

func scanCustomVerificationRequest(row pgx.Row) (domain.CustomVerificationRequest, error) {
	var r customVerificationRequestRow
	if err := row.Scan(r.dest()...); err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	return r.value(), nil
}

// lockCustomVerificationRequestTx reads the application for mutation. FOR UPDATE
// plus the version guard on the following UPDATE is what serialises two
// reviewers: the second one blocks here and then sees the bumped version.
func lockCustomVerificationRequestTx(ctx context.Context, tx pgx.Tx, requestID int64) (domain.CustomVerificationRequest, error) {
	req, err := scanCustomVerificationRequest(tx.QueryRow(ctx, `
SELECT `+customVerificationRequestColumnList+`
FROM custom_verification_requests
WHERE id = $1
FOR UPDATE`, requestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	if err != nil {
		return domain.CustomVerificationRequest{}, fmt.Errorf("lock custom verification request: %w", err)
	}
	return req, nil
}

// customVerificationDecisionNeedsApply reports whether a decision moves the mark
// and therefore needs the callback: approving grants it, revoking removes it,
// and a rejection never had one to move.
func customVerificationDecisionNeedsApply(status domain.CustomVerificationRequestStatus) bool {
	return status == domain.CustomVerificationApproved || status == domain.CustomVerificationRevoked
}

// customVerificationDecisionState projects a decision onto the stored row.
//
// The approved_at / rejected_at stamps are not free-form: 0154 pairs each with
// its status ("(status = 'approved') = (approved_at IS NOT NULL)"), so a
// revocation has to clear approved_at as it leaves the approved state. The
// application's history of having been approved lives in the status itself --
// revoked is reachable only from approved.
func customVerificationDecisionState(current domain.CustomVerificationRequest, status domain.CustomVerificationRequestStatus, decidedBy, reason, note string, now time.Time) domain.CustomVerificationRequest {
	next := current
	next.Status = status
	next.DecidedBy = decidedBy
	next.DecisionReason = reason
	next.InternalNote = note
	next.ApprovedAt = time.Time{}
	next.RejectedAt = time.Time{}
	switch status {
	case domain.CustomVerificationApproved:
		next.ApprovedAt = now
	case domain.CustomVerificationRejected:
		next.RejectedAt = now
	}
	next.Version = current.Version + 1
	if now.After(next.UpdatedAt) {
		next.UpdatedAt = now
	}
	return next
}

func normalizeCustomVerificationRequest(req domain.CustomVerificationRequest) domain.CustomVerificationRequest {
	req.PeerTitle = strings.TrimSpace(req.PeerTitle)
	req.PeerUsername = domain.NormalizeUsername(req.PeerUsername)
	req.Reason = strings.TrimSpace(req.Reason)
	req.RequestedDescription = strings.TrimSpace(req.RequestedDescription)
	req.DecidedBy = strings.TrimSpace(req.DecidedBy)
	req.DecisionReason = strings.TrimSpace(req.DecisionReason)
	req.InternalNote = strings.TrimSpace(req.InternalNote)
	req.CorrelationID = strings.TrimSpace(req.CorrelationID)
	return req
}

// botVerifierSettingsColumnsFit reports whether the verifier text fits the
// columns in bytes, which the rune-counting domain Validate cannot answer.
func botVerifierSettingsColumnsFit(settings domain.BotVerifierSettings) bool {
	return len(settings.CompanyName) <= maxVerifierCompanyBytes &&
		len(settings.DefaultDescription) <= maxVerifierDescriptionBytes &&
		len(settings.GrantReason) <= maxVerifierGrantReasonBytes
}

// validateCustomVerificationRequestColumns guards the octet_length CHECKs on
// custom_verification_requests, so an over-long snapshot is a domain error
// rather than a constraint violation from the driver.
func validateCustomVerificationRequestColumns(req domain.CustomVerificationRequest) error {
	if len(req.PeerTitle) > maxCustomVerificationTitleBytes ||
		len(req.PeerUsername) > maxCustomVerificationUsernameBytes ||
		len(req.Reason) > maxCustomVerificationReasonBytes ||
		len(req.RequestedDescription) > maxCustomVerificationDescriptionBytes ||
		len(req.DecidedBy) > maxCustomVerificationDecidedByBytes ||
		len(req.DecisionReason) > maxCustomVerificationDecisionBytes ||
		len(req.InternalNote) > maxCustomVerificationNoteBytes ||
		len(req.CorrelationID) > maxCustomVerificationCorrelationBytes {
		return domain.ErrCustomVerificationRequestInvalid
	}
	return nil
}

// botVerificationTimeArg keeps a zero time out of a NOT NULL-paired column: the
// schema wants NULL, and pgx would otherwise write year 1.
func botVerificationTimeArg(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at.UTC()
}

// botVerificationPeerArrays turns the batch input into the two parallel arrays
// the single projection query unnests, dropping unverifiable peers and
// duplicates so one peer cannot cost two rows.
func botVerificationPeerArrays(peers []domain.Peer) ([]string, []int64) {
	types := make([]string, 0, len(peers))
	ids := make([]int64, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		if !validBotVerificationPeer(peer) {
			continue
		}
		if _, dup := seen[peer]; dup {
			continue
		}
		seen[peer] = struct{}{}
		types = append(types, string(peer.Type))
		ids = append(ids, peer.ID)
	}
	return types, ids
}

// validBotVerificationPeer mirrors the peer_type CHECK: only users and channels
// carry a third-party mark.
func validBotVerificationPeer(peer domain.Peer) bool {
	return botVerificationPeerType(peer.Type) && peer.ID > 0
}

func botVerificationPeerType(peerType domain.PeerType) bool {
	return peerType == domain.PeerTypeUser || peerType == domain.PeerTypeChannel
}

func botVerificationLimit(limit int) int {
	if limit <= 0 {
		return defaultBotVerificationListLimit
	}
	if limit > maxBotVerificationListLimit {
		return maxBotVerificationListLimit
	}
	return limit
}

// parseBotVerificationQuery splits an admin search term into its two shapes: a
// number addresses a row id or a peer id, anything else is text. Telegram
// usernames never start with a digit, so the two shapes cannot collide.
func parseBotVerificationQuery(query string) (numeric int64, isNumeric bool, text string) {
	query = strings.TrimSpace(query)
	query = strings.TrimPrefix(query, "@")
	query = strings.TrimSpace(query)
	if query == "" {
		return 0, false, ""
	}
	if id, err := strconv.ParseInt(query, 10, 64); err == nil && id > 0 {
		return id, true, ""
	}
	return 0, false, strings.ToLower(query)
}
