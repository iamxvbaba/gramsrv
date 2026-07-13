package mtprotoedge

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestListenAndServeCallsServingHookOnBoundListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	metrics := newIntakeCaptureMetrics()
	hookEntered := make(chan net.Addr, 1)
	releaseHook := make(chan struct{})
	hookReleased := false
	defer func() {
		if !hookReleased {
			close(releaseHook)
		}
	}()
	srv := New(Options{Metrics: metrics, OnServing: func(addr net.Addr) {
		hookEntered <- addr
		<-releaseHook
	}})
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx, "127.0.0.1:0") }()

	var addr net.Addr
	select {
	case addr = <-hookEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("serving callback was not published")
	}
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatalf("dial after serving callback: %v", err)
	}
	_ = conn.Close()

	// The observation hook is still blocked: raw_accept can only advance here
	// when the intake loop was installed before OnServing was called.
	deadline := time.After(3 * time.Second)
	for !hasIntakeEvent(metrics.snapshot(), "raw_accept", "accepted") {
		select {
		case <-metrics.wake:
		case <-deadline:
			t.Fatal("raw accept loop did not run while serving hook was blocked")
		}
	}
	close(releaseHook)
	hookReleased = true

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe did not stop")
	}
}

type intakeCaptureMetrics struct {
	NopMetrics

	mu     sync.Mutex
	events []intakeMetricEvent
	wake   chan struct{}
}

type intakeMetricEvent struct {
	stage   string
	outcome string
}

func newIntakeCaptureMetrics() *intakeCaptureMetrics {
	return &intakeCaptureMetrics{wake: make(chan struct{}, 16)}
}

func (m *intakeCaptureMetrics) ConnectionIntake(stage, outcome string, _ time.Duration) {
	m.mu.Lock()
	m.events = append(m.events, intakeMetricEvent{stage: stage, outcome: outcome})
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *intakeCaptureMetrics) snapshot() []intakeMetricEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]intakeMetricEvent(nil), m.events...)
}

func TestConnectionIntakeStagesIncludePrePromotionDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	metrics := newIntakeCaptureMetrics()
	ready := make(chan net.Addr, 1)
	srv := New(Options{
		Metrics:       metrics,
		ObfuscatedTCP: true,
		WebSocket:     true,
		OnServing: func(addr net.Addr) {
			ready <- addr
		},
	})
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx, "127.0.0.1:0") }()

	var addr net.Addr
	select {
	case addr = <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("serving callback was not published")
	}
	conn, err := net.DialTimeout("tcp", addr.String(), time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Four non-HTTP bytes let the mux classify the connection as raw MTProto;
	// closing before the remaining obfuscated2 header arrives must still expose
	// the exact transport_promote failure phase.
	if _, err := conn.Write([]byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("write prefix: %v", err)
	}
	_ = conn.Close()

	deadline := time.After(3 * time.Second)
	for {
		events := metrics.snapshot()
		if hasIntakeEvent(events, "raw_accept", "accepted") &&
			hasIntakeEvent(events, "mux_sniff", "ready") &&
			hasIntakeEvent(events, "transport_dispatch", "accepted") &&
			hasIntakeEvent(events, "transport_promote", "client_disconnect") {
			break
		}
		select {
		case <-metrics.wake:
		case <-deadline:
			t.Fatalf("intake events did not converge: %v", events)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "closed") {
			t.Fatalf("server shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

func hasIntakeEvent(events []intakeMetricEvent, stage, outcome string) bool {
	for _, event := range events {
		if event.stage == stage && event.outcome == outcome {
			return true
		}
	}
	return false
}
