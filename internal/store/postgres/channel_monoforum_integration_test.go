package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelStoreEnablingDirectMessagesCreatesMonoforum regression for migration 0020 + monoforum lifecycle:
// enabling channel Direct Messages creates/links a monoforum virtual channel for a parent broadcast within the transaction,
// and the new columns (monoforum/linked_monoforum_id) round-trip correctly through the shared channelColumns + scan; re-enabling reuses it and
// syncs the price; a non-broadcast does not create one. Gated on TELESRV_TEST_POSTGRES_DSN.
func TestChannelStoreEnablingDirectMessagesCreatesMonoforum(t *testing.T) {
	pool := testPool(t) // an unset DSN makes it t.Skip
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 81, Phone: "+1779" + suffix + "31", FirstName: "MonoOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Mono Broadcast " + suffix, Broadcast: true, Date: 1700000900,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelIDs = append(channelIDs, created.Channel.ID)

	updatedResult, err := channels.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 5, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	updated := updatedResult.Channel
	if updated.LinkedMonoforumID == 0 || !updated.BroadcastMessagesAllowed || updated.SendPaidMessagesStars != 5 {
		t.Fatalf("after enable = %+v, want linked monoforum + broadcast allowed + 5 stars", updated)
	}
	monoID := updated.LinkedMonoforumID
	channelIDs = append(channelIDs, monoID)

	// The parent channel's new columns are read back via scan (the scanChannelWithMember path).
	view, err := channels.GetChannel(ctx, owner.ID, created.Channel.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if view.Channel.LinkedMonoforumID != monoID || view.Channel.Monoforum {
		t.Fatalf("parent view = monoforum %v linked %d, want not-monoforum linked %d", view.Channel.Monoforum, view.Channel.LinkedMonoforumID, monoID)
	}

	// monoforum row: monoforum=true, reverse-links the parent channel, price, creator.
	var mfMono, mfBroadcast, mfMegagroup bool
	var mfLinked, mfStars, mfCreator int64
	var mfTop, mfPts int
	if err := pool.QueryRow(ctx, `SELECT monoforum, broadcast, megagroup, linked_monoforum_id, send_paid_messages_stars, creator_user_id, top_message_id, pts FROM channels WHERE id = $1`, monoID).Scan(&mfMono, &mfBroadcast, &mfMegagroup, &mfLinked, &mfStars, &mfCreator, &mfTop, &mfPts); err != nil {
		t.Fatalf("read monoforum row: %v", err)
	}
	if !mfMono || mfBroadcast || !mfMegagroup || mfLinked != created.Channel.ID || mfStars != 5 || mfCreator != owner.ID {
		t.Fatalf("monoforum row = mono %v broadcast %v megagroup %v linked %d stars %d creator %d, want true/false/true/%d/5/%d", mfMono, mfBroadcast, mfMegagroup, mfLinked, mfStars, mfCreator, created.Channel.ID, owner.ID)
	}
	if mfTop == 0 || mfPts == 0 {
		t.Fatalf("monoforum top/pts = %d/%d, want paid-messages service top", mfTop, mfPts)
	}
	read, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID: owner.ID, ChannelID: monoID, MaxID: mfTop, Date: 1700000901,
	})
	if err != nil {
		t.Fatalf("read synthetic monoforum history: %v", err)
	}
	if !read.ReadOnly || read.Changed || read.MaxID != mfTop {
		t.Fatalf("synthetic monoforum read = %+v, want read-only no-op at %d", read, mfTop)
	}
	var memberExists, dialogExists bool
	if err := pool.QueryRow(ctx, `
SELECT EXISTS (
           SELECT 1 FROM channel_members WHERE channel_id=$1 AND user_id=$2
       ),
       EXISTS (
           SELECT 1 FROM channel_dialogs WHERE channel_id=$1 AND user_id=$2
       )`, monoID, owner.ID).Scan(&memberExists, &dialogExists); err != nil {
		t.Fatalf("check synthetic monoforum read footprint: %v", err)
	}
	if memberExists || dialogExists {
		t.Fatalf("synthetic monoforum read persisted member/dialog = %v/%v", memberExists, dialogExists)
	}
	// Deliver the parent broadcast channel in the same batch (TDesktop uses this to resolve linked_monoforum_id and derive MonoforumAdmin
	// to render the Direct-Messages container): the chats[] of GetChannelDialogs([mono]) must include both the mono and the parent channel.
	coDelivery, err := channels.GetChannelDialogs(ctx, owner.ID, []int64{monoID})
	if err != nil {
		t.Fatalf("get channel dialogs: %v", err)
	}
	var hasMono, hasParent bool
	for _, ch := range coDelivery.Channels {
		switch ch.ID {
		case monoID:
			hasMono = true
		case created.Channel.ID:
			hasParent = true
		}
	}
	if !hasMono || !hasParent {
		t.Fatalf("GetChannelDialogs([mono]) hasMono=%v hasParent=%v, want both (parent co-delivered): %+v", hasMono, hasParent, coDelivery.Channels)
	}
	dialogs, err := channels.ListChannelDialogs(ctx, owner.ID, domain.DialogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list dialogs after enable: %v", err)
	}
	var foundMono bool
	for _, dialog := range dialogs.Dialogs {
		if dialog.Peer == (domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}) {
			foundMono = true
			if dialog.TopMessage != mfTop || dialog.UnreadCount != 0 || dialog.ReadInboxMaxID < mfTop {
				t.Fatalf("monoforum dialog = %+v, want top %d read/no-unread", dialog, mfTop)
			}
		}
	}
	if !foundMono {
		t.Fatalf("dialogs after enable do not include admin-visible monoforum %d: %+v", monoID, dialogs.Dialogs)
	}
	// A monoforum has only one creation service message (TDesktop renders "Direct messages were enabled in this channel.",
	// lng_action_created_monoforum). The paid_messages_price for toggling/price changes goes only into the parent broadcast channel (rendered in broadcast
	// history as "Channel enabled/disabled Direct Messages"), never into the mono—otherwise, since the mono is a megagroup,
	// the same action would render the wrong "messages free / price" text.
	var actionType string
	var monoMsgCount int
	assertMonoSoleCreation := func(label string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM channel_messages WHERE channel_id = $1`, monoID).Scan(&monoMsgCount); err != nil {
			t.Fatalf("count monoforum messages (%s): %v", label, err)
		}
		if monoMsgCount != 1 {
			t.Fatalf("monoforum messages (%s) = %d, want exactly one creation service", label, monoMsgCount)
		}
		if err := pool.QueryRow(ctx, `SELECT action->>'Type' FROM channel_messages WHERE channel_id = $1 ORDER BY id DESC LIMIT 1`, monoID).Scan(&actionType); err != nil {
			t.Fatalf("read monoforum service action (%s): %v", label, err)
		}
		if actionType != string(domain.ChannelActionCreate) {
			t.Fatalf("monoforum service action (%s) = %q, want channel_create", label, actionType)
		}
	}
	assertMonoSoleCreation("after enable")

	var parentActionType string
	var parentActionAllowed bool
	var parentActionStars int64
	readParentAction := func() {
		t.Helper()
		if err := pool.QueryRow(ctx, `
SELECT action->>'Type', (action->>'BroadcastMessagesAllowed')::boolean, (action->>'Stars')::bigint
FROM channel_messages
WHERE channel_id = $1
ORDER BY id DESC
LIMIT 1`, created.Channel.ID).Scan(&parentActionType, &parentActionAllowed, &parentActionStars); err != nil {
			t.Fatalf("read parent paid messages service action: %v", err)
		}
	}
	readParentAction()
	if parentActionType != string(domain.ChannelActionPaidMessagesPrice) || !parentActionAllowed || parentActionStars != 5 {
		t.Fatalf("parent service action = %q/%v/%d, want paid_messages_price/true/5", parentActionType, parentActionAllowed, parentActionStars)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM channel_messages WHERE channel_id = $1`, monoID); err != nil {
		t.Fatalf("delete monoforum service message for legacy simulation: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM channel_update_events WHERE channel_id = $1`, monoID); err != nil {
		t.Fatalf("delete monoforum service event for legacy simulation: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channels SET top_message_id = 0, pts = 0 WHERE id = $1`, monoID); err != nil {
		t.Fatalf("reset monoforum top for legacy simulation: %v", err)
	}
	repairedResult, err := channels.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 5, true)
	if err != nil {
		t.Fatalf("repair legacy DM: %v", err)
	}
	repaired := repairedResult.Channel
	if repaired.LinkedMonoforumID != monoID {
		t.Fatalf("repair linked = %d, want stable %d", repaired.LinkedMonoforumID, monoID)
	}
	// After the repair, the monoforum regains a creation message.
	assertMonoSoleCreation("after legacy repair")

	// Idempotency + price sync: re-enabling creates no second monoforum and changes the price to 8; changes go only to the parent channel.
	againResult, err := channels.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 8, true)
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	again := againResult.Channel
	if again.LinkedMonoforumID != monoID {
		t.Fatalf("re-enable linked = %d, want stable %d (no second monoforum)", again.LinkedMonoforumID, monoID)
	}
	if err := pool.QueryRow(ctx, `SELECT send_paid_messages_stars FROM channels WHERE id = $1`, monoID).Scan(&mfStars); err != nil {
		t.Fatalf("read monoforum price: %v", err)
	}
	if mfStars != 8 {
		t.Fatalf("monoforum price = %d, want synced 8", mfStars)
	}
	assertMonoSoleCreation("after price update")
	readParentAction()
	if parentActionType != string(domain.ChannelActionPaidMessagesPrice) || !parentActionAllowed || parentActionStars != 8 {
		t.Fatalf("parent latest action = %q/%v/%d, want paid_messages_price/true/8", parentActionType, parentActionAllowed, parentActionStars)
	}
	disabledResult, err := channels.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, false)
	if err != nil {
		t.Fatalf("disable DM: %v", err)
	}
	disabled := disabledResult.Channel
	if disabled.LinkedMonoforumID != monoID || disabled.BroadcastMessagesAllowed {
		t.Fatalf("disable linked/allowed = %d/%v, want stable %d and disabled", disabled.LinkedMonoforumID, disabled.BroadcastMessagesAllowed, monoID)
	}
	if disabledResult.ServiceMessage == nil || disabledResult.ServiceMessage.Event.Pts == 0 {
		t.Fatalf("disable service result = %+v, want paid_messages_price event", disabledResult.ServiceMessage)
	}
	// Disabling goes only to the parent channel (+ the monoforumDisabled state); the monoforum still has only the creation message.
	assertMonoSoleCreation("after disable")
	readParentAction()
	if parentActionType != string(domain.ChannelActionPaidMessagesPrice) || parentActionAllowed || parentActionStars != 0 {
		t.Fatalf("parent latest action after disable = %q/%v/%d, want paid_messages_price/false/0", parentActionType, parentActionAllowed, parentActionStars)
	}

	// A supergroup (non-broadcast) enabling paid messages does not create a monoforum.
	group, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Mono SG " + suffix, Megagroup: true, Date: 1700000902,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	channelIDs = append(channelIDs, group.Channel.ID)
	sgResult, err := channels.SetPaidMessagesPrice(ctx, owner.ID, group.Channel.ID, 3, true)
	if err != nil {
		t.Fatalf("group paid: %v", err)
	}
	sg := sgResult.Channel
	if sg.LinkedMonoforumID != 0 || sg.BroadcastMessagesAllowed {
		t.Fatalf("megagroup = linked %d broadcastAllowed %v, want no monoforum", sg.LinkedMonoforumID, sg.BroadcastMessagesAllowed)
	}
}
