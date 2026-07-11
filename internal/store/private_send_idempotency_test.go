package store

import (
	"testing"

	"telesrv/internal/domain"
)

func TestPrivateSendSnapshotIsDeepAndVersioned(t *testing.T) {
	message := domain.Message{
		ID: 7, UID: 8, RandomID: 9, OwnerUserID: 10,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 11},
		From: domain.Peer{Type: domain.PeerTypeUser, ID: 10},
		Date: 12, Out: true, Body: "first", Pts: 13,
		Entities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Length: 5}},
		Media:    &domain.MessageMedia{Kind: domain.MessageMediaKindContact, Contact: &domain.MessageContact{PhoneNumber: "+1", FirstName: "Alice"}},
	}
	raw, err := EncodePrivateSendSnapshot(message)
	if err != nil {
		t.Fatalf("encode private snapshot: %v", err)
	}
	message.Body = "mutated"
	message.Entities[0].Length = 1
	message.Media.Contact.FirstName = "Mutated"
	decoded, err := DecodePrivateSendSnapshot(raw)
	if err != nil {
		t.Fatalf("decode private snapshot: %v", err)
	}
	if decoded.Body != "first" || decoded.Entities[0].Length != 5 || decoded.Media.Contact.FirstName != "Alice" {
		t.Fatalf("decoded private snapshot = %+v, want immutable nested graph", decoded)
	}
	decoded.Media.Contact.FirstName = "Second mutation"
	again, err := DecodePrivateSendSnapshot(raw)
	if err != nil || again.Media.Contact.FirstName != "Alice" {
		t.Fatalf("second decode = %+v err=%v, want fresh graph", again, err)
	}
}

func TestChannelSendSnapshotRejectsEmptyLegacyValue(t *testing.T) {
	if _, err := DecodeChannelSendSnapshot([]byte(`{}`)); err == nil {
		t.Fatal("empty legacy channel snapshot decoded successfully")
	}
	message := domain.ChannelMessage{
		ChannelID: 21, ID: 22, RandomID: 23, SenderUserID: 24,
		From: domain.Peer{Type: domain.PeerTypeUser, ID: 24},
		Date: 25, Body: "channel", Pts: 26,
	}
	raw, err := EncodeChannelSendSnapshot(message)
	if err != nil {
		t.Fatalf("encode channel snapshot: %v", err)
	}
	decoded, err := DecodeChannelSendSnapshot(raw)
	if err != nil || decoded.ChannelID != message.ChannelID || decoded.ID != message.ID || decoded.RandomID != message.RandomID || decoded.SenderUserID != message.SenderUserID || decoded.Body != message.Body || decoded.Pts != message.Pts {
		t.Fatalf("decoded channel snapshot = %+v err=%v, want %+v", decoded, err, message)
	}
}
