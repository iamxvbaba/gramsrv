package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func newSuggestedPostMemoryFixture(t *testing.T) (*ChannelStore, domain.Channel, domain.Channel, domain.Peer) {
	t.Helper()
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: 1, Title: "Suggestions", Broadcast: true, Date: 1_700_000_000})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := store.SetPaidMessagesPrice(ctx, 1, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	mono := store.channels[enabled.Channel.LinkedMonoforumID]
	return store, store.channels[created.Channel.ID], mono, domain.Peer{Type: domain.PeerTypeUser, ID: 42}
}

func TestMonoforumManagerRequiresManageDirectMessages(t *testing.T) {
	ctx := context.Background()
	store, parent, mono, subscriber := newSuggestedPostMemoryFixture(t)
	store.mu.Lock()
	store.members[parent.ID][2] = domain.ChannelMember{ChannelID: parent.ID, UserID: 2, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, AdminRights: domain.ChannelAdminRights{PostMessages: true}}
	store.mu.Unlock()
	if _, manager, err := store.ResolveMonoforumSend(ctx, 2, mono.ID); err != nil || manager {
		t.Fatalf("ordinary admin resolved as manager: manager=%v err=%v", manager, err)
	}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: 2, SavedPeer: subscriber, RandomID: 1, Message: "must not send", Date: 1_700_000_010}); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("ordinary admin send err=%v, want admin required", err)
	}
	fromSubscriber, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: subscriber, RandomID: 10, Message: "private", Date: 1_700_000_010})
	if err != nil {
		t.Fatal(err)
	}
	if containsInt64(fromSubscriber.Recipients, 2) {
		t.Fatalf("ordinary admin leaked into recipients: %v", fromSubscriber.Recipients)
	}
	dialogs, err := store.ListChannelDialogs(ctx, 2, domain.DialogFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, dialog := range dialogs.Dialogs {
		if dialog.Peer.ID == mono.ID {
			t.Fatalf("ordinary admin received monoforum dialog")
		}
	}
	store.mu.Lock()
	member := store.members[parent.ID][2]
	member.AdminRights.ManageDirectMessages = true
	store.members[parent.ID][2] = member
	store.mu.Unlock()
	if _, manager, err := store.ResolveMonoforumSend(ctx, 2, mono.ID); err != nil || !manager {
		t.Fatalf("DM manager not resolved: manager=%v err=%v", manager, err)
	}
	dialogs, err = store.ListChannelDialogs(ctx, 2, domain.DialogFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	foundMono := false
	for _, dialog := range dialogs.Dialogs {
		foundMono = foundMono || dialog.Peer.ID == mono.ID
	}
	if !foundMono {
		t.Fatalf("DM manager missing monoforum dialog")
	}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: 2, SavedPeer: subscriber, RandomID: 2, Message: "allowed", Date: 1_700_000_011}); err != nil {
		t.Fatalf("DM manager send: %v", err)
	}
}

