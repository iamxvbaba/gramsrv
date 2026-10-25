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
	Version   int64
	Since     time.Time
	Until     time.Time
	AppealURL string
	Reason    string
	Actor     string
	CommandID string
	UpdatedAt time.Time
}

// AccountFreezeNotification is a durable, coalesced online refresh for one
// viewer. UpdateUser itself has no pts; offline clients always recover from the
// authoritative viewer-scoped user projection instead of replaying this row.
type AccountFreezeNotification struct {
	ID           int64
	TargetUserID int64
	FrozenUserID int64
	Version      int64
	Frozen       bool
	Attempts     int
}

// AccountDeletionNotification is the durable, coalesced tombstone refresh one
// viewer still owes after an account was deleted.
//
// It is the deletion counterpart of AccountFreezeNotification. A freeze already
// had this queue; deletion used to reach viewers through an online-only fan-out,
// which silently skipped every viewer that happened to be offline at that moment
// and left the client rendering a stale name with a live composer. Offline
// viewers receive this row once they come back, so the tombstone converges for
// everyone rather than only for whoever was connected.
type AccountDeletionNotification struct {
	ID            int64
	TargetUserID  int64
	DeletedUserID int64
	Attempts      int
}
