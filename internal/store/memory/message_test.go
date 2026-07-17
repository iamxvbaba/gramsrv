package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

var validRichMessageBlocks = []byte{
	0x15, 0xc4, 0xb5, 0x1c, // vector
	0x01, 0x00, 0x00, 0x00, // count
	0x8a, 0x7e, 0x56, 0x13, // pageBlockUnsupported
}

func TestMessageStoreSendPrivateTextCreatesBothOwnerBoxes(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	req := domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        99,
		Message:         "hello",
		Date:            1700000100,
	}

	got, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	if got.SenderMessage.ID != 1 || got.SenderMessage.OwnerUserID != req.SenderUserID || !got.SenderMessage.Out || got.SenderMessage.Pts != 1 {
		t.Fatalf("sender message = %+v, want first outgoing box with pts=1", got.SenderMessage)
	}
	if got.RecipientMessage.ID != 1 || got.RecipientMessage.OwnerUserID != req.RecipientUserID || got.RecipientMessage.Out || got.RecipientMessage.Pts != 1 {
		t.Fatalf("recipient message = %+v, want first incoming box with pts=1", got.RecipientMessage)
	}
	if got.SenderMessage.UID == 0 || got.SenderMessage.UID != got.RecipientMessage.UID {
		t.Fatalf("uid = sender %d recipient %d, want shared private message uid", got.SenderMessage.UID, got.RecipientMessage.UID)
	}

	second, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    req.SenderUserID,
		RecipientUserID: req.RecipientUserID,
		RandomID:        100,
		Message:         "again",
		Date:            1700000110,
	})
	if err != nil {
		t.Fatalf("SendPrivateText second: %v", err)
	}
	if second.SenderMessage.ID != 2 || second.SenderMessage.Pts != 2 || second.RecipientMessage.ID != 2 || second.RecipientMessage.Pts != 2 {
		t.Fatalf("second send = %+v/%+v, want per-owner box_id and pts to advance", second.SenderMessage, second.RecipientMessage)
	}

	dup, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText duplicate: %v", err)
	}
	if !dup.Duplicate || dup.SenderMessage.ID != got.SenderMessage.ID || dup.RecipientMessage.ID != got.RecipientMessage.ID {
		t.Fatalf("duplicate = %+v, want original message boxes", dup)
	}

	afterDup, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    req.SenderUserID,
		RecipientUserID: req.RecipientUserID,
		RandomID:        101,
		Message:         "after duplicate",
		Date:            1700000111,
	})
	if err != nil {
		t.Fatalf("SendPrivateText after duplicate: %v", err)
	}
	if afterDup.SenderMessage.ID != 3 || afterDup.SenderMessage.Pts != 3 || afterDup.RecipientMessage.ID != 3 || afterDup.RecipientMessage.Pts != 3 {
		t.Fatalf("send after duplicate = %+v/%+v, want next contiguous box_id and pts", afterDup.SenderMessage, afterDup.RecipientMessage)
	}

	senderHistory, err := messages.ListByUser(ctx, req.SenderUserID, domain.MessageFilter{HasPeer: true, Peer: got.SenderMessage.Peer, Limit: 10})
	if err != nil {
		t.Fatalf("sender history: %v", err)
	}
	recipientHistory, err := messages.ListByUser(ctx, req.RecipientUserID, domain.MessageFilter{HasPeer: true, Peer: got.RecipientMessage.Peer, Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(senderHistory.Messages) != 3 || len(recipientHistory.Messages) != 3 {
		t.Fatalf("history sizes = sender %d recipient %d, want both owner partitions populated", len(senderHistory.Messages), len(recipientHistory.Messages))
	}
}

func TestMessageStoreSendPrivateTextContentInvariant(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()

	richOnly, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        191,
		Date:            1700000191,
		RichMessage:     &domain.MessageRichMessage{Blocks: validRichMessageBlocks},
	})
	if err != nil {
		t.Fatalf("SendPrivateText rich-only: %v", err)
	}
	if richOnly.SenderMessage.Body != "" || richOnly.SenderMessage.RichMessage.IsZero() {
		t.Fatalf("rich-only sender message = %+v, want empty body with rich payload", richOnly.SenderMessage)
	}

	_, err = messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        192,
		Date:            1700000192,
	})
	if !errors.Is(err, domain.ErrMessageEmpty) {
		t.Fatalf("SendPrivateText empty err = %v, want ErrMessageEmpty", err)
	}
}

func TestMessageStoreEditRichOnlyMessageUsesFinalContentState(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        201,
		Date:            1700000200,
		RichMessage:     &domain.MessageRichMessage{Blocks: validRichMessageBlocks},
	})
	if err != nil {
		t.Fatalf("SendPrivateText rich-only: %v", err)
	}

	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
		ID:          sent.SenderMessage.ID,
		Message:     "",
		HideEdited:  true,
		EditDate:    1700000210,
	})
	if err != nil {
		t.Fatalf("EditMessage rich-only hide-edit: %v", err)
	}
	if len(edited.Edited) != 2 {
		t.Fatalf("edited boxes = %d, want 2", len(edited.Edited))
	}
	for _, item := range edited.Edited {
		if item.Message.RichMessage.IsZero() {
			t.Fatalf("edited rich-only box for user %d lost rich message: %+v", item.UserID, item.Message)
		}
	}
}

