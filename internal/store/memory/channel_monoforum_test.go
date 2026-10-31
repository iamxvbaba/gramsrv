package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestSetPaidMessagesPriceCreatesAndReusesMonoforum verifies that enabling channel DMs
// (Direct Messages) associates a monoforum virtual channel with the broadcast channel:
// created on first enable, reused with the price synced on the next; a supergroup (not a
// broadcast) gets none. Behaviour matches the postgres implementation.
func TestSetPaidMessagesPriceCreatesAndReusesMonoforum(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	broadcast, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1, Title: "DM Broadcast", Broadcast: true, Date: 1_700_000_900,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	channelID := broadcast.Channel.ID

	updatedResult, err := store.SetPaidMessagesPrice(ctx, 1, channelID, 5, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	updated := updatedResult.Channel
	if updated.LinkedMonoforumID == 0 || !updated.BroadcastMessagesAllowed || updated.SendPaidMessagesStars != 5 {
		t.Fatalf("after enable = %+v, want linked monoforum + broadcast allowed + 5 stars", updated)
	}
	monoID := updated.LinkedMonoforumID

	mono, ok := store.channels[monoID]
	if !ok {
		t.Fatalf("monoforum channel %d not created", monoID)
	}
	if !mono.Monoforum || mono.Broadcast || !mono.Megagroup || mono.LinkedMonoforumID != channelID || mono.SendPaidMessagesStars != 5 || mono.CreatorUserID != 1 {
		t.Fatalf("monoforum channel = %+v, want megagroup monoforum + back-link %d + 5 stars + creator 1", mono, channelID)
	}
	if mono.TopMessageID == 0 || mono.Pts == 0 {
		t.Fatalf("monoforum top/pts = %d/%d, want service top message", mono.TopMessageID, mono.Pts)
	}
	for _, userID := range []int64{1, 42} {
		read, err := store.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
			UserID: userID, ChannelID: monoID, MaxID: mono.TopMessageID, Date: 1_700_000_901,
		})
		if err != nil {
			t.Fatalf("synthetic monoforum read for %d: %v", userID, err)
		}
		if !read.ReadOnly || read.Changed || read.MaxID != mono.TopMessageID {
			t.Fatalf("synthetic monoforum read for %d = %+v, want read-only no-op at %d", userID, read, mono.TopMessageID)
		}
		if _, exists := store.members[monoID][userID]; exists {
			t.Fatalf("synthetic monoforum read persisted member for %d", userID)
		}
		if _, exists := store.dialogs[userID][monoID]; exists {
			t.Fatalf("synthetic monoforum read persisted dialog for %d", userID)
		}
	}
	dialogs, err := store.ListChannelDialogs(ctx, 1, domain.DialogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list dialogs after enable: %v", err)
	}
	var foundMono bool
	for _, dialog := range dialogs.Dialogs {
		if dialog.Peer == (domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}) {
			foundMono = true
			if dialog.TopMessage != mono.TopMessageID || dialog.UnreadCount != 0 || dialog.ReadInboxMaxID < mono.TopMessageID {
				t.Fatalf("monoforum dialog = %+v, want top %d read/no-unread", dialog, mono.TopMessageID)
			}
		}
	}
	if !foundMono {
		t.Fatalf("dialogs after enable do not include admin-visible monoforum %d: %+v", monoID, dialogs.Dialogs)
	}
	msgs := store.messages[monoID]
	if len(msgs) != 1 {
		t.Fatalf("monoforum messages = %d, want exactly one creation service message", len(msgs))
	}
	// The monoforum's service message is the creation message (rendered as
	// "Direct messages were enabled in this channel."), not paid_messages_price (the latter
	// goes only into the parent channel; in a megagroup it would render as the wrong
	// "messages are free" text).
	if action := msgs[0].Action; action == nil || action.Type != domain.ChannelActionCreate {
		t.Fatalf("monoforum service action = %+v, want channel_create", action)
	}
	parentMsgs := store.messages[channelID]
	if len(parentMsgs) != 2 {
		t.Fatalf("parent channel messages = %d, want create + paid_messages_price service", len(parentMsgs))
	}
	if action := parentMsgs[1].Action; action == nil || action.Type != domain.ChannelActionPaidMessagesPrice || !action.BroadcastMessagesAllowed || action.Stars != 5 {
		t.Fatalf("parent service action = %+v, want paid_messages_price allowed stars=5", action)
	}
	mono.TopMessageID = 0
	mono.Pts = 0
	store.channels[monoID] = mono
	store.messages[monoID] = nil
	repairedResult, err := store.SetPaidMessagesPrice(ctx, 1, channelID, 5, true)
	if err != nil {
		t.Fatalf("repair legacy DM: %v", err)
	}
	repaired := repairedResult.Channel
	if repaired.LinkedMonoforumID != monoID {
		t.Fatalf("repair linked = %d, want stable %d", repaired.LinkedMonoforumID, monoID)
	}
	msgs = store.messages[monoID]
	if len(msgs) != 1 {
		t.Fatalf("monoforum messages after legacy repair = %d, want one creation service message", len(msgs))
	}
	if action := msgs[0].Action; action == nil || action.Type != domain.ChannelActionCreate {
		t.Fatalf("monoforum repaired service action = %+v, want channel_create", action)
	}

	// Idempotent: re-enabling does not create a new one, only syncs the price.
	againResult, err := store.SetPaidMessagesPrice(ctx, 1, channelID, 8, true)
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	again := againResult.Channel
	if again.LinkedMonoforumID != monoID {
		t.Fatalf("re-enable linked = %d, want stable %d (no second monoforum)", again.LinkedMonoforumID, monoID)
	}
	if got := store.channels[monoID].SendPaidMessagesStars; got != 8 {
		t.Fatalf("monoforum price = %d, want synced 8", got)
	}
	// A price change goes only into the parent broadcast channel; the monoforum still has only the one creation message.
	if msgs = store.messages[monoID]; len(msgs) != 1 {
		t.Fatalf("monoforum messages after price update = %d, want still one (price change goes to parent only)", len(msgs))
	}
	if pm := store.messages[channelID]; len(pm) == 0 || pm[len(pm)-1].Action == nil || pm[len(pm)-1].Action.Type != domain.ChannelActionPaidMessagesPrice || !pm[len(pm)-1].Action.BroadcastMessagesAllowed || pm[len(pm)-1].Action.Stars != 8 {
		t.Fatalf("parent latest action = %+v, want paid_messages_price allowed stars=8", store.messages[channelID])
	}
	disabledResult, err := store.SetPaidMessagesPrice(ctx, 1, channelID, 0, false)
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
	// Disabling goes only into the parent broadcast channel (+ the monoforumDisabled state shows the disabled footer); the monoforum still has only the one creation message.
	if msgs = store.messages[monoID]; len(msgs) != 1 {
		t.Fatalf("monoforum messages after disable = %d, want still one (disable goes to parent only)", len(msgs))
	}
	if pm := store.messages[channelID]; len(pm) == 0 || pm[len(pm)-1].Action == nil || pm[len(pm)-1].Action.Type != domain.ChannelActionPaidMessagesPrice || pm[len(pm)-1].Action.BroadcastMessagesAllowed || pm[len(pm)-1].Action.Stars != 0 {
		t.Fatalf("parent latest action = %+v, want paid_messages_price disabled stars=0", store.messages[channelID])
	}

	// A supergroup (not a broadcast) gets no monoforum even with broadcastMessagesAllowed=true: DMs are exclusive to broadcast channels.
	group, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1, Title: "SG", Megagroup: true, Date: 1_700_000_901,
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	sgResult, err := store.SetPaidMessagesPrice(ctx, 1, group.Channel.ID, 3, true)
	if err != nil {
		t.Fatalf("group paid: %v", err)
	}
	sg := sgResult.Channel
	if sg.LinkedMonoforumID != 0 || sg.BroadcastMessagesAllowed {
		t.Fatalf("megagroup = linked %d broadcastAllowed %v, want no monoforum", sg.LinkedMonoforumID, sg.BroadcastMessagesAllowed)
	}
}

