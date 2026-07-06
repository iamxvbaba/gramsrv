package mtprotoedge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/go-faster/errors"
	"go.uber.org/multierr"

	"github.com/gotd/td/bin"
	tdcrypto "github.com/gotd/td/crypto"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/transport"
)

const maxTransportMessageSize = 1 << 24
const quickAckResponseFlag = uint32(1 << 31)

type transportListener interface {
	Accept() (transport.Conn, error)
	Close() error
	Addr() net.Addr
}

type quickAckTransport interface {
	ConsumeQuickAckRequested() bool
	SendQuickAck(ctx context.Context, token uint32) error
}

type compatTransportListener struct {
	codec    func() transport.Codec
	listener net.Listener
}

func newCompatTransportListener(codec func() transport.Codec, listener net.Listener) transportListener {
	if codec != nil {
		return transport.ListenCodec(codec, listener)
	}
	return &compatTransportListener{listener: listener}
}

// singleConnListener 是一个只产出一条「已接受」连接、随后阻塞到关闭的 net.Listener。
// 它让单条裸连接可以走 listener 形态的去混淆/codec 管线（ObfuscatedListener +
// compatTransportListener），从而把这部分阻塞读取从 accept 循环挪到每连接 goroutine。
type singleConnListener struct {
	addr net.Addr
	ch   chan net.Conn
	once sync.Once
}

func newSingleConnListener(c net.Conn) *singleConnListener {
	ch := make(chan net.Conn, 1)
	ch <- c
	return &singleConnListener{addr: c.LocalAddr(), ch: ch}
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l *singleConnListener) Close() error {
	l.once.Do(func() { close(l.ch) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr {
	return l.addr
}

func (l *compatTransportListener) Accept() (_ transport.Conn, rErr error) {
	conn, err := l.listener.Accept()
	if err != nil {
		return nil, err
	}
	defer func() {
		if rErr != nil {
			multierr.AppendInto(&rErr, conn.Close())
		}
	}()

	connCodec, reader, err := detectCompatCodec(conn)
	if err != nil {
		return nil, errors.Wrap(err, "detect codec")
	}

	return &compatTransportConn{
		conn: wrappedCompatConn{
			reader: reader,
			Conn:   conn,
		},
		codec: connCodec,
	}, nil
}

func (l *compatTransportListener) Close() error {
	return l.listener.Close()
}

func (l *compatTransportListener) Addr() net.Addr {
	return l.listener.Addr()
}

type wrappedCompatConn struct {
	reader io.Reader
	net.Conn
}

func (w wrappedCompatConn) Read(p []byte) (int, error) {
	return w.reader.Read(p)
}

type compatTransportConn struct {
	conn  net.Conn
	codec transport.Codec

	readMux  sync.Mutex
	writeMux sync.Mutex
}

func (c *compatTransportConn) Send(ctx context.Context, b *bin.Buffer) error {
	deadline, _ := ctx.Deadline()
	return c.SendDeadline(deadline, b)
}

// SendDeadline 按显式写超时发送一帧（deadline 为零值表示不设超时）。
// 出站热路径（Conn.writeFrame）走这里，免去 per-frame context timer 分配。
func (c *compatTransportConn) SendDeadline(deadline time.Time, b *bin.Buffer) error {
	c.writeMux.Lock()
	defer c.writeMux.Unlock()

	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return errors.Wrap(err, "set write deadline")
	}
	if err := c.codec.Write(c.conn, b); err != nil {
		return errors.Wrap(err, "write")
	}
	return nil
}

func (c *compatTransportConn) ConsumeQuickAckRequested() bool {
	q, ok := c.codec.(quickAckCodec)
	if !ok {
		return false
	}
	return q.consumeQuickAckRequested()
}

func (c *compatTransportConn) SendQuickAck(ctx context.Context, token uint32) error {
	q, ok := c.codec.(quickAckCodec)
	if !ok {
		return nil
	}

	c.writeMux.Lock()
	defer c.writeMux.Unlock()

	deadline, _ := ctx.Deadline()
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return errors.Wrap(err, "set write deadline")
	}

	raw := q.quickAckResponse(token)
	if err := writeAll(c.conn, raw[:]); err != nil {
		return errors.Wrap(err, "write quick ack")
	}
	return nil
}

func (c *compatTransportConn) Recv(ctx context.Context, b *bin.Buffer) error {
	deadline, _ := ctx.Deadline()
	return c.RecvDeadline(deadline, b)
}

// RecvDeadline 按显式读超时收一帧（deadline 为零值表示不设超时）。
// serveConn 的每帧读走这里，免去 per-frame context timer 分配；连接取消仍由
// serveConn 的 ctx watcher 主动 Close 底层连接来解除阻塞（与旧行为一致）。
func (c *compatTransportConn) RecvDeadline(deadline time.Time, b *bin.Buffer) error {
	c.readMux.Lock()
	defer c.readMux.Unlock()

	if err := c.conn.SetReadDeadline(deadline); err != nil {
		return errors.Wrap(err, "set read deadline")
	}
	if err := c.codec.Read(c.conn, b); err != nil {
		return errors.Wrap(err, "read")
	}
	return nil
}

func (c *compatTransportConn) Close() error {
	return c.conn.Close()
}

func detectCompatCodec(c io.Reader) (transport.Codec, io.Reader, error) {
	var buf [4]byte
	if _, err := io.ReadFull(c, buf[:1]); err != nil {
		return nil, nil, errors.Wrap(err, "read first byte")
	}

	if buf[0] == codec.AbridgedClientStart[0] {
		return &quickAckAbridgedCodec{}, c, nil
	}

	if _, err := io.ReadFull(c, buf[1:4]); err != nil {
		return nil, nil, errors.Wrap(err, "read header")
	}
	switch buf {
	case codec.IntermediateClientStart:
		return &quickAckIntermediateCodec{}, c, nil
	case codec.PaddedIntermediateClientStart:
		return &quickAckPaddedIntermediateCodec{}, c, nil
	default:
		return transport.Full.Codec(), io.MultiReader(bytes.NewReader(buf[:]), c), nil
	}
}

type quickAckCodec interface {
	transport.Codec
	consumeQuickAckRequested() bool
	quickAckResponse(token uint32) [4]byte
}

type quickAckAbridgedCodec struct {
	quickAckRequested bool
	wbuf              []byte
}

func (*quickAckAbridgedCodec) WriteHeader(w io.Writer) error {
	return (codec.Abridged{}).WriteHeader(w)
}

func (*quickAckAbridgedCodec) ReadHeader(r io.Reader) error {
	return (codec.Abridged{}).ReadHeader(r)
}

func (q *quickAckAbridgedCodec) Write(w io.Writer, b *bin.Buffer) error {
	if err := validateOutgoingCompatMessage(b); err != nil {
		return err
	}

	words := b.Len() >> 2
	var header [4]byte
	headerLen := 1
	if words < 0x7f {
		header[0] = byte(words)
	} else {
		header[0] = 0x7f
		header[1] = byte(words)
		header[2] = byte(words >> 8)
		header[3] = byte(words >> 16)
		headerLen = 4
	}
	return writeCompatPacket(w, &q.wbuf, header[:headerLen], b.Raw())
}

func (q *quickAckAbridgedCodec) Read(r io.Reader, b *bin.Buffer) error {
	requested, err := readQuickAckAbridged(r, b)
	if err != nil {
		return errors.Wrap(err, "read abridged")
	}
	q.quickAckRequested = requested
	return checkCompatProtocolError(b)
}

func (q *quickAckAbridgedCodec) consumeQuickAckRequested() bool {
	v := q.quickAckRequested
	q.quickAckRequested = false
	return v
}

func (*quickAckAbridgedCodec) quickAckResponse(token uint32) [4]byte {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], (token&^quickAckResponseFlag)|quickAckResponseFlag)
	return raw
}