func TestMessageStoreWebViewDataServiceActionRoundTrip(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	req := domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        199,
		Date:            1700000120,
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionWebViewDataSent,
				WebViewData: &domain.MessageWebViewDataAction{
					ButtonText: "Open",
					Data:       `{"ok":true}`,
				},
			},
		},
	}

	got, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	assertWebViewData := func(name string, msg domain.Message) {
		if msg.Media == nil || msg.Media.ServiceAction == nil ||
			msg.Media.ServiceAction.Kind != domain.MessageServiceActionWebViewDataSent ||
			msg.Media.ServiceAction.WebViewData == nil {
			t.Fatalf("%s media = %+v, want webview data service action", name, msg.Media)
		}
		if data := msg.Media.ServiceAction.WebViewData; data.ButtonText != "Open" || data.Data != `{"ok":true}` {
			t.Fatalf("%s webview data = %+v, want Open/data", name, data)
		}
	}
	assertWebViewData("sender", got.SenderMessage)
	assertWebViewData("recipient", got.RecipientMessage)

	_, err = messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    req.SenderUserID,
		RecipientUserID: req.RecipientUserID,
		RandomID:        req.RandomID,
		Date:            1700000121,
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionWebViewDataSent,
				WebViewData: &domain.MessageWebViewDataAction{
					ButtonText: "Changed",
					Data:       `{"ok":false}`,
				},
			},
		},
	})
	if !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("changed-media duplicate err = %v, want ErrMessageRandomIDDuplicate", err)
	}

	recipientHistory, err := messages.ListByUser(ctx, req.RecipientUserID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
		Limit:   10,
	})
	if err != nil || len(recipientHistory.Messages) != 1 {
		t.Fatalf("recipient history = %+v err=%v, want one message", recipientHistory, err)
	}
	assertWebViewData("recipient history", recipientHistory.Messages[0])
}

