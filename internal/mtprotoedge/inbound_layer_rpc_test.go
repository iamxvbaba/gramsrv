package mtprotoedge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/rpc"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func exactLayerRPCBody(t *testing.T, request bin.Encoder) []byte {
	t.Helper()
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Copy()
}

func exactOutboundLayerRPCBody(t *testing.T, profile tlprofile.Profile, request bin.Object) []byte {
	t.Helper()
	var body bin.Buffer
	if err := tlprofile.EncodeObject(profile, request, &body); err != nil {
		t.Fatal(err)
	}
	return body.Copy()
}

type admissionOnlyLayerRPC struct {
	dispatcher *tlprofile.Dispatcher
	mu         sync.Mutex
	published  []publishedLayerEvidence
}

type orderedAdmissionOnlyLayerRPC struct {
	*admissionOnlyLayerRPC
	exactMu sync.Mutex
	exact   map[orderedTestSessionKey]struct {
		layer int
		msgID int64
	}
}

type orderedTestSessionKey struct {
	authKeyID [8]byte
	sessionID int64
}

type publishedLayerEvidence struct {
	authKeyID    [8]byte
	sessionID    int64
	msgID        int64
	admissionSeq uint64
	safeFloor    uint64
	layer        int
}

type exactProfileCapacityTestError struct{}

func (exactProfileCapacityTestError) Error() string                { return "exact profile capacity" }
func (exactProfileCapacityTestError) ExactSessionProfileCapacity() {}

type layerDurabilityUnavailableTestError struct{}

func (layerDurabilityUnavailableTestError) Error() string                       { return "layer durability unavailable" }
func (layerDurabilityUnavailableTestError) LayerEvidenceDurabilityUnavailable() {}

type capacityAdmissionOnlyLayerRPC struct {
	*orderedAdmissionOnlyLayerRPC
}

type unavailableDurableAdmissionLayerRPC struct {
	*orderedAdmissionOnlyLayerRPC
}

type unavailableDurableSeedLayerRPC struct {
	*admissionOnlyLayerRPC
	resolveCalls   atomic.Int32
	inheritedCalls atomic.Int32
}

type unavailableEdgeSessionLayerStore struct {
	err error
}

type countingEdgeSessionLayerStore struct {
	store.AuthKeySessionLayerStore
	getCalls atomic.Int32
}

func (s *countingEdgeSessionLayerStore) GetSessionLayer(
	ctx context.Context,
	rawAuthKeyID [8]byte,
	sessionID int64,
) (store.AuthKeySessionLayer, bool, error) {
	s.getCalls.Add(1)
	return s.AuthKeySessionLayerStore.GetSessionLayer(ctx, rawAuthKeyID, sessionID)
}

func (s unavailableEdgeSessionLayerStore) GetSessionLayer(context.Context, [8]byte, int64) (store.AuthKeySessionLayer, bool, error) {
	return store.AuthKeySessionLayer{}, false, s.err
}

func (s unavailableEdgeSessionLayerStore) AdvanceSessionLayer(context.Context, [8]byte, int64, int, int64) (store.AuthKeySessionLayer, bool, error) {
	return store.AuthKeySessionLayer{}, false, s.err
}

func (s unavailableEdgeSessionLayerStore) DeleteSessionLayer(context.Context, [8]byte, int64) (bool, error) {
	return false, s.err
}

func (s unavailableEdgeSessionLayerStore) DeleteExpiredSessionLayers(context.Context, int) (int, error) {
	return 0, s.err
}

type dispatchProfileCaptureRouter struct {
	*rpc.Router
	mu             sync.Mutex
	requestProfile tlprofile.Profile
	resultProfile  tlprofile.Profile
}

func (h *dispatchProfileCaptureRouter) DispatchAdmitted(
	ctx context.Context,
	authKeyID [8]byte,
	sessionID int64,
	msgID int64,
	admissionSeq uint64,
	request tlprofile.Admission,
) (tlprofile.Result, string, error) {
	result, method, err := h.Router.DispatchAdmitted(ctx, authKeyID, sessionID, msgID, admissionSeq, request)
	h.mu.Lock()
	h.requestProfile = request.Call().Profile()
	if result != nil {
		h.resultProfile = result.Prepared().Call().Profile()
	}
	h.mu.Unlock()
	return result, method, err
}

func (h *dispatchProfileCaptureRouter) profiles() (tlprofile.Profile, tlprofile.Profile) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requestProfile, h.resultProfile
}

func (*unavailableDurableAdmissionLayerRPC) ResolveNegotiatedSessionLayerEvidence(
	context.Context, [8]byte, int64,
) (int, int64, bool, error) {
	return 0, 0, false, nil
}

func (h *unavailableDurableSeedLayerRPC) ResolveNegotiatedSessionLayerEvidence(
	context.Context, [8]byte, int64,
) (int, int64, bool, error) {
	h.resolveCalls.Add(1)
	return 0, 0, false, layerDurabilityUnavailableTestError{}
}

func (h *unavailableDurableSeedLayerRPC) ResolveInheritedAuthKeyLayer(
	context.Context, [8]byte,
) (int, bool, error) {
	h.inheritedCalls.Add(1)
	return 0, false, layerDurabilityUnavailableTestError{}
}

func (*unavailableDurableAdmissionLayerRPC) AdvanceNegotiatedSessionLayerEvidence(
	context.Context, [8]byte, int64, int, int64,
) (int, int64, bool, error) {
	return 0, 0, false, layerDurabilityUnavailableTestError{}
}

func (h *capacityAdmissionOnlyLayerRPC) FreezeNegotiatedSessionLayerAt([8]byte, int64, int, int64) (bool, error) {
	return false, exactProfileCapacityTestError{}
}

type replayProfileCaptureLayerRPC struct {
	*admissionOnlyLayerRPC
	mu       sync.Mutex
	profiles []tlprofile.Profile
	known    []bool
}

func (h *replayProfileCaptureLayerRPC) PrepareAdmittedReplay(
	_ context.Context,
	_ [8]byte,
	_ int64,
	_ int64,
	_ uint64,
	request tlprofile.Admission,
) (func() error, error) {
	profile, known := request.EffectiveProfile()
	h.mu.Lock()
	h.profiles = append(h.profiles, profile)
	h.known = append(h.known, known)
	h.mu.Unlock()
	return nil, nil
}

func (h *replayProfileCaptureLayerRPC) capturedProfiles() ([]tlprofile.Profile, []bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]tlprofile.Profile(nil), h.profiles...), append([]bool(nil), h.known...)
}

func newAdmissionOnlyLayerRPC() *admissionOnlyLayerRPC {
	return &admissionOnlyLayerRPC{dispatcher: tlprofile.NewDispatcher()}
}

func newOrderedAdmissionOnlyLayerRPC() *orderedAdmissionOnlyLayerRPC {
	return &orderedAdmissionOnlyLayerRPC{
		admissionOnlyLayerRPC: newAdmissionOnlyLayerRPC(),
		exact: make(map[orderedTestSessionKey]struct {
			layer int
			msgID int64
		}),
	}
}

func (h *orderedAdmissionOnlyLayerRPC) NegotiatedSessionLayerEvidence(authKeyID [8]byte, sessionID int64) (int, int64, bool) {
	h.exactMu.Lock()
	defer h.exactMu.Unlock()
	entry, ok := h.exact[orderedTestSessionKey{authKeyID: authKeyID, sessionID: sessionID}]
	return entry.layer, entry.msgID, ok
}

func (h *orderedAdmissionOnlyLayerRPC) FreezeNegotiatedSessionLayerAt(authKeyID [8]byte, sessionID int64, layer int, msgID int64) (bool, error) {
	h.exactMu.Lock()
	defer h.exactMu.Unlock()
	key := orderedTestSessionKey{authKeyID: authKeyID, sessionID: sessionID}
	current, ok := h.exact[key]
	if ok {
		if msgID < current.msgID {
			return false, nil
		}
		if msgID == current.msgID {
			if layer != current.layer {
				return false, fmt.Errorf("same msg_id selected two layers")
			}
			return false, nil
		}
	}
	h.exact[key] = struct {
		layer int
		msgID int64
	}{layer: layer, msgID: msgID}
	return true, nil
}

func (h *admissionOnlyLayerRPC) AdmitLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	return h.dispatcher.Admit(profile, b, limits)
}

func (h *admissionOnlyLayerRPC) AdmitDefaultLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	return h.dispatcher.AdmitDefault(profile, b, limits)
}

func (h *admissionOnlyLayerRPC) AdmitUnprofiled(b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	return h.dispatcher.AdmitUnprofiled(b, limits)
}

func (*admissionOnlyLayerRPC) DispatchAdmitted(context.Context, [8]byte, int64, int64, uint64, tlprofile.Admission) (tlprofile.Result, string, error) {
	return nil, "", fmt.Errorf("admission-only handler")
}

func (h *admissionOnlyLayerRPC) PublishAdmittedLayerProfileEvidence(
	_ context.Context,
	authKeyID [8]byte,
	sessionID int64,
	msgID int64,
	admissionSeq uint64,
	safeFloor uint64,
	layer int,
) error {
	h.mu.Lock()
	h.published = append(h.published, publishedLayerEvidence{
		authKeyID: authKeyID, sessionID: sessionID, msgID: msgID,
		admissionSeq: admissionSeq, safeFloor: safeFloor, layer: layer,
	})
	h.mu.Unlock()
	return nil
}

func (h *admissionOnlyLayerRPC) publications() []publishedLayerEvidence {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]publishedLayerEvidence(nil), h.published...)
}