type quickAckIntermediateCodec struct {
	quickAckRequested bool
	wbuf              []byte
}

func (*quickAckIntermediateCodec) WriteHeader(w io.Writer) error {
	return (codec.Intermediate{}).WriteHeader(w)
}

func (*quickAckIntermediateCodec) ReadHeader(r io.Reader) error {
	return (codec.Intermediate{}).ReadHeader(r)
}

func (q *quickAckIntermediateCodec) Write(w io.Writer, b *bin.Buffer) error {
	if err := validateOutgoingCompatMessage(b); err != nil {
		return err
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(b.Len()))
	return writeCompatPacket(w, &q.wbuf, header[:], b.Raw())
}

func (q *quickAckIntermediateCodec) Read(r io.Reader, b *bin.Buffer) error {
	requested, err := readQuickAckIntermediate(r, b, false)
	if err != nil {
		return errors.Wrap(err, "read intermediate")
	}
	q.quickAckRequested = requested
	return checkCompatProtocolError(b)
}

func (q *quickAckIntermediateCodec) consumeQuickAckRequested() bool {
	v := q.quickAckRequested
	q.quickAckRequested = false
	return v
}

func (*quickAckIntermediateCodec) quickAckResponse(token uint32) [4]byte {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], (token&^quickAckResponseFlag)|quickAckResponseFlag)
	return raw
}

type quickAckPaddedIntermediateCodec struct {
	quickAckRequested bool
	wbuf              []byte
	rand              *bufio.Reader
}

func (*quickAckPaddedIntermediateCodec) WriteHeader(w io.Writer) error {
	return (codec.PaddedIntermediate{}).WriteHeader(w)
}

func (*quickAckPaddedIntermediateCodec) ReadHeader(r io.Reader) error {
	return (codec.PaddedIntermediate{}).ReadHeader(r)
}

