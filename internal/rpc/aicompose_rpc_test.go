package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	aiapp "telesrv/internal/app/ai"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newAIComposeTestRouter(t *testing.T) *Router {
	t.Helper()
	return New(Config{}, Deps{
		AICompose: aiapp.NewService(memory.NewAIComposeStore()),
	}, zaptest.NewLogger(t), clock.System)
}

func TestAIComposeGetTonesReturnsDefaultsAndNotModified(t *testing.T) {
	r := newAIComposeTestRouter(t)
	ctx := WithUserID(context.Background(), 1001)

	got, err := r.onAicomposeGetTones(ctx, 0)
	if err != nil {
		t.Fatalf("getTones: %v", err)
	}
	tones, ok := got.(*tg.AicomposeTones)
	if !ok {
		t.Fatalf("getTones = %T, want *tg.AicomposeTones", got)
	}
	if tones.Hash == 0 || len(tones.Tones) == 0 {
		t.Fatalf("getTones hash/tones = %d/%d, want non-empty", tones.Hash, len(tones.Tones))
	}
	again, err := r.onAicomposeGetTones(ctx, tones.Hash)
	if err != nil {
		t.Fatalf("getTones(hash): %v", err)
	}
	if _, ok := again.(*tg.AicomposeTonesNotModified); !ok {
		t.Fatalf("getTones(hash) = %T, want tonesNotModified", again)
	}
}

func TestMessagesComposeMessageWithAIProofread(t *testing.T) {
	r := newAIComposeTestRouter(t)
	ctx := WithUserID(context.Background(), 1001)

	got, err := r.onMessagesComposeMessageWithAI(ctx, &tg.MessagesComposeMessageWithAIRequest{
		Proofread: true,
		Text:      tg.TextWithEntities{Text: "hello   world"},
	})
	if err != nil {
		t.Fatalf("composeMessageWithAI: %v", err)
	}
	if got.ResultText.Text != "hello world." {
		t.Fatalf("compose result = %q, want local polished text", got.ResultText.Text)
	}
	diff, ok := got.GetDiffText()
	if !ok {
		t.Fatal("DiffText = nil, want proofread diff")
	}
	if len(diff.Entities) != 1 {
		t.Fatalf("DiffText entities = %d, want 1", len(diff.Entities))
	}
	replace, ok := diff.Entities[0].(*tg.MessageEntityDiffReplace)
	if !ok {
		t.Fatalf("DiffText entity = %T, want *tg.MessageEntityDiffReplace", diff.Entities[0])
	}
	if replace.OldText != "hello   world" || replace.Offset != 0 || replace.Length != len([]rune(got.ResultText.Text)) {
		t.Fatalf("DiffText replace = %#v", replace)
	}
}

func TestMessagesComposeMessageWithAIEmptyDefaultToneIsOptional(t *testing.T) {
	r := newAIComposeTestRouter(t)
	ctx := WithUserID(context.Background(), 1001)

	req := &tg.MessagesComposeMessageWithAIRequest{
		Text: tg.TextWithEntities{Text: "hello world"},
	}
	req.SetTranslateToLang("en")
	req.SetTone(&tg.InputAiComposeToneDefault{})
	got, err := r.onMessagesComposeMessageWithAI(ctx, req)
	if err != nil {
		t.Fatalf("composeMessageWithAI empty default tone: %v", err)
	}
	if got.ResultText.Text == "" {
		t.Fatal("compose result is empty")
	}

	if _, err := r.onAicomposeGetTone(ctx, &tg.InputAiComposeToneDefault{}); !tgerr.Is(err, "TONE_NOT_FOUND") {
		t.Fatalf("getTone empty default err = %v, want TONE_NOT_FOUND", err)
	}
}

