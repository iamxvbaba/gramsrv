package domain

// PeerType identifies the peer type a dialog belongs to.
type PeerType string

const (
	PeerTypeUser    PeerType = "user"
	PeerTypeChannel PeerType = "channel"
	// PeerTypeCommunity identifies a Layer 228 Community container. Communities
	// have dialog pin/notify state but never own messages, read boundaries or pts.
	PeerTypeCommunity PeerType = "community"
	// PeerTypeFolder is used only in dialog pin events to express dialogPeerFolder
	// (the archive folder row itself is pinned/unpinned); ID is folder_id.
	PeerTypeFolder PeerType = "folder"
)

const (
	// DialogMainFolderID is the folder_id of TDesktop's main chat list.
	DialogMainFolderID = 0
	// DialogArchiveFolderID is Telegram's conventional folder_id for the archive.
	DialogArchiveFolderID = 1
	// User-defined filters are only allowed from DialogCustomFolderMinID up.
	DialogCustomFolderMinID = 2
	// MaxDialogFolders limits a user's custom filter count, so unbounded configuration cannot drag down startup sync.
	MaxDialogFolders = 100
	// MaxDialogFolderPeers limits the include/exclude/pinned peer count within one filter.
	MaxDialogFolderPeers = 100
	// MaxDialogFolderTitleRunes matches the short-title semantics of a Telegram folder title.
	MaxDialogFolderTitleRunes = 64
	// MaxDialogDraftsPerUser bounds messages.getAllDrafts / clearAllDrafts work.
	MaxDialogDraftsPerUser = 1000
	// MaxPinnedDialogsMainFolder/MaxPinnedDialogsArchiveFolder match TDesktop's
	// built-in default limits (appConfig dialogs_pinned_limit_default=5 /
	// dialogs_folder_pinned_limit_default=100); exceeding them returns
	// PINNED_DIALOGS_TOO_MUCH, a server-side fallback that keeps the two sides from drifting after client truncation.
	MaxPinnedDialogsMainFolder    = 5
	MaxPinnedDialogsArchiveFolder = 100
	// The Premium tier matches appConfig dialogs_pinned_limit_premium=10 /
	// dialogs_folder_pinned_limit_premium=200. The server tier must be ≥ the client-declared
	// tier, or a Premium user pinning a 6th chat is rejected and the UI rolls back.
	MaxPinnedDialogsMainFolderPremium    = 10
	MaxPinnedDialogsArchiveFolderPremium = 200
)

// PinnedDialogsLimit returns the pin limit tier for a folder dimension (Premium dual tier).
func PinnedDialogsLimit(folderID int, premium bool) int {
	if folderID == DialogArchiveFolderID {
		if premium {
			return MaxPinnedDialogsArchiveFolderPremium
		}
		return MaxPinnedDialogsArchiveFolder
	}
	if premium {
		return MaxPinnedDialogsMainFolderPremium
	}
	return MaxPinnedDialogsMainFolder
}

// Peer is the business-layer peer value object, independent of TL types.
type Peer struct {
	Type PeerType
	ID   int64
}

// IsSelfUser reports whether this peer is the current user's own user peer.
func (p Peer) IsSelfUser(userID int64) bool {
	return userID != 0 && p.Type == PeerTypeUser && p.ID == userID
}

// Dialog is one chat summary for an account.
type Dialog struct {
	Peer           Peer
	ChannelLeft    bool
	FolderID       int
	TopMessage     int
	TopMessageDate int
	// HistoryClearAnchorID/Date are internal owner-view projection metadata.
	// They are not TL dialog fields; dialog response assembly uses them to
	// materialize the matching messageActionHistoryClear top payload.
	HistoryClearAnchorID   int
	HistoryClearAnchorDate int
	ReadInboxMaxID         int
	ReadOutboxMaxID        int
	UnreadCount            int
	UnreadMentions         int
	UnreadReactions        int
	TTLPeriod              int
	ThemeEmoticon          string
	HasScheduled           bool
	Pinned                 bool
	PinnedOrder            int
	UnreadMark             bool
	ViewForumAsMessages    bool
	PeerSettingsBarHidden  bool
	// TopMessageMentioned/MediaUnread/UnreadProjected are internal owner-view
	// facts for materialized channel dialog snapshots. They are not TL dialog
	// fields; response assembly applies them to the shared top-message payload.
	TopMessageMentioned       bool
	TopMessageMediaUnread     bool
	TopMessageUnreadProjected bool
	// DefaultSendAs is internal owner-view channel dialog metadata. It is kept
	// in the materialized owner snapshot so warming the exact viewer/channel
	// projection cannot erase channels.setDefaultSendAs state.
	DefaultSendAs *Peer
	// ChannelMember is the internal exact access/read projection captured with
	// a materialized channel dialog. It is never a TL dialog field. Keeping it
	// under the same dialog_owner generation lets startup warm permission reads
	// before channel difference without a per-channel PostgreSQL lookup.
	ChannelMember *ChannelMember
	// Pts is the current channel pts of a channel peer; the client uses dialog.pts to initialize its local
	// channel sequence and decide the getChannelDifference starting point. Required for a channel dialog.
	Pts   int
	Draft *DialogDraft
	// NotifySettings is the per-peer notification setting for this chat (nil=unset, falls back to the default when projected).
	// Assembled in bulk by the read path (not a persisted dialog store field); see rpc.withDialogNotifySettings.
	NotifySettings *PeerNotifySettings
}

