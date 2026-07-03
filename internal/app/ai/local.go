package ai

import (
	"context"
	"strings"

	"telesrv/internal/domain"
)

// LocalProvider 是默认开发 provider：不出网、不记录内容，做确定性轻量整理。
type LocalProvider struct{}

func (LocalProvider) Name() string { return "local" }

func (LocalProvider) Compose(_ context.Context, req ProviderRequest) (domain.AIComposeText, error) {
	if req.Purpose == ProviderPurposeTextGeneration {
		return domain.AIComposeText{}, domain.ErrAIComposeProviderUnavailable
	}
	text := localTransform(req.Request.Text.Text, req.Request, req.Tone)
	return domain.AIComposeText{Text: text}, nil
}

func localTransform(text string, req domain.AIComposeRequest, tone domain.AIComposeTone) string {
	text = strings.TrimSpace(collapseWhitespace(text))
	if text == "" {
		return text
	}
	if req.TranslateToLang != "" {
		return text
	}
	switch tone.Slug {
	case "formal":
		return ensureSentencePunctuation(text)
	case "friendly":
		return ensureSentencePunctuation(text)
	case "concise":
		return trimVerboseLead(text)
	default:
		return ensureSentencePunctuation(text)
	}
}

func collapseWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.Join(strings.Fields(lines[i]), " ")
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func ensureSentencePunctuation(s string) string {
	if s == "" {
		return s
	}
	var last rune
	for _, r := range s {
		last = r
	}
	switch last {
	case '.', '!', '?', ':', ';', '。', '！', '？':
		return s
	default:
		return s + "."
	}
}

func trimVerboseLead(s string) string {
	prefixes := []string{
		"I just wanted to ",
		"I wanted to ",
		"Just wanted to ",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return ensureSentencePunctuation(strings.TrimPrefix(s, p))
		}
	}
	return ensureSentencePunctuation(s)
}
