package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// Регрессия: available_min_id 只增不减,导致"隐藏历史期间入群"的成员在频道关闭隐藏历史后
// 仍永久看到空频道,且重新加入也救不回(upsert 取 GREATEST)。
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
	store.AttachDeliveryOutbox(NewDeliveryOutboxStore())
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

	// 开启隐藏历史:新成员只能看到入群之后的消息。
	hidden, err := store.SetPreHistoryHidden(ctx, owner.ID, channelID, true)
	if err != nil || !hidden.PreHistoryHidden {
		t.Fatalf("enable prehistory hidden = %+v err=%v", hidden, err)
	}
	late, err := users.Create(ctx, domain.User{AccessHash: 23, Phone: "+15550008003", FirstName: "Late"})
	if err != nil {
		t.Fatalf("create late: %v", err)
	}
	if _, err := store.JoinChannel(ctx, channelID, late.ID, 1700000100, testPendingJoinEffects); err != nil {
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
	// 入群服务消息 + 入群后的 2 条种子。
	if len(lateHistory.Messages) != 3 {
		t.Fatalf("late member sees %d messages, want 3 (join service + 2 post-join)", len(lateHistory.Messages))
	}

	// 关闭隐藏历史:必须归还读边界,否则该成员永久空频道。
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

	// 重新加入同样要归还边界(upsert 是 GREATEST,不显式归零则救不回)。
	if _, err := store.LeaveChannel(ctx, channelID, late.ID, 1700000200); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := store.JoinChannel(ctx, channelID, late.ID, 1700000300, testPendingJoinEffects); err != nil {
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

	// 护栏:成员自己"清空历史"产生的边界(history_clear_anchor)不得被群设置改写。
	if _, err := store.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID: late.ID, ChannelID: channelID, Date: 1700000400,
	}, testAvailableMinEffects); err != nil {
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
