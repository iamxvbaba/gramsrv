package mtprotoedge

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/edgecontrol"
)

type closeCountingTransport struct {
	closes int
}

type sessionDestructionRecorder struct {
	destroyed []sessionKey
}

func (*sessionDestructionRecorder) SessionOffline([8]byte, int64, int64, bool) {}

func (r *sessionDestructionRecorder) SessionDestroyed(authKeyID [8]byte, sessionID int64) {
	r.destroyed = append(r.destroyed, sessionKey{authKeyID: authKeyID, sessionID: sessionID})
}

func TestConnChannelDeliveryWatermarkSkipsDuplicatesAndOverlaps(t *testing.T) {
	c := &Conn{}
	payload5 := edgecontrol.ChannelDeliveryWatermark{Kind: edgecontrol.ChannelDeliveryPayload, ChannelID: 1001, MinPts: 5, MaxPts: 5}
	if !c.claimChannelDeliveryWatermark(payload5) {
		t.Fatal("first payload watermark claim rejected")
	}
	if c.claimChannelDeliveryWatermark(payload5) {
		t.Fatal("duplicate payload watermark accepted")
	}

	nudge5 := edgecontrol.ChannelDeliveryWatermark{Kind: edgecontrol.ChannelDeliveryNudge, ChannelID: 1001, MinPts: 5, MaxPts: 5}
	if !c.claimChannelDeliveryWatermark(nudge5) {
		t.Fatal("first nudge watermark claim rejected")
	}
	if c.claimChannelDeliveryWatermark(nudge5) {
		t.Fatal("duplicate nudge watermark accepted")
	}

	payload6to7 := edgecontrol.ChannelDeliveryWatermark{Kind: edgecontrol.ChannelDeliveryPayload, ChannelID: 1001, MinPts: 6, MaxPts: 7}
	if !c.claimChannelDeliveryWatermark(payload6to7) {
		t.Fatal("next non-overlapping payload range rejected")
	}
	overlap7to8 := edgecontrol.ChannelDeliveryWatermark{Kind: edgecontrol.ChannelDeliveryPayload, ChannelID: 1001, MinPts: 7, MaxPts: 8}
	if c.claimChannelDeliveryWatermark(overlap7to8) {
		t.Fatal("overlapping payload range accepted without splitting")
	}

	c.clearChannelDeliveryWatermarks()
	if !c.claimChannelDeliveryWatermark(payload5) {
		t.Fatal("payload watermark rejected after clear")
	}
}

func TestConnChannelDeliveryWatermarkReservationRollback(t *testing.T) {
	c := &Conn{}
	payload5 := edgecontrol.ChannelDeliveryWatermark{Kind: edgecontrol.ChannelDeliveryPayload, ChannelID: 1001, MinPts: 5, MaxPts: 5}
	claim, ok := c.reserveChannelDeliveryWatermark(payload5)
	if !ok {
		t.Fatal("reserve payload watermark rejected")
	}
	claim.rollback()
	if !c.claimChannelDeliveryWatermark(payload5) {
		t.Fatal("payload watermark was not restored after rollback")
	}

	payload6to7 := edgecontrol.ChannelDeliveryWatermark{Kind: edgecontrol.ChannelDeliveryPayload, ChannelID: 1001, MinPts: 6, MaxPts: 7}
	claim, ok = c.reserveChannelDeliveryWatermark(payload6to7)
	if !ok {
		t.Fatal("reserve next payload range rejected")
	}
	payload8 := edgecontrol.ChannelDeliveryWatermark{Kind: edgecontrol.ChannelDeliveryPayload, ChannelID: 1001, MinPts: 8, MaxPts: 8}
	next, ok := c.reserveChannelDeliveryWatermark(payload8)
	if !ok {
		t.Fatal("reserve higher payload range rejected")
	}
	next.commit()
	claim.rollback()
	if c.claimChannelDeliveryWatermark(payload8) {
		t.Fatal("stale rollback clobbered higher committed watermark")
	}
}

func TestConnChannelDeliveryWatermarkEvictsInsteadOfDroppingNewChannel(t *testing.T) {
	c := &Conn{}
	for channelID := int64(1); channelID <= maxChannelDeliveryWatermarksPerSession; channelID++ {
		if !c.claimChannelDeliveryWatermark(edgecontrol.ChannelDeliveryWatermark{
			Kind:      edgecontrol.ChannelDeliveryPayload,
			ChannelID: channelID,
			MinPts:    1,
			MaxPts:    1,
		}) {
			t.Fatalf("initial watermark rejected for channel %d", channelID)
		}
	}
	newChannelID := int64(maxChannelDeliveryWatermarksPerSession + 1)
	if !c.claimChannelDeliveryWatermark(edgecontrol.ChannelDeliveryWatermark{
		Kind:      edgecontrol.ChannelDeliveryPayload,
		ChannelID: newChannelID,
		MinPts:    1,
		MaxPts:    1,
	}) {
		t.Fatal("new channel watermark was dropped when the bounded table was full")
	}
	if got := len(c.channelPayloadPts); got != maxChannelDeliveryWatermarksPerSession {
		t.Fatalf("watermark table size = %d, want %d", got, maxChannelDeliveryWatermarksPerSession)
	}
	if got := c.channelPayloadPts[newChannelID]; got != 1 {
		t.Fatalf("new channel watermark = %d, want 1", got)
	}
}

type slowCloseTransport struct {
	delay   time.Duration
	release <-chan struct{}
	done    chan struct{}
	once    sync.Once
	closes  atomic.Int32
}

func newSlowCloseTransport(delay time.Duration, release <-chan struct{}) *slowCloseTransport {
	return &slowCloseTransport{delay: delay, release: release, done: make(chan struct{})}
}

func (*slowCloseTransport) Send(context.Context, *bin.Buffer) error {
	return errors.New("test transport send")
}
func (*slowCloseTransport) Recv(context.Context, *bin.Buffer) error {
	return errors.New("test transport recv")
}
func (t *slowCloseTransport) Close() error {
	t.closes.Add(1)
	if t.release != nil {
		<-t.release
	} else if t.delay > 0 {
		time.Sleep(t.delay)
	}
	t.once.Do(func() { close(t.done) })
	return nil
}

func (t *closeCountingTransport) Send(context.Context, *bin.Buffer) error {
	return errors.New("test transport send")
}

func (t *closeCountingTransport) Recv(context.Context, *bin.Buffer) error {
	return errors.New("test transport recv")
}

func (t *closeCountingTransport) Close() error {
	t.closes++
	return nil
}

type captureLocationRegistry struct {
	mu sync.Mutex

	records           map[sessionKey]edgecontrol.LocationRecord
	batches           [][]edgecontrol.LocationMutation
	memberships       map[int64]edgecontrol.ChannelMembershipRecord
	membershipBatches [][]edgecontrol.ChannelMembershipMutation

	leaseID      string
	acquireErr   error
	renewErr     error
	applyErr     error
	acquireCalls int
	renewCalls   int
	applyCalls   int
	releaseCalls int
	called       chan struct{}
}

func newCaptureLocationRegistry() *captureLocationRegistry {
	return &captureLocationRegistry{
		records:     make(map[sessionKey]edgecontrol.LocationRecord),
		memberships: make(map[int64]edgecontrol.ChannelMembershipRecord),
		called:      make(chan struct{}, 32),
	}
}

func (r *captureLocationRegistry) AcquireInstanceLease(_ context.Context, _, leaseID string, _ time.Duration) error {
	r.mu.Lock()
	r.acquireCalls++
	err := r.acquireErr
	if err == nil {
		r.leaseID = leaseID
	}
	r.mu.Unlock()
	r.signal()
	return err
}

func (r *captureLocationRegistry) RenewInstanceLease(_ context.Context, _, leaseID string, _ time.Duration) error {
	r.mu.Lock()
	r.renewCalls++
	err := r.renewErr
	if err == nil && r.leaseID != leaseID {
		err = edgecontrol.ErrLocationLeaseLost
	}
	r.mu.Unlock()
	r.signal()
	return err
}

func (r *captureLocationRegistry) ApplyLocationMutations(_ context.Context, _, leaseID string, mutations []edgecontrol.LocationMutation) error {
	r.mu.Lock()
	r.applyCalls++
	err := r.applyErr
	if err == nil && r.leaseID != leaseID {
		err = edgecontrol.ErrLocationLeaseLost
	}
	batch := append([]edgecontrol.LocationMutation(nil), mutations...)
	r.batches = append(r.batches, batch)
	if err == nil {
		for _, mutation := range mutations {
			key := sessionKey{authKeyID: mutation.Record.RawAuthKeyID, sessionID: mutation.Record.SessionID}
			if mutation.Deleted {
				delete(r.records, key)
			} else {
				r.records[key] = mutation.Record
			}
		}
	}
	r.mu.Unlock()
	r.signal()
	return err
}

func (r *captureLocationRegistry) ApplyChannelMembershipMutations(_ context.Context, _, leaseID string, mutations []edgecontrol.ChannelMembershipMutation) error {
	r.mu.Lock()
	err := r.applyErr
	if err == nil && r.leaseID != leaseID {
		err = edgecontrol.ErrLocationLeaseLost
	}
	batch := append([]edgecontrol.ChannelMembershipMutation(nil), mutations...)
	r.membershipBatches = append(r.membershipBatches, batch)
	if err == nil {
		for _, mutation := range mutations {
			if mutation.Deleted {
				delete(r.memberships, mutation.Record.UserID)
			} else {
				r.memberships[mutation.Record.UserID] = mutation.Record
			}
		}
	}
	r.mu.Unlock()
	r.signal()
	return err
}

func (r *captureLocationRegistry) ReleaseInstanceLease(_ context.Context, _, leaseID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	if r.leaseID != leaseID {
		return edgecontrol.ErrLocationLeaseLost
	}
	r.leaseID = ""
	clear(r.records)
	clear(r.memberships)
	return nil
}

func (r *captureLocationRegistry) ListUser(context.Context, int64) ([]edgecontrol.LocationRecord, error) {
	return nil, nil
}

