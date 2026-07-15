package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appsecret "telesrv/internal/app/secretchat"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// seqSecretChatIDAllocator 是单调自增的测试 chat id 分配器。
type seqSecretChatIDAllocator struct{ n int }

func (a *seqSecretChatIDAllocator) NextSecretChatID(context.Context) (int, error) {
	a.n++
	return a.n, nil
}

func (a *seqSecretChatIDAllocator) NextSecretChatIDAtLeast(_ context.Context, floor int) (int, error) {
	if a.n < floor {
		a.n = floor
	}
	a.n++
	return a.n, nil
}

func (a *seqSecretChatIDAllocator) CurrentSecretChatID(context.Context) (int, error) { return a.n, nil }

func dhParam(lead byte) []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = 0x42
	}
	b[0] = lead
	return b
}

type encryptedFixture struct {
	ctx         context.Context
	router      *Router
	sessions    *phoneCaptureSessions
	store       *memory.SecretChatStore
	queue       *memory.EncryptedQueueStore
	admin       domain.User
	participant domain.User
}

const (
	encAdminSession = int64(301)
	encPartSession  = int64(302)
)

var (
	encAdminAuthKey = [8]byte{1, 0, 0, 0, 0, 0, 0, 0}
	encPartAuthKey  = [8]byte{2, 0, 0, 0, 0, 0, 0, 0}
)

func newEncryptedFixture(t *testing.T) *encryptedFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	sessions := &phoneCaptureSessions{}
	secretStore := memory.NewSecretChatStore()
	queueStore := memory.NewEncryptedQueueStore()
	router := New(Config{}, Deps{
		Users:       appusers.NewService(userStore),
		SecretChats: appsecret.NewService(secretStore, queueStore, &seqSecretChatIDAllocator{}),
		Updates:     appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Files:       &fakeFiles{},
		Sessions:    sessions,
	}, zaptest.NewLogger(t), clock.System)
	f := &encryptedFixture{ctx: ctx, router: router, sessions: sessions, store: secretStore, queue: queueStore}
	mk := func(hash int64, phone, name string) domain.User {
		u, err := userStore.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		return u
	}
	f.admin = mk(5001, "13800000011", "Admin")
	f.participant = mk(5002, "13800000012", "Participant")
	return f
}

func (f *encryptedFixture) adminCtx() context.Context {
	return WithAuthKeyID(WithSessionID(WithUserID(f.ctx, f.admin.ID), encAdminSession), encAdminAuthKey)
}

func (f *encryptedFixture) participantCtx() context.Context {
	return WithAuthKeyID(WithSessionID(WithUserID(f.ctx, f.participant.ID), encPartSession), encPartAuthKey)
}

// encChatPayload 从捕获的推送里取出 updateEncryption 载荷。
func encChatPayload(t *testing.T, rec phonePushRecord) tg.EncryptedChatClass {
	t.Helper()
	updates, ok := rec.msg.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed msg = %T, want single-update tg.Updates", rec.msg)
	}
	upd, ok := updates.Updates[0].(*tg.UpdateEncryption)
	if !ok {
		t.Fatalf("pushed update = %T, want UpdateEncryption", updates.Updates[0])
	}
	return upd.Chat
}

func TestEncryptedChatRPCHappyPath(t *testing.T) {
	f := newEncryptedFixture(t)
	ga := dhParam(0x55)
	gb := dhParam(0x66)

	// --- requestEncryption（发起方） ---
	res, err := f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
		UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
		RandomID: 777,
		GA:       ga,
	})
	if err != nil {
		t.Fatalf("requestEncryption: %v", err)
	}
	waiting, ok := res.(*tg.EncryptedChatWaiting)
	if !ok {
		t.Fatalf("request response = %T, want *tg.EncryptedChatWaiting", res)
	}
	// 推送给接受方的 encryptedChatRequested（携 g_a）。
	recs := f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.participant.ID {
		t.Fatalf("request push = %+v, want single push to participant %d", recs, f.participant.ID)
	}
	requested, ok := encChatPayload(t, recs[0]).(*tg.EncryptedChatRequested)
	if !ok {
		t.Fatalf("participant payload = %T, want EncryptedChatRequested", encChatPayload(t, recs[0]))
	}
	if requested.ID != waiting.ID {
		t.Fatalf("chat id mismatch admin=%d participant=%d", waiting.ID, requested.ID)
	}
	if string(requested.GA) != string(ga) {
		t.Fatal("requested g_a not relayed verbatim")
	}

	chat, found, _ := f.store.GetSecretChat(f.ctx, waiting.ID)
	if !found {
		t.Fatal("chat not persisted")
	}

	// --- acceptEncryption（接受方） ---
	f.sessions.reset()
	const fp = int64(0x0123456789abcdef)
	accRes, err := f.router.onMessagesAcceptEncryption(f.participantCtx(), &tg.MessagesAcceptEncryptionRequest{
		Peer:           tg.InputEncryptedChat{ChatID: chat.ID, AccessHash: chat.ParticipantAccessHash},
		GB:             gb,
		KeyFingerprint: fp,
	})
	if err != nil {
		t.Fatalf("acceptEncryption: %v", err)
	}
	// 接受方同步响应：encryptedChat，GAOrB = g_a。
	partView, ok := accRes.(*tg.EncryptedChat)
	if !ok {
		t.Fatalf("accept response = %T, want *tg.EncryptedChat", accRes)
	}
	if string(partView.GAOrB) != string(ga) {
		t.Fatal("participant view GAOrB must be g_a")
	}
	if partView.KeyFingerprint != fp {
		t.Fatalf("key fingerprint = %x, want %x", partView.KeyFingerprint, fp)
	}
	// 推送给发起方：encryptedChat，GAOrB = g_b。
	recs = f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.admin.ID {
		t.Fatalf("accept push = %+v, want single push to admin %d", recs, f.admin.ID)
	}
	adminView, ok := encChatPayload(t, recs[0]).(*tg.EncryptedChat)
	if !ok {
		t.Fatalf("admin payload = %T, want EncryptedChat", encChatPayload(t, recs[0]))
	}
	if string(adminView.GAOrB) != string(gb) {
		t.Fatal("admin view GAOrB must be g_b")
	}
	if adminView.KeyFingerprint != fp {
		t.Fatal("admin view key fingerprint not relayed byte-for-byte")
	}

	// --- discardEncryption（发起方） ---
	f.sessions.reset()
	okRes, err := f.router.onMessagesDiscardEncryption(f.adminCtx(), &tg.MessagesDiscardEncryptionRequest{
		ChatID:        chat.ID,
		DeleteHistory: true,
	})
	if err != nil || !okRes {
		t.Fatalf("discardEncryption: ok=%v err=%v", okRes, err)
	}
	recs = f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.participant.ID {
		t.Fatalf("discard push = %+v, want single push to participant", recs)
	}
	discarded, ok := encChatPayload(t, recs[0]).(*tg.EncryptedChatDiscarded)
	if !ok || !discarded.HistoryDeleted {
		t.Fatalf("discard payload = %+v, want EncryptedChatDiscarded{HistoryDeleted:true}", encChatPayload(t, recs[0]))
	}
}

