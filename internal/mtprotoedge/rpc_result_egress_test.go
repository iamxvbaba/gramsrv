package mtprotoedge

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
)

type opaqueRPCResult struct{ body []byte }

func (o opaqueRPCResult) Encode(b *bin.Buffer) error {
	b.PutID(0x10203040)
	b.Put(o.body)
	return nil
}

func TestEncodeRPCResultUsesAdaptiveGZIP(t *testing.T) {
	s := New(Options{})
	c := &Conn{metrics: NopMetrics{}}
	large := &tg.DataJSON{Data: string(bytes.Repeat([]byte("sticker-metadata-"), 16<<10))}
	encoded, err := s.encodeRPCResult(c, 123, large)
	if err != nil {
		t.Fatalf("encode compressed rpc_result: %v", err)
	}
	if !encoded.compressed {
		t.Fatal("compressible large rpc_result was not gzip_packed")
	}
	if encoded.uncompressedBytes <= len(encoded.body) {
		t.Fatalf("compressed wire=%d is not smaller than inner=%d", len(encoded.body), encoded.uncompressedBytes)
	}
	var result proto.Result
	if err := result.Decode(&bin.Buffer{Buf: encoded.body}); err != nil {
		t.Fatalf("decode rpc_result: %v", err)
	}
	var packed proto.GZIP
	if err := packed.Decode(&bin.Buffer{Buf: result.Result}); err != nil {
		t.Fatalf("decode gzip_packed: %v", err)
	}
	var decoded tg.DataJSON
	if err := decoded.Decode(&bin.Buffer{Buf: packed.Data}); err != nil {
		t.Fatalf("decode compressed inner result: %v", err)
	}
	if decoded.Data != large.Data {
		t.Fatal("gzip round trip changed rpc_result")
	}
}

func TestEncodeRPCResultKeepsIncompressibleBodyRaw(t *testing.T) {
	s := New(Options{})
	c := &Conn{metrics: NopMetrics{}}
	raw := make([]byte, 96<<10)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random body: %v", err)
	}
	encoded, err := s.encodeRPCResult(c, 456, opaqueRPCResult{body: raw})
	if err != nil {
		t.Fatalf("encode incompressible rpc_result: %v", err)
	}
	if encoded.compressed {
		t.Fatal("incompressible rpc_result retained a larger gzip envelope")
	}
	var result proto.Result
	if err := result.Decode(&bin.Buffer{Buf: encoded.body}); err != nil {
		t.Fatalf("decode raw rpc_result: %v", err)
	}
	id, err := (&bin.Buffer{Buf: result.Result}).PeekID()
	if err != nil || id != 0x10203040 {
		t.Fatalf("raw result type = %#x err=%v", id, err)
	}
}

func TestBootstrapBarriersAlwaysUseConvergenceLane(t *testing.T) {
	large := &encodedOutboundMessage{body: make([]byte, bulkOutboundThreshold)}
	for _, method := range []string{
		"updates.getDifference", "updates.getDifference#25939651",
		"updates.getChannelDifference#03173d78", "updates.getState",
		"messages.getDialogs", "messages.getDialogs#a0f4cb4f",
		"messages.getPinnedDialogs", "messages.getPinnedDialogs#d6b94df2",
	} {
		if got := rpcResultPriority(method, large); got != outboundPriorityCritical {
			t.Fatalf("priority(%q) = %s, want convergence", method, got.String())
		}
	}
	if got := rpcResultPriority("messages.getStickerSet", large); got != outboundPriorityBulk {
		t.Fatalf("sticker-set priority = %s, want bulk", got.String())
	}
}

type gatedRecordingTransport struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	sends   atomic.Int32
	mu      sync.Mutex
	frames  [][]byte
}

func newGatedRecordingTransport() *gatedRecordingTransport {
	return &gatedRecordingTransport{started: make(chan struct{}), release: make(chan struct{})}
}

func (t *gatedRecordingTransport) Send(_ context.Context, b *bin.Buffer) error {
	if t.sends.Add(1) == 1 {
		close(t.started)
		<-t.release
	}
	t.mu.Lock()
	t.frames = append(t.frames, append([]byte(nil), b.Raw()...))
	t.mu.Unlock()
	return nil
}

