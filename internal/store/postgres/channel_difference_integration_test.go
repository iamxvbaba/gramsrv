package postgres

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

func TestChannelStoreDifferenceStartsAtMemberAvailableMinPts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 41,
		Phone:      "+1778" + suffix + "01",
		FirstName:  "PtsOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{
		AccessHash: 42,
		Phone:      "+1778" + suffix + "02",
		FirstName:  "PtsMember",
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	joiner, err := users.Create(ctx, domain.User{
		AccessHash: 43,
		Phone:      "+1778" + suffix + "03",
		FirstName:  "PtsJoiner",
	})
	if err != nil {
		t.Fatalf("create joiner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID, joiner.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "PTS Floor " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000350,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	ptsFloor := created.Channel.Pts
	promoted, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		MemberID:  member.ID,
		AdminRights: domain.ChannelAdminRights{
			InviteUsers: true,
		},
		Date: 1700000351,
	})
	if err != nil {
		t.Fatalf("edit admin: %v", err)
	}
	if promoted.Event.Pts != 0 || promoted.Event.PtsCount != 0 || promoted.Channel.Pts != ptsFloor {
		t.Fatalf("promote affected channel pts = event(%d,%d) channel %d, want no pts advance from %d", promoted.Event.Pts, promoted.Event.PtsCount, promoted.Channel.Pts, ptsFloor)
	}
	adminDiff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    member.ID,
		ChannelID: channelID,
		Pts:       ptsFloor,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("difference after promote: %v", err)
	}
	if len(adminDiff.Events) != 0 || adminDiff.Pts != ptsFloor {
		t.Fatalf("difference after promote = %+v, want no durable participant event at pts %d", adminDiff, ptsFloor)
	}
	joined, err := channels.JoinChannel(ctx, channelID, joiner.ID, 1700000352)
	if err != nil {
		t.Fatalf("join channel: %v", err)
	}
	if len(joined.Members) != 1 || joined.Members[0].AvailableMinPts != ptsFloor {
		t.Fatalf("joined members = %+v, want available_min_pts %d", joined.Members, ptsFloor)
	}
	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    joiner.ID,
		ChannelID: channelID,
		Pts:       0,
		Limit:     100,
	})
	if err != nil {
		t.Fatalf("list channel difference: %v", err)
	}
	if diff.Pts != joined.Channel.Pts {
		t.Fatalf("diff pts = %d, want current channel pts %d", diff.Pts, joined.Channel.Pts)
	}
	for _, msg := range diff.NewMessages {
		if msg.Pts <= ptsFloor {
			t.Fatalf("diff leaks pre-join message %+v at or before available_min_pts %d", msg, ptsFloor)
		}
	}
	for _, event := range diff.OtherUpdates {
		if event.Pts <= ptsFloor {
			t.Fatalf("diff leaks pre-join event %+v at or before available_min_pts %d", event, ptsFloor)
		}
	}
}

func TestChannelStorePublicPreviewDifferenceSkipsNonMemberMessages(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 241,
		Phone:      "+1778" + suffix + "41",
		FirstName:  "PreviewDiffOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := users.Create(ctx, domain.User{
		AccessHash: 242,
		Phone:      "+1778" + suffix + "42",
		FirstName:  "PreviewDiffViewer",
	})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, viewer.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Preview Difference " + suffix,
		Broadcast:     true,
		Date:          1700000370,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	if _, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Username:  "preview_diff_" + suffix,
	}); err != nil {
		t.Fatalf("update username: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  1700000371,
		Message:   "public preview difference",
		Date:      1700000371,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    viewer.ID,
		ChannelID: channelID,
		Pts:       created.Event.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list public preview difference: %v", err)
	}
	if !diff.Final || diff.Pts != sent.Event.Pts || len(diff.Events) != 0 || len(diff.NewMessages) != 0 || len(diff.OtherUpdates) != 0 {
		t.Fatalf("preview diff = %+v, want empty public preview difference at current pts", diff)
	}
	if diff.Dialog.UnreadCount != 0 || diff.Dialog.ReadInboxMaxID < sent.Message.ID {
		t.Fatalf("preview diff dialog = %+v, want read-only public preview dialog", diff.Dialog)
	}
}

