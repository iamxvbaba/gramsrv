package rpc

import (
	"context"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
	"reflect"
	"strings"
	"sync"
	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
)

func TestDialogFilterFromGetDialogsRequestUsesAllParameters(t *testing.T) {
	r := New(Config{}, Deps{
		Users: staticUsersService{user: domain.User{ID: 1000000001, AccessHash: 42}},
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		ExcludePinned: true,
		OffsetDate:    1700000000,
		OffsetID:      22,
		OffsetPeer:    &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		Limit:         999,
		Hash:          123456,
	}
	req.SetFolderID(7)

	filter, err := r.dialogFilterFromRequest(context.Background(), 1000000001, req)
	if err != nil {
		t.Fatalf("dialogFilterFromRequest: %v", err)
	}
	if !filter.ExcludePinned || !filter.HasFolderID || filter.FolderID != 7 {
		t.Fatalf("folder/pinned filter = %+v", filter)
	}
	if filter.OffsetDate != req.OffsetDate || filter.OffsetID != req.OffsetID || !filter.HasOffsetPeer || filter.OffsetPeer.ID != domain.OfficialSystemUserID {
		t.Fatalf("offset filter = %+v", filter)
	}
	if filter.Limit != 500 || filter.Hash != req.Hash {
		t.Fatalf("limit/hash filter = %+v", filter)
	}
}

func TestMessagesGetDialogsReturnsNotModifiedFromFullListHash(t *testing.T) {
	dialogs := &captureDialogs{list: domain.DialogList{Count: 3, Hash: 77}}
	r := New(Config{}, Deps{Dialogs: dialogs}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
		Hash:       77,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.MessagesDialogsBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesDialogsBox", enc)
	}
	got, ok := box.Dialogs.(*tg.MessagesDialogsNotModified)
	if !ok {
		t.Fatalf("boxed response = %T, want *tg.MessagesDialogsNotModified", box.Dialogs)
	}
	if got.Count != 3 || dialogs.filter.Hash != 77 {
		t.Fatalf("not modified = %+v filter %+v, want count/hash from service", got, dialogs.filter)
	}
}

func TestMessagesGetDialogsHashHitSkipsFullListLoad(t *testing.T) {
	dialogs := &captureDialogs{hashCheck: domain.DialogHashCheck{
		Known:   true,
		Matched: true,
		Hash:    77,
		Count:   5,
	}}
	r := New(Config{}, Deps{Dialogs: dialogs}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      20,
		Hash:       77,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.MessagesDialogsBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesDialogsBox", enc)
	}
	got, ok := box.Dialogs.(*tg.MessagesDialogsNotModified)
	if !ok {
		t.Fatalf("boxed response = %T, want *tg.MessagesDialogsNotModified", box.Dialogs)
	}
	if got.Count != 5 {
		t.Fatalf("not modified count = %d, want 5", got.Count)
	}
	if dialogs.hashCalls != 1 || dialogs.getDialogsCalls != 0 {
		t.Fatalf("hash/full calls = %d/%d, want 1/0", dialogs.hashCalls, dialogs.getDialogsCalls)
	}
	if dialogs.hashFilter.Hash != 77 || dialogs.hashFilter.OffsetID != 0 || dialogs.hashFilter.HasOffsetPeer {
		t.Fatalf("hash filter = %+v, want request hash without offset", dialogs.hashFilter)
	}
}

func TestMessagesGetPeerDialogsReturnsRequestedDialogsAndState(t *testing.T) {
	msg := domain.Message{
		ID:          3,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000100,
		Body:        "Login code: 12345",
	}
	dialogs := &captureDialogs{peerList: domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           msg.Peer,
			TopMessage:     msg.ID,
			TopMessageDate: msg.Date,
			UnreadCount:    1,
		}},
		Messages: []domain.Message{msg},
		Users:    []domain.User{domain.OfficialSystemUser()},
	}}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 8, Date: 1700000200, Seq: 4}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetPeerDialogsRequest{
		Peers: []tg.InputDialogPeerClass{&tg.InputDialogPeer{
			Peer: &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		}},
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesPeerDialogs)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesPeerDialogs", enc)
	}
	if len(dialogs.peers) != 1 || dialogs.peers[0].ID != domain.OfficialSystemUserID {
		t.Fatalf("requested peers = %+v, want official peer", dialogs.peers)
	}
	if got.State.Pts != 8 || got.State.Seq != 4 {
		t.Fatalf("state = %+v, want updates state", got.State)
	}
	if len(got.Dialogs) != 1 || len(got.Messages) != 1 || len(got.Users) != 1 {
		t.Fatalf("peer dialogs = %+v, want one dialog/message/user", got)
	}
}

func TestDialogSettingRPCsRecordDurableUpdates(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 9
	rawAuthKeyID := [8]byte{9, 7}
	ctx := WithRawAuthKeyID(
		WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55),
		rawAuthKeyID,
	)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	dialogPeer := &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: peer.ID}}
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 11, Date: 1700000400, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	toggle := &tg.MessagesToggleDialogPinRequest{Peer: dialogPeer}
	toggle.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(ctx, toggle); err != nil || !ok {
		t.Fatalf("toggle pin = %v, %v", ok, err)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventDialogPinned || updates.events[0].Peer != peer || !updates.events[0].Bool || updates.excludeSessionID != 55 {
		t.Fatalf("pin event = %+v, want durable dialog_pinned", updates.events)
	}
	if updates.authKeyID != authKeyID || updates.excludeAuthKeyID != rawAuthKeyID {
		t.Fatalf("durable update keys = state:%x exclude:%x, want business:%x raw:%x", updates.authKeyID, updates.excludeAuthKeyID, authKeyID, rawAuthKeyID)
	}

	if ok, err := r.onMessagesReorderPinnedDialogs(ctx, &tg.MessagesReorderPinnedDialogsRequest{Order: []tg.InputDialogPeerClass{dialogPeer}}); err != nil || !ok {
		t.Fatalf("reorder pinned = %v, %v", ok, err)
	}
	if len(updates.events) != 2 || updates.events[1].Type != domain.UpdateEventPinnedDialogs || len(updates.events[1].Peers) != 1 || updates.events[1].Peers[0] != peer {
		t.Fatalf("reorder event = %+v, want durable pinned_dialogs", updates.events)
	}

	mark := &tg.MessagesMarkDialogUnreadRequest{Peer: dialogPeer}
	mark.SetUnread(true)
	if ok, err := r.onMessagesMarkDialogUnread(ctx, mark); err != nil || !ok {
		t.Fatalf("mark unread = %v, %v", ok, err)
	}
	if len(updates.events) != 3 || updates.events[2].Type != domain.UpdateEventDialogUnreadMark || updates.events[2].Peer != peer || !updates.events[2].Bool {
		t.Fatalf("unread event = %+v, want durable dialog_unread_mark", updates.events)
	}

	parentMark := &tg.MessagesMarkDialogUnreadRequest{
		ParentPeer: &tg.InputPeerChannel{ChannelID: 9001},
		Peer:       &tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}},
	}
	parentMark.SetUnread(true)
	parentMark.SetParentPeer(parentMark.ParentPeer)
	if ok, err := r.onMessagesMarkDialogUnread(ctx, parentMark); err != nil || !ok {
		t.Fatalf("mark monoforum unread compat = %v, %v", ok, err)
	}
	if len(updates.events) != 3 {
		t.Fatalf("parent_peer unread compat recorded events = %+v, want no durable event", updates.events)
	}

	getParentMarks := &tg.MessagesGetDialogUnreadMarksRequest{ParentPeer: &tg.InputPeerChannel{ChannelID: 9001}}
	getParentMarks.SetParentPeer(getParentMarks.ParentPeer)
	marks, err := r.onMessagesGetDialogUnreadMarks(ctx, getParentMarks)
	if err != nil {
		t.Fatalf("get monoforum unread marks compat: %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("monoforum unread marks = %+v, want empty compat list", marks)
	}

	invalidParentMarks := &tg.MessagesGetDialogUnreadMarksRequest{ParentPeer: &tg.InputPeerSelf{}}
	invalidParentMarks.SetParentPeer(invalidParentMarks.ParentPeer)
	if _, err := r.onMessagesGetDialogUnreadMarks(ctx, invalidParentMarks); err == nil || !strings.Contains(err.Error(), "PARENT_PEER_INVALID") {
		t.Fatalf("get unread marks invalid parent err = %v, want PARENT_PEER_INVALID", err)
	}

	if ok, err := r.onMessagesHidePeerSettingsBar(ctx, &tg.InputPeerUser{UserID: peer.ID}); err != nil || !ok {
		t.Fatalf("hide peer settings = %v, %v", ok, err)
	}
	if len(updates.events) != 4 || updates.events[3].Type != domain.UpdateEventPeerSettings || updates.events[3].Peer != peer || !updates.events[3].Settings.HiddenPeerSettingsBar {
		t.Fatalf("peer settings event = %+v, want durable peer_settings", updates.events)
	}
}

