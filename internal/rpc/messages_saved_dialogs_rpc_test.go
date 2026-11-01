package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appcontacts "telesrv/internal/app/contacts"
	appdialogs "telesrv/internal/app/dialogs"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newSavedDialogsTestRouter(t *testing.T) (*Router, *memory.MessageStore, *memory.UserStore) {
	t.Helper()
	userStore := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	contactStore := memory.NewContactStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Contacts: appcontacts.NewService(contactStore, userStore),
		Messages: appmessages.NewService(messageStore, dialogs),
		Dialogs:  appdialogs.NewService(dialogs),
	}, zaptest.NewLogger(t), clock.System)
	return r, messageStore, userStore
}

// savedDialogsFixture builds two Saved Messages sub-dialogs: a directly-sent note
// (saved=self) + a forward from a bob private chat (saved=bob, whose top updates
// later due to the forward).
func savedDialogsFixture(t *testing.T) (*Router, *memory.MessageStore, domain.User, domain.User, int, int) {
	t.Helper()
	ctx := context.Background()
	r, messageStore, userStore := newSavedDialogsTestRouter(t)
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 81, Phone: "15550009301", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 82, Phone: "15550009302", FirstName: "Bob"})

	if _, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerSelf{},
		Message:  "my note",
		RandomID: 93001,
	}); err != nil {
		t.Fatalf("send self note: %v", err)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, bob.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
		Message:  "from bob",
		RandomID: 93002,
	}); err != nil {
		t.Fatalf("bob send: %v", err)
	}
	bobMsgID := privateTopMessageID(t, messageStore, alice.ID, bob.ID)
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, alice.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ToPeer:   &tg.InputPeerSelf{},
		ID:       []int{bobMsgID},
		RandomID: []int64{93003},
	}); err != nil {
		t.Fatalf("forward to saved: %v", err)
	}
	noteID := savedTopMessageID(t, messageStore, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID})
	fwdID := savedTopMessageID(t, messageStore, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID})
	return r, messageStore, alice, bob, noteID, fwdID
}

func privateTopMessageID(t *testing.T, store *memory.MessageStore, ownerID, peerID int64) int {
	t.Helper()
	list, err := store.ListByUser(context.Background(), ownerID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Limit:   1,
	})
	if err != nil || len(list.Messages) == 0 {
		t.Fatalf("list private top: %+v err %v", list.Messages, err)
	}
	return list.Messages[0].ID
}

func savedTopMessageID(t *testing.T, store *memory.MessageStore, ownerID int64, savedPeer domain.Peer) int {
	t.Helper()
	list, err := store.ListByUser(context.Background(), ownerID, domain.MessageFilter{
		HasPeer:   true,
		Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: ownerID},
		SavedPeer: savedPeer,
		Limit:     1,
	})
	if err != nil || len(list.Messages) == 0 {
		t.Fatalf("list saved top: %+v err %v", list.Messages, err)
	}
	return list.Messages[0].ID
}

func savedDialogPage(t *testing.T, res tg.MessagesSavedDialogsClass) ([]*tg.SavedDialog, []tg.MessageClass, []tg.UserClass, int, bool) {
	t.Helper()
	var rawDialogs []tg.SavedDialogClass
	var messages []tg.MessageClass
	var users []tg.UserClass
	count := 0
	full := false
	switch v := res.(type) {
	case *tg.MessagesSavedDialogs:
		rawDialogs, messages, users = v.Dialogs, v.Messages, v.Users
		count = len(v.Dialogs)
		full = true
	case *tg.MessagesSavedDialogsSlice:
		rawDialogs, messages, users = v.Dialogs, v.Messages, v.Users
		count = v.Count
	default:
		t.Fatalf("saved dialogs = %T, want savedDialogs/slice", res)
	}
	dialogs := make([]*tg.SavedDialog, 0, len(rawDialogs))
	for _, d := range rawDialogs {
		sd, ok := d.(*tg.SavedDialog)
		if !ok {
			t.Fatalf("dialog = %T, want *tg.SavedDialog", d)
		}
		dialogs = append(dialogs, sd)
	}
	return dialogs, messages, users, count, full
}

