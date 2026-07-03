package messages

import (
	"context"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

type BusinessAITextGenerator interface {
	GenerateText(ctx context.Context, req domain.AITextGenerationRequest) (domain.AIComposeText, error)
}

type AIBusinessAutomationProvider struct {
	generator BusinessAITextGenerator
}

func NewAIBusinessAutomationProvider(generator BusinessAITextGenerator) AIBusinessAutomationProvider {
	return AIBusinessAutomationProvider{generator: generator}
}

func (p AIBusinessAutomationProvider) BusinessAutomationReplies(ctx context.Context, input BusinessAutomationReplyInput) ([]domain.QuickReplyMessage, error) {
	if p.generator == nil {
		return nil, nil
	}
	body := strings.TrimSpace(input.TriggerMessage.Body)
	if body == "" {
		return nil, nil
	}
	out, err := p.generator.GenerateText(ctx, domain.AITextGenerationRequest{
		UserID: input.OwnerUserID,
		Text: domain.AIComposeText{
			Text:     input.TriggerMessage.Body,
			Entities: append([]domain.MessageEntity(nil), input.TriggerMessage.Entities...),
		},
		Instruction: businessAIReplyInstruction(input),
	})
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(out.Text)
	if text == "" || utf8.RuneCountInString(text) > domain.MaxMessageTextLength || len(out.Entities) > domain.MaxMessageEntityCount {
		return nil, nil
	}
	return []domain.QuickReplyMessage{{
		ID:       1,
		Date:     input.Now,
		Message:  text,
		Entities: append([]domain.MessageEntity(nil), out.Entities...),
	}}, nil
}

func businessAIReplyInstruction(input BusinessAutomationReplyInput) string {
	parts := []string{
		"Write one brief, helpful chat reply from the business owner to the customer.",
		"Return only the message text, without explanations, markdown fences, labels, quotes, or signatures.",
		"Do not claim to be an AI and do not mention internal automation rules.",
	}
	switch input.Kind {
	case domain.BusinessAutomationGreeting:
		parts = append(parts, "Context: this is a greeting or first response for the conversation.")
	case domain.BusinessAutomationAway:
		parts = append(parts, "Context: the business owner may be away; acknowledge the customer naturally without promising exact availability.")
	case domain.BusinessAutomationAI:
		parts = append(parts, "Context: this is a connected business bot reply on behalf of the owner.")
	}
	if len(input.Templates) > 0 {
		parts = append(parts, "Owner quick reply templates may be used as style or policy hints:")
		count := 0
		for _, tmpl := range input.Templates {
			text := strings.TrimSpace(tmpl.Message)
			if text == "" {
				continue
			}
			parts = append(parts, "- "+trimInstructionLine(text, 240))
			count++
			if count >= 3 {
				break
			}
		}
	}
	return strings.Join(parts, "\n")
}

func trimInstructionLine(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(text), " ")
	if maxRunes <= 0 || utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	var b strings.Builder
	count := 0
	for _, r := range text {
		if count >= maxRunes {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}
