package secretchat

import (
	"context"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// validGA returns a 256-byte g_a that falls within the valid DH range (first byte 0x55 ≈
// 2^2046, both > 2^1984 and < p≈0xc7..).
func validGA() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = 0x42
	}
	b[0] = 0x55
	return b
}

func newTestService() (*Service, *memory.SecretChatStore) {
	st := memory.NewSecretChatStore()
	return NewService(st, memory.NewEncryptedQueueStore()), st
}

const (
	adminUser    = int64(1001)
	partUser     = int64(2002)
	adminAuthKey = int64(0x1111)
	partAuthKey  = int64(0x2222)
	otherAuthKey = int64(0x3333)
	keyFP        = int64(0x0123456789abcdef)
)

func requestFixture() domain.SecretChatRequest {
	return domain.SecretChatRequest{
		AdminUserID:       adminUser,
		AdminAuthKeyID:    adminAuthKey,
		ParticipantUserID: partUser,
		RandomID:          12345,
		GA:                validGA(),
		Date:              1000,
	}
}

func TestRequestEncryption(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, err := svc.RequestEncryption(ctx, requestFixture())
	if err != nil {
		t.Fatalf("RequestEncryption: %v", err)
	}
	if chat.ID != int(requestFixture().RandomID) {
		t.Fatalf("chat id = %d, want request random_id %d", chat.ID, requestFixture().RandomID)
	}
	if chat.State != domain.SecretChatStateRequested {
		t.Fatalf("state = %q, want requested", chat.State)
	}
	if len(chat.GA) != dhPubSize {
		t.Fatalf("g_a length = %d, want %d (left-padded)", len(chat.GA), dhPubSize)
	}
	if chat.AdminAccessHash == 0 || chat.ParticipantAccessHash == 0 {
		t.Fatal("access hashes must be non-zero")
	}
	if chat.AdminAccessHash == chat.ParticipantAccessHash {
		t.Fatal("admin/participant access hashes must differ (per-viewer)")
	}
	if chat.ParticipantAuthKeyID != 0 {
		t.Fatalf("participant auth key must be unbound before accept, got %d", chat.ParticipantAuthKeyID)
	}
}

func TestRequestEncryptionIdempotent(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	first, err := svc.RequestEncryption(ctx, requestFixture())
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, err := svc.RequestEncryption(ctx, requestFixture())
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent re-request must return same chat: %d vs %d", first.ID, second.ID)
	}
}

func TestRequestEncryptionConcurrentExactRetry(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	results := make([]domain.SecretChat, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.RequestEncryption(ctx, requestFixture())
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent request %d: %v", i, err)
		}
	}
	if results[0].ID != int(requestFixture().RandomID) || results[1].ID != results[0].ID ||
		results[1].AdminAccessHash != results[0].AdminAccessHash ||
		results[1].ParticipantAccessHash != results[0].ParticipantAccessHash {
		t.Fatalf("concurrent exact retry diverged: first=%+v second=%+v", results[0], results[1])
	}
}

func TestRequestEncryptionPreservesNegativeRandomID(t *testing.T) {
	svc, _ := newTestService()
	req := requestFixture()
	req.RandomID = -12345
	chat, err := svc.RequestEncryption(context.Background(), req)
	if err != nil {
		t.Fatalf("request negative random_id: %v", err)
	}
	if chat.ID != int(req.RandomID) || chat.RandomID != req.RandomID {
		t.Fatalf("chat id/random_id = %d/%d, want %d", chat.ID, chat.RandomID, req.RandomID)
	}
}

