package postgres

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/sync/errgroup"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

// channelIDAtLeastAllocator is the collision self-healing extension of the Redis allocator: it pushes the
// counter above floor in one step, avoiding 1-by-1 stepping to catch up with a large gap.
type channelIDAtLeastAllocator interface {
	NextChannelIDAtLeast(ctx context.Context, floor int64) (int64, error)
}

// allocateFreshChannelID allocates an unoccupied channel id. The counter may lag behind
// the channels table's true max id (Redis snapshot rollback, or the test fallback allocator bypassing
// Redis and writing directly to the same DB); using it blindly would collide with the primary key and could pollute subsequent channel message id
// allocation; here we do a point-lookup pre-check and, on collision, reconcile the allocator to the table's max id before taking one.
func (s *ChannelStore) allocateFreshChannelID(ctx context.Context) (int64, error) {
	const maxAttempts = 4
	channelID, err := s.ids.NextChannelID(ctx)
	if err != nil {
		return 0, fmt.Errorf("allocate channel id: %w", err)
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM channels WHERE id = $1)`, channelID).Scan(&exists); err != nil {
			return 0, fmt.Errorf("probe channel id %d: %w", channelID, err)
		}
		if !exists {
			return channelID, nil
		}
		var maxID int64
		if err := s.db.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM channels`).Scan(&maxID); err != nil {
			return 0, fmt.Errorf("load max channel id: %w", err)
		}
		if atLeast, ok := s.ids.(channelIDAtLeastAllocator); ok {
			channelID, err = atLeast.NextChannelIDAtLeast(ctx, maxID)
		} else {
			channelID, err = s.ids.NextChannelID(ctx)
		}
		if err != nil {
			return 0, fmt.Errorf("re-allocate channel id past %d: %w", maxID, err)
		}
	}
	return 0, fmt.Errorf("allocate channel id: counter still colliding after %d attempts (id %d)", maxAttempts, channelID)
}

