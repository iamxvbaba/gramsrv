package rpc

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestBusinessAuthKeyInt64 pins [8]byte->int64 to the same source as
// store/postgres authKeyIDToInt64 (little-endian, MTProto is the low 64 bits of
// SHA1). Drift between the two would make the secret chat binding key and
// update_states/authorizations not line up.
func TestBusinessAuthKeyInt64(t *testing.T) {
	cases := [][8]byte{
		{1, 0, 0, 0, 0, 0, 0, 0},
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04},
	}
	for _, id := range cases {
		want := int64(binary.LittleEndian.Uint64(id[:]))
		if got := businessAuthKeyInt64(id); got != want {
			t.Fatalf("businessAuthKeyInt64(%v) = %d, want %d (little-endian, same as authKeyIDToInt64)", id, got, want)
		}
	}
}

func baseChat() domain.SecretChat {
	return domain.SecretChat{
		ID:                    7,
		AdminUserID:           1,
		ParticipantUserID:     2,
		AdminAccessHash:       111,
		ParticipantAccessHash: 222,
		GA:                    []byte{0x0a, 0x0b},
		GB:                    []byte{0x0c, 0x0d},
		KeyFingerprint:        99,
		Date:                  1000,
	}
}

func TestEncryptedChatForViewerRequested(t *testing.T) {
	chat := baseChat()
	chat.State = domain.SecretChatStateRequested

	// admin viewpoint: encryptedChatWaiting (no g_a), access_hash=admin.
	adminView, ok := tgEncryptedChatForViewer(chat, 1).(*tg.EncryptedChatWaiting)
	if !ok {
		t.Fatalf("admin view type = %T, want *tg.EncryptedChatWaiting", tgEncryptedChatForViewer(chat, 1))
	}
	if adminView.ID != 7 || adminView.AccessHash != 111 || adminView.AdminID != 1 || adminView.ParticipantID != 2 {
		t.Fatalf("admin waiting view = %+v", adminView)
	}

	// participant viewpoint: encryptedChatRequested (carries g_a), access_hash=participant.
	partView, ok := tgEncryptedChatForViewer(chat, 2).(*tg.EncryptedChatRequested)
	if !ok {
		t.Fatalf("participant view type = %T, want *tg.EncryptedChatRequested", tgEncryptedChatForViewer(chat, 2))
	}
	if partView.AccessHash != 222 || !bytes.Equal(partView.GA, chat.GA) {
		t.Fatalf("participant requested view = %+v", partView)
	}
}

func TestEncryptedChatForViewerNormal(t *testing.T) {
	chat := baseChat()
	chat.State = domain.SecretChatStateNormal

	// admin viewpoint: GAOrB = g_b.
	adminView, ok := tgEncryptedChatForViewer(chat, 1).(*tg.EncryptedChat)
	if !ok {
		t.Fatalf("admin view type = %T, want *tg.EncryptedChat", tgEncryptedChatForViewer(chat, 1))
	}
	if adminView.AccessHash != 111 || !bytes.Equal(adminView.GAOrB, chat.GB) || adminView.KeyFingerprint != 99 {
		t.Fatalf("admin normal view = %+v (GAOrB must be g_b)", adminView)
	}

	// participant viewpoint: GAOrB = g_a.
	partView := tgEncryptedChatForViewer(chat, 2).(*tg.EncryptedChat)
	if partView.AccessHash != 222 || !bytes.Equal(partView.GAOrB, chat.GA) {
		t.Fatalf("participant normal view = %+v (GAOrB must be g_a)", partView)
	}
}

func TestEncryptedChatForViewerDiscarded(t *testing.T) {
	chat := baseChat()
	chat.State = domain.SecretChatStateDiscarded
	chat.HistoryDeleted = true
	view, ok := tgEncryptedChatForViewer(chat, 1).(*tg.EncryptedChatDiscarded)
	if !ok {
		t.Fatalf("discarded view type = %T", tgEncryptedChatForViewer(chat, 1))
	}
	if view.ID != 7 || !view.HistoryDeleted {
		t.Fatalf("discarded view = %+v", view)
	}
}
