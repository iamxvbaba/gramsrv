package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// UserStore implements store.UserStore with PostgreSQL.
type UserStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

const officialUsernameClaimAttempts = 3

var errOfficialUsernameClaimRetry = errors.New("official username claim changed concurrently")

// OfficialUsernameClaimResult reports the authoritative 777000 username
// reconciliation performed during startup. DisplacedUserID is set only when an
// ordinary account's editable username was cleared; bots, other built-in users,
// channels and collectible names are never silently seized.
type OfficialUsernameClaimResult struct {
	Official        domain.User
	DisplacedUserID int64
	Changed         bool
}

// NewUserStore creates a UserStore on top of a pgx pool (or transaction).
func NewUserStore(db sqlcgen.DBTX) *UserStore {
	return &UserStore{db: db, q: sqlcgen.New(db)}
}

func (s *UserStore) ByID(ctx context.Context, id int64) (domain.User, bool, error) {
	row, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, false, nil
		}
		return domain.User{}, false, fmt.Errorf("get user by id: %w", err)
	}
	return userFromModel(row), true, nil
}

func (s *UserStore) ByIDs(ctx context.Context, ids []int64) ([]domain.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.q.GetUsersByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("get users by ids: %w", err)
	}
	out := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, userFromModel(row))
	}
	return out, nil
}

func (s *UserStore) ByPhone(ctx context.Context, phone string) (domain.User, bool, error) {
	// A bot row's phone is the empty string (since 0090 phone uniqueness covers only non-empty values), so an empty lookup must report not-found.
	if phone == "" {
		return domain.User{}, false, nil
	}
	row, err := s.q.GetUserByPhone(ctx, phone)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, false, nil
		}
		return domain.User{}, false, fmt.Errorf("get user by phone: %w", err)
	}
	return userFromModel(row), true, nil
}

func (s *UserStore) ByPhones(ctx context.Context, phones []string) ([]domain.User, error) {
	filtered := make([]string, 0, len(phones))
	for _, phone := range phones {
		if phone != "" {
			filtered = append(filtered, phone)
		}
	}
	phones = filtered
	if len(phones) == 0 {
		return nil, nil
	}
	rows, err := s.q.GetUsersByPhones(ctx, phones)
	if err != nil {
		return nil, fmt.Errorf("get users by phones: %w", err)
	}
	out := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, userFromModel(row))
	}
	return out, nil
}

func (s *UserStore) ByUsername(ctx context.Context, username string) (domain.User, bool, error) {
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	if username == "" {
		return domain.User{}, false, nil
	}
	row, err := s.q.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The scalar users.username column only holds the editable slot, so a
			// collectible username resolves through the registry instead. This is a
			// fallback rather than the primary path: the fast lookup above stays
			// untouched for every pre-existing username.
			return s.byCollectibleUsername(ctx, strings.ToLower(username))
		}
		return domain.User{}, false, fmt.Errorf("get user by username: %w", err)
	}
	return userFromModel(row), true, nil
}

// byCollectibleUsername resolves an active collectible username to its holder.
// An inactive (client-hidden) name stays occupied but must not resolve.
func (s *UserStore) byCollectibleUsername(ctx context.Context, usernameLower string) (domain.User, bool, error) {
	owner, found, err := getPeerUsernameOwner(ctx, s.db, usernameLower, false)
	if err != nil {
		return domain.User{}, false, fmt.Errorf("get user by collectible username: %w", err)
	}
	if !found || !owner.collectible || !owner.active || owner.peerType != peerUsernameTypeUser {
		return domain.User{}, false, nil
	}
	return s.ByID(ctx, owner.peerID)
}

func (s *UserStore) CheckUsername(ctx context.Context, userID int64, username string) (bool, error) {
	usernameLower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	if usernameLower == "" {
		return true, nil
	}
	return peerUsernameAvailable(ctx, s.db, usernameLower, peerUsernameTypeUser, userID)
}