func (*gatedRecordingTransport) Recv(context.Context, *bin.Buffer) error { return io.EOF }
func (t *gatedRecordingTransport) Close() error {
	t.once.Do(func() { close(t.release) })
	return nil
}

func (t *gatedRecordingTransport) snapshot() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.frames))
	for i := range t.frames {
		out[i] = append([]byte(nil), t.frames[i]...)
	}
	return out
}

func encodedRPCResultForPriorityTest(reqMsgID int64, payloadBytes int) *encodedOutboundMessage {
	var b bin.Buffer
	b.PutID(proto.ResultTypeID)
	b.PutLong(reqMsgID)
	b.PutID(tg.BoolTrueTypeID)
	if payloadBytes > 0 {
		b.Put(make([]byte, payloadBytes))
	}
	return &encodedOutboundMessage{typeID: proto.ResultTypeID, reqMsgID: reqMsgID, body: b.Raw()}
}

func TestConvergenceResultPassesQueuedBulkAfterBlockedWrite(t *testing.T) {
	tr := newGatedRecordingTransport()
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(2<<20))
	gate := &encodedOutboundMessage{typeID: tg.UpdatesTooLongTypeID, body: []byte{0x0b, 0xa1, 0x01, 0xe3}}
	if err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, gate, 0); err != nil {
		t.Fatalf("enqueue gate: %v", err)
	}
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("first write did not block")
	}

	const ordinaryResults = 17
	for i := 0; i < ordinaryResults; i++ {
		ordinary := encodedRPCResultForPriorityTest(2000+int64(i), 0)
		if err := c.enqueueEncodedDelivery(context.Background(), proto.MessageServerResponse, ordinary, outboundPriorityNormal, nil); err != nil {
			t.Fatalf("enqueue ordinary result %d: %v", i, err)
		}
	}
	bulk := encodedRPCResultForPriorityTest(1001, bulkOutboundThreshold)
	critical := encodedRPCResultForPriorityTest(1002, 0)
	if err := c.enqueueEncodedDelivery(context.Background(), proto.MessageServerResponse, bulk, outboundPriorityBulk, nil); err != nil {
		t.Fatalf("enqueue bulk: %v", err)
	}
	if err := c.enqueueEncodedDelivery(context.Background(), proto.MessageServerResponse, critical, outboundPriorityCritical, nil); err != nil {
		t.Fatalf("enqueue convergence result: %v", err)
	}
	tr.once.Do(func() { close(tr.release) })
	wantSends := int32(1 + ordinaryResults + 2)
	deadline := time.Now().Add(2 * time.Second)
	for tr.sends.Load() < wantSends && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := tr.sends.Load(); got != wantSends {
		t.Fatalf("physical sends = %d, want %d", got, wantSends)
	}

	var resultOrder []int64
	clientCipher := crypto.NewClientCipher(rand.Reader)
	for _, frame := range tr.snapshot() {
		data, err := clientCipher.DecryptFromBuffer(c.key, &bin.Buffer{Buf: frame})
		if err != nil {
			t.Fatalf("decrypt frame: %v", err)
		}
		plain := &bin.Buffer{Buf: append([]byte(nil), data.Data()...)}
		id, err := plain.PeekID()
		if err != nil || id != proto.ResultTypeID {
			continue
		}
		var result proto.Result
		if err := result.Decode(plain); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		resultOrder = append(resultOrder, result.RequestMessageID)
	}
	if len(resultOrder) != ordinaryResults+2 || resultOrder[0] != 1002 {
		t.Fatalf("rpc_result order = %v, want convergence first", resultOrder)
	}
	bulkIndex := -1
	for i, reqMsgID := range resultOrder {
		if reqMsgID == 1001 {
			bulkIndex = i
			break
		}
	}
	if bulkIndex < 0 || bulkIndex > maxOrdinaryBeforeBulk+1 {
		t.Fatalf("bulk result index = %d in %v, want bounded ordinary burst", bulkIndex, resultOrder)
	}
}