func (r *captureLocationRegistry) ListBusinessAuthKey(context.Context, [8]byte) ([]edgecontrol.LocationRecord, error) {
	return nil, nil
}

func (r *captureLocationRegistry) ListInstance(context.Context, string) ([]edgecontrol.LocationRecord, error) {
	return nil, nil
}

func (r *captureLocationRegistry) snapshot() ([]edgecontrol.LocationRecord, int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := make([]edgecontrol.LocationRecord, 0, len(r.records))
	for _, record := range r.records {
		records = append(records, record)
	}
	return records, r.acquireCalls, r.renewCalls, r.applyCalls
}

func (r *captureLocationRegistry) setApplyErr(err error) {
	r.mu.Lock()
	r.applyErr = err
	r.mu.Unlock()
}

func (r *captureLocationRegistry) setRenewErr(err error) {
	r.mu.Lock()
	r.renewErr = err
	r.mu.Unlock()
}

func (r *captureLocationRegistry) resetBatches() {
	r.mu.Lock()
	r.batches = nil
	r.membershipBatches = nil
	r.applyCalls = 0
	r.mu.Unlock()
}

func (r *captureLocationRegistry) membershipSnapshot() (map[int64]edgecontrol.ChannelMembershipRecord, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	records := make(map[int64]edgecontrol.ChannelMembershipRecord, len(r.memberships))
	for userID, record := range r.memberships {
		record.ChannelIDs = append([]int64(nil), record.ChannelIDs...)
		records[userID] = record
	}
	return records, len(r.membershipBatches)
}

func (r *captureLocationRegistry) lastBatch() []edgecontrol.LocationMutation {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.batches) == 0 {
		return nil
	}
	return append([]edgecontrol.LocationMutation(nil), r.batches[len(r.batches)-1]...)
}

func (r *captureLocationRegistry) signal() {
	select {
	case r.called <- struct{}{}:
	default:
	}
}

