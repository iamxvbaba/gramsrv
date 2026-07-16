package mtprotoedge

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

type opaqueRPCResult struct{ body []byte }

func (o opaqueRPCResult) Encode(b *bin.Buffer) error {
	b.PutID(0x10203040)
	b.Put(o.body)
	return nil
}

func TestEncodeRPCResultUsesAdaptiveGZIP(t *testing.T) {
	s := New(Options{})
	c := legacyCanonicalTestConn(t, &Conn{metrics: NopMetrics{}})
	large := &tg.DataJSON{Data: string(bytes.Repeat([]byte("sticker-metadata-"), 16<<10))}
	encoded, err := s.encodeRPCResult(c, 123, exactTestRPCResult(large))
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
	c := legacyCanonicalTestConn(t, &Conn{metrics: NopMetrics{}})
	raw := make([]byte, 96<<10)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("random body: %v", err)
	}
	encoded, err := s.encodeRPCResult(c, 456, exactTestRPCResult(opaqueRPCResult{body: raw}))
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

func TestEncodeRPCResultReservedChargesBodyBeforeReturning(t *testing.T) {
	budget := newOutboundTrackedBudget(1 << 20)
	c := legacyCanonicalTestConn(t, &Conn{metrics: NopMetrics{}, outboundTrackedBudget: budget})
	s := New(Options{})

	encoded, reserved, err := s.encodeRPCResultReservedContext(
		context.Background(), c, 789, exactTestRPCResult(&tg.DataJSON{Data: "bounded"}),
	)
	if err != nil {
		t.Fatalf("encode reserved rpc_result: %v", err)
	}
	if reserved == nil {
		t.Fatal("encode returned no retained-byte reservation")
	}
	if got, want := budget.used.Load(), int64(len(encoded.body)); got != want {
		t.Fatalf("reserved bytes = %d, want encoded body %d", got, want)
	}
	reserved.release()
	if got := budget.used.Load(); got != 0 {
		t.Fatalf("reserved bytes after release = %d, want 0", got)
	}
}

func TestEncodeRPCResultReservedDropsBodyOnBudgetTimeout(t *testing.T) {
	const maxBytes = 1 << 20
	budget := newOutboundTrackedBudget(maxBytes)
	if !budget.reserve(maxBytes) {
		t.Fatal("saturate outbound body budget")
	}
	c := legacyCanonicalTestConn(t, &Conn{metrics: NopMetrics{}, outboundTrackedBudget: budget})
	s := New(Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	encoded, reserved, err := s.encodeRPCResultReservedContext(
		ctx, c, 790, exactTestRPCResult(&tg.DataJSON{Data: "must-not-escape"}),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("saturated reservation error = %v, want deadline exceeded", err)
	}
	if encoded != nil || reserved != nil {
		t.Fatalf("untracked result escaped encode slot: encoded=%p reserved=%p", encoded, reserved)
	}
	if got := len(outboundEncodeSlots); got != 0 {
		t.Fatalf("encode slots retained after timeout = %d, want 0", got)
	}
	if got := budget.snapshot(); got != maxBytes {
		t.Fatalf("primary budget after timeout = %d, want saturated %d", got, maxBytes)
	}
	budget.release(maxBytes)
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("primary budget after release = %d, want 0", got)
	}
}

func TestEncodeRPCResultFailedRetentionHandoffDropsBodyInSlot(t *testing.T) {
	budget := newOutboundTrackedBudget(1)
	if !budget.reserve(1) {
		t.Fatal("saturate outbound body budget")
	}
	c := legacyCanonicalTestConn(t, &Conn{metrics: NopMetrics{}, outboundTrackedBudget: budget})
	s := New(Options{})
	observedInSlot := false

	encoded, reserved, retained, err := s.encodeRPCResultReservedWithHandoffContext(
		context.Background(), c, 791, exactTestRPCResult(&tg.DataJSON{Data: "handoff-fails"}),
		func(body *encodedOutboundMessage, admissionErr error) error {
			observedInSlot = body != nil && len(body.body) > 0 && len(outboundEncodeSlots) > 0 &&
				errors.Is(admissionErr, ErrOutboundTrackedBudget)
			return errors.New("forced retention failure")
		},
	)
	if !errors.Is(err, errRPCResultRetentionHandoff) {
		t.Fatalf("retention error = %v, want handoff sentinel", err)
	}
	if !observedInSlot {
		t.Fatal("retention handoff did not run while encoded body was slot-confined")
	}
	if retained || encoded != nil || reserved != nil {
		t.Fatalf("failed handoff escaped ownership: retained=%v encoded=%p reserved=%p", retained, encoded, reserved)
	}
	if got := len(outboundEncodeSlots); got != 0 {
		t.Fatalf("encode slots retained after failed handoff = %d, want 0", got)
	}
	if got := budget.snapshot(); got != 1 {
		t.Fatalf("primary budget after failed handoff = %d, want 1", got)
	}
	budget.release(1)
}

