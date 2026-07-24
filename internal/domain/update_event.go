package domain

// UpdateEventType 标识 update 队列事件类型。
type UpdateEventType string

const (
	UpdateEventNewMessage        UpdateEventType = "new_message"
	UpdateEventReadHistoryInbox  UpdateEventType = "read_history_inbox"
	UpdateEventReadHistoryOutbox UpdateEventType = "read_history_outbox"
	// forum 话题级已读：messages.readDiscussion 推进 per-topic 水位后下发。
	UpdateEventReadChannelDiscussionInbox  UpdateEventType = "read_channel_discussion_inbox"
	UpdateEventReadChannelDiscussionOutbox UpdateEventType = "read_channel_discussion_outbox"
	UpdateEventReadMessageContents         UpdateEventType = "read_message_contents"
	UpdateEventEditMessage                 UpdateEventType = "edit_message"
	// UpdateEventBotCallbackQuery 仅用于 Bot API 专用 update_id 队列投影；不写账号
	// pts/difference/outbox。
	UpdateEventBotCallbackQuery UpdateEventType = "bot_callback_query"
	// UpdateEventWebPage 映射 updateWebPage：异步解析完成后把消息里的 pending 链接预览
	// 占位就地替换为已解析卡片。携带账号 pts（非 LacksWirePts），消息快照经 box JOIN 重建，
	// 故 difference/dispatch 与 edit_message 同走通用消息事件路径，仅 tg 投影构造器不同。
	UpdateEventWebPage          UpdateEventType = "web_page"
	UpdateEventMessageReactions UpdateEventType = "message_reactions"
	// UpdateEventMessagePoll 映射 updateMessagePoll（投票/关闭后 poll 状态变化；
	// Message 为该 owner 视角消息，media 在 difference 重放时按 viewer 重新 enrich）。
	// 与 reaction 同款：占账号 pts 但 TL 构造器无 pts，见 LacksWirePts。
	UpdateEventMessagePoll      UpdateEventType = "message_poll"
	UpdateEventContactsReset    UpdateEventType = "contacts_reset"
	UpdateEventDialogPinned     UpdateEventType = "dialog_pinned"
	UpdateEventPinnedDialogs    UpdateEventType = "pinned_dialogs"
	UpdateEventDialogUnreadMark UpdateEventType = "dialog_unread_mark"
	UpdateEventPeerSettings     UpdateEventType = "peer_settings"
	UpdateEventPeerStoryBlocked UpdateEventType = "peer_story_blocked"
	// UpdateEventUserPhone 映射 updateUserPhone。它是账号绝对状态更新，TL
	// 构造器不携 pts；事件仍占账号 pts，以便其它设备在线/离线保持同一水位。
	UpdateEventUserPhone UpdateEventType = "user_phone"
	// UpdateEventUserEmojiStatus carries the exact immutable status snapshot.
	// It consumes account pts even though updateUserEmojiStatus has no pts.
	UpdateEventUserEmojiStatus UpdateEventType = "user_emoji_status"
	// UpdateEventUserProfile is a durable viewer-scoped refresh for another
	// user's absolute profile flags (currently moderation SCAM/FAKE state).
	// The changed user is carried in Peer; hydration attaches the authoritative
	// viewer projection to Users before online dispatch or getDifference.
	UpdateEventUserProfile UpdateEventType = "user_profile"
	// UpdateEventPrivacy carries the immutable account privacy key/rule
	// snapshot committed at this pts. updatePrivacy has no wire pts.
	UpdateEventPrivacy        UpdateEventType = "privacy"
	UpdateEventDeleteMessages UpdateEventType = "delete_messages"
	// UpdateEventPinnedMessages 映射 updatePinnedMessages（私聊置顶/取消
	// 置顶；MessageIDs 是该 owner 自己视角的 box id，Bool 为 pinned）。
	// TL 构造器自带账号 pts/pts_count，不属于 LacksWirePts。
	UpdateEventPinnedMessages           UpdateEventType = "pinned_messages"
	UpdateEventDialogFilter             UpdateEventType = "dialog_filter"
	UpdateEventDialogFilterOrder        UpdateEventType = "dialog_filter_order"
	UpdateEventDialogFilters            UpdateEventType = "dialog_filters"
	UpdateEventFolderPeers              UpdateEventType = "folder_peers"
	UpdateEventChannelAvailable         UpdateEventType = "channel_available_messages"
	UpdateEventChannelViewForum         UpdateEventType = "channel_view_forum_as_messages"
	UpdateEventStory                    UpdateEventType = "story"
	UpdateEventReadStories              UpdateEventType = "read_stories"
	UpdateEventSentStoryReaction        UpdateEventType = "sent_story_reaction"
	UpdateEventNewStoryReaction         UpdateEventType = "new_story_reaction"
	UpdateEventQuickReplies             UpdateEventType = "quick_replies"
	UpdateEventNewQuickReply            UpdateEventType = "new_quick_reply"
	UpdateEventDeleteQuickReply         UpdateEventType = "delete_quick_reply"
	UpdateEventQuickReplyMessage        UpdateEventType = "quick_reply_message"
	UpdateEventDeleteQuickReplyMessages UpdateEventType = "delete_quick_reply_messages"
	// UpdateEventChannelState 表示当前账号与某频道的成员关系发生变化
	// （leave/kick），离线设备经 difference 收到 updateChannel 后重新拉取
	// channel 状态并移除会话。
	UpdateEventChannelState UpdateEventType = "channel_state"
	// UpdateEventDraftMessage 映射 updateDraftMessage（云草稿变更；Peer 为会话，
	// MaxID 复用为 forum top_msg_id）。事件只是"该会话草稿变过"的标记——草稿是
	// 绝对状态而非增量，difference/outbox 重放时按 Peer 重载当前草稿填充 Draft
	// 字段（已删则下发 draftMessageEmpty），不在事件行里固化内容快照。
	UpdateEventDraftMessage UpdateEventType = "draft_message"
	// UpdateEventSavedDialogPinned 映射 updateSavedDialogPinned（收藏夹
	// 子会话置顶翻转；Peer 为子会话分组键，Bool 为 pinned）。
	UpdateEventSavedDialogPinned UpdateEventType = "saved_dialog_pinned"
	// UpdateEventPinnedSavedDialogs 映射 updatePinnedSavedDialogs
	// （收藏夹置顶顺序整表，Peers 为新顺序）。
	UpdateEventPinnedSavedDialogs UpdateEventType = "pinned_saved_dialogs"
	UpdateEventNoop               UpdateEventType = "noop"
)

