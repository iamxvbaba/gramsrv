package memory

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

func TestChannelCreateCreatesPermanentInviteAndHasLink(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "private group with main link",
		Megagroup:     true,
		Date:          1_700_000_090,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if !created.Channel.HasLink {
		t.Fatalf("created channel HasLink = false, want true")
	}
	view, err := store.GetChannel(ctx, 1, created.Channel.ID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if !view.Channel.HasLink {
		t.Fatalf("stored channel HasLink = false, want true")
	}
	if view.ExportedInvite == nil || !view.ExportedInvite.Permanent || view.ExportedInvite.Revoked || view.ExportedInvite.AdminUserID != 1 {
		t.Fatalf("owner view exported invite = %+v, want creator permanent main link", view.ExportedInvite)
	}
	invites, err := store.ListExportedInvites(ctx, domain.ChannelInviteListRequest{
		UserID:      1,
		ChannelID:   created.Channel.ID,
		AdminUserID: 1,
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list exported invites: %v", err)
	}
	if invites.Count != 1 || len(invites.Invites) != 1 || !invites.Invites[0].Permanent || invites.Invites[0].Revoked {
		t.Fatalf("invites = %+v, want one active permanent main link", invites)
	}
}

func TestChannelViewExportedInviteIsAdminOnly(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "private group with regular member",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_091,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	ownerView, err := store.GetChannel(ctx, 1, created.Channel.ID)
	if err != nil {
		t.Fatalf("get owner channel: %v", err)
	}
	if ownerView.ExportedInvite == nil || !ownerView.ExportedInvite.Permanent {
		t.Fatalf("owner exported invite = %+v, want permanent main link", ownerView.ExportedInvite)
	}
	memberView, err := store.GetChannel(ctx, 2, created.Channel.ID)
	if err != nil {
		t.Fatalf("get member channel: %v", err)
	}
	if memberView.ExportedInvite != nil {
		t.Fatalf("member exported invite = %+v, want nil", memberView.ExportedInvite)
	}
}

func TestChannelRealtimeRecipientsAreCapped(t *testing.T) {
	store := NewChannelStore()
	memberIDs := make([]int64, domain.MaxChannelRealtimeFanout+25)
	for i := range memberIDs {
		memberIDs[i] = int64(10_000 + i)
	}

	created, err := store.CreateChannel(context.Background(), domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "large realtime cap",
		Megagroup:     true,
		MemberUserIDs: memberIDs,
		Date:          1_700_000_100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if got := len(created.Recipients); got != domain.MaxChannelRealtimeFanout {
		t.Fatalf("create recipients = %d, want capped %d", got, domain.MaxChannelRealtimeFanout)
	}

	recipients, err := store.ListActiveChannelMemberIDs(context.Background(), 1, created.Channel.ID, 0)
	if err != nil {
		t.Fatalf("list active members: %v", err)
	}
	if got := len(recipients); got != domain.MaxChannelRealtimeFanout {
		t.Fatalf("listed active members = %d, want capped %d", got, domain.MaxChannelRealtimeFanout)
	}
}

func TestChannelCreatorLeaveTransfersOwner(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "creator transfer",
		Megagroup:     true,
		MemberUserIDs: []int64{2, 3},
		Date:          1_700_000_110,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := store.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		MemberID:  3,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
			AddAdmins:  true,
		},
		Date: 1_700_000_111,
	}); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	future, err := store.FutureCreatorAfterLeave(ctx, created.Channel.ID, 1)
	if err != nil {
		t.Fatalf("future creator: %v", err)
	}
	if future.UserID != 3 {
		t.Fatalf("future creator = %+v, want admin user 3", future)
	}
	left, err := store.LeaveChannel(ctx, created.Channel.ID, 1, 1_700_000_112)
	if err != nil {
		t.Fatalf("creator leave: %v", err)
	}
	if left.Channel.CreatorUserID != 3 || left.Channel.ParticipantsCount != 2 || left.Channel.AdminsCount != 1 {
		t.Fatalf("channel after creator leave = %+v, want owner=3 participants=2 admins=1", left.Channel)
	}
	newOwner, err := store.GetParticipant(ctx, 3, created.Channel.ID, 3)
	if err != nil {
		t.Fatalf("get new owner: %v", err)
	}
	if newOwner.Role != domain.ChannelRoleCreator || newOwner.Status != domain.ChannelMemberActive {
		t.Fatalf("new owner = %+v, want active creator", newOwner)
	}
	oldOwner, err := store.GetParticipant(ctx, 3, created.Channel.ID, 1)
	if err != nil {
		t.Fatalf("get old owner: %v", err)
	}
	if oldOwner.Status != domain.ChannelMemberLeft || oldOwner.Role == domain.ChannelRoleCreator {
		t.Fatalf("old owner = %+v, want left non-creator", oldOwner)
	}
	if _, err := store.JoinChannel(ctx, created.Channel.ID, 1, 1_700_000_113); err != nil {
		t.Fatalf("old owner rejoin: %v", err)
	}
	rejoined, err := store.GetParticipant(ctx, 3, created.Channel.ID, 1)
	if err != nil {
		t.Fatalf("get rejoined old owner: %v", err)
	}
	if rejoined.Role != domain.ChannelRoleMember || rejoined.Status != domain.ChannelMemberActive || rejoined.Rank != "" {
		t.Fatalf("rejoined old owner = %+v, want active plain member", rejoined)
	}
}

