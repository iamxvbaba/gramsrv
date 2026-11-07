package domain

// UpdateEventType identifies the update queue event type.
type UpdateEventType string

const (
	UpdateEventNewMessage        UpdateEventType = "new_message"
	UpdateEventReadHistoryInbox  UpdateEventType = "read_history_inbox"
	UpdateEventReadHistoryOutbox UpdateEventType = "read_history_outbox"
	// Forum topic-level read: sent after messages.readDiscussion advances the per-topic watermark.
	UpdateEventReadChannelDiscussionInbox  UpdateEventType = "read_channel_discussion_inbox"
	UpdateEventReadChannelDiscussionOutbox UpdateEventType = "read_channel_discussion_outbox"
	UpdateEventReadMessageContents         UpdateEventType = "read_message_contents"
	UpdateEventEditMessage                 UpdateEventType = "edit_message"
	// UpdateEventBotCallbackQuery is used only for the Bot API-specific update_id queue projection; it does not write account
	// pts/difference/outbox.
	UpdateEventBotCallbackQuery UpdateEventType = "bot_callback_query"
	// UpdateEventBotPreCheckoutQuery, like callback, only serves the Bot API queue projection;
	// it does not write account pts/difference/outbox. The payer waits in the payment RPC for the bot's answer, see
	// precheckout_registry.go.
	UpdateEventBotPreCheckoutQuery UpdateEventType = "bot_pre_checkout_query"
	// UpdateEventWebPage maps updateWebPage: after async parsing completes it replaces the pending link preview
	// placeholder in the message in place with the resolved card. It carries account pts (not LacksWirePts), and the message snapshot is rebuilt via a box JOIN,
	// so difference/dispatch follows the generic message event path like edit_message, differing only in the tg projection constructor.
	UpdateEventWebPage UpdateEventType = "web_page"
	// UpdateEventMessagePoll maps updateMessagePoll (poll state changes after a vote/close;
	// Message is the message from that owner's perspective, and the media is re-enriched per viewer when difference replays it).
	// The current history implementation still treats poll as a to-be-reviewed LacksWirePts event.
	UpdateEventMessagePoll      UpdateEventType = "message_poll"
	UpdateEventContactsReset    UpdateEventType = "contacts_reset"
	UpdateEventDialogPinned     UpdateEventType = "dialog_pinned"
	UpdateEventPinnedDialogs    UpdateEventType = "pinned_dialogs"
	UpdateEventDialogUnreadMark UpdateEventType = "dialog_unread_mark"
	UpdateEventPeerSettings     UpdateEventType = "peer_settings"
	UpdateEventPeerStoryBlocked UpdateEventType = "peer_story_blocked"
	// UpdateEventUserPhone is used only to read updateUserPhone events persisted by historical versions.
	// The TL constructor carries no pts, and the current write path must no longer produce this event.
	UpdateEventUserPhone UpdateEventType = "user_phone"
	// UpdateEventUserEmojiStatus carries the exact immutable status snapshot.
	// It consumes account pts even though updateUserEmojiStatus has no pts.
	UpdateEventUserEmojiStatus UpdateEventType = "user_emoji_status"
	UpdateEventDeleteMessages  UpdateEventType = "delete_messages"
	// UpdateEventPinnedMessages maps updatePinnedMessages (private-chat pin/unpin;
	// MessageIDs are the box ids from that owner's own perspective, and Bool is pinned).
	// The TL constructor carries its own account pts/pts_count and is not LacksWirePts.
	UpdateEventPinnedMessages           UpdateEventType = "pinned_messages"
	UpdateEventDialogFilter             UpdateEventType = "dialog_filter"
	UpdateEventDialogFilterOrder        UpdateEventType = "dialog_filter_order"
	UpdateEventDialogFilters            UpdateEventType = "dialog_filters"
	UpdateEventFolderPeers              UpdateEventType = "folder_peers"
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
	// UpdateEventChannelState means the current account's membership in a channel changed
	// (leave/kick); an offline device receiving updateChannel via difference re-fetches
	// the channel state and removes the chat.
	UpdateEventChannelState UpdateEventType = "channel_state"
	// UpdateEventDraftMessage maps updateDraftMessage (cloud draft change; Peer is the chat and
	// MaxID is reused as the forum top_msg_id). The event is only a marker that "this chat's draft changed" — a draft is
	// absolute state, not a delta, so a difference/outbox replay reloads the current draft by Peer to fill the Draft
	// field (or sends draftMessageEmpty if deleted), not freezing a content snapshot in the event row.
	UpdateEventDraftMessage UpdateEventType = "draft_message"
	// UpdateEventSavedDialogPinned maps updateSavedDialogPinned (a saved-messages
	// sub-chat pin flip; Peer is the sub-chat grouping key and Bool is pinned).
	UpdateEventSavedDialogPinned UpdateEventType = "saved_dialog_pinned"
	// UpdateEventPinnedSavedDialogs maps updatePinnedSavedDialogs
	// (the full saved-messages pin order table; Peers is the new order).
	UpdateEventPinnedSavedDialogs UpdateEventType = "pinned_saved_dialogs"
	UpdateEventNoop               UpdateEventType = "noop"
)

