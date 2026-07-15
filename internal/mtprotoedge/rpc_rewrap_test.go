package mtprotoedge

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
)

func encodeRewrapTestRequest(t *testing.T) ([]byte, []byte) {
	t.Helper()
	var inner bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&inner); err != nil {
		t.Fatalf("encode inner request: %v", err)
	}
	var wrapped bin.Buffer
	if err := (&tg.InvokeWithLayerRequest{
		Layer: 227,
		Query: &tg.InitConnectionRequest{
			APIID: 6, DeviceModel: "Pixel", SystemVersion: "SDK 36",
			AppVersion: "12.8.1", SystemLangCode: "en", LangPack: "android", LangCode: "en",
			Query: &tg.HelpGetConfigRequest{},
		},
	}).Encode(&wrapped); err != nil {
		t.Fatalf("encode wrapped request: %v", err)
	}
	return append([]byte(nil), inner.Raw()...), append([]byte(nil), wrapped.Raw()...)
}

func TestDecodeRPCRewrapInitExtractsExactInnerQuery(t *testing.T) {
	inner, wrapped := encodeRewrapTestRequest(t)
	init, ok := decodeRPCRewrapInit(wrapped)
	if !ok {
		t.Fatal("valid invokeWithLayer(initConnection(query)) was not recognized")
	}
	if init.layer != 227 || init.apiID != 6 || init.langPack != "android" {
		t.Fatalf("metadata = layer:%d api:%d lang_pack:%q", init.layer, init.apiID, init.langPack)
	}
	if string(init.inner) != string(inner) {
		t.Fatalf("inner = %x, want %x", init.inner, inner)
	}
	if _, ok := decodeRPCRewrapInit(inner); ok {
		t.Fatal("naked request must not be classified as an init rewrap")
	}
}

func TestRPCResultDeliveryRetargetHasExactWritingBarrier(t *testing.T) {
	const oldReqID, newReqID, tooLateReqID = int64(101), int64(202), int64(303)
	encoded := &encodedOutboundMessage{
		typeID: mt.RPCResultTypeID, reqMsgID: oldReqID,
		body: make([]byte, 16), delivery: newRPCResultDelivery(oldReqID),
	}
	if !encoded.tryRetarget(newReqID) {
		t.Fatal("prepared result should be retargetable")
	}
	if got := encoded.beginWriting(); got != newReqID {
		t.Fatalf("writing target = %d, want %d", got, newReqID)
	}
	if encoded.tryRetarget(tooLateReqID) {
		t.Fatal("writing result must not be mutated")
	}
}

func TestRPCRewrapAliasKeepsHookAcrossFailedSourceAttempt(t *testing.T) {
	executor := newRPCDeliveryHookExecutor(1, 2)
	var hooks atomic.Int32
	source := &encodedOutboundMessage{
		typeID:   mt.RPCResultTypeID,
		reqMsgID: 101,
		body:     make([]byte, 16),
		delivery: newRPCResultDelivery(101),
	}
	binary.LittleEndian.PutUint32(source.body[:4], mt.RPCResultTypeID)
	binary.LittleEndian.PutUint64(source.body[4:12], 101)
	source.setDeliveryHook(func() { hooks.Add(1) })
	if err := source.prepareDeliveryHook(executor); err != nil {
		t.Fatalf("reserve source attempt: %v", err)
	}
	source.markReplayable()

	alias, err := cloneRPCResultForRequest(source, 202, false)
	if err != nil {
		t.Fatalf("clone alias: %v", err)
	}
	if alias.delivery == source.delivery {
		t.Fatal("alias reused source physical-attempt state")
	}
	if alias.delivery.coordinator != source.delivery.coordinator {
		t.Fatal("alias did not inherit logical delivery coordinator")
	}
	if err := alias.prepareDeliveryHook(executor); err != nil {
		t.Fatalf("reserve alias attempt: %v", err)
	}
	alias.markDelivered()
	deadline := time.Now().Add(time.Second)
	for hooks.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := hooks.Load(); got != 1 {
		t.Fatalf("alias delivery hooks = %d, want 1", got)
	}
	if got := source.deliveryState(); got != rpcResultDeliveryReplayable {
		t.Fatalf("source attempt state = %d, want replayable", got)
	}
	if got := alias.deliveryState(); got != rpcResultDeliveryDelivered {
		t.Fatalf("alias attempt state = %d, want delivered", got)
	}
}

