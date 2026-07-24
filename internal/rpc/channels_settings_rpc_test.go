package rpc

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appmoderation "telesrv/internal/app/moderation"
	"telesrv/internal/app/readmodel"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type fakeRPCReadModelVersions struct {
	hashes map[store.ReadModelKey]int64
}

func (f *fakeRPCReadModelVersions) ReadModelHash(_ context.Context, model string, ownerUserID int64, peerType domain.PeerType, peerID int64) (int64, bool, error) {
	hash := f.hashes[store.ReadModelKey{Model: model, OwnerUserID: ownerUserID, PeerType: peerType, PeerID: peerID}]
	return hash, hash != 0, nil
}

func (f *fakeRPCReadModelVersions) ReadModelHashes(_ context.Context, keys []store.ReadModelKey) (map[store.ReadModelKey]int64, error) {
	out := make(map[store.ReadModelKey]int64, len(keys))
	for _, key := range keys {
		if hash := f.hashes[key]; hash != 0 {
			out[key] = hash
		}
	}
	return out, nil
}

func TestChannelInputAccessHashIsValidatedRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550001102", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	invited, err := r.onMessagesCreateChat(WithUserID(ctx, owner.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash}},
		Title: "Hash Group",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	channel := invited.Updates.(*tg.Updates).Chats[0].(*tg.Channel)
	badHash := channel.AccessHash + 1
	if badHash == 0 {
		badHash = 1
	}

	if _, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), &tg.InputChannel{ChannelID: channel.ID, AccessHash: badHash}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("get full bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		Message:  "bad hash",
		RandomID: 991,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("send bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesGetPeerSettings(WithUserID(ctx, owner.ID), &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("peer settings bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesToggleDialogPin(WithUserID(ctx, owner.ID), &tg.MessagesToggleDialogPinRequest{
		Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash}},
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("toggle pin bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onFoldersEditPeerFolders(WithUserID(ctx, owner.ID), []tg.InputFolderPeer{{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		FolderID: domain.DialogArchiveFolderID,
	}}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("edit peer folder bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onUpdatesGetChannelDifference(WithUserID(ctx, friend.ID), &tg.UpdatesGetChannelDifferenceRequest{
		Channel: &tg.InputChannel{ChannelID: channel.ID, AccessHash: badHash},
		Filter:  &tg.ChannelMessagesFilterEmpty{},
		Pts:     0,
		Limit:   10,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("difference bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	chats, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{&tg.InputChannel{ChannelID: channel.ID, AccessHash: badHash}})
	if err != nil {
		t.Fatalf("get channels bad access_hash: %v", err)
	}
	if got := len(chats.(*tg.MessagesChats).Chats); got != 0 {
		t.Fatalf("get channels bad access_hash chats = %d, want 0", got)
	}
	mixedChats, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{
		&tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		&tg.InputChannel{ChannelID: channel.ID, AccessHash: badHash},
		&tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("get channels mixed access_hash: %v", err)
	}
	if got := len(mixedChats.(*tg.MessagesChats).Chats); got != 2 {
		t.Fatalf("get channels mixed access_hash chats = %d, want two good refs", got)
	}
	if _, err := r.dialogFilterFromRequest(WithUserID(ctx, owner.ID), owner.ID, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		Limit:      20,
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("get dialogs offset bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	if _, err := r.onMessagesSaveDraft(WithUserID(ctx, owner.ID), &tg.MessagesSaveDraftRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		Message: "draft",
	}); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("save draft bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
	filterReq := &tg.MessagesUpdateDialogFilterRequest{ID: domain.DialogCustomFolderMinID}
	filterReq.SetFilter(&tg.DialogFilter{
		ID:    domain.DialogCustomFolderMinID,
		Title: tg.TextWithEntities{Text: "Channels"},
		IncludePeers: []tg.InputPeerClass{
			&tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash},
		},
	})
	if ok, err := r.onMessagesUpdateDialogFilter(WithUserID(ctx, owner.ID), filterReq); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") || ok {
		t.Fatalf("update dialog filter bad access_hash = %v, %v; want CHANNEL_PRIVATE", ok, err)
	}
	reply := &tg.InputReplyToMessage{ReplyToMsgID: 1}
	reply.SetReplyToPeerID(&tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: badHash})
	sendReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "bad reply hash",
		RandomID: 992,
	}
	sendReq.SetReplyTo(reply)
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), sendReq); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("send reply_to_peer bad access_hash err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
}

func TestChannelsGetChannelsUsesBatchServiceRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550003101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	first, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Batch One",
		Broadcast: true,
		Date:      1700001300,
	})
	if err != nil {
		t.Fatalf("create first channel: %v", err)
	}
	second, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Batch Two",
		Megagroup: true,
		Date:      1700001310,
	})
	if err != nil {
		t.Fatalf("create second channel: %v", err)
	}
	counting := &countingChannelsService{Service: channelService}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: counting,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	chats, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{
		&tg.InputChannel{ChannelID: first.Channel.ID, AccessHash: first.Channel.AccessHash},
		&tg.InputChannel{ChannelID: second.Channel.ID, AccessHash: second.Channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("get channels batch: %v", err)
	}
	if got := len(chats.(*tg.MessagesChats).Chats); got != 2 {
		t.Fatalf("get channels returned %d chats, want 2", got)
	}
	if counting.getChannelsCalls != 1 || counting.getChannelCalls != 0 {
		t.Fatalf("channel service calls: GetChannels=%d GetChannel=%d, want one batch call only", counting.getChannelsCalls, counting.getChannelCalls)
	}
}

func TestMessagesGetChatsUsesBatchServiceRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550004101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	first, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Legacy Batch One",
		Broadcast: true,
		Date:      1700001400,
	})
	if err != nil {
		t.Fatalf("create first channel: %v", err)
	}
	second, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Legacy Batch Two",
		Megagroup: true,
		Date:      1700001410,
	})
	if err != nil {
		t.Fatalf("create second channel: %v", err)
	}
	counting := &countingChannelsService{Service: channelService}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: counting,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	chats, err := r.onMessagesGetChats(WithUserID(ctx, owner.ID), []int64{
		first.Channel.ID,
		second.Channel.ID,
		first.Channel.ID,
		0,
	})
	if err != nil {
		t.Fatalf("messages.getChats batch: %v", err)
	}
	got := chats.(*tg.MessagesChats).Chats
	if len(got) != 3 {
		t.Fatalf("messages.getChats returned %d chats, want duplicate-preserving 3", len(got))
	}
	if got[0].(*tg.Channel).ID != first.Channel.ID || got[1].(*tg.Channel).ID != second.Channel.ID || got[2].(*tg.Channel).ID != first.Channel.ID {
		t.Fatalf("messages.getChats order = %+v, want first, second, first", got)
	}
	if counting.getChannelsCalls != 1 || counting.getChannelCalls != 0 {
		t.Fatalf("channel service calls: GetChannels=%d GetChannel=%d, want one batch call only", counting.getChannelsCalls, counting.getChannelCalls)
	}
}

func TestChannelsEditPhotoReturnsServiceMessageUpdatesRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 91, Phone: "15550009101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 92, Phone: "15550009102", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Photo RPC",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700001900,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	files := &fakeFiles{}
	photo := files.putPhoto(domain.Photo{
		ID:         9901,
		AccessHash: 9902,
		DCID:       2,
		Date:       1700001901,
		Sizes: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindStripped, Type: "i", Bytes: []byte{4, 5, 6}},
			{Kind: domain.PhotoSizeKindDefault, Type: "m", W: 160, H: 160, Size: 4096},
		},
	})
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
		Files:    files,
	}, zaptest.NewLogger(t), clock.System)
	input := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	setResult, err := r.onChannelsEditPhoto(WithUserID(ctx, owner.ID), &tg.ChannelsEditPhotoRequest{
		Channel: input,
		Photo:   &tg.InputChatPhoto{ID: &tg.InputPhoto{ID: photo.ID, AccessHash: photo.AccessHash}},
	})
	if err != nil {
		t.Fatalf("channels.editPhoto set: %v", err)
	}
	setAction := requireChannelPhotoServiceAction(t, setResult, created.Channel.ID)
	editPhoto, ok := setAction.(*tg.MessageActionChatEditPhoto)
	if !ok {
		t.Fatalf("set action = %T, want MessageActionChatEditPhoto", setAction)
	}
	tgPhoto, ok := editPhoto.Photo.(*tg.Photo)
	if !ok || tgPhoto.ID != photo.ID {
		t.Fatalf("set action photo = %#v, want tg.Photo id %d", editPhoto.Photo, photo.ID)
	}

	clearResult, err := r.onChannelsEditPhoto(WithUserID(ctx, owner.ID), &tg.ChannelsEditPhotoRequest{
		Channel: input,
		Photo:   &tg.InputChatPhotoEmpty{},
	})
	if err != nil {
		t.Fatalf("channels.editPhoto clear: %v", err)
	}
	clearAction := requireChannelPhotoServiceAction(t, clearResult, created.Channel.ID)
	if _, ok := clearAction.(*tg.MessageActionChatDeletePhoto); !ok {
		t.Fatalf("clear action = %T, want MessageActionChatDeletePhoto", clearAction)
	}
}

