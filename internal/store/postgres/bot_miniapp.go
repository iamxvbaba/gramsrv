package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *BotStore) withBotInfoBumpRawTx(ctx context.Context, botUserID int64, fn func(pgx.Tx) error) (int, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return 0, fmt.Errorf("bot mini app update: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("bot mini app update: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return 0, err
	}
	ver, err := s.q.WithTx(tx).BumpBotInfoVersion(ctx, botUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, domain.ErrBotNotFound
		}
		return 0, fmt.Errorf("bot mini app update: bump version: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("bot mini app update: commit: %w", err)
	}
	return int(ver), nil
}

func (s *BotStore) enrichBotProfile(ctx context.Context, profile domain.BotProfile) (domain.BotProfile, error) {
	if profile.BotUserID == 0 {
		return profile, nil
	}
	var hasMain, hasAttach, hasPreview bool
	if err := s.db.QueryRow(ctx, `
SELECT
  EXISTS(SELECT 1 FROM bot_apps WHERE bot_user_id=$1 AND is_main),
  EXISTS(SELECT 1 FROM attach_menu_bots WHERE bot_user_id=$1),
  EXISTS(SELECT 1 FROM bot_app_preview_media WHERE bot_user_id=$1)`, profile.BotUserID).
		Scan(&hasMain, &hasAttach, &hasPreview); err != nil {
		return profile, fmt.Errorf("enrich bot profile: flags: %w", err)
	}
	profile.HasMainApp = hasMain
	profile.HasAttachMenu = hasAttach
	profile.HasPreviewMedias = hasPreview
	settings, found, err := s.GetBotAppSettings(ctx, profile.BotUserID)
	if err != nil {
		return profile, err
	}
	if found {
		profile.AppSettings = &settings
	}
	return profile, nil
}

func (s *BotStore) UpsertBotApp(ctx context.Context, app domain.BotApp) (domain.BotApp, int, error) {
	if app.BotUserID == 0 || app.ID == 0 || app.AccessHash == 0 || strings.TrimSpace(app.ShortName) == "" || strings.TrimSpace(app.URL) == "" {
		return domain.BotApp{}, 0, domain.ErrBotAppInvalid
	}
	app.ShortName = strings.ToLower(strings.TrimSpace(app.ShortName))
	version, err := s.withBotInfoBumpRawTx(ctx, app.BotUserID, func(tx pgx.Tx) error {
		if app.Main {
			if _, err := tx.Exec(ctx, `UPDATE bot_apps SET is_main=false, updated_at=now() WHERE bot_user_id=$1 AND id<>$2 AND is_main`, app.BotUserID, app.ID); err != nil {
				return fmt.Errorf("upsert bot app: clear main: %w", err)
			}
		}
		_, err := tx.Exec(ctx, `
INSERT INTO bot_apps (
  id, bot_user_id, short_name, title, description, url, photo_id, document_id,
  access_hash, hash, inactive, request_write_access, has_settings, is_main
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (id) DO UPDATE SET
  short_name=EXCLUDED.short_name,
  title=EXCLUDED.title,
  description=EXCLUDED.description,
  url=EXCLUDED.url,
  photo_id=EXCLUDED.photo_id,
  document_id=EXCLUDED.document_id,
  access_hash=EXCLUDED.access_hash,
  hash=EXCLUDED.hash,
  inactive=EXCLUDED.inactive,
  request_write_access=EXCLUDED.request_write_access,
  has_settings=EXCLUDED.has_settings,
  is_main=EXCLUDED.is_main,
  updated_at=now()`,
			app.ID, app.BotUserID, app.ShortName, app.Title, app.Description, app.URL, app.PhotoID, app.DocumentID,
			app.AccessHash, app.Hash, app.Inactive, app.RequestWriteAccess, app.HasSettings, app.Main)
		if err != nil {
			if isUniqueViolation(err) {
				return domain.ErrBotAppInvalid
			}
			return fmt.Errorf("upsert bot app: %w", err)
		}
		return nil
	})
	if err != nil {
		return domain.BotApp{}, 0, err
	}
	return app, version, nil
}

func (s *BotStore) GetBotAppByID(ctx context.Context, appID, accessHash int64) (domain.BotApp, bool, error) {
	if appID == 0 || accessHash == 0 {
		return domain.BotApp{}, false, nil
	}
	row := s.db.QueryRow(ctx, botAppSelectSQL()+` WHERE id=$1 AND access_hash=$2`, appID, accessHash)
	return scanBotApp(row)
}

