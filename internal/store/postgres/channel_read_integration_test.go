package postgres

import (
	"context"
	"reflect"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestChannelStoreReadOutboxDoesNotRegressSenderDialogUnread(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 35,
		Phone:      "+1777" + suffix + "17",
		FirstName:  "ReadOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 36,
		Phone:      "+1777" + suffix + "18",
		FirstName:  "ReadMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Read Outbox " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000340,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	ownerMsg, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9341,
		Message:   "owner message before member reply",
		Date:      1700000341,
	})
	if err != nil {
		t.Fatalf("send owner message: %v", err)
	}
	if _, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MaxID:     ownerMsg.Message.ID,
		Date:      1700000342,
	}); err != nil {
		t.Fatalf("member read owner message: %v", err)
	}
	memberMsg, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		RandomID:  9342,
		Message:   "member reply should stay read for sender",
		Date:      1700000343,
	})
	if err != nil {
		t.Fatalf("send member message: %v", err)
	}

	// H4a：发送不再写全员 dialog 行；sender 自己的 read_inbox 在发送事务内推进
	// channel_members 行，读侧 GREATEST(dialog, member) 派生。
	memberView, err := channels.GetChannelDialogs(ctx, member.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get member dialogs after self send: %v", err)
	}
	if len(memberView.Dialogs) != 1 || memberView.Dialogs[0].ReadInboxMaxID != memberMsg.Message.ID || memberView.Dialogs[0].UnreadCount != 0 {
		t.Fatalf("member derived dialog after self send = %+v, want read %d unread 0", memberView.Dialogs, memberMsg.Message.ID)
	}

	read, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MaxID:     memberMsg.Message.ID,
		Date:      1700000344,
	})
	if err != nil {
		t.Fatalf("owner read member message: %v", err)
	}
	if len(read.OutboxUpdates) != 1 || read.OutboxUpdates[0].UserID != member.ID || read.OutboxUpdates[0].MaxID != memberMsg.Message.ID {
		t.Fatalf("read outbox updates = %+v, want member max id %d", read.OutboxUpdates, memberMsg.Message.ID)
	}
	memberView, err = channels.GetChannelDialogs(ctx, member.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get member dialogs after owner read: %v", err)
	}
	if len(memberView.Dialogs) != 1 || memberView.Dialogs[0].ReadInboxMaxID != memberMsg.Message.ID || memberView.Dialogs[0].UnreadCount != 0 {
		t.Fatalf("member derived dialog after owner read = %+v, want read %d unread 0", memberView.Dialogs, memberMsg.Message.ID)
	}
}

func TestChannelStoreReadHistoryNoopDoesNotRewriteDialogState(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 351,
		Phone:      "+1777" + suffix + "41",
		FirstName:  "ReadNoopOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 352,
		Phone:      "+1777" + suffix + "42",
		FirstName:  "ReadNoopMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Read Noop " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000440,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9441,
		Message:   "read once then repeat",
		Date:      1700000441,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	first, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MaxID:     sent.Message.ID,
		Date:      1700000442,
	})
	if err != nil {
		t.Fatalf("first read history: %v", err)
	}
	if !first.Changed {
		t.Fatalf("first read changed = false, want true")
	}

	stableUpdatedAt := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
