package mtprotoedge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/tg"

	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type activationGatedAuthKeyStore struct {
	store.AuthKeyStore
	gets         atomic.Int32
	finalStarted chan struct{}
	finalRelease chan struct{}
	startOnce    sync.Once
}

func (s *activationGatedAuthKeyStore) Get(ctx context.Context, id [8]byte) (store.AuthKeyData, bool, error) {
	if s.gets.Add(1) == 2 {
		s.startOnce.Do(func() { close(s.finalStarted) })
		select {
		case <-s.finalRelease:
		case <-ctx.Done():
			return store.AuthKeyData{}, false, ctx.Err()
		}
	}
	return s.AuthKeyStore.Get(ctx, id)
}

func waitForManagedSessionAbsent(t *testing.T, manager *SessionManager, key sessionKey) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		manager.mu.RLock()
		claim, active := manager.claims[key], manager.bySession[key]
		manager.mu.RUnlock()
		if claim == nil && active == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed session survived terminal rejection: claim=%p active=%p", claim, active)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBadSaltStormRevalidatesStoreOnlyAtActivationBoundary(t *testing.T) {
	const dc = 2
	keys := &countingAuthKeyStore{AuthKeyStore: memory.NewAuthKeyStore()}
	handler := &admissionCountingRPC{}
	addr, pub, _ := startTestServer(t, Options{DC: dc, AuthKeys: keys, RPC: handler})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)
	ids := proto.NewMessageIDGen(time.Now)
	firstID := ids.New(proto.MessageFromClient)

	const wrongFrames = 16
	for i := 0; i < wrongFrames; i++ {
		msgID := firstID
		if i != 0 {
			msgID = ids.New(proto.MessageFromClient)
		}
		sendEncryptedWithSalt(t, conn, cipher, auth, auth.ServerSalt+1, msgID, &tg.HelpGetConfigRequest{})
		_, typeID, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
		if typeID != mt.BadServerSaltTypeID {
			t.Fatalf("correction %d type = %#x", i, typeID)
		}
	}
	if got := keys.gets.Load(); got != 1 {
		t.Fatalf("AuthKeyStore.Get during bad-salt storm = %d, want initial lookup only", got)
	}

	sendEncrypted(t, conn, cipher, auth, firstID, &tg.HelpGetConfigRequest{})
	collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{proto.ResultTypeID: 1})
	if got := keys.gets.Load(); got != 2 {
		t.Fatalf("AuthKeyStore.Get after activation boundary = %d, want 2", got)
	}
	waitForAtomicCalls(t, &handler.calls, 1)
}

func TestActivationFinalAuthKeyCheckRunsAfterClaim(t *testing.T) {
	const dc = 2
	base := memory.NewAuthKeyStore()
	keys := &activationGatedAuthKeyStore{
		AuthKeyStore: base,
		finalStarted: make(chan struct{}),
		finalRelease: make(chan struct{}),
	}
	defer func() {
		select {
		case <-keys.finalRelease:
		default:
			close(keys.finalRelease)
		}
	}()
	handler := &admissionCountingRPC{}
	addr, pub, srv := startTestServer(t, Options{DC: dc, AuthKeys: keys, RPC: handler})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)
	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)

	// The first Get is serveConn's decrypt lookup. The second is deliberately
	// blocked: it must start only after BeginActivation indexed the claim.
	sendEncrypted(t, conn, cipher, auth, msgID, &tg.HelpGetConfigRequest{})
	select {
	case <-keys.finalStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("activation final auth-key check did not start")
	}
	key := sessionKey{authKeyID: auth.AuthKey.ID, sessionID: auth.SessionID}
	srv.conns.mu.RLock()
	claim, active := srv.conns.claims[key], srv.conns.bySession[key]
	srv.conns.mu.RUnlock()
	if claim == nil || active != nil {
		t.Fatalf("final auth-key check not protected by claim: claim=%p active=%p", claim, active)
	}

	// Model Delete committing after the initial decrypt lookup but before the
	// activation check returns. The claimant must emit terminal -404, never publish
	// or dispatch the request, even before revocation fan-out gets the manager lock.
	if err := base.Delete(context.Background(), auth.AuthKey.ID); err != nil {
		t.Fatalf("delete auth key during activation: %v", err)
	}
	close(keys.finalRelease)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var response bin.Buffer
	err := conn.Recv(ctx, &response)
	var protocolErr *codec.ProtocolErr
	if !errors.As(err, &protocolErr) || protocolErr.Code != codec.CodeAuthKeyNotFound {
		t.Fatalf("revoked activation recv = %T %v, want protocol -404", err, err)
	}
	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("revoked activation executed %d RPCs", got)
	}
	waitForManagedSessionAbsent(t, srv.conns, key)
}

