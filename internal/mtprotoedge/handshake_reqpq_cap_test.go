package mtprotoedge

import (
	"context"
	"errors"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/transport"
)

// reqPQConn is a fake transport.Conn that just keeps returning the same
// req_pq_multi frame, used to drive bufferedConn's req_pq count bound. Methods
// other than Recv are never called by bufferedConn.Recv.
type reqPQConn struct {
	transport.Conn
	frame []byte
}

func (c *reqPQConn) Recv(_ context.Context, b *bin.Buffer) error {
	b.ResetTo(append([]byte(nil), c.frame...))
	return nil
}

func buildReqPQFrame(t *testing.T) []byte {
	t.Helper()
	var payload bin.Buffer
	if err := (&mt.ReqPqMultiRequest{}).Encode(&payload); err != nil {
		t.Fatalf("encode req_pq_multi: %v", err)
	}
	msg := proto.UnencryptedMessage{MessageID: 1, MessageData: payload.Raw()}
	var frame bin.Buffer
	if err := msg.Encode(&frame); err != nil {
		t.Fatalf("encode unencrypted message: %v", err)
	}
	return append([]byte(nil), frame.Raw()...)
}

// TestBufferedConnReqPQCapAborts locks in the handshake req_pq count bound: after
// consecutive req_pq exceeds maxHandshakeReqPQ, bufferedConn.Recv returns
// errTooManyHandshakeReqPQ, stopping the handshake restart loop (without relying
// on the 20s overall timeout).
func TestBufferedConnReqPQCapAborts(t *testing.T) {
	frame := buildReqPQFrame(t)
	bc := newBufferedConn(&reqPQConn{frame: frame})

	ctx := context.Background()
	var b bin.Buffer
	// The first maxHandshakeReqPQ req_pq return normally (accommodating "fake+real"
	// and a few normal reconnect restarts).
	for i := 0; i < maxHandshakeReqPQ; i++ {
		if err := bc.Recv(ctx, &b); err != nil {
			t.Fatalf("req_pq %d: unexpected err %v (cap should not trip yet)", i+1, err)
		}
		if !isUnencryptedReqPQFrame(&b) {
			t.Fatalf("frame %d not recognized as req_pq", i+1)
		}
	}
	// The maxHandshakeReqPQ+1-th trips the bound and drops the connection immediately.
	if err := bc.Recv(ctx, &b); !errors.Is(err, errTooManyHandshakeReqPQ) {
		t.Fatalf("after cap: err = %v, want errTooManyHandshakeReqPQ", err)
	}
}
