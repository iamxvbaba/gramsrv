package postgres

import (
	"bytes"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestPrivateSendRequestFingerprintUsesImmutableIntent(t *testing.T) {
	req := domain.SendPrivateTextRequest{
		SenderUserID:     1001,
		RecipientUserID:  1002,
		RandomID:         99,
		Message:          "hello",
		Silent:           true,
		Date:             1700000000,
		OriginSessionID:  7,
		RecipientBlocked: true,
	}
	fingerprint := func(in domain.SendPrivateTextRequest) []byte {
		t.Helper()
		got, err := store.PrivateSendFingerprint(in)
		if err != nil {
			t.Fatalf("privateSendRequestFingerprint: %v", err)
		}
		return got
	}

	first := fingerprint(req)
	replay := req
	replay.Date++
	replay.OriginSessionID++
	replay.OriginAuthKeyID[0] = 9
	replay.RecipientBlocked = false
	if got := fingerprint(replay); !bytes.Equal(first, got) {
		t.Fatalf("execution-context-only changes altered fingerprint: %x != %x", got, first)
	}

	changedPeer := req
	changedPeer.RecipientUserID++
	if got := fingerprint(changedPeer); bytes.Equal(first, got) {
		t.Fatal("changed recipient retained fingerprint")
	}
	changedBody := req
	changedBody.Message = "different"
	if got := fingerprint(changedBody); bytes.Equal(first, got) {
		t.Fatal("changed body retained fingerprint")
	}
	changedMedia := req
	changedMedia.Media = &domain.MessageMedia{
		Kind: domain.MessageMediaKindContact,
		Contact: &domain.MessageContact{
			PhoneNumber: "+10000000000",
			FirstName:   "Changed",
		},
	}
	if got := fingerprint(changedMedia); bytes.Equal(first, got) {
		t.Fatal("changed media retained fingerprint")
	}
}

func TestPrivateSendRequestFingerprintPrefersRPCFingerprint(t *testing.T) {
	want := bytes.Repeat([]byte{0x5a}, 32)
	req := domain.SendPrivateTextRequest{IdempotencyFingerprint: want}
	got, err := store.PrivateSendFingerprint(req)
	if err != nil {
		t.Fatalf("privateSendRequestFingerprint: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("fingerprint = %x, want %x", got, want)
	}
	got[0] ^= 0xff
	if got[0] == want[0] {
		t.Fatal("returned fingerprint aliases caller storage")
	}

	req.IdempotencyFingerprint = []byte{1, 2, 3}
	if _, err := store.PrivateSendFingerprint(req); err == nil {
		t.Fatal("short caller fingerprint accepted")
	}
}