UPDATE channel_members
SET updated_at = $3, unread_mark = false
WHERE channel_id = $1 AND user_id = $2`, channelID, member.ID, stableUpdatedAt); err != nil {
		t.Fatalf("stabilize member updated_at: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE channel_dialogs
SET updated_at = $3, unread_mark = false
WHERE channel_id = $1 AND user_id = $2`, channelID, member.ID, stableUpdatedAt); err != nil {
		t.Fatalf("stabilize dialog updated_at: %v", err)
	}

	repeat, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MaxID:     sent.Message.ID,
		Date:      1700000443,
	})
	if err != nil {
		t.Fatalf("repeat read history: %v", err)
	}
	if repeat.Changed {
		t.Fatalf("repeat read changed = true, want false")
	}
	var memberUpdatedAt, dialogUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `
SELECT updated_at
FROM channel_members
WHERE channel_id = $1 AND user_id = $2`, channelID, member.ID).Scan(&memberUpdatedAt); err != nil {
		t.Fatalf("read member updated_at: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT updated_at
FROM channel_dialogs
WHERE channel_id = $1 AND user_id = $2`, channelID, member.ID).Scan(&dialogUpdatedAt); err != nil {
		t.Fatalf("read dialog updated_at: %v", err)
	}
	if !memberUpdatedAt.Equal(stableUpdatedAt) {
		t.Fatalf("member updated_at = %v, want no-op timestamp %v", memberUpdatedAt, stableUpdatedAt)
	}
	if !dialogUpdatedAt.Equal(stableUpdatedAt) {
		t.Fatalf("dialog updated_at = %v, want no-op timestamp %v", dialogUpdatedAt, stableUpdatedAt)
	}
}

func TestChannelStoreChannelUnreadExcludesOwnOutgoing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 39,
		Phone:      "+1777" + suffix + "19",
		FirstName:  "OwnUnreadOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Own Unread " + suffix,
		Megagroup:     true,
		Date:          1700000350,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9351,
		Message:   "own outgoing should not be unread",
		Date:      1700000351,
	})
	if err != nil {
		t.Fatalf("send owner message: %v", err)
	}
	readBeforeOwnMessage := sent.Message.ID - 1
	if _, err := pool.Exec(ctx, `
UPDATE channel_members
SET read_inbox_max_id = $3, unread_mark = false
WHERE channel_id = $1 AND user_id = $2`, channelID, owner.ID, readBeforeOwnMessage); err != nil {
		t.Fatalf("regress owner member read watermark: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE channel_dialogs
SET read_inbox_max_id = $3, unread_count = 0, unread_mark = false
WHERE channel_id = $1 AND user_id = $2`, channelID, owner.ID, readBeforeOwnMessage); err != nil {
		t.Fatalf("regress owner dialog unread: %v", err)
	}

	dialogs, err := channels.GetChannelDialogs(ctx, owner.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get owner channel dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 {
		t.Fatalf("dialogs = %+v, want one dialog", dialogs.Dialogs)
	}
	if dialogs.Dialogs[0].UnreadCount != 0 {
		t.Fatalf("owner dialog unread = %d, want own outgoing excluded", dialogs.Dialogs[0].UnreadCount)
	}
	unreadOnly, err := channels.ListChannelDialogs(ctx, owner.ID, domain.DialogFilter{
		Folder: &domain.DialogFolder{ExcludeRead: true, Groups: true},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("list unread-only channel dialogs: %v", err)
	}
	for _, dialog := range unreadOnly.Dialogs {
		if dialog.Peer.ID == channelID {
			t.Fatalf("unread-only dialogs include own-outgoing-only channel: %+v", unreadOnly.Dialogs)
		}
	}
	if _, err := pool.Exec(ctx, `
UPDATE channel_dialogs
SET unread_count = 99
WHERE channel_id = $1 AND user_id = $2`, channelID, owner.ID); err != nil {
		t.Fatalf("corrupt owner dialog unread before read repair: %v", err)
	}

	read, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MaxID:     sent.Message.ID,
		Date:      1700000352,
	})
	if err != nil {
		t.Fatalf("read owner channel history: %v", err)
	}
	if read.StillUnreadCount != 0 || read.Dialog.UnreadCount != 0 {
		t.Fatalf("read result = %+v, want no unread own outgoing messages", read)
	}
	var storedUnread int
	if err := pool.QueryRow(ctx, `
SELECT unread_count
FROM channel_dialogs
WHERE channel_id = $1 AND user_id = $2`, channelID, owner.ID).Scan(&storedUnread); err != nil {
		t.Fatalf("read stored owner unread: %v", err)
	}
	if storedUnread != 0 {
		t.Fatalf("stored owner unread = %d, want repaired to 0", storedUnread)
	}
}