func TestMessageStorePrivateMessageReactionsAreSharedAcrossOwnerBoxes(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	aliceID := int64(1000000001)
	bobID := int64(1000000002)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    aliceID,
		RecipientUserID: bobID,
		RandomID:        101,
		Message:         "react to me",
		Date:            1700000100,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	reaction := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f44d"}
	res, err := messages.SetMessageReactions(ctx, domain.SetPrivateMessageReactionsRequest{
		UserID:    bobID,
		Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
		MessageID: sent.RecipientMessage.ID,
		Reactions: []domain.MessageReaction{
			reaction,
		},
		Big:  true,
		Date: 1700000200,
	})
	if err != nil {
		t.Fatalf("SetMessageReactions: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("reaction result messages = %d, want both owner boxes", len(res.Messages))
	}

	aliceReactions, err := messages.GetMessageReactions(ctx, domain.PrivateMessageReactionsRequest{
		OwnerUserID: aliceID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
		IDs:         []int{sent.SenderMessage.ID},
	})
	if err != nil {
		t.Fatalf("alice GetMessageReactions: %v", err)
	}
	if len(aliceReactions.Messages) != 1 || aliceReactions.Messages[0].Reactions == nil {
		t.Fatalf("alice reactions = %+v, want one enriched message", aliceReactions)
	}
	if got := aliceReactions.Messages[0].Reactions.Results; len(got) != 1 || got[0].Reaction != reaction || got[0].Count != 1 || got[0].ChosenOrder != 0 {
		t.Fatalf("alice reaction counts = %+v, want one peer reaction without chosen order", got)
	}
	if got := aliceReactions.Messages[0].Reactions.Recent; len(got) != 1 || got[0].UserID != bobID || got[0].SenderUserID != aliceID || !got[0].Unread || !got[0].Big || got[0].My {
		t.Fatalf("alice recent reactions = %+v, want bob non-my big reaction", got)
	}
	aliceBox, err := messages.GetByIDs(ctx, aliceID, []int{sent.SenderMessage.ID})
	if err != nil {
		t.Fatalf("alice GetByIDs after reaction: %v", err)
	}
	if len(aliceBox.Messages) != 1 || !aliceBox.Messages[0].ReactionUnread {
		t.Fatalf("alice box after reaction = %+v, want reaction_unread", aliceBox.Messages)
	}
	read, err := messages.ReadMessageContents(ctx, domain.ReadMessageContentsRequest{
		OwnerUserID: aliceID,
		IDs:         []int{sent.SenderMessage.ID},
		Date:        1700000210,
	})
	if err != nil {
		t.Fatalf("ReadMessageContents reaction: %v", err)
	}
	if !reflect.DeepEqual(read.MessageIDs, []int{sent.SenderMessage.ID}) || read.Event.Type != domain.UpdateEventReadMessageContents || read.Event.Pts == 0 {
		t.Fatalf("read reaction contents = %+v, want one read_message_contents event", read)
	}
	aliceBox, err = messages.GetByIDs(ctx, aliceID, []int{sent.SenderMessage.ID})
	if err != nil {
		t.Fatalf("alice GetByIDs after read reaction: %v", err)
	}
	if len(aliceBox.Messages) != 1 || aliceBox.Messages[0].ReactionUnread {
		t.Fatalf("alice box after read reaction = %+v, want reaction_unread cleared", aliceBox.Messages)
	}
	if got := aliceBox.Messages[0].Reactions.Recent; len(got) != 1 || got[0].Unread || got[0].SenderUserID != aliceID {
		t.Fatalf("alice recent reactions after read = %+v, want unread flag cleared", got)
	}

	bobReactions, err := messages.GetMessageReactions(ctx, domain.PrivateMessageReactionsRequest{
		OwnerUserID: bobID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
		IDs:         []int{sent.RecipientMessage.ID},
	})
	if err != nil {
		t.Fatalf("bob GetMessageReactions: %v", err)
	}
	if got := bobReactions.Messages[0].Reactions.Results; len(got) != 1 || got[0].ChosenOrder != 1 {
		t.Fatalf("bob reaction counts = %+v, want own chosen order", got)
	}
	if got := bobReactions.Messages[0].Reactions.Recent; len(got) != 1 || got[0].UserID != bobID || got[0].SenderUserID != aliceID || got[0].Unread || !got[0].My {
		t.Fatalf("bob recent reactions = %+v, want my reaction", got)
	}
}

func TestMessageStorePrivateReactionEnrichesDialogTopMessages(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	aliceID := int64(1000000001)
	bobID := int64(1000000002)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    aliceID,
		RecipientUserID: bobID,
		RandomID:        111,
		Message:         "latest from alice",
		Date:            1700000300,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	reaction := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\u2764"}
	if _, err := messages.SetMessageReactions(ctx, domain.SetPrivateMessageReactionsRequest{
		UserID:    bobID,
		Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
		MessageID: sent.RecipientMessage.ID,
		Reactions: []domain.MessageReaction{
			reaction,
		},
		Date: 1700000310,
	}); err != nil {
		t.Fatalf("SetMessageReactions: %v", err)
	}

	aliceDialogs, err := dialogs.ListByUser(ctx, aliceID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("alice ListByUser: %v", err)
	}
	if len(aliceDialogs.Dialogs) != 1 || aliceDialogs.Dialogs[0].TopMessage != sent.SenderMessage.ID || aliceDialogs.Dialogs[0].UnreadReactions != 1 {
		t.Fatalf("alice dialog = %+v, want top message with one unread reaction", aliceDialogs.Dialogs)
	}
	if len(aliceDialogs.Messages) != 1 || aliceDialogs.Messages[0].ID != sent.SenderMessage.ID || aliceDialogs.Messages[0].Reactions == nil {
		t.Fatalf("alice dialog messages = %+v, want enriched top message", aliceDialogs.Messages)
	}
	if got := aliceDialogs.Messages[0].Reactions.Recent; len(got) != 1 || got[0].UserID != bobID || got[0].SenderUserID != aliceID || !got[0].Unread || got[0].My {
		t.Fatalf("alice dialog recent reactions = %+v, want bob unread non-my reaction", got)
	}

	bobDialogs, err := dialogs.ListByPeers(ctx, bobID, []domain.Peer{{Type: domain.PeerTypeUser, ID: aliceID}})
	if err != nil {
		t.Fatalf("bob ListByPeers: %v", err)
	}
	if len(bobDialogs.Messages) != 1 || bobDialogs.Messages[0].Reactions == nil {
		t.Fatalf("bob peer dialog messages = %+v, want enriched top message", bobDialogs.Messages)
	}
	if got := bobDialogs.Messages[0].Reactions.Recent; len(got) != 1 || got[0].UserID != bobID || got[0].SenderUserID != aliceID || got[0].Unread || !got[0].My {
		t.Fatalf("bob peer dialog recent reactions = %+v, want my read reaction", got)
	}

	if _, err := messages.ReadMessageContents(ctx, domain.ReadMessageContentsRequest{
		OwnerUserID: aliceID,
		IDs:         []int{sent.SenderMessage.ID},
		Date:        1700000320,
	}); err != nil {
		t.Fatalf("ReadMessageContents: %v", err)
	}
	aliceDialogs, err = dialogs.ListByUser(ctx, aliceID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("alice ListByUser after read: %v", err)
	}
	if len(aliceDialogs.Dialogs) != 1 || aliceDialogs.Dialogs[0].UnreadReactions != 0 {
		t.Fatalf("alice dialog after read = %+v, want no unread reactions", aliceDialogs.Dialogs)
	}
	if got := aliceDialogs.Messages[0].Reactions.Recent; len(got) != 1 || got[0].Unread {
		t.Fatalf("alice dialog recent reactions after read = %+v, want unread cleared", got)
	}
}

func TestMessageStoreSendPrivateTextReplyAndForwardMetadata(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	aliceID := int64(1000000001)
	bobID := int64(1000000002)
	first, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    aliceID,
		RecipientUserID: bobID,
		RandomID:        501,
		Message:         "first",
		Date:            1700000100,
	})
	if err != nil {
		t.Fatalf("seed SendPrivateText: %v", err)
	}
	reply, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    aliceID,
		RecipientUserID: bobID,
		RandomID:        502,
		Message:         "reply",
		Silent:          true,
		NoForwards:      true,
		ReplyTo: &domain.MessageReply{
			MessageID:   first.SenderMessage.ID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
			QuoteText:   "fir",
			QuoteOffset: 0,
		},
		Date: 1700000110,
	})
	if err != nil {
		t.Fatalf("reply SendPrivateText: %v", err)
	}
	if reply.SenderMessage.ReplyTo == nil || reply.SenderMessage.ReplyTo.MessageID != first.SenderMessage.ID {
		t.Fatalf("sender reply = %+v, want sender-side message id", reply.SenderMessage.ReplyTo)
	}
	if reply.RecipientMessage.ReplyTo == nil || reply.RecipientMessage.ReplyTo.MessageID != first.RecipientMessage.ID {
		t.Fatalf("recipient reply = %+v, want translated recipient-side message id", reply.RecipientMessage.ReplyTo)
	}
	if !reply.SenderMessage.Silent || !reply.SenderMessage.NoForwards {
		t.Fatalf("reply flags = silent %v noforwards %v, want true/true", reply.SenderMessage.Silent, reply.SenderMessage.NoForwards)
	}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    aliceID,
		RecipientUserID: bobID,
		RandomID:        504,
		Message:         "bad quote offset",
		ReplyTo: &domain.MessageReply{
			MessageID:   first.SenderMessage.ID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
			QuoteText:   "fir",
			QuoteOffset: domain.MaxMessageReplyQuoteOffset + 1,
		},
		Date: 1700000115,
	}); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("bad quote offset err = %v, want ErrReplyMessageIDInvalid", err)
	}

	forwarded, err := messages.ForwardPrivateMessages(ctx, domain.ForwardPrivateMessagesRequest{
		OwnerUserID: aliceID,
		FromPeer:    domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
		ToUserID:    bobID,
		MessageIDs:  []int{first.SenderMessage.ID},
		RandomIDs:   []int64{503},
		ReplyTo: &domain.MessageReply{
			MessageID: first.SenderMessage.ID,
			Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
		},
		Date: 1700000120,
	})
	if err != nil {
		t.Fatalf("ForwardPrivateMessages: %v", err)
	}
	if len(forwarded.SenderMessages) != 1 || forwarded.SenderMessages[0].Forward == nil || forwarded.SenderMessages[0].Forward.From.ID != aliceID {
		t.Fatalf("forwarded messages = %+v, want original author header", forwarded.SenderMessages)
	}
	if forwarded.SenderMessages[0].ReplyTo == nil || forwarded.SenderMessages[0].ReplyTo.MessageID != first.SenderMessage.ID {
		t.Fatalf("forward reply = %+v, want target dialog reply header", forwarded.SenderMessages[0].ReplyTo)
	}
	if _, err := messages.ForwardPrivateMessages(ctx, domain.ForwardPrivateMessagesRequest{
		OwnerUserID: aliceID,
		FromPeer:    domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
		ToUserID:    aliceID,
		MessageIDs:  []int{reply.SenderMessage.ID},
		RandomIDs:   []int64{504},
		Date:        1700000130,
	}); err != domain.ErrChatForwardsRestricted {
		t.Fatalf("forward protected err=%v, want ErrChatForwardsRestricted", err)
	}
}

