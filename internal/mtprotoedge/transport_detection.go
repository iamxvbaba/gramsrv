package mtprotoedge

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/iamxvbaba/td/mtproxy/obfuscated2"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/transport"
)

var errInvalidTCPTransportPrefix = errors.New("invalid MTProto TCP transport prefix")
var errInvalidObfuscatedProtocol = errors.New("invalid obfuscated MTProto protocol tag")

type tcpWireMode uint8

const (
	tcpWireModeUnknown tcpWireMode = iota
	tcpWireModePlain
	tcpWireModeObfuscated
)

type detectedTCPCodec uint8

const (
	detectedTCPCodecUnknown detectedTCPCodec = iota
	detectedTCPCodecAbridged
	detectedTCPCodecIntermediate
	detectedTCPCodecPaddedIntermediate
	detectedTCPCodecFull
)

type tcpTransportProbe struct {
	raw       net.Conn
	prefix    [8]byte
	prefixLen int
	mode      tcpWireMode
	codec     detectedTCPCodec
}

// detectTCPTransport reads at most the first eight bytes once per physical TCP
// connection. Telegram's obfuscated2 nonce generation explicitly excludes all
// plaintext codec tags and requires bytes 4..8 to be non-zero, while the first
// Full frame has transport sequence number zero. Those disjoint invariants let
// one public port admit both wire modes without probabilistic guessing.
func detectTCPTransport(raw net.Conn) (tcpTransportProbe, error) {
	probe := tcpTransportProbe{raw: raw}
	if _, err := io.ReadFull(raw, probe.prefix[:1]); err != nil {
		return probe, fmt.Errorf("read first transport byte: %w", err)
	}
	probe.prefixLen = 1
	if probe.prefix[0] == codec.AbridgedClientStart[0] {
		probe.mode = tcpWireModePlain
		probe.codec = detectedTCPCodecAbridged
		return probe, nil
	}

	if _, err := io.ReadFull(raw, probe.prefix[1:4]); err != nil {
		return probe, fmt.Errorf("read transport prefix: %w", err)
	}
	probe.prefixLen = 4
	var firstFour [4]byte
	copy(firstFour[:], probe.prefix[:4])
	switch firstFour {
	case codec.IntermediateClientStart:
		probe.mode = tcpWireModePlain
		probe.codec = detectedTCPCodecIntermediate
		return probe, nil
	case codec.PaddedIntermediateClientStart:
		probe.mode = tcpWireModePlain
		probe.codec = detectedTCPCodecPaddedIntermediate
		return probe, nil
	}

	first := binary.LittleEndian.Uint32(firstFour[:])
	if isHTTPHeaderPrefix(firstFour) || first == 0x02010316 {
		return probe, fmt.Errorf("%w: reserved prefix %x", errInvalidTCPTransportPrefix, firstFour)
	}

	if _, err := io.ReadFull(raw, probe.prefix[4:8]); err != nil {
		return probe, fmt.Errorf("read transport discriminator: %w", err)
	}
	probe.prefixLen = 8
	if binary.LittleEndian.Uint32(probe.prefix[4:8]) == 0 {
		length := binary.LittleEndian.Uint32(probe.prefix[:4])
		if length < 3*4 || length > maxTransportMessageSize || length%4 != 0 {
			return probe, fmt.Errorf("%w: invalid full header length %d", errInvalidTCPTransportPrefix, length)
		}
		probe.mode = tcpWireModePlain
		probe.codec = detectedTCPCodecFull
		return probe, nil
	}

	probe.mode = tcpWireModeObfuscated
	return probe, nil
}

func (p tcpTransportProbe) replayConn() net.Conn {
	return newReplayNetConn(p.raw, p.prefix[:p.prefixLen])
}

func (p tcpTransportProbe) plainFrameConn() net.Conn {
	if p.codec == detectedTCPCodecFull {
		// Full has no standalone codec tag: the first eight bytes are already the
		// length and sequence number of its first frame and must be replayed.
		return p.replayConn()
	}
	// Abridged/intermediate/padded-intermediate tags are client-only connection
	// headers. They were consumed by detection and are not part of the first frame.
	return p.raw
}