func TestReorderPinnedDialogsNoopDoesNotRecordUpdate(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 9
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	dialogPeer := &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: peer.ID}}
	dialogs := &captureDialogs{reorderNoChange: true}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 11, Date: 1700000400, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	if ok, err := r.onMessagesReorderPinnedDialogs(ctx, &tg.MessagesReorderPinnedDialogsRequest{
		Order: []tg.InputDialogPeerClass{dialogPeer},
	}); err != nil || !ok {
		t.Fatalf("reorder pinned noop = %v, %v", ok, err)
	}
	if len(updates.events) != 0 {
		t.Fatalf("noop reorder recorded events = %+v, want none", updates.events)
	}
}

func TestDialogUnreadMarksIncludeChannelDialogs(t *testing.T) {
	ctx := context.Background()
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateMegagroupFromCreateChat(ctx, 1000000001, domain.CreateChannelRequest{
		Title: "Unread Mark Channel",
		Date:  1700001000,
	})
	if err != nil {
		t.Fatalf("CreateMegagroupFromCreateChat: %v", err)
	}
	dialogs := appdialogs.NewService(memory.NewDialogStore(), channelStore)
	r := New(Config{}, Deps{
		Channels: channelSvc,
		Dialogs:  dialogs,
	}, zaptest.NewLogger(t), clock.System)
	rpcCtx := WithUserID(ctx, 1000000001)
	input := &tg.InputDialogPeer{Peer: &tg.InputPeerChannel{
		ChannelID:  created.Channel.ID,
		AccessHash: created.Channel.AccessHash,
	}}

	mark := &tg.MessagesMarkDialogUnreadRequest{Peer: input}
	mark.SetUnread(true)
	if ok, err := r.onMessagesMarkDialogUnread(rpcCtx, mark); err != nil || !ok {
		t.Fatalf("mark channel unread = %v, %v", ok, err)
	}
	marks, err := r.onMessagesGetDialogUnreadMarks(rpcCtx, &tg.MessagesGetDialogUnreadMarksRequest{})
	if err != nil {
		t.Fatalf("get channel unread marks: %v", err)
	}
	if len(marks) != 1 {
		t.Fatalf("channel unread marks len = %d, want 1 (%+v)", len(marks), marks)
	}
	got, ok := marks[0].(*tg.DialogPeer)
	if !ok {
		t.Fatalf("channel unread mark = %T, want dialogPeer", marks[0])
	}
	peer, ok := got.Peer.(*tg.PeerChannel)
	if !ok || peer.ChannelID != created.Channel.ID {
		t.Fatalf("channel unread mark peer = %#v, want channel %d", got.Peer, created.Channel.ID)
	}
	dialogsList, err := dialogs.GetPeerDialogs(ctx, 1000000001, []domain.Peer{{Type: domain.PeerTypeChannel, ID: created.Channel.ID}})
	if err != nil {
		t.Fatalf("GetPeerDialogs: %v", err)
	}
	var dialog domain.Dialog
	for _, item := range dialogsList.Dialogs {
		if item.Peer.Type == domain.PeerTypeChannel && item.Peer.ID == created.Channel.ID {
			dialog = item
			break
		}
	}
	if dialog.Peer.ID == 0 {
		t.Fatalf("channel dialog missing from %+v", dialogsList.Dialogs)
	}
	if !dialog.UnreadMark {
		t.Fatalf("channel dialog unread mark = false, want true")
	}

	clear := &tg.MessagesMarkDialogUnreadRequest{Peer: input}
	if ok, err := r.onMessagesMarkDialogUnread(rpcCtx, clear); err != nil || !ok {
		t.Fatalf("clear channel unread = %v, %v", ok, err)
	}
	marks, err = r.onMessagesGetDialogUnreadMarks(rpcCtx, &tg.MessagesGetDialogUnreadMarksRequest{})
	if err != nil {
		t.Fatalf("get channel unread marks after clear: %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("channel unread marks after clear = %+v, want empty", marks)
	}
}

func TestDialogFolderRPCsPersistAndRecordUpdates(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 12
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 66)
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 21, Date: 1700000500, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	req := &tg.MessagesUpdateDialogFilterRequest{ID: 2}
	req.SetFilter(&tg.DialogFilter{
		ID:       2,
		Contacts: true,
		Title:    tg.TextWithEntities{Text: "Work"},
		IncludePeers: []tg.InputPeerClass{&tg.InputPeerUser{
			UserID:     1000000002,
			AccessHash: 222,
		}},
	})
	if ok, err := r.onMessagesUpdateDialogFilter(ctx, req); err != nil || !ok {
		t.Fatalf("update filter = %v, %v", ok, err)
	}
	if dialogs.savedFolder.ID != 2 || dialogs.savedFolder.Title != "Work" || !dialogs.savedFolder.Contacts || len(dialogs.savedFolder.IncludePeers) != 1 {
		t.Fatalf("saved folder = %+v, want parsed dialog folder", dialogs.savedFolder)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventDialogFilter || updates.events[0].FilterID != 2 || updates.excludeSessionID != 66 {
		t.Fatalf("filter event = %+v, want durable dialog_filter", updates.events)
	}

	if ok, err := r.onMessagesUpdateDialogFiltersOrder(ctx, []int{2, 2, 0, 3}); err != nil || !ok {
		t.Fatalf("update filter order = %v, %v", ok, err)
	}
	if len(dialogs.folderOrder) != 2 || dialogs.folderOrder[0] != 2 || dialogs.folderOrder[1] != 3 {
		t.Fatalf("folder order = %+v, want cleaned custom IDs", dialogs.folderOrder)
	}
	if len(updates.events) != 2 || updates.events[1].Type != domain.UpdateEventDialogFilterOrder {
		t.Fatalf("order event = %+v, want durable dialog_filter_order", updates.events)
	}

	if ok, err := r.onMessagesToggleDialogFilterTags(ctx, true); err != nil || !ok {
		t.Fatalf("toggle filter tags = %v, %v", ok, err)
	}
	if !dialogs.tagsEnabled || len(updates.events) != 3 || updates.events[2].Type != domain.UpdateEventDialogFilters {
		t.Fatalf("tags/update = tags %v events %+v, want reload update", dialogs.tagsEnabled, updates.events)
	}
}

func TestFoldersEditPeerFoldersPersistsArchiveAndReturnsPtsUpdate(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 13
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 77)
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 31, Date: 1700000600, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	got, err := r.onFoldersEditPeerFolders(ctx, []tg.InputFolderPeer{{
		Peer:     &tg.InputPeerUser{UserID: 1000000002},
		FolderID: domain.DialogArchiveFolderID,
	}})
	if err != nil {
		t.Fatalf("folders.editPeerFolders: %v", err)
	}
	if len(dialogs.folderPeers) != 1 || dialogs.folderPeers[0].FolderID != domain.DialogArchiveFolderID {
		t.Fatalf("folder peers = %+v, want archive update", dialogs.folderPeers)
	}
	out, ok := got.(*tg.Updates)
	if !ok || len(out.Updates) != 1 {
		t.Fatalf("updates = %T %+v, want tg.Updates with updateFolderPeers", got, got)
	}
	update, ok := out.Updates[0].(*tg.UpdateFolderPeers)
	if !ok || update.Pts != 31 || update.PtsCount != 1 || len(update.FolderPeers) != 1 {
		t.Fatalf("update = %T %+v, want updateFolderPeers pts=31", out.Updates[0], out.Updates[0])
	}
}

