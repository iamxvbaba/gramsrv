package mtprotoedge

import (
	"math/rand"
	"testing"
)

// referenceValidateSeq is validateSeq's old full-scan semantics (without the
// high-water fast path), used as the behavioral baseline for randomized
// differential testing: the fast path may only accept the subset that a full scan
// would also necessarily accept.
func referenceValidateSeq(cs *connState, msgID int64, seqNo int32, content bool) int {
	if !content {
		return 0
	}
	for seenMsgID, record := range cs.seen {
		if !record.content {
			continue
		}
		if seenMsgID < msgID && record.seqNo >= seqNo {
			return badMsgSeqTooLow
		}
		if seenMsgID > msgID && record.seqNo <= seqNo {
			return badMsgSeqTooHigh
		}
	}
	return 0
}

func TestValidateSeqFastPathMatchesFullScan(t *testing.T) {
	rng := rand.New(rand.NewSource(20260705))
	for round := 0; round < 32; round++ {
		fast := newConnState()
		ref := newConnState()
		for i := 0; i < 2000; i++ {
			// A small value range produces out-of-order, duplicate seq and
			// too_low/too_high inversions; large i also covers the eviction window.
			msgID := int64(rng.Intn(3000) + 1)
			seqNo := int32(rng.Intn(600))
			content := rng.Intn(4) != 0
			if _, ok := fast.seen[msgID]; ok {
				continue // the real call chain does not reach validateSeq when seenRecord hits
			}
			got := fast.validateSeq(msgID, seqNo, content)
			want := referenceValidateSeq(ref, msgID, seqNo, content)
			if got != want {
				t.Fatalf("round %d step %d: validateSeq(msg_id=%d seq=%d content=%v) = %d, want %d",
					round, i, msgID, seqNo, content, got, want)
			}
			if got == 0 {
				fast.track(msgID, seqNo, content, msgStateReceived)
				ref.track(msgID, seqNo, content, msgStateReceived)
			}
		}
	}
}

func TestValidateSeqOrderedFastPath(t *testing.T) {
	cs := newConnState()
	// Well-behaved client: msg_id and content seq_no strictly increase, so all should pass.
	for i := 0; i < 1000; i++ {
		msgID := int64(1000 + i*4)
		seqNo := int32(i*2 + 1)
		if code := cs.validateSeq(msgID, seqNo, true); code != 0 {
			t.Fatalf("ordered message %d rejected with code %d", i, code)
		}
		cs.track(msgID, seqNo, true, msgStateReceived)
	}
	// A seq regression must still be rejected (the fast path does not let it through).
	if code := cs.validateSeq(1000+1000*4, 3, true); code != badMsgSeqTooLow {
		t.Fatalf("seq regression code = %d, want badMsgSeqTooLow", code)
	}
	// An old msg_id with a new seq must also still be rejected.
	if code := cs.validateSeq(500, 5000, true); code != badMsgSeqTooHigh {
		t.Fatalf("old msg_id with high seq code = %d, want badMsgSeqTooHigh", code)
	}
}
