package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/log/logzap"
	tdcrypto "github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/mtproxy"
	"github.com/iamxvbaba/td/mtproxy/obfuscator"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/transport"

	"telesrv/internal/mtprotoedge"
)

func TestExecutablePrivateMessageRoundTrip(t *testing.T) {
	exe := os.Getenv("TELESRV_TEST_EXE")
	if exe == "" {
		t.Skip("set TELESRV_TEST_EXE to run executable-level MTProto roundtrip")
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	if !filepath.IsAbs(exe) {
		exe = filepath.Join(repoRoot, exe)
	}
	exeAbs, err := filepath.Abs(exe)
	if err != nil {
		t.Fatalf("resolve executable path: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, exeAbs)
	cmd.Dir = repoRoot
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(),
		"TELESRV_LISTEN="+addr,
		"TELESRV_ADVERTISE_IP=127.0.0.1",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start executable: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	processDone := false
	t.Cleanup(func() {
		if processDone {
			return
		}
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	})
	select {
	case err := <-done:
		processDone = true
		t.Fatalf("executable exited before client test: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	case <-time.After(4 * time.Second):
	case <-ctx.Done():
		t.Fatalf("wait executable startup: %v\nstdout:\n%s\nstderr:\n%s", ctx.Err(), stdout.String(), stderr.String())
	}

	rsaKey, err := mtprotoedge.LoadOrGenerateRSAKey(filepath.Join(repoRoot, "data", "server_rsa.pem"))
	if err != nil {
		t.Fatalf("load rsa key: %v", err)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}

	newClient := func(storage *session.StorageMemory) *telegram.Client {
		opts := telegram.Options{
			PublicKeys:     []exchange.PublicKey{{RSA: &rsaKey.PublicKey}},
			Resolver:       noSecretObfuscatedResolver{addr: addr, dc: 2},
			DCList:         dcs.List{Options: []tg.DCOption{{ID: 2, IPAddress: host, Port: port, Static: true}}},
			Logger:         logzap.New(zaptest.NewLogger(t).Named("exe-client")),
			SessionStorage: storage,
			UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
		}
		return telegram.NewClient(1, "hash", opts)
	}
	messagesOf := func(history tg.MessagesMessagesClass) []tg.MessageClass {
		t.Helper()
		switch v := history.(type) {
		case *tg.MessagesMessages:
			return v.Messages
		case *tg.MessagesMessagesSlice:
			return v.Messages
		default:
			t.Fatalf("history = %T %+v, want messages", history, history)
			return nil
		}
	}
	signUp := func(storage *session.StorageMemory, phone, firstName string) tg.User {
		t.Helper()
		client := newClient(storage)
		var out tg.User
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			sent, err := raw.AuthSendCode(ctx, &tg.AuthSendCodeRequest{
				PhoneNumber: phone,
				APIID:       1,
				APIHash:     "hash",
				Settings:    tg.CodeSettings{},
			})
			if err != nil {
				return err
			}
			hash := sent.(*tg.AuthSentCode).PhoneCodeHash
			signInRes, err := raw.AuthSignIn(ctx, &tg.AuthSignInRequest{
				PhoneNumber:   phone,
				PhoneCodeHash: hash,
				PhoneCode:     "12345",
			})
			if err != nil {
				return err
			}
			if authz, ok := signInRes.(*tg.AuthAuthorization); ok {
				out = *(authz.User.(*tg.User))
				return nil
			}
			res, err := raw.AuthSignUp(ctx, &tg.AuthSignUpRequest{
				PhoneNumber:   phone,
				PhoneCodeHash: hash,
				FirstName:     firstName,
			})
			if err != nil {
				return err
			}
			authz := res.(*tg.AuthAuthorization)
			out = *(authz.User.(*tg.User))
			return nil
		}); err != nil {
			t.Fatalf("sign up %s: %v\nstdout:\n%s\nstderr:\n%s", firstName, err, stdout.String(), stderr.String())
		}
		return out
	}
	sendAndAssertHistory := func(storage *session.StorageMemory, to tg.User, body string, randomID int64, wantOut bool) {
		t.Helper()
		client := newClient(storage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			if _, err := raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer:     &tg.InputPeerUser{UserID: to.ID, AccessHash: to.AccessHash},
				Message:  body,
				RandomID: randomID,
			}); err != nil {
				return err
			}
			history, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:  &tg.InputPeerUser{UserID: to.ID, AccessHash: to.AccessHash},
				Limit: 10,
			})
			if err != nil {
				return err
			}
			msgs := messagesOf(history)
			if len(msgs) == 0 {
				t.Fatalf("history = %T %+v, want messages", history, history)
			}
			msg, ok := msgs[0].(*tg.Message)
			if !ok || msg.Message != body || msg.Out != wantOut {
				t.Fatalf("latest message = %#v, want out=%v text=%q", msgs[0], wantOut, body)
			}
			return nil
		}); err != nil {
			t.Fatalf("send %q: %v\nstdout:\n%s\nstderr:\n%s", body, err, stdout.String(), stderr.String())
		}
	}
	readLatest := func(storage *session.StorageMemory, from tg.User, body string, wantOut bool) {
		t.Helper()
		client := newClient(storage)
		if err := client.Run(ctx, func(ctx context.Context) error {
			raw := tg.NewClient(client)
			history, err := raw.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
				Peer:  &tg.InputPeerUser{UserID: from.ID, AccessHash: from.AccessHash},
				Limit: 10,
			})
			if err != nil {
				return err
			}
			msgs := messagesOf(history)
			if len(msgs) == 0 {
				t.Fatalf("history = %T %+v, want messages", history, history)
			}
			msg, ok := msgs[0].(*tg.Message)
			if !ok || msg.Message != body || msg.Out != wantOut {
				t.Fatalf("latest message = %#v, want out=%v text=%q", msgs[0], wantOut, body)
			}
			return nil
		}); err != nil {
			t.Fatalf("read latest %q: %v\nstdout:\n%s\nstderr:\n%s", body, err, stdout.String(), stderr.String())
		}
	}

	suffix := time.Now().UnixNano() % 100000000
	storageA := &session.StorageMemory{}
	storageB := &session.StorageMemory{}
	userA := signUp(storageA, fmt.Sprintf("+1555%08d", suffix), "ExeAlice")
	userB := signUp(storageB, fmt.Sprintf("+1556%08d", suffix), "ExeBob")

	sendAndAssertHistory(storageA, userB, "exe hello bob", time.Now().UnixNano(), true)
	readLatest(storageB, userA, "exe hello bob", false)
	sendAndAssertHistory(storageB, userA, "exe hi alice", time.Now().UnixNano()+1, true)
	readLatest(storageA, userB, "exe hi alice", false)
}

type noSecretObfuscatedResolver struct {
	addr string
	dc   int
}

func (r noSecretObfuscatedResolver) Primary(ctx context.Context, _ int, _ dcs.List) (transport.Conn, error) {
	return r.connect(ctx)
}

func (r noSecretObfuscatedResolver) MediaOnly(ctx context.Context, _ int, _ dcs.List) (transport.Conn, error) {
	return r.connect(ctx)
}

func (r noSecretObfuscatedResolver) CDN(ctx context.Context, _ int, _ dcs.List) (transport.Conn, error) {
	return r.connect(ctx)
}

func (r noSecretObfuscatedResolver) connect(ctx context.Context) (_ transport.Conn, rerr error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		return nil, err
	}
	defer func() {
		if rerr != nil {
			_ = conn.Close()
		}
	}()

	obfsConn := obfuscator.Obfuscated2(tdcrypto.DefaultRand(), conn)
	if err := obfsConn.Handshake(codec.IntermediateClientStart, r.dc, mtproxy.Secret{}); err != nil {
		return nil, err
	}
	proto := transport.NewProtocol(func() transport.Codec {
		return codec.NoHeader{Codec: codec.Intermediate{}}
	})
	return proto.Handshake(obfsConn)
}