func TestDialogSettingRPCsSkipManualPushWhenReliableDispatch(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 11
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 11, Date: 1700000400, Seq: 0}, reliableDispatch: true}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates, Sessions: sessions}, zaptest.NewLogger(t), clock.System)

	toggle := &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: peer.ID}}}
	toggle.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(ctx, toggle); err != nil || !ok {
		t.Fatalf("toggle pin = %v, %v", ok, err)
	}
	if len(updates.events) != 1 || updates.events[0].Type != domain.UpdateEventDialogPinned {
		t.Fatalf("events = %+v, want durable event recorded", updates.events)
	}
	// reliable outbox 仍是 update 本体的唯一在线投递者；当前 session 只
	// 收到一条纯 pts 簿记信封（outbox 排除了它，事件占用的账号 pts 必须
	// 显式同步，否则当前设备水位落后）。
	got := sessions.snapshot()
	if got.message == nil {
		t.Fatalf("manual push = nil, want pts bookkeeping envelope for the current session")
	}
	envelope, ok := got.message.(*tg.Updates)
	if !ok || len(envelope.Updates) != 1 {
		t.Fatalf("manual push = %T %+v, want a single bookkeeping update", got.message, got.message)
	}
	if bookkeeping, ok := envelope.Updates[0].(*tg.UpdateDeleteMessages); !ok || len(bookkeeping.Messages) != 0 {
		t.Fatalf("manual push update = %#v, want empty updateDeleteMessages bookkeeping", envelope.Updates[0])
	}
}

func TestMessagesSaveDraftPushesDraftUpdateToOtherSessions(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{
		Sessions: sessions,
		Users: mapUsersService{users: map[int64]domain.User{
			userID: {ID: userID, FirstName: "Alice"},
			peerID: {ID: peerID, FirstName: "Bob"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	var authKeyID [8]byte
	authKeyID[0] = 8

	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), userID), authKeyID), 66)
	ok, err := r.onMessagesSaveDraft(ctx, &tg.MessagesSaveDraftRequest{
		Peer:    &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		Message: "1111",
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 4},
		},
	})
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if !ok {
		t.Fatalf("save draft = false, want true")
	}

	got := sessions.snapshot()
	if got.userID != userID || got.sessionID != 66 || got.messageType != proto.MessageFromServer {
		t.Fatalf("push = user %d exclude session %d type %v, want self/exclude/from_server", got.userID, got.sessionID, got.messageType)
	}
	if gotAuthKeyID := sessions.scopedAuthKey(); gotAuthKeyID != authKeyID {
		t.Fatalf("exclude auth_key_id = %x, want %x", gotAuthKeyID, authKeyID)
	}
	updates, ok := got.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", got.message)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("updates = %+v, want one update", updates.Updates)
	}
	update, ok := updates.Updates[0].(*tg.UpdateDraftMessage)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateDraftMessage", updates.Updates[0])
	}
	peer, ok := update.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != peerID {
		t.Fatalf("draft peer = %#v, want peer user %d", update.Peer, peerID)
	}
	draft, ok := update.Draft.(*tg.DraftMessage)
	if !ok || draft.Message != "1111" || len(draft.Entities) != 1 {
		t.Fatalf("draft = %#v, want message and entities", update.Draft)
	}
	if len(updates.Users) != 2 {
		t.Fatalf("users = %+v, want self and peer", updates.Users)
	}
}

func TestMessagesSaveDraftPersistsAndGetAllDraftsReturnsForumDraft(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Forum",
		Forum:         true,
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	dialogStore := memory.NewDialogStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Dialogs:  appdialogs.NewService(dialogStore, channelStore),
	}, zaptest.NewLogger(t), clock.System)

	reply := &tg.InputReplyToMessage{}
	reply.SetTopMsgID(123)
	ok, err := r.onMessagesSaveDraft(WithUserID(ctx, owner.ID), &tg.MessagesSaveDraftRequest{
		Peer:    &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		ReplyTo: reply,
		Message: "topic draft",
		Media: &tg.InputMediaWebPage{
			URL:      "https://example.test/draft",
			Optional: true,
		},
	})
	if err != nil || !ok {
		t.Fatalf("save forum draft = %v, %v", ok, err)
	}
	drafts, err := dialogStore.ListDrafts(ctx, owner.ID, 10)
	if err != nil {
		t.Fatalf("list stored drafts: %v", err)
	}
	if len(drafts) != 1 || drafts[0].TopMessageID != 123 || drafts[0].Message != "topic draft" || drafts[0].WebPage == nil {
		t.Fatalf("stored drafts = %+v, want forum draft with webpage", drafts)
	}

	got, err := r.onMessagesGetAllDrafts(WithUserID(ctx, owner.ID))
	if err != nil {
		t.Fatalf("get all drafts: %v", err)
	}
	updates, ok := got.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("get all drafts = %T %+v, want one updates", got, got)
	}
	update, ok := updates.Updates[0].(*tg.UpdateDraftMessage)
	if !ok {
		t.Fatalf("draft update = %T", updates.Updates[0])
	}
	if top, ok := update.GetTopMsgID(); !ok || top != 123 {
		t.Fatalf("top_msg_id = %d/%v, want 123/true", top, ok)
	}
	draft, ok := update.Draft.(*tg.DraftMessage)
	if !ok || draft.Message != "topic draft" {
		t.Fatalf("draft = %#v, want message draft", update.Draft)
	}
	if media, ok := draft.Media.(*tg.InputMediaWebPage); !ok || media.URL != "https://example.test/draft" {
		t.Fatalf("draft media = %#v, want webpage", draft.Media)
	}
	if len(updates.Chats) != 1 {
		t.Fatalf("chats = %+v, want channel context", updates.Chats)
	}
}