func savedDialogPeerID(t *testing.T, d *tg.SavedDialog) int64 {
	t.Helper()
	switch p := d.Peer.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChannel:
		return p.ChannelID
	default:
		t.Fatalf("saved dialog peer = %T", d.Peer)
		return 0
	}
}

// TestMessagesGetSavedDialogsGroupsBySource verifies direct/forward grouping,
// ordering, saved_peer_id delivery and users projection.
func TestMessagesGetSavedDialogsGroupsBySource(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, noteID, fwdID := savedDialogsFixture(t)

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs: %v", err)
	}
	dialogs, messages, users, _, full := savedDialogPage(t, res)
	if !full || len(dialogs) != 2 {
		t.Fatalf("dialogs = %d full %v, want 2 full", len(dialogs), full)
	}
	// The forward is later, so the bob sub-dialog comes first.
	if savedDialogPeerID(t, dialogs[0]) != bob.ID || dialogs[0].TopMessage != fwdID {
		t.Fatalf("first dialog = peer %d top %d, want bob %d top %d", savedDialogPeerID(t, dialogs[0]), dialogs[0].TopMessage, bob.ID, fwdID)
	}
	if savedDialogPeerID(t, dialogs[1]) != alice.ID || dialogs[1].TopMessage != noteID {
		t.Fatalf("second dialog = peer %d top %d, want self %d top %d", savedDialogPeerID(t, dialogs[1]), dialogs[1].TopMessage, alice.ID, noteID)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2 tops", len(messages))
	}
	for _, raw := range messages {
		msg, ok := raw.(*tg.Message)
		if !ok {
			t.Fatalf("message = %T", raw)
		}
		saved, ok := msg.GetSavedPeerID()
		if !ok {
			t.Fatalf("message %d lacks saved_peer_id", msg.ID)
		}
		want := alice.ID
		if msg.ID == fwdID {
			want = bob.ID
		}
		if user, ok := saved.(*tg.PeerUser); !ok || user.UserID != want {
			t.Fatalf("message %d saved_peer_id = %+v, want user %d", msg.ID, saved, want)
		}
		if msg.ID == fwdID {
			fwd, ok := msg.GetFwdFrom()
			if !ok {
				t.Fatalf("forwarded top lacks fwd header")
			}
			savedFrom, ok := fwd.GetSavedFromPeer()
			if !ok {
				t.Fatalf("forwarded top lacks saved_from_peer")
			}
			if user, ok := savedFrom.(*tg.PeerUser); !ok || user.UserID != bob.ID {
				t.Fatalf("saved_from_peer = %+v, want bob", savedFrom)
			}
		}
	}
	var hasSelf, hasBob bool
	for _, raw := range users {
		u, ok := raw.(*tg.User)
		if !ok {
			continue
		}
		if u.ID == alice.ID {
			hasSelf = true
			if !u.Self {
				t.Fatalf("self user lacks self flag")
			}
		}
		if u.ID == bob.ID {
			hasBob = true
		}
	}
	if !hasSelf || !hasBob {
		t.Fatalf("users projection self=%v bob=%v, want both", hasSelf, hasBob)
	}
}

// TestMessagesGetSavedDialogsPagination verifies pagination: a truncated limit ->
// slice+count, offset paging -> the final page as savedDialogs; Android's first
// page with offset_id=int32 max is equivalent to the first page.
func TestMessagesGetSavedDialogsPagination(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, noteID, fwdID := savedDialogsFixture(t)

	first, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	dialogs, _, _, count, full := savedDialogPage(t, first)
	if full || count != 2 || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("first page = %d dialogs count %d full %v, want slice 1/2 bob first", len(dialogs), count, full)
	}
	second, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		Limit:    1,
		OffsetID: fwdID,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	dialogs, _, _, _, full = savedDialogPage(t, second)
	if !full || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != alice.ID || dialogs[0].TopMessage != noteID {
		t.Fatalf("second page = %+v full %v, want self note tail", dialogs, full)
	}

	androidFirst, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		Limit:    20,
		OffsetID: 2147483647,
	})
	if err != nil {
		t.Fatalf("android first page: %v", err)
	}
	dialogs, _, _, _, full = savedDialogPage(t, androidFirst)
	if !full || len(dialogs) != 2 {
		t.Fatalf("android first page = %d full %v, want 2 full", len(dialogs), full)
	}
}

