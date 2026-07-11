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

type deadlineQuickAckTransport interface {
	SendQuickAckDeadline(deadline time.Time, token uint32) error
}

type compatTransportListener struct {
	codec    func() transport.Codec
	listener net.Listener
	budget   *inboundFrameBudget
}

func newCompatTransportListener(codec func() transport.Codec, listener net.Listener, budget *inboundFrameBudget) transportListener {
	if budget == nil {
		panic("mtprotoedge: nil inbound frame budget")
	}
	return &compatTransportListener{codec: codec, listener: listener, budget: budget}
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

	var (
		connCodec transport.Codec
		reader    io.Reader = conn
	)
	if l.codec != nil {
		connCodec = l.codec()
		if classifyInboundFrameCodec(connCodec) == inboundFrameCodecUnknown {
			// Unknown codecs are rejected before their header or first frame is read. Without an
			// explicit preflight contract, calling Codec.Read could allocate from an attacker-
			// controlled length before the process-wide budget can be reserved.
			return nil, errInboundFrameCodecUnsupported
		}
		if err := connCodec.ReadHeader(conn); err != nil {
			return nil, errors.Wrap(err, "read codec header")
		}
	} else {
		var err error
		connCodec, reader, err = detectCompatCodec(conn)
		if err != nil {
			return nil, errors.Wrap(err, "detect codec")
		}
	}

	return &compatTransportConn{
		conn: wrappedCompatConn{
			reader: reader,
			Conn:   conn,
		},
		codec:  connCodec,
		budget: l.budget,
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
	conn   net.Conn
	codec  transport.Codec
	budget *inboundFrameBudget

	readMux  sync.Mutex
	writeMux sync.Mutex

	frameMu        sync.Mutex
	heldFrameBytes int64
	frameDelivered bool
	closed         bool
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
	deadline, _ := ctx.Deadline()
	return c.SendQuickAckDeadline(deadline, token)
}

func (c *compatTransportConn) SendQuickAckDeadline(deadline time.Time, token uint32) error {
	q, ok := c.codec.(quickAckCodec)
	if !ok {
		return nil
	}

	c.writeMux.Lock()
	defer c.writeMux.Unlock()

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

	// Starting the next Recv proves the previous frame slices are no longer consumed, but its
	// reusable backing remains live. Keep the high-water reservation until the new length prefix
	// atomically grows/reuses it; serveConn later shrinks it to the actually retained capacities.
	c.beginInboundFrameRead()
	if err := c.conn.SetReadDeadline(deadline); err != nil {
		c.releaseInboundFrame()
		return errors.Wrap(err, "set read deadline")
	}
	if err := c.readInboundFrame(b); err != nil {
		// A short payload or protocol error cannot escape while retaining a reservation.
		c.releaseInboundFrame()
		return errors.Wrap(err, "read")
	}
	return nil
}

func (c *compatTransportConn) Close() error {
	c.frameMu.Lock()
	c.closed = true
	// Never release here: Close can race both a delivered frame owned by serveConn and a codec read
	// still writing into b. Recv's error path or serveConn's deferred ownership release is the
	// unique point where those backings become dead.
	c.frameMu.Unlock()
	return c.conn.Close()
}

func (c *compatTransportConn) readInboundFrame(b *bin.Buffer) error {
	kind := classifyInboundFrameCodec(c.codec)
	if kind == inboundFrameCodecUnknown {
		return errInboundFrameCodecUnsupported
	}
	reserveCalls := 0
	reserved := false
	var reserveErr error
	reserve := func(wireBytes, plaintextBytes int64) error {
		reserveCalls++
		if reserveCalls != 1 {
			reserveErr = errors.New("inbound frame codec reserved more than once")
			return reserveErr
		}
		reserveErr = c.reserveInboundFrame(wireBytes, plaintextBytes)
		reserved = reserveErr == nil
		return reserveErr
	}

	var err error
	if kind == inboundFrameCodecCustom {
		custom := unwrapInboundFrameBudgetedCodec(c.codec)
		if custom == nil {
			return errInboundFrameCodecUnsupported
		}
		err = custom.ReadWithInboundFrameBudget(c.conn, b, reserve)
	} else {
		preflight := &inboundFramePreflightReader{r: c.conn, kind: kind, reserve: reserve}
		err = c.codec.Read(preflight, b)
	}
	if err != nil {
		return err
	}
	if reserveErr != nil {
		return reserveErr
	}
	if reserveCalls != 1 || !reserved {
		return errInboundFrameNotReserved
	}
	return c.markInboundFrameDelivered()
}

func (c *compatTransportConn) reserveInboundFrame(wireBytes, plaintextBytes int64) error {
	c.frameMu.Lock()
	defer c.frameMu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	n, err := c.budget.growReservation(c.heldFrameBytes, wireBytes, plaintextBytes)
	if err != nil {
		return err
	}
	c.heldFrameBytes = n
	return nil
}

func (c *compatTransportConn) beginInboundFrameRead() {
	c.frameMu.Lock()
	c.frameDelivered = false
	c.frameMu.Unlock()
}

// retainInboundFrameBytes shrinks the high-water frame charge to the capacities that serveConn
// intentionally keeps for reuse after dispatch. It may grow only to account allocator rounding;
// callers drop both buffers and retry with zero when that extra admission is unavailable.
func (c *compatTransportConn) retainInboundFrameBytes(n int64) bool {
	if n < 0 {
		return false
	}
	c.frameMu.Lock()
	old := c.heldFrameBytes
	if n > old {
		grown, err := c.budget.growReservation(old, n, 0)
		if err != nil {
			c.frameMu.Unlock()
			return false
		}
		c.heldFrameBytes = grown
		c.frameMu.Unlock()
		return true
	}
	c.heldFrameBytes = n
	c.frameMu.Unlock()
	c.budget.release(old - n)
	return true
}

func (c *compatTransportConn) releaseInboundFrame() {
	c.frameMu.Lock()
	n := c.heldFrameBytes
	c.heldFrameBytes = 0
	c.frameDelivered = false
	c.frameMu.Unlock()
	c.budget.release(n)
}

func (c *compatTransportConn) markInboundFrameDelivered() error {
	c.frameMu.Lock()
	defer c.frameMu.Unlock()
	if c.closed {
		// Do not hand a frame to the consumer after Close. The read error path keeps ownership
		// accounting until the codec has stopped touching its backing, then releases it.
		return net.ErrClosed
	}
	if c.heldFrameBytes == 0 {
		return errInboundFrameNotReserved
	}
	c.frameDelivered = true
	return nil
}

type inboundFrameOwnershipReleaser interface {
	releaseInboundFrame()
}

type inboundFrameBackingRetainer interface {
	retainInboundFrameBytes(int64) bool
}

func releaseInboundFrameOwnership(conn transport.Conn) {
	if releaser, ok := conn.(inboundFrameOwnershipReleaser); ok {
		releaser.releaseInboundFrame()
	}
}

// retainInboundFrameBackings transfers the current-frame reservation into a persistent charge
// for reusable buffer capacities. If allocator rounding would exceed the available budget, drop
// both backings and release the reservation rather than retaining unaccounted memory.
func retainInboundFrameBackings(conn transport.Conn, buffers ...*bin.Buffer) {
	retainer, ok := conn.(inboundFrameBackingRetainer)
	if !ok {
		return
	}
	var retained int64
	for _, b := range buffers {
		if b == nil {
			continue
		}
		retained += int64(cap(b.Buf))
	}
	if retainer.retainInboundFrameBytes(retained) {
		return
	}
	for _, b := range buffers {
		if b != nil {
			b.Buf = nil
		}
	}
	if !retainer.retainInboundFrameBytes(0) {
		panic("mtprotoedge: failed to release inbound frame backing reservation")
	}
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
	return writeCompatPacket(w, header[:headerLen], b.Raw())
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
	return writeCompatPacket(w, header[:], b.Raw())
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
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(b.Len()+n))
	buffers := net.Buffers{header[:], b.Raw(), padding[:n]}
	_, err := buffers.WriteTo(w)
	return err
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

// writeCompatPacket avoids a full-frame codec copy. net.Buffers uses vectored I/O for raw TCP
// (one syscall); wrapped writers may receive ordered writes, still serialized by writeMux. The
// outbound scratch lease keeps the encrypted payload alive until all segments finish.
func writeCompatPacket(w io.Writer, header, payload []byte) error {
	buffers := net.Buffers{header, payload}
	_, err := buffers.WriteTo(w)
	return err
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