func TestMessagesGetAllDraftsUsesSingleBatchUserLookup(t *testing.T) {
	const (
		userID    = int64(1000000001)
		firstID   = int64(1000000002)
		secondID  = int64(1000000003)
		channelID = int64(2000000001)
	)
	dialogs := &captureDialogs{drafts: []domain.DialogDraft{
		{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: firstID}, Date: 1700000100, Message: "first"},
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, TopMessageID: 77, Date: 1700000101, Message: "topic"},
		{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: firstID}, Date: 1700000102, Message: "duplicate"},
		{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: secondID}, Date: 1700000103, Message: "second"},
	}}
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{
		userID:   {ID: userID, FirstName: "Alice"},
		firstID:  {ID: firstID, FirstName: "Bob"},
		secondID: {ID: secondID, FirstName: "Carol"},
	}}}
	r := New(Config{}, Deps{
		Dialogs: dialogs,
		Users:   users,
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onMessagesGetAllDrafts(WithUserID(context.Background(), userID))
	if err != nil {
		t.Fatalf("get all drafts: %v", err)
	}
	updates := got.(*tg.Updates)
	if len(updates.Updates) != len(dialogs.drafts) {
		t.Fatalf("updates = %+v, want one update per draft", updates.Updates)
	}
	if users.selfCalls != 1 || users.byIDsCalls != 1 || users.byIDCalls != 0 {
		t.Fatalf("user lookups self=%d byIDs=%d byID=%d, want one Self, one ByIDs and no ByID", users.selfCalls, users.byIDsCalls, users.byIDCalls)
	}
	if len(users.lastByIDs) != 2 || users.lastByIDs[0] != firstID || users.lastByIDs[1] != secondID {
		t.Fatalf("ByIDs ids = %+v, want [%d %d]", users.lastByIDs, firstID, secondID)
	}
	if ids := gotUserIDs(updates.Users); len(ids) != 3 || ids[0] != userID || ids[1] != firstID || ids[2] != secondID {
		t.Fatalf("users = %+v, want self and two peer users", updates.Users)
	}
	channelDraft := updates.Updates[1].(*tg.UpdateDraftMessage)
	if top, ok := channelDraft.GetTopMsgID(); !ok || top != 77 {
		t.Fatalf("channel draft top_msg_id = %d/%v, want 77/true", top, ok)
	}
}

func TestMessagesGetAllDraftsUsesSingleBatchChannelLookup(t *testing.T) {
	ctx := context.Background()
	const userID = int64(1000000001)
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	first, err := channelService.CreateChannel(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         "Draft Channel One",
		Broadcast:     true,
		Date:          1700000200,
	})
	if err != nil {
		t.Fatalf("create first channel: %v", err)
	}
	second, err := channelService.CreateChannel(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         "Draft Channel Two",
		Megagroup:     true,
		Date:          1700000210,
	})
	if err != nil {
		t.Fatalf("create second channel: %v", err)
	}
	dialogs := &captureDialogs{drafts: []domain.DialogDraft{
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: first.Channel.ID}, Date: 1700000220, Message: "first"},
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: second.Channel.ID}, TopMessageID: 77, Date: 1700000221, Message: "topic"},
		{Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: first.Channel.ID}, Date: 1700000222, Message: "duplicate"},
	}}
	counting := &countingChannelsService{Service: channelService}
	r := New(Config{}, Deps{
		Dialogs:  dialogs,
		Channels: counting,
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onMessagesGetAllDrafts(WithUserID(ctx, userID))
	if err != nil {
		t.Fatalf("get all drafts: %v", err)
	}
	updates := got.(*tg.Updates)
	if len(updates.Updates) != len(dialogs.drafts) {
		t.Fatalf("updates = %+v, want one update per draft", updates.Updates)
	}
	if counting.getChannelsCalls != 1 || counting.getChannelCalls != 0 {
		t.Fatalf("channel lookups GetChannels=%d GetChannel=%d, want one batch and no single lookups", counting.getChannelsCalls, counting.getChannelCalls)
	}
	if len(updates.Chats) != 2 {
		t.Fatalf("chats = %+v, want two unique channel refs", updates.Chats)
	}
	firstChat, ok := updates.Chats[0].(*tg.Channel)
	if !ok || firstChat.ID != first.Channel.ID {
		t.Fatalf("first chat = %#v, want first channel", updates.Chats[0])
	}
	secondChat, ok := updates.Chats[1].(*tg.Channel)
	if !ok || secondChat.ID != second.Channel.ID {
		t.Fatalf("second chat = %#v, want second channel", updates.Chats[1])
	}
	channelDraft := updates.Updates[1].(*tg.UpdateDraftMessage)
	if top, ok := channelDraft.GetTopMsgID(); !ok || top != 77 {
		t.Fatalf("channel draft top_msg_id = %d/%v, want 77/true", top, ok)
	}
}

func TestMessagesGetDialogsIncludesCloudDraft(t *testing.T) {
	ctx := context.Background()
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	dialogStore := memory.NewDialogStore()
	if err := dialogStore.Upsert(ctx, userID, domain.Dialog{
		Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		TopMessage:     10,
		TopMessageDate: 1700000000,
	}); err != nil {
		t.Fatalf("upsert dialog: %v", err)
	}
	if err := dialogStore.SaveDraft(ctx, userID, domain.DialogDraft{
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Date:    1700000100,
		Message: "saved draft",
	}); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	r := New(Config{}, Deps{
		Users: mapUsersService{users: map[int64]domain.User{
			userID: {ID: userID, FirstName: "Alice"},
			peerID: {ID: peerID, FirstName: "Bob"},
		}},
		Dialogs: appdialogs.NewService(dialogStore),
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      10,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode get dialogs: %v", err)
	}
	got, err := r.Dispatch(WithUserID(ctx, userID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("get dialogs: %v", err)
	}
	box, ok := got.(*tg.MessagesDialogsBox)
	if !ok {
		t.Fatalf("response = %T, want boxed messages.dialogs", got)
	}
	dialogs, ok := box.Dialogs.(*tg.MessagesDialogs)
	if !ok || len(dialogs.Dialogs) != 1 {
		t.Fatalf("dialogs = %T %+v, want one messages.dialogs", box.Dialogs, box.Dialogs)
	}
	dialog, ok := dialogs.Dialogs[0].(*tg.Dialog)
	if !ok {
		t.Fatalf("dialog = %T", dialogs.Dialogs[0])
	}
	draft, ok := dialog.Draft.(*tg.DraftMessage)
	if !ok || draft.Message != "saved draft" {
		t.Fatalf("dialog draft = %#v, want saved draft", dialog.Draft)
	}
}

func TestMessagesClearAllDraftsClearsAndPushesEmptyUpdates(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	dialogs := &captureDialogs{drafts: []domain.DialogDraft{{
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Date:    1700000100,
		Message: "draft",
	}}}
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{
		Dialogs:  dialogs,
		Sessions: sessions,
		Users: mapUsersService{users: map[int64]domain.User{
			userID: {ID: userID, FirstName: "Alice"},
			peerID: {ID: peerID, FirstName: "Bob"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	ok, err := r.onMessagesClearAllDrafts(WithSessionID(WithUserID(context.Background(), userID), 9))
	if err != nil || !ok {
		t.Fatalf("clear all drafts = %v, %v", ok, err)
	}
	got := sessions.snapshot()
	updates, ok := got.message.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed = %T %+v, want one update", got.message, got.message)
	}
	update, ok := updates.Updates[0].(*tg.UpdateDraftMessage)
	if !ok {
		t.Fatalf("update = %T", updates.Updates[0])
	}
	if _, ok := update.Draft.(*tg.DraftMessageEmpty); !ok {
		t.Fatalf("draft = %#v, want draftMessageEmpty", update.Draft)
	}
	if len(dialogs.drafts) != 0 {
		t.Fatalf("capture drafts = %+v, want cleared", dialogs.drafts)
	}
}

func TestAccountUpdateStatusPushesPresenceToOnlinePrivateDialogPeers(t *testing.T) {
	ctx := context.Background()
	alice := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob"}
	dialogs := memory.NewDialogStore()
	// 私聊 dialog 双向建行：alice↔bob 两侧都存。presence 接收者由「主体自己的
	// dialog 对端 ∩ 在线」算出，故 bob 改状态时需 bob 侧 dialog 含 alice。
	if err := dialogs.SaveList(ctx, alice.ID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
			TopMessage:     10,
			TopMessageDate: 1700000000,
		}},
		Users: []domain.User{bob},
	}); err != nil {
		t.Fatalf("save dialogs: %v", err)
	}
	if err := dialogs.SaveList(ctx, bob.ID, domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID},
			TopMessage:     10,
			TopMessageDate: 1700000000,
		}},
		Users: []domain.User{alice},
	}); err != nil {
		t.Fatalf("save bob dialogs: %v", err)
	}
	sessions := &captureSessions{onlineUserIDs: []int64{alice.ID}}
	r := New(Config{}, Deps{
		Dialogs:  appdialogs.NewService(dialogs),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	ok, err := r.onAccountUpdateStatus(WithSessionID(WithUserID(ctx, bob.ID), 22), false)
	if err != nil || !ok {
		t.Fatalf("bob account.updateStatus online = %v, %v", ok, err)
	}
	gotPushes := sessions.pushedUserIDs()
	if !reflect.DeepEqual(gotPushes, []int64{bob.ID, alice.ID}) {
		t.Fatalf("pushed users = %+v, want self and online private dialog peer", gotPushes)
	}
	update := pushedUserStatus(t, sessions.snapshot().message)
	if update.UserID != bob.ID {
		t.Fatalf("status user = %d, want bob", update.UserID)
	}
	if status, ok := update.Status.(*tg.UserStatusOnline); !ok || status.Expires <= int(time.Now().Unix()) {
		t.Fatalf("status = %#v, want online with future expires", update.Status)
	}
}

func TestDialogPinFolderScoping(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 9
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	dialogPeer := &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: peer.ID}}
	dialogs := &captureDialogs{folderID: domain.DialogArchiveFolderID}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 11, Date: 1700000400, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	// 归档内会话置顶：durable 事件必须携带 folder_id=1，否则离线设备
	// 重放时会把置顶应用到主列表。
	toggle := &tg.MessagesToggleDialogPinRequest{Peer: dialogPeer}
	toggle.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(ctx, toggle); err != nil || !ok {
		t.Fatalf("toggle pin = %v, %v", ok, err)
	}
	if len(updates.events) != 1 || updates.events[0].FolderID != domain.DialogArchiveFolderID {
		t.Fatalf("pin event = %+v, want folder_id=1", updates.events)
	}

	// 归档列表内重排：req.folder_id 必须传到 service 与 durable 事件。
	reorder := &tg.MessagesReorderPinnedDialogsRequest{Order: []tg.InputDialogPeerClass{dialogPeer}}
	reorder.FolderID = domain.DialogArchiveFolderID
	if ok, err := r.onMessagesReorderPinnedDialogs(ctx, reorder); err != nil || !ok {
		t.Fatalf("reorder pinned = %v, %v", ok, err)
	}
	if dialogs.folderID != domain.DialogArchiveFolderID {
		t.Fatalf("reorder folder = %d, want archive", dialogs.folderID)
	}
	if len(updates.events) != 2 || updates.events[1].FolderID != domain.DialogArchiveFolderID {
		t.Fatalf("reorder event = %+v, want folder_id=1", updates.events)
	}

	// 差分重放转换：归档内置顶 update 带 folder_id flag。
	pinnedUpdate := tgOtherUpdateFromEvent(updates.events[0])
	dialogPinned, ok := pinnedUpdate.(*tg.UpdateDialogPinned)
	if !ok {
		t.Fatalf("replayed update = %T, want *tg.UpdateDialogPinned", pinnedUpdate)
	}
	if folderID, has := dialogPinned.GetFolderID(); !has || folderID != domain.DialogArchiveFolderID {
		dialogPinned.SetFlags()
		if folderID, has = dialogPinned.GetFolderID(); !has || folderID != domain.DialogArchiveFolderID {
			t.Fatalf("replayed updateDialogPinned folder = %d (%v), want 1", folderID, has)
		}
	}
}

