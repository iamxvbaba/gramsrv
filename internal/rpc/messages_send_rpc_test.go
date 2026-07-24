package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strconv"
	"strings"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestMessagesSendMessageReturnsUpdateAndRecordsOwnerContext(t *testing.T) {
	sender := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Sender"}
	recipient := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Recipient"}
	messages := &captureMessages{}
	metrics := &captureRPCMetrics{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    mapUsersService{users: map[int64]domain.User{sender.ID: sender, recipient.ID: recipient}},
		Metrics:  metrics,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Message:  "hello",
		RandomID: 123456,
		Entities: []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 5},
			&tg.MessageEntityFormattedDate{Offset: 6, Length: 8, Date: 1773436800, ShortDate: true, ShortTime: true},
		},
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), sender.ID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.Updates)
	if !ok {
		t.Fatalf("response = %T, want *tg.Updates", enc)
	}
	if messages.sendUserID != sender.ID || messages.sendReq.SenderUserID != sender.ID || messages.sendReq.RecipientUserID != recipient.ID || messages.sendReq.OriginSessionID != 77 {
		t.Fatalf("send context = user %d req %+v, want sender/recipient/session", messages.sendUserID, messages.sendReq)
	}
	if len(messages.sendReq.IdempotencyFingerprint) != 32 {
		t.Fatalf("idempotency fingerprint length = %d, want SHA-256", len(messages.sendReq.IdempotencyFingerprint))
	}
	if len(messages.sendReq.Entities) != 2 || messages.sendReq.Entities[0].Type != domain.MessageEntityBold {
		t.Fatalf("entities = %+v, want bold and formatted date converted to domain", messages.sendReq.Entities)
	}
	dateEntity := messages.sendReq.Entities[1]
	if dateEntity.Type != domain.MessageEntityFormattedDate || dateEntity.Date != 1773436800 || !dateEntity.ShortDate || !dateEntity.ShortTime {
		t.Fatalf("formatted date entity = %+v, want date and flags preserved", dateEntity)
	}
	if len(got.Updates) != 2 {
		t.Fatalf("updates = %+v, want message id + new message", got.Updates)
	}
	if id, ok := got.Updates[0].(*tg.UpdateMessageID); !ok || id.ID != 1 || id.RandomID != req.RandomID {
		t.Fatalf("update id = %#v, want id=1 random_id=%d", got.Updates[0], req.RandomID)
	}
	newMsg, ok := got.Updates[1].(*tg.UpdateNewMessage)
	if !ok || newMsg.Pts != 1 || newMsg.PtsCount != 1 {
		t.Fatalf("new message update = %#v, want pts=1 pts_count=1", got.Updates[1])
	}
	msg, ok := newMsg.Message.(*tg.Message)
	if !ok || !msg.Out || msg.PeerID.(*tg.PeerUser).UserID != recipient.ID || msg.Message != req.Message {
		t.Fatalf("message = %#v, want outgoing private text to recipient", newMsg.Message)
	}
	if len(msg.Entities) != 2 {
		t.Fatalf("message entities = %+v, want 2", msg.Entities)
	}
	formatted, ok := msg.Entities[1].(*tg.MessageEntityFormattedDate)
	if !ok || formatted.Date != 1773436800 || !formatted.ShortDate || !formatted.ShortTime {
		t.Fatalf("formatted response entity = %#v, want date and flags preserved", msg.Entities[1])
	}
	if metrics.messageSend != 1 || metrics.messageSendErr != nil {
		t.Fatalf("metrics send=%d err=%v, want one successful send", metrics.messageSend, metrics.messageSendErr)
	}
}

