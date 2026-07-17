package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestSendPrivateRichMessageSurvivesReadPaths 验证 Layer 227 富文本（rich_message JSONB）
// 经全部读路径字节级保真且双盒一致：
//   - 发送结果双端（sender/recipient）携带 rich_message；
//   - Blocks 是不透明 TL 字节，含 0x00/0xFF/非 UTF-8 经 JSONB base64 逐字节相等；
//   - 内嵌 photo 快照（id/access_hash/sizes）存活；
//   - history（ListByUser）/ GetByIDs / 更新事件重建路径均带 rich_message。
func TestSendPrivateRichMessageSurvivesReadPaths(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 81, Phone: "+1996" + suffix + "01", FirstName: "RichSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 82, Phone: "+1996" + suffix + "02", FirstName: "RichRecipient"})
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

	// Blocks 在 store 层是不透明字节（rpc 层 TL 编解码），含 0x00/0xFF/非 UTF-8 以验证
	// JSONB base64 字节级 round-trip。
	rawBlocks := []byte{0x00, 0x01, 0xFF, 0x80, 'b', 'l', 'k'}
	rich := &domain.MessageRichMessage{
		Rtl:    true,
		Blocks: rawBlocks,
		Photos: []domain.Photo{{ID: 9911, AccessHash: 7, DCID: 2, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600}}}},
	}

	res, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        time.Now().UnixNano(),
		RichMessage:     rich,
		Date:            int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("send private rich: %v", err)
	}

	assertRich := func(name string, m *domain.MessageRichMessage) {
		t.Helper()
		if m == nil {
			t.Fatalf("%s: rich message lost", name)
		}
		if !m.Rtl {
			t.Errorf("%s: rtl = false, want true", name)
		}
		if !bytes.Equal(m.Blocks, rawBlocks) {
			t.Fatalf("%s: blocks byte mismatch: %v want %v", name, m.Blocks, rawBlocks)
		}
		if len(m.Photos) != 1 || m.Photos[0].ID != 9911 || m.Photos[0].AccessHash != 7 {
			t.Fatalf("%s: embedded photo lost: %+v", name, m.Photos)
		}
		if len(m.Photos[0].Sizes) != 1 || m.Photos[0].Sizes[0].Type != "x" {
			t.Fatalf("%s: embedded photo sizes lost: %+v", name, m.Photos[0].Sizes)
		}
	}

	// 双端发送结果带 rich_message。
	assertRich("sender result", res.SenderMessage.RichMessage)
	assertRich("recipient result", res.RecipientMessage.RichMessage)

	// history 读回（getHistory 主渲染路径，backward 扁平查询）。
	list, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list by user: %v", err)
	}
	if len(list.Messages) == 0 {
		t.Fatal("recipient history empty")
	}
	assertRich("recipient history", list.Messages[0].RichMessage)

	// GetByIDs（getMessages / getRichMessage 共用取数路径）。
	byID, err := messages.GetByIDs(ctx, recipient.ID, []int{res.RecipientMessage.ID})
	if err != nil {
		t.Fatalf("recipient get by ids: %v", err)
	}
	if len(byID.Messages) != 1 {
		t.Fatalf("get by ids = %d, want 1", len(byID.Messages))
	}
	assertRich("recipient get by ids", byID.Messages[0].RichMessage)

	// 接收方经更新事件重建（离线 difference / 在线推送共用路径）。
	events := NewUpdateEventStore(pool)
	got, err := events.ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("list recipient events: %v", err)
	}
	var sawEvent bool
	for _, ev := range got {
		if ev.Type == domain.UpdateEventNewMessage && ev.Message.ID != 0 {
			sawEvent = true
			assertRich("recipient update-event", ev.Message.RichMessage)
		}
	}
	if !sawEvent {
		t.Fatal("no new_message event for recipient")
	}

	// The shared store invariant still rejects a command with no text, media, or
	// rich payload; accepting rich-only must not make truly empty rows possible.
	_, err = messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        time.Now().UnixNano(),
		Date:            int(time.Now().Unix()),
	})
	if !errors.Is(err, domain.ErrMessageEmpty) {
		t.Fatalf("send empty private message err = %v, want ErrMessageEmpty", err)
	}
}