func TestToggleDialogPinArchiveFolderRow(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 9
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)
	dialogs := &captureDialogs{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 11, Date: 1700000400, Seq: 0}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)

	// archive folder 行自身置顶（TDesktop 归档行右键 Pin）。
	toggle := &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeerFolder{FolderID: domain.DialogArchiveFolderID}}
	toggle.SetPinned(true)
	if ok, err := r.onMessagesToggleDialogPin(ctx, toggle); err != nil || !ok {
		t.Fatalf("toggle archive folder pin = %v, %v", ok, err)
	}
	if !dialogs.archivePinned {
		t.Fatalf("archive pinned not persisted")
	}
	if len(updates.events) != 1 || updates.events[0].Peer.Type != domain.PeerTypeFolder || updates.events[0].Peer.ID != domain.DialogArchiveFolderID {
		t.Fatalf("archive pin event = %+v, want folder peer", updates.events)
	}
	// 重放转换：dialogPeerFolder 且不带 folder_id flag（TDesktop 会把带
	// flag 的 folder peer 置顶视为 "Nested folders" 协议错误）。
	update := tgOtherUpdateFromEvent(updates.events[0])
	dialogPinned, ok := update.(*tg.UpdateDialogPinned)
	if !ok {
		t.Fatalf("replayed update = %T, want *tg.UpdateDialogPinned", update)
	}
	folderPeer, ok := dialogPinned.Peer.(*tg.DialogPeerFolder)
	if !ok || folderPeer.FolderID != domain.DialogArchiveFolderID {
		t.Fatalf("replayed peer = %+v, want dialogPeerFolder(1)", dialogPinned.Peer)
	}
	dialogPinned.SetFlags()
	if folderID, has := dialogPinned.GetFolderID(); has {
		t.Fatalf("folder peer pin carries folder_id=%d flag, want absent", folderID)
	}

	// 非归档 folder id 一律 FOLDER_ID_INVALID。
	badToggle := &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeerFolder{FolderID: 0}}
	badToggle.SetPinned(true)
	if _, err := r.onMessagesToggleDialogPin(ctx, badToggle); err == nil || !strings.Contains(err.Error(), "FOLDER_ID_INVALID") {
		t.Fatalf("toggle folder 0 err = %v, want FOLDER_ID_INVALID", err)
	}
}