// TestSessionManagerRegistry verifies the registry's register/unregister/lookup semantics (without network sending).
func TestSessionManagerRegistry(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	c := &Conn{sessionID: 42, authKeyID: [8]byte{1, 2, 3}}
	c.receivesUpdates.Store(true)

	sm.Register(c)
	if got := sm.Online(); got != 1 {
		t.Fatalf("online = %d, want 1", got)
	}
	sm.BindAuthKey(42, [8]byte{1, 2, 3})
	sm.BindUser(42, 100)
	if userID, ok := sm.UserID(42); !ok || userID != 100 {
		t.Fatalf("cached user = %d ok %v, want 100/true", userID, ok)
	}
	sm.BindAuthKey(42, [8]byte{9})
	if userID, ok := sm.UserID(42); ok || userID != 0 {
		t.Fatalf("cached user after auth key switch = %d ok %v, want 0/false", userID, ok)
	}
	if userID, resolved := sm.UserIDResolved(42); resolved || userID != 0 {
		t.Fatalf("resolved user after auth key switch = %d resolved %v, want unresolved", userID, resolved)
	}
	sm.BindUser(42, 0)
	if userID, resolved := sm.UserIDResolved(42); !resolved || userID != 0 {
		t.Fatalf("negative user cache = %d resolved %v, want 0/true", userID, resolved)
	}

	sm.Unregister(c)
	if got := sm.Online(); got != 0 {
		t.Fatalf("online after unregister = %d, want 0", got)
	}

	err := sm.PushToSession(context.Background(), 42, proto.MessageFromServer, &tg.UpdatesTooLong{})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("push to missing session err = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionManagerActiveLocationRecords(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	rawAuthKeyID := [8]byte{1, 2, 3}
	businessAuthKeyID := [8]byte{9, 8, 7}
	const sessionID = int64(42)
	const userID = int64(1000000002)
	c := &Conn{sessionID: sessionID, authKeyID: rawAuthKeyID}
	if err := sm.Register(c); err != nil {
		t.Fatalf("Register err = %v", err)
	}
	sm.BindAuthKeyForSession(rawAuthKeyID, sessionID, businessAuthKeyID)
	sm.BindUserForAuthKey(rawAuthKeyID, sessionID, userID)
	sm.SetClientLayerForAuthKey(rawAuthKeyID, sessionID, 228)
	sm.SetReceivesUpdatesForAuthKey(rawAuthKeyID, sessionID, true)

	records := sm.ActiveLocationRecords("edge-a")
	if len(records) != 1 {
		t.Fatalf("location records = %+v, want one", records)
	}
	got := records[0]
	if got.InstanceID != "edge-a" ||
		got.RawAuthKeyID != rawAuthKeyID ||
		got.BusinessAuthKeyID != businessAuthKeyID ||
		got.SessionID != sessionID ||
		got.UserID != userID ||
		!got.ReceivesUpdates ||
		got.Layer != 228 {
		t.Fatalf("location record = %+v, want active session identity/layer", got)
	}

	sm.Unregister(c)
	if records := sm.ActiveLocationRecords("edge-a"); len(records) != 0 {
		t.Fatalf("records after unregister = %+v, want empty", records)
	}
}

func TestSessionManagerLocationRegistryPublishesSnapshotAndRenewsOneInstanceLease(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const sessions = 64
	for i := 0; i < sessions; i++ {
		c := &Conn{sessionID: int64(i + 1), authKeyID: [8]byte{byte(i + 1)}}
		if err := sm.Register(c); err != nil {
			t.Fatalf("Register(%d) err = %v", i, err)
		}
	}
	registry := newCaptureLocationRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sm.StartLocationRegistry(ctx, registry, "edge-b", time.Second, 20*time.Millisecond); err != nil {
		t.Fatalf("StartLocationRegistry err = %v", err)
	}
	records, acquireCalls, _, applyCalls := registry.snapshot()
	if len(records) != sessions || acquireCalls != 1 || applyCalls != 1 {
		t.Fatalf("initial locations=%d acquire=%d apply=%d, want %d/1/1", len(records), acquireCalls, applyCalls, sessions)
	}
	deadline := time.After(time.Second)
	for {
		_, _, renewCalls, currentApplyCalls := registry.snapshot()
		if renewCalls >= 2 {
			if currentApplyCalls != 1 {
				t.Fatalf("lease renew rewrote session snapshots: apply calls = %d, want 1", currentApplyCalls)
			}
			return
		}
		select {
		case <-registry.called:
		case <-deadline:
			t.Fatal("instance lease was not renewed")
		}
	}
}

func TestSessionManagerLocationRegistryStartFailsWithoutLease(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	registry := newCaptureLocationRegistry()
	registry.acquireErr = errors.New("redis unavailable")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sm.StartLocationRegistry(ctx, registry, "edge-down", time.Minute, 20*time.Second); err == nil {
		t.Fatal("StartLocationRegistry err = nil, want lease acquisition failure")
	}
}

func TestSessionManagerLocationMutationFailureKeepsLeasedSessions(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	registry := newCaptureLocationRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sm.StartLocationRegistry(ctx, registry, "edge-lease", time.Minute, 20*time.Second); err != nil {
		t.Fatalf("StartLocationRegistry err = %v", err)
	}
	firstTransport := newSlowCloseTransport(0, nil)
	first := &Conn{sessionID: 44, authKeyID: [8]byte{4, 4, 4}, transport: firstTransport}
	if err := sm.Register(first); err != nil {
		t.Fatalf("register leased session: %v", err)
	}
	registry.setApplyErr(errors.New("redis unavailable"))
	second := &Conn{sessionID: 55, authKeyID: [8]byte{5, 5, 5}, transport: newSlowCloseTransport(0, nil)}
	if err := sm.Register(second); err == nil {
		t.Fatal("register unpublished session err = nil")
	}
	if got := sm.Online(); got != 1 {
		t.Fatalf("online after one location mutation failure = %d, want existing session only", got)
	}
	if got := firstTransport.closes.Load(); got != 0 {
		t.Fatalf("leased transport closes = %d, want 0", got)
	}
}

func TestSessionManagerTransientLocationLeaseRenewFailureKeepsSessions(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	registry := newCaptureLocationRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sm.StartLocationRegistry(ctx, registry, "edge-transient", time.Minute, 20*time.Second); err != nil {
		t.Fatalf("StartLocationRegistry err = %v", err)
	}
	transports := []*slowCloseTransport{newSlowCloseTransport(0, nil), newSlowCloseTransport(0, nil)}
	for i, transport := range transports {
		if err := sm.Register(&Conn{sessionID: int64(i + 1), authKeyID: [8]byte{byte(i + 1)}, transport: transport}); err != nil {
			t.Fatalf("register session %d: %v", i, err)
		}
	}

	state := sm.locationState.Load()
	registry.setRenewErr(errors.New("redis temporarily unavailable"))
	if state.renew(sm) {
		t.Fatal("transient failed renewal reported success")
	}
	select {
	case err := <-sm.LocationRegistryFatal():
		t.Fatalf("transient renewal failure reported fatal error: %v", err)
	default:
	}
	if got := sm.Online(); got != len(transports) {
		t.Fatalf("online after transient renewal failure = %d, want %d", got, len(transports))
	}
	for i, transport := range transports {
		if got := transport.closes.Load(); got != 0 {
			t.Fatalf("transport %d closes after transient renewal failure = %d, want 0", i, got)
		}
	}

	registry.setRenewErr(nil)
	if !state.renew(sm) {
		t.Fatal("renewal did not recover after transient failure")
	}
}

func TestSessionManagerLocationLeaseLossFailsClosed(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	registry := newCaptureLocationRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sm.StartLocationRegistry(ctx, registry, "edge-lost", time.Minute, 20*time.Second); err != nil {
		t.Fatalf("StartLocationRegistry err = %v", err)
	}
	transports := []*slowCloseTransport{newSlowCloseTransport(0, nil), newSlowCloseTransport(0, nil)}
	for i, transport := range transports {
		if err := sm.Register(&Conn{sessionID: int64(i + 1), authKeyID: [8]byte{byte(i + 1)}, transport: transport}); err != nil {
			t.Fatalf("register session %d: %v", i, err)
		}
	}
	registry.setRenewErr(errors.New("redis unavailable"))
	state := sm.locationState.Load()
	state.setLeaseExpiry(time.Now().Add(-time.Second))
	state.renew(sm)
	select {
	case err := <-sm.LocationRegistryFatal():
		if !errors.Is(err, edgecontrol.ErrLocationLeaseLost) {
			t.Fatalf("location fatal error = %v, want ErrLocationLeaseLost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("location lease loss did not notify Edge runtime")
	}
	for i, transport := range transports {
		select {
		case <-transport.done:
		case <-time.After(time.Second):
			t.Fatalf("transport %d was not closed after lease loss", i)
		}
	}
}

func TestSessionManagerLocationRegistryPublishesOnlyChangedSession(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	firstRaw := [8]byte{6, 6, 6}
	secondRaw := [8]byte{8, 8, 8}
	businessAuthKeyID := [8]byte{7, 7, 7}
	registry := newCaptureLocationRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sm.StartLocationRegistry(ctx, registry, "edge-c", time.Minute, 20*time.Second); err != nil {
		t.Fatalf("StartLocationRegistry err = %v", err)
	}
	first := &Conn{sessionID: 88, authKeyID: firstRaw}
	second := &Conn{sessionID: 99, authKeyID: secondRaw}
	for _, conn := range []*Conn{first, second} {
		if err := sm.Register(conn); err != nil {
			t.Fatalf("Register err = %v", err)
		}
	}
	registry.resetBatches()
	sm.BindAuthKeyForSession(firstRaw, 88, businessAuthKeyID)
	sm.BindUserForAuthKey(firstRaw, 88, 1000000004)
	batch := registry.lastBatch()
	if len(batch) != 1 {
		t.Fatalf("identity refresh mutations = %+v, want one changed session", batch)
	}
	got := batch[0].Record
	if got.RawAuthKeyID != firstRaw || got.SessionID != 88 || got.BusinessAuthKeyID != businessAuthKeyID || got.UserID != 1000000004 {
		t.Fatalf("identity refresh record = %+v", got)
	}
	registry.resetBatches()
	sm.Unregister(second)
	batch = registry.lastBatch()
	if len(batch) != 1 || !batch[0].Deleted || batch[0].Record.RawAuthKeyID != secondRaw || batch[0].Record.SessionID != 99 {
		t.Fatalf("unregister mutations = %+v, want one deleted session", batch)
	}
}

func TestSessionManagerChannelMembershipProjectionIsUserScopedAndIndependent(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	registry := newCaptureLocationRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sm.StartLocationRegistry(ctx, registry, "edge-membership", time.Minute, 20*time.Second); err != nil {
		t.Fatalf("StartLocationRegistry err = %v", err)
	}
	const userID = int64(1000000042)
	firstRaw := [8]byte{0x41}
	first := &Conn{sessionID: 41, authKeyID: firstRaw}
	if err := sm.Register(first); err != nil {
		t.Fatal(err)
	}
	sm.BindUserForAuthKey(firstRaw, first.sessionID, userID)
	channelIDs := make([]int64, 9000)
	for i := range channelIDs {
		channelIDs[i] = int64(i + 1)
	}
	if !syncTestSessionChannelMemberships(t, sm, firstRaw, first.sessionID, userID, channelIDs) {
		t.Fatal("large membership sync did not commit")
	}
	memberships, batches := registry.membershipSnapshot()
	if got := memberships[userID]; len(got.ChannelIDs) != len(channelIDs) || batches != 1 {
		t.Fatalf("published membership channels=%d batches=%d, want %d/1", len(got.ChannelIDs), batches, len(channelIDs))
	}

	// A session-only interest mutation must not reindex the 9000 memberships.
	registry.resetBatches()
	sm.TrackChannelInterest(firstRaw, first.sessionID, userID, []int64{99999})
	if _, batches := registry.membershipSnapshot(); batches != 0 {
		t.Fatalf("session location mutation rewrote membership batches=%d", batches)
	}

	// A second session on the same Edge reuses the first session's baseline and
	// must not publish another (instance,user) membership record.
	secondRaw := [8]byte{0x42}
	second := &Conn{sessionID: 42, authKeyID: secondRaw}
	if err := sm.Register(second); err != nil {
		t.Fatal(err)
	}
	sm.BindUserForAuthKey(secondRaw, second.sessionID, userID)
	if !syncTestSessionChannelMemberships(t, sm, secondRaw, second.sessionID, userID, nil) {
		t.Fatal("second session did not reuse membership baseline")
	}
	if _, batches := registry.membershipSnapshot(); batches != 0 {
		t.Fatalf("second session duplicated membership batches=%d", batches)
	}

	// Removing one of two equivalent sessions is a projection no-op. Removing
	// the last session deletes the single user-level membership projection.
	sm.Unregister(first)
	if _, batches := registry.membershipSnapshot(); batches != 0 {
		t.Fatalf("first session removal rewrote membership batches=%d", batches)
	}
	sm.Unregister(second)
	memberships, batches = registry.membershipSnapshot()
	if len(memberships) != 0 || batches != 1 {
		t.Fatalf("last session removal memberships=%v batches=%d, want empty/1", memberships, batches)
	}
}

func TestBindAuthKeyForRawAuthKeyUpdatesEveryTemporarySession(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{0x71}
	perm := [8]byte{0x31}
	expiresAt := int(time.Now().Add(time.Hour).Unix())
	c1 := &Conn{sessionID: 101, authKeyID: raw, authKeyExpiresAt: expiresAt}
	c2 := &Conn{sessionID: 102, authKeyID: raw, authKeyExpiresAt: expiresAt}
	if err := sm.Register(c1); err != nil {
		t.Fatalf("register c1: %v", err)
	}
	if err := sm.Register(c2); err != nil {
		t.Fatalf("register c2: %v", err)
	}
	sm.BindAuthKeyForSession(raw, c1.sessionID, raw)
	sm.BindAuthKeyForSession(raw, c2.sessionID, raw)
	sm.BindUserForAuthKey(raw, c1.sessionID, 1001)
	sm.BindUserForAuthKey(raw, c2.sessionID, 1001)

	if got := sm.BindAuthKeyForRawAuthKey(raw, perm); got != 2 {
		t.Fatalf("bound sessions = %d, want 2", got)
	}
	for _, sessionID := range []int64{c1.sessionID, c2.sessionID} {
		if got, ok := sm.AuthKeyIDForSession(raw, sessionID); !ok || got != perm {
			t.Fatalf("session %d business key = %x/%v, want perm %x", sessionID, got, ok, perm)
		}
		if userID, resolved := sm.UserIDResolvedForAuthKey(raw, sessionID); resolved || userID != 0 {
			t.Fatalf("session %d user after identity switch = %d/%v, want unresolved", sessionID, userID, resolved)
		}
		if got, found := sm.AuthKeyExpiresAtForSession(raw, sessionID); !found || got != expiresAt {
			t.Fatalf("session %d raw expiry = %d/%v, want %d", sessionID, got, found, expiresAt)
		}
	}
}

func TestRawAuthKeyIdentityUpdatesAndInheritsAcrossSessions(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{0x72}
	perm := [8]byte{0x32}
	const userID = int64(1001)
	first := &Conn{sessionID: 201, authKeyID: raw}
	second := &Conn{sessionID: 202, authKeyID: raw}
	if err := sm.Register(first); err != nil {
		t.Fatalf("register first: %v", err)
	}
	if err := sm.Register(second); err != nil {
		t.Fatalf("register second: %v", err)
	}

	sm.BindAuthKeyForSession(raw, first.sessionID, perm)
	if got, ok := sm.AuthKeyIDForSession(raw, second.sessionID); !ok || got != perm {
		t.Fatalf("second session business key = %x/%v, want %x/true", got, ok, perm)
	}

	sm.BindUserForAuthKey(raw, first.sessionID, userID)
	if got, ok := sm.UserIDResolvedForAuthKey(raw, second.sessionID); !ok || got != userID {
		t.Fatalf("second session user = %d/%v, want %d/true", got, ok, userID)
	}

	third := &Conn{sessionID: 203, authKeyID: raw}
	if err := sm.Register(third); err != nil {
		t.Fatalf("register third: %v", err)
	}
	if got, ok := sm.AuthKeyIDForSession(raw, third.sessionID); !ok || got != perm {
		t.Fatalf("third session inherited business key = %x/%v, want %x/true", got, ok, perm)
	}
	if got, ok := sm.UserIDResolvedForAuthKey(raw, third.sessionID); !ok || got != userID {
		t.Fatalf("third session inherited user = %d/%v, want %d/true", got, ok, userID)
	}

	if got := sm.UnbindAuthKey(perm); got != 3 {
		t.Fatalf("unbind affected = %d, want 3", got)
	}
	afterUnbind := &Conn{sessionID: 204, authKeyID: raw}
	if err := sm.Register(afterUnbind); err != nil {
		t.Fatalf("register after unbind: %v", err)
	}
	if got, ok := sm.AuthKeyIDForSession(raw, afterUnbind.sessionID); !ok || got != perm {
		t.Fatalf("post-unbind session business key = %x/%v, want %x/true", got, ok, perm)
	}
	if got, ok := sm.UserIDResolvedForAuthKey(raw, afterUnbind.sessionID); ok || got != 0 {
		t.Fatalf("post-unbind session user = %d/%v, want unresolved", got, ok)
	}
}

func TestSessionManagerReplacementClosesOldPhysicalTransport(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	oldTransport := &closeCountingTransport{}
	old := &Conn{sessionID: 42, authKeyID: raw, transport: oldTransport}
	replacement := &Conn{sessionID: 42, authKeyID: raw}

	sm.Register(old)
	sm.Register(replacement)
	if oldTransport.closes != 1 {
		t.Fatalf("old transport closes = %d, want 1", oldTransport.closes)
	}
	// When the old serveConn exits later it must not delete the replacement from the index.
	sm.Unregister(old)
	if got, ok := sm.bySession[sessionKey{authKeyID: raw, sessionID: 42}]; !ok || got != replacement {
		t.Fatal("old unregister removed the replacement connection")
	}
}

func TestSessionManagerDestroyClosesPhysicalTransport(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	observer := &sessionDestructionRecorder{}
	sm.SetLifecycleObserver(observer)
	raw := [8]byte{4, 5, 6}
	physical := &closeCountingTransport{}
	c := &Conn{sessionID: 77, authKeyID: raw, transport: physical}
	sm.Register(c)

	if !sm.DestroySessionForAuthKey(raw, 77) {
		t.Fatal("DestroySessionForAuthKey returned false")
	}
	if physical.closes != 1 {
		t.Fatalf("destroyed transport closes = %d, want 1", physical.closes)
	}
	if sm.Online() != 0 {
		t.Fatalf("online after destroy = %d, want 0", sm.Online())
	}
	if len(observer.destroyed) != 1 || observer.destroyed[0] != (sessionKey{authKeyID: raw, sessionID: 77}) {
		t.Fatalf("destroy callbacks = %+v, want exact destroyed session", observer.destroyed)
	}
	// An explicit destroy for an already-offline logical session must still
	// invalidate retained exact-profile metadata even though the wire response
	// remains destroy_session_none.
	if sm.DestroySessionForAuthKey(raw, 88) {
		t.Fatal("missing session reported destroyed")
	}
	if len(observer.destroyed) != 2 || observer.destroyed[1] != (sessionKey{authKeyID: raw, sessionID: 88}) {
		t.Fatalf("offline destroy callbacks = %+v", observer.destroyed)
	}
}

func TestSessionManagerUnregisterIsNotSessionDestruction(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	observer := &sessionDestructionRecorder{}
	sm.SetLifecycleObserver(observer)
	c := &Conn{sessionID: 91, authKeyID: [8]byte{9, 1}}
	if err := sm.Register(c); err != nil {
		t.Fatal(err)
	}
	sm.Unregister(c)
	if len(observer.destroyed) != 0 {
		t.Fatalf("ordinary unregister emitted destruction: %+v", observer.destroyed)
	}
}

func TestSessionManagerBestEffortFanoutPreparesOncePerProfile(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(100)
	conns := make([]*Conn, 0, 2)
	for i := 0; i < 2; i++ {
		c := &Conn{
			sessionID:       int64(i + 1),
			authKeyID:       [8]byte{byte(i + 1)},
			outbound:        make(chan *outboundOp, 1),
			outboundControl: make(chan *outboundOp, 1),
			outboundStop:    make(chan struct{}),
			metrics:         NopMetrics{},
		}
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
		c.receivesUpdates.Store(true)
		if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
			t.Fatalf("freeze profile: %v", err)
		}
		sm.Register(c)
		conns = append(conns, c)
	}

	sent, err := sm.PushToUserExceptSessionBestEffort(
		context.Background(),
		userID,
		0,
		proto.MessageFromServer,
		&tg.UpdatesTooLong{},
		0,
	)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if sent != 2 {
		t.Fatalf("sent = %d, want 2", sent)
	}
	first := <-conns[0].outbound
	second := <-conns[1].outbound
	defer first.releaseReservation(conns[0].outboundTrackedBudget)
	defer second.releaseReservation(conns[1].outboundTrackedBudget)
	if first.encoded == nil || second.encoded == nil || !sameBacking(first.encoded.body, second.encoded.body) {
		t.Fatal("same-profile fanout did not share one exact prepared body")
	}
	if first.encoded.layer == nil || second.encoded.layer == nil || first.encoded.layer == second.encoded.layer {
		t.Fatal("same-profile fanout shared connection-specific epoch binding")
	}
}

func TestSessionManagerMixedLayerFanoutUsesProfileBoundBodies(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(103)
	authKeyID := [8]byte{0x22, 0x70, 0x22, 0x80}
	profiles := []tlprofile.Profile{tlprofile.Profile225, tlprofile.Profile227, tlprofile.Profile228}
	conns := make([]*Conn, 0, len(profiles))
	for _, profile := range profiles {
		c := &Conn{
			sessionID:       int64(profile),
			authKeyID:       authKeyID,
			outbound:        make(chan *outboundOp, 1),
			outboundControl: make(chan *outboundOp, 1),
			outboundStop:    make(chan struct{}),
			metrics:         NopMetrics{},
		}
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
		c.receivesUpdates.Store(true)
		if profile == tlprofile.Profile228 {
			if err := c.SeedInheritedLayerProfile(tlprofile.Profile227); err != nil {
				t.Fatalf("seed Alice inherited profile: %v", err)
			}
		}
		if err := c.FreezeLayerProfile(profile); err != nil {
			t.Fatalf("freeze profile %d: %v", profile, err)
		}
		sm.Register(c)
		conns = append(conns, c)
	}

	value := testLayerChannelUpdatesValue(321)
	sent, err := sm.PushToUserExceptSessionBestEffort(
		context.Background(), userID, 0, proto.MessageFromServer, value, 0,
	)
	if err != nil || sent != len(conns) {
		t.Fatalf("mixed fanout = sent:%d err:%v", sent, err)
	}
	var previous *encodedOutboundMessage
	for i, c := range conns {
		op := <-c.outbound
		defer op.releaseReservation(c.outboundTrackedBudget)
		if op.encoded == nil || op.encoded.layer == nil || op.encoded.layer.profile != profiles[i] {
			t.Fatalf("connection %d binding = %#v", i, op.encoded)
		}
		state := c.LayerProfileState()
		if op.encoded.layer.kind != outboundLayerBindingSession || op.encoded.layer.epoch != state.Epoch {
			t.Fatalf("connection %d target binding = %#v, state=%#v", i, op.encoded.layer, state)
		}
		if previous == op.encoded {
			t.Fatal("different profiles shared one final wire body")
		}
		previous = op.encoded
		wantChannelID := testChannelWireID(profiles[i])
		if !bytes.Contains(op.encoded.body, littleEndianID(wantChannelID)) {
			t.Fatalf("profile %d push lacks channel constructor %#08x", profiles[i], wantChannelID)
		}
		if otherChannelID := testOtherChannelWireID(profiles[i]); bytes.Contains(op.encoded.body, littleEndianID(otherChannelID)) {
			t.Fatalf("profile %d push leaked channel constructor %#08x", profiles[i], otherChannelID)
		}
		input := bin.Buffer{Buf: op.encoded.body}
		decoded, decodeErr := tlprofile.DecodeObject(profiles[i], &input, tlprofile.Limits{})
		if decodeErr != nil || input.Len() != 0 {
			t.Fatalf("decode profile %d: remaining=%d err=%v", profiles[i], input.Len(), decodeErr)
		}
		updates := decoded.(*tg.Updates)
		channel, ok := updates.Chats[0].(*tg.Channel)
		if !ok || channel.ID != 100 {
			t.Fatalf("profile %d decoded channel=%#v", profiles[i], updates.Chats)
		}
		status := updates.Updates[0].(*tg.UpdateUserStatus).Status.(*tg.UserStatusOnline)
		if status.Expires != 321 {
			t.Fatalf("profile %d decoded expires=%d", profiles[i], status.Expires)
		}
	}
}

func TestSessionManagerPendingFanoutSharesOneEncodedBodyAndBudget(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(102)
	keys := make([]sessionKey, 0, 2)
	for i := 0; i < 2; i++ {
		c := &Conn{sessionID: int64(i + 1), authKeyID: [8]byte{byte(i + 1)}}
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
		sm.Register(c)
		keys = append(keys, connSessionKey(c))
	}

	msg := &tg.UpdatesTooLong{}
	sent, err := sm.PushToUserExceptSession(context.Background(), userID, 0, proto.MessageFromServer, msg)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if sent != 2 {
		t.Fatalf("pending fanout sent=%d, want 2", sent)
	}

	sm.mu.Lock()
	first := sm.pending[keys[0]][0]
	second := sm.pending[keys[1]][0]
	if first.updates != second.updates || first.reservation != second.reservation {
		sm.mu.Unlock()
		t.Fatal("pending sessions did not share frozen updates/reservation")
	}
	wantBytes := int64(first.updates.canonicalSize())
	sm.deletePendingLocked(keys[0])
	if got := sm.pendingBudget.snapshot(); got != wantBytes {
		sm.mu.Unlock()
		t.Fatalf("budget after first session drop = %d, want shared body %d", got, wantBytes)
	}
	sm.deletePendingLocked(keys[1])
	sm.mu.Unlock()
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("budget after last session drop = %d, want 0", got)
	}
}

func TestSessionManagerBestEffortFanoutUsesOneBudgetAndDropsOnlySlowConsumers(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(101)

	// Three full queues simulate three slow devices; with no outbound actor, ensuring the queues do not drain on their own during the test.
	slow := make([]*Conn, 0, 3)
	for i := 0; i < 3; i++ {
		tr := &closeCountingTransport{}
		c := &Conn{
			sessionID:       int64(i + 1),
			authKeyID:       [8]byte{byte(i + 1)},
			transport:       tr,
			metrics:         NopMetrics{},
			outbound:        make(chan *outboundOp, 1),
			outboundControl: make(chan *outboundOp, 1),
			outboundStop:    make(chan struct{}),
		}
		c.outbound <- &outboundOp{}
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
		c.receivesUpdates.Store(true)
		if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
			t.Fatalf("freeze profile: %v", err)
		}
		sm.Register(c)
		slow = append(slow, c)
	}

	healthy := &Conn{
		sessionID:       99,
		authKeyID:       [8]byte{99},
		metrics:         NopMetrics{},
		outbound:        make(chan *outboundOp, 1),
		outboundControl: make(chan *outboundOp, 1),
		outboundStop:    make(chan struct{}),
	}
	healthy.userID.Store(userID)
	healthy.userIDResolved.Store(true)
	healthy.receivesUpdates.Store(true)
	if err := healthy.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatalf("freeze healthy profile: %v", err)
	}
	sm.Register(healthy)

	const budget = 40 * time.Millisecond
	start := time.Now()
	sent, err := sm.PushToUserExceptSessionBestEffort(
		context.Background(), userID, 0, proto.MessageFromServer, &tg.UpdatesTooLong{}, budget,
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want only healthy session", sent)
	}
	if elapsed >= 3*budget {
		t.Fatalf("fan-out waited %v; want one shared %v budget, not one per slow session", elapsed, budget)
	}
	if got := len(healthy.outbound); got != 1 {
		t.Fatalf("healthy queued ops = %d, want 1", got)
	}
	if healthy.isRetired() {
		t.Fatal("healthy session was terminalized")
	}
	for i, c := range slow {
		if !c.isRetired() {
			t.Fatalf("slow session %d was not terminalized", i)
		}
		if tr := c.transport.(*closeCountingTransport); tr.closes != 1 {
			t.Fatalf("slow session %d transport closes = %d, want 1", i, tr.closes)
		}
	}
}

