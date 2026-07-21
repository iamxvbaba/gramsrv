package bots

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	telegramloginapp "telesrv/internal/app/telegramlogin"
	"telesrv/internal/store/memory"
)

func newBotFatherTelegramLoginService(t *testing.T) *telegramloginapp.Service {
	t.Helper()
	sealKey := make([]byte, 32)
	sealKey[0] = 1
	sealer, err := telegramloginapp.NewCodeSealer("test", map[string][]byte{"test": sealKey})
	if err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	pepper[0] = 2
	service, err := telegramloginapp.NewService(memory.NewTelegramLoginStore(nil), sealer, telegramloginapp.Config{
		Issuer: "http://192.0.2.25:2404", AppScheme: "telesrv", AllowHTTP: true,
		ClientSecretPepper: pepper, Now: func() time.Time { return time.Unix(1_780_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestBotFatherTelegramLoginConfigurationFlow(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	svc.telegramLogin = newBotFatherTelegramLoginService(t)
	owner := newOwner(t, users, "+1090")
	bot, _, err := svc.CreateBot(context.Background(), owner.ID, "Login Demo", "login_demo_bot")
	if err != nil {
		t.Fatal(err)
	}

	if reply := sendToBotFather(t, svc, messages, owner, "/setlogin"); !strings.Contains(reply, "Choose a bot") {
		t.Fatalf("/setlogin reply = %q", reply)
	}
	created := sendToBotFather(t, svc, messages, owner, "@login_demo_bot")
	if !strings.Contains(created, "Client ID: "+strconv.FormatInt(bot.ID, 10)) || !strings.Contains(created, "only be shown once") {
		t.Fatalf("create login reply = %q", created)
	}
	secretMarker := "only be shown once:\n"
	secret := strings.SplitN(strings.SplitN(created, secretMarker, 2)[1], "\n", 2)[0]
	if len(secret) < 32 {
		t.Fatalf("client secret is unexpectedly short: %q", secret)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "add origin http://rp.example.test:3000"); !strings.Contains(reply, "Success!") {
		t.Fatalf("add origin reply = %q", reply)
	}

	sendToBotFather(t, svc, messages, owner, "/setlogin")
	sendToBotFather(t, svc, messages, owner, "login_demo_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "add redirect http://192.0.2.26:3000/auth/callback"); !strings.Contains(reply, "Success!") {
		t.Fatalf("add redirect reply = %q", reply)
	}

	sendToBotFather(t, svc, messages, owner, "/setlogin")
	sendToBotFather(t, svc, messages, owner, "login_demo_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "algorithm ES256"); !strings.Contains(reply, "ES256") {
		t.Fatalf("algorithm reply = %q", reply)
	}

	sendToBotFather(t, svc, messages, owner, "/setlogin")
	sendToBotFather(t, svc, messages, owner, "login_demo_bot")
	if reply := sendToBotFather(t, svc, messages, owner, "add ios dev.bedolaga.demo ABCDE12345 bedolaga://telegram-login Bedolaga iOS Demo"); !strings.Contains(reply, "Registered native app #") {
		t.Fatalf("add iOS app reply = %q", reply)
	}

	sendToBotFather(t, svc, messages, owner, "/setlogin")
	sendToBotFather(t, svc, messages, owner, "login_demo_bot")
	fingerprint := strings.Repeat("A", 64)
	if reply := sendToBotFather(t, svc, messages, owner, "add android dev.bedolaga.demo "+fingerprint+" bedolaga://android-login Bedolaga Android Demo"); !strings.Contains(reply, "Registered native app #") {
		t.Fatalf("add Android app reply = %q", reply)
	}

	sendToBotFather(t, svc, messages, owner, "/logininfo")
	info := sendToBotFather(t, svc, messages, owner, "login_demo_bot")
	for _, want := range []string{"Signing algorithm: ES256", "web_origin http://rp.example.test:3000", "redirect_uri http://192.0.2.26:3000/auth/callback", "dev.bedolaga.demo", "Bedolaga iOS Demo", "Bedolaga Android Demo"} {
		if !strings.Contains(info, want) {
			t.Fatalf("login info = %q, missing %q", info, want)
		}
	}
	if strings.Contains(info, secret) {
		t.Fatal("/logininfo leaked the one-time client secret")
	}

	sendToBotFather(t, svc, messages, owner, "/resetloginsecret")
	rotated := sendToBotFather(t, svc, messages, owner, "login_demo_bot")
	if !strings.Contains(rotated, "previous OIDC Client Secret") || strings.Contains(rotated, secret) {
		t.Fatalf("rotate reply = %q", rotated)
	}
}
