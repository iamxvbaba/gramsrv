package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"

	appmoderation "telesrv/internal/app/moderation"
	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
	"telesrv/internal/store/memory"
)

// acceptChat 跑完 request→accept，返回 normal 态密聊 id 与 participant 视角 access_hash。
func (f *encryptedFixture) acceptChat(t *testing.T) (chatID int, partAccessHash int64) {
	t.Helper()
	res, err := f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
		UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
		RandomID: 4242,
		GA:       dhParam(0x55),
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	chatID = res.(*tg.EncryptedChatWaiting).ID
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)
	if _, err := f.router.onMessagesAcceptEncryption(f.participantCtx(), &tg.MessagesAcceptEncryptionRequest{
		Peer:           tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.ParticipantAccessHash},
		GB:             dhParam(0x66),
		KeyFingerprint: 7,
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	return chatID, chat.ParticipantAccessHash
}

func encNewMessagePayload(t *testing.T, rec phonePushRecord) *tg.UpdateNewEncryptedMessage {
	t.Helper()
	updates, ok := rec.msg.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed msg = %T, want single-update tg.Updates", rec.msg)
	}
	upd, ok := updates.Updates[0].(*tg.UpdateNewEncryptedMessage)
	if !ok {
		t.Fatalf("pushed update = %T, want UpdateNewEncryptedMessage", updates.Updates[0])
	}
	return upd
}

func TestReportEncryptedSpamPersistsMetadataOnly(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, participantAccessHash := f.acceptChat(t)
	reports := memory.NewModerationReportStore()
	f.router.deps.Moderation = appmoderation.NewService(reports)
	ok, err := f.router.onMessagesReportEncryptedSpam(
		f.participantCtx(),
		tg.InputEncryptedChat{ChatID: chatID, AccessHash: participantAccessHash},
	)
	if err != nil || !ok {
		t.Fatalf("report encrypted spam ok=%v err=%v", ok, err)
	}
	stored := reports.Reports()
	if len(stored) != 1 ||
		stored[0].Source != domain.ModerationSourceEncryptedSpam ||
		stored[0].ReporterUserID != f.participant.ID ||
		stored[0].Target != (domain.Peer{Type: domain.PeerTypeUser, ID: f.admin.ID}) ||
		len(stored[0].Items) != 1 ||
		stored[0].Items[0].Kind != domain.ModerationItemEncryptedChat {
		t.Fatalf("stored encrypted report=%+v", stored)
	}
	if string(stored[0].Items[0].Evidence) == "" ||
		string(stored[0].Items[0].Evidence) == "plaintext" {
		t.Fatalf("encrypted metadata evidence=%s", stored[0].Items[0].Evidence)
	}
}

