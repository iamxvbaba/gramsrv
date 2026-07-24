package mtprotoedge

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mtproxy"
	"github.com/iamxvbaba/td/mtproxy/obfuscator"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/transport"
)

func TestDetectTCPTransportFragmentedPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		prefix []byte
		mode   tcpWireMode
		codec  detectedTCPCodec
	}{
		{name: "abridged", prefix: []byte{0xef}, mode: tcpWireModePlain, codec: detectedTCPCodecAbridged},
		{name: "intermediate", prefix: []byte{0xee, 0xee, 0xee, 0xee}, mode: tcpWireModePlain, codec: detectedTCPCodecIntermediate},
		{name: "padded", prefix: []byte{0xdd, 0xdd, 0xdd, 0xdd}, mode: tcpWireModePlain, codec: detectedTCPCodecPaddedIntermediate},
		{name: "full", prefix: []byte{12, 0, 0, 0, 0, 0, 0, 0}, mode: tcpWireModePlain, codec: detectedTCPCodecFull},
		{name: "obfuscated", prefix: []byte{1, 2, 3, 4, 5, 6, 7, 8}, mode: tcpWireModeObfuscated, codec: detectedTCPCodecUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer func() { _ = server.Close() }()
			defer func() { _ = client.Close() }()
			writeErr := make(chan error, 1)
			go func() {
				for _, b := range tt.prefix {
					if _, err := client.Write([]byte{b}); err != nil {
						writeErr <- err
						return
					}
				}
				writeErr <- nil
			}()

			probe, err := detectTCPTransport(server)
			if err != nil {
				t.Fatalf("detect: %v", err)
			}
			if probe.mode != tt.mode || probe.codec != tt.codec || probe.prefixLen != len(tt.prefix) {
				t.Fatalf("probe = mode:%d codec:%d prefix:%d, want %d/%d/%d",
					probe.mode, probe.codec, probe.prefixLen, tt.mode, tt.codec, len(tt.prefix))
			}
			if err := <-writeErr; err != nil {
				t.Fatalf("write prefix: %v", err)
			}
		})
	}
}

func TestDetectTCPTransportRejectsReservedAndInvalidFullPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		prefix []byte
	}{
		{name: "http", prefix: []byte("GET ")},
		{name: "reserved", prefix: []byte{0x16, 0x03, 0x01, 0x02}},
		{name: "full_too_short", prefix: []byte{8, 0, 0, 0, 0, 0, 0, 0}},
		{name: "full_unaligned", prefix: []byte{13, 0, 0, 0, 0, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer func() { _ = server.Close() }()
			defer func() { _ = client.Close() }()
			go func() {
				_, _ = client.Write(tt.prefix)
			}()
			_, err := detectTCPTransport(server)
			if !errors.Is(err, errInvalidTCPTransportPrefix) {
				t.Fatalf("detect error = %v, want invalid prefix", err)
			}
		})
	}
}

func TestServerMixedTCPAcceptsEveryPlainCodec(t *testing.T) {
	tests := []struct {
		name     string
		protocol transport.Protocol
	}{
		{name: "abridged", protocol: transport.Abridged},
		{name: "intermediate", protocol: transport.Intermediate},
		{name: "padded_intermediate", protocol: transport.PaddedIntermediate},
		{name: "full", protocol: transport.Full},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, frames := startTransportFrameServer(t, Options{
				Logger:        zaptest.NewLogger(t),
				ObfuscatedTCP: true,
			})
			raw, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			conn, err := tt.protocol.Handshake(raw)
			if err != nil {
				_ = raw.Close()
				t.Fatalf("transport handshake: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			var payload bin.Buffer
			payload.PutInt32(0x12345678)
			payload.PutInt32(0x0badf00d)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := conn.Send(ctx, &payload); err != nil {
				t.Fatalf("send: %v", err)
			}
			select {
			case n := <-frames:
				if n < payload.Len() || n > payload.Len()+15 {
					t.Fatalf("frame len = %d, want payload %d plus at most padded-intermediate padding", n, payload.Len())
				}
			case <-ctx.Done():
				t.Fatal("server did not receive plain frame in mixed mode")
			}
		})
	}
}