func (s *ChannelStore) CreateChannel(ctx context.Context, req domain.CreateChannelRequest) (domain.CreateChannelResult, error) {
	if req.CreatorUserID == 0 || strings.TrimSpace(req.Title) == "" {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	if !req.Broadcast && !req.Megagroup {
		req.Broadcast = true
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.CreateChannelResult{}, fmt.Errorf("create channel: db does not support transactions")
	}
	channelID, err := s.allocateFreshChannelID(ctx)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	accessHash, err := randomChannelAccessHash()
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	inviteID, err := randomPositiveInt64()
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	inviteHash, err := randomInviteHash()
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	date := req.Date
	if date == 0 {
		date = nowUnix()
	}
	members := []domain.ChannelMember{creatorChannelMember(channelID, req.CreatorUserID, date)}
	for _, userID := range uniqueChannelUserIDs(req.MemberUserIDs, req.CreatorUserID) {
		members = append(members, domain.ChannelMember{
			ChannelID:     channelID,
			UserID:        userID,
			InviterUserID: req.CreatorUserID,
			Role:          domain.ChannelRoleMember,
			Status:        domain.ChannelMemberActive,
			JoinedAt:      date,
		})
	}

	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("begin create channel: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	msgID, err := s.msgIDs.NextChannelMessageID(ctx, channelID)
	if err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("allocate channel message id: %w", err)
	}
	// PTS 1 is the empty channel message-box baseline. The create service
	// message is the first real event, so its post-event state is 2.
	pts := domain.FirstChannelEventPts
	channel := domain.Channel{
		ID:                channelID,
		AccessHash:        accessHash,
		CreatorUserID:     req.CreatorUserID,
		Title:             strings.TrimSpace(req.Title),
		About:             req.About,
		Broadcast:         req.Broadcast,
		Megagroup:         req.Megagroup,
		Forum:             req.Forum,
		ForumTabs:         req.ForumTabs,
		ParticipantsCount: len(members),
		AdminsCount:       1,
		HasLink:           true,
		TopMessageID:      msgID,
		Pts:               pts,
		TTLPeriod:         req.TTLPeriod,
		Date:              date,
	}
	msg := domain.ChannelMessage{
		ChannelID:    channelID,
		ID:           msgID,
		SenderUserID: req.CreatorUserID,
		From:         domain.Peer{Type: domain.PeerTypeUser, ID: req.CreatorUserID},
		Date:         date,
		Post:         channel.Broadcast,
		Action:       &domain.ChannelMessageAction{Type: domain.ChannelActionCreate, Title: channel.Title},
		Pts:          pts,
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    channelID,
		Type:         domain.ChannelUpdateNewMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         date,
		Message:      msg,
		SenderUserID: req.CreatorUserID,
	}
	if err := insertChannelTx(ctx, tx, channel); err != nil {
		return domain.CreateChannelResult{}, err
	}
	for _, member := range members {
		if err := upsertChannelMemberTx(ctx, tx, channel, member); err != nil {
			return domain.CreateChannelResult{}, err
		}
	}
	if err := insertChannelMessageTx(ctx, tx, msg); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := insertChannelEventTx(ctx, tx, event); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE channel_update_checkpoints
SET retained_through_pts = $2,
    updated_at = now()
WHERE channel_id = $1`, channelID, domain.InitialChannelPts); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("initialize channel pts baseline: %w", err)
	}
	for _, member := range members {
		readMax := 0
		if member.UserID == req.CreatorUserID {
			readMax = msgID
		}
		if err := upsertChannelDialogTx(ctx, tx, member.UserID, channel, msg, readMax, 0); err != nil {
			return domain.CreateChannelResult{}, err
		}
	}
	if err := insertChannelInviteTx(ctx, tx, domain.ChannelInvite{
		ChannelID:   channelID,
		InviteID:    inviteID,
		Hash:        inviteHash,
		AdminUserID: req.CreatorUserID,
		Permanent:   true,
		Date:        date,
	}); err != nil {
		return domain.CreateChannelResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CreateChannelResult{}, fmt.Errorf("commit create channel: %w", err)
	}
	committed = true
	return domain.CreateChannelResult{
		Channel:    channel,
		Members:    append([]domain.ChannelMember(nil), members...),
		Message:    msg,
		Event:      event,
		Recipients: channelMemberIDs(members),
	}, nil
}

func (s *ChannelStore) GetChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	channel, member, preview, err := s.getChannelForViewer(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if preview {
		selfBoosts, err := s.countActiveUserBoostsForPeer(ctx, s.db, viewerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, nowUnix())
		if err != nil {
			return domain.ChannelView{}, err
		}
		return domain.ChannelView{
			Channel:           channel,
			Self:              member,
			Dialog:            previewChannelDialog(viewerUserID, channel, member),
			SelfBoostsApplied: selfBoosts,
		}, nil
	}
	// getChannelDialog and countActiveUserBoostsForPeer do not depend on each other; running them concurrently saves a serial round trip.
	// Both go through s.db (pgxpool, concurrency-safe, each taking its own connection) and write their own result variables.
	var (
		dialog         domain.ChannelDialog
		selfBoosts     int
		exportedInvite *domain.ChannelInvite
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var derr error
		dialog, derr = s.getChannelDialog(gctx, s.db, viewerUserID, channel)
		return derr
	})
	g.Go(func() error {
		var berr error
		selfBoosts, berr = s.countActiveUserBoostsForPeer(gctx, s.db, viewerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}, nowUnix())
		return berr
	})
	if canExportChannelInvite(member) {
		g.Go(func() error {
			invite, found, ierr := s.getPermanentInviteForAdmin(gctx, s.db, channel.ID, viewerUserID)
			if ierr != nil {
				return ierr
			}
			if found {
				exportedInvite = &invite
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return domain.ChannelView{}, err
	}
	view := domain.ChannelView{Channel: channel, Self: member, Dialog: dialog, SelfBoostsApplied: selfBoosts, ExportedInvite: exportedInvite}
	view.LinkedChatUsable = s.linkedChatUsable(ctx, channel)
	return view, nil
}

// linkedChatUsable verifies that the counterpart pointed to by Channel.LinkedChatID is really usable. linked_chat_id is bidirectional:
// a broadcast points to a discussion group (which must be a megagroup), a megagroup points to the parent channel (which must be a broadcast). A dangling
// direction in either case (counterpart missing/deleted/type mismatch) makes the client render a "Discussion" entry that opens an empty channel.
func (s *ChannelStore) linkedChatUsable(ctx context.Context, ch domain.Channel) bool {
	if ch.LinkedChatID == 0 {
		return false
	}
	linked, err := s.channelByID(ctx, s.db, ch.LinkedChatID)
	if err != nil || linked.Deleted {
		return false
	}
	switch {
	case ch.Broadcast:
		return linked.Megagroup && !linked.Broadcast
	case ch.Megagroup:
		return linked.Broadcast && !linked.Megagroup
	}
	return false
}

// GetLinkedDiscussionChannel projects a private discussion peer through the
// viewer's active membership in the source broadcast channel. This is a
// peer-discovery boundary only; it never creates discussion-group membership.
func (s *ChannelStore) GetLinkedDiscussionChannel(ctx context.Context, viewerUserID, sourceChannelID int64) (domain.ChannelView, error) {
	source, _, err := s.getChannelForMember(ctx, s.db, viewerUserID, sourceChannelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if !source.Broadcast || source.LinkedChatID == 0 {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	linked, err := s.channelByID(ctx, s.db, source.LinkedChatID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if !linked.Megagroup || linked.Broadcast {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	self, guest, guestErr := s.getLinkedDiscussionGuest(ctx, s.db, viewerUserID, linked)
	if guestErr != nil {
		return domain.ChannelView{}, guestErr
	}
	if !guest {
		if member, memberErr := s.getChannelMember(ctx, s.db, linked.ID, viewerUserID); memberErr == nil {
			self = member
		} else {
			return domain.ChannelView{}, memberErr
		}
	}
	return domain.ChannelView{Channel: linked, Self: self}, nil
}

// ResolveChannel is the lightweight version of GetChannel: it only does the access check and returns Channel (with access_hash) + Self,
// skipping the 3 extra PG queries for dialog top message / read state / boost sum. Used by pure-resolution paths like inputPeerFor that only need
// access_hash / channel flags—which previously paid the full 4-query projection just to get an access_hash.
// Access semantics are exactly the same as GetChannel (private non-member ErrChannelPrivate, public preview member).
func (s *ChannelStore) ResolveChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error) {
	channel, member, preview, err := s.getChannelForViewer(ctx, s.db, viewerUserID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	view := domain.ChannelView{Channel: channel, Self: member}
	if preview {
		// previewChannelDialog is a pure in-memory construction (no extra query), keeping the Dialog consistent with GetChannel's preview state.
		view.Dialog = previewChannelDialog(viewerUserID, channel, member)
	}
	return view, nil
}

// AuthoritativeResolveChannelCache declares that ResolveChannel is already
// protected by ChannelRowCache + ChannelMemberCache. Both consume exact
// channel_base/channel_member invalidations, reject stale in-flight writes by
// epoch, and flush after listener reconnect. The app layer must therefore not
// place a second read_model_versions gate in front of this store path.
func (*ChannelStore) AuthoritativeResolveChannelCache() {}

func (s *ChannelStore) GetChannels(ctx context.Context, viewerUserID int64, channelIDs []int64) ([]domain.ChannelView, error) {
	if viewerUserID == 0 || len(channelIDs) == 0 {
		return nil, nil
	}
	ids := uniqueNonZeroInt64s(channelIDs...)
	if len(ids) == 0 {
		return nil, nil
	}
	views := make(map[int64]domain.ChannelView, len(ids))
	previewMembers := make(map[int64]domain.ChannelMember)
	activeIDs := make([]int64, 0, len(ids))

	rows, err := s.db.Query(ctx, `
SELECT `+channelColumns+`,
       m.channel_id, m.user_id, m.inviter_user_id, m.role, m.status, m.joined_at, m.left_at,
       m.admin_rights::text, m.banned_rights::text, m.rank, m.available_min_id, m.available_min_pts,
       m.history_clear_anchor_id, m.history_clear_anchor_date,
       m.read_inbox_max_id, m.read_outbox_max_id, m.unread_mark, m.slowmode_last_send_date
FROM channels c
JOIN channel_members m ON m.channel_id = c.id AND m.user_id = $1
WHERE c.id = ANY($2::bigint[]) AND NOT c.deleted`, viewerUserID, ids)
	if err != nil {
		return nil, fmt.Errorf("list channels for viewer: %w", err)
	}
	for rows.Next() {
		channel, member, err := scanChannelWithMember(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if err := validateChannelMemberVisible(member); err != nil {
			if errors.Is(err, domain.ErrChannelUserBanned) {
				views[channel.ID] = domain.ChannelView{Channel: channel, Self: member, Forbidden: true}
				continue
			}
			if errors.Is(err, domain.ErrChannelPrivate) {
				previewMembers[channel.ID] = member
				continue
			}
			rows.Close()
			return nil, err
		}
		views[channel.ID] = domain.ChannelView{Channel: channel, Self: member}
		activeIDs = append(activeIDs, channel.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list channels for viewer: %w", err)
	}
	rows.Close()

	dialogs, err := s.getChannelDialogs(ctx, s.db, viewerUserID, activeIDs)
	if err != nil {
		return nil, err
	}
	selfBoostsByChannelID, err := s.countActiveUserBoostsForChannels(ctx, s.db, viewerUserID, activeIDs, nowUnix())
	if err != nil {
		return nil, err
	}
	for id, dialog := range dialogs {
		view := views[id]
		view.Dialog = dialog
		view.SelfBoostsApplied = selfBoostsByChannelID[id]
		views[id] = view
	}

	remaining := make([]int64, 0, len(ids)-len(views))
	for _, id := range ids {
		if _, ok := views[id]; ok {
			continue
		}
		remaining = append(remaining, id)
	}
	channels, err := listChannelsByIDs(ctx, s.db, remaining)
	if err != nil {
		return nil, err
	}
	parentIDs := make([]int64, 0, len(channels))
	for _, channel := range channels {
		if channel.Monoforum && channel.LinkedMonoforumID != 0 {
			parentIDs = append(parentIDs, channel.LinkedMonoforumID)
		}
	}
	parents, err := listChannelsByIDs(ctx, s.db, parentIDs)
	if err != nil {
		return nil, err
	}
	parentsByID := make(map[int64]domain.Channel, len(parents))
	for _, parent := range parents {
		parentsByID[parent.ID] = parent
	}
	linkedGuests, err := s.listLinkedDiscussionGuests(ctx, s.db, viewerUserID, remaining)
	if err != nil {
		return nil, err
	}
	publicUsernameIDs, err := activeCollectibleUsernamePeerIDs(ctx, s.db, peerUsernameTypeChannel, remaining)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		if member, ok := linkedGuests[channel.ID]; ok {
			views[channel.ID] = domain.ChannelView{
				Channel:           channel,
				Self:              member,
				Dialog:            previewChannelDialog(viewerUserID, channel, member),
				SelfBoostsApplied: 0,
			}
			continue
		}
		if member, _, ok, err := s.monoforumAdminPreview(ctx, s.db, viewerUserID, channel); err != nil {
			return nil, err
		} else if ok {
			views[channel.ID] = domain.ChannelView{
				Channel:           channel,
				Self:              member,
				Dialog:            previewChannelDialog(viewerUserID, channel, member),
				SelfBoostsApplied: 0,
			}
			continue
		}
		if channel.Monoforum && channel.LinkedMonoforumID != 0 {
			if parent, ok := parentsByID[channel.LinkedMonoforumID]; ok && parent.BroadcastMessagesAllowed && parent.LinkedMonoforumID == channel.ID {
				member := syntheticMonoforumUserMember(channel, viewerUserID)
				views[channel.ID] = domain.ChannelView{
					Channel: channel,
					Self:    member,
					Dialog:  previewChannelDialog(viewerUserID, channel, member),
				}
				continue
			}
		}
		_, hasActiveUsername := publicUsernameIDs[channel.ID]
		if !publicPreviewableChannel(channel, hasActiveUsername) {
			continue
		}
		existing, found := previewMembers[channel.ID]
		member := publicPreviewMember(channel, viewerUserID, existing, found)
		views[channel.ID] = domain.ChannelView{
			Channel:           channel,
			Self:              member,
			Dialog:            previewChannelDialog(viewerUserID, channel, member),
			SelfBoostsApplied: 0,
		}
	}

	out := make([]domain.ChannelView, 0, len(views))
	for _, id := range ids {
		if view, ok := views[id]; ok {
			out = append(out, view)
		}
	}
	return out, nil
}

func (s *ChannelStore) GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error) {
	if channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	return s.channelByID(ctx, s.db, channelID)
}

func insertChannelTx(ctx context.Context, tx pgx.Tx, ch domain.Channel) error {
	rights, err := marshalJSON(ch.DefaultBannedRights, "{}")
	if err != nil {
		return err
	}
	reactions, err := marshalJSON(ch.ReactionPolicy, "{}")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO channels (
    id, access_hash, creator_user_id, title, about, username, verified, broadcast, megagroup, forum, forum_tabs,
    autotranslation, restricted_sponsored, broadcast_messages_allowed, send_paid_messages_stars,
    noforwards, join_to_send, join_request, signatures, pre_history_hidden, participants_hidden, antispam, linked_chat_id, slowmode_seconds, boosts_unrestrict, default_banned_rights, available_reactions,
    color_set, color, color_background_emoji_id, profile_color_set, profile_color, profile_color_background_emoji_id, emoji_status_document_id, emoji_status_until,
    participants_count, admins_count, kicked_count, banned_count, top_message_id, pinned_message_id, pts, ttl_period, date, deleted, monoforum, linked_monoforum_id, signature_profiles
) VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47,$48)`,
		ch.ID, ch.AccessHash, ch.CreatorUserID, ch.Title, ch.About, ch.Username, ch.Verified, ch.Broadcast, ch.Megagroup, ch.Forum,
		ch.ForumTabs, ch.Autotranslation, ch.RestrictedSponsored, ch.BroadcastMessagesAllowed, ch.SendPaidMessagesStars, ch.NoForwards, ch.JoinToSend, ch.JoinRequest, ch.Signatures, ch.PreHistoryHidden, ch.ParticipantsHidden, ch.AntiSpam, ch.LinkedChatID, ch.SlowmodeSeconds, ch.BoostsUnrestrict, rights, reactions,
		ch.Color.HasColor, ch.Color.Color, ch.Color.BackgroundEmojiID, ch.ProfileColor.HasColor, ch.ProfileColor.Color, ch.ProfileColor.BackgroundEmojiID, ch.EmojiStatus.DocumentID, ch.EmojiStatus.Until,
		ch.ParticipantsCount, ch.AdminsCount,
		ch.KickedCount, ch.BannedCount, ch.TopMessageID, ch.PinnedMessageID, ch.Pts, ch.TTLPeriod, ch.Date, ch.Deleted, ch.Monoforum, ch.LinkedMonoforumID, ch.SignatureProfiles); err != nil {
		return fmt.Errorf("insert channel: %w", err)
	}
	return nil
}