func requireChannelPhotoServiceAction(t *testing.T, updatesClass tg.UpdatesClass, channelID int64) tg.MessageActionClass {
	t.Helper()
	updates, ok := updatesClass.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updatesClass)
	}
	hasUpdateChannel := false
	var action tg.MessageActionClass
	for _, update := range updates.Updates {
		switch item := update.(type) {
		case *tg.UpdateChannel:
			if item.ChannelID == channelID {
				hasUpdateChannel = true
			}
		case *tg.UpdateNewChannelMessage:
			service, ok := item.Message.(*tg.MessageService)
			if !ok {
				t.Fatalf("new channel message = %T, want MessageService", item.Message)
			}
			action = service.Action
		}
	}
	if !hasUpdateChannel {
		t.Fatalf("updates = %+v, want UpdateChannel for %d", updates.Updates, channelID)
	}
	if action == nil {
		t.Fatalf("updates = %+v, want UpdateNewChannelMessage service action", updates.Updates)
	}
	if len(updates.Chats) == 0 {
		t.Fatalf("updates chats empty, want channel projection")
	}
	if len(updates.Users) == 0 {
		t.Fatalf("updates users empty, want service sender projection")
	}
	return action
}

func TestMessagesGetPeerSettingsUsesResolveChannelForAccessCheck(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550005101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Peer Settings Channel",
		Broadcast: true,
		Date:      1700001500,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	counting := &countingChannelsService{Service: channelService}
	r := New(Config{}, Deps{Channels: counting}, zaptest.NewLogger(t), clock.System)
	input := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	if _, err := r.onMessagesGetPeerSettings(WithUserID(ctx, owner.ID), input); err != nil {
		t.Fatalf("messages.getPeerSettings: %v", err)
	}
	if counting.resolveChannelCalls != 1 || counting.getChannelCalls != 0 {
		t.Fatalf("channel access check calls: ResolveChannel=%d GetChannel=%d, want one resolve and no full get", counting.resolveChannelCalls, counting.getChannelCalls)
	}
	bad := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash + 1}
	if _, err := r.onMessagesGetPeerSettings(WithUserID(ctx, owner.ID), bad); err == nil || !strings.Contains(err.Error(), "CHANNEL_PRIVATE") {
		t.Fatalf("bad access_hash err = %v, want CHANNEL_PRIVATE", err)
	}
}

func TestChannelsGetFullChannelUsesChannelReadModel(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550006101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	creator := appchannels.NewService(channelStore)
	created, err := creator.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Full Read Model",
		Megagroup: true,
		Date:      1700004200,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	versions := &fakeRPCReadModelVersions{hashes: map[store.ReadModelKey]int64{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}:          101,
		{Model: readmodel.ModelChannelMember, OwnerUserID: owner.ID, PeerType: peer.Type, PeerID: peer.ID}: 202,
		{Model: readmodel.ModelDialogLight, OwnerUserID: owner.ID, PeerType: peer.Type, PeerID: peer.ID}:   303,
	}}
	counting := &countingChannelsService{Service: appchannels.NewService(channelStore, appchannels.WithReadModelVersions(versions))}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: counting,
	}, zaptest.NewLogger(t), clock.System)
	input := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	if _, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input); err != nil {
		t.Fatalf("first getFullChannel: %v", err)
	}
	if _, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input); err != nil {
		t.Fatalf("second getFullChannel: %v", err)
	}
	if counting.getChannelReadModelCalls != 1 || counting.getChannelCalls != 0 {
		t.Fatalf("channel calls readModel/GetChannel = %d/%d, want 1/0 after projection cache hit", counting.getChannelReadModelCalls, counting.getChannelCalls)
	}
}

type countingChannelsService struct {
	*appchannels.Service
	getChannelCalls          int
	getChannelReadModelCalls int
	resolveChannelCalls      int
	getChannelsCalls         int
}

func (s *countingChannelsService) GetChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error) {
	s.getChannelCalls++
	return s.Service.GetChannel(ctx, userID, channelID)
}

func (s *countingChannelsService) GetChannelReadModel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error) {
	s.getChannelReadModelCalls++
	return s.Service.GetChannelReadModel(ctx, userID, channelID)
}

func (s *countingChannelsService) ResolveChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error) {
	s.resolveChannelCalls++
	return s.Service.ResolveChannel(ctx, userID, channelID)
}

func (s *countingChannelsService) GetChannels(ctx context.Context, userID int64, channelIDs []int64) ([]domain.ChannelView, error) {
	s.getChannelsCalls++
	return s.Service.GetChannels(ctx, userID, channelIDs)
}

func TestChannelsCreateChannelUnsupportedOptionsReturnExplicitErrors(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 73, Phone: "15550002173", FirstName: "Owner"})
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(memory.NewChannelStore()),
	}, zaptest.NewLogger(t), clock.System)

	tests := []struct {
		name string
		req  *tg.ChannelsCreateChannelRequest
		want string
	}{
		{
			name: "history import",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Import Group",
				Megagroup: true,
				ForImport: true,
			},
			want: "CHAT_INVALID",
		},
		{
			name: "geogroup",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Geo Group",
				Megagroup: true,
				GeoPoint:  &tg.InputGeoPoint{Lat: 1.2, Long: 3.4},
				Address:   "somewhere",
			},
			want: "ADDRESS_INVALID",
		},
		{
			name: "address without geo",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Bad Geo Group",
				Megagroup: true,
				Address:   "somewhere",
			},
			want: "ADDRESS_INVALID",
		},
		{
			name: "negative ttl",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Bad TTL Group",
				Megagroup: true,
				TTLPeriod: -1,
			},
			want: "TTL_PERIOD_INVALID",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("create channel err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestChannelsGetFullChannelCanSetUsernameOnlyForCreator(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 56, Phone: "15550002211", FirstName: "Owner"})
	member, _ := userStore.Create(ctx, domain.User{AccessHash: 57, Phone: "15550002212", FirstName: "Member"})
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
	}, zaptest.NewLogger(t), clock.System)

	tests := []struct {
		name string
		req  *tg.ChannelsCreateChannelRequest
	}{
		{
			name: "broadcast",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Editable Broadcast",
				About:     "public link editable",
				Broadcast: true,
			},
		},
		{
			name: "megagroup",
			req: &tg.ChannelsCreateChannelRequest{
				Title:     "Editable Megagroup",
				About:     "public link editable",
				Megagroup: true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), tc.req)
			if err != nil {
				t.Fatalf("create channel: %v", err)
			}
			channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
			input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

			ownerFull, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
			if err != nil {
				t.Fatalf("owner get full channel: %v", err)
			}
			ownerChannelFull := ownerFull.FullChat.(*tg.ChannelFull)
			if !ownerChannelFull.CanSetUsername || !ownerChannelFull.CanDeleteChannel {
				t.Fatalf("owner full flags can_set_username=%v can_delete_channel=%v, want both true", ownerChannelFull.CanSetUsername, ownerChannelFull.CanDeleteChannel)
			}

			if _, err := r.onChannelsInviteToChannel(WithUserID(ctx, owner.ID), &tg.ChannelsInviteToChannelRequest{
				Channel: input,
				Users:   []tg.InputUserClass{&tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash}},
			}); err != nil {
				t.Fatalf("invite member: %v", err)
			}
			memberFull, err := r.onChannelsGetFullChannel(WithUserID(ctx, member.ID), input)
			if err != nil {
				t.Fatalf("member get full channel: %v", err)
			}
			memberChannelFull := memberFull.FullChat.(*tg.ChannelFull)
			if memberChannelFull.CanSetUsername || memberChannelFull.CanDeleteChannel {
				t.Fatalf("member full flags can_set_username=%v can_delete_channel=%v, want both false", memberChannelFull.CanSetUsername, memberChannelFull.CanDeleteChannel)
			}
		})
	}
}

func TestChannelUsernameAndManagementRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550002301", FirstName: "Owner"})
	requester, _ := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550002302", FirstName: "Requester"})
	channelStore := memory.NewChannelStore()
	moderationStore := memory.NewModerationReportStore()
	userService := appusers.NewService(userStore)
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    userService,
		Channels: channelService,
		Moderation: appmoderation.NewService(
			moderationStore,
			appmoderation.WithMessageReaders(nil, channelService),
			appmoderation.WithPeerReaders(userService, channelService),
		),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Public Team",
		About:     "public",
		Megagroup: true,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "channel management seed",
		RandomID: 991,
	})
	if err != nil {
		t.Fatalf("send seed message: %v", err)
	}
	msgID := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	if ok, err := r.onChannelsReportAntiSpamFalsePositive(
		WithUserID(ctx, owner.ID),
		&tg.ChannelsReportAntiSpamFalsePositiveRequest{Channel: input, MsgID: msgID},
	); err == nil || ok || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("report anti-spam without native decision = ok %v err %v, want MESSAGE_ID_INVALID", ok, err)
	}
	antiSpamDecision, err := domain.NewChannelAntiSpamDecision(
		channel.ID, msgID, owner.ID,
		[]byte(`{"schema_version":1,"source":"native_antispam"}`),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := moderationStore.CreateChannelAntiSpamDecision(ctx, antiSpamDecision); err != nil {
		t.Fatal(err)
	}

	okUsername, err := r.onChannelsCheckUsername(WithUserID(ctx, owner.ID), &tg.ChannelsCheckUsernameRequest{
		Channel:  input,
		Username: "public_team",
	})
	if err != nil || !okUsername {
		t.Fatalf("check username = ok %v err %v, want true", okUsername, err)
	}
	updated, err := r.onChannelsUpdateUsername(WithUserID(ctx, owner.ID), &tg.ChannelsUpdateUsernameRequest{
		Channel:  input,
		Username: "public_team",
	})
	if err != nil || !updated {
		t.Fatalf("update username = %v err %v, want true", updated, err)
	}
	admined, err := r.onChannelsGetAdminedPublicChannels(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminedPublicChannelsRequest{})
	if err != nil {
		t.Fatalf("get admined public channels: %v", err)
	}
	adminedChats := admined.(*tg.MessagesChats).Chats
	if len(adminedChats) != 1 || adminedChats[0].(*tg.Channel).Username != "public_team" {
		t.Fatalf("admined chats = %+v, want public channel with username", adminedChats)
	}

	signatures, err := r.onChannelsToggleSignatures(WithUserID(ctx, owner.ID), &tg.ChannelsToggleSignaturesRequest{
		Channel:           input,
		SignaturesEnabled: true,
	})
	if err != nil {
		t.Fatalf("toggle signatures: %v", err)
	}
	if signed := signatures.(*tg.Updates).Chats[0].(*tg.Channel); !signed.Signatures {
		t.Fatalf("signed channel = %+v, want signatures enabled", signed)
	}
	if _, err := r.onChannelsToggleSlowMode(WithUserID(ctx, owner.ID), &tg.ChannelsToggleSlowModeRequest{Channel: input, Seconds: 7}); err == nil {
		t.Fatalf("toggle slow mode invalid seconds err = nil, want SECONDS_INVALID")
	}
	slowUpdates, err := r.onChannelsToggleSlowMode(WithUserID(ctx, owner.ID), &tg.ChannelsToggleSlowModeRequest{Channel: input, Seconds: 30})
	if err != nil {
		t.Fatalf("toggle slow mode valid: %v", err)
	}
	if slowChannel := slowUpdates.(*tg.Updates).Chats[0].(*tg.Channel); !slowChannel.SlowmodeEnabled {
		t.Fatalf("slow mode channel = %+v, want slowmode enabled", slowChannel)
	}
	if ok, err := r.onChannelsSetStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetStickersRequest{Channel: input, Stickerset: &tg.InputStickerSetEmpty{}}); err != nil || !ok {
		t.Fatalf("set stickers = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsSetStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetStickersRequest{
		Channel:    input,
		Stickerset: &tg.InputStickerSetID{ID: 1, AccessHash: 2},
	}); err == nil || !strings.Contains(err.Error(), "STICKERSET_INVALID") {
		t.Fatalf("set stickers non-empty err = %v, want STICKERSET_INVALID", err)
	}
	if ok, err := r.onChannelsReorderUsernames(WithUserID(ctx, owner.ID), &tg.ChannelsReorderUsernamesRequest{Channel: input, Order: []string{"public_team"}}); err != nil || !ok {
		t.Fatalf("reorder usernames = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsTogglePreHistoryHidden(WithUserID(ctx, owner.ID), &tg.ChannelsTogglePreHistoryHiddenRequest{Channel: input, Enabled: true}); err != nil {
		t.Fatalf("toggle prehistory hidden: %v", err)
	}
	fullAfterSettings, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("get full channel after settings: %v", err)
	}
	fullSettings := fullAfterSettings.FullChat.(*tg.ChannelFull)
	if !fullSettings.HiddenPrehistory || fullSettings.SlowmodeSeconds != 30 {
		t.Fatalf("full settings = %+v, want hidden prehistory and slowmode=30", fullSettings)
	}
	joinToSendUpdates, err := r.onChannelsToggleJoinToSend(WithUserID(ctx, owner.ID), &tg.ChannelsToggleJoinToSendRequest{Channel: input, Enabled: true})
	if err != nil {
		t.Fatalf("toggle join-to-send: %v", err)
	}
	if joinToSendChannel := joinToSendUpdates.(*tg.Updates).Chats[0].(*tg.Channel); !joinToSendChannel.GetJoinToSend() {
		t.Fatalf("join-to-send channel = %+v, want join_to_send flag", joinToSendChannel)
	}
	joinRequestUpdates, err := r.onChannelsToggleJoinRequest(WithUserID(ctx, owner.ID), &tg.ChannelsToggleJoinRequestRequest{Channel: input, Enabled: true})
	if err != nil {
		t.Fatalf("toggle join request: %v", err)
	}
	if joinRequestChannel := joinRequestUpdates.(*tg.Updates).Chats[0].(*tg.Channel); !joinRequestChannel.GetJoinRequest() {
		t.Fatalf("join-request channel = %+v, want join_request flag", joinRequestChannel)
	}
	chatsWithJoinFlags, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channels after join settings: %v", err)
	}
	listedJoinChannel := chatsWithJoinFlags.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if !listedJoinChannel.GetJoinToSend() || !listedJoinChannel.GetJoinRequest() {
		t.Fatalf("listed channel join flags = %+v, want both flags", listedJoinChannel)
	}
	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, requester.ID), input); err == nil || !strings.Contains(err.Error(), "INVITE_REQUEST_SENT") {
		t.Fatalf("public join request err = %v, want INVITE_REQUEST_SENT", err)
	}
	pendingPublic, err := r.onMessagesGetChatInviteImporters(WithUserID(ctx, owner.ID), &tg.MessagesGetChatInviteImportersRequest{
		Requested: true,
		Peer:      &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("get public pending join request: %v", err)
	}
	if pendingPublic.Count != 1 || len(pendingPublic.Importers) != 1 || pendingPublic.Importers[0].UserID != requester.ID || !pendingPublic.Importers[0].Requested {
		t.Fatalf("public pending importers = %+v, want requester pending", pendingPublic)
	}
	fullWithPublicPending, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("get full channel with public pending request: %v", err)
	}
	fullPublicPending := fullWithPublicPending.FullChat.(*tg.ChannelFull)
	publicPendingCount, ok := fullPublicPending.GetRequestsPending()
	publicRecent, recentOK := fullPublicPending.GetRecentRequesters()
	if !ok || publicPendingCount != 1 || !recentOK || len(publicRecent) != 1 || publicRecent[0] != requester.ID {
		t.Fatalf("public full pending = count %d ok %v recent %+v ok %v, want requester", publicPendingCount, ok, publicRecent, recentOK)
	}
	approvedPublic, err := r.onMessagesHideChatJoinRequest(WithUserID(ctx, owner.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		UserID:   &tg.InputUser{UserID: requester.ID, AccessHash: requester.AccessHash},
	})
	if err != nil {
		t.Fatalf("approve public join request: %v", err)
	}
	var publicPendingCleared *tg.UpdatePendingJoinRequests
	for _, update := range approvedPublic.(*tg.Updates).Updates {
		if pending, ok := update.(*tg.UpdatePendingJoinRequests); ok {
			publicPendingCleared = pending
			break
		}
	}
	if publicPendingCleared == nil || publicPendingCleared.RequestsPending != 0 || len(publicPendingCleared.RecentRequesters) != 0 {
		t.Fatalf("public approve pending update = %+v, want cleared pending requests", publicPendingCleared)
	}
	colorReq := &tg.ChannelsUpdateColorRequest{Channel: input}
	colorReq.SetForProfile(true)
	colorReq.SetColor(1)
	colorReq.SetBackgroundEmojiID(9001)
	colorUpdates, err := r.onChannelsUpdateColor(WithUserID(ctx, owner.ID), colorReq)
	if err != nil {
		t.Fatalf("update color: %v", err)
	}
	colorChannel := colorUpdates.(*tg.Updates).Chats[0].(*tg.Channel)
	profileColor, ok := colorChannel.GetProfileColor()
	if !ok {
		t.Fatalf("update color channel = %+v, want profile color", colorChannel)
	}
	if peerColor := profileColor.(*tg.PeerColor); peerColor.Color != 1 || peerColor.BackgroundEmojiID != 9001 {
		t.Fatalf("profile color = %+v, want color/background", peerColor)
	}
	status := &tg.EmojiStatus{DocumentID: 9101}
	status.SetUntil(1700000100)
	statusUpdates, err := r.onChannelsUpdateEmojiStatus(WithUserID(ctx, owner.ID), &tg.ChannelsUpdateEmojiStatusRequest{Channel: input, EmojiStatus: status})
	if err != nil {
		t.Fatalf("update emoji status: %v", err)
	}
	statusChannel := statusUpdates.(*tg.Updates).Chats[0].(*tg.Channel)
	emojiStatus, ok := statusChannel.GetEmojiStatus()
	if !ok {
		t.Fatalf("update emoji status channel = %+v, want emoji status", statusChannel)
	}
	if got := emojiStatus.(*tg.EmojiStatus); got.DocumentID != 9101 || got.Until != 1700000100 {
		t.Fatalf("emoji status = %+v, want document/until", got)
	}
	chatsWithAppearance, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channels after appearance settings: %v", err)
	}
	persistedAppearance := chatsWithAppearance.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if _, ok := persistedAppearance.GetProfileColor(); !ok {
		t.Fatalf("persisted channel appearance = %+v, want profile color", persistedAppearance)
	}
	if _, ok := persistedAppearance.GetEmojiStatus(); !ok {
		t.Fatalf("persisted channel appearance = %+v, want emoji status", persistedAppearance)
	}
	link, err := r.onChannelsExportMessageLink(WithUserID(ctx, owner.ID), &tg.ChannelsExportMessageLinkRequest{Channel: input, ID: msgID})
	if err != nil {
		t.Fatalf("export message link: %v", err)
	}
	if !strings.Contains(link.Link, "public_team") || !strings.Contains(link.Link, strconv.Itoa(msgID)) {
		t.Fatalf("exported link = %+v, want username/message id", link)
	}
	replyToSeed := &tg.InputReplyToMessage{ReplyToMsgID: msgID}
	replyReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "thread reply",
		RandomID: 992,
	}
	replyReq.SetReplyTo(replyToSeed)
	replySent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), replyReq)
	if err != nil {
		t.Fatalf("send reply for export link: %v", err)
	}
	replyMsgID := replySent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	threadLink, err := r.onChannelsExportMessageLink(WithUserID(ctx, owner.ID), &tg.ChannelsExportMessageLinkRequest{Thread: true, Channel: input, ID: replyMsgID})
	if err != nil {
		t.Fatalf("export thread message link: %v", err)
	}
	if !strings.Contains(threadLink.Link, strconv.Itoa(replyMsgID)) || !strings.Contains(threadLink.Link, "?thread="+strconv.Itoa(msgID)) {
		t.Fatalf("thread exported link = %+v, want reply id and thread root %d", threadLink, msgID)
	}
	if _, err := r.onChannelsExportMessageLink(WithUserID(ctx, owner.ID), &tg.ChannelsExportMessageLinkRequest{Channel: input, ID: replyMsgID + 10000}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("export missing message link err = %v, want MESSAGE_ID_INVALID", err)
	}
	if ok, err := r.onChannelsReadMessageContents(WithUserID(ctx, owner.ID), &tg.ChannelsReadMessageContentsRequest{Channel: input, ID: []int{msgID}}); err != nil || !ok {
		t.Fatalf("read message contents = ok %v err %v, want true", ok, err)
	}
	author, err := r.onChannelsGetMessageAuthor(WithUserID(ctx, owner.ID), &tg.ChannelsGetMessageAuthorRequest{Channel: input, ID: msgID})
	if err != nil {
		t.Fatalf("get message author: %v", err)
	}
	if author.(*tg.User).ID != owner.ID {
		t.Fatalf("message author = %+v, want owner", author)
	}
	if _, err := r.onChannelsGetMessageAuthor(WithUserID(ctx, owner.ID), &tg.ChannelsGetMessageAuthorRequest{Channel: input, ID: msgID + 10000}); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("get missing message author err = %v, want MESSAGE_ID_INVALID", err)
	}
	if ok, err := r.onChannelsReportSpam(WithUserID(ctx, owner.ID), &tg.ChannelsReportSpamRequest{
		Channel:     input,
		Participant: &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash},
		ID:          []int{msgID},
	}); err != nil || !ok {
		t.Fatalf("report spam = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsGetLeftChannels(WithUserID(ctx, owner.ID), 0); err != nil {
		t.Fatalf("get left channels: %v", err)
	}
	if _, err := r.onChannelsGetInactiveChannels(WithUserID(ctx, owner.ID)); err != nil {
		t.Fatalf("get inactive channels: %v", err)
	}
	if _, err := r.onChannelsGetGroupsForDiscussion(WithUserID(ctx, owner.ID)); err != nil {
		t.Fatalf("get groups for discussion: %v", err)
	}
	if _, err := r.onChannelsSetDiscussionGroup(WithUserID(ctx, owner.ID), &tg.ChannelsSetDiscussionGroupRequest{Broadcast: input, Group: &tg.InputChannelEmpty{}}); err == nil || !strings.Contains(err.Error(), "BROADCAST_ID_INVALID") {
		t.Fatalf("set discussion group on megagroup err = %v, want BROADCAST_ID_INVALID", err)
	}
	if ok, err := r.onChannelsEditLocation(WithUserID(ctx, owner.ID), &tg.ChannelsEditLocationRequest{Channel: input}); err != nil || !ok {
		t.Fatalf("edit location = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsConvertToGigagroup(WithUserID(ctx, owner.ID), input); err != nil {
		t.Fatalf("convert to gigagroup: %v", err)
	}
	// 建群服务消息（id=1）被保留为会话兜底 top message，不随创建者
	// 历史一起删除；可删的是 seed 文本与 thread reply 两条。
	if affected, err := r.onChannelsDeleteParticipantHistory(WithUserID(ctx, owner.ID), &tg.ChannelsDeleteParticipantHistoryRequest{Channel: input, Participant: &tg.InputPeerUser{UserID: owner.ID, AccessHash: owner.AccessHash}}); err != nil || affected.PtsCount != 2 || affected.Pts == 0 || affected.Offset != 0 {
		t.Fatalf("delete participant history = %+v err %v, want one bounded delete update for owner text and reply messages", affected, err)
	}
	if updates, err := r.onChannelsToggleParticipantsHidden(WithUserID(ctx, owner.ID), &tg.ChannelsToggleParticipantsHiddenRequest{Channel: input, Enabled: true}); err != nil {
		t.Fatalf("toggle participants hidden: %v", err)
	} else if len(updates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("toggle participants hidden updates = %+v, want channel chat", updates)
	}
	fullHidden, err := r.onChannelsGetFullChannel(WithUserID(ctx, requester.ID), input)
	if err != nil {
		t.Fatalf("requester get full after participants hidden: %v", err)
	}
	fullChannel := fullHidden.FullChat.(*tg.ChannelFull)
	if hidden := fullChannel.GetParticipantsHidden(); !hidden || fullChannel.CanViewParticipants {
		t.Fatalf("requester full channel = %+v, want participants_hidden and can_view_participants=false", fullChannel)
	}
	hiddenMembers, err := r.onChannelsGetParticipants(WithUserID(ctx, requester.ID), &tg.ChannelsGetParticipantsRequest{
		Channel: input,
		Filter:  &tg.ChannelParticipantsRecent{},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("requester get participants hidden: %v", err)
	}
	if page := hiddenMembers.(*tg.ChannelsChannelParticipants); len(page.Participants) != 0 || page.Count == 0 {
		t.Fatalf("hidden participants page = %+v, want empty page with aggregate count", page)
	}
	viewAsMessagesUpdates, err := r.onChannelsToggleViewForumAsMessages(WithUserID(ctx, owner.ID), &tg.ChannelsToggleViewForumAsMessagesRequest{Channel: input, Enabled: true})
	if err != nil {
		t.Fatalf("toggle forum as messages: %v", err)
	}
	viewAsMessagesUpdate, ok := viewAsMessagesUpdates.(*tg.Updates).Updates[0].(*tg.UpdateChannelViewForumAsMessages)
	if !ok || viewAsMessagesUpdate.ChannelID != channel.ID || !viewAsMessagesUpdate.Enabled {
		t.Fatalf("toggle forum as messages updates = %+v, want updateChannelViewForumAsMessages", viewAsMessagesUpdates)
	}
	fullWithViewAsMessages, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("full channel after view as messages: %v", err)
	}
	if full := fullWithViewAsMessages.FullChat.(*tg.ChannelFull); !full.ViewForumAsMessages {
		t.Fatalf("full channel view_forum_as_messages = false, want true")
	}
	forumUpdates, err := r.onChannelsToggleForum(WithUserID(ctx, owner.ID), &tg.ChannelsToggleForumRequest{Channel: input, Enabled: true, Tabs: true})
	if err != nil {
		t.Fatalf("toggle forum: %v", err)
	}
	if len(forumUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("toggle forum updates = %+v, want channel chat", forumUpdates)
	}
	forumChat, ok := forumUpdates.(*tg.Updates).Chats[0].(*tg.Channel)
	if !ok || !forumChat.Forum || !forumChat.ForumTabs {
		t.Fatalf("toggle forum chat = %+v, want forum+forum_tabs", forumUpdates.(*tg.Updates).Chats[0])
	}
	forumPeer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	if _, err := r.onMessagesGetForumTopicsByID(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsByIDRequest{
		Peer: forumPeer,
	}); err == nil || !strings.Contains(err.Error(), "TOPICS_EMPTY") {
		t.Fatalf("messages.getForumTopicsByID empty topics err = %v, want TOPICS_EMPTY", err)
	}
	forumTopics, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics after toggle: %v", err)
	}
	if forumTopics.Count != 1 || len(forumTopics.Topics) != 1 || len(forumTopics.Chats) != 1 || forumTopics.Pts == 0 {
		t.Fatalf("messages.getForumTopics after toggle = %+v, want general topic with channel context", forumTopics)
	}
	generalTopic, ok := forumTopics.Topics[0].(*tg.ForumTopic)
	if !ok || generalTopic.ID != forumGeneralTopicID || generalTopic.Title != "General" {
		t.Fatalf("forum topic = %+v, want General topic", forumTopics.Topics[0])
	}
	forumTopicsByID, err := r.onMessagesGetForumTopicsByID(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   forumPeer,
		Topics: []int{forumGeneralTopicID},
	})
	if err != nil {
		t.Fatalf("messages.getForumTopicsByID after toggle: %v", err)
	}
	if forumTopicsByID.Count != 1 || len(forumTopicsByID.Topics) != 1 {
		t.Fatalf("messages.getForumTopicsByID after toggle = %+v, want General topic", forumTopicsByID)
	}
	createdForumTopic, err := r.onMessagesCreateForumTopic(WithUserID(ctx, owner.ID), &tg.MessagesCreateForumTopicRequest{
		Peer:      forumPeer,
		Title:     "Ops",
		IconColor: domain.DefaultForumTopicIconColor,
		RandomID:  17803001,
	})
	if err != nil {
		t.Fatalf("messages.createForumTopic after toggle: %v", err)
	}
	var topicID int
	for _, update := range createdForumTopic.(*tg.Updates).Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		msg, ok := newMsg.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		action, ok := msg.Action.(*tg.MessageActionTopicCreate)
		if ok && action.Title == "Ops" {
			topicID = msg.ID
			break
		}
	}
	if topicID == 0 {
		t.Fatalf("messages.createForumTopic updates = %+v, want topic root service message", createdForumTopic)
	}
	forumTopicsWithCreated, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics with created topic: %v", err)
	}
	if forumTopicsWithCreated.Count != 2 || len(forumTopicsWithCreated.Topics) != 2 || len(forumTopicsWithCreated.Messages) == 0 {
		t.Fatalf("messages.getForumTopics with created topic = %+v, want General + created topic + root message", forumTopicsWithCreated)
	}
	// 回归:官方 web(telegram-tt 首屏 loadTopics)发 limit=500,超单页上限。服务端必须钳到上限正常返回,
	// 而非报 LIMIT_INVALID(此前 web 端每次 getForumTopics 恒失败)。客户端据返回的 count 用 offset 翻页。
	forumTopicsLargeLimit, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 500,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics limit=500 should clamp to page size, got err: %v", err)
	}
	if forumTopicsLargeLimit.Count != 2 || len(forumTopicsLargeLimit.Topics) != 2 {
		t.Fatalf("messages.getForumTopics limit=500 = %+v, want clamped result (General + created topic)", forumTopicsLargeLimit)
	}
	var foundCreated bool
	for _, item := range forumTopicsWithCreated.Topics {
		topic, ok := item.(*tg.ForumTopic)
		if ok && topic.ID == topicID && topic.Title == "Ops" && topic.TopMessage == topicID {
			foundCreated = true
			break
		}
	}
	if !foundCreated {
		t.Fatalf("messages.getForumTopics topics = %+v, want created topic id %d", forumTopicsWithCreated.Topics, topicID)
	}
	missingTopicID := topicID + 1000
	forumTopicsByID, err = r.onMessagesGetForumTopicsByID(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   forumPeer,
		Topics: []int{topicID, missingTopicID, forumGeneralTopicID, topicID},
	})
	if err != nil {
		t.Fatalf("messages.getForumTopicsByID with created topic: %v", err)
	}
	if forumTopicsByID.Count != 3 || len(forumTopicsByID.Topics) != 3 || len(forumTopicsByID.Messages) == 0 {
		t.Fatalf("messages.getForumTopicsByID with created/missing/duplicate topics = %+v, want three unique results", forumTopicsByID)
	}
	if topic, ok := forumTopicsByID.Topics[0].(*tg.ForumTopic); !ok || topic.ID != topicID {
		t.Fatalf("messages.getForumTopicsByID result[0] = %T %+v, want live topic %d", forumTopicsByID.Topics[0], forumTopicsByID.Topics[0], topicID)
	}
	if topic, ok := forumTopicsByID.Topics[1].(*tg.ForumTopicDeleted); !ok || topic.ID != missingTopicID {
		t.Fatalf("messages.getForumTopicsByID result[1] = %T %+v, want deleted placeholder %d", forumTopicsByID.Topics[1], forumTopicsByID.Topics[1], missingTopicID)
	}
	if topic, ok := forumTopicsByID.Topics[2].(*tg.ForumTopic); !ok || topic.ID != forumGeneralTopicID {
		t.Fatalf("messages.getForumTopicsByID result[2] = %T %+v, want General", forumTopicsByID.Topics[2], forumTopicsByID.Topics[2])
	}
	topicReply := &tg.InputReplyToMessage{ReplyToMsgID: 0}
	topicReply.SetTopMsgID(topicID)
	topicSendReq := &tg.MessagesSendMessageRequest{
		Peer:     forumPeer,
		Message:  "topic body",
		RandomID: 17803002,
	}
	topicSendReq.SetReplyTo(topicReply)
	topicSent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), topicSendReq)
	if err != nil {
		t.Fatalf("messages.sendMessage topic-only reply: %v", err)
	}
	var topicMsg *tg.Message
	for _, update := range topicSent.(*tg.Updates).Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		if msg, ok := newMsg.Message.(*tg.Message); ok && msg.Message == "topic body" {
			topicMsg = msg
			break
		}
	}
	if topicMsg == nil {
		t.Fatalf("messages.sendMessage topic updates = %+v, want topic message", topicSent)
	}
	header, ok := topicMsg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || !header.ForumTopic || header.ReplyToMsgID != 0 {
		t.Fatalf("topic reply header = %#v, want forum topic without reply_to_msg_id", topicMsg.ReplyTo)
	}
	if topID, ok := header.GetReplyToTopID(); !ok || topID != topicID {
		t.Fatalf("topic reply top id = %d ok %v, want %d", topID, ok, topicID)
	}
	topicReplies, err := r.onMessagesGetReplies(WithUserID(ctx, owner.ID), &tg.MessagesGetRepliesRequest{
		Peer:  forumPeer,
		MsgID: topicID,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getReplies forum topic: %v", err)
	}
	topicReplyPage, ok := topicReplies.(*tg.MessagesChannelMessages)
	if !ok || topicReplyPage.Pts == 0 || len(topicReplyPage.Messages) != 1 || len(topicReplyPage.Topics) != 1 {
		t.Fatalf("messages.getReplies forum topic = %T %+v, want channelMessages with one topic", topicReplies, topicReplies)
	}
	if got := topicReplyPage.Messages[0].(*tg.Message); got.ID != topicMsg.ID || got.Message != "topic body" {
		t.Fatalf("topic reply message = %#v, want id %d", got, topicMsg.ID)
	}
	if topic, ok := topicReplyPage.Topics[0].(*tg.ForumTopic); !ok || !topic.Short || topic.ID != topicID || topic.TopMessage != topicMsg.ID {
		t.Fatalf("topic reply page topic = %#v, want short topic with updated top message %d", topicReplyPage.Topics[0], topicMsg.ID)
	}
	forumTopicsAfterMessage, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics after topic message: %v", err)
	}
	var foundTopicTop bool
	for _, item := range forumTopicsAfterMessage.Topics {
		topic, ok := item.(*tg.ForumTopic)
		if ok && topic.ID == topicID && topic.TopMessage == topicMsg.ID {
			foundTopicTop = true
			break
		}
	}
	if !foundTopicTop {
		t.Fatalf("messages.getForumTopics after topic message topics = %+v, want top message %d", forumTopicsAfterMessage.Topics, topicMsg.ID)
	}
	forwardToTopicReq := &tg.MessagesForwardMessagesRequest{
		FromPeer: forumPeer,
		ToPeer:   forumPeer,
		ID:       []int{topicMsg.ID},
		RandomID: []int64{17803003},
	}
	forwardToTopicReq.SetTopMsgID(topicID)
	forwardToTopic, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), forwardToTopicReq)
	if err != nil {
		t.Fatalf("messages.forwardMessages to forum topic: %v", err)
	}
	var forwardedTopicMsg *tg.Message
	for _, update := range forwardToTopic.(*tg.Updates).Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		if msg, ok := newMsg.Message.(*tg.Message); ok && msg.Message == "topic body" && msg.ID != topicMsg.ID {
			forwardedTopicMsg = msg
			break
		}
	}
	if forwardedTopicMsg == nil {
		t.Fatalf("messages.forwardMessages to topic updates = %+v, want forwarded topic message", forwardToTopic)
	}
	if _, ok := forwardedTopicMsg.GetFwdFrom(); !ok {
		t.Fatalf("forwarded topic message = %#v, want fwd header", forwardedTopicMsg)
	}
	forwardReply, ok := forwardedTopicMsg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || !forwardReply.ForumTopic || forwardReply.ReplyToMsgID != 0 {
		t.Fatalf("forward topic reply header = %#v, want forum topic without reply_to_msg_id", forwardedTopicMsg.ReplyTo)
	}
	if topID, ok := forwardReply.GetReplyToTopID(); !ok || topID != topicID {
		t.Fatalf("forward topic reply top id = %d ok %v, want %d", topID, ok, topicID)
	}
	forumTopicsAfterForward, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics after forward: %v", err)
	}
	var foundForwardTop bool
	for _, item := range forumTopicsAfterForward.Topics {
		topic, ok := item.(*tg.ForumTopic)
		if ok && topic.ID == topicID && topic.TopMessage == forwardedTopicMsg.ID {
			foundForwardTop = true
			break
		}
	}
	if !foundForwardTop {
		t.Fatalf("messages.getForumTopics after forward topics = %+v, want top message %d", forumTopicsAfterForward.Topics, forwardedTopicMsg.ID)
	}
	invalidForwardTop := &tg.MessagesForwardMessagesRequest{
		FromPeer: forumPeer,
		ToPeer:   forumPeer,
		ID:       []int{topicMsg.ID},
		RandomID: []int64{17803004},
	}
	invalidForwardTop.SetTopMsgID(domain.MaxMessageBoxID + 1)
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), invalidForwardTop); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("messages.forwardMessages huge top_msg_id err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
	editTopicReq := &tg.MessagesEditForumTopicRequest{Peer: forumPeer, TopicID: topicID}
	editTopicReq.SetTitle("Ops 2")
	editTopicReq.SetClosed(true)
	editedForumTopic, err := r.onMessagesEditForumTopic(WithUserID(ctx, owner.ID), editTopicReq)
	if err != nil {
		t.Fatalf("messages.editForumTopic: %v", err)
	}
	var editedRootID int
	for _, update := range editedForumTopic.(*tg.Updates).Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		msg, ok := newMsg.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		action, ok := msg.Action.(*tg.MessageActionTopicEdit)
		if ok && action.Title == "Ops 2" {
			if closed, closedOK := action.GetClosed(); closedOK && closed {
				editedRootID = msg.ID
				break
			}
		}
	}
	if editedRootID == 0 {
		t.Fatalf("messages.editForumTopic updates = %+v, want topic edit service message", editedForumTopic)
	}
	pinnedTopic, err := r.onMessagesUpdatePinnedForumTopic(WithUserID(ctx, owner.ID), &tg.MessagesUpdatePinnedForumTopicRequest{
		Peer:    forumPeer,
		TopicID: topicID,
		Pinned:  true,
	})
	if err != nil {
		t.Fatalf("messages.updatePinnedForumTopic: %v", err)
	}
	if got := pinnedTopic.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.updatePinnedForumTopic updates = %+v, want one update", got)
	} else if update, ok := got[0].(*tg.UpdatePinnedForumTopic); !ok || update.TopicID != topicID || !update.GetPinned() {
		t.Fatalf("messages.updatePinnedForumTopic update = %+v, want pinned topic", got[0])
	}
	reorderedTopics, err := r.onMessagesReorderPinnedForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesReorderPinnedForumTopicsRequest{
		Peer:  forumPeer,
		Order: []int{topicID},
		Force: true,
	})
	if err != nil {
		t.Fatalf("messages.reorderPinnedForumTopics: %v", err)
	}
	if got := reorderedTopics.(*tg.Updates).Updates; len(got) != 1 {
		t.Fatalf("messages.reorderPinnedForumTopics updates = %+v, want one update", got)
	} else if update, ok := got[0].(*tg.UpdatePinnedForumTopics); !ok || len(update.Order) != 1 || update.Order[0] != topicID {
		t.Fatalf("messages.reorderPinnedForumTopics update = %+v, want order", got[0])
	}
	deletedTopic, err := r.onMessagesDeleteTopicHistory(WithUserID(ctx, owner.ID), &tg.MessagesDeleteTopicHistoryRequest{
		Peer:     forumPeer,
		TopMsgID: topicID,
	})
	if err != nil {
		t.Fatalf("messages.deleteTopicHistory: %v", err)
	}
	if deletedTopic.Pts == 0 || deletedTopic.PtsCount == 0 || deletedTopic.Offset != 0 {
		t.Fatalf("messages.deleteTopicHistory = %+v, want affected history with final offset", deletedTopic)
	}
	forumTopicsAfterDelete, err := r.onMessagesGetForumTopics(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsRequest{
		Peer:  forumPeer,
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("messages.getForumTopics after delete topic: %v", err)
	}
	if forumTopicsAfterDelete.Count != 1 || len(forumTopicsAfterDelete.Topics) != 1 {
		t.Fatalf("messages.getForumTopics after delete topic = %+v, want General only", forumTopicsAfterDelete)
	}
	deletedByID, err := r.onMessagesGetForumTopicsByID(WithUserID(ctx, owner.ID), &tg.MessagesGetForumTopicsByIDRequest{
		Peer:   forumPeer,
		Topics: []int{topicID},
	})
	if err != nil {
		t.Fatalf("messages.getForumTopicsByID after delete topic: %v", err)
	}
	if deletedByID.Count != 1 || len(deletedByID.Topics) != 1 {
		t.Fatalf("messages.getForumTopicsByID after delete = %+v, want one deleted placeholder", deletedByID)
	}
	if topic, ok := deletedByID.Topics[0].(*tg.ForumTopicDeleted); !ok || topic.ID != topicID {
		t.Fatalf("messages.getForumTopicsByID after delete result = %T %+v, want deleted topic %d", deletedByID.Topics[0], deletedByID.Topics[0], topicID)
	}
	antiSpamUpdates, err := r.onChannelsToggleAntiSpam(WithUserID(ctx, owner.ID), &tg.ChannelsToggleAntiSpamRequest{Channel: input, Enabled: true})
	if err != nil {
		t.Fatalf("toggle antispam: %v", err)
	}
	if len(antiSpamUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("toggle antispam updates = %+v, want channel chat", antiSpamUpdates)
	}
	fullWithAntiSpam, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("full channel after antispam: %v", err)
	}
	if full := fullWithAntiSpam.FullChat.(*tg.ChannelFull); !full.GetAntispam() {
		t.Fatalf("full channel antispam = false, want true")
	}
	settingsFilter := tg.ChannelAdminLogEventsFilter{}
	settingsFilter.SetSettings(true)
	antiSpamLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminLogRequest{
		Channel:      input,
		EventsFilter: settingsFilter,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("admin log after antispam: %v", err)
	}
	foundAntiSpamLog := false
	foundForumLog := false
	for _, event := range antiSpamLog.Events {
		if action, ok := event.Action.(*tg.ChannelAdminLogEventActionToggleAntiSpam); ok && action.NewValue {
			foundAntiSpamLog = true
		}
		if action, ok := event.Action.(*tg.ChannelAdminLogEventActionToggleForum); ok && action.NewValue {
			foundForumLog = true
		}
	}
	if !foundAntiSpamLog || !foundForumLog {
		t.Fatalf("admin log events = %+v, want toggle antispam and toggle forum actions", antiSpamLog.Events)
	}
	channelUpdateStubs := []struct {
		name string
		call func() (tg.UpdatesClass, error)
	}{
		{"set boosts", func() (tg.UpdatesClass, error) {
			return r.onChannelsSetBoostsToUnblockRestrictions(WithUserID(ctx, owner.ID), &tg.ChannelsSetBoostsToUnblockRestrictionsRequest{Channel: input, Boosts: 1})
		}},
		{"restrict sponsored", func() (tg.UpdatesClass, error) {
			return r.onChannelsRestrictSponsoredMessages(WithUserID(ctx, owner.ID), &tg.ChannelsRestrictSponsoredMessagesRequest{Channel: input, Restricted: true})
		}},
		{"update paid messages", func() (tg.UpdatesClass, error) {
			return r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: input, SendPaidMessagesStars: 7})
		}},
		{"toggle autotranslation", func() (tg.UpdatesClass, error) {
			return r.onChannelsToggleAutotranslation(WithUserID(ctx, owner.ID), &tg.ChannelsToggleAutotranslationRequest{Channel: input, Enabled: true})
		}},
	}
	for _, item := range channelUpdateStubs {
		updates, err := item.call()
		if err != nil {
			t.Fatalf("%s: %v", item.name, err)
		}
		if len(updates.(*tg.Updates).Chats) != 1 {
			t.Fatalf("%s updates = %+v, want channel chat", item.name, updates)
		}
	}
	fullAfterPaidSettings, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), input)
	if err != nil {
		t.Fatalf("full channel after paid/settings toggles: %v", err)
	}
	fullPaidSettings := fullAfterPaidSettings.FullChat.(*tg.ChannelFull)
	if !fullPaidSettings.GetRestrictedSponsored() {
		t.Fatalf("full channel restricted_sponsored = false, want true")
	}
	if stars, ok := fullPaidSettings.GetSendPaidMessagesStars(); !fullPaidSettings.GetPaidMessagesAvailable() || !ok || stars != 7 {
		t.Fatalf("full channel paid messages = available %v stars %d ok %v, want available true stars 7", fullPaidSettings.GetPaidMessagesAvailable(), stars, ok)
	}
	if boosts, ok := fullPaidSettings.GetBoostsUnrestrict(); !ok || boosts != 1 {
		t.Fatalf("full channel boosts_unrestrict = %d ok %v, want 1", boosts, ok)
	}
	chatsAfterAutotranslation, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channels after autotranslation: %v", err)
	}
	channelAfterAutotranslation := chatsAfterAutotranslation.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if !channelAfterAutotranslation.GetAutotranslation() {
		t.Fatalf("channel autotranslation = false, want true")
	}
	autoLog, err := r.onChannelsGetAdminLog(WithUserID(ctx, owner.ID), &tg.ChannelsGetAdminLogRequest{
		Channel:      input,
		EventsFilter: settingsFilter,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("admin log after autotranslation: %v", err)
	}
	foundAutoLog := false
	for _, event := range autoLog.Events {
		if action, ok := event.Action.(*tg.ChannelAdminLogEventActionToggleAutotranslation); ok && action.NewValue {
			foundAutoLog = true
			break
		}
	}
	if !foundAutoLog {
		t.Fatalf("admin log events = %+v, want toggle autotranslation action", autoLog.Events)
	}
	if _, err := r.onChannelsSetBoostsToUnblockRestrictions(WithUserID(ctx, owner.ID), &tg.ChannelsSetBoostsToUnblockRestrictionsRequest{Channel: input, Boosts: maxChannelBoostsToUnblockRestrictions + 1}); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("set boosts over max err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: input, SendPaidMessagesStars: -1}); err == nil || !strings.Contains(err.Error(), "STARS_AMOUNT_INVALID") {
		t.Fatalf("update paid messages negative supergroup err = %v, want STARS_AMOUNT_INVALID", err)
	}
	if _, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: input, SendPaidMessagesStars: maxChannelPaidMessageStars + 1}); err == nil || !strings.Contains(err.Error(), "STARS_AMOUNT_INVALID") {
		t.Fatalf("update paid messages over max err = %v, want STARS_AMOUNT_INVALID", err)
	}
	broadcastCreated, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Broadcast Paid",
		About:     "broadcast direct messages",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create broadcast channel: %v", err)
	}
	broadcast := broadcastCreated.(*tg.Updates).Chats[0].(*tg.Channel)
	broadcastInput := &tg.InputChannel{ChannelID: broadcast.ID, AccessHash: broadcast.AccessHash}
	disabledUpdates, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: broadcastInput, SendPaidMessagesStars: -1})
	if err != nil {
		t.Fatalf("update paid messages broadcast disable: %v", err)
	}
	if len(disabledUpdates.(*tg.Updates).Chats) != 1 {
		t.Fatalf("disable paid messages chats = %d, want parent only", len(disabledUpdates.(*tg.Updates).Chats))
	}
	if _, ok := disabledUpdates.(*tg.Updates).Chats[0].(*tg.Channel).GetLinkedMonoforumID(); ok {
		t.Fatalf("disable paid messages projected linked_monoforum_id, want hidden")
	}
	freeUpdates, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{
		BroadcastMessagesAllowed: true,
		Channel:                  broadcastInput,
		SendPaidMessagesStars:    0,
	})
	if err != nil {
		t.Fatalf("update paid messages broadcast free: %v", err)
	}
	freeChats := freeUpdates.(*tg.Updates).Chats
	if len(freeChats) != 2 {
		t.Fatalf("enable free direct messages chats = %d, want parent + monoforum", len(freeChats))
	}
	freeParent := freeChats[0].(*tg.Channel)
	monoID, ok := freeParent.GetLinkedMonoforumID()
	if !ok || monoID == 0 {
		t.Fatalf("enable free direct messages parent linked_monoforum_id = %d ok %v, want monoforum id", monoID, ok)
	}
	if stars, ok := freeParent.GetSendPaidMessagesStars(); !ok || stars != 0 {
		t.Fatalf("free parent send_paid_messages_stars = %d ok %v, want 0 true", stars, ok)
	}
	monoChat, ok := freeChats[1].(*tg.Channel)
	if !ok || monoChat.ID != monoID || !monoChat.GetMonoforum() || monoChat.GetMin() {
		t.Fatalf("enable free direct messages linked chat = %T %+v, want full monoforum %d", freeChats[1], freeChats[1], monoID)
	}
	if stars, ok := monoChat.GetSendPaidMessagesStars(); !ok || stars != 0 {
		t.Fatalf("free monoforum send_paid_messages_stars = %d ok %v, want 0 true", stars, ok)
	}
	if parentID, ok := monoChat.GetLinkedMonoforumID(); !ok || parentID != broadcast.ID {
		t.Fatalf("free monoforum linked_monoforum_id = %d ok %v, want parent %d", parentID, ok, broadcast.ID)
	}
	if monoChat.Broadcast || !monoChat.Megagroup {
		t.Fatalf("free monoforum kind = broadcast:%v megagroup:%v, want megagroup monoforum", monoChat.Broadcast, monoChat.Megagroup)
	}
	// 母广播频道收到 paid_messages_price(渲染 "Channel enabled Direct Messages");monoforum 首次
	// 创建则收到 channelCreate 创建消息(渲染 "Direct messages were enabled in this channel."),
	// 不再收 paid_messages_price——在 megagroup 里那会渲染成错误的"消息免费/设价"文案。
	freeServices := paidMessagesPriceServicePeers(t, freeUpdates)
	if price := freeServices[broadcast.ID]; price == nil || !price.BroadcastMessagesAllowed || price.Stars != 0 {
		t.Fatalf("free parent direct messages action = %+v, want enabled stars=0", price)
	}
	if _, ok := freeServices[monoID]; ok {
		t.Fatalf("free monoforum got a paid_messages_price service, want only the channelCreate creation message")
	}
	if _, ok := monoforumCreationServicePeers(t, freeUpdates)[monoID]; !ok {
		t.Fatalf("free enable did not deliver monoforum creation service for %d", monoID)
	}
	paidUpdates, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{
		BroadcastMessagesAllowed: true,
		Channel:                  broadcastInput,
		SendPaidMessagesStars:    maxChannelPaidMessageStars,
	})
	if err != nil {
		t.Fatalf("update paid messages broadcast max: %v", err)
	}
	paidChats := paidUpdates.(*tg.Updates).Chats
	if len(paidChats) != 2 {
		t.Fatalf("enable paid messages chats = %d, want parent + monoforum", len(paidChats))
	}
	paidParent := paidChats[0].(*tg.Channel)
	paidMonoID, ok := paidParent.GetLinkedMonoforumID()
	if !ok || paidMonoID != monoID {
		t.Fatalf("enable paid messages parent linked_monoforum_id = %d ok %v, want stable monoforum id %d", paidMonoID, ok, monoID)
	}
	monoChat, ok = paidChats[1].(*tg.Channel)
	if !ok || monoChat.ID != monoID || !monoChat.GetMonoforum() || monoChat.GetMin() {
		t.Fatalf("enable paid messages linked chat = %T %+v, want full monoforum %d", paidChats[1], paidChats[1], monoID)
	}
	if stars, ok := monoChat.GetSendPaidMessagesStars(); !ok || stars != maxChannelPaidMessageStars {
		t.Fatalf("paid monoforum send_paid_messages_stars = %d ok %v, want %d true", stars, ok, maxChannelPaidMessageStars)
	}
	if parentID, ok := monoChat.GetLinkedMonoforumID(); !ok || parentID != broadcast.ID {
		t.Fatalf("monoforum linked_monoforum_id = %d ok %v, want parent %d", parentID, ok, broadcast.ID)
	}
	if monoChat.Broadcast || !monoChat.Megagroup {
		t.Fatalf("paid monoforum kind = broadcast:%v megagroup:%v, want megagroup monoforum", monoChat.Broadcast, monoChat.Megagroup)
	}
	// 价格变更只进母频道;monoforum 已存在,不再收任何服务消息(仍作为引用 chat 被同批下发)。
	paidServices := paidMessagesPriceServicePeers(t, paidUpdates)
	if price := paidServices[broadcast.ID]; price == nil || !price.BroadcastMessagesAllowed || price.Stars != maxChannelPaidMessageStars {
		t.Fatalf("paid parent direct messages action = %+v, want enabled stars=%d", price, maxChannelPaidMessageStars)
	}
	if _, ok := paidServices[monoID]; ok {
		t.Fatalf("paid monoforum got a paid_messages_price service on re-enable, want none")
	}
	disabledAfterEnableUpdates, err := r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{Channel: broadcastInput, SendPaidMessagesStars: -1})
	if err != nil {
		t.Fatalf("disable direct messages after enable: %v", err)
	}
	disabledAfterEnableChats := disabledAfterEnableUpdates.(*tg.Updates).Chats
	// 关闭后只下发母频道(linked_monoforum_id 被隐藏 → 客户端缓存里 monoforum 翻出 MonoforumDisabled
	// 状态显示停用页脚);monoforum 自身不再随关闭重新下发(无新服务消息,且 broadcast_messages_allowed
	// =false 时 linkedMonoforumForChannelState 也不再附带它)。
	if len(disabledAfterEnableChats) != 1 {
		t.Fatalf("disable direct messages chats = %d, want parent only", len(disabledAfterEnableChats))
	}
	disabledParent := disabledAfterEnableChats[0].(*tg.Channel)
	if disabledParent.GetBroadcastMessagesAllowed() {
		t.Fatalf("disable direct messages parent broadcast_messages_allowed = true, want false")
	}
	if _, ok := disabledParent.GetLinkedMonoforumID(); ok {
		t.Fatalf("disable direct messages parent projected linked_monoforum_id, want hidden")
	}
	// 关闭只进母频道;monoforum 不再收服务消息(停用页脚靠客户端从隐藏的 linked_monoforum_id 派生)。
	disabledServices := paidMessagesPriceServicePeers(t, disabledAfterEnableUpdates)
	if price := disabledServices[broadcast.ID]; price == nil || price.BroadcastMessagesAllowed || price.Stars != 0 {
		t.Fatalf("disable parent direct messages action = %+v, want disabled stars=0", price)
	}
	if _, ok := disabledServices[monoID]; ok {
		t.Fatalf("disable monoforum got a paid_messages_price service, want none")
	}
	broadcastChats, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{broadcastInput})
	if err != nil {
		t.Fatalf("get broadcast after paid messages: %v", err)
	}
	broadcastAfterPaid := broadcastChats.(*tg.MessagesChats).Chats[0].(*tg.Channel)
	if broadcastAfterPaid.GetBroadcastMessagesAllowed() {
		t.Fatalf("broadcast_messages_allowed = true after disable, want false")
	}
	if _, ok := broadcastAfterPaid.GetLinkedMonoforumID(); ok {
		t.Fatalf("broadcast after disable projected linked_monoforum_id, want hidden")
	}
	_, err = r.onChannelsUpdatePaidMessagesPrice(WithUserID(ctx, owner.ID), &tg.ChannelsUpdatePaidMessagesPriceRequest{
		BroadcastMessagesAllowed: true,
		Channel:                  broadcastInput,
		SendPaidMessagesStars:    maxChannelPaidMessageStars,
	})
	if err != nil {
		t.Fatalf("re-enable direct messages before full channel: %v", err)
	}
	fullBroadcast, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), broadcastInput)
	if err != nil {
		t.Fatalf("get full broadcast after paid messages: %v", err)
	}
	if len(fullBroadcast.Chats) != 2 {
		t.Fatalf("full broadcast chats = %d, want parent + monoforum", len(fullBroadcast.Chats))
	}
	fullParent := fullBroadcast.Chats[0].(*tg.Channel)
	fullMonoID, ok := fullParent.GetLinkedMonoforumID()
	if !ok || fullMonoID != monoID {
		t.Fatalf("full parent linked_monoforum_id = %d ok %v, want %d", fullMonoID, ok, monoID)
	}
	fullMono := fullBroadcast.Chats[1].(*tg.Channel)
	if fullMono.ID != monoID || !fullMono.GetMonoforum() || fullMono.GetMin() {
		t.Fatalf("full monoforum chat = %+v, want full monoforum %d", fullMono, monoID)
	}
	if fullMono.Broadcast || !fullMono.Megagroup {
		t.Fatalf("full monoforum kind = broadcast:%v megagroup:%v, want megagroup monoforum", fullMono.Broadcast, fullMono.Megagroup)
	}
	cachedFullBroadcast, err := r.onChannelsGetFullChannel(WithUserID(ctx, owner.ID), broadcastInput)
	if err != nil {
		t.Fatalf("get cached full broadcast after paid messages: %v", err)
	}
	if len(cachedFullBroadcast.Chats) != 2 {
		t.Fatalf("cached full broadcast chats = %d, want parent + monoforum", len(cachedFullBroadcast.Chats))
	}
	if _, err := r.onChannelsSetStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetStickersRequest{Channel: broadcastInput, Stickerset: &tg.InputStickerSetEmpty{}}); err == nil || !strings.Contains(err.Error(), "CHANNEL_INVALID") {
		t.Fatalf("set stickers broadcast err = %v, want CHANNEL_INVALID", err)
	}
	if ok, err := r.onChannelsReportAntiSpamFalsePositive(WithUserID(ctx, owner.ID), &tg.ChannelsReportAntiSpamFalsePositiveRequest{Channel: input, MsgID: msgID}); err != nil || !ok {
		t.Fatalf("report antispam false positive = ok %v err %v, want true", ok, err)
	}
	recommendationsReq := &tg.ChannelsGetChannelRecommendationsRequest{}
	recommendationsReq.SetChannel(input)
	if _, err := r.onChannelsGetChannelRecommendations(WithUserID(ctx, owner.ID), recommendationsReq); err == nil || !strings.Contains(err.Error(), "CHANNEL_INVALID") {
		t.Fatalf("megagroup channel recommendations err = %v, want CHANNEL_INVALID", err)
	}
	if ok, err := r.onChannelsSetEmojiStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetEmojiStickersRequest{Channel: input, Stickerset: &tg.InputStickerSetEmpty{}}); err != nil || !ok {
		t.Fatalf("set emoji stickers = ok %v err %v, want true", ok, err)
	}
	if _, err := r.onChannelsSetEmojiStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetEmojiStickersRequest{
		Channel:    input,
		Stickerset: &tg.InputStickerSetShortName{ShortName: "custom_emoji"},
	}); err == nil || !strings.Contains(err.Error(), "STICKERSET_INVALID") {
		t.Fatalf("set emoji stickers non-empty err = %v, want STICKERSET_INVALID", err)
	}
	if _, err := r.onChannelsSetEmojiStickers(WithUserID(ctx, owner.ID), &tg.ChannelsSetEmojiStickersRequest{Channel: broadcastInput, Stickerset: &tg.InputStickerSetEmpty{}}); err == nil || !strings.Contains(err.Error(), "CHANNEL_INVALID") {
		t.Fatalf("set emoji stickers broadcast err = %v, want CHANNEL_INVALID", err)
	}
	searchPostsReq := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 10}
	searchPostsReq.SetHashtag("ops")
	if posts, err := r.onChannelsSearchPosts(WithUserID(ctx, owner.ID), searchPostsReq); err != nil || len(posts.(*tg.MessagesMessages).Messages) != 0 {
		t.Fatalf("search posts = %+v err %v, want empty", posts, err)
	}
	floodReq := &tg.ChannelsCheckSearchPostsFloodRequest{}
	floodReq.SetQuery("ops")
	if flood, err := r.onChannelsCheckSearchPostsFlood(WithUserID(ctx, owner.ID), floodReq); err != nil || !flood.QueryIsFree {
		t.Fatalf("check search posts flood = %+v err %v, want free", flood, err)
	}
	if ok, err := r.onChannelsSetMainProfileTab(WithUserID(ctx, owner.ID), &tg.ChannelsSetMainProfileTabRequest{Channel: input}); err != nil || !ok {
		t.Fatalf("set main profile tab = ok %v err %v, want true", ok, err)
	}
	if ok, err := r.onChannelsToggleUsername(WithUserID(ctx, owner.ID), &tg.ChannelsToggleUsernameRequest{Channel: input, Username: "public_team", Active: false}); err != nil || !ok {
		t.Fatalf("toggle username = ok %v err %v, want true", ok, err)
	}
	if ok, err := r.onChannelsDeactivateAllUsernames(WithUserID(ctx, owner.ID), input); err != nil || !ok {
		t.Fatalf("deactivate usernames = ok %v err %v, want true", ok, err)
	}
	stillPublic, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channel after deactivate: %v", err)
	}
	if got := stillPublic.(*tg.MessagesChats).Chats[0].(*tg.Channel); got.Username != "public_team" {
		t.Fatalf("channel after fragment username stubs = %+v, want primary username preserved", got)
	}
	if ok, err := r.onChannelsUpdateUsername(WithUserID(ctx, owner.ID), &tg.ChannelsUpdateUsernameRequest{Channel: input, Username: ""}); err != nil || !ok {
		t.Fatalf("clear username = ok %v err %v, want true", ok, err)
	}
	after, err := r.onChannelsGetChannels(WithUserID(ctx, owner.ID), []tg.InputChannelClass{input})
	if err != nil {
		t.Fatalf("get channel after clear: %v", err)
	}
	if got := after.(*tg.MessagesChats).Chats[0].(*tg.Channel); got.Username != "" || len(got.Usernames) != 0 {
		t.Fatalf("channel after clear = %+v, want username cleared", got)
	}
}

