package rpc

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	appphone "telesrv/internal/app/phone"
	appsecret "telesrv/internal/app/secretchat"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

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
	encAdminSession     = int64(301)
	encPartSession      = int64(302)
	encPartOtherSession = int64(303)
)

var (
	encAdminAuthKey     = [8]byte{1, 0, 0, 0, 0, 0, 0, 0}
	encPartAuthKey      = [8]byte{2, 0, 0, 0, 0, 0, 0, 0}
	encPartOtherAuthKey = [8]byte{3, 0, 0, 0, 0, 0, 0, 0}
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
		SecretChats: appsecret.NewService(secretStore, queueStore),
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

func (f *encryptedFixture) participantOtherCtx() context.Context {
	return WithAuthKeyID(WithSessionID(WithUserID(f.ctx, f.participant.ID), encPartOtherSession), encPartOtherAuthKey)
}

// encChatPayload extracts the updateEncryption payload from a captured push.
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

func secretChatDHFixture(t *testing.T, wantNegativeFingerprint bool) (ga, gb []byte, fingerprint int64) {
	t.Helper()
	prime := new(big.Int).SetBytes(appphone.DHPrime())
	generator := big.NewInt(int64(appphone.DHG))
	privateA := new(big.Int).SetBytes(make([]byte, 256))
	privateA.SetBit(privateA, 2046, 1)
	privateA.Add(privateA, big.NewInt(0x12345))
	gaInt := new(big.Int).Exp(generator, privateA, prime)
	ga = gaInt.Bytes()

	for n := int64(1); n < 128; n++ {
		privateB := new(big.Int).SetBit(new(big.Int), 2045, 1)
		privateB.Add(privateB, big.NewInt(0x54321+n))
		gbInt := new(big.Int).Exp(generator, privateB, prime)
		sharedA := new(big.Int).Exp(gbInt, privateA, prime)
		sharedB := new(big.Int).Exp(gaInt, privateB, prime)
		if sharedA.Cmp(sharedB) != 0 {
			t.Fatal("DH fixture derived different shared keys")
		}
		key := make([]byte, 256)
		sharedBytes := sharedA.Bytes()
		copy(key[len(key)-len(sharedBytes):], sharedBytes)
		digest := sha1.Sum(key)
		fingerprint = int64(binary.LittleEndian.Uint64(digest[12:20]))
		if (fingerprint < 0) == wantNegativeFingerprint {
			return ga, gbInt.Bytes(), fingerprint
		}
	}
	t.Fatalf("could not generate DH fixture with negative=%v fingerprint", wantNegativeFingerprint)
	return nil, nil, 0
}

func TestEncryptedChatRealDHHandshakeAcrossExactLayers(t *testing.T) {
	for _, negative := range []bool{false, true} {
		name := "positive_fingerprint"
		if negative {
			name = "negative_fingerprint"
		}
		t.Run(name, func(t *testing.T) {
			f := newEncryptedFixture(t)
			ga, gb, fingerprint := secretChatDHFixture(t, negative)
			waitingClass, err := f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
				UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
				RandomID: 909,
				GA:       ga,
			})
			if err != nil {
				t.Fatalf("requestEncryption: %v", err)
			}
			waiting := waitingClass.(*tg.EncryptedChatWaiting)
			if waiting.ID != 909 {
				t.Fatalf("waiting id = %d, want request random_id 909", waiting.ID)
			}
			chat, ok, err := f.store.GetSecretChat(f.ctx, waiting.ID)
			if err != nil || !ok {
				t.Fatalf("stored requested chat: ok=%v err=%v", ok, err)
			}

			f.sessions.reset()
			if _, err := f.router.onMessagesAcceptEncryption(f.participantCtx(), &tg.MessagesAcceptEncryptionRequest{
				Peer:           tg.InputEncryptedChat{ChatID: chat.ID, AccessHash: chat.ParticipantAccessHash},
				GB:             gb,
				KeyFingerprint: fingerprint,
			}); err != nil {
				t.Fatalf("acceptEncryption: %v", err)
			}

			var adminUpdates *tg.Updates
			for _, rec := range f.sessions.records() {
				if rec.userID == f.admin.ID {
					adminUpdates = rec.msg.(*tg.Updates)
					break
				}
			}
			if adminUpdates == nil {
				t.Fatal("missing accepted update for the initiating device")
			}

			for _, profile := range []tlprofile.Profile{
				tlprofile.Profile225,
				tlprofile.Profile226,
				tlprofile.Profile227,
				tlprofile.Profile228,
			} {
				var body bin.Buffer
				if err := tlprofile.EncodeObject(profile, adminUpdates, &body); err != nil {
					t.Fatalf("encode accepted update for profile %d: %v", profile, err)
				}
				decoded, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: body.Buf}, tlprofile.Limits{})
				if err != nil {
					t.Fatalf("decode accepted update for profile %d: %v", profile, err)
				}
				updates, ok := decoded.(*tg.Updates)
				if !ok || len(updates.Updates) != 1 {
					t.Fatalf("profile %d decoded update = %T", profile, decoded)
				}
				encUpdate, ok := updates.Updates[0].(*tg.UpdateEncryption)
				if !ok {
					t.Fatalf("profile %d nested update = %T", profile, updates.Updates[0])
				}
				accepted, ok := encUpdate.Chat.(*tg.EncryptedChat)
				if !ok {
					t.Fatalf("profile %d chat = %T", profile, encUpdate.Chat)
				}
				if new(big.Int).SetBytes(accepted.GAOrB).Cmp(new(big.Int).SetBytes(gb)) != 0 {
					t.Fatalf("profile %d changed g_b", profile)
				}
				if accepted.KeyFingerprint != fingerprint {
					t.Fatalf("profile %d fingerprint = %d, want %d", profile, accepted.KeyFingerprint, fingerprint)
				}
			}
		})
	}
}

