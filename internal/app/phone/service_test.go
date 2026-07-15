package phone

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"

	"telesrv/internal/domain"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *testClock) Timer(d time.Duration) clock.Timer   { return clock.System.Timer(d) }
func (c *testClock) Ticker(d time.Duration) clock.Ticker { return clock.System.Ticker(d) }

func testProtocol(versions ...string) domain.PhoneCallProtocol {
	if len(versions) == 0 {
		versions = []string{"11.0.0", "10.0.0"}
	}
	return domain.PhoneCallProtocol{
		UDPP2P:          true,
		UDPReflector:    true,
		MinLayer:        65,
		MaxLayer:        92,
		LibraryVersions: versions,
	}
}

func testGA() ([]byte, []byte) {
	ga := make([]byte, 256)
	for i := range ga {
		ga[i] = byte(i + 1)
	}
	hash := sha256.Sum256(ga)
	return ga, hash[:]
}

func testGB() []byte {
	gb := make([]byte, 256)
	for i := range gb {
		gb[i] = byte(255 - i%200)
	}
	return gb
}

func newTestService(clk clock.Clock, mutate ...func(*Config)) *Service {
	cfg := Config{
		RingTimeout:            90 * time.Second,
		TombstoneTTL:           60 * time.Second,
		MaxActivePerUser:       4,
		SignalingRatePerSecond: 50,
	}
	for _, fn := range mutate {
		fn(&cfg)
	}
	return NewService(cfg, WithClock(clk))
}

func mustRequest(t *testing.T, s *Service, caller, callee int64, gaHash []byte) domain.PhoneCall {
	t.Helper()
	call, err := s.RequestCall(context.Background(), caller, domain.PhoneCallRequest{
		CalleeID:   callee,
		RandomID:   caller*1000 + callee,
		GAHash:     gaHash,
		Protocol:   testProtocol(),
		PrivacyP2P: true, // rpc 层算定的 phone_p2p 双向放行（P3 起参与 p2p_allowed AND）
	})
	if err != nil {
		t.Fatalf("RequestCall: %v", err)
	}
	return call
}

func TestPhoneCallHappyPath(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk)
	ctx := context.Background()
	ga, gaHash := testGA()
	gb := testGB()

	call := mustRequest(t, s, 1, 2, gaHash)
	if call.State != domain.PhoneCallStateRequested || call.AdminID != 1 || call.ParticipantID != 2 {
		t.Fatalf("requested call = %+v", call)
	}

	clk.Advance(2 * time.Second)
	ringing, transitioned, err := s.ReceivedCall(ctx, 2, call.ID, call.AccessHash)
	if err != nil || !transitioned || ringing.State != domain.PhoneCallStateRinging || ringing.ReceiveDate == 0 {
		t.Fatalf("ReceivedCall = %+v transitioned=%v err=%v", ringing, transitioned, err)
	}
	if _, again, err := s.ReceivedCall(ctx, 2, call.ID, call.AccessHash); err != nil || again {
		t.Fatalf("second ReceivedCall transitioned=%v err=%v, want idempotent", again, err)
	}

	accepted, err := s.AcceptCall(ctx, 2, call.ID, call.AccessHash, gb, testProtocol(), domain.SessionRef{SessionID: 22})
	if err != nil || accepted.State != domain.PhoneCallStateAccepted {
		t.Fatalf("AcceptCall = %+v err=%v", accepted, err)
	}
	if string(accepted.GB) != string(gb) {
		t.Fatalf("accepted.GB mismatch")
	}

	confirmed, forced, err := s.ConfirmCall(ctx, 1, call.ID, call.AccessHash, ga, 0x1234, testProtocol())
	if err != nil || forced || confirmed.State != domain.PhoneCallStateConfirmed {
		t.Fatalf("ConfirmCall = %+v forced=%v err=%v", confirmed, forced, err)
	}
	if !confirmed.P2PAllowed || confirmed.KeyFingerprint != 0x1234 || confirmed.StartDate == 0 {
		t.Fatalf("confirmed snapshot = %+v", confirmed)
	}

	clk.Advance(30 * time.Second)
	discarded, already, err := s.DiscardCall(ctx, 2, call.ID, call.AccessHash, domain.PhoneCallDiscardReasonHangup, 30)
	if err != nil || already || discarded.State != domain.PhoneCallStateDiscarded || discarded.Duration != 30 {
		t.Fatalf("DiscardCall = %+v already=%v err=%v", discarded, already, err)
	}
	// 双方同时挂断：后到者幂等拿快照，reason 由先到者决定。
	again, already, err := s.DiscardCall(ctx, 1, call.ID, call.AccessHash, domain.PhoneCallDiscardReasonBusy, 0)
	if err != nil || !already || again.DiscardReason != domain.PhoneCallDiscardReasonHangup {
		t.Fatalf("second DiscardCall = %+v already=%v err=%v", again, already, err)
	}
}