func TestRPCRewrapFailedSourceAttemptPhysicallyDeliversAliasOnce(t *testing.T) {
	s := New(Options{WriteTimeout: time.Second})
	failedTransport := &failAfterTransport{}
	failedTransport.failAt.Store(1)
	aliasTransport := &collectingSessionTransport{}
	key := newTestAuthKey(t)
	const sessionID, oldReqID, newReqID = int64(89), int64(5101), int64(5201)
	sourceConn := s.newConn(failedTransport, key, sessionID, 1)
	aliasConn := s.newConn(aliasTransport, key, sessionID, 1)
	legacyCanonicalTestConn(t, sourceConn)
	legacyCanonicalTestConn(t, aliasConn)
	t.Cleanup(sourceConn.ForceClose)
	t.Cleanup(aliasConn.ForceClose)

	oldClaim, err := s.rpcResults.Acquire(key.ID, sessionID, oldReqID)
	if err != nil || oldClaim.state != rpcResultAcquireOwner {
		t.Fatalf("old flight = %+v err=%v", oldClaim, err)
	}
	requestBody := []byte{1, 2, 3, 4}
	if !s.rpcRewrap.register(sourceConn, requestBody, oldReqID, "test.method", oldClaim.owner) {
		t.Fatal("register source rewrap candidate")
	}
	candidate := s.rpcRewrap.claim(aliasConn, requestBody)
	if candidate == nil {
		t.Fatal("claim source rewrap candidate")
	}
	newClaim, err := s.rpcResults.Acquire(key.ID, sessionID, newReqID)
	if err != nil || newClaim.state != rpcResultAcquireOwner {
		t.Fatalf("alias flight = %+v err=%v", newClaim, err)
	}
	alias := &rpcRewrapAlias{
		conn: aliasConn, newReqID: newReqID, method: "test.method",
		oldWaiter: oldClaim.owner.Waiter(), newOwner: newClaim.owner,
		sourceConn: sourceConn, sourceOwner: oldClaim.owner,
		candidate: candidate, registry: s.rpcRewrap,
	}
	if err := alias.activate(s); err != nil {
		t.Fatalf("activate alias: %v", err)
	}

	var hooks atomic.Int32
	if err := s.publishRPCResult(sourceConn, oldReqID, "test.method", oldClaim.owner,
		&mt.RPCError{ErrorCode: 400, ErrorMessage: "TEST"}, func() { hooks.Add(1) }); err != nil {
		t.Fatalf("publish source result: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cached, ok := s.rpcResults.Get(key.ID, sessionID, newReqID)
		if ok && cached.deliveryState() == rpcResultDeliveryDelivered && hooks.Load() == 1 {
			if got := cached.writtenRequestID(); got != newReqID {
				t.Fatalf("alias physical request ID = %d, want %d", got, newReqID)
			}
			if got := len(aliasTransport.snapshot()); got != 1 {
				t.Fatalf("alias physical writes = %d, want 1", got)
			}
			sourceCached, sourceOK := s.rpcResults.Get(key.ID, sessionID, oldReqID)
			if !sourceOK || sourceCached.deliveryState() != rpcResultDeliveryReplayable {
				t.Fatalf("source physical attempt = cached:%v state:%d, want replayable", sourceOK, sourceCached.deliveryState())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cached, ok := s.rpcResults.Get(key.ID, sessionID, newReqID)
	t.Fatalf("alias result = cached:%v state:%v hooks:%d writes:%d", ok, cached.deliveryState(), hooks.Load(), len(aliasTransport.snapshot()))
}

func TestRPCRewrapRepeatedReplacementSubscriberCapacityStaysBounded(t *testing.T) {
	s := New(Options{WriteTimeout: time.Second})
	s.rpcResults = newRPCResultSubscriberTestCache(2, 2, 2, 2)
	transport := &collectingSessionTransport{}
	key := newTestAuthKey(t)
	const sessionID, oldReqID, newReqID = int64(188), int64(15101), int64(15201)
	c := s.newConn(transport, key, sessionID, 1)
	legacyCanonicalTestConn(t, c)
	t.Cleanup(c.ForceClose)

	oldClaim, err := s.rpcResults.Acquire(key.ID, sessionID, oldReqID)
	if err != nil || oldClaim.owner == nil {
		t.Fatalf("old flight = %#v err=%v", oldClaim, err)
	}
	var sourceResultCalls, sourceExecutionCalls atomic.Int32
	if err := oldClaim.owner.Waiter().SubscribeResultAndExecution(
		func(*encodedOutboundMessage, bool) { sourceResultCalls.Add(1) },
		func(bool) { sourceExecutionCalls.Add(1) },
	); err != nil {
		t.Fatalf("fill source subscriber capacity: %v", err)
	}

	for i := 0; i < 100; i++ {
		newClaim, err := s.rpcResults.Acquire(key.ID, sessionID, newReqID)
		if err != nil || newClaim.owner == nil {
			t.Fatalf("replacement %d Acquire = %#v err=%v", i, newClaim, err)
		}
		plan := &inboundPlan{rewrapAliases: []*rpcRewrapAlias{{
			conn: c, oldWaiter: oldClaim.owner.Waiter(), newOwner: newClaim.owner,
			newReqID: newReqID, method: "test.method",
		}}}
		err = plan.commitRewrapAliases(s)
		if !errors.Is(err, ErrRPCResultSubscriberCapacity) {
			t.Fatalf("replacement %d activation err=%v, want subscriber capacity", i, err)
		}
		if got := s.rpcResults.subscriberBudget.global.snapshot(); got != 2 {
			t.Fatalf("replacement %d subscriber usage=%d, want 2", i, got)
		}
		c.rpcMu.Lock()
		barriers := c.rpcReplayRestores
		c.rpcMu.Unlock()
		if barriers != 0 {
			t.Fatalf("replacement %d leaked replay barrier=%d", i, barriers)
		}
	}

	if !oldClaim.owner.Abort() {
		t.Fatal("abort source owner")
	}
	if sourceResultCalls.Load() != 1 || sourceExecutionCalls.Load() != 1 {
		t.Fatalf("source callbacks result=%d execution=%d", sourceResultCalls.Load(), sourceExecutionCalls.Load())
	}
	if got := s.rpcResults.subscriberBudget.global.snapshot(); got != 0 {
		t.Fatalf("subscriber usage=%d after source abort", got)
	}
}

func TestRPCRewrapRetargetFailureRequiresReplacementAliasWrite(t *testing.T) {
	s := New(Options{WriteTimeout: time.Second})
	failedTransport := &failAfterTransport{}
	failedTransport.failAt.Store(1)
	key := newTestAuthKey(t)
	const sessionID, oldReqID, newReqID = int64(90), int64(6101), int64(6201)
	failedConn := s.newConn(failedTransport, key, sessionID, 1)
	legacyCanonicalTestConn(t, failedConn)
	t.Cleanup(failedConn.ForceClose)

	oldClaim, err := s.rpcResults.Acquire(key.ID, sessionID, oldReqID)
	if err != nil || oldClaim.state != rpcResultAcquireOwner {
		t.Fatalf("old flight = %+v err=%v", oldClaim, err)
	}
	requestBody := []byte{5, 6, 7, 8}
	if !s.rpcRewrap.register(failedConn, requestBody, oldReqID, "test.method", oldClaim.owner) {
		t.Fatal("register source rewrap candidate")
	}
	candidate := s.rpcRewrap.claim(failedConn, requestBody)
	if candidate == nil {
		t.Fatal("claim source rewrap candidate")
	}
	newClaim, err := s.rpcResults.Acquire(key.ID, sessionID, newReqID)
	if err != nil || newClaim.state != rpcResultAcquireOwner {
		t.Fatalf("alias flight = %+v err=%v", newClaim, err)
	}
	alias := &rpcRewrapAlias{
		conn: failedConn, newReqID: newReqID, method: "test.method",
		oldWaiter: oldClaim.owner.Waiter(), newOwner: newClaim.owner,
		sourceConn: failedConn, sourceOwner: oldClaim.owner,
		candidate: candidate, registry: s.rpcRewrap,
	}
	if err := alias.activate(s); err != nil {
		t.Fatalf("activate alias: %v", err)
	}
	if !alias.retargeted.Load() {
		t.Fatal("same-Conn queued source was not retargeted")
	}

	var hooks atomic.Int32
	if err := s.publishRPCResult(failedConn, oldReqID, "test.method", oldClaim.owner,
		&mt.RPCError{ErrorCode: 400, ErrorMessage: "TEST"}, func() { hooks.Add(1) }); err != nil {
		t.Fatalf("publish source result: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var aliasCached *encodedOutboundMessage
	for time.Now().Before(deadline) {
		if cached, ok := s.rpcResults.Get(key.ID, sessionID, newReqID); ok && cached.deliveryState() == rpcResultDeliveryReplayable {
			aliasCached = cached
			break
		}
		time.Sleep(time.Millisecond)
	}
	if aliasCached == nil {
		t.Fatal("failed retarget was incorrectly treated as delivered or alias result was not retained")
	}
	if hooks.Load() != 0 {
		t.Fatalf("failed retarget ran delivery hook %d times", hooks.Load())
	}

	replacementTransport := &collectingSessionTransport{}
	replacement := s.newConn(replacementTransport, key, sessionID, 1)
	legacyCanonicalTestConn(t, replacement)
	t.Cleanup(replacement.ForceClose)
	if err := s.sendCachedRPCResult(context.Background(), replacement, aliasCached); err != nil {
		t.Fatalf("replacement alias replay: %v", err)
	}
	deadline = time.Now().Add(time.Second)
	for hooks.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if hooks.Load() != 1 || len(replacementTransport.snapshot()) != 1 {
		t.Fatalf("replacement delivery = hooks:%d writes:%d, want 1/1", hooks.Load(), len(replacementTransport.snapshot()))
	}
}

func TestRPCResultWaiterSubscribeIsEventDriven(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 8)
	claim, err := cache.Acquire([8]byte{1}, 2, 3)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("Acquire owner = %+v, %v", claim, err)
	}
	var called atomic.Bool
	if err := claim.owner.Waiter().Subscribe(func(encoded *encodedOutboundMessage, ok bool) {
		if !ok || encoded == nil {
			t.Errorf("subscriber result = %#v, %v", encoded, ok)
		}
		called.Store(true)
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if called.Load() {
		t.Fatal("Subscribe waited for or fabricated a result")
	}
	encoded := &encodedOutboundMessage{typeID: mt.RPCResultTypeID, body: make([]byte, 16), reqMsgID: 3}
	cache.Put([8]byte{1}, 2, 3, encoded)
	if !called.Load() {
		t.Fatal("completion event did not invoke subscriber")
	}
}

func TestRPCResultOwnerAbortHookInstallationIsFlightBound(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 8)
	claim, err := cache.Acquire([8]byte{2}, 3, 4)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("Acquire owner = %+v, %v", claim, err)
	}
	var called atomic.Bool
	if !claim.owner.InstallAbortHook(func() { called.Store(true) }) {
		t.Fatal("live owner rejected abort hook")
	}
	if !claim.owner.Abort() || !called.Load() {
		t.Fatal("owner abort did not invoke installed hook")
	}
	if claim.owner.InstallAbortHook(func() {}) {
		t.Fatal("completed flight accepted a new abort hook")
	}
}

func TestRPCRewrapRegistryIsPlatformAgnosticAndAckBound(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 8)
	claim, err := cache.Acquire([8]byte{4}, 5, 6)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("Acquire owner = %+v, %v", claim, err)
	}
	c := &Conn{authKeyID: [8]byte{4}, sessionID: 5}
	r := newRPCRewrapRegistry(8)
	body := []byte{1, 2, 3, 4}
	if !r.register(c, body, 6, "test.method", claim.owner) {
		t.Fatal("protocol candidate was rejected without platform metadata")
	}
	candidate := r.claim(c, body)
	if candidate == nil {
		t.Fatal("exact protocol fingerprint did not match")
	}
	if got := r.claim(c, body); got != nil {
		t.Fatal("one candidate was claimed by two rewrapped requests")
	}
	r.release(candidate)
	if got := r.claim(&Conn{authKeyID: [8]byte{7}, sessionID: 5}, body); got != nil {
		t.Fatal("fingerprint crossed the auth-key boundary")
	}
	if got := r.claim(c, []byte{4, 3, 2, 1}); got != nil {
		t.Fatal("mismatched TL bytes claimed a candidate")
	}
	if got := r.claim(c, body); got != candidate {
		t.Fatal("released candidate was lost from the fingerprint index")
	}
	r.release(candidate)
	if r.total != 1 {
		t.Fatalf("released claim retired candidate: total=%d", r.total)
	}
	r.acknowledge(c, 6)
	if r.total != 0 || len(r.byKey) != 0 || len(r.bySession) != 0 || len(r.byRequest) != 0 {
		t.Fatalf("ACK did not retire every candidate index: total=%d key=%d session=%d request=%d",
			r.total, len(r.byKey), len(r.bySession), len(r.byRequest))
	}
}

func TestInitRewrapAfterWritingReplaysWithoutBusinessExecution(t *testing.T) {
	inner, wrapped := encodeRewrapTestRequest(t)
	s := New(Options{RPCGlobalWorkers: 1, RPCGlobalMaxTasks: 16, RPCGlobalMaxBytes: 1 << 20})
	transport := &collectingSessionTransport{}
	key := newTestAuthKey(t)
	c := s.newConn(transport, key, 88, 99)
	defer c.ForceClose()

	const oldReqID, newReqID = int64(3001), int64(4001)
	oldPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: oldReqID, typeID: tg.HelpGetConfigRequestTypeID, body: inner,
	}}}
	defer oldPlan.close()
	if err := s.prepareInboundRPCBatch(context.Background(), c, oldPlan); err != nil {
		t.Fatalf("prepare old request: %v", err)
	}
	oldOwner := oldPlan.rpcOwners[0]
	if got := (&encodedOutboundMessage{delivery: oldOwner.Delivery()}).beginWriting(); got != oldReqID {
		t.Fatalf("old writing target = %d, want %d", got, oldReqID)
	}

	newPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: newReqID, typeID: tg.InvokeWithLayerRequestTypeID, body: wrapped,
	}}}
	defer newPlan.close()
	if err := s.prepareInboundRPCBatch(context.Background(), c, newPlan); err != nil {
		t.Fatalf("prepare rewrapped request: %v", err)
	}
	if len(newPlan.rpcTasks) != 0 || len(newPlan.rewrapAliases) != 1 {
		t.Fatalf("late rewrap dispatched business: tasks=%d aliases=%d", len(newPlan.rpcTasks), len(newPlan.rewrapAliases))
	}
	if err := newPlan.commitRewrapAliases(s); err != nil {
		t.Fatalf("activate late alias: %v", err)
	}

	encoded, err := s.encodeRPCResultContext(context.Background(), c, oldReqID, &mt.RPCError{
		ErrorCode: 400, ErrorMessage: "TEST",
	})
	if err != nil {
		t.Fatalf("encode old result: %v", err)
	}
	encoded.delivery = oldOwner.Delivery()
	if !oldOwner.HandOff() {
		t.Fatal("old owner handoff failed")
	}
	s.rpcResults.Put(c.authKeyID, c.sessionID, oldReqID, encoded)

	deadline := time.Now().Add(2 * time.Second)
	var replayed *encodedOutboundMessage
	for time.Now().Before(deadline) {
		if got, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, newReqID); ok {
			replayed = got
			break
		}
		time.Sleep(time.Millisecond)
	}
	if replayed == nil {
		t.Fatal("late alias did not publish a result under the new request ID")
	}
	if got := int64(binary.LittleEndian.Uint64(replayed.body[4:12])); got != newReqID {
		t.Fatalf("replayed req_msg_id = %d, want %d", got, newReqID)
	}
	if len(transport.snapshot()) != 1 {
		t.Fatalf("late alias physical result count = %d, want 1", len(transport.snapshot()))
	}
}

