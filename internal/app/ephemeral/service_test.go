package ephemeral

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

const (
	testHumanID int64 = 1001
	testBotID   int64 = 2001
	testChannel int64 = 3001
	testSession int64 = 4001
)

var testDeviceKey = [8]byte{1, 2, 3, 4}

type testChannels struct {
	roles   map[int64]domain.ChannelMemberRole
	status  map[int64]domain.ChannelMemberStatus
	channel domain.Channel
}

func (c *testChannels) ResolveChannel(_ context.Context, userID, channelID int64) (domain.ChannelView, error) {
	if channelID != c.channel.ID {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	return domain.ChannelView{Channel: c.channel, Self: domain.ChannelMember{
		ChannelID: channelID, UserID: userID, Role: c.roles[userID], Status: c.status[userID],
	}}, nil
}

func (c *testChannels) GetParticipant(_ context.Context, _ int64, channelID, participantUserID int64) (domain.ChannelMember, error) {
	if channelID != c.channel.ID {
		return domain.ChannelMember{}, domain.ErrChannelInvalid
	}
	return domain.ChannelMember{ChannelID: channelID, UserID: participantUserID, Role: c.roles[participantUserID], Status: c.status[participantUserID]}, nil
}

func (c *testChannels) GetForumTopicsByID(_ context.Context, _ int64, channelID int64, ids []int) (domain.ChannelForumTopicList, error) {
	if channelID != c.channel.ID {
		return domain.ChannelForumTopicList{}, domain.ErrChannelInvalid
	}
	out := domain.ChannelForumTopicList{Channel: c.channel}
	for _, id := range ids {
		if id > 0 {
			out.Topics = append(out.Topics, domain.ChannelForumTopic{ChannelID: channelID, TopicID: id})
		}
	}
	return out, nil
}

type testUsers map[int64]domain.User

func (u testUsers) ByID(_ context.Context, _ int64, userID int64) (domain.User, bool, error) {
	user, found := u[userID]
	return user, found, nil
}

type testBots map[int64][]domain.BotCommand

func (b testBots) GetBotCommands(_ context.Context, botUserID int64) ([]domain.BotCommand, error) {
	return append([]domain.BotCommand(nil), b[botUserID]...), nil
}

type serviceFixture struct {
	service  *Service
	store    *memory.EphemeralMessageStore
	now      time.Time
	nextID   int
	channels *testChannels
}

func newServiceFixture() *serviceFixture {
	f := &serviceFixture{
		store:  memory.NewEphemeralMessageStore(),
		now:    time.Unix(1_900_000_000, 0),
		nextID: 10,
		channels: &testChannels{
			roles:   map[int64]domain.ChannelMemberRole{testHumanID: domain.ChannelRoleMember, testBotID: domain.ChannelRoleMember},
			status:  map[int64]domain.ChannelMemberStatus{testHumanID: domain.ChannelMemberActive, testBotID: domain.ChannelMemberActive},
			channel: domain.Channel{ID: testChannel, Megagroup: true},
		},
	}
	f.service = NewService(f.store, f.channels, testUsers{
		testHumanID: {ID: testHumanID, Username: "alice"},
		testBotID:   {ID: testBotID, Username: "private_bot", Bot: true, BotInfoVersion: 1},
	}, testBots{testBotID: {{Command: "private", Description: "private", Ephemeral: true}, {Command: "public", Description: "public"}}},
		WithClock(func() time.Time { return f.now }),
		WithIDGenerator(func() (int, error) { f.nextID++; return f.nextID, nil }))
	return f
}

func (f *serviceFixture) clientRequest() domain.SendClientEphemeralRequest {
	return domain.SendClientEphemeralRequest{
		SenderUserID: testHumanID, ReceiverBotID: testBotID,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: testChannel},
		RandomID: 91, Content: domain.EphemeralContent{Message: "/private@private_bot hello"},
		OriginDevice: domain.EphemeralDevice{UserID: testHumanID, BusinessAuthKeyID: testDeviceKey, SessionID: testSession},
	}
}

