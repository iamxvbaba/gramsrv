package rpc

import (
	"encoding/binary"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

type exactProfileTestClock struct {
	now time.Time
}

func BenchmarkExactSessionProfileRefreshParallel(b *testing.B) {
	clk := &exactProfileTestClock{now: time.Unix(1_700_000_000, 0)}
	r := New(Config{DC: 2}, Deps{}, zap.NewNop(), clk)
	const sessions = 4096
	for i := 1; i <= sessions; i++ {
		authKeyID := [8]byte{byte(i), byte(i >> 8), 1}
		if err := r.FreezeNegotiatedSessionLayer(authKeyID, int64(i), 227); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	var next atomic.Uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := int(next.Add(1)%sessions) + 1
			authKeyID := [8]byte{byte(i), byte(i >> 8), 1}
			if err := r.FreezeNegotiatedSessionLayer(authKeyID, int64(i), 227); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func (c *exactProfileTestClock) Now() time.Time                      { return c.now }
func (c *exactProfileTestClock) Timer(d time.Duration) clock.Timer   { return clock.System.Timer(d) }
func (c *exactProfileTestClock) Ticker(d time.Duration) clock.Ticker { return clock.System.Ticker(d) }
func (c *exactProfileTestClock) advance(d time.Duration)             { c.now = c.now.Add(d) }

func TestExactSessionProfileSurvivesTransportOfflineUntilTTL(t *testing.T) {
	clk := &exactProfileTestClock{now: time.Unix(1_700_000_000, 0)}
	r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clk)
	authKeyID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	const sessionID = int64(44)

	if err := r.FreezeNegotiatedSessionLayer(authKeyID, sessionID, 225); err != nil {
		t.Fatal(err)
	}
	// SessionOffline is a physical transport event. It may clear transient
	// client metadata, but the same logical MTProto session can reconnect and
	// immediately replay a naked exact request.
	r.SessionOffline(authKeyID, sessionID, 0, true)
	if layer, ok := r.NegotiatedSessionLayer(authKeyID, sessionID); !ok || layer != 225 {
		t.Fatalf("profile after transport offline = (%d,%v), want (225,true)", layer, ok)
	}

	clk.advance(exactSessionProfileTTL + time.Nanosecond)
	if layer, ok := r.NegotiatedSessionLayer(authKeyID, sessionID); ok || layer != 0 {
		t.Fatalf("expired profile = (%d,%v), want (0,false)", layer, ok)
	}
	if got := len(r.exactProfiles); got != 0 {
		t.Fatalf("expired registry entries = %d, want 0", got)
	}
}

func TestExactSessionProfileSupportsOrderedCorrectionAndRefreshesTTL(t *testing.T) {
	clk := &exactProfileTestClock{now: time.Unix(1_700_000_000, 0)}
	r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clk)
	authKeyID := [8]byte{9, 8, 7, 6, 5, 4, 3, 2}
	const sessionID = int64(45)

	if err := r.FreezeNegotiatedSessionLayer(authKeyID, sessionID, 225); err != nil {
		t.Fatal(err)
	}
	clk.advance(exactSessionProfileTTL - time.Second)
	if err := r.FreezeNegotiatedSessionLayer(authKeyID, sessionID, 225); err != nil {
		t.Fatalf("idempotent refresh: %v", err)
	}
	if err := r.FreezeNegotiatedSessionLayer(authKeyID, sessionID, 227); err == nil {
		// accepted below
	} else {
		t.Fatalf("ordered profile correction: %v", err)
	}
	clk.advance(2 * time.Second)
	if layer, ok := r.NegotiatedSessionLayer(authKeyID, sessionID); !ok || layer != 227 {
		t.Fatalf("corrected profile = (%d,%v), want (227,true)", layer, ok)
	}
}

func TestExactSessionProfileMessageIDEvidenceLinearizesCorrections(t *testing.T) {
	r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clock.System)
	authKeyID := [8]byte{4, 5, 6, 7}
	const sessionID = int64(46)

	if applied, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, 225, 100); err != nil || !applied {
		t.Fatalf("first evidence = (%v,%v), want (true,nil)", applied, err)
	}
	if applied, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, 227, 90); err != nil || applied {
		t.Fatalf("older evidence = (%v,%v), want (false,nil)", applied, err)
	}
	if applied, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, 227, 100); err == nil || applied {
		t.Fatalf("same msg_id conflicting evidence = (%v,%v), want conflict", applied, err)
	}
	if layer, msgID, ok := r.NegotiatedSessionLayerEvidence(authKeyID, sessionID); !ok || layer != 225 || msgID != 100 {
		t.Fatalf("evidence after stale/conflict = (%d,%d,%v), want (225,100,true)", layer, msgID, ok)
	}
	if applied, err := r.FreezeNegotiatedSessionLayerAt(authKeyID, sessionID, 227, 110); err != nil || !applied {
		t.Fatalf("newer correction = (%v,%v), want (true,nil)", applied, err)
	}
	// A same-layer legacy refresh must not erase the positive ordering watermark.
	if err := r.FreezeNegotiatedSessionLayer(authKeyID, sessionID, 227); err != nil {
		t.Fatal(err)
	}
	if layer, msgID, ok := r.NegotiatedSessionLayerEvidence(authKeyID, sessionID); !ok || layer != 227 || msgID != 110 {
		t.Fatalf("same-layer legacy refresh = (%d,%d,%v), want (227,110,true)", layer, msgID, ok)
	}
	// A different legacy value remains the explicit force API and resets order.
	if err := r.FreezeNegotiatedSessionLayer(authKeyID, sessionID, 225); err != nil {
		t.Fatal(err)
	}
	if layer, msgID, ok := r.NegotiatedSessionLayerEvidence(authKeyID, sessionID); !ok || layer != 225 || msgID != 0 {
		t.Fatalf("legacy force = (%d,%d,%v), want (225,0,true)", layer, msgID, ok)
	}
}