// DialogDraftWebPage stores a draft link preview without depending on TL input media types.
type DialogDraftWebPage struct {
	URL             string
	ForceLargeMedia bool
	ForceSmallMedia bool
	Optional        bool
}

type SuggestedPostPriceKind string

const (
	SuggestedPostPriceStars SuggestedPostPriceKind = "stars"
	SuggestedPostPriceTON   SuggestedPostPriceKind = "ton"
)

// SuggestedPostPrice is either a decimal Stars amount or a nanotons amount.
type SuggestedPostPrice struct {
	Kind   SuggestedPostPriceKind
	Amount int64
	Nanos  int
}

// SuggestedPost is the domain-only snapshot shared by a monoforum message and its cloud draft.
type SuggestedPost struct {
	Accepted     bool
	Rejected     bool
	Price        *SuggestedPostPrice
	ScheduleDate int
}

// DialogDraft is a cloud draft for one peer/topic, expressed only in domain types.
type DialogDraft struct {
	Peer          Peer
	TopMessageID  int
	Date          int
	NoWebpage     bool
	InvertMedia   bool
	Message       string
	Entities      []MessageEntity
	ReplyTo       *MessageReply
	WebPage       *DialogDraftWebPage
	Effect        int64
	SuggestedPost *SuggestedPost
	RichMessage   *MessageRichMessage
}

// Empty reports whether this draft should clear the cloud draft slot.
func (d DialogDraft) Empty() bool {
	replyOnlyTopic := d.ReplyTo != nil && d.ReplyTo.MessageID == 0 && d.ReplyTo.TopMessageID > 0
	return !d.NoWebpage &&
		!d.InvertMedia &&
		d.Message == "" &&
		len(d.Entities) == 0 &&
		(d.ReplyTo == nil || replyOnlyTopic) &&
		d.WebPage == nil &&
		d.Effect == 0 &&
		d.SuggestedPost == nil &&
		d.RichMessage.IsZero()
}

// DialogArchiveSummary aggregates archive (folder_id=1) state for the main getDialogs list
// to emit a dialogFolder entry on its first page: TDesktop relies on that entry to discover the archive
// (a newly logged-in device has no update to replay, so without it archived chats would be entirely invisible).
type DialogArchiveSummary struct {
	// TopPeer/TopMessage are the newest chat in the archive and its top message (dialogFolder.peer/top_message).
	TopPeer    Peer
	TopMessage int
	// TopDialog retains the owner projection needed to hydrate a channel archive
	// top payload without re-reading channel_members/channel_dialogs. It is an
	// internal read-model field and is never converted into a second TL dialog.
	TopDialog *Dialog
	// UnreadPeersCount is the number of chats in the archive with unread (or manually marked unread) items;
	// UnreadMessagesCount is the archive's total unread message count. Per-peer mute state is not yet wired up,
	// so all are counted in the unmuted bucket.
	UnreadPeersCount    int
	UnreadMessagesCount int
	Pinned              bool
}

// DialogList is a dialogs query result.
type DialogList struct {
	Dialogs         []Dialog
	Messages        []Message
	ChannelMessages []ChannelMessage
	Users           []User
	Channels        []Channel
	Communities     []CommunityView
	State           UpdateState
	Hash            int64
	Count           int
	// When ArchiveSummary is non-nil, a dialogFolder entry is appended to the head of the main list response.
	ArchiveSummary *DialogArchiveSummary
}

// DialogHashCheck reports whether a cached stable dialog list hash is known.
type DialogHashCheck struct {
	Known   bool
	Matched bool
	Hash    int64
	Count   int
}

// DialogFilter is a chat list query condition.
type DialogFilter struct {
	PinnedOnly    bool
	ExcludePinned bool
	HasFolderID   bool
	FolderID      int
	Folder        *DialogFolder
	OffsetDate    int
	OffsetID      int
	HasOffsetPeer bool
	OffsetPeer    Peer
	Limit         int
	Hash          int64
}

// DialogFolderPeer is a peer in a folder/filter rule, keeping access_hash for the RPC layer to write back InputPeer.
type DialogFolderPeer struct {
	Peer       Peer
	AccessHash int64
}

// DialogFolder is a user-defined chat grouping rule. It expresses only business meaning and does not depend on TL-generated types.
type DialogFolder struct {
	ID              int
	Contacts        bool
	NonContacts     bool
	Groups          bool
	Broadcasts      bool
	Bots            bool
	ExcludeMuted    bool
	ExcludeRead     bool
	ExcludeArchived bool
	TitleNoanimate  bool
	Title           string
	TitleEntities   []MessageEntity
	Emoticon        string
	HasEmoticon     bool
	Color           int
	HasColor        bool
	HasMyInvites    bool
	PinnedPeers     []DialogFolderPeer
	IncludePeers    []DialogFolderPeer
	ExcludePeers    []DialogFolderPeer
	IsChatlist      bool
}

// DialogFolderList is the business response of messages.getDialogFilters.
type DialogFolderList struct {
	TagsEnabled bool
	Folders     []DialogFolder
}

// FolderPeerUpdate describes a single archive/unarchive change of folders.editPeerFolders.
type FolderPeerUpdate struct {
	Peer     Peer
	FolderID int
}
