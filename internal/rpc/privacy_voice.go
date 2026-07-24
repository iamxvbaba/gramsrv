package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) ensureVoiceMessagesAllowed(ctx context.Context, senderUserID int64, peer domain.Peer, voiceOrRound bool) error {
	if !voiceOrRound || peer.Type != domain.PeerTypeUser || peer.ID == 0 || peer.ID == senderUserID || r.deps.Privacy == nil {
		return nil
	}
	allowed, err := r.deps.Privacy.CanSee(ctx, peer.ID, senderUserID, domain.PrivacyKeyVoiceMessages)
	if err != nil {
		return internalErr()
	}
	if !allowed {
		return chatSendVoicesForbiddenErr()
	}
	return nil
}

// preflightVoiceOrRound inspects upload attributes and referenced documents
// before resolveInputMedia materializes any uploaded blob. Referenced document
// misses are loaded in one bounded read-model batch.
func (r *Router) preflightVoiceOrRound(ctx context.Context, inputs []tg.InputMediaClass) (bool, error) {
	documentIDs := make([]int64, 0)
	seen := make(map[int64]struct{})
	for _, input := range inputs {
		switch media := input.(type) {
		case *tg.InputMediaUploadedDocument:
			if documentAttributesVoiceOrRound(domainDocumentAttributes(media.Attributes)) {
				return true, nil
			}
		case *tg.InputMediaDocument:
			ids, ok := inputDocumentCandidateIDs(media.ID)
			if !ok {
				continue // resolveInputMedia retains MEDIA_INVALID precedence.
			}
			for _, id := range ids {
				if id == 0 {
					continue
				}
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				documentIDs = append(documentIDs, id)
			}
		}
	}
	if len(documentIDs) == 0 || r.deps.Files == nil {
		return false, nil
	}
	documents, err := r.deps.Files.GetDocuments(ctx, documentIDs)
	if err != nil {
		return false, internalErr()
	}
	for _, document := range documents {
		if documentAttributesVoiceOrRound(document.Attributes) {
			return true, nil
		}
	}
	return false, nil
}

func documentAttributesVoiceOrRound(attributes []domain.DocumentAttribute) bool {
	for _, attribute := range attributes {
		switch attribute.Kind {
		case domain.DocAttrAudio:
			if attribute.Voice {
				return true
			}
		case domain.DocAttrVideo:
			if attribute.RoundMessage {
				return true
			}
		}
	}
	return false
}