func scanChannel(row rowScanner) (domain.Channel, error) {
	var ch domain.Channel
	var rights, reactionPolicy string
	var wallpaper *string
	if err := row.Scan(channelScanDest(&ch, &rights, &reactionPolicy, &wallpaper)...); err != nil {
		return domain.Channel{}, err
	}
	finishChannelScan(&ch, rights, reactionPolicy, wallpaper)
	return ch, nil
}

func channelScanDest(ch *domain.Channel, rights, reactionPolicy *string, wallpaper **string) []any {
	return []any{
		&ch.ID, &ch.AccessHash, &ch.CreatorUserID, &ch.Title, &ch.About, &ch.Username, &ch.Verified, &ch.Scam, &ch.Fake, &ch.Gigagroup,
		&ch.Broadcast, &ch.Megagroup, &ch.Forum, &ch.ForumTabs, &ch.Autotranslation, &ch.RestrictedSponsored, &ch.BroadcastMessagesAllowed, &ch.SendPaidMessagesStars, &ch.NoForwards, &ch.JoinToSend, &ch.JoinRequest, &ch.Signatures, &ch.SignatureProfiles, &ch.PreHistoryHidden, &ch.ParticipantsHidden, &ch.AntiSpam, &ch.HasLink, &ch.LinkedChatID, &ch.LinkedCommunityID, &ch.Monoforum, &ch.LinkedMonoforumID, &ch.SlowmodeSeconds, &ch.BoostsUnrestrict, rights,
		reactionPolicy, &ch.Color.HasColor, &ch.Color.Color, &ch.Color.BackgroundEmojiID, &ch.ProfileColor.HasColor, &ch.ProfileColor.Color, &ch.ProfileColor.BackgroundEmojiID, &ch.EmojiStatus.DocumentID, &ch.EmojiStatus.Until,
		wallpaper, &ch.ParticipantsCount, &ch.AdminsCount, &ch.KickedCount, &ch.BannedCount, &ch.TopMessageID,
		&ch.PinnedMessageID, &ch.Pts, &ch.TTLPeriod, &ch.Date, &ch.Deleted,
		&ch.PhotoID, &ch.PhotoDCID, &ch.PhotoStripped,
		&ch.ActiveCallID, &ch.ActiveCallAccessHash, &ch.ActiveCallNotEmpty,
	}
}