// TestMessagesGetSavedDialogsNegativePendingOffset verifies that when a local
// pending negative message id appears in DrKLO Android's self Saved Messages
// grouping, the next-page request is not misjudged as LIMIT_INVALID. The
// server-persisted top_message stays positive; a negative offset is only local
// client cursor noise and is treated as first page/no offset.
func TestMessagesGetSavedDialogsNegativePendingOffset(t *testing.T) {
	ctx := context.Background()
	r, _, alice, _, _, _ := savedDialogsFixture(t)

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		Limit:      20,
		OffsetID:   -1001,
		OffsetDate: -1,
		OffsetPeer: &tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("negative pending offset: %v", err)
	}
	dialogs, _, _, _, full := savedDialogPage(t, res)
	if !full || len(dialogs) != 2 {
		t.Fatalf("negative pending offset page = %d full %v, want first page 2 full", len(dialogs), full)
	}

	_, err = r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: -1})
	if !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("negative limit err = %v, want LIMIT_INVALID", err)
	}
}

// TestMessagesGetSavedDialogsHashNotModified verifies that an Android calcHash
// sequence hit returns savedDialogsNotModified{count}.
func TestMessagesGetSavedDialogsHashNotModified(t *testing.T) {
	ctx := context.Background()
	r, _, alice, _, _, _ := savedDialogsFixture(t)

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs: %v", err)
	}
	dialogs, messages, _, _, _ := savedDialogPage(t, res)
	dateByID := map[int]int{}
	for _, raw := range messages {
		if msg, ok := raw.(*tg.Message); ok {
			dateByID[msg.ID] = msg.Date
		}
	}
	var hash uint64
	for _, d := range dialogs {
		pinned := int64(0)
		if d.Pinned {
			pinned = 1
		}
		hash = tdesktopHashUpdate(hash, pinned)
		hash = tdesktopHashUpdate(hash, savedDialogPeerID(t, d))
		hash = tdesktopHashUpdate(hash, int64(d.TopMessage))
		hash = tdesktopHashUpdate(hash, int64(dateByID[d.TopMessage]))
	}
	cached, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		Limit: 20,
		Hash:  int64(hash),
	})
	if err != nil {
		t.Fatalf("getSavedDialogs hash: %v", err)
	}
	notModified, ok := cached.(*tg.MessagesSavedDialogsNotModified)
	if !ok || notModified.Count != 2 {
		t.Fatalf("hash hit = %#v, want notModified count 2", cached)
	}
}

