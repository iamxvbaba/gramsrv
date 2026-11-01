package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/log/logzap"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/rpc"
	"telesrv/internal/store"
)

type testLayerSessionKey struct {
	authKeyID [8]byte
	sessionID int64
}

type testAuthKeySessionLayerStore struct {
	mu       sync.Mutex
	now      func() time.Time
	next     int64
	sessions map[testLayerSessionKey]store.AuthKeySessionLayer
	defaults map[[8]byte]store.AuthKeyLayerDefault
}

func newTestAuthKeySessionLayerStore() *testAuthKeySessionLayerStore {
	return newTestAuthKeySessionLayerStoreAt(time.Now)
}

func newTestAuthKeySessionLayerStoreAt(now func() time.Time) *testAuthKeySessionLayerStore {
	return &testAuthKeySessionLayerStore{
		now:      now,
		sessions: make(map[testLayerSessionKey]store.AuthKeySessionLayer),
		defaults: make(map[[8]byte]store.AuthKeyLayerDefault),
	}
}

func testLayerRPCDeps() rpc.Deps {
	return rpc.Deps{AuthKeySessionLayers: newTestAuthKeySessionLayerStore()}
}

func testLayerRPCDepsAt(now func() time.Time) rpc.Deps {
	return rpc.Deps{AuthKeySessionLayers: newTestAuthKeySessionLayerStoreAt(now)}
}

func (s *testAuthKeySessionLayerStore) GetSessionLayer(_ context.Context, authKeyID [8]byte, sessionID int64) (store.AuthKeySessionLayer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := testLayerSessionKey{authKeyID: authKeyID, sessionID: sessionID}
	value, found := s.sessions[key]
	if found && !s.now().Before(value.ExpiresAt) {
		delete(s.sessions, key)
		return store.AuthKeySessionLayer{}, false, nil
	}
	if found {
		def := s.defaults[authKeyID]
		value.SharedDefault = def.Layer == value.Layer && def.ObservationID == value.ObservationID
	}
	return value, found, nil
}