func TestSuggestedPostStarsApprovalRefundAndSettlement(t *testing.T) {
	ctx := context.Background()
	store, parent, mono, subscriber := newSuggestedPostMemoryFixture(t)
	store.starsBalances[subscriber.ID] = 100

	suggestion, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: subscriber, RandomID: 11, Message: "publish me", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}}, Date: 1_700_000_100})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: suggestion.Message.ID, Date: 1_700_000_200})
	if err != nil {
		t.Fatal(err)
	}
	if approved.State != domain.SuggestedPostStatePublished || approved.OriginalEvent.Type != domain.ChannelUpdateEditMessage || approved.ServiceMessage.Action == nil || approved.ServiceMessage.Action.Type != domain.ChannelActionSuggestedPostApproval || approved.Published == nil {
		t.Fatalf("approval result=%+v", approved)
	}
	if approved.OriginalMessage.SuggestedPost.ScheduleDate != 1_700_000_200 || approved.ServiceMessage.Action.SuggestedPostScheduleDate != 1_700_000_200 {
		t.Fatalf("immediate approval dates original/action=%d/%d, want commit date", approved.OriginalMessage.SuggestedPost.ScheduleDate, approved.ServiceMessage.Action.SuggestedPostScheduleDate)
	}
	if store.starsBalances[subscriber.ID] != 90 || store.channelStarsBalances[parent.ID] != 0 {
		t.Fatalf("escrow/channel balances=%d/%d, want 90/0", store.starsBalances[subscriber.ID], store.channelStarsBalances[parent.ID])
	}
	duplicate, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: suggestion.Message.ID, Date: 1_700_000_201})
	if err != nil || !duplicate.Duplicate || store.starsBalances[subscriber.ID] != 90 {
		t.Fatalf("duplicate=%+v err=%v balance=%d", duplicate, err, store.starsBalances[subscriber.ID])
	}
	if duplicate.OriginalMessage.SuggestedPost.ScheduleDate != 1_700_000_200 || duplicate.ServiceMessage.Action.SuggestedPostScheduleDate != 1_700_000_200 {
		t.Fatalf("duplicate changed immediate approval date: %+v", duplicate)
	}
	store.mu.Lock()
	for i := range store.messages[parent.ID] {
		if store.messages[parent.ID][i].ID == approved.Published.Message.ID {
			store.messages[parent.ID][i].Deleted = true
		}
	}
	store.mu.Unlock()
	lifecycle, err := store.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_000_300, Limit: 10})
	if err != nil || len(lifecycle) != 1 || lifecycle[0].State != domain.SuggestedPostStateRefunded || lifecycle[0].ServiceMessage.Action.Type != domain.ChannelActionSuggestedPostRefund {
		t.Fatalf("refund lifecycle=%+v err=%v", lifecycle, err)
	}
	if store.starsBalances[subscriber.ID] != 100 || store.channelStarsBalances[parent.ID] != 0 {
		t.Fatalf("refund balances=%d/%d, want 100/0", store.starsBalances[subscriber.ID], store.channelStarsBalances[parent.ID])
	}

	second, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: subscriber, RandomID: 12, Message: "settle me", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 20}}, Date: 1_700_000_400})
	if err != nil {
		t.Fatal(err)
	}
	settling, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: second.Message.ID, Date: 1_700_000_500})
	if err != nil || settling.State != domain.SuggestedPostStatePublished {
		t.Fatalf("second approval=%+v err=%v", settling, err)
	}
	lifecycle, err = store.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_000_500 + suggestedPostSettlementAge, Limit: 10})
	if err != nil || len(lifecycle) != 1 || lifecycle[0].State != domain.SuggestedPostStateCompleted || lifecycle[0].ServiceMessage.Action.Type != domain.ChannelActionSuggestedPostSuccess {
		t.Fatalf("success lifecycle=%+v err=%v", lifecycle, err)
	}
	if store.starsBalances[subscriber.ID] != 80 || store.channelStarsBalances[parent.ID] != 17 {
		t.Fatalf("settled balances=%d/%d, want 80/17", store.starsBalances[subscriber.ID], store.channelStarsBalances[parent.ID])
	}
}

func TestSuggestedPostLowBalanceRetryScheduleAndRoleMatrix(t *testing.T) {
	ctx := context.Background()
	store, parent, mono, subscriber := newSuggestedPostMemoryFixture(t)
	store.starsBalances[subscriber.ID] = 5
	suggestion, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: subscriber, RandomID: 21, Message: "later", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}}, Date: 1_700_001_000})
	if err != nil {
		t.Fatal(err)
	}
	low, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: suggestion.Message.ID, ScheduleDate: 1_700_001_400, Date: 1_700_001_000})
	if err != nil || low.State != domain.SuggestedPostStateBalanceLow || low.ServiceMessage.Action == nil || !low.ServiceMessage.Action.SuggestedPostBalanceTooLow {
		t.Fatalf("low=%+v err=%v", low, err)
	}
	again, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: suggestion.Message.ID, ScheduleDate: 1_700_001_400, Date: 1_700_001_001})
	if err != nil || !again.Duplicate {
		t.Fatalf("low retry=%+v err=%v", again, err)
	}
	store.starsBalances[subscriber.ID] = 20
	accepted, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: suggestion.Message.ID, ScheduleDate: 1_700_001_400, Date: 1_700_001_050})
	if err != nil || accepted.State != domain.SuggestedPostStateScheduled || accepted.Published != nil {
		t.Fatalf("scheduled=%+v err=%v", accepted, err)
	}
	due, err := store.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_001_400, Limit: 10})
	if err != nil || len(due) != 1 || due[0].Published == nil || due[0].State != domain.SuggestedPostStatePublished {
		t.Fatalf("due=%+v err=%v", due, err)
	}

	store.mu.Lock()
	store.members[parent.ID][2] = domain.ChannelMember{ChannelID: parent.ID, UserID: 2, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive, AdminRights: domain.ChannelAdminRights{ManageDirectMessages: true}}
	store.mu.Unlock()
	third, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: subscriber, RandomID: 22, Message: "decline only", SuggestedPost: &domain.SuggestedPost{}, Date: 1_700_002_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 2, MonoforumID: mono.ID, MessageID: third.Message.ID, Date: 1_700_002_100}); !errors.Is(err, domain.ErrSuggestedPostApprovalForbidden) {
		t.Fatalf("manager without post right approve err=%v", err)
	}
	rejected, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 2, MonoforumID: mono.ID, MessageID: third.Message.ID, Reject: true, RejectComment: "no", Date: 1_700_002_100})
	if err != nil || rejected.State != domain.SuggestedPostStateRejected {
		t.Fatalf("decline=%+v err=%v", rejected, err)
	}
}

