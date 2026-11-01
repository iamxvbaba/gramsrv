package domain

import "time"

type TranscriptionStatus string

const (
	TranscriptionStatusPending TranscriptionStatus = "pending"
	TranscriptionStatusDone    TranscriptionStatus = "done"
	TranscriptionStatusFailed  TranscriptionStatus = "failed"
)

// MessageTranscription is the transcription result for one voice document, deduplicated by document_id:
// the same file referenced by multiple messages/users runs ASR only once.
type MessageTranscription struct {
	DocumentID      int64
	TranscriptionID int64
	Status          TranscriptionStatus
	Text            string
	RequestedBy     int64
	Peer            Peer
	MsgID           int
	Failure         string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type PendingTranscriptionRequest struct {
	DocumentID      int64
	TranscriptionID int64
	RequestedBy     int64
	Peer            Peer
	MsgID           int
}

// TranscriptionResult is the synchronous answer of messages.transcribeAudio:
// Pending=true means the background ASR is still running and will be pushed via updateTranscribedAudio when done.
type TranscriptionResult struct {
	TranscriptionID int64
	Pending         bool
	Text            string
}