func TestBadSaltProvisionalCannotReactivateDeletedAuthKey(t *testing.T) {
	const dc = 2
	handler := &admissionCountingRPC{}
	addr, pub, srv := startTestServer(t, Options{DC: dc, RPC: handler})
	provisional, auth, cipher := dialHandshake(t, addr, dc, pub)
	ids := proto.NewMessageIDGen(time.Now)
	reqMsgID := ids.New(proto.MessageFromClient)

	// Socket A is retained as a bad-salt provisional and is intentionally absent
	// from SessionManager active/claim indexes.
	sendEncryptedWithSalt(t, provisional, cipher, auth, auth.ServerSalt+1, reqMsgID, &tg.HelpGetConfigRequest{})
	_, typeID, _ := readServerMessage(t, provisional, cipher, auth.AuthKey)
	if typeID != mt.BadServerSaltTypeID {
		t.Fatalf("provisional correction type = %#x, want bad_server_salt", typeID)
	}
	key := sessionKey{authKeyID: auth.AuthKey.ID, sessionID: auth.SessionID}
	srv.conns.mu.RLock()
	activeBefore, claimBefore := srv.conns.bySession[key], srv.conns.claims[key]
	srv.conns.mu.RUnlock()
	if activeBefore != nil || claimBefore != nil {
		t.Fatalf("bad-salt provisional leaked into manager: active=%p claim=%p", activeBefore, claimBefore)
	}

	// Socket B uses the same auth key with another session and deletes it. The
	// provisional is not manager-indexed, so correctness depends on its next-frame
	// AuthKeyStore recheck rather than fan-out close alone.
	destroyer := dialTransportOnly(t, addr)
	destroySessionID := auth.SessionID ^ 1
	destroyBody := encodeClientMessageBodyForTest(t, &destroyAuthKeyRequest{})
	sendEncryptedWithSessionSaltAndSeq(
		t, destroyer, cipher, auth, destroySessionID, auth.ServerSalt,
		ids.New(proto.MessageFromClient), 1, destroyBody,
	)
	destroyReplies := collectReplies(t, destroyer, cipher, auth.AuthKey, destroyAuthKeyOkTypeID)
	mustHave(t, destroyReplies, destroyAuthKeyOkTypeID, "destroy_auth_key_ok")
	if _, found, err := srv.authKeys.Get(context.Background(), auth.AuthKey.ID); err != nil || found {
		t.Fatalf("auth key after destroy: found=%v err=%v", found, err)
	}

	// A corrected resend must now receive terminal -404; it must not activate or
	// execute the previously rejected business request with its cached key.
	sendEncrypted(t, provisional, cipher, auth, reqMsgID, &tg.HelpGetConfigRequest{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var response bin.Buffer
	err := provisional.Recv(ctx, &response)
	var protocolErr *codec.ProtocolErr
	if !errors.As(err, &protocolErr) || protocolErr.Code != codec.CodeAuthKeyNotFound {
		t.Fatalf("corrected revoked provisional recv = %T %v, want protocol -404", err, err)
	}
	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("revoked provisional executed %d RPCs", got)
	}
	waitForManagedSessionAbsent(t, srv.conns, key)
}