func TestPhoneCallStateErrors(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk)
	ctx := context.Background()
	ga, gaHash := testGA()
	gb := testGB()
	call := mustRequest(t, s, 1, 2, gaHash)

	// confirm 前置必须 Accepted。
	if _, _, err := s.ConfirmCall(ctx, 1, call.ID, call.AccessHash, ga, 1, testProtocol()); !errors.Is(err, ErrPeerInvalid) {
		t.Fatalf("confirm before accept err = %v, want ErrPeerInvalid", err)
	}
	// 非被叫不能 accept / receivedCall。
	if _, err := s.AcceptCall(ctx, 1, call.ID, call.AccessHash, gb, testProtocol(), domain.SessionRef{}); !errors.Is(err, ErrPeerInvalid) {
		t.Fatalf("accept by caller err = %v, want ErrPeerInvalid", err)
	}
	if _, _, err := s.ReceivedCall(ctx, 1, call.ID, call.AccessHash); !errors.Is(err, ErrPeerInvalid) {
		t.Fatalf("receivedCall by caller err = %v, want ErrPeerInvalid", err)
	}
	// access_hash 不符。
	if _, err := s.AcceptCall(ctx, 2, call.ID, call.AccessHash+1, gb, testProtocol(), domain.SessionRef{}); !errors.Is(err, ErrPeerInvalid) {
		t.Fatalf("wrong access hash err = %v, want ErrPeerInvalid", err)
	}

	if _, err := s.AcceptCall(ctx, 2, call.ID, call.AccessHash, gb, testProtocol(), domain.SessionRef{}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := s.AcceptCall(ctx, 2, call.ID, call.AccessHash, gb, testProtocol(), domain.SessionRef{}); !errors.Is(err, ErrAlreadyAccepted) {
		t.Fatalf("double accept err = %v, want ErrAlreadyAccepted", err)
	}
	if _, _, err := s.DiscardCall(ctx, 1, call.ID, call.AccessHash, domain.PhoneCallDiscardReasonHangup, 0); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := s.AcceptCall(ctx, 2, call.ID, call.AccessHash, gb, testProtocol(), domain.SessionRef{}); !errors.Is(err, ErrAlreadyDeclined) {
		t.Fatalf("accept after discard err = %v, want ErrAlreadyDeclined", err)
	}
}

func TestPhoneCallGAHashMismatchForcesDiscard(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk)
	ctx := context.Background()
	_, gaHash := testGA()
	call := mustRequest(t, s, 1, 2, gaHash)
	if _, err := s.AcceptCall(ctx, 2, call.ID, call.AccessHash, testGB(), testProtocol(), domain.SessionRef{}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	wrong := make([]byte, 256)
	wrong[0] = 0x7
	snap, forced, err := s.ConfirmCall(ctx, 1, call.ID, call.AccessHash, wrong, 1, testProtocol())
	if !errors.Is(err, ErrGAHashMismatch) || !forced {
		t.Fatalf("confirm with wrong ga: forced=%v err=%v", forced, err)
	}
	if snap.State != domain.PhoneCallStateDiscarded || snap.DiscardReason != domain.PhoneCallDiscardReasonDisconnect {
		t.Fatalf("forced discard snapshot = %+v", snap)
	}
}

func TestPhoneCallConcurrentAcceptSingleWinner(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk)
	ctx := context.Background()
	_, gaHash := testGA()
	call := mustRequest(t, s, 1, 2, gaHash)

	const devices = 8
	var wg sync.WaitGroup
	wins := make(chan int64, devices)
	losses := make(chan error, devices)
	for i := 0; i < devices; i++ {
		wg.Add(1)
		go func(sessionID int64) {
			defer wg.Done()
			_, err := s.AcceptCall(ctx, 2, call.ID, call.AccessHash, testGB(), testProtocol(), domain.SessionRef{SessionID: sessionID})
			if err == nil {
				wins <- sessionID
			} else {
				losses <- err
			}
		}(int64(100 + i))
	}
	wg.Wait()
	close(wins)
	close(losses)
	if len(wins) != 1 {
		t.Fatalf("winners = %d, want exactly 1", len(wins))
	}
	for err := range losses {
		if !errors.Is(err, ErrAlreadyAccepted) {
			t.Fatalf("loser err = %v, want ErrAlreadyAccepted", err)
		}
	}
	winner := <-wins
	snap, ok := s.Lookup(ctx, call.ID, call.AccessHash)
	if !ok || snap.CalleeDevice.SessionID != winner {
		t.Fatalf("callee device = %+v, want session %d", snap.CalleeDevice, winner)
	}
}

