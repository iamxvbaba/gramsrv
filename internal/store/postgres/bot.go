package postgres

import (
	"context"
	"encoding/json"
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

// BotStore implements store.BotStore with PostgreSQL.
type BotStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewBotStore creates a BotStore backed by a pgx pool (or transaction).
func NewBotStore(db sqlcgen.DBTX) *BotStore {
	return &BotStore{db: db, q: sqlcgen.New(db)}
}

// CreateBotAccount creates the users row (is_bot=true, empty phone) and the bots row in a single transaction.
func (s *BotStore) CreateBotAccount(ctx context.Context, user domain.User, profile domain.BotProfile) (domain.User, domain.BotProfile, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.User{}, domain.BotProfile{}, fmt.Errorf("create bot account: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.User{}, domain.BotProfile{}, fmt.Errorf("create bot account: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)

	// Take an advisory lock on the owner inside the transaction, then re-check the count, closing the
	// service layer's count-then-insert TOCTOU (multiple devices/concurrent /newbot each reading count<limit and then
	// over-inserting). The key is owner_user_id, which may share key space with the private-chat send
	// lock, but bot creation is infrequent so at worst it causes occasional serialization with no correctness impact.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", profile.OwnerUserID); err != nil {
		return domain.User{}, domain.BotProfile{}, fmt.Errorf("create bot account: lock owner: %w", err)
	}
	var owned int64
	if err := tx.QueryRow(ctx,
		"SELECT count(*) FROM bots WHERE owner_user_id = $1 AND bot_user_id <> owner_user_id",
		profile.OwnerUserID).Scan(&owned); err != nil {
		return domain.User{}, domain.BotProfile{}, fmt.Errorf("create bot account: count: %w", err)
	}
	if owned >= int64(domain.MaxBotsPerOwner) {
		return domain.User{}, domain.BotProfile{}, domain.ErrBotsTooMany
	}

	row, err := q.InsertBotUser(ctx, sqlcgen.InsertBotUserParams{
		AccessHash: user.AccessHash,
		FirstName:  user.FirstName,
		Username:   strings.TrimSpace(strings.TrimPrefix(user.Username, "@")),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == "users_username_lower_unique_idx" {
			return domain.User{}, domain.BotProfile{}, domain.ErrUsernameOccupied
		}
		return domain.User{}, domain.BotProfile{}, fmt.Errorf("create bot account: insert user: %w", err)
	}
	if usernameLower := strings.ToLower(row.Username); usernameLower != "" {
		if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, row.ID, row.Username, usernameLower); err != nil {
			return domain.User{}, domain.BotProfile{}, err
		}
	}
	if err := q.InsertBot(ctx, sqlcgen.InsertBotParams{
		BotUserID:   row.ID,
		OwnerUserID: profile.OwnerUserID,
		TokenSecret: profile.TokenSecret,
	}); err != nil {
		return domain.User{}, domain.BotProfile{}, fmt.Errorf("create bot account: insert bot: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, domain.BotProfile{}, fmt.Errorf("create bot account: commit: %w", err)
	}
	profile.BotUserID = row.ID
	return userFromModel(row), profile, nil
}

// DeleteBotAccount permanently removes a user-created bot in one transaction:
// it revokes the bot's sessions, purges its private state, releases its
// username, drops the bots row (which invalidates the token) and tombstones the
// users row. System service bots and non-bot users are rejected. The reused
// helpers are the same vetted primitives that back account deletion, so the
// tombstone satisfies users_deletion_state_check. Returns the tombstoned user
// for change notifications.
func (s *BotStore) DeleteBotAccount(ctx context.Context, botUserID int64) (domain.User, error) {
	if botUserID == 0 || domain.IsSystemUserID(botUserID) {
		return domain.User{}, domain.ErrBotNotFound
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.User{}, fmt.Errorf("delete bot account: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("delete bot account: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockUsersForUpdate(ctx, tx, botUserID); err != nil {
		return domain.User{}, fmt.Errorf("delete bot account: lock: %w", err)
	}
	u, found, err := NewUserStore(tx).ByID(ctx, botUserID)
	if err != nil {
		return domain.User{}, err
	}
	if !found || !u.Bot || u.Deleted {
		return domain.User{}, domain.ErrBotNotFound
	}
	// Only bots backed by a bots row (created via /newbot or the admin) are
	// deletable here; system service bots are already excluded above.
	var hasBotRow bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bots WHERE bot_user_id = $1)`, botUserID).Scan(&hasBotRow); err != nil {
		return domain.User{}, fmt.Errorf("delete bot account: probe bots row: %w", err)
	}
	if !hasBotRow {
		return domain.User{}, domain.ErrBotNotFound
	}

	now := time.Now().UTC()
	if _, err := revokeByUserExceptTx(ctx, tx, botUserID, 0); err != nil {
		return domain.User{}, fmt.Errorf("delete bot account: revoke sessions: %w", err)
	}
	if err := purgeDeletedBotPrivateState(ctx, tx, botUserID, now); err != nil {
		return domain.User{}, err
	}
	if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeUser, botUserID, "", ""); err != nil {
		return domain.User{}, fmt.Errorf("delete bot account: release username: %w", err)
	}
	// Drop the bots row so the token can no longer authenticate a login.
	if _, err := tx.Exec(ctx, `DELETE FROM bots WHERE bot_user_id = $1`, botUserID); err != nil {
		return domain.User{}, fmt.Errorf("delete bot account: delete bots row: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE users SET
  phone = '', first_name = '', last_name = '', username = '', country_code = '', about = '',
  verified = false, support = false, last_seen_at = 0,
  premium_expires_at = NULL, emoji_status_document_id = 0, emoji_status_until = 0,
  emoji_status_collectible_id = NULL, emoji_status_collectible = '{}'::jsonb,
  color_set = false, color = 0, color_background_emoji_id = 0,
  profile_color_set = false, profile_color = 0, profile_color_background_emoji_id = 0,
  birthday_day = 0, birthday_month = 0, birthday_year = 0, personal_channel_id = 0,
  deleted_at = $2, deletion_source = 'manual', deletion_reason = 'admin bot deletion',
  account_delete_at = NULL, updated_at = $2
WHERE id = $1 AND deleted_at IS NULL`, botUserID, now); err != nil {
		return domain.User{}, fmt.Errorf("delete bot account: tombstone: %w", err)
	}
	u, found, err = NewUserStore(tx).ByID(ctx, botUserID)
	if err != nil || !found {
		if err == nil {
			err = domain.ErrUserNotFound
		}
		return domain.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("delete bot account: commit: %w", err)
	}
	return u, nil
}

func (s *BotStore) GetBot(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	if botUserID == 0 {
		return domain.BotProfile{}, false, nil
	}
	row, err := s.q.GetBot(ctx, botUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BotProfile{}, false, nil
		}
		return domain.BotProfile{}, false, fmt.Errorf("get bot: %w", err)
	}
	profile, err := s.enrichBotProfile(ctx, botProfileFromModel(row))
	if err != nil {
		return domain.BotProfile{}, false, err
	}
	return profile, true, nil
}

