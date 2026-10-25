package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appcommunities "telesrv/internal/app/communities"
	appdialogs "telesrv/internal/app/dialogs"
	appstories "telesrv/internal/app/stories"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func rpcCommunityTestEffects(snapshot storepkg.CommunityDeliverySnapshot) ([]storepkg.DeliveryEffect, error) {
	effects := make([]storepkg.DeliveryEffect, 0, len(snapshot.Targets))
	for _, target := range snapshot.Targets {
		effects = append(effects, storepkg.AbsoluteDeliveryEffect(storepkg.DeliveryOutboxEnqueue{
			TargetUserID:   target.TargetUserID,
			Payload:        []byte{1},
			RecoveryPolicy: storepkg.OutboxRecoveryAbsoluteReload,
		}))
	}
	return effects, nil
}

func TestCommunityDeliveryEffectsEncodeViewerStateAndOriginExclusion(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	authKeyID := [8]byte{7, 6, 5}
	requestCtx := WithSessionID(WithRawAuthKeyID(WithUserID(context.Background(), 1001), authKeyID), 77)
	community := domain.Community{ID: 3001, AccessHash: 9001, CreatorUserID: 1001, Title: "Atomic Community", Date: 123}
	effects, err := r.communityDeliveryEffects(requestCtx, 1001, 456)(storepkg.CommunityDeliverySnapshot{Targets: []storepkg.CommunityDeliveryTarget{
		{
			TargetUserID: 1001,
			View: domain.CommunityView{
				Community: community,
				Self:      domain.CommunityMember{CommunityID: community.ID, UserID: 1001, Role: domain.CommunityRoleCreator, Status: domain.CommunityMemberActive},
				Channels:  []domain.Channel{{ID: 42, AccessHash: 4200, Title: "Linked", Megagroup: true}},
			},
		},
		{
			TargetUserID: 2002,
			View: domain.CommunityView{
				Community: community,
				Self:      domain.CommunityMember{CommunityID: community.ID, UserID: 2002},
				Forbidden: true,
			},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 {
		t.Fatalf("effects = %+v, want two viewer projections", effects)
	}
	byTarget := map[int64]storepkg.DeliveryEffect{}
	for _, effect := range effects {
		byTarget[effect.TargetUserID] = effect
	}
	actor := byTarget[1001]
	if actor.ExcludeAuthKeyID != authKeyID || actor.ExcludeSessionID != 77 {
		t.Fatalf("actor exclusion = %x/%d", actor.ExcludeAuthKeyID, actor.ExcludeSessionID)
	}
	requester := byTarget[2002]
	if requester.ExcludeAuthKeyID != ([8]byte{}) || requester.ExcludeSessionID != 0 {
		t.Fatalf("cross-user exclusion leaked origin identity = %x/%d", requester.ExcludeAuthKeyID, requester.ExcludeSessionID)
	}
	actorDecoded, err := decodeDeliveryUpdate(actor.Payload)
	if err != nil {
		t.Fatal(err)
	}
	actorUpdates, ok := actorDecoded.(*tg.Updates)
	if !ok || actorUpdates.Date != 456 || len(actorUpdates.Chats) != 2 {
		t.Fatalf("actor payload = %#v", actorDecoded)
	}
	if _, ok := actorUpdates.Chats[0].(*tg.Community); !ok {
		t.Fatalf("actor community = %#v, want full Community", actorUpdates.Chats[0])
	}
	requesterDecoded, err := decodeDeliveryUpdate(requester.Payload)
	if err != nil {
		t.Fatal(err)
	}
	requesterUpdates, ok := requesterDecoded.(*tg.Updates)
	if !ok || len(requesterUpdates.Chats) != 1 {
		t.Fatalf("requester payload = %#v", requesterDecoded)
	}
	if _, ok := requesterUpdates.Chats[0].(*tg.CommunityForbidden); !ok {
		t.Fatalf("requester community = %#v, want CommunityForbidden", requesterUpdates.Chats[0])
	}
}

func communityRPCChannel(t *testing.T, service *appchannels.Service, creator domain.User, title string, members ...domain.User) domain.Channel {
	t.Helper()
	memberIDs := make([]int64, 0, len(members))
	for _, member := range members {
		memberIDs = append(memberIDs, member.ID)
	}
	created, err := service.CreateChannel(context.Background(), creator.ID, domain.CreateChannelRequest{
		CreatorUserID: creator.ID,
		Title:         title,
		Megagroup:     true,
		MemberUserIDs: memberIDs,
		Date:          1_800_100_000,
	})
	if err != nil {
		t.Fatalf("create channel %q: %v", title, err)
	}
	return created.Channel
}

func TestCommunityDialogsSharePinnedLimit(t *testing.T) {
	ctx := WithLayer(context.Background(), communitiesLayer)
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 711, Phone: "15552000011", FirstName: "Pin Owner"})
	if err != nil {
		t.Fatal(err)
	}
	channels := memory.NewChannelStore()
	channelService := appchannels.NewService(channels)
	communityStore := memory.NewCommunityStore(users, channels, nil, nil, memory.NewDeliveryOutboxStore())
	communityService := appcommunities.NewService(communityStore)
	dialogStore := memory.NewDialogStore()
	dialogStore.BindDialogAggregateStores(channels, communityStore)
	r := New(Config{}, Deps{
		Users: appusers.NewService(users), Channels: channelService,
		Communities: communityService, Dialogs: appdialogs.NewService(dialogStore, channels),
	}, zaptest.NewLogger(t), clock.System)

	inputs := make([]*tg.InputChannel, 0, domain.MaxPinnedDialogsMainFolder)
	for i := 0; i < domain.MaxPinnedDialogsMainFolder; i++ {
		channel := communityRPCChannel(t, channelService, owner, "Pinned Community Channel")
		view, err := communityService.Create(ctx, owner.ID, domain.CreateCommunityRequest{
			Title: "Pinned Community", InitialPeer: domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID},
			Visibility: domain.CommunityPeerVisible, Date: 1_800_110_000 + i,
		})
		if err != nil {
			t.Fatalf("create community %d: %v", i, err)
		}
		if _, _, err := communityService.SetCollapsed(ctx, owner.ID, view.Community.ID, true, rpcCommunityTestEffects); err != nil {
			t.Fatalf("collapse community %d: %v", i, err)
		}
		inputs = append(inputs, &tg.InputChannel{ChannelID: view.Community.ID, AccessHash: view.Community.AccessHash})
	}
	for i := 0; i < domain.MaxPinnedDialogsMainFolder-1; i++ {
		input := inputs[i]
		toggle := &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeerCommunity{Community: input}}
		toggle.SetPinned(true)
		ok, err := r.onMessagesToggleDialogPin(WithUserID(ctx, owner.ID), toggle)
		if err != nil || !ok {
			t.Fatalf("pin community %d = %v, %v", i, ok, err)
		}
	}
	joined, err := communityService.ListJoined(ctx, owner.ID)
	if err != nil || len(joined) != domain.MaxPinnedDialogsMainFolder {
		t.Fatalf("joined Communities before ordinary pin = %+v, %v", joined, err)
	}
	for i := 0; i < domain.MaxPinnedDialogsMainFolder-1; i++ {
		if !joined[i].State.Pinned || !joined[i].State.Collapsed {
			t.Fatalf("joined Community %d state before ordinary pin = %+v", i, joined[i].State)
		}
	}
	ordinary := communityRPCChannel(t, channelService, owner, "Ordinary Pinned Channel")
	ordinaryToggle := &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerChannel{
		ChannelID: ordinary.ID, AccessHash: ordinary.AccessHash,
	}}}
	ordinaryToggle.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(WithUserID(ctx, owner.ID), ordinaryToggle); err != nil || !ok {
		t.Fatalf("pin ordinary dialog at shared limit = %v, %v", ok, err)
	}
	pinned, err := r.pinnedDialogsList(ctx, owner.ID, domain.DialogMainFolderID)
	if err != nil {
		t.Fatal(err)
	}
	order := combinedPinnedDialogPeers(pinned)
	if len(order) != domain.MaxPinnedDialogsMainFolder || order[0] != (domain.Peer{Type: domain.PeerTypeChannel, ID: ordinary.ID}) {
		t.Fatalf("combined pinned order = %+v (dialogs=%+v communities=%+v count=%d), want ordinary dialog promoted above Communities", order, pinned.Dialogs, pinned.Communities, pinned.Count)
	}
	legacyPinned, err := r.pinnedDialogsList(WithLayer(ctx, 227), owner.ID, domain.DialogMainFolderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyPinned.Communities) != 0 || len(legacyPinned.Dialogs) != 1 || legacyPinned.Count != 1 {
		t.Fatalf("Layer 227 pinned dialogs = %+v, want only the ordinary pinned dialog", legacyPinned)
	}
	legacyOrdinary := communityRPCChannel(t, channelService, owner, "Legacy Ordinary Pinned Channel")
	legacyToggle := &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerChannel{
		ChannelID: legacyOrdinary.ID, AccessHash: legacyOrdinary.AccessHash,
	}}}
	legacyToggle.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(WithLayer(WithUserID(ctx, owner.ID), 227), legacyToggle); err == nil || ok || !tgerr.Is(err, "PINNED_DIALOGS_TOO_MUCH") {
		t.Fatalf("Layer 227 pin beyond shared account limit = %v, %v", ok, err)
	}
	overLimit := &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeerCommunity{Community: inputs[len(inputs)-1]}}
	overLimit.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(WithUserID(ctx, owner.ID), overLimit); err == nil || ok || !tgerr.Is(err, "PINNED_DIALOGS_TOO_MUCH") {
		t.Fatalf("pin over shared limit = %v, %v", ok, err)
	}
	if _, err := r.onMessagesReorderPinnedDialogs(WithUserID(ctx, owner.ID), &tg.MessagesReorderPinnedDialogsRequest{
		FolderID: domain.DialogArchiveFolderID,
		Order:    []tg.InputDialogPeerClass{&tg.InputDialogPeerCommunity{Community: inputs[0]}},
	}); err == nil || !tgerr.Is(err, "FOLDER_ID_INVALID") {
		t.Fatalf("archive Community reorder error = %v", err)
	}
}

