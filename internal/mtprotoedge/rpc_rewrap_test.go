package mtprotoedge

import (
	"context"
	"encoding/binary"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/tg"
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

	aliased, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, newReqID)
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
