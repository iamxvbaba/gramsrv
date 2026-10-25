package rpc

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type multiCaptureMessages struct {
	captureMessages
	requests []domain.SendPrivateTextRequest
}

func (m *multiCaptureMessages) SendPrivateText(_ context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	m.requests = append(m.requests, req)
	id := len(m.requests)
	msg := domain.Message{
		ID:          id,
		OwnerUserID: req.SenderUserID,
		RandomID:    req.RandomID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID},
		Date:        req.Date,
		Out:         true,
		Silent:      req.Silent,
		NoForwards:  req.NoForwards,
		Body:        req.Message,
		Entities:    req.Entities,
		Media:       req.Media,
		ReplyTo:     req.ReplyTo,
		Pts:         id,
	}
	res := domain.SendPrivateTextResult{
		SenderMessage: msg,
		SenderEvent: domain.UpdateEvent{
			UserID:   req.SenderUserID,
			Type:     domain.UpdateEventNewMessage,
			Pts:      id,
			PtsCount: 1,
			Date:     req.Date,
			Message:  msg,
		},
	}
	return res, nil
}

func TestMessagesSendSplit_Exactly4096Runes(t *testing.T) {
	sender := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Sender"}
	recipient := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Recipient"}
	messages := &multiCaptureMessages{}
	dialogs := &captureDialogs{}
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{sender.ID: sender, recipient.ID: recipient}}}
	metrics := &captureRPCMetrics{}

	r := New(Config{}, Deps{
		Messages: messages,
		Dialogs:  dialogs,
		Users:    users,
		Metrics:  metrics,
	}, zaptest.NewLogger(t), clock.System)

	text := strings.Repeat("a", 4096)
	req := &tg.MessagesSendMessageRequest{
		Peer:       &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Message:    text,
		RandomID:   111222,
		ClearDraft: true,
	}

	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), sender.ID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	got, ok := enc.(*tg.Updates)
	if !ok {
		t.Fatalf("response = %T, want *tg.Updates", enc)
	}

	if len(messages.requests) != 1 {
		t.Fatalf("messages sent = %d, want 1", len(messages.requests))
	}
	if utf8.RuneCountInString(messages.requests[0].Message) != 4096 {
		t.Fatalf("message rune count = %d, want 4096", utf8.RuneCountInString(messages.requests[0].Message))
	}
	if messages.requests[0].RandomID != req.RandomID {
		t.Fatalf("random id = %d, want %d", messages.requests[0].RandomID, req.RandomID)
	}
	// v2 clears the draft inside the send transaction, so the request carries it.
	if !messages.requests[0].ClearDraft {
		t.Fatalf("single send did not carry clear_draft for peer %d", recipient.ID)
	}
	if len(got.Updates) != 2 {
		t.Fatalf("updates count = %d, want 2 (UpdateMessageID and UpdateNewMessage)", len(got.Updates))
	}
}

func TestMessagesSendSplit_5000Runes(t *testing.T) {
	sender := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Sender"}
	recipient := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Recipient"}
	messages := &multiCaptureMessages{}
	dialogs := &captureDialogs{}
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{sender.ID: sender, recipient.ID: recipient}}}
	metrics := &captureRPCMetrics{}

	r := New(Config{}, Deps{
		Messages: messages,
		Dialogs:  dialogs,
		Users:    users,
		Metrics:  metrics,
	}, zaptest.NewLogger(t), clock.System)

	text := strings.Repeat("x", 5000)
	req := &tg.MessagesSendMessageRequest{
		Peer:       &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Message:    text,
		RandomID:   998877,
		ClearDraft: true,
	}

	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), sender.ID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	got, ok := enc.(*tg.Updates)
	if !ok {
		t.Fatalf("response = %T, want *tg.Updates", enc)
	}

	if len(messages.requests) != 2 {
		t.Fatalf("messages sent = %d, want 2 chunks", len(messages.requests))
	}

	// Chunk 1
	chunk1 := messages.requests[0]
	if utf8.RuneCountInString(chunk1.Message) != 4096 {
		t.Fatalf("chunk 1 rune count = %d, want 4096", utf8.RuneCountInString(chunk1.Message))
	}
	if chunk1.RandomID != req.RandomID {
		t.Fatalf("chunk 1 random id = %d, want client random id %d", chunk1.RandomID, req.RandomID)
	}

	// Chunk 2
	chunk2 := messages.requests[1]
	if utf8.RuneCountInString(chunk2.Message) != 904 {
		t.Fatalf("chunk 2 rune count = %d, want 904", utf8.RuneCountInString(chunk2.Message))
	}
	if chunk2.RandomID == 0 || chunk2.RandomID == req.RandomID {
		t.Fatalf("chunk 2 random id = %d, want fresh non-zero random id", chunk2.RandomID)
	}

	// Only the final chunk clears the draft (inside its send transaction in v2).
	if chunk1.ClearDraft || !chunk2.ClearDraft {
		t.Fatalf("clear_draft = %v/%v, want only the final chunk", chunk1.ClearDraft, chunk2.ClearDraft)
	}

	// Check combined text
	if chunk1.Message+chunk2.Message != text {
		t.Fatalf("recombined text does not match original")
	}

	// Check updates returned to client: should contain 2 UpdateMessageID and 2 UpdateNewMessage
	msgIDs := 0
	newMsgs := 0
	for _, u := range got.Updates {
		switch update := u.(type) {
		case *tg.UpdateMessageID:
			msgIDs++
			if msgIDs == 1 && update.RandomID != req.RandomID {
				t.Fatalf("first UpdateMessageID random_id = %d, want %d", update.RandomID, req.RandomID)
			}
			if msgIDs == 2 && update.RandomID != chunk2.RandomID {
				t.Fatalf("second UpdateMessageID random_id = %d, want %d", update.RandomID, chunk2.RandomID)
			}
		case *tg.UpdateNewMessage:
			newMsgs++
		}
	}
	if msgIDs != 2 || newMsgs != 2 {
		t.Fatalf("got %d UpdateMessageID and %d UpdateNewMessage, want 2 and 2", msgIDs, newMsgs)
	}
}