func TestOutboundAckReturnsRPCRequestIDs(t *testing.T) {
	state := outboundState{
		pending: map[int64]*outboundFrame{
			10: {msgID: 10, reqMsgID: 101},
			20: {msgID: 20},
		},
		byRequest:   map[int64]int64{101: 10},
		maxMessages: 8,
	}
	got := state.ack([]int64{10, 20, 30})
	if len(got) != 1 || got[0] != 101 {
		t.Fatalf("acked request IDs = %v, want [101]", got)
	}
}

func TestInitRewrapAliasesExecutionAndRetargetsQueuedResult(t *testing.T) {
	inner, wrapped := encodeRewrapTestRequest(t)
	s := New(Options{RPCGlobalWorkers: 1, RPCGlobalMaxTasks: 16, RPCGlobalMaxBytes: 1 << 20})
	c := &Conn{
		metrics: NopMetrics{}, authKeyID: [8]byte{9}, authKeyHex: "09",
		sessionID: 77, key: crypto.AuthKey{ID: [8]byte{9}},
	}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	defer c.closeInboundRPCScheduler()

	const oldReqID, newReqID = int64(1001), int64(2001)
	oldPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: oldReqID, typeID: tg.HelpGetConfigRequestTypeID, body: inner,
	}}}
	defer oldPlan.close()
	if err := s.prepareInboundRPCBatch(context.Background(), c, oldPlan); err != nil {
		t.Fatalf("prepare old request: %v", err)
	}
	if len(oldPlan.rpcTasks) != 1 || len(oldPlan.rpcOwners) != 1 {
		t.Fatalf("old admission tasks=%d owners=%d", len(oldPlan.rpcTasks), len(oldPlan.rpcOwners))
	}
	oldOwner := oldPlan.rpcOwners[0]

	newPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: newReqID, typeID: tg.InvokeWithLayerRequestTypeID, body: wrapped,
	}}}
	defer newPlan.close()
	if err := s.prepareInboundRPCBatch(context.Background(), c, newPlan); err != nil {
		t.Fatalf("prepare rewrapped request: %v", err)
	}
	if len(newPlan.rpcTasks) != 0 || len(newPlan.rewrapAliases) != 1 || newPlan.items[0].kind != inboundItemRewrappedRPC {
		t.Fatalf("rewrap admission tasks=%d aliases=%d kind=%d", len(newPlan.rpcTasks), len(newPlan.rewrapAliases), newPlan.items[0].kind)
	}
	if err := newPlan.commitRewrapAliases(s); err != nil {
		t.Fatalf("activate alias: %v", err)
	}

	encoded, err := s.encodeRPCResultContext(context.Background(), c, oldReqID, &mt.RPCError{
		ErrorCode: 400, ErrorMessage: "TEST",
	})
	if err != nil {
		t.Fatalf("encode old result: %v", err)
	}
	encoded.delivery = oldOwner.Delivery()
	encoded.markQueued()
	if got := encoded.beginWriting(); got != newReqID {
		t.Fatalf("physical result target = %d, want new req %d", got, newReqID)
	}
	if !oldOwner.HandOff() {
		t.Fatal("old owner handoff failed")
	}
	encoded.markDelivered()
	s.rpcResults.Put(c.authKeyID, c.sessionID, oldReqID, encoded)

	var (
		aliased *encodedOutboundMessage
		ok      bool
	)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if aliased, ok = s.rpcResults.Get(c.authKeyID, c.sessionID, newReqID); ok {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ok {
		t.Fatal("new req_msg_id result was not completed")
	}
	if got := int64(binary.LittleEndian.Uint64(aliased.body[4:12])); got != newReqID {
		t.Fatalf("cached aliased req_msg_id = %d, want %d", got, newReqID)
	}
	if s.rpcRewrap.total != 0 {
		t.Fatalf("rewrap registry retained %d consumed candidates", s.rpcRewrap.total)
	}
}

