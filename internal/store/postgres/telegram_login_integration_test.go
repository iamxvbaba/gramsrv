package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

func telegramLoginPGHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func TestTelegramLoginStorePostgresAtomicStateAndCodeConsumption(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := now.UnixNano() % 1_000_000_000

	users := NewUserStore(pool)
	bots := NewBotStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: suffix + 101,
		Phone:      fmt.Sprintf("1777%09d", suffix),
		FirstName:  "OIDC Owner",
	})
	if err != nil {
		t.Fatalf("create oidc owner: %v", err)
	}
	bot, _, err := bots.CreateBotAccount(ctx, domain.User{
		AccessHash: suffix + 102,
		FirstName:  "OIDC Test Bot",
		Username:   fmt.Sprintf("oidc_%09d_bot", suffix),
	}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "bot-secret"})
	if err != nil {
		t.Fatalf("create oidc bot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id IN ($1,$2)", owner.ID, bot.ID)
	})

	store := NewTelegramLoginStore(pool)
	client, err := store.UpsertTelegramLoginClient(ctx, domain.TelegramLoginClient{
		BotUserID:        bot.ID,
		ClientID:         fmt.Sprintf("%d", bot.ID),
		SecretHash:       telegramLoginPGHash("client-secret"),
		SecretVersion:    1,
		SigningAlgorithm: domain.TelegramLoginSigningRS256,
		Enabled:          true,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		t.Fatalf("upsert oidc client: %v", err)
	}
	redirectURI := fmt.Sprintf("https://rp-%d.example/callback", suffix)
	origin := fmt.Sprintf("https://rp-%d.example", suffix)
	if _, err := store.AddTelegramLoginAllowedURL(ctx, domain.TelegramLoginAllowedURL{
		BotUserID: client.BotUserID, Kind: domain.TelegramLoginAllowedRedirectURI,
		NormalizedURL: redirectURI, CreatedAt: now,
	}); err != nil {
		t.Fatalf("add redirect: %v", err)
	}
	if _, err := store.AddTelegramLoginAllowedURL(ctx, domain.TelegramLoginAllowedURL{
		BotUserID: client.BotUserID, Kind: domain.TelegramLoginAllowedWebOrigin,
		NormalizedURL: origin, CreatedAt: now,
	}); err != nil {
		t.Fatalf("add web origin: %v", err)
	}

	newRequest := func(label string) domain.TelegramLoginRequest {
		request, err := store.CreateTelegramLoginRequest(ctx, domain.TelegramLoginRequest{
			RequestTokenHash:    telegramLoginPGHash("request-" + label),
			BrowserTokenHash:    telegramLoginPGHash("browser-" + label),
			BotUserID:           bot.ID,
			ClientID:            client.ClientID,
			SigningAlgorithm:    client.SigningAlgorithm,
			Source:              domain.TelegramLoginRequestWeb,
			ResponseType:        "code",
			RedirectURI:         redirectURI,
			Origin:              origin,
			Domain:              fmt.Sprintf("rp-%d.example", suffix),
			Scopes:              []domain.TelegramLoginScope{domain.TelegramLoginScopeOpenID, domain.TelegramLoginScopeProfile, domain.TelegramLoginScopeBotAccess},
			State:               "state",
			Nonce:               "nonce",
			CodeChallenge:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CodeChallengeMethod: "S256",
			Browser:             "Firefox",
			Platform:            "Windows",
			IP:                  "192.0.2.10",
			Region:              "Test Region",
			MatchCodes:          []string{"🟢", "🔵", "🟠"},
			MatchCode:           "🔵",
			MatchCodesFirst:     true,
			Status:              domain.TelegramLoginRequestPending,
			CreatedAt:           now,
			ExpiresAt:           now.Add(5 * time.Minute),
		})
		if err != nil {
			t.Fatalf("create request %s: %v", label, err)
		}
		return request
	}

	request := newRequest(fmt.Sprintf("race-%d", suffix))
	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		_, _, err := store.ApproveTelegramLoginRequest(ctx, domain.TelegramLoginApproval{
			RequestID:    request.ID,
			Identity:     domain.TelegramLoginIdentitySnapshot{UserID: owner.ID, Name: owner.FirstName, GivenName: owner.FirstName},
			WriteAllowed: true,
			MatchCode:    request.MatchCode, ApprovedAt: now.Add(time.Second),
		}, suffix+10_000)
		errs <- err
	}()
	go func() {
		<-start
		_, err := store.DeclineTelegramLoginRequest(ctx, request.ID, owner.ID, now.Add(time.Second))
		errs <- err
	}()
	close(start)
	var success, conflict int
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			success++
		case errors.Is(err, domain.ErrTelegramLoginRequestConflict):
			conflict++
		default:
			t.Fatalf("accept/decline race error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("accept/decline success=%d conflict=%d, want 1/1", success, conflict)
	}

	codeRequest := newRequest(fmt.Sprintf("code-%d", suffix))
	_, web, err := store.ApproveTelegramLoginRequest(ctx, domain.TelegramLoginApproval{
		RequestID:    codeRequest.ID,
		Identity:     domain.TelegramLoginIdentitySnapshot{UserID: owner.ID, Name: owner.FirstName, GivenName: owner.FirstName},
		WriteAllowed: true,
		MatchCode:    codeRequest.MatchCode, ApprovedAt: now.Add(2 * time.Second),
	}, suffix+20_000)
	if err != nil {
		t.Fatalf("approve code request: %v", err)
	}
	canSend, err := bots.CanBotSendMessage(ctx, bot.ID, owner.ID)
	if err != nil || !canSend {
		t.Fatalf("bot access after atomic approval = %v,%v", canSend, err)
	}
	code := domain.TelegramLoginAuthorizationCode{
		RequestID:  codeRequest.ID,
		CodeHash:   telegramLoginPGHash(fmt.Sprintf("code-%d", suffix)),
		SealedCode: append(make([]byte, 32), 1),
		SealNonce:  make([]byte, 12),
		SealKeyID:  "integration-key",
		IssuedAt:   now.Add(3 * time.Second),
		ExpiresAt:  now.Add(time.Minute),
	}
	if _, err := store.PutTelegramLoginAuthorizationCode(ctx, code); err != nil {
		t.Fatalf("put code: %v", err)
	}
	exchange := domain.TelegramLoginCodeExchange{
		CodeHash: code.CodeHash, ClientID: client.ClientID, ClientSecretVersion: client.SecretVersion,
		RedirectURI: codeRequest.RedirectURI, CodeChallenge: codeRequest.CodeChallenge, Now: now.Add(4 * time.Second),
	}
	start = make(chan struct{})
	errs = make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, _, err := store.ConsumeTelegramLoginAuthorizationCode(ctx, exchange)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflict = 0, 0
	for err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, domain.ErrTelegramLoginCodeConsumed):
			conflict++
		default:
			t.Fatalf("code consume race error: %v", err)
		}
	}
	if success != 1 || conflict != 7 {
		t.Fatalf("code consume success=%d consumed=%d, want 1/7", success, conflict)
	}

	miniRequest, err := store.CreateTelegramLoginRequest(ctx, domain.TelegramLoginRequest{
		RequestTokenHash: telegramLoginPGHash(fmt.Sprintf("mini-request-%d", suffix)),
		BrowserTokenHash: telegramLoginPGHash(fmt.Sprintf("mini-browser-%d", suffix)),
		BotUserID:        bot.ID, ClientID: client.ClientID, SigningAlgorithm: client.SigningAlgorithm,
		Source: domain.TelegramLoginRequestMiniApp, ResponseType: "post_message",
		RedirectURI: origin + "/", Origin: origin, InAppOrigin: origin, Domain: fmt.Sprintf("rp-%d.example", suffix),
		Scopes:  []domain.TelegramLoginScope{domain.TelegramLoginScopeOpenID, domain.TelegramLoginScopeProfile},
		Browser: "Telegram Mini App", Platform: "Telegram Mini App", IP: "192.0.2.11", Region: "Test Region",
		MatchCodes: []string{"🟢", "🔵", "🟠"}, MatchCode: "🔵", MatchCodesFirst: true,
		Status: domain.TelegramLoginRequestPending, CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create mini-app request: %v", err)
	}
	if _, _, err := store.ApproveTelegramLoginRequest(ctx, domain.TelegramLoginApproval{
		RequestID: miniRequest.ID,
		Identity:  domain.TelegramLoginIdentitySnapshot{UserID: owner.ID, Name: owner.FirstName, GivenName: owner.FirstName},
		MatchCode: miniRequest.MatchCode, ApprovedAt: now.Add(5 * time.Second),
	}, suffix+25_000); err != nil {
		t.Fatalf("approve mini-app request: %v", err)
	}
	directToken := domain.TelegramLoginAuthorizationCode{
		RequestID: miniRequest.ID, CodeHash: telegramLoginPGHash(fmt.Sprintf("mini-token-%d", suffix)),
		SealedCode: append(make([]byte, 32), 1), SealNonce: make([]byte, 12), SealKeyID: "integration-key",
		IssuedAt: now.Add(6 * time.Second), ExpiresAt: now.Add(time.Minute),
	}
	if _, err := store.PutTelegramLoginAuthorizationCode(ctx, directToken); err != nil {
		t.Fatalf("put mini-app token: %v", err)
	}
	start = make(chan struct{})
	errs = make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, _, err := store.ConsumeTelegramLoginDirectToken(ctx, directToken.CodeHash, origin, now.Add(7*time.Second))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	success, conflict = 0, 0
	for err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, domain.ErrTelegramLoginCodeConsumed):
			conflict++
		default:
			t.Fatalf("mini-app token consume race error: %v", err)
		}
	}
	if success != 1 || conflict != 7 {
		t.Fatalf("mini-app token consume success=%d consumed=%d, want 1/7", success, conflict)
	}

	if revoked, err := store.RevokeTelegramLoginWebAuthorization(ctx, owner.ID, web.Hash, now.Add(5*time.Second)); err != nil || !revoked {
		t.Fatalf("revoke web authorization = %v,%v", revoked, err)
	}
	if listed, err := store.ListTelegramLoginWebAuthorizations(ctx, owner.ID); err != nil {
		t.Fatalf("list web authorizations: %v", err)
	} else {
		for _, got := range listed {
			if got.Hash == web.Hash {
				t.Fatalf("revoked web authorization still listed: %#v", got)
			}
		}
	}
	assertTelegramLoginConfigDeleteTakesClientLock(t, pool, client.BotUserID, func() (bool, error) {
		return store.DeleteTelegramLoginAllowedURL(ctx, client.BotUserID, domain.TelegramLoginAllowedRedirectURI, redirectURI)
	})
}

