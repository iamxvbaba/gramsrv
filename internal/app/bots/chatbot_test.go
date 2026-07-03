package bots

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	messageapp "telesrv/internal/app/messages"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type fakeChatAI struct {
	chunks []string
	final  string
	err    error
	req    domain.AITextGenerationRequest
	calls  int
}

func (f *fakeChatAI) GenerateTextStream(_ context.Context, req domain.AITextGenerationRequest, emit func(domain.AIComposeText) error) (domain.AIComposeText, error) {
	f.calls++
	f.req = req
	if f.err != nil {
		return domain.AIComposeText{}, f.err
	}
	for _, chunk := range f.chunks {
		if emit != nil {
			if err := emit(domain.AIComposeText{Text: chunk}); err != nil {
				return domain.AIComposeText{}, err
			}
		}
	}
	final := f.final
	if final == "" && len(f.chunks) > 0 {
		final = f.chunks[len(f.chunks)-1]
	}
	return domain.AIComposeText{Text: final}, nil
}

func newChatBotTestService(t *testing.T, ai *fakeChatAI, opts ...Option) (*Service, *memory.UserStore, *memory.BotStore, *memory.MessageStore) {
	t.Helper()
	users := memory.NewUserStore()
	bots := memory.NewBotStore(users)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	all := []Option{WithAIChatGenerator(ai), WithAIChatStreamThrottle(0)}
	all = append(all, opts...)
	return NewService(users, bots, messages, all...), users, bots, messages
}

func latestChatBotReply(t *testing.T, messages *memory.MessageStore, userID int64) domain.Message {
	t.Helper()
	list, err := messages.ListByUser(context.Background(), userID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.ChatBotUserID},
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("list chatbot history: %v", err)
	}
	var latest domain.Message
	for _, msg := range list.Messages {
		if msg.From.ID == domain.ChatBotUserID && msg.ID > latest.ID {
			latest = msg
		}
	}
	if latest.ID == 0 {
		t.Fatal("no ChatBot reply")
	}
	return latest
}

func waitForChatBotReply(t *testing.T, messages *memory.MessageStore, userID int64, body string) domain.Message {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list, err := messages.ListByUser(context.Background(), userID, domain.MessageFilter{
			HasPeer: true,
			Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.ChatBotUserID},
			Limit:   100,
		})
		if err != nil {
			t.Fatalf("list chatbot history: %v", err)
		}
		for _, msg := range list.Messages {
			if msg.From.ID == domain.ChatBotUserID && (body == "" || msg.Body == body) {
				return msg
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for ChatBot reply %q", body)
	return domain.Message{}
}

func TestChatBotSystemSeedAndCommands(t *testing.T) {
	ai := &fakeChatAI{}
	svc, users, bots, messages := newChatBotTestService(t, ai)
	owner := newOwner(t, users, "+4000")
	ctx := context.Background()

	if !svc.HandlesBot(domain.ChatBotUserID) {
		t.Fatal("service should handle ChatBot")
	}
	u, found, err := users.ByUsername(ctx, "ChatBot")
	if err != nil || !found {
		t.Fatalf("@ChatBot user not seeded: found=%v err=%v", found, err)
	}
	if u.ID != domain.ChatBotUserID || !u.Bot || u.BotInfoVersion < 1 {
		t.Fatalf("@ChatBot user = %+v, want seeded bot", u)
	}
	profile, found, err := bots.GetBot(ctx, domain.ChatBotUserID)
	if err != nil || !found {
		t.Fatalf("@ChatBot profile not seeded: found=%v err=%v", found, err)
	}
	if !botCommandExists(profile.Commands, "start") || !botCommandExists(profile.Commands, "help") || !botCommandExists(profile.Commands, "reset") {
		t.Fatalf("@ChatBot commands = %+v, want start/help/reset", profile.Commands)
	}

	svc.respondAsChatBot(owner.ID, domain.Message{From: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, Body: "/start"})
	reply := latestChatBotReply(t, messages, owner.ID)
	if !strings.Contains(reply.Body, "/help") || ai.calls != 0 {
		t.Fatalf("/start reply=%q ai_calls=%d, want help without AI", reply.Body, ai.calls)
	}
	assertReplyEntityText(t, reply, domain.MessageEntityBotCommand, "/help")
}

func TestChatBotStreamsByTypingDraftThenFinalMessage(t *testing.T) {
	ai := &fakeChatAI{
		chunks: []string{"Hel", "Hello from AI"},
		final:  "Hello from AI",
	}
	svc, users, _, messages := newChatBotTestService(t, ai)
	hooks := &chatBotHookRecorder{}
	svc.SetTextDraftPusher(hooks)
	owner := newOwner(t, users, "+4001")

	svc.respondAsChatBot(owner.ID, domain.Message{
		From: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.ChatBotUserID},
		Body: "hello",
	})

	reply := latestChatBotReply(t, messages, owner.ID)
	if reply.Body != "Hello from AI" || reply.EditDate != 0 {
		t.Fatalf("ChatBot reply = body %q edit_date %d, want ordinary final message", reply.Body, reply.EditDate)
	}
	if ai.calls != 1 {
		t.Fatalf("AI calls = %d, want 1", ai.calls)
	}
	if ai.req.UserID != owner.ID || !strings.Contains(ai.req.Text.Text, "hello") || !strings.Contains(ai.req.Instruction, "ChatBot") {
		t.Fatalf("AI request = %#v", ai.req)
	}
	if len(hooks.drafts) < 2 {
		t.Fatalf("draft pushes = %+v, want streamed chunks", hooks.drafts)
	}
	randomID := hooks.drafts[0].randomID
	if randomID == 0 {
		t.Fatal("draft random_id = 0, want fixed non-zero id")
	}
	for _, draft := range hooks.drafts {
		if draft.botUserID != domain.ChatBotUserID || draft.userID != owner.ID || draft.randomID != randomID {
			t.Fatalf("draft push = %+v, want same bot/user/random_id", draft)
		}
	}
	if got := hooks.drafts[len(hooks.drafts)-1].text; got != "Hello from AI" {
		t.Fatalf("last draft text = %q, want final cumulative text", got)
	}
}

