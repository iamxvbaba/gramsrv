package rpc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	telegramloginapp "telesrv/internal/app/telegramlogin"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type telegramLoginBotPermissionAdapter struct{ bots BotsService }

func (a telegramLoginBotPermissionAdapter) AllowBotSendMessage(ctx context.Context, botUserID, userID int64, fromRequest bool) (bool, error) {
	return a.bots.AllowSendMessage(ctx, userID, botUserID, fromRequest)
}

type telegramLoginRPCFixture struct {
	ctx      context.Context
	service  *telegramloginapp.Service
	router   *Router
	user     domain.User
	intruder domain.User
	bot      domain.User
	client   telegramloginapp.ClientCredentials
	redirect string
}

func newTelegramLoginRPCFixture(t *testing.T) *telegramLoginRPCFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	user, err := users.Create(ctx, domain.User{Phone: "+15551001", FirstName: "Alice", LastName: "Example", Username: "alice", AccessHash: 11})
	if err != nil {
		t.Fatal(err)
	}
	intruder, err := users.Create(ctx, domain.User{Phone: "+15551002", FirstName: "Mallory", Username: "mallory", AccessHash: 13})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := users.Create(ctx, domain.User{FirstName: "Login Bot", Username: "login_rpc_bot", AccessHash: 12, Bot: true, BotInfoVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	sealKey := make([]byte, 32)
	sealKey[0] = 3
	sealer, err := telegramloginapp.NewCodeSealer("test", map[string][]byte{"test": sealKey})
	if err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	pepper[0] = 4
	service, err := telegramloginapp.NewService(memory.NewTelegramLoginStore(nil), sealer, telegramloginapp.Config{
		Issuer: "https://oauth.test", AppScheme: "telesrv", ClientSecretPepper: pepper,
		Now: func() time.Time { return time.Unix(1_780_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := service.CreateClient(ctx, bot.ID, domain.TelegramLoginSigningRS256)
	if err != nil {
		t.Fatal(err)
	}
	redirect := "https://rp.test/callback"
	if _, err := service.AddAllowedURL(ctx, bot.ID, domain.TelegramLoginAllowedRedirectURI, redirect); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddAllowedURL(ctx, bot.ID, domain.TelegramLoginAllowedWebOrigin, "https://rp.test"); err != nil {
		t.Fatal(err)
	}
	router := New(Config{}, Deps{Users: appusers.NewService(users), TelegramLogin: service}, zaptest.NewLogger(t), clock.System)
	return &telegramLoginRPCFixture{ctx: WithUserID(ctx, user.ID), service: service, router: router, user: user, intruder: intruder, bot: bot, client: client, redirect: redirect}
}

func (f *telegramLoginRPCFixture) authorization(t *testing.T, match bool) telegramloginapp.CreatedAuthorization {
	t.Helper()
	challenge, err := telegramloginapp.PKCEChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.service.CreateAuthorization(f.ctx, telegramloginapp.CreateAuthorizationParams{
		ClientID: f.client.Client.ClientID, RedirectURI: f.redirect, ResponseType: "code",
		Scope: "openid profile telegram:bot_access", CodeChallenge: challenge, CodeChallengeMethod: "S256",
		IncludeMatchCodes: match, MatchCodesFirst: match,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestTelegramLoginRPCsAcrossExactLayerProfiles(t *testing.T) {
	for profile := tlprofile.Profile225; profile <= tlprofile.Profile228; profile++ {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			f := newTelegramLoginRPCFixture(t)

			approve := f.authorization(t, true)
			// TDesktop normalizes the configured telesrv:// launcher to the
			// official internal tg://oauth form before invoking MTProto.
			canonicalURL := strings.Replace(approve.DeepLink, "telesrv://", "tg://", 1)
			request := &tg.MessagesRequestURLAuthRequest{}
			request.SetURL(canonicalURL)
			result, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, request)
			if method != "messages.requestUrlAuth" {
				t.Fatalf("method = %q", method)
			}
			prompt, ok := dispatchCanonicalValue(result).(*tg.URLAuthResultRequest)
			if !ok || prompt.Bot.GetID() != f.bot.ID || !prompt.RequestWriteAccess || len(prompt.MatchCodes) != 5 {
				t.Fatalf("request result = %#v", dispatchCanonicalValue(result))
			}

			checked, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, &tg.MessagesCheckURLAuthMatchCodeRequest{
				URL: canonicalURL, MatchCode: approve.Request.MatchCode,
			})
			if method != "messages.checkUrlAuthMatchCode" || dispatchCanonicalValue(checked) != true {
				t.Fatalf("check result = %#v method=%q", dispatchCanonicalValue(checked), method)
			}
			accept := &tg.MessagesAcceptURLAuthRequest{WriteAllowed: true}
			accept.SetURL(canonicalURL)
			accept.SetMatchCode(approve.Request.MatchCode)
			accepted, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, accept)
			if method != "messages.acceptUrlAuth" {
				t.Fatalf("accept method = %q", method)
			}
			if _, ok := dispatchCanonicalValue(accepted).(*tg.URLAuthResultAccepted); !ok {
				t.Fatalf("accept result = %#v", dispatchCanonicalValue(accepted))
			}

			decline := f.authorization(t, false)
			declined, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, &tg.MessagesDeclineURLAuthRequest{URL: decline.DeepLink})
			if method != "messages.declineUrlAuth" || dispatchCanonicalValue(declined) != true {
				t.Fatalf("decline result = %#v method=%q", dispatchCanonicalValue(declined), method)
			}

			listed, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, &tg.AccountGetWebAuthorizationsRequest{})
			web, ok := dispatchCanonicalValue(listed).(*tg.AccountWebAuthorizations)
			if method != "account.getWebAuthorizations" || !ok || len(web.Authorizations) != 1 || web.Authorizations[0].BotID != f.bot.ID {
				t.Fatalf("getWebAuthorizations = %#v method=%q", dispatchCanonicalValue(listed), method)
			}
			reset, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, &tg.AccountResetWebAuthorizationRequest{Hash: web.Authorizations[0].Hash})
			if method != "account.resetWebAuthorization" || dispatchCanonicalValue(reset) != true {
				t.Fatalf("resetWebAuthorization = %#v method=%q", dispatchCanonicalValue(reset), method)
			}

			const nativeCallback = "bedolaga://telegram-login"
			if _, err := f.service.AddNativeApp(f.ctx, f.bot.ID, domain.TelegramLoginNativeAndroid,
				"dev.bedolaga.demo", strings.Repeat("A", 64), nativeCallback, "Bedolaga Android Demo"); err != nil {
				t.Fatal(err)
			}
			challenge, err := telegramloginapp.PKCEChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
			if err != nil {
				t.Fatal(err)
			}
			native, err := f.service.CreateAuthorization(f.ctx, telegramloginapp.CreateAuthorizationParams{
				ClientID: f.client.Client.ClientID, RedirectURI: nativeCallback, ResponseType: "code",
				Scope: "profile", CodeChallenge: challenge, CodeChallengeMethod: "S256",
				NativePlatform: domain.TelegramLoginNativeAndroid, IncludeMatchCodes: true, MatchCodesFirst: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			nativeRequest := &tg.MessagesRequestURLAuthRequest{}
			nativeRequest.SetURL(native.DeepLink)
			nativeResult, _ := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, nativeRequest)
			nativePrompt, ok := dispatchCanonicalValue(nativeResult).(*tg.URLAuthResultRequest)
			if !ok || !nativePrompt.IsApp || nativePrompt.VerifiedAppName != "Bedolaga Android Demo" || len(nativePrompt.MatchCodes) != 5 {
				t.Fatalf("native request result = %#v", dispatchCanonicalValue(nativeResult))
			}
			nativeAccept := &tg.MessagesAcceptURLAuthRequest{}
			nativeAccept.SetURL(native.DeepLink)
			nativeAccept.SetMatchCode(native.Request.MatchCode)
			nativeAcceptedResult, _ := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, nativeAccept)
			nativeAccepted, ok := dispatchCanonicalValue(nativeAcceptedResult).(*tg.URLAuthResultAccepted)
			if !ok || !strings.HasPrefix(nativeAccepted.URL, nativeCallback+"?code=") {
				t.Fatalf("native accepted result = %#v", dispatchCanonicalValue(nativeAcceptedResult))
			}
			nativeRetryResult, _ := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, nativeRequest)
			nativeRetry, ok := dispatchCanonicalValue(nativeRetryResult).(*tg.URLAuthResultAccepted)
			if !ok || nativeRetry.URL != nativeAccepted.URL {
				t.Fatalf("native retry result = %#v, want URL %q", dispatchCanonicalValue(nativeRetryResult), nativeAccepted.URL)
			}

			const miniAppOrigin = "https://rp.test"
			miniApp, err := f.service.CreateAuthorization(f.ctx, telegramloginapp.CreateAuthorizationParams{
				ClientID: f.client.Client.ClientID, RedirectURI: miniAppOrigin + "/", ResponseType: "post_message",
				Scope: "openid profile", Origin: miniAppOrigin, InAppOrigin: miniAppOrigin,
				Source: domain.TelegramLoginRequestMiniApp, IncludeMatchCodes: true, MatchCodesFirst: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			miniAppRequest := &tg.MessagesRequestURLAuthRequest{}
			miniAppRequest.SetURL(miniApp.DeepLink)
			miniAppRequest.SetInAppOrigin(miniAppOrigin)
			miniAppResult, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, miniAppRequest)
			miniAppPrompt, ok := dispatchCanonicalValue(miniAppResult).(*tg.URLAuthResultRequest)
			if method != "messages.requestUrlAuth" || !ok || len(miniAppPrompt.MatchCodes) != 5 {
				t.Fatalf("mini-app request result = %#v method=%q", dispatchCanonicalValue(miniAppResult), method)
			}
			miniAppAccept := &tg.MessagesAcceptURLAuthRequest{}
			miniAppAccept.SetURL(miniApp.DeepLink)
			miniAppAccept.SetMatchCode(miniApp.Request.MatchCode)
			miniAppAcceptedResult, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, miniAppAccept)
			miniAppAccepted, ok := dispatchCanonicalValue(miniAppAcceptedResult).(*tg.URLAuthResultAccepted)
			if method != "messages.acceptUrlAuth" || !ok || !strings.HasPrefix(miniAppAccepted.URL, "https://oauth.test/inapp?token=") {
				t.Fatalf("mini-app accepted result = %#v method=%q", dispatchCanonicalValue(miniAppAcceptedResult), method)
			}
			miniAppRetryResult, _ := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, miniAppRequest)
			miniAppRetry, ok := dispatchCanonicalValue(miniAppRetryResult).(*tg.URLAuthResultAccepted)
			if !ok || miniAppRetry.URL != miniAppAccepted.URL {
				t.Fatalf("mini-app retry result = %#v, want URL %q", dispatchCanonicalValue(miniAppRetryResult), miniAppAccepted.URL)
			}
			resetAll, method := dispatchExactLayerRPCTest(t, f.router, f.ctx, profile, &tg.AccountResetWebAuthorizationsRequest{})
			if method != "account.resetWebAuthorizations" || dispatchCanonicalValue(resetAll) != true {
				t.Fatalf("resetWebAuthorizations = %#v method=%q", dispatchCanonicalValue(resetAll), method)
			}
		})
	}
}