func TestSendEncryptedRPCFlow(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, _ := f.acceptChat(t)
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)
	f.sessions.reset()

	// admin 发加密消息 → 投给 participant 设备。
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	sent, err := f.router.onMessagesSendEncrypted(f.adminCtx(), &tg.MessagesSendEncryptedRequest{
		Peer:     tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash},
		RandomID: 88888,
		Data:     data,
	})
	if err != nil {
		t.Fatalf("sendEncrypted: %v", err)
	}
	sentMsg, ok := sent.(*tg.MessagesSentEncryptedMessage)
	if !ok || sentMsg.Date == 0 {
		t.Fatalf("send response = %T %+v, want SentEncryptedMessage{date}", sent, sent)
	}
	recs := f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.participant.ID {
		t.Fatalf("send push = %+v, want single push to participant", recs)
	}
	upd := encNewMessagePayload(t, recs[0])
	if upd.Qts != 1 {
		t.Fatalf("pushed qts = %d, want 1", upd.Qts)
	}
	em, ok := upd.Message.(*tg.EncryptedMessage)
	if !ok || string(em.Bytes) != string(data) || em.ChatID != chatID {
		t.Fatalf("pushed message = %+v, want EncryptedMessage bytes verbatim", upd.Message)
	}

	// participant getState：设备 qts = 1。
	st, err := f.router.onUpdatesGetState(f.participantCtx())
	if err != nil {
		t.Fatalf("getState: %v", err)
	}
	if st.Qts != 1 {
		t.Fatalf("participant getState qts = %d, want 1", st.Qts)
	}

	// participant 离线补差分：从 qts=0 拿回该加密消息。
	differenceCtx := postresponse.WithCallbacks(f.participantCtx())
	diff, err := f.router.onUpdatesGetDifference(differenceCtx, &tg.UpdatesGetDifferenceRequest{Qts: 0})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", diff)
	}
	if len(full.NewEncryptedMessages) != 1 || full.State.Qts != 1 {
		t.Fatalf("difference enc msgs = %d state.qts = %d, want 1/1", len(full.NewEncryptedMessages), full.State.Qts)
	}
	gotEM, ok := full.NewEncryptedMessages[0].(*tg.EncryptedMessage)
	if !ok || string(gotEM.Bytes) != string(data) {
		t.Fatalf("difference message = %+v, want bytes verbatim", full.NewEncryptedMessages[0])
	}
	postresponse.Run(differenceCtx)

	// receivedQueue：确认到 qts=1，返回空 Vector。
	rq, err := f.router.onMessagesReceivedQueue(f.participantCtx(), 1)
	if err != nil {
		t.Fatalf("receivedQueue: %v", err)
	}
	if len(rq) != 0 {
		t.Fatalf("receivedQueue = %v, want empty vector", rq)
	}

	// 确认后再补差分（qts=1）：已无新消息 → DifferenceEmpty。
	diff2, err := f.router.onUpdatesGetDifference(f.participantCtx(), &tg.UpdatesGetDifferenceRequest{Qts: 1})
	if err != nil {
		t.Fatalf("getDifference 2: %v", err)
	}
	if _, ok := diff2.(*tg.UpdatesDifferenceEmpty); !ok {
		t.Fatalf("difference after ack = %T, want UpdatesDifferenceEmpty", diff2)
	}

	// 幂等重发同 random_id → 返回首次 date，不产生新 qts。
	f.sessions.reset()
	sent2, err := f.router.onMessagesSendEncrypted(f.adminCtx(), &tg.MessagesSendEncryptedRequest{
		Peer:     tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash},
		RandomID: 88888,
		Data:     data,
	})
	if err != nil {
		t.Fatalf("idempotent resend: %v", err)
	}
	if sent2.(*tg.MessagesSentEncryptedMessage).Date != sentMsg.Date {
		t.Fatalf("idempotent resend date = %d, want %d (首次落库 date)", sent2.(*tg.MessagesSentEncryptedMessage).Date, sentMsg.Date)
	}
}

func encOtherUpdate[T tg.UpdateClass](t *testing.T, diff tg.UpdatesDifferenceClass) T {
	t.Helper()
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", diff)
	}
	for _, u := range full.OtherUpdates {
		if got, ok := u.(T); ok {
			return got
		}
	}
	var zero T
	t.Fatalf("OtherUpdates %+v missing %T", full.OtherUpdates, zero)
	return zero
}