func TestSessionManagerScopesSameSessionIDByAuthKey(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw1 := [8]byte{1}
	raw2 := [8]byte{2}
	perm1 := [8]byte{9}
	c1 := &Conn{sessionID: 42, authKeyID: raw1}
	c2 := &Conn{sessionID: 42, authKeyID: raw2}

	sm.Register(c1)
	sm.Register(c2)
	if got := sm.Online(); got != 2 {
		t.Fatalf("online = %d, want 2", got)
	}

	sm.BindAuthKeyForSession(raw1, 42, perm1)
	// Two PFS/raw connections can resolve to the same business perm key and reuse the
	// same session_id; the exact exclusion must match only raw1 and must not exclude
	// raw2 along with it by business key.
	sm.BindAuthKeyForSession(raw2, 42, perm1)
	sm.BindUserForAuthKey(raw1, 42, 100)
	sm.BindUserForAuthKey(raw2, 42, 200)

	if userID, ok := sm.UserIDForAuthKey(raw1, 42); !ok || userID != 100 {
		t.Fatalf("scoped user raw1 = %d ok %v, want 100/true", userID, ok)
	}
	if userID, ok := sm.UserIDForAuthKey(raw2, 42); !ok || userID != 200 {
		t.Fatalf("scoped user raw2 = %d ok %v, want 200/true", userID, ok)
	}
	if _, ok := sm.UserID(42); ok {
		t.Fatal("legacy UserID unexpectedly resolved ambiguous session_id")
	}
	if err := sm.PushToSession(context.Background(), 42, proto.MessageFromServer, &tg.UpdatesTooLong{}); !errors.Is(err, ErrSessionAmbiguous) {
		t.Fatalf("ambiguous push err = %v, want ErrSessionAmbiguous", err)
	}

	sm.BindUserForAuthKey(raw1, 42, 300)
	sm.BindUserForAuthKey(raw2, 42, 300)
	sent, err := sm.PushToUserExceptAuthKeySession(context.Background(), 300, raw1, 42, proto.MessageFromServer, &tg.UpdatesTooLong{})
	if err != nil {
		t.Fatalf("push except scoped session: %v", err)
	}
	if sent != 1 {
		t.Fatalf("pushed to %d sessions, want 1", sent)
	}
	if _, ok := sm.pending[sessionKey{authKeyID: raw1, sessionID: 42}]; ok {
		t.Fatal("excluded session received pending push")
	}
	if got := len(sm.pending[sessionKey{authKeyID: raw2, sessionID: 42}]); got != 1 {
		t.Fatalf("raw2 pending pushes = %d, want 1", got)
	}
	if !sm.DestroySessionForAuthKey(raw1, 42) {
		t.Fatal("scoped destroy did not remove raw1 session")
	}
	if _, ok := sm.AuthKeyIDForSession(raw1, 42); ok {
		t.Fatal("raw1 session survived scoped destroy")
	}
	if _, ok := sm.AuthKeyIDForSession(raw2, 42); !ok {
		t.Fatal("same session_id under raw2 was removed by scoped destroy")
	}
}