func TestRPCRewrapDeliveryJobPanicAndDeadlineReleaseBarrier(t *testing.T) {
	s := &Server{log: zaptest.NewLogger(t)}
	c := &Conn{metrics: NopMetrics{}}
	alias := &rpcRewrapAlias{conn: c}
	// Even a legacy alias with no replacement metadata callback must hold the
	// scheduler barrier until its physical replay reaches a terminal state.
	alias.beginReplayRestore()
	if alias.finishReplayRestore == nil {
		t.Fatal("rewrap alias did not install its replay restore barrier")
	}

	job := s.rpcRewrapRestoreJob(alias, "panic regression", func(*rpcRewrapDeliveryControl, time.Time) {
		panic("boom")
	})
	runRPCRewrapDeliveryJob(job)

	c.rpcMu.Lock()
	pending := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pending != 0 {
		t.Fatalf("replay restore barriers after panic = %d, want 0", pending)
	}
	if !c.isRetired() {
		t.Fatal("panic in a rewrap job did not fence the partially restored connection")
	}

	var ran atomic.Bool
	var deadlineErr error
	runRPCRewrapDeliveryJob(rpcRewrapDeliveryJob{
		deadline: time.Now().Add(-time.Millisecond),
		run:      func(*rpcRewrapDeliveryControl, time.Time) { ran.Store(true) },
		fail:     func(err error) { deadlineErr = err },
	})
	if ran.Load() {
		t.Fatal("expired rewrap job ran after its absolute queue deadline")
	}
	if !errors.Is(deadlineErr, context.DeadlineExceeded) {
		t.Fatalf("expired rewrap job error = %v, want context deadline", deadlineErr)
	}

	// Recovery is per job: a panic must not poison the worker loop's next item.
	runRPCRewrapDeliveryJob(rpcRewrapDeliveryJob{
		deadline: time.Now().Add(time.Second),
		run:      func(*rpcRewrapDeliveryControl, time.Time) { ran.Store(true) },
	})
	if !ran.Load() {
		t.Fatal("rewrap worker did not remain usable after a recovered panic")
	}
}