func TestPhoneCallRandomIDIdempotent(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk)
	ctx := context.Background()
	_, gaHash := testGA()
	req := domain.PhoneCallRequest{CalleeID: 2, RandomID: 777, GAHash: gaHash, Protocol: testProtocol()}
	first, err := s.RequestCall(ctx, 1, req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	second, err := s.RequestCall(ctx, 1, req)
	if err != nil || second.ID != first.ID {
		t.Fatalf("retry id = %d err=%v, want %d", second.ID, err, first.ID)
	}
	// 终结后同 random_id 重新可用（新通话）。
	if _, _, err := s.DiscardCall(ctx, 1, first.ID, first.AccessHash, domain.PhoneCallDiscardReasonHangup, 0); err != nil {
		t.Fatalf("discard: %v", err)
	}
	third, err := s.RequestCall(ctx, 1, req)
	if err != nil || third.ID == first.ID {
		t.Fatalf("post-discard request id = %d err=%v, want fresh call", third.ID, err)
	}
}

func TestPhoneCallQuotaAndSweep(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk, func(c *Config) { c.MaxActivePerUser = 2 })
	ctx := context.Background()
	_, gaHash := testGA()

	for i := int64(0); i < 2; i++ {
		if _, err := s.RequestCall(ctx, 1, domain.PhoneCallRequest{CalleeID: 10 + i, RandomID: i, GAHash: gaHash, Protocol: testProtocol()}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := s.RequestCall(ctx, 1, domain.PhoneCallRequest{CalleeID: 99, RandomID: 99, GAHash: gaHash, Protocol: testProtocol()}); !errors.Is(err, ErrOccupyFailed) {
		t.Fatalf("over quota err = %v, want ErrOccupyFailed", err)
	}
	// 双端崩溃兜底：超过 2×RingTimeout 的僵尸通话被纯年龄 GC 回收，配额释放。
	clk.Advance(181 * time.Second)
	if _, err := s.RequestCall(ctx, 1, domain.PhoneCallRequest{CalleeID: 99, RandomID: 99, GAHash: gaHash, Protocol: testProtocol()}); err != nil {
		t.Fatalf("request after sweep: %v", err)
	}
}

func TestPhoneCallTombstoneGC(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk)
	ctx := context.Background()
	_, gaHash := testGA()
	call := mustRequest(t, s, 1, 2, gaHash)
	if _, _, err := s.DiscardCall(ctx, 1, call.ID, call.AccessHash, domain.PhoneCallDiscardReasonHangup, 0); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, ok := s.Lookup(ctx, call.ID, call.AccessHash); !ok {
		t.Fatalf("tombstone should be visible before TTL")
	}
	clk.Advance(61 * time.Second)
	mustRequest(t, s, 3, 4, gaHash) // 触发 sweep
	if _, ok := s.Lookup(ctx, call.ID, call.AccessHash); ok {
		t.Fatalf("tombstone should be collected after TTL")
	}
}

func TestPhoneCallDurationOnlyWhenConfirmed(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk)
	ctx := context.Background()
	_, gaHash := testGA()
	call := mustRequest(t, s, 1, 2, gaHash)
	snap, _, err := s.DiscardCall(ctx, 2, call.ID, call.AccessHash, domain.PhoneCallDiscardReasonBusy, 55)
	if err != nil || snap.Duration != 0 {
		t.Fatalf("unconfirmed discard duration = %d err=%v, want 0", snap.Duration, err)
	}
}

