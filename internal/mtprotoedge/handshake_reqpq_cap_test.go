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

// reqPQConn 是只会不断返回同一个 req_pq_multi 帧的假 transport.Conn，用于驱动 bufferedConn
// 的 req_pq 计数上界。除 Recv 外的方法不会被 bufferedConn.Recv 调用。
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

// TestBufferedConnReqPQCapAborts 锁定握手 req_pq 计数上界：连续 req_pq 超过 maxHandshakeReqPQ
// 后，bufferedConn.Recv 返回 errTooManyHandshakeReqPQ，止住握手重启死循环（不依赖 20s 总超时）。
func TestBufferedConnReqPQCapAborts(t *testing.T) {
	frame := buildReqPQFrame(t)
	bc := newBufferedConn(&reqPQConn{frame: frame})

	ctx := context.Background()
	var b bin.Buffer
	// 前 maxHandshakeReqPQ 个 req_pq 正常返回（容纳「fake+真」与少量正常重连重启）。
	for i := 0; i < maxHandshakeReqPQ; i++ {
		if err := bc.Recv(ctx, &b); err != nil {
			t.Fatalf("req_pq %d: unexpected err %v (cap should not trip yet)", i+1, err)
		}
		if !isUnencryptedReqPQFrame(&b) {
			t.Fatalf("frame %d not recognized as req_pq", i+1)
		}
	}
	// 第 maxHandshakeReqPQ+1 个触发上界，瞬断。
	if err := bc.Recv(ctx, &b); !errors.Is(err, errTooManyHandshakeReqPQ) {
		t.Fatalf("after cap: err = %v, want errTooManyHandshakeReqPQ", err)
	}
}