func TestMessagesSendSplit_10000Runes(t *testing.T) {
	sender := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Sender"}
	recipient := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Recipient"}
	messages := &multiCaptureMessages{}
	dialogs := &captureDialogs{}
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{sender.ID: sender, recipient.ID: recipient}}}
	metrics := &captureRPCMetrics{}

	r := New(Config{}, Deps{
		Messages: messages,
		Dialogs:  dialogs,
		Users:    users,
		Metrics:  metrics,
	}, zaptest.NewLogger(t), clock.System)

	text := strings.Repeat("z", 10000)
	req := &tg.MessagesSendMessageRequest{
		Peer:       &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Message:    text,
		RandomID:   554433,
		ClearDraft: true,
	}

	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), sender.ID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	got, ok := enc.(*tg.Updates)
	if !ok {
		t.Fatalf("response = %T, want *tg.Updates", enc)
	}

	if len(messages.requests) != 3 {
		t.Fatalf("messages sent = %d, want 3 chunks", len(messages.requests))
	}
	if utf8.RuneCountInString(messages.requests[0].Message) != 4096 {
		t.Fatalf("chunk 0 len = %d, want 4096", utf8.RuneCountInString(messages.requests[0].Message))
	}
	if utf8.RuneCountInString(messages.requests[1].Message) != 4096 {
		t.Fatalf("chunk 1 len = %d, want 4096", utf8.RuneCountInString(messages.requests[1].Message))
	}
	if utf8.RuneCountInString(messages.requests[2].Message) != 1808 {
		t.Fatalf("chunk 2 len = %d, want 1808", utf8.RuneCountInString(messages.requests[2].Message))
	}
	if messages.requests[0].RandomID != req.RandomID {
		t.Fatalf("chunk 0 random id = %d, want %d", messages.requests[0].RandomID, req.RandomID)
	}
	if messages.requests[0].ClearDraft || messages.requests[1].ClearDraft || !messages.requests[2].ClearDraft {
		t.Fatalf("final chunk did not carry clear_draft alone")
	}

	// Verify all chunks have distinct non-zero random IDs
	if messages.requests[1].RandomID == 0 || messages.requests[1].RandomID == messages.requests[0].RandomID {
		t.Fatalf("chunk 1 random id not unique: %d", messages.requests[1].RandomID)
	}
	if messages.requests[2].RandomID == 0 || messages.requests[2].RandomID == messages.requests[1].RandomID {
		t.Fatalf("chunk 2 random id not unique: %d", messages.requests[2].RandomID)
	}

	// Verify 3 UpdateMessageID and 3 UpdateNewMessage in updates
	msgIDs := 0
	newMsgs := 0
	for _, u := range got.Updates {
		switch u.(type) {
		case *tg.UpdateMessageID:
			msgIDs++
		case *tg.UpdateNewMessage:
			newMsgs++
		}
	}
	if msgIDs != 3 || newMsgs != 3 {
		t.Fatalf("got %d UpdateMessageID and %d UpdateNewMessage, want 3 and 3", msgIDs, newMsgs)
	}
}