func TestChannelCreatorCanEditOwnAdminRights(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "creator self admin",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_114,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	edited, err := store.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		MemberID:  1,
		AdminRights: domain.ChannelAdminRights{
			Anonymous: true,
		},
		Date: 1_700_000_115,
	})
	if err != nil {
		t.Fatalf("creator edits own admin rights: %v", err)
	}
	if edited.Participant.Role != domain.ChannelRoleCreator || !edited.Participant.AdminRights.Anonymous || !edited.Participant.AdminRights.ChangeInfo || !edited.Participant.AdminRights.AddAdmins {
		t.Fatalf("edited creator = %+v, want creator with anonymous plus full creator projection", edited.Participant)
	}
	persisted, err := store.GetParticipant(ctx, 1, created.Channel.ID, 1)
	if err != nil {
		t.Fatalf("get creator participant: %v", err)
	}
	if persisted.Role != domain.ChannelRoleCreator || !persisted.AdminRights.Anonymous || !persisted.AdminRights.ChangeInfo || !persisted.AdminRights.AddAdmins {
		t.Fatalf("persisted creator = %+v, want creator with anonymous plus full creator projection", persisted)
	}

	if _, err := store.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		MemberID:  2,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
			AddAdmins:  true,
		},
		Date: 1_700_000_116,
	}); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	if _, err := store.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    2,
		ChannelID: created.Channel.ID,
		MemberID:  1,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
		},
		Date: 1_700_000_117,
	}); !errors.Is(err, domain.ErrChannelUserCreator) {
		t.Fatalf("admin edits creator err = %v, want ErrChannelUserCreator", err)
	}
}

func TestChannelAdminAndBanDoNotAdvanceChannelPts(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "participant state no pts",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_120,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	ptsFloor := created.Channel.Pts

	promoted, err := store.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    1,
		ChannelID: channelID,
		MemberID:  2,
		AdminRights: domain.ChannelAdminRights{
			InviteUsers: true,
		},
		Date: 1_700_000_121,
	})
	if err != nil {
		t.Fatalf("edit admin: %v", err)
	}
	if promoted.Event.Pts != 0 || promoted.Event.PtsCount != 0 || promoted.Channel.Pts != ptsFloor {
		t.Fatalf("edit admin pts = event(%d,%d) channel %d, want unchanged %d", promoted.Event.Pts, promoted.Event.PtsCount, promoted.Channel.Pts, ptsFloor)
	}

	muted, err := store.EditChannelBanned(ctx, domain.EditChannelBannedRequest{
		UserID:      1,
		ChannelID:   channelID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: 2},
		BannedRights: domain.ChannelBannedRights{
			SendMessages: true,
			UntilDate:    1_700_001_121,
		},
		Date: 1_700_000_122,
	})
	if err != nil {
		t.Fatalf("edit banned mute: %v", err)
	}
	if muted.Event.Pts != 0 || muted.Event.PtsCount != 0 || muted.Channel.Pts != ptsFloor || muted.ServiceEvent.Pts != 0 {
		t.Fatalf("mute pts = event(%d,%d) channel %d, want unchanged %d without service message", muted.Event.Pts, muted.Event.PtsCount, muted.Channel.Pts, ptsFloor)
	}

	kicked, err := store.EditChannelBanned(ctx, domain.EditChannelBannedRequest{
		UserID:      1,
		ChannelID:   channelID,
		Participant: domain.Peer{Type: domain.PeerTypeUser, ID: 2},
		BannedRights: domain.ChannelBannedRights{
			ViewMessages: true,
			UntilDate:    1_700_001_121,
		},
		Date: 1_700_000_123,
	})
	if err != nil {
		t.Fatalf("edit banned kick: %v", err)
	}
	// megagroup 踢人产生可见 "X removed Y" 服务消息并占 channel pts；
	// participant update 本身仍不占 pts。
	if kicked.Event.Pts != 0 || kicked.Event.PtsCount != 0 {
		t.Fatalf("kick participant event = (%d,%d), must stay transient", kicked.Event.Pts, kicked.Event.PtsCount)
	}
	if kicked.ServiceEvent.Pts != ptsFloor+1 || kicked.Message.Action == nil || kicked.Message.Action.Type != domain.ChannelActionChatDelete {
		t.Fatalf("kick service = event %+v message %+v, want ChatDelete service message at pts %d", kicked.ServiceEvent, kicked.Message, ptsFloor+1)
	}
	if kicked.Channel.Pts != ptsFloor+1 {
		t.Fatalf("kick channel pts = %d, want %d", kicked.Channel.Pts, ptsFloor+1)
	}

	diff, err := store.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{
		UserID:    1,
		ChannelID: channelID,
		Pts:       ptsFloor,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list difference: %v", err)
	}
	if len(diff.Events) != 1 || diff.Pts != ptsFloor+1 {
		t.Fatalf("difference after kick = %+v, want only the kick service message at pts %d", diff, ptsFloor+1)
	}
}