func TestRequestEncryptionRejectsChangedIntentAndGlobalCollision(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	if _, err := svc.RequestEncryption(ctx, requestFixture()); err != nil {
		t.Fatalf("first request: %v", err)
	}

	changedPeer := requestFixture()
	changedPeer.ParticipantUserID++
	if _, err := svc.RequestEncryption(ctx, changedPeer); !errors.Is(err, domain.ErrSecretChatRandomIDDuplicate) {
		t.Fatalf("changed peer err = %v, want ErrSecretChatRandomIDDuplicate", err)
	}

	changedGA := requestFixture()
	changedGA.GA = validGA()
	changedGA.GA[1] ^= 0x01
	if _, err := svc.RequestEncryption(ctx, changedGA); !errors.Is(err, domain.ErrSecretChatRandomIDDuplicate) {
		t.Fatalf("changed g_a err = %v, want ErrSecretChatRandomIDDuplicate", err)
	}

	otherAuthKey := requestFixture()
	otherAuthKey.AdminUserID++
	otherAuthKey.AdminAuthKeyID++
	if _, err := svc.RequestEncryption(ctx, otherAuthKey); !errors.Is(err, domain.ErrSecretChatRandomIDDuplicate) {
		t.Fatalf("global collision err = %v, want ErrSecretChatRandomIDDuplicate", err)
	}
}

func TestRequestEncryptionRejectsZeroAndDiscardedReuse(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	zero := requestFixture()
	zero.RandomID = 0
	if _, err := svc.RequestEncryption(ctx, zero); !errors.Is(err, domain.ErrSecretChatRandomIDDuplicate) {
		t.Fatalf("zero random_id err = %v, want ErrSecretChatRandomIDDuplicate", err)
	}

	chat, err := svc.RequestEncryption(ctx, requestFixture())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, _, err := svc.DiscardEncryption(ctx, chat.ID, adminUser, adminAuthKey, true); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := svc.RequestEncryption(ctx, requestFixture()); !errors.Is(err, domain.ErrSecretChatRandomIDDuplicate) {
		t.Fatalf("discarded reuse err = %v, want ErrSecretChatRandomIDDuplicate", err)
	}
}

func TestRequestEncryptionInvalidGA(t *testing.T) {
	svc, _ := newTestService()
	req := requestFixture()
	req.GA = []byte{0x01} // value 1 → not in (1, p-1)
	if _, err := svc.RequestEncryption(context.Background(), req); !errors.Is(err, ErrGAInvalid) {
		t.Fatalf("err = %v, want ErrGAInvalid", err)
	}
}

func TestAcceptEncryption(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, err := svc.RequestEncryption(ctx, requestFixture())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	accepted, err := svc.AcceptEncryption(ctx, chat.ID, partUser, partAuthKey, chat.ParticipantAccessHash, validGA(), keyFP)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if accepted.State != domain.SecretChatStateNormal {
		t.Fatalf("state = %q, want normal", accepted.State)
	}
	if accepted.KeyFingerprint != keyFP {
		t.Fatalf("key fingerprint not relayed byte-for-byte: got %x want %x", accepted.KeyFingerprint, keyFP)
	}
	if accepted.ParticipantAuthKeyID != partAuthKey {
		t.Fatalf("participant auth key not bound: %d", accepted.ParticipantAuthKeyID)
	}
	if len(accepted.GB) != dhPubSize {
		t.Fatalf("g_b length = %d, want %d", len(accepted.GB), dhPubSize)
	}
}

func TestAcceptEncryptionWrongAccessHash(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, _ := svc.RequestEncryption(ctx, requestFixture())
	_, err := svc.AcceptEncryption(ctx, chat.ID, partUser, partAuthKey, chat.ParticipantAccessHash+1, validGA(), keyFP)
	if !errors.Is(err, domain.ErrSecretChatNotFound) {
		t.Fatalf("err = %v, want ErrSecretChatNotFound", err)
	}
}

func TestAcceptEncryptionWrongUser(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, _ := svc.RequestEncryption(ctx, requestFixture())
	// admin impersonates the recipient.
	_, err := svc.AcceptEncryption(ctx, chat.ID, adminUser, adminAuthKey, chat.ParticipantAccessHash, validGA(), keyFP)
	if !errors.Is(err, domain.ErrSecretChatNotFound) {
		t.Fatalf("err = %v, want ErrSecretChatNotFound", err)
	}
}