func (s *BotStore) GetBots(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	ids := uniqueNonZeroInt64s(botUserIDs...)
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT bot_user_id, owner_user_id, token_secret, description, commands, bot_chat_history,
       bot_nochats, inline_placeholder, created_at, updated_at, menu_button_type,
       menu_button_text, menu_button_url, bot_inline_geo
FROM bots
WHERE bot_user_id = ANY($1::bigint[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("get bots: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]domain.BotProfile, len(ids))
	for rows.Next() {
		var row sqlcgen.Bot
		if err := rows.Scan(
			&row.BotUserID,
			&row.OwnerUserID,
			&row.TokenSecret,
			&row.Description,
			&row.Commands,
			&row.BotChatHistory,
			&row.BotNochats,
			&row.InlinePlaceholder,
			&row.CreatedAt,
			&row.UpdatedAt,
			&row.MenuButtonType,
			&row.MenuButtonText,
			&row.MenuButtonUrl,
			&row.BotInlineGeo,
		); err != nil {
			return nil, err
		}
		out[row.BotUserID] = botProfileFromModel(row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get bots: %w", err)
	}
	for id, profile := range out {
		enriched, err := s.enrichBotProfile(ctx, profile)
		if err != nil {
			return nil, err
		}
		out[id] = enriched
	}
	return out, nil
}

func (s *BotStore) ListBotsByOwner(ctx context.Context, ownerUserID int64) ([]domain.BotProfile, error) {
	if ownerUserID == 0 {
		return nil, nil
	}
	rows, err := s.q.ListBotsByOwner(ctx, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list bots by owner: %w", err)
	}
	out := make([]domain.BotProfile, 0, len(rows))
	for _, row := range rows {
		profile, err := s.enrichBotProfile(ctx, botProfileFromModel(row))
		if err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	return out, nil
}

func (s *BotStore) CountBotsByOwner(ctx context.Context, ownerUserID int64) (int, error) {
	if ownerUserID == 0 {
		return 0, nil
	}
	n, err := s.q.CountBotsByOwner(ctx, ownerUserID)
	if err != nil {
		return 0, fmt.Errorf("count bots by owner: %w", err)
	}
	return int(n), nil
}

func (s *BotStore) UpdateBotTokenSecret(ctx context.Context, botUserID int64, secret string) error {
	n, err := s.q.UpdateBotTokenSecret(ctx, sqlcgen.UpdateBotTokenSecretParams{
		BotUserID:   botUserID,
		TokenSecret: secret,
	})
	if err != nil {
		return fmt.Errorf("update bot token secret: %w", err)
	}
	if n == 0 {
		return domain.ErrBotNotFound
	}
	return nil
}

// withBumpTx runs the update closure and bumps the bot's bot_info_version in a single transaction, returning the new version.
func (s *BotStore) withBumpTx(ctx context.Context, botUserID int64, fn func(q *sqlcgen.Queries) error) (int, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return 0, fmt.Errorf("bot update: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("bot update: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	if err := fn(q); err != nil {
		return 0, err
	}
	ver, err := q.BumpBotInfoVersion(ctx, botUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, domain.ErrBotNotFound
		}
		return 0, fmt.Errorf("bot update: bump version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("bot update: commit: %w", err)
	}
	return int(ver), nil
}

func (s *BotStore) UpdateBotCommands(ctx context.Context, botUserID int64, commands []domain.BotCommand) (int, error) {
	payload, err := json.Marshal(commands)
	if err != nil {
		return 0, fmt.Errorf("update bot commands: encode: %w", err)
	}
	if len(commands) == 0 {
		payload = []byte("[]")
	}
	return s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		if _, err := q.UpdateBotCommandsRow(ctx, sqlcgen.UpdateBotCommandsRowParams{
			BotUserID: botUserID,
			Commands:  payload,
		}); err != nil {
			return fmt.Errorf("update bot commands: %w", err)
		}
		return nil
	})
}

func (s *BotStore) UpdateBotInfo(ctx context.Context, botUserID int64, upd domain.BotInfoUpdate) (int, error) {
	return s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		if upd.SetName || upd.SetAbout {
			params := sqlcgen.UpdateBotProfileFieldsParams{ID: botUserID}
			if upd.SetName {
				name := upd.Name
				params.FirstName = &name
			}
			if upd.SetAbout {
				about := upd.About
				params.About = &about
			}
			if _, err := q.UpdateBotProfileFields(ctx, params); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return domain.ErrBotNotFound
				}
				return fmt.Errorf("update bot profile: %w", err)
			}
		}
		if upd.SetDescription {
			if _, err := q.UpdateBotDescriptionRow(ctx, sqlcgen.UpdateBotDescriptionRowParams{
				BotUserID:   botUserID,
				Description: upd.Description,
			}); err != nil {
				return fmt.Errorf("update bot description: %w", err)
			}
		}
		return nil
	})
}