func TestChannelStoreGetMessagesNonMemberPublicPreview(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	const owner, outsider int64 = 1, 99

	// 公开广播频道：非成员可读取消息（查看他人资料里的公开「个人频道」时，DrKLO 经
	// channels.getMessages 拉最新一帖依赖此；否则资料页个人频道整块不显示）。
	pub, err := store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "public", Broadcast: true, Date: 1_700_000_000})
	if err != nil {
		t.Fatalf("create public: %v", err)
	}
	if _, err := store.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{UserID: owner, ChannelID: pub.Channel.ID, Username: "pub_preview_mem"}); err != nil {
		t.Fatalf("make public: %v", err)
	}
	sent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: owner, ChannelID: pub.Channel.ID, RandomID: 111, Message: "hello", Date: 1_700_000_001})
	if err != nil {
		t.Fatalf("send public: %v", err)
	}
	got, err := store.GetChannelMessages(ctx, outsider, pub.Channel.ID, []int{sent.Message.ID})
	if err != nil {
		t.Fatalf("non-member getMessages on public channel: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].ID != sent.Message.ID {
		t.Fatalf("non-member should read public channel message, got %+v", got.Messages)
	}

	// 私有广播频道（无 username）：非成员仍被拒（ErrChannelPrivate）。
	priv, err := store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "private", Broadcast: true, Date: 1_700_000_002})
	if err != nil {
		t.Fatalf("create private: %v", err)
	}
	privSent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: owner, ChannelID: priv.Channel.ID, RandomID: 222, Message: "secret", Date: 1_700_000_003})
	if err != nil {
		t.Fatalf("send private: %v", err)
	}
	if _, err := store.GetChannelMessages(ctx, outsider, priv.Channel.ID, []int{privSent.Message.ID}); !errors.Is(err, domain.ErrChannelPrivate) {
		t.Fatalf("non-member getMessages on private channel = %v, want ErrChannelPrivate", err)
	}
}

func TestChannelStoreStoryMessageForwardsPublicOnlyAndDeleteRollback(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	source := domain.Peer{Type: domain.PeerTypeUser, ID: 42}
	media := &domain.MessageMedia{
		Kind: domain.MessageMediaKindStory,
		Story: &domain.MessageStory{
			Peer: source,
			ID:   7,
			Story: &domain.Story{
				Owner: source,
				ID:    7,
			},
		},
	}
	publicCreated, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "public story forwards",
		Broadcast:     true,
		Date:          1_700_000_130,
	})
	if err != nil {
		t.Fatalf("create public channel: %v", err)
	}
	publicChannel, err := store.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    1,
		ChannelID: publicCreated.Channel.ID,
		Username:  "story_forward_memory",
	})
	if err != nil {
		t.Fatalf("make public channel: %v", err)
	}
	privateCreated, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "private story forwards",
		Broadcast:     true,
		Date:          1_700_000_131,
	})
	if err != nil {
		t.Fatalf("create private channel: %v", err)
	}
	publicSent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: publicChannel.ID,
		RandomID:  9141301,
		Media:     media,
		Date:      1_700_000_132,
	})
	if err != nil {
		t.Fatalf("send public story message: %v", err)
	}
	if _, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: privateCreated.Channel.ID,
		RandomID:  9141302,
		Media:     media,
		Date:      1_700_000_133,
	}); err != nil {
		t.Fatalf("send private story message: %v", err)
	}
	list, err := store.ListStoryMessageForwards(ctx, domain.StoryMessageForwardListRequest{
		ViewerUserID: 42,
		Owner:        source,
		StoryID:      7,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list story message forwards: %v", err)
	}
	if list.Count != 1 || len(list.Forwards) != 1 {
		t.Fatalf("story message forwards = %+v, want one public forward", list)
	}
	if got := list.Forwards[0].PublicForward.Message.ChannelID; got != publicChannel.ID {
		t.Fatalf("forward channel = %d, want public channel %d", got, publicChannel.ID)
	}
	if _, err := store.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID:    1,
		ChannelID: publicChannel.ID,
		IDs:       []int{publicSent.Message.ID},
		Date:      1_700_000_134,
	}); err != nil {
		t.Fatalf("delete public story message: %v", err)
	}
	empty, err := store.ListStoryMessageForwards(ctx, domain.StoryMessageForwardListRequest{
		ViewerUserID: 42,
		Owner:        source,
		StoryID:      7,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list story message forwards after delete: %v", err)
	}
	if empty.Count != 0 || len(empty.Forwards) != 0 {
		t.Fatalf("story message forwards after delete = %+v, want empty", empty)
	}
}

func TestPendingJoinRequestsSummaryAndInviteAdmins(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "pending join requests",
		Megagroup:     true,
		MemberUserIDs: []int64{2, 3, 4},
		Date:          1_700_000_150,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	if _, err := store.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    1,
		ChannelID: channelID,
		MemberID:  2,
		AdminRights: domain.ChannelAdminRights{
			InviteUsers: true,
		},
		Date: 1_700_000_151,
	}); err != nil {
		t.Fatalf("promote invite admin: %v", err)
	}
	if _, err := store.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{
		UserID:    1,
		ChannelID: channelID,
		MemberID:  4,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
		},
		Date: 1_700_000_152,
	}); err != nil {
		t.Fatalf("promote change-info admin: %v", err)
	}
	invite, err := store.ExportInvite(ctx, domain.ExportChannelInviteRequest{
		UserID:        1,
		ChannelID:     channelID,
		Title:         "approval",
		RequestNeeded: true,
		Date:          1_700_000_153,
	})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	for i := 0; i < domain.MaxChannelPendingJoinRecentRequesters+2; i++ {
		_, err := store.ImportInvite(ctx, domain.ImportChannelInviteRequest{
			UserID: int64(10 + i),
			Hash:   invite.Invite.Hash,
			Date:   1_700_000_160 + i,
		})
		if !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatalf("import pending %d err = %v, want ErrInviteRequestSent", i, err)
		}
	}
	pending, err := store.PendingJoinRequests(ctx, channelID, 99)
	if err != nil {
		t.Fatalf("pending join requests: %v", err)
	}
	if pending.Count != domain.MaxChannelPendingJoinRecentRequesters+2 || len(pending.RecentRequesters) != domain.MaxChannelPendingJoinRecentRequesters {
		t.Fatalf("pending summary = %+v, want bounded recent with full count", pending)
	}
	if pending.RecentRequesters[0] != 16 || pending.RecentRequesters[len(pending.RecentRequesters)-1] != 12 {
		t.Fatalf("recent requesters = %+v, want newest first", pending.RecentRequesters)
	}
	admins, err := store.ListChannelInviteAdminMemberIDs(ctx, channelID, 0)
	if err != nil {
		t.Fatalf("invite admins: %v", err)
	}
	want := []int64{1, 2, 4}
	if !reflect.DeepEqual(admins, want) {
		t.Fatalf("invite admins = %+v, want %+v", admins, want)
	}
}