func TestAcceptEncryptionDoubleAccept(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, _ := svc.RequestEncryption(ctx, requestFixture())
	if _, err := svc.AcceptEncryption(ctx, chat.ID, partUser, partAuthKey, chat.ParticipantAccessHash, validGA(), keyFP); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	// A second device accepts: CAS misses → ENCRYPTION_ALREADY_ACCEPTED.
	_, err := svc.AcceptEncryption(ctx, chat.ID, partUser, int64(0x3333), chat.ParticipantAccessHash, validGA(), keyFP)
	if !errors.Is(err, domain.ErrSecretChatAlreadyAccepted) {
		t.Fatalf("err = %v, want ErrSecretChatAlreadyAccepted", err)
	}
}

func TestAcceptEncryptionConcurrentDevicesSingleWinner(t *testing.T) {
	svc, st := newTestService()
	ctx := context.Background()
	chat, err := svc.RequestEncryption(ctx, requestFixture())
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	authKeys := []int64{partAuthKey, otherAuthKey}
	errs := make([]error, len(authKeys))
	var wg sync.WaitGroup
	for i, authKeyID := range authKeys {
		wg.Add(1)
		go func(i int, authKeyID int64) {
			defer wg.Done()
			_, errs[i] = svc.AcceptEncryption(ctx, chat.ID, partUser, authKeyID, chat.ParticipantAccessHash, validGA(), keyFP)
		}(i, authKeyID)
	}
	wg.Wait()

	winners := 0
	losers := 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, domain.ErrSecretChatAlreadyAccepted):
			losers++
		default:
			t.Fatalf("concurrent accept err = %v", err)
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("concurrent accepts winners=%d losers=%d, want 1/1", winners, losers)
	}

	stored, ok, err := st.GetSecretChat(ctx, chat.ID)
	if err != nil || !ok {
		t.Fatalf("get accepted chat: ok=%v err=%v", ok, err)
	}
	if stored.State != domain.SecretChatStateNormal ||
		(stored.ParticipantAuthKeyID != partAuthKey && stored.ParticipantAuthKeyID != otherAuthKey) {
		t.Fatalf("accepted chat = %+v, want normal bound to one participant device", stored)
	}
	loserAuthKeyID := partAuthKey
	if stored.ParticipantAuthKeyID == partAuthKey {
		loserAuthKeyID = otherAuthKey
	}
	if _, _, err := svc.DiscardEncryption(ctx, chat.ID, partUser, loserAuthKeyID, true); !errors.Is(err, domain.ErrSecretChatNotFound) {
		t.Fatalf("loser discard err = %v, want ErrSecretChatNotFound", err)
	}
	stored, ok, err = st.GetSecretChat(ctx, chat.ID)
	if err != nil || !ok || stored.State != domain.SecretChatStateNormal {
		t.Fatalf("chat after loser discard = %+v ok=%v err=%v, want normal", stored, ok, err)
	}
}

func TestAcceptEncryptionInvalidGB(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, _ := svc.RequestEncryption(ctx, requestFixture())
	_, err := svc.AcceptEncryption(ctx, chat.ID, partUser, partAuthKey, chat.ParticipantAccessHash, []byte{0x01}, keyFP)
	if !errors.Is(err, ErrGAInvalid) {
		t.Fatalf("err = %v, want ErrGAInvalid", err)
	}
}

func TestDiscardEncryption(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, _ := svc.RequestEncryption(ctx, requestFixture())
	got, already, err := svc.DiscardEncryption(ctx, chat.ID, adminUser, adminAuthKey, true)
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if already {
		t.Fatal("first discard must not report already")
	}
	if got.State != domain.SecretChatStateDiscarded || !got.HistoryDeleted {
		t.Fatalf("discarded chat = %+v", got)
	}
	// Idempotent: discarding again returns already=true.
	_, already, err = svc.DiscardEncryption(ctx, chat.ID, partUser, partAuthKey, false)
	if err != nil || !already {
		t.Fatalf("idempotent discard: already=%v err=%v", already, err)
	}
}

func TestDiscardEncryptionNonParticipant(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, _ := svc.RequestEncryption(ctx, requestFixture())
	_, _, err := svc.DiscardEncryption(ctx, chat.ID, int64(9999), int64(9999), false)
	if !errors.Is(err, domain.ErrSecretChatNotFound) {
		t.Fatalf("err = %v, want ErrSecretChatNotFound", err)
	}
}

