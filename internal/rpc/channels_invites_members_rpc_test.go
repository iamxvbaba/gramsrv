package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestChannelParticipantsSearchQueryIsBounded(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002111", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002112", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Participants RPC Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)

	_, err = r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsSearch{Q: strings.Repeat("x", domain.MaxChannelParticipantsQueryLength+1)},
		Limit:   20,
	})
	if err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("get participants long query err = %v, want LIMIT_INVALID", err)
	}
}

func TestChannelsGetParticipantsUsesSingleBatchUserLookup(t *testing.T) {
	ctx := context.Background()
	owner := domain.User{ID: 1, AccessHash: 101, Phone: "15550002131", FirstName: "Owner"}
	first := domain.User{ID: 2, AccessHash: 102, Phone: "15550002132", FirstName: "First"}
	second := domain.User{ID: 3, AccessHash: 103, Phone: "15550002133", FirstName: "Second"}
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{
		owner.ID:  owner,
		first.ID:  first,
		second.ID: second,
	}}}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    users,
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{
			&tg.InputUser{UserID: first.ID, AccessHash: first.AccessHash},
			&tg.InputUser{UserID: second.ID, AccessHash: second.AccessHash},
		},
		Title: "Participants Batch Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	users.byIDCalls = 0
	users.byIDsCalls = 0
	users.lastByIDs = nil

	got, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("get participants: %v", err)
	}
	list := got.(*tg.ChannelsChannelParticipants)
	if users.byIDsCalls != 1 || users.byIDCalls != 0 {
		t.Fatalf("user lookups byIDs=%d byID=%d, want one ByIDs and no ByID", users.byIDsCalls, users.byIDCalls)
	}
	if len(users.lastByIDs) != len(list.Participants) {
		t.Fatalf("ByIDs ids = %+v, participants=%d", users.lastByIDs, len(list.Participants))
	}
	seen := make(map[int64]struct{}, len(users.lastByIDs))
	for _, id := range users.lastByIDs {
		seen[id] = struct{}{}
	}
	for _, want := range []int64{owner.ID, first.ID, second.ID} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("ByIDs ids = %+v, missing %d", users.lastByIDs, want)
		}
	}
	if len(list.Users) != 3 {
		t.Fatalf("users = %+v, want three projected users", list.Users)
	}
}

func TestChannelsGetParticipantsValidatesHashAfterParticipantAccessCheck(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550002141", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550002142", FirstName: "Member"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Participants Access",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700002100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	counting := &countingChannelsService{Service: channelService}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: counting,
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("get participants: %v", err)
	}
	if _, ok := got.(*tg.ChannelsChannelParticipants); !ok {
		t.Fatalf("participants = %T, want *tg.ChannelsChannelParticipants", got)
	}
	if counting.resolveChannelCalls != 0 || counting.getChannelCalls != 0 {
		t.Fatalf("participant access calls ResolveChannel=%d GetChannel=%d, want no pre-resolve/full get", counting.resolveChannelCalls, counting.getChannelCalls)
	}

	_, err = r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash + 1},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   20,
	})
	if err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
}