// TestGetChannelDialogsCoDeliversMonoforumParent locks the "co-delivery" invariant for
// Direct-Messages rendering: when an admin fetches a monoforum DM dialog by id, the
// response must also carry the parent broadcast channel (as an attached chats[] entry, not
// an extra dialog), so the client can resolve linked_monoforum_id and derive
// MonoforumAdmin; a missing parent object makes the mono degrade into an ordinary
// megagroup. A non-admin gets neither the monoforum nor a leaked parent channel.
func TestGetChannelDialogsCoDeliversMonoforumParent(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	broadcast, err := store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: 1, Title: "DM Broadcast", Broadcast: true, Date: 1_700_000_900})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	parentID := broadcast.Channel.ID
	enabled, err := store.SetPaidMessagesPrice(ctx, 1, parentID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}

	list, err := store.GetChannelDialogs(ctx, 1, []int64{monoID})
	if err != nil {
		t.Fatalf("get channel dialogs: %v", err)
	}
	var hasMono, hasParent bool
	for _, ch := range list.Channels {
		switch ch.ID {
		case monoID:
			hasMono = true
		case parentID:
			hasParent = true
		}
	}
	if !hasMono {
		t.Fatalf("GetChannelDialogs([mono]) channels missing mono %d: %+v", monoID, list.Channels)
	}
	if !hasParent {
		t.Fatalf("GetChannelDialogs([mono]) did NOT co-deliver parent broadcast %d (TDesktop can't derive MonoforumAdmin): %+v", parentID, list.Channels)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].Peer.ID != monoID {
		t.Fatalf("dialogs = %+v, want exactly the mono dialog (parent only as chats[])", list.Dialogs)
	}

	// A non-admin (not a member of the parent channel) does not get the monoforum and is not leaked the parent channel.
	deniedList, err := store.GetChannelDialogs(ctx, 999, []int64{monoID})
	if err != nil {
		t.Fatalf("get channel dialogs (non-admin): %v", err)
	}
	for _, ch := range deniedList.Channels {
		if ch.ID == monoID || ch.ID == parentID {
			t.Fatalf("non-admin leaked channel %d: %+v", ch.ID, deniedList.Channels)
		}
	}
}