func TestCommonChannelsOnlySharedMegagroups(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	first, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "common one",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_170,
	})
	if err != nil {
		t.Fatalf("create first common channel: %v", err)
	}
	second, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "common two",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_171,
	})
	if err != nil {
		t.Fatalf("create second common channel: %v", err)
	}
	if _, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "broadcast excluded",
		Broadcast:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_172,
	}); err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	left, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "left excluded",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_173,
	})
	if err != nil {
		t.Fatalf("create left channel: %v", err)
	}
	if _, err := store.LeaveChannel(ctx, left.Channel.ID, 2, 1_700_000_174); err != nil {
		t.Fatalf("leave channel: %v", err)
	}
	if _, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "not shared",
		Megagroup:     true,
		MemberUserIDs: []int64{3},
		Date:          1_700_000_175,
	}); err != nil {
		t.Fatalf("create non-shared channel: %v", err)
	}

	page, err := store.ListCommonChannels(ctx, domain.CommonChannelsRequest{
		UserID:       1,
		TargetUserID: 2,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list common channels: %v", err)
	}
	if page.Count != 2 || len(page.Channels) != 2 || page.Channels[0].ID != first.Channel.ID || page.Channels[1].ID != second.Channel.ID {
		t.Fatalf("common channels = %+v, want two shared megagroups in id order", page)
	}

	next, err := store.ListCommonChannels(ctx, domain.CommonChannelsRequest{
		UserID:       1,
		TargetUserID: 2,
		MaxID:        first.Channel.ID,
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("list common channels after max id: %v", err)
	}
	if next.Count != 2 || len(next.Channels) != 1 || next.Channels[0].ID != second.Channel.ID {
		t.Fatalf("paged common channels = %+v, want second channel with full count", next)
	}

	countOnly, err := store.ListCommonChannels(ctx, domain.CommonChannelsRequest{
		UserID:       1,
		TargetUserID: 2,
		CountOnly:    true,
	})
	if err != nil {
		t.Fatalf("count common channels: %v", err)
	}
	if countOnly.Count != 2 || len(countOnly.Channels) != 0 {
		t.Fatalf("count-only common channels = %+v, want count without channels", countOnly)
	}
}

func TestLeftChannelsReturnsPagedLeftMemberships(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	older, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "older left",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_180,
	})
	if err != nil {
		t.Fatalf("create older channel: %v", err)
	}
	newer, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "newer left broadcast",
		Broadcast:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_181,
	})
	if err != nil {
		t.Fatalf("create newer channel: %v", err)
	}
	if _, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "active excluded",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_182,
	}); err != nil {
		t.Fatalf("create active channel: %v", err)
	}
	if _, err := store.LeaveChannel(ctx, older.Channel.ID, 2, 1_700_000_183); err != nil {
		t.Fatalf("leave older channel: %v", err)
	}
	if _, err := store.LeaveChannel(ctx, newer.Channel.ID, 2, 1_700_000_184); err != nil {
		t.Fatalf("leave newer channel: %v", err)
	}

	page, err := store.ListLeftChannels(ctx, 2, 0, 1)
	if err != nil {
		t.Fatalf("list left channels: %v", err)
	}
	if page.Count != 2 || len(page.Channels) != 1 || page.Channels[0].Channel.ID != newer.Channel.ID || page.Channels[0].Self.Status != domain.ChannelMemberLeft {
		t.Fatalf("first left page = %+v, want newest left channel and full count", page)
	}
	next, err := store.ListLeftChannels(ctx, 2, 1, 1)
	if err != nil {
		t.Fatalf("list next left channels: %v", err)
	}
	if next.Count != 2 || len(next.Channels) != 1 || next.Channels[0].Channel.ID != older.Channel.ID {
		t.Fatalf("second left page = %+v, want older left channel", next)
	}
	empty, err := store.ListLeftChannels(ctx, 2, 2, 1)
	if err != nil {
		t.Fatalf("list empty left page: %v", err)
	}
	if empty.Count != 2 || len(empty.Channels) != 0 {
		t.Fatalf("empty left page = %+v, want full count and no chats", empty)
	}
	if _, err := store.ListLeftChannels(ctx, 2, domain.MaxLeftChannelsOffset+1, 1); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("huge offset err = %v, want ErrChannelInvalid", err)
	}
}