func TestSendFromClientRequiresEphemeralCommandAndPreservesDevice(t *testing.T) {
	f := newServiceFixture()
	message, fresh, err := f.service.SendFromClient(context.Background(), f.clientRequest())
	if err != nil || !fresh {
		t.Fatalf("send = %+v fresh=%v err=%v", message, fresh, err)
	}
	if message.SenderUserID != testHumanID || message.ReceiverUserID != testBotID || message.OriginDevice.BusinessAuthKeyID != testDeviceKey {
		t.Fatalf("message = %+v", message)
	}
	request := f.clientRequest()
	request.RandomID++
	request.Content.Message = "/public"
	if _, _, err := f.service.SendFromClient(context.Background(), request); !errors.Is(err, domain.ErrEphemeralCommandInvalid) {
		t.Fatalf("ordinary command err=%v", err)
	}
}

func TestDeletedCreateReplayReturnsTombstoneWithoutResurrection(t *testing.T) {
	f := newServiceFixture()
	request := f.clientRequest()
	message, fresh, err := f.service.SendFromClient(context.Background(), request)
	if err != nil || !fresh {
		t.Fatalf("create fresh=%v err=%v", fresh, err)
	}
	device := request.OriginDevice
	if _, changed, err := f.service.DeleteFromDevice(context.Background(), testHumanID, testBotID, device, message.Peer, message.ID); err != nil || !changed {
		t.Fatalf("delete changed=%v err=%v", changed, err)
	}
	replayed, fresh, err := f.service.SendFromClient(context.Background(), request)
	if err != nil || fresh || !replayed.Deleted || replayed.ID != message.ID || replayed.Version != 2 {
		t.Fatalf("replay=%+v fresh=%v err=%v", replayed, fresh, err)
	}
}

func TestClientReplyMustMatchTargetDevice(t *testing.T) {
	f := newServiceFixture()
	incoming := f.putIncoming(t, testDeviceKey, f.now)
	request := f.clientRequest()
	request.Content.Message = "reply"
	request.ReplyToEphemeralID = incoming.ID
	reply, fresh, err := f.service.SendFromClient(context.Background(), request)
	if err != nil || !fresh || reply.BotAPIReply == nil || reply.BotAPIReply.ID != incoming.ID {
		t.Fatalf("reply=%+v fresh=%v err=%v", reply, fresh, err)
	}
	request.RandomID++
	request.OriginDevice.BusinessAuthKeyID = [8]byte{9}
	if _, _, err := f.service.SendFromClient(context.Background(), request); !errors.Is(err, domain.ErrEphemeralDeviceMismatch) {
		t.Fatalf("other device reply err=%v", err)
	}
}

func TestBotReplyWindowAndAdminBroadcast(t *testing.T) {
	f := newServiceFixture()
	action, _, err := f.service.SendFromClient(context.Background(), f.clientRequest())
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(14 * time.Second)
	reply, fresh, err := f.service.SendFromBot(context.Background(), domain.SendBotEphemeralRequest{
		BotUserID: testBotID, ReceiverUserID: testHumanID,
		Peer: action.Peer, RandomID: 92, Content: domain.EphemeralContent{Message: "answer"}, ActionMessageID: action.ID,
	})
	if err != nil || !fresh || reply.OriginDevice.BusinessAuthKeyID != testDeviceKey || reply.ReplyToEphemeralID != action.ID ||
		reply.BotAPIReply == nil || reply.BotAPIReply.ID != action.ID {
		t.Fatalf("bot reply = %+v fresh=%v err=%v", reply, fresh, err)
	}
	f.now = f.now.Add(2 * time.Second)
	if _, _, err := f.service.SendFromBot(context.Background(), domain.SendBotEphemeralRequest{
		BotUserID: testBotID, ReceiverUserID: testHumanID, Peer: action.Peer,
		RandomID: 93, Content: domain.EphemeralContent{Message: "late"}, ActionMessageID: action.ID,
	}); !errors.Is(err, domain.ErrEphemeralReplyExpired) {
		t.Fatalf("late bot reply err=%v", err)
	}
	f.channels.roles[testBotID] = domain.ChannelRoleAdmin
	broadcast, _, err := f.service.SendFromBot(context.Background(), domain.SendBotEphemeralRequest{
		BotUserID: testBotID, ReceiverUserID: testHumanID, Peer: action.Peer,
		RandomID: 94, Content: domain.EphemeralContent{Message: "admin"},
	})
	if err != nil || broadcast.OriginDevice.BusinessAuthKeyID != ([8]byte{}) {
		t.Fatalf("admin broadcast = %+v err=%v", broadcast, err)
	}
}