func TestNestedExplicitLayerAdmissionErrorsAreNotDefaultFailures(t *testing.T) {
	handler := newAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	state := LayerProfileSnapshot{Profile: tlprofile.Profile227, Origin: LayerProfileInherited}

	unsupported := exactLayerRPCBody(t, &tg.InvokeAfterMsgRequest{
		MsgID: 1,
		Query: &tg.InvokeWithLayerRequest{
			Layer: 229,
			Query: &tg.HelpGetConfigRequest{},
		},
	})
	conflict := exactLayerRPCBody(t, &tg.InvokeAfterMsgsRequest{
		MsgIDs: []int64{1, 2},
		Query: &tg.InvokeWithLayerRequest{
			Layer: 225,
			Query: &tg.InvokeWithoutUpdatesRequest{Query: &tg.InvokeWithLayerRequest{
				Layer: 227,
				Query: &tg.HelpGetConfigRequest{},
			}},
		},
	})
	var truncatedSelector bin.Buffer
	truncatedSelector.PutID(tg.InvokeWithoutUpdatesRequestTypeID)
	truncatedSelector.PutID(tg.InvokeAfterMsgRequestTypeID)
	truncatedSelector.PutLong(3)
	truncatedSelector.PutID(tg.InvokeWithLayerRequestTypeID)
	var malformedSelectedQuery bin.Buffer
	malformedSelectedQuery.PutID(tg.InvokeAfterMsgRequestTypeID)
	malformedSelectedQuery.PutLong(4)
	malformedSelectedQuery.PutID(tg.InvokeWithLayerRequestTypeID)
	malformedSelectedQuery.PutInt(225)
	malformedSelectedQuery.PutID(tg.MessagesGetHistoryRequestTypeID)

	for _, test := range []struct {
		name string
		body []byte
	}{
		{name: "unsupported", body: unsupported},
		{name: "conflict", body: conflict},
		{name: "truncated_selector", body: truncatedSelector.Copy()},
		{name: "malformed_selected_query", body: malformedSelectedQuery.Copy()},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := s.decodeInboundLayerRPC(state, test.body)
			if err == nil {
				t.Fatal("malformed explicit admission unexpectedly succeeded")
			}
			if errors.Is(err, errDefaultLayerAdmission) {
				t.Fatalf("explicit admission was misclassified as stale default: %v", err)
			}
			switch test.name {
			case "unsupported":
				if !strings.Contains(err.Error(), "unsupported exact profile 229") {
					t.Fatalf("unsupported selector error = %v", err)
				}
			case "conflict":
				if !errors.Is(err, tlprofile.ErrProfileConflict) {
					t.Fatalf("conflicting selector error = %v", err)
				}
			}
		})
	}

	var nakedMalformed bin.Buffer
	nakedMalformed.PutID(tg.MessagesGetHistoryRequestTypeID)
	if _, _, err := s.decodeInboundLayerRPC(state, nakedMalformed.Copy()); !errors.Is(err, errDefaultLayerAdmission) {
		t.Fatalf("naked malformed request error = %v, want stale-default classification", err)
	}
}

func TestBatchProvisionalCursorKeepsRegistryWatermarkAcrossOldReplay(t *testing.T) {
	handler := newOrderedAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	authKeyID := [8]byte{0x31, 0x01}
	const sessionID = int64(3101)
	if _, err := handler.FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, 227, 104); err != nil {
		t.Fatal(err)
	}
	c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	if err := c.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	defer c.Close()

	oldBody := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 225, Query: &tg.HelpGetConfigRequest{}})
	oldAdmitted, _, err := s.decodeInboundLayerRPC(
		LayerProfileSnapshot{Profile: tlprofile.Profile225, Origin: LayerProfileExplicit}, oldBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	oldClaim, err := s.rpcResults.AcquireLayerIdentified(
		authKeyID, sessionID, 100, oldAdmitted.Call().Profile(), oldAdmitted.Prepared().Identity(),
	)
	if err != nil || oldClaim.owner == nil {
		t.Fatalf("old owner err=%v", err)
	}
	oldClaim.owner.CompleteExecution(true)
	s.rpcResults.Put(authKeyID, sessionID, 100, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 100})

	nakedBody := exactOutboundLayerRPCBody(t, tlprofile.Profile227, &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 1,
	})
	plan := &inboundPlan{items: []inboundItem{
		{kind: inboundItemRPC, msgID: 100, body: oldBody},
		{kind: inboundItemRPC, msgID: 108, body: nakedBody},
	}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if plan.items[0].kind != inboundItemReplayRPC {
		t.Fatalf("old explicit item kind=%d, want completed replay", plan.items[0].kind)
	}
	if profile, ok := s.rpcResults.ExactAdmissionProfile(authKeyID, sessionID, 108); !ok || profile != tlprofile.Profile227 {
		t.Fatalf("following naked admission profile = (%d,%v), want registry Layer 227", profile, ok)
	}
}

func TestBatchProvisionalCursorUsesPendingNewerExplicitEvidence(t *testing.T) {
	handler := newAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	authKeyID := [8]byte{0x31, 0x02}
	const sessionID = int64(3102)
	c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	if err := c.seedOrderedLayerProfile(tlprofile.Profile225, 100); err != nil {
		t.Fatal(err)
	}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	defer c.Close()

	explicitBody := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetConfigRequest{}})
	explicit, _, err := s.decodeInboundLayerRPC(
		LayerProfileSnapshot{Profile: tlprofile.Profile227, Origin: LayerProfileExplicit}, explicitBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.rpcResults.AcquireLayerIdentified(
		authKeyID, sessionID, 104, explicit.Call().Profile(), explicit.Prepared().Identity(),
	)
	if err != nil || pending.owner == nil {
		t.Fatalf("pending explicit owner err=%v", err)
	}
	defer pending.owner.Abort()

	nakedBody := exactOutboundLayerRPCBody(t, tlprofile.Profile227, &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 1,
	})
	plan := &inboundPlan{items: []inboundItem{
		{kind: inboundItemRPC, msgID: 104, body: explicitBody},
		{kind: inboundItemRPC, msgID: 108, body: nakedBody},
	}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if plan.items[0].kind != inboundItemRewrappedRPC {
		t.Fatalf("pending explicit item kind=%d, want pending replay", plan.items[0].kind)
	}
	if profile, ok := s.rpcResults.ExactAdmissionProfile(authKeyID, sessionID, 108); !ok || profile != tlprofile.Profile227 {
		t.Fatalf("following naked admission profile = (%d,%v), want pending Layer 227", profile, ok)
	}
	if state, msgID := c.layerProfileEvidenceState(); state.Profile != tlprofile.Profile227 || state.Origin != LayerProfileExplicit || msgID != 104 {
		t.Fatalf("pending full-identity evidence was not committed = %#v msgID:%d", state, msgID)
	}
	if got := handler.publications(); len(got) != 0 {
		t.Fatalf("pending join published auth-key default: %#v", got)
	}
}

func TestBatchProvisionalCursorRejectsSameMsgIDLayerConflict(t *testing.T) {
	handler := newOrderedAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	authKeyID := [8]byte{0x31, 0x03}
	const sessionID = int64(3103)
	if _, err := handler.FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, 225, 104); err != nil {
		t.Fatal(err)
	}
	c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	defer c.Close()
	plan := &inboundPlan{items: []inboundItem{{
		kind:  inboundItemRPC,
		msgID: 104,
		body:  exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetConfigRequest{}}),
	}}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); !errors.Is(err, ErrLayerProfileConflict) {
		t.Fatalf("same-msg_id Layer conflict err=%v, want %v", err, ErrLayerProfileConflict)
	}
}

func TestFutureExactLayerWatermarkAllowsOnlyNewerSupportedSelfHeal(t *testing.T) {
	handler := newOrderedAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	authKeyID := [8]byte{0x31, 0x04}
	const sessionID = int64(3104)
	if _, err := handler.FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, 229, 100); err != nil {
		t.Fatal(err)
	}
	c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	if err := c.SeedInheritedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	defer c.Close()

	oldPlan := &inboundPlan{items: []inboundItem{
		{
			kind: inboundItemRPC, msgID: 96,
			body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetConfigRequest{}}),
		},
		{
			kind: inboundItemRPC, msgID: 104,
			body: exactOutboundLayerRPCBody(t, tlprofile.Profile227, &tg.MessagesGetHistoryRequest{
				Peer: &tg.InputPeerSelf{}, Limit: 1,
			}),
		},
	}}
	defer oldPlan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, oldPlan); err != nil {
		t.Fatal(err)
	}
	if oldPlan.items[1].kind != inboundItemRPCAdmissionError {
		t.Fatalf("naked non-invariant after future watermark kind=%d, want admission error", oldPlan.items[1].kind)
	}
	if state, rawLayer, msgID := c.layerProfileRawEvidenceState(); state.Origin != LayerProfileUnknown || rawLayer != 229 || msgID != 100 {
		t.Fatalf("future raw watermark = %#v raw:%d msgID:%d", state, rawLayer, msgID)
	}
	if got := handler.publications(); len(got) != 0 {
		t.Fatalf("older supported selector published shared default: %#v", got)
	}

	conflict := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: 100,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetConfigRequest{}}),
	}}}
	defer conflict.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, conflict); !errors.Is(err, ErrLayerProfileConflict) {
		t.Fatalf("same-msg future Layer conflict err=%v, want %v", err, ErrLayerProfileConflict)
	}

	newPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: 108,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetNearestDCRequest{}}),
	}}}
	defer newPlan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, newPlan); err != nil {
		t.Fatal(err)
	}
	if state, rawLayer, msgID := c.layerProfileRawEvidenceState(); state.Profile != tlprofile.Profile227 || state.Origin != LayerProfileExplicit || rawLayer != 227 || msgID != 108 {
		t.Fatalf("newer supported self-heal = %#v raw:%d msgID:%d", state, rawLayer, msgID)
	}
	if got := handler.publications(); len(got) != 1 || got[0].layer != 227 || got[0].msgID != 108 {
		t.Fatalf("self-heal publication = %#v", got)
	}
}

