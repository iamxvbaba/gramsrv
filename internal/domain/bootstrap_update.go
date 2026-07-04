package domain

import "time"

type BootstrapUpdateJobKind string

const (
	BootstrapUpdateJobLoginMessage BootstrapUpdateJobKind = "login_message"
)

type BootstrapUpdateJobStatus string

const (
	BootstrapUpdateJobPending    BootstrapUpdateJobStatus = "pending"
	BootstrapUpdateJobReady      BootstrapUpdateJobStatus = "ready"
	BootstrapUpdateJobPublishing BootstrapUpdateJobStatus = "publishing"
	BootstrapUpdateJobPublished  BootstrapUpdateJobStatus = "published"
	BootstrapUpdateJobFailed     BootstrapUpdateJobStatus = "failed"
)

const BootstrapUpdateMaxAttempts = 5

// BootstrapUpdateJob defers first-session account updates until the client has
// received its initial updates baseline. Pending jobs do not occupy pts.
type BootstrapUpdateJob struct {
	ID           int64
	Kind         BootstrapUpdateJobKind
	UserID       int64
	AuthKeyID    [8]byte
	SessionID    int64
	MessageBoxID int
	Status       BootstrapUpdateJobStatus
	Attempts     int
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ReadyAt      time.Time
	PublishedAt  time.Time
}
