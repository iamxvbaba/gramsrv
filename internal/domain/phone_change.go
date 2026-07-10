package domain

// PhoneChangeRequest 是账号改号的持久化命令。PG 实现必须把 User、Event 与
// dispatch outbox 放在同一事务；Exclude* 精确排除发起设备，因为当前设备从
// account.changePhone 的 User 返回值更新本地状态。
type PhoneChangeRequest struct {
	UserID           int64
	Phone            string
	Date             int
	ExcludeAuthKeyID [8]byte
	ExcludeSessionID int64
}

type PhoneChangeResult struct {
	User    User
	Event   UpdateEvent
	Changed bool
}
