package domain

import "time"

type TranscriptionStatus string

const (
	TranscriptionStatusPending TranscriptionStatus = "pending"
	TranscriptionStatusDone    TranscriptionStatus = "done"
	TranscriptionStatusFailed  TranscriptionStatus = "failed"
)

// MessageTranscription 是一条 voice 文档的转写结果，按 document_id 去重：
// 同一文件被多条消息/多个用户引用时只跑一次 ASR。
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

// TranscriptionResult 是 messages.transcribeAudio 的同步答案：
// Pending=true 表示后台 ASR 仍在跑，完成后经 updateTranscribedAudio 推送。
type TranscriptionResult struct {
	TranscriptionID int64
	Pending         bool
	Text            string
}