func (s *BotStore) GetBotAppByShortName(ctx context.Context, botUserID int64, shortName string) (domain.BotApp, bool, error) {
	shortName = strings.ToLower(strings.TrimSpace(shortName))
	if botUserID == 0 || shortName == "" {
		return domain.BotApp{}, false, nil
	}
	row := s.db.QueryRow(ctx, botAppSelectSQL()+` WHERE bot_user_id=$1 AND lower(short_name)=lower($2)`, botUserID, shortName)
	return scanBotApp(row)
}

func (s *BotStore) GetMainBotApp(ctx context.Context, botUserID int64) (domain.BotApp, bool, error) {
	if botUserID == 0 {
		return domain.BotApp{}, false, nil
	}
	row := s.db.QueryRow(ctx, botAppSelectSQL()+` WHERE bot_user_id=$1 AND is_main LIMIT 1`, botUserID)
	return scanBotApp(row)
}

func (s *BotStore) ListBotApps(ctx context.Context, botUserID int64) ([]domain.BotApp, error) {
	rows, err := s.db.Query(ctx, botAppSelectSQL()+` WHERE bot_user_id=$1 ORDER BY is_main DESC, short_name, id`, botUserID)
	if err != nil {
		return nil, fmt.Errorf("list bot apps: %w", err)
	}
	defer rows.Close()
	var out []domain.BotApp
	for rows.Next() {
		app, err := scanBotAppRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bot apps: %w", err)
	}
	return out, nil
}

func (s *BotStore) GetBotAppSettings(ctx context.Context, botUserID int64) (domain.BotAppSettings, bool, error) {
	var settings domain.BotAppSettings
	var bg, bgDark, header, headerDark sql.NullInt64
	err := s.db.QueryRow(ctx, `
SELECT placeholder_path, background_color, background_dark_color, header_color, header_dark_color
FROM bot_app_settings WHERE bot_user_id=$1`, botUserID).
		Scan(&settings.PlaceholderPath, &bg, &bgDark, &header, &headerDark)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BotAppSettings{}, false, nil
		}
		return domain.BotAppSettings{}, false, fmt.Errorf("get bot app settings: %w", err)
	}
	settings.HasBackgroundColor = bg.Valid
	settings.HasBackgroundDark = bgDark.Valid
	settings.HasHeaderColor = header.Valid
	settings.HasHeaderDarkColor = headerDark.Valid
	if bg.Valid {
		settings.BackgroundColor = int(bg.Int64)
	}
	if bgDark.Valid {
		settings.BackgroundDarkColor = int(bgDark.Int64)
	}
	if header.Valid {
		settings.HeaderColor = int(header.Int64)
	}
	if headerDark.Valid {
		settings.HeaderDarkColor = int(headerDark.Int64)
	}
	return settings, true, nil
}