func TestMessageStoreListByUserSupportsForwardAndAroundHistoryOffsets(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	aliceID := int64(1000000001)
	bobID := int64(1000000002)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: aliceID}

	for i := 1; i <= 6; i++ {
		if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    aliceID,
			RecipientUserID: bobID,
			RandomID:        int64(600 + i),
			Message:         "history",
			Date:            1700000000 + i,
		}); err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
	}

	around, err := messages.ListByUser(ctx, bobID, domain.MessageFilter{
		HasPeer:   true,
		Peer:      peer,
		OffsetID:  3,
		AddOffset: -3,
		Limit:     6,
	})
	if err != nil {
		t.Fatalf("around history: %v", err)
	}
	if got := messageIDs(around.Messages); !sameInts(got, []int{6, 5, 4, 3, 2, 1}) {
		t.Fatalf("around ids = %v, want unread/newer side plus older context", got)
	}

	forward, err := messages.ListByUser(ctx, bobID, domain.MessageFilter{
		HasPeer:   true,
		Peer:      peer,
		OffsetID:  3,
		AddOffset: -3,
		Limit:     3,
	})
	if err != nil {
		t.Fatalf("forward history: %v", err)
	}
	if got := messageIDs(forward.Messages); !sameInts(got, []int{6, 5, 4}) {
		t.Fatalf("forward ids = %v, want messages newer than offset", got)
	}

	hugePositive, err := messages.ListByUser(ctx, bobID, domain.MessageFilter{
		HasPeer:   true,
		Peer:      peer,
		AddOffset: 1 << 30,
		Limit:     3,
	})
	if err != nil {
		t.Fatalf("huge positive add_offset history: %v", err)
	}
	if len(hugePositive.Messages) != 0 {
		t.Fatalf("huge positive add_offset ids = %v, want bounded empty page", messageIDs(hugePositive.Messages))
	}

	hugeNegative, err := messages.ListByUser(ctx, bobID, domain.MessageFilter{
		HasPeer:   true,
		Peer:      peer,
		OffsetID:  3,
		AddOffset: -1 << 30,
		Limit:     3,
	})
	if err != nil {
		t.Fatalf("huge negative add_offset history: %v", err)
	}
	if got := messageIDs(hugeNegative.Messages); !sameInts(got, []int{6, 5, 4}) {
		t.Fatalf("huge negative add_offset ids = %v, want clamped forward page", got)
	}
}

func TestMessageStoreReadHistoryEmitsInboxAndOutboxReceipts(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000001)
	recipientID := int64(1000000002)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    senderID,
		RecipientUserID: recipientID,
		RandomID:        101,
		Message:         "hello",
		Date:            1700000100,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}

	read, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipientID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: senderID},
		Date:        1700000200,
	})
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	if !read.Changed || read.InboxEvent.Type != domain.UpdateEventReadHistoryInbox || read.InboxEvent.Pts != 2 || read.InboxEvent.MaxID != sent.RecipientMessage.ID {
		t.Fatalf("inbox read = %+v, want recipient inbox pts=2 max recipient id", read)
	}
	if !read.OutboxChanged || read.OutboxUserID != senderID || read.OutboxEvent.Type != domain.UpdateEventReadHistoryOutbox || read.OutboxEvent.MaxID != sent.SenderMessage.ID {
		t.Fatalf("outbox read = %+v, want sender outbox receipt with sender message id", read)
	}
	date, err := messages.GetOutboxReadDate(ctx, domain.OutboxReadDateRequest{
		OwnerUserID: senderID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: recipientID},
		ID:          sent.SenderMessage.ID,
	})
	if err != nil || date != 1700000200 {
		t.Fatalf("outbox read date = %d err=%v, want read date", date, err)
	}
}

func TestMessageStoreReadHistoryStaleUnreadRepairDoesNotAdvancePts(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000101)
	recipientID := int64(1000000102)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    senderID,
		RecipientUserID: recipientID,
		RandomID:        201,
		Message:         "hello",
		Date:            1700000100,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: senderID}
	if _, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipientID,
		Peer:        peer,
		MaxID:       sent.RecipientMessage.ID,
		Date:        1700000200,
	}); err != nil {
		t.Fatalf("first ReadHistory: %v", err)
	}

	dialogs.mu.Lock()
	list := dialogs.m[recipientID]
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer != peer {
			continue
		}
		list.Dialogs[i].UnreadCount = 1
		list.Dialogs[i].UnreadMentions = 1
		list.Dialogs[i].UnreadReactions = 1
		list.Dialogs[i].UnreadMark = true
	}
	dialogs.m[recipientID] = list
	dialogs.mu.Unlock()

	read, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipientID,
		Peer:        peer,
		MaxID:       sent.RecipientMessage.ID,
		Date:        1700000300,
	})
	if err != nil {
		t.Fatalf("second ReadHistory: %v", err)
	}
	if read.Changed || read.InboxEvent.Pts != 0 || read.OutboxChanged {
		t.Fatalf("stale unread repair = %+v, want no read pts/outbox event", read)
	}

	dialogs.mu.RLock()
	repaired := dialogs.m[recipientID].Dialogs[0]
	dialogs.mu.RUnlock()
	// readHistory 重算 UnreadCount、清 mentions/mark，但不清 reaction 角标（与 PG
	// 对齐；reaction 未读由 readReactions/readMessageContents 单独清），故 UnreadReactions
	// 保留为 1。
	if repaired.UnreadCount != 0 || repaired.UnreadMentions != 0 || repaired.UnreadReactions != 1 || repaired.UnreadMark {
		t.Fatalf("dialog after repair = %+v, want unread/mentions/mark cleared and reactions preserved", repaired)
	}
	next, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    senderID,
		RecipientUserID: recipientID,
		RandomID:        202,
		Message:         "next",
		Date:            1700000400,
	})
	if err != nil {
		t.Fatalf("next SendPrivateText: %v", err)
	}
	if next.RecipientMessage.Pts != 3 {
		t.Fatalf("next recipient pts = %d, want 3 after no-op read repair", next.RecipientMessage.Pts)
	}
}

