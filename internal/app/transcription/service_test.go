package transcription

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type fakeTranscriptionStore struct {
	mu   sync.Mutex
	rows map[int64]domain.MessageTranscription
}

func newFakeTranscriptionStore() *fakeTranscriptionStore {
	return &fakeTranscriptionStore{rows: map[int64]domain.MessageTranscription{}}
}

func (s *fakeTranscriptionStore) GetTranscription(_ context.Context, documentID int64) (domain.MessageTranscription, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[documentID]
	return row, ok, nil
}

func (s *fakeTranscriptionStore) CreateTranscriptionPending(_ context.Context, req domain.PendingTranscriptionRequest) (domain.MessageTranscription, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[req.DocumentID]; ok {
		return domain.MessageTranscription{}, false, nil
	}
	row := domain.MessageTranscription{
		DocumentID:      req.DocumentID,
		TranscriptionID: req.TranscriptionID,
		Status:          domain.TranscriptionStatusPending,
		RequestedBy:     req.RequestedBy,
		Peer:            req.Peer,
		MsgID:           req.MsgID,
	}
	s.rows[req.DocumentID] = row
	return row, true, nil
}

func (s *fakeTranscriptionStore) ResetTranscriptionPending(_ context.Context, req domain.PendingTranscriptionRequest) (domain.MessageTranscription, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[req.DocumentID]
	if !ok || row.Status != domain.TranscriptionStatusFailed {
		return domain.MessageTranscription{}, false, nil
	}
	row.Status = domain.TranscriptionStatusPending
	row.TranscriptionID = req.TranscriptionID
	row.RequestedBy = req.RequestedBy
	row.Peer = req.Peer
	row.MsgID = req.MsgID
	row.Failure = ""
	s.rows[req.DocumentID] = row
	return row, true, nil
}

func (s *fakeTranscriptionStore) MarkTranscriptionDone(_ context.Context, documentID int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[documentID]
	row.Status = domain.TranscriptionStatusDone
	row.Text = text
	s.rows[documentID] = row
	return nil
}

func (s *fakeTranscriptionStore) MarkTranscriptionFailed(_ context.Context, documentID int64, failure string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[documentID]
	if row.Status == domain.TranscriptionStatusDone {
		return nil
	}
	row.Status = domain.TranscriptionStatusFailed
	row.Failure = failure
	s.rows[documentID] = row
	return nil
}

func (s *fakeTranscriptionStore) row(t *testing.T, documentID int64) domain.MessageTranscription {
	t.Helper()
	row, ok, err := s.GetTranscription(context.Background(), documentID)
	if err != nil || !ok {
		t.Fatalf("store row: ok=%v err=%v", ok, err)
	}
	return row
}

type fakeFileSource struct {
	data  []byte
	total int64
}

func (f *fakeFileSource) GetFile(_ context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error) {
	return domain.FileChunk{Bytes: f.data, Total: f.total}, true, nil
}