func (s *BotStore) UpsertBotAppSettings(ctx context.Context, botUserID int64, settings domain.BotAppSettings) (int, error) {
	return s.withBotInfoBumpRawTx(ctx, botUserID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
INSERT INTO bot_app_settings (
  bot_user_id, placeholder_path, background_color, background_dark_color, header_color, header_dark_color
) VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (bot_user_id) DO UPDATE SET
  placeholder_path=EXCLUDED.placeholder_path,
  background_color=EXCLUDED.background_color,
  background_dark_color=EXCLUDED.background_dark_color,
  header_color=EXCLUDED.header_color,
  header_dark_color=EXCLUDED.header_dark_color,
  updated_at=now()`,
			botUserID, settings.PlaceholderPath,
			nullableBotAppInt(settings.BackgroundColor, settings.HasBackgroundColor),
			nullableBotAppInt(settings.BackgroundDarkColor, settings.HasBackgroundDark),
			nullableBotAppInt(settings.HeaderColor, settings.HasHeaderColor),
			nullableBotAppInt(settings.HeaderDarkColor, settings.HasHeaderDarkColor))
		if err != nil {
			return fmt.Errorf("upsert bot app settings: %w", err)
		}
		return nil
	})
}

func (s *BotStore) ListBotAppPreviewMedia(ctx context.Context, botUserID, appID int64) ([]domain.BotAppPreviewMedia, error) {
	rows, err := s.db.Query(ctx, `
SELECT id, bot_user_id, app_id, position, photo_id, document_id
FROM bot_app_preview_media
WHERE bot_user_id=$1 AND app_id=$2
ORDER BY position, id`, botUserID, appID)
	if err != nil {
		return nil, fmt.Errorf("list bot app preview media: %w", err)
	}
	defer rows.Close()
	var out []domain.BotAppPreviewMedia
	for rows.Next() {
		var media domain.BotAppPreviewMedia
		if err := rows.Scan(&media.ID, &media.BotUserID, &media.AppID, &media.Position, &media.PhotoID, &media.DocumentID); err != nil {
			return nil, err
		}
		out = append(out, media)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bot app preview media: %w", err)
	}
	return out, nil
}

func (s *BotStore) UpsertBotAppPreviewMedia(ctx context.Context, media domain.BotAppPreviewMedia) (domain.BotAppPreviewMedia, int, error) {
	if media.BotUserID == 0 || media.AppID == 0 || (media.PhotoID == 0 && media.DocumentID == 0) {
		return domain.BotAppPreviewMedia{}, 0, domain.ErrBotAppInvalid
	}
	version, err := s.withBotInfoBumpRawTx(ctx, media.BotUserID, func(tx pgx.Tx) error {
		if media.Position <= 0 {
			if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM bot_app_preview_media WHERE app_id=$1`, media.AppID).Scan(&media.Position); err != nil {
				return fmt.Errorf("upsert bot preview media: next position: %w", err)
			}
		}
		if media.ID == 0 {
			return tx.QueryRow(ctx, `
INSERT INTO bot_app_preview_media (bot_user_id, app_id, position, photo_id, document_id)
VALUES ($1,$2,$3,$4,$5)
RETURNING id`, media.BotUserID, media.AppID, media.Position, media.PhotoID, media.DocumentID).Scan(&media.ID)
		}
		_, err := tx.Exec(ctx, `
INSERT INTO bot_app_preview_media (id, bot_user_id, app_id, position, photo_id, document_id)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (id) DO UPDATE SET
  position=EXCLUDED.position,
  photo_id=EXCLUDED.photo_id,
  document_id=EXCLUDED.document_id,
  updated_at=now()`,
			media.ID, media.BotUserID, media.AppID, media.Position, media.PhotoID, media.DocumentID)
		return err
	})
	if err != nil {
		return domain.BotAppPreviewMedia{}, 0, err
	}
	return media, version, nil
}

func (s *BotStore) DeleteBotAppPreviewMedia(ctx context.Context, botUserID, appID, mediaID int64) (int, error) {
	return s.withBotInfoBumpRawTx(ctx, botUserID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM bot_app_preview_media WHERE bot_user_id=$1 AND app_id=$2 AND id=$3`, botUserID, appID, mediaID)
		if err != nil {
			return fmt.Errorf("delete bot app preview media: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrBotAppInvalid
		}
		return nil
	})
}

func (s *BotStore) ReorderBotAppPreviewMedia(ctx context.Context, botUserID, appID int64, mediaIDs []int64) (int, error) {
	return s.withBotInfoBumpRawTx(ctx, botUserID, func(tx pgx.Tx) error {
		for i, id := range mediaIDs {
			tag, err := tx.Exec(ctx, `UPDATE bot_app_preview_media SET position=$1, updated_at=now() WHERE bot_user_id=$2 AND app_id=$3 AND id=$4`, -(i + 1), botUserID, appID, id)
			if err != nil {
				return fmt.Errorf("reorder bot preview media: stage: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return domain.ErrBotAppInvalid
			}
		}
		for i, id := range mediaIDs {
			tag, err := tx.Exec(ctx, `UPDATE bot_app_preview_media SET position=$1, updated_at=now() WHERE bot_user_id=$2 AND app_id=$3 AND id=$4`, i+1, botUserID, appID, id)
			if err != nil {
				return fmt.Errorf("reorder bot preview media: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return domain.ErrBotAppInvalid
			}
		}
		return nil
	})
}

func (s *BotStore) UpsertAttachMenuBot(ctx context.Context, bot domain.BotAttachMenuBot) (int, error) {
	if bot.BotUserID == 0 || strings.TrimSpace(bot.ShortName) == "" {
		return 0, domain.ErrBotAttachMenuInvalid
	}
	bot.ShortName = strings.ToLower(strings.TrimSpace(bot.ShortName))
	icons, err := json.Marshal(bot.Icons)
	if err != nil {
		return 0, fmt.Errorf("upsert attach menu bot: icons: %w", err)
	}
	return s.withBotInfoBumpRawTx(ctx, bot.BotUserID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
INSERT INTO attach_menu_bots (
  bot_user_id, app_id, short_name, inactive, has_settings, request_write_access,
  show_in_attach_menu, show_in_side_menu, side_menu_disclaimer_needed, peer_types, icons
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (bot_user_id) DO UPDATE SET
  app_id=EXCLUDED.app_id,
  short_name=EXCLUDED.short_name,
  inactive=EXCLUDED.inactive,
  has_settings=EXCLUDED.has_settings,
  request_write_access=EXCLUDED.request_write_access,
  show_in_attach_menu=EXCLUDED.show_in_attach_menu,
  show_in_side_menu=EXCLUDED.show_in_side_menu,
  side_menu_disclaimer_needed=EXCLUDED.side_menu_disclaimer_needed,
  peer_types=EXCLUDED.peer_types,
  icons=EXCLUDED.icons,
  updated_at=now()`,
			bot.BotUserID, nullableInt64(bot.AppID), bot.ShortName, bot.Inactive, bot.HasSettings, bot.RequestWriteAccess,
			bot.ShowInAttachMenu, bot.ShowInSideMenu, bot.SideMenuDisclaimerNeeded, bot.PeerTypes, icons)
		if err != nil {
			return fmt.Errorf("upsert attach menu bot: %w", err)
		}
		return nil
	})
}