func TestMessageStoreSendPrivateTextRecomputesInboxUnreadFromReadMax(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000201)
	recipientID := int64(1000000202)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: senderID}

	first, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    senderID,
		RecipientUserID: recipientID,
		RandomID:        301,
		Message:         "already read",
		Date:            1700000500,
	})
	if err != nil {
		t.Fatalf("SendPrivateText first: %v", err)
	}
	if _, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipientID,
		Peer:        peer,
		MaxID:       first.RecipientMessage.ID,
		Date:        1700000510,
	}); err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}

	dialogs.mu.Lock()
	list := dialogs.m[recipientID]
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer == peer {
			list.Dialogs[i].UnreadCount = 2
		}
	}
	dialogs.m[recipientID] = list
	dialogs.mu.Unlock()

	bodies := []string{"one", "two", "three"}
	var last domain.SendPrivateTextResult
	for i, body := range bodies {
		last, err = messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    senderID,
			RecipientUserID: recipientID,
			RandomID:        310 + int64(i),
			Message:         body,
			Date:            1700000520 + i,
		})
		if err != nil {
			t.Fatalf("SendPrivateText %q: %v", body, err)
		}
	}

	dialogs.mu.RLock()
	var got domain.Dialog
	for _, dialog := range dialogs.m[recipientID].Dialogs {
		if dialog.Peer == peer {
			got = dialog
			break
		}
	}
	dialogs.mu.RUnlock()
	if got.UnreadCount != len(bodies) || got.TopMessage != last.RecipientMessage.ID || got.ReadInboxMaxID != first.RecipientMessage.ID {
		t.Fatalf("dialog = %+v, want unread=3 top=%d read=%d", got, last.RecipientMessage.ID, first.RecipientMessage.ID)
	}
}

func TestMessageStoreDeleteMessagesRecomputesRecipientUnreadAndTop(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000301)
	recipientID := int64(1000000302)
	recipientPeer := domain.Peer{Type: domain.PeerTypeUser, ID: senderID}

	bodies := []string{"one", "two", "three"}
	sent := make([]domain.SendPrivateTextResult, 0, len(bodies))
	for i, body := range bodies {
		got, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    senderID,
			RecipientUserID: recipientID,
			RandomID:        330 + int64(i),
			Message:         body,
			Date:            1700000600 + i,
		})
		if err != nil {
			t.Fatalf("SendPrivateText %q: %v", body, err)
		}
		sent = append(sent, got)
	}

	deleted, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
		OwnerUserID: senderID,
		IDs:         []int{sent[2].SenderMessage.ID},
		Revoke:      true,
		Date:        1700000610,
	})
	if err != nil {
		t.Fatalf("DeleteMessages revoke latest: %v", err)
	}
	if len(deleted.Deleted) != 2 || !deleted.Changed() {
		t.Fatalf("deleted = %+v, want both owner boxes revoked", deleted)
	}

	dialogList, err := dialogs.ListByUser(ctx, recipientID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("recipient dialogs: %v", err)
	}
	var got domain.Dialog
	for _, dialog := range dialogList.Dialogs {
		if dialog.Peer == recipientPeer {
			got = dialog
			break
		}
	}
	if got.UnreadCount != 2 || got.TopMessage != sent[1].RecipientMessage.ID {
		t.Fatalf("recipient dialog after revoke = %+v, want unread=2 top=%d", got, sent[1].RecipientMessage.ID)
	}
}

func TestMessageStoreDeleteMiddleUnreadKeepsRecipientTop(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000401)
	recipientID := int64(1000000402)
	recipientPeer := domain.Peer{Type: domain.PeerTypeUser, ID: senderID}

	first, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    senderID,
		RecipientUserID: recipientID,
		RandomID:        429,
		Message:         "already read",
		Date:            1700000690,
	})
	if err != nil {
		t.Fatalf("SendPrivateText first: %v", err)
	}
	if _, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: recipientID,
		Peer:        recipientPeer,
		MaxID:       first.RecipientMessage.ID,
		Date:        1700000695,
	}); err != nil {
		t.Fatalf("ReadHistory first: %v", err)
	}

	bodies := []string{"one", "two", "three"}
	sent := make([]domain.SendPrivateTextResult, 0, len(bodies))
	for i, body := range bodies {
		got, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    senderID,
			RecipientUserID: recipientID,
			RandomID:        430 + int64(i),
			Message:         body,
			Date:            1700000700 + i,
		})
		if err != nil {
			t.Fatalf("SendPrivateText %q: %v", body, err)
		}
		sent = append(sent, got)
	}

	if _, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
		OwnerUserID: senderID,
		IDs:         []int{sent[1].SenderMessage.ID},
		Revoke:      true,
		Date:        1700000710,
	}); err != nil {
		t.Fatalf("DeleteMessages revoke middle: %v", err)
	}

	dialogList, err := dialogs.ListByUser(ctx, recipientID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("recipient dialogs: %v", err)
	}
	var got domain.Dialog
	for _, dialog := range dialogList.Dialogs {
		if dialog.Peer == recipientPeer {
			got = dialog
			break
		}
	}
	if got.UnreadCount != 2 || got.TopMessage != sent[2].RecipientMessage.ID || got.ReadInboxMaxID != first.RecipientMessage.ID {
		t.Fatalf("recipient dialog after middle revoke = %+v, want unread=2 top=%d read=%d", got, sent[2].RecipientMessage.ID, first.RecipientMessage.ID)
	}
}

