package postresponse

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestTakeTransfersCallbacksAndRunsOnce(t *testing.T) {
	ctx := WithCallbacks(context.Background())
	var calls atomic.Int32
	if !Register(ctx, func() { calls.Add(1) }) || !Register(ctx, func() { calls.Add(1) }) {
		t.Fatal("register callbacks")
	}
	run := Take(ctx)
	if run == nil {
		t.Fatal("Take returned no callback")
	}
	Run(ctx)
	if got := calls.Load(); got != 0 {
		t.Fatalf("callbacks remained attached after Take: %d", got)
	}
	run()
	run()
	if got := calls.Load(); got != 2 {
		t.Fatalf("transferred callbacks ran %d times, want 2 total", got)
	}
}
