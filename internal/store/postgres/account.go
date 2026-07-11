package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

const accountPasswordsLoginEmailUniqueIdx = "account_passwords_login_email_lower_unique_idx"

// PasswordStore 用 PostgreSQL 实现 store.PasswordStore。
type PasswordStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewPasswordStore 基于 pgx 连接池（或事务）创建 PasswordStore。
func NewPasswordStore(db sqlcgen.DBTX) *PasswordStore {
	return &PasswordStore{db: db, q: sqlcgen.New(db)}
}

func (s *PasswordStore) GetByUser(ctx context.Context, userID int64) (domain.PasswordSettings, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT
  has_recovery, has_secure_values, has_password, hint,
  email_unconfirmed_pattern, login_email_pattern, secure_random,
  current_algo_salt1, current_algo_salt2, current_algo_g, current_algo_p,
  srp_id, srp_verifier, srp_b_secret, srp_b,
  recovery_email, recovery_code, recovery_code_expires_at, login_email
FROM account_passwords
WHERE user_id = $1`, userID)
	var settings domain.PasswordSettings
	var salt1, salt2, p []byte
	var recoveryExpires sql.NullTime
	if err := row.Scan(
		&settings.HasRecovery, &settings.HasSecureValues, &settings.HasPassword, &settings.Hint,
		&settings.EmailUnconfirmedPattern, &settings.LoginEmailPattern, &settings.SecureRandom,
		&salt1, &salt2, &settings.NewAlgo.G, &p,
		&settings.SRPID, &settings.SRPVerifier, &settings.SRPBSecret, &settings.SRPB,
		&settings.RecoveryEmail, &settings.RecoveryCode, &recoveryExpires, &settings.LoginEmail,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PasswordSettings{}, false, nil
		}
		return domain.PasswordSettings{}, false, fmt.Errorf("get account password: %w", err)
	}
	if len(salt1) > 0 || len(salt2) > 0 || len(p) > 0 || settings.NewAlgo.G != 0 {
		settings.CurrentAlgo = &domain.PasswordKDFAlgo{
			Salt1: append([]byte(nil), salt1...),
			Salt2: append([]byte(nil), salt2...),
			G:     settings.NewAlgo.G,
			P:     append([]byte(nil), p...),
		}
	}
	settings.NewAlgo.Salt1 = append([]byte(nil), salt1...)
	settings.NewAlgo.Salt2 = append([]byte(nil), salt2...)
	settings.NewAlgo.P = append([]byte(nil), p...)
	if recoveryExpires.Valid {
		settings.RecoveryCodeExpiresAt = recoveryExpires.Time.Unix()
	}
	settings.SecureRandom = append([]byte(nil), settings.SecureRandom...)
	settings.SRPVerifier = append([]byte(nil), settings.SRPVerifier...)
	settings.SRPBSecret = append([]byte(nil), settings.SRPBSecret...)
	settings.SRPB = append([]byte(nil), settings.SRPB...)
	return settings, true, nil
}

func (s *PasswordStore) LoginEmailOwner(ctx context.Context, email string) (int64, bool, error) {
	email = normalizeStoredLoginEmail(email)
	if email == "" {
		return 0, false, nil
	}
	row := s.db.QueryRow(ctx, `
SELECT user_id
FROM account_passwords
WHERE login_email <> '' AND lower(login_email) = $1
LIMIT 1`, email)
	var userID int64
	if err := row.Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get login email owner: %w", err)
	}
	return userID, true, nil
}

func (s *PasswordStore) Save(ctx context.Context, userID int64, settings domain.PasswordSettings) error {
	settings.LoginEmail = normalizeStoredLoginEmail(settings.LoginEmail)
	settings.LoginEmailPattern = domain.MaskEmail(settings.LoginEmail)
	algo := settings.NewAlgo
	if settings.CurrentAlgo != nil {
		algo = *settings.CurrentAlgo
	}
	var recoveryExpires any
	if settings.RecoveryCodeExpiresAt > 0 {
		recoveryExpires = time.Unix(settings.RecoveryCodeExpiresAt, 0)
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO account_passwords (
  user_id, has_recovery, has_secure_values, has_password, hint,
  email_unconfirmed_pattern, login_email_pattern, secure_random,
  current_algo_salt1, current_algo_salt2, current_algo_g, current_algo_p,
  srp_id, srp_verifier, srp_b_secret, srp_b,
  recovery_email, recovery_code, recovery_code_expires_at, login_email
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
ON CONFLICT (user_id) DO UPDATE SET
  has_recovery = EXCLUDED.has_recovery,
  has_secure_values = EXCLUDED.has_secure_values,
  has_password = EXCLUDED.has_password,
  hint = EXCLUDED.hint,
  email_unconfirmed_pattern = EXCLUDED.email_unconfirmed_pattern,
  login_email_pattern = EXCLUDED.login_email_pattern,
  secure_random = EXCLUDED.secure_random,
  current_algo_salt1 = EXCLUDED.current_algo_salt1,
  current_algo_salt2 = EXCLUDED.current_algo_salt2,
  current_algo_g = EXCLUDED.current_algo_g,
  current_algo_p = EXCLUDED.current_algo_p,
  srp_id = EXCLUDED.srp_id,
  srp_verifier = EXCLUDED.srp_verifier,
  srp_b_secret = EXCLUDED.srp_b_secret,
  srp_b = EXCLUDED.srp_b,
  recovery_email = EXCLUDED.recovery_email,
  recovery_code = EXCLUDED.recovery_code,
  recovery_code_expires_at = EXCLUDED.recovery_code_expires_at,
  login_email = EXCLUDED.login_email,
  updated_at = now()`,
		userID,
		settings.HasRecovery, settings.HasSecureValues, settings.HasPassword, settings.Hint,
		settings.EmailUnconfirmedPattern, settings.LoginEmailPattern, nonNilBytea(settings.SecureRandom),
		nonNilBytea(algo.Salt1), nonNilBytea(algo.Salt2), algo.G, nonNilBytea(algo.P),
		settings.SRPID, nonNilBytea(settings.SRPVerifier), nonNilBytea(settings.SRPBSecret), nonNilBytea(settings.SRPB),
		settings.RecoveryEmail, settings.RecoveryCode, recoveryExpires, settings.LoginEmail,
	)
	if err != nil {
		if isAccountPasswordLoginEmailUnique(err) {
			return domain.ErrEmailOccupied
		}
		return fmt.Errorf("upsert account password: %w", err)
	}
	return nil
}

func normalizeStoredLoginEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func isAccountPasswordLoginEmailUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == pgerrcode.UniqueViolation &&
		pgErr.ConstraintName == accountPasswordsLoginEmailUniqueIdx
}

func nonNilBytea(in []byte) []byte {
	if in != nil {
		return in
	}
	return []byte{}
}

func (s *PasswordStore) GetReactionSettings(ctx context.Context, userID int64) (domain.AccountReactionSettings, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT messages_notify_from, stories_notify_from, poll_votes_notify_from, show_previews,
       default_reaction_type, default_reaction_value,
       paid_privacy_kind, paid_privacy_peer_type, paid_privacy_peer_id
FROM account_reaction_settings
WHERE user_id = $1`, userID)
	var messagesFrom, storiesFrom, pollVotesFrom string
	var defaultType, defaultValue string
	var paidKind string
	var paidPeerType sql.NullString
	var paidPeerID sql.NullInt64
	settings := domain.DefaultAccountReactionSettings()
	if err := row.Scan(
		&messagesFrom, &storiesFrom, &pollVotesFrom, &settings.Notify.ShowPreviews,
		&defaultType, &defaultValue, &paidKind, &paidPeerType, &paidPeerID,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AccountReactionSettings{}, false, nil
		}
		return domain.AccountReactionSettings{}, false, fmt.Errorf("get account reaction settings: %w", err)
	}
	settings.Notify.MessagesFrom = domain.ReactionNotifyFrom(messagesFrom)
	settings.Notify.StoriesFrom = domain.ReactionNotifyFrom(storiesFrom)
	settings.Notify.PollVotesFrom = domain.ReactionNotifyFrom(pollVotesFrom)
	if reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(defaultType), defaultValue); ok {
		settings.DefaultReaction = reaction
	}
	settings.PaidPrivacy = domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyKind(paidKind)}
	if settings.PaidPrivacy.Kind == domain.PaidReactionPrivacyPeer && paidPeerType.Valid && paidPeerID.Valid {
		peer := domain.Peer{Type: domain.PeerType(paidPeerType.String), ID: paidPeerID.Int64}
		settings.PaidPrivacy.Peer = &peer
	}
	return settings, true, nil
}

func (s *PasswordStore) SaveReactionSettings(ctx context.Context, userID int64, settings domain.AccountReactionSettings) error {
	var paidPeerType any
	var paidPeerID any
	if settings.PaidPrivacy.Kind == domain.PaidReactionPrivacyPeer && settings.PaidPrivacy.Peer != nil {
		paidPeerType = string(settings.PaidPrivacy.Peer.Type)
		paidPeerID = settings.PaidPrivacy.Peer.ID
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO account_reaction_settings (
    user_id, messages_notify_from, stories_notify_from, poll_votes_notify_from, show_previews,
    default_reaction_type, default_reaction_value, paid_privacy_kind, paid_privacy_peer_type, paid_privacy_peer_id
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (user_id) DO UPDATE SET
    messages_notify_from = EXCLUDED.messages_notify_from,
    stories_notify_from = EXCLUDED.stories_notify_from,
    poll_votes_notify_from = EXCLUDED.poll_votes_notify_from,
    show_previews = EXCLUDED.show_previews,
    default_reaction_type = EXCLUDED.default_reaction_type,
    default_reaction_value = EXCLUDED.default_reaction_value,
    paid_privacy_kind = EXCLUDED.paid_privacy_kind,
    paid_privacy_peer_type = EXCLUDED.paid_privacy_peer_type,
    paid_privacy_peer_id = EXCLUDED.paid_privacy_peer_id,
    updated_at = now()`,
		userID,
		string(settings.Notify.MessagesFrom), string(settings.Notify.StoriesFrom), string(settings.Notify.PollVotesFrom), settings.Notify.ShowPreviews,
		string(settings.DefaultReaction.Type), settings.DefaultReaction.Value(),
		string(settings.PaidPrivacy.Kind), paidPeerType, paidPeerID,
	); err != nil {
		return fmt.Errorf("save account reaction settings: %w", err)
	}
	return nil
}