const saturatedSlotWaveResultData = "exact-business-success"

type saturatedSlotWaveGate struct {
	firstWave int32
	encodes   atomic.Int32
	entered   chan struct{}
	release   chan struct{}
}

type saturatedSlotWaveResult struct{ gate *saturatedSlotWaveGate }

func (r saturatedSlotWaveResult) Encode(b *bin.Buffer) error {
	call := r.gate.encodes.Add(1)
	if call <= r.gate.firstWave {
		r.gate.entered <- struct{}{}
		<-r.gate.release
	}
	return (&tg.DataJSON{Data: saturatedSlotWaveResultData}).Encode(b)
}

type saturatedSlotWaveRPC struct {
	calls atomic.Int32
	gate  *saturatedSlotWaveGate
}

func (h *saturatedSlotWaveRPC) Dispatch(context.Context, [8]byte, int64, *bin.Buffer) (bin.Encoder, error) {
	h.calls.Add(1)
	return exactTestRPCResult(saturatedSlotWaveResult{gate: h.gate}), nil
}

func (*saturatedSlotWaveRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

func TestPublishRPCResultSaturatedBudgetRetainsExactResultsAcrossSlotWaves(t *testing.T) {
	slotCount := cap(outboundEncodeSlots)
	requestCount := slotCount*2 + 1
	gate := &saturatedSlotWaveGate{
		firstWave: int32(slotCount),
		entered:   make(chan struct{}, slotCount),
		release:   make(chan struct{}),
	}
	handler := &saturatedSlotWaveRPC{gate: gate}
	s := New(Options{legacyRPC: handler})
	now := time.Unix(1_700_000_000, 0)
	s.rpcResults = newRPCResultCacheWithFlightLimit(func() time.Time { return now }, requestCount+1)

	const primaryMax = 1 << 20
	primary := newOutboundTrackedBudget(primaryMax)
	if !primary.reserve(primaryMax) {
		t.Fatal("saturate shared primary outbound budget")
	}

	conns := make([]*Conn, requestCount)
	tasks := make([]inboundRPC, requestCount)
	owners := make([]*rpcResultOwnerLease, requestCount)
	reqMsgIDs := make([]int64, requestCount)
	requestBody := mustEncodeTL(t, &tg.PhoneGetCallConfigRequest{})
	for i := 0; i < requestCount; i++ {
		var authKeyID [8]byte
		authKeyID[0] = byte(i + 1)
		authKeyID[1] = byte((i + 1) >> 8)
		reqMsgID := int64(10_000 + i)
		c := &Conn{
			metrics:               NopMetrics{},
			writeTimeout:          time.Second,
			authKeyID:             authKeyID,
			sessionID:             int64(20_000 + i),
			outboundTrackedBudget: primary,
		}
		legacyLayerWireTestConn(t, c, 227)
		claim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgID)
		if err != nil || claim.state != rpcResultAcquireOwner {
			t.Fatalf("acquire request %d = %+v err=%v", i, claim, err)
		}
		conns[i] = c
		owners[i] = claim.owner
		reqMsgIDs[i] = reqMsgID
		tasks[i] = s.newInboundRPCTask(c, reqMsgID, "phone.getCallConfig", requestBody, claim.owner)
	}

	start := make(chan struct{})
	errs := make([]error, requestCount)
	var wg sync.WaitGroup
	wg.Add(requestCount)
	for i := range tasks {
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = tasks[i].run(context.Background())
			if tasks[i].release != nil {
				tasks[i].release()
			}
		}(i)
	}
	close(start)
	for i := 0; i < slotCount; i++ {
		select {
		case <-gate.entered:
		case <-time.After(time.Second):
			close(gate.release)
			t.Fatalf("first encode wave entered %d/%d slots; handlers=%d encodes=%d first_err=%v", i, slotCount, handler.calls.Load(), gate.encodes.Load(), errs[0])
		}
	}
	if got := gate.encodes.Load(); got != int32(slotCount) {
		close(gate.release)
		t.Fatalf("encodes before releasing first wave = %d, want slot cap %d", got, slotCount)
	}
	close(gate.release)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("slot waves did not converge after saturated-budget retention")
	}

	if got := handler.calls.Load(); got != int32(requestCount) {
		t.Fatalf("business executions = %d, want %d", got, requestCount)
	}
	if got := gate.encodes.Load(); got != int32(requestCount) {
		t.Fatalf("successful result encodes = %d, want %d", got, requestCount)
	}
	if got := len(outboundEncodeSlots); got != 0 {
		t.Fatalf("encode slots after both waves = %d, want 0", got)
	}
	if got := primary.snapshot(); got != primaryMax {
		t.Fatalf("primary budget changed under saturation = %d, want %d", got, primaryMax)
	}

	var completedBytes int64
	for i, c := range conns {
		if !errors.Is(errs[i], ErrOutboundTrackedBudget) {
			t.Fatalf("publish request %d error = %v, want terminal budget saturation", i, errs[i])
		}
		if !c.isRetired() {
			t.Fatalf("request %d connection was not explicitly fenced", i)
		}
		if !owners[i].handedOff.Load() {
			t.Fatalf("request %d owner was not handed to completed cache", i)
		}
		cached, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, reqMsgIDs[i])
		if !ok || cached == nil {
			t.Fatalf("request %d exact result missing from completed cache", i)
		}
		completedBytes += int64(len(cached.body))
		var envelope proto.Result
		if err := envelope.Decode(&bin.Buffer{Buf: cached.body}); err != nil {
			t.Fatalf("decode request %d cached rpc_result: %v", i, err)
		}
		if envelope.RequestMessageID != reqMsgIDs[i] {
			t.Fatalf("request %d cached req_msg_id = %d, want %d", i, envelope.RequestMessageID, reqMsgIDs[i])
		}
		var result tg.DataJSON
		if err := result.Decode(&bin.Buffer{Buf: envelope.Result}); err != nil {
			t.Fatalf("decode request %d exact business result (possibly INTERNAL): %v", i, err)
		}
		if result.Data != saturatedSlotWaveResultData {
			t.Fatalf("request %d cached result = %q, want %q", i, result.Data, saturatedSlotWaveResultData)
		}
		retry, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgIDs[i])
		if err != nil || retry.state != rpcResultAcquireCompleted || retry.encoded != cached {
			t.Fatalf("retry request %d = %+v err=%v, want exact completed result", i, retry, err)
		}
	}
	if got := handler.calls.Load(); got != int32(requestCount) {
		t.Fatalf("business executions after retries = %d, want unchanged %d", got, requestCount)
	}
	if got := s.rpcResults.completedBytes.snapshot(); got != completedBytes {
		t.Fatalf("completed-cache charge = %d, want exact retained bytes %d", got, completedBytes)
	}

	// Expiry is the completed cache's ownership release point. Force it
	// deterministically and prove every retained byte is returned exactly once.
	now = now.Add(rpcResultCacheTTL + time.Second)
	for i, c := range conns {
		if _, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, reqMsgIDs[i]); ok {
			t.Fatalf("request %d remained cached after forced expiry", i)
		}
	}
	if got := s.rpcResults.completedBytes.snapshot(); got != 0 {
		t.Fatalf("completed-cache bytes after expiry = %d, want 0", got)
	}
	primary.release(primaryMax)
	if got := primary.snapshot(); got != 0 {
		t.Fatalf("primary budget after release = %d, want 0", got)
	}
}