func TestExactProfileRegistryCapacityBecomesBoundedRPCAdmission(t *testing.T) {
	handler := &capacityAdmissionOnlyLayerRPC{orderedAdmissionOnlyLayerRPC: newOrderedAdmissionOnlyLayerRPC()}
	s := New(Options{DC: 2, LayerRPC: handler})
	c := &Conn{authKeyID: [8]byte{0x31, 0x05}, sessionID: 3105, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	defer c.Close()
	plan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: 100,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetConfigRequest{}}),
	}}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatalf("capacity escaped as connection error: %v", err)
	}
	if plan.items[0].kind != inboundItemCapacityError || len(plan.rpcTasks) != 0 || plan.rpcReservation != nil {
		t.Fatalf("capacity plan = kind:%d tasks:%d reservation:%v", plan.items[0].kind, len(plan.rpcTasks), plan.rpcReservation != nil)
	}
	if got := handler.publications(); len(got) != 0 {
		t.Fatalf("capacity-rejected evidence published: %#v", got)
	}
	if state := c.LayerProfileState(); state.Origin != LayerProfileUnknown {
		t.Fatalf("capacity rejection mutated Conn profile: %#v", state)
	}
}

func TestDurabilityOutageKeepsExplicitLayerConnectionLocal(t *testing.T) {
	handler := &unavailableDurableAdmissionLayerRPC{orderedAdmissionOnlyLayerRPC: newOrderedAdmissionOnlyLayerRPC()}
	s := New(Options{DC: 2, LayerRPC: handler})
	c := &Conn{authKeyID: [8]byte{0x31, 0x07}, sessionID: 3107, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	defer c.Close()
	plan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: 100,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetConfigRequest{}}),
	}}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatalf("durability outage rejected explicit request: %v", err)
	}
	if plan.items[0].kind != inboundItemRPC || len(plan.rpcTasks) != 1 {
		t.Fatalf("local-only plan = kind:%d tasks:%d", plan.items[0].kind, len(plan.rpcTasks))
	}
	if !plan.items[0].profileEvidenceFresh() {
		t.Fatal("durability fallback incorrectly disabled current-connection wrapper effects")
	}
	if state, msgID := c.layerProfileEvidenceState(); state.Profile != tlprofile.Profile227 || state.Origin != LayerProfileExplicit || msgID != 100 {
		t.Fatalf("connection-local evidence = %#v msgID:%d", state, msgID)
	}
	if _, _, found := handler.NegotiatedSessionLayerEvidence(c.authKeyID, c.sessionID); found {
		t.Fatal("durability fallback polluted exact registry")
	}
	if got := handler.publications(); len(got) != 0 {
		t.Fatalf("durability fallback published auth-key default: %#v", got)
	}
}

func TestDurabilityOutageInitializesOnlyCurrentConnection(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("layer primary unavailable")
	manager := NewSessionManager(zaptest.NewLogger(t))
	router := rpc.New(
		rpc.Config{DC: 2},
		rpc.Deps{
			Sessions:             manager,
			AuthKeySessionLayers: unavailableEdgeSessionLayerStore{err: boom},
		},
		zaptest.NewLogger(t),
		clock.System,
	)
	s := New(Options{DC: 2, LayerRPC: router, ActiveSessions: manager})
	c := newOutboundTestConn(t, &collectingSessionTransport{}, newOutboundTrackedBudget(1<<20))
	c.authKeyID = [8]byte{0x31, 0x08}
	c.sessionID = 3108
	// newOutboundTestConn installs a canonical profile for generic outbound
	// tests. This scenario starts before any selector, so reset that fixture-only
	// state before exposing the Conn to Router/SessionManager.
	c.layerProfileMu.Lock()
	c.layerProfileState.Store(0)
	c.layerProfileEvidenceLayer = 0
	c.layerProfileEvidenceMsgID = 0
	c.setLegacyClientLayer(0)
	c.layerProfileMu.Unlock()
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	if err := manager.Register(c); err != nil {
		t.Fatal(err)
	}
	defer manager.Unregister(c)
	manager.BindAuthKeyForSession(c.authKeyID, c.sessionID, c.authKeyID)
	manager.BindUserForAuthKey(c.authKeyID, c.sessionID, 42)
	// Production wires Channels and marks this after its empty/non-empty
	// membership snapshot. This focused test has no channel service, so seed the
	// independent membership half of the readiness predicate.
	c.membershipsSynced.Store(true)

	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	plan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: msgID,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
			Layer: 227,
			Query: &tg.InitConnectionRequest{
				APIID:          6,
				DeviceModel:    "Desktop",
				SystemVersion:  "Windows",
				AppVersion:     "outage-test",
				SystemLangCode: "en",
				LangPack:       "tdesktop",
				LangCode:       "en",
				Query:          &tg.HelpGetConfigRequest{},
			},
		}),
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(ctx, c, plan); err != nil {
		t.Fatalf("explicit init during outage: %v", err)
	}
	if len(plan.rpcTasks) != 1 || !plan.items[0].profileEvidenceFresh() {
		t.Fatalf("outage init plan = tasks:%d fresh:%v", len(plan.rpcTasks), plan.items[0].profileEvidenceFresh())
	}
	if !c.rpcRewrapInitialized.Load() {
		t.Fatal("current connection did not retain successful init wrapper state")
	}
	if state, evidenceMsgID := c.layerProfileEvidenceState(); state.Profile != tlprofile.Profile227 || state.Origin != LayerProfileExplicit || evidenceMsgID != msgID {
		t.Fatalf("current connection profile = %#v msg:%d", state, evidenceMsgID)
	}
	if _, _, found := router.NegotiatedSessionLayerEvidence(c.authKeyID, c.sessionID); found {
		t.Fatal("outage-local selector entered Router exact-session cache")
	}

	if err := plan.rpcTasks[0].run(ctx); err != nil {
		t.Fatalf("execute outage-local init: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !manager.ReceivesUpdatesForAuthKey(c.authKeyID, c.sessionID) {
		if time.Now().After(deadline) {
			t.Fatal("successful outage-local init never made current session updates-ready")
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := s.rpcResults.Get(c.authKeyID, c.sessionID, msgID); !ok {
		t.Fatal("successful outage-local init did not publish its exact RPC result")
	}
	if layer, found, err := router.ResolveInheritedAuthKeyLayer(ctx, c.authKeyID); err != nil || found || layer != 0 {
		t.Fatalf("outage-local init leaked auth-key Layer default = (%d,%v,%v)", layer, found, err)
	}

	// Proactive updates use the current connection's local wire profile even
	// though no exact/auth-key durable seed was published.
	fanout, err := newLayerUpdatesFanout(testLayerUpdatesValue(123))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := fanout.prepareForConn(ctx, c)
	if err != nil || encoded.layer == nil || encoded.layer.profile != tlprofile.Profile227 {
		t.Fatalf("outage-local push profile = encoded:%#v err:%v", encoded, err)
	}

	// Simulate a new logical session on the same physical transport. Passing the
	// old Conn snapshot must not leak the outage-local selector across sessions.
	replacement := &Conn{authKeyID: c.authKeyID, sessionID: c.sessionID + 1, metrics: NopMetrics{}}
	if err := s.seedInitialLayerProfile(ctx, replacement, 0, c.LayerProfileState()); err != nil {
		t.Fatal(err)
	}
	if state := replacement.LayerProfileState(); state.Origin != LayerProfileUnknown {
		t.Fatalf("new session inherited outage-local profile: %#v", state)
	}
	naked := exactOutboundLayerRPCBody(t, tlprofile.Profile227, &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 1,
	})
	if _, _, err := s.admitInboundLayerRPCAt(replacement, msgID+4, naked); err == nil {
		t.Fatal("new session admitted profile-dependent naked RPC without its own selector")
	}
}

func TestInvariantReplayNeverCachesInternalCanonicalProfile(t *testing.T) {
	handler := &replayProfileCaptureLayerRPC{admissionOnlyLayerRPC: newAdmissionOnlyLayerRPC()}
	s := New(Options{DC: 2, LayerRPC: handler})
	authKeyID := [8]byte{0x31, 0x06}
	const sessionID = int64(3106)
	newConn := func() *Conn {
		c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
		c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
		return c
	}
	body := exactLayerRPCBody(t, &tg.AuthBindTempAuthKeyRequest{
		PermAuthKeyID: 1, Nonce: 2, ExpiresAt: 3, EncryptedMessage: []byte("bind"),
	})

	original := newConn()
	defer original.Close()
	first := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
	defer first.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), original, first); err != nil {
		t.Fatal(err)
	}
	owner, _ := first.items[0].payload.(*rpcResultOwnerLease)
	if owner == nil {
		t.Fatal("invariant request did not acquire owner")
	}
	if profile, ok := s.rpcResults.ExactAdmissionProfile(authKeyID, sessionID, 100); ok || profile != 0 {
		t.Fatalf("pending invariant cached profile=(%d,%v)", profile, ok)
	}

	replacement := newConn()
	defer replacement.Close()
	pending := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
	defer pending.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), replacement, pending); err != nil {
		t.Fatal(err)
	}
	if pending.items[0].kind != inboundItemRewrappedRPC || replacement.LayerProfileState().Origin != LayerProfileUnknown {
		t.Fatalf("unknown pending replay = kind:%d state:%#v", pending.items[0].kind, replacement.LayerProfileState())
	}
	profiles, known := handler.capturedProfiles()
	if len(known) != 1 || known[0] || profiles[0] != 0 {
		t.Fatalf("unknown pending replay effective profiles=%v known=%v", profiles, known)
	}

	owner.CompleteExecution(true)
	s.rpcResults.Put(authKeyID, sessionID, 100, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 100})
	if profile, ok := s.rpcResults.ExactAdmissionProfile(authKeyID, sessionID, 100); ok || profile != 0 {
		t.Fatalf("completed invariant cached profile=(%d,%v)", profile, ok)
	}
	profiled := newConn()
	defer profiled.Close()
	if err := profiled.seedOrderedLayerProfile(tlprofile.Profile225, 104); err != nil {
		t.Fatal(err)
	}
	completed := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
	defer completed.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), profiled, completed); err != nil {
		t.Fatal(err)
	}
	if completed.items[0].kind != inboundItemReplayRPC {
		t.Fatalf("profiled invariant completed replay kind=%d", completed.items[0].kind)
	}
	if state, msgID := profiled.layerProfileEvidenceState(); state.Profile != tlprofile.Profile225 || state.Origin != LayerProfileExplicit || msgID != 104 {
		t.Fatalf("invariant replay polluted explicit profile = %#v msgID:%d", state, msgID)
	}
}