// UpdateEvent 是账号视角的增量事件，按 user_id + pts 顺序持久化。
type UpdateEvent struct {
	UserID           int64
	Type             UpdateEventType
	Pts              int
	PtsCount         int
	Date             int
	Message          Message
	Story            Story
	Peer             Peer
	Peers            []Peer
	Bool             bool
	Phone            string
	EmojiStatus      UserEmojiStatus
	Privacy          PrivacyRules
	Settings         PeerSettings
	MessageIDs       []int
	MaxID            int
	StillUnreadCount int
	ChannelPts       int
	// TopMsgID 仅 forum per-topic 已读事件（read_channel_discussion_*）使用：承载话题 id
	// （General=1），与 MaxID(=read_max_id) 一起映射 updateReadChannelDiscussionInbox/Outbox。
	TopMsgID int
	Users    []User
	Channels []Channel
	FilterID int
	// FolderID 是 read 类事件发生时该会话所在的物理 folder（0 主列表/1 归档），
	// 填入 updateReadChannelInbox.folder_id。
	FolderID     int
	DialogFilter *DialogFolder
	// Draft 仅 draft_message 事件使用：不持久化，difference/outbox 重放时按
	// Peer(+MaxID=top_msg_id) 重载当前草稿填充；nil 表示草稿已删（下发 empty）。
	Draft             *DialogDraft
	FilterOrder       []int
	FolderPeers       []FolderPeerUpdate
	TagsEnabled       bool
	Reaction          *MessageReaction
	QuickReplies      []QuickReply
	QuickReply        QuickReply
	QuickReplyMessage QuickReplyMessage
	BotCallbackQuery  *BotCallbackQuery
	// BotAPIUpdateID is the HTTP Bot API update_id. It is intentionally separate
	// from MTProto Pts: Bot API ephemeral envelopes never advance account state.
	BotAPIUpdateID   int64
	EphemeralMessage *EphemeralMessage
}

// LacksWirePts 表示该事件占用了账号 pts，但它对应的 TL update 构造器没有
// 账号 pts 字段（reaction、channel 已读、dialog/folder/settings 状态类）。
// 在线投递这类事件必须附带显式 pts 簿记，否则客户端水位与服务端错位，
// 下一条真正带 pts 的更新会被判为空洞。
func (e UpdateEvent) LacksWirePts() bool {
	switch e.Type {
	case UpdateEventMessageReactions,
		UpdateEventMessagePoll,
		UpdateEventDraftMessage,
		UpdateEventChannelState,
		UpdateEventContactsReset,
		UpdateEventDialogPinned,
		UpdateEventPinnedDialogs,
		UpdateEventSavedDialogPinned,
		UpdateEventPinnedSavedDialogs,
		UpdateEventDialogUnreadMark,
		UpdateEventPeerSettings,
		UpdateEventPeerStoryBlocked,
		UpdateEventUserPhone,
		UpdateEventUserEmojiStatus,
		UpdateEventUserProfile,
		UpdateEventPrivacy,
		UpdateEventDialogFilter,
		UpdateEventDialogFilterOrder,
		UpdateEventDialogFilters,
		UpdateEventChannelAvailable,
		UpdateEventChannelViewForum,
		UpdateEventStory,
		UpdateEventReadStories,
		UpdateEventSentStoryReaction,
		UpdateEventNewStoryReaction,
		UpdateEventQuickReplies,
		UpdateEventNewQuickReply,
		UpdateEventDeleteQuickReply,
		UpdateEventQuickReplyMessage,
		UpdateEventDeleteQuickReplyMessages:
		// updateFolderPeers 自带 pts/pts_count，不在此列。
		return true
	case UpdateEventReadChannelDiscussionInbox, UpdateEventReadChannelDiscussionOutbox:
		// forum 话题已读映射 updateReadChannelDiscussionInbox/Outbox，无账号 pts 字段。
		return true
	case UpdateEventReadHistoryInbox, UpdateEventReadHistoryOutbox:
		// channel peer 映射 updateReadChannelInbox/Outbox，无账号 pts 字段；
		// 私聊形态自带 pts/pts_count。
		return e.Peer.Type == PeerTypeChannel
	default:
		return false
	}
}

// UpdateDifference 是 updates.getDifference 的业务层结果。
type UpdateDifference struct {
	State         UpdateState
	Events        []UpdateEvent
	ChannelNudges []ChannelDifferenceNudge
	// Partial 为 true 表示连续事件被 limit 截断、后面还有（映射 updates.differenceSlice，
	// 客户端据 State 继续翻页）；false 表示已到当前连续末尾（updates.difference）。
	Partial bool
}

// ChannelDifferenceNudge is a computed account-level hint that a channel diff is dirty.
type ChannelDifferenceNudge struct {
	ChannelID int64
	Pts       int
	Channel   *ChannelView
}