func newASRServer(t *testing.T, status int, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/inference" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}
		if _, _, err := r.FormFile("file"); err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func waitForDone(t *testing.T, store *fakeTranscriptionStore, documentID int64) domain.MessageTranscription {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, ok, err := store.GetTranscription(context.Background(), documentID)
		if err == nil && ok && row.Status == domain.TranscriptionStatusDone {
			return row
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("transcription %d did not finish", documentID)
	return domain.MessageTranscription{}
}

func TestTranscribeStartsJobAndPushesResult(t *testing.T) {
	store := newFakeTranscriptionStore()
	var hits atomic.Int32
	server := newASRServer(t, http.StatusOK, `{"text":"hello from asr"}`, &hits)
	var pushed struct {
		mu   sync.Mutex
		user int64
		msg  int
		text string
	}
	svc := NewService(store, &fakeFileSource{data: []byte("ogg"), total: 3}, server.URL, 5*time.Second, zaptest.NewLogger(t))
	svc.SetCompletionHandler(func(_ context.Context, userID int64, _ domain.Peer, msgID int, _ int64, text string) {
		pushed.mu.Lock()
		defer pushed.mu.Unlock()
		pushed.user, pushed.msg, pushed.text = userID, msgID, text
	})

	document := domain.Document{ID: 555}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1002}
	first, err := svc.Transcribe(context.Background(), 1001, peer, 7, document)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if !first.Pending || first.TranscriptionID == 0 {
		t.Fatalf("first = %+v, want pending with id", first)
	}
	row := waitForDone(t, store, 555)
	if row.Text != "hello from asr" {
		t.Fatalf("stored text = %q", row.Text)
	}
	pushed.mu.Lock()
	user, msg, text := pushed.user, pushed.msg, pushed.text
	pushed.mu.Unlock()
	if user != 1001 || msg != 7 || text != "hello from asr" {
		t.Fatalf("push = user %d msg %d text %q", user, msg, text)
	}
	again, err := svc.Transcribe(context.Background(), 1001, peer, 7, document)
	if err != nil {
		t.Fatalf("second transcribe: %v", err)
	}
	if again.Pending || again.Text != "hello from asr" || again.TranscriptionID != row.TranscriptionID {
		t.Fatalf("second = %+v, want cached done", again)
	}
	if hits.Load() != 1 {
		t.Fatalf("asr hits = %d, want 1", hits.Load())
	}
}

func TestTranscribeWithoutBackendIsUnavailable(t *testing.T) {
	svc := NewService(newFakeTranscriptionStore(), &fakeFileSource{}, "", time.Second, zaptest.NewLogger(t))
	if _, err := svc.Transcribe(context.Background(), 1001, domain.Peer{Type: domain.PeerTypeUser, ID: 1002}, 7, domain.Document{ID: 1}); err != ErrUnavailable {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestTranscribeServesCachedDoneWithoutASR(t *testing.T) {
	store := newFakeTranscriptionStore()
	store.rows[9] = domain.MessageTranscription{
		DocumentID: 9, TranscriptionID: 42, Status: domain.TranscriptionStatusDone, Text: "cached",
	}
	var hits atomic.Int32
	server := newASRServer(t, http.StatusOK, `{"text":"should not be called"}`, &hits)
	svc := NewService(store, &fakeFileSource{data: []byte("x"), total: 1}, server.URL, 5*time.Second, zaptest.NewLogger(t))
	got, err := svc.Transcribe(context.Background(), 1001, domain.Peer{Type: domain.PeerTypeUser, ID: 1002}, 7, domain.Document{ID: 9})
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got.Pending || got.Text != "cached" || got.TranscriptionID != 42 {
		t.Fatalf("got = %+v, want cached done", got)
	}
	if hits.Load() != 0 {
		t.Fatalf("asr hits = %d, want 0", hits.Load())
	}
}

func TestTranscribeRetriesFailedRow(t *testing.T) {
	store := newFakeTranscriptionStore()
	store.rows[11] = domain.MessageTranscription{
		DocumentID: 11, TranscriptionID: 7, Status: domain.TranscriptionStatusFailed, Failure: "asr down",
	}
	var hits atomic.Int32
	server := newASRServer(t, http.StatusOK, `{"text":"recovered"}`, &hits)
	svc := NewService(store, &fakeFileSource{data: []byte("ogg"), total: 3}, server.URL, 5*time.Second, zaptest.NewLogger(t))
	got, err := svc.Transcribe(context.Background(), 1001, domain.Peer{Type: domain.PeerTypeUser, ID: 1002}, 7, domain.Document{ID: 11})
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if !got.Pending {
		t.Fatalf("got = %+v, want pending retry", got)
	}
	row := waitForDone(t, store, 11)
	if row.Text != "recovered" || row.TranscriptionID == 7 {
		t.Fatalf("row = %+v, want recovered text with new id", row)
	}
}

func TestTranscribeMarksFailedWhenASRRejects(t *testing.T) {
	store := newFakeTranscriptionStore()
	var hits atomic.Int32
	server := newASRServer(t, http.StatusInternalServerError, "boom", &hits)
	svc := NewService(store, &fakeFileSource{data: []byte("ogg"), total: 3}, server.URL, 5*time.Second, zaptest.NewLogger(t))
	if _, err := svc.Transcribe(context.Background(), 1001, domain.Peer{Type: domain.PeerTypeUser, ID: 1002}, 7, domain.Document{ID: 13}); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, ok, err := store.GetTranscription(context.Background(), 13)
		if err == nil && ok && row.Status == domain.TranscriptionStatusFailed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("row never marked failed")
}

func TestTranscribeRejectsOversizedDocument(t *testing.T) {
	store := newFakeTranscriptionStore()
	svc := NewService(store, &fakeFileSource{data: nil, total: maxDocumentBytes + 1}, "http://127.0.0.1:1", 5*time.Second, zaptest.NewLogger(t))
	if _, err := svc.Transcribe(context.Background(), 1001, domain.Peer{Type: domain.PeerTypeUser, ID: 1002}, 7, domain.Document{ID: 15}); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, ok, err := store.GetTranscription(context.Background(), 15)
		if err == nil && ok && row.Status == domain.TranscriptionStatusFailed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("oversized row never marked failed")
}