func TestMessagesSendMessageRateLimitReturnsFloodWait(t *testing.T) {
	const userID = int64(1000000001)
	limiter := &captureRateLimiter{block: true, retryAfter: 9}
	metrics := &captureRPCMetrics{}
	r := New(Config{SendRateLimit: 1, SendRateWindow: time.Minute}, Deps{
		Limiter: limiter,
		Metrics: metrics,
	}, zaptest.NewLogger(t), clock.System)

	_, err := r.onMessagesSendMessage(WithUserID(context.Background(), userID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerSelf{},
		Message:  "too fast",
		RandomID: 123456,
	})
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") || !strings.Contains(err.Error(), "(9)") {
		t.Fatalf("sendMessage rate err = %v, want FLOOD_WAIT 9", err)
	}
	if len(limiter.calls) != 1 {
		t.Fatalf("limiter calls = %d, want 1", len(limiter.calls))
	}
	call := limiter.calls[0]
	if call.key != sendRateLimitKeyPrefix+strconv.FormatInt(userID, 10) || call.cost != 1 || call.limit != 1 || call.window != time.Minute {
		t.Fatalf("limiter call = %+v, want send key cost=1 limit=1 window=1m", call)
	}
	if metrics.rateLimited != 9 {
		t.Fatalf("rate limited metric = %d, want 9", metrics.rateLimited)
	}
}

func TestMessagesForwardMessagesRateLimitCountsIDs(t *testing.T) {
	const userID = int64(1000000001)
	limiter := &captureRateLimiter{block: true, retryAfter: 13}
	r := New(Config{SendRateLimit: 3, SendRateWindow: 4 * time.Second}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	_, err := r.onMessagesForwardMessages(WithUserID(context.Background(), userID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerSelf{},
		ToPeer:   &tg.InputPeerSelf{},
		ID:       []int{1, 2, 3},
		RandomID: []int64{11, 22, 33},
	})
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") || !strings.Contains(err.Error(), "(13)") {
		t.Fatalf("forwardMessages rate err = %v, want FLOOD_WAIT 13", err)
	}
	if len(limiter.calls) != 1 {
		t.Fatalf("limiter calls = %d, want 1", len(limiter.calls))
	}
	call := limiter.calls[0]
	if call.key != sendRateLimitKeyPrefix+strconv.FormatInt(userID, 10) || call.cost != 3 || call.limit != 3 || call.window != 4*time.Second {
		t.Fatalf("limiter call = %+v, want shared send key cost=3 limit=3 window=4s", call)
	}
}

func TestMessagesSendMessageSupportsReplyAndFlags(t *testing.T) {
	const (
		senderID    = int64(1000000001)
		recipientID = int64(1000000002)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    mapUsersService{users: map[int64]domain.User{senderID: {ID: senderID, FirstName: "Sender"}, recipientID: {ID: recipientID, FirstName: "Recipient"}}},
	}, zaptest.NewLogger(t), clock.System)
	reply := &tg.InputReplyToMessage{ReplyToMsgID: 7}
	reply.SetQuoteText("hello")
	reply.SetQuoteOffset(1)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipientID},
		Message:  "reply",
		RandomID: 456,
		Silent:   true,
	}
	req.SetNoforwards(true)
	req.SetReplyTo(reply)
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), senderID), [8]byte{}, 88, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if messages.sendReq.ReplyTo == nil || messages.sendReq.ReplyTo.MessageID != 7 || messages.sendReq.ReplyTo.Peer.ID != recipientID || messages.sendReq.ReplyTo.QuoteText != "hello" {
		t.Fatalf("reply request = %+v, want reply metadata", messages.sendReq.ReplyTo)
	}
	if !messages.sendReq.Silent || !messages.sendReq.NoForwards {
		t.Fatalf("send flags silent=%v noforwards=%v, want true/true", messages.sendReq.Silent, messages.sendReq.NoForwards)
	}
	got, ok := enc.(*tg.Updates)
	if !ok {
		t.Fatalf("response = %T, want *tg.Updates", enc)
	}
	newMsg := got.Updates[1].(*tg.UpdateNewMessage)
	msg := newMsg.Message.(*tg.Message)
	if !msg.Silent || !msg.Noforwards {
		t.Fatalf("message flags silent=%v noforwards=%v, want true/true", msg.Silent, msg.Noforwards)
	}
	header, ok := msg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header.ReplyToMsgID != 7 {
		t.Fatalf("reply header = %#v, want msg id 7", msg.ReplyTo)
	}
}