func (s *BotStore) UpdateBotMenuButton(ctx context.Context, botUserID int64, button domain.BotMenuButton) (int, error) {
	return s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		if _, err := q.UpdateBotMenuButtonRow(ctx, sqlcgen.UpdateBotMenuButtonRowParams{
			BotUserID:      botUserID,
			MenuButtonType: int16(button.Type),
			MenuButtonText: button.Text,
			MenuButtonUrl:  button.URL,
		}); err != nil {
			return fmt.Errorf("update bot menu button: %w", err)
		}
		return nil
	})
}

func (s *BotStore) SetBotInlinePlaceholder(ctx context.Context, botUserID int64, placeholder string) (int, error) {
	return s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		if _, err := q.SetBotInlinePlaceholderRow(ctx, sqlcgen.SetBotInlinePlaceholderRowParams{
			BotUserID:         botUserID,
			InlinePlaceholder: placeholder,
		}); err != nil {
			return fmt.Errorf("set bot inline placeholder: %w", err)
		}
		return nil
	})
}

func (s *BotStore) SetBotInlineGeo(ctx context.Context, botUserID int64, inlineGeo bool) (int, error) {
	return s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		if _, err := q.SetBotInlineGeoRow(ctx, sqlcgen.SetBotInlineGeoRowParams{
			BotUserID:    botUserID,
			BotInlineGeo: inlineGeo,
		}); err != nil {
			return fmt.Errorf("set bot inline geo: %w", err)
		}
		return nil
	})
}

