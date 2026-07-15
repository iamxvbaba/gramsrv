package mtprotoedge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/transport"
)

type frameBudgetTestConn struct {
	reader bytes.Reader
	read   int
	closed bool
}

func newFrameBudgetTestConn(packet []byte) *frameBudgetTestConn {
	c := &frameBudgetTestConn{}
	c.reader.Reset(packet)
	return c
}

func (c *frameBudgetTestConn) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.read += n
	return n, err
}

func (*frameBudgetTestConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *frameBudgetTestConn) Close() error {
	c.closed = true
	return nil
}
func (*frameBudgetTestConn) LocalAddr() net.Addr              { return frameBudgetTestAddr("local") }
func (*frameBudgetTestConn) RemoteAddr() net.Addr             { return frameBudgetTestAddr("remote") }
func (*frameBudgetTestConn) SetDeadline(time.Time) error      { return nil }
func (*frameBudgetTestConn) SetReadDeadline(time.Time) error  { return nil }
func (*frameBudgetTestConn) SetWriteDeadline(time.Time) error { return nil }

type frameBudgetTestAddr string

func (a frameBudgetTestAddr) Network() string { return "frame-budget-test" }
func (a frameBudgetTestAddr) String() string  { return string(a) }

func newFrameBudgetTestTransport(packet []byte, c transport.Codec, budget *inboundFrameBudget) (*compatTransportConn, *frameBudgetTestConn) {
	raw := newFrameBudgetTestConn(packet)
	return &compatTransportConn{conn: raw, codec: c, budget: budget}, raw
}

func TestInboundFrameBudgetSupportsBuiltInCodecs(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	abridged := append([]byte{byte(len(payload) / bin.Word)}, payload...)
	intermediate := make([]byte, bin.Word+len(payload))
	binary.LittleEndian.PutUint32(intermediate, uint32(len(payload)))
	copy(intermediate[bin.Word:], payload)
	padded := make([]byte, bin.Word+len(payload)+1)
	binary.LittleEndian.PutUint32(padded, uint32(len(payload)+1))
	copy(padded[bin.Word:], payload)
	padded[len(padded)-1] = 0xa5

	var full bytes.Buffer
	fullCodec := &codec.Full{}
	fullPayload := &bin.Buffer{Buf: append([]byte(nil), payload...)}
	if err := fullCodec.Write(&full, fullPayload); err != nil {
		t.Fatalf("encode full frame: %v", err)
	}

	tests := []struct {
		name        string
		packet      []byte
		codec       transport.Codec
		reservation int64
	}{
		{name: "abridged", packet: abridged, codec: &quickAckAbridgedCodec{}, reservation: 2 * int64(len(payload))},
		{name: "intermediate", packet: intermediate, codec: &quickAckIntermediateCodec{}, reservation: 2 * int64(len(payload))},
		{name: "padded_intermediate", packet: padded, codec: &quickAckPaddedIntermediateCodec{}, reservation: 2 * int64(len(payload)+1)},
		{name: "full", packet: full.Bytes(), codec: &codec.Full{}, reservation: int64(full.Len() + len(payload))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := newInboundFrameBudget(tt.reservation)
			conn, _ := newFrameBudgetTestTransport(tt.packet, tt.codec, budget)
			var got bin.Buffer
			if err := conn.Recv(context.Background(), &got); err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if !bytes.Equal(got.Raw(), payload) {
				t.Fatalf("payload = %x, want %x", got.Raw(), payload)
			}
			if used := budget.usedBytes(); used != tt.reservation {
				t.Fatalf("held budget = %d, want %d", used, tt.reservation)
			}
			if err := conn.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if used := budget.usedBytes(); used != tt.reservation {
				t.Fatalf("budget after concurrent Close = %d, want delivered ownership %d", used, tt.reservation)
			}
			conn.releaseInboundFrame()
			if used := budget.usedBytes(); used != 0 {
				t.Fatalf("budget after ownership release = %d, want 0", used)
			}
		})
	}
}

func TestInboundFrameBudgetRejectsBeforePayloadAllocation(t *testing.T) {
	const payloadBytes = 1 << 20
	var header [bin.Word]byte
	binary.LittleEndian.PutUint32(header[:], payloadBytes)
	budget := newInboundFrameBudget(2*payloadBytes - 1)
	conn, raw := newFrameBudgetTestTransport(header[:], &quickAckIntermediateCodec{}, budget)
	var got bin.Buffer

	err := conn.Recv(context.Background(), &got)
	if !errors.Is(err, ErrInboundFrameBudgetExceeded) {
		t.Fatalf("Recv error = %v, want ErrInboundFrameBudgetExceeded", err)
	}
	if raw.read != bin.Word {
		t.Fatalf("wire bytes read = %d, want only %d-byte length prefix", raw.read, bin.Word)
	}
	if cap(got.Buf) != 0 {
		t.Fatalf("payload buffer capacity = %d, want 0 before admission", cap(got.Buf))
	}
	if used := budget.usedBytes(); used != 0 {
		t.Fatalf("budget after rejected preflight = %d, want 0", used)
	}
}

