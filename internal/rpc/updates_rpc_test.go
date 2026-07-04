package rpc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appupdates "telesrv/internal/app/updates"
	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
	"telesrv/internal/store/memory"
)

func TestBootstrapLoginMessagePublishesNewMessageAfterReady(t *testing.T) {
	bootstrap := memory.NewBootstrapUpdateJobStore()
	updates := &captureUpdates{state: domain.UpdateState{Pts: 3, Date: 1700000000}}
	messages := &captureMessages{
		list: domain.MessageList{Messages: []domain.Message{{
			ID:          99,
			OwnerUserID: 1000000001,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
			Date:        1700000100,
			Body:        "Login code: 12345",
			Entities:    []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 11}},
		}}},
	}
	r := New(Config{}, Deps{BootstrapUpdates: bootstrap, Updates: updates, Messages: messages}, zaptest.NewLogger(t), clock.System)
	msg := domain.Message{
		ID:          99,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
		Entities:    []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 11}},
	}
	authKeyID := [8]byte{1, 2, 3}
	sessionID := int64(55)
	ctx := WithSessionID(WithAuthKeyID(context.Background(), authKeyID), sessionID)
	r.enqueueLoginMessageBootstrap(ctx, msg)
	claimed := r.publishReadyBootstrapUpdates(context.Background(), 10, time.Second, zaptest.NewLogger(t))
	if claimed != 0 || len(updates.events) != 0 {
		t.Fatalf("published before ready = claimed %d events %d, want none", claimed, len(updates.events))
	}

	ready, err := bootstrap.MarkReadyForSession(context.Background(), msg.OwnerUserID, authKeyID, sessionID)
	if err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if ready != 1 {
		t.Fatalf("ready jobs = %d, want 1", ready)
	}
	claimed = r.publishReadyBootstrapUpdates(context.Background(), 10, time.Second, zaptest.NewLogger(t))
	if claimed != 1 {
		t.Fatalf("claimed jobs = %d, want 1", claimed)
	}
	if len(updates.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(updates.events))
	}
	event := updates.events[0]
	if event.Type != domain.UpdateEventNewMessage || event.Message.ID != msg.ID || event.Pts != 4 {
		t.Fatalf("event = %+v, want login message pts 4", event)
	}
}

func TestUpdatesGetStatePublishesBootstrapAfterRPCResult(t *testing.T) {
	bootstrap := memory.NewBootstrapUpdateJobStore()
	updates := &captureUpdates{state: domain.UpdateState{Pts: 0, Date: 1700000000}}
	msg := domain.Message{
		ID:          1,
		OwnerUserID: 1780243001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
	}
	messages := &captureMessages{list: domain.MessageList{Messages: []domain.Message{msg}}}
	r := New(Config{}, Deps{BootstrapUpdates: bootstrap, Updates: updates, Messages: messages}, zaptest.NewLogger(t), clock.System)
	authKeyID := [8]byte{9, 8, 7}
	sessionID := int64(5723482677041206318)
	ctx := postresponse.WithCallbacks(
		WithUserID(
			WithSessionID(WithAuthKeyID(context.Background(), authKeyID), sessionID),
			msg.OwnerUserID,
		),
	)
	r.enqueueLoginMessageBootstrap(ctx, msg)

	state, err := r.onUpdatesGetState(ctx)
	if err != nil {
		t.Fatalf("getState: %v", err)
	}
	if state.Pts != 0 {
		t.Fatalf("state pts = %d, want 0 before bootstrap publish", state.Pts)
	}
	if claimed := r.publishReadyBootstrapUpdates(context.Background(), 10, time.Second, zaptest.NewLogger(t)); claimed != 0 {
		t.Fatalf("bootstrap published before post-response callback: %d", claimed)
	}
	if len(updates.events) != 0 {
		t.Fatalf("events before post-response = %d, want 0", len(updates.events))
	}

	postresponse.Run(ctx)
	if len(updates.events) != 1 {
		t.Fatalf("events after post-response = %d, want 1", len(updates.events))
	}
	if got := updates.events[0]; got.Type != domain.UpdateEventNewMessage || got.Message.ID != msg.ID {
		t.Fatalf("event after post-response = %+v, want login message", got)
	}
}