// TestDiscardSecretChatsForAuthKeyOnLogout 回归 P1：设备登出/授权撤销销毁其 perm auth_key 后，
// 必须级联 discard 该设备绑定的活跃密聊并向对端推送 encryptedChatDiscarded。修复前 onAuthLogOut
// 不处理密聊，对端继续往死 auth_key 投递成静默死链（消息 acked=f / qts 永久积压）。
func TestDiscardSecretChatsForAuthKeyOnLogout(t *testing.T) {
	f := newEncryptedFixture(t)
	ga := dhParam(0x55)
	gb := dhParam(0x66)

	// 建链到 normal：admin 发起 + participant 接受。
	res, err := f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
		UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
		RandomID: 11,
		GA:       ga,
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	chatID := res.(*tg.EncryptedChatWaiting).ID
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)
	if _, err := f.router.onMessagesAcceptEncryption(f.participantCtx(), &tg.MessagesAcceptEncryptionRequest{
		Peer:           tg.InputEncryptedChat{ChatID: chat.ID, AccessHash: chat.ParticipantAccessHash},
		GB:             gb,
		KeyFingerprint: 0x1234,
	}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if chat, _, _ := f.store.GetSecretChat(f.ctx, chatID); chat.State != domain.SecretChatStateNormal {
		t.Fatalf("pre-logout state=%s, want normal", chat.State)
	}

	// 模拟 participant 设备登出：级联 discard 其 perm auth_key 绑定的密聊。
	f.sessions.reset()
	bobAuthKey := businessAuthKeyInt64(encPartAuthKey)
	f.router.discardSecretChatsForAuthKey(f.ctx, bobAuthKey, f.participant.ID)

	// [1] 密聊已迁移到 discarded（不再是 normal 死链）。
	if chat, _, _ := f.store.GetSecretChat(f.ctx, chatID); chat.State != domain.SecretChatStateDiscarded {
		t.Fatalf("post-logout state=%s, want discarded", chat.State)
	}
	// [2] 对端 admin 收到单条 encryptedChatDiscarded 在线推送。
	recs := f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.admin.ID {
		t.Fatalf("discard push = %+v, want single push to admin %d", recs, f.admin.ID)
	}
	if _, ok := encChatPayload(t, recs[0]).(*tg.EncryptedChatDiscarded); !ok {
		t.Fatalf("peer payload = %T, want EncryptedChatDiscarded", encChatPayload(t, recs[0]))
	}
	// [3] durable 离线补偿事件写给对端设备（getDifference 兜底）。
	if events, err := f.queue.ListUndeliveredStateEvents(f.ctx, f.admin.ID, businessAuthKeyInt64(encAdminAuthKey), 10); err != nil || len(events) == 0 {
		t.Fatalf("durable discard event for admin = %d (err=%v), want >=1", len(events), err)
	}

	// [4] 幂等：再次对同 auth_key 登出不再 discard、不再推送（已是终态）。
	f.sessions.reset()
	f.router.discardSecretChatsForAuthKey(f.ctx, bobAuthKey, f.participant.ID)
	if recs := f.sessions.records(); len(recs) != 0 {
		t.Fatalf("idempotent re-logout pushed %d updates, want 0", len(recs))
	}
}

func TestRequestEncryptionSelf(t *testing.T) {
	f := newEncryptedFixture(t)
	_, err := f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
		UserID:   &tg.InputUser{UserID: f.admin.ID, AccessHash: f.admin.AccessHash},
		RandomID: 1,
		GA:       dhParam(0x55),
	})
	assertPhoneRPCErr(t, err, "USER_ID_INVALID")
}

func TestAcceptEncryptionWrongAccessHashRPC(t *testing.T) {
	f := newEncryptedFixture(t)
	res, err := f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
		UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
		RandomID: 9,
		GA:       dhParam(0x55),
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	chatID := res.(*tg.EncryptedChatWaiting).ID
	_, err = f.router.onMessagesAcceptEncryption(f.participantCtx(), &tg.MessagesAcceptEncryptionRequest{
		Peer:           tg.InputEncryptedChat{ChatID: chatID, AccessHash: 999999},
		GB:             dhParam(0x66),
		KeyFingerprint: 1,
	})
	assertPhoneRPCErr(t, err, "CHAT_ID_INVALID")
}