func TestMessagesDialogsIncludesArchiveFolderEntry(t *testing.T) {
	list := domain.DialogList{
		Dialogs: []domain.Dialog{{
			Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
			TopMessage:     9,
			TopMessageDate: 40,
		}},
		ArchiveSummary: &domain.DialogArchiveSummary{
			TopPeer:             domain.Peer{Type: domain.PeerTypeUser, ID: 1000000003},
			TopMessage:          7,
			UnreadPeersCount:    2,
			UnreadMessagesCount: 5,
			Pinned:              true,
		},
	}
	out, ok := tgMessagesDialogs(1000000001, list).(*tg.MessagesDialogs)
	if !ok {
		t.Fatalf("response type = %T, want *tg.MessagesDialogs", tgMessagesDialogs(1000000001, list))
	}
	if len(out.Dialogs) != 2 {
		t.Fatalf("dialogs = %d, want folder entry + dialog", len(out.Dialogs))
	}
	folder, ok := out.Dialogs[0].(*tg.DialogFolder)
	if !ok {
		t.Fatalf("first dialog = %T, want *tg.DialogFolder", out.Dialogs[0])
	}
	if folder.Folder.ID != domain.DialogArchiveFolderID || !folder.Pinned {
		t.Fatalf("folder entry = %+v, want archive id pinned", folder)
	}
	if folder.TopMessage != 7 || folder.UnreadUnmutedPeersCount != 2 || folder.UnreadUnmutedMessagesCount != 5 {
		t.Fatalf("folder counters = %+v, want top 7 / peers 2 / messages 5", folder)
	}
	peer, ok := folder.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != 1000000003 {
		t.Fatalf("folder peer = %+v, want archived top peer", folder.Peer)
	}
	// 无归档时不输出条目。
	plain := domain.DialogList{Dialogs: list.Dialogs}
	plainOut, _ := tgMessagesDialogs(1000000001, plain).(*tg.MessagesDialogs)
	if len(plainOut.Dialogs) != 1 {
		t.Fatalf("plain dialogs = %d, want no folder entry", len(plainOut.Dialogs))
	}
}

func TestPeerDialogsIncludesArchiveFolderEntry(t *testing.T) {
	list := domain.DialogList{
		ArchiveSummary: &domain.DialogArchiveSummary{
			TopPeer:             domain.Peer{Type: domain.PeerTypeChannel, ID: 18},
			TopMessage:          2,
			UnreadPeersCount:    1,
			UnreadMessagesCount: 4,
			Pinned:              true,
		},
	}
	out := tgPeerDialogs(1000000001, list, domain.UpdateState{Pts: 5})
	if len(out.Dialogs) != 1 {
		t.Fatalf("pinned dialogs = %d entries, want folder entry", len(out.Dialogs))
	}
	folder, ok := out.Dialogs[0].(*tg.DialogFolder)
	if !ok {
		t.Fatalf("first dialog = %T, want *tg.DialogFolder", out.Dialogs[0])
	}
	// DrKLO fetchFolderInLoadedPinnedDialogs 要求 top_message 与 peer 非零，
	// 否则条目被丢弃。
	if folder.TopMessage != 2 {
		t.Fatalf("folder top message = %d, want 2", folder.TopMessage)
	}
	peer, ok := folder.Peer.(*tg.PeerChannel)
	if !ok || peer.ChannelID != 18 {
		t.Fatalf("folder peer = %+v, want channel 18", folder.Peer)
	}
}

func TestMessagesGetDialogsTDesktopInitialPageMergesPinnedHeader(t *testing.T) {
	viewer := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob"}
	charlie := domain.User{ID: 1000000003, AccessHash: 33, FirstName: "Charlie"}
	dialogs := &captureDialogs{
		list: domain.DialogList{
			Dialogs: []domain.Dialog{{
				Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: charlie.ID},
				TopMessage:     31,
				TopMessageDate: 1781597300,
			}},
			Messages: []domain.Message{{
				ID:          31,
				OwnerUserID: viewer.ID,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: charlie.ID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: charlie.ID},
				Date:        1781597300,
				Body:        "normal",
			}},
			Users: []domain.User{viewer, charlie},
			Count: 1,
		},
		pinnedList: domain.DialogList{
			ArchiveSummary: &domain.DialogArchiveSummary{
				TopPeer:             domain.Peer{Type: domain.PeerTypeChannel, ID: 13},
				TopMessage:          7,
				UnreadPeersCount:    1,
				UnreadMessagesCount: 2,
				Pinned:              true,
			},
			Dialogs: []domain.Dialog{
				{
					Peer:           domain.Peer{Type: domain.PeerTypeChannel, ID: 9},
					TopMessage:     16,
					TopMessageDate: 1781541965,
					Pinned:         true,
					PinnedOrder:    2,
					Pts:            16,
				},
				{
					Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
					TopMessage:     77,
					TopMessageDate: 1781597372,
					Pinned:         true,
					PinnedOrder:    1,
				},
			},
			ChannelMessages: []domain.ChannelMessage{
				{ChannelID: 13, ID: 7, SenderUserID: bob.ID, From: domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID}, Date: 1781540000, Body: "archived top", Pts: 7},
				{ChannelID: 9, ID: 16, SenderUserID: bob.ID, From: domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID}, Date: 1781541965, Body: "pinned channel", Pts: 16},
			},
			Messages: []domain.Message{{
				ID:          77,
				OwnerUserID: viewer.ID,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: viewer.ID},
				Date:        1781597372,
				Out:         true,
				Body:        "pinned private",
			}},
			Users: []domain.User{viewer, bob},
			Channels: []domain.Channel{
				{ID: 13, AccessHash: 1313, CreatorUserID: viewer.ID, Title: "Archive Top", Megagroup: true, TopMessageID: 7, Pts: 7},
				{ID: 9, AccessHash: 9090, CreatorUserID: viewer.ID, Title: "Pinned Channel", Megagroup: true, TopMessageID: 16, Pts: 16},
			},
			Count: 2,
		},
		hasPinnedList: true,
	}
	r := New(Config{}, Deps{Dialogs: dialogs}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		ExcludePinned: true,
		OffsetPeer:    &tg.InputPeerEmpty{},
		Limit:         20,
		Hash:          0,
	}
	req.SetFolderID(domain.DialogMainFolderID)
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode get dialogs: %v", err)
	}
	ctx := WithClientInfo(WithUserID(context.Background(), viewer.ID), ClientInfo{Type: ClientTypeTDesktop})
	enc, err := r.Dispatch(ctx, [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch get dialogs: %v", err)
	}
	box, ok := enc.(*tg.MessagesDialogsBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesDialogsBox", enc)
	}
	out, ok := box.Dialogs.(*tg.MessagesDialogs)
	if !ok {
		t.Fatalf("dialogs response = %T, want *tg.MessagesDialogs", box.Dialogs)
	}
	if dialogs.getDialogsCalls != 2 || len(dialogs.filters) != 2 {
		t.Fatalf("GetDialogs calls = %d filters %+v, want normal + pinned", dialogs.getDialogsCalls, dialogs.filters)
	}
	if !dialogs.filters[0].ExcludePinned || !dialogs.filters[1].PinnedOnly {
		t.Fatalf("filters = %+v, want exclude-pinned then pinned-only", dialogs.filters)
	}
	if len(out.Dialogs) != 4 {
		t.Fatalf("dialogs = %d, want archive + two pinned + normal", len(out.Dialogs))
	}
	if _, ok := out.Dialogs[0].(*tg.DialogFolder); !ok {
		t.Fatalf("first dialog = %T, want archive folder", out.Dialogs[0])
	}
	pinnedChannel := out.Dialogs[1].(*tg.Dialog)
	if peer, ok := pinnedChannel.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != 9 || !pinnedChannel.Pinned {
		t.Fatalf("second dialog = %+v peer %+v, want pinned channel 9", pinnedChannel, pinnedChannel.Peer)
	}
	pinnedUser := out.Dialogs[2].(*tg.Dialog)
	if peer, ok := pinnedUser.Peer.(*tg.PeerUser); !ok || peer.UserID != bob.ID || !pinnedUser.Pinned {
		t.Fatalf("third dialog = %+v peer %+v, want pinned user %d", pinnedUser, pinnedUser.Peer, bob.ID)
	}
	normal := out.Dialogs[3].(*tg.Dialog)
	if peer, ok := normal.Peer.(*tg.PeerUser); !ok || peer.UserID != charlie.ID || normal.Pinned {
		t.Fatalf("fourth dialog = %+v peer %+v, want normal user %d", normal, normal.Peer, charlie.ID)
	}
	if !messagesDialogsHasChannel(out, 13) || !messagesDialogsHasChannel(out, 9) {
		t.Fatalf("chats = %+v, want archive and pinned channels", out.Chats)
	}
	if !messagesDialogsHasUser(out, bob.ID) || !messagesDialogsHasUser(out, charlie.ID) {
		t.Fatalf("users = %+v, want pinned and normal users", out.Users)
	}
	if !messagesDialogsHasChannelMessage(out, 13, 7) || !messagesDialogsHasChannelMessage(out, 9, 16) {
		t.Fatalf("messages = %+v, want archive and pinned channel top messages", out.Messages)
	}
	if !messagesDialogsHasPrivateMessage(out, bob.ID, 77) || !messagesDialogsHasPrivateMessage(out, charlie.ID, 31) {
		t.Fatalf("messages = %+v, want pinned and normal private top messages", out.Messages)
	}
}

