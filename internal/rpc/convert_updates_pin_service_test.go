package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestDifferenceNewMessageKeepsPinServiceReply 验证差分输出的 pin 服务
// 消息保留 reply 头（reply_to_msg_id = 接收方视角被置顶消息 id）；缺失
// 时 TDesktop 渲染 "pinned Deleted message"。
func TestDifferenceNewMessageKeepsPinServiceReply(t *testing.T) {
	const bobID = int64(1000000042)
	const aliceID = int64(1000000041)
	event := domain.UpdateEvent{
		UserID:   bobID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      351,
		PtsCount: 1,
		Date:     1700000900,
		Message: domain.Message{
			ID:          117,
			OwnerUserID: bobID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
			Date:        1700000900,
			Media: &domain.MessageMedia{
				Kind:          domain.MessageMediaKindService,
				ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionPinMessage},
			},
			ReplyTo: &domain.MessageReply{
				MessageID: 115,
				Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
			},
		},
	}
	diff := domain.UpdateDifference{
		State:  domain.UpdateState{Pts: 351, Date: 1700000901},
		Events: []domain.UpdateEvent{event},
	}
	out, ok := tgUpdatesDifference(bobID, diff).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference type = %T, want *tg.UpdatesDifference", tgUpdatesDifference(bobID, diff))
	}
	if len(out.NewMessages) != 1 {
		t.Fatalf("new messages = %d, want 1", len(out.NewMessages))
	}
	svc, ok := out.NewMessages[0].(*tg.MessageService)
	if !ok {
		t.Fatalf("message type = %T, want messageService", out.NewMessages[0])
	}
	if _, ok := svc.Action.(*tg.MessageActionPinMessage); !ok {
		t.Fatalf("action = %T, want messageActionPinMessage", svc.Action)
	}
	reply, ok := svc.GetReplyTo()
	if !ok {
		t.Fatalf("difference 服务消息丢失 reply 头（TDesktop 将渲染 pinned Deleted message）")
	}
	header, ok := reply.(*tg.MessageReplyHeader)
	if !ok || header.ReplyToMsgID != 115 {
		t.Fatalf("reply header = %+v, want reply_to_msg_id=115", reply)
	}
}