func TestChannelStoreBroadcastUnreadDerivesDespiteStaleCache(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 421,
		Phone:      "+1777" + suffix + "33",
		FirstName:  "BroadcastUnreadOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 422,
		Phone:      "+1777" + suffix + "34",
		FirstName:  "BroadcastUnreadMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Broadcast Unread " + suffix,
		Broadcast:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000333,
	})
	if err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	channelID = created.Channel.ID
	if _, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MaxID:     created.Message.ID,
		Date:      1700000334,
	}); err != nil {
		t.Fatalf("read initial broadcast service message: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9333,
		Message:   "broadcast unread derives despite stale cache",
		Date:      1700000335,
	})
	if err != nil {
		t.Fatalf("send broadcast message: %v", err)
	}

	var storedUnread int
	if err := pool.QueryRow(ctx, `
SELECT unread_count
FROM channel_dialogs
WHERE channel_id = $1 AND user_id = $2`, channelID, member.ID).Scan(&storedUnread); err != nil {
		t.Fatalf("read stale broadcast dialog cache: %v", err)
	}
	if storedUnread != 0 {
		t.Fatalf("stored broadcast unread cache = %d, want no send fanout", storedUnread)
	}

	list, err := channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list broadcast channel dialogs: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].TopMessage != sent.Message.ID || list.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("broadcast dialogs = %+v, want sent top and dynamic unread=1", list.Dialogs)
	}

	unreadOnly, err := channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{
		Folder: &domain.DialogFolder{ExcludeRead: true, Broadcasts: true},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("list unread-only broadcast dialogs: %v", err)
	}
	if len(unreadOnly.Dialogs) != 1 || unreadOnly.Dialogs[0].Peer.ID != channelID {
		t.Fatalf("unread-only broadcast dialogs = %+v, want stale-cache channel included", unreadOnly.Dialogs)
	}

	view, err := channels.GetChannel(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("get broadcast channel: %v", err)
	}
	if view.Dialog.UnreadCount != 1 || view.Dialog.TopMessageID != sent.Message.ID {
		t.Fatalf("broadcast view dialog = %+v, want dynamic unread=1", view.Dialog)
	}
	dialogs, err := channels.GetChannelDialogs(ctx, member.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get broadcast channel dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("get broadcast dialogs = %+v, want dynamic unread=1", dialogs.Dialogs)
	}
}