func finishChannelScan(ch *domain.Channel, rights, reactionPolicy string, wallpaper *string) {
	_ = json.Unmarshal([]byte(rights), &ch.DefaultBannedRights)
	_ = json.Unmarshal([]byte(reactionPolicy), &ch.ReactionPolicy)
	if wallpaper != nil {
		ch.Wallpaper = decodeJSONPtr[domain.Wallpaper](*wallpaper)
	}
}

func publicPreviewableChannel(channel domain.Channel, hasActiveUsername bool) bool {
	return !channel.Deleted &&
		(channel.Broadcast || channel.Megagroup) &&
		(strings.TrimSpace(channel.Username) != "" || hasActiveUsername)
}

func refreshChannelCountsTx(ctx context.Context, tx pgx.Tx, channel domain.Channel) (domain.Channel, error) {
	var participants, admins, kicked, banned int
	rows, err := tx.Query(ctx, `
SELECT channel_id, user_id, inviter_user_id, role, status, joined_at, left_at, admin_rights::text, banned_rights::text,
       rank, available_min_id, available_min_pts, history_clear_anchor_id, history_clear_anchor_date,
       read_inbox_max_id, read_outbox_max_id, unread_mark, slowmode_last_send_date
FROM channel_members
WHERE channel_id = $1`, channel.ID)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("list channel members for counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		member, err := scanChannelMember(rows)
		if err != nil {
			return domain.Channel{}, err
		}
		if member.Status == domain.ChannelMemberKicked {
			kicked++
		}
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		participants++
		if member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
			admins++
		}
		if !zeroChannelBannedRights(member.BannedRights) {
			banned++
		}
	}
	if err := rows.Err(); err != nil {
		return domain.Channel{}, err
	}
	if _, err := tx.Exec(ctx, `
UPDATE channels
SET participants_count = $2, admins_count = $3, kicked_count = $4, banned_count = $5, updated_at = now()
WHERE id = $1`, channel.ID, participants, admins, kicked, banned); err != nil {
		return domain.Channel{}, fmt.Errorf("refresh channel counts: %w", err)
	}
	channel.ParticipantsCount = participants
	channel.AdminsCount = admins
	channel.KickedCount = kicked
	channel.BannedCount = banned
	return channel, nil
}