func TestInboundFrameBudgetAbridgedPreflightMatchesCodecSemantics(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	quickPacket := append([]byte{0x80 | byte(len(payload)/bin.Word)}, payload...)
	quickBudget := newInboundFrameBudget(int64(2 * len(payload)))
	quick, _ := newFrameBudgetTestTransport(quickPacket, &quickAckAbridgedCodec{}, quickBudget)
	var got bin.Buffer
	if err := quick.Recv(context.Background(), &got); err != nil {
		t.Fatalf("quick-ack abridged Recv: %v", err)
	}
	requested := quick.ConsumeQuickAckRequested()
	if !bytes.Equal(got.Raw(), payload) || !requested {
		t.Fatalf("quick-ack frame = %x requested=%v", got.Raw(), requested)
	}
	quick.releaseInboundFrame()
	_ = quick.Close()

	// gotd's plain codec treats every first byte >= 0x7f as the extended form (it does not
	// implement the quick-ack high bit). The preflight parser must mirror that behavior; treating
	// 0x82 as a short two-word frame would let the codec allocate from the following three bytes.
	malicious := []byte{0x82, 0xff, 0xff, 0xff}
	plainBudget := newInboundFrameBudget(defaultInboundFrameGlobalMaxBytes)
	plain, raw := newFrameBudgetTestTransport(malicious, codec.Abridged{}, plainBudget)
	got.Reset()
	err := plain.Recv(context.Background(), &got)
	if err == nil {
		t.Fatal("plain abridged accepted oversized extended length")
	}
	if raw.read != 4 || cap(got.Buf) > 2*bin.Word {
		t.Fatalf("plain abridged read=%d buffer_cap=%d, want prefix-only allocation", raw.read, cap(got.Buf))
	}
	_ = plain.Close()
}

func TestInboundFrameBudgetReleasedAtNextRecvAndReusable(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	frame := make([]byte, bin.Word+len(payload))
	binary.LittleEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[bin.Word:], payload)
	packet := append(append([]byte(nil), frame...), frame...)
	reservation := int64(2 * len(payload))
	budget := newInboundFrameBudget(reservation)
	conn, _ := newFrameBudgetTestTransport(packet, &quickAckIntermediateCodec{}, budget)

	for i := 0; i < 2; i++ {
		var got bin.Buffer
		if err := conn.Recv(context.Background(), &got); err != nil {
			t.Fatalf("Recv %d: %v", i+1, err)
		}
		if used := budget.usedBytes(); used != reservation {
			t.Fatalf("held budget after frame %d = %d, want %d", i+1, used, reservation)
		}
	}
	conn.releaseInboundFrame()
	_ = conn.Close()
}

func TestInboundFrameRetainedBackingStaysChargedAcrossSmallFrame(t *testing.T) {
	const largeBytes = 1 << 20
	large := make([]byte, bin.Word+largeBytes)
	binary.LittleEndian.PutUint32(large, largeBytes)
	smallPayload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	small := make([]byte, bin.Word+len(smallPayload))
	binary.LittleEndian.PutUint32(small, uint32(len(smallPayload)))
	copy(small[bin.Word:], smallPayload)
	packet := append(large, small...)
	budget := newInboundFrameBudget(2 * largeBytes)
	conn, _ := newFrameBudgetTestTransport(packet, &quickAckIntermediateCodec{}, budget)
	var wire bin.Buffer
	if err := conn.Recv(context.Background(), &wire); err != nil {
		t.Fatalf("large Recv: %v", err)
	}
	// Model decryptClientFrame's exact-size plaintext reuse buffer.
	plain := bin.Buffer{Buf: make([]byte, largeBytes)}
	retainInboundFrameBackings(conn, &wire, &plain)
	retained := int64(cap(wire.Buf) + cap(plain.Buf))
	if got := budget.usedBytes(); got != retained {
		t.Fatalf("retained budget after large frame = %d, want capacities %d", got, retained)
	}

	wire.Reset()
	if err := conn.Recv(context.Background(), &wire); err != nil {
		t.Fatalf("small Recv: %v", err)
	}
	// The small announcement must not release the large backing's charge. This was the
	// warm-many-connections bypass: each socket retained MiBs while the global budget saw bytes.
	if got := budget.usedBytes(); got != retained {
		t.Fatalf("budget after small frame = %d, want retained high-water %d", got, retained)
	}

	wire.Buf = nil
	plain.Buf = nil
	retainInboundFrameBackings(conn, &wire, &plain)
	if got := budget.usedBytes(); got != 0 {
		t.Fatalf("budget after dropping reusable backings = %d, want 0", got)
	}
	_ = conn.Close()
}