func TestDiscussionGroupLinksAreBidirectionalAndReplaceOldLinks(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	broadcast, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "broadcast",
		Broadcast:     true,
		Date:          1_700_000_190,
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	firstGroup, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "first group",
		Megagroup:     true,
		Date:          1_700_000_191,
	})
	if err != nil {
		t.Fatalf("create first group: %v", err)
	}
	secondGroup, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "second group",
		Megagroup:     true,
		Date:          1_700_000_192,
	})
	if err != nil {
		t.Fatalf("create second group: %v", err)
	}
	if _, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "broadcast excluded",
		Broadcast:     true,
		Date:          1_700_000_193,
	}); err != nil {
		t.Fatalf("create excluded broadcast: %v", err)
	}

	candidates, err := store.ListDiscussionGroups(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list discussion groups: %v", err)
	}
	if len(candidates) != 2 || candidates[0].ID != secondGroup.Channel.ID || candidates[1].ID != firstGroup.Channel.ID {
		t.Fatalf("discussion candidates = %+v, want creator megagroups newest id first", candidates)
	}

	linked, err := store.SetDiscussionGroup(ctx, 1, broadcast.Channel.ID, firstGroup.Channel.ID)
	if err != nil {
		t.Fatalf("link first group: %v", err)
	}
	if len(linked.Channels) != 2 {
		t.Fatalf("linked changed channels = %+v, want broadcast and group", linked.Channels)
	}
	gotBroadcast, err := store.GetChannelByID(ctx, broadcast.Channel.ID)
	if err != nil {
		t.Fatalf("get linked broadcast: %v", err)
	}
	gotFirst, err := store.GetChannelByID(ctx, firstGroup.Channel.ID)
	if err != nil {
		t.Fatalf("get linked first group: %v", err)
	}
	if gotBroadcast.LinkedChatID != firstGroup.Channel.ID || gotFirst.LinkedChatID != broadcast.Channel.ID {
		t.Fatalf("first link = broadcast %+v group %+v, want bidirectional ids", gotBroadcast, gotFirst)
	}

	replaced, err := store.SetDiscussionGroup(ctx, 1, broadcast.Channel.ID, secondGroup.Channel.ID)
	if err != nil {
		t.Fatalf("replace discussion group: %v", err)
	}
	if len(replaced.Channels) != 3 {
		t.Fatalf("replace changed channels = %+v, want broadcast, old group, new group", replaced.Channels)
	}
	gotBroadcast, _ = store.GetChannelByID(ctx, broadcast.Channel.ID)
	gotFirst, _ = store.GetChannelByID(ctx, firstGroup.Channel.ID)
	gotSecond, err := store.GetChannelByID(ctx, secondGroup.Channel.ID)
	if err != nil {
		t.Fatalf("get linked second group: %v", err)
	}
	if gotBroadcast.LinkedChatID != secondGroup.Channel.ID || gotSecond.LinkedChatID != broadcast.Channel.ID || gotFirst.LinkedChatID != 0 {
		t.Fatalf("replace link = broadcast %d first %d second %d, want old cleared and new bidirectional",
			gotBroadcast.LinkedChatID, gotFirst.LinkedChatID, gotSecond.LinkedChatID)
	}

	unlinked, err := store.SetDiscussionGroup(ctx, 1, 0, secondGroup.Channel.ID)
	if err != nil {
		t.Fatalf("unlink from group side: %v", err)
	}
	if len(unlinked.Channels) != 2 {
		t.Fatalf("unlink changed channels = %+v, want broadcast and group", unlinked.Channels)
	}
	gotBroadcast, _ = store.GetChannelByID(ctx, broadcast.Channel.ID)
	gotSecond, _ = store.GetChannelByID(ctx, secondGroup.Channel.ID)
	if gotBroadcast.LinkedChatID != 0 || gotSecond.LinkedChatID != 0 {
		t.Fatalf("unlink = broadcast %d second %d, want both cleared", gotBroadcast.LinkedChatID, gotSecond.LinkedChatID)
	}
	if _, err := store.SetDiscussionGroup(ctx, 1, 0, secondGroup.Channel.ID); !errors.Is(err, domain.ErrLinkNotModified) {
		t.Fatalf("repeat unlink err = %v, want ErrLinkNotModified", err)
	}
	if _, err := store.SetPreHistoryHidden(ctx, 1, firstGroup.Channel.ID, true); err != nil {
		t.Fatalf("hide first group prehistory: %v", err)
	}
	if _, err := store.SetDiscussionGroup(ctx, 1, broadcast.Channel.ID, firstGroup.Channel.ID); !errors.Is(err, domain.ErrMegagroupPrehistoryHidden) {
		t.Fatalf("hidden group link err = %v, want ErrMegagroupPrehistoryHidden", err)
	}
}

func TestChannelDeleteHistoryCapsHugeMaxID(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "bounded delete history",
		Megagroup:     true,
		Date:          1_700_000_200,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}

	totalMessages := domain.MaxDeleteHistoryBatch + 2
	for i := 0; i < totalMessages; i++ {
		if _, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    1,
			ChannelID: created.Channel.ID,
			RandomID:  int64(10_000 + i),
			Message:   "bulk",
			Date:      1_700_000_201 + i,
		}); err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
	}

	first, err := store.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:      1,
		ChannelID:   created.Channel.ID,
		MaxID:       int(^uint(0) >> 1),
		ForEveryone: true,
		Date:        1_700_001_300,
	})
	if err != nil {
		t.Fatalf("delete first batch: %v", err)
	}
	if first.Offset != 1 || len(first.DeletedIDs) != domain.MaxDeleteHistoryBatch || first.Event.PtsCount != domain.MaxDeleteHistoryBatch {
		t.Fatalf("first batch = %+v, want capped page with offset", first)
	}

	second, err := store.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:      1,
		ChannelID:   created.Channel.ID,
		MaxID:       int(^uint(0) >> 1),
		ForEveryone: true,
		Date:        1_700_001_301,
	})
	if err != nil {
		t.Fatalf("delete second batch: %v", err)
	}
	if second.Offset != 0 || len(second.DeletedIDs) != 2 || second.Event.PtsCount != 2 {
		t.Fatalf("second batch = %+v, want final bounded page keeping create service message", second)
	}
	if second.Channel.TopMessageID != created.Message.ID {
		t.Fatalf("top after full clear = %d, want create service message %d", second.Channel.TopMessageID, created.Message.ID)
	}
	history, err := store.ListChannelHistory(ctx, 1, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list history after full clear: %v", err)
	}
	if len(history.Messages) != 1 || history.Messages[0].ID != created.Message.ID || history.Messages[0].Action == nil {
		t.Fatalf("history after full clear = %+v, want only create service message", history.Messages)
	}
}