func TestPhoneCallSignal(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk, func(c *Config) { c.SignalingRatePerSecond = 2 })
	ctx := context.Background()
	_, gaHash := testGA()
	call := mustRequest(t, s, 1, 2, gaHash)

	// Accepted 前不可转发。
	if _, err := s.Signal(ctx, 1, call.ID, call.AccessHash, func(int64, domain.SessionRef) {}); !errors.Is(err, ErrPeerInvalid) {
		t.Fatalf("signal before accept err = %v, want ErrPeerInvalid", err)
	}
	if _, err := s.AcceptCall(ctx, 2, call.ID, call.AccessHash, testGB(), testProtocol(), domain.SessionRef{}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	var forwarded []int64
	forward := func(peer int64, _ domain.SessionRef) { forwarded = append(forwarded, peer) }
	for i := 0; i < 3; i++ {
		if _, err := s.Signal(ctx, 1, call.ID, call.AccessHash, forward); err != nil {
			t.Fatalf("signal %d: %v", i, err)
		}
	}
	// 限速 2/s：第三条被静默丢弃。
	if len(forwarded) != 2 || forwarded[0] != 2 || forwarded[1] != 2 {
		t.Fatalf("forwarded = %v, want [2 2]", forwarded)
	}
	clk.Advance(time.Second)
	if drop, err := s.Signal(ctx, 2, call.ID, call.AccessHash, forward); err != nil || drop {
		t.Fatalf("signal new window drop=%v err=%v", drop, err)
	}
	if forwarded[len(forwarded)-1] != 1 {
		t.Fatalf("callee→caller forward peer = %d, want 1", forwarded[len(forwarded)-1])
	}
	// 终态尾包静默吞掉。
	if _, _, err := s.DiscardCall(ctx, 1, call.ID, call.AccessHash, domain.PhoneCallDiscardReasonHangup, 0); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if drop, err := s.Signal(ctx, 1, call.ID, call.AccessHash, forward); err != nil || !drop {
		t.Fatalf("signal after discard drop=%v err=%v, want drop", drop, err)
	}
}

func TestNegotiateProtocol(t *testing.T) {
	base := func(min, max int, versions ...string) domain.PhoneCallProtocol {
		return domain.PhoneCallProtocol{UDPP2P: true, UDPReflector: true, MinLayer: min, MaxLayer: max, LibraryVersions: versions}
	}
	t.Run("layer intersection", func(t *testing.T) {
		out, err := negotiateProtocol(base(65, 92, "9.0.0"), base(70, 110, "9.0.0"))
		if err != nil || out.MinLayer != 70 || out.MaxLayer != 92 {
			t.Fatalf("negotiated = %+v err=%v", out, err)
		}
	})
	t.Run("layer disjoint", func(t *testing.T) {
		if _, err := negotiateProtocol(base(65, 70, "9.0.0"), base(80, 92, "9.0.0")); !errors.Is(err, ErrProtocolCompatLayerInvalid) {
			t.Fatalf("err = %v, want ErrProtocolCompatLayerInvalid", err)
		}
	})
	t.Run("best common version is semver max", func(t *testing.T) {
		out, err := negotiateProtocol(base(65, 92, "11.0.0", "9.0.0", "2.4.4"), base(65, 92, "2.4.4", "9.0.0"))
		if err != nil || len(out.LibraryVersions) != 1 || out.LibraryVersions[0] != "9.0.0" {
			t.Fatalf("versions = %v err=%v, want [9.0.0]", out.LibraryVersions, err)
		}
	})
	t.Run("preferred version beats semver max", func(t *testing.T) {
		// ⚠ "9.0.0" 优先于更高版本：DrKLO 视频 gate 是字符串字典序比较
		//（"1x.0.0" < "2.7.7" 会判不支持视频），且 12/13 走 V3 SCTP 信令。
		out, err := negotiateProtocol(base(65, 92, "13.0.0", "10.0.0", "9.0.0"), base(65, 92, "9.0.0", "10.0.0", "13.0.0"))
		if err != nil || out.LibraryVersions[0] != "9.0.0" {
			t.Fatalf("versions = %v err=%v, want preferred [9.0.0]", out.LibraryVersions, err)
		}
	})
	t.Run("numeric compare fallback not lexicographic", func(t *testing.T) {
		// 交集无 preferred 版本时退化为语义化最高（数值比较，非字典序）。
		out, err := negotiateProtocol(base(65, 92, "10.0.0", "11.0.0"), base(65, 92, "11.0.0", "10.0.0"))
		if err != nil || out.LibraryVersions[0] != "11.0.0" {
			t.Fatalf("versions = %v err=%v, want [11.0.0]", out.LibraryVersions, err)
		}
	})
	t.Run("no common versions passes callee list through", func(t *testing.T) {
		// ⚠ P1-3：版本无交集绝不拒绝通话，透传被叫列表。
		out, err := negotiateProtocol(base(65, 92, "11.0.0"), base(65, 92, "2.4.4", "3.0.0"))
		if err != nil || len(out.LibraryVersions) != 2 || out.LibraryVersions[0] != "2.4.4" {
			t.Fatalf("versions = %v err=%v, want callee passthrough", out.LibraryVersions, err)
		}
	})
}