func TestSameMsgIDNakedReplayUsesWinnerAdmissionProfile(t *testing.T) {
	handler := newAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	s.rpcResults = newRPCResultCacheWithFlightLimit(time.Now, 8)
	authKeyID := [8]byte{0x22, 0x99}
	const sessionID = int64(2299)
	body := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 1,
	})

	item220 := inboundItem{msgID: 100, body: body}
	var err error
	item220.admitted, item220.method, err = s.decodeInboundLayerRPC(
		LayerProfileSnapshot{Profile: tlprofile.Profile225, Origin: LayerProfileInherited}, body,
	)
	if err != nil {
		t.Fatal(err)
	}
	item227 := inboundItem{msgID: 100, body: body}
	item227.admitted, item227.method, err = s.decodeInboundLayerRPC(
		LayerProfileSnapshot{Profile: tlprofile.Profile227, Origin: LayerProfileInherited}, body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if item220.admitted.Prepared().Identity() == item227.admitted.Prepared().Identity() {
		t.Fatal("test request identity is invariant; need profile-dependent result grammar")
	}
	c220 := &Conn{authKeyID: authKeyID, sessionID: sessionID}
	c227 := &Conn{authKeyID: authKeyID, sessionID: sessionID}
	winner, err := s.acquireAdmittedLayerRPC(c220, &item220, nil)
	if err != nil || winner.state != rpcResultAcquireOwner || winner.owner == nil {
		t.Fatalf("winner = state:%d err:%v", winner.state, err)
	}
	loser, err := s.acquireAdmittedLayerRPC(c227, &item227, nil)
	if err != nil || loser.state != rpcResultAcquirePending || loser.admissionSeq != winner.admissionSeq {
		t.Fatalf("loser join = state:%d seq:%d err:%v, winner seq:%d", loser.state, loser.admissionSeq, err, winner.admissionSeq)
	}
	if got := item227.admitted.Call().Profile(); got != tlprofile.Profile225 {
		t.Fatalf("loser re-admitted profile = %d, want winner 225", got)
	}

	changed := inboundItem{msgID: 100, body: exactLayerRPCBody(t, &tg.HelpGetNearestDCRequest{})}
	changed.admitted, changed.method, err = s.decodeInboundLayerRPC(
		LayerProfileSnapshot{Profile: tlprofile.Profile225, Origin: LayerProfileInherited}, changed.body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.acquireAdmittedLayerRPC(c220, &changed, nil); !errors.Is(err, ErrRPCResultIdentityMismatch) {
		t.Fatalf("same-msg_id changed body err=%v, want identity mismatch", err)
	}
	winner.owner.Abort()
}

func TestInheritedLayerServesRepeatedNakedRPCsWithoutSelectorRefresh(t *testing.T) {
	for _, profile := range []tlprofile.Profile{tlprofile.Profile225, tlprofile.Profile227} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			s := New(Options{DC: 2, LayerRPC: newAdmissionOnlyLayerRPC()})
			c := &Conn{authKeyID: [8]byte{0x71, byte(profile)}, sessionID: int64(profile), metrics: NopMetrics{}}
			if err := c.SeedInheritedLayerProfile(profile); err != nil {
				t.Fatalf("seed inherited profile: %v", err)
			}
			initial := c.LayerProfileState()
			body := exactOutboundLayerRPCBody(t, profile, &tg.MessagesGetHistoryRequest{
				Peer: &tg.InputPeerSelf{}, Limit: 1,
			})
			for i := 0; i < 512; i++ {
				admitted, method, err := s.admitInboundLayerRPC(c, body)
				if err != nil {
					t.Fatalf("naked admission %d: %v", i, err)
				}
				if method != "messages.getHistory" || admitted.Call().Profile() != profile {
					t.Fatalf("naked admission %d = method:%q profile:%d", i, method, admitted.Call().Profile())
				}
				if _, explicit := admitted.ProfileEvidence(); explicit {
					t.Fatalf("naked admission %d fabricated explicit Layer evidence", i)
				}
			}
			if got := c.LayerProfileState(); got != initial || got.Origin != LayerProfileInherited {
				t.Fatalf("repeated naked traffic changed inherited profile: got %#v want %#v", got, initial)
			}
		})
	}
}

func TestLayerEvidencePublicationBelongsOnlyToFreshFlightOwner(t *testing.T) {
	handler := newAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	authKeyID := [8]byte{0x22, 0x98}
	const sessionID = int64(2298)
	body := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
		Layer: 225, Query: &tg.HelpGetConfigRequest{},
	})
	newConn := func() *Conn {
		c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
		c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
		return c
	}
	firstConn := newConn()
	defer firstConn.Close()
	first := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
	defer first.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), firstConn, first); err != nil {
		t.Fatal(err)
	}
	publications := handler.publications()
	if len(publications) != 1 || publications[0].admissionSeq == 0 || publications[0].msgID != 100 || publications[0].layer != 225 {
		t.Fatalf("fresh owner publications = %#v", publications)
	}
	if first.items[0].admissionSeq != publications[0].admissionSeq {
		t.Fatalf("task seq=%d publication seq=%d", first.items[0].admissionSeq, publications[0].admissionSeq)
	}

	replayConn := newConn()
	defer replayConn.Close()
	replay := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
	defer replay.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), replayConn, replay); err != nil {
		t.Fatal(err)
	}
	if replay.items[0].kind != inboundItemRewrappedRPC || replay.items[0].admissionSeq != publications[0].admissionSeq {
		t.Fatalf("pending replay = kind:%d seq:%d", replay.items[0].kind, replay.items[0].admissionSeq)
	}
	if got := handler.publications(); len(got) != 1 {
		t.Fatalf("pending replay republished Layer evidence: %#v", got)
	}
}

func TestLayerEvidenceNotPublishedWhenBatchFlightCapacityRollsBack(t *testing.T) {
	handler := newAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	s.rpcResults = newRPCResultCacheWithFlightLimit(time.Now, 1)
	c := &Conn{authKeyID: [8]byte{0x22, 0x97}, sessionID: 2297, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	defer c.Close()
	plan := &inboundPlan{items: []inboundItem{
		{kind: inboundItemRPC, msgID: 100, body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 225, Query: &tg.HelpGetConfigRequest{}})},
		{kind: inboundItemRPC, msgID: 104, body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 225, Query: &tg.HelpGetNearestDCRequest{}})},
	}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if got := handler.publications(); len(got) != 0 {
		t.Fatalf("capacity-rejected batch published evidence: %#v", got)
	}
	for index := range plan.items {
		if plan.items[index].kind != inboundItemCapacityError {
			t.Fatalf("item %d kind=%d, want capacity error", index, plan.items[index].kind)
		}
	}
}

func TestOldCompletedLayerRequestCannotRollBackCorrectedSession(t *testing.T) {
	handler := newOrderedAdmissionOnlyLayerRPC()
	s := New(Options{DC: 2, LayerRPC: handler})
	authKeyID := [8]byte{0x22, 0x96}
	const sessionID = int64(2296)
	newConn := func() *Conn {
		c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
		c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
		return c
	}
	c := newConn()
	defer c.Close()
	oldBody := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 225, Query: &tg.HelpGetConfigRequest{}})
	oldPlan := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: oldBody}}}
	defer oldPlan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, oldPlan); err != nil {
		t.Fatal(err)
	}
	oldOwner, _ := oldPlan.items[0].payload.(*rpcResultOwnerLease)
	if oldOwner == nil {
		t.Fatal("old request did not acquire owner")
	}
	oldOwner.CompleteExecution(true)
	s.rpcResults.Put(authKeyID, sessionID, 100, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 100})

	correctPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: 104,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetNearestDCRequest{}}),
	}}}
	defer correctPlan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, correctPlan); err != nil {
		t.Fatal(err)
	}
	if state, msgID := c.layerProfileEvidenceState(); state.Profile != tlprofile.Profile227 || msgID != 104 {
		t.Fatalf("corrected Conn = %#v msgID:%d", state, msgID)
	}

	replacement := newConn()
	defer replacement.Close()
	if err := s.seedInitialLayerProfile(context.Background(), replacement, 0, LayerProfileSnapshot{}); err != nil {
		t.Fatal(err)
	}
	replay := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: oldBody}}}
	defer replay.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), replacement, replay); err != nil {
		t.Fatal(err)
	}
	if replay.items[0].kind != inboundItemReplayRPC {
		t.Fatalf("old request kind=%d, want completed replay", replay.items[0].kind)
	}
	if state, msgID := replacement.layerProfileEvidenceState(); state.Profile != tlprofile.Profile227 || msgID != 104 {
		t.Fatalf("old replay rolled replacement back = %#v msgID:%d", state, msgID)
	}
	if layer, msgID, ok := handler.NegotiatedSessionLayerEvidence(authKeyID, sessionID); !ok || layer != 227 || msgID != 104 {
		t.Fatalf("old replay rolled registry back = (%d,%d,%v)", layer, msgID, ok)
	}
}

