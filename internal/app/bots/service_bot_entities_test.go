package bots

import (
	"testing"

	"telesrv/internal/domain"
)

func TestServiceBotReplyEntitiesCommandsAndURLsUseUTF16Offsets(t *testing.T) {
	text := "🙂 Send /cancel or https://telesrv.net/addstickers/fun_pack."
	entities := serviceBotReplyEntities(text, nil)

	assertEntity := func(typ domain.MessageEntityType, offset, length int) {
		t.Helper()
		for _, entity := range entities {
			if entity.Type == typ && entity.Offset == offset && entity.Length == length {
				return
			}
		}
		t.Fatalf("entities %+v missing %s at offset=%d length=%d", entities, typ, offset, length)
	}

	assertEntity(domain.MessageEntityBotCommand, 8, len("/cancel"))
	assertEntity(domain.MessageEntityURL, 19, len("https://telesrv.net/addstickers/fun_pack"))
}

func TestServiceBotReplyEntitiesSkipCommandsInsideURLsAndExplicitEntities(t *testing.T) {
	text := "Token: abc/def\nhttps://telesrv.net/addstickers/fun_pack\nUse /help"
	entities := serviceBotReplyEntities(text, []domain.MessageEntity{{
		Type:   domain.MessageEntityCode,
		Offset: len("Token: "),
		Length: len("abc/def"),
	}})

	commandCount := 0
	for _, entity := range entities {
		if entity.Type == domain.MessageEntityBotCommand {
			commandCount++
		}
		if entity.Type == domain.MessageEntityBotCommand && entity.Offset < len("Token: abc/def\nhttps://telesrv.net/") {
			t.Fatalf("unexpected command entity inside code/url: %+v in %+v", entity, entities)
		}
	}
	if commandCount != 1 {
		t.Fatalf("bot command entities = %d in %+v, want only /help", commandCount, entities)
	}
}

func TestServiceBotReplyEntitiesIgnoreBareSlashBeforeEmoji(t *testing.T) {
	entities := serviceBotReplyEntities("not a command /🙂 but /help is", nil)
	commandCount := 0
	for _, entity := range entities {
		if entity.Type == domain.MessageEntityBotCommand {
			commandCount++
		}
	}
	if commandCount != 1 {
		t.Fatalf("bot command entities = %d in %+v, want only /help", commandCount, entities)
	}
}
