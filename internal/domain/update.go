package domain

// UpdateState is the account's update state (pts/qts/seq/date).
// In phase one an empty account is the zero value; the real state machine belongs to phase two.
type UpdateState struct {
	Pts  int
	Qts  int
	Date int
	Seq  int
}

// UpdateStateCommitMode describes what a physically delivered update-state
// response proves. Every delivered baseline advances the device-local
// confirmed cursor; only an explicitly audited getState baseline also proves
// that retention may advance the client-observed cursor to the same point.
type UpdateStateCommitMode uint8

const (
	UpdateStateCommitDeliveredOnly UpdateStateCommitMode = iota + 1
	UpdateStateCommitDeliveredAndObservedBaseline
)