func TestChannelStoreLargeMegagroupUnreadDerivesDespiteStaleCache(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 431,
		Phone:      "+1777" + suffix + "35",
		FirstName:  "LargeUnreadOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 432,
		Phone:      "+1777" + suffix + "36",
		FirstName:  "LargeUnreadMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Large Unread " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000336,
	})
	if err != nil {
		t.Fatalf("create large megagroup: %v", err)
	}
	channelID = created.Channel.ID
	if _, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MaxID:     created.Message.ID,
		Date:      1700000337,
	}); err != nil {
		t.Fatalf("read initial large service message: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE channels
SET participants_count = $2
WHERE id = $1`, channelID, domain.MaxSynchronousChannelDialogFanout+1); err != nil {
		t.Fatalf("mark megagroup as over synchronous fanout threshold: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9336,
		Message:   "large megagroup unread derives despite stale cache",
		Date:      1700000338,
	})
	if err != nil {
		t.Fatalf("send large megagroup message: %v", err)
	}

	var storedTop, storedUnread int
	if err := pool.QueryRow(ctx, `
SELECT top_message_id, unread_count
FROM channel_dialogs
WHERE channel_id = $1 AND user_id = $2`, channelID, member.ID).Scan(&storedTop, &storedUnread); err != nil {
		t.Fatalf("read stale large dialog cache: %v", err)
	}
	if storedTop == sent.Message.ID || storedUnread != 0 {
		t.Fatalf("stored large dialog cache top=%d unread=%d, want stale top and unread=0", storedTop, storedUnread)
	}

	list, err := channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list large channel dialogs: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].TopMessage != sent.Message.ID || list.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("large dialogs = %+v, want sent top and dynamic unread=1", list.Dialogs)
	}
	ownerView, err := channels.GetChannel(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("get large channel for sender: %v", err)
	}
	if ownerView.Dialog.UnreadCount != 0 {
		t.Fatalf("large sender dialog = %+v, want own outgoing excluded from dynamic unread", ownerView.Dialog)
	}

	unreadOnly, err := channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{
		Folder: &domain.DialogFolder{ExcludeRead: true, Groups: true},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("list unread-only large dialogs: %v", err)
	}
	if len(unreadOnly.Dialogs) != 1 || unreadOnly.Dialogs[0].Peer.ID != channelID {
		t.Fatalf("unread-only large dialogs = %+v, want stale-cache channel included", unreadOnly.Dialogs)
	}

	view, err := channels.GetChannel(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("get large channel: %v", err)
	}
	if view.Dialog.UnreadCount != 1 || view.Dialog.TopMessageID != sent.Message.ID {
		t.Fatalf("large view dialog = %+v, want dynamic unread=1", view.Dialog)
	}
	dialogs, err := channels.GetChannelDialogs(ctx, member.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get large channel dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("get large dialogs = %+v, want dynamic unread=1", dialogs.Dialogs)
	}

	cleared, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MaxID:     sent.Message.ID,
		Date:      1700000339,
	})
	if err != nil {
		t.Fatalf("local clear large history: %v", err)
	}
	if cleared.AvailableMinID != sent.Message.ID {
		t.Fatalf("large local clear available_min_id = %d, want %d", cleared.AvailableMinID, sent.Message.ID)
	}
	afterClear, err := channels.GetChannel(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("get large channel after local clear: %v", err)
	}
	if afterClear.Dialog.TopMessageID != 0 || afterClear.Dialog.UnreadCount != 0 {
		t.Fatalf("large dialog after local clear = %+v, want no visible unread top", afterClear.Dialog)
	}
}

func TestChannelStoreLargeMegagroupUnreadSkipsDeletedHole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 441,
		Phone:      "+1777" + suffix + "37",
		FirstName:  "DeletedHoleOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 442,
		Phone:      "+1777" + suffix + "38",
		FirstName:  "DeletedHoleMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Deleted Hole " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000339,
	})
	if err != nil {
		t.Fatalf("create deleted-hole megagroup: %v", err)
	}
	channelID = created.Channel.ID
	if _, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MaxID:     created.Message.ID,
		Date:      1700000340,
	}); err != nil {
		t.Fatalf("read initial deleted-hole service message: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE channels
SET participants_count = $2
WHERE id = $1`, channelID, domain.MaxSynchronousChannelDialogFanout+1); err != nil {
		t.Fatalf("mark deleted-hole megagroup over threshold: %v", err)
	}
	first, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9339,
		Message:   "deleted unread hole",
		Date:      1700000341,
	})
	if err != nil {
		t.Fatalf("send first large message: %v", err)
	}
	second, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9340,
		Message:   "remaining unread message",
		Date:      1700000342,
	})
	if err != nil {
		t.Fatalf("send second large message: %v", err)
	}
	deleted, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		IDs:       []int{first.Message.ID},
		Date:      1700000343,
	})
	if err != nil {
		t.Fatalf("delete non-top unread message: %v", err)
	}
	if len(deleted.DeletedIDs) != 1 || deleted.DeletedIDs[0] != first.Message.ID {
		t.Fatalf("deleted ids = %+v, want first message only", deleted.DeletedIDs)
	}

	list, err := channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list deleted-hole dialogs: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].TopMessage != second.Message.ID || list.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("deleted-hole dialogs = %+v, want only non-deleted unread top counted", list.Dialogs)
	}
}