func (q *quickAckPaddedIntermediateCodec) Write(w io.Writer, b *bin.Buffer) error {
	if err := validateOutgoingCompatMessage(b); err != nil {
		return err
	}
	// padding 随机数走 per-codec 缓冲预读；codec 写入被 compatTransportConn.writeMux
	// 串行化，单 goroutine 访问安全。
	if q.rand == nil {
		q.rand = bufio.NewReaderSize(tdcrypto.DefaultRand(), 64)
	}
	var padding [4]byte
	if _, err := io.ReadFull(q.rand, padding[:]); err != nil {
		return err
	}
	n := int(padding[0] % 4)
	// header(4B) + payload + padding 一次拼进复用缓冲，单次 Write 出站。
	buf := append(q.wbuf[:0], 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(buf[:4], uint32(b.Len()+n))
	buf = append(buf, b.Raw()...)
	buf = append(buf, padding[:n]...)
	q.wbuf = buf
	return writeAll(w, buf)
}

func (q *quickAckPaddedIntermediateCodec) Read(r io.Reader, b *bin.Buffer) error {
	requested, err := readQuickAckIntermediate(r, b, true)
	if err != nil {
		return errors.Wrap(err, "read padded intermediate")
	}
	q.quickAckRequested = requested
	return checkCompatProtocolError(b)
}

func (q *quickAckPaddedIntermediateCodec) consumeQuickAckRequested() bool {
	v := q.quickAckRequested
	q.quickAckRequested = false
	return v
}

func (*quickAckPaddedIntermediateCodec) quickAckResponse(token uint32) [4]byte {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], (token&^quickAckResponseFlag)|quickAckResponseFlag)
	return raw
}

func readQuickAckAbridged(r io.Reader, b *bin.Buffer) (bool, error) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return false, err
	}

	requested := first[0]&0x80 != 0
	lengthByte := first[0] & 0x7f
	var n int
	if lengthByte == 0x7f {
		var tail [3]byte
		if _, err := io.ReadFull(r, tail[:]); err != nil {
			return false, err
		}
		words := uint32(tail[0]) | uint32(tail[1])<<8 | uint32(tail[2])<<16
		n = int(words << 2)
	} else {
		n = int(lengthByte) << 2
	}

	if err := validateCompatTransportLength(n); err != nil {
		return false, err
	}
	resetCompatBufferN(b, n)
	if _, err := io.ReadFull(r, b.Buf); err != nil {
		return false, errors.Wrap(err, "read payload")
	}
	return requested, nil
}

func readQuickAckIntermediate(r io.Reader, b *bin.Buffer, padding bool) (bool, error) {
	var lengthBuf [4]byte
	if _, err := io.ReadFull(r, lengthBuf[:]); err != nil {
		return false, errors.Wrap(err, "read length")
	}
	rawLength := binary.LittleEndian.Uint32(lengthBuf[:])
	requested := rawLength&quickAckResponseFlag != 0
	n := int(rawLength &^ quickAckResponseFlag)
	if err := validateCompatTransportLength(n); err != nil {
		return false, err
	}
	resetCompatBufferN(b, n)
	if _, err := io.ReadFull(r, b.Buf); err != nil {
		return false, errors.Wrap(err, "read payload")
	}
	if padding {
		paddingLength := n % 4
		b.Buf = b.Buf[:n-paddingLength]
	}
	return requested, nil
}

func validateOutgoingCompatMessage(b *bin.Buffer) error {
	n := b.Len()
	if err := validateCompatTransportLength(n); err != nil {
		return err
	}
	if n%bin.Word != 0 {
		return fmt.Errorf("invalid message length %d: not aligned to %d", n, bin.Word)
	}
	return nil
}

// writeCompatPacket 把 header+payload 拼进调用方持有的复用缓冲后单次写出：
// 保持 MTProto 帧单包出站（quick ack 尾延迟契约），同时避免每帧分配拼包缓冲。
func writeCompatPacket(w io.Writer, scratch *[]byte, header, payload []byte) error {
	buf := append((*scratch)[:0], header...)
	buf = append(buf, payload...)
	*scratch = buf
	return writeAll(w, buf)
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

func validateCompatTransportLength(n int) error {
	if n <= 0 || n > maxTransportMessageSize {
		return fmt.Errorf("invalid message length %d", n)
	}
	return nil
}

func resetCompatBufferN(b *bin.Buffer, n int) {
	if cap(b.Buf) < n {
		b.Buf = make([]byte, n)
		return
	}
	b.Buf = b.Buf[:n]
}

func checkCompatProtocolError(b *bin.Buffer) error {
	if b.Len() != bin.Word {
		return nil
	}
	code, err := b.Int32()
	if err != nil {
		return err
	}
	return &codec.ProtocolErr{Code: -code}
}