func TestLogicalSessionLayerWatermarkSurvivesResultExpiryAndOldContainer(t *testing.T) {
	now := newExpiryTestClock(time.Unix(1_900_000_000, 0))
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), now)
	s := New(Options{DC: 2, LayerRPC: router, Clock: now})
	s.rpcResults = newRPCResultCacheWithFlightLimit(now.Now, 8)
	authKeyID := [8]byte{0x22, 0x95}
	const sessionID = int64(2295)
	newConn := func() *Conn {
		c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
		c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
		return c
	}

	ids := proto.NewMessageIDGen(now.Now)
	oldMsgID := ids.New(proto.MessageFromClient)
	correctedMsgID := ids.New(proto.MessageFromClient)
	original := newConn()
	defer original.Close()
	oldBody := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 225, Query: &tg.HelpGetConfigRequest{}})
	oldPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: oldMsgID, body: oldBody,
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	defer oldPlan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), original, oldPlan); err != nil {
		t.Fatal(err)
	}
	oldOwner, _ := oldPlan.items[0].payload.(*rpcResultOwnerLease)
	if oldOwner == nil || !oldOwner.CompleteExecution(true) {
		t.Fatal("old Layer 225 request did not establish a completed owner")
	}
	s.rpcResults.Put(authKeyID, sessionID, oldMsgID, &encodedOutboundMessage{body: []byte{1}, reqMsgID: oldMsgID})

	correctPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: correctedMsgID,
		body:                          exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: &tg.HelpGetNearestDCRequest{}}),
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	defer correctPlan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), original, correctPlan); err != nil {
		t.Fatal(err)
	}
	if layer, msgID, ok := router.NegotiatedSessionLayerEvidence(authKeyID, sessionID); !ok || layer != 227 || msgID != correctedMsgID {
		t.Fatalf("corrected logical-session watermark = (%d,%d,%v)", layer, msgID, ok)
	}

	// Exact-session evidence and completed results are bounded. The auth-key
	// inherited default remains Layer 227, while an old inner request may execute
	// request-bound but cannot recreate or roll back the exact-session watermark.
	now.Advance(10 * time.Minute)
	if _, ok := s.rpcResults.Get(authKeyID, sessionID, oldMsgID); ok {
		t.Fatal("old completed result did not expire")
	}

	replacement := newConn()
	defer replacement.Close()
	if err := s.seedInitialLayerProfile(context.Background(), replacement, 0, LayerProfileSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if state, msgID := replacement.layerProfileEvidenceState(); state.Profile != tlprofile.Profile227 || state.Origin != LayerProfileInherited || msgID != 0 {
		t.Fatalf("replacement seed = %#v msgID:%d, want inherited Layer 227", state, msgID)
	}
	freshMsgID := proto.NewMessageIDGen(now.Now).New(proto.MessageFromClient)
	nakedBody := exactOutboundLayerRPCBody(t, tlprofile.Profile227, &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 1,
	})
	replayPlan := &inboundPlan{items: []inboundItem{
		{
			kind: inboundItemRPC, msgID: oldMsgID, body: oldBody,
			layerProfileEvidenceFreshness: inboundLayerProfileEvidenceRequestBound,
		},
		{
			kind: inboundItemRPC, msgID: freshMsgID, body: nakedBody,
			layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
		},
	}}
	defer replayPlan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), replacement, replayPlan); err != nil {
		t.Fatal(err)
	}
	if profile, ok := s.rpcResults.ExactAdmissionProfile(authKeyID, sessionID, freshMsgID); !ok || profile != tlprofile.Profile227 {
		t.Fatalf("naked request after expired old replay = (%d,%v), want Layer 227", profile, ok)
	}
	if state, msgID := replacement.layerProfileEvidenceState(); state.Profile != tlprofile.Profile227 || state.Origin != LayerProfileInherited || msgID != 0 {
		t.Fatalf("old request-bound flight rolled replacement back = %#v msgID:%d", state, msgID)
	}
	if _, _, ok := router.NegotiatedSessionLayerEvidence(authKeyID, sessionID); ok {
		t.Fatal("request-bound old selector recreated expired exact-session evidence")
	}
}

func TestDurableLayerEvidenceRestoresAcrossEdgeRouterRestart(t *testing.T) {
	ctx := context.Background()
	now := newExpiryTestClock(time.Now().UTC().Truncate(time.Second).Add(123_456_780 * time.Nanosecond))
	keys := memory.NewAuthKeyStore()
	authKeyID := [8]byte{0xd0, 0x22, 0x70}
	const sessionID = int64(227_001)
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}

	firstRouter := rpc.New(
		rpc.Config{DC: 2},
		rpc.Deps{AuthKeySessionLayers: keys},
		zaptest.NewLogger(t),
		now,
	)
	firstEdge := New(Options{DC: 2, LayerRPC: firstRouter, Clock: now})
	firstConn := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	firstConn.startInboundRPCScheduler(firstEdge.rpcScheduler, 1, 8, time.Second)
	defer firstConn.Close()
	msgIDs := proto.NewMessageIDGen(now.Now)
	selectorMsgID := msgIDs.New(proto.MessageFromClient)
	firstPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: selectorMsgID,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
			Layer: 225,
			Query: &tg.HelpGetConfigRequest{},
		}),
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	defer firstPlan.close()
	if err := firstEdge.prepareInboundLayerRPCBatch(ctx, firstConn, firstPlan); err != nil {
		t.Fatal(err)
	}
	durable, found, err := keys.GetSessionLayer(ctx, authKeyID, sessionID)
	if err != nil || !found || durable.Layer != 225 || durable.MessageID != selectorMsgID {
		t.Fatalf("durable evidence = (%+v,%v,%v)", durable, found, err)
	}

	// Recreate both Router and edge to prove the result is not coming from either
	// process-local exact-profile registry or the completed-result cache.
	restartedRouter := rpc.New(
		rpc.Config{DC: 2},
		rpc.Deps{AuthKeySessionLayers: keys},
		zaptest.NewLogger(t),
		now,
	)
	restartedEdge := New(Options{DC: 2, LayerRPC: restartedRouter, Clock: now})
	replacement := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	replacement.startInboundRPCScheduler(restartedEdge.rpcScheduler, 1, 8, time.Second)
	defer replacement.Close()
	if err := restartedEdge.seedInitialLayerProfile(ctx, replacement, 0, LayerProfileSnapshot{}); err != nil {
		t.Fatal(err)
	}
	state, rawLayer, evidenceMsgID := replacement.layerProfileRawEvidenceState()
	if state.Profile != tlprofile.Profile225 || state.Origin != LayerProfileExplicit || rawLayer != 225 || evidenceMsgID != selectorMsgID {
		t.Fatalf("restart seed = state:%#v raw:%d msg:%d", state, rawLayer, evidenceMsgID)
	}

	nakedMsgID := msgIDs.New(proto.MessageFromClient)
	nakedPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: nakedMsgID,
		body: exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.MessagesGetHistoryRequest{
			Peer: &tg.InputPeerSelf{}, Limit: 1,
		}),
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	defer nakedPlan.close()
	if err := restartedEdge.prepareInboundLayerRPCBatch(ctx, replacement, nakedPlan); err != nil {
		t.Fatal(err)
	}
	profile, profiled := restartedEdge.rpcResults.ExactAdmissionProfile(authKeyID, sessionID, nakedMsgID)
	if len(nakedPlan.rpcTasks) != 1 || !profiled || profile != tlprofile.Profile225 {
		t.Fatalf("restart naked admission = tasks:%d item:%+v", len(nakedPlan.rpcTasks), nakedPlan.items[0])
	}
}

func TestPhysicalConnectionReadsDurableLayerOnceBeforeAdmissionHotPath(t *testing.T) {
	ctx := context.Background()
	now := newExpiryTestClock(time.Now().UTC().Truncate(time.Second).Add(123_456_780 * time.Nanosecond))
	keys := memory.NewAuthKeyStore()
	authKeyID := [8]byte{0xda, 0x22, 0x70}
	const sessionID = int64(227_003)
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	msgIDs := proto.NewMessageIDGen(now.Now)
	selectorMsgID := msgIDs.New(proto.MessageFromClient)
	if current, applied, err := keys.AdvanceSessionLayer(ctx, authKeyID, sessionID, 225, selectorMsgID); err != nil || !applied || current.Layer != 225 {
		t.Fatalf("seed durable evidence = current:%#v applied:%v err:%v", current, applied, err)
	}
	counted := &countingEdgeSessionLayerStore{AuthKeySessionLayerStore: keys}
	router := rpc.New(
		rpc.Config{DC: 2}, rpc.Deps{AuthKeySessionLayers: counted}, zaptest.NewLogger(t), now,
	)
	edge := New(Options{DC: 2, LayerRPC: router, Clock: now})
	c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(edge.rpcScheduler, 1, 8, time.Second)
	defer c.Close()
	if err := edge.seedInitialLayerProfile(ctx, c, 0, LayerProfileSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if got := counted.getCalls.Load(); got != 1 {
		t.Fatalf("connection seed GetSessionLayer calls=%d, want 1", got)
	}

	body := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	for i := 0; i < 64; i++ {
		plan := &inboundPlan{items: []inboundItem{{
			kind: inboundItemRPC, msgID: msgIDs.New(proto.MessageFromClient), body: body,
			layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
		}}}
		if err := edge.prepareInboundLayerRPCBatch(ctx, c, plan); err != nil {
			plan.close()
			t.Fatalf("batch %d: %v", i, err)
		}
		plan.close()
	}
	if got := counted.getCalls.Load(); got != 1 {
		t.Fatalf("64 admission batches raised GetSessionLayer calls to %d, want 1", got)
	}
}

func TestDurableExactSeedOutageKeepsFetchedAuthKeyDefaultServing(t *testing.T) {
	handler := &unavailableDurableSeedLayerRPC{admissionOnlyLayerRPC: newAdmissionOnlyLayerRPC()}
	edge := New(Options{DC: 2, LayerRPC: handler})
	c := &Conn{
		authKeyID: [8]byte{0xdc, 0x22, 0x70}, sessionID: 227_004,
		authKeyExpiresAt: 0, metrics: NopMetrics{},
	}
	c.startInboundRPCScheduler(edge.rpcScheduler, 1, 8, time.Second)
	defer c.Close()
	if err := edge.seedInitialLayerProfile(context.Background(), c, 225, LayerProfileSnapshot{}); err != nil {
		t.Fatal(err)
	}
	initial := c.LayerProfileState()
	if initial.Profile != tlprofile.Profile225 || initial.Origin != LayerProfileInherited {
		t.Fatalf("outage seed discarded fetched auth-key default: %#v", initial)
	}

	msgIDs := proto.NewMessageIDGen(time.Now)
	body := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 1,
	})
	for i := 0; i < 64; i++ {
		plan := &inboundPlan{items: []inboundItem{{
			kind: inboundItemRPC, msgID: msgIDs.New(proto.MessageFromClient), body: body,
			layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
		}}}
		if err := edge.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
			plan.close()
			t.Fatalf("naked batch %d: %v", i, err)
		}
		plan.close()
	}
	if got := c.LayerProfileState(); got != initial {
		t.Fatalf("naked traffic changed outage fallback: got %#v want %#v", got, initial)
	}
	if got := handler.resolveCalls.Load(); got != 1 {
		t.Fatalf("durable resolver calls=%d after 64 batches, want only initial seed", got)
	}
	if got := handler.inheritedCalls.Load(); got != 0 {
		t.Fatalf("permanent fetched default unexpectedly queried inherited resolver %d times", got)
	}
}