func TestExactSessionProfileExplicitInvalidation(t *testing.T) {
	clk := &exactProfileTestClock{now: time.Unix(1_700_000_000, 0)}
	r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clk)
	authKeyID := [8]byte{7, 7, 7, 7, 7, 7, 7, 7}
	otherAuthKeyID := [8]byte{8, 8, 8, 8, 8, 8, 8, 8}

	for _, key := range []struct {
		authKeyID [8]byte
		sessionID int64
	}{
		{authKeyID, 1},
		{authKeyID, 2},
		{otherAuthKeyID, 3},
	} {
		if err := r.FreezeNegotiatedSessionLayer(key.authKeyID, key.sessionID, 225); err != nil {
			t.Fatal(err)
		}
	}

	r.SessionDestroyed(authKeyID, 1)
	if _, ok := r.NegotiatedSessionLayer(authKeyID, 1); ok {
		t.Fatal("destroy_session retained exact profile")
	}
	if _, ok := r.NegotiatedSessionLayer(authKeyID, 2); !ok {
		t.Fatal("destroy_session removed a sibling session")
	}

	r.ForgetNegotiatedAuthKey(authKeyID)
	if _, ok := r.NegotiatedSessionLayer(authKeyID, 2); ok {
		t.Fatal("auth-key revocation retained exact profile")
	}
	if layer, ok := r.NegotiatedSessionLayer(otherAuthKeyID, 3); !ok || layer != 225 {
		t.Fatalf("unrelated auth profile = (%d,%v), want (225,true)", layer, ok)
	}
}

func TestExactSessionProfileCapacityNeverEvictsLiveWatermark(t *testing.T) {
	clk := &exactProfileTestClock{now: time.Unix(1_700_000_000, 0)}
	r := New(Config{DC: 2}, Deps{}, zaptest.NewLogger(t), clk)
	expiresAt := clk.now.Add(exactSessionProfileTTL)
	for i := uint64(1); i <= maxExactSessionProfileEntries; i++ {
		var authKeyID [8]byte
		binary.LittleEndian.PutUint64(authKeyID[:], i)
		r.exactProfiles[clientInfoSessionKey{rawAuthKeyID: authKeyID, sessionID: int64(i)}] = exactSessionProfileEntry{
			layer: 225, msgID: int64(i), expiresAt: expiresAt,
		}
	}
	r.exactProfileEarliestExpiry = expiresAt

	newAuthKeyID := [8]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	_, err := r.FreezeNegotiatedSessionLayerAt(newAuthKeyID, 99, 227, 99)
	var capacityErr *ExactSessionProfileCapacityError
	if !errors.As(err, &capacityErr) || capacityErr.Limit != maxExactSessionProfileEntries {
		t.Fatalf("full live registry err = %v, want typed capacity %d", err, maxExactSessionProfileEntries)
	}
	if len(r.exactProfiles) != maxExactSessionProfileEntries {
		t.Fatalf("capacity refusal evicted live watermark: entries=%d", len(r.exactProfiles))
	}
	if _, ok := r.NegotiatedSessionLayer(newAuthKeyID, 99); ok {
		t.Fatal("capacity-refused logical session was installed")
	}

	clk.advance(exactSessionProfileTTL + time.Nanosecond)
	if applied, err := r.FreezeNegotiatedSessionLayerAt(newAuthKeyID, 99, 227, 99); err != nil || !applied {
		t.Fatalf("claim after expiry purge = (%v,%v), want (true,nil)", applied, err)
	}
	if len(r.exactProfiles) != 1 {
		t.Fatalf("expired capacity purge entries=%d, want 1", len(r.exactProfiles))
	}
}
