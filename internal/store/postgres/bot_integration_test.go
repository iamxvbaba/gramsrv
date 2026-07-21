package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestBotStoreRoundTripPostgres 验证迁移 0090 的 bot 模型：BotFather 种子行、
// CreateBotAccount 双行事务、空 phone 不撞唯一索引、token 轮换、对话状态。
func TestBotStoreRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	users := NewUserStore(pool)
	bots := NewBotStore(pool)

	// BotFather 种子：users 行 + bots 行 + 不可登录（token 为空）。
	bf, found, err := users.ByID(ctx, domain.BotFatherUserID)
	if err != nil || !found {
		t.Fatalf("BotFather user not seeded: found=%v err=%v", found, err)
	}
	if !bf.Bot || bf.BotInfoVersion < 1 || bf.Username != "BotFather" || !bf.Verified {
		t.Fatalf("BotFather user = %+v, want verified bot with bot_info_version>=1", bf)
	}
	bfProfile, found, err := bots.GetBot(ctx, domain.BotFatherUserID)
	if err != nil || !found {
		t.Fatalf("BotFather bots row not seeded: found=%v err=%v", found, err)
	}
	if bfProfile.TokenSecret != "" || len(bfProfile.Commands) == 0 {
		t.Fatalf("BotFather profile = %+v, want empty token with seeded commands", bfProfile)
	}
	hasDone := false
	for _, command := range bfProfile.Commands {
		if command.Command == "done" {
			hasDone = true
			break
		}
	}
	if !hasDone {
		t.Fatalf("BotFather commands = %+v, want /done for persistent /setlogin sessions", bfProfile.Commands)
	}
	// 空 phone 查询不得命中任何行。
	if _, found, err := users.ByPhone(ctx, ""); err != nil || found {
		t.Fatalf("ByPhone('') found=%v err=%v, want not found", found, err)
	}

	// owner 用户 + 两个空 phone 的 bot（验证部分唯一索引）。
	suffix := time.Now().UnixNano() % 1_000_000_000
	ownerPhone := fmt.Sprintf("1666%d", suffix)
	owner, err := users.Create(ctx, domain.User{AccessHash: 7, Phone: ownerPhone, FirstName: "BotOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})
	makeBot := func(i int) (domain.User, domain.BotProfile) {
		u, p, err := bots.CreateBotAccount(ctx, domain.User{
			AccessHash: int64(100 + i),
			FirstName:  fmt.Sprintf("IT Bot %d", i),
			Username:   fmt.Sprintf("it%d_%d_bot", i, suffix),
		}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: fmt.Sprintf("secret-%d-%d", i, suffix)})
		if err != nil {
			t.Fatalf("create bot %d: %v", i, err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", u.ID) // bots 行随 FK CASCADE
		})
		return u, p
	}
	bot1, profile1 := makeBot(1)
	bot2, _ := makeBot(2)
	if bot1.Phone != "" || bot2.Phone != "" {
		t.Fatalf("bot phones = %q/%q, want empty", bot1.Phone, bot2.Phone)
	}
	if !bot1.Bot || bot1.BotInfoVersion != 1 {
		t.Fatalf("bot1 = %+v, want is_bot with bot_info_version=1", bot1)
	}

	// username 冲突映射 ErrUsernameOccupied，且不留 users 孤儿行。
	if _, _, err := bots.CreateBotAccount(ctx, domain.User{
		AccessHash: 999,
		FirstName:  "Dup",
		Username:   bot1.Username,
	}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "x"}); err != domain.ErrUsernameOccupied {
		t.Fatalf("duplicate username err = %v, want ErrUsernameOccupied", err)
	}

	if n, err := bots.CountBotsByOwner(ctx, owner.ID); err != nil || n != 2 {
		t.Fatalf("CountBotsByOwner = %d err=%v, want 2", n, err)
	}
	list, err := bots.ListBotsByOwner(ctx, owner.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListBotsByOwner = %d err=%v, want 2", len(list), err)
	}

	// token 轮换。
	if err := bots.UpdateBotTokenSecret(ctx, bot1.ID, "rotated-secret"); err != nil {
		t.Fatalf("rotate token: %v", err)
	}
	rotated, _, err := bots.GetBot(ctx, bot1.ID)
	if err != nil || rotated.TokenSecret != "rotated-secret" {
		t.Fatalf("rotated profile = %+v err=%v", rotated, err)
	}
	if err := bots.UpdateBotTokenSecret(ctx, 424242, "x"); err != domain.ErrBotNotFound {
		t.Fatalf("rotate missing bot err = %v, want ErrBotNotFound", err)
	}

	// 对话状态 upsert/get/delete。
	state := domain.BotChatState{
		BotUserID: domain.BotFatherUserID,
		UserID:    owner.ID,
		Command:   "newbot",
		Step:      "username",
		Draft:     map[string]string{"name": "IT Bot"},
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_chat_states WHERE user_id = $1", owner.ID)
	})
	if err := bots.UpsertBotChatState(ctx, state); err != nil {
		t.Fatalf("upsert state: %v", err)
	}
	got, found, err := bots.GetBotChatState(ctx, domain.BotFatherUserID, owner.ID)
	if err != nil || !found {
		t.Fatalf("get state: found=%v err=%v", found, err)
	}
	if got.Command != "newbot" || got.Step != "username" || got.Draft["name"] != "IT Bot" {
		t.Fatalf("state = %+v, want round-trip", got)
	}
	if err := bots.DeleteBotChatState(ctx, domain.BotFatherUserID, owner.ID); err != nil {
		t.Fatalf("delete state: %v", err)
	}
	if _, found, _ := bots.GetBotChatState(ctx, domain.BotFatherUserID, owner.ID); found {
		t.Fatal("state still present after delete")
	}

	// P2 元数据写入 + bot_info_version bump（postgres 单事务）。
	uBefore, _, _ := users.ByID(ctx, bot1.ID)
	v1, err := bots.UpdateBotCommands(ctx, bot1.ID, []domain.BotCommand{{Command: "start", Description: "begin"}})
	if err != nil || v1 <= uBefore.BotInfoVersion {
		t.Fatalf("UpdateBotCommands version = %d err=%v, want > %d", v1, err, uBefore.BotInfoVersion)
	}
	gotBot, _, _ := bots.GetBot(ctx, bot1.ID)
	if len(gotBot.Commands) != 1 || gotBot.Commands[0].Command != "start" {
		t.Fatalf("commands = %+v, want [start]", gotBot.Commands)
	}
	v2, err := bots.UpdateBotInfo(ctx, bot1.ID, domain.BotInfoUpdate{
		SetName: true, Name: "PG Bot", SetAbout: true, About: "pg about", SetDescription: true, Description: "pg desc",
	})
	if err != nil || v2 <= v1 {
		t.Fatalf("UpdateBotInfo version = %d err=%v, want > %d", v2, err, v1)
	}
	uAfter, _, _ := users.ByID(ctx, bot1.ID)
	if uAfter.FirstName != "PG Bot" || uAfter.About != "pg about" {
		t.Fatalf("bot user after setInfo = first_name=%q about=%q", uAfter.FirstName, uAfter.About)
	}
	if descBot, _, _ := bots.GetBot(ctx, bot1.ID); descBot.Description != "pg desc" {
		t.Fatalf("description = %q, want 'pg desc'", descBot.Description)
	}
	v3, err := bots.UpdateBotMenuButton(ctx, bot1.ID, domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Open", URL: "https://pg.example/app"})
	if err != nil || v3 <= v2 {
		t.Fatalf("UpdateBotMenuButton version = %d err=%v, want > %d", v3, err, v2)
	}
	if mbBot, _, _ := bots.GetBot(ctx, bot1.ID); mbBot.MenuButton.Type != domain.BotMenuButtonWebView || mbBot.MenuButton.URL != "https://pg.example/app" {
		t.Fatalf("menu button = %+v", mbBot.MenuButton)
	}
	v4, err := bots.SetBotInlinePlaceholder(ctx, bot1.ID, "Search PG")
	if err != nil || v4 <= v3 {
		t.Fatalf("SetBotInlinePlaceholder version = %d err=%v, want > %d", v4, err, v3)
	}
	if inlineBot, _, _ := bots.GetBot(ctx, bot1.ID); inlineBot.InlinePlaceholder != "Search PG" {
		t.Fatalf("inline placeholder = %q, want Search PG", inlineBot.InlinePlaceholder)
	}
	v5, err := bots.SetBotInlineGeo(ctx, bot1.ID, true)
	if err != nil || v5 <= v4 {
		t.Fatalf("SetBotInlineGeo version = %d err=%v, want > %d", v5, err, v4)
	}
	if inlineBot, _, _ := bots.GetBot(ctx, bot1.ID); !inlineBot.InlineGeo {
		t.Fatalf("inline geo = false, want true")
	}
	if _, err := bots.SetBotNochats(ctx, bot1.ID, true); err != nil {
		t.Fatalf("SetBotNochats: %v", err)
	}
	if _, err := bots.SetBotChatHistory(ctx, bot1.ID, true); err != nil {
		t.Fatalf("SetBotChatHistory: %v", err)
	}
	if flagBot, _, _ := bots.GetBot(ctx, bot1.ID); !flagBot.Nochats || !flagBot.ChatHistory {
		t.Fatalf("flags = nochats=%v chat_history=%v, want both true", flagBot.Nochats, flagBot.ChatHistory)
	}
	requestedButton := domain.BotRequestedWebViewButton{
		WebAppReqID: fmt.Sprintf("pg-requested-%d", suffix), BotUserID: bot1.ID, UserID: owner.ID,
		ButtonID: 45, Text: "Share", PeerType: "user", MaxQuantity: 2,
		NameRequested: true, UsernameRequested: true, PhotoRequested: true,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	t.Cleanup(func() {
		_ = bots.DeleteRequestedWebViewButton(ctx, bot1.ID, owner.ID, requestedButton.WebAppReqID)
	})
	if err := bots.SaveRequestedWebViewButton(ctx, requestedButton); err != nil {
		t.Fatalf("save requested button: %v", err)
	}
	storedButton, found, err := bots.GetRequestedWebViewButton(ctx, bot1.ID, owner.ID, requestedButton.WebAppReqID)
	if err != nil || !found || !storedButton.NameRequested || !storedButton.UsernameRequested || !storedButton.PhotoRequested {
		t.Fatalf("requested button=%#v found=%v err=%v", storedButton, found, err)
	}
	if can, err := bots.CanBotSendMessage(ctx, bot1.ID, owner.ID); err != nil || can {
		t.Fatalf("CanBotSendMessage before allow = %v,%v, want false,nil", can, err)
	}
	if created, err := bots.AllowBotSendMessage(ctx, bot1.ID, owner.ID, true); err != nil || !created {
		t.Fatalf("AllowBotSendMessage first = %v,%v, want true,nil", created, err)
	}
	if can, err := bots.CanBotSendMessage(ctx, bot1.ID, owner.ID); err != nil || !can {
		t.Fatalf("CanBotSendMessage after allow = %v,%v, want true,nil", can, err)
	}
	if created, err := bots.AllowBotSendMessage(ctx, bot1.ID, owner.ID, true); err != nil || created {
		t.Fatalf("AllowBotSendMessage repeat = %v,%v, want false,nil", created, err)
	}
	// 不存在的 bot → ErrBotNotFound。
	if _, err := bots.UpdateBotCommands(ctx, 424243, nil); err != domain.ErrBotNotFound {
		t.Fatalf("update missing bot err = %v, want ErrBotNotFound", err)
	}

	// SignInBot：PG 全链路 token 校验 + authorizations 绑定。
	authSvc := appauth.NewService(users, NewAuthorizationStore(pool), memory.NewCodeStore(), nil, nil, "12345",
		appauth.WithBotLogin(bots))
	var authKeyID [8]byte
	copy(authKeyID[:], fmt.Sprintf("%08d", suffix%100000000))
	if _, err := pool.Exec(ctx, "INSERT INTO auth_keys (auth_key_id, body, server_salt, expires_at) VALUES ($1, $2, 0, 0) ON CONFLICT DO NOTHING",
		authKeyIDToInt64(authKeyID), make([]byte, 256)); err != nil {
		t.Fatalf("seed auth key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM auth_keys WHERE auth_key_id = $1", authKeyIDToInt64(authKeyID))
	})
	token := domain.FormatBotToken(profile1.BotUserID, "rotated-secret")
	u, err := authSvc.SignInBot(ctx, domain.Authorization{AuthKeyID: authKeyID}, token)
	if err != nil {
		t.Fatalf("SignInBot: %v", err)
	}
	if u.ID != bot1.ID || !u.Bot {
		t.Fatalf("SignInBot user = %+v, want bot %d", u, bot1.ID)
	}
	if uid, ok, err := authSvc.UserID(ctx, authKeyID); err != nil || !ok || uid != bot1.ID {
		t.Fatalf("UserID after SignInBot = %d,%v,%v, want %d", uid, ok, err, bot1.ID)
	}
	// 旧 token（轮换前 secret）必须失效。
	oldToken := domain.FormatBotToken(profile1.BotUserID, profile1.TokenSecret)
	if _, err := authSvc.SignInBot(ctx, domain.Authorization{AuthKeyID: authKeyID}, oldToken); err != domain.ErrBotTokenInvalid {
		t.Fatalf("old token err = %v, want ErrBotTokenInvalid", err)
	}
	// BotFather 空 token 永不可登录。
	if _, err := authSvc.SignInBot(ctx, domain.Authorization{AuthKeyID: authKeyID}, domain.FormatBotToken(domain.BotFatherUserID, "")); err != domain.ErrBotTokenInvalid {
		t.Fatalf("BotFather token err = %v, want ErrBotTokenInvalid", err)
	}
}