func (s *BotStore) GetAttachMenuBot(ctx context.Context, botUserID int64) (domain.BotAttachMenuBot, bool, error) {
	row := s.db.QueryRow(ctx, attachMenuBotSelectSQL()+` WHERE bot_user_id=$1`, botUserID)
	return scanAttachMenuBot(row)
}

func (s *BotStore) ListAttachMenuBots(ctx context.Context) ([]domain.BotAttachMenuBot, error) {
	rows, err := s.db.Query(ctx, attachMenuBotSelectSQL()+` ORDER BY inactive, bot_user_id`)
	if err != nil {
		return nil, fmt.Errorf("list attach menu bots: %w", err)
	}
	defer rows.Close()
	var out []domain.BotAttachMenuBot
	for rows.Next() {
		bot, err := scanAttachMenuBotRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, bot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list attach menu bots: %w", err)
	}
	return out, nil
}

func (s *BotStore) GetAttachMenuState(ctx context.Context, userID, botUserID int64) (domain.BotAttachMenuState, bool, error) {
	var state domain.BotAttachMenuState
	err := s.db.QueryRow(ctx, `
SELECT user_id, bot_user_id, enabled, write_allowed
FROM attach_menu_user_states WHERE user_id=$1 AND bot_user_id=$2`, userID, botUserID).
		Scan(&state.UserID, &state.BotUserID, &state.Enabled, &state.WriteAllowed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BotAttachMenuState{}, false, nil
		}
		return domain.BotAttachMenuState{}, false, fmt.Errorf("get attach menu state: %w", err)
	}
	return state, true, nil
}