func TestCallbackAndDeleteEnforceParticipantsAndDevice(t *testing.T) {
	f := newServiceFixture()
	incoming := f.putIncoming(t, testDeviceKey, f.now)
	device := domain.EphemeralDevice{UserID: testHumanID, BusinessAuthKeyID: testDeviceKey, SessionID: testSession}
	callback, err := f.service.Callback(context.Background(), testHumanID, device, incoming.Peer, incoming.ID, []byte("ok"))
	if err != nil || callback.BotUserID != testBotID || string(callback.Data) != "ok" {
		t.Fatalf("callback = %+v err=%v", callback, err)
	}
	device.BusinessAuthKeyID = [8]byte{7}
	if _, err := f.service.Callback(context.Background(), testHumanID, device, incoming.Peer, incoming.ID, []byte("ok")); !errors.Is(err, domain.ErrEphemeralDeviceMismatch) {
		t.Fatalf("other device callback err=%v", err)
	}
	if _, _, err := f.service.DeleteFromDevice(context.Background(), testHumanID, testHumanID, device, incoming.Peer, incoming.ID); !errors.Is(err, domain.ErrEphemeralDeviceMismatch) {
		t.Fatalf("other device delete err=%v", err)
	}
	device.BusinessAuthKeyID = testDeviceKey
	deleted, changed, err := f.service.DeleteFromDevice(context.Background(), testHumanID, testHumanID, device, incoming.Peer, incoming.ID)
	if err != nil || !changed || !deleted.Deleted {
		t.Fatalf("delete = %+v changed=%v err=%v", deleted, changed, err)
	}
}

func TestCallbackActionTargetsExactDeviceAndExpiresAtFifteenSeconds(t *testing.T) {
	f := newServiceFixture()
	incoming := f.putIncoming(t, testDeviceKey, f.now)
	device := domain.EphemeralDevice{UserID: testHumanID, BusinessAuthKeyID: testDeviceKey, SessionID: testSession}
	callback, err := f.service.Callback(context.Background(), testHumanID, device, incoming.Peer, incoming.ID, []byte("ok"))
	if err != nil {
		t.Fatal(err)
	}
	const queryID = int64(777)
	created, err := f.service.PutCallbackAction(context.Background(), domain.EphemeralCallbackAction{
		QueryID: queryID, BotUserID: testBotID, UserID: testHumanID, Peer: incoming.Peer,
		MessageID: incoming.ID, Device: callback.Device, CreatedAt: f.now,
		ExpiresAt: f.now.Add(domain.EphemeralReplyWindow),
	})
	if err != nil || !created {
		t.Fatalf("put callback action created=%v err=%v", created, err)
	}
	reply, fresh, err := f.service.SendFromBot(context.Background(), domain.SendBotEphemeralRequest{
		BotUserID: testBotID, ReceiverUserID: testHumanID, Peer: incoming.Peer,
		CallbackQueryID: queryID, Content: domain.EphemeralContent{Message: "callback response"},
	})
	if err != nil || !fresh || reply.OriginDevice.BusinessAuthKeyID != testDeviceKey {
		t.Fatalf("callback reply=%+v fresh=%v err=%v", reply, fresh, err)
	}
	f.now = f.now.Add(domain.EphemeralReplyWindow)
	if _, _, err := f.service.SendFromBot(context.Background(), domain.SendBotEphemeralRequest{
		BotUserID: testBotID, ReceiverUserID: testHumanID, Peer: incoming.Peer,
		CallbackQueryID: queryID, Content: domain.EphemeralContent{Message: "too late"},
	}); !errors.Is(err, domain.ErrEphemeralReplyExpired) {
		t.Fatalf("expired callback action err=%v", err)
	}
}