func TestChannelDeleteHistoryLocalClearReturnsMonotonicAvailableMinID(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "monotonic local clear",
		Megagroup:     true,
		Date:          1_700_000_250,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	first, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		RandomID:  30_001,
		Message:   "first visible",
		Date:      1_700_000_251,
	})
	if err != nil {
		t.Fatalf("send first message: %v", err)
	}
	second, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		RandomID:  30_002,
		Message:   "second visible",
		Date:      1_700_000_252,
	})
	if err != nil {
		t.Fatalf("send second message: %v", err)
	}

	high, err := store.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		MaxID:     second.Message.ID,
		Date:      1_700_000_253,
	})
	if err != nil {
		t.Fatalf("clear high watermark: %v", err)
	}
	if high.AvailableMinID != second.Message.ID {
		t.Fatalf("high available_min_id = %d, want %d", high.AvailableMinID, second.Message.ID)
	}

	stale, err := store.DeleteChannelHistory(ctx, domain.DeleteChannelHistoryRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		MaxID:     first.Message.ID,
		Date:      1_700_000_254,
	})
	if err != nil {
		t.Fatalf("clear stale low watermark: %v", err)
	}
	if stale.AvailableMinID != second.Message.ID {
		t.Fatalf("stale available_min_id = %d, want monotonic %d", stale.AvailableMinID, second.Message.ID)
	}

	history, err := store.ListChannelHistory(ctx, 1, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history.Messages) != 0 {
		t.Fatalf("history after stale clear = %+v, want no visible messages", history.Messages)
	}
	dialogs, err := store.GetChannelDialogs(ctx, 1, []int64{created.Channel.ID})
	if err != nil {
		t.Fatalf("get channel dialog: %v", err)
	}
	if len(dialogs.Dialogs) != 1 {
		t.Fatalf("dialogs = %+v, want one dialog", dialogs.Dialogs)
	}
	if dialogs.Dialogs[0].TopMessage != 0 || dialogs.Dialogs[0].ReadInboxMaxID != second.Message.ID || dialogs.Dialogs[0].UnreadCount != 0 {
		t.Fatalf("dialog after stale clear = %+v, want top=0 read=%d unread=0", dialogs.Dialogs[0], second.Message.ID)
	}
}

func TestChannelListDialogsDerivesRecipientTopWithoutWriteFanout(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "single copy dialog top",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_300,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := store.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    2,
		ChannelID: created.Channel.ID,
		MaxID:     created.Message.ID,
		Date:      1_700_000_301,
	}); err != nil {
		t.Fatalf("read initial service message: %v", err)
	}

	sent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		RandomID:  88,
		Message:   "visible without write fanout",
		Date:      1_700_000_302,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	list, err := store.ListChannelDialogs(ctx, 2, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list recipient channel dialogs: %v", err)
	}
	if len(list.Dialogs) != 1 {
		t.Fatalf("dialogs = %+v, want one channel dialog", list.Dialogs)
	}
	dialog := list.Dialogs[0]
	if dialog.TopMessage != sent.Message.ID || dialog.TopMessageDate != sent.Message.Date || dialog.UnreadCount != 1 {
		t.Fatalf("recipient dialog = %+v, want top sent message and unread=1", dialog)
	}
	if len(list.Messages) != 1 || list.Messages[0].ID != sent.Message.ID {
		t.Fatalf("dialog messages = %+v, want sent top message", list.Messages)
	}
}

func TestChannelUnreadExcludesOwnOutgoing(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "own outgoing unread",
		Megagroup:     true,
		Date:          1_700_000_360,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		RandomID:  36_001,
		Message:   "own outgoing only",
		Date:      1_700_000_361,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}

	store.mu.Lock()
	member := store.members[created.Channel.ID][1]
	member.ReadInboxMaxID = sent.Message.ID - 1
	store.members[created.Channel.ID][1] = member
	dialog := store.dialogs[1][created.Channel.ID]
	dialog.ReadInboxMaxID = sent.Message.ID - 1
	dialog.UnreadCount = 99
	store.dialogs[1][created.Channel.ID] = dialog
	store.mu.Unlock()

	dialogs, err := store.GetChannelDialogs(ctx, 1, []int64{created.Channel.ID})
	if err != nil {
		t.Fatalf("get channel dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].UnreadCount != 0 {
		t.Fatalf("dialogs = %+v, want own outgoing excluded from unread", dialogs.Dialogs)
	}
	read, err := store.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		MaxID:     sent.Message.ID,
		Date:      1_700_000_362,
	})
	if err != nil {
		t.Fatalf("read channel history: %v", err)
	}
	if read.StillUnreadCount != 0 || read.Dialog.UnreadCount != 0 {
		t.Fatalf("read result = %+v, want no own-outgoing unread", read)
	}
}

