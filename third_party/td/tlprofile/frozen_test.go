package tlprofile

import (
	"bytes"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestFrozenObjectOwnsCanonicalSnapshotAndEncodesExactProfiles(t *testing.T) {
	value := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: 1, Seq: 2}
	frozen, err := FreezeObject(value)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.CanonicalSize() <= 0 {
		t.Fatal("frozen canonical size is empty")
	}
	value.Date = 99
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		var first, second bin.Buffer
		if err := frozen.Encode(profile, &first); err != nil {
			t.Fatal(err)
		}
		if err := frozen.Encode(profile, &second); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Raw(), second.Raw()) {
			t.Fatalf("profile %d frozen encode changed", profile)
		}
		decoded, err := DecodeObject(profile, &bin.Buffer{Buf: first.Copy()}, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		updates, ok := decoded.(*tg.Updates)
		if !ok || updates.Date != 1 {
			t.Fatalf("profile %d snapshot = %#v", profile, decoded)
		}
	}
}

func TestFrozenObjectOwnsByteFields(t *testing.T) {
	value := &tg.UpdateBotCallbackQuery{
		QueryID: 1,
		UserID:  2,
		Peer:    &tg.PeerUser{UserID: 2},
		MsgID:   3,
	}
	value.SetData([]byte("before"))
	frozen, err := FreezeObject(value)
	if err != nil {
		t.Fatal(err)
	}
	value.Data[0] = 'x'

	var encoded bin.Buffer
	if err := frozen.Encode(ProfileCanonical, &encoded); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObject(ProfileCanonical, &encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	callback, ok := decoded.(*tg.UpdateBotCallbackQuery)
	if !ok || !bytes.Equal(callback.Data, []byte("before")) {
		t.Fatalf("frozen callback data = %#v", decoded)
	}
}

func BenchmarkFreezeObjectUpdates64KiB(b *testing.B) {
	value := &tg.UpdateShortMessage{
		ID:       1,
		UserID:   2,
		Message:  string(bytes.Repeat([]byte{'x'}, 64<<10)),
		Pts:      3,
		PtsCount: 1,
		Date:     4,
	}
	b.Run("owned_buffer", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(64 << 10)
		for range b.N {
			if _, err := FreezeObject(value); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("defensive_copy_baseline", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(64 << 10)
		for range b.N {
			var canonical bin.Buffer
			if err := value.Encode(&canonical); err != nil {
				b.Fatal(err)
			}
			cursor := &bin.Buffer{Buf: canonical.Copy()}
			if _, err := DecodeObject(ProfileCanonical, cursor, Limits{}); err != nil {
				b.Fatal(err)
			}
			if cursor.Len() != 0 {
				b.Fatalf("left %d canonical bytes", cursor.Len())
			}
		}
	})
}