func (s *UserStore) Search(ctx context.Context, currentUserID int64, query, phoneQuery string, limit int) (domain.UserSearchResult, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if currentUserID == 0 || query == "" {
		return domain.UserSearchResult{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	rows, err := s.q.SearchUsers(ctx, sqlcgen.SearchUsersParams{
		CurrentUserID: currentUserID,
		QueryLower:    query,
		QueryLike:     escapeLike(query),
		PhoneQuery:    phoneQuery,
		LimitCount:    int32(limit),
	})
	if err != nil {
		return domain.UserSearchResult{}, fmt.Errorf("search users: %w", err)
	}
	out := domain.UserSearchResult{
		MyResults: make([]domain.User, 0, len(rows)),
		Results:   make([]domain.User, 0, len(rows)),
	}
	for _, row := range rows {
		collectible := mustDecodeEmojiStatusCollectible(row.EmojiStatusCollectibleID, row.EmojiStatusCollectible)
		u := domain.User{
			ID:                     row.ID,
			AccessHash:             row.AccessHash,
			Phone:                  row.Phone,
			FirstName:              row.FirstName,
			LastName:               row.LastName,
			About:                  row.About,
			Username:               row.Username,
			CountryCode:            row.CountryCode,
			Verified:               row.Verified,
			Support:                row.Support,
			Bot:                    row.IsBot,
			BotInfoVersion:         int(row.BotInfoVersion),
			PremiumUntil:           premiumUntilFromModel(row.PremiumExpiresAt),
			EmojiStatusDocumentID:  row.EmojiStatusDocumentID,
			EmojiStatusUntil:       int(row.EmojiStatusUntil),
			EmojiStatusCollectible: collectible,
			Color:                  peerColorFromModel(row.ColorSet, row.Color, row.ColorBackgroundEmojiID),
			ProfileColor:           peerColorFromModel(row.ProfileColorSet, row.ProfileColor, row.ProfileColorBackgroundEmojiID),
			LinkedCommunityID:      row.LinkedCommunityID,
			LastSeenAt:             int(row.LastSeenAt),
			Contact:                row.Contact,
			Mutual:                 row.Mutual,
		}
		if row.Contact {
			out.MyResults = append(out.MyResults, u)
		} else {
			out.Results = append(out.Results, u)
		}
	}
	return out, nil
}

func (s *UserStore) UpdateProfile(ctx context.Context, userID int64, firstName, lastName, about string) (domain.User, error) {
	row, err := s.q.UpdateUserProfile(ctx, sqlcgen.UpdateUserProfileParams{
		ID:        userID,
		FirstName: firstName,
		LastName:  lastName,
		About:     about,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrFirstNameInvalid
		}
		return domain.User{}, fmt.Errorf("update user profile: %w", err)
	}
	return userFromModel(row), nil
}

func (s *UserStore) UpdateUsername(ctx context.Context, userID int64, username string) (domain.User, error) {
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	usernameLower := strings.ToLower(username)
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.User{}, fmt.Errorf("update user username: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin update user username: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	qtx := s.q.WithTx(tx)
	var lockedUserID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&lockedUserID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUsernameNotOccupied
		}
		return domain.User{}, fmt.Errorf("lock user for username update: %w", err)
	}
	if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, userID, username, usernameLower); err != nil {
		return domain.User{}, err
	}
	row, err := qtx.UpdateUserUsername(ctx, sqlcgen.UpdateUserUsernameParams{
		ID:       userID,
		Username: username,
	})
	if err != nil {
		if isUniqueConstraint(err, "users_username_lower_unique_idx") {
			return domain.User{}, domain.ErrUsernameOccupied
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUsernameNotOccupied
		}
		return domain.User{}, fmt.Errorf("update user username: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit update user username: %w", err)
	}
	committed = true
	return userFromModel(row), nil
}

// ClaimOfficialUsername makes the configured product username authoritative
// for the official 777000 account. If an ordinary user currently owns that
// editable username, the user's slot is cleared and 777000 claims it in the same
// transaction. The method deliberately refuses to seize bots, other system
// users, channels, collectible assets or non-editable registry rows.
func (s *UserStore) ClaimOfficialUsername(ctx context.Context, username string) (OfficialUsernameClaimResult, error) {
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	usernameLower := strings.ToLower(username)
	if usernameLower == "" {
		return OfficialUsernameClaimResult{}, domain.ErrUsernameInvalid
	}
	var lastErr error
	for attempt := 0; attempt < officialUsernameClaimAttempts; attempt++ {
		result, err := s.claimOfficialUsernameOnce(ctx, username, usernameLower)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil || (!errors.Is(err, errOfficialUsernameClaimRetry) && !isRetryablePostgresTxError(err)) {
			return OfficialUsernameClaimResult{}, err
		}
		lastErr = err
	}
	return OfficialUsernameClaimResult{}, lastErr
}