// TestMessagesSavedDialogPinLifecycle verifies the full pin toggle/order/exclude
// chain: updateSavedDialogPinned push, pinned first on the first page,
// getPinnedSavedDialogs, the exclude_pinned semantics, and reorder pushing
// updatePinnedSavedDialogs.
func TestMessagesSavedDialogPinLifecycle(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, noteID, _ := savedDialogsFixture(t)

	pinSelfReq := &tg.MessagesToggleSavedDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}}}
	pinSelfReq.SetPinned(true)
	ok, err := r.onMessagesToggleSavedDialogPin(WithUserID(ctx, alice.ID), pinSelfReq)
	if err != nil || !ok {
		t.Fatalf("pin self sublist = %v err %v", ok, err)
	}

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs: %v", err)
	}
	dialogs, _, _, _, _ := savedDialogPage(t, res)
	if len(dialogs) != 2 || savedDialogPeerID(t, dialogs[0]) != alice.ID || !dialogs[0].Pinned {
		t.Fatalf("pinned self sublist not first: %+v", dialogs)
	}
	if dialogs[1].Pinned {
		t.Fatalf("bob sublist unexpectedly pinned")
	}

	pinnedRes, err := r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs: %v", err)
	}
	dialogs, _, _, _, full := savedDialogPage(t, pinnedRes)
	if !full || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != alice.ID || dialogs[0].TopMessage != noteID {
		t.Fatalf("pinned list = %+v, want self note", dialogs)
	}

	excluded, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		ExcludePinned: true,
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("getSavedDialogs exclude: %v", err)
	}
	dialogs, _, _, count, full := savedDialogPage(t, excluded)
	if !full || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("exclude pinned = %+v, want only bob", dialogs)
	}
	if count != 1 {
		t.Fatalf("exclude pinned count = %d, want 1", count)
	}

	pinBobReq := &tg.MessagesToggleSavedDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}}}
	pinBobReq.SetPinned(true)
	ok, err = r.onMessagesToggleSavedDialogPin(WithUserID(ctx, alice.ID), pinBobReq)
	if err != nil || !ok {
		t.Fatalf("pin bob sublist = %v err %v", ok, err)
	}
	// A new pin is inserted at the front.
	pinnedRes, err = r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs: %v", err)
	}
	dialogs, _, _, _, _ = savedDialogPage(t, pinnedRes)
	if len(dialogs) != 2 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("pinned order after pin bob = %+v, want bob first", dialogs)
	}

	ok, err = r.onMessagesReorderPinnedSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesReorderPinnedSavedDialogsRequest{
		Order: []tg.InputDialogPeerClass{
			&tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}},
			&tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}},
		},
	})
	if err != nil || !ok {
		t.Fatalf("reorder = %v err %v", ok, err)
	}
	pinnedRes, err = r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs after reorder: %v", err)
	}
	dialogs, _, _, _, _ = savedDialogPage(t, pinnedRes)
	if len(dialogs) != 2 || savedDialogPeerID(t, dialogs[0]) != alice.ID || savedDialogPeerID(t, dialogs[1]) != bob.ID {
		t.Fatalf("reorder result = %+v, want self then bob", dialogs)
	}

	ok, err = r.onMessagesToggleSavedDialogPin(WithUserID(ctx, alice.ID), &tg.MessagesToggleSavedDialogPinRequest{
		Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}},
	})
	if err != nil || !ok {
		t.Fatalf("unpin self = %v err %v", ok, err)
	}
	pinnedRes, err = r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs after unpin: %v", err)
	}
	dialogs, _, _, _, _ = savedDialogPage(t, pinnedRes)
	if len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("after unpin = %+v, want only bob", dialogs)
	}
}

// TestMessagesGetSavedDialogsByID verifies an explicit lookup: hits are returned, unknown peers are silently absent.
func TestMessagesGetSavedDialogsByID(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, _, fwdID := savedDialogsFixture(t)

	res, err := r.onMessagesGetSavedDialogsByID(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsByIDRequest{
		IDs: []tg.InputPeerClass{
			&tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
			&tg.InputPeerUser{UserID: 999999, AccessHash: 1},
		},
	})
	if err != nil {
		t.Fatalf("getSavedDialogsByID: %v", err)
	}
	dialogs, _, _, _, full := savedDialogPage(t, res)
	if !full || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID || dialogs[0].TopMessage != fwdID {
		t.Fatalf("byID = %+v, want bob sublist only", dialogs)
	}
}

