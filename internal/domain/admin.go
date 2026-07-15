package domain

import "time"

type AdminCommandStatus string

const (
	AdminCommandRunning   AdminCommandStatus = "running"
	AdminCommandCompleted AdminCommandStatus = "completed"
	AdminCommandFailed    AdminCommandStatus = "failed"
)

type AdminCommand struct {
	CommandID    string
	Actor        string
	Action       string
	TargetUserID int64
	TargetPeer   Peer
	DryRun       bool
	Reason       string
	RequestJSON  []byte
	ResultJSON   []byte
	Status       AdminCommandStatus
	Error        string
	CreatedAt    time.Time
	CompletedAt  *time.Time
}

// AccountFreeze is the durable account-level read-only state advertised to
// Telegram clients through help.getAppConfig. Until is the appeal/deletion
// deadline; reaching it does not silently unfreeze the account.
type AccountFreeze struct {
	UserID    int64
	Frozen    bool
	Since     time.Time
	Until     time.Time
	AppealURL string
	Reason    string
	Actor     string
	CommandID string
	UpdatedAt time.Time
}
