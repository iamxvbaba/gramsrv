package ai

import "telesrv/internal/domain"

func DefaultTones() []domain.AIComposeTone {
	return []domain.AIComposeTone{
		defaultTone("neutral", "Polish", "Make the draft clearer, smoother, and chat-ready while keeping the original intent. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("formal", "Formal", "Rewrite in a more professional, polished, and polite tone. Avoid casual wording and contractions. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("friendly", "Friendly", "Rewrite in a warmer, conversational tone with natural phrasing. Light contractions are acceptable. Avoid returning the exact original text when a safe wording improvement is possible."),
		defaultTone("concise", "Concise", "Rewrite the draft to be shorter and easier to scan while keeping the key meaning. Avoid returning the exact original text when a safe wording improvement is possible."),
	}
}

func defaultTone(slug, title, prompt string) domain.AIComposeTone {
	ex := domain.AIComposeToneExample{
		From: domain.AIComposeText{Text: "Can you send me the file when you have time?"},
		To:   domain.AIComposeText{Text: "Could you send me the file when you have a moment?"},
	}
	return domain.AIComposeTone{
		Default:        true,
		Slug:           slug,
		Title:          title,
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
