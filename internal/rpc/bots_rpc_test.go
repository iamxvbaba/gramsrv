package rpc

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"

	botsapp "telesrv/internal/app/bots"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newBotRPCTestRouter(t *testing.T) (*Router, *botsapp.Service, *memory.UserStore, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	svc := botsapp.NewService(users, botStore, messages)
	owner, err := users.Create(ctx, domain.User{AccessHash: 5101, Phone: "15550005101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	manager, _, err := svc.CreateBot(ctx, owner.ID, "Manager Bot", "manager_test_bot")
	if err != nil {
		t.Fatalf("create manager bot: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users: appusers.NewService(users),
		Bots:  svc,
	}, zaptest.NewLogger(t), clock.System)
	return r, svc, users, owner, manager
}

func TestBotsManagedCreateAndTokenRPCs(t *testing.T) {
	ctx := context.Background()
	r, svc, _, owner, manager := newBotRPCTestRouter(t)
	ownerCtx := WithUserID(ctx, owner.ID)

	if ok, err := r.onBotsCheckUsername(ownerCtx, "fresh_rpc_bot"); err != nil || !ok {
		t.Fatalf("check free username = %v,%v, want true,nil", ok, err)
	}

	createdClass, err := r.onBotsCreateBot(ownerCtx, &tg.BotsCreateBotRequest{
		Name:      "Created Bot",
		Username:  "fresh_rpc_bot",
		ManagerID: &tg.InputUser{UserID: manager.ID, AccessHash: manager.AccessHash},
	})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	created := createdClass.(*tg.User)
	version, hasVersion := created.GetBotInfoVersion()
	if !created.Bot || !hasVersion || version < 1 || created.Username != "fresh_rpc_bot" {
		t.Fatalf("created user = %+v, want bot with version and username", created)
	}

	if ok, err := r.onBotsCheckUsername(ownerCtx, "fresh_rpc_bot"); err != nil || ok {
		t.Fatalf("check occupied username = %v,%v, want false,nil", ok, err)
	}

	admined, err := r.onBotsGetAdminedBots(ownerCtx)
	if err != nil {
		t.Fatalf("get admined bots: %v", err)
	}
	if len(admined) != 2 {
		t.Fatalf("admined bots len = %d, want manager+created", len(admined))
	}

	token, err := r.onBotsExportBotToken(ownerCtx, &tg.BotsExportBotTokenRequest{
		Bot:    &tg.InputUser{UserID: created.ID, AccessHash: created.AccessHash},
		Revoke: false,
	})
	if err != nil {
		t.Fatalf("export token: %v", err)
	}
	if !strings.HasPrefix(token.Token, "1") || !strings.Contains(token.Token, ":") {
		t.Fatalf("token = %q, want <bot_id>:<secret>", token.Token)
	}
	if !strings.HasPrefix(token.Token, strconv.FormatInt(created.ID, 10)+":") {
		t.Fatalf("token = %q, want bot id prefix %d", token.Token, created.ID)
	}

	rotated, err := r.onBotsExportBotToken(ownerCtx, &tg.BotsExportBotTokenRequest{
		Bot:    &tg.InputUser{UserID: created.ID, AccessHash: created.AccessHash},
		Revoke: true,
	})
	if err != nil {
		t.Fatalf("export revoked token: %v", err)
	}
	if rotated.Token == token.Token {
		t.Fatalf("revoke kept token %q", token.Token)
	}
	profile, found, err := svc.BotInfo(ctx, created.ID)
	if err != nil || !found {
		t.Fatalf("bot info: found=%v err=%v", found, err)
	}
	if domain.FormatBotToken(created.ID, profile.TokenSecret) != rotated.Token {
		t.Fatalf("stored token = %q, exported %q", domain.FormatBotToken(created.ID, profile.TokenSecret), rotated.Token)
	}
}

func TestBotsCreateBotRPCErrorMapping(t *testing.T) {
	ctx := context.Background()
	r, _, users, owner, _ := newBotRPCTestRouter(t)
	ownerCtx := WithUserID(ctx, owner.ID)

	if _, err := r.onBotsCreateBot(ownerCtx, &tg.BotsCreateBotRequest{
		Name:      "Bad Manager",
		Username:  "bad_manager_bot",
		ManagerID: &tg.InputUserSelf{},
	}); err == nil || !strings.Contains(err.Error(), "MANAGER_PERMISSION_MISSING") {
		t.Fatalf("bad manager err = %v, want MANAGER_PERMISSION_MISSING", err)
	}

	if _, err := r.onBotsCreateBot(ownerCtx, &tg.BotsCreateBotRequest{
		Name:      "Bad Username",
		Username:  "notvalid",
		ManagerID: &tg.InputUser{UserID: domain.BotFatherUserID},
	}); err == nil || !strings.Contains(err.Error(), "USERNAME_INVALID") {
		t.Fatalf("bad username err = %v, want USERNAME_INVALID", err)
	}

	other, err := users.Create(ctx, domain.User{AccessHash: 5102, Phone: "15550005102", FirstName: "Other"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if _, err := r.onBotsExportBotToken(WithUserID(ctx, other.ID), &tg.BotsExportBotTokenRequest{
		Bot:    &tg.InputUser{UserID: domain.BotFatherUserID},
		Revoke: false,
	}); err == nil || !strings.Contains(err.Error(), "BOT_INVALID") {
		t.Fatalf("non-owned export err = %v, want BOT_INVALID", err)
	}
}