func TestForumRepliesInheritTopicAndNonForumRejectsTopic(t *testing.T) {
	f := newServiceFixture()
	f.channels.channel.Forum = true
	incoming := f.putIncomingInTopic(t, testDeviceKey, f.now, 42)
	request := f.clientRequest()
	request.Content.Message = "topic reply"
	request.ReplyToEphemeralID = incoming.ID
	reply, _, err := f.service.SendFromClient(context.Background(), request)
	if err != nil || reply.TopMessageID != 42 {
		t.Fatalf("topic reply=%+v err=%v", reply, err)
	}

	f = newServiceFixture()
	request = f.clientRequest()
	request.TopMessageID = 42
	if _, _, err := f.service.SendFromClient(context.Background(), request); !errors.Is(err, domain.ErrEphemeralPeerInvalid) {
		t.Fatalf("non-forum topic err=%v", err)
	}
}

func TestEphemeralTextLimitCountsUnicodeCharacters(t *testing.T) {
	f := newServiceFixture()
	request := f.clientRequest()
	request.Content.Message = "/private " + strings.Repeat("界", domain.MaxMessageTextLength-len("/private "))
	if _, _, err := f.service.SendFromClient(context.Background(), request); err != nil {
		t.Fatalf("4096 Unicode characters rejected: %v", err)
	}
	request.RandomID++
	request.Content.Message += "界"
	if _, _, err := f.service.SendFromClient(context.Background(), request); !errors.Is(err, domain.ErrEphemeralInvalid) {
		t.Fatalf("overlong Unicode text err=%v", err)
	}
}

func TestBotEditModesCannotCrossTextAndMediaShapes(t *testing.T) {
	f := newServiceFixture()
	textMessage := f.putIncoming(t, testDeviceKey, f.now)
	if _, err := f.service.EditFieldsFromBot(context.Background(), testBotID, testHumanID, textMessage.Peer, textMessage.ID,
		domain.EphemeralEditText, domain.EditEphemeralFields{SetMessage: true, Message: "edited"}); err != nil {
		t.Fatalf("text edit: %v", err)
	}
	if _, err := f.service.EditFieldsFromBot(context.Background(), testBotID, testHumanID, textMessage.Peer, textMessage.ID,
		domain.EphemeralEditCaption, domain.EditEphemeralFields{SetMessage: true, Message: "caption"}); !errors.Is(err, domain.ErrEphemeralInvalid) {
		t.Fatalf("caption edit on text err=%v", err)
	}

	mediaMessage := f.putIncoming(t, testDeviceKey, f.now)
	mediaContent := domain.EphemeralContent{
		Message: "caption",
		Media:   &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 99}},
	}
	mediaMessage, err := f.store.EditEphemeralMessage(context.Background(), mediaMessage.Peer, mediaMessage.ID, mediaMessage.Version, mediaContent, int(f.now.Unix()), f.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.EditFieldsFromBot(context.Background(), testBotID, testHumanID, mediaMessage.Peer, mediaMessage.ID,
		domain.EphemeralEditCaption, domain.EditEphemeralFields{SetMessage: true, Message: "new caption"}); err != nil {
		t.Fatalf("media caption edit: %v", err)
	}
	if _, err := f.service.EditFieldsFromBot(context.Background(), testBotID, testHumanID, mediaMessage.Peer, mediaMessage.ID,
		domain.EphemeralEditText, domain.EditEphemeralFields{SetMessage: true, Message: "turn into text"}); !errors.Is(err, domain.ErrEphemeralInvalid) {
		t.Fatalf("text edit on media err=%v", err)
	}
}

