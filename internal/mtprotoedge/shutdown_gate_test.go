package mtprotoedge

import (
	"testing"
	"time"
)

func TestTerminalFailurePathsCloseGatesBeforeBlockingTransportClose(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Conn)
	}{
		{name: "write failure", run: (*Conn).failTransport},
		{name: "slow consumer", run: (*Conn).dropSlowConsumer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release := make(chan struct{})
			tr := newSlowCloseTransport(0, release)
			scheduler := newInboundRPCScheduler(1, 1, 1024)
			defer scheduler.stop(time.Second)
			c := &Conn{
				transport:       tr,
				metrics:         NopMetrics{},
				outbound:        make(chan outboundOp, 1),
				outboundControl: make(chan outboundOp, 1),
				outboundStop:    make(chan struct{}),
			}
			c.startInboundRPCScheduler(scheduler, 1, 1, time.Second)
			returned := make(chan struct{})
			go func() {
				tt.run(c)
				close(returned)
			}()

			deadline := time.Now().Add(time.Second)
			for tr.closes.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if tr.closes.Load() == 0 {
				t.Fatal("terminal path did not enter transport.Close")
			}
			if !c.terminal.Load() {
				t.Fatal("producer terminal gate was not published before blocking Close")
			}
			select {
			case <-c.outboundStop:
			default:
				t.Fatal("outbound stop was not published before blocking Close")
			}
			select {
			case <-c.rpcRootCtx.Done():
			default:
				t.Fatal("RPC root was not canceled before blocking Close")
			}

			close(release)
			select {
			case <-returned:
			case <-time.After(time.Second):
				t.Fatal("terminal path did not return after transport release")
			}
		})
	}
}