func mapKeysInt64(items map[int64]struct{}) []int64 {
	if len(items) == 0 {
		return nil
	}
	out := make([]int64, 0, len(items))
	for id := range items {
		if id != 0 {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func uniqueChannelUserIDs(ids []int64, exclude int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 || id == exclude {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func uniqueNonZeroInt64s(items ...int64) []int64 {
	seen := make(map[int64]struct{}, len(items))
	out := make([]int64, 0, len(items))
	for _, item := range items {
		if item == 0 {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func marshalJSON(v any, empty string) ([]byte, error) {
	if v == nil {
		return []byte(empty), nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return []byte(empty), nil
	}
	return raw, nil
}

func int64s(ids []int64) []int64 {
	return append([]int64(nil), ids...)
}

// nonNullInt64s returns a non-nil copy of []int64, for writing a NOT NULL bigint[] column—a nil slice is
// encoded by pgx as SQL NULL, violating the NOT NULL constraint (the column's DEFAULT '{}' only applies when the column is omitted, not when passed explicitly).
// An empty list is written as '{}' rather than NULL; the read path still normalizes an empty array back to nil, so both stores behave consistently.
func nonNullInt64s(ids []int64) []int64 {
	if len(ids) == 0 {
		return []int64{}
	}
	return append([]int64(nil), ids...)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func decodeJSONPtr[T any](raw string) *T {
	if raw == "" || raw == "{}" || raw == "null" {
		return nil
	}
	var out T
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return &out
}

func randomChannelAccessHash() (int64, error) {
	return randomPositiveInt64()
}

func randomPositiveInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("rand int64: %w", err)
	}
	return int64(binary.LittleEndian.Uint64(b[:]) & ((1 << 63) - 1)), nil
}

func nowUnix() int {
	return int(time.Now().Unix())
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isRetryablePostgresTxError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40P01" || pgErr.Code == "40001"
}

type pgChannelIDAllocator struct {
	db sqlcgen.DBTX
}
