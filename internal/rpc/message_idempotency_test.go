package rpc

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func TestRPCRequestFingerprintStableAndPayloadSensitive(t *testing.T) {
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: 1002, AccessHash: 22},
		Message:  "hello",
		RandomID: 991,
		Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 5}},
	}

	first, err := rpcRequestFingerprint(req)
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	second, err := rpcRequestFingerprint(req)
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if len(first) != 32 || !bytes.Equal(first, second) {
		t.Fatalf("fingerprints = %x / %x, want stable SHA-256", first, second)
	}

	req.Message = "changed"
	changed, err := rpcRequestFingerprint(req)
	if err != nil {
		t.Fatalf("changed fingerprint: %v", err)
	}
	if bytes.Equal(first, changed) {
		t.Fatalf("changed payload fingerprint = %x, want different from %x", changed, first)
	}
}

func TestSendMessageFingerprintIgnoresRetryOnlyHints(t *testing.T) {
	first := &tg.MessagesSendMessageRequest{
		Peer:                   &tg.InputPeerUser{UserID: 1002, AccessHash: 22},
		Message:                "hello",
		RandomID:               991,
		ClearDraft:             true,
		Background:             true,
		UpdateStickersetsOrder: true,
	}
	retry := *first
	retry.Flags.Set(31) // stale decoded flags must not leak into the canonical intent.
	retry.ClearDraft = false
	retry.Background = false
	retry.UpdateStickersetsOrder = false

	a, err := sendMessageIdempotencyFingerprint(first)
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	b, err := sendMessageIdempotencyFingerprint(&retry)
	if err != nil {
		t.Fatalf("retry fingerprint: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("retry-only flags changed fingerprint: %x != %x", a, b)
	}

	retry.Message = "different"
	c, err := sendMessageIdempotencyFingerprint(&retry)
	if err != nil {
		t.Fatalf("changed fingerprint: %v", err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("durable message change did not change fingerprint")
	}
}

func TestSendMultiMediaFingerprintIsPerItemAndSubsetStable(t *testing.T) {
	item1 := tg.InputSingleMedia{Media: &tg.InputMediaEmpty{}, RandomID: 101, Message: "one"}
	item2 := tg.InputSingleMedia{Media: &tg.InputMediaEmpty{}, RandomID: 102, Message: "two"}
	full := &tg.MessagesSendMultiMediaRequest{
		Peer:       &tg.InputPeerUser{UserID: 1002, AccessHash: 22},
		ClearDraft: true,
		Background: true,
		MultiMedia: []tg.InputSingleMedia{item1, item2},
	}
	subset := &tg.MessagesSendMultiMediaRequest{
		Peer:       full.Peer,
		MultiMedia: []tg.InputSingleMedia{item2},
	}

	fromFull, err := sendMultiMediaItemIdempotencyFingerprint(full, item2)
	if err != nil {
		t.Fatalf("full item fingerprint: %v", err)
	}
	fromSubset, err := sendMultiMediaItemIdempotencyFingerprint(subset, item2)
	if err != nil {
		t.Fatalf("subset item fingerprint: %v", err)
	}
	if !bytes.Equal(fromFull, fromSubset) {
		t.Fatalf("subset retry fingerprint = %x, want %x", fromSubset, fromFull)
	}

	changed := item2
	changed.Message = "changed"
	different, err := sendMultiMediaItemIdempotencyFingerprint(subset, changed)
	if err != nil {
		t.Fatalf("changed item fingerprint: %v", err)
	}
	if bytes.Equal(fromFull, different) {
		t.Fatal("changed album item reused the original fingerprint")
	}
}

func TestForwardFingerprintIsPerItemAndSubsetStable(t *testing.T) {
	full := &tg.MessagesForwardMessagesRequest{
		FromPeer:   &tg.InputPeerUser{UserID: 1002, AccessHash: 22},
		ID:         []int{41, 42},
		RandomID:   []int64{201, 202},
		ToPeer:     &tg.InputPeerUser{UserID: 1003, AccessHash: 33},
		Background: true,
	}
	subset := *full
	subset.Flags = 0
	subset.Background = false
	subset.ID = []int{42}
	subset.RandomID = []int64{202}

	fromFull, err := forwardMessagesItemIdempotencyFingerprint(full, 42, 202)
	if err != nil {
		t.Fatalf("full item fingerprint: %v", err)
	}
	fromSubset, err := forwardMessagesItemIdempotencyFingerprint(&subset, 42, 202)
	if err != nil {
		t.Fatalf("subset item fingerprint: %v", err)
	}
	if !bytes.Equal(fromFull, fromSubset) {
		t.Fatalf("subset retry fingerprint = %x, want %x", fromSubset, fromFull)
	}

	different, err := forwardMessagesItemIdempotencyFingerprint(&subset, 41, 202)
	if err != nil {
		t.Fatalf("changed source fingerprint: %v", err)
	}
	if bytes.Equal(fromFull, different) {
		t.Fatal("different source message reused the original fingerprint")
	}
}

func TestMessageSendErrMapsRandomIDConflict(t *testing.T) {
	err := messageSendErr(fmt.Errorf("wrapped store error: %w", domain.ErrMessageRandomIDDuplicate))
	if !tgerr.Is(err, "RANDOM_ID_DUPLICATE") || !tgerr.IsCode(err, 500) {
		t.Fatalf("messageSendErr = %v, want 500 RANDOM_ID_DUPLICATE", err)
	}
}

func TestMessageForwardErrMapsRandomIDConflict(t *testing.T) {
	err := messageForwardErr(fmt.Errorf("wrapped store error: %w", domain.ErrMessageRandomIDDuplicate))
	if !tgerr.Is(err, "RANDOM_ID_DUPLICATE") || !tgerr.IsCode(err, 500) {
		t.Fatalf("messageForwardErr = %v, want 500 RANDOM_ID_DUPLICATE", err)
	}
}

func TestPrivateSendDuplicateResponseIncludesAndroidConfirmationSnapshot(t *testing.T) {
	res := domain.SendPrivateTextResult{
		Duplicate: true,
		SenderMessage: domain.Message{
			ID: 41, UID: 51, RandomID: 9911, OwnerUserID: 1001,
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
			From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
			Date: 1700000000, Out: true, Body: "confirmed", Pts: 7,
		},
		SenderEvent: domain.UpdateEvent{Pts: 7, PtsCount: 1, Date: 1700000000},
	}
	updates := tgPrivateSendResultUpdates(res, 9911, true, nil, nil)
	if len(updates.Updates) != 2 {
		t.Fatalf("duplicate updates = %#v, want mapping + new message", updates.Updates)
	}
	mapping, ok := updates.Updates[0].(*tg.UpdateMessageID)
	if !ok || mapping.ID != 41 || mapping.RandomID != 9911 {
		t.Fatalf("duplicate mapping = %#v, want id/random_id 41/9911", updates.Updates[0])
	}
	confirmed, ok := updates.Updates[1].(*tg.UpdateNewMessage)
	if !ok || confirmed.Pts != 7 || confirmed.PtsCount != 1 {
		t.Fatalf("duplicate confirmation = %#v, want UpdateNewMessage pts 7/1", updates.Updates[1])
	}
	msg, ok := confirmed.Message.(*tg.Message)
	if !ok || msg.ID != 41 || msg.Message != "confirmed" {
		t.Fatalf("duplicate confirmation message = %#v, want sender snapshot", confirmed.Message)
	}
}

func TestPrivateSendDeletedDuplicateConfirmsThenConvergesWithDurableDelete(t *testing.T) {
	deleteEvent := domain.UpdateEvent{UserID: 1001, Type: domain.UpdateEventDeleteMessages, Pts: 9, PtsCount: 1, Date: 1700000002, MessageIDs: []int{41}}
	res := domain.SendPrivateTextResult{
		Duplicate: true,
		SenderMessage: domain.Message{
			ID: 41, UID: 51, RandomID: 9911, OwnerUserID: 1001,
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
			From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
			Date: 1700000000, Out: true, Body: "original", Pts: 7,
		},
		SenderEvent:       domain.UpdateEvent{Pts: 7, PtsCount: 1, Date: 1700000000},
		ReplayDeleteEvent: &deleteEvent,
	}
	updates := tgPrivateSendResultUpdates(res, 9911, true, nil, nil)
	if len(updates.Updates) != 3 {
		t.Fatalf("deleted duplicate updates = %#v, want mapping + new + delete", updates.Updates)
	}
	if _, ok := updates.Updates[1].(*tg.UpdateNewMessage); !ok {
		t.Fatalf("deleted duplicate confirmation = %#v, want UpdateNewMessage", updates.Updates[1])
	}
	deleted, ok := updates.Updates[2].(*tg.UpdateDeleteMessages)
	if !ok || deleted.Pts != 9 || deleted.PtsCount != 1 || len(deleted.Messages) != 1 || deleted.Messages[0] != 41 {
		t.Fatalf("deleted duplicate convergence = %#v, want real delete event", updates.Updates[2])
	}
}

func TestForwardDeletedDuplicateIncludesMessageAndDelete(t *testing.T) {
	deleteEvent := &domain.UpdateEvent{UserID: 1001, Type: domain.UpdateEventDeleteMessages, Pts: 12, PtsCount: 1, Date: 1700000012, MessageIDs: []int{61}}
	updates := tgForwardMessagesUpdates(domain.ForwardPrivateMessagesResult{
		SenderMessages: []domain.Message{{
			ID: 61, UID: 71, RandomID: 8811, OwnerUserID: 1001,
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
			From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
			Date: 1700000010, Out: true, Body: "forwarded", Pts: 10,
		}},
		SenderEvents:       []domain.UpdateEvent{{Pts: 10, PtsCount: 1, Date: 1700000010}},
		Duplicates:         []bool{true},
		ReplayDeleteEvents: []*domain.UpdateEvent{deleteEvent},
	}, []int64{8811}, nil, nil)
	if len(updates.Updates) != 3 {
		t.Fatalf("forward duplicate updates = %#v, want mapping + new + delete", updates.Updates)
	}
	if _, ok := updates.Updates[1].(*tg.UpdateNewMessage); !ok {
		t.Fatalf("forward duplicate confirmation = %#v, want UpdateNewMessage", updates.Updates[1])
	}
	if deleted, ok := updates.Updates[2].(*tg.UpdateDeleteMessages); !ok || len(deleted.Messages) != 1 || deleted.Messages[0] != 61 || deleted.Pts != 12 {
		t.Fatalf("forward duplicate delete = %#v, want durable delete", updates.Updates[2])
	}
}

func TestChannelDeletedDuplicateEchoIncludesMessageAndDelete(t *testing.T) {
	router := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	deleteEvent := &domain.ChannelUpdateEvent{ChannelID: 2001, Type: domain.ChannelUpdateDeleteMessages, Pts: 12, PtsCount: 1, Date: 1700000012, MessageIDs: []int{61}}
	msg := domain.ChannelMessage{
		ChannelID: 2001, ID: 61, RandomID: 8811, SenderUserID: 1001,
		From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
		Date: 1700000010, Body: "channel", Pts: 10,
	}
	updates := router.channelMessagesUpdatesWithPeerCache(context.Background(), 1001, []domain.SendChannelMessageResult{{
		Channel: domain.Channel{ID: 2001, AccessHash: 22, Title: "group", Megagroup: true, Date: 1700000000},
		Message: msg,
		Event: domain.ChannelUpdateEvent{
			ChannelID: 2001, Type: domain.ChannelUpdateNewMessage, Pts: 10, PtsCount: 1, Date: 1700000010, Message: msg,
		},
		Duplicate:         true,
		ReplayDeleteEvent: deleteEvent,
	}}, []int64{8811}, true, nil, newViewerPeerCache(router))
	if len(updates.Updates) != 3 {
		t.Fatalf("channel duplicate updates = %#v, want mapping + new + delete", updates.Updates)
	}
	if _, ok := updates.Updates[1].(*tg.UpdateNewChannelMessage); !ok {
		t.Fatalf("channel duplicate confirmation = %#v, want UpdateNewChannelMessage", updates.Updates[1])
	}
	if deleted, ok := updates.Updates[2].(*tg.UpdateDeleteChannelMessages); !ok || deleted.ChannelID != 2001 || len(deleted.Messages) != 1 || deleted.Messages[0] != 61 || deleted.Pts != 12 {
		t.Fatalf("channel duplicate delete = %#v, want durable channel delete", updates.Updates[2])
	}
}

func TestPrivateSendRecordsRawOriginAuthKey(t *testing.T) {
	const (
		senderID    = int64(1001)
		recipientID = int64(1002)
	)
	raw := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	business := [8]byte{8, 7, 6, 5, 4, 3, 2, 1}
	messages := &captureMessages{}
	router := New(Config{}, Deps{
		Messages: messages,
		Users: mapUsersService{users: map[int64]domain.User{
			senderID:    {ID: senderID, FirstName: "Sender"},
			recipientID: {ID: recipientID, FirstName: "Recipient"},
		}},
	}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(
		WithRawAuthKeyID(
			WithAuthKeyID(
				WithUserID(context.Background(), senderID),
				business,
			),
			raw,
		),
		77,
	)
	if _, err := router.onMessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: recipientID},
		Message:  "raw origin",
		RandomID: 992,
	}); err != nil {
		t.Fatalf("send message: %v", err)
	}
	if messages.sendReq.OriginAuthKeyID != raw || messages.sendReq.OriginAuthKeyID == business {
		t.Fatalf("origin auth key = %x, want raw %x (business %x)", messages.sendReq.OriginAuthKeyID, raw, business)
	}
	if messages.sendReq.OriginSessionID != 77 {
		t.Fatalf("origin session = %d, want 77", messages.sendReq.OriginSessionID)
	}
}