// TestMessagesGetSavedHistoryFiltersBySavedPeer verifies sub-dialog history filtering, count and hash notModified.
func TestMessagesGetSavedHistoryFiltersBySavedPeer(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, noteID, fwdID := savedDialogsFixture(t)
	verify := newFakeBotVerifications()
	r.deps.BotVerifications = verify
	const savedHistoryIcon = int64(8800022)
	bobPeer := domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID}
	verify.marks[bobPeer] = domain.CustomVerification{
		VerifierBotID:  777000123,
		Peer:           bobPeer,
		IconDocumentID: savedHistoryIcon,
		Description:    "Verified saved peer",
	}

	bobHistory, err := r.onMessagesGetSavedHistory(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("getSavedHistory bob: %v", err)
	}
	assertMessagesEnvelopeBotVerificationIcon(t, bobHistory, bobPeer, savedHistoryIcon)
	bobMsgs := savedHistoryMessages(t, bobHistory)
	if len(bobMsgs) != 1 || bobMsgs[0].ID != fwdID {
		t.Fatalf("bob saved history = %+v, want forwarded message only", bobMsgs)
	}

	selfHistory, err := r.onMessagesGetSavedHistory(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("getSavedHistory self: %v", err)
	}
	selfMsgs := savedHistoryMessages(t, selfHistory)
	if len(selfMsgs) != 1 || selfMsgs[0].ID != noteID {
		t.Fatalf("self saved history = %+v, want note only", selfMsgs)
	}

	// max_id boundary: below the forward id it is empty.
	empty, err := r.onMessagesGetSavedHistory(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		MaxID: fwdID,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("getSavedHistory max_id: %v", err)
	}
	if msgs := savedHistoryMessages(t, empty); len(msgs) != 0 {
		t.Fatalf("max_id bounded history = %+v, want empty", msgs)
	}
}

func savedHistoryMessages(t *testing.T, res tg.MessagesMessagesClass) []*tg.Message {
	t.Helper()
	var raw []tg.MessageClass
	switch v := res.(type) {
	case *tg.MessagesMessages:
		raw = v.Messages
	case *tg.MessagesMessagesSlice:
		raw = v.Messages
	default:
		t.Fatalf("saved history = %T", res)
	}
	out := make([]*tg.Message, 0, len(raw))
	for _, m := range raw {
		msg, ok := m.(*tg.Message)
		if !ok {
			t.Fatalf("message = %T", m)
		}
		out = append(out, msg)
	}
	return out
}

// TestMessagesDeleteSavedHistoryRemovesSublist verifies deleting a sub-dialog: a
// pts-bearing affectedHistory, the sub-dialog disappearing from the list, pin
// cleanup, and the self-chat history disappearing in sync.
func TestMessagesDeleteSavedHistoryRemovesSublist(t *testing.T) {
	ctx := context.Background()
	r, messageStore, alice, bob, noteID, fwdID := savedDialogsFixture(t)

	pinBobReq := &tg.MessagesToggleSavedDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}}}
	pinBobReq.SetPinned(true)
	if _, err := r.onMessagesToggleSavedDialogPin(WithUserID(ctx, alice.ID), pinBobReq); err != nil {
		t.Fatalf("pin bob: %v", err)
	}

	affected, err := r.onMessagesDeleteSavedHistory(WithUserID(ctx, alice.ID), &tg.MessagesDeleteSavedHistoryRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
	})
	if err != nil {
		t.Fatalf("deleteSavedHistory: %v", err)
	}
	if affected.Pts == 0 || affected.PtsCount != 1 || affected.Offset != 0 {
		t.Fatalf("affected = %+v, want pts advance for 1 message, no continuation", affected)
	}

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs after delete: %v", err)
	}
	dialogs, _, _, _, _ := savedDialogPage(t, res)
	if len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != alice.ID {
		t.Fatalf("dialogs after delete = %+v, want only self note", dialogs)
	}

	pinnedRes, err := r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs after delete: %v", err)
	}
	dialogs, _, _, _, _ = savedDialogPage(t, pinnedRes)
	if len(dialogs) != 0 {
		t.Fatalf("pinned after delete = %+v, want empty (pin cleared)", dialogs)
	}

	list, err := messageStore.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("self-chat history: %v", err)
	}
	if len(list.Messages) != 1 || list.Messages[0].ID != noteID {
		t.Fatalf("self-chat history after delete = %+v, want only note (fwd %d gone)", list.Messages, fwdID)
	}
}

