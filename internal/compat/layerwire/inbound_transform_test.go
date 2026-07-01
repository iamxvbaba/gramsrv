package layerwire

import (
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// validateMethodRequest asserts that buf holds a single canonical (227) method
// request: its leading id equals wantCRC and the canonical walker consumes every
// byte (proving the rebuilt body matches the 227 layout).
func validateMethodRequest(t *testing.T, buf *bin.Buffer, wantCRC uint32, label string) {
	t.Helper()
	id, err := (&bin.Buffer{Buf: buf.Buf}).PeekID()
	if err != nil {
		t.Fatalf("%s: peek id: %v", label, err)
	}
	if id != wantCRC {
		t.Fatalf("%s: id = %#08x, want %#08x", label, id, wantCRC)
	}
	probe := &bin.Buffer{Buf: append([]byte(nil), buf.Buf...)}
	if err := canonical.skipObject(probe); err != nil {
		t.Fatalf("%s: result not a valid 227 request: %v", label, err)
	}
	if probe.Len() != 0 {
		t.Fatalf("%s: %d trailing bytes in rebuilt request", label, probe.Len())
	}
}

func TestInboundBodyTransforms(t *testing.T) {
	// uploadMedia: peer + media -> flags + peer + media.
	t.Run("uploadMedia", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x519bc2b1)
		_ = (&tg.InputPeerSelf{}).Encode(&in)
		_ = (&tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 10, Parts: 1, Name: "a.jpg"}}).Encode(&in)
		out, ok, err := UpgradeInbound(0x519bc2b1, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0x14967978, "uploadMedia")
	})

	t.Run("authSignUp", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x80eee427)
		in.PutString("+15550000000")
		in.PutString("hash")
		in.PutString("First")
		in.PutString("Last")
		out, ok, err := UpgradeInbound(0x80eee427, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0xaac7b717, "authSignUp")
	})

	t.Run("channelsGetMessages", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x93d7b347)
		_ = (&tg.InputChannel{ChannelID: 4, AccessHash: 5}).Encode(&in)
		in.PutVectorHeader(2)
		in.PutInt(11)
		in.PutInt(12)
		out, ok, err := UpgradeInbound(0x93d7b347, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0xad8c9a23, "channelsGetMessages")
	})

	t.Run("messagesGetMessages", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x4222fa74)
		in.PutVectorHeader(2)
		in.PutInt(21)
		in.PutInt(22)
		out, ok, err := UpgradeInbound(0x4222fa74, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, tg.MessagesGetMessagesRequestTypeID, "messagesGetMessages")
		var req tg.MessagesGetMessagesRequest
		if err := req.Decode(&bin.Buffer{Buf: append([]byte(nil), out.Buf...)}); err != nil {
			t.Fatalf("decode upgraded messages.getMessages: %v", err)
		}
		if len(req.ID) != 2 {
			t.Fatalf("upgraded ids = %d, want 2", len(req.ID))
		}
		first, ok := req.ID[0].(*tg.InputMessageID)
		if !ok || first.ID != 21 {
			t.Fatalf("upgraded id[0] = %T %+v, want inputMessageID(21)", req.ID[0], req.ID[0])
		}
	})

	t.Run("botsExportBotToken", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x0063b089)
		in.PutLong(777)
		in.PutID(0x997275b5) // boolTrue (revoke)
		out, ok, err := UpgradeInbound(0x0063b089, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0xbd0d99eb, "botsExportBotToken")
	})

	t.Run("accountRegisterDevice", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x637ea878)
		in.PutInt(2)
		in.PutString("token-blob")
		out, ok, err := UpgradeInbound(0x637ea878, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0xec86017a, "accountRegisterDevice")
	})

	t.Run("contactsSearch", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x11f812d8)
		in.PutString("ngame")
		in.PutInt(20)
		out, ok, err := UpgradeInbound(0x11f812d8, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, tg.ContactsSearchRequestTypeID, "contactsSearch")
		var req tg.ContactsSearchRequest
		if err := req.Decode(&bin.Buffer{Buf: append([]byte(nil), out.Buf...)}); err != nil {
			t.Fatalf("decode upgraded contacts.search: %v", err)
		}
		if req.Flags != 0 || req.Q != "ngame" || req.Limit != 20 {
			t.Fatalf("upgraded contacts.search = flags:%#x q:%q limit:%d", req.Flags, req.Q, req.Limit)
		}
	})

	t.Run("langpackGetLangPack", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x9ab5c58e)
		in.PutString("en")
		out, ok, err := UpgradeInbound(0x9ab5c58e, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0xf2f2330a, "langpackGetLangPack")
	})

	t.Run("langpackGetStrings", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x2e1ee318)
		in.PutString("en")
		in.PutVectorHeader(2)
		in.PutString("key1")
		in.PutString("key2")
		out, ok, err := UpgradeInbound(0x2e1ee318, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0xefea3803, "langpackGetStrings")
	})

	t.Run("langpackGetLanguages", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x800fd57d)
		out, ok, err := UpgradeInbound(0x800fd57d, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0x42c6978f, "langpackGetLanguages")
	})
}

// TestInboundCRCSwaps covers the body-compatible client-drift methods that only
// need a 4-byte id swap.
func TestInboundCRCSwaps(t *testing.T) {
	t.Run("updatesGetDifference", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x25939651)
		in.PutUint32(0) // flags (no pts_total_limit)
		in.PutInt(100)  // pts
		in.PutInt(200)  // date
		in.PutInt(0)    // qts
		out, ok, err := UpgradeInbound(0x25939651, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0x19c2f763, "updatesGetDifference")
	})

	t.Run("createChat", func(t *testing.T) {
		var in bin.Buffer
		in.PutID(0x0034a818)
		if err := (&tg.MessagesCreateChatRequest{
			Users: []tg.InputUserClass{&tg.InputUser{UserID: 2, AccessHash: 3}},
			Title: "Group",
		}).EncodeBare(&in); err != nil {
			t.Fatalf("encode createChat body: %v", err)
		}
		out, ok, err := UpgradeInbound(0x0034a818, &in)
		if !ok || err != nil {
			t.Fatalf("upgrade: ok=%v err=%v", ok, err)
		}
		validateMethodRequest(t, out, 0x92ceddd4, "createChat")
	})
}