func TestTelegramLoginStorePostgresNativeCallbackAndRetention(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	suffix := now.UnixNano() % 1_000_000_000

	users := NewUserStore(pool)
	bots := NewBotStore(pool)
	owner, err := users.Create(ctx, domain.User{
		AccessHash: suffix + 301, Phone: fmt.Sprintf("1666%09d", suffix), FirstName: "Native Owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := bots.CreateBotAccount(ctx, domain.User{
		AccessHash: suffix + 302, FirstName: "Native Login Bot", Username: fmt.Sprintf("native_%09d_bot", suffix),
	}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "native-bot-secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id IN ($1,$2)", owner.ID, bot.ID) })

	store := NewTelegramLoginStore(pool)
	client, err := store.CreateTelegramLoginClient(ctx, domain.TelegramLoginClient{
		BotUserID: bot.ID, ClientID: fmt.Sprintf("%d", bot.ID), SecretHash: telegramLoginPGHash("native-secret"),
		SecretVersion: 1, SigningAlgorithm: domain.TelegramLoginSigningRS256, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	const callbackURI = "bedolaga://telegram-login"
	nativeApp, err := store.UpsertTelegramLoginNativeApp(ctx, domain.TelegramLoginNativeApp{
		BotUserID: bot.ID, Platform: domain.TelegramLoginNativeAndroid, ApplicationID: "dev.bedolaga.demo",
		VerificationID: strings.Repeat("A", 64), CallbackURI: callbackURI, VerifiedDisplayName: "Bedolaga Demo",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	createRequest := func(label string) domain.TelegramLoginRequest {
		t.Helper()
		request, err := store.CreateTelegramLoginRequest(ctx, domain.TelegramLoginRequest{
			RequestTokenHash: telegramLoginPGHash("native-request-" + label), BrowserTokenHash: telegramLoginPGHash("native-browser-" + label),
			BotUserID: bot.ID, ClientID: client.ClientID, SigningAlgorithm: client.SigningAlgorithm,
			Source: domain.TelegramLoginRequestNative, ResponseType: "code", RedirectURI: callbackURI,
			Domain: "dev.bedolaga.demo", Scopes: []domain.TelegramLoginScope{domain.TelegramLoginScopeOpenID, domain.TelegramLoginScopeProfile},
			CodeChallenge: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CodeChallengeMethod: "S256",
			Browser: "TelegramLogin/Android", Platform: "Android", IP: "192.0.2.20", Region: "Test Region",
			IsApp: true, VerifiedAppName: "Bedolaga Demo", MatchCodes: []string{}, Status: domain.TelegramLoginRequestPending,
			CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
		})
		if err != nil {
			t.Fatalf("create native request: %v", err)
		}
		return request
	}
	approve := func(request domain.TelegramLoginRequest, hash int64) domain.TelegramLoginWebAuthorization {
		t.Helper()
		_, web, err := store.ApproveTelegramLoginRequest(ctx, domain.TelegramLoginApproval{
			RequestID: request.ID, Identity: domain.TelegramLoginIdentitySnapshot{UserID: owner.ID, Name: "Native Owner", GivenName: "Native"},
			ApprovedAt: now.Add(time.Second),
		}, hash)
		if err != nil {
			t.Fatalf("approve native request: %v", err)
		}
		return web
	}

	revokedRequest := createRequest(fmt.Sprintf("revoked-%d", suffix))
	revokedWeb := approve(revokedRequest, suffix+30_000)
	code := domain.TelegramLoginAuthorizationCode{
		RequestID: revokedRequest.ID, CodeHash: telegramLoginPGHash(fmt.Sprintf("native-code-%d", suffix)),
		SealedCode: append(make([]byte, 32), 1), SealNonce: make([]byte, 12), SealKeyID: "integration-key",
		IssuedAt: now.Add(2 * time.Second), ExpiresAt: now.Add(time.Minute),
	}
	if _, err := store.PutTelegramLoginAuthorizationCode(ctx, code); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ConsumeTelegramLoginAuthorizationCode(ctx, domain.TelegramLoginCodeExchange{
		CodeHash: code.CodeHash, ClientID: client.ClientID, ClientSecretVersion: client.SecretVersion,
		RedirectURI: callbackURI, CodeChallenge: revokedRequest.CodeChallenge, Now: now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("consume native code: %v", err)
	}
	if ok, err := store.RevokeTelegramLoginWebAuthorization(ctx, owner.ID, revokedWeb.Hash, now.Add(4*time.Second)); err != nil || !ok {
		t.Fatalf("revoke native authorization = %v,%v", ok, err)
	}

	activeRequest := createRequest(fmt.Sprintf("active-%d", suffix))
	activeWeb := approve(activeRequest, suffix+40_000)
	deleted, err := store.DeleteExpiredTelegramLoginArtifacts(ctx, now.Add(2*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if deleted < 2 {
		t.Fatalf("retention deleted=%d, want at least code and revoked request", deleted)
	}
	if _, found, _ := store.GetTelegramLoginRequest(ctx, revokedRequest.ID); found {
		t.Fatal("revoked native request survived retention")
	}
	if _, found, _ := store.GetTelegramLoginRequest(ctx, activeRequest.ID); !found {
		t.Fatal("active native request was deleted")
	}
	listed, err := store.ListTelegramLoginWebAuthorizations(ctx, owner.ID)
	if err != nil || len(listed) != 1 || listed[0].Hash != activeWeb.Hash {
		t.Fatalf("active authorization list=%#v err=%v", listed, err)
	}
	assertTelegramLoginConfigDeleteTakesClientLock(t, pool, client.BotUserID, func() (bool, error) {
		return store.DeleteTelegramLoginNativeApp(ctx, client.BotUserID, nativeApp.ID)
	})
}

func assertTelegramLoginConfigDeleteTakesClientLock(t *testing.T, pool *pgxpool.Pool, botUserID int64, remove func() (bool, error)) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedID int64
	if err := tx.QueryRow(ctx, `SELECT bot_user_id FROM bot_login_clients WHERE bot_user_id = $1 FOR UPDATE`, botUserID).Scan(&lockedID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		deleted, err := remove()
		if err == nil && !deleted {
			err = errors.New("configuration row was not deleted")
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("configuration delete bypassed client serialization lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("configuration delete remained blocked after client lock committed")
	}
}
