package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestSendPrivateReplyMarkupSurvivesReadPaths 验证 bot inline keyboard（reply_markup）
// 经全部读路径字节级保真且双盒一致（P3 I2/I3）：
//   - 发送结果双端（sender/recipient）携带 markup；
//   - history（ListByUser）读回 markup，callback data 含 0x00/0xFF/非 UTF-8 逐字节相等；
//   - 接收方经 UpdateEventStore.ListAfter（在线/离线 difference 共用重建路径）带 markup；
//   - 编辑替换/清空 markup 后双盒一致。
func TestSendPrivateReplyMarkupSurvivesReadPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 71, Phone: "+1997" + suffix + "01", FirstName: "MarkupBot"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 72, Phone: "+1997" + suffix + "02", FirstName: "MarkupRecipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	ids := []int64{sender.ID, recipient.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})

	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))

	rawData := []byte{0x00, 0x01, 0xFF, 0x80, 'd', 'a', 't', 'a'}
	markup := &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{
		{Type: domain.MarkupButtonCallback, Text: "Press", Data: rawData},
		{Type: domain.MarkupButtonURL, Text: "Open", URL: "https://example.com/p"},
	}}}

	res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        time.Now().UnixNano(),
		Message:         "tap",
		ReplyMarkup:     markup,
		Date:            int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("send private markup: %v", err)
	}

	assertMarkup := func(name string, m *domain.MessageReplyMarkup) {
		t.Helper()
		if m == nil || len(m.Inline) != 1 || len(m.Inline[0]) != 2 {
			t.Fatalf("%s: markup shape lost: %+v", name, m)
		}
		btn := m.Inline[0][0]
		if btn.Type != domain.MarkupButtonCallback || !bytes.Equal(btn.Data, rawData) {
			t.Fatalf("%s: callback data byte mismatch: %v want %v", name, btn.Data, rawData)
		}
		if m.Inline[0][1].Type != domain.MarkupButtonURL || m.Inline[0][1].URL != "https://example.com/p" {
			t.Fatalf("%s: url button lost: %+v", name, m.Inline[0][1])
		}
	}

	// 双端发送结果带 markup（I3）。
	assertMarkup("sender result", res.SenderMessage.ReplyMarkup)
	assertMarkup("recipient result", res.RecipientMessage.ReplyMarkup)

	// history 读回（getHistory 主渲染路径）字节保真。
	list, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list by user: %v", err)
	}
	if len(list.Messages) == 0 {
		t.Fatal("recipient history empty")
	}
	assertMarkup("recipient history", list.Messages[0].ReplyMarkup)

	// 接收方经更新事件重建（离线 difference / 在线推送共用路径）带 markup。
	events := NewUpdateEventStore(pool)
	got, err := events.ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	var sawEvent bool
	for _, ev := range got {
		if ev.Type == domain.UpdateEventNewMessage && ev.Message.ID != 0 {
			sawEvent = true
			assertMarkup("recipient update-event", ev.Message.ReplyMarkup)
		}
	}
	if !sawEvent {
		t.Fatal("no new_message event for recipient")
	}

	// 编辑替换 markup（仅一个 callback 按钮）→ 双盒一致更新。
	editRes, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID:    sender.ID,
		Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
		ID:             res.SenderMessage.ID,
		Message:        "tap edited",
		EditDate:       int(time.Now().Unix()) + 1,
		SetReplyMarkup: true,
		ReplyMarkup:    &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{{Type: domain.MarkupButtonCallback, Text: "Only", Data: []byte("x")}}}},
	})
	if err != nil {
		t.Fatalf("edit markup: %v", err)
	}
	if len(editRes.Edited) == 0 {
		t.Fatal("edit produced no boxes")
	}
	for _, e := range editRes.Edited {
		m := e.Message.ReplyMarkup
		if m == nil || len(m.Inline) != 1 || len(m.Inline[0]) != 1 || string(m.Inline[0][0].Data) != "x" {
			t.Fatalf("edit box %d markup not replaced: %+v", e.UserID, m)
		}
	}

	// 编辑清空 markup（SetReplyMarkup + nil）→ 双盒 markup 清空。
	clearRes, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID:    sender.ID,
		Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
		ID:             res.SenderMessage.ID,
		Message:        "tap cleared",
		EditDate:       int(time.Now().Unix()) + 2,
		SetReplyMarkup: true,
		ReplyMarkup:    nil,
	})
	if err != nil {
		t.Fatalf("clear markup: %v", err)
	}
	for _, e := range clearRes.Edited {
		if e.Message.ReplyMarkup != nil {
			t.Fatalf("clear box %d markup not cleared: %+v", e.UserID, e.Message.ReplyMarkup)
		}
	}
}

