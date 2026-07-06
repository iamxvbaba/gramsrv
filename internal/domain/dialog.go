package domain

// PeerType 标识 dialog 所属 peer 类型。
type PeerType string

const (
	PeerTypeUser    PeerType = "user"
	PeerTypeChannel PeerType = "channel"
	// PeerTypeFolder 仅用于 dialog 置顶事件中表达 dialogPeerFolder
	// （archive folder 行本身被置顶/取消置顶），ID 为 folder_id。
	PeerTypeFolder PeerType = "folder"
)

const (
	// DialogMainFolderID 是 TDesktop 主会话列表 folder_id。
	DialogMainFolderID = 0
	// DialogArchiveFolderID 是 Telegram 约定的归档会话 folder_id。
	DialogArchiveFolderID = 1
	// DialogCustomFolderMinID 起才允许用户自定义 filter。
	DialogCustomFolderMinID = 2
	// MaxDialogFolders 限制单用户自定义 filter 数量，避免无界配置拖垮启动同步。
	MaxDialogFolders = 100
	// MaxDialogFolderPeers 限制单 filter 中 include/exclude/pinned peer 数。
	MaxDialogFolderPeers = 100
	// MaxDialogFolderTitleRunes 对齐 Telegram folder title 的短标题语义。
	MaxDialogFolderTitleRunes = 64
	// MaxDialogDraftsPerUser bounds messages.getAllDrafts / clearAllDrafts work.
	MaxDialogDraftsPerUser = 1000
	// MaxPinnedDialogsMainFolder/MaxPinnedDialogsArchiveFolder 对齐 TDesktop
	// 内置默认限额（appConfig dialogs_pinned_limit_default=5 /
	// dialogs_folder_pinned_limit_default=100）；超限返回
	// PINNED_DIALOGS_TOO_MUCH，服务端兜底防止客户端截断后两端漂移。
	MaxPinnedDialogsMainFolder    = 5
	MaxPinnedDialogsArchiveFolder = 100
	// premium 档对齐 appConfig dialogs_pinned_limit_premium=10 /
	// dialogs_folder_pinned_limit_premium=200。服务端档位必须 ≥ 客户端宣告
	// 档位，否则 premium 用户 pin 第 6 个会话会被拒、UI 回滚。
	MaxPinnedDialogsMainFolderPremium    = 10
	MaxPinnedDialogsArchiveFolderPremium = 200
)

// PinnedDialogsLimit 返回 folder 维度的置顶上限档位（premium 双档）。
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

// Peer 是业务层 peer 值对象，不依赖 TL 类型。
type Peer struct {
	Type PeerType
	ID   int64
}

// IsSelfUser reports whether this peer is the current user's own user peer.
func (p Peer) IsSelfUser(userID int64) bool {
	return userID != 0 && p.Type == PeerTypeUser && p.ID == userID
}

// Dialog 是账号的一条会话摘要。
type Dialog struct {
	Peer                  Peer
	ChannelLeft           bool
	FolderID              int
	TopMessage            int
	TopMessageDate        int
	ReadInboxMaxID        int
	ReadOutboxMaxID       int
	UnreadCount           int
	UnreadMentions        int
	UnreadReactions       int
	TTLPeriod             int
	ThemeEmoticon         string
	HasScheduled          bool
	Pinned                bool
	PinnedOrder           int
	UnreadMark            bool
	ViewForumAsMessages   bool
	PeerSettingsBarHidden bool
	// Pts 是 channel peer 当前 channel pts；客户端用 dialog.pts 初始化本地
	// channel 序列并决定 getChannelDifference 起点，channel dialog 必填。
	Pts   int
	Draft *DialogDraft
	// NotifySettings 是该会话的 per-peer 通知设置（nil=未配置，投影时回落默认）。
	// 由读路径批量装配（非 dialog store 持久字段），见 rpc.withDialogNotifySettings。
	NotifySettings *PeerNotifySettings
}

// DialogDraftWebPage stores a draft link preview without depending on TL input media types.
type DialogDraftWebPage struct {
	URL             string
	ForceLargeMedia bool
	ForceSmallMedia bool
	Optional        bool
}

// DialogDraft is a cloud draft for one peer/topic, expressed only in domain types.
type DialogDraft struct {
	Peer         Peer
	TopMessageID int
	Date         int
	NoWebpage    bool
	InvertMedia  bool
	Message      string
	Entities     []MessageEntity
	ReplyTo      *MessageReply
	WebPage      *DialogDraftWebPage
	Effect       int64
	RichMessage  *MessageRichMessage
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
		d.RichMessage.IsZero()
}

// DialogArchiveSummary 聚合归档（folder_id=1）状态，供主列表 getDialogs
// 第一页输出 dialogFolder 条目：TDesktop 依赖该条目发现 archive 的存在
// （新登录设备没有任何 update 可重放，缺少它归档会话将彻底不可见）。
type DialogArchiveSummary struct {
	// TopPeer/TopMessage 是归档内最新会话及其 top 消息（dialogFolder.peer/top_message）。
	TopPeer    Peer
	TopMessage int
	// UnreadPeersCount 是归档内有未读（或手动标记未读）的会话数；
	// UnreadMessagesCount 是归档未读消息总数。当前未接 per-peer mute
	// 状态，全部计入 unmuted 桶。
	UnreadPeersCount    int
	UnreadMessagesCount int
	Pinned              bool
}

// DialogList 是 dialogs 查询结果。
type DialogList struct {
	Dialogs         []Dialog
	Messages        []Message
	ChannelMessages []ChannelMessage
	Users           []User
	Channels        []Channel
	State           UpdateState
	Hash            int64
	Count           int
	// ArchiveSummary 非 nil 时，主列表响应头部追加 dialogFolder 条目。
	ArchiveSummary *DialogArchiveSummary
}

// DialogHashCheck reports whether a cached stable dialog list hash is known.
type DialogHashCheck struct {
	Known   bool
	Matched bool
	Hash    int64
	Count   int
}

// DialogFilter 是会话列表查询条件。
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

// DialogFolderPeer 是 folder/filter 规则中的 peer，保留 access_hash 供 RPC 层回写 InputPeer。
type DialogFolderPeer struct {
	Peer       Peer
	AccessHash int64
}

// DialogFolder 是用户自定义会话分组规则。它只表达业务含义，不依赖 TL 生成类型。
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

// DialogFolderList 是 messages.getDialogFilters 的业务响应。
type DialogFolderList struct {
	TagsEnabled bool
	Folders     []DialogFolder
}

// FolderPeerUpdate 描述 folders.editPeerFolders 的单个归档/还原变更。
type FolderPeerUpdate struct {
	Peer     Peer
	FolderID int
}