func TestMessageStoreReadMessageContentsClearsUnreadContentOnce(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1001,
		RecipientUserID: 1002,
		RandomID:        88,
		Message:         "voice placeholder",
		Media:           &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Voice: true},
		Date:            1700000300,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	if !sent.RecipientMessage.MediaUnread {
		t.Fatalf("recipient MediaUnread = false, want true for incoming media")
	}
	got, err := messages.ReadMessageContents(ctx, domain.ReadMessageContentsRequest{
		OwnerUserID: 1002,
		IDs:         []int{sent.RecipientMessage.ID, domain.MaxMessageBoxID},
		Date:        1700000400,
	})
	if err != nil {
		t.Fatalf("ReadMessageContents: %v", err)
	}
	if !reflect.DeepEqual(got.MessageIDs, []int{sent.RecipientMessage.ID}) {
		t.Fatalf("MessageIDs = %v, want unread recipient id", got.MessageIDs)
	}
	if got.Event.Type != domain.UpdateEventReadMessageContents || got.Event.Pts == 0 || got.Event.PtsCount != 1 {
		t.Fatalf("Event = %+v, want read_message_contents pts update", got.Event)
	}
	repeated, err := messages.ReadMessageContents(ctx, domain.ReadMessageContentsRequest{
		OwnerUserID: 1002,
		IDs:         []int{sent.RecipientMessage.ID},
		Date:        1700000500,
	})
	if err != nil {
		t.Fatalf("ReadMessageContents repeat: %v", err)
	}
	if len(repeated.MessageIDs) != 0 || repeated.Event.Pts != 0 {
		t.Fatalf("repeat = %+v, want no affected messages and no pts", repeated)
	}
	if _, err := messages.ReadMessageContents(ctx, domain.ReadMessageContentsRequest{
		OwnerUserID: 1002,
		IDs:         []int{0},
	}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("invalid id error = %v, want ErrMessageIDInvalid", err)
	}
}

func TestMessageStoreReadMessageContentsNotifiesVoiceSender(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1001,
		RecipientUserID: 1002,
		RandomID:        99,
		Message:         "",
		Media:           &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Voice: true},
		Date:            1700000300,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	if !sent.SenderMessage.MediaUnread {
		t.Fatalf("sender voice MediaUnread = false, want true until the peer listens")
	}
	photo, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1001,
		RecipientUserID: 1002,
		RandomID:        100,
		Message:         "",
		Media:           &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 7}},
		Date:            1700000301,
	})
	if err != nil {
		t.Fatalf("SendPrivateText photo: %v", err)
	}
	if photo.SenderMessage.MediaUnread || photo.RecipientMessage.MediaUnread {
		t.Fatalf("photo media_unread sender=%v recipient=%v, want false: only voice/round carry unread payload",
			photo.SenderMessage.MediaUnread, photo.RecipientMessage.MediaUnread)
	}
	read, err := messages.ReadMessageContents(ctx, domain.ReadMessageContentsRequest{
		OwnerUserID: 1002,
		IDs:         []int{sent.RecipientMessage.ID},
		Date:        1700000400,
	})
	if err != nil {
		t.Fatalf("ReadMessageContents: %v", err)
	}
	if len(read.SenderEvents) != 1 {
		t.Fatalf("SenderEvents = %+v, want one sender receipt", read.SenderEvents)
	}
	receipt := read.SenderEvents[0]
	if receipt.UserID != 1001 || receipt.Type != domain.UpdateEventReadMessageContents || receipt.Pts == 0 {
		t.Fatalf("receipt = %+v, want sender-side read_message_contents", receipt)
	}
	if !reflect.DeepEqual(receipt.MessageIDs, []int{sent.SenderMessage.ID}) {
		t.Fatalf("receipt ids = %v, want sender box id %d", receipt.MessageIDs, sent.SenderMessage.ID)
	}
	if receipt.Date != 1700000400 {
		t.Fatalf("receipt date = %d, want read time 1700000400", receipt.Date)
	}
	repeat, err := messages.ReadMessageContents(ctx, domain.ReadMessageContentsRequest{
		OwnerUserID: 1002,
		IDs:         []int{sent.RecipientMessage.ID},
		Date:        1700000500,
	})
	if err != nil {
		t.Fatalf("ReadMessageContents repeat: %v", err)
	}
	if len(repeat.SenderEvents) != 0 {
		t.Fatalf("repeat SenderEvents = %+v, want none", repeat.SenderEvents)
	}
}

func messageIDs(messages []domain.Message) []int {
	out := make([]int, 0, len(messages))
	for _, msg := range messages {
		out = append(out, msg.ID)
	}
	return out
}

func sameInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestMessageStoreEditMessageUpdatesBothBoxes(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000001)
	recipientID := int64(1000000002)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    senderID,
		RecipientUserID: recipientID,
		RandomID:        102,
		Message:         "before",
		Date:            1700000100,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}

	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID: senderID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: recipientID},
		ID:          sent.SenderMessage.ID,
		Message:     "after",
		EditDate:    1700000200,
	})
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if len(edited.Edited) != 2 || edited.Self().Message.Body != "after" || edited.Self().Event.Type != domain.UpdateEventEditMessage {
		t.Fatalf("edited = %+v, want both owner boxes and self edit event", edited)
	}
	recipientHistory, err := messages.ListByUser(ctx, recipientID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: senderID}, Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(recipientHistory.Messages) != 1 || recipientHistory.Messages[0].Body != "after" || recipientHistory.Messages[0].EditDate != 1700000200 {
		t.Fatalf("recipient history = %+v, want edited body/date", recipientHistory.Messages)
	}
}

