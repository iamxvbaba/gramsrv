package domain

// Contact 是当前账号通讯录中的一个已注册联系人。
//
// FirstName/LastName/Phone/Note 是 owner 视角数据：同一个 target user 在不同
// owner 的通讯录里可以有不同备注，这些字段不回写 users 全局资料。
type Contact struct {
	User         User
	FirstName    string
	LastName     string
	Phone        string
	Note         string
	NoteEntities []MessageEntity
	Mutual       bool
	CloseFriend  bool
}

// ContactList 是通讯录查询结果。
type ContactList struct {
	Contacts []Contact
	Hash     int64
}

// CloseFriendsEditResult describes a full close-friends list replacement.
type CloseFriendsEditResult struct {
	AddedUserIDs   []int64
	RemovedUserIDs []int64
}

// BlockedContact is one owner-visible blocked peer.
type BlockedContact struct {
	User User
	Date int
}

// BlockedContactList describes contacts.getBlocked output.
type BlockedContactList struct {
	Blocked []BlockedContact
	Count   int
}

// ContactInput 描述一次 owner 视角联系人写入。
type ContactInput struct {
	ContactUserID            int64
	ClientID                 int64
	Phone                    string
	FirstName                string
	LastName                 string
	Note                     string
	NoteEntities             []MessageEntity
	AddPhonePrivacyException bool
}

// ImportedContact 是 contacts.importContacts 成功导入项。
type ImportedContact struct {
	UserID   int64
	ClientID int64
}

// ImportContactsResult 是 contacts.importContacts 的业务结果。
type ImportContactsResult struct {
	Imported      []ImportedContact
	Contacts      []Contact
	RetryContacts []int64
}

// UserSearchResult 是 contacts.search 的业务结果。
// MyResults 放当前账号通讯录内命中的用户；Results 放其他全局命中用户。
// ChannelResults 放未加入的公开 channel/supergroup 命中；已加入频道由 dialogs/resolve 路径呈现。
type UserSearchResult struct {
	MyResults        []User
	Results          []User
	MyChannelResults []Channel
	ChannelResults   []Channel
}

// PeerSettings 是当前 owner 看某个 peer 的可操作状态。
type PeerSettings struct {
	AddContact            bool
	BlockContact          bool
	ShareContact          bool
	NeedContactsException bool
	HiddenPeerSettingsBar bool
	BusinessBotID         int64
	BusinessBotManageURL  string
	BusinessBotPaused     bool
	BusinessBotCanReply   bool
}
