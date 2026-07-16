package mtprotoedge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/tlprofile"
	appfiles "telesrv/internal/app/files"
	"telesrv/internal/rpc"
)

func TestLayerRPCAdmissionMaterializationConstants(t *testing.T) {
	constructors := make([]reflect.Type, 0, len(tg.TypesConstructorMap()))
	for _, constructor := range tg.TypesConstructorMap() {
		if typ := reflect.TypeOf(constructor()); typ != nil {
			constructors = append(constructors, typ)
		}
	}
	seen := make(map[reflect.Type]struct{})
	var (
		maxSize uintptr
		maxType reflect.Type
		visit   func(reflect.Type)
	)
	visit = func(typ reflect.Type) {
		if typ == nil {
			return
		}
		if _, ok := seen[typ]; ok {
			return
		}
		seen[typ] = struct{}{}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(typ.Elem())
		case reflect.Interface:
			for _, candidate := range constructors {
				// invokeWithLayer/query uses the deliberately broad bin.Object
				// interface. Exact admission restricts that slot to generated RPC
				// methods, so only request constructors are reachable there.
				broad := typ.PkgPath() != "github.com/iamxvbaba/td/tg"
				if candidate.Implements(typ) && (!broad || strings.HasSuffix(candidate.Elem().Name(), "Request")) {
					visit(candidate)
				}
			}
		case reflect.Struct:
			if typ.PkgPath() == "github.com/iamxvbaba/td/tg" && typ.Size() > maxSize {
				maxSize, maxType = typ.Size(), typ
			}
			for i := 0; i < typ.NumField(); i++ {
				visit(typ.Field(i).Type)
			}
		}
	}
	for _, typ := range constructors {
		if typ.Kind() == reflect.Pointer && strings.HasSuffix(typ.Elem().Name(), "Request") {
			visit(typ)
		}
	}
	if maxSize > layerRPCAdmissionStaticObjectBytes {
		t.Fatalf("request-reachable generated TL object %v is %d bytes, exceeds admission ceiling %d", maxType, maxSize, layerRPCAdmissionStaticObjectBytes)
	}
	if layerRPCAdmissionGraphSlack != layerRPCAdmissionStaticObjectBytes*inboundLayerDecodeLimits.MaxDepth {
		t.Fatalf("graph slack %d does not cover %d bytes across decode depth %d", layerRPCAdmissionGraphSlack, layerRPCAdmissionStaticObjectBytes, inboundLayerDecodeLimits.MaxDepth)
	}
	// Preserve enough room for the maximum upload payload plus any supported
	// transparent wrapper/client-info envelope on a default connection.
	if got := layerRPCAdmissionReservationSize(appfiles.MaxUploadPartBytes + (32 << 10)); got > maxInflightRPCBytes {
		t.Fatalf("largest legal upload request charge = %d, exceeds default connection budget %d", got, maxInflightRPCBytes)
	}
	maxInt := int(^uint(0) >> 1)
	if got := layerRPCAdmissionReservationSize(maxInt); got != maxInt {
		t.Fatalf("saturating charge = %d, want max int %d", got, maxInt)
	}
	if got := layerRPCAdmissionReservationSize(-1); got != layerRPCAdmissionGraphSlack {
		t.Fatalf("negative wire charge = %d, want fixed slack %d", got, layerRPCAdmissionGraphSlack)
	}
}

type countingLayerRPCAdmission struct {
	LayerRPCHandler
	decodeCalls atomic.Int32
}

type failingReplayLayerRPC struct {
	LayerRPCHandler
	err error
}

func (h *failingReplayLayerRPC) PrepareAdmittedReplay(
	context.Context,
	[8]byte,
	int64,
	int64,
	uint64,
	tlprofile.Admission,
) (func() error, error) {
	return nil, h.err
}

func (h *countingLayerRPCAdmission) AdmitLayer(profile tlprofile.Profile, b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	h.decodeCalls.Add(1)
	return h.LayerRPCHandler.AdmitLayer(profile, b, limits)
}

func (h *countingLayerRPCAdmission) AdmitUnprofiled(b *bin.Buffer, limits tlprofile.Limits) (tlprofile.Admission, error) {
	h.decodeCalls.Add(1)
	return h.LayerRPCHandler.AdmitUnprofiled(b, limits)
}

