package rpc

import (
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestTgUserPremiumFlag(t *testing.T) {
	now := time.Now().Unix()
	premium := domain.User{ID: 1, AccessHash: 2, FirstName: "P", PremiumUntil: int(now + 3600)}
	expired := domain.User{ID: 2, AccessHash: 2, FirstName: "E", PremiumUntil: int(now - 1)}
	bot := domain.User{ID: 3, AccessHash: 2, FirstName: "B", Bot: true, BotInfoVersion: 1, PremiumUntil: int(now + 3600)}

	if out := tgUser(premium); !out.Premium {
		t.Fatal("premium user missing premium flag")
	}
	if out := tgSelfUser(premium); !out.Premium {
		t.Fatal("premium self user missing premium flag")
	}
	if out := tgUser(expired); out.Premium {
		t.Fatal("expired user must not carry premium flag")
	}
	if out := tgUser(bot); out.Premium {
		t.Fatal("bot must never carry premium flag")
	}
}

func TestTgUserEmojiStatusHydration(t *testing.T) {
	now := time.Now().Unix()
	u := domain.User{
		ID: 1, AccessHash: 2, FirstName: "P",
		PremiumUntil:          int(now + 3600),
		EmojiStatusDocumentID: 777,
		EmojiStatusUntil:      int(now + 600),
	}
	out := tgUser(u)
	status, ok := out.GetEmojiStatus()
	if !ok {
		t.Fatal("premium user with status missing emoji_status field")
	}
	es, ok := status.(*tg.EmojiStatus)
	if !ok || es.DocumentID != 777 {
		t.Fatalf("emoji status = %#v, want document 777", status)
	}
	if until, ok := es.GetUntil(); !ok || until != int(now+600) {
		t.Fatalf("emoji status until = %d ok=%v", until, ok)
	}

	// 会员到期后 emoji status 残值不得内联进 user TL。
	u.PremiumUntil = int(now - 1)
	if _, ok := tgUser(u).GetEmojiStatus(); ok {
		t.Fatal("expired premium must not inline emoji_status")
	}

	// updateUserEmojiStatus 清除语义返回 emojiStatusEmpty。
	if _, ok := tgUserEmojiStatus(u, now).(*tg.EmojiStatusEmpty); !ok {
		t.Fatalf("tgUserEmojiStatus expired = %#v, want EmojiStatusEmpty", tgUserEmojiStatus(u, now))
	}
}
