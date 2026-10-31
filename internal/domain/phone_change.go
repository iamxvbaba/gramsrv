package domain

// PhoneChangeRequest is the persisted command for an account phone change. updateUserPhone carries no PTS,
// so this command only maintains the phone fact; the Exclude* fields remain in the DTO for call-boundary compatibility,
// while online non-PTS notifications are excluded by the RPC layer per originating device.
type PhoneChangeRequest struct {
	UserID           int64
	Phone            string
	Date             int
	ExcludeAuthKeyID [8]byte
	ExcludeSessionID int64
}

type PhoneChangeResult struct {
	User    User
	Changed bool
}