func (s *testAuthKeySessionLayerStore) AdvanceSessionLayer(_ context.Context, authKeyID [8]byte, sessionID int64, layer int, msgID int64) (store.AuthKeySessionLayer, bool, error) {
	if authKeyID == ([8]byte{}) || sessionID == 0 || layer <= 0 || msgID <= 0 {
		return store.AuthKeySessionLayer{}, false, store.ErrAuthKeySessionLayerInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := testLayerSessionKey{authKeyID: authKeyID, sessionID: sessionID}
	if current, found := s.sessions[key]; found {
		if s.now().Before(current.ExpiresAt) {
			if msgID < current.MessageID {
				return current, false, nil
			}
			if msgID == current.MessageID {
				if layer != current.Layer {
					return current, false, store.ErrAuthKeySessionLayerConflict
				}
				return current, false, nil
			}
		}
		delete(s.sessions, key)
	}
	s.next++
	expiresAt, fresh := store.AuthKeySessionLayerEvidenceFresh(s.now(), msgID)
	if !fresh {
		// Focused protocol tests use small synthetic msg_ids. Keep those fixtures
		// bounded without pretending they are valid production timestamps.
		expiresAt = s.now().Add(time.Hour)
	}
	current := store.AuthKeySessionLayer{
		Layer: layer, MessageID: msgID, ObservationID: s.next,
		ExpiresAt: expiresAt, SharedDefault: true,
	}
	s.sessions[key] = current
	s.defaults[authKeyID] = store.AuthKeyLayerDefault{Layer: layer, ObservationID: current.ObservationID}
	return current, true, nil
}

func (s *testAuthKeySessionLayerStore) GetAuthKeyLayerDefault(_ context.Context, authKeyID [8]byte) (store.AuthKeyLayerDefault, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.defaults[authKeyID]
	return value, found, nil
}

func (s *testAuthKeySessionLayerStore) DeleteSessionLayer(_ context.Context, authKeyID [8]byte, sessionID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := testLayerSessionKey{authKeyID: authKeyID, sessionID: sessionID}
	_, found := s.sessions[key]
	delete(s.sessions, key)
	return found, nil
}

func (*testAuthKeySessionLayerStore) DeleteExpiredSessionLayers(context.Context, int) (int, error) {
	return 0, nil
}

type emptyDurableLayerResolver struct{}

func (emptyDurableLayerResolver) ResolveNegotiatedSessionLayerEvidence(context.Context, [8]byte, int64) (int, int64, bool, error) {
	return 0, 0, false, nil
}

func (emptyDurableLayerResolver) ResolveInheritedAuthKeyLayer(context.Context, [8]byte) (int, bool, error) {
	return 0, false, nil
}

// legacyCanonicalTestConn explicitly declares the canonical-only profile used
// by old connection-state tests. Production exact-path tests must instead call
// FreezeLayerProfile/SeedLayerProfile with protocol evidence.
func legacyCanonicalTestConn(t testing.TB, c *Conn) *Conn {
	return legacyLayerWireTestConn(t, c, int(tlprofile.ProfileCanonical))
}

// legacyLayerWireTestConn preserves only the old tests' profile setup. It does
// not enable any wire conversion; application values still need an exact
// generated binding at the outbound boundary.
func legacyLayerWireTestConn(t testing.TB, c *Conn, layer int) *Conn {
	t.Helper()
	if c == nil {
		t.Fatal("nil legacy exact-layer test Conn")
	}
	profile, ok := tlprofile.ResolveProfile(layer)
	if !ok {
		t.Fatalf("unsupported generated test Layer %d", layer)
	}
	if err := c.FreezeLayerProfile(profile); err != nil {
		t.Fatalf("freeze generated test Layer %d: %v", layer, err)
	}
	c.setLegacyClientLayer(layer)
	return c
}

// exactTestUpdatesEncoded gives transport/state-machine tests an explicit
// generated session binding without invoking the production fan-out cache.
// Tests which assert wire conversion use layerUpdatesFanout directly instead.
func exactTestUpdatesEncoded(t testing.TB, c *Conn, body []byte) *encodedOutboundMessage {
	t.Helper()
	if c == nil {
		t.Fatal("nil exact test Conn")
	}
	state := c.LayerProfileState()
	if state.Origin == LayerProfileUnknown {
		t.Fatal("exact test Conn has no generated Layer profile")
	}
	return &encodedOutboundMessage{
		body:   append([]byte(nil), body...),
		typeID: tg.UpdatesTooLongTypeID,
		layer: &outboundLayerBinding{
			profile: state.Profile,
			epoch:   state.Epoch,
		},
	}
}

func exactTestUpdatesTooLong(t testing.TB, c *Conn) *encodedOutboundMessage {
	t.Helper()
	var body bin.Buffer
	if err := (&tg.UpdatesTooLong{}).Encode(&body); err != nil {
		t.Fatalf("encode exact test updatesTooLong: %v", err)
	}
	return exactTestUpdatesEncoded(t, c, body.Raw())
}

// opaqueExactTestRPCResult is an explicit request-bound capability for tests
// of compression, retention and delivery mechanics. Semantic result conversion
// is covered by generated dispatcher tests; no production path constructs it.
type opaqueExactTestRPCResult struct{ result bin.Encoder }

func (r *opaqueExactTestRPCResult) Encode(b *bin.Buffer) error { return r.result.Encode(b) }

func (r *opaqueExactTestRPCResult) exactLayerRPCResultBinding() outboundLayerBinding {
	return outboundLayerBinding{
		profile: tlprofile.ProfileCanonical,
		kind:    outboundLayerBindingRequest,
	}
}

func exactTestRPCResult(result bin.Encoder) bin.Encoder {
	if result == nil || isLayerInvariantRPCResultEncoder(result) {
		return result
	}
	if _, ok := result.(exactLayerRPCResultEncoder); ok {
		return result
	}
	return &opaqueExactTestRPCResult{result: result}
}

// startTestServer generates an RSA key, listens on a random port and starts a
// Server, returning the listen address and public key. It automatically cancels
// via t.Cleanup and checks graceful shutdown. opts's RSAKey/Logger/DC are filled
// in with defaults.
func startTestServer(t *testing.T, opts Options) (addr string, pub exchange.PublicKey, srv *Server) {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}
	opts.RSAKey = rsaKey
	if opts.Logger == nil {
		opts.Logger = zaptest.NewLogger(t)
	}
	if opts.DC == 0 {
		opts.DC = 2
	}
	if _, productionResolver := opts.LayerRPC.(layerRPCProfileResolver); !productionResolver && opts.layerProfileRPC == nil {
		opts.layerProfileRPC = emptyDurableLayerResolver{}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv = New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after ctx cancel")
		}
	})

	return ln.Addr().String(), exchange.PublicKey{RSA: &rsaKey.PublicKey}, srv
}