func TestChatBotRespondsFromMessageSendHook(t *testing.T) {
	ai := &fakeChatAI{
		chunks: []string{"hooked reply"},
		final:  "hooked reply",
	}
	users := memory.NewUserStore()
	botsStore := memory.NewBotStore(users)
	dialogsStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogsStore)
	botSvc := NewService(users, botsStore, messageStore, WithAIChatGenerator(ai), WithAIChatStreamThrottle(0))
	hooks := &chatBotHookRecorder{}
	botSvc.SetTextDraftPusher(hooks)
	messageSvc := messageapp.NewService(messageStore, dialogsStore, messageapp.WithBotResponder(botSvc))
	owner := newOwner(t, users, "+4005")

	if _, err := messageSvc.SendPrivateText(context.Background(), owner.ID, domain.SendPrivateTextRequest{
		RecipientUserID: domain.ChatBotUserID,
		RandomID:        4005,
		Message:         "hello hook",
	}); err != nil {
		t.Fatalf("send to ChatBot through messages service: %v", err)
	}

	reply := waitForChatBotReply(t, messageStore, owner.ID, "hooked reply")
	if reply.EditDate != 0 {
		t.Fatalf("hook reply edit_date = %d, want ordinary final message", reply.EditDate)
	}
	if len(hooks.drafts) == 0 {
		t.Fatal("draft pushes = 0, want streamed draft from message hook")
	}
	if count := strings.Count(ai.req.Text.Text, "hello hook"); count != 1 {
		t.Fatalf("prompt = %q, want current user text once", ai.req.Text.Text)
	}
}