func TestSessionManagerCloseSessionsForBusinessAuthKeyClosesBoundTempAndRaw(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	rawTemp := [8]byte{1}
	perm := [8]byte{9}
	otherRaw := [8]byte{2}
	otherPerm := [8]byte{8}
	tempTransport := &closeCountingTransport{}
	permTransport := &closeCountingTransport{}
	otherTransport := &closeCountingTransport{}
	cTemp := &Conn{sessionID: 11, authKeyID: rawTemp, transport: tempTransport}
	cPerm := &Conn{sessionID: 12, authKeyID: perm, transport: permTransport}
	cOther := &Conn{sessionID: 13, authKeyID: otherRaw}
	cOther.transport = otherTransport

	sm.Register(cTemp)
	sm.Register(cPerm)
	sm.Register(cOther)
	sm.BindAuthKeyForSession(rawTemp, 11, perm)
	sm.BindAuthKeyForSession(perm, 12, perm)
	sm.BindAuthKeyForSession(otherRaw, 13, otherPerm)
	sm.BindUserForAuthKey(rawTemp, 11, 100)
	sm.BindUserForAuthKey(perm, 12, 100)
	sm.BindUserForAuthKey(otherRaw, 13, 200)

	if closed := sm.CloseSessionsForBusinessAuthKey(perm); closed != 2 {
		t.Fatalf("closed sessions = %d, want 2", closed)
	}
	if tempTransport.closes != 1 || permTransport.closes != 1 {
		t.Fatalf("transport closes temp=%d perm=%d, want 1/1", tempTransport.closes, permTransport.closes)
	}
	if otherTransport.closes != 0 {
		t.Fatalf("other transport closes = %d, want 0", otherTransport.closes)
	}
	if got := sm.Online(); got != 1 {
		t.Fatalf("online after close = %d, want 1", got)
	}
	if _, ok := sm.AuthKeyIDForSession(rawTemp, 11); ok {
		t.Fatal("temp session still indexed after business auth key close")
	}
	if _, ok := sm.AuthKeyIDForSession(perm, 12); ok {
		t.Fatal("raw perm session still indexed after business auth key close")
	}
	if userID, ok := sm.UserIDForAuthKey(otherRaw, 13); !ok || userID != 200 {
		t.Fatalf("other session user = %d ok %v, want 200/true", userID, ok)
	}
	if closed := sm.CloseSessionsForBusinessAuthKey(perm); closed != 0 {
		t.Fatalf("second close = %d, want 0", closed)
	}
}

func TestSessionManagerCloseSessionsRunsSlowPhysicalClosesConcurrently(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	business := [8]byte{9, 9, 9}
	const sessions = 8
	const closeDelay = 75 * time.Millisecond
	transports := make([]*slowCloseTransport, 0, sessions)
	for i := 0; i < sessions; i++ {
		raw := [8]byte{byte(i + 1)}
		tr := newSlowCloseTransport(closeDelay, nil)
		c := &Conn{sessionID: int64(i + 1), authKeyID: raw, transport: tr}
		sm.Register(c)
		sm.BindAuthKeyForSession(raw, c.sessionID, business)
		transports = append(transports, tr)
	}

	started := time.Now()
	if got := sm.CloseSessionsForBusinessAuthKey(business); got != sessions {
		t.Fatalf("closed sessions = %d, want %d", got, sessions)
	}
	elapsed := time.Since(started)
	// A serial implementation takes ~600ms. Leave ample Windows/CI scheduling margin while
	// still proving that the per-Conn delay is not multiplied by the session count.
	if elapsed >= 4*closeDelay {
		t.Fatalf("batch close elapsed = %v, want concurrent closes near %v", elapsed, closeDelay)
	}
	for i, tr := range transports {
		select {
		case <-tr.done:
		default:
			t.Fatalf("transport %d close had not completed when batch returned", i)
		}
		if got := tr.closes.Load(); got != 1 {
			t.Fatalf("transport %d closes = %d, want 1", i, got)
		}
	}
}

func TestSessionManagerCloseRawSessionsExceptRunsConcurrentlyAndPreservesExcluded(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{6, 6, 6}
	const sessions = 7
	const excludedSession = int64(4)
	const closeDelay = 60 * time.Millisecond
	transports := make([]*slowCloseTransport, 0, sessions)
	for i := 0; i < sessions; i++ {
		tr := newSlowCloseTransport(closeDelay, nil)
		c := &Conn{sessionID: int64(i + 1), authKeyID: raw, transport: tr}
		sm.Register(c)
		transports = append(transports, tr)
	}

	started := time.Now()
	if got, want := sm.CloseSessionsForRawAuthKeyExcept(raw, excludedSession), sessions-1; got != want {
		t.Fatalf("closed sessions = %d, want %d", got, want)
	}
	if elapsed := time.Since(started); elapsed >= 4*closeDelay {
		t.Fatalf("raw-key batch close elapsed = %v, want concurrent closes near %v", elapsed, closeDelay)
	}
	for i, tr := range transports {
		sessionID := int64(i + 1)
		if sessionID == excludedSession {
			if got := tr.closes.Load(); got != 0 {
				t.Fatalf("excluded transport closes = %d, want 0", got)
			}
			continue
		}
		select {
		case <-tr.done:
		default:
			t.Fatalf("transport for session %d had not closed", sessionID)
		}
	}
	if _, ok := sm.bySession[sessionKey{authKeyID: raw, sessionID: excludedSession}]; !ok {
		t.Fatal("excluded session was removed from the registry")
	}
	// Clean up the deliberately preserved connection without making the assertion path depend
	// on test process teardown.
	if !sm.DestroySessionForAuthKey(raw, excludedSession) {
		t.Fatal("cleanup destroy of excluded session failed")
	}
}

