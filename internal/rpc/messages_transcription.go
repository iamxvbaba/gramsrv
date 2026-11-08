package rpc

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// onMessagesTranscribeAudio is the entry point of messages.transcribeAudio. A cache hit returns
// the text directly; otherwise it registers/reuses a pending background ASR job, and on completion
// TranscribedAudioDelivery writes a durable updateTranscribedAudio in the same transaction (as
// upstream: the client sees the Pending flag and waits for the update with the final text).
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

// TranscribedAudioDelivery encodes the final transcription for one requester
// as a durable delivery to all of the user's sessions. transcription.Service
// writes it in the same transaction that marks the row done (v2: Core never
// pushes it directly). Offline devices recover by calling transcribeAudio again,
// which answers from the stored result.
func (r *Router) TranscribedAudioDelivery(
	userID int64,
	peer domain.Peer,
	msgID int,
	transcriptionID int64,
	text string,
) (store.DeliveryOutboxEnqueue, error) {
	if userID == 0 || peer.ID == 0 || msgID <= 0 {
		return store.DeliveryOutboxEnqueue{}, fmt.Errorf("transcribed audio delivery: invalid target")
	}
	payload, err := encodeDeliveryUpdate(&tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateTranscribedAudio{
			Peer:            tgPeer(peer),
			MsgID:           msgID,
			TranscriptionID: transcriptionID,
			Text:            text,
		}},
		Date: int(r.clock.Now().Unix()),
	})
	if err != nil {
		return store.DeliveryOutboxEnqueue{}, err
	}
	return store.DeliveryOutboxEnqueue{
		TargetUserID:   userID,
		Payload:        payload,
		RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	}, nil
}

// TranscribedAudioFileDelivery encodes the final upload transcription
// (messages.transcribeAudioFile) for one requester as a durable delivery to all
// of the user's sessions. transcription.Service writes it in the same
// transaction that marks the row done (v2: Core never pushes it directly).
// Offline devices recover by calling transcribeAudioFile again, which answers
// from the stored result.
func (r *Router) TranscribedAudioFileDelivery(userID int64, transcriptionID int64, text string) (store.DeliveryOutboxEnqueue, error) {
	if userID == 0 || transcriptionID == 0 {
		return store.DeliveryOutboxEnqueue{}, fmt.Errorf("transcribed audio file delivery: invalid target")
	}
	payload, err := encodeDeliveryUpdate(&tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateTranscribedAudioFile{
			TranscriptionID: transcriptionID,
			Text:            text,
		}},
		Date: int(r.clock.Now().Unix()),
	})
	if err != nil {
		return store.DeliveryOutboxEnqueue{}, err
	}
	return store.DeliveryOutboxEnqueue{
		TargetUserID:   userID,
		Payload:        payload,
		RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	}, nil
}
