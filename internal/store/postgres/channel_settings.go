package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) EditChannelTitle(ctx context.Context, req domain.EditChannelTitleRequest) (domain.EditChannelTitleResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || strings.TrimSpace(req.Title) == "" {
		return domain.EditChannelTitleResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditChannelTitleResult{}, fmt.Errorf("edit channel title: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	title := strings.TrimSpace(req.Title)
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditChannelTitleResult{}, fmt.Errorf("begin edit channel title: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelTitleResult{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.EditChannelTitleResult{}, domain.ErrChannelAdminRequired
	}
	if channel.Title == title {
		return domain.EditChannelTitleResult{}, domain.ErrChannelNotModified
	}
	prevTitle := channel.Title
	if _, err := tx.Exec(ctx, `UPDATE channels SET title = $2, updated_at = now() WHERE id = $1`, req.ChannelID, title); err != nil {
		return domain.EditChannelTitleResult{}, fmt.Errorf("update channel title: %w", err)
	}
	channel.Title = title
	msg, event, err := s.insertServiceMessage(ctx, tx, channel, req.UserID, req.Date, domain.ChannelMessageAction{
		Type:  domain.ChannelActionEditTitle,
		Title: title,
	})
	if err != nil {
		return domain.EditChannelTitleResult{}, err
	}
	channel.TopMessageID = msg.ID
	channel.Pts = event.Pts
	// read_outbox 由公共已读水位派生；服务消息只自读（inbox），不能把
	// actor 的 outbox 水位顶到自己刚发的消息上造成虚假双勾。
	if err := upsertChannelDialogTx(ctx, tx, req.UserID, channel, msg, msg.ID, 0); err != nil {
		return domain.EditChannelTitleResult{}, err
	}
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID:  req.ChannelID,
		UserID:     req.UserID,
		Date:       req.Date,
		Type:       domain.ChannelAdminLogChangeTitle,
		PrevString: prevTitle,
		NewString:  title,
	}); err != nil {
		return domain.EditChannelTitleResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EditChannelTitleResult{}, fmt.Errorf("commit edit channel title: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.EditChannelTitleResult{Channel: channel, Message: msg, Event: event, Recipients: recipients}, nil
}

func (s *ChannelStore) SetChannelWallpaper(ctx context.Context, req domain.SetChannelWallpaperRequest) (domain.SetChannelWallpaperResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.SetChannelWallpaperResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.SetChannelWallpaperResult{}, fmt.Errorf("set channel wallpaper: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	wallpaper := domain.CloneWallpaperPtr(req.Wallpaper)
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.SetChannelWallpaperResult{}, fmt.Errorf("begin set channel wallpaper: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.SetChannelWallpaperResult{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.SetChannelWallpaperResult{}, domain.ErrChannelAdminRequired
	}
	if domain.WallpaperEqual(channel.Wallpaper, wallpaper) {
		if err := tx.Commit(ctx); err != nil {
			return domain.SetChannelWallpaperResult{}, fmt.Errorf("commit unchanged channel wallpaper: %w", err)
		}
		committed = true
		return domain.SetChannelWallpaperResult{Channel: channel}, nil
	}
	var wallpaperParam any
	if wallpaper != nil {
		raw, err := json.Marshal(wallpaper)
		if err != nil {
			return domain.SetChannelWallpaperResult{}, fmt.Errorf("encode channel wallpaper: %w", err)
		}
		wallpaperParam = string(raw)
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET wallpaper = $2::jsonb, updated_at = now() WHERE id = $1`, req.ChannelID, wallpaperParam); err != nil {
		return domain.SetChannelWallpaperResult{}, fmt.Errorf("update channel wallpaper: %w", err)
	}
	channel.Wallpaper = domain.CloneWallpaperPtr(wallpaper)
	var (
		msg   domain.ChannelMessage
		event domain.ChannelUpdateEvent
	)
	if wallpaper != nil {
		msg, event, err = s.insertServiceMessage(ctx, tx, channel, req.UserID, req.Date, domain.ChannelMessageAction{
			Type:      domain.ChannelActionSetChatWallpaper,
			Wallpaper: domain.CloneWallpaperPtr(wallpaper),
		})
		if err != nil {
			return domain.SetChannelWallpaperResult{}, err
		}
		channel.TopMessageID = msg.ID
		channel.Pts = event.Pts
		if err := upsertChannelDialogTx(ctx, tx, req.UserID, channel, msg, msg.ID, 0); err != nil {
			return domain.SetChannelWallpaperResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SetChannelWallpaperResult{}, fmt.Errorf("commit set channel wallpaper: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, req.UserID, req.ChannelID, 0)
	return domain.SetChannelWallpaperResult{
		Channel:    channel,
		Message:    msg,
		Event:      event,
		Recipients: recipients,
		Changed:    true,
	}, nil
}

func (s *ChannelStore) EditChannelAbout(ctx context.Context, req domain.EditChannelAboutRequest) (domain.Channel, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("edit channel about: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin edit channel about: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET about = $2, updated_at = now() WHERE id = $1`, req.ChannelID, req.About); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel about: %w", err)
	}
	channel.About = req.About
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit edit channel about: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) CheckUsername(ctx context.Context, userID, channelID int64, username string) (bool, error) {
	if userID == 0 || channelID == 0 || strings.TrimSpace(username) == "" {
		return false, domain.ErrChannelInvalid
	}
	if _, _, err := s.getChannelForMember(ctx, s.db, userID, channelID); err != nil {
		return false, err
	}
	usernameLower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	return peerUsernameAvailable(ctx, s.db, usernameLower, peerUsernameTypeChannel, channelID)
}

func (s *ChannelStore) UpdateUsername(ctx context.Context, req domain.UpdateChannelUsernameRequest) (domain.Channel, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("update channel username: db does not support transactions")
	}
	username := strings.TrimSpace(strings.TrimPrefix(req.Username, "@"))
	usernameLower := strings.ToLower(username)
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin update channel username: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if member.Role != domain.ChannelRoleCreator {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if strings.EqualFold(channel.Username, username) {
		return domain.Channel{}, domain.ErrChannelNotModified
	}
	if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeChannel, req.ChannelID, usernameLower); err != nil {
		return domain.Channel{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET username = NULLIF($2,''), updated_at = now() WHERE id = $1`, req.ChannelID, username); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel username: %w", err)
	}
	if err := markUserChannelMemberIndexPublicTx(ctx, tx, req.ChannelID, username != ""); err != nil {
		return domain.Channel{}, err
	}
	prevUsername := channel.Username
	channel.Username = username
	if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID:  req.ChannelID,
		UserID:     req.UserID,
		Date:       nowUnix(),
		Type:       domain.ChannelAdminLogChangeUsername,
		PrevString: prevUsername,
		NewString:  username,
	}); err != nil {
		return domain.Channel{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit update channel username: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) SetChannelVerified(ctx context.Context, channelID int64, verified bool) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	channel, err := s.channelByID(ctx, s.db, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if channel.Verified == verified {
		return channel, nil
	}
	if _, err := s.db.Exec(ctx, `UPDATE channels SET verified = $2, updated_at = now() WHERE id = $1 AND NOT deleted`, channelID, verified); err != nil {
		return domain.Channel{}, fmt.Errorf("set channel verified: %w", err)
	}
	if s.rowCache != nil {
		s.rowCache.delete(channelID)
	}
	channel.Verified = verified
	return channel, nil
}

// SetChannelScamFake 设置/取消频道的 scam 与 fake 标记。
func (s *ChannelStore) SetChannelScamFake(ctx context.Context, channelID int64, scam, fake bool) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	if scam && fake {
		return domain.Channel{}, domain.ErrPeerModerationFlagsInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("set channel scam/fake: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin set channel scam/fake: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	var currentScam, currentFake bool
	if err := tx.QueryRow(ctx, `
SELECT scam, fake
FROM channels
WHERE id = $1 AND NOT deleted
FOR UPDATE`, channelID).Scan(&currentScam, &currentFake); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ErrChannelInvalid
		}
		return domain.Channel{}, fmt.Errorf("lock channel scam/fake: %w", err)
	}
	channel, err := s.channelByID(ctx, tx, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if channel.Scam == scam && channel.Fake == fake {
		if err := tx.Commit(ctx); err != nil {
			return domain.Channel{}, fmt.Errorf("commit unchanged channel scam/fake: %w", err)
		}
		committed = true
		return channel, nil
	}
	if currentScam != channel.Scam || currentFake != channel.Fake {
		return domain.Channel{}, fmt.Errorf("channel scam/fake snapshot changed while locked")
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET scam = $2, fake = $3, updated_at = now() WHERE id = $1 AND NOT deleted`, channelID, scam, fake); err != nil {
		return domain.Channel{}, fmt.Errorf("set channel scam/fake: %w", err)
	}
	audience, err := channelModerationFlagAudience(ctx, tx, channelID, maxModerationFlagAudience)
	if err != nil {
		return domain.Channel{}, err
	}
	qtx := sqlcgen.New(tx)
	date := nowUnix()
	for _, viewerUserID := range audience {
		pts, err := reserveUserPts(ctx, tx, viewerUserID, 1)
		if err != nil {
			return domain.Channel{}, fmt.Errorf("reserve channel moderation flag pts for viewer %d: %w", viewerUserID, err)
		}
		event := domain.UpdateEvent{
			UserID: viewerUserID,
			Type:   domain.UpdateEventChannelState,
			Pts:    pts, PtsCount: 1, Date: date,
			Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		}
		if err := appendUserUpdateEvent(ctx, tx, qtx, viewerUserID, event); err != nil {
			return domain.Channel{}, fmt.Errorf("append channel moderation flag event for viewer %d: %w", viewerUserID, err)
		}
		if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
			TargetUserID: viewerUserID,
			Pts:          int32(pts),
			EventType:    string(event.Type),
		}); err != nil {
			return domain.Channel{}, fmt.Errorf("enqueue channel moderation flag dispatch for viewer %d: %w", viewerUserID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit channel scam/fake: %w", err)
	}
	committed = true
	if s.rowCache != nil {
		s.rowCache.delete(channelID)
	}
	channel.Scam = scam
	channel.Fake = fake
	return channel, nil
}

func channelModerationFlagAudience(ctx context.Context, db sqlcgen.DBTX, channelID int64, limit int) ([]int64, error) {
	if channelID <= 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, `
SELECT cm.user_id
FROM channel_members cm
JOIN users u ON u.id = cm.user_id AND u.deleted_at IS NULL
WHERE cm.channel_id = $1 AND cm.status = 'active'
ORDER BY cm.user_id
LIMIT $2`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("list channel moderation flag audience: %w", err)
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scan channel moderation flag audience: %w", err)
		}
		if userID != 0 {
			out = append(out, userID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel moderation flag audience: %w", err)
	}
	return out, nil
}

// SetChannelAdminSettings applies an admin-direct moderation-settings patch
// (no membership/permission checks). nil fields are left unchanged.
func (s *ChannelStore) SetChannelAdminSettings(ctx context.Context, channelID int64, patch domain.ChannelAdminSettings) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	if patch.Empty() {
		return s.channelByID(ctx, s.db, channelID)
	}
	sets := make([]string, 0, 7)
	args := []any{channelID}
	idx := 2
	add := func(col string, val any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, idx))
		args = append(args, val)
		idx++
	}
	if patch.Gigagroup != nil {
		add("gigagroup", *patch.Gigagroup)
	}
	if patch.AntiSpam != nil {
		add("antispam", *patch.AntiSpam)
	}
	if patch.ParticipantsHidden != nil {
		add("participants_hidden", *patch.ParticipantsHidden)
	}
	if patch.NoForwards != nil {
		add("noforwards", *patch.NoForwards)
	}
	if patch.JoinToSend != nil {
		add("join_to_send", *patch.JoinToSend)
	}
	if patch.JoinRequest != nil {
		add("join_request", *patch.JoinRequest)
	}
	if patch.SlowmodeSeconds != nil {
		add("slowmode_seconds", *patch.SlowmodeSeconds)
	}
	query := "UPDATE channels SET " + strings.Join(sets, ", ") + ", updated_at = now() WHERE id = $1 AND NOT deleted"
	if _, err := s.db.Exec(ctx, query, args...); err != nil {
		return domain.Channel{}, fmt.Errorf("set channel admin settings: %w", err)
	}
	if s.rowCache != nil {
		s.rowCache.delete(channelID)
	}
	return s.channelByID(ctx, s.db, channelID)
}

