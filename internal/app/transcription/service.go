package transcription

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	maxDocumentBytes = 25 << 20
	asrResponseLimit = 1 << 20
	dbWriteTimeout   = 10 * time.Second
)

var (
	// ErrUnavailable means the ASR backend is not configured (TELESRV_ASR_URL is empty); the caller maps it to TRANSCRIPTION_FAILED.
	ErrUnavailable = errors.New("transcription backend is not configured")
	// ErrDocument means the voice document is unreadable or over the size limit and cannot be sent to ASR.
	ErrDocument = errors.New("voice document is not available for transcription")
)

type FileSource interface {
	GetFile(ctx context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error)
}

type jobRequest struct {
	documentID      int64
	transcriptionID int64
	userID          int64
	peer            domain.Peer
	msgID           int
}

type Service struct {
	store     store.TranscriptionStore
	files     FileSource
	url       string
	client    *http.Client
	log       *zap.Logger
	requestMu sync.Mutex
	mu        sync.Mutex
	inflight  map[int64][]jobRequest
	// completed pushes updateTranscribedAudio after the background task writes the DB; it is
	// injected by main after the router is created, to avoid the app layer depending on rpc
	// in the reverse direction.
	completed func(ctx context.Context, userID int64, peer domain.Peer, msgID int, transcriptionID int64, text string)
}

func NewService(
	st store.TranscriptionStore,
	files FileSource,
	url string,
	timeout time.Duration,
	log *zap.Logger,
) *Service {
	return &Service{
		store:    st,
		files:    files,
		url:      strings.TrimRight(strings.TrimSpace(url), "/"),
		client:   &http.Client{Timeout: timeout},
		log:      log,
		inflight: map[int64][]jobRequest{},
	}
}

