package ai

import "telesrv/internal/domain"

const (
	defaultToneEmojiFormal   int64 = 4963195715414131468
	defaultToneEmojiShort    int64 = 5089558399201313570
	defaultToneEmojiTribal   int64 = 4906965037207257780
	defaultToneEmojiCorp     int64 = 5103015433682813448
	defaultToneEmojiZen      int64 = 5129871924314243582
	defaultToneEmojiBiblical int64 = 5006296094481580688
	defaultToneEmojiViking   int64 = 5102866720440189629
)

func DefaultTones() []domain.AIComposeTone {
	return []domain.AIComposeTone{
		defaultTone("formal", "Formal", defaultToneEmojiFormal, "Rewrite in a more professional, polished, and polite tone. Avoid casual wording and contractions. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("short", "Short", defaultToneEmojiShort, "Rewrite the draft to be shorter and easier to scan while keeping the key meaning. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("tribal", "Tribal", defaultToneEmojiTribal, "Rewrite in a primal, chant-like style with short punchy phrasing and playful energy. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("corp", "Corp", defaultToneEmojiCorp, "Rewrite in a business-corporate style with clear, action-oriented wording. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("zen", "Zen", defaultToneEmojiZen, "Rewrite in a calm, mindful, minimal style with gentle phrasing. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("biblical", "Biblical", defaultToneEmojiBiblical, "Rewrite in a solemn, archaic, scripture-like style while preserving the original meaning. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("viking", "Viking", defaultToneEmojiViking, "Rewrite in a bold, saga-like style with confident phrasing. Avoid returning the exact original text when a safe wording improvement is possible."),
	}
}

func defaultTone(slug, title string, emojiID int64, prompt string) domain.AIComposeTone {
	ex := domain.AIComposeToneExample{
		From: domain.AIComposeText{Text: "Can you send me the file when you have time?"},
		To:   domain.AIComposeText{Text: "Could you send me the file when you have a moment?"},
	}
	return domain.AIComposeTone{
		Default:        true,
		Slug:           slug,
		Title:          title,
		EmojiID:        emojiID,
		Prompt:         prompt,
		ExampleEnglish: &ex,
	}
}

func exampleSource(num int) domain.AIComposeText {
	switch num {
	case 2:
		return domain.AIComposeText{Text: "I can join the meeting later today if that works."}
	case 3:
		return domain.AIComposeText{Text: "Please take a look and tell me what you think."}
	default:
		return domain.AIComposeText{Text: "Can you send me the file when you have time?"}
	}
}
