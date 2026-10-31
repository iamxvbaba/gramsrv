package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// onMessagesTranscribeAudio is the entry point for messages.transcribeAudio. A cache hit returns
// the text directly; otherwise it registers/reuses a pending background ASR task and pushes
// updateTranscribedAudio via PushTranscribedAudioUpdate on completion (matching official behavior:
// the client waits for the update using the Pending flag to get the final text).
func (r *Router) onMessagesTranscribeAudio(
	ctx context.Context,
	req *tg.MessagesTranscribeAudioRequest,
) (any, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Transcriptions == nil {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	document, ok := r.voiceMessageDocument(ctx, userID, peer, req.MsgID)
	if !ok {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	res, err := r.deps.Transcriptions.Transcribe(ctx, userID, peer, req.MsgID, document)
	if err != nil {
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	}
	return &tg.MessagesTranscribedAudio{
		Pending:         res.Pending,
		TranscriptionID: res.TranscriptionID,
		Text:            res.Text,
	}, nil
}

// voiceMessageDocument loads the voice document at (peer, msgID) from the viewer's perspective,
// with the same structure as loadMessagePoll (channels are scoped by channelID; private chats
// must match the peer).
func (r *Router) voiceMessageDocument(
	ctx context.Context,
	userID int64,
	peer domain.Peer,
	msgID int,
) (domain.Document, bool) {
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return domain.Document{}, false
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
		if err != nil {
			return domain.Document{}, false
		}
		for _, msg := range history.Messages {
			if doc, ok := voiceDocumentOf(msg.ID, msg.Media, msgID); ok {
				return doc, true
			}
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return domain.Document{}, false
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{msgID})
		if err != nil {
			return domain.Document{}, false
		}
		for _, msg := range list.Messages {
			if msg.Peer != peer {
				continue
			}
			if doc, ok := voiceDocumentOf(msg.ID, msg.Media, msgID); ok {
				return doc, true
			}
		}
	}
	return domain.Document{}, false
}

func voiceDocumentOf(id int, media *domain.MessageMedia, msgID int) (domain.Document, bool) {
	if id != msgID || media == nil ||
		media.Kind != domain.MessageMediaKindDocument ||
		!media.Voice || media.Document == nil ||
		media.Document.ID == 0 {
		return domain.Document{}, false
	}
	return *media.Document, true
}

// PushTranscribedAudioUpdate pushes the completed transcription final text to the requester's
// online sessions. Called by the transcription.Service completion callback (background ctx, no
// session affiliation).
func (r *Router) PushTranscribedAudioUpdate(
	ctx context.Context,
	userID int64,
	peer domain.Peer,
	msgID int,
	transcriptionID int64,
	text string,
) {
	if userID == 0 || peer.ID == 0 || msgID <= 0 {
		return
	}
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateTranscribedAudio{
			Peer:            tgPeer(peer),
			MsgID:           msgID,
			TranscriptionID: transcriptionID,
			Text:            text,
		}},
		Date: int(r.clock.Now().Unix()),
	})
}