func TestMessageStoreEditViaBotMessageUpdatesBothBoxes(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000001)
	recipientID := int64(1000000002)
	viaBotID := int64(1000000900)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    senderID,
		RecipientUserID: recipientID,
		RandomID:        103,
		Message:         "via before",
		ViaBotID:        viaBotID,
		Date:            1700000100,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}

	if _, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID:     recipientID,
		Peer:            domain.Peer{Type: domain.PeerTypeUser, ID: senderID},
		ID:              sent.RecipientMessage.ID,
		Message:         "bad bot",
		EditDate:        1700000190,
		ViaBotEditBotID: viaBotID + 1,
	}); err != domain.ErrMessageAuthorRequired {
		t.Fatalf("EditMessage wrong via bot err = %v, want ErrMessageAuthorRequired", err)
	}

	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID:     recipientID,
		Peer:            domain.Peer{Type: domain.PeerTypeUser, ID: senderID},
		ID:              sent.RecipientMessage.ID,
		Message:         "via after",
		EditDate:        1700000200,
		ViaBotEditBotID: viaBotID,
	})
	if err != nil {
		t.Fatalf("EditMessage via bot: %v", err)
	}
	if len(edited.Edited) != 2 {
		t.Fatalf("edited boxes = %d, want 2", len(edited.Edited))
	}
	senderHistory, err := messages.ListByUser(ctx, senderID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: recipientID}, Limit: 10})
	if err != nil {
		t.Fatalf("sender history: %v", err)
	}
	if len(senderHistory.Messages) != 1 || senderHistory.Messages[0].Body != "via after" || senderHistory.Messages[0].ViaBotID != viaBotID {
		t.Fatalf("sender history = %+v, want via after with via bot", senderHistory.Messages)
	}
}

func TestMessageStoreDeleteHistoryDeletesOrPreservesDialogAndRebuilds(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000001)
	recipientID := int64(1000000002)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipientID}

	for i := 0; i < 2; i++ {
		if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    senderID,
			RecipientUserID: recipientID,
			RandomID:        int64(100 + i),
			Message:         "hello",
			Date:            1700000200 + i,
		}); err != nil {
			t.Fatalf("seed send %d: %v", i, err)
		}
	}
	deleted, err := messages.DeleteHistory(ctx, domain.DeleteHistoryRequest{
		OwnerUserID: senderID,
		Peer:        peer,
		Date:        1700000300,
	})
	if err != nil {
		t.Fatalf("DeleteHistory: %v", err)
	}
	if self := deleted.Self(); self.Event.Pts != 4 || self.Event.PtsCount != 2 || len(self.MessageIDs) != 2 {
		t.Fatalf("delete result = %+v, want sender delete range pts=4 count=2", self)
	}
	senderHistory, err := messages.ListByUser(ctx, senderID, domain.MessageFilter{HasPeer: true, Peer: peer, Limit: 10})
	if err != nil {
		t.Fatalf("sender history: %v", err)
	}
	recipientHistory, err := messages.ListByUser(ctx, recipientID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: senderID}, Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(senderHistory.Messages) != 0 || len(recipientHistory.Messages) != 2 {
		t.Fatalf("history sizes sender=%d recipient=%d, want sender cleared only", len(senderHistory.Messages), len(recipientHistory.Messages))
	}
	senderDialogs, err := dialogs.ListByUser(ctx, senderID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("sender dialogs after delete: %v", err)
	}
	if len(senderDialogs.Dialogs) != 0 {
		t.Fatalf("sender dialogs = %+v, want dialog deleted after full history delete", senderDialogs.Dialogs)
	}

	rebuilt, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    senderID,
		RecipientUserID: recipientID,
		RandomID:        200,
		Message:         "rebuilt",
		Date:            1700000400,
	})
	if err != nil {
		t.Fatalf("send after delete: %v", err)
	}
	senderDialogs, err = dialogs.ListByUser(ctx, senderID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("sender dialogs after rebuild: %v", err)
	}
	if len(senderDialogs.Dialogs) != 1 || senderDialogs.Dialogs[0].Peer != peer || senderDialogs.Dialogs[0].TopMessage != rebuilt.SenderMessage.ID {
		t.Fatalf("rebuilt dialogs = %+v, want one dialog with new top message %d", senderDialogs.Dialogs, rebuilt.SenderMessage.ID)
	}

	preservedOwner := int64(1000000003)
	preservedPeerID := int64(1000000004)
	preservedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: preservedPeerID}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    preservedOwner,
		RecipientUserID: preservedPeerID,
		RandomID:        300,
		Message:         "clear but keep dialog",
		Date:            1700000500,
	}); err != nil {
		t.Fatalf("seed preserved send: %v", err)
	}
	if _, err := messages.DeleteHistory(ctx, domain.DeleteHistoryRequest{
		OwnerUserID: preservedOwner,
		Peer:        preservedPeer,
		JustClear:   true,
		Date:        1700000600,
	}); err != nil {
		t.Fatalf("DeleteHistory just_clear: %v", err)
	}
	preservedDialogs, err := dialogs.ListByUser(ctx, preservedOwner, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("preserved dialogs: %v", err)
	}
	if len(preservedDialogs.Dialogs) != 1 || preservedDialogs.Dialogs[0].Peer != preservedPeer || preservedDialogs.Dialogs[0].TopMessage != 0 || len(preservedDialogs.Messages) != 0 {
		t.Fatalf("preserved dialogs = %+v messages=%+v, want empty dialog kept after just_clear", preservedDialogs.Dialogs, preservedDialogs.Messages)
	}
}

