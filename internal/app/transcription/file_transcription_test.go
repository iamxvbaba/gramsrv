package transcription

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// A failed upload transcription is terminal: TranscribeFile must not reset the
// row (and thus restart the ASR job) when a client polls again.
func TestTranscribeFileFailedIsTerminal(t *testing.T) {
	ctx := context.Background()
	store := newFakeTranscriptionStore()
	store.rows[7] = domain.MessageTranscription{
		DocumentID: 7, TranscriptionID: 3, Status: domain.TranscriptionStatusFailed,
	}
	svc := NewService(store, nil, "http://asr.invalid", time.Second, zaptest.NewLogger(t))

	if _, err := svc.TranscribeFile(ctx, 42, domain.Document{ID: 7}); !errors.Is(err, ErrTranscriptionFailed) {
		t.Fatalf("TranscribeFile on failed row = %v, want ErrTranscriptionFailed", err)
	}
	row, found, err := store.GetTranscription(ctx, 7)
	if err != nil || !found {
		t.Fatalf("GetTranscription = %v, %v", found, err)
	}
	if row.Status != domain.TranscriptionStatusFailed {
		t.Fatalf("status = %q, want failed (must not reset)", row.Status)
	}
}
