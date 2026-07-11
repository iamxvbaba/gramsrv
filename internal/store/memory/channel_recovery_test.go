package memory

import (
	"context"
	"testing"
)

func TestMaxChannelPtsBatchUsesOneSnapshotAndOmitsMissing(t *testing.T) {
	channels := NewChannelStore()
	channels.mu.Lock()
	channels.ptsSeq[10] = 7
	channels.ptsSeq[20] = 11
	channels.mu.Unlock()

	got, err := channels.MaxChannelPtsBatch(context.Background(), []int64{20, 999, 10, 20})
	if err != nil {
		t.Fatalf("MaxChannelPtsBatch: %v", err)
	}
	if len(got) != 2 || got[10] != 7 || got[20] != 11 {
		t.Fatalf("batch pts = %v, want map[10:7 20:11] with missing id omitted", got)
	}
}
