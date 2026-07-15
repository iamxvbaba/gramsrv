package domain

// UpdateState 是账号的 update 状态（pts/qts/seq/date）。
// 第一阶段空账号为零值；真实状态机属第二阶段。
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