func TestChannelMessageReplyMarkupSurvivesReadPaths(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "channel reply markup",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_380,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	markup := &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{
		{Type: domain.MarkupButtonCallback, Text: "Open", Data: []byte{0x00, 0xff, 0x42}},
	}}}
	assertMarkup := func(name string, got *domain.MessageReplyMarkup) {
		t.Helper()
		if got == nil || len(got.Inline) != 1 || len(got.Inline[0]) != 1 {
			t.Fatalf("%s markup = %+v, want one callback button", name, got)
		}
		btn := got.Inline[0][0]
		if btn.Type != domain.MarkupButtonCallback || btn.Text != "Open" || !bytes.Equal(btn.Data, []byte{0x00, 0xff, 0x42}) {
			t.Fatalf("%s button = %+v, want callback Open bytes", name, btn)
		}
	}
	sent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:      1,
		ChannelID:   created.Channel.ID,
		RandomID:    38_001,
		Message:     "via inline keyboard",
		ViaBotID:    99,
		ReplyMarkup: markup,
		Date:        1_700_000_381,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	markup.Inline[0][0].Text = "Changed"
	markup.Inline[0][0].Data[0] = 0x7f
	assertMarkup("send result", sent.Message.ReplyMarkup)
	assertMarkup("send event", sent.Event.Message.ReplyMarkup)

	duplicate, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		RandomID:  38_001,
		Message:   "via inline keyboard",
		ViaBotID:  99,
		ReplyMarkup: &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{
			{Type: domain.MarkupButtonCallback, Text: "Open", Data: []byte{0x00, 0xff, 0x42}},
		}}},
		Date: 1_700_000_382,
	})
	if err != nil {
		t.Fatalf("duplicate send: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Message.ViaBotID != 99 {
		t.Fatalf("duplicate = %+v, want original via bot", duplicate.Message)
	}
	assertMarkup("duplicate message", duplicate.Message.ReplyMarkup)
	assertMarkup("duplicate event", duplicate.Event.Message.ReplyMarkup)

	history, err := store.ListChannelHistory(ctx, 1, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(history.Messages) == 0 || history.Messages[0].ViaBotID != 99 {
		t.Fatalf("history messages = %+v, want via bot 99", history.Messages)
	}
	assertMarkup("history message", history.Messages[0].ReplyMarkup)

	byID, err := store.GetChannelMessages(ctx, 1, created.Channel.ID, []int{sent.Message.ID})
	if err != nil {
		t.Fatalf("get channel messages: %v", err)
	}
	if len(byID.Messages) != 1 || byID.Messages[0].ViaBotID != 99 {
		t.Fatalf("get messages = %+v, want via bot 99", byID.Messages)
	}
	assertMarkup("get messages", byID.Messages[0].ReplyMarkup)

	diff, err := store.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: 1, ChannelID: created.Channel.ID, Pts: created.Channel.Pts, Limit: 10})
	if err != nil {
		t.Fatalf("list difference: %v", err)
	}
	if len(diff.NewMessages) != 1 || diff.NewMessages[0].ViaBotID != 99 {
		t.Fatalf("difference messages = %+v, want via bot 99", diff.NewMessages)
	}
	assertMarkup("difference message", diff.NewMessages[0].ReplyMarkup)

	dialogs, err := store.ListChannelDialogs(ctx, 1, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs: %v", err)
	}
	if len(dialogs.Messages) == 0 || dialogs.Messages[0].ViaBotID != 99 {
		t.Fatalf("dialog messages = %+v, want via bot 99", dialogs.Messages)
	}
	assertMarkup("dialog top message", dialogs.Messages[0].ReplyMarkup)
}

func TestChannelMessageViaBotEditUpdatesReplyMarkup(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "channel via bot edit",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_390,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		RandomID:  39_001,
		Message:   "before",
		ViaBotID:  99,
		Date:      1_700_000_391,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	markup := &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{
		{Type: domain.MarkupButtonCallback, Text: "Done", Data: []byte("v2")},
	}}}
	if _, err := store.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID:          1,
		ChannelID:       created.Channel.ID,
		ID:              sent.Message.ID,
		Message:         "wrong",
		ViaBotEditBotID: 100,
		EditDate:        1_700_000_392,
		SetReplyMarkup:  true,
		ReplyMarkup:     markup,
	}); err != domain.ErrMessageAuthorRequired {
		t.Fatalf("wrong via bot edit err = %v, want ErrMessageAuthorRequired", err)
	}
	edited, err := store.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID:          1,
		ChannelID:       created.Channel.ID,
		ID:              sent.Message.ID,
		Message:         "after",
		ViaBotEditBotID: 99,
		EditDate:        1_700_000_393,
		SetReplyMarkup:  true,
		ReplyMarkup:     markup,
	})
	if err != nil {
		t.Fatalf("via bot edit: %v", err)
	}
	if edited.Event.Type != domain.ChannelUpdateEditMessage || edited.Message.Body != "after" || edited.Message.ViaBotID != 99 {
		t.Fatalf("edited = %+v event=%+v, want edit via bot", edited.Message, edited.Event)
	}
	if edited.Message.ReplyMarkup == nil || edited.Message.ReplyMarkup.Inline[0][0].Text != "Done" || !bytes.Equal(edited.Message.ReplyMarkup.Inline[0][0].Data, []byte("v2")) {
		t.Fatalf("edited markup = %+v, want Done/v2", edited.Message.ReplyMarkup)
	}
	diff, err := store.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: 1, ChannelID: created.Channel.ID, Pts: sent.Event.Pts, Limit: 10})
	if err != nil {
		t.Fatalf("list edit difference: %v", err)
	}
	if len(diff.OtherUpdates) != 1 || diff.OtherUpdates[0].Message.Body != "after" {
		t.Fatalf("edit difference = %+v, want one edited message", diff.OtherUpdates)
	}
}