func (s *Service) SetCompletionHandler(handler func(ctx context.Context, userID int64, peer domain.Peer, msgID int, transcriptionID int64, text string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = handler
}

// Transcribe returns the currently known state: if done it returns the text directly, otherwise it ensures the background task is running and returns pending.
func (s *Service) Transcribe(
	ctx context.Context,
	userID int64,
	peer domain.Peer,
	msgID int,
	document domain.Document,
) (domain.TranscriptionResult, error) {
	if s == nil || s.url == "" {
		return domain.TranscriptionResult{}, ErrUnavailable
	}
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	if document.ID == 0 {
		return domain.TranscriptionResult{}, ErrDocument
	}
	row, found, err := s.store.GetTranscription(ctx, document.ID)
	if err != nil {
		return domain.TranscriptionResult{}, err
	}
	if found && row.Status == domain.TranscriptionStatusDone {
		return domain.TranscriptionResult{TranscriptionID: row.TranscriptionID, Text: row.Text}, nil
	}
	job := jobRequest{
		documentID:      document.ID,
		transcriptionID: row.TranscriptionID,
		userID:          userID,
		peer:            peer,
		msgID:           msgID,
	}
	if !found {
		job.transcriptionID, err = newTranscriptionID()
		if err != nil {
			return domain.TranscriptionResult{}, err
		}
		_, inserted, err := s.store.CreateTranscriptionPending(ctx, domain.PendingTranscriptionRequest{
			DocumentID:      job.documentID,
			TranscriptionID: job.transcriptionID,
			RequestedBy:     userID,
			Peer:            peer,
			MsgID:           msgID,
		})
		if err != nil {
			return domain.TranscriptionResult{}, err
		}
		if !inserted {
			// A concurrent first call already inserted the row: re-read, and if it completed in the meantime return the text directly.
			row, found, err = s.store.GetTranscription(ctx, document.ID)
			if err != nil {
				return domain.TranscriptionResult{}, err
			}
			if !found {
				return domain.TranscriptionResult{}, ErrDocument
			}
			if row.Status == domain.TranscriptionStatusDone {
				return domain.TranscriptionResult{TranscriptionID: row.TranscriptionID, Text: row.Text}, nil
			}
			job.transcriptionID = row.TranscriptionID
		}
	} else if row.Status == domain.TranscriptionStatusFailed {
		job.transcriptionID, err = newTranscriptionID()
		if err != nil {
			return domain.TranscriptionResult{}, err
		}
		if _, _, err := s.store.ResetTranscriptionPending(ctx, domain.PendingTranscriptionRequest{
			DocumentID:      job.documentID,
			TranscriptionID: job.transcriptionID,
			RequestedBy:     userID,
			Peer:            peer,
			MsgID:           msgID,
		}); err != nil {
			return domain.TranscriptionResult{}, err
		}
	}
	if !s.startJob(document, job) {
		return domain.TranscriptionResult{}, ErrUnavailable
	}
	return domain.TranscriptionResult{TranscriptionID: job.transcriptionID, Pending: true}, nil
}

const maxConcurrentTranscriptions = 4
const maxTranscriptionSubscribers = 128

func (s *Service) startJob(document domain.Document, job jobRequest) bool {
	s.mu.Lock()
	if subscribers, busy := s.inflight[job.documentID]; busy {
		for _, subscriber := range subscribers {
			if subscriber == job {
				s.mu.Unlock()
				return true
			}
		}
		if len(subscribers) >= maxTranscriptionSubscribers {
			s.mu.Unlock()
			return false
		}
		s.inflight[job.documentID] = append(subscribers, job)
		s.mu.Unlock()
		return true
	}
	if len(s.inflight) >= maxConcurrentTranscriptions {
		s.mu.Unlock()
		return false
	}
	s.inflight[job.documentID] = []jobRequest{job}
	s.mu.Unlock()
	go s.run(context.Background(), document, job)
	return true
}

func (s *Service) run(parent context.Context, document domain.Document, job jobRequest) {
	ctx, cancel := context.WithTimeout(parent, s.client.Timeout)
	defer cancel()
	text, err := s.transcribeDocument(ctx, document.ID)
	s.requestMu.Lock()
	defer s.requestMu.Unlock()
	defer func() { s.mu.Lock(); delete(s.inflight, job.documentID); s.mu.Unlock() }()
	dbCtx, cancelDB := context.WithTimeout(context.Background(), dbWriteTimeout)
	defer cancelDB()
	if err != nil {
		s.log.Warn("transcription failed",
			zap.Int64("document_id", job.documentID),
			zap.Int64("user_id", job.userID),
			zap.Error(err))
		if markErr := s.store.MarkTranscriptionFailed(dbCtx, job.documentID, err.Error()); markErr != nil {
			s.log.Warn("mark transcription failed", zap.Error(markErr))
		}
		return
	}
	if err := s.store.MarkTranscriptionDone(dbCtx, job.documentID, text); err != nil {
		s.log.Warn("mark transcription done", zap.Error(err))
		return
	}
	s.mu.Lock()
	completed := s.completed
	subscribers := append([]jobRequest(nil), s.inflight[job.documentID]...)
	s.mu.Unlock()
	if completed != nil {
		for _, subscriber := range subscribers {
			if subscriber.userID != 0 && subscriber.msgID > 0 {
				completed(dbCtx, subscriber.userID, subscriber.peer, subscriber.msgID, job.transcriptionID, text)
			}
		}
	}

}

func (s *Service) transcribeDocument(ctx context.Context, documentID int64) (string, error) {
	chunk, found, err := s.files.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("doc:%d", documentID),
		Limit:       maxDocumentBytes + 1,
	})
	if err != nil {
		return "", err
	}
	if !found || chunk.Total <= 0 || chunk.Total > maxDocumentBytes ||
		int64(len(chunk.Bytes)) != chunk.Total {
		return "", ErrDocument
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "voice.ogg")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(chunk.Bytes); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, asrResponseLimit))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("asr responded %d", resp.StatusCode)
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("decode asr response: %w", err)
	}
	return strings.TrimSpace(parsed.Text), nil
}

func newTranscriptionID() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	id := int64(binary.LittleEndian.Uint64(buf[:]) & math.MaxInt64)
	if id == 0 {
		id = 1
	}
	return id, nil
}