type replayNetConn struct {
	net.Conn
	prefix [8]byte
	n      uint8
	offset uint8
}

func newReplayNetConn(conn net.Conn, prefix []byte) *replayNetConn {
	if len(prefix) > 8 {
		panic("mtprotoedge: replay prefix exceeds fixed transport discriminator")
	}
	replayed := &replayNetConn{Conn: conn, n: uint8(len(prefix))}
	copy(replayed.prefix[:], prefix)
	return replayed
}

func (c *replayNetConn) Read(p []byte) (int, error) {
	if c.offset < c.n {
		n := copy(p, c.prefix[c.offset:c.n])
		c.offset += uint8(n)
		return n, nil
	}
	return c.Conn.Read(p)
}

type serverObfuscatedConn struct {
	net.Conn
	rw io.ReadWriter
}

func (c *serverObfuscatedConn) Read(p []byte) (int, error) {
	return c.rw.Read(p)
}

func (c *serverObfuscatedConn) Write(p []byte) (int, error) {
	return c.rw.Write(p)
}

func detectedCompatCodec(kind detectedTCPCodec) (transport.Codec, error) {
	switch kind {
	case detectedTCPCodecAbridged:
		return &quickAckAbridgedCodec{}, nil
	case detectedTCPCodecIntermediate:
		return &quickAckIntermediateCodec{}, nil
	case detectedTCPCodecPaddedIntermediate:
		return &quickAckPaddedIntermediateCodec{}, nil
	case detectedTCPCodecFull:
		return transport.Full.Codec(), nil
	default:
		return nil, fmt.Errorf("%w: unknown detected codec %d", errInvalidTCPTransportPrefix, kind)
	}
}

func detectObfuscatedProtocol(tag [4]byte) (detectedTCPCodec, int, error) {
	switch tag {
	case (codec.Abridged{}).ObfuscatedTag():
		return detectedTCPCodecAbridged, 1, nil
	case codec.IntermediateClientStart:
		return detectedTCPCodecIntermediate, 4, nil
	case codec.PaddedIntermediateClientStart:
		return detectedTCPCodecPaddedIntermediate, 4, nil
	default:
		return detectedTCPCodecUnknown, 0, fmt.Errorf("%w: %x", errInvalidObfuscatedProtocol, tag)
	}
}

func (s *Server) promoteMixedTCP(raw net.Conn) (transport.Conn, string, error) {
	probe, err := detectTCPTransport(raw)
	if err != nil {
		return nil, "tcp_auto", err
	}
	if probe.mode == tcpWireModePlain {
		if s.codec != nil {
			conn, err := newCompatTransportConn(s.codec, probe.replayConn(), s.frameBudget)
			return conn, "tcp", err
		}
		connCodec, err := detectedCompatCodec(probe.codec)
		if err != nil {
			return nil, "tcp", err
		}
		conn, err := newCompatTransportConnWithCodec(
			probe.plainFrameConn(),
			connCodec,
			s.frameBudget,
			false,
		)
		return conn, "tcp", err
	}

	replayed := probe.replayConn()
	rw, metadata, err := obfuscated2.Accept(replayed, nil)
	if err != nil {
		return nil, "obfuscated_tcp", fmt.Errorf("accept obfuscated2: %w", err)
	}
	obfuscated := &serverObfuscatedConn{Conn: replayed, rw: rw}
	detected, tagLen, err := detectObfuscatedProtocol(metadata.Protocol)
	if err != nil {
		return nil, "obfuscated_tcp", err
	}
	if s.codec != nil {
		conn, err := newCompatTransportConn(
			s.codec,
			newReplayNetConn(obfuscated, metadata.Protocol[:tagLen]),
			s.frameBudget,
		)
		return conn, "obfuscated_tcp", err
	}
	connCodec, err := detectedCompatCodec(detected)
	if err != nil {
		return nil, "obfuscated_tcp", err
	}
	conn, err := newCompatTransportConnWithCodec(
		obfuscated,
		connCodec,
		s.frameBudget,
		false,
	)
	return conn, "obfuscated_tcp", err
}