func TestMessageStoreRevokeHistorySweepsPeerSideAfterLocalClear(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	for i := 0; i < 3; i++ {
		if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    1001,
			RecipientUserID: 1002,
			RandomID:        int64(700 + i),
			Message:         "history",
			Date:            1700000600 + i,
		}); err != nil {
			t.Fatalf("SendPrivateText %d: %v", i, err)
		}
	}
	// 先单向清空自己侧。
	if _, err := messages.DeleteHistory(ctx, domain.DeleteHistoryRequest{
		OwnerUserID: 1001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		Date:        1700000700,
	}); err != nil {
		t.Fatalf("local clear: %v", err)
	}
	// 再双向清史：反查模型对"我方已无 box"的消息失效，必须直扫对端。
	res, err := messages.DeleteHistory(ctx, domain.DeleteHistoryRequest{
		OwnerUserID: 1001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		Revoke:      true,
		Date:        1700000800,
	})
	if err != nil {
		t.Fatalf("revoke clear: %v", err)
	}
	var peerEvent bool
	for _, d := range res.Deleted {
		if d.UserID == 1002 && len(d.MessageIDs) == 3 {
			peerEvent = true
		}
	}
	if !peerEvent {
		t.Fatalf("revoke deleted = %+v, want peer-side sweep of all 3 messages", res.Deleted)
	}
	peerHistory, err := messages.ListByUser(ctx, 1002, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("peer history: %v", err)
	}
	if len(peerHistory.Messages) != 0 {
		t.Fatalf("peer history after revoke = %+v, want empty", peerHistory.Messages)
	}
}

func TestReorderPinnedForceScopedToFolder(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	mainPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 2001}
	archivedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 2002}
	if err := dialogs.Upsert(ctx, 1001, domain.Dialog{Peer: mainPeer, TopMessage: 1, TopMessageDate: 10}); err != nil {
		t.Fatalf("upsert main: %v", err)
	}
	if err := dialogs.Upsert(ctx, 1001, domain.Dialog{Peer: archivedPeer, FolderID: domain.DialogArchiveFolderID, TopMessage: 2, TopMessageDate: 20}); err != nil {
		t.Fatalf("upsert archived: %v", err)
	}
	if _, _, err := dialogs.SetPinned(ctx, 1001, mainPeer, true); err != nil {
		t.Fatalf("pin main: %v", err)
	}
	if _, folderID, err := dialogs.SetPinned(ctx, 1001, archivedPeer, true); err != nil || folderID != domain.DialogArchiveFolderID {
		t.Fatalf("pin archived folder = %d err %v, want archive", folderID, err)
	}
	// 归档列表内 force 重排：绝不允许清掉主列表的置顶。
	if changed, err := dialogs.ReorderPinned(ctx, 1001, domain.DialogArchiveFolderID, []domain.Peer{archivedPeer}, true); err != nil || changed {
		t.Fatalf("reorder archive = changed %v err %v, want no-op", changed, err)
	}
	list, err := dialogs.ListByUser(ctx, 1001, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list main: %v", err)
	}
	for _, dialog := range list.Dialogs {
		if dialog.Peer == mainPeer && !dialog.Pinned {
			t.Fatalf("main pin cleared by archive force reorder: %+v", dialog)
		}
	}
	// 主列表 force 重排同样不得波及归档置顶。
	if changed, err := dialogs.ReorderPinned(ctx, 1001, domain.DialogMainFolderID, []domain.Peer{mainPeer}, true); err != nil || changed {
		t.Fatalf("reorder main = changed %v err %v, want no-op", changed, err)
	}
	archived, err := dialogs.ListByUser(ctx, 1001, domain.DialogFilter{HasFolderID: true, FolderID: domain.DialogArchiveFolderID, Limit: 10})
	if err != nil {
		t.Fatalf("list archive: %v", err)
	}
	for _, dialog := range archived.Dialogs {
		if dialog.Peer == archivedPeer && !dialog.Pinned {
			t.Fatalf("archive pin cleared by main force reorder: %+v", dialog)
		}
	}
}

func TestSendPrivateTextClearsSenderUnreadMark(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	first, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        1,
		Message:         "hi",
		Date:            1700000100,
	})
	if err != nil {
		t.Fatalf("seed send: %v", err)
	}
	peer := first.SenderMessage.Peer
	if _, err := dialogs.SetUnreadMark(ctx, 1000000001, peer, true); err != nil {
		t.Fatalf("mark unread: %v", err)
	}
	// 发送方向发出消息即清手动未读标记（对齐 postgres UpsertOutboxDialog
	// 与 channel 发送路径）。
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        2,
		Message:         "again",
		Date:            1700000200,
	}); err != nil {
		t.Fatalf("send after mark: %v", err)
	}
	marks, err := dialogs.ListUnreadMarked(ctx, 1000000001)
	if err != nil {
		t.Fatalf("list unread marks: %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("unread marks after send = %+v, want cleared", marks)
	}
}

func TestSendPrivateTextPreservesPinnedDialog(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	first, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        1,
		Message:         "hi",
		Date:            1700000100,
	})
	if err != nil {
		t.Fatalf("seed send: %v", err)
	}
	peer := first.SenderMessage.Peer
	if _, _, err := dialogs.SetPinned(ctx, 1000000001, peer, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1000000001,
		RecipientUserID: 1000000002,
		RandomID:        2,
		Message:         "again",
		Date:            1700000200,
	}); err != nil {
		t.Fatalf("send after pin: %v", err)
	}
	list, err := dialogs.ListByUser(ctx, 1000000001, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, dialog := range list.Dialogs {
		if dialog.Peer == peer {
			found = true
			if !dialog.Pinned || dialog.PinnedOrder == 0 {
				t.Fatalf("dialog after send = %+v, want pinned preserved", dialog)
			}
		}
	}
	if !found {
		t.Fatalf("dialog not found after send")
	}
}