func TestChannelStoreSmallMegagroupDeleteDerivesUnread(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 451,
		Phone:      "+1777" + suffix + "39",
		FirstName:  "SmallDeleteOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 452,
		Phone:      "+1777" + suffix + "40",
		FirstName:  "SmallDeleteMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Small Delete Cache " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000344,
	})
	if err != nil {
		t.Fatalf("create small megagroup: %v", err)
	}
	channelID = created.Channel.ID
	if _, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		MaxID:     created.Message.ID,
		Date:      1700000345,
	}); err != nil {
		t.Fatalf("read initial service message: %v", err)
	}
	first, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9341,
		Message:   "remaining first unread",
		Date:      1700000346,
	})
	if err != nil {
		t.Fatalf("send first message: %v", err)
	}
	second, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9342,
		Message:   "deleted middle unread",
		Date:      1700000347,
	})
	if err != nil {
		t.Fatalf("send second message: %v", err)
	}
	third, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9343,
		Message:   "deleted top unread",
		Date:      1700000348,
	})
	if err != nil {
		t.Fatalf("send third message: %v", err)
	}

	// H4a：dialog unread 读时派生，不再依赖 channel_dialogs 缓存列刷新。
	beforeDelete, err := channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs before delete: %v", err)
	}
	if len(beforeDelete.Dialogs) != 1 || beforeDelete.Dialogs[0].TopMessage != third.Message.ID || beforeDelete.Dialogs[0].UnreadCount != 3 {
		t.Fatalf("derived dialog before delete = %+v, want top %d unread 3", beforeDelete.Dialogs, third.Message.ID)
	}

	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		IDs:       []int{second.Message.ID},
		Date:      1700000349,
	}); err != nil {
		t.Fatalf("delete middle unread message: %v", err)
	}
	list, err := channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs after middle delete: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].TopMessage != third.Message.ID || list.Dialogs[0].UnreadCount != 2 {
		t.Fatalf("dialogs after middle delete = %+v, want top third unread 2", list.Dialogs)
	}

	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		IDs:       []int{third.Message.ID},
		Date:      1700000350,
	}); err != nil {
		t.Fatalf("delete top unread message: %v", err)
	}
	list, err = channels.ListChannelDialogs(ctx, member.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs after top delete: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].TopMessage != first.Message.ID || list.Dialogs[0].UnreadCount != 1 {
		t.Fatalf("dialogs after top delete = %+v, want top first unread 1", list.Dialogs)
	}
	if len(list.Messages) != 1 || list.Messages[0].ID != first.Message.ID {
		t.Fatalf("dialog messages after top delete = %+v, want first message", list.Messages)
	}
}

