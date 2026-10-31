package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// Regression (postgres wording): disabling "restrict new members from viewing history" must release
// the member read boundary. available_min_id only increases (upsert/batch/clear history are all
// GREATEST), so without an explicit reset, members who joined while history was hidden see an empty
// channel forever, and rejoining cannot recover it.
func TestPreHistoryHiddenBoundaryReleasedOnDisable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 31, Phone: "+1555" + suffix + "01", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 32, Phone: "+1555" + suffix + "02", FirstName: "Member"})
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
		CreatorUserID: owner.ID, Title: "Hidden " + suffix, Megagroup: true,
		MemberUserIDs: []int64{member.ID}, Date: 1700000300,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	seedBase := int64(8000)
	seed := func(n int) {
		for i := 1; i <= n; i++ {
			seedBase++
			if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
				UserID: owner.ID, ChannelID: channelID, RandomID: seedBase, Message: "seed", Date: 1700000300 + i,
			}); err != nil {
				t.Fatalf("seed %d: %v", i, err)
			}
		}
	}
	seed(3)

	hidden, err := channels.SetPreHistoryHidden(ctx, owner.ID, channelID, true)
	if err != nil || !hidden.PreHistoryHidden {
		t.Fatalf("enable prehistory hidden = %+v err=%v", hidden, err)
	}
	late, err := users.Create(ctx, domain.User{AccessHash: 33, Phone: "+1555" + suffix + "03", FirstName: "Late"})
	if err != nil {
		t.Fatalf("create late: %v", err)
	}
	if _, err := channels.JoinChannel(ctx, channelID, late.ID, 1700000400); err != nil {
		t.Fatalf("late join: %v", err)
	}
	seed(2)
	lateView, err := channels.GetChannel(ctx, late.ID, channelID)
	if err != nil {
		t.Fatalf("late view: %v", err)
	}
	if lateView.Self.AvailableMinID <= 0 {
		t.Fatalf("late member available_min_id = %d, want > 0 (hidden history)", lateView.Self.AvailableMinID)
	}
	lateHistory, err := channels.ListChannelHistory(ctx, late.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 20})
	if err != nil {
		t.Fatalf("late history: %v", err)
	}
	if len(lateHistory.Messages) != 3 {
		t.Fatalf("late member sees %d messages, want 3 (join service + 2 post-join)", len(lateHistory.Messages))
	}

	shown, err := channels.SetPreHistoryHidden(ctx, owner.ID, channelID, false)
	if err != nil || shown.PreHistoryHidden {
		t.Fatalf("disable prehistory hidden = %+v err=%v", shown, err)
	}
	lateHistory, err = channels.ListChannelHistory(ctx, late.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 20})
	if err != nil {
		t.Fatalf("late history after release: %v", err)
	}
	ownerAll, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 100})
	if err != nil {
		t.Fatalf("owner history: %v", err)
	}
	if len(lateHistory.Messages) != len(ownerAll.Messages) {
		t.Fatalf("late member sees %d messages after release, want all %d", len(lateHistory.Messages), len(ownerAll.Messages))
	}

	// Rejoining must likewise release the boundary.
	if _, err := channels.LeaveChannel(ctx, channelID, late.ID, 1700000500); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := channels.JoinChannel(ctx, channelID, late.ID, 1700000600); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	rejoined, err := channels.GetChannel(ctx, late.ID, channelID)
	if err != nil {
		t.Fatalf("rejoined view: %v", err)
	}
	if rejoined.Self.AvailableMinID != 0 {
		t.Fatalf("rejoined available_min_id = %d, want 0", rejoined.Self.AvailableMinID)
	}
	rejoinedHistory, err := channels.ListChannelHistory(ctx, late.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 20})
	if err != nil {
		t.Fatalf("rejoined history: %v", err)
	}
	ownerAll, err = channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 100})
	if err != nil {
		t.Fatalf("owner history after rejoin: %v", err)
	}
	if len(rejoinedHistory.Messages) != len(ownerAll.Messages) {
		t.Fatalf("rejoined member sees %d messages, want all %d", len(rejoinedHistory.Messages), len(ownerAll.Messages))
	}

	// Guardrail: a member's own "clear history" boundary must not be rewritten by the group settings.
	if _, err := channels.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID: late.ID, ChannelID: channelID, Date: 1700000700,
	}); err != nil {
		t.Fatalf("clear history: %v", err)
	}
	cleared, err := channels.GetChannel(ctx, late.ID, channelID)
	if err != nil {
		t.Fatalf("cleared view: %v", err)
	}
	if cleared.Self.AvailableMinID <= 0 || cleared.Self.HistoryClearAnchorID <= 0 {
		t.Fatalf("cleared member = %+v, want available_min_id>0 with anchor", cleared.Self)
	}
	if _, err := channels.SetPreHistoryHidden(ctx, owner.ID, channelID, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if _, err := channels.SetPreHistoryHidden(ctx, owner.ID, channelID, false); err != nil {
		t.Fatalf("re-disable: %v", err)
	}
	after, err := channels.GetChannel(ctx, late.ID, channelID)
	if err != nil {
		t.Fatalf("after toggle: %v", err)
	}
	if after.Self.AvailableMinID != cleared.Self.AvailableMinID {
		t.Fatalf("clear-history boundary lowered by group toggle: %d -> %d", cleared.Self.AvailableMinID, after.Self.AvailableMinID)
	}
}
