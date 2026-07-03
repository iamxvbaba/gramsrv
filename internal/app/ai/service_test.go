package ai

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type fakeProvider struct {
	name string
	text string
	err  error
	seen ProviderRequest
}

func (p *fakeProvider) Name() string {
	if p.name == "" {
		return "fake"
	}
	return p.name
}

func (p *fakeProvider) Compose(_ context.Context, req ProviderRequest) (domain.AIComposeText, error) {
	p.seen = req
	if p.err != nil {
		return domain.AIComposeText{}, p.err
	}
	return domain.AIComposeText{Text: p.text}, nil
}

type fakeStreamingProvider struct {
	fakeProvider
	chunks []string
	final  string
}

func (p *fakeStreamingProvider) ComposeStream(_ context.Context, req ProviderRequest, emit func(domain.AIComposeText) error) (domain.AIComposeText, error) {
	p.seen = req
	if p.err != nil {
		return domain.AIComposeText{}, p.err
	}
	for _, chunk := range p.chunks {
		if emit != nil {
			if err := emit(domain.AIComposeText{Text: chunk}); err != nil {
				return domain.AIComposeText{}, err
			}
		}
	}
	final := p.final
	if final == "" && len(p.chunks) > 0 {
		final = p.chunks[len(p.chunks)-1]
	}
	return domain.AIComposeText{Text: final}, nil
}

type denyLimiter struct{}

func (denyLimiter) Allow(context.Context, string, int, time.Duration) (bool, int, error) {
	return false, 60, nil
}

func TestListTonesReturnsDefaultsAndHash(t *testing.T) {
	svc := NewService(memory.NewAIComposeStore())

	tones, notModified, err := svc.ListTones(context.Background(), 1001, 0)
	if err != nil || notModified {
		t.Fatalf("ListTones = notModified %v err %v", notModified, err)
	}
	if len(tones.Tones) == 0 {
		t.Fatal("ListTones returned no default tones; TDesktop would hide AI compose button")
	}
	if tones.Hash == 0 {
		t.Fatal("ListTones hash = 0, want stable non-zero hash")
	}
	_, notModified, err = svc.ListTones(context.Background(), 1001, tones.Hash)
	if err != nil || !notModified {
		t.Fatalf("ListTones(hash) = notModified %v err %v, want notModified", notModified, err)
	}
}

func TestDefaultTonePromptsDiscourageEcho(t *testing.T) {
	for _, tone := range DefaultTones() {
		if !strings.Contains(tone.Prompt, "Avoid returning the exact original text") {
			t.Fatalf("default tone %q prompt = %q, want echo guard", tone.Slug, tone.Prompt)
		}
	}
}

func TestDefaultTonesMatchOfficialDirectory(t *testing.T) {
	want := []struct {
		slug    string
		title   string
		emojiID int64
	}{
		{"formal", "Formal", defaultToneEmojiFormal},
		{"short", "Short", defaultToneEmojiShort},
		{"tribal", "Tribal", defaultToneEmojiTribal},
		{"corp", "Corp", defaultToneEmojiCorp},
		{"zen", "Zen", defaultToneEmojiZen},
		{"biblical", "Biblical", defaultToneEmojiBiblical},
		{"viking", "Viking", defaultToneEmojiViking},
	}
	got := DefaultTones()
	if len(got) != len(want) {
		t.Fatalf("DefaultTones len = %d, want %d", len(got), len(want))
	}
	for i, tone := range got {
		if tone.Slug != want[i].slug || tone.Title != want[i].title {
			t.Fatalf("DefaultTones[%d] = %s/%s, want %s/%s", i, tone.Slug, tone.Title, want[i].slug, want[i].title)
		}
		if !tone.Default {
			t.Fatalf("DefaultTones[%d].Default = false, want true", i)
		}
		if tone.EmojiID != want[i].emojiID {
			t.Fatalf("DefaultTones[%d].EmojiID = %d, want %d", i, tone.EmojiID, want[i].emojiID)
		}
		if tone.ExampleEnglish == nil {
			t.Fatalf("DefaultTones[%d].ExampleEnglish = nil", i)
		}
	}
}