func TestSendPrivateViaBotIDSurvivesReadPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 73, Phone: "+1997" + suffix + "03", FirstName: "InlineSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 74, Phone: "+1997" + suffix + "04", FirstName: "InlineRecipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	ids := []int64{sender.ID, recipient.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM user_update_events WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})

	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	viaBotID := int64(770000000001)
	randomID := time.Now().UnixNano()
	res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        randomID,
		Message:         "inline via",
		ViaBotID:        viaBotID,
		Date:            int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("send private via bot: %v", err)
	}
	if res.SenderMessage.ViaBotID != viaBotID || res.RecipientMessage.ViaBotID != viaBotID {
		t.Fatalf("send result via = sender %d recipient %d, want %d", res.SenderMessage.ViaBotID, res.RecipientMessage.ViaBotID, viaBotID)
	}

	dup, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        randomID,
		Message:         "inline via",
		ViaBotID:        viaBotID,
		Date:            int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("duplicate private via bot: %v", err)
	}
	if !dup.Duplicate ||
		dup.SenderMessage.ID != res.SenderMessage.ID || dup.SenderMessage.Pts != res.SenderMessage.Pts ||
		dup.RecipientMessage.ID != res.RecipientMessage.ID || dup.RecipientMessage.Pts != res.RecipientMessage.Pts {
		t.Fatalf("duplicate immutable receipt = %+v/%+v, want sender id/pts %d/%d recipient %d/%d",
			dup.SenderMessage, dup.RecipientMessage,
			res.SenderMessage.ID, res.SenderMessage.Pts,
			res.RecipientMessage.ID, res.RecipientMessage.Pts)
	}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        randomID,
		Message:         "inline via conflict",
		ViaBotID:        viaBotID,
		Date:            int(time.Now().Unix()),
	}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("conflicting via-bot replay err = %v, want ErrMessageRandomIDDuplicate", err)
	}

	recipientHistory, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(recipientHistory.Messages) == 0 || recipientHistory.Messages[0].ViaBotID != viaBotID {
		t.Fatalf("recipient history via lost: %+v", recipientHistory.Messages)
	}

	byID, err := messages.GetByIDs(ctx, recipient.ID, []int{res.RecipientMessage.ID})
	if err != nil {
		t.Fatalf("recipient get by ids: %v", err)
	}
	if len(byID.Messages) != 1 || byID.Messages[0].ViaBotID != viaBotID {
		t.Fatalf("recipient get by ids via lost: %+v", byID.Messages)
	}

	events := NewUpdateEventStore(pool)
	got, err := events.ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	var sawEvent bool
	for _, ev := range got {
		if ev.Type == domain.UpdateEventNewMessage && ev.Message.ID != 0 {
			sawEvent = true
			if ev.Message.ViaBotID != viaBotID {
				t.Fatalf("recipient update-event via = %d, want %d", ev.Message.ViaBotID, viaBotID)
			}
		}
	}
	if !sawEvent {
		t.Fatal("no new_message event for recipient")
	}

	dialogs := NewDialogStore(pool)
	dialogList, err := dialogs.ListByUser(ctx, sender.ID, domain.DialogFilter{Limit: 20})
	if err != nil {
		t.Fatalf("sender dialogs: %v", err)
	}
	if len(dialogList.Messages) == 0 || dialogList.Messages[0].ViaBotID != viaBotID {
		t.Fatalf("sender dialog top via lost: %+v", dialogList.Messages)
	}

	if _, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID:     recipient.ID,
		Peer:            domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID},
		ID:              res.RecipientMessage.ID,
		Message:         "wrong via bot edit",
		EditDate:        int(time.Now().Unix()) + 1,
		ViaBotEditBotID: viaBotID + 1,
	}); err != domain.ErrMessageAuthorRequired {
		t.Fatalf("wrong via bot edit err = %v, want ErrMessageAuthorRequired", err)
	}

	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID:     recipient.ID,
		Peer:            domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID},
		ID:              res.RecipientMessage.ID,
		Message:         "inline via edited",
		EditDate:        int(time.Now().Unix()) + 2,
		ViaBotEditBotID: viaBotID,
	})
	if err != nil {
		t.Fatalf("via bot edit: %v", err)
	}
	if len(edited.Edited) != 2 {
		t.Fatalf("via bot edit boxes = %d, want 2", len(edited.Edited))
	}
	senderHistory, err := messages.ListByUser(ctx, sender.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}, Limit: 10})
	if err != nil {
		t.Fatalf("sender history after via edit: %v", err)
	}
	if len(senderHistory.Messages) == 0 || senderHistory.Messages[0].Body != "inline via edited" || senderHistory.Messages[0].ViaBotID != viaBotID {
		t.Fatalf("sender history after via edit = %+v, want edited body with via bot", senderHistory.Messages)
	}
}