func TestChannelsGetParticipantsHidesAnonymousAdminFromRegularMember(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002161", FirstName: "Owner"})
	anonymousAdmin, _ := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002162", FirstName: "Hidden"})
	regular, _ := userStore.Create(ctx, domain.User{AccessHash: 33, Phone: "15550002163", FirstName: "Regular"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{
			&tg.InputUser{UserID: anonymousAdmin.ID, AccessHash: anonymousAdmin.AccessHash},
			&tg.InputUser{UserID: regular.ID, AccessHash: regular.AccessHash},
		},
		Title: "Anonymous Admin RPC Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:  &tg.InputUser{UserID: anonymousAdmin.ID, AccessHash: anonymousAdmin.AccessHash},
		AdminRights: tg.ChatAdminRights{
			Anonymous:  true,
			ChangeInfo: true,
		},
	}); err != nil {
		t.Fatalf("edit anonymous admin: %v", err)
	}

	adminsForRegular, err := r.onChannelsGetParticipants(WithUserID(ctx, regular.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsAdmins{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("regular get admins: %v", err)
	}
	regularAdminsPage := adminsForRegular.(*tg.ChannelsChannelParticipants)
	if tgParticipantListHasUser(regularAdminsPage.Participants, anonymousAdmin.ID) || tgUserListHasUser(regularAdminsPage.Users, anonymousAdmin.ID) {
		t.Fatalf("regular admins page leaks anonymous admin: participants=%+v users=%+v", regularAdminsPage.Participants, regularAdminsPage.Users)
	}
	if !tgParticipantListHasUser(regularAdminsPage.Participants, owner.ID) {
		t.Fatalf("regular admins page = %+v, want creator still visible", regularAdminsPage.Participants)
	}

	recentForRegular, err := r.onChannelsGetParticipants(WithUserID(ctx, regular.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("regular get recent: %v", err)
	}
	regularRecentPage := recentForRegular.(*tg.ChannelsChannelParticipants)
	if tgParticipantListHasUser(regularRecentPage.Participants, anonymousAdmin.ID) || tgUserListHasUser(regularRecentPage.Users, anonymousAdmin.ID) {
		t.Fatalf("regular recent page leaks anonymous admin: participants=%+v users=%+v", regularRecentPage.Participants, regularRecentPage.Users)
	}
	if regularRecentPage.Count != 2 {
		t.Fatalf("regular recent count = %d, want visible member count 2", regularRecentPage.Count)
	}

	adminsForOwner, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsAdmins{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("owner get admins: %v", err)
	}
	if !tgParticipantListHasUser(adminsForOwner.(*tg.ChannelsChannelParticipants).Participants, anonymousAdmin.ID) {
		t.Fatalf("owner admins page = %+v, want anonymous admin visible to admins", adminsForOwner.(*tg.ChannelsChannelParticipants).Participants)
	}
}

func TestChannelsEditAdminCreatorSelfCanToggleAnonymous(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550002171", FirstName: "Owner"})
	admin, _ := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550002172", FirstName: "Admin"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: admin.ID, AccessHash: admin.AccessHash}},
		Title: "Creator Anonymous Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	inputChannel := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	updates, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel: inputChannel,
		UserID:  &tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		AdminRights: tg.ChatAdminRights{
			Anonymous: true,
		},
	})
	if err != nil {
		t.Fatalf("creator toggles own anonymous admin rights: %v", err)
	}
	var participantUpdate *tg.ChannelParticipantCreator
	for _, update := range updates.(*tg.Updates).Updates {
		upd, ok := update.(*tg.UpdateChannelParticipant)
		if !ok {
			continue
		}
		if creator, ok := upd.NewParticipant.(*tg.ChannelParticipantCreator); ok && creator.UserID == owner.ID {
			participantUpdate = creator
			break
		}
	}
	if participantUpdate == nil {
		t.Fatalf("editAdmin updates = %+v, want creator participant update", updates.(*tg.Updates).Updates)
	}
	if !participantUpdate.AdminRights.Anonymous || !participantUpdate.AdminRights.ChangeInfo || !participantUpdate.AdminRights.AddAdmins {
		t.Fatalf("creator update rights = %+v, want anonymous plus full creator projection", participantUpdate.AdminRights)
	}

	participant, err := r.onChannelsGetParticipant(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantRequest{
		Channel:     inputChannel,
		Participant: &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
	})
	if err != nil {
		t.Fatalf("get creator participant: %v", err)
	}
	projectedCreator, ok := participant.Participant.(*tg.ChannelParticipantCreator)
	if !ok {
		t.Fatalf("participant = %T, want ChannelParticipantCreator", participant.Participant)
	}
	if !projectedCreator.AdminRights.Anonymous || !projectedCreator.AdminRights.ChangeInfo || !projectedCreator.AdminRights.AddAdmins {
		t.Fatalf("projected creator rights = %+v, want anonymous plus full creator projection", projectedCreator.AdminRights)
	}

	chats, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{inputChannel})
	if err != nil {
		t.Fatalf("get channels: %v", err)
	}
	projectedChannel := chats.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	rights, ok := projectedChannel.GetAdminRights()
	if !ok {
		t.Fatalf("projected channel has no admin rights: %+v", projectedChannel)
	}
	if !rights.Anonymous || !rights.ChangeInfo || !rights.AddAdmins {
		t.Fatalf("projected channel admin rights = %+v, want anonymous plus full creator projection", rights)
	}

	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel: inputChannel,
		UserID:  &tg.InputUser{UserID: admin.ID, AccessHash: admin.AccessHash},
		AdminRights: tg.ChatAdminRights{
			Anonymous:  true,
			ChangeInfo: true,
			AddAdmins:  true,
		},
	}); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	_, err = r.onChannelsEditAdmin(WithUserID(ctx, admin.ID), &tg.ChannelsEditAdminRequest{
		Channel: inputChannel,
		UserID:  &tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		AdminRights: tg.ChatAdminRights{
			ChangeInfo: true,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "USER_CREATOR") {
		t.Fatalf("admin edits creator err = %v, want USER_CREATOR", err)
	}
}

func tgParticipantListHasUser(participants []tg.ChannelParticipantClass, userID int64) bool {
	for _, participant := range participants {
		for _, id := range channelParticipantUserRefs(participant) {
			if id == userID {
				return true
			}
		}
	}
	return false
}

func tgUserListHasUser(users []tg.UserClass, userID int64) bool {
	for _, user := range users {
		if u, ok := user.(*tg.User); ok && u.ID == userID {
			return true
		}
	}
	return false
}

// TestChannelCreateHasPermanentInviteLink 复刻 DrKLO 建频道后的真实调用序列：
// ChannelCreateActivity.generateLink() 发 getExportedChatInvites(admin=self, limit=1)
// 后对 invites.get(0) 直接取值，空列表即 IndexOutOfBounds 闪退——服务端必须保证
// 创建者的永久主链接随创建即存在，且重复列出不会重复生成。
func TestChannelCreateHasPermanentInviteLink(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550002301", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Broadcast: true,
		Title:     "Crash Repro Channel",
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)

	inviteList, err := r.onMessagesGetExportedChatInvites(WithUserID(ctx, owner.ID), &tg.MessagesGetExportedChatInvitesRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		AdminID: &tg.InputUserSelf{},
		Limit:   1,
	})
	if err != nil {
		t.Fatalf("get exported invites after create: %v", err)
	}
	if inviteList.Count < 1 || len(inviteList.Invites) < 1 {
		t.Fatalf("exported invites after create = %+v, want at least the permanent link (DrKLO 直接取 invites[0])", inviteList)
	}
	invite, ok := inviteList.Invites[0].(*tg.ChatInviteExported)
	if !ok || !invite.Permanent || invite.Revoked || invite.AdminID != owner.ID || !strings.HasPrefix(invite.Link, "https://telesrv.net/+") {
		t.Fatalf("invites[0] = %#v, want creator's non-revoked permanent link", inviteList.Invites[0])
	}

	// 幂等：再次列出仍只有同一条主链接，不重复生成。
	again, err := r.onMessagesGetExportedChatInvites(WithUserID(ctx, owner.ID), &tg.MessagesGetExportedChatInvitesRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		AdminID: &tg.InputUserSelf{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get exported invites again: %v", err)
	}
	if again.Count != 1 || len(again.Invites) != 1 {
		t.Fatalf("second list = %+v, want exactly one permanent link", again)
	}
	if link := again.Invites[0].(*tg.ChatInviteExported).Link; link != invite.Link {
		t.Fatalf("second list link = %q, want stable %q", link, invite.Link)
	}

	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel: %v", err)
	}
	fullInviteRaw, ok := full.FullChat.(*tg.ChannelFull).GetExportedInvite()
	if !ok {
		t.Fatalf("channelFull.exported_invite missing, want creator permanent link for DrKLO group settings")
	}
	fullInvite := fullInviteRaw.(*tg.ChatInviteExported)
	if fullInvite.Link != invite.Link || !fullInvite.Permanent || fullInvite.Revoked {
		t.Fatalf("channelFull.exported_invite = %#v, want active permanent link %q", fullInvite, invite.Link)
	}

	replacedRaw, err := r.onMessagesExportChatInvite(WithUserID(ctx, owner.ID), &tg.MessagesExportChatInviteRequest{
		Peer:                  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		LegacyRevokePermanent: true,
	})
	if err != nil {
		t.Fatalf("replace permanent invite: %v", err)
	}
	replaced := replacedRaw.(*tg.ChatInviteExported)
	if replaced.Link == invite.Link || !replaced.Permanent || replaced.Revoked {
		t.Fatalf("replaced permanent invite = %#v, want a fresh active permanent link", replaced)
	}
	refreshedFull, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get full channel after replacing invite: %v", err)
	}
	refreshedRaw, ok := refreshedFull.FullChat.(*tg.ChannelFull).GetExportedInvite()
	if !ok {
		t.Fatalf("channelFull.exported_invite missing after replacing permanent link")
	}
	refreshedInvite := refreshedRaw.(*tg.ChatInviteExported)
	if refreshedInvite.Link != replaced.Link || !refreshedInvite.Permanent || refreshedInvite.Revoked {
		t.Fatalf("channelFull.exported_invite after replace = %#v, want fresh active permanent link %q", refreshedInvite, replaced.Link)
	}
}

func TestChannelAdminPinInviteRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550002201", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550002202", FirstName: "Friend"})
	joiner, _ := userStore.Create(ctx, domain.User{AccessHash: 53, Phone: "15550002203", FirstName: "Joiner"})
	invited, _ := userStore.Create(ctx, domain.User{AccessHash: 54, Phone: "15550002204", FirstName: "Invited"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "RPC Admin Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	createdChannel, err := channelStore.GetChannelByID(ctx, channel.ID)
	if err != nil {
		t.Fatalf("get created channel: %v", err)
	}
	initialChannelPts := createdChannel.Pts

	selfParticipant, err := r.onChannelsGetParticipant(WithUserID(ctx, friend.ID), &tg.ChannelsGetParticipantRequest{
		Channel:     &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Participant: &tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("get self regular participant: %v", err)
	}
	if _, ok := selfParticipant.Participant.(*tg.ChannelParticipantSelf); !ok {
		t.Fatalf("self regular participant = %T, want channelParticipantSelf", selfParticipant.Participant)
	}
	recentForFriend, err := r.onChannelsGetParticipants(WithUserID(ctx, friend.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get recent participants for regular self: %v", err)
	}
	foundSelf := false
	for _, participant := range recentForFriend.(*tg.ChannelsChannelParticipants).Participants {
		if _, ok := participant.(*tg.ChannelParticipantSelf); ok {
			foundSelf = true
			break
		}
	}
	if !foundSelf {
		t.Fatalf("recent participants = %+v, want current regular member as channelParticipantSelf", recentForFriend.(*tg.ChannelsChannelParticipants).Participants)
	}

	adminUpdates, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:  &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		AdminRights: tg.ChatAdminRights{
			ChangeInfo:  true,
			InviteUsers: true,
			PinMessages: true,
		},
		Rank: "ops",
	})
	if err != nil {
		t.Fatalf("edit admin: %v", err)
	}
	if updates := adminUpdates.(*tg.Updates); len(updates.Updates) != 2 {
		t.Fatalf("admin updates = %+v, want participant update and channel refresh", updates.Updates)
	} else if _, ok := updates.Updates[0].(*tg.UpdateChannelParticipant); !ok {
		t.Fatalf("admin update[0] = %T, want updateChannelParticipant", updates.Updates[0])
	} else if _, ok := updates.Updates[1].(*tg.UpdateChannel); !ok {
		t.Fatalf("admin update[1] = %T, want updateChannel", updates.Updates[1])
	}
	adminDiff, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     initialChannelPts,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("channel difference after admin: %v", err)
	}
	adminEmptyDiff, ok := adminDiff.(*tg.UpdatesChannelDifferenceEmpty)
	if !ok || !adminEmptyDiff.Final || adminEmptyDiff.Pts != initialChannelPts {
		t.Fatalf("admin diff = %T %+v, want empty difference at unchanged pts %d", adminDiff, adminDiff, initialChannelPts)
	}
	admins, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Filter:  &tg.ChannelParticipantsAdmins{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get admin participants: %v", err)
	}
	if list := admins.(*tg.ChannelsChannelParticipants); len(list.Participants) != 2 {
		t.Fatalf("admin participants = %+v, want creator and promoted admin", list.Participants)
	}

	titleUpdates, err := r.onChannelsEditTitle(WithUserID(ctx, friend.ID), &tg.ChannelsEditTitleRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Title:   "RPC Admin Group 2",
	})
	if err != nil {
		t.Fatalf("edit title: %v", err)
	}
	titleContainer := titleUpdates.(*tg.Updates)
	if len(titleContainer.Updates) < 2 {
		t.Fatalf("title updates = %+v, want channel + service message", titleContainer.Updates)
	}
	titleMsg, ok := titleContainer.Updates[1].(*tg.UpdateNewChannelMessage)
	if !ok {
		t.Fatalf("title update[1] = %T, want updateNewChannelMessage", titleContainer.Updates[1])
	}
	if action := titleMsg.Message.(*tg.MessageService).Action; action.(*tg.MessageActionChatEditTitle).Title != "RPC Admin Group 2" {
		t.Fatalf("title action = %#v, want new title", action)
	}

	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "pin me",
		RandomID: 123,
	})
	if err != nil {
		t.Fatalf("send for pin: %v", err)
	}
	msgID := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	pinUpdates, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, friend.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ID:   msgID,
	})
	if err != nil {
		t.Fatalf("pin message: %v", err)
	}
	pinned, ok := pinUpdates.(*tg.Updates).Updates[0].(*tg.UpdatePinnedChannelMessages)
	if !ok || !pinned.Pinned || pinned.Messages[0] != msgID {
		t.Fatalf("pin update = %#v, want pinned channel message id=%d", pinUpdates.(*tg.Updates).Updates[0], msgID)
	}

	invitedUsers, err := r.onChannelsInviteToChannel(WithUserID(ctx, friend.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash}},
	})
	if err != nil {
		t.Fatalf("invite to channel: %v", err)
	}
	if invitedUsers.Updates == nil || len(invitedUsers.MissingInvitees) != 0 {
		t.Fatalf("invited users = %+v, want updates and no missing users", invitedUsers)
	}
	inviteUpdates, ok := invitedUsers.Updates.(*tg.Updates)
	if !ok || len(inviteUpdates.Chats) == 0 {
		t.Fatalf("invite updates = %T %+v, want channel chat", invitedUsers.Updates, invitedUsers.Updates)
	}
	assertDefaultBannedRightsAllowsSend(t, inviteUpdates.Chats[0])

	invite, err := r.onMessagesExportChatInvite(WithUserID(ctx, friend.ID), &tg.MessagesExportChatInviteRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Title: "join",
	})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	exported := invite.(*tg.ChatInviteExported)
	hash := strings.TrimPrefix(exported.Link, "https://telesrv.net/+")
	checked, err := r.onMessagesCheckChatInvite(WithUserID(ctx, joiner.ID), hash)
	if err != nil {
		t.Fatalf("check invite: %v", err)
	}
	if preview, ok := checked.(*tg.ChatInvite); !ok || !preview.Megagroup || preview.Title != "RPC Admin Group 2" {
		t.Fatalf("invite preview = %#v, want megagroup title", checked)
	}
	imported, err := r.onMessagesImportChatInvite(WithUserID(ctx, joiner.ID), hash)
	if err != nil {
		t.Fatalf("import invite: %v", err)
	}
	importOk, ok := imported.(*tg.MessagesChatInviteJoinResultOk)
	if !ok {
		t.Fatalf("import result = %T, want *tg.MessagesChatInviteJoinResultOk", imported)
	}
	importUpdates := importOk.Updates.(*tg.Updates)
	if len(importUpdates.Chats) != 1 || len(importUpdates.Updates) != 2 {
		t.Fatalf("import updates = %+v, want chat, join service update, and channel refresh", importUpdates)
	}
	assertDefaultBannedRightsAllowsSend(t, importUpdates.Chats[0])
	if _, ok := importUpdates.Updates[0].(*tg.UpdateNewChannelMessage); !ok {
		t.Fatalf("import first update = %T, want join service update", importUpdates.Updates[0])
	} else if refresh, ok := importUpdates.Updates[1].(*tg.UpdateChannel); !ok || refresh.ChannelID != channel.ID {
		t.Fatalf("import second update = %#v, want channel refresh", importUpdates.Updates[1])
	}
	inviteList, err := r.onMessagesGetExportedChatInvites(WithUserID(ctx, friend.ID), &tg.MessagesGetExportedChatInvitesRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		AdminID: &tg.InputUserSelf{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get exported invites: %v", err)
	}
	// 官方语义：管理员列出自己的有效链接时永久主链接必有（首页自愈生成），
	// 因此列表 = 主链接 + 显式导出的 "join" 链接。
	if inviteList.Count != 2 || len(inviteList.Invites) != 2 || len(inviteList.Users) == 0 {
		t.Fatalf("exported invite list = %+v, want permanent link plus exported invite", inviteList)
	}
	var listedInvite, permanentInvite *tg.ChatInviteExported
	for _, raw := range inviteList.Invites {
		invite, ok := raw.(*tg.ChatInviteExported)
		if !ok {
			t.Fatalf("invite = %T, want *tg.ChatInviteExported", raw)
		}
		if invite.Permanent {
			permanentInvite = invite
		}
		if invite.Link == exported.Link {
			listedInvite = invite
		}
	}
	if permanentInvite == nil || permanentInvite.Revoked || permanentInvite.AdminID != friend.ID {
		t.Fatalf("exported invite list = %+v, want non-revoked permanent link owned by admin", inviteList.Invites)
	}
	if listedInvite == nil {
		t.Fatalf("exported invite list = %+v, missing explicitly exported link %q", inviteList.Invites, exported.Link)
	}
	listedUsage, listedUsageOK := listedInvite.GetUsage()
	listedTitle, listedTitleOK := listedInvite.GetTitle()
	if !listedUsageOK || listedUsage != 1 || !listedTitleOK || listedTitle != "join" {
		t.Fatalf("listed invite = %#v, want exported link with one import", listedInvite)
	}
	if _, err := r.onMessagesGetExportedChatInvites(WithUserID(ctx, friend.ID), &tg.MessagesGetExportedChatInvitesRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		AdminID: &tg.InputUserSelf{},
		Limit:   101,
	}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("get exported invites high limit err = %v, want LIMIT_INVALID", err)
	}
	inviteDetails, err := r.onMessagesGetExportedChatInvite(WithUserID(ctx, friend.ID), &tg.MessagesGetExportedChatInviteRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Link: exported.Link,
	})
	if err != nil {
		t.Fatalf("get exported invite: %v", err)
	}
	if details := inviteDetails.(*tg.MessagesExportedChatInvite); details.Invite == nil || len(details.Users) == 0 {
		t.Fatalf("exported invite details = %+v, want invite plus user context", inviteDetails)
	}
	editInviteReq := &tg.MessagesEditExportedChatInviteRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Link: exported.Link,
	}
	editInviteReq.SetTitle("ops link")
	editedInvite, err := r.onMessagesEditExportedChatInvite(WithUserID(ctx, friend.ID), editInviteReq)
	if err != nil {
		t.Fatalf("edit exported invite: %v", err)
	}
	if edited := editedInvite.(*tg.MessagesExportedChatInvite); edited.Invite == nil || len(edited.Users) == 0 {
		t.Fatalf("edited invite = %+v, want invite plus user context", editedInvite)
	} else if got, ok := edited.Invite.(*tg.ChatInviteExported).GetTitle(); !ok || got != "ops link" {
		t.Fatalf("edited invite title = %q, want ops link", got)
	}
	adminsWithInvites, err := r.onMessagesGetAdminsWithInvites(WithUserID(ctx, friend.ID), &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash})
	if err != nil {
		t.Fatalf("get admins with invites: %v", err)
	}
	// 创建者随建自动持有主链接（1 条）；friend 为显式导出的 "join" + 列表自愈
	// 生成的主链接（2 条）。
	adminInvites := map[int64]int{}
	for _, admin := range adminsWithInvites.Admins {
		adminInvites[admin.AdminID] = admin.InvitesCount
	}
	if len(adminsWithInvites.Admins) != 2 || adminInvites[owner.ID] != 1 || adminInvites[friend.ID] != 2 || len(adminsWithInvites.Users) == 0 {
		t.Fatalf("admins with invites = %+v, want creator permanent link plus friend's two links", adminsWithInvites)
	}
	importers, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, friend.ID), &tg.MessagesGetChatInviteImportersRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("get invite importers: %v", err)
	}
	if importers.Count != 1 || len(importers.Importers) != 1 || importers.Importers[0].UserID != joiner.ID || len(importers.Users) == 0 {
		t.Fatalf("invite importers = %+v, want joined importer", importers)
	}
	importersSearchReq := &tg.MessagesGetChatInviteImportersRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit: 10,
	}
	importersSearchReq.SetLink(exported.Link)
	importersSearchReq.SetQ("bob")
	if _, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, friend.ID), importersSearchReq); err == nil || !strings.Contains(err.Error(), "SEARCH_WITH_LINK_NOT_SUPPORTED") {
		t.Fatalf("get invite importers q+link err = %v, want SEARCH_WITH_LINK_NOT_SUPPORTED", err)
	}
	if _, err := r.onMessagesHideChatJoinRequest(WithUserID(ctx, friend.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:   &tg.InputUser{UserID: invited.ID, AccessHash: invited.AccessHash},
	}); err == nil || !strings.Contains(err.Error(), "HIDE_REQUESTER_MISSING") {
		t.Fatalf("hide chat join request without pending err = %v, want HIDE_REQUESTER_MISSING", err)
	}
	if updates, err := r.onMessagesHideAllChatJoinRequests(WithUserID(ctx, friend.ID), &tg.MessagesHideAllChatJoinRequestsRequest{
		Approved: false,
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}); err != nil {
		t.Fatalf("hide all chat join requests: %v", err)
	} else if _, ok := updates.(*tg.Updates); !ok {
		t.Fatalf("hide all chat join requests updates = %T, want *tg.Updates", updates)
	}
	if ok, err := r.onMessagesDeleteExportedChatInvite(WithUserID(ctx, friend.ID), &tg.MessagesDeleteExportedChatInviteRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Link: exported.Link,
	}); err != nil || !ok {
		t.Fatalf("delete exported invite ok=%v err=%v, want true nil", ok, err)
	}
	if ok, err := r.onMessagesDeleteRevokedExportedChatInvites(WithUserID(ctx, friend.ID), &tg.MessagesDeleteRevokedExportedChatInvitesRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		AdminID: &tg.InputUserSelf{},
	}); err != nil || !ok {
		t.Fatalf("delete revoked invites ok=%v err=%v, want true nil", ok, err)
	}
	adminLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminLogRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("get admin log: %v", err)
	}
	if len(adminLog.Events) < 5 || len(adminLog.Chats) != 1 || len(adminLog.Users) < 3 {
		t.Fatalf("admin log = %+v, want events plus chat/users", adminLog)
	}
	tooManyAdmins := make([]tg.InputUserClass, domain.MaxChannelAdminLogAdmins+1)
	for i := range tooManyAdmins {
		tooManyAdmins[i] = &tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash}
	}
	tooManyAdminsReq := &tg.ChannelsGetAdminLogRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit:   1,
	}
	tooManyAdminsReq.SetAdmins(tooManyAdmins)
	if _, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), tooManyAdminsReq); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("get admin log too many admins err = %v, want LIMIT_INVALID", err)
	}
	pinnedFilter := tg.ChannelAdminLogEventsFilter{}
	pinnedFilter.SetPinned(true)
	pinnedReq := &tg.ChannelsGetAdminLogRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit:   10,
	}
	pinnedReq.SetEventsFilter(pinnedFilter)
	pinnedLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), pinnedReq)
	if err != nil {
		t.Fatalf("get pinned admin log: %v", err)
	}
	if len(pinnedLog.Events) != 1 {
		t.Fatalf("pinned admin log events = %+v, want one", pinnedLog.Events)
	}
	if _, ok := pinnedLog.Events[0].Action.(*tg.ChannelAdminLogEventActionUpdatePinned); !ok {
		t.Fatalf("pinned admin log action = %T, want updatePinned", pinnedLog.Events[0].Action)
	}
	unpinnedAll, err := r.onMessagesUnpinAllMessages(WithUserID(ctx, friend.ID), &tg.MessagesUnpinAllMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("unpin all messages: %v", err)
	}
	if unpinnedAll.Pts == 0 || unpinnedAll.PtsCount != 1 || unpinnedAll.Offset != 0 {
		t.Fatalf("unpin all affected history = %+v, want one channel pts event", unpinnedAll)
	}
	afterUnpin, err := r.deps.Channels.GetChannel(ctx, friend.ID, channel.ID)
	if err != nil {
		t.Fatalf("get channel after unpin: %v", err)
	}
	if afterUnpin.Channel.PinnedMessageID != 0 {
		t.Fatalf("pinned message after unpin all = %d, want 0", afterUnpin.Channel.PinnedMessageID)
	}
	unpinnedAgain, err := r.onMessagesUnpinAllMessages(WithUserID(ctx, friend.ID), &tg.MessagesUnpinAllMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("unpin all messages again: %v", err)
	}
	if unpinnedAgain.Pts != afterUnpin.Channel.Pts || unpinnedAgain.PtsCount != 0 || unpinnedAgain.Offset != 0 {
		t.Fatalf("unpin all no-op affected history = %+v, want current pts with zero pts_count", unpinnedAgain)
	}
	invalidTopicUnpin := &tg.MessagesUnpinAllMessagesRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	}
	invalidTopicUnpin.SetTopMsgID(domain.MaxMessageBoxID + 1)
	if _, err := r.onMessagesUnpinAllMessages(WithUserID(ctx, friend.ID), invalidTopicUnpin); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("unpin all invalid top msg err = %v, want MESSAGE_ID_INVALID", err)
	}
}