// acceptedChat runs request→accept and returns a normal-state secret chat.
func acceptedChat(t *testing.T, svc *Service) domain.SecretChat {
	t.Helper()
	ctx := context.Background()
	chat, err := svc.RequestEncryption(ctx, requestFixture())
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	accepted, err := svc.AcceptEncryption(ctx, chat.ID, partUser, partAuthKey, chat.ParticipantAccessHash, dhParamGB(), keyFP)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return accepted
}

func dhParamGB() []byte {
	b := make([]byte, 256)
	for i := range b {
		b[i] = 0x42
	}
	b[0] = 0x66
	return b
}

func TestSendEncryptedQtsAllocation(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat := acceptedChat(t, svc)

	// admin sends → delivered to the participant device (partAuthKey), qts starts at 1.
	_, m1, err := svc.SendEncrypted(ctx, chat.ID, adminUser, adminAuthKey, chat.AdminAccessHash, domain.SecretMessageDelivery{RandomID: 111, Bytes: []byte{1, 2, 3}, Date: 2000})
	if err != nil {
		t.Fatalf("send 1: %v", err)
	}
	if m1.Qts != 1 || m1.ReceiverAuthKeyID != partAuthKey || m1.ReceiverUserID != partUser {
		t.Fatalf("msg1 = %+v (want qts=1, receiver=participant device)", m1)
	}
	_, m2, err := svc.SendEncrypted(ctx, chat.ID, adminUser, adminAuthKey, chat.AdminAccessHash, domain.SecretMessageDelivery{RandomID: 222, Bytes: []byte{4}, Date: 2001})
	if err != nil || m2.Qts != 2 {
		t.Fatalf("msg2 qts = %d err=%v, want 2", m2.Qts, err)
	}

	// Idempotent resend of the same random_id → returns the first qts/date, does not allocate a new qts.
	_, dup, err := svc.SendEncrypted(ctx, chat.ID, adminUser, adminAuthKey, chat.AdminAccessHash, domain.SecretMessageDelivery{RandomID: 111, Bytes: []byte{1, 2, 3}, Date: 9999})
	if err != nil {
		t.Fatalf("dup send: %v", err)
	}
	if dup.Qts != 1 || dup.Date != 2000 {
		t.Fatalf("idempotent resend = %+v, want qts=1 date=2000 (first persisted values)", dup)
	}

	// participant sends → delivered to the admin device (adminAuthKey), an independent qts sequence starting at 1.
	_, pm, err := svc.SendEncrypted(ctx, chat.ID, partUser, partAuthKey, chat.ParticipantAccessHash, domain.SecretMessageDelivery{RandomID: 333, Bytes: []byte{9}, Date: 2002})
	if err != nil {
		t.Fatalf("participant send: %v", err)
	}
	if pm.Qts != 1 || pm.ReceiverAuthKeyID != adminAuthKey || pm.ReceiverUserID != adminUser {
		t.Fatalf("participant msg = %+v (want qts=1, receiver=admin device)", pm)
	}
}

func TestSendEncryptedWrongAccessHash(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat := acceptedChat(t, svc)
	_, _, err := svc.SendEncrypted(ctx, chat.ID, adminUser, adminAuthKey, chat.AdminAccessHash+1, domain.SecretMessageDelivery{RandomID: 1, Bytes: []byte{1}, Date: 2000})
	if !errors.Is(err, domain.ErrSecretChatNotFound) {
		t.Fatalf("err = %v, want ErrSecretChatNotFound", err)
	}
}

func TestSendEncryptedRejectsUnboundAccountDevice(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat := acceptedChat(t, svc)

	for _, tc := range []struct {
		name       string
		userID     int64
		accessHash int64
	}{
		{name: "admin", userID: adminUser, accessHash: chat.AdminAccessHash},
		{name: "participant", userID: partUser, accessHash: chat.ParticipantAccessHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := svc.SendEncrypted(ctx, chat.ID, tc.userID, otherAuthKey, tc.accessHash, domain.SecretMessageDelivery{
				RandomID: 991, Bytes: []byte{1}, Date: 2000,
			})
			if !errors.Is(err, domain.ErrSecretChatNotFound) {
				t.Fatalf("err = %v, want ErrSecretChatNotFound", err)
			}
		})
	}
}