// SetChannelUsernameAdmin force-sets or clears (empty) a channel username with
// no permission checks. Username uniqueness is still enforced by peer_usernames.
func (s *ChannelStore) SetChannelUsernameAdmin(ctx context.Context, channelID int64, username string) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("set channel username: db does not support transactions")
	}
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	usernameLower := strings.ToLower(username)
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin set channel username: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, err := s.channelByID(ctx, tx, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if strings.EqualFold(channel.Username, username) {
		return channel, nil
	}
	if err := replacePeerUsernameTx(ctx, tx, peerUsernameTypeChannel, channelID, usernameLower); err != nil {
		return domain.Channel{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET username = NULLIF($2,''), updated_at = now() WHERE id = $1`, channelID, username); err != nil {
		return domain.Channel{}, fmt.Errorf("set channel username: %w", err)
	}
	if err := markUserChannelMemberIndexPublicTx(ctx, tx, channelID, username != ""); err != nil {
		return domain.Channel{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit set channel username: %w", err)
	}
	committed = true
	if s.rowCache != nil {
		s.rowCache.delete(channelID)
	}
	channel.Username = username
	return channel, nil
}

// SetChannelColorAdmin force-sets a channel name/profile color (no permission checks).
func (s *ChannelStore) SetChannelColorAdmin(ctx context.Context, channelID int64, forProfile bool, color domain.ChannelPeerColor) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	channel, err := s.channelByID(ctx, s.db, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if forProfile {
		if _, err := s.db.Exec(ctx, `UPDATE channels SET profile_color_set = $2, profile_color = $3, profile_color_background_emoji_id = $4, updated_at = now() WHERE id = $1`,
			channelID, color.HasColor, color.Color, color.BackgroundEmojiID); err != nil {
			return domain.Channel{}, fmt.Errorf("set channel profile color: %w", err)
		}
		channel.ProfileColor = color
	} else {
		if _, err := s.db.Exec(ctx, `UPDATE channels SET color_set = $2, color = $3, color_background_emoji_id = $4, updated_at = now() WHERE id = $1`,
			channelID, color.HasColor, color.Color, color.BackgroundEmojiID); err != nil {
			return domain.Channel{}, fmt.Errorf("set channel color: %w", err)
		}
		channel.Color = color
	}
	if s.rowCache != nil {
		s.rowCache.delete(channelID)
	}
	return channel, nil
}

// SetChannelEmojiStatusAdmin force-sets or clears a channel emoji status (no permission checks).
func (s *ChannelStore) SetChannelEmojiStatusAdmin(ctx context.Context, channelID int64, status domain.ChannelEmojiStatus) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	if status.DocumentID == 0 {
		status.Until = 0
	}
	channel, err := s.channelByID(ctx, s.db, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE channels SET emoji_status_document_id = $2, emoji_status_until = $3, updated_at = now() WHERE id = $1`,
		channelID, status.DocumentID, status.Until); err != nil {
		return domain.Channel{}, fmt.Errorf("set channel emoji status: %w", err)
	}
	if s.rowCache != nil {
		s.rowCache.delete(channelID)
	}
	channel.EmojiStatus = status
	return channel, nil
}

func (s *ChannelStore) ResolvePublicChannelUsername(ctx context.Context, viewerUserID int64, username string) (domain.Channel, bool, error) {
	_ = viewerUserID // zero is the anonymous public-web view; this query is viewer-independent.
	usernameLower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	if usernameLower == "" {
		return domain.Channel{}, false, nil
	}
	owner, found, err := getPeerUsernameOwner(ctx, s.db, usernameLower, false)
	if err != nil {
		return domain.Channel{}, false, fmt.Errorf("resolve public channel username: %w", err)
	}
	if !found || owner.peerType != peerUsernameTypeChannel {
		return domain.Channel{}, false, nil
	}
	ch, err := getChannelByID(ctx, s.db, owner.peerID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) {
			return domain.Channel{}, false, nil
		}
		return domain.Channel{}, false, fmt.Errorf("resolve public channel username channel: %w", err)
	}
	if !publicPreviewableChannel(ch) || !strings.EqualFold(ch.Username, usernameLower) {
		return domain.Channel{}, false, nil
	}
	return ch, true, nil
}