func TestMessagesGetDialogsNonTDesktopKeepsExcludePinnedResponse(t *testing.T) {
	viewer := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob"}
	dialogs := &captureDialogs{
		list: domain.DialogList{
			Dialogs: []domain.Dialog{{
				Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
				TopMessage:     31,
				TopMessageDate: 1781597300,
			}},
			Messages: []domain.Message{{
				ID:          31,
				OwnerUserID: viewer.ID,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
				Date:        1781597300,
				Body:        "normal",
			}},
			Users: []domain.User{viewer, bob},
			Count: 1,
		},
		pinnedList: domain.DialogList{
			Dialogs: []domain.Dialog{{
				Peer:           domain.Peer{Type: domain.PeerTypeChannel, ID: 9},
				TopMessage:     16,
				TopMessageDate: 1781541965,
				Pinned:         true,
			}},
			Channels: []domain.Channel{{ID: 9, AccessHash: 9090, Title: "Pinned Channel", Megagroup: true}},
			Count:    1,
		},
		hasPinnedList: true,
	}
	r := New(Config{}, Deps{Dialogs: dialogs}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetDialogsRequest{
		ExcludePinned: true,
		OffsetPeer:    &tg.InputPeerEmpty{},
		Limit:         20,
	}
	req.SetFolderID(domain.DialogMainFolderID)
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode get dialogs: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(context.Background(), viewer.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch get dialogs: %v", err)
	}
	box := enc.(*tg.MessagesDialogsBox)
	out := box.Dialogs.(*tg.MessagesDialogs)
	if dialogs.getDialogsCalls != 1 {
		t.Fatalf("GetDialogs calls = %d, want no pinned compatibility load", dialogs.getDialogsCalls)
	}
	if len(out.Dialogs) != 1 {
		t.Fatalf("dialogs = %d, want only exclude-pinned response", len(out.Dialogs))
	}
	normal := out.Dialogs[0].(*tg.Dialog)
	if peer, ok := normal.Peer.(*tg.PeerUser); !ok || peer.UserID != bob.ID || normal.Pinned {
		t.Fatalf("dialog = %+v peer %+v, want normal user %d", normal, normal.Peer, bob.ID)
	}
}

func TestPinnedDialogsListSingleflightsConcurrentStartupLoads(t *testing.T) {
	const userID int64 = 1000000001
	dialogs := &blockingPinnedDialogs{
		captureDialogs: captureDialogs{
			pinnedList: domain.DialogList{Dialogs: []domain.Dialog{{
				Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
				TopMessage:     11,
				TopMessageDate: 1781597372,
				Pinned:         true,
			}}},
			hasPinnedList: true,
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	r := New(Config{}, Deps{Dialogs: dialogs}, zaptest.NewLogger(t), clock.System)
	errs := make(chan error, 2)
	results := make(chan domain.DialogList, 2)
	call := func() {
		list, err := r.pinnedDialogsList(context.Background(), userID, domain.DialogMainFolderID)
		errs <- err
		results <- list
	}
	go call()
	<-dialogs.entered
	go call()
	time.Sleep(10 * time.Millisecond)
	close(dialogs.release)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("pinnedDialogsList call %d: %v", i, err)
		}
		if got := <-results; len(got.Dialogs) != 1 || got.Dialogs[0].TopMessage != 11 {
			t.Fatalf("result %d = %+v, want shared pinned list", i, got)
		}
	}
	if calls := dialogs.pinnedCalls(); calls != 1 {
		t.Fatalf("pinned GetDialogs calls = %d, want singleflight to share one in-flight load", calls)
	}
}

type blockingPinnedDialogs struct {
	captureDialogs
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (s *blockingPinnedDialogs) GetDialogs(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error) {
	if !filter.PinnedOnly {
		return s.captureDialogs.GetDialogs(ctx, userID, filter)
	}
	s.mu.Lock()
	s.calls++
	calls := s.calls
	s.mu.Unlock()
	if calls == 1 {
		close(s.entered)
		<-s.release
	}
	return s.captureDialogs.GetDialogs(ctx, userID, filter)
}

func (s *blockingPinnedDialogs) pinnedCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestMessagesGetPinnedDialogsIncludesArchiveAndReferencedPeers(t *testing.T) {
	viewer := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Bob"}
	dialogs := &captureDialogs{list: domain.DialogList{
		ArchiveSummary: &domain.DialogArchiveSummary{
			TopPeer:             domain.Peer{Type: domain.PeerTypeChannel, ID: 13},
			TopMessage:          7,
			UnreadPeersCount:    2,
			UnreadMessagesCount: 5,
			Pinned:              true,
		},
		Dialogs: []domain.Dialog{
			{
				Peer:           domain.Peer{Type: domain.PeerTypeChannel, ID: 9},
				TopMessage:     16,
				TopMessageDate: 1781541965,
				Pinned:         true,
				PinnedOrder:    2,
				Pts:            16,
			},
			{
				Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
				TopMessage:     77,
				TopMessageDate: 1781597372,
				Pinned:         true,
				PinnedOrder:    1,
			},
		},
		ChannelMessages: []domain.ChannelMessage{
			{
				ChannelID:    13,
				ID:           7,
				SenderUserID: bob.ID,
				From:         domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
				Date:         1781540000,
				Body:         "archived top",
				Pts:          7,
			},
			{
				ChannelID:    9,
				ID:           16,
				SenderUserID: bob.ID,
				From:         domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
				Date:         1781541965,
				Body:         "pinned channel",
				Pts:          16,
			},
		},
		Messages: []domain.Message{
			{
				ID:          77,
				OwnerUserID: viewer.ID,
				Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
				From:        domain.Peer{Type: domain.PeerTypeUser, ID: viewer.ID},
				Date:        1781597372,
				Out:         true,
				Body:        "pinned private",
				Pts:         77,
			},
		},
		Users: []domain.User{viewer, bob},
		Channels: []domain.Channel{
			{ID: 13, AccessHash: 1313, CreatorUserID: viewer.ID, Title: "Archive Top", Megagroup: true, TopMessageID: 7, Pts: 7},
			{ID: 9, AccessHash: 9090, CreatorUserID: viewer.ID, Title: "Pinned Channel", Megagroup: true, TopMessageID: 16, Pts: 16},
		},
	}}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 55, Qts: 1, Date: 1781597400}}
	r := New(Config{}, Deps{Dialogs: dialogs, Updates: updates}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetPinnedDialogsRequest{FolderID: 0}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode get pinned dialogs: %v", err)
	}
	authKeyID := [8]byte{9}
	enc, err := r.Dispatch(WithUserID(context.Background(), viewer.ID), authKeyID, 0, &in)
	if err != nil {
		t.Fatalf("dispatch get pinned dialogs: %v", err)
	}
	out, ok := enc.(*tg.MessagesPeerDialogs)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesPeerDialogs", enc)
	}
	if dialogs.filter != (domain.DialogFilter{PinnedOnly: true, HasFolderID: true, FolderID: 0, Limit: 100}) {
		t.Fatalf("filter = %+v, want pinned folder 0 limit 100", dialogs.filter)
	}
	if updates.authKeyID != authKeyID || updates.userID != viewer.ID {
		t.Fatalf("state lookup auth/user = %x/%d, want %x/%d", updates.authKeyID, updates.userID, authKeyID, viewer.ID)
	}
	if len(out.Dialogs) != 3 {
		t.Fatalf("dialogs = %d, want archive + two pinned peers", len(out.Dialogs))
	}
	folder, ok := out.Dialogs[0].(*tg.DialogFolder)
	if !ok {
		t.Fatalf("first dialog = %T, want *tg.DialogFolder", out.Dialogs[0])
	}
	archivePeer, ok := folder.Peer.(*tg.PeerChannel)
	if !ok || archivePeer.ChannelID != 13 || folder.TopMessage != 7 || !folder.Pinned {
		t.Fatalf("archive folder = %+v peer %+v, want pinned channel 13 top 7", folder, folder.Peer)
	}
	pinnedChannel, ok := out.Dialogs[1].(*tg.Dialog)
	if !ok || !pinnedChannel.Pinned {
		t.Fatalf("second dialog = %T %+v, want pinned channel dialog", out.Dialogs[1], out.Dialogs[1])
	}
	channelPeer, ok := pinnedChannel.Peer.(*tg.PeerChannel)
	if !ok || channelPeer.ChannelID != 9 || pinnedChannel.TopMessage != 16 {
		t.Fatalf("second peer = %+v top %d, want channel 9 top 16", pinnedChannel.Peer, pinnedChannel.TopMessage)
	}
	pinnedUser, ok := out.Dialogs[2].(*tg.Dialog)
	if !ok || !pinnedUser.Pinned {
		t.Fatalf("third dialog = %T %+v, want pinned user dialog", out.Dialogs[2], out.Dialogs[2])
	}
	userPeer, ok := pinnedUser.Peer.(*tg.PeerUser)
	if !ok || userPeer.UserID != bob.ID || pinnedUser.TopMessage != 77 {
		t.Fatalf("third peer = %+v top %d, want user %d top 77", pinnedUser.Peer, pinnedUser.TopMessage, bob.ID)
	}
	if !peerDialogsHasChannel(out, 13) || !peerDialogsHasChannel(out, 9) {
		t.Fatalf("chats = %+v, want archive top channel 13 and pinned channel 9", out.Chats)
	}
	if !peerDialogsHasUser(out, bob.ID) {
		t.Fatalf("users = %+v, want pinned private user %d", out.Users, bob.ID)
	}
	if !peerDialogsHasChannelMessage(out, 13, 7) || !peerDialogsHasChannelMessage(out, 9, 16) {
		t.Fatalf("messages = %+v, want channel messages 13/7 and 9/16", out.Messages)
	}
	if !peerDialogsHasPrivateMessage(out, bob.ID, 77) {
		t.Fatalf("messages = %+v, want private dialog top message %d/%d", out.Messages, bob.ID, 77)
	}
}

