package domain

// Contact is a registered contact in the current account's address book.
//
// FirstName/LastName/Phone/Note are owner-perspective data: the same target user can have different
// notes in different owners' address books, and these fields are not written back to the global users profile.
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

// ContactList is an address book query result.
type ContactList struct {
	Contacts []Contact
	Hash     int64
}

// ContactProjectionBatch is a viewer-owned contact read model for fan-out
// projection. Contacts[viewerID][targetUserID] is the same row GetMany(viewer)
// would return; PersonalPhotos carries that viewer's personal photo overrides
// for the same target set.
type ContactProjectionBatch struct {
	Contacts       map[int64]map[int64]Contact
	PersonalPhotos map[int64]map[int64]ProfilePhotoRef
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

// ContactInput describes one owner-perspective contact write.
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

// ImportedContact is a successfully imported item of contacts.importContacts.
type ImportedContact struct {
	UserID   int64
	ClientID int64
}

// ImportContactsResult is the business result of contacts.importContacts.
type ImportContactsResult struct {
	Imported      []ImportedContact
	Contacts      []Contact
	RetryContacts []int64
}

// UserSearchResult is the business result of contacts.search.
// MyResults holds users matched within the current account's address book; Results holds other global matches.
// ChannelResults holds matches for public channels/supergroups not joined; joined channels are surfaced via the dialogs/resolve path.
type UserSearchResult struct {
	MyResults        []User
	Results          []User
	MyChannelResults []Channel
	ChannelResults   []Channel
}

// PeerSettings is the actionable state of a peer as seen by the current owner.
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