func TestBotLazyBuildersRunOnlyAfterAuthorization(t *testing.T) {
	f := newServiceFixture()
	builds := 0
	buildText := func(context.Context) (domain.EphemeralContent, error) {
		builds++
		return domain.EphemeralContent{Message: "authorized"}, nil
	}
	request := domain.SendBotEphemeralRequest{
		BotUserID: testBotID, ReceiverUserID: testHumanID + 99,
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: testChannel},
	}
	if _, _, err := f.service.SendFromBotLazy(context.Background(), request, buildText); err == nil {
		t.Fatal("unknown receiver was accepted")
	}
	if builds != 0 {
		t.Fatalf("unauthorized send materialized content %d times", builds)
	}

	f.channels.roles[testBotID] = domain.ChannelRoleAdmin
	request.ReceiverUserID = testHumanID
	if _, fresh, err := f.service.SendFromBotLazy(context.Background(), request, buildText); err != nil || !fresh {
		t.Fatalf("authorized lazy send fresh=%v err=%v", fresh, err)
	}
	if builds != 1 {
		t.Fatalf("authorized send materialized content %d times", builds)
	}

	incoming := f.putIncoming(t, testDeviceKey, f.now)
	editBuilds := 0
	buildEdit := func(context.Context) (domain.EditEphemeralFields, error) {
		editBuilds++
		return domain.EditEphemeralFields{SetMessage: true, Message: "edited"}, nil
	}
	if _, err := f.service.EditFieldsFromBotLazy(context.Background(), testBotID+99, testHumanID, incoming.Peer, incoming.ID,
		domain.EphemeralEditText, buildEdit); !errors.Is(err, domain.ErrEphemeralForbidden) {
		t.Fatalf("unauthorized lazy edit err=%v", err)
	}
	if editBuilds != 0 {
		t.Fatalf("unauthorized edit materialized content %d times", editBuilds)
	}
	if _, err := f.service.EditFieldsFromBotLazy(context.Background(), testBotID, testHumanID, incoming.Peer, incoming.ID,
		domain.EphemeralEditText, buildEdit); err != nil {
		t.Fatalf("authorized lazy edit: %v", err)
	}
	if editBuilds != 1 {
		t.Fatalf("authorized edit materialized content %d times", editBuilds)
	}
}

func (f *serviceFixture) putIncoming(t *testing.T, deviceKey [8]byte, createdAt time.Time) domain.EphemeralMessage {
	return f.putIncomingInTopic(t, deviceKey, createdAt, 0)
}

func (f *serviceFixture) putIncomingInTopic(t *testing.T, deviceKey [8]byte, createdAt time.Time, topMessageID int) domain.EphemeralMessage {
	t.Helper()
	f.nextID++
	message := domain.EphemeralMessage{
		ID: f.nextID, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: testChannel},
		SenderUserID: testBotID, ReceiverUserID: testHumanID, Date: int(createdAt.Unix()), RandomID: int64(f.nextID),
		TopMessageID: topMessageID,
		Content: domain.EphemeralContent{Message: "incoming", ReplyMarkup: &domain.MessageReplyMarkup{
			Type:   domain.MessageReplyMarkupInline,
			Inline: [][]domain.MarkupButton{{{Type: domain.MarkupButtonCallback, Text: "OK", Data: []byte("ok")}}},
		}},
		OriginDevice: domain.EphemeralDevice{UserID: testHumanID, BusinessAuthKeyID: deviceKey, SessionID: testSession},
		PayloadHash:  sha256.Sum256([]byte("incoming")), Version: 1,
		CreatedAt: createdAt, ExpiresAt: createdAt.Add(domain.EphemeralMessageRetention),
	}
	stored, _, err := f.store.CreateEphemeralMessage(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}