func TestForceCloseBatchTimeoutStillClosesProducerAndRPCGates(t *testing.T) {
	release := make(chan struct{})
	const sessions = 4
	scheduler := newInboundRPCScheduler(1, 16, 1<<20)
	defer scheduler.stop(time.Second)
	conns := make([]*Conn, 0, sessions)
	transports := make([]*slowCloseTransport, 0, sessions)
	for i := 0; i < sessions; i++ {
		tr := newSlowCloseTransport(0, release)
		c := &Conn{
			transport:       tr,
			metrics:         NopMetrics{},
			outbound:        make(chan *outboundOp, 1),
			outboundControl: make(chan *outboundOp, 1),
			outboundStop:    make(chan struct{}),
		}
		c.startInboundRPCScheduler(scheduler, 1, 1, time.Second)
		if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
			method: "shutdown.budget",
			size:   32,
		}); err != nil {
			t.Fatalf("enqueue queued RPC %d: %v", i, err)
		}
		conns = append(conns, c)
		transports = append(transports, tr)
	}

	started := time.Now()
	if completed := forceCloseConnBatch(conns, 40*time.Millisecond); completed {
		t.Fatal("blocked transport close batch unexpectedly completed")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("timed batch close blocked for %v", elapsed)
	}
	for i, c := range conns {
		if !c.isRetired() {
			t.Fatalf("connection %d producer gate remains open after batch timeout", i)
		}
		select {
		case <-c.outboundStop:
		default:
			t.Fatalf("connection %d outbound stop was not published", i)
		}
		select {
		case <-c.rpcRootCtx.Done():
		default:
			t.Fatalf("connection %d RPC root remains open after batch timeout", i)
		}
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("RPC budget after batch gate close = tasks:%d bytes:%d, want zero", tasks, bytes)
	}

	close(release)
	for i, tr := range transports {
		select {
		case <-tr.done:
		case <-time.After(time.Second):
			t.Fatalf("transport %d did not finish after release", i)
		}
	}
}

func TestSessionManagerBusinessAuthKeyIndexTracksRebind(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1}
	oldPerm := [8]byte{7}
	newPerm := [8]byte{8}
	c := &Conn{sessionID: 21, authKeyID: raw}

	sm.Register(c)
	sm.BindAuthKeyForSession(raw, 21, oldPerm)
	sm.BindAuthKeyForSession(raw, 21, newPerm)

	if closed := sm.CloseSessionsForBusinessAuthKey(oldPerm); closed != 0 {
		t.Fatalf("close old business auth key = %d, want 0", closed)
	}
	if got := sm.Online(); got != 1 {
		t.Fatalf("online after closing old key = %d, want 1", got)
	}
	if closed := sm.CloseSessionsForBusinessAuthKey(newPerm); closed != 1 {
		t.Fatalf("close new business auth key = %d, want 1", closed)
	}
	if got := sm.Online(); got != 0 {
		t.Fatalf("online after closing new key = %d, want 0", got)
	}
}

func TestPushToUserAuthKeyUsesOneDeadlineAndDropsOnlySlowPFSConnections(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	business := [8]byte{9, 9}
	const userID = int64(100)
	newConn := func(raw [8]byte, sessionID int64, queueFull bool) (*Conn, *closeCountingTransport) {
		transport := &closeCountingTransport{}
		c := &Conn{
			authKeyID:       raw,
			sessionID:       sessionID,
			metrics:         NopMetrics{},
			transport:       transport,
			outbound:        make(chan *outboundOp, 1),
			outboundControl: make(chan *outboundOp, 1),
			outboundStop:    make(chan struct{}),
		}
		c.receivesUpdates.Store(true)
		if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
			t.Fatalf("freeze profile: %v", err)
		}
		if queueFull {
			c.outbound <- &outboundOp{}
		}
		sm.Register(c)
		sm.BindAuthKeyForSession(raw, sessionID, business)
		sm.BindUserForAuthKey(raw, sessionID, userID)
		return c, transport
	}

	slowOne, slowOneTransport := newConn([8]byte{1}, 11, true)
	slowTwo, slowTwoTransport := newConn([8]byte{2}, 12, true)
	healthy, healthyTransport := newConn([8]byte{3}, 13, false)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	sent, err := sm.PushToUserAuthKey(ctx, userID, business, proto.MessageFromServer, &tg.UpdatesTooLong{})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("PushToUserAuthKey: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want only healthy connection", sent)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("elapsed = %v, want one shared deadline rather than per-session waits", elapsed)
	}
	if !slowOne.isRetired() || !slowTwo.isRetired() || slowOneTransport.closes != 1 || slowTwoTransport.closes != 1 {
		t.Fatalf("slow connections not terminal/closed: one=%v/%d two=%v/%d",
			slowOne.isRetired(), slowOneTransport.closes, slowTwo.isRetired(), slowTwoTransport.closes)
	}
	if healthy.isRetired() || healthyTransport.closes != 0 {
		t.Fatalf("healthy connection was dropped: lifecycle=%v closes=%d", healthy.lifecycleState(), healthyTransport.closes)
	}
	select {
	case <-healthy.outbound:
	default:
		t.Fatal("healthy PFS connection did not receive best-effort enqueue")
	}
}

func TestSessionManagerChannelInterestIndex(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{sessionID: 42, authKeyID: raw}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 42, 100)

	sm.TrackChannelInterest(raw, 42, 100, []int64{10, 10, 20})
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 10 online users = %v, want [100]", got)
	}
	sm.TrackChannelInterest(raw, 42, 100, []int64{20})
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel 10 after viewer switch = %v, want empty", got)
	}
	if got := sm.OnlineChannelUserIDs(20, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 20 after viewer switch = %v, want [100]", got)
	}
	sm.TrackChannelInterest(raw, 42, 100, []int64{10})
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel 10 online members before membership sync = %v, want empty", got)
	}
	syncTestSessionChannelMemberships(t, sm, raw, 42, 100, []int64{10, 30})
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 10 online members = %v, want [100]", got)
	}
	if got := sm.OnlineChannelUserIDs(30, 10); len(got) != 0 {
		t.Fatalf("channel 30 viewers = %v, want empty", got)
	}
	if got := sm.OnlineUserIDsForCandidates([]int64{0, 200, 100, 100}, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("candidate online users = %v, want [100]", got)
	}

	sm.BindUserForAuthKey(raw, 42, 200)
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel interest after user switch = %v, want empty", got)
	}
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel membership after user switch = %v, want empty", got)
	}
	sm.TrackChannelInterest(raw, 42, 200, []int64{10})
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 1 || got[0] != 200 {
		t.Fatalf("channel 10 after re-track = %v, want [200]", got)
	}
	sm.AddUserChannelMembership(200, 10)
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 1 || got[0] != 200 {
		t.Fatalf("channel 10 membership after add = %v, want [200]", got)
	}
	sm.RemoveUserChannelMembership(200, 10)
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel membership after remove = %v, want empty", got)
	}
	sm.ClearChannelInterest(raw, 42, 200)
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel interest after explicit clear = %v, want empty", got)
	}

	sm.Unregister(c)
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel interest after unregister = %v, want empty", got)
	}
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel membership after unregister = %v, want empty", got)
	}
}

func TestSessionManagerChannelSubscriptionsAreBoundedDeduplicatedAndExpire(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	rawOne := [8]byte{1, 2, 3}
	rawTwo := [8]byte{4, 5, 6}
	first := &Conn{sessionID: 41, authKeyID: rawOne}
	second := &Conn{sessionID: 42, authKeyID: rawTwo}
	sm.Register(first)
	sm.Register(second)
	sm.BindUserForAuthKey(rawOne, 41, 100)
	sm.BindUserForAuthKey(rawTwo, 42, 100)

	sm.RefreshChannelSubscription(rawOne, 41, 100, 10, time.Second)
	sm.RefreshChannelSubscription(rawTwo, 42, 100, 10, time.Second)
	if got := sm.OnlineChannelSubscriberUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("deduplicated subscribers = %v, want [100]", got)
	}
	if got := sm.OnlineChannelIDsSnapshot(); len(got) != 1 || got[0] != 10 {
		t.Fatalf("subscribed channel snapshot = %v, want [10]", got)
	}

	for channelID := int64(20); channelID < 20+maxChannelSubscriptionsPerSession; channelID++ {
		sm.RefreshChannelSubscription(rawOne, 41, 100, channelID, time.Second)
	}
	// Channel 10 already consumes one of the ten slots, so channel 29 must be
	// refused rather than growing the session-controlled index to eleven.
	if got := sm.OnlineChannelSubscriberUserIDs(29, 10); len(got) != 0 {
		t.Fatalf("subscriber beyond per-session cap = %v, want empty", got)
	}
	if got := sm.OnlineChannelSubscriberUserIDsExcluding(10, map[int64]struct{}{100: {}}, 10); len(got) != 0 {
		t.Fatalf("excluded subscribers = %v, want empty", got)
	}

	sm.RefreshChannelSubscription(rawTwo, 42, 100, 99, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if got := sm.OnlineChannelSubscriberUserIDs(99, 10); len(got) != 0 {
		t.Fatalf("expired subscribers = %v, want empty", got)
	}

	sm.Unregister(first)
	sm.Unregister(second)
	if got := sm.OnlineChannelSubscriberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("subscribers after unregister = %v, want empty", got)
	}
}

func TestSessionManagerClearsChannelIndexesOnAuthAndReadinessChanges(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	business := [8]byte{8}
	c := &Conn{sessionID: 42, authKeyID: raw}
	sm.Register(c)
	sm.BindAuthKeyForSession(raw, 42, business)
	sm.BindUserForAuthKey(raw, 42, 100)

	track := func() {
		sm.TrackChannelInterest(raw, 42, 100, []int64{10})
		sm.RefreshChannelSubscription(raw, 42, 100, 10, time.Second)
		syncTestSessionChannelMemberships(t, sm, raw, 42, 100, []int64{10})
		if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
			t.Fatalf("channel viewers before cleanup = %v, want [100]", got)
		}
		if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
			t.Fatalf("channel members before cleanup = %v, want [100]", got)
		}
		if got := sm.OnlineChannelSubscriberUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
			t.Fatalf("channel subscribers before cleanup = %v, want [100]", got)
		}
	}
	assertCleared := func(label string) {
		if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
			t.Fatalf("%s viewers = %v, want empty", label, got)
		}
		if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
			t.Fatalf("%s members = %v, want empty", label, got)
		}
		if got := sm.OnlineChannelSubscriberUserIDs(10, 10); len(got) != 0 {
			t.Fatalf("%s subscribers = %v, want empty", label, got)
		}
	}

	track()
	sm.SetReceivesUpdatesForAuthKey(raw, 42, false)
	assertCleared("after receivesUpdates=false")

	track()
	sm.BindAuthKeyForSession(raw, 42, [8]byte{9})
	assertCleared("after business auth key change")

	sm.BindAuthKeyForSession(raw, 42, business)
	sm.BindUserForAuthKey(raw, 42, 100)
	track()
	if n := sm.UnbindAuthKey(business); n != 1 {
		t.Fatalf("UnbindAuthKey count = %d, want 1", n)
	}
	assertCleared("after unbind auth key")
}

