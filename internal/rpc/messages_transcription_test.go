package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type stubTranscriptions struct {
	res     domain.TranscriptionResult
	err     error
	calls   int
	peer    domain.Peer
	msgID   int
	docID   int64
	handler func(ctx context.Context, userID int64, peer domain.Peer, msgID int, transcriptionID int64, text string)
}

func (s *stubTranscriptions) Transcribe(
	_ context.Context,
	_ int64,
	peer domain.Peer,
	msgID int,
	document domain.Document,
) (domain.TranscriptionResult, error) {
	s.calls++
	s.peer, s.msgID, s.docID = peer, msgID, document.ID
	return s.res, s.err
}

func (s *stubTranscriptions) SetCompletionHandler(
	handler func(ctx context.Context, userID int64, peer domain.Peer, msgID int, transcriptionID int64, text string),
) {
	s.handler = handler
}

func transcriptionVoiceMessage() domain.Message {
	return domain.Message{
		ID:   7,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		Media: &domain.MessageMedia{
			Kind:     domain.MessageMediaKindDocument,
			Voice:    true,
			Document: &domain.Document{ID: 555, MimeType: "audio/ogg", Size: 3},
		},
	}
}

func newTranscriptionTestRouter(t *testing.T, messages MessagesService, transcriptions TranscriptionService) *Router {
	t.Helper()
	return New(Config{}, Deps{
		Messages:       messages,
		Transcriptions: transcriptions,
	}, zaptest.NewLogger(t), clock.System)
}

func transcriptionErrType(t *testing.T, err error) *tgerr.Error {
	t.Helper()
	var tgErr *tgerr.Error
	if !errors.As(err, &tgErr) {
		t.Fatalf("err = %v (%T), want *tgerr.Error", err, err)
	}
	return tgErr
}

func TestTranscribeAudioReturnsPendingForVoiceMessage(t *testing.T) {
	stub := &stubTranscriptions{res: domain.TranscriptionResult{TranscriptionID: 77, Pending: true}}
	r := newTranscriptionTestRouter(t, &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{transcriptionVoiceMessage()},
	}}, stub)
	ctx := WithUserID(context.Background(), 1001)

	got, err := r.onMessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{
		Peer:  &tg.InputPeerUser{UserID: 1002},
		MsgID: 7,
	})
	if err != nil {
		t.Fatalf("transcribeAudio: %v", err)
	}
	res, ok := got.(*tg.MessagesTranscribedAudio)
	if !ok {
		t.Fatalf("got %T, want *tg.MessagesTranscribedAudio", got)
	}
	if !res.Pending || res.TranscriptionID != 77 || res.Text != "" {
		t.Fatalf("res = %+v, want pending id 77", res)
	}
	if stub.calls != 1 || stub.docID != 555 || stub.msgID != 7 {
		t.Fatalf("stub calls=%d doc=%d msg=%d", stub.calls, stub.docID, stub.msgID)
	}
	if stub.peer != (domain.Peer{Type: domain.PeerTypeUser, ID: 1002}) {
		t.Fatalf("stub peer = %+v", stub.peer)
	}
}

func TestTranscribeAudioReturnsCachedText(t *testing.T) {
	stub := &stubTranscriptions{res: domain.TranscriptionResult{TranscriptionID: 77, Text: "hello"}}
	r := newTranscriptionTestRouter(t, &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{transcriptionVoiceMessage()},
	}}, stub)
	ctx := WithUserID(context.Background(), 1001)

	got, err := r.onMessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{
		Peer:  &tg.InputPeerUser{UserID: 1002},
		MsgID: 7,
	})
	if err != nil {
		t.Fatalf("transcribeAudio: %v", err)
	}
	res := got.(*tg.MessagesTranscribedAudio)
	if res.Pending || res.Text != "hello" {
		t.Fatalf("res = %+v, want done text", res)
	}
}

func TestTranscribeAudioFailsWithoutVoiceMessage(t *testing.T) {
	nonVoice := transcriptionVoiceMessage()
	nonVoice.Media.Voice = false
	stub := &stubTranscriptions{res: domain.TranscriptionResult{Pending: true}}
	r := newTranscriptionTestRouter(t, &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{nonVoice},
	}}, stub)
	ctx := WithUserID(context.Background(), 1001)

	_, err := r.onMessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{
		Peer:  &tg.InputPeerUser{UserID: 1002},
		MsgID: 7,
	})
	tgErr := transcriptionErrType(t, err)
	if tgErr.Code != 400 || tgErr.Message != "TRANSCRIPTION_FAILED" {
		t.Fatalf("err = %+v, want 400 TRANSCRIPTION_FAILED", tgErr)
	}
	if stub.calls != 0 {
		t.Fatalf("stub calls = %d, want 0", stub.calls)
	}
}

func TestTranscribeAudioFailsWithoutService(t *testing.T) {
	r := newTranscriptionTestRouter(t, &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{transcriptionVoiceMessage()},
	}}, nil)
	ctx := WithUserID(context.Background(), 1001)

	_, err := r.onMessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{
		Peer:  &tg.InputPeerUser{UserID: 1002},
		MsgID: 7,
	})
	tgErr := transcriptionErrType(t, err)
	if tgErr.Message != "TRANSCRIPTION_FAILED" {
		t.Fatalf("err = %+v, want TRANSCRIPTION_FAILED", tgErr)
	}
}