type immediateLargeRPC struct{}

func (immediateLargeRPC) Dispatch(context.Context, [8]byte, int64, *bin.Buffer) (bin.Encoder, error) {
	return &tg.DataJSON{Data: string(bytes.Repeat([]byte("large-sticker-set"), 16<<10))}, nil
}

func (immediateLargeRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

type wrappedConvergenceRPC struct{ immediateLargeRPC }

func (w wrappedConvergenceRPC) DispatchWithMethod(
	ctx context.Context,
	authKeyID [8]byte,
	sessionID int64,
	b *bin.Buffer,
) (bin.Encoder, string, error) {
	result, err := w.Dispatch(ctx, authKeyID, sessionID, b)
	return result, "updates.getDifference", err
}

type captureRPCResultMetrics struct {
	NopMetrics
	mu             sync.Mutex
	preparedMethod string
	priority       string
	innerBytes     int
	wireBytes      int
	compressed     bool
	delivered      chan error
}

func (m *captureRPCResultMetrics) RPCResultPrepared(method, priority string, innerBytes, wireBytes int, compressed bool) {
	m.mu.Lock()
	m.preparedMethod, m.priority = method, priority
	m.innerBytes, m.wireBytes, m.compressed = innerBytes, wireBytes, compressed
	m.mu.Unlock()
}

func (m *captureRPCResultMetrics) RPCResultDelivered(_ string, _ time.Duration, _ int, err error) {
	m.delivered <- err
}

func TestRPCResultPipelineExportsPreparationAndDeliveryMetrics(t *testing.T) {
	metrics := &captureRPCResultMetrics{delivered: make(chan error, 1)}
	s := New(Options{Metrics: metrics})
	c := newOutboundTestConn(t, &failAfterTransport{}, newOutboundTrackedBudget(1<<20))
	const reqMsgID = int64(9050)
	claim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("acquire flight = %+v err=%v", claim, err)
	}
	result := &tg.DataJSON{Data: string(bytes.Repeat([]byte("sticker-data"), 12<<10))}
	if err := s.publishRPCResult(c, reqMsgID, "updates.getDifference#25939651", claim.owner, result, nil); err != nil {
		t.Fatalf("publish result: %v", err)
	}
	select {
	case err := <-metrics.delivered:
		if err != nil {
			t.Fatalf("delivery metric error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing delivery metric")
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.preparedMethod != "updates.getDifference#25939651" || metrics.priority != "convergence" {
		t.Fatalf("prepared metric = %q/%q", metrics.preparedMethod, metrics.priority)
	}
	if !metrics.compressed || metrics.innerBytes <= metrics.wireBytes {
		t.Fatalf("compression metric = compressed:%v inner:%d wire:%d", metrics.compressed, metrics.innerBytes, metrics.wireBytes)
	}
}

func TestWrappedConvergenceMethodDrivesEgressAndReplayPriority(t *testing.T) {
	metrics := &captureRPCResultMetrics{delivered: make(chan error, 1)}
	s := New(Options{RPC: wrappedConvergenceRPC{}, Metrics: metrics})
	c := newOutboundTestConn(t, &failAfterTransport{}, newOutboundTrackedBudget(1<<20))
	const reqMsgID = int64(9051)
	claim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("acquire flight = %+v err=%v", claim, err)
	}
	body := mustEncodeTL(t, &tg.HelpGetConfigRequest{})
	if err := s.handleRPC(context.Background(), c, reqMsgID, "invokeWithLayer#da9b0d0d", &bin.Buffer{Buf: body}, claim.owner); err != nil {
		t.Fatalf("handle wrapped convergence RPC: %v", err)
	}
	select {
	case err := <-metrics.delivered:
		if err != nil {
			t.Fatalf("delivery metric error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing delivery metric")
	}
	metrics.mu.Lock()
	method, priority := metrics.preparedMethod, metrics.priority
	metrics.mu.Unlock()
	if method != "updates.getDifference" || priority != "convergence" {
		t.Fatalf("wrapped prepared metric = %q/%q, want updates.getDifference/convergence", method, priority)
	}
	var cached *encodedOutboundMessage
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, reqMsgID); ok {
			cached = got
			break
		}
		time.Sleep(time.Millisecond)
	}
	if cached == nil {
		t.Fatal("wrapped convergence result missing from replay cache")
	}
	if got := classifyOutboundPriority(cached, false); got != outboundPriorityCritical {
		t.Fatalf("cached convergence priority = %s, want convergence", got.String())
	}
}