func TestPushToSessionForAuthKeyImmediateBypassesReadinessQueue(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{
		sessionID:       42,
		authKeyID:       raw,
		outbound:        make(chan *outboundOp, 1),
		outboundControl: make(chan *outboundOp, 1),
		outboundStop:    make(chan struct{}),
	}
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatalf("freeze profile: %v", err)
	}
	sm.Register(c)

	msg := &tg.UpdateShort{Update: &tg.UpdateLoginToken{}, Date: 1700000000}
	if err := sm.PushToSessionForAuthKeyImmediate(context.Background(), raw, 42, proto.MessageFromServer, msg); err != nil {
		t.Fatalf("immediate push: %v", err)
	}

	select {
	case op := <-c.outbound:
		defer op.releaseReservation(c.outboundTrackedBudget)
		if op.encoded == nil {
			t.Fatal("immediate push did not retain its encoded body")
		}
		var got tg.UpdateShort
		if err := got.Decode(&bin.Buffer{Buf: op.encoded.body}); err != nil {
			t.Fatalf("decode enqueued update: %v", err)
		}
		if _, ok := got.Update.(*tg.UpdateLoginToken); !ok || got.Date != msg.Date {
			t.Fatalf("enqueued update = %+v, want login token date %d", got, msg.Date)
		}
	case <-time.After(time.Second):
		t.Fatal("immediate push was not enqueued")
	}

	sm.mu.RLock()
	pending := len(sm.pending[sessionKey{authKeyID: raw, sessionID: 42}])
	sm.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("pending pushes = %d, want 0", pending)
	}
}

func TestSessionManagerWithholdsUpdatesReadinessUntilExactProfile(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	key := sessionKey{authKeyID: [8]byte{0x22, 0x02, 0x27}, sessionID: 220227}
	c := &Conn{
		authKeyID:             key.authKeyID,
		sessionID:             key.sessionID,
		outbound:              make(chan *outboundOp, 1),
		outboundControl:       make(chan *outboundOp, 1),
		outboundStop:          make(chan struct{}),
		metrics:               NopMetrics{},
		outboundTrackedBudget: newOutboundTrackedBudget(1 << 20),
	}
	const userID = int64(1000000227)
	c.userID.Store(userID)
	c.userIDResolved.Store(true)
	c.membershipsSynced.Store(true) // model work performed before a defensive Set(true)
	if err := sm.Register(c); err != nil {
		t.Fatal(err)
	}

	update := &tg.UpdateShort{Update: &tg.UpdateUserStatus{UserID: userID, Status: &tg.UserStatusOnline{Expires: 1}}, Date: 1}
	if err := sm.PushToSessionForAuthKey(context.Background(), key.authKeyID, key.sessionID, proto.MessageFromServer, update); err != nil {
		t.Fatal(err)
	}
	sm.SetReceivesUpdatesForAuthKey(key.authKeyID, key.sessionID, true)
	if c.receivesUpdates.Load() || c.membershipsSynced.Load() || sm.ReceivesUpdatesForAuthKey(key.authKeyID, key.sessionID) {
		t.Fatal("unknown-profile connection became updates-ready")
	}
	if c.isRetired() {
		t.Fatal("unknown-profile readiness attempt retired a healthy connection")
	}
	sm.mu.RLock()
	pending, flushing := len(sm.pending[key]), sm.flushing[key]
	sm.mu.RUnlock()
	if pending != 1 || flushing {
		t.Fatalf("unknown-profile readiness changed pending state = pending:%d flushing:%v", pending, flushing)
	}
	select {
	case op := <-c.outbound:
		op.releaseReservation(c.outboundTrackedBudget)
		t.Fatal("unknown-profile readiness flushed a proactive update")
	default:
	}

	if err := c.FreezeLayerProfile(tlprofile.Profile225); err != nil {
		t.Fatal(err)
	}
	c.membershipsSynced.Store(true)
	sm.SetReceivesUpdatesForAuthKey(key.authKeyID, key.sessionID, true)
	var op *outboundOp
	select {
	case op = <-c.outbound:
	case <-time.After(time.Second):
		t.Fatal("profiled readiness did not flush pending update")
	}
	if op.encoded == nil || op.encoded.layer == nil || op.encoded.layer.profile != tlprofile.Profile225 {
		t.Fatalf("flushed update layer binding = %#v", op.encoded)
	}
	op.releaseReservation(c.outboundTrackedBudget)
	deadline := time.Now().Add(time.Second)
	for !c.receivesUpdates.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !c.receivesUpdates.Load() || !sm.ReceivesUpdatesForAuthKey(key.authKeyID, key.sessionID) {
		t.Fatal("profiled connection did not become updates-ready after ordered flush")
	}
	if c.isRetired() {
		t.Fatal("profiled pending flush retired connection")
	}
}

func TestSessionUpdatesActivationIsSingleFlightAndGenerationFenced(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	key := sessionKey{authKeyID: [8]byte{0x51}, sessionID: 5100}
	old := &Conn{authKeyID: key.authKeyID, sessionID: key.sessionID}
	if err := sm.Register(old); err != nil {
		t.Fatal(err)
	}
	oldToken, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID)
	if !ok || oldToken == 0 {
		t.Fatal("first physical generation did not acquire activation")
	}
	if token, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID); ok || token != 0 {
		t.Fatalf("concurrent activation acquired token %d", token)
	}
	sm.EndSessionUpdatesActivation(key.authKeyID, key.sessionID, oldToken+1)
	if token, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID); ok || token != 0 {
		t.Fatal("wrong-token release cleared active claim")
	}

	replacement := &Conn{authKeyID: key.authKeyID, sessionID: key.sessionID}
	if err := sm.Register(replacement); err != nil {
		t.Fatal(err)
	}
	newToken, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID)
	if !ok || newToken == 0 || newToken == oldToken {
		t.Fatalf("replacement activation token = %d, old %d", newToken, oldToken)
	}
	sm.EndSessionUpdatesActivation(key.authKeyID, key.sessionID, oldToken)
	if token, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID); ok || token != 0 {
		t.Fatal("old generation callback cleared replacement claim")
	}
	sm.EndSessionUpdatesActivation(key.authKeyID, key.sessionID, newToken)
	if token, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID); !ok || token == 0 {
		t.Fatal("matching replacement token did not release claim")
	}
}

func TestSessionUpdatesActivationLeaseRecoversAbandonedClaim(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	key := sessionKey{authKeyID: [8]byte{0x52}, sessionID: 5200}
	now := time.Unix(1700000000, 0)
	c := &Conn{authKeyID: key.authKeyID, sessionID: key.sessionID, now: func() time.Time { return now }}
	if err := sm.Register(c); err != nil {
		t.Fatal(err)
	}
	first, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID)
	if !ok {
		t.Fatal("first activation claim rejected")
	}
	now = now.Add(updatesActivationClaimTTL - time.Second)
	if token, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID); ok || token != 0 {
		t.Fatal("live activation lease was stolen")
	}
	now = now.Add(2 * time.Second)
	second, ok := sm.BeginSessionUpdatesActivation(key.authKeyID, key.sessionID)
	if !ok || second == 0 || second == first {
		t.Fatalf("expired activation lease was not replaced: first=%d second=%d", first, second)
	}
}

func TestSessionBootstrapProbeIsOneShotRetryableAndGenerationFenced(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	key := sessionKey{authKeyID: [8]byte{0x53}, sessionID: 5300}
	old := &Conn{authKeyID: key.authKeyID, sessionID: key.sessionID}
	if err := sm.Register(old); err != nil {
		t.Fatal(err)
	}
	failedToken, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID)
	if !ok || failedToken == 0 {
		t.Fatal("first bootstrap probe was not claimed")
	}
	if token, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID); ok || token != 0 {
		t.Fatalf("concurrent bootstrap probe acquired token %d", token)
	}
	sm.EndSessionBootstrapProbe(key.authKeyID, key.sessionID, failedToken, false)
	oldToken, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID)
	if !ok || oldToken == 0 || oldToken == failedToken {
		t.Fatalf("failed probe did not become retryable: failed=%d retry=%d", failedToken, oldToken)
	}

	replacement := &Conn{authKeyID: key.authKeyID, sessionID: key.sessionID}
	if err := sm.Register(replacement); err != nil {
		t.Fatal(err)
	}
	newToken, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID)
	if !ok || newToken == 0 || newToken == oldToken {
		t.Fatalf("replacement bootstrap token = %d, old %d", newToken, oldToken)
	}
	sm.EndSessionBootstrapProbe(key.authKeyID, key.sessionID, oldToken, true)
	if token, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID); ok || token != 0 {
		t.Fatal("old generation callback cleared replacement probe")
	}
	sm.EndSessionBootstrapProbe(key.authKeyID, key.sessionID, newToken, true)
	if token, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID); ok || token != 0 {
		t.Fatal("successful bootstrap probe was not one-shot")
	}

	sm.BindUserForAuthKey(key.authKeyID, key.sessionID, 100)
	sm.BindUserForAuthKey(key.authKeyID, key.sessionID, 200)
	if token, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID); !ok || token == 0 {
		t.Fatal("user identity change did not reset bootstrap probe")
	}
}

