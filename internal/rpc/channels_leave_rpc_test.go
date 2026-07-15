package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type acceptPasswordAccountService struct {
	AccountService
}

func (acceptPasswordAccountService) CheckPassword(_ context.Context, _ int64, check domain.PasswordCheck) error {
	if check.Empty {
		return domain.ErrPasswordHashInvalid
	}
	return nil
}

func TestMessagesGetFutureChatCreatorAfterLeaveAndCreatorLeaveTransfers(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 9101, Phone: "15550009101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	admin, err := userStore.Create(ctx, domain.User{AccessHash: 9102, Phone: "15550009102", FirstName: "Admin"})
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 9103, Phone: "15550009103", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700009100, 0)})
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "leave owner transfer",
		Megagroup:     true,
		MemberUserIDs: []int64{admin.ID, member.ID},
		Date:          1700009100,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if _, err := channelService.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{
		UserID:    owner.ID,
		ChannelID: created.Channel.ID,
		MemberID:  admin.ID,
		AdminRights: domain.ChannelAdminRights{
			ChangeInfo: true,
			AddAdmins:  true,
		},
		Date: 1700009101,
	}); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	inputChannel := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	future, err := r.onMessagesGetFutureChatCreatorAfterLeave(WithUserID(ctx, owner.ID), peer)
	if err != nil {
		t.Fatalf("get future creator: %v", err)
	}
	if user, ok := future.(*tg.User); !ok || user.ID != admin.ID {
		t.Fatalf("future creator = %T %+v, want admin user %d", future, future, admin.ID)
	}

	if _, err := r.onChannelsLeaveChannel(WithUserID(ctx, owner.ID), inputChannel); err != nil {
		t.Fatalf("creator leaves: %v", err)
	}
	view, err := channelService.GetChannel(ctx, admin.ID, created.Channel.ID)
	if err != nil {
		t.Fatalf("get channel after leave: %v", err)
	}
	if view.Channel.CreatorUserID != admin.ID || view.Self.Role != domain.ChannelRoleCreator {
		t.Fatalf("channel after leave = %+v self=%+v, want admin as creator", view.Channel, view.Self)
	}
	oldOwner, err := channelService.GetParticipant(ctx, admin.ID, created.Channel.ID, owner.ID)
	if err != nil {
		t.Fatalf("get old owner after leave: %v", err)
	}
	if oldOwner.Status != domain.ChannelMemberLeft || oldOwner.Role == domain.ChannelRoleCreator {
		t.Fatalf("old owner after leave = %+v, want left non-creator", oldOwner)
	}
	if _, err := r.onChannelsJoinChannel(WithUserID(ctx, owner.ID), inputChannel); err != nil {
		t.Fatalf("old owner rejoins: %v", err)
	}
	rejoined, err := channelService.GetParticipant(ctx, admin.ID, created.Channel.ID, owner.ID)
	if err != nil {
		t.Fatalf("get old owner after rejoin: %v", err)
	}
	if rejoined.Role != domain.ChannelRoleMember || rejoined.Status != domain.ChannelMemberActive || rejoined.Rank != "" {
		t.Fatalf("rejoined old owner = %+v, want active plain member", rejoined)
	}
}

func TestMessagesEditChatCreatorTransfersWithoutChannelPts(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 9121, Phone: "15550009121", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 9122, Phone: "15550009122", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Account:  acceptPasswordAccountService{},
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700009130, 0)})
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "explicit transfer",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700009130,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	ownerCtx := WithUserID(ctx, owner.ID)
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	if _, err := r.onMessagesEditChatCreator(ownerCtx, &tg.MessagesEditChatCreatorRequest{
		Peer:     peer,
		UserID:   &tg.InputUserEmpty{},
		Password: &tg.InputCheckPasswordEmpty{},
	}); err == nil || !tgerr.Is(err, "PASSWORD_HASH_INVALID") {
		t.Fatalf("editChatCreator probe err = %v, want PASSWORD_HASH_INVALID", err)
	}

	updatesClass, err := r.onMessagesEditChatCreator(ownerCtx, &tg.MessagesEditChatCreatorRequest{
		Peer:     peer,
		UserID:   &tg.InputUser{UserID: member.ID, AccessHash: member.AccessHash},
		Password: &tg.InputCheckPasswordSRP{SRPID: 1, A: []byte{1}, M1: []byte{2}},
	})
	if err != nil {
		t.Fatalf("editChatCreator transfer: %v", err)
	}
	updates := updatesClass.(*tg.Updates)
	participantUpdates := 0
	hasChannel := false
	for _, update := range updates.Updates {
		switch update.(type) {
		case *tg.UpdateChannelParticipant:
			participantUpdates++
		case *tg.UpdateChannel:
			hasChannel = true
		}
	}
	if participantUpdates != 2 || !hasChannel {
		t.Fatalf("transfer updates = %+v, want two participant updates and updateChannel", updates.Updates)
	}
	if chat, ok := updates.Chats[0].(*tg.Channel); !ok || chat.Creator || !chat.AdminRights.AddAdmins {
		t.Fatalf("owner response chat = %T %+v, want old owner as non-creator admin", updates.Chats[0], updates.Chats[0])
	}
	view, err := channelService.GetChannel(ctx, member.ID, created.Channel.ID)
	if err != nil {
		t.Fatalf("member get channel after transfer: %v", err)
	}
	if view.Channel.CreatorUserID != member.ID || view.Self.Role != domain.ChannelRoleCreator || view.Channel.Pts != created.Channel.Pts {
		t.Fatalf("channel after transfer = %+v self=%+v, want member creator and pts unchanged %d", view.Channel, view.Self, created.Channel.Pts)
	}
	oldOwner, err := channelService.GetParticipant(ctx, member.ID, created.Channel.ID, owner.ID)
	if err != nil {
		t.Fatalf("old owner participant after transfer: %v", err)
	}
	if oldOwner.Role != domain.ChannelRoleAdmin {
		t.Fatalf("old owner after transfer = %+v, want admin", oldOwner)
	}
}

func TestMessagesGetFutureChatCreatorAfterLeaveNoCandidate(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 9111, Phone: "15550009111", FirstName: "Solo"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelService,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700009120, 0)})
	created, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "solo owner",
		Megagroup:     true,
		Date:          1700009120,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	inputChannel := &tg.InputChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}

	if _, err := r.onMessagesGetFutureChatCreatorAfterLeave(WithUserID(ctx, owner.ID), peer); err == nil || !tgerr.Is(err, "USER_NOT_PARTICIPANT") {
		t.Fatalf("future creator err = %v, want USER_NOT_PARTICIPANT", err)
	}
	if _, err := r.onChannelsLeaveChannel(WithUserID(ctx, owner.ID), inputChannel); err == nil || !tgerr.Is(err, "USER_CREATOR") {
		t.Fatalf("creator leave err = %v, want USER_CREATOR", err)
	}
}