func (s *BotStore) SetAttachMenuState(ctx context.Context, state domain.BotAttachMenuState) (domain.BotAttachMenuState, error) {
	if state.UserID == 0 || state.BotUserID == 0 || state.UserID == state.BotUserID {
		return domain.BotAttachMenuState{}, domain.ErrBotAttachMenuInvalid
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO attach_menu_user_states (user_id, bot_user_id, enabled, write_allowed)
VALUES ($1,$2,$3,$4)
ON CONFLICT (user_id, bot_user_id) DO UPDATE SET
  enabled=EXCLUDED.enabled,
  write_allowed=attach_menu_user_states.write_allowed OR EXCLUDED.write_allowed,
  updated_at=now()`,
		state.UserID, state.BotUserID, state.Enabled, state.WriteAllowed)
	if err != nil {
		return domain.BotAttachMenuState{}, fmt.Errorf("set attach menu state: %w", err)
	}
	return s.GetAttachMenuStateValue(ctx, state.UserID, state.BotUserID)
}

func (s *BotStore) GetAttachMenuStateValue(ctx context.Context, userID, botUserID int64) (domain.BotAttachMenuState, error) {
	state, found, err := s.GetAttachMenuState(ctx, userID, botUserID)
	if err != nil {
		return domain.BotAttachMenuState{}, err
	}
	if !found {
		return domain.BotAttachMenuState{}, domain.ErrBotAttachMenuInvalid
	}
	return state, nil
}

func (s *BotStore) SaveRequestedWebViewButton(ctx context.Context, button domain.BotRequestedWebViewButton) error {
	if button.BotUserID == 0 || button.UserID == 0 || button.WebAppReqID == "" || button.ExpiresAt.IsZero() {
		return domain.ErrBotRequestedButtonInvalid
	}
	peerFilter, err := json.Marshal(button.PeerFilter)
	if err != nil {
		return domain.ErrBotRequestedButtonInvalid
	}
	_, err = s.db.Exec(ctx, `
INSERT INTO webview_requested_buttons (
  webapp_req_id, bot_user_id, user_id, button_id, text, peer_type, max_quantity,
  peer_filter, name_requested, username_requested, photo_requested, created_at, expires_at
)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (webapp_req_id) DO UPDATE SET
  button_id=EXCLUDED.button_id,
  text=EXCLUDED.text,
  peer_type=EXCLUDED.peer_type,
  max_quantity=EXCLUDED.max_quantity,
  peer_filter=EXCLUDED.peer_filter,
  name_requested=EXCLUDED.name_requested,
  username_requested=EXCLUDED.username_requested,
  photo_requested=EXCLUDED.photo_requested,
  expires_at=EXCLUDED.expires_at`,
		button.WebAppReqID, button.BotUserID, button.UserID, button.ButtonID, button.Text,
		button.PeerType, button.MaxQuantity, peerFilter, button.NameRequested,
		button.UsernameRequested, button.PhotoRequested, button.CreatedAt, button.ExpiresAt)
	if err != nil {
		return fmt.Errorf("save requested webview button: %w", err)
	}
	return nil
}

func (s *BotStore) GetRequestedWebViewButton(ctx context.Context, botUserID, userID int64, webAppReqID string) (domain.BotRequestedWebViewButton, bool, error) {
	_, _ = s.db.Exec(ctx, `DELETE FROM webview_requested_buttons WHERE expires_at <= now()`)
	var button domain.BotRequestedWebViewButton
	var peerFilter []byte
	err := s.db.QueryRow(ctx, `
SELECT webapp_req_id, bot_user_id, user_id, button_id, text, peer_type, max_quantity,
       peer_filter, name_requested, username_requested, photo_requested, created_at, expires_at
FROM webview_requested_buttons
WHERE bot_user_id=$1 AND user_id=$2 AND webapp_req_id=$3 AND expires_at > now()`,
		botUserID, userID, webAppReqID).
		Scan(&button.WebAppReqID, &button.BotUserID, &button.UserID, &button.ButtonID,
			&button.Text, &button.PeerType, &button.MaxQuantity, &peerFilter,
			&button.NameRequested, &button.UsernameRequested, &button.PhotoRequested,
			&button.CreatedAt, &button.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BotRequestedWebViewButton{}, false, nil
		}
		return domain.BotRequestedWebViewButton{}, false, fmt.Errorf("get requested webview button: %w", err)
	}
	if string(peerFilter) != "{}" && string(peerFilter) != "null" {
		if err := json.Unmarshal(peerFilter, &button.PeerFilter); err != nil {
			return domain.BotRequestedWebViewButton{}, false, fmt.Errorf("decode requested webview button filter: %w", err)
		}
	}
	return button, true, nil
}

func (s *BotStore) DeleteRequestedWebViewButton(ctx context.Context, botUserID, userID int64, webAppReqID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM webview_requested_buttons WHERE bot_user_id=$1 AND user_id=$2 AND webapp_req_id=$3`, botUserID, userID, webAppReqID)
	if err != nil {
		return fmt.Errorf("delete requested webview button: %w", err)
	}
	return nil
}

