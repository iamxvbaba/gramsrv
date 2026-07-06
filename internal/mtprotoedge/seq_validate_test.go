package mtprotoedge

import (
	"math/rand"
	"testing"
)

// referenceValidateSeq 是 validateSeq 的旧全扫描语义（无高水位快路径），
// 作为随机对拍的行为基准：快路径只允许接受「全扫描也必然接受」的子集。
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
			// 小值域制造乱序、重复 seq 与 too_low/too_high 反转；大 i 也覆盖淘汰窗口。
			msgID := int64(rng.Intn(3000) + 1)
			seqNo := int32(rng.Intn(600))
			content := rng.Intn(4) != 0
			if _, ok := fast.seen[msgID]; ok {
				continue // 真实调用链在 seenRecord 命中时不会走 validateSeq
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
	// 正常客户端：msg_id 与 content seq_no 严格递增，应全部通过。
	for i := 0; i < 1000; i++ {
		msgID := int64(1000 + i*4)
		seqNo := int32(i*2 + 1)
		if code := cs.validateSeq(msgID, seqNo, true); code != 0 {
			t.Fatalf("ordered message %d rejected with code %d", i, code)
		}
		cs.track(msgID, seqNo, true, msgStateReceived)
	}
	// seq 回退必须仍被拒绝（快路径不放行）。
	if code := cs.validateSeq(1000+1000*4, 3, true); code != badMsgSeqTooLow {
		t.Fatalf("seq regression code = %d, want badMsgSeqTooLow", code)
	}
	// 旧 msg_id 配新 seq 也必须仍被拒绝。
	if code := cs.validateSeq(500, 5000, true); code != badMsgSeqTooHigh {
		t.Fatalf("old msg_id with high seq code = %d, want badMsgSeqTooHigh", code)
	}
}
