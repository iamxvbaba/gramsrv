package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestChannelReactionsViewerUpdatesMin 回归：channel reaction fan-out 中，
// 非发起者 viewer 收到的 updateMessageReactions 必须是 min 形态（不带发起者的
// chosen/My），否则 TDesktop 会把发起者的 reaction 高亮成接收者自己的。
func TestChannelReactionsViewerUpdatesMin(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	reactions := &domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction:    domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001F44D"},
			Count:       1,
			ChosenOrder: 1, // 发起者视角烘焙的 chosen
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

	// 发起者本人：全量视角，chosen 保留。
	self := findUpdate(r.channelReactionsViewerUpdates(ctx, owner.ID, owner.ID, res, ids))
	if self.Reactions.Min {
		t.Fatal("requester's own update must not be min")
	}
	if _, has := self.Reactions.Results[0].GetChosenOrder(); !has {
		t.Fatal("requester's own update should keep chosen_order")
	}

	// 其他 viewer：min 形态，chosen/My 全剥离（deps.Channels 为 nil 时作者分支也落 min）。
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

	// 原 res 不得被 minify 污染（请求者响应还要用全量视角）。
	if res.Message.Reactions.Results[0].ChosenOrder != 1 || !res.Message.Reactions.Recent[0].My {
		t.Fatalf("minify must not mutate the original result: %+v", res.Message.Reactions)
	}
}