func TestEncryptedChatRPCHappyPath(t *testing.T) {
	f := newEncryptedFixture(t)
	ga := dhParam(0x55)
	gb := dhParam(0x66)

	// --- requestEncryption (initiator) ---
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
	if waiting.ID != 777 {
		t.Fatalf("waiting id = %d, want request random_id 777", waiting.ID)
	}
	// The encryptedChatRequested (carries g_a) pushed to the acceptor.
	recs := f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.participant.ID {
		t.Fatalf("request push = %+v, want single push to participant %d", recs, f.participant.ID)
	}
	requested, ok := encChatPayload(t, recs[0]).(*tg.EncryptedChatRequested)
	if !ok {
		t.Fatalf("participant payload = %T, want EncryptedChatRequested", encChatPayload(t, recs[0]))
	}
	if requested.ID != 777 {
		t.Fatalf("participant requested id = %d, want request random_id 777", requested.ID)
	}
	if string(requested.GA) != string(ga) {
		t.Fatal("requested g_a not relayed verbatim")
	}

	chat, found, _ := f.store.GetSecretChat(f.ctx, waiting.ID)
	if !found {
		t.Fatal("chat not persisted")
	}

	// --- acceptEncryption (acceptor) ---
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
	// Acceptor's synchronous response: encryptedChat, GAOrB = g_a.
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
	// Target-push encryptedChat to the initiating device and converge the participant's other devices to discarded.
	recs = f.sessions.records()
	if len(recs) != 2 {
		t.Fatalf("accept pushes = %+v, want admin accepted + participant loser discarded", recs)
	}
	var adminRec, loserRec *phonePushRecord
	for i := range recs {
		switch recs[i].userID {
		case f.admin.ID:
			adminRec = &recs[i]
		case f.participant.ID:
			loserRec = &recs[i]
		}
	}
	if adminRec == nil || adminRec.rawAuthKeyID != encAdminAuthKey {
		t.Fatalf("admin accept push = %+v, want target auth key %x", adminRec, encAdminAuthKey)
	}
	if loserRec == nil || loserRec.rawAuthKeyID != encPartAuthKey {
		t.Fatalf("loser discard push = %+v, want exclusion auth key %x", loserRec, encPartAuthKey)
	}
	adminView, ok := encChatPayload(t, *adminRec).(*tg.EncryptedChat)
	if !ok {
		t.Fatalf("admin payload = %T, want EncryptedChat", encChatPayload(t, *adminRec))
	}
	if string(adminView.GAOrB) != string(gb) {
		t.Fatal("admin view GAOrB must be g_b")
	}
	if adminView.KeyFingerprint != fp {
		t.Fatal("admin view key fingerprint not relayed byte-for-byte")
	}
	if discarded, ok := encChatPayload(t, *loserRec).(*tg.EncryptedChatDiscarded); !ok || !discarded.HistoryDeleted {
		t.Fatalf("loser payload = %+v, want history-deleting EncryptedChatDiscarded", encChatPayload(t, *loserRec))
	}

	// --- discardEncryption (initiator) ---
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

// TestDiscardSecretChatsForAuthKeyOnLogout regresses P1: after a device logout /
// authorization revocation destroys its perm auth_key, it must cascade-discard the
// active secret chats bound to that device and push encryptedChatDiscarded to the
// peer. Before the fix, onAuthLogOut did not handle secret chats, and the peer kept
// delivering to the dead auth_key, becoming a silent dead link (messages acked=f /
// qts backlogging forever).
func TestDiscardSecretChatsForAuthKeyOnLogout(t *testing.T) {
	f := newEncryptedFixture(t)
	ga := dhParam(0x55)
	gb := dhParam(0x66)

	// Establish the link to normal: admin initiates + participant accepts.
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

	// Simulate the participant device logging out: cascade-discard the secret chats bound to its perm auth_key.
	f.sessions.reset()
	bobAuthKey := businessAuthKeyInt64(encPartAuthKey)
	f.router.discardSecretChatsForAuthKey(f.ctx, bobAuthKey, f.participant.ID)

	// [1] The secret chat has migrated to discarded (no longer a normal dead link).
	if chat, _, _ := f.store.GetSecretChat(f.ctx, chatID); chat.State != domain.SecretChatStateDiscarded {
		t.Fatalf("post-logout state=%s, want discarded", chat.State)
	}
	// [2] The peer admin receives a single online encryptedChatDiscarded push.
	recs := f.sessions.records()
	if len(recs) != 1 || recs[0].userID != f.admin.ID {
		t.Fatalf("discard push = %+v, want single push to admin %d", recs, f.admin.ID)
	}
	if _, ok := encChatPayload(t, recs[0]).(*tg.EncryptedChatDiscarded); !ok {
		t.Fatalf("peer payload = %T, want EncryptedChatDiscarded", encChatPayload(t, recs[0]))
	}
	// [3] The durable offline compensation event is written for the peer device (getDifference fallback).
	if events, err := f.queue.ListUndeliveredStateEvents(f.ctx, f.admin.ID, businessAuthKeyInt64(encAdminAuthKey), 10); err != nil || len(events) == 0 {
		t.Fatalf("durable discard event for admin = %d (err=%v), want >=1", len(events), err)
	}

	// [4] Idempotent: logging out the same auth_key again discards and pushes nothing more (already terminal).
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

func TestRequestEncryptionRandomIDContractRPC(t *testing.T) {
	f := newEncryptedFixture(t)
	request := func(randomID int) (tg.EncryptedChatClass, error) {
		return f.router.onMessagesRequestEncryption(f.adminCtx(), &tg.MessagesRequestEncryptionRequest{
			UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
			RandomID: randomID,
			GA:       dhParam(0x55),
		})
	}

	negative, err := request(-808)
	if err != nil {
		t.Fatalf("negative random_id: %v", err)
	}
	if got := negative.(*tg.EncryptedChatWaiting).ID; got != -808 {
		t.Fatalf("negative waiting id = %d, want -808", got)
	}
	negativeChat, found, err := f.store.GetSecretChat(f.ctx, -808)
	if err != nil || !found {
		t.Fatalf("stored negative chat: found=%v err=%v", found, err)
	}
	accepted, err := f.router.onMessagesAcceptEncryption(f.participantCtx(), &tg.MessagesAcceptEncryptionRequest{
		Peer:           tg.InputEncryptedChat{ChatID: -808, AccessHash: negativeChat.ParticipantAccessHash},
		GB:             dhParam(0x66),
		KeyFingerprint: 0x1234,
	})
	if err != nil {
		t.Fatalf("accept negative chat id: %v", err)
	}
	if got := accepted.(*tg.EncryptedChat).ID; got != -808 {
		t.Fatalf("accepted id = %d, want -808", got)
	}

	if _, err := request(0); err == nil {
		t.Fatal("zero random_id succeeded, want RANDOM_ID_DUPLICATE")
	} else {
		assertPhoneRPCErr(t, err, "RANDOM_ID_DUPLICATE")
	}

	// Changing the handshake intent for the same global chat_id must not be silently swallowed by idempotency.
	changed := &tg.MessagesRequestEncryptionRequest{
		UserID:   &tg.InputUser{UserID: f.participant.ID, AccessHash: f.participant.AccessHash},
		RandomID: -808,
		GA:       dhParam(0x66),
	}
	if _, err := f.router.onMessagesRequestEncryption(f.adminCtx(), changed); err == nil {
		t.Fatal("changed intent succeeded, want RANDOM_ID_DUPLICATE")
	} else {
		assertPhoneRPCErr(t, err, "RANDOM_ID_DUPLICATE")
	}
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