func TestComposeCallsProviderWithInstruction(t *testing.T) {
	provider := &fakeProvider{text: "Please send the file when you have a moment."}
	svc := NewService(memory.NewAIComposeStore(), WithProvider(provider))

	got, err := svc.Compose(context.Background(), domain.AIComposeRequest{
		UserID: 1001,
		Text:   domain.AIComposeText{Text: "send file when free"},
		Tone:   domain.AIComposeToneRef{Kind: domain.AIComposeToneRefDefault, DefaultTone: "formal"},
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if got.ResultText.Text != provider.text {
		t.Fatalf("Compose text = %q, want provider text", got.ResultText.Text)
	}
	if provider.seen.Instruction == "" || provider.seen.Tone.Slug != "formal" {
		t.Fatalf("provider request = %#v, want formal instruction", provider.seen)
	}
	for _, want := range []string{
		"Produce a visibly revised variant",
		"Make the selected style visible",
		"Avoid returning the exact original text",
	} {
		if !strings.Contains(provider.seen.Instruction, want) {
			t.Fatalf("instruction = %q, missing %q", provider.seen.Instruction, want)
		}
	}
}

func TestComposeInstructionDoesNotAnswerDraftQuestions(t *testing.T) {
	provider := &fakeProvider{text: "What is AI?"}
	svc := NewService(memory.NewAIComposeStore(), WithProvider(provider))

	if _, err := svc.Compose(context.Background(), domain.AIComposeRequest{
		UserID:    1001,
		Text:      domain.AIComposeText{Text: "what is AI"},
		Proofread: true,
	}); err != nil {
		t.Fatalf("Compose: %v", err)
	}
	for _, want := range []string{
		"not as a request, question, command, or chat message to answer",
		"Do not answer questions",
		"If the draft is a question, keep it as a question",
	} {
		if !strings.Contains(provider.seen.Instruction, want) {
			t.Fatalf("instruction = %q, missing %q", provider.seen.Instruction, want)
		}
	}
}

func TestComposeProofreadReturnsDiffText(t *testing.T) {
	provider := &fakeProvider{text: "Hello world."}
	svc := NewService(memory.NewAIComposeStore(), WithProvider(provider))

	got, err := svc.Compose(context.Background(), domain.AIComposeRequest{
		UserID:    1001,
		Text:      domain.AIComposeText{Text: "hello   world"},
		Proofread: true,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if got.DiffText == nil {
		t.Fatal("DiffText = nil, want proofread diff")
	}
	if got.DiffText.Text != "Hello world." || len(got.DiffText.Entities) != 1 {
		t.Fatalf("DiffText = %#v", got.DiffText)
	}
	ent := got.DiffText.Entities[0]
	if ent.Type != domain.MessageEntityDiffReplace || ent.Offset != 0 || ent.Length != 12 || ent.OldText != "hello   world" {
		t.Fatalf("diff entity = %#v", ent)
	}
}

func TestGetToneExampleUsesProviderForCustomTone(t *testing.T) {
	provider := &fakeProvider{text: "A crisp example."}
	store := memory.NewAIComposeStore()
	svc := NewService(store, WithProvider(provider))
	tone, err := svc.CreateTone(context.Background(), domain.AIComposeToneInput{
		UserID: 1001,
		Title:  "Crisp",
		Prompt: "Make it very crisp.",
	})
	if err != nil {
		t.Fatalf("CreateTone: %v", err)
	}
	got, err := svc.GetToneExample(context.Background(), 1001, domain.AIComposeToneRef{Kind: domain.AIComposeToneRefSlug, Slug: tone.Slug}, 2)
	if err != nil {
		t.Fatalf("GetToneExample: %v", err)
	}
	if got.To.Text != provider.text {
		t.Fatalf("example to = %q, want provider text", got.To.Text)
	}
	if provider.seen.Instruction == "" || provider.seen.Tone.ID != tone.ID {
		t.Fatalf("provider request = %#v, want custom tone instruction", provider.seen)
	}
}

func TestComposeRateLimited(t *testing.T) {
	svc := NewService(memory.NewAIComposeStore(), WithRateLimiter(denyLimiter{}, 1, time.Minute))
	_, err := svc.Compose(context.Background(), domain.AIComposeRequest{
		UserID:    1001,
		Text:      domain.AIComposeText{Text: "please polish this"},
		Proofread: true,
	})
	if !errors.Is(err, domain.ErrAIComposeRateLimited) {
		t.Fatalf("Compose err = %v, want ErrAIComposeRateLimited", err)
	}
}

func TestGenerateTextUsesProviderInstruction(t *testing.T) {
	provider := &fakeProvider{text: "Thanks for reaching out."}
	svc := NewService(memory.NewAIComposeStore(), WithProvider(provider))

	got, err := svc.GenerateText(context.Background(), domain.AITextGenerationRequest{
		UserID:      1001,
		Text:        domain.AIComposeText{Text: "hello"},
		Instruction: "Reply as the business owner.",
	})
	if err != nil {
		t.Fatalf("GenerateText: %v", err)
	}
	if got.Text != provider.text {
		t.Fatalf("GenerateText = %q, want provider text", got.Text)
	}
	if provider.seen.Instruction != "Reply as the business owner." || provider.seen.Request.Text.Text != "hello" {
		t.Fatalf("provider request = %#v", provider.seen)
	}
	if provider.seen.Purpose != ProviderPurposeTextGeneration {
		t.Fatalf("provider purpose = %q, want text generation", provider.seen.Purpose)
	}
}

func TestGenerateTextStreamUsesStreamingProvider(t *testing.T) {
	provider := &fakeStreamingProvider{
		chunks: []string{"Hel", "Hello"},
		final:  "Hello",
	}
	svc := NewService(memory.NewAIComposeStore(), WithProvider(provider))

	var chunks []string
	got, err := svc.GenerateTextStream(context.Background(), domain.AITextGenerationRequest{
		UserID:      1001,
		Text:        domain.AIComposeText{Text: "hello"},
		Instruction: "Reply as an assistant.",
	}, func(text domain.AIComposeText) error {
		chunks = append(chunks, text.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("GenerateTextStream: %v", err)
	}
	if got.Text != "Hello" {
		t.Fatalf("final text = %q, want Hello", got.Text)
	}
	if len(chunks) != 2 || chunks[0] != "Hel" || chunks[1] != "Hello" {
		t.Fatalf("chunks = %#v", chunks)
	}
	if provider.seen.Instruction != "Reply as an assistant." || provider.seen.Request.Text.Text != "hello" {
		t.Fatalf("provider request = %#v", provider.seen)
	}
	if provider.seen.Purpose != ProviderPurposeTextGeneration {
		t.Fatalf("provider purpose = %q, want text generation", provider.seen.Purpose)
	}
}

func TestGenerateTextStreamFallsBackToNonStreamingProvider(t *testing.T) {
	provider := &fakeProvider{text: "One-shot answer."}
	svc := NewService(memory.NewAIComposeStore(), WithProvider(provider))

	var chunks []string
	got, err := svc.GenerateTextStream(context.Background(), domain.AITextGenerationRequest{
		UserID:      1001,
		Text:        domain.AIComposeText{Text: "hello"},
		Instruction: "Reply.",
	}, func(text domain.AIComposeText) error {
		chunks = append(chunks, text.Text)
		return nil
	})
	if err != nil {
		t.Fatalf("GenerateTextStream: %v", err)
	}
	if got.Text != "One-shot answer." || len(chunks) != 1 || chunks[0] != "One-shot answer." {
		t.Fatalf("final=%q chunks=%#v", got.Text, chunks)
	}
}

func TestGenerateTextStreamDoesNotFallbackToLocalEcho(t *testing.T) {
	provider := &fakeStreamingProvider{
		fakeProvider: fakeProvider{err: domain.ErrAIComposeProviderTimeout},
	}
	svc := NewService(memory.NewAIComposeStore(), WithProviders(provider, LocalProvider{}))

	var chunks []string
	_, err := svc.GenerateTextStream(context.Background(), domain.AITextGenerationRequest{
		UserID:      1001,
		Text:        domain.AIComposeText{Text: "User: secret prompt\nAssistant: hidden reply\nUser: hello"},
		Instruction: "Reply.",
	}, func(text domain.AIComposeText) error {
		chunks = append(chunks, text.Text)
		return nil
	})
	if !errors.Is(err, domain.ErrAIComposeProviderTimeout) {
		t.Fatalf("GenerateTextStream err = %v, want provider timeout", err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks = %#v, want no local prompt echo", chunks)
	}
}

func TestGenerateTextRejectsLocalOnlyProvider(t *testing.T) {
	svc := NewService(memory.NewAIComposeStore(), WithProvider(LocalProvider{}))

	_, err := svc.GenerateText(context.Background(), domain.AITextGenerationRequest{
		UserID:      1001,
		Text:        domain.AIComposeText{Text: "User: hello"},
		Instruction: "Reply.",
	})
	if !errors.Is(err, domain.ErrAIComposeProviderUnavailable) {
		t.Fatalf("GenerateText err = %v, want provider unavailable", err)
	}
}

func TestCustomToneCRUDAndSave(t *testing.T) {
	store := memory.NewAIComposeStore()
	svc := NewService(store, WithClock(func() time.Time { return time.Unix(100, 0) }))

	tone, err := svc.CreateTone(context.Background(), domain.AIComposeToneInput{
		UserID:        1001,
		DisplayAuthor: true,
		Title:         "Sharp",
		Prompt:        "Make it direct and crisp.",
	})
	if err != nil {
		t.Fatalf("CreateTone: %v", err)
	}
	if tone.ID == 0 || tone.AccessHash == 0 || tone.Slug == "" || !tone.Creator || tone.AuthorID != 1001 {
		t.Fatalf("created tone = %#v", tone)
	}
	newTitle := "Brief"
	updated, err := svc.UpdateTone(context.Background(), domain.AIComposeToneUpdate{
		UserID: 1001,
		Ref: domain.AIComposeToneRef{
			Kind:       domain.AIComposeToneRefID,
			ID:         tone.ID,
			AccessHash: tone.AccessHash,
		},
		Title: &newTitle,
	})
	if err != nil {
		t.Fatalf("UpdateTone: %v", err)
	}
	if updated.Title != newTitle {
		t.Fatalf("updated title = %q, want %q", updated.Title, newTitle)
	}
	if err := svc.SaveTone(context.Background(), 2002, domain.AIComposeToneRef{Kind: domain.AIComposeToneRefSlug, Slug: tone.Slug}, false); err != nil {
		t.Fatalf("SaveTone: %v", err)
	}
	other, _, err := svc.ListTones(context.Background(), 2002, 0)
	if err != nil {
		t.Fatalf("ListTones other: %v", err)
	}
	var found bool
	for _, item := range other.Tones {
		if item.ID == tone.ID && item.Saved && !item.Creator {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved tone not visible for other user: %#v", other.Tones)
	}
	if err := svc.DeleteTone(context.Background(), 1001, domain.AIComposeToneRef{Kind: domain.AIComposeToneRefID, ID: tone.ID, AccessHash: tone.AccessHash}); err != nil {
		t.Fatalf("DeleteTone: %v", err)
	}
	if _, err := svc.GetTone(context.Background(), 1001, domain.AIComposeToneRef{Kind: domain.AIComposeToneRefSlug, Slug: tone.Slug}); !errors.Is(err, domain.ErrAIComposeToneNotFound) {
		t.Fatalf("GetTone after delete err = %v, want ErrAIComposeToneNotFound", err)
	}
}
