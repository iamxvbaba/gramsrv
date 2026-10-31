package sfu

import (
	"errors"
	"io"
	"net"
	"time"

	"github.com/pion/transport/v4/packetio"
)

// demuxer dispatches the mixed traffic on an ICE conn by first byte per RFC 7983:
//   - [20,63]   → DTLS (handshake and application data)
//   - [128,191] → RTP/RTCP (further split by RTCP PT 200..207)
//
// The write direction goes straight to the underlying ICE conn (same 5-tuple, RTCP-mux).
type demuxer struct {
	base  net.Conn
	dtls  *packetio.Buffer
	srtp  *packetio.Buffer
	srtcp *packetio.Buffer
	done  chan struct{}
}

func newDemuxer(base net.Conn) *demuxer {
	d := &demuxer{
		base:  base,
		dtls:  packetio.NewBuffer(),
		srtp:  packetio.NewBuffer(),
		srtcp: packetio.NewBuffer(),
		done:  make(chan struct{}),
	}
	go d.readLoop()
	return d
}

func (d *demuxer) readLoop() {
	defer close(d.done)
	buf := make([]byte, 1500)
	for {
		n, err := d.base.Read(buf)
		if err != nil {
			_ = d.dtls.Close()
			_ = d.srtp.Close()
			_ = d.srtcp.Close()
			return
		}
		if n == 0 {
			continue
		}
		first := buf[0]
		switch {
		case first >= 20 && first <= 63:
			_, _ = d.dtls.Write(buf[:n])
		case first >= 128 && first <= 191:
			if n >= 2 && isRTCPPayloadType(buf[1]) {
				_, _ = d.srtcp.Write(buf[:n])
			} else {
				_, _ = d.srtp.Write(buf[:n])
			}
		default:
			// STUN is consumed by the ICE layer; drop the rest.
		}
	}
}

func isRTCPPayloadType(pt byte) bool {
	// RTCP packet type range (SR/RR/SDES/BYE/APP/RTPFB/PSFB...).
	return pt >= 192 && pt <= 223
}

func (d *demuxer) Close() {
	_ = d.dtls.Close()
	_ = d.srtp.Close()
	_ = d.srtcp.Close()
}

// demuxConn wraps a single buffer as a net.Conn: reads come from the buffer, writes go straight to the underlying conn.
type demuxConn struct {
	buf  *packetio.Buffer
	base net.Conn
}

func (c *demuxConn) Read(b []byte) (int, error) {
	n, err := c.buf.Read(b)
	if errors.Is(err, io.EOF) || errors.Is(err, packetio.ErrFull) {
		return n, err
	}
	return n, err
}

func (c *demuxConn) Write(b []byte) (int, error) { return c.base.Write(b) }
func (c *demuxConn) Close() error                { return c.buf.Close() }
func (c *demuxConn) LocalAddr() net.Addr         { return c.base.LocalAddr() }
func (c *demuxConn) RemoteAddr() net.Addr        { return c.base.RemoteAddr() }

// SetDeadline/SetReadDeadline delegate to packetio.Buffer: the DTLS handshake relies
// on read timeouts to drive retransmission and failure exit (pion/dtls v3's Handshake
// blocks indefinitely by default, so the caller must set a hard limit during the
// handshake, or a stalled handshake hangs the goroutine).
func (c *demuxConn) SetDeadline(t time.Time) error     { return c.buf.SetReadDeadline(t) }
func (c *demuxConn) SetReadDeadline(t time.Time) error { return c.buf.SetReadDeadline(t) }

// The write direction goes straight to the underlying ICE conn; no queue can time out.
func (c *demuxConn) SetWriteDeadline(t time.Time) error { return nil }

func (d *demuxer) dtlsConn() net.Conn  { return &demuxConn{buf: d.dtls, base: d.base} }
func (d *demuxer) srtpConn() net.Conn  { return &demuxConn{buf: d.srtp, base: d.base} }
func (d *demuxer) srtcpConn() net.Conn { return &demuxConn{buf: d.srtcp, base: d.base} }