func TestAIComposeCustomToneCRUD(t *testing.T) {
	r := newAIComposeTestRouter(t)
	ctx := WithUserID(context.Background(), 1001)

	createdRaw, err := r.onAicomposeCreateTone(ctx, &tg.AicomposeCreateToneRequest{
		DisplayAuthor: true,
		Title:         "Sharp",
		Prompt:        "Make it crisp.",
	})
	if err != nil {
		t.Fatalf("createTone: %v", err)
	}
	created, ok := createdRaw.(*tg.AiComposeTone)
	if !ok {
		t.Fatalf("createTone = %T, want *tg.AiComposeTone", createdRaw)
	}
	if created.ID == 0 || created.AccessHash == 0 || created.Slug == "" || !created.GetCreator() {
		t.Fatalf("created tone = %#v", created)
	}
	update := &tg.AicomposeUpdateToneRequest{Tone: &tg.InputAiComposeToneID{ID: created.ID, AccessHash: created.AccessHash}}
	update.SetTitle("Brief")
	updatedRaw, err := r.onAicomposeUpdateTone(ctx, update)
	if err != nil {
		t.Fatalf("updateTone: %v", err)
	}
	updated := updatedRaw.(*tg.AiComposeTone)
	if updated.Title != "Brief" {
		t.Fatalf("updated title = %q, want Brief", updated.Title)
	}
	got, err := r.onAicomposeGetTone(ctx, &tg.InputAiComposeToneSlug{Slug: created.Slug})
	if err != nil {
		t.Fatalf("getTone: %v", err)
	}
	one := got.(*tg.AicomposeTones)
	if len(one.Tones) != 1 {
		t.Fatalf("getTone tones = %d, want 1", len(one.Tones))
	}
	if ok, err := r.onAicomposeDeleteTone(ctx, &tg.InputAiComposeToneID{ID: created.ID, AccessHash: created.AccessHash}); err != nil || !ok {
		t.Fatalf("deleteTone = %v/%v, want true/nil", ok, err)
	}
	if _, err := r.onAicomposeGetTone(ctx, &tg.InputAiComposeToneSlug{Slug: created.Slug}); !tgerr.Is(err, "TONE_NOT_FOUND") {
		t.Fatalf("getTone after delete err = %v, want TONE_NOT_FOUND", err)
	}
}

func TestAIComposeToneLimitUsesClientError(t *testing.T) {
	r := newAIComposeTestRouter(t)
	ctx := WithUserID(context.Background(), 1001)

	for i := 0; i < domain.AIComposeToneSavedLimitDefault; i++ {
		if _, err := r.onAicomposeCreateTone(ctx, &tg.AicomposeCreateToneRequest{
			Title:  "Tone",
			Prompt: "Make it crisp.",
		}); err != nil {
			t.Fatalf("createTone %d: %v", i, err)
		}
	}
	_, err := r.onAicomposeCreateTone(ctx, &tg.AicomposeCreateToneRequest{
		Title:  "Extra",
		Prompt: "Make it crisp.",
	})
	if !tgerr.Is(err, "TONES_SAVED_TOO_MANY") {
		t.Fatalf("createTone over limit err = %v, want TONES_SAVED_TOO_MANY", err)
	}
}

func TestAIComposeToneMutationPushesRefreshUpdate(t *testing.T) {
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		AICompose: aiapp.NewService(memory.NewAIComposeStore()),
		Sessions:  sessions,
	}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1001), [8]byte{1}), 77)

	if _, err := r.onAicomposeCreateTone(ctx, &tg.AicomposeCreateToneRequest{
		Title:  "Sharp",
		Prompt: "Make it crisp.",
	}); err != nil {
		t.Fatalf("createTone: %v", err)
	}
	got := sessions.lastUserPush()
	short, ok := got.(*tg.UpdateShort)
	if !ok {
		t.Fatalf("pushed update = %T, want *tg.UpdateShort", got)
	}
	if _, ok := short.Update.(*tg.UpdateAiComposeTones); !ok {
		t.Fatalf("pushed short update = %T, want *tg.UpdateAiComposeTones", short.Update)
	}
	if snap := sessions.snapshot(); snap.sessionID != 77 {
		t.Fatalf("excluded session = %d, want 77", snap.sessionID)
	}
}