func TestCommunitiesRPCLayer228Lifecycle(t *testing.T) {
	ctx := WithLayer(context.Background(), communitiesLayer)
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 701, Phone: "15552000001", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 702, Phone: "15552000002", FirstName: "Member"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	initial := communityRPCChannel(t, channelService, owner, "Initial", member)
	communityService := appcommunities.NewService(memory.NewCommunityStore(userStore, channelStore, nil, nil, memory.NewDeliveryOutboxStore()))
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, ID: 1,
		Date: 1_800_100_001, ExpireDate: 1_900_100_001, Public: true,
	}}); err != nil {
		t.Fatalf("seed owner story: %v", err)
	}
	r := New(Config{}, Deps{
		Users:       appusers.NewService(userStore),
		Channels:    channelService,
		Communities: communityService,
		Stories:     appstories.NewService(storyStore),
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_800_100_100, 0)})

	createdResult, err := r.onCommunitiesCreate(WithUserID(ctx, owner.ID), &tg.CommunitiesCreateRequest{
		Hidden: true,
		Title:  "Official Community",
		About:  "Layer 228",
		Peer:   &tg.InputPeerChannel{ChannelID: initial.ID, AccessHash: initial.AccessHash},
	})
	if err != nil {
		t.Fatalf("communities.create: %v", err)
	}
	created, ok := createdResult.(*tg.Updates)
	if !ok || len(created.Chats) != 1 {
		t.Fatalf("create result = %#v, want Updates with Community", createdResult)
	}
	community, ok := created.Chats[0].(*tg.Community)
	if !ok || community.Title != "Official Community" || !community.Creator {
		t.Fatalf("create chat = %#v", created.Chats[0])
	}
	inputCommunity := &tg.InputChannel{ChannelID: community.ID, AccessHash: community.AccessHash}

	joinedResult, err := r.onCommunitiesGetJoined(WithUserID(ctx, member.ID))
	if err != nil {
		t.Fatalf("communities.getJoinedCommunities: %v", err)
	}
	joined := joinedResult.(*tg.MessagesChats)
	if len(joined.Chats) != 1 || joined.Chats[0].(*tg.Community).ID != community.ID {
		t.Fatalf("joined communities = %+v", joined.Chats)
	}

	full, err := r.onChannelsGetFullChannel(WithUserID(ctx, member.ID), inputCommunity)
	if err != nil {
		t.Fatalf("channels.getFullChannel community: %v", err)
	}
	communityFull, ok := full.FullChat.(*tg.CommunityFull)
	if !ok || communityFull.About != "Layer 228" || len(communityFull.LinkedPeers) != 1 || len(full.Chats) != 2 {
		t.Fatalf("community full = %#v chats=%+v", full.FullChat, full.Chats)
	}

	collapsedResult, err := r.onCommunitiesToggleCollapsed(WithUserID(ctx, owner.ID), &tg.CommunitiesToggleCommunityCollapsedInDialogsRequest{
		Collapsed: true,
		Community: inputCommunity,
	})
	if err != nil {
		t.Fatalf("toggle collapsed: %v", err)
	}
	collapsed := collapsedResult.(*tg.Updates)
	if len(collapsed.Chats) == 0 || !collapsed.Chats[0].(*tg.Community).CollapsedInDialogs {
		t.Fatalf("collapsed updates = %+v", collapsed.Chats)
	}
	legacyList, err := r.withCommunityDialogList(WithLayer(ctx, 227), owner.ID, domain.DialogFilter{}, domain.DialogList{Count: 7})
	if err != nil || len(legacyList.Communities) != 0 || legacyList.Count != 7 {
		t.Fatalf("Layer 227 community dialog projection = %+v err=%v, want unchanged list", legacyList, err)
	}
	list, err := r.withCommunityDialogList(ctx, owner.ID, domain.DialogFilter{}, domain.DialogList{})
	if err != nil || len(list.Communities) != 1 || list.Count != 1 {
		t.Fatalf("community dialog list = %+v err=%v", list, err)
	}
	dialogs := tgMessagesDialogs(owner.ID, list).(*tg.MessagesDialogs)
	if len(dialogs.Dialogs) != 1 {
		t.Fatalf("dialogs = %+v, want dialogCommunity", dialogs.Dialogs)
	}
	if dialog, ok := dialogs.Dialogs[0].(*tg.DialogCommunity); !ok || dialog.CommunityID != community.ID {
		t.Fatalf("dialog = %#v, want community %d", dialogs.Dialogs[0], community.ID)
	}

	ownedOne := communityRPCChannel(t, channelService, member, "Owned One")
	ownedTwo := communityRPCChannel(t, channelService, member, "Owned Two")
	requestLink := func(channel domain.Channel) {
		t.Helper()
		ok, err := r.onCommunitiesTogglePeerLink(WithUserID(ctx, member.ID), &tg.CommunitiesTogglePeerLinkRequest{
			Visible:   true,
			Community: inputCommunity,
			Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		})
		if err == nil || ok || !tgerr.Is(err, "COMMUNITY_REQUEST_CREATED") {
			t.Fatalf("request link %d = %v, %v", channel.ID, ok, err)
		}
	}
	requestLink(ownedOne)
	requestLink(ownedTwo)

	requests, err := r.onCommunitiesGetPeerLinkRequests(WithUserID(ctx, owner.ID), &tg.CommunitiesGetPeerLinkRequestsRequest{
		Community: inputCommunity,
		Limit:     20,
	})
	if err != nil || requests.TotalCount != 2 || len(requests.Requests) != 2 {
		t.Fatalf("peer link requests = %+v err=%v", requests, err)
	}
	// The Community owner is deliberately not a member of Owned One. Approval
	// must use the request's validated ownership rather than ordinary channel
	// membership access.
	approved, err := r.onCommunitiesTogglePeerLinkRequestApproval(WithUserID(ctx, owner.ID), &tg.CommunitiesTogglePeerLinkRequestApprovalRequest{
		Community: inputCommunity,
		Peer:      &tg.InputPeerChannel{ChannelID: ownedOne.ID, AccessHash: ownedOne.AccessHash},
	})
	if err != nil || !approved {
		t.Fatalf("approve peer link = %v, %v", approved, err)
	}
	approved, err = r.onCommunitiesToggleAllPeerLinkRequestApproval(WithUserID(ctx, owner.ID), &tg.CommunitiesToggleAllPeerLinkRequestApprovalRequest{Community: inputCommunity})
	if err != nil || !approved {
		t.Fatalf("approve all peer links = %v, %v", approved, err)
	}

	joinedChats, err := r.onCommunitiesGetParticipantJoinedChats(WithUserID(ctx, owner.ID), &tg.CommunitiesGetParticipantJoinedChatsRequest{
		Community:   inputCommunity,
		Participant: &tg.InputPeerUser{UserID: member.ID, AccessHash: member.AccessHash},
	})
	if err != nil || len(joinedChats.JoinedChatIDs) != 3 || len(joinedChats.CreatorChatIDs) != 2 {
		t.Fatalf("participant joined chats = %+v err=%v", joinedChats, err)
	}

	participantsResult, err := r.onChannelsGetParticipants(WithUserID(ctx, owner.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: inputCommunity,
		Filter:  &tg.ChannelParticipantsSearch{Q: "memBER"},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("community participants search: %v", err)
	}
	participants := participantsResult.(*tg.ChannelsChannelParticipants)
	if participants.Count != 1 || len(participants.Participants) != 1 {
		t.Fatalf("community participants = %+v", participants)
	}
	adminsResult, err := r.onChannelsGetParticipants(WithUserID(ctx, member.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: inputCommunity,
		Filter:  &tg.ChannelParticipantsAdmins{},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("ordinary member Community admins: %v", err)
	}
	admins := adminsResult.(*tg.ChannelsChannelParticipants)
	if admins.Count != 1 || len(admins.Participants) != 1 {
		t.Fatalf("ordinary member Community admins = %+v, want creator", admins)
	}
	if _, err := r.onChannelsGetParticipants(WithUserID(ctx, member.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: inputCommunity,
		Filter:  &tg.ChannelParticipantsBanned{Q: ""},
		Limit:   100,
	}); err == nil || !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("ordinary member Community banned list err = %v, want CHAT_ADMIN_REQUIRED", err)
	}

	recent, err := r.onStoriesGetPeerMaxIDs(WithUserID(ctx, owner.ID), []tg.InputPeerClass{
		&tg.InputPeerSelf{},
		&tg.InputPeerChannel{ChannelID: community.ID, AccessHash: community.AccessHash},
		&tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("stories.getPeerMaxIDs with Community slot: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("stories.getPeerMaxIDs slots = %d, want 3", len(recent))
	}
	for _, index := range []int{0, 2} {
		if maxID, ok := recent[index].GetMaxID(); !ok || maxID != 1 {
			t.Fatalf("stories.getPeerMaxIDs[%d] max_id = %d ok=%v, want 1 true", index, maxID, ok)
		}
	}
	if maxID, ok := recent[1].GetMaxID(); ok || maxID != 0 || recent[1].Live {
		t.Fatalf("stories.getPeerMaxIDs Community slot = %+v, want empty recentStory", recent[1])
	}
	if _, err := r.onStoriesGetPeerMaxIDs(WithUserID(ctx, owner.ID), []tg.InputPeerClass{
		&tg.InputPeerChannel{ChannelID: community.ID, AccessHash: community.AccessHash + 1},
	}); err == nil || !tgerr.Is(err, "CHANNEL_PRIVATE") {
		t.Fatalf("stories.getPeerMaxIDs Community wrong hash err = %v, want CHANNEL_PRIVATE", err)
	}

	banned, err := r.onCommunitiesToggleParticipantBanned(WithUserID(ctx, owner.ID), &tg.CommunitiesToggleParticipantBannedRequest{
		Community:   inputCommunity,
		Participant: &tg.InputPeerUser{UserID: member.ID, AccessHash: member.AccessHash},
	})
	if err != nil || !banned {
		t.Fatalf("toggle participant banned = %v, %v", banned, err)
	}
	joinedResult, err = r.onCommunitiesGetJoined(WithUserID(ctx, member.ID))
	if err != nil || len(joinedResult.(*tg.MessagesChats).Chats) != 0 {
		t.Fatalf("banned member joined communities = %#v err=%v", joinedResult, err)
	}
}

func TestCommunityPhotoUploadAndDownloadLifecycle(t *testing.T) {
	ctx := WithLayer(context.Background(), communitiesLayer)
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 701, Phone: "15552000001", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	initial := communityRPCChannel(t, channelService, owner, "Initial")
	communityService := appcommunities.NewService(memory.NewCommunityStore(userStore, channelStore, nil, nil, memory.NewDeliveryOutboxStore()))
	files := &fakeFiles{
		getFileFound: true,
		getFileChunk: domain.FileChunk{
			Bytes:    []byte{1, 2, 3, 4},
			MimeType: "image/jpeg",
			Total:    4,
		},
		photos: map[int64]domain.Photo{},
	}
	r := New(Config{DC: 2}, Deps{
		Users:       appusers.NewService(userStore),
		Channels:    channelService,
		Communities: communityService,
		Files:       files,
	}, zaptest.NewLogger(t), clock.System)

	createdResult, err := r.onCommunitiesCreate(WithUserID(ctx, owner.ID), &tg.CommunitiesCreateRequest{
		Title: "Avatar Community",
		Peer:  &tg.InputPeerChannel{ChannelID: initial.ID, AccessHash: initial.AccessHash},
	})
	if err != nil {
		t.Fatalf("communities.create: %v", err)
	}
	community := createdResult.(*tg.Updates).Chats[0].(*tg.Community)
	inputCommunity := &tg.InputChannel{ChannelID: community.ID, AccessHash: community.AccessHash}

	// 1. Set community avatar from a decoded upload with the file flag present.
	upload := &tg.InputChatUploadedPhoto{}
	upload.SetFile(&tg.InputFile{ID: 55001, Parts: 1, Name: "community_avatar.jpg"})
	setResult, err := r.onChannelsEditPhoto(WithUserID(ctx, owner.ID), &tg.ChannelsEditPhotoRequest{
		Channel: inputCommunity,
		Photo:   upload,
	})
	if err != nil {
		t.Fatalf("channels.editPhoto uploaded photo: %v", err)
	}
	updates, ok := setResult.(*tg.Updates)
	if !ok || len(updates.Chats) == 0 {
		t.Fatalf("channels.editPhoto result = %#v, want Updates with Chats", setResult)
	}
	var hasUpdateChannel bool
	for _, u := range updates.Updates {
		if uc, ok := u.(*tg.UpdateChannel); ok && uc.ChannelID == community.ID {
			hasUpdateChannel = true
			break
		}
	}
	if !hasUpdateChannel {
		t.Fatalf("updates.Updates = %+v, want UpdateChannel for community %d", updates.Updates, community.ID)
	}
	commChat, ok := updates.Chats[0].(*tg.Community)
	if !ok {
		t.Fatalf("chats[0] = %T, want *tg.Community", updates.Chats[0])
	}
	chatPhoto, ok := commChat.Photo.(*tg.ChatPhoto)
	if !ok || chatPhoto.PhotoID != 778 || chatPhoto.DCID != 2 {
		t.Fatalf("community Photo = %#v, want PhotoID 778 and DCID 2", commChat.Photo)
	}

	// 2. Full channel check
	fullResult, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), inputCommunity)
	if err != nil {
		t.Fatalf("channels.getFullChannel: %v", err)
	}
	commFull, ok := fullResult.FullChat.(*tg.CommunityFull)
	if !ok {
		t.Fatalf("FullChat = %T, want *tg.CommunityFull", fullResult.FullChat)
	}
	fullPhoto, ok := commFull.ChatPhoto.(*tg.Photo)
	if !ok || fullPhoto.ID != 778 || fullPhoto.DCID != 2 {
		t.Fatalf("fullChat ChatPhoto = %#v, want Photo ID 778 and DCID 2", commFull.ChatPhoto)
	}

	// Downloads (upload.getFile on inputPeerPhotoFileLocation, including the
	// photo_id=0 refusal) are served by the v2 Edge/FileData path
	// (mtprotoedge/filedata_layer_rpc.go), not by the Core router.

}