func (s *BotStore) SetBotNochats(ctx context.Context, botUserID int64, nochats bool) (int, error) {
	return s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		if _, err := q.SetBotNochatsRow(ctx, sqlcgen.SetBotNochatsRowParams{
			BotUserID:  botUserID,
			BotNochats: nochats,
		}); err != nil {
			return fmt.Errorf("set bot nochats: %w", err)
		}
		return nil
	})
}

func (s *BotStore) SetBotChatHistory(ctx context.Context, botUserID int64, chatHistory bool) (int, error) {
	return s.withBumpTx(ctx, botUserID, func(q *sqlcgen.Queries) error {
		if _, err := q.SetBotChatHistoryRow(ctx, sqlcgen.SetBotChatHistoryRowParams{
			BotUserID:      botUserID,
			BotChatHistory: chatHistory,
		}); err != nil {
			return fmt.Errorf("set bot chat history: %w", err)
		}
		return nil
	})
}

func (s *BotStore) CanBotSendMessage(ctx context.Context, botUserID, userID int64) (bool, error) {
	if botUserID == 0 || userID == 0 || botUserID == userID {
		return false, nil
	}
	allowed, err := s.q.CanBotSendMessage(ctx, sqlcgen.CanBotSendMessageParams{
		BotUserID: botUserID,
		UserID:    userID,
	})
	if err != nil {
		return false, fmt.Errorf("can bot send message: %w", err)
	}
	return allowed, nil
}

func (s *BotStore) AllowBotSendMessage(ctx context.Context, botUserID, userID int64, fromRequest bool) (bool, error) {
	if botUserID == 0 || userID == 0 || botUserID == userID {
		return false, domain.ErrBotNotFound
	}
	created, err := s.q.AllowBotSendMessage(ctx, sqlcgen.AllowBotSendMessageParams{
		BotUserID:   botUserID,
		UserID:      userID,
		FromRequest: fromRequest,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
			return false, domain.ErrBotNotFound
		}
		return false, fmt.Errorf("allow bot send message: %w", err)
	}
	return created, nil
}

func (s *BotStore) GetBotChatState(ctx context.Context, botUserID, userID int64) (domain.BotChatState, bool, error) {
	row, err := s.q.GetBotChatState(ctx, sqlcgen.GetBotChatStateParams{
		BotUserID: botUserID,
		UserID:    userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BotChatState{}, false, nil
		}
		return domain.BotChatState{}, false, fmt.Errorf("get bot chat state: %w", err)
	}
	state := domain.BotChatState{BotUserID: botUserID, UserID: userID}
	if err := json.Unmarshal(row.State, &state); err != nil {
		return domain.BotChatState{}, false, fmt.Errorf("get bot chat state: decode: %w", err)
	}
	state.BotUserID, state.UserID = botUserID, userID
	return state, true, nil
}

func (s *BotStore) UpsertBotChatState(ctx context.Context, state domain.BotChatState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("upsert bot chat state: encode: %w", err)
	}
	if err := s.q.UpsertBotChatState(ctx, sqlcgen.UpsertBotChatStateParams{
		BotUserID: state.BotUserID,
		UserID:    state.UserID,
		State:     payload,
	}); err != nil {
		return fmt.Errorf("upsert bot chat state: %w", err)
	}
	return nil
}

func (s *BotStore) DeleteBotChatState(ctx context.Context, botUserID, userID int64) error {
	if err := s.q.DeleteBotChatState(ctx, sqlcgen.DeleteBotChatStateParams{
		BotUserID: botUserID,
		UserID:    userID,
	}); err != nil {
		return fmt.Errorf("delete bot chat state: %w", err)
	}
	return nil
}

func botProfileFromModel(r sqlcgen.Bot) domain.BotProfile {
	p := domain.BotProfile{
		BotUserID:         r.BotUserID,
		OwnerUserID:       r.OwnerUserID,
		TokenSecret:       r.TokenSecret,
		Description:       r.Description,
		ChatHistory:       r.BotChatHistory,
		Nochats:           r.BotNochats,
		InlineGeo:         r.BotInlineGeo,
		InlinePlaceholder: r.InlinePlaceholder,
		MenuButton: domain.BotMenuButton{
			Type: domain.BotMenuButtonType(r.MenuButtonType),
			Text: r.MenuButtonText,
			URL:  r.MenuButtonUrl,
		},
	}
	if len(r.Commands) > 0 {
		// A corrupt commands column does not block the read path: botInfo degrades to an empty command list.
		_ = json.Unmarshal(r.Commands, &p.Commands)
	}
	return p
}