// TestModernForwardMessagesConstructorSavesToSelf reproduces Android's "Save to
// Saved Messages" path: DrKLO 12.7.x sends messages.forwardMessages#41d41ade,
// which is later than Layer 225; on a server 500 tgnet retries infinitely (the UI
// spins forever). After compat decoding it must be equivalent to #13704a7c: the
// forward succeeds and the source sub-dialog appears in Saved Messages.
func TestModernForwardMessagesConstructorSavesToSelf(t *testing.T) {
	ctx := context.Background()
	r, messageStore, userStore := newSavedDialogsTestRouter(t)
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 83, Phone: "15550009401", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 84, Phone: "15550009402", FirstName: "Bob"})

	if _, err := r.onMessagesSendMessage(WithUserID(ctx, bob.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
		Message:  "save me",
		RandomID: 94001,
	}); err != nil {
		t.Fatalf("bob send: %v", err)
	}
	bobMsgID := privateTopMessageID(t, messageStore, alice.ID, bob.ID)

	// Hand-encode the wire format per DrKLO TL_messages_forwardMessages.serializeToStream:
	// constructor + flags + from_peer + Vector<int> id + Vector<long> random_id + to_peer.
	var raw bin.Buffer
	raw.PutID(0x41d41ade)
	raw.PutInt(0) // flags: saving to Saved Messages carries no optional fields
	if err := (&tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}).Encode(&raw); err != nil {
		t.Fatalf("encode from_peer: %v", err)
	}
	raw.PutVectorHeader(1)
	raw.PutInt(bobMsgID)
	raw.PutVectorHeader(1)
	raw.PutLong(94002)
	if err := (&tg.InputPeerSelf{}).Encode(&raw); err != nil {
		t.Fatalf("encode to_peer: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(androidClientContext(), alice.ID), [8]byte{}, 0, &raw)
	if err != nil {
		t.Fatalf("dispatch messages.forwardMessages#41d41ade: %v", err)
	}
	// The sparse dispatcher exposes the canonical concrete class result.
	if _, ok := enc.(tg.UpdatesClass); !ok {
		t.Fatalf("forward result = %T, want tg.UpdatesClass", enc)
	}

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs after android forward: %v", err)
	}
	dialogs, messages, _, _, _ := savedDialogPage(t, res)
	if len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("saved dialogs after android forward = %+v, want bob sublist", dialogs)
	}
	msg, ok := messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("top message = %T", messages[0])
	}
	if saved, ok := msg.GetSavedPeerID(); !ok {
		t.Fatalf("forwarded message lacks saved_peer_id")
	} else if user, ok := saved.(*tg.PeerUser); !ok || user.UserID != bob.ID {
		t.Fatalf("saved_peer_id = %+v, want bob", saved)
	}
}

// TestSavedPeerForSelfChat verifies the sub-dialog grouping-key computation aligns with TDLib semantics.
func TestSavedPeerForSelfChat(t *testing.T) {
	self := int64(1000000001)
	bobPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	cases := []struct {
		name    string
		forward *domain.MessageForward
		want    domain.Peer
	}{
		{"direct note", nil, domain.Peer{Type: domain.PeerTypeUser, ID: self}},
		{"forward with source", &domain.MessageForward{From: bobPeer, SavedFrom: bobPeer, SavedFromMsgID: 7}, bobPeer},
		{"forward channel source", &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: 500}, SavedFrom: domain.Peer{Type: domain.PeerTypeChannel, ID: 500}}, domain.Peer{Type: domain.PeerTypeChannel, ID: 500}},
		{"hidden author without source", &domain.MessageForward{FromName: "Hidden"}, domain.Peer{Type: domain.PeerTypeUser, ID: domain.SavedHiddenAuthorUserID}},
		{"visible author without source", &domain.MessageForward{From: bobPeer}, domain.Peer{Type: domain.PeerTypeUser, ID: self}},
	}
	for _, tc := range cases {
		if got := domain.SavedPeerForSelfChat(self, tc.forward); got != tc.want {
			t.Fatalf("%s: SavedPeerForSelfChat = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