func (s *BotStore) SetBotEmojiStatusPermission(ctx context.Context, botUserID, userID int64, allowed bool) error {
	if botUserID == 0 || userID == 0 || botUserID == userID {
		return domain.ErrBotNotFound
	}
	if !allowed {
		_, err := s.db.Exec(ctx, `DELETE FROM bot_emoji_status_permissions WHERE bot_user_id=$1 AND user_id=$2`, botUserID, userID)
		return err
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO bot_emoji_status_permissions (bot_user_id, user_id, allowed)
VALUES ($1,$2,true)
ON CONFLICT (bot_user_id, user_id) DO UPDATE SET allowed=true, updated_at=now()`, botUserID, userID)
	if err != nil {
		return fmt.Errorf("set bot emoji status permission: %w", err)
	}
	return nil
}

func (s *BotStore) BotEmojiStatusPermission(ctx context.Context, botUserID, userID int64) (bool, error) {
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT allowed FROM bot_emoji_status_permissions WHERE bot_user_id=$1 AND user_id=$2`, botUserID, userID).Scan(&allowed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("bot emoji status permission: %w", err)
	}
	return allowed, nil
}

func (s *BotStore) PutWebViewCustomMethodQuery(ctx context.Context, query domain.BotWebViewCustomMethodQuery) error {
	if query.ID == "" || query.BotUserID == 0 || query.UserID == 0 || query.CustomMethod == "" || query.ExpiresAt.IsZero() {
		return domain.ErrBotCustomMethodUnavailable
	}
	var params any = json.RawMessage(query.ParamsJSON)
	if query.ParamsJSON == "" {
		params = json.RawMessage(`{}`)
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO webview_custom_method_queries (id, bot_user_id, user_id, custom_method, params, created_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (id) DO UPDATE SET
  params=EXCLUDED.params,
  expires_at=EXCLUDED.expires_at`,
		query.ID, query.BotUserID, query.UserID, query.CustomMethod, params, query.CreatedAt, query.ExpiresAt)
	if err != nil {
		return fmt.Errorf("put webview custom method query: %w", err)
	}
	return nil
}

func botAppSelectSQL() string {
	return `SELECT id, bot_user_id, short_name, title, description, url, photo_id, document_id,
access_hash, hash, inactive, request_write_access, has_settings, is_main FROM bot_apps`
}

func scanBotApp(row rowScanner) (domain.BotApp, bool, error) {
	app, err := scanBotAppAny(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BotApp{}, false, nil
		}
		return domain.BotApp{}, false, err
	}
	return app, true, nil
}

func scanBotAppRows(rows pgx.Rows) (domain.BotApp, error) {
	return scanBotAppAny(rows)
}

func scanBotAppAny(row rowScanner) (domain.BotApp, error) {
	var app domain.BotApp
	if err := row.Scan(&app.ID, &app.BotUserID, &app.ShortName, &app.Title, &app.Description, &app.URL, &app.PhotoID, &app.DocumentID, &app.AccessHash, &app.Hash, &app.Inactive, &app.RequestWriteAccess, &app.HasSettings, &app.Main); err != nil {
		return domain.BotApp{}, err
	}
	return app, nil
}

func attachMenuBotSelectSQL() string {
	return `SELECT bot_user_id, COALESCE(app_id,0), short_name, inactive, has_settings,
request_write_access, show_in_attach_menu, show_in_side_menu, side_menu_disclaimer_needed,
peer_types, icons FROM attach_menu_bots`
}

func scanAttachMenuBot(row rowScanner) (domain.BotAttachMenuBot, bool, error) {
	bot, err := scanAttachMenuBotAny(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BotAttachMenuBot{}, false, nil
		}
		return domain.BotAttachMenuBot{}, false, err
	}
	return bot, true, nil
}

func scanAttachMenuBotRows(rows pgx.Rows) (domain.BotAttachMenuBot, error) {
	return scanAttachMenuBotAny(rows)
}

func scanAttachMenuBotAny(row rowScanner) (domain.BotAttachMenuBot, error) {
	var bot domain.BotAttachMenuBot
	var icons []byte
	if err := row.Scan(&bot.BotUserID, &bot.AppID, &bot.ShortName, &bot.Inactive, &bot.HasSettings, &bot.RequestWriteAccess, &bot.ShowInAttachMenu, &bot.ShowInSideMenu, &bot.SideMenuDisclaimerNeeded, &bot.PeerTypes, &icons); err != nil {
		return domain.BotAttachMenuBot{}, err
	}
	if len(icons) > 0 {
		_ = json.Unmarshal(icons, &bot.Icons)
	}
	return bot, nil
}

func nullableBotAppInt(value int, valid bool) any {
	if !valid {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