func TestChannelsGetChannelsRejectsHugeVector(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	ids := make([]tg.InputChannelClass, maxGetMessagesIDs+1)
	for i := range ids {
		ids[i] = &tg.InputChannel{ChannelID: int64(i + 1)}
	}
	if _, err := r.onChannelsGetChannels(context.Background(), ids); err == nil || !strings.Contains(err.Error(), "LIMIT_INVALID") {
		t.Fatalf("channels.getChannels huge vector err = %v, want LIMIT_INVALID", err)
	}
}

func paidMessagesPriceServicePeers(t *testing.T, updates tg.UpdatesClass) map[int64]*tg.MessageActionPaidMessagesPrice {
	t.Helper()
	box, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	out := make(map[int64]*tg.MessageActionPaidMessagesPrice)
	for _, update := range box.Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		service, ok := newMsg.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		peer, ok := service.PeerID.(*tg.PeerChannel)
		if !ok {
			continue
		}
		action, ok := service.Action.(*tg.MessageActionPaidMessagesPrice)
		if !ok {
			continue
		}
		out[peer.ChannelID] = action
	}
	return out
}

// monoforumCreationServicePeers 收集 updates 里 messageActionChannelCreate 服务消息的 peer 集合。
// monoforum 首次开启时收到一条创建消息(TDesktop 渲染 "Direct messages were enabled in this
// channel."),这是它在直连会话里唯一的服务消息;开关/价格变更不再往 mono 追加服务消息。
func monoforumCreationServicePeers(t *testing.T, updates tg.UpdatesClass) map[int64]struct{} {
	t.Helper()
	box, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	out := make(map[int64]struct{})
	for _, update := range box.Updates {
		newMsg, ok := update.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		service, ok := newMsg.Message.(*tg.MessageService)
		if !ok {
			continue
		}
		peer, ok := service.PeerID.(*tg.PeerChannel)
		if !ok {
			continue
		}
		if _, ok := service.Action.(*tg.MessageActionChannelCreate); !ok {
			continue
		}
		out[peer.ChannelID] = struct{}{}
	}
	return out
}