func TestChannelEditBannedKickNotifiesKickedViewer(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 58, Phone: "15550002258", FirstName: "Owner"})
	kicked, _ := userStore.Create(ctx, domain.User{AccessHash: 59, Phone: "15550002259", FirstName: "Kicked"})
	channelStore := memory.NewChannelStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash}},
		Title: "Kick Notify",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	// 被踢者视角的推送 chats 必须是完整（非 min）投影并带 left：
	// 客户端只对非 min channel 应用 left/banned_rights。
	kickedView := r.channelParticipantUpdates(ctx, kicked.ID, owner.ID, domain.Channel{ID: channel.ID, AccessHash: channel.AccessHash, Title: channel.Title, Megagroup: true},
		domain.ChannelMember{ChannelID: channel.ID, UserID: kicked.ID, Status: domain.ChannelMemberActive},
		domain.ChannelMember{ChannelID: channel.ID, UserID: kicked.ID, Status: domain.ChannelMemberKicked, BannedRights: domain.ChannelBannedRights{ViewMessages: true}},
		1700000000)
	if len(kickedView.Chats) != 1 {
		t.Fatalf("kicked view chats = %+v, want one chat", kickedView.Chats)
	}
	kickedChat, ok := kickedView.Chats[0].(*tg.Channel)
	if !ok {
		t.Fatalf("kicked chat = %#v, want *tg.Channel", kickedView.Chats[0])
	}
	if kickedChat.Min {
		t.Fatalf("kicked chat = %#v, must not be min: min objects do not apply membership state", kickedChat)
	}
	if _, hasBanned := kickedChat.GetBannedRights(); !hasBanned {
		t.Fatalf("kicked chat = %#v, want banned_rights so the viewer learns the kick", kickedChat)
	}
	adminView := r.channelParticipantUpdates(ctx, owner.ID, owner.ID, domain.Channel{ID: channel.ID, AccessHash: channel.AccessHash, Title: channel.Title, Megagroup: true},
		domain.ChannelMember{ChannelID: channel.ID, UserID: kicked.ID, Status: domain.ChannelMemberActive},
		domain.ChannelMember{ChannelID: channel.ID, UserID: kicked.ID, Status: domain.ChannelMemberKicked},
		1700000000)
	if adminChat, ok := adminView.Chats[0].(*tg.Channel); !ok || !adminChat.Min {
		t.Fatalf("admin-side chat = %#v, want min channel that preserves local rights", adminView.Chats[0])
	}

	if _, err := r.onChannelsEditBanned(WithUserID(ctx, owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:      input,
		Participant:  &tg.InputPeerUser{UserID: kicked.ID, AccessHash: kicked.AccessHash},
		BannedRights: tg.ChatBannedRights{ViewMessages: true},
	}); err != nil {
		t.Fatalf("kick member: %v", err)
	}

	// 被踢后 channels.getChannels 必须返回 channelForbidden 而不是省略。
	got, err := r.onChannelsGetChannels(WithUserID(ctx, kicked.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("kicked getChannels: %v", err)
	}
	chats, ok := got.(*tg.MessagesChats)
	if !ok || len(chats.Chats) != 1 {
		t.Fatalf("kicked getChannels = %T %+v, want one channelForbidden", got, got)
	}
	forbidden, ok := chats.Chats[0].(*tg.ChannelForbidden)
	if !ok || forbidden.ID != channel.ID || forbidden.AccessHash != channel.AccessHash || !forbidden.Megagroup {
		t.Fatalf("kicked chat = %#v, want channelForbidden tombstone", chats.Chats[0])
	}
}

