package mtprotoedge

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
	"go.uber.org/zap/zaptest/observer"

	"github.com/gotd/log/logzap"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mt"
	tgproto "github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// TestKeyExchange verifies M1: the client completes the MTProto key exchange
// using the server's public key, both sides get a consistent auth key and server
// salt, and the server stores them in AuthKeyStore.
func TestKeyExchange(t *testing.T) {
	const dc = 2

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	keys := memory.NewAuthKeyStore()
	logCore, observedLogs := observer.New(zap.DebugLevel)
	srv := New(Options{
		Logger:   zap.New(logCore),
		DC:       dc,
		RSAKey:   rsaKey,
		AuthKeys: keys,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	// Client: TCP dial + intermediate handshake, running the client-side key exchange.
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("transport handshake: %v", err)
	}

	pub := exchange.PublicKey{RSA: &rsaKey.PublicKey}
	exchCtx, ec := context.WithTimeout(context.Background(), 10*time.Second)
	defer ec()
	res, err := exchange.NewExchanger(conn, dc).
		WithRand(rand.Reader).
		WithLogger(logzap.New(zaptest.NewLogger(t).Named("client"))).
		Client([]exchange.PublicKey{pub}).
		Run(exchCtx)
	if err != nil {
		t.Fatalf("client exchange: %v", err)
	}

	// The server persists after Run returns; poll and wait.
	var saved store.AuthKeyData
	found := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		saved, found, _ = keys.Get(context.Background(), res.AuthKey.ID)
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("server did not store auth key %x", res.AuthKey.ID)
	}
	if saved.Value != [256]byte(res.AuthKey.Value) {
		t.Fatal("server auth key value mismatch")
	}
	if saved.ServerSalt != res.ServerSalt {
		t.Fatalf("server salt mismatch: server=%d client=%d", saved.ServerSalt, res.ServerSalt)
	}
	// "Key exchange completed" is logged by the server only after the exchange
	// run returns, while the auth key store commit happens first (before
	// DhGenOk). The server goroutine can be descheduled in that window, so poll
	// for the entry instead of asserting once right after the key is visible.
	var completed []observer.LoggedEntry
	logDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(logDeadline) {
		completed = observedLogs.FilterMessage("Key exchange completed").All()
		if len(completed) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(completed) != 1 || completed[0].Level != zap.DebugLevel {
		t.Fatalf("successful key exchange logs = %+v, want one Debug entry", completed)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

type tdlibZeroHandshakeMessageIDConn struct {
	transport.Conn
}

func (c *tdlibZeroHandshakeMessageIDConn) Send(ctx context.Context, frame *bin.Buffer) error {
	candidate := &bin.Buffer{Buf: frame.Copy()}
	var message tgproto.UnencryptedMessage
	if err := message.Decode(candidate); err != nil {
		return c.Conn.Send(ctx, frame)
	}

	payload := &bin.Buffer{Buf: message.MessageData}
	typeID, err := payload.PeekID()
	if err != nil {
		return c.Conn.Send(ctx, frame)
	}
	switch typeID {
	case mt.ReqPqMultiRequestTypeID,
		mt.ReqDHParamsRequestTypeID,
		mt.SetClientDHParamsRequestTypeID:
		message.MessageID = 0
	default:
		return c.Conn.Send(ctx, frame)
	}

	// TDLib NoCryptoImpl includes 0-255 random bytes in message_data_length.
	// Use its maximum legal alignment-plus-15-block shape so the complete
	// permanent and temporary exchanges exercise padded bodies at every stage.
	paddingSize := (-len(message.MessageData)) & 15
	paddingSize += 16 * 15
	message.MessageData = append(
		message.MessageData,
		bytes.Repeat([]byte{0xa5}, paddingSize)...,
	)

	var rewritten bin.Buffer
	if err := message.Encode(&rewritten); err != nil {
		return err
	}
	return c.Conn.Send(ctx, &rewritten)
}

func TestKeyExchangeAcceptsTDLibZeroMessageIDs(t *testing.T) {
	const (
		dc        = 2
		expiresIn = 60
	)
	tests := []struct {
		name       string
		clientDC   int
		temporary  bool
		wantExpiry bool
	}{
		{name: "permanent key", clientDC: dc},
		{
			name:       "media temporary key",
			clientDC:   -dc,
			temporary:  true,
			wantExpiry: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			addr, pub, srv := startTestServer(t, Options{DC: dc})
			conn := &tdlibZeroHandshakeMessageIDConn{Conn: dialTransportOnly(t, addr)}
			t.Cleanup(func() { _ = conn.Close() })

			exchanger := exchange.NewExchanger(conn, test.clientDC).
				WithRand(rand.Reader).
				WithLogger(logzap.New(zaptest.NewLogger(t).Named("tdlib-zero-msg-id-client")))
			if test.temporary {
				exchanger = exchanger.WithTempMode(expiresIn)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result, err := exchanger.
				Client([]exchange.PublicKey{pub}).
				Run(ctx)
			if err != nil {
				t.Fatalf("TDLib-shaped client exchange: %v", err)
			}
			if result.AuthKey.ID == ([8]byte{}) {
				t.Fatal("TDLib-shaped client exchange returned an empty auth key id")
			}
			if got := result.ExpiresAt > 0; got != test.wantExpiry {
				t.Fatalf("client expiry present = %v, want %v", got, test.wantExpiry)
			}

			var (
				saved store.AuthKeyData
				found bool
			)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				saved, found, _ = srv.authKeys.Get(context.Background(), result.AuthKey.ID)
				if found {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if !found {
				t.Fatalf("server did not store TDLib-shaped auth key %x", result.AuthKey.ID)
			}
			if got := saved.ExpiresAt > 0; got != test.wantExpiry {
				t.Fatalf("server expiry present = %v, want %v", got, test.wantExpiry)
			}
		})
	}
}

func TestKeyExchangeRestartsStaleHandshakeAfterProbe(t *testing.T) {
	addr, pub, _ := startTestServer(t, Options{DC: 2, AuthKeys: memory.NewAuthKeyStore()})
	for _, kind := range []string{"fresh", "nonce", "server_nonce", "dh_without_probe", "client_dh_after_probe"} {
		t.Run(kind, func(t *testing.T) {
			conn := dialTransportOnly(t, addr)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			send := func(value bin.Encoder, id int64) {
				t.Helper()
				var payload, frame bin.Buffer
				if err := value.Encode(&payload); err != nil {
					t.Fatal(err)
				}
				if err := (tgproto.UnencryptedMessage{MessageID: id, MessageData: payload.Copy()}).Encode(&frame); err != nil {
					t.Fatal(err)
				}
				if err := conn.Send(ctx, &frame); err != nil {
					t.Fatal(err)
				}
			}
			nonce := bin.Int128{1}
			serverNonce := bin.Int128{2}
			if kind != "dh_without_probe" {
				send(&mt.ReqPqMultiRequest{Nonce: nonce}, 1)
				var frame bin.Buffer
				if err := conn.Recv(ctx, &frame); err != nil {
					t.Fatal(err)
				}
				var response tgproto.UnencryptedMessage
				if err := response.Decode(&frame); err != nil {
					t.Fatal(err)
				}
				var pq mt.ResPQ
				if err := pq.Decode(&bin.Buffer{Buf: response.MessageData}); err != nil {
					t.Fatal(err)
				}
				serverNonce = pq.ServerNonce
			}
			if kind != "fresh" {
				switch kind {
				case "nonce":
					nonce[0] ^= 1
				case "server_nonce":
					serverNonce[0] ^= 1
				}
				if kind == "client_dh_after_probe" {
					send(&mt.SetClientDHParamsRequest{Nonce: nonce, ServerNonce: serverNonce}, 0)
				} else {
					send(&mt.ReqDHParamsRequest{Nonce: nonce, ServerNonce: serverNonce, PublicKeyFingerprint: pub.Fingerprint()}, 0)
				}
				var rejected bin.Buffer
				err := conn.Recv(ctx, &rejected)
				var protocolError *codec.ProtocolErr
				if !errors.As(err, &protocolError) || protocolError.Code != codec.CodeAuthKeyNotFound {
					t.Fatalf("missing handshake reset: %v", err)
				}
			}
			// TDLib clears the old nonce/last_query only on -404. A new full exchange
			// must succeed; neither a probe nor a rejected resume may poison it.
			_, err := exchange.NewExchanger(&tdlibZeroHandshakeMessageIDConn{Conn: conn}, 2).
				WithRand(rand.Reader).Client([]exchange.PublicKey{pub}).Run(ctx)
			if err != nil {
				t.Fatalf("fresh exchange: %v", err)
			}
		})
	}
}

func TestValidUnencryptedHandshakeMessageID(t *testing.T) {
	encode := func(value bin.Encoder) []byte {
		t.Helper()
		var payload bin.Buffer
		if err := value.Encode(&payload); err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		return payload.Copy()
	}

	const ordinaryClientMessageID = int64(1<<32 | 4)
	tests := []struct {
		name      string
		messageID int64
		payload   []byte
		want      bool
	}{
		{
			name:      "ordinary client id retains existing admission",
			messageID: ordinaryClientMessageID,
			payload:   encode(&mt.PingRequest{}),
			want:      true,
		},
		{
			name:      "probe sentinel rejects legacy req pq",
			messageID: 1,
			payload:   encode(&mt.ReqPqRequest{}),
			want:      false,
		},
		{
			name:      "TDLib probe req pq multi",
			messageID: 1,
			payload:   encode(&mt.ReqPqMultiRequest{}),
			want:      true,
		},
		{
			name:      "probe sentinel cannot carry req DH",
			messageID: 1,
			payload:   encode(&mt.ReqDHParamsRequest{}),
			want:      false,
		},
		{
			name:      "zero sentinel rejects legacy req pq",
			messageID: 0,
			payload:   encode(&mt.ReqPqRequest{}),
			want:      false,
		},
		{
			name:      "TDLib handshake req pq multi",
			messageID: 0,
			payload:   encode(&mt.ReqPqMultiRequest{}),
			want:      true,
		},
		{
			name:      "TDLib handshake req DH",
			messageID: 0,
			payload:   encode(&mt.ReqDHParamsRequest{}),
			want:      true,
		},
		{
			name:      "TDLib handshake set client DH",
			messageID: 0,
			payload:   encode(&mt.SetClientDHParamsRequest{}),
			want:      true,
		},
		{
			name:      "zero sentinel cannot carry ack",
			messageID: 0,
			payload:   encode(&mt.MsgsAck{}),
			want:      false,
		},
		{
			name:      "other invalid nonzero id remains rejected",
			messageID: 2,
			payload:   encode(&mt.ReqPqMultiRequest{}),
			want:      false,
		},
		{
			name:      "negative id remains rejected",
			messageID: -4,
			payload:   encode(&mt.ReqPqMultiRequest{}),
			want:      false,
		},
		{
			name:      "sentinel requires a complete constructor id",
			messageID: 0,
			payload:   []byte{1, 2, 3},
			want:      false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validUnencryptedHandshakeMessageID(test.messageID, test.payload); got != test.want {
				t.Fatalf(
					"validUnencryptedHandshakeMessageID(%d) = %v, want %v",
					test.messageID,
					got,
					test.want,
				)
			}
		})
	}
}

type authKeySaveContextObservation struct {
	hasDeadline bool
	deadline    time.Time
}

type observingAuthKeyStore struct {
	store.AuthKeyStore
	saveContext chan authKeySaveContextObservation
}

func (s *observingAuthKeyStore) Save(ctx context.Context, key store.AuthKeyData) error {
	deadline, hasDeadline := ctx.Deadline()
	select {
	case s.saveContext <- authKeySaveContextObservation{hasDeadline: hasDeadline, deadline: deadline}:
	default:
	}
	return s.AuthKeyStore.Save(ctx, key)
}

type gatedAuthKeyStore struct {
	store.AuthKeyStore
	entered chan store.AuthKeyData
	release chan struct{}
	saveErr error
}

type ownershipFrameConn struct {
	transport.Conn
	frame []byte
}

func (c *ownershipFrameConn) Recv(_ context.Context, b *bin.Buffer) error {
	b.ResetTo(c.frame)
	return nil
}

func TestReadUnencryptedAcceptsTDLibProbeMessageID(t *testing.T) {
	nonce := bin.Int128{1, 2, 3, 4}
	var payload bin.Buffer
	if err := (&mt.ReqPqMultiRequest{Nonce: nonce}).Encode(&payload); err != nil {
		t.Fatalf("encode req_pq_multi: %v", err)
	}

	tests := []struct {
		name    string
		padding []byte
	}{
		{name: "without TDLib no-crypto padding"},
		{name: "with minimum TDLib no-crypto padding", padding: bytes.Repeat([]byte{0xa5}, 12)},
		{name: "with maximum TDLib no-crypto padding", padding: bytes.Repeat([]byte{0x5a}, 252)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messageData := append(payload.Copy(), test.padding...)
			var frame bin.Buffer
			if err := (tgproto.UnencryptedMessage{
				MessageID:   1,
				MessageData: messageData,
			}).Encode(&frame); err != nil {
				t.Fatalf("encode unencrypted probe: %v", err)
			}
			ex := serverExchangeCompat{
				conn:    &ownershipFrameConn{frame: frame.Copy()},
				timeout: time.Second,
			}
			var decoded compatReqPQ
			var scratch bin.Buffer
			if err := ex.readUnencrypted(context.Background(), &scratch, &decoded); err != nil {
				t.Fatalf("read TDLib probe: %v", err)
			}
			if decoded.Type != mt.ReqPqMultiRequestTypeID || decoded.Nonce != nonce {
				t.Fatalf(
					"decoded probe = {type:%#x nonce:%x}, want req_pq_multi nonce %x",
					decoded.Type,
					decoded.Nonce,
					nonce,
				)
			}
		})
	}
}

func TestExchangeEncryptedReplayTransfersFrameOwnership(t *testing.T) {
	backing := make([]byte, 64)
	copy(backing[:8], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	conn := &ownershipFrameConn{frame: backing}
	ex := serverExchangeCompat{conn: conn, timeout: time.Second}
	var b bin.Buffer
	err := ex.readUnencrypted(context.Background(), &b, &compatReqPQ{})
	var encrypted *exchange.UnexpectedEncryptedError
	if !errors.As(err, &encrypted) {
		t.Fatalf("read encrypted frame err = %v, want UnexpectedEncryptedError", err)
	}
	if len(encrypted.Frame) != len(backing) || &encrypted.Frame[0] != &backing[0] {
		t.Fatal("encrypted replay copied the received frame instead of transferring ownership")
	}
	if b.Buf != nil {
		t.Fatal("exchange buffer retained transferred encrypted frame backing")
	}
}

func (s *gatedAuthKeyStore) Save(ctx context.Context, key store.AuthKeyData) error {
	select {
	case s.entered <- key:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.AuthKeyStore.Save(ctx, key)
}

// TestKeyExchangeDoesNotAcknowledgeBeforeAuthKeyCommit pins the protocol commit
// boundary: while durable Save is blocked, the client must not receive DhGenOk
// and therefore must not report a successful exchange.
func TestKeyExchangeDoesNotAcknowledgeBeforeAuthKeyCommit(t *testing.T) {
	base := memory.NewAuthKeyStore()
	keys := &gatedAuthKeyStore{
		AuthKeyStore: base,
		entered:      make(chan store.AuthKeyData, 1),
		release:      make(chan struct{}, 1),
	}
	addr, pub, _ := startTestServer(t, Options{DC: 2, AuthKeys: keys})
	conn := dialTransportOnly(t, addr)

	type exchangeOutcome struct {
		result exchange.ClientExchangeResult
		err    error
	}
	outcome := make(chan exchangeOutcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		result, err := exchange.NewExchanger(conn, 2).
			WithRand(rand.Reader).
			Client([]exchange.PublicKey{pub}).
			Run(ctx)
		outcome <- exchangeOutcome{result: result, err: err}
	}()

	var pending store.AuthKeyData
	select {
	case pending = <-keys.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("AuthKeyStore.Save was not reached")
	}
	defer func() {
		select {
		case keys.release <- struct{}{}:
		default:
		}
	}()

	select {
	case got := <-outcome:
		t.Fatalf("client exchange completed before auth key commit: err=%v", got.err)
	case <-time.After(150 * time.Millisecond):
	}
	if _, found, err := base.Get(context.Background(), pending.ID); err != nil {
		t.Fatalf("Get before commit: %v", err)
	} else if found {
		t.Fatal("auth key became visible while durable Save was blocked")
	}

	keys.release <- struct{}{}
	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("client exchange after commit: %v", got.err)
		}
		if got.result.AuthKey.ID != pending.ID {
			t.Fatalf("committed auth key id = %x, client got %x", pending.ID, got.result.AuthKey.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client exchange did not finish after auth key commit")
	}
	if _, found, err := base.Get(context.Background(), pending.ID); err != nil {
		t.Fatalf("Get after commit: %v", err)
	} else if !found {
		t.Fatal("auth key is not durable after successful client exchange")
	}
}

// TestKeyExchangeAuthKeyCommitFailureWithholdsDhGenOk proves the failure side
// of the same invariant. The client must not observe success if storage rejects
// the key; the server closes this exchange and lets the client retry cleanly.
func TestKeyExchangeAuthKeyCommitFailureWithholdsDhGenOk(t *testing.T) {
	base := memory.NewAuthKeyStore()
	keys := &gatedAuthKeyStore{
		AuthKeyStore: base,
		entered:      make(chan store.AuthKeyData, 1),
		release:      make(chan struct{}, 1),
		saveErr:      errors.New("injected auth key persistence failure"),
	}
	keys.release <- struct{}{}
	addr, pub, _ := startTestServer(t, Options{DC: 2, AuthKeys: keys})
	conn := dialTransportOnly(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := exchange.NewExchanger(conn, 2).
		WithRand(rand.Reader).
		Client([]exchange.PublicKey{pub}).
		Run(ctx)
	if err == nil {
		t.Fatal("client exchange succeeded even though auth key commit failed")
	}

	select {
	case attempted := <-keys.entered:
		if _, found, getErr := base.Get(context.Background(), attempted.ID); getErr != nil {
			t.Fatalf("Get failed key: %v", getErr)
		} else if found {
			t.Fatal("failed auth key commit became visible")
		}
	case <-time.After(time.Second):
		t.Fatal("AuthKeyStore.Save was not attempted")
	}
}

func TestKeyExchangeAuthKeySaveUsesHandshakeDeadline(t *testing.T) {
	const handshakeMax = 10 * time.Second
	observed := make(chan authKeySaveContextObservation, 1)
	keys := &observingAuthKeyStore{
		AuthKeyStore: memory.NewAuthKeyStore(),
		saveContext:  observed,
	}
	addr, pub, _ := startTestServer(t, Options{
		DC:                   2,
		AuthKeys:             keys,
		HandshakeMaxDuration: handshakeMax,
	})

	_, _, _ = dialHandshake(t, addr, 2, pub)
	select {
	case got := <-observed:
		if !got.hasDeadline {
			t.Fatal("AuthKeyStore.Save context has no handshake deadline")
		}
		remaining := time.Until(got.deadline)
		if remaining <= 0 || remaining > handshakeMax {
			t.Fatalf("AuthKeyStore.Save deadline remaining = %v, want (0, %v]", remaining, handshakeMax)
		}
	case <-time.After(time.Second):
		t.Fatal("AuthKeyStore.Save was not called")
	}
}

func TestKeyExchangeAcceptsAndroidMediaTempNegativeDC(t *testing.T) {
	const (
		dc        = 2
		expiresIn = 24 * 60 * 60
	)
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	conn := dialTransportOnly(t, addr)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	startedAt := time.Now()
	res, err := exchange.NewExchanger(conn, -dc).
		WithTempMode(expiresIn).
		WithRand(rand.Reader).
		WithLogger(logzap.New(zaptest.NewLogger(t).Named("client"))).
		Client([]exchange.PublicKey{pub}).
		Run(ctx)
	if err != nil {
		t.Fatalf("client exchange: %v", err)
	}
	completedAt := time.Now()

	var saved store.AuthKeyData
	found := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		saved, found, _ = srv.authKeys.Get(context.Background(), res.AuthKey.ID)
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("server did not store media temp auth key %x", res.AuthKey.ID)
	}
	if saved.Value != [256]byte(res.AuthKey.Value) {
		t.Fatal("server auth key value mismatch")
	}
	if saved.ServerSalt != res.ServerSalt {
		t.Fatalf("server salt mismatch: server=%d client=%d", saved.ServerSalt, res.ServerSalt)
	}
	minExpiresAt := int(startedAt.Unix()) + expiresIn
	maxExpiresAt := int(completedAt.Unix()) + expiresIn
	if saved.ExpiresAt < minExpiresAt || saved.ExpiresAt > maxExpiresAt {
		t.Fatalf("server temp auth key expires_at = %d, want absolute unix time in [%d, %d]", saved.ExpiresAt, minExpiresAt, maxExpiresAt)
	}
}

func TestKeyExchangeAcceptsAnyDCLabelByDefault(t *testing.T) {
	ex := serverExchangeCompat{dc: 2, log: zaptest.NewLogger(t)}
	labels := []int{
		2,      // canonical
		3,      // another production DC
		0,      // no conventional DC mapping
		-2,     // Android media-temp convention
		10002,  // test-environment style label
		-10002, // negative test-environment style label
		-1 << 31,
		1<<31 - 1,
	}

	for _, label := range labels {
		t.Run(fmt.Sprintf("permanent_%d", label), func(t *testing.T) {
			if err := ex.validatePQInnerDataDC(&mt.PQInnerDataDC{DC: label}); err != nil {
				t.Fatalf("validate permanent DC label %d: %v", label, err)
			}
		})
		t.Run(fmt.Sprintf("temporary_%d", label), func(t *testing.T) {
			if err := ex.validatePQInnerDataDC(&mt.PQInnerDataTempDC{DC: label, ExpiresIn: 60}); err != nil {
				t.Fatalf("validate temporary DC label %d: %v", label, err)
			}
		})
	}
}

func TestKeyExchangePersistsArbitraryPermanentDCLabelByDefault(t *testing.T) {
	const clientDC = 10002
	keys := memory.NewAuthKeyStore()
	addr, pub, _ := startTestServer(t, Options{DC: 2, AuthKeys: keys})

	_, auth, _ := dialHandshake(t, addr, clientDC, pub)
	saved, found, err := keys.Get(context.Background(), auth.AuthKey.ID)
	if err != nil {
		t.Fatalf("get persisted auth key: %v", err)
	}
	if !found {
		t.Fatalf("auth key %x was not persisted", auth.AuthKey.ID)
	}
	if saved.Value != [256]byte(auth.AuthKey.Value) {
		t.Fatal("persisted auth key value mismatch")
	}
}

func TestKeyExchangeStrictDCValidation(t *testing.T) {
	ex := serverExchangeCompat{dc: 2, strictDC: true, log: zaptest.NewLogger(t)}
	tests := []struct {
		name    string
		data    mt.PQInnerDataClass
		wantErr bool
	}{
		{name: "permanent exact", data: &mt.PQInnerDataDC{DC: 2}},
		{name: "permanent other", data: &mt.PQInnerDataDC{DC: 3}, wantErr: true},
		{name: "permanent zero", data: &mt.PQInnerDataDC{DC: 0}, wantErr: true},
		{name: "temporary positive exact", data: &mt.PQInnerDataTempDC{DC: 2}},
		{name: "temporary negative exact", data: &mt.PQInnerDataTempDC{DC: -2}},
		{name: "temporary other", data: &mt.PQInnerDataTempDC{DC: 3}, wantErr: true},
		{name: "temporary negative other", data: &mt.PQInnerDataTempDC{DC: -3}, wantErr: true},
		{name: "temporary test label", data: &mt.PQInnerDataTempDC{DC: 10002}, wantErr: true},
		{name: "temporary min int32", data: &mt.PQInnerDataTempDC{DC: -1 << 31}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ex.validatePQInnerDataDC(test.data)
			if !test.wantErr {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				return
			}
			assertWrongDCError(t, err)
		})
	}
}

func assertWrongDCError(t *testing.T, err error) {
	t.Helper()
	var exErr *exchange.ServerExchangeError
	if !errors.As(err, &exErr) {
		t.Fatalf("err = %T %v, want ServerExchangeError", err, err)
	}
	if exErr.Code != codec.CodeWrongDC {
		t.Fatalf("error code = %d, want %d", exErr.Code, codec.CodeWrongDC)
	}
}

func TestDecodeCompatPQInnerDataTemp(t *testing.T) {
	want := mt.PQInnerData{
		Pq:          []byte{0x0f},
		P:           []byte{0x03},
		Q:           []byte{0x05},
		Nonce:       bin.Int128{1, 2, 3},
		ServerNonce: bin.Int128{4, 5, 6},
		NewNonce:    bin.Int256{7, 8, 9},
	}
	b := new(bin.Buffer)
	b.PutID(pqInnerDataTempTypeID)
	if err := want.EncodeBare(b); err != nil {
		t.Fatalf("encode bare: %v", err)
	}
	b.PutInt(86400)

	got, generated, err := decodeCompatPQInnerData(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if generated != nil {
		t.Fatalf("generated class = %T, want nil for iOS temp compatibility type", generated)
	}
	if !got.Temp || got.ExpiresIn != 86400 {
		t.Fatalf("temp metadata = (%v, %d), want (true, 86400)", got.Temp, got.ExpiresIn)
	}
	if !bytes.Equal(got.Data.Pq, want.Pq) || !bytes.Equal(got.Data.P, want.P) || !bytes.Equal(got.Data.Q, want.Q) ||
		got.Data.Nonce != want.Nonce || got.Data.ServerNonce != want.ServerNonce || got.Data.NewNonce != want.NewNonce {
		t.Fatalf("decoded data = %+v, want %+v", got.Data, want)
	}
}

func TestDecodeCompatPQInnerDataTempRejectsTruncatedData(t *testing.T) {
	b := new(bin.Buffer)
	b.PutID(pqInnerDataTempTypeID)
	b.PutBytes([]byte{0x0f})
	if _, _, err := decodeCompatPQInnerData(b); err == nil {
		t.Fatal("truncated p_q_inner_data_temp decoded successfully")
	}
}

func TestValidatePQInnerDataInvariants(t *testing.T) {
	nonce := bin.Int128{1}
	serverNonce := bin.Int128{2}
	pq := big.NewInt(15)
	valid := compatPQInnerData{
		Data: mt.PQInnerData{
			Pq:          pq.Bytes(),
			P:           []byte{3},
			Q:           []byte{5},
			Nonce:       nonce,
			ServerNonce: serverNonce,
		},
		Temp:      true,
		ExpiresIn: 86400,
	}
	req := compatReqPQ{Nonce: nonce}
	dh := mt.ReqDHParamsRequest{P: []byte{3}, Q: []byte{5}}
	if err := validatePQInnerData(valid, req, dh, serverNonce, pq); err != nil {
		t.Fatalf("valid inner data: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*compatPQInnerData)
	}{
		{name: "nonce", mutate: func(d *compatPQInnerData) { d.Data.Nonce = bin.Int128{9} }},
		{name: "server nonce", mutate: func(d *compatPQInnerData) { d.Data.ServerNonce = bin.Int128{9} }},
		{name: "pq", mutate: func(d *compatPQInnerData) { d.Data.Pq = []byte{21} }},
		{name: "outer factors", mutate: func(d *compatPQInnerData) { d.Data.P = []byte{5} }},
		{name: "factor product", mutate: func(d *compatPQInnerData) { d.Data.P = []byte{2}; dh.P = []byte{2} }},
		{name: "expiry", mutate: func(d *compatPQInnerData) { d.ExpiresIn = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			candidate.Data.Pq = bytes.Clone(valid.Data.Pq)
			candidate.Data.P = bytes.Clone(valid.Data.P)
			candidate.Data.Q = bytes.Clone(valid.Data.Q)
			localDH := dh
			if tt.name == "factor product" {
				candidate.Data.P = []byte{2}
				localDH.P = []byte{2}
			} else {
				tt.mutate(&candidate)
			}
			if err := validatePQInnerData(candidate, req, localDH, serverNonce, pq); err == nil {
				t.Fatal("invalid inner data validated successfully")
			}
		})
	}
}

func TestKeyExchangeIgnoresUnencryptedMsgsAck(t *testing.T) {
	const dc = 2

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	keys := memory.NewAuthKeyStore()
	srv := New(Options{
		Logger:   zaptest.NewLogger(t),
		DC:       dc,
		RSAKey:   rsaKey,
		AuthKeys: keys,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("transport handshake: %v", err)
	}

	pub := exchange.PublicKey{RSA: &rsaKey.PublicKey}
	exchCtx, ec := context.WithTimeout(context.Background(), 10*time.Second)
	defer ec()
	res, err := exchange.NewExchanger(&ackingExchangeConn{Conn: conn, t: t}, dc).
		WithRand(rand.Reader).
		WithLogger(logzap.New(zaptest.NewLogger(t).Named("client"))).
		Client([]exchange.PublicKey{pub}).
		Run(exchCtx)
	if err != nil {
		t.Fatalf("client exchange: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, found, _ := keys.Get(context.Background(), res.AuthKey.ID); found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not store auth key %x", res.AuthKey.ID)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

func TestBufferedExchangePushTransfersFrameOwnershipWithoutCopy(t *testing.T) {
	backing := make([]byte, 64)
	for i := range backing {
		backing[i] = byte(i)
	}
	source := &bin.Buffer{Buf: backing}
	buffered := newBufferedConn(nil)
	buffered.push(source)
	if source.Buf != nil {
		t.Fatal("push retained ownership in the source buffer")
	}

	var got bin.Buffer
	if err := buffered.Recv(context.Background(), &got); err != nil {
		t.Fatalf("Recv pending frame: %v", err)
	}
	if len(got.Buf) != len(backing) || &got.Buf[0] != &backing[0] {
		t.Fatal("pending frame was copied instead of transferring its backing")
	}
	if len(buffered.pending) != 0 || cap(buffered.pending) != 0 {
		t.Fatalf("consumed pending ownership retained: len=%d cap=%d", len(buffered.pending), cap(buffered.pending))
	}
}

func TestBufferedExchangeLargeTrailingMsgsAckReleasesFrameBeforeNextRecv(t *testing.T) {
	encodeUnencrypted := func(msg bin.Encoder, msgID int64) []byte {
		var payload bin.Buffer
		if err := msg.Encode(&payload); err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		var frame bin.Buffer
		if err := (tgproto.UnencryptedMessage{MessageID: msgID, MessageData: payload.Raw()}).Encode(&frame); err != nil {
			t.Fatalf("encode unencrypted frame: %v", err)
		}
		return frame.Copy()
	}
	intermediate := func(frame []byte) []byte {
		packet := make([]byte, bin.Word+len(frame))
		binary.LittleEndian.PutUint32(packet, uint32(len(frame)))
		copy(packet[bin.Word:], frame)
		return packet
	}

	// Make the ignored ack larger than the per-codec retained-buffer threshold. The following
	// small req_pq frame forces bufferedConn to cross the next-Recv ownership boundary while the
	// same destination bin.Buffer is reused.
	ids := make([]int64, 300_000)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	ackFrame := encodeUnencrypted(&mt.MsgsAck{MsgIDs: ids}, 4)
	reqFrame := encodeUnencrypted(&mt.ReqPqMultiRequest{}, 8)
	packet := append(intermediate(ackFrame), intermediate(reqFrame)...)
	budget := newInboundFrameBudget(2 * int64(len(ackFrame)))
	conn, _ := newFrameBudgetTestTransport(packet, &quickAckIntermediateCodec{}, budget)
	buffered := newBufferedConn(conn)

	var got bin.Buffer
	if err := buffered.Recv(context.Background(), &got); err != nil {
		t.Fatalf("Recv after large msgs_ack: %v", err)
	}
	if id, ok := unencryptedPayloadID(&got); !ok || id != mt.ReqPqMultiRequestTypeID {
		t.Fatalf("returned frame type = 0x%x ok=%v, want req_pq_multi", id, ok)
	}
	if used, want := budget.usedBytes(), 2*int64(len(reqFrame)); used != want {
		t.Fatalf("inbound budget after skipped ack = %d, want only next frame %d", used, want)
	}
	if cap(got.Buf) >= len(ackFrame)/2 {
		t.Fatalf("large ignored ack backing retained by next frame: cap=%d ack=%d", cap(got.Buf), len(ackFrame))
	}
	conn.releaseInboundFrame()
	if used := budget.usedBytes(); used != 0 {
		t.Fatalf("inbound budget after final ownership release = %d, want 0", used)
	}
}

type ackingExchangeConn struct {
	transport.Conn
	t *testing.T
}

func (c *ackingExchangeConn) Recv(ctx context.Context, b *bin.Buffer) error {
	if err := c.Conn.Recv(ctx, b); err != nil {
		return err
	}
	c.ackHandshakeMessage(b)
	return nil
}

func (c *ackingExchangeConn) ackHandshakeMessage(frame *bin.Buffer) {
	var msg tgproto.UnencryptedMessage
	copy := &bin.Buffer{Buf: frame.Copy()}
	if err := msg.Decode(copy); err != nil {
		return
	}
	payload := &bin.Buffer{Buf: msg.MessageData}
	id, err := payload.PeekID()
	if err != nil {
		return
	}
	switch id {
	case mt.ResPQTypeID, mt.ServerDHParamsOkTypeID:
	default:
		return
	}

	var ackPayload bin.Buffer
	if err := (&mt.MsgsAck{MsgIDs: []int64{msg.MessageID}}).Encode(&ackPayload); err != nil {
		c.t.Fatalf("encode msgs_ack: %v", err)
	}
	var ackFrame bin.Buffer
	if err := (tgproto.UnencryptedMessage{
		MessageID:   int64(tgproto.NewMessageID(time.Now(), tgproto.MessageFromClient)),
		MessageData: ackPayload.Raw(),
	}).Encode(&ackFrame); err != nil {
		c.t.Fatalf("encode msgs_ack frame: %v", err)
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Conn.Send(sendCtx, &ackFrame); err != nil {
		c.t.Fatalf("send msgs_ack: %v", err)
	}
}

func TestReconnectFakeReqPQThenEncryptedFrame(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})

	firstConn, auth, cipher := dialHandshake(t, addr, dc, pub)
	_ = firstConn.Close()

	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial reconnect: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("transport reconnect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var reqPayload bin.Buffer
	nonce, err := randInt128ForTest()
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	if err := (&mt.ReqPqMultiRequest{Nonce: nonce}).Encode(&reqPayload); err != nil {
		t.Fatalf("encode req_pq_multi: %v", err)
	}
	var fakeReq bin.Buffer
	if err := (tgproto.UnencryptedMessage{
		MessageID:   int64(tgproto.NewMessageID(time.Now(), tgproto.MessageFromClient)),
		MessageData: reqPayload.Raw(),
	}).Encode(&fakeReq); err != nil {
		t.Fatalf("encode fake req_pq: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := conn.Send(ctx, &fakeReq); err != nil {
		cancel()
		t.Fatalf("send fake req_pq: %v", err)
	}
	cancel()

	msgGen := tgproto.NewMessageIDGen(time.Now)
	sendEncryptedWithSeq(t, conn, cipher, auth, msgGen.New(tgproto.MessageFromClient), 1, &mt.PingRequest{PingID: 7})

	var resPQFrame bin.Buffer
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err = conn.Recv(ctx, &resPQFrame)
	cancel()
	if err != nil {
		t.Fatalf("recv resPQ: %v", err)
	}
	var plain tgproto.UnencryptedMessage
	if err := plain.Decode(&resPQFrame); err != nil {
		t.Fatalf("decode resPQ frame: %v", err)
	}
	if id, err := (&bin.Buffer{Buf: plain.MessageData}).PeekID(); err != nil || id != mt.ResPQTypeID {
		t.Fatalf("resPQ payload id = %#x err=%v, want %#x", id, err, mt.ResPQTypeID)
	}

	got := collectReplies(t, conn, cipher, auth.AuthKey, mt.PongTypeID)
	mustHave(t, got, mt.PongTypeID, "pong after fake req_pq reconnect")
}

func randInt128ForTest() (v bin.Int128, err error) {
	_, err = rand.Read(v[:])
	return v, err
}