func TestMessagesSendSplit_CyrillicAndEmojis(t *testing.T) {
	sender := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Sender"}
	recipient := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Recipient"}
	messages := &multiCaptureMessages{}
	dialogs := &captureDialogs{}
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{sender.ID: sender, recipient.ID: recipient}}}

	r := New(Config{}, Deps{
		Messages: messages,
		Dialogs:  dialogs,
		Users:    users,
	}, zaptest.NewLogger(t), clock.System)

	// Pattern with Cyrillic characters and emojis: "Привет мир 🚀 " = 13 runes
	unit := "Привет мир 🚀 "
	repeatCount := 500
	text := strings.Repeat(unit, repeatCount) // 6500 runes
	totalRunes := utf8.RuneCountInString(text)
	if totalRunes != 6500 {
		t.Fatalf("total runes = %d, want 6500", totalRunes)
	}

	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Message:  text,
		RandomID: 777888,
	}

	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	_, err := r.Dispatch(WithUserID(context.Background(), sender.ID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if len(messages.requests) != 2 {
		t.Fatalf("messages sent = %d, want 2 chunks", len(messages.requests))
	}
	if utf8.RuneCountInString(messages.requests[0].Message) != 4096 {
		t.Fatalf("chunk 0 runes = %d, want 4096", utf8.RuneCountInString(messages.requests[0].Message))
	}
	if utf8.RuneCountInString(messages.requests[1].Message) != 6500-4096 {
		t.Fatalf("chunk 1 runes = %d, want %d", utf8.RuneCountInString(messages.requests[1].Message), 6500-4096)
	}
	if messages.requests[0].Message+messages.requests[1].Message != text {
		t.Fatalf("recombined Cyrillic/emoji text does not match original")
	}
}

func TestMessagesSendSplit_EntitiesSlicedAcrossChunks(t *testing.T) {
	sender := domain.User{ID: 1000000001, AccessHash: 11, FirstName: "Sender"}
	recipient := domain.User{ID: 1000000002, AccessHash: 22, FirstName: "Recipient"}
	messages := &multiCaptureMessages{}
	dialogs := &captureDialogs{}
	users := &countingMapUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{sender.ID: sender, recipient.ID: recipient}}}

	r := New(Config{}, Deps{
		Messages: messages,
		Dialogs:  dialogs,
		Users:    users,
	}, zaptest.NewLogger(t), clock.System)

	text := strings.Repeat("a", 5000)
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		Message:  text,
		RandomID: 12345,
		Entities: []tg.MessageEntityClass{
			// Entity 1: wholly within chunk 0
			&tg.MessageEntityBold{Offset: 10, Length: 20},
			// Entity 2: crossing the chunk 0 / chunk 1 boundary (4090 to 4110)
			&tg.MessageEntityItalic{Offset: 4090, Length: 20},
			// Entity 3: wholly within chunk 1 (4200 to 4230)
			&tg.MessageEntityCode{Offset: 4200, Length: 30},
		},
	}

	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	_, err := r.Dispatch(WithUserID(context.Background(), sender.ID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch error: %v", err)
	}

	if len(messages.requests) != 2 {
		t.Fatalf("messages sent = %d, want 2 chunks", len(messages.requests))
	}

	// Inspect chunk 0 entities
	chunk0Ents := messages.requests[0].Entities
	if len(chunk0Ents) != 2 {
		t.Fatalf("chunk 0 entity count = %d, want 2 (bold + partial italic)", len(chunk0Ents))
	}
	if chunk0Ents[0].Type != domain.MessageEntityBold || chunk0Ents[0].Offset != 10 || chunk0Ents[0].Length != 20 {
		t.Fatalf("chunk 0 bold entity = %+v, want offset=10 length=20", chunk0Ents[0])
	}
	if chunk0Ents[1].Type != domain.MessageEntityItalic || chunk0Ents[1].Offset != 4090 || chunk0Ents[1].Length != 6 {
		t.Fatalf("chunk 0 sliced italic entity = %+v, want offset=4090 length=6", chunk0Ents[1])
	}

	// Inspect chunk 1 entities
	chunk1Ents := messages.requests[1].Entities
	if len(chunk1Ents) != 2 {
		t.Fatalf("chunk 1 entity count = %d, want 2 (partial italic + code)", len(chunk1Ents))
	}
	if chunk1Ents[0].Type != domain.MessageEntityItalic || chunk1Ents[0].Offset != 0 || chunk1Ents[0].Length != 14 {
		t.Fatalf("chunk 1 sliced italic entity = %+v, want offset=0 length=14", chunk1Ents[0])
	}
	if chunk1Ents[1].Type != domain.MessageEntityCode || chunk1Ents[1].Offset != 4200-4096 || chunk1Ents[1].Length != 30 {
		t.Fatalf("chunk 1 code entity = %+v, want offset=%d length=30", chunk1Ents[1], 4200-4096)
	}
}