func TestBoundTempSeedOutageKeepsRawFetchedLayerServingCurrentConn(t *testing.T) {
	handler := &unavailableDurableSeedLayerRPC{admissionOnlyLayerRPC: newAdmissionOnlyLayerRPC()}
	edge := New(Options{DC: 2, LayerRPC: handler})
	c := &Conn{
		authKeyID: [8]byte{0xdc, 0x22, 0x71}, sessionID: 227_005,
		authKeyExpiresAt: 1_900_000_000, metrics: NopMetrics{},
	}
	c.startInboundRPCScheduler(edge.rpcScheduler, 1, 8, time.Second)
	defer c.Close()
	if err := edge.seedInitialLayerProfile(context.Background(), c, 225, LayerProfileSnapshot{}); err != nil {
		t.Fatal(err)
	}
	initial := c.LayerProfileState()
	if initial.Profile != tlprofile.Profile225 || initial.Origin != LayerProfileInherited {
		t.Fatalf("bound-temp outage discarded same-frame raw default: %#v", initial)
	}

	msgIDs := proto.NewMessageIDGen(time.Now)
	body := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 1,
	})
	for i := 0; i < 64; i++ {
		plan := &inboundPlan{items: []inboundItem{{
			kind: inboundItemRPC, msgID: msgIDs.New(proto.MessageFromClient), body: body,
			layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
		}}}
		if err := edge.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
			plan.close()
			t.Fatalf("naked batch %d: %v", i, err)
		}
		plan.close()
	}
	if got := c.LayerProfileState(); got != initial {
		t.Fatalf("naked traffic changed bound-temp outage shadow: got %#v want %#v", got, initial)
	}
	if got := handler.resolveCalls.Load(); got != 1 {
		t.Fatalf("exact durable resolver calls=%d after 64 batches, want only initial seed", got)
	}
	if got := handler.inheritedCalls.Load(); got != 1 {
		t.Fatalf("inherited durable resolver calls=%d after 64 batches, want only initial seed", got)
	}
	if got := handler.publications(); len(got) != 0 {
		t.Fatalf("raw outage shadow leaked into shared Layer publication: %#v", got)
	}
}

func TestLiveConnectionKeepsFrozenLayerUntilItsOwnExplicitCorrection(t *testing.T) {
	ctx := context.Background()
	now := newExpiryTestClock(time.Now().UTC().Truncate(time.Second).Add(123_456_780 * time.Nanosecond))
	keys := memory.NewAuthKeyStore()
	authKeyID := [8]byte{0xdb, 0x22, 0x70}
	const sessionID = int64(227_002)
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	routerA := rpc.New(
		rpc.Config{DC: 2}, rpc.Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), now,
	)
	handlerA := &dispatchProfileCaptureRouter{Router: routerA}
	edgeA := New(Options{DC: 2, LayerRPC: handlerA, Clock: now})
	connA := newOutboundTestConn(t, &collectingSessionTransport{}, newOutboundTrackedBudget(1<<20))
	connA.authKeyID = authKeyID
	connA.sessionID = sessionID
	connA.startInboundRPCScheduler(edgeA.rpcScheduler, 1, 8, time.Second)
	msgIDs := proto.NewMessageIDGen(now.Now)

	oldMsgID := msgIDs.New(proto.MessageFromClient)
	oldPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: oldMsgID,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
			Layer: 225,
			Query: &tg.HelpGetConfigRequest{},
		}),
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	if err := edgeA.prepareInboundLayerRPCBatch(ctx, connA, oldPlan); err != nil {
		oldPlan.close()
		t.Fatal(err)
	}
	oldPlan.close()
	if state, raw, msgID := connA.layerProfileRawEvidenceState(); state.Profile != tlprofile.Profile225 || raw != 225 || msgID != oldMsgID {
		t.Fatalf("A initial profile = state:%#v raw:%d msg:%d", state, raw, msgID)
	}

	// Another server process advances the same logical-session durable watermark.
	// It must not silently rewrite A's already-live physical connection or add a
	// PostgreSQL read to every inbound batch. A replacement connection reads the
	// durable 227 seed; A keeps serving 225 until its own wire corrects itself.
	routerB := rpc.New(
		rpc.Config{DC: 2}, rpc.Deps{AuthKeySessionLayers: keys}, zaptest.NewLogger(t), now,
	)
	remoteMsgID := msgIDs.New(proto.MessageFromClient)
	if layer, msgID, _, err := routerB.AdvanceNegotiatedSessionLayerEvidence(ctx, authKeyID, sessionID, 227, remoteMsgID); err != nil || layer != 227 || msgID != remoteMsgID {
		t.Fatalf("B durable advance = (%d,%d,%v)", layer, msgID, err)
	}

	oldNakedMsgID := msgIDs.New(proto.MessageFromClient)
	oldNakedPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: oldNakedMsgID,
		body:                          exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{}),
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	if err := edgeA.prepareInboundLayerRPCBatch(ctx, connA, oldNakedPlan); err != nil {
		oldNakedPlan.close()
		t.Fatal(err)
	}
	if state, raw, msgID := connA.layerProfileRawEvidenceState(); state.Profile != tlprofile.Profile225 || raw != 225 || msgID != oldMsgID {
		oldNakedPlan.close()
		t.Fatalf("remote durable advance rewrote live A = state:%#v raw:%d msg:%d", state, raw, msgID)
	}
	if profile, ok := edgeA.rpcResults.ExactAdmissionProfile(authKeyID, sessionID, oldNakedMsgID); !ok || profile != tlprofile.Profile225 {
		oldNakedPlan.close()
		t.Fatalf("old naked admission profile = (%d,%v), want 225", profile, ok)
	}
	oldNakedPlan.close()

	// A naked constructor that requires the newer grammar is rejected with the
	// normal correction path; it still must not mutate A's frozen snapshot.
	mismatchMsgID := msgIDs.New(proto.MessageFromClient)
	mismatchPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: mismatchMsgID,
		body: exactOutboundLayerRPCBody(t, tlprofile.Profile227, &tg.ChannelsJoinChannelRequest{
			Channel: &tg.InputChannelEmpty{},
		}),
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	if err := edgeA.prepareInboundLayerRPCBatch(ctx, connA, mismatchPlan); err != nil {
		mismatchPlan.close()
		t.Fatal(err)
	}
	if mismatchPlan.items[0].kind != inboundItemRPCAdmissionError {
		mismatchPlan.close()
		t.Fatalf("new naked grammar kind=%d, want admission error", mismatchPlan.items[0].kind)
	}
	mismatchPlan.close()
	if state, raw, msgID := connA.layerProfileRawEvidenceState(); state.Profile != tlprofile.Profile225 || raw != 225 || msgID != oldMsgID {
		t.Fatalf("failed naked correction mutated A = state:%#v raw:%d msg:%d", state, raw, msgID)
	}

	correctionMsgID := msgIDs.New(proto.MessageFromClient)
	correctionPlan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: correctionMsgID,
		body: exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
			Layer: 227, Query: &tg.HelpGetConfigRequest{},
		}),
		layerProfileEvidenceFreshness: inboundLayerProfileEvidenceFresh,
	}}}
	defer correctionPlan.close()
	if err := edgeA.prepareInboundLayerRPCBatch(ctx, connA, correctionPlan); err != nil {
		t.Fatal(err)
	}
	if state, raw, msgID := connA.layerProfileRawEvidenceState(); state.Profile != tlprofile.Profile227 || raw != 227 || msgID != correctionMsgID {
		t.Fatalf("A explicit correction = state:%#v raw:%d msg:%d", state, raw, msgID)
	}
	if profile, ok := edgeA.rpcResults.ExactAdmissionProfile(authKeyID, sessionID, correctionMsgID); !ok || profile != tlprofile.Profile227 {
		t.Fatalf("corrected admission profile = (%d,%v), want 227", profile, ok)
	}
	if len(correctionPlan.rpcTasks) != 1 {
		t.Fatalf("corrected tasks = %d, want 1", len(correctionPlan.rpcTasks))
	}
	if err := correctionPlan.rpcTasks[0].run(ctx); err != nil {
		t.Fatal(err)
	}
	requestProfile, resultProfile := handlerA.profiles()
	if requestProfile != tlprofile.Profile227 || resultProfile != tlprofile.Profile227 {
		t.Fatalf("dispatch/result profiles = %d/%d, want 227/227", requestProfile, resultProfile)
	}
}

func TestPreflightMarksOldContainerInnerLayerEvidenceRequestBound(t *testing.T) {
	now := time.Unix(1_900_000_000, 123_456_780)
	testClock := newExpiryTestClock(now)
	s := New(Options{DC: 2, LayerRPC: newAdmissionOnlyLayerRPC(), Clock: testClock})
	innerMsgID := proto.NewMessageIDGen(func() time.Time { return now.Add(-10 * time.Minute) }).New(proto.MessageFromClient)
	outerMsgID := proto.NewMessageIDGen(func() time.Time { return now }).New(proto.MessageFromClient)
	body := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
		Layer: 225, Query: &tg.HelpGetConfigRequest{},
	})
	container := exactLayerRPCBody(t, &proto.MessageContainer{Messages: []proto.Message{{
		ID: innerMsgID, SeqNo: 1, Bytes: len(body), Body: body,
	}}})
	plan, err := s.preflightInbound(newConnState(), outerMsgID, 2, container)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.close()
	if len(plan.items) != 1 || plan.items[0].kind != inboundItemRPC {
		t.Fatalf("old-inner plan items = %+v", plan.items)
	}
	if plan.items[0].profileEvidenceFresh() {
		t.Fatalf("old inner msg_id %d was classified fresh", innerMsgID)
	}
}