func (s *UserStore) claimOfficialUsernameOnce(ctx context.Context, username, usernameLower string) (OfficialUsernameClaimResult, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return OfficialUsernameClaimResult{}, fmt.Errorf("claim official username: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return OfficialUsernameClaimResult{}, fmt.Errorf("begin official username claim: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	// Discover the user rows that can participate, then lock them in numeric
	// order. Ordinary UpdateUsername locks its user before the registry row; this
	// order avoids reversing that dependency during a rolling restart.
	lockIDs := map[int64]struct{}{domain.OfficialSystemUserID: {}}
	if holderID, found, err := usernameScalarHolder(ctx, tx, usernameLower); err != nil {
		return OfficialUsernameClaimResult{}, err
	} else if found {
		lockIDs[holderID] = struct{}{}
	}
	if owner, found, err := getPeerUsernameOwner(ctx, tx, usernameLower, false); err != nil {
		return OfficialUsernameClaimResult{}, err
	} else if found && owner.peerType == peerUsernameTypeUser {
		lockIDs[owner.peerID] = struct{}{}
	}
	ids := make([]int64, 0, len(lockIDs))
	for id := range lockIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	type lockedUser struct {
		bot bool
	}
	locked := make(map[int64]lockedUser, len(ids))
	rows, err := tx.Query(ctx, `
SELECT id, is_bot
FROM users
WHERE id = ANY($1::bigint[]) AND deleted_at IS NULL
ORDER BY id
FOR UPDATE`, ids)
	if err != nil {
		return OfficialUsernameClaimResult{}, fmt.Errorf("lock users for official username claim: %w", err)
	}
	for rows.Next() {
		var id int64
		var item lockedUser
		if err := rows.Scan(&id, &item.bot); err != nil {
			rows.Close()
			return OfficialUsernameClaimResult{}, fmt.Errorf("scan user for official username claim: %w", err)
		}
		locked[id] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return OfficialUsernameClaimResult{}, fmt.Errorf("iterate users for official username claim: %w", err)
	}
	rows.Close()
	if _, found := locked[domain.OfficialSystemUserID]; !found {
		return OfficialUsernameClaimResult{}, domain.ErrUserNotFound
	}

	// Re-read both ownership facts after the row locks. A newly observed user was
	// not locked in the stable order above, so retry the whole transaction.
	owner, ownerFound, err := getPeerUsernameOwner(ctx, tx, usernameLower, true)
	if err != nil {
		return OfficialUsernameClaimResult{}, err
	}
	holderID, holderFound, err := usernameScalarHolder(ctx, tx, usernameLower)
	if err != nil {
		return OfficialUsernameClaimResult{}, err
	}
	if holderFound {
		if _, found := locked[holderID]; !found {
			return OfficialUsernameClaimResult{}, errOfficialUsernameClaimRetry
		}
	}
	if ownerFound && owner.peerType == peerUsernameTypeUser {
		if _, found := locked[owner.peerID]; !found {
			return OfficialUsernameClaimResult{}, errOfficialUsernameClaimRetry
		}
	}

	ordinaryUser := func(userID int64) bool {
		item, found := locked[userID]
		return found && !item.bot && !domain.IsSystemUserID(userID)
	}
	if holderFound && holderID != domain.OfficialSystemUserID && !ordinaryUser(holderID) {
		return OfficialUsernameClaimResult{}, domain.ErrUsernameOccupied
	}
	if ownerFound {
		allowedOfficialSlot := owner.matches(peerUsernameTypeUser, domain.OfficialSystemUserID) && owner.editable && !owner.collectible
		allowedOrdinarySlot := owner.peerType == peerUsernameTypeUser && owner.editable && !owner.collectible && ordinaryUser(owner.peerID)
		if !allowedOfficialSlot && !allowedOrdinarySlot {
			return OfficialUsernameClaimResult{}, domain.ErrUsernameOccupied
		}
	}

	qtx := s.q.WithTx(tx)
	officialRow, err := qtx.GetUserByID(ctx, domain.OfficialSystemUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OfficialUsernameClaimResult{}, domain.ErrUserNotFound
		}
		return OfficialUsernameClaimResult{}, fmt.Errorf("get official user during username claim: %w", err)
	}
	if holderFound && holderID == domain.OfficialSystemUserID && ownerFound && owner.matches(peerUsernameTypeUser, domain.OfficialSystemUserID) &&
		owner.editable && !owner.collectible && officialRow.Username == username {
		return OfficialUsernameClaimResult{Official: userFromModel(officialRow)}, nil
	}

	result := OfficialUsernameClaimResult{Changed: true}
	if holderFound && holderID != domain.OfficialSystemUserID {
		if _, err := tx.Exec(ctx, `UPDATE users SET username = '', updated_at = now() WHERE id = $1`, holderID); err != nil {
			return OfficialUsernameClaimResult{}, fmt.Errorf("clear displaced product username: %w", err)
		}
		result.DisplacedUserID = holderID
	}
	if _, err := tx.Exec(ctx, `DELETE FROM peer_usernames WHERE username_lower = $1`, usernameLower); err != nil {
		return OfficialUsernameClaimResult{}, fmt.Errorf("release product username registry slot: %w", err)
	}
	if err := deletePeerUsernameTx(ctx, tx, peerUsernameTypeUser, domain.OfficialSystemUserID); err != nil {
		return OfficialUsernameClaimResult{}, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO peer_usernames (username_lower, peer_type, peer_id, username, active, editable, sort_order, collectible_id)
VALUES ($1, 'user', $2, $3, true, true, 0, NULL)`, usernameLower, domain.OfficialSystemUserID, username); err != nil {
		if isUniqueViolation(err) {
			return OfficialUsernameClaimResult{}, errOfficialUsernameClaimRetry
		}
		return OfficialUsernameClaimResult{}, fmt.Errorf("claim official username registry slot: %w", err)
	}
	officialRow, err = qtx.UpdateUserUsername(ctx, sqlcgen.UpdateUserUsernameParams{
		ID:       domain.OfficialSystemUserID,
		Username: username,
	})
	if err != nil {
		if isUniqueConstraint(err, "users_username_lower_unique_idx") {
			return OfficialUsernameClaimResult{}, errOfficialUsernameClaimRetry
		}
		return OfficialUsernameClaimResult{}, fmt.Errorf("update official username: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return OfficialUsernameClaimResult{}, fmt.Errorf("commit official username claim: %w", err)
	}
	committed = true
	result.Official = userFromModel(officialRow)
	return result, nil
}

func usernameScalarHolder(ctx context.Context, db sqlcgen.DBTX, usernameLower string) (int64, bool, error) {
	var userID int64
	err := db.QueryRow(ctx, `
SELECT id
FROM users
WHERE deleted_at IS NULL AND lower(username) = $1
LIMIT 1`, usernameLower).Scan(&userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get scalar username holder: %w", err)
	}
	return userID, true, nil
}

// UpdatePhone is the non-PTS admin profile write. updateUserPhone has no
// pts/pts_count, so this path relies on the users phone unique index without
// manufacturing a durable difference event.
func (s *UserStore) UpdatePhone(ctx context.Context, userID int64, phone string) (domain.User, error) {
	row, err := s.q.UpdateUserPhone(ctx, sqlcgen.UpdateUserPhoneParams{ID: userID, Phone: phone})
	if err != nil {
		if isUniqueConstraint(err, "users_phone_unique_idx") {
			return domain.User{}, domain.ErrPhoneNumberOccupied
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("update user phone: %w", err)
	}
	return userFromModel(row), nil
}

func (s *UserStore) UpdateLastSeen(ctx context.Context, userID int64, lastSeenAt int) error {
	if lastSeenAt <= 0 {
		return nil
	}
	if err := s.q.UpdateUserLastSeen(ctx, sqlcgen.UpdateUserLastSeenParams{
		ID:         userID,
		LastSeenAt: int64(lastSeenAt),
	}); err != nil {
		return fmt.Errorf("update user last seen: %w", err)
	}
	return nil
}

// UpdateLastSeenBatch applies a set of monotonic presence watermarks with one
// PostgreSQL round trip. Duplicate user IDs are collapsed to their maximum
// timestamp before the query so UPDATE ... FROM never has an ambiguous source
// row. Missing/deleted users are intentionally ignored, matching the ordinary
// UpdateLastSeen WHERE boundary.
func (s *UserStore) UpdateLastSeenBatch(ctx context.Context, updates []store.UserLastSeenUpdate) error {
	latest := make(map[int64]int, len(updates))
	for _, update := range updates {
		if update.UserID == 0 || update.LastSeenAt <= 0 {
			continue
		}
		if current := latest[update.UserID]; update.LastSeenAt > current {
			latest[update.UserID] = update.LastSeenAt
		}
	}
	if len(latest) == 0 {
		return nil
	}
	userIDs := make([]int64, 0, len(latest))
	for userID := range latest {
		userIDs = append(userIDs, userID)
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	lastSeen := make([]int64, len(userIDs))
	for index, userID := range userIDs {
		lastSeen[index] = int64(latest[userID])
	}
	if _, err := s.db.Exec(ctx, `
WITH incoming AS MATERIALIZED (
  SELECT user_id, last_seen_at
  FROM unnest($1::bigint[], $2::bigint[]) AS value(user_id, last_seen_at)
), locked AS MATERIALIZED (
  SELECT target.id, incoming.last_seen_at
  FROM users AS target
  JOIN incoming ON incoming.user_id = target.id
  WHERE target.deleted_at IS NULL
  ORDER BY target.id
  FOR UPDATE OF target
)
UPDATE users AS target
SET last_seen_at = GREATEST(target.last_seen_at, locked.last_seen_at),
    updated_at = now()
FROM locked
WHERE target.id = locked.id
`, userIDs, lastSeen); err != nil {
		return fmt.Errorf("update user last seen batch: %w", err)
	}
	return nil
}

func (s *UserStore) Create(ctx context.Context, u domain.User) (domain.User, error) {
	u.Username = strings.TrimSpace(strings.TrimPrefix(u.Username, "@"))
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.User{}, fmt.Errorf("create user: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin create user: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	qtx := s.q.WithTx(tx)
	row, err := qtx.CreateUser(ctx, sqlcgen.CreateUserParams{
		AccessHash:       u.AccessHash,
		Phone:            u.Phone,
		FirstName:        u.FirstName,
		LastName:         u.LastName,
		Username:         u.Username,
		CountryCode:      u.CountryCode,
		PremiumExpiresAt: premiumUntilToModel(u.PremiumUntil),
	})
	if err != nil {
		if isUniqueConstraint(err, "users_username_lower_unique_idx") {
			return domain.User{}, domain.ErrUsernameOccupied
		}
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}
	usernameLower := strings.ToLower(row.Username)
	if usernameLower != "" {
		if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, row.ID, row.Username, usernameLower); err != nil {
			return domain.User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit create user: %w", err)
	}
	committed = true
	return userFromModel(row), nil
}

// SetPremiumUntil sets the Premium expiry to an absolute Unix second (0 = clear Premium).
func (s *UserStore) SetPremiumUntil(ctx context.Context, userID int64, until int) (domain.User, error) {
	row, err := s.q.SetUserPremiumUntil(ctx, sqlcgen.SetUserPremiumUntilParams{
		ID:               userID,
		PremiumExpiresAt: premiumUntilToModel(until),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("set user premium until: %w", err)
	}
	return userFromModel(row), nil
}

// SetVerified sets/clears the user's verified flag.
func (s *UserStore) SetVerified(ctx context.Context, userID int64, verified bool) (domain.User, error) {
	row, err := s.q.SetUserVerified(ctx, sqlcgen.SetUserVerifiedParams{
		ID:       userID,
		Verified: verified,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("set user verified: %w", err)
	}
	return userFromModel(row), nil
}

// SetSupport sets/clears the user's support flag (official support account).
func (s *UserStore) SetSupport(ctx context.Context, userID int64, support bool) (domain.User, error) {
	row, err := s.q.SetUserSupport(ctx, sqlcgen.SetUserSupportParams{
		ID:      userID,
		Support: support,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("set user support: %w", err)
	}
	return userFromModel(row), nil
}

// SetScamFake sets/clears the user's scam and fake flags (bots reuse the same path).
func (s *UserStore) SetScamFake(ctx context.Context, userID int64, scam, fake bool) (domain.User, error) {
	if scam && fake {
		return domain.User{}, domain.ErrPeerModerationFlagsInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.User{}, fmt.Errorf("set user scam/fake: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin set user scam/fake: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	qtx := s.q.WithTx(tx)

	var currentScam, currentFake bool
	if err := tx.QueryRow(ctx, `
SELECT scam, fake
FROM users
WHERE id = $1
FOR UPDATE`, userID).Scan(&currentScam, &currentFake); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("lock user scam/fake: %w", err)
	}
	if currentScam == scam && currentFake == fake {
		row, err := qtx.GetUserByID(ctx, userID)
		if err != nil {
			return domain.User{}, fmt.Errorf("reload unchanged user scam/fake: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.User{}, fmt.Errorf("commit unchanged user scam/fake: %w", err)
		}
		committed = true
		return userFromModel(row), nil
	}

	row, err := qtx.SetUserScamFake(ctx, sqlcgen.SetUserScamFakeParams{
		ID:   userID,
		Scam: scam,
		Fake: fake,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("set user scam/fake: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit user scam/fake: %w", err)
	}
	committed = true
	return userFromModel(row), nil
}

const maxModerationFlagAudience = 4096

// ModerationFlagAudience returns the bounded set of accounts that can already
// observe the target through a direct contact or private dialog. It is used
// only for best-effort, non-PTS updateUser fanout after the authoritative flag
// mutation commits.
func (s *UserStore) ModerationFlagAudience(ctx context.Context, userID int64, limit int) ([]int64, error) {
	if limit > maxModerationFlagAudience {
		limit = maxModerationFlagAudience
	}
	return moderationFlagAudience(ctx, s.db, userID, limit)
}

func moderationFlagAudience(ctx context.Context, db sqlcgen.DBTX, userID int64, limit int) ([]int64, error) {
	if userID <= 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, `
SELECT picked.user_id
FROM (
  SELECT candidates.user_id
  FROM (
    SELECT $1::bigint AS user_id, 0 AS priority, 2147483647::bigint AS activity
    UNION ALL
    SELECT contact_user_id, 1, 0 FROM contacts WHERE user_id = $1
    UNION ALL
    SELECT user_id, 1, 0 FROM contacts WHERE contact_user_id = $1
    UNION ALL
    SELECT peer_id, 2, top_message_date FROM dialogs WHERE user_id = $1 AND peer_type = 'user'
    UNION ALL
    SELECT user_id, 2, top_message_date FROM dialogs WHERE peer_type = 'user' AND peer_id = $1
  ) candidates
  JOIN users u ON u.id = candidates.user_id AND u.deleted_at IS NULL
  GROUP BY candidates.user_id
  ORDER BY min(candidates.priority), max(candidates.activity) DESC, candidates.user_id
  LIMIT $2
) picked
ORDER BY picked.user_id`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list moderation flag audience: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan moderation flag audience: %w", err)
		}
		if id != 0 {
			out = append(out, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate moderation flag audience: %w", err)
	}
	return out, nil
}

// SweepExpiredPremium clears expired Premium rows and returns the swept users.
func (s *UserStore) SweepExpiredPremium(ctx context.Context, now int64, limit int) ([]domain.User, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.q.SweepExpiredPremium(ctx, sqlcgen.SweepExpiredPremiumParams{
		Now:        pgtype.Timestamptz{Time: time.Unix(now, 0).UTC(), Valid: true},
		LimitCount: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("sweep expired premium: %w", err)
	}
	out := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		out = append(out, userFromModel(row))
	}
	return out, nil
}

// UpdateEmojiStatus atomically replaces the complete emoji-status snapshot.
func (s *UserStore) UpdateEmojiStatus(ctx context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error) {
	collectibleJSON, collectibleID, err := encodeEmojiStatusCollectible(status)
	if err != nil {
		return domain.User{}, err
	}
	params := sqlcgen.UpdateUserEmojiStatusParams{
		ID:                       userID,
		EmojiStatusDocumentID:    status.DocumentID,
		EmojiStatusUntil:         int64(status.Until),
		EmojiStatusCollectibleID: collectibleID,
		EmojiStatusCollectible:   collectibleJSON,
	}
	var row sqlcgen.User
	if status.Collectible.Empty() {
		row, err = updateEmojiStatusRow(ctx, s.db, s.q, userID, status, params)
	} else {
		// Serialize selection against transfer/export/burn. RPC-level ownership
		// checks are advisory; this lock is the write-boundary invariant that
		// prevents a concurrent lifecycle commit from leaving a non-owned gift
		// installed after its invalidation trigger already ran.
		err = withTx(ctx, s.db, "update collectible emoji status", func(tx pgx.Tx) error {
			row, err = updateEmojiStatusRow(ctx, tx, sqlcgen.New(tx), userID, status, params)
			return err
		})
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		if errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
			return domain.User{}, err
		}
		return domain.User{}, fmt.Errorf("update user emoji status: %w", err)
	}
	return userFromModel(row), nil
}

// UpdateEmojiStatusWithEvent commits the user snapshot, allocated pts event
// and dispatch outbox row as one aggregate transaction. This is the production
// boundary used by account.updateEmojiStatus; no success can expose a users
// row whose change is absent from updates.getDifference.
func (s *UserStore) UpdateEmojiStatusWithEvent(ctx context.Context, userID int64, status domain.UserEmojiStatus, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, domain.UpdateEvent, error) {
	collectibleJSON, collectibleID, err := encodeEmojiStatusCollectible(status)
	if err != nil {
		return domain.User{}, domain.UpdateEvent{}, err
	}
	if event.Type != domain.UpdateEventUserEmojiStatus || event.EmojiStatus != status ||
		event.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
		return domain.User{}, domain.UpdateEvent{}, domain.ErrStarGiftCollectibleInvalid
	}
	params := sqlcgen.UpdateUserEmojiStatusParams{
		ID:                       userID,
		EmojiStatusDocumentID:    status.DocumentID,
		EmojiStatusUntil:         int64(status.Until),
		EmojiStatusCollectibleID: collectibleID,
		EmojiStatusCollectible:   collectibleJSON,
	}
	var row sqlcgen.User
	err = withTx(ctx, s.db, "update emoji status with event", func(tx pgx.Tx) error {
		row, err = updateEmojiStatusRow(ctx, tx, sqlcgen.New(tx), userID, status, params)
		if err != nil {
			return err
		}
		event, err = NewUpdateEventStore(tx).AppendAllocatedWithDispatch(
			ctx, userID, event, excludeAuthKeyID, excludeSessionID,
		)
		return err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.UpdateEvent{}, domain.ErrUserNotFound
		}
		if errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
			return domain.User{}, domain.UpdateEvent{}, err
		}
		return domain.User{}, domain.UpdateEvent{}, fmt.Errorf("update user emoji status with event: %w", err)
	}
	return userFromModel(row), event, nil
}

func updateEmojiStatusRow(ctx context.Context, db sqlcgen.DBTX, q *sqlcgen.Queries, userID int64, status domain.UserEmojiStatus, params sqlcgen.UpdateUserEmojiStatusParams) (sqlcgen.User, error) {
	if !status.Collectible.Empty() {
		var lockedID int64
		if err := db.QueryRow(ctx, `
SELECT id FROM unique_star_gifts WHERE id=$1 FOR UPDATE`, status.Collectible.CollectibleID).Scan(&lockedID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return sqlcgen.User{}, domain.ErrStarGiftCollectibleInvalid
			}
			return sqlcgen.User{}, err
		}
		gift, found, err := NewStarGiftStore(db).UniqueByID(ctx, lockedID)
		if err != nil {
			return sqlcgen.User{}, err
		}
		expected, valid := domain.CollectibleEmojiStatus(gift)
		if !found || !valid || gift.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) ||
			gift.Burned || gift.OwnerAddress != "" || expected != status.Collectible {
			return sqlcgen.User{}, domain.ErrStarGiftCollectibleInvalid
		}
	}
	return q.UpdateUserEmojiStatus(ctx, params)
}

// UpdateBirthday updates the user's birthday (a zero-value Birthday clears it).
func (s *UserStore) UpdateBirthday(ctx context.Context, userID int64, birthday domain.Birthday) (domain.User, error) {
	row, err := s.q.UpdateUserBirthday(ctx, sqlcgen.UpdateUserBirthdayParams{
		ID:            userID,
		BirthdayDay:   int32(birthday.Day),
		BirthdayMonth: int32(birthday.Month),
		BirthdayYear:  int32(birthday.Year),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("update user birthday: %w", err)
	}
	return userFromModel(row), nil
}

// UpdatePersonalChannel sets/clears the profile-page personal channel (channelID=0 clears it).
func (s *UserStore) UpdatePersonalChannel(ctx context.Context, userID int64, channelID int64) (domain.User, error) {
	row, err := s.q.UpdateUserPersonalChannel(ctx, sqlcgen.UpdateUserPersonalChannelParams{
		ID:                userID,
		PersonalChannelID: channelID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("update user personal channel: %w", err)
	}
	return userFromModel(row), nil
}

func (s *UserStore) UpdateColor(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error) {
	if forProfile {
		row, err := s.q.UpdateUserProfileColor(ctx, sqlcgen.UpdateUserProfileColorParams{
			ID:                userID,
			ColorSet:          color.HasColor,
			Color:             int32(color.Color),
			BackgroundEmojiID: color.BackgroundEmojiID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.User{}, domain.ErrUserNotFound
			}
			return domain.User{}, fmt.Errorf("update user profile color: %w", err)
		}
		return userFromModel(row), nil
	}
	row, err := s.q.UpdateUserColor(ctx, sqlcgen.UpdateUserColorParams{
		ID:                userID,
		ColorSet:          color.HasColor,
		Color:             int32(color.Color),
		BackgroundEmojiID: color.BackgroundEmojiID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("update user color: %w", err)
	}
	return userFromModel(row), nil
}

// premiumUntilFromModel converts a nullable timestamptz to Unix seconds (NULL -> 0).
func premiumUntilFromModel(t pgtype.Timestamptz) int {
	if !t.Valid {
		return 0
	}
	return int(t.Time.Unix())
}

// premiumUntilToModel converts Unix seconds to a nullable timestamptz (<=0 -> NULL).
func premiumUntilToModel(until int) pgtype.Timestamptz {
	if until <= 0 {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: time.Unix(int64(until), 0).UTC(), Valid: true}
}

func escapeLike(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '%' || r == '_' || r == '\\' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func userFromModel(r sqlcgen.User) domain.User {
	collectible := mustDecodeEmojiStatusCollectible(r.EmojiStatusCollectibleID, r.EmojiStatusCollectible)
	u := domain.User{
		ID:                     r.ID,
		AccessHash:             r.AccessHash,
		Phone:                  r.Phone,
		FirstName:              r.FirstName,
		LastName:               r.LastName,
		About:                  r.About,
		Username:               r.Username,
		CountryCode:            r.CountryCode,
		Verified:               r.Verified,
		Scam:                   r.Scam,
		Fake:                   r.Fake,
		Support:                r.Support,
		Bot:                    r.IsBot,
		BotInfoVersion:         int(r.BotInfoVersion),
		PremiumUntil:           premiumUntilFromModel(r.PremiumExpiresAt),
		EmojiStatusDocumentID:  r.EmojiStatusDocumentID,
		EmojiStatusUntil:       int(r.EmojiStatusUntil),
		EmojiStatusCollectible: collectible,
		Birthday:               domain.Birthday{Day: int(r.BirthdayDay), Month: int(r.BirthdayMonth), Year: int(r.BirthdayYear)},
		PersonalChannelID:      r.PersonalChannelID,
		LinkedCommunityID:      r.LinkedCommunityID,
		Color:                  peerColorFromModel(r.ColorSet, r.Color, r.ColorBackgroundEmojiID),
		ProfileColor:           peerColorFromModel(r.ProfileColorSet, r.ProfileColor, r.ProfileColorBackgroundEmojiID),
		LastSeenAt:             int(r.LastSeenAt),
		Deleted:                r.DeletedAt.Valid,
		DeletionSource:         domain.AccountDeletionSource(r.DeletionSource),
		DeletionReason:         r.DeletionReason,
		CreatedAt:              r.CreatedAt.Time,
		AccountDeleteAt:        r.AccountDeleteAt.Time,
	}
	if r.DeletedAt.Valid {
		u.DeletedAt = r.DeletedAt.Time.Unix()
		return u.DeletedTombstone()
	}
	return u
}

func encodeEmojiStatusCollectible(status domain.UserEmojiStatus) ([]byte, *int64, error) {
	if !status.Valid() {
		return nil, nil, domain.ErrStarGiftCollectibleInvalid
	}
	if status.Collectible.Empty() {
		return []byte(`{}`), nil, nil
	}
	raw, err := json.Marshal(status.Collectible)
	if err != nil {
		return nil, nil, fmt.Errorf("encode collectible emoji status: %w", err)
	}
	id := status.Collectible.CollectibleID
	return raw, &id, nil
}

func mustDecodeEmojiStatusCollectible(id *int64, raw []byte) domain.EmojiStatusCollectible {
	var collectible domain.EmojiStatusCollectible
	if err := json.Unmarshal(raw, &collectible); err != nil {
		panic(fmt.Sprintf("invalid users.emoji_status_collectible JSON: %v", err))
	}
	if id == nil {
		if !collectible.Empty() {
			panic("users emoji-status invariant: snapshot exists without collectible id")
		}
		return domain.EmojiStatusCollectible{}
	}
	if !collectible.Valid() || collectible.CollectibleID != *id {
		panic("users emoji-status invariant: incomplete or mismatched collectible snapshot")
	}
	return collectible
}

func peerColorFromModel(hasColor bool, color int32, backgroundEmojiID int64) domain.PeerColor {
	return domain.PeerColor{
		HasColor:          hasColor,
		Color:             int(color),
		BackgroundEmojiID: backgroundEmojiID,
	}
}
