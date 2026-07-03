package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"telesrv/internal/domain"
)

func (r *Router) registerAiCompose(d *tg.ServerDispatcher) {
	d.OnAicomposeGetTones(r.onAicomposeGetTones)
	d.OnAicomposeCreateTone(r.onAicomposeCreateTone)
	d.OnAicomposeUpdateTone(r.onAicomposeUpdateTone)
	d.OnAicomposeSaveTone(r.onAicomposeSaveTone)
	d.OnAicomposeDeleteTone(r.onAicomposeDeleteTone)
	d.OnAicomposeGetTone(r.onAicomposeGetTone)
	d.OnAicomposeGetToneExample(r.onAicomposeGetToneExample)
}

func (r *Router) onAicomposeGetTones(ctx context.Context, hash int64) (tg.AicomposeTonesClass, error) {
	if r.deps.AICompose == nil {
		return &tg.AicomposeTones{Hash: 0, Tones: []tg.AiComposeToneClass{}, Users: []tg.UserClass{}}, nil
	}
	userID, err := r.currentAIComposeUserID(ctx)
	if err != nil {
		return nil, err
	}
	tones, notModified, err := r.deps.AICompose.ListTones(ctx, userID, hash)
	if err != nil {
		return nil, aiComposeErr(err)
	}
	if notModified {
		return &tg.AicomposeTonesNotModified{}, nil
	}
	return r.tgAIComposeTones(ctx, userID, tones), nil
}

func (r *Router) onAicomposeCreateTone(ctx context.Context, req *tg.AicomposeCreateToneRequest) (tg.AiComposeToneClass, error) {
	if r.deps.AICompose == nil {
		return nil, tgerr.New(400, "AICOMPOSE_TONE_INVALID")
	}
	userID, err := r.currentAIComposeUserID(ctx)
	if err != nil {
		return nil, err
	}
	tone, err := r.deps.AICompose.CreateTone(ctx, domain.AIComposeToneInput{
		UserID:        userID,
		DisplayAuthor: req.DisplayAuthor,
		EmojiID:       req.EmojiID,
		Title:         req.Title,
		Prompt:        req.Prompt,
	})
	if err != nil {
		return nil, aiComposeErr(err)
	}
	r.pushAIComposeTonesChanged(ctx, userID)
	return r.tgAIComposeTone(ctx, userID, tone), nil
}