func TestChatBotResetClearsPromptContextAndSkipsCommandReplies(t *testing.T) {
	ai := &fakeChatAI{
		chunks: []string{"fresh reply"},
		final:  "fresh reply",
	}
	users := memory.NewUserStore()
	botsStore := memory.NewBotStore(users)
	dialogsStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogsStore)
	botSvc := NewService(users, botsStore, messageStore, WithAIChatGenerator(ai), WithAIChatStreamThrottle(0))
	messageSvc := messageapp.NewService(messageStore, dialogsStore, messageapp.WithBotResponder(botSvc))
	owner := newOwner(t, users, "+4006")
	ctx := context.Background()

	if _, err := messageSvc.SendPrivateText(ctx, owner.ID, domain.SendPrivateTextRequest{
		RecipientUserID: domain.ChatBotUserID,
		RandomID:        40060,
		Message:         "old question",
	}); err != nil {
		t.Fatalf("send old question: %v", err)
	}
	waitForChatBotReply(t, messageStore, owner.ID, "fresh reply")

	if _, err := messageSvc.SendPrivateText(ctx, owner.ID, domain.SendPrivateTextRequest{
		RecipientUserID: domain.ChatBotUserID,
		RandomID:        40061,
		Message:         "/reset",
	}); err != nil {
		t.Fatalf("send reset: %v", err)
	}
	waitForChatBotReply(t, messageStore, owner.ID, chatBotResetText)

	if _, err := messageSvc.SendPrivateText(ctx, owner.ID, domain.SendPrivateTextRequest{
		RecipientUserID: domain.ChatBotUserID,
		RandomID:        40062,
		Message:         "fresh question",
	}); err != nil {
		t.Fatalf("send fresh question: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ai.calls >= 2 && strings.Contains(ai.req.Text.Text, "fresh question") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	prompt := ai.req.Text.Text
	if !strings.Contains(prompt, "fresh question") || strings.Contains(prompt, "old question") || strings.Contains(prompt, "/reset") || strings.Contains(prompt, chatBotResetText) {
		t.Fatalf("prompt after reset = %q", prompt)
	}
	if count := strings.Count(prompt, "fresh question"); count != 1 {
		t.Fatalf("fresh question count = %d in prompt %q, want 1", count, prompt)
	}
}

func TestChatBotPromptEchoIsNotPersisted(t *testing.T) {
	ai := &fakeChatAI{
		chunks: []string{"User: hello\nAssistant: leaked prompt"},
		final:  "User: hello\nAssistant: leaked prompt",
	}
	svc, users, _, messages := newChatBotTestService(t, ai)
	hooks := &chatBotHookRecorder{}
	svc.SetTextDraftPusher(hooks)
	owner := newOwner(t, users, "+4007")

	svc.respondAsChatBot(owner.ID, domain.Message{
		From: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.ChatBotUserID},
		Body: "hello",
	})

	reply := latestChatBotReply(t, messages, owner.ID)
	if reply.Body != chatBotUnavailableText {
		t.Fatalf("reply body = %q, want unavailable fallback", reply.Body)
	}
	if len(hooks.drafts) != 1 || hooks.drafts[0].text != chatBotUnavailableText {
		t.Fatalf("draft pushes = %+v, want only unavailable fallback", hooks.drafts)
	}
}

func TestChatBotProviderFailureSendsFallbackMessage(t *testing.T) {
	ai := &fakeChatAI{err: errors.New("provider down")}
	svc, users, _, messages := newChatBotTestService(t, ai)
	hooks := &chatBotHookRecorder{}
	svc.SetTextDraftPusher(hooks)
	owner := newOwner(t, users, "+4002")

	svc.respondAsChatBot(owner.ID, domain.Message{
		From: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.ChatBotUserID},
		Body: "hello",
	})

	reply := latestChatBotReply(t, messages, owner.ID)
	if reply.Body != chatBotUnavailableText || reply.EditDate != 0 {
		t.Fatalf("fallback reply = body %q edit_date %d", reply.Body, reply.EditDate)
	}
	if len(hooks.drafts) != 1 || hooks.drafts[0].text != chatBotUnavailableText {
		t.Fatalf("fallback draft pushes = %+v, want one fallback draft", hooks.drafts)
	}
}

func TestChatBotRespectsBlockBeforeAI(t *testing.T) {
	ai := &fakeChatAI{final: "should not call"}
	blocker := &stubBlocker{blocked: true}
	svc, users, _, messages := newChatBotTestService(t, ai, WithBlockChecker(blocker))
	owner := newOwner(t, users, "+4003")

	svc.respondAsChatBot(owner.ID, domain.Message{
		From: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: domain.ChatBotUserID},
		Body: "hello",
	})
	if ai.calls != 0 {
		t.Fatalf("AI calls = %d, want 0 for blocked ChatBot", ai.calls)
	}
	if blocker.gotUser != owner.ID || blocker.gotPeer != domain.ChatBotUserID {
		t.Fatalf("IsBlocked called with (%d,%d), want (%d,%d)", blocker.gotUser, blocker.gotPeer, owner.ID, domain.ChatBotUserID)
	}
	list, err := messages.ListByUser(context.Background(), owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: domain.ChatBotUserID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(list.Messages) != 0 {
		t.Fatalf("blocked user received messages: %+v", list.Messages)
	}
}

type chatBotDraftPush struct {
	botUserID int64
	userID    int64
	randomID  int64
	text      string
}

type chatBotHookRecorder struct {
	drafts []chatBotDraftPush
}

func (h *chatBotHookRecorder) PushBotTextDraft(_ context.Context, botUserID, userID, randomID int64, text string) {
	h.drafts = append(h.drafts, chatBotDraftPush{botUserID: botUserID, userID: userID, randomID: randomID, text: text})
}