func TestExactSessionProfileSurvivesUnregisterAndSeedsNakedReplay(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	authKeyID := [8]byte{0x22, 0x00, 0x77}
	const sessionID = int64(22077)
	old := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}

	first := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
		Layer: 225,
		Query: &tg.HelpGetConfigRequest{},
	})
	if _, _, err := s.admitInboundLayerRPC(old, first); err != nil {
		t.Fatalf("initial invokeWithLayer admission: %v", err)
	}

	manager := NewSessionManager(zaptest.NewLogger(t))
	manager.SetLifecycleObserver(router)
	if err := manager.Register(old); err != nil {
		t.Fatal(err)
	}
	manager.Unregister(old)

	replacement := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	layer, ok := router.NegotiatedSessionLayer(authKeyID, sessionID)
	if !ok || layer != 225 {
		t.Fatalf("reconnect seed = (%d,%v), want (225,true)", layer, ok)
	}
	profile, ok := tlprofile.ResolveProfile(layer)
	if !ok {
		t.Fatalf("resolve retained profile %d", layer)
	}
	if err := replacement.SeedLayerProfile(profile); err != nil {
		t.Fatal(err)
	}
	naked := exactOutboundLayerRPCBody(t, profile, &tg.HelpGetConfigRequest{})
	admitted, method, err := s.admitInboundLayerRPC(replacement, naked)
	if err != nil {
		t.Fatalf("same-session naked replay admission: %v", err)
	}
	if method != "help.getConfig" || admitted.Call().Profile() != tlprofile.Profile225 {
		t.Fatalf("naked replay = method:%q profile:%d", method, admitted.Call().Profile())
	}
}

func TestSameAuthKeyNewSessionRequiresOwnLayerEvidence(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	authKeyID := [8]byte{0x22, 0x70, 0x22, 0x70}
	const (
		bobSession   = int64(227)
		aliceSession = int64(228)
	)

	bobConn := &Conn{authKeyID: authKeyID, sessionID: bobSession, metrics: NopMetrics{}}
	bobWrapped := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
		Layer: 227,
		Query: &tg.HelpGetConfigRequest{},
	})
	bobRequest, _, err := s.admitInboundLayerRPC(bobConn, bobWrapped)
	if err != nil {
		t.Fatalf("Bob session layer 227: %v", err)
	}
	if bobRequest.Call().Profile() != tlprofile.Profile227 {
		t.Fatalf("Bob profile = %d, want 227", bobRequest.Call().Profile())
	}

	aliceConn := &Conn{authKeyID: authKeyID, sessionID: aliceSession, metrics: NopMetrics{}}
	profileDependent := &tg.MessagesGetHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 1,
	}
	naked228 := exactOutboundLayerRPCBody(t, tlprofile.Profile228, profileDependent)
	if _, _, err := s.admitInboundLayerRPC(aliceConn, naked228); err == nil {
		t.Fatal("new session inherited another session's Layer for naked application RPC")
	}
	if profile, ok := aliceConn.LayerProfile(); ok || profile != 0 {
		t.Fatalf("rejected new session froze profile = (%d,%v)", profile, ok)
	}
	if layer, ok := router.NegotiatedSessionLayer(authKeyID, aliceSession); ok || layer != 0 {
		t.Fatalf("new session registry inherited layer = (%d,%v)", layer, ok)
	}

	aliceWrapped := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
		Layer: 228,
		Query: profileDependent,
	})
	aliceRequest, _, err := s.admitInboundLayerRPC(aliceConn, aliceWrapped)
	if err != nil {
		t.Fatalf("Alice session own layer 228 evidence: %v", err)
	}
	if aliceRequest.Call().Profile() != tlprofile.Profile228 {
		t.Fatalf("Alice profile = %d, want 228", aliceRequest.Call().Profile())
	}
	if layer, ok := router.NegotiatedSessionLayer(authKeyID, bobSession); !ok || layer != 227 {
		t.Fatalf("Bob exact registry changed = (%d,%v), want (227,true)", layer, ok)
	}
	if layer, ok := router.NegotiatedSessionLayer(authKeyID, aliceSession); !ok || layer != 228 {
		t.Fatalf("Alice exact registry = (%d,%v), want (228,true)", layer, ok)
	}
}

func TestExactSessionRegistryAllowsOrderedExplicitCorrection(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	authKeyID := [8]byte{0x22, 0x07, 0x07}
	const sessionID = int64(22707)
	if err := router.FreezeNegotiatedSessionLayer(authKeyID, sessionID, 225); err != nil {
		t.Fatal(err)
	}
	// A later well-formed invokeWithLayer is authoritative correction, including
	// when same-session recovery initially restored an older explicit profile.
	c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	if err := c.SeedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	wrapped := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{
		Layer: 227,
		Query: &tg.HelpGetConfigRequest{},
	})
	request, _, err := s.admitInboundLayerRPC(c, wrapped)
	if err != nil {
		t.Fatalf("profile correction: %v", err)
	}
	if request.Call().Profile() != tlprofile.Profile227 {
		t.Fatalf("corrected request profile = %d", request.Call().Profile())
	}
	if got := c.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileExplicit || got.Epoch < 2 {
		t.Fatalf("corrected Conn profile = %#v", got)
	}
	if layer, ok := router.NegotiatedSessionLayer(authKeyID, sessionID); !ok || layer != 227 {
		t.Fatalf("corrected registry = (%d,%v)", layer, ok)
	}
}

func TestRestoredExplicitProfileNakedFailureRequestsLayerCorrection(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	c := &Conn{authKeyID: [8]byte{2, 2, 0, 2}, sessionID: 220227, metrics: NopMetrics{}}
	if err := c.SeedLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	request := &tg.ChannelsJoinChannelRequest{Channel: &tg.InputChannelEmpty{}}
	naked227 := exactOutboundLayerRPCBody(t, tlprofile.Profile227, request)
	if _, _, err := s.admitInboundLayerRPC(c, naked227); err == nil {
		t.Fatal("stale explicit profile admitted newer naked constructor")
	} else if rpcErr := layerRPCAdmissionError(err); rpcErr.ErrorCode != 400 || rpcErr.ErrorMessage != "CONNECTION_LAYER_INVALID" {
		t.Fatalf("stale explicit profile error = (%d,%q): %v", rpcErr.ErrorCode, rpcErr.ErrorMessage, err)
	}
	if got := c.LayerProfileState(); got.Profile != tlprofile.Profile225 || got.Origin != LayerProfileExplicit {
		t.Fatalf("failed naked admission changed profile = %#v", got)
	}

	wrapped := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 227, Query: request})
	admitted, _, err := s.admitInboundLayerRPC(c, wrapped)
	if err != nil {
		t.Fatalf("explicit correction retry: %v", err)
	}
	if admitted.Call().Profile() != tlprofile.Profile227 {
		t.Fatalf("corrected call profile = %d", admitted.Call().Profile())
	}
	if got := c.LayerProfileState(); got.Profile != tlprofile.Profile227 || got.Origin != LayerProfileExplicit || got.Epoch < 2 {
		t.Fatalf("corrected profile = %#v", got)
	}
}

func TestUnprofiledInvariantBindKeepsProfileUnknownAndReturnsExactBool(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	authKeyID := [8]byte{0xcd, 0xd4, 0x2a, 0x05}
	const sessionID = int64(227220)
	c := &Conn{authKeyID: authKeyID, sessionID: sessionID, metrics: NopMetrics{}}
	bind := &tg.AuthBindTempAuthKeyRequest{
		PermAuthKeyID:    1,
		Nonce:            2,
		ExpiresAt:        3,
		EncryptedMessage: []byte("bind"),
	}
	body := exactLayerRPCBody(t, bind)
	admitted, method, err := s.admitInboundLayerRPC(c, body)
	if err != nil {
		t.Fatal(err)
	}
	if method != "auth.bindTempAuthKey" || !admitted.Call().WireInvariant() {
		t.Fatalf("unprofiled admission = method:%q invariant:%v", method, admitted.Call().WireInvariant())
	}
	if _, evidence := admitted.ProfileEvidence(); evidence {
		t.Fatal("unprofiled invariant bind exposed profile evidence")
	}
	if profile, ok := c.LayerProfile(); ok || profile != 0 {
		t.Fatalf("unprofiled invariant froze Conn profile = (%d,%v)", profile, ok)
	}
	if layer, ok := router.NegotiatedSessionLayer(authKeyID, sessionID); ok || layer != 0 {
		t.Fatalf("unprofiled invariant froze session registry = (%d,%v)", layer, ok)
	}

	result, dispatchedMethod, err := router.DispatchAdmitted(context.Background(), authKeyID, sessionID, 1, 1, admitted)
	if err != nil {
		t.Fatal(err)
	}
	if dispatchedMethod != method || result == nil || !result.WireInvariant() {
		t.Fatalf("bind result = method:%q result:%T invariant:%v", dispatchedMethod, result, result != nil && result.WireInvariant())
	}
	exact := &layerRPCResultEncoder{call: result.Prepared().Call(), result: result}
	encoded, err := s.encodeRPCResult(c, 100, exact)
	if err != nil {
		t.Fatalf("encode invariant result on unknown-profile Conn: %v", err)
	}
	if encoded.layer == nil || !encoded.layer.wireInvariant {
		t.Fatalf("encoded bind result lost invariant binding: %#v", encoded.layer)
	}
	var envelope proto.Result
	if err := envelope.Decode(&bin.Buffer{Buf: encoded.body}); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []tlprofile.Profile{tlprofile.Profile225, tlprofile.Profile227} {
		inner := bin.Buffer{Buf: append([]byte(nil), envelope.Result...)}
		decoded, err := tlprofile.DecodeObject(profile, &inner, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode invariant Bool at layer %d: %v", profile, err)
		}
		if _, ok := decoded.(*tg.BoolTrue); !ok || inner.Len() != 0 {
			t.Fatalf("layer %d bind result = %T remaining:%d", profile, decoded, inner.Len())
		}
	}

	// The same immutable bytes remain legal if profile evidence arrives before
	// the queued bind result is physically written.
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	if err := validateOutboundLayerBinding(c, encoded); err != nil {
		t.Fatalf("validate invariant result after layer 225 freeze: %v", err)
	}
	profiledBody := exactOutboundLayerRPCBody(t, tlprofile.Profile225, bind)
	profiled, _, err := s.admitInboundLayerRPC(c, profiledBody)
	if err != nil {
		t.Fatal(err)
	}
	if profiled.Prepared().Identity() != admitted.Prepared().Identity() {
		t.Fatal("invariant bind identity changed after layer 225 freeze")
	}
}

