package domain

import (
	"testing"
	"time"
)

func TestPremiumActiveAt(t *testing.T) {
	now := time.Now().Unix()
	cases := []struct {
		name string
		user User
		want bool
	}{
		{"zero until = non-member", User{}, false},
		{"future until = member", User{PremiumUntil: int(now + 3600)}, true},
		{"past until = expired", User{PremiumUntil: int(now - 1)}, false},
		{"exactly now = expired (boundary excluded)", User{PremiumUntil: int(now)}, false},
		{"bot is never Premium (belt and suspenders)", User{Bot: true, BotInfoVersion: 1, PremiumUntil: int(now + 3600)}, false},
	}
	for _, tc := range cases {
		if got := tc.user.PremiumActiveAt(now); got != tc.want {
			t.Errorf("%s: PremiumActiveAt = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEmojiStatusActiveAt(t *testing.T) {
	now := time.Now().Unix()
	premium := int(now + 3600)
	cases := []struct {
		name string
		user User
		want bool
	}{
		{"not set", User{PremiumUntil: premium}, false},
		{"permanent status", User{PremiumUntil: premium, EmojiStatusDocumentID: 7}, true},
		{"unexpired status", User{PremiumUntil: premium, EmojiStatusDocumentID: 7, EmojiStatusUntil: int(now + 60)}, true},
		{"expired status", User{PremiumUntil: premium, EmojiStatusDocumentID: 7, EmojiStatusUntil: int(now - 60)}, false},
		{"residual value not sent after Premium expires", User{PremiumUntil: int(now - 1), EmojiStatusDocumentID: 7}, false},
	}
	for _, tc := range cases {
		if got := tc.user.EmojiStatusActiveAt(now); got != tc.want {
			t.Errorf("%s: EmojiStatusActiveAt = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTrimMessageReactionsToUserMaxTiers(t *testing.T) {
	reactions := []MessageReaction{
		{Type: MessageReactionEmoji, Emoticon: "👍"},
		{Type: MessageReactionEmoji, Emoticon: "❤"},
		{Type: MessageReactionEmoji, Emoticon: "🔥"},
		{Type: MessageReactionEmoji, Emoticon: "🎉"},
	}
	// The default tier (including 0 = unfilled by an old caller) trims to 1, keeping the newest at the tail.
	for _, max := range []int{0, -1, MaxMessageReactionsPerUser} {
		got := TrimMessageReactionsToUserMax(reactions, max)
		if len(got) != 1 || got[0].Emoticon != "🎉" {
			t.Fatalf("default tier (max=%d) = %+v, want [🎉]", max, got)
		}
	}
	// The Premium tier trims to 3, keeping the newest at the tail.
	got := TrimMessageReactionsToUserMax(reactions, MessageReactionsUserMax(true))
	if len(got) != 3 || got[0].Emoticon != "❤" || got[2].Emoticon != "🎉" {
		t.Fatalf("premium tier = %+v, want [❤ 🔥 🎉]", got)
	}
	// A requested value beyond the Premium tier is capped.
	if got := TrimMessageReactionsToUserMax(reactions, 99); len(got) != MaxMessageReactionsPerUserPremium {
		t.Fatalf("over-cap tier = %d entries, want %d", len(got), MaxMessageReactionsPerUserPremium)
	}
	if MessageReactionsUserMax(false) != MaxMessageReactionsPerUser {
		t.Fatalf("MessageReactionsUserMax(false) = %d", MessageReactionsUserMax(false))
	}
}

func TestPinnedDialogsLimitTiers(t *testing.T) {
	cases := []struct {
		folderID int
		premium  bool
		want     int
	}{
		{DialogMainFolderID, false, MaxPinnedDialogsMainFolder},
		{DialogMainFolderID, true, MaxPinnedDialogsMainFolderPremium},
		{DialogArchiveFolderID, false, MaxPinnedDialogsArchiveFolder},
		{DialogArchiveFolderID, true, MaxPinnedDialogsArchiveFolderPremium},
	}
	for _, tc := range cases {
		if got := PinnedDialogsLimit(tc.folderID, tc.premium); got != tc.want {
			t.Errorf("PinnedDialogsLimit(%d, %v) = %d, want %d", tc.folderID, tc.premium, got, tc.want)
		}
	}
}

func TestPremiumGiftMessageValidatesUTF16EntityBounds(t *testing.T) {
	valid := PremiumGiftMessage{
		Text: "A😀B",
		// A=1 UTF-16 unit, 😀=2 units.
		Entities: []MessageEntity{{Type: MessageEntityBold, Offset: 1, Length: 2}},
	}
	if !valid.Valid() {
		t.Fatal("valid UTF-16 entity was rejected")
	}
	for _, invalid := range []PremiumGiftMessage{
		{Text: "A😀B", Entities: []MessageEntity{{Type: MessageEntityBold, Offset: 1, Length: 4}}},
		{Text: "A😀B", Entities: []MessageEntity{{Type: MessageEntityBold, Offset: 2, Length: 1}}},
		{Text: "hello", Entities: []MessageEntity{{Type: MessageEntityBold, Offset: -1, Length: 1}}},
		{Text: "hello", Entities: []MessageEntity{{Type: MessageEntityBold, Offset: 0, Length: 0}}},
		{Text: "hello", Entities: []MessageEntity{{Type: MessageEntityBold, Offset: 6, Length: 1}}},
		{Text: "hello", Entities: []MessageEntity{{Type: MessageEntityURL, Offset: 0, Length: 5}}},
		{Text: "hello", Entities: []MessageEntity{{Type: MessageEntityCustomEmoji, Offset: 0, Length: 5}}},
	} {
		if invalid.Valid() {
			t.Fatalf("invalid entity bounds accepted: %+v", invalid)
		}
	}
}

func TestPremiumPlanBounds(t *testing.T) {
	base := PremiumPlan{
		Months: 3, DurationDays: 90, AmountStars: 750, Enabled: true,
		Label: "3 months", Version: 1,
	}
	if !base.Valid() {
		t.Fatal("normal Premium plan is invalid")
	}
	for _, mutate := range []func(*PremiumPlan){
		func(p *PremiumPlan) { p.Months = MaxPremiumPlanMonths + 1 },
		func(p *PremiumPlan) { p.DurationDays = MaxPremiumPlanDurationDays + 1 },
		func(p *PremiumPlan) { p.AmountStars = MaxPremiumPlanAmountStars + 1 },
	} {
		plan := base
		mutate(&plan)
		if plan.Valid() {
			t.Fatalf("out-of-range Premium plan accepted: %+v", plan)
		}
	}
}
