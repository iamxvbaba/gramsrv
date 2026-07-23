package bots

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newTestService(t *testing.T) (*Service, *memory.UserStore, *memory.BotStore, *memory.MessageStore) {
	t.Helper()
	users := memory.NewUserStore()
	bots := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	return NewService(users, bots, messages), users, bots, messages
}

func newOwner(t *testing.T, users *memory.UserStore, phone string) domain.User {
	t.Helper()
	u, err := users.Create(context.Background(), domain.User{AccessHash: 1, Phone: phone, FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	return u
}

// sendToBotFather 同步驱动 responder（绕过 OnPrivateMessage 的 goroutine 派发以
// 保证单测确定性；异步派发由 mtprotoedge bot e2e 覆盖），返回 BotFather 最新回复文本。
func sendToBotFather(t *testing.T, svc *Service, messages *memory.MessageStore, owner domain.User, text string) string {
	t.Helper()
	ctx := context.Background()
	svc.respondAsBotFather(owner.ID, text)
	list, err := messages.ListByUser(ctx, owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.BotFatherUserID},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	var latest domain.Message
	for _, msg := range list.Messages {
		if msg.From.ID == domain.BotFatherUserID && msg.ID > latest.ID {
			latest = msg
		}
	}
	if latest.ID == 0 {
		t.Fatalf("no BotFather reply after sending %q", text)
	}
	return latest.Body
}

var tokenRe = regexp.MustCompile(`(\d+):([A-Za-z0-9_-]{35})`)

func TestBotFatherNewBotFlow(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+1000")
	ctx := context.Background()

	if reply := sendToBotFather(t, svc, messages, owner, "/start"); !strings.Contains(reply, "/newbot") {
		t.Fatalf("/start reply = %q, want help text", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "/newbot"); !strings.Contains(reply, "choose a name") {
		t.Fatalf("/newbot reply = %q, want name prompt", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "My Test Bot"); !strings.Contains(reply, "username") {
		t.Fatalf("name reply = %q, want username prompt", reply)
	}
	// 非法 username：不以 bot 结尾。
	if reply := sendToBotFather(t, svc, messages, owner, "mytest"); !strings.Contains(reply, "invalid") {
		t.Fatalf("invalid username reply = %q, want invalid notice", reply)
	}
	reply := sendToBotFather(t, svc, messages, owner, "my_test_bot")
	match := tokenRe.FindStringSubmatch(reply)
	if match == nil {
		t.Fatalf("done reply = %q, want token", reply)
	}
	if !strings.Contains(reply, "telesrv.net/my_test_bot") {
		t.Fatalf("done reply = %q, want deep link", reply)
	}

	created, found, err := users.ByUsername(ctx, "my_test_bot")
	if err != nil || !found {
		t.Fatalf("bot user not found: %v", err)
	}
	if !created.Bot || created.BotInfoVersion < 1 || created.Phone != "" {
		t.Fatalf("bot user = %+v, want bot with bot_info_version>=1 and empty phone", created)
	}
	profile, found, err := bots.GetBot(ctx, created.ID)
	if err != nil || !found {
		t.Fatalf("bot profile not found: %v", err)
	}
	if profile.OwnerUserID != owner.ID {
		t.Fatalf("bot owner = %d, want %d", profile.OwnerUserID, owner.ID)
	}
	if fmt.Sprintf("%d", created.ID) != match[1] || profile.TokenSecret != match[2] {
		t.Fatalf("token %q does not match stored bot %d/%q", match[0], created.ID, profile.TokenSecret)
	}
	// 状态机已复位：普通文本回兜底提示。
	if reply := sendToBotFather(t, svc, messages, owner, "hello"); !strings.Contains(reply, "/help") {
		t.Fatalf("post-done reply = %q, want fallback", reply)
	}
}

func TestBotProfileCacheCachesPositiveAndNegativeProfiles(t *testing.T) {
	users := memory.NewUserStore()
	botsStore := &countingBotStore{BotStore: memory.NewBotStore(users)}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	svc := NewService(users, botsStore, messages)
	owner := newOwner(t, users, "+1090")
	ctx := context.Background()

	bot, _, err := svc.CreateBot(ctx, owner.ID, "Cache Bot", "cache_test_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	botsStore.reset()
	if _, found, err := svc.BotInfo(ctx, 424242); err != nil || found {
		t.Fatalf("negative BotInfo = found %v err %v, want false,nil", found, err)
	}
	if _, found, err := svc.BotInfo(ctx, 424242); err != nil || found {
		t.Fatalf("second negative BotInfo = found %v err %v, want false,nil", found, err)
	}
	if botsStore.getBotCalls != 1 {
		t.Fatalf("negative GetBot calls = %d, want 1", botsStore.getBotCalls)
	}

	botsStore.reset()
	if profile, found, err := svc.BotInfo(ctx, bot.ID); err != nil || !found || profile.BotUserID != bot.ID {
		t.Fatalf("cached positive BotInfo = profile %+v found %v err %v", profile, found, err)
	}
	if _, found, err := svc.BotInfo(ctx, bot.ID); err != nil || !found {
		t.Fatalf("second positive BotInfo = found %v err %v, want true,nil", found, err)
	}
	if botsStore.getBotCalls != 0 {
		t.Fatalf("positive GetBot calls after create prewarm = %d, want 0", botsStore.getBotCalls)
	}

	botsStore.reset()
	profiles, err := svc.BotInfos(ctx, []int64{bot.ID, 424242, 424243, 424243})
	if err != nil {
		t.Fatalf("batch BotInfos: %v", err)
	}
	if len(profiles) != 1 || profiles[bot.ID].BotUserID != bot.ID {
		t.Fatalf("batch profiles = %+v, want only bot", profiles)
	}
	if botsStore.getBotsCalls != 1 {
		t.Fatalf("batch GetBots calls = %d, want 1 for new miss", botsStore.getBotsCalls)
	}
	if _, err := svc.BotInfos(ctx, []int64{bot.ID, 424242, 424243}); err != nil {
		t.Fatalf("second batch BotInfos: %v", err)
	}
	if botsStore.getBotsCalls != 1 {
		t.Fatalf("second batch GetBots calls = %d, want still 1", botsStore.getBotsCalls)
	}

	if _, err := svc.SetBotCommands(ctx, bot.ID, []domain.BotCommand{{Command: "start", Description: "begin"}}); err != nil {
		t.Fatalf("set commands: %v", err)
	}
	botsStore.reset()
	profile, found, err := svc.BotInfo(ctx, bot.ID)
	if err != nil || !found {
		t.Fatalf("BotInfo after invalidation = found %v err %v", found, err)
	}
	if len(profile.Commands) != 1 || profile.Commands[0].Command != "start" {
		t.Fatalf("commands after invalidation = %+v, want [start]", profile.Commands)
	}
	if botsStore.getBotCalls != 1 {
		t.Fatalf("GetBot calls after invalidation = %d, want 1", botsStore.getBotCalls)
	}
}

type countingBotStore struct {
	*memory.BotStore
	getBotCalls  int
	getBotsCalls int
	deleteCalls  int
}

func (s *countingBotStore) reset() {
	s.getBotCalls = 0
	s.getBotsCalls = 0
}

func (s *countingBotStore) GetBot(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	s.getBotCalls++
	return s.BotStore.GetBot(ctx, botUserID)
}

func (s *countingBotStore) GetBots(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	s.getBotsCalls++
	return s.BotStore.GetBots(ctx, botUserIDs)
}

func (s *countingBotStore) DeleteBotAccount(_ context.Context, botUserID int64) (domain.User, error) {
	s.deleteCalls++
	return domain.User{ID: botUserID, Bot: true, Deleted: true}, nil
}

func TestBotFatherCancelAndUnknown(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+1001")

	if reply := sendToBotFather(t, svc, messages, owner, "/cancel"); !strings.Contains(reply, "No active command") {
		t.Fatalf("idle /cancel reply = %q", reply)
	}
	sendToBotFather(t, svc, messages, owner, "/newbot")
	if reply := sendToBotFather(t, svc, messages, owner, "/cancel"); !strings.Contains(reply, "cancelled") {
		t.Fatalf("active /cancel reply = %q", reply)
	}
	// 取消后名字输入不再被当作 newbot 步骤。
	if reply := sendToBotFather(t, svc, messages, owner, "Some Name"); !strings.Contains(reply, "/help") {
		t.Fatalf("post-cancel reply = %q, want fallback", reply)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "/definitelynotacommand"); !strings.Contains(reply, "Unrecognized") {
		t.Fatalf("unknown command reply = %q", reply)
	}
}

func TestBotFatherUsernameTaken(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+1002")

	if _, _, err := svc.CreateBot(context.Background(), owner.ID, "First", "taken_bot"); err != nil {
		t.Fatalf("seed first bot: %v", err)
	}
	sendToBotFather(t, svc, messages, owner, "/newbot")
	sendToBotFather(t, svc, messages, owner, "Second")
	if reply := sendToBotFather(t, svc, messages, owner, "taken_bot"); !strings.Contains(reply, "already taken") {
		t.Fatalf("taken username reply = %q", reply)
	}
	// 状态保留：可继续尝试新 username。
	if reply := sendToBotFather(t, svc, messages, owner, "second_bot"); !strings.Contains(reply, "telesrv.net/second_bot") {
		t.Fatalf("retry username reply = %q", reply)
	}
}

type usernameResolverStub struct {
	taken string
}

func (s usernameResolverStub) ResolvePublicChannelUsername(_ context.Context, _ int64, username string) (domain.Channel, bool, error) {
	return domain.Channel{}, strings.EqualFold(username, s.taken), nil
}

func TestCheckUsernameRejectsUserAndPublicChannelCollision(t *testing.T) {
	users := memory.NewUserStore()
	botsStore := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	svc := NewService(users, botsStore, messages, WithPublicChannelUsernameResolver(usernameResolverStub{taken: "channel_bot"}))
	owner := newOwner(t, users, "+1012")
	ctx := context.Background()

	if ok, err := svc.CheckUsername(ctx, owner.ID, "fresh_bot"); err != nil || !ok {
		t.Fatalf("fresh username = %v,%v, want true,nil", ok, err)
	}
	if _, _, err := svc.CreateBot(ctx, owner.ID, "Taken", "user_taken_bot"); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	if ok, err := svc.CheckUsername(ctx, owner.ID, "user_taken_bot"); err != nil || ok {
		t.Fatalf("user collision = %v,%v, want false,nil", ok, err)
	}
	if ok, err := svc.CheckUsername(ctx, owner.ID, "channel_bot"); err != nil || ok {
		t.Fatalf("channel collision = %v,%v, want false,nil", ok, err)
	}
	if _, _, err := svc.CreateBot(ctx, owner.ID, "Channel Collision", "channel_bot"); err != domain.ErrUsernameOccupied {
		t.Fatalf("create channel collision err = %v, want ErrUsernameOccupied", err)
	}
	if _, err := svc.CheckUsername(ctx, owner.ID, "notvalid"); err != domain.ErrBotUsernameInvalid {
		t.Fatalf("invalid username err = %v, want ErrBotUsernameInvalid", err)
	}
}

func TestBotFatherTokenAndRevoke(t *testing.T) {
	svc, users, bots, messages := newTestService(t)
	owner := newOwner(t, users, "+1003")
	ctx := context.Background()

	created, token, err := svc.CreateBot(ctx, owner.ID, "Token Bot", "tok_test_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	sendToBotFather(t, svc, messages, owner, "/token")
	if reply := sendToBotFather(t, svc, messages, owner, "@tok_test_bot"); !strings.Contains(reply, token) {
		t.Fatalf("/token reply = %q, want current token %q", reply, token)
	}

	sendToBotFather(t, svc, messages, owner, "/revoke")
	reply := sendToBotFather(t, svc, messages, owner, "tok_test_bot")
	match := tokenRe.FindStringSubmatch(reply)
	if match == nil {
		t.Fatalf("/revoke reply = %q, want new token", reply)
	}
	newToken := match[0]
	if newToken == token {
		t.Fatalf("revoke kept old token %q", token)
	}
	profile, _, err := bots.GetBot(ctx, created.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	if domain.FormatBotToken(created.ID, profile.TokenSecret) != newToken {
		t.Fatalf("stored secret %q does not match revoked token %q", profile.TokenSecret, newToken)
	}

	// 选择不属于自己的 bot。
	sendToBotFather(t, svc, messages, owner, "/token")
	if reply := sendToBotFather(t, svc, messages, owner, "@nosuch_bot"); !strings.Contains(reply, "don't see that bot") {
		t.Fatalf("unknown choose reply = %q", reply)
	}
}

func TestBotFatherMyBotsAndLimit(t *testing.T) {
	svc, users, _, messages := newTestService(t)
	owner := newOwner(t, users, "+1004")
	ctx := context.Background()

	if reply := sendToBotFather(t, svc, messages, owner, "/mybots"); !strings.Contains(reply, "don't have any bots") {
		t.Fatalf("empty /mybots reply = %q", reply)
	}
	for i := 0; i < domain.MaxBotsPerOwner; i++ {
		if _, _, err := svc.CreateBot(ctx, owner.ID, fmt.Sprintf("Bot %d", i), fmt.Sprintf("limit%d_bot", i)); err != nil {
			t.Fatalf("create bot %d: %v", i, err)
		}
	}
	if reply := sendToBotFather(t, svc, messages, owner, "/mybots"); !strings.Contains(reply, "@limit0_bot") {
		t.Fatalf("/mybots reply = %q, want bot list", reply)
	}
	if _, _, err := svc.CreateBot(ctx, owner.ID, "One Too Many", "toomany_bot"); err != domain.ErrBotsTooMany {
		t.Fatalf("create over limit err = %v, want ErrBotsTooMany", err)
	}
	if reply := sendToBotFather(t, svc, messages, owner, "/newbot"); !strings.Contains(reply, "limit") {
		t.Fatalf("over-limit /newbot reply = %q", reply)
	}
}

func TestParseBotCommand(t *testing.T) {
	cases := []struct {
		in  string
		cmd string
		ok  bool
	}{
		{"/newbot", "newbot", true},
		{"/NewBot@BotFather", "newbot", true},
		{"/token extra args", "token", true},
		{"plain text", "", false},
		{"/", "", false},
	}
	for _, tc := range cases {
		cmd, ok := parseBotCommand(tc.in)
		if cmd != tc.cmd || ok != tc.ok {
			t.Errorf("parseBotCommand(%q) = %q,%v want %q,%v", tc.in, cmd, ok, tc.cmd, tc.ok)
		}
	}
}

type stubBlocker struct {
	blocked bool
	gotUser int64
	gotPeer int64
}

func (s *stubBlocker) IsBlocked(_ context.Context, userID, blockedUserID int64) (bool, error) {
	s.gotUser, s.gotPeer = userID, blockedUserID
	return s.blocked, nil
}

func TestBotFatherReplyRespectsBlock(t *testing.T) {
	users := memory.NewUserStore()
	bots := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	blocker := &stubBlocker{blocked: true}
	svc := NewService(users, bots, messages, WithBlockChecker(blocker))
	owner := newOwner(t, users, "+1099")
	ctx := context.Background()

	svc.respondAsBotFather(owner.ID, "/help")

	// IsBlocked 参数语义：owner(userID) 是否 block 了 BotFather(blockedUserID)。
	if blocker.gotUser != owner.ID || blocker.gotPeer != domain.BotFatherUserID {
		t.Fatalf("IsBlocked called with (user=%d, peer=%d), want (%d, %d)", blocker.gotUser, blocker.gotPeer, owner.ID, domain.BotFatherUserID)
	}
	// 被 block：回复不投递到 owner 收件箱。
	list, err := messages.ListByUser(ctx, owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.BotFatherUserID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	for _, msg := range list.Messages {
		if msg.From.ID == domain.BotFatherUserID {
			t.Fatalf("blocked owner received BotFather reply: %q", msg.Body)
		}
	}

	// 未 block：回复正常投递。
	blocker.blocked = false
	other := newOwner(t, users, "+1098")
	svc.respondAsBotFather(other.ID, "/help")
	otherList, err := messages.ListByUser(ctx, other.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.BotFatherUserID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list other history: %v", err)
	}
	delivered := false
	for _, msg := range otherList.Messages {
		if msg.From.ID == domain.BotFatherUserID {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("unblocked user did not receive BotFather reply")
	}
}

func TestValidBotUsername(t *testing.T) {
	valid := []string{"my_bot", "TetrisBot", "a1234bot", "x_bot_BOT"}
	invalid := []string{"bot", "abot", "1abcbot", "_abcbot", "has space bot", "endsinbo", strings.Repeat("a", 30) + "bot" + "x"}
	for _, u := range valid {
		if !domain.ValidBotUsername(u) {
			t.Errorf("ValidBotUsername(%q) = false, want true", u)
		}
	}
	for _, u := range invalid {
		if domain.ValidBotUsername(u) {
			t.Errorf("ValidBotUsername(%q) = true, want false", u)
		}
	}
}
