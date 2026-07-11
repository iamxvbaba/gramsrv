package mtprotoedge

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
)

type quickAckDeadlineProbe struct {
	requested bool
	deadline  time.Time
	token     uint32
}

func (p *quickAckDeadlineProbe) ConsumeQuickAckRequested() bool {
	if !p.requested {
		return false
	}
	p.requested = false
	return true
}

func (p *quickAckDeadlineProbe) SendQuickAck(ctx context.Context, token uint32) error {
	p.deadline, _ = ctx.Deadline()
	p.token = token
	return nil
}

func (p *quickAckDeadlineProbe) SendQuickAckDeadline(deadline time.Time, token uint32) error {
	p.deadline = deadline
	p.token = token
	return nil
}

func (*quickAckDeadlineProbe) Send(context.Context, *bin.Buffer) error { return nil }
func (*quickAckDeadlineProbe) Recv(context.Context, *bin.Buffer) error { return nil }
func (*quickAckDeadlineProbe) Close() error                            { return nil }

func TestQuickAckUsesServerWriteDeadline(t *testing.T) {
	probe := &quickAckDeadlineProbe{requested: true}
	var key crypto.Key
	authKey := key.WithID()
	before := time.Now()
	if err := sendQuickAckIfRequested(context.Background(), probe, authKey, []byte("plain"), 50*time.Millisecond); err != nil {
		t.Fatalf("send quick ack: %v", err)
	}
	if probe.deadline.IsZero() {
		t.Fatal("quick ack did not receive a write deadline")
	}
	if probe.deadline.Before(before.Add(40*time.Millisecond)) || probe.deadline.After(time.Now().Add(60*time.Millisecond)) {
		t.Fatalf("quick ack deadline = %v, want about server timeout from now", probe.deadline)
	}
}

func TestQuickAckHonorsEarlierCallerDeadline(t *testing.T) {
	probe := &quickAckDeadlineProbe{requested: true}
	ctxDeadline := time.Now().Add(25 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), ctxDeadline)
	defer cancel()
	var key crypto.Key
	if err := sendQuickAckIfRequested(ctx, probe, key.WithID(), []byte("plain"), time.Second); err != nil {
		t.Fatalf("send quick ack: %v", err)
	}
	if delta := probe.deadline.Sub(ctxDeadline); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("quick ack deadline = %v, want caller deadline %v", probe.deadline, ctxDeadline)
	}
}