func TestLayerRPCAdmissionCapacityRejectsBeforeDecoder(t *testing.T) {
	for _, test := range []struct {
		name       string
		fillGlobal bool
	}{
		{name: "connection_queue"},
		{name: "global_task", fillGlobal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
			counting := &countingLayerRPCAdmission{LayerRPCHandler: router}
			s := New(Options{DC: 2, LayerRPC: counting})
			scheduler := newInboundRPCScheduler(1, 1, 1<<30)
			s.rpcScheduler = scheduler

			target := &Conn{authKeyID: [8]byte{8, 1}, sessionID: 81, metrics: NopMetrics{}}
			target.startInboundRPCScheduler(scheduler, 1, 1, time.Second)
			holder := target
			if test.fillGlobal {
				holder = &Conn{authKeyID: [8]byte{8, 2}, sessionID: 82, metrics: NopMetrics{}}
				holder.startInboundRPCScheduler(scheduler, 1, 1, time.Second)
			}
			occupied, err := holder.reserveInboundRPCBatch(context.Background(), []inboundRPCSpec{{method: "occupied", size: 1}})
			if err != nil {
				t.Fatal(err)
			}
			defer occupied.abort()

			body := exactLayerRPCBody(t, &tg.InvokeWithLayerRequest{Layer: 225, Query: &tg.HelpGetConfigRequest{}})
			plan := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
			defer plan.close()
			if err := s.prepareInboundLayerRPCBatch(context.Background(), target, plan); err != nil {
				t.Fatal(err)
			}
			if got := counting.decodeCalls.Load(); got != 0 {
				t.Fatalf("typed decoder entered %d times after capacity rejection", got)
			}
			if plan.items[0].kind != inboundItemCapacityError || plan.rpcReservation != nil || len(plan.rpcTasks) != 0 {
				t.Fatalf("rejected plan = kind:%d reservation:%v tasks:%d", plan.items[0].kind, plan.rpcReservation != nil, len(plan.rpcTasks))
			}
		})
	}
}

func TestLayerRPCAdmissionTransfersOriginalReservationToFreshOwner(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	c := &Conn{authKeyID: [8]byte{8, 3}, sessionID: 83, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 4, time.Second)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}

	bad := make([]byte, bin.Word)
	bad[0], bad[1], bad[2], bad[3] = 0x04, 0x03, 0x02, 0x01
	fresh := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	plan := &inboundPlan{items: []inboundItem{
		{kind: inboundItemRPC, msgID: 100, body: bad},
		{kind: inboundItemRPC, msgID: 104, body: fresh},
	}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if plan.items[0].kind != inboundItemRPCAdmissionError || len(plan.rpcTasks) != 1 || plan.rpcReservation == nil {
		t.Fatalf("classified plan = bad:%d tasks:%d reservation:%v", plan.items[0].kind, len(plan.rpcTasks), plan.rpcReservation != nil)
	}
	wantCharge := int64(layerRPCAdmissionReservationSize(len(fresh)))
	if got := c.inflightRPCBytes.Load(); got != wantCharge {
		t.Fatalf("connection retained bytes = %d, want %d", got, wantCharge)
	}
	s.rpcScheduler.budgetMu.Lock()
	globalTasks, globalBytes := s.rpcScheduler.tasks, s.rpcScheduler.bytes
	s.rpcScheduler.budgetMu.Unlock()
	if globalTasks != 1 || globalBytes != wantCharge {
		t.Fatalf("global retained budget = %d/%d, want 1/%d", globalTasks, globalBytes, wantCharge)
	}
	plan.close()
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("plan abort leaked %d connection bytes", got)
	}
	s.rpcScheduler.budgetMu.Lock()
	globalTasks, globalBytes = s.rpcScheduler.tasks, s.rpcScheduler.bytes
	s.rpcScheduler.budgetMu.Unlock()
	if globalTasks != 0 || globalBytes != 0 {
		t.Fatalf("plan abort leaked global budget %d/%d", globalTasks, globalBytes)
	}
}

func TestLayerRPCAdmissionPendingReplayReleasesProvisionalEntry(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	c := &Conn{authKeyID: [8]byte{8, 4}, sessionID: 84, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 4, time.Second)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}

	pendingBody := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	identityBuffer := &bin.Buffer{Buf: append([]byte(nil), pendingBody...)}
	pendingRequest, err := router.AdmitLayer(tlprofile.Profile225, identityBuffer, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.rpcResults.AcquireIdentified(c.authKeyID, c.sessionID, 100, pendingRequest.Prepared().Identity())
	if err != nil || pending.owner == nil {
		t.Fatalf("pending owner = %v, %v", pending.owner, err)
	}

	freshBody := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetNearestDCRequest{})
	plan := &inboundPlan{items: []inboundItem{
		{kind: inboundItemRPC, msgID: 100, body: pendingBody},
		{kind: inboundItemRPC, msgID: 104, body: freshBody},
	}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if plan.items[0].kind != inboundItemRewrappedRPC || len(plan.rewrapAliases) != 1 || len(plan.rpcTasks) != 1 {
		t.Fatalf("pending/fresh classification = kind:%d aliases:%d tasks:%d", plan.items[0].kind, len(plan.rewrapAliases), len(plan.rpcTasks))
	}
	wantCharge := int64(layerRPCAdmissionReservationSize(len(freshBody)))
	if got := c.inflightRPCBytes.Load(); got != wantCharge {
		t.Fatalf("pending replay retained bytes = %d, want only fresh %d", got, wantCharge)
	}
	plan.close()
	if !pending.owner.Abort() {
		t.Fatal("plan cleanup aborted the pre-existing pending replay owner")
	}
}