func TestMessagesSendMessageRejectsHugeReplyQuoteOffset(t *testing.T) {
	const (
		senderID    = int64(1000000001)
		recipientID = int64(1000000002)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    mapUsersService{users: map[int64]domain.User{recipientID: {ID: recipientID, FirstName: "Recipient"}}},
	}, zaptest.NewLogger(t), clock.System)
	reply := &tg.InputReplyToMessage{ReplyToMsgID: 7}
	reply.SetQuoteText("hello")
	reply.SetQuoteOffset(domain.MaxMessageReplyQuoteOffset + 1)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipientID},
		Message:  "reply",
		RandomID: 457,
	}
	req.SetReplyTo(reply)

	if _, err := r.onMessagesSendMessage(WithUserID(context.Background(), senderID), req); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("huge quote offset err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
	if messages.sendReq.RandomID != 0 {
		t.Fatalf("send request reached service: %+v", messages.sendReq)
	}
}

// story 回复（评论）：reply_to=inputReplyToStory 必须被接受并投影为 messageReplyStoryHeader，
// 而非旧的 STORY_ID_INVALID 拒绝（真机暴露：Alice 回复 Bob story 时评论消息发送失败）。
func TestMessageReplyFromInputStorySucceedsAndProjectsStoryHeader(t *testing.T) {
	sender := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Sender"}
	recipient := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Recipient"}
	r := New(Config{}, Deps{
		Users: mapUsersService{users: map[int64]domain.User{sender.ID: sender, recipient.ID: recipient}},
	}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), sender.ID)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}
	input := &tg.InputReplyToStory{Peer: &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash}, StoryID: 7}

	reply, err := r.messageReplyFromInput(ctx, sender.ID, peer, input)
	if err != nil {
		t.Fatalf("story reply err = %v, want nil（story 回复应被接受）", err)
	}
	if reply == nil || reply.StoryID != 7 || reply.Peer != peer {
		t.Fatalf("reply = %+v, want StoryID=7 peer=recipient", reply)
	}

	header := tgMessageReplyHeader(domain.Message{Peer: peer, ReplyTo: reply})
	sh, ok := header.(*tg.MessageReplyStoryHeader)
	if !ok {
		t.Fatalf("header = %T, want *tg.MessageReplyStoryHeader", header)
	}
	if sh.StoryID != 7 {
		t.Fatalf("header story id = %d, want 7", sh.StoryID)
	}
	if pu, ok := sh.Peer.(*tg.PeerUser); !ok || pu.UserID != recipient.ID {
		t.Fatalf("header peer = %#v, want recipient story owner", sh.Peer)
	}

	// 回复非会话对端的 story（peer 不匹配）仍被拒。
	wrongPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}
	if _, err := r.messageReplyFromInput(ctx, sender.ID, wrongPeer, input); err == nil || !strings.Contains(err.Error(), "STORY_ID_INVALID") {
		t.Fatalf("mismatched story owner err = %v, want STORY_ID_INVALID", err)
	}
}

func TestMessageReplyFromInputEmptyMessageIsAbsent(t *testing.T) {
	const userID = int64(1000000001)
	ctx := WithUserID(context.Background(), userID)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}

	reply, err := r.messageReplyFromInput(ctx, userID, peer, &tg.InputReplyToMessage{})
	if err != nil {
		t.Fatalf("empty reply err = %v, want nil", err)
	}
	if reply != nil {
		t.Fatalf("empty reply = %+v, want nil", reply)
	}

	topicReply := &tg.InputReplyToMessage{}
	topicReply.SetTopMsgID(123)
	reply, err = r.messageReplyFromInput(ctx, userID, peer, topicReply)
	if err != nil {
		t.Fatalf("topic-only reply err = %v, want nil", err)
	}
	if reply == nil || reply.MessageID != 0 || reply.TopMessageID != 123 {
		t.Fatalf("topic-only reply = %+v, want top_msg_id=123", reply)
	}

	quoteOnly := &tg.InputReplyToMessage{}
	quoteOnly.SetQuoteText("orphan quote")
	if _, err := r.messageReplyFromInput(ctx, userID, peer, quoteOnly); err == nil || !strings.Contains(err.Error(), "REPLY_MESSAGE_ID_INVALID") {
		t.Fatalf("quote-only reply err = %v, want REPLY_MESSAGE_ID_INVALID", err)
	}
}