func TestExpiredRPCRewrapResultJobPublishesCompletedAliasExactlyOnce(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	s := &Server{log: zaptest.NewLogger(t), rpcResults: cache}
	c := &Conn{
		metrics:   NopMetrics{},
		authKeyID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		sessionID: 7001,
	}
	const reqMsgID = int64(8001)

	claim, err := cache.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil {
		t.Fatalf("acquire aliased result owner: %v", err)
	}
	if claim.state != rpcResultAcquireOwner || claim.owner == nil {
		t.Fatalf("aliased result claim = %#v, want owner", claim)
	}
	if !claim.owner.CompleteExecution(true) {
		t.Fatal("complete aliased result execution")
	}
	encoded := encodedRPCResultForPriorityTest(reqMsgID, 0)
	encoded.delivery = claim.owner.Delivery()
	alias := &rpcRewrapAlias{
		conn:     c,
		newReqID: reqMsgID,
		method:   "help.getConfig",
		newOwner: claim.owner,
	}
	alias.finishReplayRestore = c.beginRPCReplayRestore()

	var ran atomic.Bool
	job := s.rpcRewrapRestoreJob(alias, "expired aliased result", func(*rpcRewrapDeliveryControl, time.Time) {
		ran.Store(true)
	})
	job.deadline = time.Now().Add(-time.Millisecond)
	job.fail = func(err error) {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("expired result job error = %v, want context deadline", err)
		}
		s.failRPCRewrapResultJob(alias, encoded, err)
	}
	runRPCRewrapDeliveryJob(job)

	if ran.Load() {
		t.Fatal("expired aliased result job executed")
	}
	if !c.isRetired() {
		t.Fatal("expired aliased result job did not fence its connection")
	}
	c.rpcMu.Lock()
	pendingRestores := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pendingRestores != 0 {
		t.Fatalf("replay restore barriers = %d, want 0", pendingRestores)
	}
	if used := cache.flightLimit.snapshot(); used != 0 {
		t.Fatalf("pending result flights = %d, want 0", used)
	}

	completed, ok := cache.Get(c.authKeyID, c.sessionID, reqMsgID)
	if !ok || completed != encoded {
		t.Fatalf("completed aliased result = (%p, %v), want (%p, true)", completed, ok, encoded)
	}
	replay, err := cache.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil {
		t.Fatalf("reacquire completed aliased result: %v", err)
	}
	if replay.state != rpcResultAcquireCompleted || replay.encoded != encoded ||
		!replay.executionKnown || !replay.executionOK {
		t.Fatalf("completed aliased result metadata = %#v", replay)
	}

	// A defensive duplicate failure report must not republish or underflow the
	// completed flight. The first handoff/cache completion is the sole winner.
	s.failRPCRewrapResultJob(alias, encoded, context.DeadlineExceeded)
	if used := cache.flightLimit.snapshot(); used != 0 {
		t.Fatalf("pending result flights after duplicate failure = %d, want 0", used)
	}
	if got, ok := cache.Get(c.authKeyID, c.sessionID, reqMsgID); !ok || got != encoded {
		t.Fatalf("completed result changed after duplicate failure = (%p, %v)", got, ok)
	}
}