func TestRPCWorkerReleasesAfterEgressAdmissionWhileWriteBlocked(t *testing.T) {
	s := New(Options{RPC: immediateLargeRPC{}, WriteTimeout: time.Second})
	tr := newGatedRecordingTransport()
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(2<<20))
	const reqMsgID = int64(9001)
	claim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("acquire flight = %+v err=%v", claim, err)
	}
	body := mustEncodeTL(t, &tg.HelpGetConfigRequest{})
	task := s.newInboundRPCTask(c, reqMsgID, "updates.getDifference#25939651", body, claim.owner)
	done := make(chan error, 1)
	go func() { done <- task.run(context.Background()) }()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("rpc_result write did not start")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RPC worker result: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("RPC worker remained coupled to blocked physical write")
	}
	acquired, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || acquired.state != rpcResultAcquirePending {
		t.Fatalf("blocked delivery flight = %+v err=%v, want pending", acquired, err)
	}
	if task.release != nil {
		task.release()
	}
	if claim.owner.Abort() {
		t.Fatal("detached egress flight was aborted by inbound release")
	}
	tr.once.Do(func() { close(tr.release) })
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, reqMsgID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("delivered result was not published to replay cache")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDeliveryHookRunsOnceAfterReplayNotFailedWrite(t *testing.T) {
	s := New(Options{})
	failing := &failAfterTransport{}
	failing.failAt.Store(1)
	oldConn := newOutboundTestConn(t, failing, newOutboundTrackedBudget(1<<20))
	const reqMsgID = int64(9101)
	claim, err := s.rpcResults.Acquire(oldConn.authKeyID, oldConn.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("acquire flight = %+v err=%v", claim, err)
	}
	var hooks atomic.Int32
	if err := s.publishRPCResult(oldConn, reqMsgID, "updates.getDifference", claim.owner,
		&tg.DataJSON{Data: "difference"}, func() { hooks.Add(1) }); err != nil {
		// Admission succeeds; the asynchronous physical failure is observed below.
		t.Fatalf("publish result: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	var cached *encodedOutboundMessage
	for time.Now().Before(deadline) {
		if got, ok := s.rpcResults.Get(oldConn.authKeyID, oldConn.sessionID, reqMsgID); ok {
			cached = got
			break
		}
		time.Sleep(time.Millisecond)
	}
	if cached == nil {
		t.Fatal("failed write was not fenced and published for replay")
	}
	if got := cached.deliveryState(); got != rpcResultDeliveryReplayable {
		t.Fatalf("failed delivery state = %d, want replayable", got)
	}
	if got := hooks.Load(); got != 0 {
		t.Fatalf("delivery hooks after failed write = %d, want 0", got)
	}

	replayTransport := &failAfterTransport{}
	replayConn := newOutboundTestConn(t, replayTransport, newOutboundTrackedBudget(1<<20))
	if err := s.sendCachedRPCResult(context.Background(), replayConn, cached); err != nil {
		t.Fatalf("replay result: %v", err)
	}
	deadline = time.Now().Add(time.Second)
	for hooks.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := hooks.Load(); got != 1 {
		t.Fatalf("delivery hooks after replay = %d, want 1", got)
	}
	if got := cached.deliveryState(); got != rpcResultDeliveryDelivered {
		t.Fatalf("replayed delivery state = %d, want delivered", got)
	}
	if err := s.sendCachedRPCResult(context.Background(), replayConn, cached); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := hooks.Load(); got != 1 {
		t.Fatalf("delivery hooks after duplicate replay = %d, want 1", got)
	}
}