func (s *ChannelStore) SetSignatures(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("toggle channel signatures: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin toggle channel signatures: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.Signatures
	if _, err := tx.Exec(ctx, `UPDATE channels SET signatures = $2, updated_at = now() WHERE id = $1`, channelID, enabled); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel signatures: %w", err)
	}
	channel.Signatures = enabled
	if prev != enabled {
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      nowUnix(),
			Type:      domain.ChannelAdminLogToggleSignatures,
			PrevBool:  prev,
			NewBool:   enabled,
		}); err != nil {
			return domain.Channel{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit toggle channel signatures: %w", err)
	}
	committed = true
	return channel, nil
}

// SetChannelPhoto 设置/清除频道头像，并生成对应频道服务消息。photo==nil 表示清除。
func (s *ChannelStore) SetChannelPhoto(ctx context.Context, userID, channelID int64, photo *domain.Photo, date int) (domain.SetChannelPhotoResult, error) {
	if userID == 0 || channelID == 0 {
		return domain.SetChannelPhotoResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.SetChannelPhotoResult{}, fmt.Errorf("set channel photo: db does not support transactions")
	}
	if date == 0 {
		date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.SetChannelPhotoResult{}, fmt.Errorf("begin set channel photo: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.SetChannelPhotoResult{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.SetChannelPhotoResult{}, domain.ErrChannelAdminRequired
	}
	var (
		photoID  int64
		dcID     int
		stripped []byte
		action   domain.ChannelMessageAction
	)
	if photo != nil && photo.ID != 0 {
		if channel.PhotoID == photo.ID {
			return domain.SetChannelPhotoResult{}, domain.ErrChannelNotModified
		}
		photoID = photo.ID
		dcID = photo.DCID
		stripped = domain.StrippedFromSizes(photo.Sizes)
		action = domain.ChannelMessageAction{
			Type:  domain.ChannelActionChatEditPhoto,
			Photo: domain.ClonePhotoPtr(photo),
		}
	} else {
		if channel.PhotoID == 0 {
			return domain.SetChannelPhotoResult{}, domain.ErrChannelNotModified
		}
		action = domain.ChannelMessageAction{Type: domain.ChannelActionChatDeletePhoto}
	}
	if stripped == nil {
		stripped = []byte{}
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET photo_id = $2, photo_dc_id = $3, photo_stripped = $4, updated_at = now() WHERE id = $1`,
		channelID, photoID, dcID, stripped); err != nil {
		return domain.SetChannelPhotoResult{}, fmt.Errorf("update channel photo: %w", err)
	}
	if s.rowCache != nil {
		s.rowCache.delete(channelID)
	}
	channel.PhotoID = photoID
	channel.PhotoDCID = dcID
	channel.PhotoStripped = stripped
	msg, event, err := s.insertServiceMessage(ctx, tx, channel, userID, date, action)
	if err != nil {
		return domain.SetChannelPhotoResult{}, err
	}
	channel.TopMessageID = msg.ID
	channel.Pts = event.Pts
	if err := upsertChannelDialogTx(ctx, tx, userID, channel, msg, msg.ID, 0); err != nil {
		return domain.SetChannelPhotoResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SetChannelPhotoResult{}, fmt.Errorf("commit set channel photo: %w", err)
	}
	committed = true
	recipients, _ := s.ListActiveChannelMemberIDs(ctx, userID, channelID, 0)
	return domain.SetChannelPhotoResult{
		Channel:    channel,
		Message:    msg,
		Event:      event,
		Recipients: recipients,
		Changed:    true,
	}, nil
}

func (s *ChannelStore) SetAutotranslation(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("toggle channel autotranslation: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin toggle channel autotranslation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.Autotranslation
	if _, err := tx.Exec(ctx, `UPDATE channels SET autotranslation = $2, updated_at = now() WHERE id = $1`, channelID, enabled); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel autotranslation: %w", err)
	}
	channel.Autotranslation = enabled
	if prev != enabled {
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      nowUnix(),
			Type:      domain.ChannelAdminLogToggleAutotranslation,
			PrevBool:  prev,
			NewBool:   enabled,
		}); err != nil {
			return domain.Channel{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit toggle channel autotranslation: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) SetRestrictedSponsored(ctx context.Context, userID, channelID int64, restricted bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("toggle channel restricted sponsored: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin toggle channel restricted sponsored: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET restricted_sponsored = $2, updated_at = now() WHERE id = $1`, channelID, restricted); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel restricted sponsored: %w", err)
	}
	channel.RestrictedSponsored = restricted
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit toggle channel restricted sponsored: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) SetAntiSpam(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("toggle channel antispam: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin toggle channel antispam: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !channel.Megagroup || !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.AntiSpam
	if _, err := tx.Exec(ctx, `UPDATE channels SET antispam = $2, updated_at = now() WHERE id = $1`, channelID, enabled); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel antispam: %w", err)
	}
	channel.AntiSpam = enabled
	if prev != enabled {
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      nowUnix(),
			Type:      domain.ChannelAdminLogToggleAntiSpam,
			PrevBool:  prev,
			NewBool:   enabled,
		}); err != nil {
			return domain.Channel{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit toggle channel antispam: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) SetSlowMode(ctx context.Context, userID, channelID int64, seconds int) (domain.Channel, error) {
	if userID == 0 || channelID == 0 || !domain.ValidChannelSlowModeSeconds(seconds) {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("toggle channel slowmode: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin toggle channel slowmode: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !channel.Megagroup || !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.SlowmodeSeconds
	if _, err := tx.Exec(ctx, `UPDATE channels SET slowmode_seconds = $2, updated_at = now() WHERE id = $1`, channelID, seconds); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel slowmode: %w", err)
	}
	channel.SlowmodeSeconds = seconds
	if prev != seconds {
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      nowUnix(),
			Type:      domain.ChannelAdminLogToggleSlowMode,
			PrevInt:   prev,
			NewInt:    seconds,
		}); err != nil {
			return domain.Channel{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit toggle channel slowmode: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) SetNoForwards(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("toggle channel noforwards: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin toggle channel noforwards: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET noforwards = $2, updated_at = now() WHERE id = $1`, channelID, enabled); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel noforwards: %w", err)
	}
	channel.NoForwards = enabled
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit toggle channel noforwards: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) SetColor(ctx context.Context, userID, channelID int64, forProfile bool, color domain.ChannelPeerColor) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("set channel color: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin set channel color: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if forProfile {
		if _, err := tx.Exec(ctx, `UPDATE channels SET profile_color_set = $2, profile_color = $3, profile_color_background_emoji_id = $4, updated_at = now() WHERE id = $1`,
			channelID, color.HasColor, color.Color, color.BackgroundEmojiID); err != nil {
			return domain.Channel{}, fmt.Errorf("update channel profile color: %w", err)
		}
		channel.ProfileColor = color
	} else {
		if _, err := tx.Exec(ctx, `UPDATE channels SET color_set = $2, color = $3, color_background_emoji_id = $4, updated_at = now() WHERE id = $1`,
			channelID, color.HasColor, color.Color, color.BackgroundEmojiID); err != nil {
			return domain.Channel{}, fmt.Errorf("update channel color: %w", err)
		}
		channel.Color = color
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit set channel color: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) SetEmojiStatus(ctx context.Context, userID, channelID int64, status domain.ChannelEmojiStatus) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	if status.DocumentID == 0 {
		status.Until = 0
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("set channel emoji status: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin set channel emoji status: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if _, err := tx.Exec(ctx, `UPDATE channels SET emoji_status_document_id = $2, emoji_status_until = $3, updated_at = now() WHERE id = $1`,
		channelID, status.DocumentID, status.Until); err != nil {
		return domain.Channel{}, fmt.Errorf("update channel emoji status: %w", err)
	}
	channel.EmojiStatus = status
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit set channel emoji status: %w", err)
	}
	committed = true
	return channel, nil
}

func (s *ChannelStore) ListChannelRecommendations(ctx context.Context, req domain.ChannelRecommendationsRequest) (domain.ChannelRecommendationsResult, error) {
	if req.UserID == 0 || req.SourceChannelID < 0 {
		return domain.ChannelRecommendationsResult{}, domain.ErrChannelInvalid
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelRecommendationsLimit {
		limit = domain.DefaultChannelRecommendationsLimit
	}
	probeLimit := domain.MaxChannelRecommendationsLimit
	if probeLimit < limit {
		probeLimit = limit
	}
	args := []any{req.UserID, req.SourceChannelID}
	where := []string{
		"($1::bigint <> 0)",
		"c.broadcast",
		"NOT c.megagroup",
		"NOT c.deleted",
		"COALESCE(c.username, '') <> ''",
		"($2::bigint = 0 OR c.id <> $2)",
	}
	if req.SourceChannelID == 0 {
		where = append(where, `NOT EXISTS (
  SELECT 1
  FROM user_channel_member_index i
  WHERE i.user_id = $1
    AND i.channel_id = c.id
    AND i.status = 'active'
)`)
	}
	whereSQL := strings.Join(where, " AND ")
	args = append(args, probeLimit)
	rows, err := s.db.Query(ctx, `
SELECT `+channelColumns+`
FROM channels c
WHERE `+whereSQL+`
ORDER BY c.participants_count DESC, c.date DESC, c.id DESC
LIMIT $3`, args...)
	if err != nil {
		return domain.ChannelRecommendationsResult{}, fmt.Errorf("list channel recommendations: %w", err)
	}
	defer rows.Close()
	out := domain.ChannelRecommendationsResult{Channels: make([]domain.Channel, 0, limit)}
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return domain.ChannelRecommendationsResult{}, err
		}
		out.Count++
		if len(out.Channels) < limit {
			out.Channels = append(out.Channels, ch)
		}
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelRecommendationsResult{}, err
	}
	return out, nil
}

