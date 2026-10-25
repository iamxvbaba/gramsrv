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

func newBotRPCTestRouter(t *testing.T) (*Router, *botsapp.Service, *memory.UserStore, *memory.DeliveryOutboxStore, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	botStore := memory.NewBotStore(users)
	outbox := memory.NewDeliveryOutboxStore()
	botStore.AttachDeliveryDependencies(dialogs, outbox)
	messages := memory.NewMessageStore(dialogs)
	svc := botsapp.NewService(users, botStore, messages)
	owner, err := users.Create(ctx, domain.User{AccessHash: 5101, Phone: "15550005101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	manager, _, err := svc.CreateBotWithDelivery(ctx, owner.ID, "Manager Bot", "manager_test_bot", rpcTestBotLifecycleEffects)
	if err != nil {
		t.Fatalf("create manager bot: %v", err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:          appusers.NewService(users),
		Bots:           svc,
		DeliveryOutbox: outbox,
	}, zaptest.NewLogger(t), clock.System)
	svc.SetRouterHooks(r)
	return r, svc, users, outbox, owner, manager
}

func TestBotCommandsMutationCommitsExactDurablePayload(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	outbox := memory.NewDeliveryOutboxStore()
	botStore := memory.NewBotStore(users)
	botStore.AttachDeliveryDependencies(dialogs, outbox)
	messages := memory.NewMessageStore(dialogs)
	svc := botsapp.NewService(users, botStore, messages)
	owner, err := users.Create(ctx, domain.User{AccessHash: 1, Phone: "+51001", FirstName: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := users.Create(ctx, domain.User{AccessHash: 2, Phone: "+51002", FirstName: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := svc.CreateBotWithDelivery(ctx, owner.ID, "Commands Bot", "commands_payload_bot", rpcTestBotLifecycleEffects)
	if err != nil {
		t.Fatal(err)
	}
	outbox = memory.NewDeliveryOutboxStore()
	botStore.AttachDeliveryDependencies(dialogs, outbox)
	if err := dialogs.SaveList(ctx, viewer.ID, domain.DialogList{Dialogs: []domain.Dialog{{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: bot.ID}}}}); err != nil {
		t.Fatal(err)
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users: appusers.NewService(users), Bots: svc, DeliveryOutbox: outbox,
	}, zaptest.NewLogger(t), clock.System)
	svc.SetRouterHooks(r)
	if _, err := svc.SetBotCommands(ctx, bot.ID, []domain.BotCommand{{Command: "/Start", Description: "begin"}}); err != nil {
		t.Fatal(err)
	}
	items := outbox.Snapshot()
	if len(items) != 1 || items[0].TargetUserID != viewer.ID || items[0].ExcludeAuthKeyID != ([8]byte{}) || items[0].ExcludeSessionID != 0 {
		t.Fatalf("durable command targets = %+v", items)
	}
	decoded, err := decodeDeliveryUpdate(items[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	updates, ok := decoded.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("decoded command payload = %#v", decoded)
	}
	changed, ok := updates.Updates[0].(*tg.UpdateBotCommands)
	if !ok || changed.BotID != bot.ID || len(changed.Commands) != 1 || changed.Commands[0].Command != "start" || changed.Commands[0].Description != "begin" {
		t.Fatalf("updateBotCommands = %#v", updates.Updates[0])
	}
	peer, ok := changed.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != bot.ID {
		t.Fatalf("updateBotCommands peer = %#v", changed.Peer)
	}
}

func TestBotsManagedCreateAndTokenRPCs(t *testing.T) {
	ctx := context.Background()
	r, svc, _, outbox, owner, manager := newBotRPCTestRouter(t)
	ownerCtx := WithUserID(ctx, owner.ID)

	if ok, err := r.onBotsCheckUsername(ownerCtx, &tg.BotsCheckUsernameRequest{Username: "fresh_rpc_bot"}); err != nil || !ok {
		t.Fatalf("check free username = %v,%v, want true,nil", ok, err)
	}

	rowsBeforeCreate := len(outbox.Snapshot())
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
	items := outbox.Snapshot()
	if len(items) != rowsBeforeCreate+1 || items[len(items)-1].TargetUserID != owner.ID {
		t.Fatalf("create delivery rows = %+v", items)
	}
	decodedCreate, err := decodeDeliveryUpdate(items[len(items)-1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	createUpdates, ok := decodedCreate.(*tg.Updates)
	if !ok || len(createUpdates.Updates) != 1 {
		t.Fatalf("create delivery = %#v", decodedCreate)
	}
	if update, ok := createUpdates.Updates[0].(*tg.UpdateUser); !ok || update.UserID != created.ID {
		t.Fatalf("create update = %#v", createUpdates.Updates[0])
	}
	if len(createUpdates.Users) != 1 || createUpdates.Users[0].(*tg.User).ID != created.ID {
		t.Fatalf("create user projection = %#v", createUpdates.Users)
	}

	if ok, err := r.onBotsCheckUsername(ownerCtx, &tg.BotsCheckUsernameRequest{Username: "fresh_rpc_bot"}); err != nil || ok {
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

func TestBotsSetBotInfoLocalizedCommitsAbsoluteReload(t *testing.T) {
	ctx := context.Background()
	r, svc, _, outbox, owner, bot := newBotRPCTestRouter(t)
	ownerCtx := WithUserID(ctx, owner.ID)
	input := &tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash}
	req := &tg.BotsSetBotInfoRequest{LangCode: "zh-Hans"}
	req.SetBot(input)
	req.SetName("本地名称")
	rowsBefore := len(outbox.Snapshot())
	if ok, err := r.onBotsSetBotInfo(ownerCtx, req); err != nil || !ok {
		t.Fatalf("localized setBotInfo = %v,%v", ok, err)
	}
	items := outbox.Snapshot()
	if len(items) != rowsBefore+1 || items[len(items)-1].TargetUserID != owner.ID {
		t.Fatalf("localized delivery rows = %+v", items)
	}
	decoded, err := decodeDeliveryUpdate(items[len(items)-1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	updates, ok := decoded.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("localized delivery = %#v", decoded)
	}
	if update, ok := updates.Updates[0].(*tg.UpdateUser); !ok || update.UserID != bot.ID {
		t.Fatalf("localized update = %#v", updates.Updates[0])
	}
	localized, err := svc.GetBotInfo(ctx, bot.ID, "zh-hans")
	if err != nil || localized.Name != "本地名称" {
		t.Fatalf("localized info = %+v err=%v", localized, err)
	}
	defaults, err := svc.GetBotInfo(ctx, bot.ID, "")
	if err != nil || defaults.Name == localized.Name {
		t.Fatalf("default info changed = %+v err=%v", defaults, err)
	}
	getReq := &tg.BotsGetBotInfoRequest{LangCode: "zh-Hans"}
	getReq.SetBot(input)
	got, err := r.onBotsGetBotInfo(ownerCtx, getReq)
	if err != nil || got.Name != "本地名称" {
		t.Fatalf("localized getBotInfo = %#v err=%v", got, err)
	}
	if ok, err := r.onBotsSetBotInfo(ownerCtx, req); err != nil || !ok {
		t.Fatalf("localized replay = %v,%v", ok, err)
	}
	if len(outbox.Snapshot()) != rowsBefore+1 {
		t.Fatalf("localized replay appended delivery: rows=%d", len(outbox.Snapshot()))
	}

	langOnly := &tg.BotsSetBotInfoRequest{LangCode: "fr"}
	langOnly.SetBot(input)
	if ok, err := r.onBotsSetBotInfo(ownerCtx, langOnly); err != nil || !ok {
		t.Fatalf("lang-only setBotInfo = %v,%v", ok, err)
	}
	if len(outbox.Snapshot()) != rowsBefore+2 {
		t.Fatalf("lang-only mutation rows=%d", len(outbox.Snapshot()))
	}
	fr, err := svc.GetBotInfo(ctx, bot.ID, "fr")
	if err != nil || fr.Name != defaults.Name || fr.About != defaults.About || fr.Description != defaults.Description {
		t.Fatalf("lang-only fallback = %+v err=%v", fr, err)
	}
}

func TestBotsCreateBotRPCErrorMapping(t *testing.T) {
	ctx := context.Background()
	r, _, users, _, owner, _ := newBotRPCTestRouter(t)
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
