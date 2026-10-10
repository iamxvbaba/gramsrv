package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// onMessagesTranscribeAudio 是 messages.transcribeAudio 的入口。缓存命中直接回文本；
// 否则登记/复用 pending 后台 ASR 任务，完成时经 PushTranscribedAudioUpdate 推
// updateTranscribedAudio（与官方一致：客户端凭 Pending 标志等 update 拿终稿）。
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

// voiceMessageDocument 按 viewer 视角加载 (peer, msgID) 上的 voice 文档，
// 结构与 loadMessagePoll 一致（频道按 channelID 限定，私聊需比对 peer）。
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

// PushTranscribedAudioUpdate 把完成的转写终稿推给发起方的在线 session。
// 由 transcription.Service 的完成回调调用（background ctx，无 session 归属）。
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