func TestMessageReplyFromInputUnsupportedShapesReturnExplicitErrors(t *testing.T) {
	const userID = int64(1000000001)
	ctx := WithUserID(context.Background(), userID)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 1000000002}
	withReplyMsg := func(update func(*tg.InputReplyToMessage)) *tg.InputReplyToMessage {
		reply := &tg.InputReplyToMessage{ReplyToMsgID: 7}
		update(reply)
		return reply
	}
	cases := []struct {
		name  string
		input tg.InputReplyToClass
		want  string
	}{
		{
			name:  "story",
			input: &tg.InputReplyToStory{Peer: &tg.InputPeerUser{UserID: 1000000003}, StoryID: 1},
			want:  "STORY_ID_INVALID",
		},
		{
			name:  "monoforum constructor",
			input: &tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerChannel{ChannelID: 1000000004}},
			want:  "REPLY_TO_MONOFORUM_PEER_INVALID",
		},
		{
			name: "monoforum field",
			input: withReplyMsg(func(reply *tg.InputReplyToMessage) {
				reply.SetMonoforumPeerID(&tg.InputPeerChannel{ChannelID: 1000000004})
			}),
			want: "REPLY_TO_MONOFORUM_PEER_INVALID",
		},
		{
			name: "todo item",
			input: withReplyMsg(func(reply *tg.InputReplyToMessage) {
				reply.SetTodoItemID(1)
			}),
			want: "REPLY_MESSAGE_ID_INVALID",
		},
		{
			name: "poll option",
			input: withReplyMsg(func(reply *tg.InputReplyToMessage) {
				reply.SetPollOption([]byte{1})
			}),
			want: "POLL_OPTION_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.messageReplyFromInput(ctx, userID, peer, tc.input); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("reply err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMessagesSendMessageUnsupportedOptionErrors(t *testing.T) {
	const (
		senderID    = int64(1000000001)
		recipientID = int64(1000000002)
	)
	ctx := WithUserID(context.Background(), senderID)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	base := func() *tg.MessagesSendMessageRequest {
		return &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerUser{UserID: recipientID},
			Message:  "hello",
			RandomID: 456,
		}
	}
	suggested := func() tg.SuggestedPost {
		post := tg.SuggestedPost{}
		post.SetAccepted(true)
		return post
	}
	cases := []struct {
		name string
		req  *tg.MessagesSendMessageRequest
		want string
	}{
		{
			name: "quick reply",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetQuickReplyShortcut(&tg.InputQuickReplyShortcut{Shortcut: "hello"})
				return req
			}(),
			want: "SHORTCUT_INVALID",
		},
		{
			name: "effect",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetEffect(1)
				return req
			}(),
			want: "EFFECT_ID_INVALID",
		},
		{
			name: "negative paid stars",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetAllowPaidStars(-1)
				return req
			}(),
			want: "STARS_AMOUNT_INVALID",
		},
		{
			name: "paid floodskip",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetAllowPaidFloodskip(true)
				return req
			}(),
			want: "PAYMENT_UNSUPPORTED",
		},
		{
			name: "suggested post",
			req: func() *tg.MessagesSendMessageRequest {
				req := base()
				req.SetSuggestedPost(suggested())
				return req
			}(),
			want: "SUGGESTED_POST_PEER_INVALID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onMessagesSendMessage(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("send err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestMessagesSendMessageAllowsUnusedPaidAuthorizationForFreeRecipient(t *testing.T) {
	const (
		senderID    = int64(1000000001)
		recipientID = int64(1000000002)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			senderID:    {ID: senderID, FirstName: "Sender"},
			recipientID: {ID: recipientID, FirstName: "Recipient"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipientID},
		Message:  "free",
		RandomID: 457,
	}
	req.SetAllowPaidStars(10)
	if _, err := r.onMessagesSendMessage(WithUserID(context.Background(), senderID), req); err != nil {
		t.Fatalf("free recipient with unused authorization: %v", err)
	}
}
