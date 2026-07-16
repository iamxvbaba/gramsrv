package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

// dispatchCanonicalValue exposes the handler value behind tlprofile's exact
// result encoder. Object/class results already arrive as concrete tg values;
// primitive and Vector<T> method results remain attached to the exact result
// plan because they are not bin.Object values.
func dispatchCanonicalValue(encoded bin.Encoder) any {
	if result, ok := encoded.(interface{ CanonicalValue() any }); ok {
		return result.CanonicalValue()
	}
	return encoded
}

type rpcChannelFixture struct {
	t        *testing.T
	ctx      context.Context
	users    *memory.UserStore
	channels *memory.ChannelStore
	router   *Router
}

func newRPCChannelFixture(t *testing.T) *rpcChannelFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	channelStore := memory.NewChannelStore()
	return &rpcChannelFixture{
		t:        t,
		ctx:      ctx,
		users:    userStore,
		channels: channelStore,
		router: New(Config{}, Deps{
			Users:    appusers.NewService(userStore),
			Channels: appchannels.NewService(channelStore),
		}, zaptest.NewLogger(t), clock.System),
	}
}

func (f *rpcChannelFixture) user(accessHash int64, phone, firstName string) domain.User {
	f.t.Helper()
	user, err := f.users.Create(f.ctx, domain.User{
		AccessHash: accessHash,
		Phone:      phone,
		FirstName:  firstName,
	})
	if err != nil {
		f.t.Fatalf("create user %s: %v", firstName, err)
	}
	return user
}

func (f *rpcChannelFixture) userCtx(user domain.User) context.Context {
	return WithUserID(f.ctx, user.ID)
}

func (f *rpcChannelFixture) createLegacyMegagroup(owner domain.User, title string, users ...domain.User) *tg.Channel {
	f.t.Helper()
	inputUsers := make([]tg.InputUserClass, 0, len(users))
	for _, user := range users {
		inputUsers = append(inputUsers, inputUser(user))
	}
	created, err := f.router.onMessagesCreateChat(f.userCtx(owner), &tg.MessagesCreateChatRequest{
		Users: inputUsers,
		Title: title,
	})
	if err != nil {
		f.t.Fatalf("create chat: %v", err)
	}
	updates, ok := created.Updates.(*tg.Updates)
	if !ok || len(updates.Chats) == 0 {
		f.t.Fatalf("create chat updates = %T %+v, want chats", created.Updates, created.Updates)
	}
	channel, ok := updates.Chats[0].(*tg.Channel)
	if !ok {
		f.t.Fatalf("created chat = %T, want *tg.Channel", updates.Chats[0])
	}
	return channel
}

func inputUser(user domain.User) *tg.InputUser {
	return &tg.InputUser{UserID: user.ID, AccessHash: user.AccessHash}
}

func inputPeerChannel(channel *tg.Channel) *tg.InputPeerChannel {
	return inputPeerChannelWithHash(channel, channel.AccessHash)
}

func inputPeerChannelWithHash(channel *tg.Channel, accessHash int64) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: accessHash}
}

func inputChannel(channel *tg.Channel) *tg.InputChannel {
	return inputChannelWithHash(channel, channel.AccessHash)
}

func inputChannelWithHash(channel *tg.Channel, accessHash int64) *tg.InputChannel {
	return &tg.InputChannel{ChannelID: channel.ID, AccessHash: accessHash}
}

func searchMessagesPayload(t *testing.T, enc bin.Encoder) ([]tg.MessageClass, []tg.ChatClass, []tg.UserClass) {
	t.Helper()
	switch result := enc.(type) {
	case *tg.MessagesMessages:
		return result.Messages, result.Chats, result.Users
	case *tg.MessagesMessagesSlice:
		return result.Messages, result.Chats, result.Users
	case *tg.MessagesChannelMessages:
		return result.Messages, result.Chats, result.Users
	default:
		t.Fatalf("search result type = %T, want messages/messagesSlice", enc)
		return nil, nil, nil
	}
}

func incrementalViewCount(result *tg.MessagesMessageViews) (int, bool) {
	if result == nil || len(result.Views) == 0 {
		return 0, false
	}
	return result.Views[0].GetViews()
}

func assertDefaultBannedRightsAllowsSend(t *testing.T, chat tg.ChatClass) {
	t.Helper()
	var rights tg.ChatBannedRights
	var ok bool
	switch ch := chat.(type) {
	case *tg.Channel:
		rights, ok = ch.GetDefaultBannedRights()
	case *tg.Chat:
		rights, ok = ch.GetDefaultBannedRights()
	default:
		t.Fatalf("chat = %T, want channel/chat with default banned rights", chat)
	}
	if !ok {
		t.Fatalf("default_banned_rights missing in %T", chat)
	}
	if rights.SendMessages {
		t.Fatalf("default_banned_rights.send_messages = true, want false")
	}
	if rights.UntilDate != defaultChatBannedRightsUntilDate {
		t.Fatalf("default_banned_rights.until_date = %d, want %d", rights.UntilDate, defaultChatBannedRightsUntilDate)
	}
}

func pushedUserStatus(t *testing.T, msg bin.Encoder) *tg.UpdateUserStatus {
	t.Helper()
	updates, ok := msg.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed message = %T %+v, want one update", msg, msg)
	}
	update, ok := updates.Updates[0].(*tg.UpdateUserStatus)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateUserStatus", updates.Updates[0])
	}
	return update
}

func waitForPushedUserIDs(t *testing.T, sessions *captureSessions, min int) []int64 {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := sessions.pushedUserIDs()
		if len(got) >= min {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("pushed users = %+v, want at least %d", got, min)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForLastUserPush(t *testing.T, sessions *captureSessions) bin.Encoder {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if msg := sessions.lastUserPush(); msg != nil {
			return msg
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for user push")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForSessionUserStatus(t *testing.T, sessions *captureSessions, userID int64) *tg.UpdateUserStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if update := userStatusFromMessage(sessions.snapshot().message); update != nil && update.UserID == userID {
			return update
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for session user status user_id=%d", userID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func userStatusFromMessage(msg bin.Encoder) *tg.UpdateUserStatus {
	updates, ok := msg.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		return nil
	}
	update, ok := updates.Updates[0].(*tg.UpdateUserStatus)
	if !ok {
		return nil
	}
	return update
}

func newBlockingUserAuthService(userID int64) *blockingUserAuthService {
	return &blockingUserAuthService{
		userID:  userID,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func limitIDs(ids []int64, limit int) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