func TestChannelStoreDeleteMessagesMaintainsUnreadMentionIndex(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 453,
		Phone:      "+1777" + suffix + "41",
		FirstName:  "MentionDeleteOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 454,
		Phone:      "+1777" + suffix + "42",
		FirstName:  "MentionDeleteMember",
		Username:   "mention_delete_" + suffix,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Mention Delete " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000351,
	})
	if err != nil {
		t.Fatalf("create mention delete channel: %v", err)
	}
	channelID = created.Channel.ID

	first, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:         owner.ID,
		ChannelID:      channelID,
		RandomID:       9352,
		Message:        "first mention",
		MentionUserIDs: []int64{member.ID},
		Date:           1700000352,
	})
	if err != nil {
		t.Fatalf("send first mention: %v", err)
	}
	second, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:         owner.ID,
		ChannelID:      channelID,
		RandomID:       9353,
		Message:        "second mention",
		MentionUserIDs: []int64{member.ID},
		Date:           1700000353,
	})
	if err != nil {
		t.Fatalf("send second mention: %v", err)
	}
	plain, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  9354,
		Message:   "plain message with stale mention index only",
		Date:      1700000354,
	})
	if err != nil {
		t.Fatalf("send plain message: %v", err)
	}
	requireUnreadMentionRows := func(messageID int, wantMentionRows, wantIndexRows int) {
		t.Helper()
		var mentionRows, indexRows int
		if err := pool.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM channel_unread_mentions
WHERE user_id = $1 AND channel_id = $2 AND message_id = $3`, member.ID, channelID, messageID).Scan(&mentionRows); err != nil {
			t.Fatalf("count mention rows for %d: %v", messageID, err)
		}
		if err := pool.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM channel_unread_mention_index
WHERE channel_id = $1 AND message_id = $2 AND user_id = $3`, channelID, messageID, member.ID).Scan(&indexRows); err != nil {
			t.Fatalf("count mention index rows for %d: %v", messageID, err)
		}
		if mentionRows != wantMentionRows || indexRows != wantIndexRows {
			t.Fatalf("mention rows for msg %d = mention:%d index:%d, want mention:%d index:%d", messageID, mentionRows, indexRows, wantMentionRows, wantIndexRows)
		}
	}
	requireDialogMentions := func(want int) {
		t.Helper()
		var got int
		if err := pool.QueryRow(ctx, `
SELECT unread_mentions_count
FROM channel_dialogs
WHERE user_id = $1 AND channel_id = $2`, member.ID, channelID).Scan(&got); err != nil {
			t.Fatalf("read dialog mention count: %v", err)
		}
		if got != want {
			t.Fatalf("dialog unread_mentions_count = %d, want %d", got, want)
		}
	}

	requireUnreadMentionRows(first.Message.ID, 1, 1)
	requireUnreadMentionRows(second.Message.ID, 1, 1)
	requireDialogMentions(2)

	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		IDs:       []int{first.Message.ID},
		Date:      1700000355,
	}); err != nil {
		t.Fatalf("delete first mention: %v", err)
	}
	requireUnreadMentionRows(first.Message.ID, 0, 0)
	requireUnreadMentionRows(second.Message.ID, 1, 1)
	requireDialogMentions(1)
	mentions, err := channels.ListChannelUnreadMentions(ctx, member.ID, domain.ChannelUnreadMentionsFilter{
		ChannelID: channelID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list unread mentions after first delete: %v", err)
	}
	if len(mentions.Messages) != 1 || mentions.Messages[0].ID != second.Message.ID || !mentions.Messages[0].Mentioned {
		t.Fatalf("unread mentions after first delete = %+v, want only second mentioned message", mentions.Messages)
	}

	if _, err := pool.Exec(ctx, `
INSERT INTO channel_unread_mention_index (channel_id, message_id, user_id)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING`, channelID, plain.Message.ID, member.ID); err != nil {
		t.Fatalf("insert stale mention index: %v", err)
	}
	requireUnreadMentionRows(plain.Message.ID, 0, 1)
	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		IDs:       []int{plain.Message.ID},
		Date:      1700000356,
	}); err != nil {
		t.Fatalf("delete stale-index plain message: %v", err)
	}
	requireUnreadMentionRows(plain.Message.ID, 0, 0)
	requireDialogMentions(1)

	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		IDs:       []int{second.Message.ID},
		Date:      1700000357,
	}); err != nil {
		t.Fatalf("delete second mention: %v", err)
	}
	requireUnreadMentionRows(second.Message.ID, 0, 0)
	requireDialogMentions(0)
}