func TestSignInServiceNotificationMatchesEnterpriseShape(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 9
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithClientInfo(context.Background(), ClientInfo{
		DeviceModel:   "Telegram Desktop",
		SystemVersion: "Windows",
		AppVersion:    "6.8.4",
	})

	got := r.tgSignInServiceNotification(ctx, domain.User{
		ID:        1000000001,
		FirstName: "Test",
		LastName:  "User",
	}, authKeyID)

	if len(got.Updates) != 1 {
		t.Fatalf("updates = %+v, want one service notification", got.Updates)
	}
	update, ok := got.Updates[0].(*tg.UpdateServiceNotification)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateServiceNotification", got.Updates[0])
	}
	if update.Popup || update.InboxDate == 0 || update.Media == nil {
		t.Fatalf("notification flags/media = popup %v inbox %d media %T", update.Popup, update.InboxDate, update.Media)
	}
	for _, want := range []string{"New login.", "Test User", "Telegram Desktop", "Settings > Devices"} {
		if !strings.Contains(update.Message, want) {
			t.Fatalf("notification message %q missing %q", update.Message, want)
		}
	}
	if len(update.Entities) < 3 {
		t.Fatalf("entities = %+v, want bold entities for title/settings links", update.Entities)
	}
}

func TestLogOutClearsSessionAndUpdateState(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 5
	auth := &captureAuthService{}
	updates := &captureUpdates{}
	sessions := &captureSessions{authKeyID: authKeyID, authKeyResolved: true, userID: 1000000001, userResolved: true}
	r := New(Config{}, Deps{
		Auth:     auth,
		Updates:  updates,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	_, err := r.onAuthLogOut(WithAuthKeyID(context.Background(), authKeyID))
	if err != nil {
		t.Fatalf("auth.logOut: %v", err)
	}
	if auth.loggedOutAuthKeyID != authKeyID {
		t.Fatalf("logged out auth key = %x, want %x", auth.loggedOutAuthKeyID, authKeyID)
	}
	if updates.clearedAuthKeyID != authKeyID || !updates.cleared {
		t.Fatalf("cleared auth key = %x cleared=%v, want %x", updates.clearedAuthKeyID, updates.cleared, authKeyID)
	}
	gotSession := sessions.snapshot()
	if gotSession.userID != 0 || !gotSession.userResolved {
		t.Fatalf("session user after logout = %d resolved=%v, want 0/true", gotSession.userID, gotSession.userResolved)
	}
}

func TestSignInDifferentUserClearsAuthKeyUpdateState(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 6
	auth := &captureAuthService{signInUser: domain.User{ID: 1000000002, FirstName: "Two"}}
	updates := &captureUpdates{}
	sessions := &captureSessions{authKeyID: authKeyID, authKeyResolved: true, userID: 1000000001, userResolved: true}
	r := New(Config{}, Deps{
		Auth:     auth,
		Updates:  updates,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(WithAuthKeyID(WithSessionID(context.Background(), 77), authKeyID), 1000000001)

	_, err := r.onAuthSignIn(ctx, &tg.AuthSignInRequest{PhoneNumber: "15550000002", PhoneCodeHash: "hash", PhoneCode: "12345"})
	if err != nil {
		t.Fatalf("auth.signIn: %v", err)
	}
	if updates.clearedAuthKeyID != authKeyID || !updates.cleared {
		t.Fatalf("cleared auth key = %x cleared=%v, want %x", updates.clearedAuthKeyID, updates.cleared, authKeyID)
	}
	gotSession := sessions.snapshot()
	if gotSession.userID != 1000000002 {
		t.Fatalf("session user = %d, want new user 1000000002", gotSession.userID)
	}
}

func TestUpdatesGetStateMarksSessionReadyForPush(t *testing.T) {
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Sessions: sessions,
		Updates:  &captureUpdates{state: domain.UpdateState{Pts: 3, Date: 1700000000, Seq: 2}},
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onUpdatesGetState(WithSessionID(context.Background(), 77))
	if err != nil {
		t.Fatalf("updates.getState: %v", err)
	}
	if got.Pts != 3 || got.Seq != 2 {
		t.Fatalf("state = %+v, want pts=3 seq=2", got)
	}
	gotSession := sessions.snapshot()
	if gotSession.sessionID != 77 || !gotSession.receives {
		t.Fatalf("session ready = id %d receives %v, want 77/true", gotSession.sessionID, gotSession.receives)
	}
}

// TestUpdatesGetStateReturnsAccountCurrentState 复现 TDesktop 冷启动未读重复：
// getState 必须返回账号当前最新状态（而非设备旧确认水位），且不得再推
// updatesTooLong 诱导差分——TDesktop 不持久化 pts，启动期离线数据由
// getDialogs 快照承载，旧水位差分会把快照里已计入的消息重放一遍
// （实测未读 2→3、dialog 预览被旧消息抢占）。
func TestUpdatesGetStateReturnsAccountCurrentState(t *testing.T) {
	sessions := &captureSessions{}
	current := domain.UpdateState{Pts: 4, Date: 1700000001}
	updates := &captureUpdates{
		state:        domain.UpdateState{Pts: 3, Date: 1700000000},
		currentState: &current,
	}
	r := New(Config{}, Deps{
		Sessions: sessions,
		Updates:  updates,
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onUpdatesGetState(WithSessionID(context.Background(), 77))
	if err != nil {
		t.Fatalf("updates.getState: %v", err)
	}
	if got.Pts != 4 {
		t.Fatalf("state pts = %d, want account current 4 (per-key stale=3 会触发客户端重放)", got.Pts)
	}
	if !updates.acknowledged {
		t.Fatal("getState 未推进设备确认水位")
	}
	time.Sleep(500 * time.Millisecond)
	if msg := sessions.snapshot().message; msg != nil {
		if _, ok := msg.(*tg.UpdatesTooLong); ok {
			t.Fatal("getState 后不应再推 updatesTooLong（会诱导 TDesktop 重放快照前差分）")
		}
	}
}

func TestUpdatesDifferenceIncludesLoginMessageAndOfficialUser(t *testing.T) {
	msg := domain.Message{
		ID:          88,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
	}
	got, ok := tgUpdatesDifference(0, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 5, Date: msg.Date, Seq: 4},
		Events: []domain.UpdateEvent{{
			Type:     domain.UpdateEventNewMessage,
			Pts:      5,
			PtsCount: 1,
			Date:     msg.Date,
			Message:  msg,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 5 || len(got.NewMessages) != 1 || len(got.Users) != 1 {
		t.Fatalf("difference = %+v, want one message, one official user, pts=5", got)
	}
	user, ok := got.Users[0].(*tg.User)
	if !ok || user.ID != domain.OfficialSystemUserID {
		t.Fatalf("user = %#v, want official system user", got.Users[0])
	}
}

func TestUpdatesDifferenceMarksViewerUserAsSelf(t *testing.T) {
	viewerID := int64(1000000001)
	msg := domain.Message{
		ID:          90,
		OwnerUserID: viewerID,
		Out:         true,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: viewerID},
		Date:        1700000102,
		Body:        "self probe",
	}
	got, ok := tgUpdatesDifference(viewerID, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 7, Date: msg.Date},
		Events: []domain.UpdateEvent{{
			UserID:   viewerID,
			Type:     domain.UpdateEventNewMessage,
			Pts:      7,
			PtsCount: 1,
			Date:     msg.Date,
			Message:  msg,
			Users: []domain.User{
				{ID: viewerID, FirstName: "Me"},
				{ID: 1000000002, FirstName: "Peer"},
			},
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	var sawSelf, sawPeer bool
	for _, item := range got.Users {
		user, ok := item.(*tg.User)
		if !ok {
			t.Fatalf("user = %#v, want *tg.User", item)
		}
		switch user.ID {
		case viewerID:
			sawSelf = true
			if !user.Self {
				t.Fatalf("viewer user = %#v, want self flag set: clients persist this object as the current account", user)
			}
		case 1000000002:
			sawPeer = true
			if user.Self {
				t.Fatalf("peer user = %#v, must not carry self flag", user)
			}
		}
	}
	if !sawSelf || !sawPeer {
		t.Fatalf("users = %+v, want viewer and peer entries", got.Users)
	}
}

func TestUpdatesDifferenceIncludesForwardSourceChannelChat(t *testing.T) {
	source := domain.Channel{
		ID:         2000000001,
		AccessHash: 9001,
		Title:      "Source Channel",
		Broadcast:  true,
		Date:       1700000000,
	}
	msg := domain.Message{
		ID:          89,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000101,
		Body:        "forwarded",
		Forward:     &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: source.ID}, Date: 1700000000},
	}
	got, ok := tgUpdatesDifference(0, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 6, Date: msg.Date},
		Events: []domain.UpdateEvent{{
			UserID:   msg.OwnerUserID,
			Type:     domain.UpdateEventNewMessage,
			Pts:      6,
			PtsCount: 1,
			Date:     msg.Date,
			Message:  msg,
			Channels: []domain.Channel{source},
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.Chats) != 1 {
		t.Fatalf("chats = %+v, want source channel", got.Chats)
	}
	ch, ok := got.Chats[0].(*tg.Channel)
	if !ok || ch.ID != source.ID || ch.Title != source.Title {
		t.Fatalf("chat = %#v, want source channel", got.Chats[0])
	}
}

func TestOutboxEventIncludesForwardSourceChannelChat(t *testing.T) {
	source := domain.Channel{
		ID:         2000000002,
		AccessHash: 9002,
		Title:      "Forward Source",
		Broadcast:  true,
		Date:       1700000000,
	}
	update := tgUpdateForOutboxEvent(domain.UpdateEvent{
		UserID:   1000000001,
		Type:     domain.UpdateEventNewMessage,
		Pts:      7,
		PtsCount: 1,
		Date:     1700000102,
		Message: domain.Message{
			ID:          90,
			OwnerUserID: 1000000001,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
			Date:        1700000102,
			Body:        "forwarded",
			Forward:     &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: source.ID}, Date: 1700000000},
		},
		Channels: []domain.Channel{source},
	})
	if update == nil || len(update.Chats) != 1 {
		t.Fatalf("update = %+v, want source channel chat", update)
	}
	ch, ok := update.Chats[0].(*tg.Channel)
	if !ok || ch.ID != source.ID {
		t.Fatalf("chat = %#v, want source channel", update.Chats[0])
	}
}

func TestOutboxEventChannelViewForumAsMessages(t *testing.T) {
	update := tgUpdateForOutboxEvent(domain.UpdateEvent{
		UserID: 1000000001,
		Type:   domain.UpdateEventChannelViewForum,
		Peer:   domain.Peer{Type: domain.PeerTypeChannel, ID: 2000000002},
		Bool:   true,
		Date:   1700000200,
	})
	if update == nil || len(update.Updates) != 1 {
		t.Fatalf("update = %+v, want one updateChannelViewForumAsMessages", update)
	}
	got, ok := update.Updates[0].(*tg.UpdateChannelViewForumAsMessages)
	if !ok || got.ChannelID != 2000000002 || !got.Enabled {
		t.Fatalf("update = %#v, want channel view-forum-as-messages enabled", update.Updates[0])
	}
}

func TestUpdatesDifferenceIncludesChannelTooLongNudge(t *testing.T) {
	const viewerID int64 = 1000000002
	got, ok := tgUpdatesDifference(viewerID, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 8, Date: 1700000250, Seq: 0},
		ChannelNudges: []domain.ChannelDifferenceNudge{{
			ChannelID: 2000000001,
			Pts:       12,
			Channel: &domain.ChannelView{
				Channel: domain.Channel{
					ID:         2000000001,
					AccessHash: 9100000001,
					Title:      "Dirty group",
					Megagroup:  true,
					Date:       1700000200,
				},
				Self: domain.ChannelMember{
					ChannelID: 2000000001,
					UserID:    viewerID,
					Role:      domain.ChannelRoleMember,
					Status:    domain.ChannelMemberActive,
				},
			},
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 8 || len(got.OtherUpdates) != 1 || len(got.Chats) != 1 {
		t.Fatalf("difference = %+v, want one channel nudge, one chat and account pts unchanged", got)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateChannelTooLong)
	if !ok || update.ChannelID != 2000000001 {
		t.Fatalf("update = %T %+v, want UpdateChannelTooLong", got.OtherUpdates[0], got.OtherUpdates[0])
	}
	if pts, ok := update.GetPts(); !ok || pts != 12 {
		t.Fatalf("channel nudge pts = %d set=%v, want 12", pts, ok)
	}
	chat, ok := got.Chats[0].(*tg.Channel)
	if !ok || chat.ID != 2000000001 || chat.Min {
		t.Fatalf("chat = %T %+v, want full channel chat for channel nudge", got.Chats[0], got.Chats[0])
	}
}

func TestUpdatesDifferenceChannelNudgeUpgradesExistingMinChat(t *testing.T) {
	const viewerID int64 = 1000000001
	channel := domain.Channel{
		ID:            2000000003,
		AccessHash:    9100000003,
		CreatorUserID: viewerID,
		Title:         "Offline created group",
		Megagroup:     true,
		Date:          1700000300,
	}
	got, ok := tgUpdatesDifference(viewerID, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 9, Date: 1700000310, Seq: 0},
		Events: []domain.UpdateEvent{{
			UserID:   viewerID,
			Type:     domain.UpdateEventChannelState,
			Pts:      8,
			PtsCount: 1,
			Date:     1700000300,
			Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID},
			Channels: []domain.Channel{channel},
		}},
		ChannelNudges: []domain.ChannelDifferenceNudge{{
			ChannelID: channel.ID,
			Pts:       3,
			Channel: &domain.ChannelView{
				Channel: channel,
				Self: domain.ChannelMember{
					ChannelID: channel.ID,
					UserID:    viewerID,
					Role:      domain.ChannelRoleCreator,
					Status:    domain.ChannelMemberActive,
				},
			},
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.OtherUpdates) != 2 || len(got.Chats) != 1 {
		t.Fatalf("difference = %+v, want channel state, nudge and one deduped chat", got)
	}
	chat, ok := got.Chats[0].(*tg.Channel)
	if !ok || chat.ID != channel.ID || chat.Min || !chat.Creator {
		t.Fatalf("chat = %T %+v, want upgraded full creator channel for Android unknown-channel recovery", got.Chats[0], got.Chats[0])
	}
}

func TestUpdatesGetDifferenceChannelNudgeIncludesFullChat(t *testing.T) {
	ctx := context.Background()
	const (
		ownerID  int64 = 1000000101
		memberID int64 = 1000000102
	)
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateMegagroupFromCreateChat(ctx, ownerID, domain.CreateChannelRequest{
		Title:         "Android visible group",
		Date:          1700000400,
		MemberUserIDs: []int64{memberID},
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	updateSvc := appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	r := New(Config{}, Deps{
		Updates:  updateSvc,
		Channels: channelSvc,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000410, 0)})

	diff, err := r.onUpdatesGetDifference(WithUserID(ctx, memberID), &tg.UpdatesGetDifferenceRequest{
		Date: 1700000399,
	})
	if err != nil {
		t.Fatalf("updates.getDifference: %v", err)
	}
	got, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", diff)
	}
	if len(got.OtherUpdates) != 1 || len(got.Chats) != 1 {
		t.Fatalf("difference = %+v, want channel nudge with chat", got)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateChannelTooLong)
	if !ok || update.ChannelID != created.Channel.ID {
		t.Fatalf("update = %T %+v, want updateChannelTooLong for channel %d", got.OtherUpdates[0], got.OtherUpdates[0], created.Channel.ID)
	}
	chat, ok := got.Chats[0].(*tg.Channel)
	if !ok || chat.ID != created.Channel.ID || chat.Min {
		t.Fatalf("chat = %T %+v, want full channel chat for Android unknown-channel recovery", got.Chats[0], got.Chats[0])
	}
	if chat.AccessHash == 0 {
		t.Fatalf("chat access_hash = 0, want full channel access hash")
	}
}

func TestUpdatesDifferenceIncludesSettingsUpdates(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	got, ok := tgUpdatesDifference(0, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 6, Date: 1700000300, Seq: 0},
		Events: []domain.UpdateEvent{
			{Type: domain.UpdateEventContactsReset, Pts: 1, PtsCount: 1, Date: 1700000300},
			{Type: domain.UpdateEventDialogPinned, Pts: 2, PtsCount: 1, Date: 1700000300, Peer: peer, Bool: true},
			{Type: domain.UpdateEventPinnedDialogs, Pts: 3, PtsCount: 1, Date: 1700000300, Peers: []domain.Peer{peer}},
			{Type: domain.UpdateEventDialogUnreadMark, Pts: 4, PtsCount: 1, Date: 1700000300, Peer: peer, Bool: false},
			{Type: domain.UpdateEventPeerSettings, Pts: 5, PtsCount: 1, Date: 1700000300, Peer: peer, Settings: domain.PeerSettings{ShareContact: true}},
			{Type: domain.UpdateEventPeerStoryBlocked, Pts: 6, PtsCount: 1, Date: 1700000300, Peer: peer, Bool: true},
		},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 6 || len(got.OtherUpdates) != 6 {
		t.Fatalf("difference = %+v, want six settings updates and pts=6", got)
	}
	if _, ok := got.OtherUpdates[0].(*tg.UpdateContactsReset); !ok {
		t.Fatalf("update[0] = %T, want contacts reset", got.OtherUpdates[0])
	}
	pinned, ok := got.OtherUpdates[1].(*tg.UpdateDialogPinned)
	if !ok || !pinned.Pinned {
		t.Fatalf("update[1] = %+v (%T), want pinned dialog", got.OtherUpdates[1], got.OtherUpdates[1])
	}
	pinnedDialogs, ok := got.OtherUpdates[2].(*tg.UpdatePinnedDialogs)
	if !ok || len(pinnedDialogs.Order) != 1 {
		t.Fatalf("update[2] = %T, want pinned dialogs", got.OtherUpdates[2])
	}
	unread, ok := got.OtherUpdates[3].(*tg.UpdateDialogUnreadMark)
	if !ok || unread.Unread {
		t.Fatalf("update[3] = %+v (%T), want unread=false mark", got.OtherUpdates[3], got.OtherUpdates[3])
	}
	peerSettings, ok := got.OtherUpdates[4].(*tg.UpdatePeerSettings)
	if !ok || !peerSettings.Settings.ShareContact {
		t.Fatalf("update[4] = %T, want peer settings", got.OtherUpdates[4])
	}
	peerBlocked, ok := got.OtherUpdates[5].(*tg.UpdatePeerBlocked)
	if !ok || !peerBlocked.Blocked || !peerBlocked.BlockedMyStoriesFrom {
		t.Fatalf("update[5] = %+v (%T), want story peer blocked", got.OtherUpdates[5], got.OtherUpdates[5])
	}
	if peerUser, ok := peerBlocked.PeerID.(*tg.PeerUser); !ok || peerUser.UserID != peer.ID {
		t.Fatalf("update[5] peer = %#v, want user %d", peerBlocked.PeerID, peer.ID)
	}
}