func TestCachedReplayRestoreIsSynchronousAndIndependentOfGlobalHookExecutor(t *testing.T) {
	// Occupy the entire executor. The replay-state callback must not reserve a
	// ticket there: slow auth/store restoration has its own bounded path.
	executor := newRPCDeliveryHookExecutor(1, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	ticket, ok := executor.reserve()
	if !ok || !ticket.submit(func() {
		close(started)
		<-release
	}) {
		t.Fatal("occupy delivery hook executor")
	}
	<-started
	oldExecutor := defaultRPCDeliveryHookExecutor
	defaultRPCDeliveryHookExecutor = executor
	defer func() {
		defaultRPCDeliveryHookExecutor = oldExecutor
		close(release)
	}()

	s := New(Options{WriteTimeout: time.Second})
	transport := &collectingSessionTransport{}
	key := newTestAuthKey(t)
	c := s.newConn(transport, key, 777, 1)
	legacyCanonicalTestConn(t, c)
	t.Cleanup(c.ForceClose)
	encoded := encodedRPCResultForPriorityTest(9001, 0)
	encoded.delivery = newRPCResultDelivery(encoded.reqMsgID)
	var restoreOrder atomic.Int32
	encoded.setDeliveryHook(func() {
		if !restoreOrder.CompareAndSwap(1, 2) {
			panic("logical replay hook did not run after replacement metadata restore")
		}
	})

	var restored atomic.Bool
	if err := s.sendCachedRPCResultWithHook(context.Background(), c, encoded, func() error {
		if got := len(transport.snapshot()); got != 1 {
			return errors.New("replay restore ran before physical write")
		}
		if !restoreOrder.CompareAndSwap(0, 1) {
			return errors.New("replacement replay restore ran out of order")
		}
		restored.Store(true)
		return nil
	}); err != nil {
		t.Fatalf("send cached replay with saturated global executor: %v", err)
	}
	if !restored.Load() {
		t.Fatal("cached replay returned before state restore completed")
	}
	if got := restoreOrder.Load(); got != 2 {
		t.Fatalf("ordered replay restore stage = %d, want replacement then logical hook", got)
	}
	c.rpcMu.Lock()
	pending := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pending != 0 {
		t.Fatalf("replay restore barriers = %d, want 0", pending)
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
	return &encodedOutboundMessage{
		typeID: proto.ResultTypeID, reqMsgID: reqMsgID, body: b.Raw(),
		layer: &outboundLayerBinding{
			profile: tlprofile.ProfileCanonical,
			kind:    outboundLayerBindingRequest,
		},
	}
}

func TestConvergenceResultPassesQueuedBulkAfterBlockedWrite(t *testing.T) {
	tr := newGatedRecordingTransport()
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(2<<20))
	gate := exactTestUpdatesTooLong(t, c)
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
	if err := s.publishRPCResult(c, reqMsgID, "updates.getDifference#25939651", claim.owner, exactTestRPCResult(result), nil); err != nil {
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
	s := New(Options{legacyRPC: wrappedConvergenceRPC{}, Metrics: metrics})
	c := newOutboundTestConn(t, &failAfterTransport{}, newOutboundTrackedBudget(1<<20))
	const reqMsgID = int64(9051)
	claim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("acquire flight = %+v err=%v", claim, err)
	}
	body := mustEncodeTL(t, &tg.HelpGetConfigRequest{})
	if err := s.handleRPC(context.Background(), c, reqMsgID, "invokeWithLayer#da9b0d0d", &bin.Buffer{Buf: body}, claim.owner); err != nil {
		t.Fatalf("handle wrapped convergence legacyRPC: %v", err)
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
	s := New(Options{legacyRPC: immediateLargeRPC{}, WriteTimeout: time.Second})
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
		exactTestRPCResult(&tg.DataJSON{Data: "difference"}), func() { hooks.Add(1) }); err != nil {
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
	if got := cached.deliveryState(); got != rpcResultDeliveryReplayable {
		t.Fatalf("cached representation state = %d, want original replayable attempt", got)
	}
	if cached.delivery == nil || cached.delivery.coordinator == nil ||
		cached.delivery.coordinator.hookState() != rpcResultDeliveryHookDone {
		t.Fatal("successful replay did not complete shared delivery coordinator")
	}
	if err := s.sendCachedRPCResult(context.Background(), replayConn, cached); err != nil {
		t.Fatalf("second replay: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if got := hooks.Load(); got != 1 {
		t.Fatalf("delivery hooks after duplicate replay = %d, want 1", got)
	}
}