func TestChannelStoreDifferenceUsesDurableMessageSnapshots(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 39,
		Phone:      "+1778" + suffix + "01",
		FirstName:  "SnapshotOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 40,
		Phone:      "+1778" + suffix + "02",
		FirstName:  "SnapshotFriend",
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
		Title:         "Snapshot Diff " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700000380,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	sendReq := domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  941,
		Message:   "original",
		Date:      1700000381,
	}
	sent, err := channels.SendChannelMessage(ctx, sendReq)
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	fingerprint, err := store.ChannelSendFingerprint(sendReq)
	if err != nil {
		t.Fatalf("fingerprint channel message: %v", err)
	}
	replayReq := domain.ChannelSendReplayRequest{ChannelID: channelID, SenderUserID: owner.ID, RandomID: sent.Message.RandomID, IdempotencyFingerprint: fingerprint}
	type replayState struct {
		pts    int
		events int
	}
	loadReplayState := func() replayState {
		t.Helper()
		var state replayState
		if err := pool.QueryRow(ctx, `SELECT pts FROM channels WHERE id = $1`, channelID).Scan(&state.pts); err != nil {
			t.Fatalf("load channel pts: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_update_events WHERE channel_id = $1`, channelID).Scan(&state.events); err != nil {
			t.Fatalf("count channel events: %v", err)
		}
		return state
	}
	if _, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		ID:        sent.Message.ID,
		Message:   "first edit",
		EditDate:  1700000382,
	}); err != nil {
		t.Fatalf("first edit: %v", err)
	}
	if _, err := channels.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		ID:        sent.Message.ID,
		Message:   "second edit",
		EditDate:  1700000383,
	}); err != nil {
		t.Fatalf("second edit: %v", err)
	}
	beforeReplay := loadReplayState()
	duplicate, found, err := channels.LookupChannelSendReplay(ctx, replayReq)
	if err != nil {
		t.Fatalf("duplicate channel message: %v", err)
	}
	if !found || !duplicate.Duplicate || duplicate.Event.Type != domain.ChannelUpdateNewMessage || duplicate.Message.Body != "second edit" || duplicate.Event.Message.Body != "second edit" || duplicate.Event.Pts != sent.Event.Pts {
		t.Fatalf("duplicate after edit = %+v found=%v, want current snapshot with first-send pts", duplicate, found)
	}
	if after := loadReplayState(); after != beforeReplay {
		t.Fatalf("edit replay mutated channel state = %+v, want %+v", after, beforeReplay)
	}

	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    friend.ID,
		ChannelID: channelID,
		Pts:       created.Event.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list channel difference: %v", err)
	}
	if len(diff.NewMessages) != 1 || diff.NewMessages[0].Body != "original" {
		t.Fatalf("new messages = %+v, want original send snapshot", diff.NewMessages)
	}
	if len(diff.OtherUpdates) != 2 {
		t.Fatalf("other updates = %+v, want two edit snapshots", diff.OtherUpdates)
	}
	if diff.OtherUpdates[0].Message.Body != "first edit" || diff.OtherUpdates[1].Message.Body != "second edit" {
		t.Fatalf("edit snapshots = %q/%q, want first edit/second edit", diff.OtherUpdates[0].Message.Body, diff.OtherUpdates[1].Message.Body)
	}
	deleted, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID: owner.ID, ChannelID: channelID, IDs: []int{sent.Message.ID}, Date: 1700000384,
	})
	if err != nil {
		t.Fatalf("delete channel message: %v", err)
	}
	beforeReplay = loadReplayState()
	duplicate, found, err = channels.LookupChannelSendReplay(ctx, replayReq)
	if err != nil {
		t.Fatalf("duplicate deleted channel message: %v", err)
	}
	if !found || !duplicate.Duplicate || duplicate.Message.Body != "original" || duplicate.Message.Pts != sent.Message.Pts || duplicate.Event.Pts != sent.Event.Pts {
		t.Fatalf("duplicate after delete = %+v found=%v, want immutable first-send snapshot", duplicate, found)
	}
	if duplicate.ReplayDeleteEvent == nil || duplicate.ReplayDeleteEvent.Pts != deleted.Event.Pts || len(duplicate.ReplayDeleteEvent.MessageIDs) != 1 || duplicate.ReplayDeleteEvent.MessageIDs[0] != sent.Message.ID {
		t.Fatalf("duplicate delete receipt = %+v, want durable event %+v", duplicate.ReplayDeleteEvent, deleted.Event)
	}
	if after := loadReplayState(); after != beforeReplay {
		t.Fatalf("delete replay mutated channel state = %+v, want %+v", after, beforeReplay)
	}
}

func TestChannelStoreSendFailureBeforePtsAllocationDoesNotRecordNoopGap(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 41,
		Phone:      "+1888" + suffix + "01",
		FirstName:  "NoopOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	outsider, err := users.Create(ctx, domain.User{
		AccessHash: 42,
		Phone:      "+1888" + suffix + "02",
		FirstName:  "NoopOutsider",
	})
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, outsider.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Noop Gap " + suffix,
		Megagroup:     true,
		Date:          1700000400,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	_, err = channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    outsider.ID,
		ChannelID: channelID,
		RandomID:  991,
		Message:   "outsider should fail",
		Date:      1700000401,
	})
	if err == nil {
		t.Fatal("SendChannelMessage outsider unexpectedly succeeded")
	}

	var gapRows int
	if err := pool.QueryRow(ctx, `
SELECT count(*)::int
FROM channel_update_events
WHERE channel_id = $1 AND pts = 2`, channelID).Scan(&gapRows); err != nil {
		t.Fatalf("count events after failed send: %v", err)
	}
	if gapRows != 0 {
		t.Fatalf("events after failed send = %d, want no pts allocation before member validation", gapRows)
	}

	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  992,
		Message:   "after rollback check",
		Date:      1700000402,
	})
	if err != nil {
		t.Fatalf("send owner after gap: %v", err)
	}
	if sent.Event.Pts != 2 {
		t.Fatalf("next channel pts = %d, want 2 after failed send before pts allocation", sent.Event.Pts)
	}
	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Pts:       1,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list channel difference: %v", err)
	}
	if diff.Pts != 2 || len(diff.Events) != 1 || diff.Events[0].Type != domain.ChannelUpdateNewMessage || diff.Events[0].Pts != 2 {
		t.Fatalf("diff after failed send = %+v, want only message pts=2", diff)
	}
}