func TestTelegramLoginApprovedDeepLinkRejectsAnotherUser(t *testing.T) {
	f := newTelegramLoginRPCFixture(t)
	created := f.authorization(t, false)
	accept := &tg.MessagesAcceptURLAuthRequest{}
	accept.SetURL(created.DeepLink)
	if _, err := f.router.onMessagesAcceptURLAuth(f.ctx, accept); err != nil {
		t.Fatal(err)
	}
	request := &tg.MessagesRequestURLAuthRequest{}
	request.SetURL(created.DeepLink)
	if _, err := f.router.onMessagesRequestURLAuth(WithUserID(context.Background(), f.intruder.ID), request); err == nil {
		t.Fatal("another user observed an approved deep link as accepted")
	}
}

func TestTelegramLoginMessageButtonRereadSignsAndGrantsWriteAccess(t *testing.T) {
	f := newBotAPIReceiveFixture(t, false)
	sealKey := make([]byte, 32)
	sealKey[0] = 7
	sealer, err := telegramloginapp.NewCodeSealer("test", map[string][]byte{"test": sealKey})
	if err != nil {
		t.Fatal(err)
	}
	pepper := make([]byte, 32)
	pepper[0] = 8
	loginStore := memory.NewTelegramLoginStore(telegramLoginBotPermissionAdapter{bots: f.router.deps.Bots})
	login, err := telegramloginapp.NewService(loginStore, sealer, telegramloginapp.Config{
		Issuer: "http://192.0.2.25:2401", AppScheme: "telesrv", AllowHTTP: true, ClientSecretPepper: pepper,
		Now: func() time.Time { return time.Unix(1_780_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := login.CreateClient(f.ctx, f.bot.ID, domain.TelegramLoginSigningRS256); err != nil {
		t.Fatal(err)
	}
	if _, err := login.AddAllowedURL(f.ctx, f.bot.ID, domain.TelegramLoginAllowedWebOrigin, "http://rp.test:3000"); err != nil {
		t.Fatal(err)
	}
	f.router.deps.TelegramLogin = login

	markup := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: [][]domain.MarkupButton{{{
		Type: domain.MarkupButtonLoginURL, Text: "Log in", URL: "http://rp.test:3000/login?next=%2Fhome", RequestWriteAccess: true,
	}}}}
	if _, err := f.router.BotAPISendMessage(f.ctx, f.bot.ID, f.owner.ID, "Authorize", nil, markup, false, false, 0); err != nil {
		t.Fatalf("BotAPISendMessage: %v", err)
	}
	history, err := f.messages.GetHistory(f.ctx, f.owner.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, Limit: 10,
	})
	if err != nil || len(history.Messages) == 0 {
		t.Fatalf("GetHistory: messages=%d err=%v", len(history.Messages), err)
	}
	message := history.Messages[0]
	if message.ReplyMarkup == nil || message.ReplyMarkup.Inline[0][0].LoginBotUserID != f.bot.ID {
		t.Fatalf("persisted login button = %#v", message.ReplyMarkup)
	}

	peer := &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}
	request := &tg.MessagesRequestURLAuthRequest{}
	request.SetPeer(peer)
	request.SetMsgID(message.ID)
	request.SetButtonID(0)
	requested, err := f.router.onMessagesRequestURLAuth(WithUserID(f.ctx, f.owner.ID), request)
	if err != nil {
		t.Fatalf("requestUrlAuth: %v", err)
	}
	prompt, ok := requested.(*tg.URLAuthResultRequest)
	if !ok || !prompt.RequestWriteAccess || prompt.Domain != "rp.test" {
		t.Fatalf("requestUrlAuth result = %#v", requested)
	}
	accept := &tg.MessagesAcceptURLAuthRequest{}
	accept.SetWriteAllowed(true)
	accept.SetPeer(peer)
	accept.SetMsgID(message.ID)
	accept.SetButtonID(0)
	accepted, err := f.router.onMessagesAcceptURLAuth(WithUserID(f.ctx, f.owner.ID), accept)
	if err != nil {
		t.Fatalf("acceptUrlAuth: %v", err)
	}
	final, ok := accepted.(*tg.URLAuthResultAccepted)
	if !ok || final.URL == "" {
		t.Fatalf("acceptUrlAuth result = %#v", accepted)
	}
	verifyLegacyTelegramLoginURL(t, final.URL, domain.FormatBotToken(f.bot.ID, "secret"), f.owner.ID)
	if allowed, err := f.router.deps.Bots.CanSendMessage(f.ctx, f.owner.ID, f.bot.ID); err != nil || !allowed {
		t.Fatalf("bot write permission = %v,%v", allowed, err)
	}
	web, err := login.ListWebAuthorizations(f.ctx, f.owner.ID)
	if err != nil || len(web) != 1 || !web[0].BotAccessGranted || web[0].Domain != "rp.test" {
		t.Fatalf("web authorizations = %#v err=%v", web, err)
	}

	// The server must re-read durable message state. A forged button id never
	// falls back to URL data supplied by the client.
	forged := &tg.MessagesAcceptURLAuthRequest{}
	forged.SetPeer(peer)
	forged.SetMsgID(message.ID)
	forged.SetButtonID(99)
	if _, err := f.router.onMessagesAcceptURLAuth(WithUserID(f.ctx, f.owner.ID), forged); err == nil {
		t.Fatal("forged button id was accepted")
	}
}

func TestTelegramLoginMarkupRequiresBotSender(t *testing.T) {
	f := newTelegramLoginRPCFixture(t)
	markup := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: [][]domain.MarkupButton{{{
		Type: domain.MarkupButtonLoginURL, Text: "Log in", URL: "https://rp.test/login",
	}}}}
	if err := f.router.prepareTelegramLoginMarkup(WithUserID(f.ctx, f.user.ID), f.user.ID, markup); !errors.Is(err, domain.ErrButtonTypeInvalid) {
		t.Fatalf("ordinary user login_url error = %v, want ErrButtonTypeInvalid", err)
	}
}

func verifyLegacyTelegramLoginURL(t *testing.T, raw, botToken string, wantUserID int64) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	provided := query.Get("hash")
	query.Del("hash")
	if query.Get("id") != strconv.FormatInt(wantUserID, 10) || query.Get("auth_date") == "" || query.Get("next") != "/home" {
		t.Fatalf("legacy login query = %#v", query)
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		if key != "next" { // Existing application query fields are not signed.
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+query.Get(key))
	}
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	_, _ = mac.Write([]byte(strings.Join(lines, "\n")))
	if !hmac.Equal([]byte(strings.ToLower(provided)), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		t.Fatalf("legacy login hash = %q, want %s", provided, hex.EncodeToString(mac.Sum(nil)))
	}
}
