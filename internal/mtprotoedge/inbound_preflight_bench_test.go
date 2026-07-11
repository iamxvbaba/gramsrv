package mtprotoedge

import (
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
)

func BenchmarkInboundPlan32RPCContainer(b *testing.B) {
	ids := proto.NewMessageIDGen(time.Now)
	messages := make([]proto.Message, 32)
	var request bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&request); err != nil {
		b.Fatal(err)
	}
	requestBody := request.Copy()
	for i := range messages {
		messages[i] = proto.Message{
			ID: ids.New(proto.MessageFromClient), SeqNo: 1 + i*2, Bytes: len(requestBody), Body: requestBody,
		}
	}
	outerMsgID := ids.New(proto.MessageFromClient)
	var container bin.Buffer
	if err := (&proto.MessageContainer{Messages: messages}).Encode(&container); err != nil {
		b.Fatal(err)
	}
	body := container.Copy()
	s := New(Options{})
	cs := newConnState()

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		plan, err := s.preflightInbound(cs, outerMsgID, 64, body)
		if err != nil {
			b.Fatal(err)
		}
		plan.close()
	}
}
