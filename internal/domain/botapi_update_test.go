package domain

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestBotAPIEphemeralPayloadCannotSerializePrivateRoutingState(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reply := EphemeralMessage{
		ID: 16, Peer: Peer{Type: PeerTypeChannel, ID: 1001},
		SenderUserID: 3001, ReceiverUserID: 2001, Date: int(now.Unix()) - 1,
		Content: EphemeralContent{Message: "prompt"}, Version: 1, ExpiresAt: now.Add(EphemeralMessageRetention),
	}
	payload := NewBotAPIEphemeralPayload(EphemeralMessage{
		ID: 17, Peer: Peer{Type: PeerTypeChannel, ID: 1001},
		SenderUserID: 2001, ReceiverUserID: 3001, Date: int(now.Unix()),
		RandomID: 99, ReplyToEphemeralID: reply.ID, Content: EphemeralContent{Message: "private"},
		OriginDevice: EphemeralDevice{UserID: 2001, BusinessAuthKeyID: [8]byte{1, 2, 3}, SessionID: 44},
		PayloadHash:  [32]byte{5, 6, 7}, Version: 1,
		CreatedAt: now, ExpiresAt: now.Add(EphemeralMessageRetention), BotAPIReply: &reply,
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, privateField := range [][]byte{
		[]byte("RandomID"), []byte("OriginDevice"), []byte("BusinessAuthKeyID"),
		[]byte("SessionID"), []byte("PayloadHash"), []byte("CreatedAt"),
	} {
		if bytes.Contains(raw, privateField) {
			t.Fatalf("durable Bot API envelope leaked %s: %s", privateField, raw)
		}
	}
	if payload.Validate() != nil || payload.Message.ID != 17 || payload.Message.Content.Message != "private" || payload.Message.ExpiresAt.IsZero() ||
		payload.ReplyTo == nil || payload.ReplyTo.ID != reply.ID {
		t.Fatalf("public payload=%+v", payload)
	}
}
