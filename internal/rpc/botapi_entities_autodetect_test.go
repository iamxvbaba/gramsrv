package rpc

import (
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func newBotAPIAutoEntityRouter(t *testing.T) *Router {
	t.Helper()
	return New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
}

func botAPIEntityText(message string, entities []domain.MessageEntity, typ domain.MessageEntityType) (string, bool) {
	units := utf16Units(message)
	for _, entity := range entities {
		if entity.Type != typ {
			continue
		}
		return utf16Substring(units, entity.Offset, entity.Length), true
	}
	return "", false
}

// utf16Units 把消息文本摊成 UTF-16 码元，实体偏移/长度正是这个口径。
func utf16Units(message string) []uint16 {
	var out []uint16
	for _, r := range message {
		if r > 0xffff {
			out = append(out, 0xd800, 0xdc00) // 真实代理对取值不影响本测试的偏移断言
		} else {
			out = append(out, uint16(r))
		}
	}
	return out
}

func utf16Substring(units []uint16, offset, length int) string {
	if offset < 0 || length < 0 || offset+length > len(units) {
		return ""
	}
	var b strings.Builder
	for _, unit := range units[offset : offset+length] {
		b.WriteRune(rune(unit))
	}
	return b.String()
}

func TestAugmentBotAPIAutoEntitiesMarksCommands(t *testing.T) {
	r := newBotAPIAutoEntityRouter(t)
	message := "Нажми /buy и потом /help@TetrisBot"
	entities := r.augmentBotAPIAutoEntities(message, nil)

	first, ok := botAPIEntityText(message, entities, domain.MessageEntityBotCommand)
	if !ok || first != "/buy" {
		t.Fatalf("bot_command entities = %#v, want /buy marked", entities)
	}
	var commands []string
	for _, entity := range entities {
		if entity.Type != domain.MessageEntityBotCommand {
			continue
		}
		commands = append(commands, utf16Substring(utf16Units(message), entity.Offset, entity.Length))
	}
	if len(commands) != 2 || commands[1] != "/help@TetrisBot" {
		t.Fatalf("commands = %#v, want [/buy /help@TetrisBot]", commands)
	}
}

func TestAugmentBotAPIAutoEntitiesKeepsExplicitEntitiesFirst(t *testing.T) {
	r := newBotAPIAutoEntityRouter(t)
	message := "привет /start"
	entities := r.augmentBotAPIAutoEntities(message, []domain.MessageEntity{{
		Type: domain.MessageEntityBold, Offset: 0, Length: 6,
	}})

	if len(entities) < 2 {
		t.Fatalf("entities = %#v, want bold plus derived bot_command", entities)
	}
	if entities[0].Type != domain.MessageEntityBold || entities[0].Offset != 0 || entities[0].Length != 6 {
		t.Fatalf("entities[0] = %+v, want explicit bold preserved first", entities[0])
	}
	got, ok := botAPIEntityText(message, entities, domain.MessageEntityBotCommand)
	if !ok || got != "/start" {
		t.Fatalf("entities = %#v, want /start marked after explicit bold", entities)
	}
}

func TestAugmentBotAPIAutoEntitiesMarksCommandAfterExplicitEntity(t *testing.T) {
	r := newBotAPIAutoEntityRouter(t)
	message := "buy: /start"
	entities := r.augmentBotAPIAutoEntities(message, []domain.MessageEntity{{
		Type: domain.MessageEntityBold, Offset: 0, Length: 3,
	}})

	got, ok := botAPIEntityText(message, entities, domain.MessageEntityBotCommand)
	if !ok || got != "/start" {
		t.Fatalf("entities = %#v, want /start marked after explicit bold", entities)
	}
}

func TestAugmentBotAPIAutoEntitiesMarksMentionAndURL(t *testing.T) {
	r := newBotAPIAutoEntityRouter(t)
	message := "пиши @alice и смотри https://example.com/help"
	entities := r.augmentBotAPIAutoEntities(message, nil)

	mention, ok := botAPIEntityText(message, entities, domain.MessageEntityMention)
	if !ok || mention != "@alice" {
		t.Fatalf("mention entities = %#v, want @alice marked", entities)
	}
	link, ok := botAPIEntityText(message, entities, domain.MessageEntityURL)
	if !ok || link != "https://example.com/help" {
		t.Fatalf("url entities = %#v, want full url marked", entities)
	}
}

func TestAugmentBotAPIAutoEntitiesSkipsCommandInsideURL(t *testing.T) {
	r := newBotAPIAutoEntityRouter(t)
	message := "ссылка https://example.com/buy"
	entities := r.augmentBotAPIAutoEntities(message, nil)

	for _, entity := range entities {
		if entity.Type == domain.MessageEntityBotCommand {
			t.Fatalf("entities = %#v, /buy inside url must stay plain text", entities)
		}
	}
}

func TestBotAPISendMessagePersistsBotCommandEntities(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	message := "Нажми /buy для покупки"

	sent, err := fixture.router.BotAPISendMessage(fixture.ctx, fixture.bot.ID, fixture.owner.ID, message, nil, nil, false, false, 0)
	if err != nil {
		t.Fatalf("BotAPISendMessage: %v", err)
	}
	if sent.Body != message {
		t.Fatalf("body = %q, want %q", sent.Body, message)
	}
	got, ok := botAPIEntityText(message, sent.Entities, domain.MessageEntityBotCommand)
	if !ok || got != "/buy" {
		t.Fatalf("sent entities = %#v, want /buy persisted as bot_command", sent.Entities)
	}

	// 同一份实体必须对读侧可见：客户端只渲染服务端下发的 entity。
	received := privateBotAPIHistory(t, fixture, fixture.owner.ID, fixture.bot.ID)
	got, ok = botAPIEntityText(received.Body, received.Entities, domain.MessageEntityBotCommand)
	if !ok || got != "/buy" {
		t.Fatalf("history entities = %#v, want /buy visible to reader", received.Entities)
	}
}

func TestAugmentBotAPIAutoEntitiesPlainTextUnchanged(t *testing.T) {
	r := newBotAPIAutoEntityRouter(t)
	if entities := r.augmentBotAPIAutoEntities("просто текст без триггеров", nil); entities != nil {
		t.Fatalf("entities = %#v, want nil for text without triggers", entities)
	}
}