func TestRetargetedRPCRestoreIsOrderedAndIndependentOfGlobalHookExecutor(t *testing.T) {
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

	s := New(Options{})
	c := &Conn{
		metrics:   NopMetrics{},
		authKeyID: [8]byte{9, 8, 7, 6, 5, 4, 3, 2},
		sessionID: 9101,
	}
	const oldReqID, newReqID = int64(9201), int64(9202)
	oldClaim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, oldReqID)
	if err != nil || oldClaim.state != rpcResultAcquireOwner {
		t.Fatalf("old claim = %#v, err=%v", oldClaim, err)
	}
	body := []byte{1, 3, 3, 7}
	if !s.rpcRewrap.register(c, body, oldReqID, "help.getConfig", oldClaim.owner) {
		t.Fatal("register source rewrap candidate")
	}
	candidate := s.rpcRewrap.claim(c, body)
	if candidate == nil {
		t.Fatal("claim source rewrap candidate")
	}
	newClaim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, newReqID)
	if err != nil || newClaim.state != rpcResultAcquireOwner {
		t.Fatalf("new claim = %#v, err=%v", newClaim, err)
	}

	var order atomic.Int32
	alias := &rpcRewrapAlias{
		conn: c, newReqID: newReqID, method: "help.getConfig",
		oldWaiter: oldClaim.owner.Waiter(), newOwner: newClaim.owner,
		sourceConn: c, sourceOwner: oldClaim.owner,
		candidate: candidate, registry: s.rpcRewrap,
		afterSuccessfulDelivery: func() error {
			if !order.CompareAndSwap(0, 1) {
				return errors.New("replacement restore ran out of order")
			}
			return nil
		},
	}
	if err := alias.activate(s); err != nil {
		t.Fatalf("activate retarget alias: %v", err)
	}
	if !alias.retargeted.Load() {
		t.Fatal("source result was not retargeted")
	}

	encoded := encodedRPCResultForPriorityTest(oldReqID, 0)
	encoded.delivery = oldClaim.owner.Delivery()
	encoded.setDeliveryHook(func() {
		if !order.CompareAndSwap(1, 2) {
			panic("logical hook did not run after replacement restore")
		}
	})
	if !oldClaim.owner.CompleteExecution(true) {
		t.Fatal("complete source execution")
	}
	if !oldClaim.owner.HandOff() {
		t.Fatal("handoff source result")
	}
	encoded.markQueued()
	if got := encoded.beginWriting(); got != newReqID {
		t.Fatalf("retargeted physical req_msg_id = %d, want %d", got, newReqID)
	}
	encoded.markDelivered()
	s.rpcResults.Put(c.authKeyID, c.sessionID, oldReqID, encoded)

	deadline := time.Now().Add(time.Second)
	for order.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := order.Load(); got != 2 {
		t.Fatalf("retarget restore order = %d, want replacement then logical", got)
	}
	c.rpcMu.Lock()
	pending := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pending != 0 {
		t.Fatalf("retarget restore barriers = %d, want 0", pending)
	}
	if _, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, newReqID); !ok {
		t.Fatal("retargeted result was not cached under new req_msg_id")
	}
}