func (r *Router) onAicomposeUpdateTone(ctx context.Context, req *tg.AicomposeUpdateToneRequest) (tg.AiComposeToneClass, error) {
	if r.deps.AICompose == nil {
		return nil, tgerr.New(400, "AICOMPOSE_TONE_INVALID")
	}
	userID, err := r.currentAIComposeUserID(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := domainAIComposeToneRef(req.Tone)
	if err != nil {
		return nil, err
	}
	update := domain.AIComposeToneUpdate{
		Ref:    ref,
		UserID: userID,
	}
	if req.Flags.Has(0) {
		v := req.DisplayAuthor
		update.DisplayAuthor = &v
	}
	if req.Flags.Has(1) {
		v := req.EmojiID
		update.EmojiID = &v
	}
	if req.Flags.Has(2) {
		v := req.Title
		update.Title = &v
	}
	if req.Flags.Has(3) {
		v := req.Prompt
		update.Prompt = &v
	}
	tone, err := r.deps.AICompose.UpdateTone(ctx, update)
	if err != nil {
		return nil, aiComposeErr(err)
	}
	r.pushAIComposeTonesChanged(ctx, userID)
	return r.tgAIComposeTone(ctx, userID, tone), nil
}

func (r *Router) onAicomposeSaveTone(ctx context.Context, req *tg.AicomposeSaveToneRequest) (bool, error) {
	if r.deps.AICompose == nil {
		return false, tgerr.New(400, "AICOMPOSE_TONE_INVALID")
	}
	userID, err := r.currentAIComposeUserID(ctx)
	if err != nil {
		return false, err
	}
	ref, err := domainAIComposeToneRef(req.Tone)
	if err != nil {
		return false, err
	}
	if err := r.deps.AICompose.SaveTone(ctx, userID, ref, req.Unsave); err != nil {
		return false, aiComposeErr(err)
	}
	r.pushAIComposeTonesChanged(ctx, userID)
	return true, nil
}

func (r *Router) onAicomposeDeleteTone(ctx context.Context, tone tg.InputAiComposeToneClass) (bool, error) {
	if r.deps.AICompose == nil {
		return false, tgerr.New(400, "AICOMPOSE_TONE_INVALID")
	}
	userID, err := r.currentAIComposeUserID(ctx)
	if err != nil {
		return false, err
	}
	ref, err := domainAIComposeToneRef(tone)
	if err != nil {
		return false, err
	}
	if err := r.deps.AICompose.DeleteTone(ctx, userID, ref); err != nil {
		return false, aiComposeErr(err)
	}
	r.pushAIComposeTonesChanged(ctx, userID)
	return true, nil
}

func (r *Router) onAicomposeGetTone(ctx context.Context, tone tg.InputAiComposeToneClass) (tg.AicomposeTonesClass, error) {
	if r.deps.AICompose == nil {
		return &tg.AicomposeTones{Hash: 0, Tones: []tg.AiComposeToneClass{}, Users: []tg.UserClass{}}, nil
	}
	userID, err := r.currentAIComposeUserID(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := domainAIComposeToneRef(tone)
	if err != nil {
		return nil, err
	}
	tones, err := r.deps.AICompose.GetTone(ctx, userID, ref)
	if err != nil {
		return nil, aiComposeErr(err)
	}
	return r.tgAIComposeTones(ctx, userID, tones), nil
}

func (r *Router) onAicomposeGetToneExample(ctx context.Context, req *tg.AicomposeGetToneExampleRequest) (*tg.AiComposeToneExample, error) {
	if r.deps.AICompose == nil {
		return nil, tgerr.New(400, "AICOMPOSE_TONE_INVALID")
	}
	userID, err := r.currentAIComposeUserID(ctx)
	if err != nil {
		return nil, err
	}
	ref, err := domainAIComposeToneRef(req.Tone)
	if err != nil {
		return nil, err
	}
	example, err := r.deps.AICompose.GetToneExample(ctx, userID, ref, req.Num)
	if err != nil {
		return nil, aiComposeErr(err)
	}
	return &tg.AiComposeToneExample{
		From: tgAIComposeText(example.From),
		To:   tgAIComposeText(example.To),
	}, nil
}

func (r *Router) onMessagesComposeMessageWithAI(ctx context.Context, req *tg.MessagesComposeMessageWithAIRequest) (*tg.MessagesComposedMessageWithAI, error) {
	if r.deps.AICompose == nil {
		return nil, tgerr.New(500, "AICOMPOSE_FAILED")
	}
	userID, err := r.currentAIComposeUserID(ctx)
	if err != nil {
		return nil, err
	}
	ref := domain.AIComposeToneRef{}
	if req.Flags.Has(2) || req.Tone != nil {
		ref, err = domainAIComposeOptionalToneRef(req.Tone)
		if err != nil {
			return nil, err
		}
	}
	in := domain.AIComposeRequest{
		UserID:          userID,
		Text:            domainAIComposeText(userID, req.Text),
		Proofread:       req.Proofread,
		Emojify:         req.Emojify,
		TranslateToLang: req.TranslateToLang,
		Tone:            ref,
	}
	result, err := r.deps.AICompose.Compose(ctx, in)
	if err != nil {
		return nil, aiComposeErr(err)
	}
	out := &tg.MessagesComposedMessageWithAI{
		ResultText: tgAIComposeText(result.ResultText),
	}
	if result.DiffText != nil {
		out.SetDiffText(tgAIComposeText(*result.DiffText))
	}
	return out, nil
}

func (r *Router) currentAIComposeUserID(ctx context.Context) (int64, error) {
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return 0, internalErr()
	}
	if !ok {
		return 0, authKeyUnregisteredErr()
	}
	return userID, nil
}

func domainAIComposeToneRef(tone tg.InputAiComposeToneClass) (domain.AIComposeToneRef, error) {
	switch t := tone.(type) {
	case *tg.InputAiComposeToneDefault:
		return domain.AIComposeToneRef{Kind: domain.AIComposeToneRefDefault, DefaultTone: t.Tone}, nil
	case *tg.InputAiComposeToneID:
		return domain.AIComposeToneRef{Kind: domain.AIComposeToneRefID, ID: t.ID, AccessHash: t.AccessHash}, nil
	case *tg.InputAiComposeToneSlug:
		return domain.AIComposeToneRef{Kind: domain.AIComposeToneRefSlug, Slug: t.Slug}, nil
	default:
		return domain.AIComposeToneRef{}, inputConstructorInvalidErr()
	}
}

func domainAIComposeOptionalToneRef(tone tg.InputAiComposeToneClass) (domain.AIComposeToneRef, error) {
	switch t := tone.(type) {
	case *tg.InputAiComposeToneDefault:
		if strings.TrimSpace(t.Tone) == "" {
			return domain.AIComposeToneRef{}, nil
		}
	}
	return domainAIComposeToneRef(tone)
}

func domainAIComposeText(viewerUserID int64, in tg.TextWithEntities) domain.AIComposeText {
	return domain.AIComposeText{
		Text:     in.Text,
		Entities: domainMessageEntitiesForViewer(viewerUserID, in.Entities),
	}
}

func tgAIComposeText(in domain.AIComposeText) tg.TextWithEntities {
	return tg.TextWithEntities{
		Text:     in.Text,
		Entities: tgMessageEntities(in.Entities),
	}
}

func (r *Router) tgAIComposeTones(ctx context.Context, userID int64, in domain.AIComposeTones) tg.AicomposeTonesClass {
	tones := make([]tg.AiComposeToneClass, 0, len(in.Tones))
	for _, tone := range in.Tones {
		tones = append(tones, r.tgAIComposeTone(ctx, userID, tone))
	}
	return &tg.AicomposeTones{
		Hash:  in.Hash,
		Tones: tones,
		Users: []tg.UserClass{},
	}
}

func (r *Router) tgAIComposeTone(_ context.Context, userID int64, in domain.AIComposeTone) tg.AiComposeToneClass {
	if in.Default {
		return &tg.AiComposeToneDefault{
			Tone:    in.Slug,
			EmojiID: in.EmojiID,
			Title:   in.Title,
		}
	}
	out := &tg.AiComposeTone{
		ID:         in.ID,
		AccessHash: in.AccessHash,
		Slug:       in.Slug,
		Title:      in.Title,
	}
	out.SetCreator(in.Creator || in.OwnerUserID == userID)
	if in.EmojiID != 0 {
		out.SetEmojiID(in.EmojiID)
	}
	if in.Prompt != "" {
		out.SetPrompt(in.Prompt)
	}
	if in.InstallsCount > 0 {
		out.SetInstallsCount(in.InstallsCount)
	}
	if in.AuthorID != 0 {
		out.SetAuthorID(in.AuthorID)
	}
	if in.ExampleEnglish != nil {
		out.SetExampleEnglish(tg.AiComposeToneExample{
			From: tgAIComposeText(in.ExampleEnglish.From),
			To:   tgAIComposeText(in.ExampleEnglish.To),
		})
	}
	return out
}

func (r *Router) pushAIComposeTonesChanged(ctx context.Context, userID int64) {
	r.pushUserMessageTransient(ctx, userID, "push ai compose tones update", &tg.UpdateShort{
		Update: &tg.UpdateAiComposeTones{},
		Date:   int(r.clock.Now().Unix()),
	})
}

func aiComposeErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrAIComposeToneNotFound):
		return tgerr.New(400, "TONE_NOT_FOUND")
	case errors.Is(err, domain.ErrAIComposeToneInvalid):
		return tgerr.New(400, "AICOMPOSE_TONE_INVALID")
	case errors.Is(err, domain.ErrAIComposeToneLimitExceeded):
		return tgerr.New(400, "TONES_SAVED_TOO_MANY")
	case errors.Is(err, domain.ErrAIComposeRateLimited):
		return floodWaitErr(60)
	case errors.Is(err, domain.ErrAIComposeInvalid):
		return inputRequestInvalidErr()
	case errors.Is(err, domain.ErrAIComposeDisabled):
		return tgerr.New(400, "AICOMPOSE_DISABLED")
	case errors.Is(err, domain.ErrAIComposeProviderTimeout):
		return tgerr.New(500, "AICOMPOSE_TIMEOUT")
	case errors.Is(err, domain.ErrAIComposeProviderUnavailable):
		return tgerr.New(500, "AICOMPOSE_FAILED")
	default:
		return internalErr()
	}
}