func TestDiscardEncryptionRejectsUnboundAccountDeviceAfterAccept(t *testing.T) {
	svc, st := newTestService()
	ctx := context.Background()
	chat := acceptedChat(t, svc)

	if _, _, err := svc.DiscardEncryption(ctx, chat.ID, partUser, otherAuthKey, true); !errors.Is(err, domain.ErrSecretChatNotFound) {
		t.Fatalf("unbound discard err = %v, want ErrSecretChatNotFound", err)
	}
	stored, ok, err := st.GetSecretChat(ctx, chat.ID)
	if err != nil || !ok || stored.State != domain.SecretChatStateNormal {
		t.Fatalf("chat after rejected discard = %+v ok=%v err=%v", stored, ok, err)
	}
}

func TestSendEncryptedNonNormal(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, _ := svc.RequestEncryption(ctx, requestFixture()) // requested, not yet accepted
	_, _, err := svc.SendEncrypted(ctx, chat.ID, adminUser, adminAuthKey, chat.AdminAccessHash, domain.SecretMessageDelivery{RandomID: 1, Bytes: []byte{1}, Date: 2000})
	if !errors.Is(err, domain.ErrSecretChatNotFound) {
		t.Fatalf("err = %v, want ErrSecretChatNotFound (cannot send before fully formed)", err)
	}
}

func TestListNewMessagesAndAck(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat := acceptedChat(t, svc)
	for i := 0; i < 3; i++ {
		if _, _, err := svc.SendEncrypted(ctx, chat.ID, adminUser, adminAuthKey, chat.AdminAccessHash, domain.SecretMessageDelivery{RandomID: int64(1000 + i), Bytes: []byte{byte(i)}, Date: 2000 + i}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	// Receiving device (participant) catches up: all 3 with qts>0.
	msgs, err := svc.ListNewMessages(ctx, partAuthKey, 0, 0)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("list since 0 = %d msgs err=%v, want 3", len(msgs), err)
	}
	if msgs[0].Qts != 1 || msgs[2].Qts != 3 {
		t.Fatalf("qts sequence broken: %d..%d", msgs[0].Qts, msgs[2].Qts)
	}
	// qts>1 → 2 remaining.
	msgs, _ = svc.ListNewMessages(ctx, partAuthKey, 1, 0)
	if len(msgs) != 2 || msgs[0].Qts != 2 {
		t.Fatalf("list since 1 = %+v, want qts 2,3", msgs)
	}
	// reserved qts = 3.
	if q, _ := svc.DeviceReservedQts(ctx, partAuthKey); q != 3 {
		t.Fatalf("reserved qts = %d, want 3", q)
	}
	// ack up to 3: no error (confirmed advances).
	if err := svc.AckQueue(ctx, partAuthKey, 3); err != nil {
		t.Fatalf("ack: %v", err)
	}
	// A device not involved has qts=0.
	if q, _ := svc.DeviceReservedQts(ctx, int64(0xDEAD)); q != 0 {
		t.Fatalf("unrelated device reserved qts = %d, want 0", q)
	}
}

func TestAcceptAfterDiscard(t *testing.T) {
	svc, _ := newTestService()
	ctx := context.Background()
	chat, _ := svc.RequestEncryption(ctx, requestFixture())
	if _, _, err := svc.DiscardEncryption(ctx, chat.ID, adminUser, adminAuthKey, false); err != nil {
		t.Fatalf("discard: %v", err)
	}
	_, err := svc.AcceptEncryption(ctx, chat.ID, partUser, partAuthKey, chat.ParticipantAccessHash, validGA(), keyFP)
	if !errors.Is(err, domain.ErrSecretChatAlreadyDeclined) {
		t.Fatalf("err = %v, want ErrSecretChatAlreadyDeclined", err)
	}
}