func TestRPCRewrapWatchdogReleasesQueuedBarrierWhenAllWorkersAreStuck(t *testing.T) {
	var (
		once sync.Once
		jobs chan rpcRewrapDeliveryJob
	)
	started := make(chan struct{}, rpcRewrapDeliveryWorkers)
	release := make(chan struct{})
	done := make(chan struct{}, rpcRewrapDeliveryWorkers)
	for range rpcRewrapDeliveryWorkers {
		if !scheduleRPCRewrapJob(rpcRewrapDeliveryJob{
			deadline: time.Now().Add(time.Second),
			run: func(*rpcRewrapDeliveryControl, time.Time) {
				started <- struct{}{}
				<-release // Deliberately ignores its deadline.
				done <- struct{}{}
			},
		}, &once, &jobs, rpcRewrapDeliveryWorkers, 8) {
			t.Fatal("schedule blocking rewrap worker")
		}
	}
	for range rpcRewrapDeliveryWorkers {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("rewrap workers did not all block")
		}
	}

	s := &Server{log: zaptest.NewLogger(t)}
	c := &Conn{metrics: NopMetrics{}}
	alias := &rpcRewrapAlias{conn: c}
	alias.beginReplayRestore()
	var ran atomic.Bool
	job := s.rpcRewrapRestoreJob(alias, "queued watchdog regression", func(*rpcRewrapDeliveryControl, time.Time) {
		ran.Store(true)
	})
	job.deadline = time.Now().Add(30 * time.Millisecond)
	if !scheduleRPCRewrapJob(job, &once, &jobs, rpcRewrapDeliveryWorkers, 8) {
		t.Fatal("schedule watched rewrap job")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.rpcMu.Lock()
		pending := c.rpcReplayRestores
		c.rpcMu.Unlock()
		if c.isRetired() && pending == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	c.rpcMu.Lock()
	pending := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if !c.isRetired() || pending != 0 {
		t.Fatalf("watchdog terminal state = retired:%v barriers:%d", c.isRetired(), pending)
	}
	close(release)
	for range rpcRewrapDeliveryWorkers {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("blocking rewrap worker did not exit")
		}
	}
	// Give a worker the opportunity to dequeue the expired job. Its atomic
	// terminal state must suppress the late run entirely.
	time.Sleep(10 * time.Millisecond)
	if ran.Load() {
		t.Fatal("expired queued rewrap job ran after watchdog failure")
	}
}

type deliveredThenBlockedTransport struct {
	collectingSessionTransport
	delivered chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newDeliveredThenBlockedTransport() *deliveredThenBlockedTransport {
	return &deliveredThenBlockedTransport{
		delivered: make(chan struct{}),
		release:   make(chan struct{}),
	}
}

func (t *deliveredThenBlockedTransport) Send(ctx context.Context, b *bin.Buffer) error {
	if err := t.collectingSessionTransport.Send(ctx, b); err != nil {
		return err
	}
	t.once.Do(func() { close(t.delivered) })
	<-t.release // Simulate a transport that reports success late and ignores ctx.
	return nil
}

func TestRPCRewrapPhysicalSuccessAfterWatchdogStillRunsLogicalRestore(t *testing.T) {
	s := New(Options{WriteTimeout: time.Second})
	transport := newDeliveredThenBlockedTransport()
	defer func() {
		select {
		case <-transport.release:
		default:
			close(transport.release)
		}
	}()
	key := newTestAuthKey(t)
	c := s.newConn(transport, key, 9401, 1)
	legacyCanonicalTestConn(t, c)
	t.Cleanup(c.ForceClose)
	const reqMsgID = int64(9402)
	claim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("rewrap claim = %#v, err=%v", claim, err)
	}
	if !claim.owner.CompleteExecution(true) {
		t.Fatal("complete rewrap execution")
	}

	var order atomic.Int32
	encoded := encodedRPCResultForPriorityTest(reqMsgID, 0)
	encoded.delivery = claim.owner.Delivery()
	encoded.setDeliveryHook(func() {
		if !order.CompareAndSwap(1, 2) {
			panic("late physical success lost ordered logical restore")
		}
	})
	alias := &rpcRewrapAlias{
		conn: c, newReqID: reqMsgID, method: "help.getConfig", newOwner: claim.owner,
		afterSuccessfulDelivery: func() error {
			if !order.CompareAndSwap(0, 1) {
				return errors.New("late replacement restore ran out of order")
			}
			return nil
		},
	}
	alias.executionOK.Store(true)
	alias.beginReplayRestore()

	var (
		once sync.Once
		jobs chan rpcRewrapDeliveryJob
	)
	done := make(chan struct{})
	job := s.rpcRewrapRestoreJob(alias, "late physical success regression", func(control *rpcRewrapDeliveryControl, deadline time.Time) {
		s.publishRewrappedRPCResult(c, reqMsgID, alias.method, claim.owner, encoded, alias, control, deadline)
		close(done)
	})
	job.deadline = time.Now().Add(30 * time.Millisecond)
	job.fail = func(err error) { s.failRPCRewrapResultJob(alias, encoded, err) }
	if !scheduleRPCRewrapJob(job, &once, &jobs, 1, 1) {
		t.Fatal("schedule late-success rewrap job")
	}
	select {
	case <-transport.delivered:
	case <-time.After(time.Second):
		t.Fatal("transport did not reach physical delivery gate")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.rpcMu.Lock()
		pending := c.rpcReplayRestores
		c.rpcMu.Unlock()
		if c.isRetired() && pending == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := order.Load(); got != 0 {
		t.Fatalf("restore ran before transport reported terminal success: %d", got)
	}
	close(transport.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("late successful physical attempt did not finish")
	}
	if got := order.Load(); got != 2 {
		t.Fatalf("late physical success restore order = %d, want 2", got)
	}
	if got := len(transport.snapshot()); got != 1 {
		t.Fatalf("late physical success writes = %d, want 1", got)
	}
}

func TestConcurrentRPCRewrapDeliveredFinalizationPublishesOnceWithMetadata(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	s := &Server{log: zaptest.NewLogger(t), rpcResults: cache}
	c := &Conn{
		metrics:   NopMetrics{},
		authKeyID: [8]byte{4, 4, 4, 4, 4, 4, 4, 4},
		sessionID: 9501,
	}
	const reqMsgID = int64(9502)
	claim, err := cache.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("finalization claim = %#v, err=%v", claim, err)
	}
	if !claim.owner.CompleteExecution(true) || !claim.owner.HandOff() {
		t.Fatal("prepare finalization owner")
	}
	var subscribers atomic.Int32
	if err := claim.owner.Waiter().Subscribe(func(*encodedOutboundMessage, bool) {
		subscribers.Add(1)
	}); err != nil {
		t.Fatalf("subscribe finalization probe: %v", err)
	}

	var replacement, logical atomic.Int32
	encoded := encodedRPCResultForPriorityTest(reqMsgID, 0)
	encoded.delivery = claim.owner.Delivery()
	encoded.setDeliveryHook(func() { logical.Add(1) })
	alias := &rpcRewrapAlias{
		conn: c, newReqID: reqMsgID, method: "help.getConfig", newOwner: claim.owner,
		afterSuccessfulDelivery: func() error {
			replacement.Add(1)
			return nil
		},
	}
	alias.executionOK.Store(true)
	alias.beginReplayRestore()

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, source := range []string{"worker", "watchdog"} {
		wg.Add(1)
		go func(source string) {
			defer wg.Done()
			<-start
			_ = s.completeDeliveredRPCRewrapResult(context.Background(), alias, encoded, source)
		}(source)
	}
	close(start)
	wg.Wait()

	if got := replacement.Load(); got != 1 {
		t.Fatalf("replacement finalizations = %d, want 1", got)
	}
	if got := logical.Load(); got != 1 {
		t.Fatalf("logical finalizations = %d, want 1", got)
	}
	if got := subscribers.Load(); got != 1 {
		t.Fatalf("cache subscriber calls = %d, want 1", got)
	}
	replay, err := cache.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || replay.state != rpcResultAcquireCompleted ||
		!replay.executionKnown || !replay.executionOK {
		t.Fatalf("completed finalization metadata = %#v, err=%v", replay, err)
	}
	c.rpcMu.Lock()
	pending := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pending != 0 {
		t.Fatalf("finalization barriers = %d, want 0", pending)
	}
}