func TestChannelDifferenceStopsAtPtsHole(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	owner, err := NewUserStore(pool).Create(ctx, domain.User{
		AccessHash: 49,
		Phone:      "+1888" + suffix + "31",
		FirstName:  "ChannelHoleOwner",
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
		Title:         "Channel Hole " + suffix,
		Megagroup:     true,
		Date:          1700000460,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	first, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  995,
		Message:   "missing event",
		Date:      1700000461,
	})
	if err != nil {
		t.Fatalf("send first: %v", err)
	}
	second, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		RandomID:  996,
		Message:   "after hole",
		Date:      1700000462,
	})
	if err != nil {
		t.Fatalf("send second: %v", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM channel_update_events WHERE channel_id = $1 AND pts = $2", channelID, first.Event.Pts); err != nil {
		t.Fatalf("delete channel event: %v", err)
	}

	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    owner.ID,
		ChannelID: channelID,
		Pts:       created.Channel.Pts,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list difference: %v", err)
	}
	if diff.Pts != created.Channel.Pts || len(diff.Events) != 0 || diff.Final {
		t.Fatalf("diff across hole = %+v, second pts=%d; want stop at previous pts", diff, second.Event.Pts)
	}
}

func TestReserveChannelPtsRollsBackWithTransaction(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	owner, err := NewUserStore(pool).Create(ctx, domain.User{
		AccessHash: 50,
		Phone:      "+1888" + suffix + "41",
		FirstName:  "ChannelPtsRollback",
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
		Title:         "Channel Pts Rollback " + suffix,
		Megagroup:     true,
		Date:          1700000470,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	allocated, err := channels.reserveChannelPts(ctx, tx, channelID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("reserve channel pts: %v", err)
	}
	if allocated != created.Channel.Pts+1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("allocated channel pts = %d, want %d", allocated, created.Channel.Pts+1)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	got, err := channels.MaxChannelPts(ctx, channelID)
	if err != nil {
		t.Fatalf("MaxChannelPts: %v", err)
	}
	if got != created.Channel.Pts {
		t.Fatalf("channel pts after rollback = %d, want unchanged %d", got, created.Channel.Pts)
	}
	batch, err := channels.MaxChannelPtsBatch(ctx, []int64{channelID, -channelID, channelID})
	if err != nil {
		t.Fatalf("MaxChannelPtsBatch: %v", err)
	}
	if len(batch) != 1 || batch[channelID] != created.Channel.Pts {
		t.Fatalf("batch channel pts = %v, want only %d:%d", batch, channelID, created.Channel.Pts)
	}
}

func TestChannelStoreDifferenceTooLongSnapshot(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: 45,
		Phone:      "+1889" + suffix + "01",
		FirstName:  "TooLongOwner",
	})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := users.Create(ctx, domain.User{
		AccessHash: 46,
		Phone:      "+1889" + suffix + "02",
		FirstName:  "TooLongFriend",
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
		Title:         "TooLong Snapshot " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1700000410,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	lastPts := created.Event.Pts
	for i := 0; i < 12; i++ {
		sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    owner.ID,
			ChannelID: channelID,
			RandomID:  int64(10_000 + i),
			Message:   "too long snapshot",
			Date:      1700000411 + i,
		})
		if err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
		lastPts = sent.Event.Pts
	}

	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    friend.ID,
		ChannelID: channelID,
		Pts:       0,
		Limit:     3,
	})
	if err != nil {
		t.Fatalf("list channel difference: %v", err)
	}
	if !diff.TooLong || !diff.Final || diff.Pts != lastPts {
		t.Fatalf("diff = %+v, want tooLong final snapshot at pts %d", diff, lastPts)
	}
	if len(diff.NewMessages) == 0 || len(diff.NewMessages) > domain.MaxChannelDifferenceTooLongMessages {
		t.Fatalf("tooLong snapshot messages = %d, want bounded latest messages", len(diff.NewMessages))
	}
	if diff.Dialog.TopMessageID == 0 || diff.Dialog.UnreadCount == 0 {
		t.Fatalf("tooLong dialog = %+v, want current dialog state", diff.Dialog)
	}
}