func peerDialogsHasChannel(out *tg.MessagesPeerDialogs, id int64) bool {
	for _, item := range out.Chats {
		if ch, ok := item.(*tg.Channel); ok && ch.ID == id {
			return true
		}
	}
	return false
}

func peerDialogsHasUser(out *tg.MessagesPeerDialogs, id int64) bool {
	for _, item := range out.Users {
		if user, ok := item.(*tg.User); ok && user.ID == id {
			return true
		}
	}
	return false
}

func peerDialogsHasChannelMessage(out *tg.MessagesPeerDialogs, channelID int64, messageID int) bool {
	for _, item := range out.Messages {
		if msg, ok := item.(*tg.Message); ok && msg.ID == messageID {
			if peer, ok := msg.PeerID.(*tg.PeerChannel); ok && peer.ChannelID == channelID {
				return true
			}
		}
	}
	return false
}

func peerDialogsHasPrivateMessage(out *tg.MessagesPeerDialogs, userID int64, messageID int) bool {
	for _, item := range out.Messages {
		if msg, ok := item.(*tg.Message); ok && msg.ID == messageID {
			if peer, ok := msg.PeerID.(*tg.PeerUser); ok && peer.UserID == userID {
				return true
			}
		}
	}
	return false
}

func messagesDialogsHasChannel(out *tg.MessagesDialogs, id int64) bool {
	for _, item := range out.Chats {
		if ch, ok := item.(*tg.Channel); ok && ch.ID == id {
			return true
		}
	}
	return false
}

func messagesDialogsHasUser(out *tg.MessagesDialogs, id int64) bool {
	for _, item := range out.Users {
		if user, ok := item.(*tg.User); ok && user.ID == id {
			return true
		}
	}
	return false
}

func messagesDialogsHasChannelMessage(out *tg.MessagesDialogs, channelID int64, messageID int) bool {
	for _, item := range out.Messages {
		if msg, ok := item.(*tg.Message); ok && msg.ID == messageID {
			if peer, ok := msg.PeerID.(*tg.PeerChannel); ok && peer.ChannelID == channelID {
				return true
			}
		}
	}
	return false
}

func messagesDialogsHasPrivateMessage(out *tg.MessagesDialogs, userID int64, messageID int) bool {
	for _, item := range out.Messages {
		if msg, ok := item.(*tg.Message); ok && msg.ID == messageID {
			if peer, ok := msg.PeerID.(*tg.PeerUser); ok && peer.UserID == userID {
				return true
			}
		}
	}
	return false
}
