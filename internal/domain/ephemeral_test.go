package domain

import (
	"errors"
	"testing"
	"time"
)

func TestValidateEphemeralContentBoundsAllRetainedVectors(t *testing.T) {
	valid := EphemeralContent{
		Message:  "hi 👋",
		Entities: []MessageEntity{{Type: MessageEntityBold, Offset: 0, Length: 2}},
		ReplyMarkup: &MessageReplyMarkup{Type: MessageReplyMarkupInline, Inline: [][]MarkupButton{{{
			Type: MarkupButtonCallback, Text: "OK", Data: []byte("ok"),
		}}}},
	}
	if err := ValidateEphemeralContent(valid); err != nil {
		t.Fatalf("valid content: %v", err)
	}

	badBounds := valid
	badBounds.Entities = []MessageEntity{{Type: MessageEntityBold, Offset: 5, Length: 2}}
	if err := ValidateEphemeralContent(badBounds); !errors.Is(err, ErrEphemeralInvalid) {
		t.Fatalf("entity bounds err=%v", err)
	}
	badKeyboard := valid
	badKeyboard.ReplyMarkup = &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{{
		Type: MarkupButtonText, Text: "public keyboard",
	}}}}
	if err := ValidateEphemeralContent(badKeyboard); !errors.Is(err, ErrEphemeralInvalid) {
		t.Fatalf("reply keyboard err=%v", err)
	}
	badRich := EphemeralContent{RichMessage: &MessageRichMessage{Blocks: make([]byte, MaxEphemeralRichBlocksBytes+1)}}
	if err := ValidateEphemeralContent(badRich); !errors.Is(err, ErrEphemeralInvalid) {
		t.Fatalf("rich bound err=%v", err)
	}
	badMedia := EphemeralContent{Media: &MessageMedia{Kind: MessageMediaKindPhoto}}
	if err := ValidateEphemeralContent(badMedia); !errors.Is(err, ErrEphemeralInvalid) {
		t.Fatalf("media shape err=%v", err)
	}
}

func TestEphemeralStoredStateRejectsPartialDeviceAndInvalidTombstone(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	message := EphemeralMessage{
		ID: 17, Peer: Peer{Type: PeerTypeChannel, ID: 1001},
		SenderUserID: 2001, ReceiverUserID: 3001, Date: int(now.Unix()), RandomID: 9,
		Content: EphemeralContent{Message: "private"}, OriginDevice: EphemeralDevice{UserID: 3001},
		PayloadHash: [32]byte{1}, Version: 1, CreatedAt: now, ExpiresAt: now.Add(EphemeralMessageRetention),
	}
	if err := message.ValidateStored(); !errors.Is(err, ErrEphemeralInvalid) {
		t.Fatalf("partial device err=%v", err)
	}
	message.OriginDevice = EphemeralDevice{}
	message.Deleted = true
	message.Content = EphemeralContent{}
	if err := message.ValidateStored(); !errors.Is(err, ErrEphemeralInvalid) {
		t.Fatalf("version-one tombstone err=%v", err)
	}
	message.Version = 2
	if err := message.ValidateStored(); err != nil {
		t.Fatalf("valid tombstone err=%v", err)
	}
}