func TestSessionBootstrapProbeLeaseRecoversLostRemoteRelease(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	key := sessionKey{authKeyID: [8]byte{0x54}, sessionID: 5400}
	now := time.Unix(1700000000, 0)
	c := &Conn{authKeyID: key.authKeyID, sessionID: key.sessionID, now: func() time.Time { return now }}
	if err := sm.Register(c); err != nil {
		t.Fatal(err)
	}
	first, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID)
	if !ok || first == 0 {
		t.Fatal("first bootstrap probe claim rejected")
	}
	now = now.Add(bootstrapProbeClaimTTL - time.Second)
	if token, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID); ok || token != 0 {
		t.Fatal("live bootstrap probe lease was stolen")
	}
	now = now.Add(2 * time.Second)
	second, ok := sm.BeginSessionBootstrapProbe(key.authKeyID, key.sessionID)
	if !ok || second == 0 || second == first {
		t.Fatalf("expired bootstrap lease was not replaced: first=%d second=%d", first, second)
	}
}

func TestPendingPushBodiesUseGlobalByteBudgetAndReleaseOnDrop(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	msg := &tg.UpdateShort{Update: &tg.UpdateLoginToken{}, Date: 1700000000}
	encoded, err := encodeOutboundMessage(msg)
	if err != nil {
		t.Fatalf("encode pending fixture: %v", err)
	}
	sm.pendingBudget = newOutboundTrackedBudget(int64(len(encoded.body)))
	key := sessionKey{authKeyID: [8]byte{9}, sessionID: 77}

	sm.mu.Lock()
	first := sm.queueLocked(key, proto.MessageFromServer, msg)
	second := sm.queueLocked(key, proto.MessageFromServer, msg)
	sm.mu.Unlock()
	if !first || second {
		t.Fatalf("pending queue results = first %v second %v, want true/false at byte cap", first, second)
	}
	if got := sm.pendingBudget.snapshot(); got != int64(len(encoded.body)) {
		t.Fatalf("pending body budget = %d, want %d", got, len(encoded.body))
	}

	sm.mu.Lock()
	sm.deletePendingLocked(key)
	sm.mu.Unlock()
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("pending body budget after drop = %d, want zero", got)
	}
}

func TestPendingFlushGlobalBodyPressureDoesNotTerminateHealthyConnection(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	key := sessionKey{authKeyID: [8]byte{6}, sessionID: 66}
	c := &Conn{
		authKeyID:             key.authKeyID,
		sessionID:             key.sessionID,
		outbound:              make(chan *outboundOp, 1),
		outboundControl:       make(chan *outboundOp, 1),
		outboundStop:          make(chan struct{}),
		metrics:               NopMetrics{},
		outboundTrackedBudget: newOutboundTrackedBudget(1),
	}
	const userID = int64(606)
	c.userID.Store(userID)
	c.userIDResolved.Store(true)
	if err := c.FreezeLayerProfile(tlprofile.ProfileCanonical); err != nil {
		t.Fatal(err)
	}
	sm.Register(c)

	msg := &tg.UpdateShort{Update: &tg.UpdateLoginToken{}, Date: 1700000000}
	sm.mu.Lock()
	if !sm.queueLocked(key, proto.MessageFromServer, msg) {
		sm.mu.Unlock()
		t.Fatal("queue pending push")
	}
	sm.flushing[key] = true
	sm.mu.Unlock()

	// Enter at the final retry so the test exercises the durable-difference fallback without
	// waiting for the production backoff timer.
	sm.runFlush(c, key, userID, maxFlushAttempts-1)
	if c.isRetired() {
		t.Fatal("shared body pressure terminated a healthy pending-flush connection")
	}
	if !c.receivesUpdates.Load() {
		t.Fatal("pending flush did not activate difference fallback after bounded retries")
	}
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("pending budget after fallback = %d, want zero", got)
	}
}

func TestPendingPushBudgetSurvivesTakeAndReturnsAcrossOverflowAndUnregister(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	msg := &tg.UpdateShort{Update: &tg.UpdateLoginToken{}, Date: 1700000000}
	encoded, err := encodeOutboundMessage(msg)
	if err != nil {
		t.Fatalf("encode pending fixture: %v", err)
	}
	bytesPerPush := int64(len(encoded.body))
	sm.pendingBudget = newOutboundTrackedBudget(bytesPerPush * (maxPendingPushesPerSession + 8))
	key := sessionKey{authKeyID: [8]byte{7}, sessionID: 55}
	c := &Conn{authKeyID: key.authKeyID, sessionID: key.sessionID}
	sm.Register(c)

	sm.mu.Lock()
	for i := 0; i < maxPendingPushesPerSession+5; i++ {
		if !sm.queueLocked(key, proto.MessageFromServer, msg) {
			sm.mu.Unlock()
			t.Fatalf("queue pending push %d unexpectedly failed", i)
		}
	}
	if got, want := sm.pendingBudget.snapshot(), bytesPerPush*maxPendingPushesPerSession; got != want {
		sm.mu.Unlock()
		t.Fatalf("budget after overflow replacement = %d, want %d", got, want)
	}
	batch := sm.takePendingLocked(key, true)
	sm.mu.Unlock()
	if len(batch) != maxPendingPushesPerSession {
		t.Fatalf("taken pending pushes = %d, want %d", len(batch), maxPendingPushesPerSession)
	}
	// take transfers ownership to runFlush; deleting the map entry must not release bodies while
	// the batch still references them.
	if got, want := sm.pendingBudget.snapshot(), bytesPerPush*maxPendingPushesPerSession; got != want {
		t.Fatalf("budget after take = %d, want transferred ownership %d", got, want)
	}
	releaseQueuedPushes(batch)
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("budget after taken batch release = %d, want 0", got)
	}

	sm.mu.Lock()
	if !sm.queueLocked(key, proto.MessageFromServer, msg) {
		sm.mu.Unlock()
		t.Fatal("queue before unregister failed")
	}
	sm.mu.Unlock()
	sm.Unregister(c)
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("budget after unregister = %d, want 0", got)
	}
}

// TestSessionManagerPush verifies proactive push end to end: after two client
// connections handshake and establish a session, the server proactively pushes to
// them via PushToSession / PushToUser and the clients receive it.
func TestSessionManagerPush(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})

	conn1, auth1, cipher1 := dialHandshake(t, addr, dc, pub)
	conn2, auth2, cipher2 := dialHandshake(t, addr, dc, pub)

	// Each sends a ping to establish a session, triggering registration (and clearing new_session_created/pong/ack).
	msgGen := proto.NewMessageIDGen(time.Now)
	sendEncryptedWithSeq(t, conn1, cipher1, auth1, msgGen.New(proto.MessageFromClient), 1, &mt.PingRequest{PingID: 1})
	collectReplies(t, conn1, cipher1, auth1.AuthKey, mt.PongTypeID)
	sendEncryptedWithSeq(t, conn2, cipher2, auth2, msgGen.New(proto.MessageFromClient), 1, &mt.PingRequest{PingID: 2})
	collectReplies(t, conn2, cipher2, auth2.AuthKey, mt.PongTypeID)

	if got := srv.Conns().Online(); got != 2 {
		t.Fatalf("online = %d, want 2", got)
	}
	if !srv.Conns().SetLayerProfile(auth1.SessionID, tlprofile.Profile227) ||
		!srv.Conns().SetLayerProfile(auth2.SessionID, tlprofile.Profile227) {
		t.Fatal("seed exact test profiles")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1) PushToSession: stag it while session2 has not yet entered the updates sync entry, deliver once ready.
	if err := srv.Conns().PushToSession(ctx, auth2.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
		t.Fatalf("push to session: %v", err)
	}
	srv.Conns().SetReceivesUpdates(auth2.SessionID, true)
	r2 := collectReplies(t, conn2, cipher2, auth2.AuthKey, tg.UpdatesTooLongTypeID)
	mustHave(t, r2, tg.UpdatesTooLongTypeID, "pushed updates on conn2")

	// 2) BindUser + PushToUser: push to conn1 by user dimension.
	srv.Conns().BindUser(auth1.SessionID, 100)
	srv.Conns().SetReceivesUpdates(auth1.SessionID, true)
	sent, err := srv.Conns().PushToUser(ctx, 100, proto.MessageFromServer, &tg.UpdatesTooLong{})
	if err != nil {
		t.Fatalf("push to user: %v", err)
	}
	if sent != 1 {
		t.Fatalf("pushed to %d conns, want 1", sent)
	}
	r1 := collectReplies(t, conn1, cipher1, auth1.AuthKey, tg.UpdatesTooLongTypeID)
	mustHave(t, r1, tg.UpdatesTooLongTypeID, "pushed updates on conn1")

	// 3) PushToUserExceptSession: simulate SyncUpdatesNotMe, skipping the current session.
	srv.Conns().BindUser(auth1.SessionID, 200)
	srv.Conns().BindUser(auth2.SessionID, 200)
	sent, err = srv.Conns().PushToUserExceptSession(ctx, 200, auth2.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{})
	if err != nil {
		t.Fatalf("push to user except session: %v", err)
	}
	if sent != 1 {
		t.Fatalf("pushed to %d conns, want 1 after excluding current session", sent)
	}
	r1 = collectReplies(t, conn1, cipher1, auth1.AuthKey, tg.UpdatesTooLongTypeID)
	mustHave(t, r1, tg.UpdatesTooLongTypeID, "pushed not-me updates on conn1")
}

func BenchmarkSessionManagerOnlineCandidateFilter(b *testing.B) {
	sm := NewSessionManager(zaptest.NewLogger(b))
	const online = 200_000
	rawPrefix := [8]byte{9}
	for i := 1; i <= online; i++ {
		raw := rawPrefix
		raw[1] = byte(i)
		raw[2] = byte(i >> 8)
		raw[3] = byte(i >> 16)
		raw[4] = byte(i >> 24)
		c := &Conn{sessionID: int64(i), authKeyID: raw}
		sm.Register(c)
		sm.BindUserForAuthKey(raw, int64(i), int64(i))
	}
	candidates := make([]int64, 0, 500)
	for i := 0; i < 500; i++ {
		candidates = append(candidates, int64(i*97+1))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := sm.OnlineUserIDsForCandidates(candidates, 500)
		if len(got) == 0 {
			b.Fatal("no candidates matched")
		}
	}
}