func TestChannelAuthoredSuggestedPostAcceptedBySubscriber(t *testing.T) {
	ctx := context.Background()
	store, _, mono, subscriber := newSuggestedPostMemoryFixture(t)
	fromChannel, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: 1, SavedPeer: subscriber, RandomID: 31, Message: "channel proposal", SuggestedPost: &domain.SuggestedPost{}, Date: 1_700_003_000})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: subscriber.ID, MonoforumID: mono.ID, MessageID: fromChannel.Message.ID, Date: 1_700_003_100})
	if err != nil || result.State != domain.SuggestedPostStateCompleted || result.Published == nil {
		t.Fatalf("subscriber approval=%+v err=%v", result, err)
	}
}

func TestScheduledSuggestedPostDeletionRefundsBeforePublication(t *testing.T) {
	ctx := context.Background()
	store, parent, mono, subscriber := newSuggestedPostMemoryFixture(t)
	store.starsBalances[subscriber.ID] = 30
	suggestion, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: subscriber, RandomID: 41, Message: "cancel scheduled", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}}, Date: 1_700_004_000})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: suggestion.Message.ID, ScheduleDate: 1_700_004_600, Date: 1_700_004_000})
	if err != nil || accepted.State != domain.SuggestedPostStateScheduled {
		t.Fatalf("accepted=%+v err=%v", accepted, err)
	}
	store.mu.Lock()
	for i := range store.messages[mono.ID] {
		if store.messages[mono.ID][i].ID == suggestion.Message.ID {
			store.messages[mono.ID][i].Deleted = true
		}
	}
	store.mu.Unlock()
	resolved, err := store.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_004_100, Limit: 10})
	if err != nil || len(resolved) != 1 || resolved[0].State != domain.SuggestedPostStateRefunded || resolved[0].Published != nil {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	if store.starsBalances[subscriber.ID] != 30 || store.channelStarsBalances[parent.ID] != 0 {
		t.Fatalf("balances=%d/%d", store.starsBalances[subscriber.ID], store.channelStarsBalances[parent.ID])
	}
}

func TestSuggestedPostLifecycleFailsFastOnCorruptAcceptedState(t *testing.T) {
	ctx := context.Background()
	store, _, mono, subscriber := newSuggestedPostMemoryFixture(t)
	suggestion, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: subscriber,
		RandomID: 61, Message: "must fail fast", SuggestedPost: &domain.SuggestedPost{}, Date: 1_700_006_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{
		UserID: 1, MonoforumID: mono.ID, MessageID: suggestion.Message.ID,
		ScheduleDate: 1_700_006_600, Date: 1_700_006_000,
	}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	for i, message := range store.messages[mono.ID] {
		if message.ID == suggestion.Message.ID {
			store.messages[mono.ID] = append(store.messages[mono.ID][:i], store.messages[mono.ID][i+1:]...)
			break
		}
	}
	store.mu.Unlock()
	if _, err := store.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_006_100, Limit: 10}); err == nil {
		t.Fatal("corrupt accepted suggestion was silently skipped")
	}
}

func TestSuggestedPostDeletedAfterMinimumAgeStillSettles(t *testing.T) {
	ctx := context.Background()
	store, parent, mono, subscriber := newSuggestedPostMemoryFixture(t)
	store.starsBalances[subscriber.ID] = 30
	suggestion, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: subscriber, RandomID: 51, Message: "late delete", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}}, Date: 1_700_005_000})
	if err != nil {
		t.Fatal(err)
	}
	approvedAt := 1_700_005_100
	approved, err := store.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: suggestion.Message.ID, Date: approvedAt})
	if err != nil || approved.Published == nil {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	due := approvedAt + suggestedPostSettlementAge
	if _, err := store.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{UserID: 1, ChannelID: parent.ID, IDs: []int{approved.Published.Message.ID}, Date: due + 1}); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: due + 2, Limit: 10})
	if err != nil || len(resolved) != 1 || resolved[0].State != domain.SuggestedPostStateCompleted || resolved[0].ServiceMessage.Action == nil || resolved[0].ServiceMessage.Action.Type != domain.ChannelActionSuggestedPostSuccess {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	if store.starsBalances[subscriber.ID] != 20 || store.channelStarsBalances[parent.ID] != 8 {
		t.Fatalf("balances=%d/%d, want 20/8", store.starsBalances[subscriber.ID], store.channelStarsBalances[parent.ID])
	}
}
