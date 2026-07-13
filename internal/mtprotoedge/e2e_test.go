package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/log/logzap"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/transport"

	"telesrv/internal/app/auth"
	"telesrv/internal/app/updates"
	"telesrv/internal/app/users"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

// TestTelegramClientEndToEnd 是连接层的最强端到端验证：用 gotd/td 的完整
// telegram.Client（而非底层 cipher）连本地 mtprotoedge，client 自动经
// invokeWithLayer(initConnection(help.getConfig)) 完成初始化，并取得含本地 DC 的 Config。
func TestTelegramClientEndToEnd(t *testing.T) {
	const dc = 2

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcpAddr := ln.Addr().(*net.TCPAddr)

	userStore := memory.NewUserStore()
	authzStore := memory.NewAuthorizationStore()
	authKeyStore := memory.NewAuthKeyStore()
	deps := rpc.Deps{
		Auth:    auth.NewService(userStore, authzStore, memory.NewCodeStore(), authKeyStore, memory.NewTempAuthKeyBindingStore(authKeyStore), "12345"),
		Users:   users.NewService(userStore),
		Updates: updates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
	}
	router := rpc.New(rpc.Config{DC: dc, IP: tcpAddr.IP.String(), Port: tcpAddr.Port}, deps, zaptest.NewLogger(t), clock.System)
	srv := New(Options{Logger: zaptest.NewLogger(t), DC: dc, RSAKey: rsaKey, AuthKeys: authKeyStore, RPC: router})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	opts := telegram.Options{
		PublicKeys:     []exchange.PublicKey{{RSA: &rsaKey.PublicKey}},
		Resolver:       dcs.Plain(dcs.PlainOptions{Protocol: transport.Intermediate}),
		DCList:         dcs.List{Options: []tg.DCOption{{ID: dc, IPAddress: tcpAddr.IP.String(), Port: tcpAddr.Port, Static: true}}},
		Logger:         logzap.New(zaptest.NewLogger(t).Named("client")),
		SessionStorage: &session.StorageMemory{},
		UpdateHandler:  telegram.UpdateHandlerFunc(func(context.Context, tg.UpdatesClass) error { return nil }),
	}
	client := telegram.NewClient(1, "hash", opts)

	if err := client.Run(ctx, func(ctx context.Context) error {
		cfg, err := tg.NewClient(client).HelpGetConfig(ctx)
		if err != nil {
			return err
		}
		if cfg.ThisDC != dc {
			t.Errorf("config.ThisDC = %d, want %d", cfg.ThisDC, dc)
		}
		// 不下发 DCOptions：客户端使用自己的 DCList / 写死 static 地址。
		if len(cfg.DCOptions) != 0 {
			t.Errorf("config.DCOptions = %+v, want empty", cfg.DCOptions)
		}
		return nil
	}); err != nil {
		t.Fatalf("telegram client run: %v", err)
	}

	cancel()
	if err := <-serveErr; err != nil {
		t.Errorf("serve: %v", err)
	}
}