func (s *PasswordStore) GetAccountSettings(ctx context.Context, userID int64) (domain.AccountSettings, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT archive_and_mute_new_noncontact_peers, keep_archived_unmuted, keep_archived_folders,
       hide_read_marks, new_noncontact_peers_require_premium, display_gifts_button,
       noncontact_peers_paid_stars, account_ttl_days, sensitive_content_enabled, contact_signup_silent
FROM account_settings
WHERE user_id = $1`, userID)
	settings := domain.DefaultAccountSettings()
	gp := &settings.GlobalPrivacy
	if err := row.Scan(
		&gp.ArchiveAndMuteNewNoncontactPeers, &gp.KeepArchivedUnmuted, &gp.KeepArchivedFolders,
		&gp.HideReadMarks, &gp.NewNoncontactPeersRequirePremium, &gp.DisplayGiftsButton,
		&gp.NoncontactPeersPaidStars, &settings.AccountTTLDays, &settings.SensitiveContentEnabled, &settings.ContactSignUpSilent,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AccountSettings{}, false, nil
		}
		return domain.AccountSettings{}, false, fmt.Errorf("get account settings: %w", err)
	}
	return settings, true, nil
}

func (s *PasswordStore) SaveAccountSettings(ctx context.Context, userID int64, settings domain.AccountSettings) error {
	gp := settings.GlobalPrivacy
	if _, err := s.db.Exec(ctx, `
INSERT INTO account_settings (
    user_id, archive_and_mute_new_noncontact_peers, keep_archived_unmuted, keep_archived_folders,
    hide_read_marks, new_noncontact_peers_require_premium, display_gifts_button,
    noncontact_peers_paid_stars, account_ttl_days, sensitive_content_enabled, contact_signup_silent
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (user_id) DO UPDATE SET
    archive_and_mute_new_noncontact_peers = EXCLUDED.archive_and_mute_new_noncontact_peers,
    keep_archived_unmuted = EXCLUDED.keep_archived_unmuted,
    keep_archived_folders = EXCLUDED.keep_archived_folders,
    hide_read_marks = EXCLUDED.hide_read_marks,
    new_noncontact_peers_require_premium = EXCLUDED.new_noncontact_peers_require_premium,
    display_gifts_button = EXCLUDED.display_gifts_button,
    noncontact_peers_paid_stars = EXCLUDED.noncontact_peers_paid_stars,
    account_ttl_days = EXCLUDED.account_ttl_days,
    sensitive_content_enabled = EXCLUDED.sensitive_content_enabled,
    contact_signup_silent = EXCLUDED.contact_signup_silent,
    updated_at = now()`,
		userID,
		gp.ArchiveAndMuteNewNoncontactPeers, gp.KeepArchivedUnmuted, gp.KeepArchivedFolders,
		gp.HideReadMarks, gp.NewNoncontactPeersRequirePremium, gp.DisplayGiftsButton,
		gp.NoncontactPeersPaidStars, settings.NormalizedTTLDays(), settings.SensitiveContentEnabled, settings.ContactSignUpSilent,
	); err != nil {
		return fmt.Errorf("save account settings: %w", err)
	}
	return nil
}

func notifyScopeColumns(scope domain.NotifyScope) (kind, peerType string, peerID int64, topicID int) {
	kind = string(scope.Kind)
	if scope.Kind == domain.NotifyScopePeer {
		return kind, string(scope.Peer.Type), scope.Peer.ID, scope.TopicID
	}
	return kind, "", 0, 0
}

func scanNotifySettings(row pgx.Row) (domain.PeerNotifySettings, error) {
	var showPreviews, silent, storiesMuted, storiesHideSender sql.NullBool
	var muteUntil sql.NullInt32
	if err := row.Scan(&showPreviews, &silent, &muteUntil, &storiesMuted, &storiesHideSender); err != nil {
		return domain.PeerNotifySettings{}, err
	}
	out := domain.PeerNotifySettings{}
	if showPreviews.Valid {
		out.ShowPreviews = &showPreviews.Bool
	}
	if silent.Valid {
		out.Silent = &silent.Bool
	}
	if muteUntil.Valid {
		v := int(muteUntil.Int32)
		out.MuteUntil = &v
	}
	if storiesMuted.Valid {
		out.StoriesMuted = &storiesMuted.Bool
	}
	if storiesHideSender.Valid {
		out.StoriesHideSender = &storiesHideSender.Bool
	}
	return out, nil
}

func (s *PasswordStore) GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, bool, error) {
	kind, peerType, peerID, topicID := notifyScopeColumns(scope)
	row := s.db.QueryRow(ctx, `
SELECT show_previews, silent, mute_until, stories_muted, stories_hide_sender
FROM notify_settings
WHERE owner_user_id = $1 AND scope_kind = $2 AND peer_type = $3 AND peer_id = $4 AND topic_id = $5`,
		ownerUserID, kind, peerType, peerID, topicID)
	settings, err := scanNotifySettings(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PeerNotifySettings{}, false, nil
		}
		return domain.PeerNotifySettings{}, false, fmt.Errorf("get notify settings: %w", err)
	}
	return settings, true, nil
}

func (s *PasswordStore) SaveNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error {
	kind, peerType, peerID, topicID := notifyScopeColumns(scope)
	if _, err := s.db.Exec(ctx, `
INSERT INTO notify_settings (
    owner_user_id, scope_kind, peer_type, peer_id, topic_id,
    show_previews, silent, mute_until, stories_muted, stories_hide_sender
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (owner_user_id, scope_kind, peer_type, peer_id, topic_id) DO UPDATE SET
    show_previews = EXCLUDED.show_previews,
    silent = EXCLUDED.silent,
    mute_until = EXCLUDED.mute_until,
    stories_muted = EXCLUDED.stories_muted,
    stories_hide_sender = EXCLUDED.stories_hide_sender,
    updated_at = now()`,
		ownerUserID, kind, peerType, peerID, topicID,
		nullableBool(settings.ShowPreviews), nullableBool(settings.Silent), nullableInt(settings.MuteUntil),
		nullableBool(settings.StoriesMuted), nullableBool(settings.StoriesHideSender),
	); err != nil {
		return fmt.Errorf("save notify settings: %w", err)
	}
	return nil
}

func (s *PasswordStore) ResetNotifySettings(ctx context.Context, ownerUserID int64) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM notify_settings WHERE owner_user_id = $1`, ownerUserID); err != nil {
		return fmt.Errorf("reset notify settings: %w", err)
	}
	return nil
}

func (s *PasswordStore) GetPeerNotifySettings(ctx context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error) {
	out := make(map[domain.Peer]domain.PeerNotifySettings, len(peers))
	if len(peers) == 0 {
		return out, nil
	}
	types := make([]string, 0, len(peers))
	ids := make([]int64, 0, len(peers))
	for _, p := range peers {
		types = append(types, string(p.Type))
		ids = append(ids, p.ID)
	}
	rows, err := s.db.Query(ctx, `
SELECT peer_type, peer_id, show_previews, silent, mute_until, stories_muted, stories_hide_sender
FROM notify_settings
WHERE owner_user_id = $1 AND scope_kind = 'peer' AND topic_id = 0
  AND (peer_type, peer_id) IN (SELECT * FROM unnest($2::text[], $3::bigint[]))`,
		ownerUserID, types, ids)
	if err != nil {
		return nil, fmt.Errorf("batch get notify settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var peerType string
		var peerID int64
		var showPreviews, silent, storiesMuted, storiesHideSender sql.NullBool
		var muteUntil sql.NullInt32
		if err := rows.Scan(&peerType, &peerID, &showPreviews, &silent, &muteUntil, &storiesMuted, &storiesHideSender); err != nil {
			return nil, fmt.Errorf("scan notify settings: %w", err)
		}
		settings := domain.PeerNotifySettings{}
		if showPreviews.Valid {
			settings.ShowPreviews = &showPreviews.Bool
		}
		if silent.Valid {
			settings.Silent = &silent.Bool
		}
		if muteUntil.Valid {
			v := int(muteUntil.Int32)
			settings.MuteUntil = &v
		}
		if storiesMuted.Valid {
			settings.StoriesMuted = &storiesMuted.Bool
		}
		if storiesHideSender.Valid {
			settings.StoriesHideSender = &storiesHideSender.Bool
		}
		out[domain.Peer{Type: domain.PeerType(peerType), ID: peerID}] = settings
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notify settings: %w", err)
	}
	return out, nil
}

func (s *PasswordStore) AllPeerNotifySettings(ctx context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error) {
	// owner-scoped 单查询，走部分索引 notify_settings_owner_peer_idx（scope_kind='peer' AND topic_id=0）。
	rows, err := s.db.Query(ctx, `
SELECT peer_type, peer_id, show_previews, silent, mute_until, stories_muted, stories_hide_sender
FROM notify_settings
WHERE owner_user_id = $1 AND scope_kind = 'peer' AND topic_id = 0`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("all peer notify settings: %w", err)
	}
	defer rows.Close()
	out := make(map[domain.Peer]domain.PeerNotifySettings)
	for rows.Next() {
		var peerType string
		var peerID int64
		var showPreviews, silent, storiesMuted, storiesHideSender sql.NullBool
		var muteUntil sql.NullInt32
		if err := rows.Scan(&peerType, &peerID, &showPreviews, &silent, &muteUntil, &storiesMuted, &storiesHideSender); err != nil {
			return nil, fmt.Errorf("scan all peer notify settings: %w", err)
		}
		settings := domain.PeerNotifySettings{}
		if showPreviews.Valid {
			settings.ShowPreviews = &showPreviews.Bool
		}
		if silent.Valid {
			settings.Silent = &silent.Bool
		}
		if muteUntil.Valid {
			v := int(muteUntil.Int32)
			settings.MuteUntil = &v
		}
		if storiesMuted.Valid {
			settings.StoriesMuted = &storiesMuted.Bool
		}
		if storiesHideSender.Valid {
			settings.StoriesHideSender = &storiesHideSender.Bool
		}
		out[domain.Peer{Type: domain.PeerType(peerType), ID: peerID}] = settings
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate all peer notify settings: %w", err)
	}
	return out, nil
}

func (s *PasswordStore) ListNotifyExceptions(ctx context.Context, ownerUserID int64) ([]domain.NotifyException, error) {
	rows, err := s.db.Query(ctx, `
SELECT peer_type, peer_id, topic_id, show_previews, silent, mute_until, stories_muted, stories_hide_sender
FROM notify_settings
WHERE owner_user_id = $1 AND scope_kind = 'peer'
ORDER BY peer_type, peer_id, topic_id`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list notify exceptions: %w", err)
	}
	defer rows.Close()
	out := make([]domain.NotifyException, 0)
	for rows.Next() {
		var peerType string
		var peerID int64
		var topicID int
		var showPreviews, silent, storiesMuted, storiesHideSender sql.NullBool
		var muteUntil sql.NullInt32
		if err := rows.Scan(&peerType, &peerID, &topicID, &showPreviews, &silent, &muteUntil, &storiesMuted, &storiesHideSender); err != nil {
			return nil, fmt.Errorf("scan notify exception: %w", err)
		}
		settings := domain.PeerNotifySettings{}
		if showPreviews.Valid {
			settings.ShowPreviews = &showPreviews.Bool
		}
		if silent.Valid {
			settings.Silent = &silent.Bool
		}
		if muteUntil.Valid {
			v := int(muteUntil.Int32)
			settings.MuteUntil = &v
		}
		if storiesMuted.Valid {
			settings.StoriesMuted = &storiesMuted.Bool
		}
		if storiesHideSender.Valid {
			settings.StoriesHideSender = &storiesHideSender.Bool
		}
		if settings.IsZero() {
			continue // 全默认行（如曾静音后取消）不算异常
		}
		out = append(out, domain.NotifyException{
			Peer:     domain.Peer{Type: domain.PeerType(peerType), ID: peerID},
			TopicID:  topicID,
			Settings: settings,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notify exceptions: %w", err)
	}
	return out, nil
}

func (s *PasswordStore) SaveStickerCollectionItem(ctx context.Context, userID int64, kind domain.StickerCollectionKind, documentID int64, unsave bool, now, max int) error {
	if userID == 0 || documentID == 0 {
		return domain.ErrStickerInvalid
	}
	if unsave {
		if _, err := s.db.Exec(ctx, `DELETE FROM user_sticker_collections WHERE owner_user_id = $1 AND kind = $2 AND document_id = $3`,
			userID, string(kind), documentID); err != nil {
			return fmt.Errorf("unsave sticker collection item: %w", err)
		}
		return nil
	}
	if max <= 0 {
		max = domain.MaxStickerCollectionItems(kind)
	}
	return withTx(ctx, s.db, "save sticker collection item", func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
INSERT INTO user_sticker_collections (owner_user_id, kind, document_id, used_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (owner_user_id, kind, document_id) DO UPDATE
SET used_at = EXCLUDED.used_at,
    order_key = nextval('user_sticker_collections_order_key_seq')`,
			userID, string(kind), documentID, now); err != nil {
			return fmt.Errorf("upsert sticker collection item: %w", err)
		}
		// 截断超上界：单次有序窗口扫描（索引 user_sticker_collections_order_idx 服务
		// order_key DESC 排序），按 ctid 删除排名 > max 的旧项，避免 NOT IN 双扫全集。
		if _, err := tx.Exec(ctx, `
DELETE FROM user_sticker_collections
WHERE ctid IN (
  SELECT ctid FROM (
    SELECT ctid, ROW_NUMBER() OVER (ORDER BY order_key DESC) AS rn
    FROM user_sticker_collections
    WHERE owner_user_id = $1 AND kind = $2
  ) t WHERE rn > $3
)`, userID, string(kind), max); err != nil {
			return fmt.Errorf("trim sticker collection: %w", err)
		}
		return nil
	})
}

func (s *PasswordStore) ListStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind, limit int) ([]domain.StickerCollectionItem, error) {
	if userID == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > domain.MaxStickerCollectionItems(kind) {
		limit = domain.MaxStickerCollectionItems(kind)
	}
	rows, err := s.db.Query(ctx, `
SELECT document_id, used_at
FROM user_sticker_collections
WHERE owner_user_id = $1 AND kind = $2
ORDER BY order_key DESC
LIMIT $3`, userID, string(kind), limit)
	if err != nil {
		return nil, fmt.Errorf("list sticker collection: %w", err)
	}
	defer rows.Close()
	out := make([]domain.StickerCollectionItem, 0, limit)
	for rows.Next() {
		var item domain.StickerCollectionItem
		if err := rows.Scan(&item.DocumentID, &item.Date); err != nil {
			return nil, fmt.Errorf("scan sticker collection item: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sticker collection: %w", err)
	}
	return out, nil
}

func (s *PasswordStore) ClearStickerCollection(ctx context.Context, userID int64, kind domain.StickerCollectionKind) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM user_sticker_collections WHERE owner_user_id = $1 AND kind = $2`,
		userID, string(kind)); err != nil {
		return fmt.Errorf("clear sticker collection: %w", err)
	}
	return nil
}

func (s *PasswordStore) InstallUserStickerSet(ctx context.Context, userID int64, setID int64, kind domain.StickerSetKind, archived bool, installedDate int) error {
	if userID == 0 || setID == 0 {
		return domain.ErrStickerInvalid
	}
	orderValue := int64(installedDate) << 32
	_, err := s.db.Exec(ctx, `
INSERT INTO user_sticker_sets (owner_user_id, sticker_set_id, set_kind, archived, installed_date, order_value, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (owner_user_id, sticker_set_id) DO UPDATE SET
  set_kind = EXCLUDED.set_kind,
  archived = EXCLUDED.archived,
  installed_date = CASE
    WHEN user_sticker_sets.installed_date = 0 THEN EXCLUDED.installed_date
    ELSE user_sticker_sets.installed_date
  END,
  order_value = EXCLUDED.order_value,
  updated_at = now()`,
		userID, setID, string(kind), archived, installedDate, orderValue)
	if err != nil {
		return fmt.Errorf("install user sticker set: %w", err)
	}
	return nil
}

func (s *PasswordStore) UninstallUserStickerSet(ctx context.Context, userID int64, setID int64) error {
	if _, err := s.db.Exec(ctx, `
DELETE FROM user_sticker_sets
WHERE owner_user_id = $1 AND sticker_set_id = $2`, userID, setID); err != nil {
		return fmt.Errorf("uninstall user sticker set: %w", err)
	}
	return nil
}

func (s *PasswordStore) SetUserStickerSetArchived(ctx context.Context, userID int64, setID int64, archived bool, now int) error {
	orderValue := int64(now) << 32
	_, err := s.db.Exec(ctx, `
UPDATE user_sticker_sets
SET archived = $3,
    order_value = CASE WHEN $3::boolean = false AND $4::bigint > 0 THEN $4::bigint ELSE order_value END,
    updated_at = now()
WHERE owner_user_id = $1 AND sticker_set_id = $2`, userID, setID, archived, orderValue)
	if err != nil {
		return fmt.Errorf("set user sticker set archived: %w", err)
	}
	return nil
}

func (s *PasswordStore) ReorderUserStickerSets(ctx context.Context, userID int64, kind domain.StickerSetKind, order []int64, now int) error {
	if len(order) == 0 {
		return nil
	}
	return withTx(ctx, s.db, "reorder user sticker sets", func(tx pgx.Tx) error {
		orderValue := int64(now) << 32
		for _, id := range order {
			if _, err := tx.Exec(ctx, `
UPDATE user_sticker_sets
SET order_value = $4, updated_at = now()
WHERE owner_user_id = $1 AND set_kind = $2 AND sticker_set_id = $3`,
				userID, string(kind), id, orderValue); err != nil {
				return fmt.Errorf("update user sticker set order: %w", err)
			}
			orderValue--
		}
		return nil
	})
}

func (s *PasswordStore) ListUserStickerSets(ctx context.Context, userID int64, kind domain.StickerSetKind, archived *bool, offsetID int64, limit int) ([]domain.UserStickerSet, int, error) {
	if userID == 0 {
		return nil, 0, nil
	}
	if limit <= 0 || limit > domain.MaxInstalledStickerSets {
		limit = domain.MaxInstalledStickerSets
	}
	var archivedFilter any
	if archived != nil {
		archivedFilter = *archived
	}
	rows, err := s.db.Query(ctx, `
WITH ordered AS (
  SELECT owner_user_id, sticker_set_id, set_kind, archived, installed_date, order_value,
         ROW_NUMBER() OVER (ORDER BY order_value DESC, sticker_set_id DESC) AS rn,
         COUNT(*) OVER () AS total
  FROM user_sticker_sets
  WHERE owner_user_id = $1
    AND set_kind = $2
    AND ($3::boolean IS NULL OR archived = $3::boolean)
),
page AS (
  SELECT COALESCE((SELECT rn FROM ordered WHERE sticker_set_id = $4), 0) AS offset_rn
)
SELECT owner_user_id, sticker_set_id, set_kind, archived, installed_date, order_value, total
FROM ordered, page
WHERE ordered.rn > page.offset_rn
ORDER BY ordered.rn ASC
LIMIT $5`, userID, string(kind), archivedFilter, offsetID, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list user sticker sets: %w", err)
	}
	defer rows.Close()
	out := make([]domain.UserStickerSet, 0, limit)
	total := 0
	for rows.Next() {
		var (
			item     domain.UserStickerSet
			kindText string
		)
		if err := rows.Scan(&item.OwnerUserID, &item.StickerSetID, &kindText, &item.Archived, &item.InstalledDate, &item.OrderValue, &total); err != nil {
			return nil, 0, fmt.Errorf("scan user sticker set: %w", err)
		}
		item.Kind = domain.StickerSetKind(kindText)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate user sticker sets: %w", err)
	}
	return out, total, nil
}

func nullableBool(v *bool) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func (s *PasswordStore) SaveMusic(ctx context.Context, req domain.SaveMusicRequest) error {
	if req.UserID == 0 || req.Document.ID == 0 || !req.Document.IsMusic() {
		return domain.ErrDocumentInvalid
	}
	return withTx(ctx, s.db, "save account music", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
SELECT document_id
FROM saved_music
WHERE user_id = $1
ORDER BY sort_order ASC, document_id ASC
FOR UPDATE`, req.UserID)
		if err != nil {
			return fmt.Errorf("lock saved music: %w", err)
		}
		defer rows.Close()
		current := make([]int64, 0, domain.MaxSavedMusicItems)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("scan saved music id: %w", err)
			}
			current = append(current, id)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("scan saved music ids: %w", err)
		}
		if req.Unsave {
			if _, err := tx.Exec(ctx, `DELETE FROM saved_music WHERE user_id = $1 AND document_id = $2`, req.UserID, req.Document.ID); err != nil {
				return fmt.Errorf("delete saved music: %w", err)
			}
			return nil
		}
		exists := false
		for _, id := range current {
			if id == req.Document.ID {
				exists = true
				break
			}
		}
		if req.AfterDocumentID == req.Document.ID {
			if exists {
				return nil
			}
			return domain.ErrDocumentInvalid
		}
		next := make([]int64, 0, len(current)+1)
		afterIndex := -1
		for _, id := range current {
			if id == req.Document.ID {
				continue
			}
			if id == req.AfterDocumentID {
				afterIndex = len(next)
			}
			next = append(next, id)
		}
		if req.AfterDocumentID != 0 {
			if afterIndex < 0 {
				return domain.ErrDocumentInvalid
			}
			next = append(next, 0)
			copy(next[afterIndex+2:], next[afterIndex+1:])
			next[afterIndex+1] = req.Document.ID
		} else {
			next = append([]int64{req.Document.ID}, next...)
		}
		if len(next) > domain.MaxSavedMusicItems {
			next = next[:domain.MaxSavedMusicItems]
		}
		if _, err := tx.Exec(ctx, `DELETE FROM saved_music WHERE user_id = $1`, req.UserID); err != nil {
			return fmt.Errorf("clear saved music order: %w", err)
		}
		for i, id := range next {
			if _, err := tx.Exec(ctx, `
INSERT INTO saved_music (user_id, document_id, sort_order, created_at, updated_at)
VALUES ($1, $2, $3, now(), now())`, req.UserID, id, i+1); err != nil {
				return fmt.Errorf("insert saved music: %w", err)
			}
		}
		return nil
	})
}

func (s *PasswordStore) ListSavedMusicIDs(ctx context.Context, userID int64, limit int) ([]int64, error) {
	if userID == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > domain.MaxSavedMusicItems {
		limit = domain.MaxSavedMusicItems
	}
	rows, err := s.db.Query(ctx, `
SELECT document_id
FROM saved_music
WHERE user_id = $1
ORDER BY sort_order ASC, document_id ASC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list saved music ids: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan saved music id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan saved music ids: %w", err)
	}
	return ids, nil
}

func (s *PasswordStore) ListSavedMusic(ctx context.Context, userID int64, offset, limit int) (domain.SavedMusicList, error) {
	out := domain.SavedMusicList{UserID: userID}
	if userID == 0 {
		return out, nil
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM saved_music WHERE user_id = $1`, userID).Scan(&out.Count); err != nil {
		return domain.SavedMusicList{}, fmt.Errorf("count saved music: %w", err)
	}
	if limit <= 0 || offset < 0 || offset >= out.Count {
		return out, nil
	}
	if limit > domain.MaxSavedMusicItems {
		limit = domain.MaxSavedMusicItems
	}
	docs, err := s.querySavedMusicDocuments(ctx, `
SELECT d.id, d.access_hash, d.file_reference, d.date, d.mime_type, d.size, d.dc_id,
       d.attributes::text AS attributes_json, d.thumbs::text AS thumbs_json
FROM saved_music sm
JOIN documents d ON d.id = sm.document_id
WHERE sm.user_id = $1
ORDER BY sm.sort_order ASC, sm.document_id ASC
OFFSET $2
LIMIT $3`, userID, offset, limit)
	if err != nil {
		return domain.SavedMusicList{}, err
	}
	out.Documents = docs
	return out, nil
}

func (s *PasswordStore) GetSavedMusicByIDs(ctx context.Context, userID int64, ids []int64) (domain.SavedMusicList, error) {
	out := domain.SavedMusicList{UserID: userID}
	if userID == 0 || len(ids) == 0 {
		return out, nil
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM saved_music WHERE user_id = $1`, userID).Scan(&out.Count); err != nil {
		return domain.SavedMusicList{}, fmt.Errorf("count saved music: %w", err)
	}
	docs, err := s.querySavedMusicDocuments(ctx, `
SELECT d.id, d.access_hash, d.file_reference, d.date, d.mime_type, d.size, d.dc_id,
       d.attributes::text AS attributes_json, d.thumbs::text AS thumbs_json
FROM saved_music sm
JOIN documents d ON d.id = sm.document_id
WHERE sm.user_id = $1
  AND sm.document_id = ANY($2::bigint[])
ORDER BY sm.sort_order ASC, sm.document_id ASC`, userID, ids)
	if err != nil {
		return domain.SavedMusicList{}, err
	}
	byID := make(map[int64]domain.Document, len(docs))
	for _, doc := range docs {
		byID[doc.ID] = doc
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if doc, ok := byID[id]; ok {
			out.Documents = append(out.Documents, doc)
		}
	}
	return out, nil
}

func (s *PasswordStore) querySavedMusicDocuments(ctx context.Context, sql string, args ...any) ([]domain.Document, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query saved music documents: %w", err)
	}
	defer rows.Close()
	docs := make([]domain.Document, 0)
	for rows.Next() {
		var row sqlcgen.GetDocumentRow
		if err := rows.Scan(
			&row.ID,
			&row.AccessHash,
			&row.FileReference,
			&row.Date,
			&row.MimeType,
			&row.Size,
			&row.DcID,
			&row.AttributesJson,
			&row.ThumbsJson,
		); err != nil {
			return nil, fmt.Errorf("scan saved music document: %w", err)
		}
		doc, err := documentFromRow(row)
		if err != nil {
			return nil, err
		}
		if doc.IsMusic() {
			docs = append(docs, doc)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan saved music documents: %w", err)
	}
	return docs, nil
}