func TestLayerRPCAdmissionCompletedReplayReleasesWholeProvisionalBatch(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	c := &Conn{authKeyID: [8]byte{8, 8}, sessionID: 88, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 2, time.Second)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	body := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	identityBuffer := &bin.Buffer{Buf: append([]byte(nil), body...)}
	request, err := router.AdmitLayer(tlprofile.Profile225, identityBuffer, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.rpcResults.AcquireIdentified(c.authKeyID, c.sessionID, 100, request.Prepared().Identity())
	if err != nil || claim.owner == nil {
		t.Fatalf("completed replay owner = %v, %v", claim.owner, err)
	}
	if !claim.owner.CompleteExecution(true) {
		t.Fatal("complete replay business outcome failed")
	}
	s.rpcResults.Put(c.authKeyID, c.sessionID, 100, &encodedOutboundMessage{body: []byte{1, 2, 3, 4}})

	plan := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if plan.items[0].kind != inboundItemReplayRPC || plan.rpcReservation != nil || len(plan.rpcTasks) != 0 {
		t.Fatalf("completed replay plan = kind:%d reservation:%v tasks:%d", plan.items[0].kind, plan.rpcReservation != nil, len(plan.rpcTasks))
	}
	if got := c.inflightRPCBytes.Load(); got != 0 || c.rpcReserved != 0 {
		t.Fatalf("completed replay leaked connection budget bytes:%d tasks:%d", got, c.rpcReserved)
	}
	s.rpcScheduler.budgetMu.Lock()
	globalTasks, globalBytes := s.rpcScheduler.tasks, s.rpcScheduler.bytes
	s.rpcScheduler.budgetMu.Unlock()
	if globalTasks != 0 || globalBytes != 0 {
		t.Fatalf("completed replay leaked global budget %d/%d", globalTasks, globalBytes)
	}
}

func TestLayerRPCAdmissionReplayPreparationErrorIsNotSilentlyDelivered(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	prepareErr := errors.New("invalid replay wrapper metadata")
	s := New(Options{DC: 2, LayerRPC: &failingReplayLayerRPC{
		LayerRPCHandler: router,
		err:             prepareErr,
	}})
	c := &Conn{authKeyID: [8]byte{8, 9}, sessionID: 89, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 2, time.Second)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	body := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	identityBuffer := &bin.Buffer{Buf: append([]byte(nil), body...)}
	request, err := router.AdmitLayer(tlprofile.Profile225, identityBuffer, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.rpcResults.AcquireIdentified(c.authKeyID, c.sessionID, 100, request.Prepared().Identity())
	if err != nil || claim.owner == nil {
		t.Fatalf("completed replay owner = %v, %v", claim.owner, err)
	}
	if !claim.owner.CompleteExecution(true) {
		t.Fatal("complete replay business outcome failed")
	}
	s.rpcResults.Put(c.authKeyID, c.sessionID, 100, &encodedOutboundMessage{body: []byte{1, 2, 3, 4}})

	plan := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); !errors.Is(err, prepareErr) {
		plan.close()
		t.Fatalf("replay preparation error = %v, want %v", err, prepareErr)
	}
	if plan.items[0].kind == inboundItemReplayRPC {
		plan.close()
		t.Fatal("invalid replay metadata was converted into a deliverable cached result")
	}
	plan.close()
	if got := c.inflightRPCBytes.Load(); got != 0 || c.rpcReserved != 0 {
		t.Fatalf("failed replay preparation leaked connection budget bytes:%d tasks:%d", got, c.rpcReserved)
	}
	s.rpcScheduler.budgetMu.Lock()
	globalTasks, globalBytes := s.rpcScheduler.tasks, s.rpcScheduler.bytes
	s.rpcScheduler.budgetMu.Unlock()
	if globalTasks != 0 || globalBytes != 0 {
		t.Fatalf("failed replay preparation leaked global budget %d/%d", globalTasks, globalBytes)
	}
}

func TestLayerRPCAdmissionTransferredBatchClosesWithoutLeak(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	c := &Conn{authKeyID: [8]byte{8, 5}, sessionID: 85, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 2, time.Second)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	plan := &inboundPlan{items: []inboundItem{{
		kind: inboundItemRPC, msgID: 100,
		body: exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{}),
	}}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	c.beginCloseInboundRPCScheduler()
	if err := plan.commitRPCBatch(); err != ErrConnClosed {
		t.Fatalf("commit after connection close = %v, want ErrConnClosed", err)
	}
	plan.close()
	if !c.waitInboundShutdown(time.Second) {
		t.Fatal("connection close did not converge after transferred reservation failed commit")
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection close leaked %d admission bytes", got)
	}
	s.rpcScheduler.budgetMu.Lock()
	globalTasks, globalBytes := s.rpcScheduler.tasks, s.rpcScheduler.bytes
	s.rpcScheduler.budgetMu.Unlock()
	if globalTasks != 0 || globalBytes != 0 {
		t.Fatalf("connection close leaked global budget %d/%d", globalTasks, globalBytes)
	}
}

func TestLayerRPCAdmissionTransferredBatchCommitsConservativeCharge(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	c := &Conn{authKeyID: [8]byte{8, 6}, sessionID: 86, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 2, time.Second)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	body := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	plan := &inboundPlan{items: []inboundItem{{kind: inboundItemRPC, msgID: 100, body: body}}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if err := plan.commitRPCBatch(); err != nil {
		t.Fatal(err)
	}
	wantCharge := layerRPCAdmissionReservationSize(len(body))
	c.rpcMu.Lock()
	queued := len(c.rpcQueue)
	gotCharge := 0
	if queued == 1 {
		gotCharge = c.rpcQueue[0].size
	}
	c.rpcMu.Unlock()
	if queued != 1 || gotCharge != wantCharge {
		t.Fatalf("committed queue = len:%d charge:%d, want 1/%d", queued, gotCharge, wantCharge)
	}
	c.beginCloseInboundRPCScheduler()
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("queued exact task close leaked %d bytes", got)
	}
	s.rpcScheduler.budgetMu.Lock()
	globalTasks, globalBytes := s.rpcScheduler.tasks, s.rpcScheduler.bytes
	s.rpcScheduler.budgetMu.Unlock()
	if globalTasks != 0 || globalBytes != 0 {
		t.Fatalf("queued exact task close leaked global budget %d/%d", globalTasks, globalBytes)
	}
}

func TestLayerRPCAdmissionLocalDuplicateConsumesNoProvisionalEntry(t *testing.T) {
	router := rpc.New(rpc.Config{DC: 2}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	s := New(Options{DC: 2, LayerRPC: router})
	c := &Conn{authKeyID: [8]byte{8, 7}, sessionID: 87, metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s.rpcScheduler, 1, 2, time.Second)
	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	body := exactOutboundLayerRPCBody(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	plan := &inboundPlan{items: []inboundItem{
		{kind: inboundItemDuplicate, msgID: 96, body: body},
		{kind: inboundItemRPC, msgID: 100, body: body},
	}}
	defer plan.close()
	if err := s.prepareInboundLayerRPCBatch(context.Background(), c, plan); err != nil {
		t.Fatal(err)
	}
	if plan.items[0].kind != inboundItemDuplicate || len(plan.rpcTasks) != 1 || c.rpcReserved != 1 {
		t.Fatalf("duplicate/fresh admission = duplicate:%d tasks:%d reserved:%d", plan.items[0].kind, len(plan.rpcTasks), c.rpcReserved)
	}
}

func TestTakeInboundRPCFIFOAdvancesSliceHead(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	c := &Conn{metrics: NopMetrics{}}
	c.startInboundRPCScheduler(scheduler, 1, 8, time.Second)
	c.rpcQueue = []inboundRPC{{method: "first"}, {method: "second"}, {method: "third"}}
	c.rpcReady = true
	oldSecond := &c.rpcQueue[1]
	task, ok, _ := c.takeInboundRPC()
	if !ok || task.method != "first" {
		t.Fatalf("take = (%q,%v), want first", task.method, ok)
	}
	if len(c.rpcQueue) != 2 || &c.rpcQueue[0] != oldSecond {
		t.Fatal("FIFO take copied the queue instead of advancing its slice head")
	}
	c.finishInboundRPC(task)
	c.beginCloseInboundRPCScheduler()
}

func BenchmarkLayerRPCAdmissionReservationSize(b *testing.B) {
	for _, size := range []int{64, appfiles.MaxUploadPartBytes + 24} {
		b.Run(time.Duration(size).String(), func(b *testing.B) {
			b.ReportAllocs()
			var charge int
			for i := 0; i < b.N; i++ {
				charge = layerRPCAdmissionReservationSize(size)
			}
			if charge == 0 {
				b.Fatal("zero admission charge")
			}
		})
	}
}