// dialHandshake establishes a TCP connection, completes the intermediate
// negotiation and the MTProto key exchange, and returns the connection, the
// handshake result and the client-side cipher. The connection is closed
// automatically via t.Cleanup.
func dialHandshake(t *testing.T, addr string, dc int, pub exchange.PublicKey) (transport.Conn, exchange.ClientExchangeResult, crypto.Cipher) {
	t.Helper()
	conn := dialTransportOnly(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	auth, err := exchange.NewExchanger(conn, dc).
		WithRand(rand.Reader).
		WithLogger(logzap.New(zaptest.NewLogger(t).Named("client"))).
		Client([]exchange.PublicKey{pub}).
		Run(ctx)
	if err != nil {
		t.Fatalf("client exchange: %v", err)
	}
	return conn, auth, crypto.NewClientCipher(rand.Reader)
}

func dialTransportOnly(t *testing.T, addr string) transport.Conn {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		_ = raw.Close()
		t.Fatalf("transport handshake: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// freezeActiveTestSessionProfile gives low-level transport fixtures the exact
// profile that a production invokeWithLayer admission would have proven. It is
// intentionally explicit: handshake/new_session_created alone never implies a
// TL Layer, and production push code must keep failing closed in that state.
func freezeActiveTestSessionProfile(t *testing.T, sessions *SessionManager, authKeyID [8]byte, sessionID int64, profile tlprofile.Profile) {
	t.Helper()
	if sessions == nil {
		t.Fatal("freeze test session profile on nil SessionManager")
	}
	key := sessionKey{authKeyID: authKeyID, sessionID: sessionID}
	sessions.mu.RLock()
	c := sessions.bySession[key]
	sessions.mu.RUnlock()
	if c == nil {
		t.Fatalf("active test session %x/%d is missing", authKeyID, sessionID)
	}
	if err := c.FreezeLayerProfile(profile); err != nil {
		t.Fatalf("freeze active test session profile %d: %v", profile, err)
	}
}

// sendEncrypted encrypts with the client cipher and sends a message with msgID.
func sendEncrypted(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, msgID int64, msg bin.Encoder) {
	t.Helper()
	sendEncryptedWithSalt(t, conn, cipher, auth, auth.ServerSalt, msgID, msg)
}

// sendEncryptedWithSalt encrypts with the given salt and sends a message.
func sendEncryptedWithSalt(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, salt, msgID int64, msg bin.Encoder) {
	t.Helper()
	body, seqNo := encodeClientMessageForTest(t, msg)
	sendEncryptedWithSaltAndSeq(t, conn, cipher, auth, salt, msgID, seqNo, body)
}

func sendEncryptedWithSeq(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, msgID int64, seqNo int32, msg bin.Encoder) {
	t.Helper()
	body := encodeClientMessageBodyForTest(t, msg)
	sendEncryptedWithSaltAndSeq(t, conn, cipher, auth, auth.ServerSalt, msgID, seqNo, body)
}

func sendEncryptedWithSaltAndSeq(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, salt, msgID int64, seqNo int32, body []byte) {
	t.Helper()
	sendEncryptedWithSessionSaltAndSeq(t, conn, cipher, auth, auth.SessionID, salt, msgID, seqNo, body)
}

func sendEncryptedWithSessionSaltAndSeq(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, sessionID, salt, msgID int64, seqNo int32, body []byte) {
	t.Helper()
	var buf bin.Buffer
	if err := cipher.Encrypt(auth.AuthKey, crypto.EncryptedMessageData{
		Salt:                   salt,
		SessionID:              sessionID,
		MessageID:              msgID,
		SeqNo:                  seqNo,
		MessageDataLen:         int32(len(body)),
		MessageDataWithPadding: body,
	}, &buf); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Send(ctx, &buf); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func encodeClientMessageForTest(t *testing.T, msg bin.Encoder) ([]byte, int32) {
	t.Helper()
	raw := encodeClientMessageBodyForTest(t, msg)
	typeID, err := (&bin.Buffer{Buf: raw}).PeekID()
	if err != nil {
		t.Fatalf("peek encrypted message type: %v", err)
	}
	if container, ok := msg.(*proto.MessageContainer); ok {
		return raw, clientContainerSeqNoForTest(container)
	}
	if clientMessageContentPolicyFor(typeID) == clientMessageContentRequired {
		return raw, 1
	}
	return raw, 0
}

func encodeClientMessageBodyForTest(t *testing.T, msg bin.Encoder) []byte {
	t.Helper()
	var body bin.Buffer
	if err := msg.Encode(&body); err != nil {
		t.Fatalf("encode encrypted message: %v", err)
	}
	return body.Copy()
}

func clientContainerSeqNoForTest(container *proto.MessageContainer) int32 {
	var maxSeq int32
	for _, msg := range container.Messages {
		if seq := int32(msg.SeqNo); seq > maxSeq {
			maxSeq = seq
		}
	}
	if maxSeq%2 != 0 {
		maxSeq++
	}
	return maxSeq
}

// collectReplies reads and decrypts messages the server sends back, collecting
// plaintext buffers by TypeID, until wantID is seen (inclusive) or the cap is
// reached. Used to assert the multiple responses triggered by one request
// (new_session_created / business response / msgs_ack).
func collectReplies(t *testing.T, conn transport.Conn, cipher crypto.Cipher, key crypto.AuthKey, wantID uint32) map[uint32]*bin.Buffer {
	t.Helper()
	got := make(map[uint32]*bin.Buffer)
	for i := 0; i < 8; i++ {
		_, id, plain := readServerMessage(t, conn, cipher, key)
		got[id] = plain
		if id == wantID {
			break
		}
	}
	return got
}

// serverReplyFrame preserves the wire order and encrypted envelope of a server
// reply. Tests which exercise session boundaries must not collapse replies into
// a TypeID-keyed map: both duplicate response types and their order are part of
// the observable protocol behavior.
type serverReplyFrame struct {
	Message *crypto.EncryptedMessageData
	TypeID  uint32
	Plain   *bin.Buffer
}

// collectReplyFrames reads ordered server replies until every requested TypeID
// has been observed the requested number of times. Unrequested frames are kept
// in the returned slice so callers can assert ordering around control messages.
func collectReplyFrames(
	t *testing.T,
	conn transport.Conn,
	cipher crypto.Cipher,
	key crypto.AuthKey,
	wantCounts map[uint32]int,
) []serverReplyFrame {
	t.Helper()

	remaining := make(map[uint32]int, len(wantCounts))
	required := 0
	for typeID, count := range wantCounts {
		if count <= 0 {
			continue
		}
		remaining[typeID] = count
		required += count
	}
	if required == 0 {
		return nil
	}

	// Keep the helper bounded while allowing unrelated control replies (notably
	// msgs_ack) to be interleaved with the frames under test.
	// One shared deadline bounds the whole collection. A per-frame deadline would
	// multiply a missing-result failure by the maximum number of unrelated frames.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	frames := make([]serverReplyFrame, 0, required)
	for i := 0; i < required+16; i++ {
		message, typeID, plain := readServerMessageContext(t, ctx, conn, cipher, key)
		frames = append(frames, serverReplyFrame{
			Message: message,
			TypeID:  typeID,
			Plain:   plain,
		})
		if count, ok := remaining[typeID]; ok {
			if count == 1 {
				delete(remaining, typeID)
			} else {
				remaining[typeID] = count - 1
			}
		}
		if len(remaining) == 0 {
			return frames
		}
	}

	t.Fatalf("missing reply counts after %d frames: %+v", len(frames), remaining)
	return nil
}

func readServerMessage(t *testing.T, conn transport.Conn, cipher crypto.Cipher, key crypto.AuthKey) (*crypto.EncryptedMessageData, uint32, *bin.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return readServerMessageContext(t, ctx, conn, cipher, key)
}

func readServerMessageContext(t *testing.T, ctx context.Context, conn transport.Conn, cipher crypto.Cipher, key crypto.AuthKey) (*crypto.EncryptedMessageData, uint32, *bin.Buffer) {
	t.Helper()
	var buf bin.Buffer
	err := conn.Recv(ctx, &buf)
	if err != nil {
		t.Fatalf("recv server message: %v", err)
	}
	data, err := cipher.DecryptFromBuffer(key, &buf)
	if err != nil {
		t.Fatalf("decrypt server message: %v", err)
	}
	plain := append([]byte(nil), data.Data()...)
	id, err := (&bin.Buffer{Buf: plain}).PeekID()
	if err != nil {
		t.Fatalf("peek server message: %v", err)
	}
	return data, id, &bin.Buffer{Buf: plain}
}

// mustHave asserts replies contains a message of the given TypeID and returns its buffer.
func mustHave(t *testing.T, replies map[uint32]*bin.Buffer, id uint32, name string) *bin.Buffer {
	t.Helper()
	b, ok := replies[id]
	if !ok {
		t.Fatalf("missing %s (%#x)", name, id)
	}
	return b
}