// TestEncryptionStateEventOfflineDelivery：participant 在 requestEncryption 时离线，
// 重连 getDifference 经 durable 状态事件补回 updateEncryption(requested)，且只补一次。
func TestEncryptionStateEventOfflineDelivery(t *testing.T) {
	f := newEncryptedFixture(t)
	res, err := f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
		UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
		RandomID: 5151,
		GA:       dhParam(0x55),
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	chatID := res.(*tg.EncryptedChatWaiting).ID

	// participant 离线补差分：拿到 updateEncryption(encryptedChatRequested, 携 g_a)。
	differenceCtx := postresponse.WithCallbacks(f.participantCtx())
	diff, err := f.router.onUpdatesGetDifference(differenceCtx, &tg.UpdatesGetDifferenceRequest{Qts: 0})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	upd := encOtherUpdate[*tg.UpdateEncryption](t, diff)
	requested, ok := upd.Chat.(*tg.EncryptedChatRequested)
	if !ok || requested.ID != chatID || len(requested.GA) == 0 {
		t.Fatalf("offline handshake update = %+v, want EncryptedChatRequested with g_a", upd.Chat)
	}
	deviceKey := businessAuthKeyInt64(encPartAuthKey)
	beforeDelivery, err := f.queue.ListUndeliveredStateEvents(f.ctx, f.participant.ID, deviceKey, 10)
	if err != nil || len(beforeDelivery) == 0 {
		t.Fatalf("state events before difference delivery = %v err=%v, want pending", beforeDelivery, err)
	}
	postresponse.Run(differenceCtx)
	afterDelivery, err := f.queue.ListUndeliveredStateEvents(f.ctx, f.participant.ID, deviceKey, 10)
	if err != nil || len(afterDelivery) != 0 {
		t.Fatalf("state events after difference delivery = %v err=%v, want none", afterDelivery, err)
	}

	// 再次补差分：已投递 → 不重复（DifferenceEmpty）。
	diff2, err := f.router.onUpdatesGetDifference(f.participantCtx(), &tg.UpdatesGetDifferenceRequest{Qts: 0})
	if err != nil {
		t.Fatalf("getDifference 2: %v", err)
	}
	if _, ok := diff2.(*tg.UpdatesDifferenceEmpty); !ok {
		t.Fatalf("redelivery: difference = %T, want UpdatesDifferenceEmpty", diff2)
	}
}

func TestEncryptedDifferenceAcknowledgesOnlyProjectedStateEvents(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, _ := f.acceptChat(t)
	deviceKey := businessAuthKeyInt64(encPartAuthKey)
	// Retire handshake events created while establishing the fixture; the test
	// below owns the complete pending set.
	prior, err := f.queue.ListUndeliveredStateEvents(f.ctx, f.participant.ID, deviceKey, 100)
	if err != nil {
		t.Fatalf("list prior events: %v", err)
	}
	priorIDs := make([]int64, 0, len(prior))
	for _, event := range prior {
		priorIDs = append(priorIDs, event.ID)
	}
	if err := f.queue.MarkStateEventsDelivered(context.Background(), deviceKey, priorIDs); err != nil {
		t.Fatalf("retire prior events: %v", err)
	}

	validID, err := f.queue.AppendStateEvent(f.ctx, domain.EncryptedStateEvent{
		TargetUserID: f.participant.ID,
		ChatID:       chatID,
		Type:         domain.EncryptedStateEventRead,
		MaxDate:      1700001000,
		Date:         1700001001,
	})
	if err != nil {
		t.Fatalf("append valid event: %v", err)
	}
	missingChatID, err := f.queue.AppendStateEvent(f.ctx, domain.EncryptedStateEvent{
		TargetUserID: f.participant.ID,
		ChatID:       999999,
		Type:         domain.EncryptedStateEventEncryption,
		Date:         1700001002,
	})
	if err != nil {
		t.Fatalf("append missing-chat event: %v", err)
	}
	unknownID, err := f.queue.AppendStateEvent(f.ctx, domain.EncryptedStateEvent{
		TargetUserID: f.participant.ID,
		ChatID:       chatID,
		Type:         domain.EncryptedStateEventType(99),
		Date:         1700001003,
	})
	if err != nil {
		t.Fatalf("append unknown event: %v", err)
	}

	ctx := postresponse.WithCallbacks(f.participantCtx())
	diff, err := f.router.onUpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	read := encOtherUpdate[*tg.UpdateEncryptedMessagesRead](t, diff)
	if read.ChatID != chatID {
		t.Fatalf("projected read chat_id = %d, want %d", read.ChatID, chatID)
	}
	postresponse.Run(ctx)
	pending, err := f.queue.ListUndeliveredStateEvents(f.ctx, f.participant.ID, deviceKey, 100)
	if err != nil {
		t.Fatalf("list pending after delivery: %v", err)
	}
	remaining := make(map[int64]bool, len(pending))
	for _, event := range pending {
		remaining[event.ID] = true
	}
	if remaining[validID] || !remaining[missingChatID] || !remaining[unknownID] {
		t.Fatalf("pending IDs after delivery = %v; valid=%d missing=%d unknown=%d", remaining, validID, missingChatID, unknownID)
	}
}