func TestInboundFrameBudgetClosePreservesDeliveredOwnershipUntilRelease(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	frame := make([]byte, bin.Word+len(payload))
	binary.LittleEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[bin.Word:], payload)
	budget := newInboundFrameBudget(int64(2 * len(payload)))

	first, _ := newFrameBudgetTestTransport(frame, &quickAckIntermediateCodec{}, budget)
	var got bin.Buffer
	if err := first.Recv(context.Background(), &got); err != nil {
		t.Fatalf("first Recv: %v", err)
	}
	blocked, _ := newFrameBudgetTestTransport(frame, &quickAckIntermediateCodec{}, budget)
	var blockedPayload bin.Buffer
	if err := blocked.Recv(context.Background(), &blockedPayload); !errors.Is(err, ErrInboundFrameBudgetExceeded) {
		t.Fatalf("concurrent Recv error = %v, want global budget rejection", err)
	}
	if cap(blockedPayload.Buf) != 0 {
		t.Fatalf("blocked connection allocated payload capacity %d", cap(blockedPayload.Buf))
	}
	_ = blocked.Close()
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if used := budget.usedBytes(); used != int64(2*len(payload)) {
		t.Fatalf("budget after concurrent Close = %d, want delivered frame still charged", used)
	}

	second, _ := newFrameBudgetTestTransport(frame, &quickAckIntermediateCodec{}, budget)
	got.Reset()
	if err := second.Recv(context.Background(), &got); !errors.Is(err, ErrInboundFrameBudgetExceeded) {
		t.Fatalf("second Recv before ownership release = %v, want budget rejection", err)
	}
	_ = second.Close()

	first.releaseInboundFrame()
	third, _ := newFrameBudgetTestTransport(frame, &quickAckIntermediateCodec{}, budget)
	got.Reset()
	if err := third.Recv(context.Background(), &got); err != nil {
		t.Fatalf("third Recv after ownership release: %v", err)
	}
	third.releaseInboundFrame()
	_ = third.Close()
}

type unsafeFrameBudgetCodec struct {
	readCalled bool
}

func (*unsafeFrameBudgetCodec) WriteHeader(io.Writer) error         { return nil }
func (*unsafeFrameBudgetCodec) ReadHeader(io.Reader) error          { return nil }
func (*unsafeFrameBudgetCodec) Write(io.Writer, *bin.Buffer) error  { return nil }
func (c *unsafeFrameBudgetCodec) Read(io.Reader, *bin.Buffer) error { c.readCalled = true; return nil }

func TestCustomCodecWithoutPreflightFailsClosed(t *testing.T) {
	raw := newFrameBudgetTestConn([]byte{1, 2, 3, 4})
	listener := newSingleConnListener(raw)
	custom := &unsafeFrameBudgetCodec{}
	budgeted := newCompatTransportListener(func() transport.Codec { return custom }, listener, newInboundFrameBudget(1024))

	conn, err := budgeted.Accept()
	if !errors.Is(err, errInboundFrameCodecUnsupported) {
		t.Fatalf("Accept error = %v, want unsupported preflight codec", err)
	}
	if conn != nil {
		t.Fatal("unsupported custom codec unexpectedly accepted")
	}
	if custom.readCalled || raw.read != 0 {
		t.Fatalf("custom codec touched frame before rejection: read_called=%v wire_read=%d", custom.readCalled, raw.read)
	}
}

func TestExplicitBuiltInCodecUsesBudgetedTransport(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	packet := append([]byte(nil), codec.IntermediateClientStart[:]...)
	var header [bin.Word]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(payload)))
	packet = append(packet, header[:]...)
	packet = append(packet, payload...)

	raw := newFrameBudgetTestConn(packet)
	budget := newInboundFrameBudget(int64(2 * len(payload)))
	listener := newCompatTransportListener(
		func() transport.Codec { return codec.Intermediate{} },
		newSingleConnListener(raw),
		budget,
	)
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	var got bin.Buffer
	if err := conn.Recv(context.Background(), &got); err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if !bytes.Equal(got.Raw(), payload) || budget.usedBytes() != int64(2*len(payload)) {
		t.Fatalf("payload=%x budget=%d", got.Raw(), budget.usedBytes())
	}
	conn.(*compatTransportConn).releaseInboundFrame()
	_ = conn.Close()
}
