package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestChannelMultiPinAndroidOpenAndJump simulates DrKLO Android's multi-pin consumption chain for
// a supergroup: open the chat -> messages.search(filterPinned, limit=40, offset_id=0) to pull the
// full pinned list; tap the pinned bar to jump to the oldest pin ->
// messages.getHistory(offset_id=pin, add_offset=-count/2, limit=count) loads AROUND, and the
// response must include the anchor message itself, otherwise the client shows MessageNotFound and
// abandons the jump; a missing local object -> channels.getMessages fetches it precisely, and
// messageEmpty is discarded by the client.
func TestChannelMultiPinAndroidOpenAndJump(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 61, Phone: "15550001161", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 62, Phone: "15550001162", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)

	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "MultiPin Android",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700001000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID

	const total = 30
	ids := make([]int, 0, total)
	for i := 0; i < total; i++ {
		sent, err := channelSvc.SendMessage(ctx, owner.ID, domain.SendChannelMessageRequest{
			ChannelID: channelID,
			RandomID:  int64(961000 + i),
			Message:   "msg",
			Date:      1700001001 + i,
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		ids = append(ids, sent.Message.ID)
	}
	// Five pins: covering the real scale from user reports; Android's pinned-bar cyclic jumping needs all of them jumpable.
	pins := []int{ids[4], ids[9], ids[14], ids[20], ids[27]}
	for _, id := range pins {
		if _, err := channelSvc.UpdatePinnedMessage(ctx, owner.ID, domain.UpdateChannelPinnedMessageRequest{
			ChannelID: channelID,
			MessageID: id,
			Pinned:    true,
			Date:      1700001100,
		}); err != nil {
			t.Fatalf("pin %d: %v", id, err)
		}
	}

	memberView, err := channelSvc.GetChannel(ctx, member.ID, channelID)
	if err != nil {
		t.Fatalf("member get channel: %v", err)
	}
	peer := &tg.InputPeerChannel{ChannelID: channelID, AccessHash: memberView.Channel.AccessHash}
	dispatch := func(req bin.Encoder) bin.Encoder {
		t.Helper()
		var b bin.Buffer
		if err := req.Encode(&b); err != nil {
			t.Fatalf("encode request: %v", err)
		}
		enc, err := r.Dispatch(WithUserID(androidClientContext(), member.ID), [8]byte{}, 0, &b)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		return enc
	}

	// (1) Open the chat: MediaDataController.loadPinnedMessages -> messages.search filterPinned.
	androidPinnedSearch := &tg.MessagesSearchRequest{
		Peer:   peer,
		Q:      "",
		Filter: &tg.InputMessagesFilterPinned{},
		Limit:  40,
	}
	// DrKLO initializes saved_reaction to an empty non-nil ArrayList and its
	// serializer consequently emits flags.3 + Vector length 0 on every
	// messages.search, including channel filterPinned.
	androidPinnedSearch.SetSavedReaction([]tg.ReactionClass{})
	searchEnc := dispatch(androidPinnedSearch)
	channelMessages, ok := searchEnc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("pinned search response = %T, want messages.channelMessages", searchEnc)
	}
	if len(channelMessages.Messages) != len(pins) {
		t.Fatalf("pinned search messages = %d, want %d", len(channelMessages.Messages), len(pins))
	}
	if channelMessages.Count != len(pins) {
		t.Fatalf("pinned search count = %d, want %d", channelMessages.Count, len(pins))
	}
	wantDesc := make([]int, len(pins))
	for i := range pins {
		wantDesc[i] = pins[len(pins)-1-i]
	}
	for i, raw := range channelMessages.Messages {
		msg, ok := raw.(*tg.Message)
		if !ok {
			// TL_messageService / TL_messageEmpty are skipped directly by Android loadPinnedMessages.
			t.Fatalf("pinned search message[%d] = %T, want *tg.Message", i, raw)
		}
		if msg.ID != wantDesc[i] {
			t.Fatalf("pinned search order[%d] = %d, want %d (id desc)", i, msg.ID, wantDesc[i])
		}
		if !msg.Pinned {
			t.Fatalf("pinned search message %d lacks pinned flag", msg.ID)
		}
	}

	// (2) tweb cold load: first fetch the latest pin with limit=1, and treat
	// messages.channelMessages.count as the full pin count; the old implementation miscounted count
	// as len(page)+hasMore, reporting only two of five.
	twebSearchEnc := dispatch(&tg.MessagesSearchRequest{
		Peer:   peer,
		Q:      "",
		Filter: &tg.InputMessagesFilterPinned{},
		Limit:  1,
	})
	twebSearch, ok := twebSearchEnc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("tweb pinned search response = %T, want messages.channelMessages", twebSearchEnc)
	}
	if len(twebSearch.Messages) != 1 || twebSearch.Count != len(pins) {
		t.Fatalf("tweb pinned search messages/count = %d/%d, want 1/%d", len(twebSearch.Messages), twebSearch.Count, len(pins))
	}

	// limit=0 is the official count-only entry point: it must not deserialize/return a message page just to count.
	countOnlyEnc := dispatch(&tg.MessagesSearchRequest{
		Peer:   peer,
		Q:      "",
		Filter: &tg.InputMessagesFilterPinned{},
		Limit:  0,
	})
	countOnly, ok := countOnlyEnc.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("count-only pinned search response = %T, want messages.channelMessages", countOnlyEnc)
	}
	if len(countOnly.Messages) != 0 || countOnly.Count != len(pins) {
		t.Fatalf("count-only pinned search messages/count = %d/%d, want 0/%d", len(countOnly.Messages), countOnly.Count, len(pins))
	}

	counters, err := r.onMessagesGetSearchCounters(WithUserID(androidClientContext(), member.ID), &tg.MessagesGetSearchCountersRequest{
		Peer:    peer,
		Filters: []tg.MessagesFilterClass{&tg.InputMessagesFilterPinned{}},
	})
	if err != nil {
		t.Fatalf("messages.getSearchCounters(filterPinned): %v", err)
	}
	if len(counters) != 1 || counters[0].Count != len(pins) {
		t.Fatalf("pinned search counters = %+v, want count %d", counters, len(pins))
	}

	// (3) Tap the pinned bar to jump to the oldest pin: scrollToMessageId -> getHistory AROUND (mobile count=20).
	const aroundCount = 20
	histEnc := dispatch(&tg.MessagesGetHistoryRequest{
		Peer:      peer,
		OffsetID:  pins[0],
		AddOffset: -aroundCount / 2,
		Limit:     aroundCount,
	})
	histMessages, _, _ := searchMessagesPayload(t, histEnc)
	if len(histMessages) == 0 || len(histMessages) > aroundCount {
		t.Fatalf("around history size = %d, want 1..%d (Android drops the newest one when count is exceeded)", len(histMessages), aroundCount)
	}
	anchorFound := false
	lastID := int(^uint(0) >> 1)
	for _, raw := range histMessages {
		if _, isEmpty := raw.(*tg.MessageEmpty); isEmpty {
			t.Fatalf("around history contains messageEmpty")
		}
		id := raw.GetID()
		if id >= lastID {
			t.Fatalf("around history not id-desc: %d then %d", lastID, id)
		}
		lastID = id
		if id == pins[0] {
			anchorFound = true
		}
	}
	if !anchorFound {
		// ChatActivity postponedScroll gives up the jump with MessageNotFound directly when the response lacks the anchor.
		t.Fatalf("around history lacks anchor %d: jump shows MessageNotFound on Android", pins[0])
	}

	// (4) Fetch a missing local object precisely: MessagesStorage.loadChatInfo -> channels.getMessages.
	// DrKLO sends the pre-InputMessage constructor #93d7b347 (id:Vector<int>), and a 500 for that
	// request makes the client delete this batch of pins from its local cache as "unpinned".
	var legacy bin.Buffer
	legacy.PutID(0x93d7b347)
	if err := (&tg.InputChannel{ChannelID: channelID, AccessHash: memberView.Channel.AccessHash}).Encode(&legacy); err != nil {
		t.Fatalf("encode legacy input channel: %v", err)
	}
	legacy.PutVectorHeader(len(pins))
	for _, id := range pins {
		legacy.PutInt(id)
	}
	getEnc, err := r.Dispatch(WithUserID(androidClientContext(), member.ID), [8]byte{}, 0, &legacy)
	if err != nil {
		t.Fatalf("dispatch legacy channels.getMessages#93d7b347: %v", err)
	}
	getMessages, _, _ := searchMessagesPayload(t, getEnc)
	if len(getMessages) != len(pins) {
		t.Fatalf("legacy channels.getMessages size = %d, want %d", len(getMessages), len(pins))
	}
	for i, raw := range getMessages {
		msg, ok := raw.(*tg.Message)
		if !ok {
			t.Fatalf("legacy channels.getMessages[%d] = %T, want *tg.Message (messageEmpty is discarded by the client)", i, raw)
		}
		if !msg.Pinned {
			t.Fatalf("legacy channels.getMessages message %d lacks pinned flag", msg.ID)
		}
	}
	// The new constructor (TDesktop path) must return the same message set as the legacy one.
	getIDs := make([]tg.InputMessageClass, 0, len(pins))
	for _, id := range pins {
		getIDs = append(getIDs, &tg.InputMessageID{ID: id})
	}
	modernEnc := dispatch(&tg.ChannelsGetMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: memberView.Channel.AccessHash},
		ID:      getIDs,
	})
	modernMessages, _, _ := searchMessagesPayload(t, modernEnc)
	if len(modernMessages) != len(getMessages) {
		t.Fatalf("modern channels.getMessages size = %d, want %d (legacy/modern must match)", len(modernMessages), len(getMessages))
	}
	for i := range modernMessages {
		if modernMessages[i].GetID() != getMessages[i].GetID() {
			t.Fatalf("modern/legacy mismatch at %d: %d != %d", i, modernMessages[i].GetID(), getMessages[i].GetID())
		}
	}

	// (5) chatFull degraded cache: pinned_msg_id must be the latest pin (Android uses it to decide whether to re-pull the list).
	fullEnc := dispatch(&tg.ChannelsGetFullChannelRequest{
		Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: memberView.Channel.AccessHash},
	})
	full, ok := fullEnc.(*tg.MessagesChatFull)
	if !ok {
		t.Fatalf("getFullChannel response = %T, want messages.chatFull", fullEnc)
	}
	channelFull, ok := full.FullChat.(*tg.ChannelFull)
	if !ok {
		t.Fatalf("full chat = %T, want channelFull", full.FullChat)
	}
	if pinnedID, _ := channelFull.GetPinnedMsgID(); pinnedID != pins[len(pins)-1] {
		t.Fatalf("channelFull pinned_msg_id = %d, want latest pin %d", pinnedID, pins[len(pins)-1])
	}
}