func TestLayerRPCBatchCapacityKeepsExistingPendingReplay(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	s.rpcResults = newRPCResultCacheWithFlightLimit(time.Now, 1)
	c := &Conn{
		authKeyID: [8]byte{7, 7, 1},
		sessionID: 771,
		metrics:   NopMetrics{},
	}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 8, time.Second)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}

	firstBody := exactLayerRPCBody(t, &tg.HelpGetConfigRequest{})
	identityBuffer := &bin.Buffer{Buf: append([]byte(nil), firstBody...)}
	admitted, err := router.AdmitLayer(tlprofile.Profile225, identityBuffer, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.rpcResults.AcquireIdentified(c.authKeyID, c.sessionID, 100, admitted.Prepared().Identity())
	if err != nil || source.state != rpcResultAcquireOwner || source.owner == nil {
		t.Fatalf("source owner = state:%d err:%v", source.state, err)
	}

	plan := &inboundPlan{items: []inboundItem{
		{kind: inboundItemRPC, msgID: 100, body: firstBody},
		{kind: inboundItemRPC, msgID: 104, body: exactLayerRPCBody(t, &tg.HelpGetNearestDCRequest{})},
	}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if plan.items[0].kind != inboundItemRewrappedRPC || len(plan.rewrapAliases) != 1 {
		t.Fatalf("pending replay was canceled: kind=%d aliases=%d", plan.items[0].kind, len(plan.rewrapAliases))
	}
	if plan.items[1].kind != inboundItemCapacityError {
		t.Fatalf("over-capacity request kind = %d, want capacity error", plan.items[1].kind)
	}
	if dependency, ok := s.rpcResults.ObserveDependency(c.authKeyID, c.sessionID, 100); !ok || dependency.waiter == nil {
		t.Fatalf("source flight was completed or removed by rollback: %#v ok:%v", dependency, ok)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 || c.rpcReserved != 0 {
		t.Fatalf("flight-capacity rollback leaked connection budget bytes:%d tasks:%d", got, c.rpcReserved)
	}
	s.rpcScheduler.budgetMu.Lock()
	globalTasks, globalBytes := s.rpcScheduler.tasks, s.rpcScheduler.bytes
	s.rpcScheduler.budgetMu.Unlock()
	if globalTasks != 0 || globalBytes != 0 {
		t.Fatalf("flight-capacity rollback leaked global budget %d/%d", globalTasks, globalBytes)
	}
	plan.close()
	if !source.owner.Abort() {
		t.Fatal("batch cleanup touched the existing source owner")
	}
}

func TestLayerRPCBatchCapacityAbortsRejectedRewrapOwner(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	authKeyID := [8]byte{7, 7, 9}
	const (
		sessionID = int64(779)
		msgID     = int64(900)
	)
	identity := rpcFlightExactIdentity(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	claim, err := cache.AcquireIdentified(authKeyID, sessionID, msgID, identity)
	if err != nil || claim.state != rpcResultAcquireOwner || claim.owner == nil {
		t.Fatalf("rewrap owner = state:%d err:%v", claim.state, err)
	}
	plan := &inboundPlan{
		items: []inboundItem{{kind: inboundItemRewrappedRPC, msgID: msgID, payload: claim.owner}},
		rewrapAliases: []*rpcRewrapAlias{{
			itemIndex: 0,
			newOwner:  claim.owner,
		}},
	}
	plan.rejectNewRPCOwners(nil)
	if plan.items[0].kind != inboundItemCapacityError || plan.items[0].payload != nil || len(plan.rewrapAliases) != 0 {
		t.Fatalf("rejected rewrap plan = kind:%d payload:%T aliases:%d", plan.items[0].kind, plan.items[0].payload, len(plan.rewrapAliases))
	}

	// The same exact request key must be acquirable immediately. A pending
	// result here means the rejected alias leaked its one-shot owner.
	reacquired, err := cache.AcquireIdentified(authKeyID, sessionID, msgID, identity)
	if err != nil || reacquired.state != rpcResultAcquireOwner || reacquired.owner == nil {
		t.Fatalf("reacquire after rejected rewrap = state:%d err:%v", reacquired.state, err)
	}
	if !reacquired.owner.Abort() {
		t.Fatal("reacquired owner cleanup failed")
	}
}

func TestLayerRPCDependencyGateUsesBusinessOutcome(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	c := &Conn{authKeyID: [8]byte{7, 7, 2}, sessionID: 772, metrics: NopMetrics{}}

	for _, test := range []struct {
		name       string
		dependency int64
		requestID  int64
		success    bool
	}{
		{name: "success", dependency: 200, requestID: 204, success: true},
		{name: "rpc_error", dependency: 208, requestID: 212, success: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, err := s.rpcResults.Acquire(c.authKeyID, c.sessionID, test.dependency)
			if err != nil || source.owner == nil {
				t.Fatalf("source owner err = %v", err)
			}
			body := exactLayerRPCBody(t, &tg.InvokeAfterMsgRequest{
				MsgID: test.dependency,
				Query: &tg.HelpGetConfigRequest{},
			})
			admitted, err := router.AdmitLayer(tlprofile.Profile225, &bin.Buffer{Buf: body}, tlprofile.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			dependencies := s.layerRPCDependencies(c, test.requestID, admitted)
			if dependencies.failed || len(dependencies.waiters) != 1 {
				t.Fatalf("dependencies before completion = %+v", dependencies)
			}
			gate := newLayerRPCExecutionGate(c, dependencies)
			if gate == nil || gate.runnable() {
				t.Fatal("dependency gate was runnable before business completion")
			}
			if !source.owner.CompleteExecution(test.success) {
				t.Fatal("source business completion lost")
			}
			if !gate.runnable() || gate.success() != test.success {
				t.Fatalf("resolved gate = runnable:%v success:%v, want success:%v", gate.runnable(), gate.success(), test.success)
			}
			if !source.owner.Abort() {
				t.Fatal("source cleanup abort lost")
			}
		})
	}

	missingBody := exactLayerRPCBody(t, &tg.InvokeAfterMsgRequest{
		MsgID: 300,
		Query: &tg.HelpGetConfigRequest{},
	})
	missing, err := router.AdmitLayer(tlprofile.Profile225, &bin.Buffer{Buf: missingBody}, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	dependencies := s.layerRPCDependencies(c, 304, missing)
	gate := newLayerRPCExecutionGate(c, dependencies)
	if !dependencies.failed || gate == nil || !gate.runnable() || gate.success() {
		t.Fatalf("missing dependency gate = deps:%+v runnable:%v success:%v", dependencies, gate.runnable(), gate.success())
	}
}

func TestLayerRPCTimeoutMessageDistinguishesDependencyWait(t *testing.T) {
	gate := newInboundRPCGate(1, nil)
	gate.resolve(true)
	if got := layerRPCTimeoutMessage(gate); got != "MSG_WAIT_TIMEOUT" {
		t.Fatalf("blocked gate timeout = %q", got)
	}
	gate.resolve(true)
	if got := layerRPCTimeoutMessage(gate); got != "RPC_TIMEOUT" {
		t.Fatalf("resolved gate timeout = %q", got)
	}
	if got := layerRPCTimeoutMessage(nil); got != "RPC_TIMEOUT" {
		t.Fatalf("ordinary timeout = %q", got)
	}
}

func TestLayerRPCAdmissionErrorUsesTypedUnknownClassification(t *testing.T) {
	err := &tlprofile.LayerCodecError{
		Operation: "admit RPC request",
		Profile:   tlprofile.Profile225,
		WireID:    0x01020304,
		Reason:    "wording may change",
		Cause:     tlprofile.ErrUnknownRPCMethod,
	}
	rpcErr := layerRPCAdmissionError(err)
	if rpcErr.ErrorCode != 501 || rpcErr.ErrorMessage != "NOT_IMPLEMENTED" {
		t.Fatalf("unknown admission error = (%d,%q)", rpcErr.ErrorCode, rpcErr.ErrorMessage)
	}
}

func TestLayerRPCAdmissionErrorDistinguishesUnknownAndInheritedProfiles(t *testing.T) {
	profileRequired := &tlprofile.LayerCodecError{Operation: "admit", Cause: tlprofile.ErrProfileRequired}
	if rpcErr := layerRPCAdmissionError(profileRequired); rpcErr.ErrorCode != 400 || rpcErr.ErrorMessage != "CONNECTION_NOT_INITED" {
		t.Fatalf("unknown profile admission = (%d,%q)", rpcErr.ErrorCode, rpcErr.ErrorMessage)
	}
	inherited := fmt.Errorf("%w: %w", errDefaultLayerAdmission, tlprofile.ErrUnknownRPCMethod)
	if rpcErr := layerRPCAdmissionError(inherited); rpcErr.ErrorCode != 400 || rpcErr.ErrorMessage != "CONNECTION_LAYER_INVALID" {
		t.Fatalf("inherited profile admission = (%d,%q)", rpcErr.ErrorCode, rpcErr.ErrorMessage)
	}
}