func TestBroadcastChannelReactionsAreAnonymousAndSkipUnread(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "broadcast reaction",
		Broadcast:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_500,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		RandomID:  50_001,
		Message:   "broadcast post",
		Date:      1_700_000_501,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	res, err := store.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
		UserID:    2,
		ChannelID: created.Channel.ID,
		MessageID: sent.Message.ID,
		Reactions: []domain.MessageReaction{{
			Type:     domain.MessageReactionEmoji,
			Emoticon: "\U0001f44d",
		}},
		Date: 1_700_000_502,
	})
	if err != nil {
		t.Fatalf("set channel reaction: %v", err)
	}
	if len(res.Reactions.Results) != 1 || res.Reactions.Results[0].Count != 1 {
		t.Fatalf("broadcast reaction results = %+v, want count-only aggregate", res.Reactions.Results)
	}
	if len(res.Reactions.Recent) != 0 {
		t.Fatalf("broadcast recent reactors = %+v, want anonymous (empty)", res.Reactions.Recent)
	}
	if res.Reactions.CanSeeList {
		t.Fatalf("broadcast can_see_list = true, want false")
	}
	for _, rows := range store.reactions[created.Channel.ID][sent.Message.ID] {
		for _, row := range rows {
			if row.Unread {
				t.Fatalf("broadcast reaction row = %+v, want unread bookkeeping skipped", row)
			}
		}
	}
	dialogs, err := store.GetChannelDialogs(ctx, 1, []int64{created.Channel.ID})
	if err != nil {
		t.Fatalf("get owner channel dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].UnreadReactions != 0 {
		t.Fatalf("owner dialogs = %+v, want no unread reaction badge on broadcast", dialogs.Dialogs)
	}
}

func TestChannelReadMessageContentsClearsVisibleUnreadReactions(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: 1,
		Title:         "visible unread reaction",
		Megagroup:     true,
		MemberUserIDs: []int64{2},
		Date:          1_700_000_400,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		RandomID:  40_001,
		Message:   "react to this",
		Date:      1_700_000_401,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	if _, err := store.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
		UserID:    2,
		ChannelID: created.Channel.ID,
		MessageID: sent.Message.ID,
		Reactions: []domain.MessageReaction{{
			Type:     domain.MessageReactionEmoji,
			Emoticon: "\U0001f525",
		}},
		Date: 1_700_000_402,
	}); err != nil {
		t.Fatalf("set channel reaction: %v", err)
	}
	dialogs, err := store.GetChannelDialogs(ctx, 1, []int64{created.Channel.ID})
	if err != nil {
		t.Fatalf("get owner channel dialogs: %v", err)
	}
	if len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].UnreadReactions != 1 {
		t.Fatalf("owner dialogs = %+v, want one unread reaction", dialogs.Dialogs)
	}
	unread, err := store.ListChannelUnreadReactions(ctx, 1, domain.ChannelUnreadReactionsFilter{
		ChannelID: created.Channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list unread reactions: %v", err)
	}
	if len(unread.Messages) != 1 || unread.Messages[0].ID != sent.Message.ID {
		t.Fatalf("unread reactions = %+v, want sent message", unread.Messages)
	}
	if unread.Messages[0].Reactions == nil || !hasUnreadChannelReaction(*unread.Messages[0].Reactions) {
		t.Fatalf("unread message reactions = %+v, want unread recent reaction", unread.Messages[0].Reactions)
	}

	read, err := store.ReadChannelMessageContents(ctx, domain.ReadChannelMessageContentsRequest{
		UserID:    1,
		ChannelID: created.Channel.ID,
		IDs:       []int{sent.Message.ID},
	})
	if err != nil {
		t.Fatalf("read channel message contents: %v", err)
	}
	if !reflect.DeepEqual(read.ClearedUnreadReactionMessageIDs, []int{sent.Message.ID}) {
		t.Fatalf("cleared reaction ids = %+v, want [%d]", read.ClearedUnreadReactionMessageIDs, sent.Message.ID)
	}
	if len(read.Messages) != 1 || read.Messages[0].Reactions == nil || hasUnreadChannelReaction(*read.Messages[0].Reactions) {
		t.Fatalf("read messages = %+v, want reaction returned as read", read.Messages)
	}
	unreadAfter, err := store.ListChannelUnreadReactions(ctx, 1, domain.ChannelUnreadReactionsFilter{
		ChannelID: created.Channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list unread reactions after read contents: %v", err)
	}
	if len(unreadAfter.Messages) != 0 {
		t.Fatalf("unread reactions after read contents = %+v, want empty", unreadAfter.Messages)
	}
	dialogsAfter, err := store.GetChannelDialogs(ctx, 1, []int64{created.Channel.ID})
	if err != nil {
		t.Fatalf("get dialogs after read contents: %v", err)
	}
	if len(dialogsAfter.Dialogs) != 1 || dialogsAfter.Dialogs[0].UnreadReactions != 0 {
		t.Fatalf("dialogs after read contents = %+v, want unread reactions 0", dialogsAfter.Dialogs)
	}
}

func hasUnreadChannelReaction(reactions domain.ChannelMessageReactions) bool {
	for _, recent := range reactions.Recent {
		if recent.Unread {
			return true
		}
	}
	return false
}