func (s *ChannelStore) ListDiscussionGroups(ctx context.Context, userID int64, limit int) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxDiscussionGroupsLimit {
		limit = domain.MaxDiscussionGroupsLimit
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id
FROM user_channel_member_index
WHERE user_id = $1
  AND status = 'active'
  AND megagroup
  AND NOT broadcast
  AND NOT forum
  AND NOT deleted
  AND (role = 'creator' OR can_pin_messages)
ORDER BY channel_id DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list discussion groups: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var channelID int64
		if err := rows.Scan(&channelID); err != nil {
			return nil, err
		}
		ids = append(ids, channelID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	channels, err := listChannelsByIDsInOrder(ctx, s.db, ids)
	if err != nil {
		return nil, fmt.Errorf("list discussion group details: %w", err)
	}
	out := make([]domain.Channel, 0, len(channels))
	for _, channel := range channels {
		if !channel.Megagroup || channel.Broadcast || channel.Forum || channel.Deleted {
			continue
		}
		out = append(out, channel)
	}
	return out, nil
}

func (s *ChannelStore) SetDiscussionGroup(ctx context.Context, userID, broadcastID, groupID int64) (domain.DiscussionGroupUpdateResult, error) {
	if userID == 0 {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrChannelInvalid
	}
	if broadcastID == 0 && groupID == 0 {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrLinkNotModified
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DiscussionGroupUpdateResult{}, fmt.Errorf("set discussion group: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DiscussionGroupUpdateResult{}, fmt.Errorf("begin set discussion group: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	changed := make(map[int64]domain.Channel)
	markChanged := func(channel domain.Channel) {
		if channel.ID != 0 {
			changed[channel.ID] = channel
		}
	}
	setLinked := func(channel domain.Channel, linkedID int64) (domain.Channel, error) {
		if channel.LinkedChatID == linkedID {
			return channel, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE channels SET linked_chat_id = $2, updated_at = now() WHERE id = $1`, channel.ID, linkedID); err != nil {
			return domain.Channel{}, fmt.Errorf("update linked chat: %w", err)
		}
		channel.LinkedChatID = linkedID
		markChanged(channel)
		return channel, nil
	}
	logLinkChange := func(channelID, prev, next int64) error {
		if prev == next || channelID == 0 {
			return nil
		}
		return s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      nowUnix(),
			Type:      domain.ChannelAdminLogChangeLinkedChat,
			PrevInt:   int(prev),
			NewInt:    int(next),
		})
	}

	if broadcastID == 0 {
		group, groupMember, err := s.getChannelForMember(ctx, tx, userID, groupID)
		if err != nil || !validDiscussionGroup(group) {
			return domain.DiscussionGroupUpdateResult{}, domain.ErrMegagroupIDInvalid
		}
		if !canManageDiscussionGroup(groupMember) {
			return domain.DiscussionGroupUpdateResult{}, domain.ErrChannelAdminRequired
		}
		oldBroadcastID := group.LinkedChatID
		if oldBroadcastID == 0 {
			return domain.DiscussionGroupUpdateResult{}, domain.ErrLinkNotModified
		}
		oldBroadcast, err := getChannelByID(ctx, tx, oldBroadcastID)
		if err == nil && oldBroadcast.LinkedChatID == groupID {
			updated, err := setLinked(oldBroadcast, 0)
			if err != nil {
				return domain.DiscussionGroupUpdateResult{}, err
			}
			if err := logLinkChange(updated.ID, groupID, 0); err != nil {
				return domain.DiscussionGroupUpdateResult{}, err
			}
		} else if err != nil && !errors.Is(err, domain.ErrChannelInvalid) {
			return domain.DiscussionGroupUpdateResult{}, err
		}
		if _, err := setLinked(group, 0); err != nil {
			return domain.DiscussionGroupUpdateResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.DiscussionGroupUpdateResult{}, fmt.Errorf("commit set discussion group: %w", err)
		}
		committed = true
		return discussionGroupUpdateResult(changed), nil
	}

	broadcast, broadcastMember, err := s.getChannelForMember(ctx, tx, userID, broadcastID)
	if err != nil || !broadcast.Broadcast || broadcast.Megagroup {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrBroadcastIDInvalid
	}
	if !canManageDiscussionBroadcast(broadcastMember) {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrChannelAdminRequired
	}
	oldGroupID := broadcast.LinkedChatID
	if groupID == 0 {
		if oldGroupID == 0 {
			return domain.DiscussionGroupUpdateResult{}, domain.ErrLinkNotModified
		}
		updated, err := setLinked(broadcast, 0)
		if err != nil {
			return domain.DiscussionGroupUpdateResult{}, err
		}
		if err := logLinkChange(updated.ID, oldGroupID, 0); err != nil {
			return domain.DiscussionGroupUpdateResult{}, err
		}
		oldGroup, err := getChannelByID(ctx, tx, oldGroupID)
		if err == nil && oldGroup.LinkedChatID == broadcastID {
			if _, err := setLinked(oldGroup, 0); err != nil {
				return domain.DiscussionGroupUpdateResult{}, err
			}
		} else if err != nil && !errors.Is(err, domain.ErrChannelInvalid) {
			return domain.DiscussionGroupUpdateResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.DiscussionGroupUpdateResult{}, fmt.Errorf("commit set discussion group: %w", err)
		}
		committed = true
		return discussionGroupUpdateResult(changed), nil
	}

	group, groupMember, err := s.getChannelForMember(ctx, tx, userID, groupID)
	if err != nil || !validDiscussionGroup(group) {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrMegagroupIDInvalid
	}
	if group.PreHistoryHidden {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrMegagroupPrehistoryHidden
	}
	if !canManageDiscussionGroup(groupMember) {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrChannelAdminRequired
	}
	if oldGroupID == groupID && group.LinkedChatID == broadcastID {
		return domain.DiscussionGroupUpdateResult{}, domain.ErrLinkNotModified
	}
	oldBroadcastID := group.LinkedChatID
	if oldGroupID != 0 && oldGroupID != groupID {
		oldGroup, err := getChannelByID(ctx, tx, oldGroupID)
		if err == nil && oldGroup.LinkedChatID == broadcastID {
			if _, err := setLinked(oldGroup, 0); err != nil {
				return domain.DiscussionGroupUpdateResult{}, err
			}
		} else if err != nil && !errors.Is(err, domain.ErrChannelInvalid) {
			return domain.DiscussionGroupUpdateResult{}, err
		}
	}
	if oldBroadcastID != 0 && oldBroadcastID != broadcastID {
		oldBroadcast, err := getChannelByID(ctx, tx, oldBroadcastID)
		if err == nil && oldBroadcast.LinkedChatID == groupID {
			updated, err := setLinked(oldBroadcast, 0)
			if err != nil {
				return domain.DiscussionGroupUpdateResult{}, err
			}
			if err := logLinkChange(updated.ID, groupID, 0); err != nil {
				return domain.DiscussionGroupUpdateResult{}, err
			}
		} else if err != nil && !errors.Is(err, domain.ErrChannelInvalid) {
			return domain.DiscussionGroupUpdateResult{}, err
		}
	}
	updatedBroadcast, err := setLinked(broadcast, groupID)
	if err != nil {
		return domain.DiscussionGroupUpdateResult{}, err
	}
	if _, err := setLinked(group, broadcastID); err != nil {
		return domain.DiscussionGroupUpdateResult{}, err
	}
	if err := logLinkChange(updatedBroadcast.ID, oldGroupID, groupID); err != nil {
		return domain.DiscussionGroupUpdateResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.DiscussionGroupUpdateResult{}, fmt.Errorf("commit set discussion group: %w", err)
	}
	committed = true
	return discussionGroupUpdateResult(changed), nil
}

func validDiscussionGroup(channel domain.Channel) bool {
	return channel.Megagroup && !channel.Broadcast && !channel.Forum && !channel.Deleted
}

func channelSlowModeWait(channel domain.Channel, member domain.ChannelMember, now int) int {
	if channel.SlowmodeSeconds <= 0 || member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
		return 0
	}
	next := member.SlowmodeLastSendDate + channel.SlowmodeSeconds
	if now >= next {
		return 0
	}
	return next - now
}
