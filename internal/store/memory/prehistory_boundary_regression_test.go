package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// Regression: available_min_id only ever increases, so a member who joined while history was
// hidden permanently sees an empty channel even after hidden history is disabled, and rejoining
// cannot recover it either (the upsert takes GREATEST).
func TestPreHistoryHiddenBoundaryIsReleasedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	users := NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 21, Phone: "+15550008001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 22, Phone: "+15550008002", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Hidden Group", Megagroup: true,
		MemberUserIDs: []int64{member.ID}, Date: 1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID

	seedBase := int64(6000)
	seed := func(n int) {
		for i := 1; i <= n; i++ {
			seedBase++
			if _, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
				UserID: owner.ID, ChannelID: channelID, RandomID: seedBase, Message: "seed", Date: 1700000000 + i,
			}); err != nil {
				t.Fatalf("seed %d: %v", i, err)
			}
		}
	}
	seed(3)

	// Enable hidden history: new members can only see messages sent after they join.
	hidden, err := store.SetPreHistoryHidden(ctx, owner.ID, channelID, true)
	if err != nil || !hidden.PreHistoryHidden {
		t.Fatalf("enable prehistory hidden = %+v err=%v", hidden, err)
	}
	late, err := users.Create(ctx, domain.User{AccessHash: 23, Phone: "+15550008003", FirstName: "Late"})
	if err != nil {
		t.Fatalf("create late: %v", err)
	}
	if _, err := store.JoinChannel(ctx, channelID, late.ID, 1700000100); err != nil {
		t.Fatalf("late join: %v", err)
	}
	seed(2)
	lateView, err := store.GetChannel(ctx, late.ID, channelID)
	if err != nil {
		t.Fatalf("late view: %v", err)
	}
	if lateView.Self.AvailableMinID <= 0 {
		t.Fatalf("late member available_min_id = %d, want > 0 (hidden history)", lateView.Self.AvailableMinID)
	}
	lateHistory, err := store.ListChannelHistory(ctx, late.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 20})
	if err != nil {
		t.Fatalf("late history: %v", err)
	}
	// The join service message + the 2 seeded messages after the join.
	if len(lateHistory.Messages) != 3 {
		t.Fatalf("late member sees %d messages, want 3 (join service + 2 post-join)", len(lateHistory.Messages))
	}

	// Disable hidden history: the read boundary must be returned, otherwise the member permanently sees an empty channel.
	shown, err := store.SetPreHistoryHidden(ctx, owner.ID, channelID, false)
	if err != nil || shown.PreHistoryHidden {
		t.Fatalf("disable prehistory hidden = %+v err=%v", shown, err)
	}
	lateHistory, err = store.ListChannelHistory(ctx, late.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 20})
	if err != nil {
		t.Fatalf("late history after release: %v", err)
	}
	ownerAll, err := store.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 100})
	if err != nil {
		t.Fatalf("owner history: %v", err)
	}
	if len(lateHistory.Messages) != len(ownerAll.Messages) {
		t.Fatalf("late member sees %d messages after release, want all %d", len(lateHistory.Messages), len(ownerAll.Messages))
	}

	// Rejoining must likewise return the boundary (the upsert takes GREATEST, so without an explicit reset it cannot recover).
	if _, err := store.LeaveChannel(ctx, channelID, late.ID, 1700000200); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := store.JoinChannel(ctx, channelID, late.ID, 1700000300); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	rejoined, err := store.GetChannel(ctx, late.ID, channelID)
	if err != nil {
		t.Fatalf("rejoined view: %v", err)
	}
	if rejoined.Self.AvailableMinID != 0 {
		t.Fatalf("rejoined available_min_id = %d, want 0", rejoined.Self.AvailableMinID)
	}
	rejoinedHistory, err := store.ListChannelHistory(ctx, late.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 20})
	if err != nil {
		t.Fatalf("rejoined history: %v", err)
	}
	ownerAll, err = store.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 100})
	if err != nil {
		t.Fatalf("owner history after rejoin: %v", err)
	}
	if len(rejoinedHistory.Messages) != len(ownerAll.Messages) {
		t.Fatalf("rejoined member sees %d messages, want all %d", len(rejoinedHistory.Messages), len(ownerAll.Messages))
	}

	// Guard rail: the boundary produced by the member's own "clear history" (history_clear_anchor) must not be rewritten by group settings.
	if _, err := store.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID: late.ID, ChannelID: channelID, Date: 1700000400,
	}); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	cleared, err := store.GetChannel(ctx, late.ID, channelID)
	if err != nil {
		t.Fatalf("cleared view: %v", err)
	}
	if cleared.Self.AvailableMinID <= 0 || cleared.Self.HistoryClearAnchorID <= 0 {
		t.Fatalf("cleared member = %+v, want available_min_id>0 with anchor", cleared.Self)
	}
	if _, err := store.SetPreHistoryHidden(ctx, owner.ID, channelID, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if _, err := store.SetPreHistoryHidden(ctx, owner.ID, channelID, false); err != nil {
		t.Fatalf("re-disable: %v", err)
	}
	after, err := store.GetChannel(ctx, late.ID, channelID)
	if err != nil {
		t.Fatalf("after toggle: %v", err)
	}
	if after.Self.AvailableMinID != cleared.Self.AvailableMinID {
		t.Fatalf("clear-history boundary lowered by group toggle: %d -> %d", cleared.Self.AvailableMinID, after.Self.AvailableMinID)
	}
}