func TestRPCRewrapSubscriberPanicCannotLoseClaimedLogicalHook(t *testing.T) {
	s := New(Options{WriteTimeout: time.Second})
	transport := &collectingSessionTransport{}
	key := newTestAuthKey(t)
	c := s.newConn(transport, key, 9301, 1)
	legacyCanonicalTestConn(t, c)
	t.Cleanup(c.ForceClose)
	const reqMsgID = int64(9302)
	claim, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("alias claim = %#v, err=%v", claim, err)
	}
	if !claim.owner.CompleteExecution(true) {
		t.Fatal("complete alias execution")
	}
	if err := claim.owner.Waiter().Subscribe(func(*encodedOutboundMessage, bool) {
		panic("subscriber boom")
	}); err != nil {
		t.Fatalf("subscribe panic probe: %v", err)
	}

	var order atomic.Int32
	encoded := encodedRPCResultForPriorityTest(reqMsgID, 0)
	encoded.delivery = claim.owner.Delivery()
	encoded.setDeliveryHook(func() {
		if !order.CompareAndSwap(1, 2) {
			panic("logical hook did not run after replacement restore")
		}
	})
	alias := &rpcRewrapAlias{
		conn: c, newReqID: reqMsgID, method: "help.getConfig", newOwner: claim.owner,
		afterSuccessfulDelivery: func() error {
			if !order.CompareAndSwap(0, 1) {
				return errors.New("replacement restore ran out of order")
			}
			return nil
		},
	}
	alias.executionOK.Store(true)
	alias.beginReplayRestore()
	job := s.rpcRewrapRestoreJob(alias, "subscriber panic regression", func(control *rpcRewrapDeliveryControl, deadline time.Time) {
		s.publishRewrappedRPCResult(c, reqMsgID, alias.method, claim.owner, encoded, alias, control, deadline)
	})
	job.fail = func(err error) { s.failRPCRewrapResultJob(alias, encoded, err) }
	runRPCRewrapDeliveryJob(job)

	if got := order.Load(); got != 2 {
		t.Fatalf("restore order after subscriber panic = %d, want 2", got)
	}
	if !c.isRetired() {
		t.Fatal("subscriber panic did not fence the connection")
	}
	c.rpcMu.Lock()
	pending := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pending != 0 {
		t.Fatalf("restore barriers after subscriber panic = %d, want 0", pending)
	}
	if cached, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, reqMsgID); !ok || cached != encoded {
		t.Fatalf("completed result after subscriber panic = (%p, %v), want (%p, true)", cached, ok, encoded)
	}
}