// TestChannelEditBannedMaintainsOnlineMembershipIndex 验证 editBanned 与 editAdmin 对称
// 维护在线成员推送路由索引：踢出/封禁后必须立即摘除（否则被踢在线成员保留 stale
// byMemberChannel 条目直到断线，占用实时 fan-out cap 名额，且群通话推送会继续投递）；
// 仅限制权限（仍为 active 成员）时索引保留。
func TestChannelEditBannedMaintainsOnlineMembershipIndex(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550002261", FirstName: "Owner"})
	target, _ := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550002262", FirstName: "Target"})
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(memory.NewChannelStore()),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: target.ID, AccessHash: target.AccessHash}},
		Title: "Kick Index",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	contains := func(ids []int64, id int64) bool {
		for _, v := range ids {
			if v == id {
				return true
			}
		}
		return false
	}
	if !contains(sessions.onlineChannelMemberIDs(channel.ID), target.ID) {
		t.Fatalf("membership index after create = %v, want target %d", sessions.onlineChannelMemberIDs(channel.ID), target.ID)
	}

	// 仅限制发言（仍为 active 成员）：索引保留。
	if _, err := r.onChannelsEditBanned(WithUserID(ctx, owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:      input,
		Participant:  &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
		BannedRights: tg.ChatBannedRights{SendMessages: true},
	}); err != nil {
		t.Fatalf("restrict member: %v", err)
	}
	if !contains(sessions.onlineChannelMemberIDs(channel.ID), target.ID) {
		t.Fatalf("membership index after restrict = %v, restricted member must stay routed", sessions.onlineChannelMemberIDs(channel.ID))
	}

	// 踢出（view_messages）：索引必须立即摘除，其他成员不受影响。
	if _, err := r.onChannelsEditBanned(WithUserID(ctx, owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:      input,
		Participant:  &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
		BannedRights: tg.ChatBannedRights{ViewMessages: true},
	}); err != nil {
		t.Fatalf("kick member: %v", err)
	}
	if contains(sessions.onlineChannelMemberIDs(channel.ID), target.ID) {
		t.Fatalf("membership index after kick = %v, kicked member must be removed", sessions.onlineChannelMemberIDs(channel.ID))
	}
	if !contains(sessions.onlineChannelMemberIDs(channel.ID), owner.ID) {
		t.Fatalf("membership index after kick = %v, owner must survive", sessions.onlineChannelMemberIDs(channel.ID))
	}
}

func TestChannelInviteKickedMemberRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 55, Phone: "15550002255", FirstName: "Owner"})
	helper, _ := userStore.Create(ctx, domain.User{AccessHash: 56, Phone: "15550002256", FirstName: "Helper"})
	kicked, _ := userStore.Create(ctx, domain.User{AccessHash: 57, Phone: "15550002257", FirstName: "Kicked"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{
			&tg.InputUser{UserID: helper.ID, AccessHash: helper.AccessHash},
			&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash},
		},
		Title: "RPC Invite Kicked",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	if _, err := r.onChannelsEditBanned(WithUserID(ctx, owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:     input,
		Participant: &tg.InputPeerUser{UserID: kicked.ID, AccessHash: kicked.AccessHash},
		BannedRights: tg.ChatBannedRights{
			ViewMessages: true,
		},
	}); err != nil {
		t.Fatalf("kick member: %v", err)
	}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, helper.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: input,
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash}},
	}); err == nil || !strings.Contains(err.Error(), "USER_KICKED") {
		t.Fatalf("helper invite kicked err = %v, want USER_KICKED", err)
	}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: input,
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash}},
	}); err != nil {
		t.Fatalf("owner restore kicked invite: %v", err)
	}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: input,
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: kicked.ID, AccessHash: kicked.AccessHash}},
	}); err == nil || !strings.Contains(err.Error(), "USER_ALREADY_PARTICIPANT") {
		t.Fatalf("duplicate invite err = %v, want USER_ALREADY_PARTICIPANT", err)
	}
}