// UpdateEvent is an account-perspective incremental event, persisted in user_id + pts order.
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
	Settings         PeerSettings
	MessageIDs       []int
	MaxID            int
	StillUnreadCount int
	ChannelPts       int
	// TopMsgID is used only by the forum per-topic read events (read_channel_discussion_*): it carries the topic id
	// (General=1), mapping updateReadChannelDiscussionInbox/Outbox together with MaxID(=read_max_id).
	TopMsgID int
	Users    []User
	Channels []Channel
	FilterID int
	// FolderID is the physical folder the chat was in when the read-class event occurred (0 main list/1 archive),
	// filled into updateReadChannelInbox.folder_id.
	FolderID     int
	DialogFilter *DialogFolder
	// Draft is used only by draft_message events: not persisted, it is filled by reloading the current draft by
	// Peer (+MaxID=top_msg_id) during a difference/outbox replay; nil means the draft was deleted (sends empty).
	Draft             *DialogDraft
	FilterOrder       []int
	FolderPeers       []FolderPeerUpdate
	TagsEnabled       bool
	Reaction          *MessageReaction
	QuickReplies      []QuickReply
	QuickReply        QuickReply
	QuickReplyMessage QuickReplyMessage
	BotCallbackQuery  *BotCallbackQuery
	// BotPreCheckout carries the pre-checkout query itself; this event has no message and Peer/MessageID are zero.
	BotPreCheckout *BotPreCheckoutQuery
	// BotAPIUpdateID is the HTTP Bot API update_id. It is intentionally separate
	// from MTProto Pts: Bot API ephemeral envelopes never advance account state.
	BotAPIUpdateID   int64
	EphemeralMessage *EphemeralMessage
}

// LacksWirePts means the event consumed account pts, but its corresponding TL update constructor has no
// account pts field (reaction, channel read, dialog/folder/settings state classes).
// Online delivery of such events must carry explicit pts bookkeeping, or the client watermark drifts from the server's
// and the next genuinely pts-bearing update is judged a gap.
func (e UpdateEvent) LacksWirePts() bool {
	switch e.Type {
	case UpdateEventMessagePoll,
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
		UpdateEventDialogFilter,
		UpdateEventDialogFilterOrder,
		UpdateEventDialogFilters,
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
		// updateFolderPeers carries its own pts/pts_count and is not in this list.
		return true
	case UpdateEventReadChannelDiscussionInbox, UpdateEventReadChannelDiscussionOutbox:
		// Forum topic read maps updateReadChannelDiscussionInbox/Outbox and has no account pts field.
		return true
	case UpdateEventReadHistoryInbox, UpdateEventReadHistoryOutbox:
		// A channel peer maps updateReadChannelInbox/Outbox and has no account pts field;
		// the private-chat form carries its own pts/pts_count.
		return e.Peer.Type == PeerTypeChannel
	default:
		return false
	}
}

// UpdateDifference is the business-layer result of updates.getDifference.
type UpdateDifference struct {
	State         UpdateState
	Events        []UpdateEvent
	ChannelNudges []ChannelDifferenceNudge
	// Partial being true means the continuous events were truncated by the limit and more remain (maps updates.differenceSlice,
	// and the client keeps paging from State); false means the current continuous end was reached (updates.difference).
	Partial bool
}

// ChannelDifferenceNudge is a computed account-level hint that a channel diff is dirty.
type ChannelDifferenceNudge struct {
	ChannelID           int64
	Pts                 int
	ChannelUpdatesDirty bool
	AvailableMinID      int
	HistoryClearDate    int
	Channel             *ChannelView
}
