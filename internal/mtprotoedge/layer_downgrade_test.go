package mtprotoedge

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
)

// TestConnDowngradedClone verifies the outbound seam downgrades a canonical
// (227) object to the connection's negotiated layer, is a no-op for 227, and
// — critically for push fan-out — never mutates the shared input message (one
// pre-encoded update is reused across many connections of differing layers).
func TestConnDowngradedClone(t *testing.T) {
	const (
		message227CRC = 0x7600b9d3
		message220CRC = 0xb92f76cf
	)
	msg := &tg.Message{
		ID:      2,
		FromID:  &tg.PeerUser{UserID: 3},
		PeerID:  &tg.PeerUser{UserID: 3},
		Date:    1,
		Message: "hi",
	}

	// layer 220: returns a NEW message rewritten to the 220 constructor id,
	// leaving the shared input untouched (227).
	enc, err := encodeOutboundMessage(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	c := &Conn{metrics: NopMetrics{}}
	c.SetClientLayer(220)
	out := c.downgradedClone(enc)

	if id, _ := (&bin.Buffer{Buf: out.body}).PeekID(); id != message220CRC {
		t.Fatalf("downgraded id = %#08x, want %#08x", id, message220CRC)
	}
	if out.typeID != message220CRC {
		t.Fatalf("downgraded typeID = %#08x, want %#08x", out.typeID, message220CRC)
	}
	// Input must be unmodified — this is what makes shared push fan-out safe.
	if id, _ := (&bin.Buffer{Buf: enc.body}).PeekID(); id != message227CRC {
		t.Fatalf("input message was mutated: id now %#08x, want 227 %#08x", id, message227CRC)
	}

	// Two connections sharing one pre-encoded message get independent results.
	encShared, _ := encodeOutboundMessage(msg)
	c220 := &Conn{metrics: NopMetrics{}}
	c220.SetClientLayer(220)
	c227 := &Conn{metrics: NopMetrics{}} // ClientLayer() defaults to 227
	out220 := c220.downgradedClone(encShared)
	out227 := c227.downgradedClone(encShared)
	if id, _ := (&bin.Buffer{Buf: out220.body}).PeekID(); id != message220CRC {
		t.Fatalf("shared->220 id = %#08x, want %#08x", id, message220CRC)
	}
	if out227 != encShared {
		t.Errorf("227 connection should pass the shared message through unchanged (same pointer)")
	}
	if !bytes.Equal(encShared.body, out227.body) {
		t.Errorf("227 passthrough altered bytes")
	}
}

func TestEncodeRPCResultDowngradesDifferenceMessagesForNegotiatedLayer225(t *testing.T) {
	const (
		message227CRC = 0x7600b9d3
		message225CRC = 0x95ef6f2b
	)
	c := &Conn{metrics: NopMetrics{}}
	c.SetClientLayer(225)
	diff := &tg.UpdatesDifference{
		NewMessages: []tg.MessageClass{
			&tg.Message{
				ID:      2,
				FromID:  &tg.PeerUser{UserID: 3},
				PeerID:  &tg.PeerUser{UserID: 3},
				Date:    1,
				Message: "hi",
			},
		},
		NewEncryptedMessages: []tg.EncryptedMessageClass{},
		OtherUpdates:         []tg.UpdateClass{},
		Chats:                []tg.ChatClass{},
		Users:                []tg.UserClass{},
		State:                tg.UpdatesState{Pts: 2, Date: 1},
	}

	s := &Server{log: zaptest.NewLogger(t)}
	encoded, err := s.encodeRPCResult(c, 12345, diff)
	if err != nil {
		t.Fatalf("encode rpc_result: %v", err)
	}
	var result proto.Result
	if err := result.Decode(&bin.Buffer{Buf: encoded.body}); err != nil {
		t.Fatalf("decode rpc_result: %v", err)
	}
	if result.RequestMessageID != 12345 {
		t.Fatalf("req_msg_id = %d, want 12345", result.RequestMessageID)
	}
	if !bytes.Contains(result.Result, littleEndianID(message225CRC)) {
		t.Fatalf("rpc_result inner object does not contain layer 225 message id %#08x", message225CRC)
	}
	if bytes.Contains(result.Result, littleEndianID(message227CRC)) {
		t.Fatalf("rpc_result inner object still contains canonical message id %#08x", message227CRC)
	}
}

func TestEncodeRPCResultDowngradesDialogMessagesForNegotiatedLayer225(t *testing.T) {
	const (
		message227CRC = 0x7600b9d3
		message225CRC = 0x95ef6f2b
	)
	c := &Conn{metrics: NopMetrics{}}
	c.SetClientLayer(225)
	dialogs := &tg.MessagesDialogs{
		Dialogs: []tg.DialogClass{
			&tg.Dialog{
				Peer:           &tg.PeerUser{UserID: 3},
				TopMessage:     2,
				NotifySettings: tg.PeerNotifySettings{},
			},
		},
		Messages: []tg.MessageClass{
			&tg.Message{
				ID:      2,
				FromID:  &tg.PeerUser{UserID: 3},
				PeerID:  &tg.PeerUser{UserID: 3},
				Date:    1,
				Message: "hi",
			},
		},
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{
			&tg.User{ID: 3, AccessHash: 5, FirstName: "A"},
		},
	}

	s := &Server{log: zaptest.NewLogger(t)}
	encoded, err := s.encodeRPCResult(c, 12345, dialogs)
	if err != nil {
		t.Fatalf("encode rpc_result: %v", err)
	}
	var result proto.Result
	if err := result.Decode(&bin.Buffer{Buf: encoded.body}); err != nil {
		t.Fatalf("decode rpc_result: %v", err)
	}
	if !bytes.Contains(result.Result, littleEndianID(message225CRC)) {
		t.Fatalf("rpc_result inner object does not contain layer 225 message id %#08x", message225CRC)
	}
	if bytes.Contains(result.Result, littleEndianID(message227CRC)) {
		t.Fatalf("rpc_result inner object still contains canonical message id %#08x", message227CRC)
	}
}

func TestConnDowngradedCloneDowngradesUpdateNewMessageForLayer225(t *testing.T) {
	const (
		message227CRC = 0x7600b9d3
		message225CRC = 0x95ef6f2b
	)
	updates := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{
				Message: &tg.Message{
					ID:      2,
					FromID:  &tg.PeerUser{UserID: 3},
					PeerID:  &tg.PeerUser{UserID: 3},
					Date:    1,
					Message: "hi",
				},
				Pts:      2,
				PtsCount: 1,
			},
		},
		Users: []tg.UserClass{
			&tg.User{ID: 3, AccessHash: 5, FirstName: "A"},
		},
		Chats: []tg.ChatClass{},
		Date:  1,
		Seq:   1,
	}
	enc, err := encodeOutboundMessage(updates)
	if err != nil {
		t.Fatalf("encode updates: %v", err)
	}
	c := &Conn{metrics: NopMetrics{}}
	c.SetClientLayer(225)
	out := c.downgradedClone(enc)
	if !bytes.Contains(out.body, littleEndianID(message225CRC)) {
		t.Fatalf("push update does not contain layer 225 message id %#08x", message225CRC)
	}
	if bytes.Contains(out.body, littleEndianID(message227CRC)) {
		t.Fatalf("push update still contains canonical message id %#08x", message227CRC)
	}
}

func littleEndianID(id uint32) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, id)
	return buf
}