// TestDiscardStateEventOfflineDelivery：admin 在 participant 未接受时 discard（账号级），
// participant 离线设备 getDifference 补回 encryptedChatDiscarded。
func TestDiscardStateEventOfflineDelivery(t *testing.T) {
	f := newEncryptedFixture(t)
	res, err := f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
		UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
		RandomID: 6262,
		GA:       dhParam(0x55),
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	chatID := res.(*tg.EncryptedChatWaiting).ID
	// admin 撤回邀请。
	if _, err := f.router.onMessagesDiscardEncryption(f.adminCtx(), &tg.MessagesDiscardEncryptionRequest{ChatID: chatID, DeleteHistory: false}); err != nil {
		t.Fatalf("discard: %v", err)
	}
	// participant 离线补差分：现态是 discarded，重建出 encryptedChatDiscarded。
	diff, err := f.router.onUpdatesGetDifference(f.participantCtx(), &tg.UpdatesGetDifferenceRequest{Qts: 0})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	upd := encOtherUpdate[*tg.UpdateEncryption](t, diff)
	if _, ok := upd.Chat.(*tg.EncryptedChatDiscarded); !ok {
		t.Fatalf("offline discard update = %T, want EncryptedChatDiscarded", upd.Chat)
	}
}

func TestReadEncryptedHistoryPushesPeer(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, _ := f.acceptChat(t)
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)
	f.sessions.reset()

	ok, err := f.router.onMessagesReadEncryptedHistory(f.adminCtx(), &tg.MessagesReadEncryptedHistoryRequest{
		Peer:    tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash},
		MaxDate: 1500,
	})
	if err != nil || !ok {
		t.Fatalf("readEncryptedHistory: ok=%v err=%v", ok, err)
	}
	recs := f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.participant.ID {
		t.Fatalf("read push = %+v, want single push to participant", recs)
	}
	updates := recs[0].msg.(*tg.Updates)
	rd, ok := updates.Updates[0].(*tg.UpdateEncryptedMessagesRead)
	if !ok || rd.ChatID != chatID || rd.MaxDate != 1500 {
		t.Fatalf("read update = %+v, want UpdateEncryptedMessagesRead{chat,max_date=1500}", updates.Updates[0])
	}
}

func TestReadEncryptedHistoryInvalidMaxDate(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, _ := f.acceptChat(t)
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)
	_, err := f.router.onMessagesReadEncryptedHistory(f.adminCtx(), &tg.MessagesReadEncryptedHistoryRequest{
		Peer:    tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash},
		MaxDate: 0,
	})
	assertPhoneRPCErr(t, err, "MAX_DATE_INVALID")
}

func TestSetEncryptedTypingPushesPeer(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, _ := f.acceptChat(t)
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)
	f.sessions.reset()

	ok, err := f.router.onMessagesSetEncryptedTyping(f.adminCtx(), &tg.MessagesSetEncryptedTypingRequest{
		Peer:   tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash},
		Typing: true,
	})
	if err != nil || !ok {
		t.Fatalf("setEncryptedTyping: ok=%v err=%v", ok, err)
	}
	recs := f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.participant.ID {
		t.Fatalf("typing push = %+v, want single push to participant", recs)
	}
	if _, ok := recs[0].msg.(*tg.Updates).Updates[0].(*tg.UpdateEncryptedChatTyping); !ok {
		t.Fatalf("typing update = %T, want UpdateEncryptedChatTyping", recs[0].msg.(*tg.Updates).Updates[0])
	}
}
