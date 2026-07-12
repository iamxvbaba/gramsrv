package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesTranslateText(ctx context.Context, req *tg.MessagesTranslateTextRequest) (*tg.MessagesTranslateResult, error) {
	if req == nil {
		return nil, inputTextEmptyErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Translation == nil {
		return nil, translationsDisabledErr()
	}
	if err := r.requireTranslationUser(ctx, userID); err != nil {
		return nil, err
	}

	peerInput, idMode := req.GetPeer()
	ids, idsSet := req.GetID()
	texts, textMode := req.GetText()
	if idMode != idsSet || idMode == textMode {
		return nil, inputTextEmptyErr()
	}
	request := domain.TranslationRequest{
		UserID: userID,
		ToLang: req.ToLang,
		Tone:   req.Tone,
	}
	if idMode {
		peer, err := r.checkedTranslationPeer(ctx, userID, peerInput)
		if err != nil {
			return nil, peerIDInvalidErr()
		}
		request.Peer = peer
		request.IDs = append([]int(nil), ids...)
	} else {
		request.Texts = make([]domain.TranslationText, 0, len(texts))
		for _, text := range texts {
			request.Texts = append(request.Texts, domain.TranslationText{
				Text:     text.Text,
				Entities: domainMessageEntitiesForViewer(userID, text.Entities),
			})
		}
	}
	result, err := r.deps.Translation.Translate(ctx, request)
	if err != nil {
		return nil, translationRPCErr(err)
	}
	out := &tg.MessagesTranslateResult{Result: make([]tg.TextWithEntities, 0, len(result.Texts))}
	for _, text := range result.Texts {
		out.Result = append(out.Result, tg.TextWithEntities{
			Text:     text.Text,
			Entities: tgMessageEntities(text.Entities),
		})
	}
	return out, nil
}

func (r *Router) onMessagesTogglePeerTranslations(ctx context.Context, req *tg.MessagesTogglePeerTranslationsRequest) (bool, error) {
	if req == nil || req.Peer == nil {
		return false, peerIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Translation == nil {
		return false, translationsDisabledErr()
	}
	if err := r.requireTranslationUser(ctx, userID); err != nil {
		return false, err
	}
	peer, err := r.checkedTranslationPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, peerIDInvalidErr()
	}
	if _, err := r.deps.Translation.SetPeerDisabled(ctx, userID, peer, req.Disabled); err != nil {
		return false, translationRPCErr(err)
	}
	return true, nil
}

func (r *Router) requireTranslationUser(ctx context.Context, userID int64) error {
	if r.deps.Users == nil {
		return nil
	}
	self, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return internalErr()
	}
	if self.Bot {
		return botMethodInvalidErr()
	}
	return nil
}

func (r *Router) checkedTranslationPeer(ctx context.Context, userID int64, input tg.InputPeerClass) (domain.Peer, error) {
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return domain.Peer{}, err
	}
	if peer.Type != domain.PeerTypeUser || r.deps.Users == nil {
		return peer, nil
	}
	var userInput tg.InputUserClass
	switch value := input.(type) {
	case *tg.InputPeerSelf:
		userInput = &tg.InputUserSelf{}
	case *tg.InputPeerUser:
		userInput = &tg.InputUser{UserID: value.UserID, AccessHash: value.AccessHash}
	default:
		return domain.Peer{}, peerIDInvalidErr()
	}
	_, found, err := r.userFromInput(ctx, userID, userInput)
	if err != nil {
		return domain.Peer{}, internalErr()
	}
	if !found {
		return domain.Peer{}, peerIDInvalidErr()
	}
	return peer, nil
}

func translationRPCErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrTranslationInputEmpty):
		return inputTextEmptyErr()
	case errors.Is(err, domain.ErrTranslationInputTooLong):
		return inputTextTooLongErr()
	case errors.Is(err, domain.ErrTranslationLanguageInvalid):
		return toLangInvalidErr()
	case errors.Is(err, domain.ErrTranslationMessageInvalid), errors.Is(err, domain.ErrMessageIDInvalid):
		return msgIDInvalidErr()
	case errors.Is(err, domain.ErrTranslationPeerInvalid), errors.Is(err, domain.ErrChannelInvalid), errors.Is(err, domain.ErrChannelPrivate):
		return peerIDInvalidErr()
	case errors.Is(err, domain.ErrTranslationRateLimited):
		return translateReqQuotaExceededErr()
	case errors.Is(err, domain.ErrTranslationDisabled):
		return translationsDisabledErr()
	case errors.Is(err, domain.ErrTranslationTimeout):
		return translationTimeoutErr()
	case errors.Is(err, domain.ErrTranslationProviderUnavailable):
		return translateReqFailedErr()
	default:
		return internalErr()
	}
}
