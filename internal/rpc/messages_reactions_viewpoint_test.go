package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestChannelReactionsViewerUpdatesMin regression: in channel reaction fan-out,
// the updateMessageReactions a non-initiator viewer receives must be the min form
// (without the initiator's chosen/My), otherwise TDesktop highlights the
// initiator's reaction as the receiver's own.
func TestChannelReactionsViewerUpdatesMin(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	reactions := &domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction:    domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001F44D"},
			Count:       1,
			ChosenOrder: 1, // chosen baked from the initiator's view
		}},
		Recent: []domain.ChannelMessagePeerReaction{{
			UserID:      owner.ID,
			Reaction:    domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001F44D"},
			My:          true,
			ChosenOrder: 1,
			Date:        100,
		}},
	}
	res := domain.ChannelMessageReactionsResult{
		Channel:   domain.Channel{ID: 9001, Megagroup: true},
		Message:   domain.ChannelMessage{ID: 5, ChannelID: 9001, SenderUserID: friend.ID, Reactions: reactions},
		Reactions: *reactions,
	}
	ids := []int{5}

	findUpdate := func(updates *tg.Updates) *tg.UpdateMessageReactions {
		t.Helper()
		if updates == nil {
			t.Fatal("nil updates")
		}
		for _, u := range updates.Updates {
			if mr, ok := u.(*tg.UpdateMessageReactions); ok {
				return mr
			}
		}
		t.Fatal("no updateMessageReactions")
		return nil
	}

	// The initiator itself: full view, chosen preserved.
	self := findUpdate(r.channelReactionsViewerUpdates(ctx, owner.ID, owner.ID, res, ids))
	if self.Reactions.Min {
		t.Fatal("requester's own update must not be min")
	}
	if _, has := self.Reactions.Results[0].GetChosenOrder(); !has {
		t.Fatal("requester's own update should keep chosen_order")
	}

	// Other viewers: min form, chosen/My fully stripped (when deps.Channels is
	// nil the author branch also falls to min).
	other := findUpdate(r.channelReactionsViewerUpdates(ctx, owner.ID, friend.ID, res, ids))
	if !other.Reactions.Min {
		t.Fatal("other viewer's update must be min")
	}
	if _, has := other.Reactions.Results[0].GetChosenOrder(); has {
		t.Fatal("other viewer's update must not carry requester's chosen_order")
	}
	if recent, has := other.Reactions.GetRecentReactions(); has {
		for _, item := range recent {
			if item.My {
				t.Fatalf("other viewer's recent entry must not be My: %+v", item)
			}
		}
	}
	if count := other.Reactions.Results[0].Count; count != 1 {
		t.Fatalf("min update should keep counts, got %d", count)
	}

	// The original res must not be polluted by minify (the requester's response still needs the full view).
	if res.Message.Reactions.Results[0].ChosenOrder != 1 || !res.Message.Reactions.Recent[0].My {
		t.Fatalf("minify must not mutate the original result: %+v", res.Message.Reactions)
	}
}

// top_msg_id on updateMessageReactions names a forum topic. A reply thread in
// an ordinary group is not a topic: setting it there made clients ask for
// forum topics of a non-forum chat (CHANNEL_FORUM_MISSING).
func TestChannelReactionsTopMsgIDOnlyForForums(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	for _, forum := range []bool{false, true} {
		res := domain.ChannelMessageReactionsResult{
			Channel: domain.Channel{ID: 9002, Megagroup: true, Forum: forum},
			Message: domain.ChannelMessage{
				ID: 7, ChannelID: 9002, SenderUserID: friend.ID,
				ReplyTo: &domain.MessageReply{MessageID: 3, TopMessageID: 3},
			},
		}
		updates := r.channelMessagesReactionsUpdates(ctx, owner.ID, res, []int{7})
		var update *tg.UpdateMessageReactions
		for _, u := range updates.Updates {
			if mr, ok := u.(*tg.UpdateMessageReactions); ok {
				update = mr
			}
		}
		if update == nil {
			t.Fatalf("forum=%v: no updateMessageReactions in %#v", forum, updates.Updates)
		}
		topID, has := update.GetTopMsgID()
		if forum && (!has || topID != 3) {
			t.Fatalf("forum topic reply: top_msg_id = %d (set %v), want 3", topID, has)
		}
		if !forum && has {
			t.Fatalf("ordinary group reply: top_msg_id = %d, want unset", topID)
		}
	}
}