func TestServerMixedTCPAcceptsObfuscatedCodecs(t *testing.T) {
	tests := []struct {
		name     string
		tag      [4]byte
		newCodec func() transport.Codec
	}{
		{
			name: "abridged",
			tag:  (codec.Abridged{}).ObfuscatedTag(),
			newCodec: func() transport.Codec {
				return codec.NoHeader{Codec: codec.Abridged{}}
			},
		},
		{
			name: "intermediate",
			tag:  codec.IntermediateClientStart,
			newCodec: func() transport.Codec {
				return codec.NoHeader{Codec: codec.Intermediate{}}
			},
		},
		{
			name: "padded_intermediate",
			tag:  codec.PaddedIntermediateClientStart,
			newCodec: func() transport.Codec {
				return codec.NoHeader{Codec: codec.PaddedIntermediate{}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, frames := startTransportFrameServer(t, Options{
				Logger:        zaptest.NewLogger(t),
				ObfuscatedTCP: true,
			})
			raw, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			obfuscated := obfuscator.Obfuscated2(rand.Reader, raw)
			if err := obfuscated.Handshake(tt.tag, 2, mtproxy.Secret{}); err != nil {
				_ = raw.Close()
				t.Fatalf("obfuscated handshake: %v", err)
			}
			conn, err := transport.NewProtocol(tt.newCodec).Handshake(obfuscated)
			if err != nil {
				_ = raw.Close()
				t.Fatalf("transport handshake: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })

			var payload bin.Buffer
			payload.PutInt32(0x12345678)
			payload.PutInt32(0x0badf00d)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := conn.Send(ctx, &payload); err != nil {
				t.Fatalf("send: %v", err)
			}
			select {
			case n := <-frames:
				if n < payload.Len() || n > payload.Len()+15 {
					t.Fatalf("frame len = %d, want payload %d plus at most padding", n, payload.Len())
				}
			case <-ctx.Done():
				t.Fatal("server did not receive obfuscated frame")
			}
		})
	}
}

func TestServerMixedTCPPlainIntermediateKeyExchange(t *testing.T) {
	addr, pub, _ := startTestServer(t, Options{DC: 2, ObfuscatedTCP: true})
	conn, _, _ := dialHandshake(t, addr, 2, pub)
	_ = conn.Close()
}

func TestServerMixedTCPResponseUsesDetectedWireMode(t *testing.T) {
	plainDial := func(protocol transport.Protocol) func(*testing.T, string) transport.Conn {
		return func(t *testing.T, addr string) transport.Conn {
			t.Helper()
			raw, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			conn, err := protocol.Handshake(raw)
			if err != nil {
				_ = raw.Close()
				t.Fatalf("transport handshake: %v", err)
			}
			return conn
		}
	}
	obfuscatedDial := func(
		tag [4]byte,
		newCodec func() transport.Codec,
	) func(*testing.T, string) transport.Conn {
		return func(t *testing.T, addr string) transport.Conn {
			t.Helper()
			raw, err := net.Dial("tcp", addr)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			obfuscated := obfuscator.Obfuscated2(rand.Reader, raw)
			if err := obfuscated.Handshake(tag, 2, mtproxy.Secret{}); err != nil {
				_ = raw.Close()
				t.Fatalf("obfuscated handshake: %v", err)
			}
			conn, err := transport.NewProtocol(newCodec).Handshake(obfuscated)
			if err != nil {
				_ = raw.Close()
				t.Fatalf("transport handshake: %v", err)
			}
			return conn
		}
	}
	tests := []struct {
		name string
		dial func(t *testing.T, addr string) transport.Conn
	}{
		{
			name: "plain_abridged",
			dial: plainDial(transport.Abridged),
		},
		{
			name: "plain_intermediate",
			dial: plainDial(transport.Intermediate),
		},
		{
			name: "plain_padded_intermediate",
			dial: plainDial(transport.PaddedIntermediate),
		},
		{
			name: "plain_full",
			dial: plainDial(transport.Full),
		},
		{
			name: "obfuscated_abridged",
			dial: obfuscatedDial(
				(codec.Abridged{}).ObfuscatedTag(),
				func() transport.Codec {
					return codec.NoHeader{Codec: codec.Abridged{}}
				},
			),
		},
		{
			name: "obfuscated_intermediate",
			dial: obfuscatedDial(
				codec.IntermediateClientStart,
				func() transport.Codec {
					return codec.NoHeader{Codec: codec.Intermediate{}}
				},
			),
		},
		{
			name: "obfuscated_padded_intermediate",
			dial: obfuscatedDial(
				codec.PaddedIntermediateClientStart,
				func() transport.Codec {
					return codec.NoHeader{Codec: codec.PaddedIntermediate{}}
				},
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, _, _ := startTestServer(t, Options{DC: 2, ObfuscatedTCP: true})
			conn := tt.dial(t, addr)
			t.Cleanup(func() { _ = conn.Close() })

			var payload bin.Buffer
			payload.PutLong(0x1020304050607080) // deliberately unknown auth_key_id
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := conn.Send(ctx, &payload); err != nil {
				t.Fatalf("send: %v", err)
			}
			var response bin.Buffer
			err := conn.Recv(ctx, &response)
			var protocolErr *codec.ProtocolErr
			if !errors.As(err, &protocolErr) || protocolErr.Code != codec.CodeAuthKeyNotFound {
				t.Fatalf("response error = %T %v, want transport -404 in the detected wire mode", err, err)
			}
		})
	}
}

func TestServerMixedTCPRejectsInvalidPrefixesWithoutObfuscationWait(t *testing.T) {
	addr, _, _ := startTestServer(t, Options{
		ObfuscatedTCP:        true,
		HandshakeIdleTimeout: 5 * time.Second,
	})
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Write([]byte{8, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("write invalid full prefix: %v", err)
	}
	if err := raw.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, err = raw.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("invalid transport prefix unexpectedly kept connection open")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("invalid prefix waited for obfuscation timeout instead of failing fast: %v", err)
	}
}

func TestServerMixedTCPRejectsUnknownObfuscatedProtocolTag(t *testing.T) {
	addr, _, _ := startTestServer(t, Options{ObfuscatedTCP: true})
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = raw.Close() }()
	obfuscated := obfuscator.Obfuscated2(rand.Reader, raw)
	if err := obfuscated.Handshake([4]byte{1, 2, 3, 4}, 2, mtproxy.Secret{}); err != nil {
		t.Fatalf("obfuscated handshake: %v", err)
	}
	if err := raw.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	_, err = obfuscated.Read(make([]byte, 1))
	if err == nil {
		t.Fatal("unknown obfuscated protocol tag unexpectedly kept connection open")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("unknown obfuscated protocol tag did not fail closed: %v", err)
	}
	if !errors.Is(err, io.EOF) {
		// Windows may report a reset instead of EOF; any non-timeout terminal
		// network error is the same fail-closed outcome.
		t.Logf("terminal read error after invalid obfuscated tag: %v", err)
	}
}

func startTransportFrameServer(t *testing.T, opts Options) (string, <-chan int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	frames := make(chan int, 1)
	srv := New(opts)
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	return ln.Addr().String(), frames
}

func BenchmarkDetectTCPTransport(b *testing.B) {
	tests := []struct {
		name   string
		prefix []byte
	}{
		{name: "plain_abridged", prefix: []byte{0xef}},
		{name: "plain_intermediate", prefix: []byte{0xee, 0xee, 0xee, 0xee}},
		{name: "plain_full", prefix: []byte{12, 0, 0, 0, 0, 0, 0, 0}},
		{name: "obfuscated", prefix: []byte{1, 2, 3, 4, 5, 6, 7, 8}},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			conn := &transportProbeBenchmarkConn{payload: tt.prefix}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				conn.offset = 0
				if _, err := detectTCPTransport(conn); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type transportProbeBenchmarkConn struct {
	payload []byte
	offset  int
}

func (c *transportProbeBenchmarkConn) Read(p []byte) (int, error) {
	if c.offset >= len(c.payload) {
		return 0, io.EOF
	}
	n := copy(p, c.payload[c.offset:])
	c.offset += n
	return n, nil
}

func (*transportProbeBenchmarkConn) Write(p []byte) (int, error) { return len(p), nil }
func (*transportProbeBenchmarkConn) Close() error                { return nil }
func (*transportProbeBenchmarkConn) LocalAddr() net.Addr         { return nil }
func (*transportProbeBenchmarkConn) RemoteAddr() net.Addr        { return nil }
func (*transportProbeBenchmarkConn) SetDeadline(time.Time) error { return nil }
func (*transportProbeBenchmarkConn) SetReadDeadline(time.Time) error {
	return nil
}
func (*transportProbeBenchmarkConn) SetWriteDeadline(time.Time) error {
	return nil
}