func TestValidateProtocol(t *testing.T) {
	cases := []struct {
		name string
		p    domain.PhoneCallProtocol
		want error
	}{
		{"min over max", domain.PhoneCallProtocol{UDPP2P: true, MinLayer: 93, MaxLayer: 92, LibraryVersions: []string{"9.0.0"}}, ErrProtocolLayerInvalid},
		{"max below 65", domain.PhoneCallProtocol{UDPP2P: true, MinLayer: 60, MaxLayer: 64, LibraryVersions: []string{"9.0.0"}}, ErrProtocolCompatLayerInvalid},
		{"no transport flags", domain.PhoneCallProtocol{MinLayer: 65, MaxLayer: 92, LibraryVersions: []string{"9.0.0"}}, ErrProtocolFlagsInvalid},
		{"no versions", domain.PhoneCallProtocol{UDPP2P: true, MinLayer: 65, MaxLayer: 92}, ErrProtocolFlagsInvalid},
		{"ok", testProtocol(), nil},
	}
	for _, tc := range cases {
		if err := validateProtocol(tc.p); !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestPhoneCallExpireDue(t *testing.T) {
	clk := newTestClock()
	s := newTestService(clk)
	ctx := context.Background()
	ga, gaHash := testGA()

	// 三通通话：振铃中（→missed）、Accepted 悬挂（→disconnect）、Confirmed（不回收）。
	ringingCall := mustRequest(t, s, 1, 2, gaHash)
	acceptedCall, err := s.RequestCall(ctx, 3, domain.PhoneCallRequest{CalleeID: 4, RandomID: 1, GAHash: gaHash, Protocol: testProtocol()})
	if err != nil {
		t.Fatalf("request accepted call: %v", err)
	}
	if _, err := s.AcceptCall(ctx, 4, acceptedCall.ID, acceptedCall.AccessHash, testGB(), testProtocol(), domain.SessionRef{}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	confirmedCall, err := s.RequestCall(ctx, 5, domain.PhoneCallRequest{CalleeID: 6, RandomID: 2, GAHash: gaHash, Protocol: testProtocol()})
	if err != nil {
		t.Fatalf("request confirmed call: %v", err)
	}
	if _, err := s.AcceptCall(ctx, 6, confirmedCall.ID, confirmedCall.AccessHash, testGB(), testProtocol(), domain.SessionRef{}); err != nil {
		t.Fatalf("accept confirmed: %v", err)
	}
	if _, _, err := s.ConfirmCall(ctx, 5, confirmedCall.ID, confirmedCall.AccessHash, ga, 1, testProtocol()); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if got := s.ExpireDue(ctx, clk.Now()); len(got) != 0 {
		t.Fatalf("nothing should expire yet, got %d", len(got))
	}
	clk.Advance(91 * time.Second)
	expired := s.ExpireDue(ctx, clk.Now())
	if len(expired) != 2 {
		t.Fatalf("expired = %d, want 2 (ringing+accepted)", len(expired))
	}
	reasons := map[int64]domain.PhoneCallDiscardReason{}
	for _, c := range expired {
		reasons[c.ID] = c.DiscardReason
	}
	if reasons[ringingCall.ID] != domain.PhoneCallDiscardReasonMissed {
		t.Fatalf("ringing call reason = %s, want missed", reasons[ringingCall.ID])
	}
	if reasons[acceptedCall.ID] != domain.PhoneCallDiscardReasonDisconnect {
		t.Fatalf("accepted call reason = %s, want disconnect", reasons[acceptedCall.ID])
	}
	// Confirmed 通话不受服务端时长限制。
	if snap, ok := s.Lookup(ctx, confirmedCall.ID, confirmedCall.AccessHash); !ok || snap.State != domain.PhoneCallStateConfirmed {
		t.Fatalf("confirmed call = %+v ok=%v, want untouched", snap, ok)
	}
	// 幂等：再跑一轮无新增。
	if got := s.ExpireDue(ctx, clk.Now()); len(got) != 0 {
		t.Fatalf("second ExpireDue = %d, want 0", len(got))
	}
}
