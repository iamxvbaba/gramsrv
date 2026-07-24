package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// UserStore 用 PostgreSQL 实现 store.UserStore。
type UserStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewUserStore 基于 pgx 连接池（或事务）创建 UserStore。
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
	// bot 行 phone 为空串（0090 起 phone 唯一性只覆盖非空值），空查询必须判未找到。
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
			return domain.User{}, false, nil
		}
		return domain.User{}, false, fmt.Errorf("get user by username: %w", err)
	}
	return userFromModel(row), true, nil
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
	if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, userID, usernameLower); err != nil {
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
		if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, row.ID, usernameLower); err != nil {
			return domain.User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit create user: %w", err)
	}
	committed = true
	return userFromModel(row), nil
}

// SetPremiumUntil 把会员到期时间设为绝对 Unix 秒（0 = 清除会员）。
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

// SetVerified 设置/取消用户认证标记。
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

// SetSupport 设置/取消用户的 support 标记（官方客服账号）。
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

// SetScamFake 设置/取消用户的 scam 与 fake 标记（bot 复用同一路径）。
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
	audience, err := moderationFlagAudience(ctx, tx, userID, maxModerationFlagAudience)
	if err != nil {
		return domain.User{}, err
	}
	date := int(time.Now().Unix())
	for _, viewerUserID := range audience {
		pts, err := reserveUserPts(ctx, tx, viewerUserID, 1)
		if err != nil {
			return domain.User{}, fmt.Errorf("reserve moderation flag pts for viewer %d: %w", viewerUserID, err)
		}
		event := domain.UpdateEvent{
			UserID: viewerUserID,
			Type:   domain.UpdateEventUserProfile,
			Pts:    pts, PtsCount: 1, Date: date,
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: userID},
		}
		if err := appendUserUpdateEvent(ctx, tx, qtx, viewerUserID, event); err != nil {
			return domain.User{}, fmt.Errorf("append moderation flag event for viewer %d: %w", viewerUserID, err)
		}
		if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
			TargetUserID: viewerUserID,
			Pts:          int32(pts),
			EventType:    string(event.Type),
		}); err != nil {
			return domain.User{}, fmt.Errorf("enqueue moderation flag dispatch for viewer %d: %w", viewerUserID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit user scam/fake: %w", err)
	}
	committed = true
	return userFromModel(row), nil
}

const maxModerationFlagAudience = 4096

// moderationFlagAudience returns the bounded set of accounts that can already
// observe the target through a direct contact or private dialog. The final
// user_id ordering is deliberate: concurrent moderation changes acquire user
// pts watermarks in the same global order and cannot deadlock by target order.
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

// SweepExpiredPremium 清空到期会员行并返回清理后的用户。
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

// UpdateBirthday 更新用户生日（零值 Birthday 表示清除）。
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

// UpdatePersonalChannel 设置/清除资料页个人频道（channelID=0 表示清除）。
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

// premiumUntilFromModel 把可空 timestamptz 转为 Unix 秒（NULL → 0）。
func premiumUntilFromModel(t pgtype.Timestamptz) int {
	if !t.Valid {
		return 0
	}
	return int(t.Time.Unix())
}

// premiumUntilToModel 把 Unix 秒转为可空 timestamptz（<=0 → NULL）。
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