func TestChannelStoreReadMessageContentsClearsVisibleUnreadReactions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 81,
		Phone:      "+1891" + suffix + "01",
		FirstName:  "ReactionOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 82,
		Phone:      "+1891" + suffix + "02",
		FirstName:  "ReactionFriend",
	})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, friend.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Visible Reaction " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700000900,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  90_001,
		Message:   "react to this",
		Date:      1700000901,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	if _, err := channels.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
		UserID:    friend.ID,
		ChannelID: channelID,
		MessageID: sent.Message.ID,
		Reactions: []domain.MessageReaction{{
			Type:     domain.MessageReactionEmoji,
			Emoticon: "\U0001f525",
		}},
		Date: 1700000902,
	}); err != nil {
		t.Fatalf("set channel reaction: %v", err)
	}
	dialogs, err := channels.GetChannelDialogs(ctx, owner.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get owner dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].UnreadReactions != 1 {
		t.Fatalf("owner dialogs = %+v, want one unread reaction", dialogs.Dialogs)
	}
	unread, err := channels.ListChannelUnreadReactions(ctx, owner.ID, domain.ChannelUnreadReactionsFilter{
		ChannelID: channelID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list unread reactions: %v", err)
	}
	if len(unread.Messages) != 1 || unread.Messages[0].ID != sent.Message.ID || unread.Messages[0].Reactions == nil || !hasUnreadChannelReactionPG(*unread.Messages[0].Reactions) {
		t.Fatalf("unread reactions = %+v, want unread sent message", unread.Messages)
	}

	read, err := channels.ReadChannelMessageContents(ctx, domain.ReadChannelMessageContentsRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		IDs:       []int{sent.Message.ID},
	})
	if err != nil {
		t.Fatalf("read channel message contents: %v", err)
	}
	if !reflect.DeepEqual(read.ClearedUnreadReactionMessageIDs, []int{sent.Message.ID}) {
		t.Fatalf("cleared reaction ids = %+v, want [%d]", read.ClearedUnreadReactionMessageIDs, sent.Message.ID)
	}
	if len(read.Messages) != 1 || read.Messages[0].Reactions == nil || hasUnreadChannelReactionPG(*read.Messages[0].Reactions) {
		t.Fatalf("read messages = %+v, want returned reaction marked read", read.Messages)
	}
	unreadAfter, err := channels.ListChannelUnreadReactions(ctx, owner.ID, domain.ChannelUnreadReactionsFilter{
		ChannelID: channelID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list unread reactions after read contents: %v", err)
	}
	if len(unreadAfter.Messages) != 0 {
		t.Fatalf("unread reactions after read contents = %+v, want empty", unreadAfter.Messages)
	}
	dialogsAfter, err := channels.GetChannelDialogs(ctx, owner.ID, []int64{channelID})
	if err != nil {
		t.Fatalf("get owner dialogs after read contents: %v", err)
	}
	if len(dialogsAfter.Dialogs) != 1 || dialogsAfter.Dialogs[0].UnreadReactions != 0 {
		t.Fatalf("owner dialogs after read contents = %+v, want unread reactions 0", dialogsAfter.Dialogs)
	}
	var stillUnread bool
	if err := pool.QueryRow(ctx, `
SELECT unread
FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reacted_user_id = $3`,
		channelID, sent.Message.ID, friend.ID).Scan(&stillUnread); err != nil {
		t.Fatalf("read reaction row: %v", err)
	}
	if stillUnread {
		t.Fatal("reaction row still unread after read contents")
	}
}

func TestChannelReadOutboxDerivesFromPublicWatermark(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 45, Phone: "+1777" + suffix + "45", FirstName: "WmOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sender, err := users.Create(ctx, domain.User{AccessHash: 46, Phone: "+1777" + suffix + "46", FirstName: "WmSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Watermark " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{sender.ID},
		Date:          1700000400,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    sender.ID,
		ChannelID: created.Channel.ID,
		RandomID:  4601,
		Message:   "read me",
		Date:      1700000401,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	if _, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    owner.ID,
		ChannelID: created.Channel.ID,
		MaxID:     sent.Message.ID,
		Date:      1700000402,
	}); err != nil {
		t.Fatalf("owner read history: %v", err)
	}

	// 模拟实时 fanout 截断：抹掉 sender 的 per-member outbox 水位与 dialog
	// 缓存，read_outbox 必须仍能从 channel 级公共水位派生出来。
	if _, err := pool.Exec(ctx, `UPDATE channel_members SET read_outbox_max_id = 0 WHERE channel_id = $1 AND user_id = $2`, created.Channel.ID, sender.ID); err != nil {
		t.Fatalf("reset sender member outbox: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM channel_dialogs WHERE channel_id = $1 AND user_id = $2`, created.Channel.ID, sender.ID); err != nil {
		t.Fatalf("reset sender dialog cache: %v", err)
	}

	view, err := channels.GetChannel(ctx, sender.ID, created.Channel.ID)
	if err != nil {
		t.Fatalf("sender view: %v", err)
	}
	if view.Dialog.ReadOutboxMaxID != sent.Message.ID {
		t.Fatalf("sender read_outbox = %d, want %d derived from public watermark even when fanout state is missing", view.Dialog.ReadOutboxMaxID, sent.Message.ID)
	}
	// top1 持有者（owner）自己派生用 top2：sender 未读过任何消息时回退 0，
	// 不能把自己的已读水位当成对方的回执。
	ownerView, err := channels.GetChannel(ctx, owner.ID, created.Channel.ID)
	if err != nil {
		t.Fatalf("owner view: %v", err)
	}
	if ownerView.Dialog.ReadOutboxMaxID > ownerView.Dialog.ReadInboxMaxID {
		t.Fatalf("owner read_outbox = %+v, must not exceed peers' actual reads", ownerView.Dialog)
	}
}
