package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestChannelMemberRankRPC covers the permission matrix of member Tags (rank-only edits):
// actor ∈ {creator, manage_ranks admin, ordinary member} × target ∈ {self, ordinary member,
// self-promoted admin, admin promoted by someone else, creator} × group-level edit_rank switch
// {on, off}, plus a regression of the old semantics where editAdmin demotion clears the rank.
func TestChannelMemberRankRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550002401", FirstName: "Owner"})
	tagger, _ := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550002402", FirstName: "Tagger"})
	plain, _ := userStore.Create(ctx, domain.User{AccessHash: 73, Phone: "15550002403", FirstName: "Plain"})
	other, _ := userStore.Create(ctx, domain.User{AccessHash: 74, Phone: "15550002404", FirstName: "Other"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{
			&tg.InputUser{UserID: tagger.ID, AccessHash: tagger.AccessHash},
			&tg.InputUser{UserID: plain.ID, AccessHash: plain.AccessHash},
			&tg.InputUser{UserID: other.ID, AccessHash: other.AccessHash},
		},
		Title: "Member Tag Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := created.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	inputChannel := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	editRank := func(actorID int64, participant tg.InputPeerClass, rank string) (tg.UpdatesClass, error) {
		return r.onMessagesEditChatParticipantRank(WithUserID(ctx, actorID), &tg.MessagesEditChatParticipantRankRequest{
			Peer:        peer,
			Participant: participant,
			Rank:        rank,
		})
	}
	getParticipant := func(viewerID, targetID int64) tg.ChannelParticipantClass {
		t.Helper()
		res, err := r.onChannelsGetParticipant(WithUserID(ctx, viewerID), &tg.ChannelsGetParticipantRequest{
			Channel:     inputChannel,
			Participant: &tg.InputPeerUser{UserID: targetID},
		})
		if err != nil {
			t.Fatalf("get participant %d: %v", targetID, err)
		}
		return res.Participant
	}

	// creator sets a tag on an ordinary member: role unchanged, rank persists, pushes updateChannelParticipant.
	rankUpdates, err := editRank(owner.ID, &tg.InputPeerUser{UserID: plain.ID, AccessHash: plain.AccessHash}, "navigator")
	if err != nil {
		t.Fatalf("creator set member tag: %v", err)
	}
	participantUpdate := tg.UpdateClass(nil)
	for _, update := range rankUpdates.(*tg.Updates).Updates {
		if _, ok := update.(*tg.UpdateChannelParticipant); ok {
			participantUpdate = update
			break
		}
	}
	if participantUpdate == nil {
		t.Fatalf("rank updates = %+v, want updateChannelParticipant", rankUpdates.(*tg.Updates).Updates)
	}
	newParticipant, ok := participantUpdate.(*tg.UpdateChannelParticipant).GetNewParticipant()
	if !ok {
		t.Fatalf("rank update missing new participant: %+v", participantUpdate)
	}
	taggedMember, ok := newParticipant.(*tg.ChannelParticipant)
	if !ok {
		t.Fatalf("tagged member = %T, want plain channelParticipant (role must not change)", newParticipant)
	}
	if rank, _ := taggedMember.GetRank(); rank != "navigator" {
		t.Fatalf("tagged member rank = %q, want navigator", rank)
	}
	if got, ok := getParticipant(owner.ID, plain.ID).(*tg.ChannelParticipant); !ok {
		t.Fatalf("plain participant after tag = %T, want channelParticipant", getParticipant(owner.ID, plain.ID))
	} else if rank, _ := got.GetRank(); rank != "navigator" {
		t.Fatalf("plain participant rank = %q, want navigator", rank)
	}

	// The admins filter is the data source for the message badges on both ends: it must return admins + ordinary members with a tag.
	adminsRes, err := r.onChannelsGetParticipants(WithUserID(ctx, other.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: inputChannel,
		Filter:  &tg.ChannelParticipantsAdmins{},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("get admins participants: %v", err)
	}
	badgeRanks := map[int64]string{}
	for _, participant := range adminsRes.(*tg.ChannelsChannelParticipants).Participants {
		switch p := participant.(type) {
		case *tg.ChannelParticipantCreator:
			badgeRanks[p.UserID], _ = p.GetRank()
		case *tg.ChannelParticipantAdmin:
			badgeRanks[p.UserID], _ = p.GetRank()
		case *tg.ChannelParticipant:
			rank, _ := p.GetRank()
			badgeRanks[p.UserID] = rank
		}
	}
	if len(badgeRanks) != 2 {
		t.Fatalf("admins filter participants = %+v, want creator + tagged plain member", badgeRanks)
	}
	if badgeRanks[plain.ID] != "navigator" {
		t.Fatalf("admins filter plain rank = %q, want navigator (message badge source)", badgeRanks[plain.ID])
	}
	if _, ok := badgeRanks[other.ID]; ok {
		t.Fatalf("admins filter must not contain untagged plain member: %+v", badgeRanks)
	}

	// An ordinary member edits itself (the switch defaults to on, inputPeerSelf path).
	if _, err := editRank(plain.ID, &tg.InputPeerSelf{}, "pilot"); err != nil {
		t.Fatalf("plain member edits own tag: %v", err)
	}
	if self, ok := getParticipant(plain.ID, plain.ID).(*tg.ChannelParticipantSelf); !ok {
		t.Fatalf("self participant = %T, want channelParticipantSelf", getParticipant(plain.ID, plain.ID))
	} else if rank, _ := self.GetRank(); rank != "pilot" {
		t.Fatalf("self rank = %q, want pilot", rank)
	}

	// An ordinary member edits someone else -> CHAT_ADMIN_REQUIRED.
	if _, err := editRank(plain.ID, &tg.InputPeerUser{UserID: other.ID, AccessHash: other.AccessHash}, "x"); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("plain member edits other tag err = %v, want CHAT_ADMIN_REQUIRED", err)
	}

	// creator grants the tagger admin rights to manage Tags; manage_ranks must persist and be sent.
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel: inputChannel,
		UserID:  &tg.InputUser{UserID: tagger.ID, AccessHash: tagger.AccessHash},
		AdminRights: tg.ChatAdminRights{
			ChangeInfo:  true,
			AddAdmins:   true,
			ManageRanks: true,
		},
	}); err != nil {
		t.Fatalf("promote tagger with manage_ranks: %v", err)
	}
	taggerAdmin, ok := getParticipant(owner.ID, tagger.ID).(*tg.ChannelParticipantAdmin)
	if !ok {
		t.Fatalf("tagger participant = %T, want channelParticipantAdmin", getParticipant(owner.ID, tagger.ID))
	}
	if !taggerAdmin.AdminRights.ManageRanks {
		t.Fatalf("tagger admin rights = %+v, want manage_ranks=true round-trip", taggerAdmin.AdminRights)
	}
	chats, err := r.onChannelsGetChannels(WithUserID(ctx, tagger.ID), []tg.InputChannelClass{inputChannel})
	if err != nil {
		t.Fatalf("get channels as tagger: %v", err)
	}
	taggerChat := chats.GetChats()[0].(*tg.Channel)
	if rights, ok := taggerChat.GetAdminRights(); !ok || !rights.ManageRanks {
		t.Fatalf("tagger chat admin rights = %+v, want manage_ranks=true", taggerChat)
	}

	// manage_ranks admin edits an ordinary member -> OK.
	if _, err := editRank(tagger.ID, &tg.InputPeerUser{UserID: plain.ID, AccessHash: plain.AccessHash}, "lookout"); err != nil {
		t.Fatalf("manage_ranks admin edits plain member: %v", err)
	}
	// manage_ranks admin edits creator -> USER_CREATOR.
	if _, err := editRank(tagger.ID, &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}, "boss"); err == nil || !strings.Contains(err.Error(), "USER_CREATOR") {
		t.Fatalf("admin edits creator tag err = %v, want USER_CREATOR", err)
	}
	// manage_ranks admin edits itself -> OK.
	if _, err := editRank(tagger.ID, &tg.InputPeerSelf{}, "chief"); err != nil {
		t.Fatalf("admin edits own tag: %v", err)
	}

	// After tagger promotes other (promoted_by=tagger) it can edit other's tag; after creator re-promotes, it cannot.
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, tagger.ID), &tg.ChannelsEditAdminRequest{
		Channel:     inputChannel,
		UserID:      &tg.InputUser{UserID: other.ID, AccessHash: other.AccessHash},
		AdminRights: tg.ChatAdminRights{ChangeInfo: true},
	}); err != nil {
		t.Fatalf("tagger promotes other: %v", err)
	}
	if _, err := editRank(tagger.ID, &tg.InputPeerUser{UserID: other.ID, AccessHash: other.AccessHash}, "scout"); err != nil {
		t.Fatalf("admin edits tag of admin they promoted: %v", err)
	}
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     inputChannel,
		UserID:      &tg.InputUser{UserID: other.ID, AccessHash: other.AccessHash},
		AdminRights: tg.ChatAdminRights{ChangeInfo: true},
		Rank:        "scout",
	}); err != nil {
		t.Fatalf("creator re-promotes other: %v", err)
	}
	if _, err := editRank(tagger.ID, &tg.InputPeerUser{UserID: other.ID, AccessHash: other.AccessHash}, "spy"); err == nil || !strings.Contains(err.Error(), "RIGHT_FORBIDDEN") {
		t.Fatalf("admin edits tag of admin promoted by creator err = %v, want RIGHT_FORBIDDEN", err)
	}

	// Turn off the group-level Member Tags switch (default_banned_rights.edit_rank=true).
	if _, err := r.onMessagesEditChatDefaultBannedRights(WithUserID(ctx, owner.ID), &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer:         peer,
		BannedRights: tg.ChatBannedRights{EditRank: true},
	}); err != nil {
		t.Fatalf("disable member tags switch: %v", err)
	}
	chatsAfterSwitch, err := r.onChannelsGetChannels(WithUserID(ctx, plain.ID), []tg.InputChannelClass{inputChannel})
	if err != nil {
		t.Fatalf("get channels after switch: %v", err)
	}
	switchedChat := chatsAfterSwitch.GetChats()[0].(*tg.Channel)
	if rights, ok := switchedChat.GetDefaultBannedRights(); !ok || !rights.EditRank {
		t.Fatalf("default banned rights after switch = %+v, want edit_rank=true round-trip", switchedChat)
	}
	// With the switch off: an ordinary member editing itself -> RIGHT_FORBIDDEN; admin/creator editing itself still works.
	if _, err := editRank(plain.ID, &tg.InputPeerSelf{}, "pilot2"); err == nil || !strings.Contains(err.Error(), "RIGHT_FORBIDDEN") {
		t.Fatalf("plain member edits own tag with switch off err = %v, want RIGHT_FORBIDDEN", err)
	}
	if _, err := editRank(tagger.ID, &tg.InputPeerSelf{}, "chief2"); err != nil {
		t.Fatalf("admin edits own tag with switch off: %v", err)
	}
	if _, err := editRank(owner.ID, &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}, "captain"); err != nil {
		t.Fatalf("creator edits own tag: %v", err)
	}
	if got, ok := getParticipant(owner.ID, owner.ID).(*tg.ChannelParticipantCreator); !ok {
		t.Fatalf("creator participant = %T, want channelParticipantCreator", getParticipant(owner.ID, owner.ID))
	} else if rank, _ := got.GetRank(); rank != "captain" {
		t.Fatalf("creator rank = %q, want captain", rank)
	}

	// rank too long -> rejected.
	if _, err := editRank(owner.ID, &tg.InputPeerUser{UserID: plain.ID, AccessHash: plain.AccessHash}, strings.Repeat("r", domain.MaxChannelAdminRankLength+1)); err == nil {
		t.Fatalf("over-length rank accepted, want error")
	}

	// editAdmin demotion still clears the rank (old-semantics regression).
	if _, err := r.onChannelsEditAdmin(WithUserID(ctx, owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     inputChannel,
		UserID:      &tg.InputUser{UserID: other.ID, AccessHash: other.AccessHash},
		AdminRights: tg.ChatAdminRights{},
	}); err != nil {
		t.Fatalf("demote other: %v", err)
	}
	demoted, ok := getParticipant(owner.ID, other.ID).(*tg.ChannelParticipant)
	if !ok {
		t.Fatalf("demoted participant = %T, want channelParticipant", getParticipant(owner.ID, other.ID))
	}
	if rank, has := demoted.GetRank(); has && rank != "" {
		t.Fatalf("demoted rank = %q, want cleared", rank)
	}

	// admin log: a rank edit must record participant_edit_rank, and it must be retrievable via the edit_rank filter.
	filter := tg.ChannelAdminLogEventsFilter{}
	filter.SetEditRank(true)
	logReq := &tg.ChannelsGetAdminLogRequest{Channel: inputChannel, Limit: 50}
	logReq.SetEventsFilter(filter)
	adminLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), logReq)
	if err != nil {
		t.Fatalf("get admin log: %v", err)
	}
	if len(adminLog.Events) == 0 {
		t.Fatalf("admin log with edit_rank filter is empty, want participant_edit_rank events")
	}

	// Rejoining is a brand-new participant: an admin (with manage_ranks and a tag) that leaves and
	// rejoins becomes an ordinary member with no admin rights and no rank, and no longer appears in
	// the admins filter (the badge data source).
	if _, err := r.onChannelsLeaveChannel(WithUserID(ctx, tagger.ID), inputChannel); err != nil {
		t.Fatalf("tagger leaves: %v", err)
	}
	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, tagger.ID), inputChannel); err != nil {
		t.Fatalf("tagger rejoins: %v", err)
	}
	rejoined, ok := getParticipant(owner.ID, tagger.ID).(*tg.ChannelParticipant)
	if !ok {
		t.Fatalf("rejoined tagger = %T, want plain channelParticipant", getParticipant(owner.ID, tagger.ID))
	}
	if rank, has := rejoined.GetRank(); has && rank != "" {
		t.Fatalf("rejoined tagger rank = %q, want cleared", rank)
	}
	adminsAfterRejoin, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: inputChannel,
		Filter:  &tg.ChannelParticipantsAdmins{},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("get admins after rejoin: %v", err)
	}
	for _, p := range adminsAfterRejoin.(*tg.ChannelsChannelParticipants).Participants {
		if p, ok := p.(*tg.ChannelParticipantAdmin); ok && p.UserID == tagger.ID {
			t.Fatalf("rejoined tagger still listed as admin in admins filter")
		}
	}

	// kick -> unban -> rejoin: the tag does not come back.
	if _, err := r.onChannelsEditBanned(WithUserID(ctx, owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:      inputChannel,
		Participant:  &tg.InputPeerUser{UserID: plain.ID, AccessHash: plain.AccessHash},
		BannedRights: tg.ChatBannedRights{ViewMessages: true},
	}); err != nil {
		t.Fatalf("kick plain: %v", err)
	}
	if _, err := r.onChannelsEditBanned(WithUserID(ctx, owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:      inputChannel,
		Participant:  &tg.InputPeerUser{UserID: plain.ID, AccessHash: plain.AccessHash},
		BannedRights: tg.ChatBannedRights{},
	}); err != nil {
		t.Fatalf("unban plain: %v", err)
	}
	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, plain.ID), inputChannel); err != nil {
		t.Fatalf("plain rejoins after unban: %v", err)
	}
	if got := getParticipant(owner.ID, plain.ID); true {
		if p, ok := got.(*tg.ChannelParticipant); !ok {
			t.Fatalf("unbanned plain participant = %T, want plain channelParticipant", got)
		} else if rank, has := p.GetRank(); has && rank != "" {
			t.Fatalf("unbanned plain rank = %q, want cleared", rank)
		}
	}

	// When the creator leaves, ownership passes to another active member; the old creator rejoining
	// is an ordinary member and does not keep the creator tag.
	if _, err := r.onChannelsLeaveChannel(WithUserID(ctx, owner.ID), inputChannel); err != nil {
		t.Fatalf("creator leaves: %v", err)
	}
	newCreator, ok := getParticipant(tagger.ID, tagger.ID).(*tg.ChannelParticipantCreator)
	if !ok {
		t.Fatalf("future creator = %T, want channelParticipantCreator", getParticipant(tagger.ID, tagger.ID))
	}
	if rank, has := newCreator.GetRank(); has && rank != "" {
		t.Fatalf("future creator rank = %q, want empty", rank)
	}
	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, owner.ID), inputChannel); err != nil {
		t.Fatalf("creator rejoins: %v", err)
	}
	creatorBack, ok := getParticipant(tagger.ID, owner.ID).(*tg.ChannelParticipant)
	if !ok {
		t.Fatalf("rejoined old creator = %T, want plain channelParticipant", getParticipant(tagger.ID, owner.ID))
	}
	if rank, has := creatorBack.GetRank(); has && rank != "" {
		t.Fatalf("rejoined old creator rank = %q, want cleared", rank)
	}

	// Member Tags are megagroup-only: on a broadcast channel every path (including creator/self) is
	// uniformly MEGAGROUP_ID_INVALID, so the broadcast admins filter stays a pure admin list.
	bcCreated, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Broadcast: true,
		Title:     "Member Tag Broadcast",
	})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	bcChannel := bcCreated.(*tg.Updates).Chats[0].(*tg.Channel)
	bcInput := &tg.InputChannel{ChannelID: bcChannel.ID, AccessHash: bcChannel.AccessHash}
	bcPeer := &tg.InputPeerChannel{ChannelID: bcChannel.ID, AccessHash: bcChannel.AccessHash}
	if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: bcInput,
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: plain.ID, AccessHash: plain.AccessHash}},
	}); err != nil {
		t.Fatalf("invite to broadcast: %v", err)
	}
	bcEditRank := func(actorID int64, participant tg.InputPeerClass, rank string) error {
		_, err := r.onMessagesEditChatParticipantRank(WithUserID(ctx, actorID), &tg.MessagesEditChatParticipantRankRequest{
			Peer:        bcPeer,
			Participant: participant,
			Rank:        rank,
		})
		return err
	}
	for name, attempt := range map[string]func() error{
		"creator tags subscriber": func() error {
			return bcEditRank(owner.ID, &tg.InputPeerUser{UserID: plain.ID, AccessHash: plain.AccessHash}, "vip")
		},
		"subscriber tags self": func() error { return bcEditRank(plain.ID, &tg.InputPeerSelf{}, "fan") },
		"creator tags self":    func() error { return bcEditRank(owner.ID, &tg.InputPeerSelf{}, "boss") },
	} {
		if err := attempt(); err == nil || !strings.Contains(err.Error(), "MEGAGROUP_ID_INVALID") {
			t.Fatalf("%s on broadcast err = %v, want MEGAGROUP_ID_INVALID", name, err)
		}
	}
	for _, event := range adminLog.Events {
		action, ok := event.Action.(*tg.ChannelAdminLogEventActionParticipantEditRank)
		if !ok {
			t.Fatalf("admin log action = %T, want channelAdminLogEventActionParticipantEditRank", event.Action)
		}
		if action.UserID == 0 {
			t.Fatalf("admin log edit rank action missing user: %+v", action)
		}
	}
	latest := adminLog.Events[0].Action.(*tg.ChannelAdminLogEventActionParticipantEditRank)
	if latest.UserID != owner.ID || latest.NewRank != "captain" {
		t.Fatalf("latest edit rank action = %+v, want owner captain", latest)
	}
}
