package domain

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// InitialChannelPts is the empty message-box state used by official clients
	// before the first real channel event. The first event therefore has PTS 2.
	InitialChannelPts = 1
	// FirstChannelEventPts is the PTS after the first single-count channel event.
	FirstChannelEventPts = InitialChannelPts + 1
	// MaxChannelDifferenceLimit limits a single updates.getChannelDifference page.
	MaxChannelDifferenceLimit = 100
	// MaxChannelDifferenceTooLongMessages limits the latest message snapshot returned by channelDifferenceTooLong.
	MaxChannelDifferenceTooLongMessages = 100
	// MaxChannelUpdateRetentionBatch bounds one channel durable-log pruning transaction.
	// Retention advances the recoverable floor only through rows actually deleted in that transaction.
	MaxChannelUpdateRetentionBatch = 10000
	// MaxChannelParticipantsLimit limits a single participants page.
	MaxChannelParticipantsLimit = 200
	// MaxChannelParticipantsOffset bounds channels.getParticipants deep OFFSET work.
	MaxChannelParticipantsOffset = 10000
	// MaxChannelParticipantsQueryLength bounds member search strings before LIKE scans.
	MaxChannelParticipantsQueryLength = 128
	// MaxChannelInviteUsers limits a single inviteToChannel/createChat member batch.
	MaxChannelInviteUsers = 200
	// MaxChannelRealtimeFanout caps best-effort realtime channel pushes until a presence/subscription index exists.
	MaxChannelRealtimeFanout = 2000
	// MaxSynchronousChannelDialogFanout bounds per-member channel_dialogs writes in the send transaction.
	MaxSynchronousChannelDialogFanout = 1000
	// MaxDialogUnreadCount clamps the upper bound of a dialog's unread message COUNT (design: fan-out epic Phase 2 / P1-v).
	// Broadcast/large groups use a dynamic COUNT(*); a huge backlog would both send astronomical badge numbers and, under a nudge
	// storm, make every nudged member's getChannelDifference/getPeerDialogs trigger an O(backlog) scan that blows up PG. The store always clamps the unread
	// COUNT to this bound with a LIMIT subquery (PG)/early break (memory) — bounding both the scan work and the sent value.
	// It only affects the badge display number, not the read watermark/pts truth or the hasUnread(EXISTS) decision.
	// The value is 99 (the "99+" badge display cap convention): the bound is the scan bound, and the lower it is the shallower each unread COUNT scan —
	// compared with the old value 1000, a member with a backlog >99 has each unread COUNT (large-group read path + fan-out write + forum topics)
	// scan about an order of magnitude fewer rows. The value can be adjusted to client badge display habits.
	MaxDialogUnreadCount = 99
	// MaxChannelTypingFanout caps transient typing fanout.
	MaxChannelTypingFanout = MaxChannelRealtimeFanout
	// MaxChannelAdminRankLength limits custom admin rank text.
	MaxChannelAdminRankLength = 32
	// MaxChannelInviteTitleLength limits invite link admin-only labels.
	MaxChannelInviteTitleLength = 32
	// MaxChannelInviteListLimit limits invite management pages.
	MaxChannelInviteListLimit = 100
	// MaxChannelHideJoinRequests limits one hideAllChatJoinRequests batch.
	MaxChannelHideJoinRequests = 1000
	// MaxChannelPendingJoinRecentRequesters limits recent_requesters carried in pending join request updates.
	MaxChannelPendingJoinRecentRequesters = 5
	// MaxAdminedPublicChannels limits channels.getAdminedPublicChannels payload size.
	MaxAdminedPublicChannels = 200
	// MaxSendAsChannels bounds the broadcast channels offered as channels.getSendAs candidates
	// (the user's own channels they can post groups as).
	MaxSendAsChannels = 100
	// MaxChannelAdminLogLimit limits one channels.getAdminLog page.
	MaxChannelAdminLogLimit = 100
	// MaxChannelAdminLogAdmins limits admin filter fan-in.
	MaxChannelAdminLogAdmins = 100
	// MaxChannelAdminLogQueryLength limits free-text admin log search.
	MaxChannelAdminLogQueryLength = 128
	// MaxChannelHistoryQueryLength limits channel history/search query strings before LIKE scans.
	MaxChannelHistoryQueryLength = 256
	// MaxChannelSearchPostsLimit limits one global public posts search page.
	MaxChannelSearchPostsLimit = 50
	// MaxChannelGlobalSearchLimit limits one messages.searchGlobal channel page.
	MaxChannelGlobalSearchLimit = 50
	// MaxPublicChannelSearchLimit limits one contacts.search public peer lookup page.
	MaxPublicChannelSearchLimit = 50
	// MaxPublicChannelSearchQueryLength bounds public channel peer search strings.
	MaxPublicChannelSearchQueryLength = 256
	// MaxChannelReactionTypes bounds how many distinct reaction types a chat may
	// allow via chatReactionsSome. DrKLO sends "enable all standard reactions" on
	// a broadcast channel as an explicit chatReactionsSome list (megagroups use
	// chatReactionsAll instead), so this MUST stay >= the advertised available
	// reactions catalog (messages.getAvailableReactions, currently ~74 entries) or
	// that legitimate "select all" payload is wrongly rejected with LIMIT_INVALID.
	// 100 leaves headroom for catalog growth while still bounding abusive payloads.
	MaxChannelReactionTypes = 100
	// MaxChannelReactionsLimit bounds the per-message distinct-reaction cap carried
	// by messages.setChatAvailableReactions.reactions_limit. Clients constrain it to
	// appConfig reactions_uniq_max (11); this wider server bound only rejects abuse
	// and must never be smaller than the reaction type count to keep the cap sane.
	MaxChannelReactionsLimit = 100
	// MaxChannelReactionEmoticonLength limits one emoji reaction string.
	MaxChannelReactionEmoticonLength = 32
	// MaxChannelMessageReactionsPerUser bounds the accepted sendReaction vector before trimming.
	MaxChannelMessageReactionsPerUser = 16
	// MaxMessageReactionsPerUser is the effective per-user cap on one message, matching the
	// advertised appConfig reactions_user_max_default.
	// Longer vectors keep the newest entries: official clients are told to drop older
	// reactions, so the tail of the vector wins.
	MaxMessageReactionsPerUser = 1
	// MaxMessageReactionsPerUserPremium is the Premium tier per-user limit, aligned with appConfig
	// reactions_user_max_premium (official value 3). The client picks a tier by the self premium flag,
	// and the server tier must be ≥ the client-declared tier, or a Premium user's multiple reactions would be
	// silently trimmed and the two sides' views would diverge.
	MaxMessageReactionsPerUserPremium = 3
	// DefaultMessageReactionsUniqMax caps distinct reaction emojis on one message, matching the
	// advertised appConfig reactions_uniq_max. ChannelReactionPolicy.Limit overrides it per chat.
	DefaultMessageReactionsUniqMax = 11
	// MaxChannelMessageReactionRecent limits recent reactors embedded in messageReactions.
	MaxChannelMessageReactionRecent = 3
	// MaxChannelMessageReactionListLimit limits one messages.getMessageReactionsList page.
	MaxChannelMessageReactionListLimit = 100
	// MaxRecentMessageReactions limits one messages.getRecentReactions page.
	MaxRecentMessageReactions = 100
	// MaxTopMessageReactions limits one messages.getTopReactions page.
	MaxTopMessageReactions = 100
	// MaxSavedReactionTags limits one messages.getSavedReactionTags page.
	MaxSavedReactionTags = 100
	// MaxChannelReadParticipants limits per-message read receipt fan-in.
	MaxChannelReadParticipants = 50
	// ChannelReadMarkExpirePeriod matches TDesktop's default chat_read_mark_expire_period.
	ChannelReadMarkExpirePeriod = 7 * 24 * 60 * 60
	// MaxChannelReadOutboxFanout caps senders notified by one channel readHistory.
	MaxChannelReadOutboxFanout = 128
	// MaxChannelReadOutboxScanMessages bounds the read delta scanned for sender receipts.
	MaxChannelReadOutboxScanMessages = 1000
	// MaxCommonChannelsLimit limits one messages.getCommonChats page.
	MaxCommonChannelsLimit = 100
	// MaxLeftChannelsLimit limits one channels.getLeftChannels page.
	MaxLeftChannelsLimit = 100
	// MaxLeftChannelsOffset bounds the legacy count-offset export API.
	MaxLeftChannelsOffset = 10000
	// MaxInactiveChannelsLimit limits one channels.getInactiveChannels payload.
	MaxInactiveChannelsLimit = 100
	// DefaultChannelRecommendationsLimit matches Telegram Desktop's default similar channel preview cap.
	DefaultChannelRecommendationsLimit = 10
	// MaxChannelRecommendationsLimit bounds one channels.getChannelRecommendations payload.
	MaxChannelRecommendationsLimit = 100
	// MaxDiscussionGroupsLimit limits the channels.getGroupsForDiscussion payload.
	MaxDiscussionGroupsLimit = 200
	// MaxChannelRepliesLimit limits one messages.getReplies page.
	MaxChannelRepliesLimit = 100
	// MaxChannelUnreadMentionsLimit limits one messages.getUnreadMentions page.
	MaxChannelUnreadMentionsLimit = 100
	// MaxChannelUnreadReactionsLimit limits one messages.getUnreadReactions page.
	MaxChannelUnreadReactionsLimit = 100
	// MaxChannelForumTopicsLimit limits one messages.getForumTopics page.
	MaxChannelForumTopicsLimit = 100
	// MaxChannelForumTopicIDs limits one messages.getForumTopicsByID vector.
	MaxChannelForumTopicIDs = 100
	// MaxChannelForumTopicTitleLength bounds topic titles before persistence.
	MaxChannelForumTopicTitleLength = 128
	// DefaultForumTopicIconColor is Telegram Desktop's General-topic fallback blue.
	DefaultForumTopicIconColor = 0x6FB9F0
	// ForumGeneralTopicID is the fixed id of the General topic (Telegram protocol; the client uses msg_id=1 for readDiscussion General).
	// A message within General has reply_to_top_id=1, and a history message from before the forum was enabled has reply_to_top_id=0; both belong to General.
	ForumGeneralTopicID = 1
	// MaxChannelReadMentionsBatch limits one messages.readMentions clearing batch.
	MaxChannelReadMentionsBatch = 1000
	// MaxChannelReadReactionsBatch limits one messages.readReactions clearing batch.
	MaxChannelReadReactionsBatch = 1000
	// MaxDeleteParticipantReactionsBatch limits one moderation clear batch.
	MaxDeleteParticipantReactionsBatch = 1000
	// MaxChannelMentionRecipients limits mention state writes for one channel message.
	MaxChannelMentionRecipients = 100
)

// ValidChannelSlowModeSeconds reports whether seconds is accepted by Telegram clients.
func ValidChannelSlowModeSeconds(seconds int) bool {
	switch seconds {
	case 0, 10, 30, 60, 300, 900, 3600:
		return true
	default:
		return false
	}
}

// ChannelMemberRole describes a member's role in a channel or megagroup.
type ChannelMemberRole string

const (
	ChannelRoleCreator ChannelMemberRole = "creator"
	ChannelRoleAdmin   ChannelMemberRole = "admin"
	ChannelRoleMember  ChannelMemberRole = "member"
)

// ChannelMemberStatus describes current membership state.
type ChannelMemberStatus string

const (
	ChannelMemberActive ChannelMemberStatus = "active"
	ChannelMemberLeft   ChannelMemberStatus = "left"
	ChannelMemberKicked ChannelMemberStatus = "kicked"
	ChannelMemberBanned ChannelMemberStatus = "banned"
)

// ChannelAdminRights is a domain-only representation of Telegram admin rights.
type ChannelAdminRights struct {
	ChangeInfo            bool
	PostMessages          bool
	EditMessages          bool
	DeleteMessages        bool
	PostStories           bool
	EditStories           bool
	DeleteStories         bool
	BanUsers              bool
	InviteUsers           bool
	PinMessages           bool
	AddAdmins             bool
	ManageCall            bool
	ManageChat            bool
	ManageTopics          bool
	Anonymous             bool
	ManageRanks           bool
	ManageLinkedPeers     bool
	ManageWelcomeMessages bool
	// ManageDirectMessages corresponds to TL ChatAdminRights.manage_direct_messages (flags.17). The parent broadcast channel's
	// admins are thereby granted the MonoforumAdmin identity for the monoforum (channel Direct Messages) container by the client; the creator goes through the amCreator bypass.
	ManageDirectMessages bool
}

// CreatorChannelAdminRights returns the full rights set clients expect on creator projections.
func CreatorChannelAdminRights() ChannelAdminRights {
	return ChannelAdminRights{
		ChangeInfo:            true,
		PostMessages:          true,
		EditMessages:          true,
		DeleteMessages:        true,
		PostStories:           true,
		EditStories:           true,
		DeleteStories:         true,
		BanUsers:              true,
		InviteUsers:           true,
		PinMessages:           true,
		AddAdmins:             true,
		ManageCall:            true,
		ManageChat:            true,
		ManageTopics:          true,
		ManageRanks:           true,
		ManageLinkedPeers:     true,
		ManageWelcomeMessages: true,
	}
}

// CreatorProjectionAdminRights returns the full rights clients expect for an
// owner while preserving creator-scoped mutable flags.
func CreatorProjectionAdminRights(rights ChannelAdminRights) ChannelAdminRights {
	creatorRights := CreatorChannelAdminRights()
	creatorRights.Anonymous = rights.Anonymous
	creatorRights.ManageDirectMessages = rights.ManageDirectMessages
	return creatorRights
}

// NormalizeFullMegagroupAdminRights fills implicit full-admin bits for megagroups.
func NormalizeFullMegagroupAdminRights(ch Channel, rights ChannelAdminRights) ChannelAdminRights {
	if ch.Megagroup && !ch.Broadcast &&
		rights.ChangeInfo &&
		rights.DeleteMessages &&
		rights.BanUsers &&
		rights.InviteUsers &&
		rights.PinMessages &&
		rights.AddAdmins &&
		rights.ManageCall {
		rights.ManageRanks = true
	}
	return rights
}

// ChannelBannedRights is a domain-only representation of Telegram banned rights.
type ChannelBannedRights struct {
	ViewMessages    bool
	SendMessages    bool
	SendMedia       bool
	SendStickers    bool
	SendGifs        bool
	SendGames       bool
	SendInline      bool
	EmbedLinks      bool
	SendPolls       bool
	ChangeInfo      bool
	InviteUsers     bool
	PinMessages     bool
	ManageTopics    bool
	SendPhotos      bool
	SendVideos      bool
	SendRoundvideos bool
	SendAudios      bool
	SendVoices      bool
	SendDocs        bool
	SendPlain       bool
	EditRank        bool
	SendReactions   bool
	// ManageLinkedPeers is the Layer 228 default restriction used by Communities:
	// true means only admins may add peers; false lets members submit requests.
	ManageLinkedPeers bool
	UntilDate         int
}

// ChannelReactionPolicyType describes which reactions are allowed in a channel.
type ChannelReactionPolicyType string

const (
	ChannelReactionPolicyDefault ChannelReactionPolicyType = ""
	ChannelReactionPolicyNone    ChannelReactionPolicyType = "none"
	ChannelReactionPolicyAll     ChannelReactionPolicyType = "all"
	ChannelReactionPolicySome    ChannelReactionPolicyType = "some"
)

// ChannelReactionPolicy is a domain-only representation of chatReactions*.
type ChannelReactionPolicy struct {
	Type           ChannelReactionPolicyType
	AllowCustom    bool
	Emoticons      []string
	CustomEmojiIDs []int64
	Limit          int
	PaidEnabled    bool
}

// AllowsReaction reports whether the policy accepts one message reaction.
// The zero policy (Default) behaves like chatReactionsAll without allow_custom:
// ordinary emoji reactions are accepted, custom emoji reactions require an
// explicit chatReactionsSome entry or chatReactionsAll.allow_custom.
func (p ChannelReactionPolicy) AllowsReaction(reaction MessageReaction) bool {
	if !reaction.Valid() {
		return false
	}
	switch p.Type {
	case ChannelReactionPolicyNone:
		return false
	case ChannelReactionPolicySome:
		switch reaction.Type {
		case MessageReactionEmoji:
			for _, emoticon := range p.Emoticons {
				if strings.TrimSpace(emoticon) == reaction.Value() {
					return true
				}
			}
		case MessageReactionCustomEmoji:
			for _, documentID := range p.CustomEmojiIDs {
				if documentID == reaction.DocumentID {
					return true
				}
			}
		}
		return false
	case ChannelReactionPolicyAll:
		if reaction.Type == MessageReactionCustomEmoji {
			return p.AllowCustom
		}
		return reaction.Type == MessageReactionEmoji
	default:
		return reaction.Type == MessageReactionEmoji
	}
}

// UniqueReactionsLimit returns the per-message distinct emoji cap for the chat:
// channelFull.reactions_limit when set, otherwise the appConfig reactions_uniq_max default.
func (p ChannelReactionPolicy) UniqueReactionsLimit() int {
	if p.Limit > 0 {
		return p.Limit
	}
	return DefaultMessageReactionsUniqMax
}

// MessageReactionsUserMax returns the viewer's per-user reaction limit tier.
func MessageReactionsUserMax(premium bool) int {
	if premium {
		return MaxMessageReactionsPerUserPremium
	}
	return MaxMessageReactionsPerUser
}

// NormalizeReactionsPerUserMax constrains the tier carried by a request to a legal range:
// <=0 (unfilled by an old caller) falls back to the default tier, and the upper bound is capped at the Premium tier.
func NormalizeReactionsPerUserMax(perUserMax int) int {
	if perUserMax <= 0 {
		return MaxMessageReactionsPerUser
	}
	if perUserMax > MaxMessageReactionsPerUserPremium {
		return MaxMessageReactionsPerUserPremium
	}
	return perUserMax
}

// TrimMessageReactionsToUserMax enforces the per-user reaction cap by keeping the newest
// entries (clients append new reactions at the end of the vector and drop older ones).
// perUserMax is constrained by NormalizeReactionsPerUserMax; a Premium viewer passes the Premium tier.
func TrimMessageReactionsToUserMax(reactions []MessageReaction, perUserMax int) []MessageReaction {
	perUserMax = NormalizeReactionsPerUserMax(perUserMax)
	if len(reactions) <= perUserMax {
		return reactions
	}
	return reactions[len(reactions)-perUserMax:]
}

// ChannelPeerColor is a domain-only representation of PeerColor.
type ChannelPeerColor struct {
	HasColor          bool
	Color             int
	BackgroundEmojiID int64
}

// Empty reports whether the peer has no explicit color state.
func (c ChannelPeerColor) Empty() bool {
	return !c.HasColor && c.BackgroundEmojiID == 0
}

// ChannelEmojiStatus is a domain-only representation of a regular EmojiStatus.
type ChannelEmojiStatus struct {
	DocumentID int64
	Until      int
}

// Empty reports whether no emoji status is set.
func (s ChannelEmojiStatus) Empty() bool {
	return s.DocumentID == 0
}

// Channel is a Telegram channel/supergroup entity.
type Channel struct {
	ID                       int64
	AccessHash               int64
	CreatorUserID            int64
	Title                    string
	About                    string
	Username                 string
	Verified                 bool
	Scam                     bool
	Fake                     bool
	Gigagroup                bool
	Broadcast                bool
	Megagroup                bool
	Forum                    bool
	ForumTabs                bool
	Autotranslation          bool
	RestrictedSponsored      bool
	BroadcastMessagesAllowed bool
	SendPaidMessagesStars    int64
	NoForwards               bool
	JoinToSend               bool
	JoinRequest              bool
	Signatures               bool
	SignatureProfiles        bool
	PreHistoryHidden         bool
	ParticipantsHidden       bool
	AntiSpam                 bool
	// HasLink means the channel/group has an unrevoked exported invite link. The Android send-as entry uses
	// megagroup && (public || has_geo || has_link) to decide whether to fetch the candidate list.
	HasLink      bool
	LinkedChatID int64
	// LinkedCommunityID is the unique Community containing this group/channel.
	// A channel can belong to at most one Community and Communities themselves
	// are stored in a separate aggregate, never in channels.
	LinkedCommunityID int64
	// Monoforum marks this channel as the monoforum virtual channel of "channel Direct Messages".
	// LinkedMonoforumID: the parent channel points to its monoforum, and the monoforum points back to the parent channel (bidirectional).
	Monoforum           bool
	LinkedMonoforumID   int64
	SlowmodeSeconds     int
	BoostsUnrestrict    int
	DefaultBannedRights ChannelBannedRights
	ReactionPolicy      ChannelReactionPolicy
	Color               ChannelPeerColor
	ProfileColor        ChannelPeerColor
	EmojiStatus         ChannelEmojiStatus
	Wallpaper           *Wallpaper
	ParticipantsCount   int
	AdminsCount         int
	KickedCount         int
	BannedCount         int
	TopMessageID        int
	PinnedMessageID     int
	Pts                 int
	TTLPeriod           int
	Date                int
	Deleted             bool
	// Active group-call association (the data source for the channel.call_active/call_not_empty flag and channelFull.call;
	// the client uses it to rebuild the banner when fetching dialogs/getFullChannel).
	ActiveCallID         int64
	ActiveCallAccessHash int64
	ActiveCallNotEmpty   bool
	// Current avatar (denormalized in the channels table). PhotoID==0 means no avatar.
	PhotoID       int64
	PhotoDCID     int
	PhotoStripped []byte
}

// MembersListAdminOnly means the channel's member/subscriber list is visible only to admins. A broadcast channel (non-
// megagroup) subscriber list is in official semantics always admin-only — an ordinary subscriber cannot see the Subscribers/
// Administrators/Channel Settings rows, nor enumerate subscribers; moreover when members are hidden
// (ParticipantsHidden) they are equally invisible to non-admins. A supergroup (megagroup) has members visible by default.
// This decision looks only at the channel itself; allowing access additionally requires checking whether the viewer is an admin.
func (c Channel) MembersListAdminOnly() bool {
	return c.ParticipantsHidden || (c.Broadcast && !c.Megagroup)
}

// ChannelMember is one user's channel membership and read state.
type ChannelMember struct {
	ChannelID       int64
	UserID          int64
	InviterUserID   int64
	Role            ChannelMemberRole
	Status          ChannelMemberStatus
	JoinedAt        int
	LeftAt          int
	AdminRights     ChannelAdminRights
	BannedRights    ChannelBannedRights
	Rank            string
	AvailableMinID  int
	AvailableMinPts int
	// HistoryClearAnchorID/Date identify an owner-local channels.deleteHistory
	// boundary. They are deliberately separate from AvailableMinID: joining a
	// hidden-prehistory channel also advances AvailableMinID but must not
	// manufacture a "History cleared" service message.
	HistoryClearAnchorID   int
	HistoryClearAnchorDate int
	ReadInboxMaxID         int
	ReadInboxDate          int
	ReadOutboxMaxID        int
	UnreadMark             bool
	SlowmodeLastSendDate   int
	// Guest is a computed, non-persisted view used for subscribers accessing a
	// private linked discussion group without joining it. Guest must never be
	// written to channel_members and is still projected to clients as left.
	Guest bool
}

// CanInviteUsers reports whether this active member may directly add users.
// Keep this predicate aligned with both memory/postgres write boundaries so an
// RPC cannot expose another user's privacy decision before authorizing the
// actor.
func (m ChannelMember) CanInviteUsers(channel Channel) bool {
	if m.Status != ChannelMemberActive {
		return false
	}
	if m.Role == ChannelRoleCreator ||
		(m.Role == ChannelRoleAdmin && (m.AdminRights.InviteUsers || m.AdminRights.ChangeInfo)) {
		return true
	}
	return channel.Megagroup && !channel.DefaultBannedRights.InviteUsers && !m.BannedRights.InviteUsers
}

// CanManageDirectMessages reports whether this active parent-channel member may
// see and address every subscriber topic in the linked direct-messages
// monoforum.  Telegram deliberately does not grant this capability to an
// ordinary channel administrator: the explicit manage_direct_messages right is
// required (creators have the capability implicitly).
func (m ChannelMember) CanManageDirectMessages() bool {
	return m.Status == ChannelMemberActive &&
		(m.Role == ChannelRoleCreator ||
			(m.Role == ChannelRoleAdmin && m.AdminRights.ManageDirectMessages))
}

// CanManageWelcomeMessages is the Layer 229 creator/admin capability. Active
// membership is mandatory even if stale admin rights remain in persisted JSON.
func (m ChannelMember) CanManageWelcomeMessages() bool {
	return m.Status == ChannelMemberActive &&
		(m.Role == ChannelRoleCreator ||
			(m.Role == ChannelRoleAdmin && m.AdminRights.ManageWelcomeMessages))
}

// CanPostChannelMessages reports whether this active member may publish a post
// to a broadcast channel.  Suggested-post managers need this in addition to
// CanManageDirectMessages when approving a subscriber-authored suggestion.
func (m ChannelMember) CanPostChannelMessages() bool {
	return m.Status == ChannelMemberActive &&
		(m.Role == ChannelRoleCreator ||
			(m.Role == ChannelRoleAdmin && m.AdminRights.PostMessages))
}

// ChannelDialog is the current user's owner-view dialog state for a channel.
type ChannelDialog struct {
	UserID                 int64
	ChannelID              int64
	FolderID               int
	TopMessageID           int
	TopMessageDate         int
	HistoryClearAnchorID   int
	HistoryClearAnchorDate int
	ReadInboxMaxID         int
	ReadOutboxMaxID        int
	UnreadCount            int
	UnreadMentions         int
	UnreadReactions        int
	Pinned                 bool
	PinnedOrder            int
	UnreadMark             bool
	ViewForumAsMessages    bool
	HasScheduled           bool
	DefaultSendAs          *Peer
}

// ChannelMessageActionType identifies service messages generated by channel operations.
type ChannelMessageActionType string

const (
	ChannelActionNone            ChannelMessageActionType = ""
	ChannelActionCreate          ChannelMessageActionType = "channel_create"
	ChannelActionChatAddUser     ChannelMessageActionType = "chat_add_user"
	ChannelActionChatDelete      ChannelMessageActionType = "chat_delete_user"
	ChannelActionChatEditPhoto   ChannelMessageActionType = "chat_edit_photo"
	ChannelActionChatDeletePhoto ChannelMessageActionType = "chat_delete_photo"
	// ChannelActionHistoryClear is an owner-local projection at the member's
	// channels.deleteHistory boundary. It is never persisted into the shared
	// channel_messages row and never produces channel PTS.
	ChannelActionHistoryClear ChannelMessageActionType = "history_clear"
	ChannelActionChatJoined   ChannelMessageActionType = "chat_joined"
	// ChannelActionChatJoinedByLink is the service message for joining via an invite link,
	// rendered as "X joined the group via invite link".
	ChannelActionChatJoinedByLink ChannelMessageActionType = "chat_joined_by_link"
	ChannelActionEditTitle        ChannelMessageActionType = "chat_edit_title"
	ChannelActionTopicCreate      ChannelMessageActionType = "topic_create"
	ChannelActionTopicEdit        ChannelMessageActionType = "topic_edit"
	// ChannelActionTodoCompletions / ChannelActionTodoAppendTasks map
	// messageActionTodoCompletions / messageActionTodoAppendTasks: the service messages generated by
	// supergroup checklist collaboration, referencing the original checklist message via reply_to.
	ChannelActionTodoCompletions ChannelMessageActionType = "todo_completions"
	ChannelActionTodoAppendTasks ChannelMessageActionType = "todo_append_tasks"
	// ChannelActionGroupCall maps messageActionGroupCall: started (CallDuration=0)
	// and ended (CallDuration>0) share the same constructor, as in the official semantics.
	ChannelActionGroupCall ChannelMessageActionType = "group_call"
	// ChannelActionGroupCallScheduled maps messageActionGroupCallScheduled:
	// the service message created by a scheduled call ("scheduled a video chat for ..."), ScheduleDate required.
	ChannelActionGroupCallScheduled ChannelMessageActionType = "group_call_scheduled"
	// ChannelActionInviteToGroupCall maps messageActionInviteToGroupCall (the invitee
	// receives a clickable join card via a channel message, with UserIDs being the invitees).
	ChannelActionInviteToGroupCall ChannelMessageActionType = "invite_to_group_call"
	// ChannelActionBoostApply maps messageActionBoostApply.
	ChannelActionBoostApply ChannelMessageActionType = "boost_apply"
	// ChannelActionPaidMessagesPrice maps messageActionPaidMessagesPrice:
	// the service message for a broadcast channel Direct Messages switch/price change.
	ChannelActionPaidMessagesPrice ChannelMessageActionType = "paid_messages_price"
	// ChannelActionStarGift maps messageActionStarGift: the admin-log snapshot of a channel gift.
	ChannelActionStarGift ChannelMessageActionType = "star_gift"
	// ChannelActionStarGiftUnique maps messageActionStarGiftUnique: collectible
	// upgrade, transfer and other ownership changes enter Recent Actions only and do not fabricate channel history/pts.
	ChannelActionStarGiftUnique ChannelMessageActionType = "star_gift_unique"
	// ChannelActionSetChatWallpaper maps messageActionSetChatWallPaper: sets a wallpaper from the channel appearance page.
	ChannelActionSetChatWallpaper ChannelMessageActionType = "set_chat_wallpaper"
	// ChannelActionChangeCommunity maps messageActionChangeCommunity. A non-zero
	// CommunityID means linked; zero means unlinked.
	ChannelActionChangeCommunity       ChannelMessageActionType = "change_community"
	ChannelActionSuggestedPostApproval ChannelMessageActionType = "suggested_post_approval"
	ChannelActionSuggestedPostSuccess  ChannelMessageActionType = "suggested_post_success"
	ChannelActionSuggestedPostRefund   ChannelMessageActionType = "suggested_post_refund"
)

// ChannelMessageAction describes a service action without depending on tg.*.
type ChannelMessageAction struct {
	Type           ChannelMessageActionType
	CommunityID    int64
	Title          string
	IconColor      int
	IconEmojiID    int64
	IconEmojiIDSet bool
	TitleMissing   bool
	Closed         *bool
	Hidden         *bool
	UserIDs        []int64
	// InviterUserID is used only by chat_joined_by_link (messageActionChatJoinedByLink.inviter_id).
	InviterUserID int64
	// CallID/CallAccessHash/CallDuration are used only by group_call / invite_to_group_call.
	CallID         int64
	CallAccessHash int64
	CallDuration   int
	// CallScheduleDate is used only by group_call_scheduled (start time).
	CallScheduleDate int
	// Boosts is used only by the boost_apply service message.
	Boosts int
	// BroadcastMessagesAllowed/Stars are used only by the paid_messages_price service message.
	BroadcastMessagesAllowed bool
	Stars                    int64
	// Completed/Incompleted/TodoItems are used only by todo_* service messages.
	Completed   []int
	Incompleted []int
	TodoItems   []MessageTodoItem
	// StarGift is used only by the star_gift service message.
	StarGift       *MessageStarGiftAction
	StarGiftUnique *MessageStarGiftUniqueAction
	// Wallpaper is used only by the set_chat_wallpaper service message.
	Wallpaper *Wallpaper
	// Photo is used only by the chat_edit_photo service message.
	Photo *Photo
	// Suggested-post lifecycle actions share the immutable price snapshot.  The
	// approval action additionally uses the reject/balance/schedule fields;
	// refund uses PayerInitiated.
	SuggestedPostRejected       bool
	SuggestedPostBalanceTooLow  bool
	SuggestedPostRejectComment  string
	SuggestedPostScheduleDate   int
	SuggestedPostPrice          *SuggestedPostPrice
	SuggestedPostPayerInitiated bool
}

// ChannelMessage is a single stored message in a channel/supergroup.
type ChannelMessage struct {
	ChannelID    int64
	ID           int
	RandomID     int64
	SenderUserID int64
	From         Peer
	SendAs       *Peer
	// SavedPeer is the monoforum direct-messages sub-chat grouping key (grouped by subscriber); an ordinary channel message is the zero value.
	SavedPeer Peer
	// SuggestedPost is the immutable send snapshot of a channel DM suggested post; an ordinary channel message is nil.
	SuggestedPost *SuggestedPost
	// PaidMessageStars is the Stars actually deducted for this channel DM; an admin free reply and an ordinary channel message are 0.
	PaidMessageStars int64
	Date             int
	EditDate         int
	Post             bool
	// ShowMessageSender snapshots profile visibility; historical posts remain anonymous.
	ShowMessageSender bool
	Silent            bool
	NoForwards        bool
	Body              string
	Entities          []MessageEntity
	ReplyTo           *MessageReply
	Forward           *MessageForward
	ViaBotID          int64
	// GroupedID is the album grouping id (the same sendMultiMedia group shares a non-zero value; non-album is always 0).
	GroupedID   int64
	ReplyMarkup *MessageReplyMarkup
	Discussion  *ChannelDiscussionRef
	Replies     *ChannelMessageReplies
	Reactions   *ChannelMessageReactions
	Action      *ChannelMessageAction
	Media       *MessageMedia
	// RichMessage is the Layer 228 rich text message (richMessage) snapshot, optional; an ordinary message is always nil.
	RichMessage *MessageRichMessage
	// FactCheck is the Layer 231 fact-check tag snapshot, optional; a message without a tag is always nil.
	FactCheck *FactCheck
	// FromBoostsApplied is the sender boost count snapshot at send time (message.from_boosts_applied).
	FromBoostsApplied int
	TTLPeriod         int
	ExpiresAt         int
	Pinned            bool
	Mentioned         bool
	MediaUnread       bool
	// Views is the broadcast post's view count aggregate; PostAuthor is the author display-name snapshot
	// when signatures are enabled.
	ViewsCount int
	PostAuthor string
	Pts        int
	Deleted    bool
}

// LinkedDiscussionRootMatches verifies the exact source-post/root pair for a
// top-only comment reply. A normal forwarded message is not a thread root.
func LinkedDiscussionRootMatches(group, source Channel, root, post ChannelMessage) bool {
	return group.Megagroup && !group.Deleted && group.LinkedChatID == source.ID &&
		source.Broadcast && !source.Deleted && source.LinkedChatID == group.ID &&
		root.ChannelID == group.ID && root.ID > 0 && !root.Deleted && root.ReplyTo == nil &&
		root.From == (Peer{Type: PeerTypeChannel, ID: source.ID}) &&
		root.Forward != nil && root.Forward.From == root.From &&
		root.Forward.ChannelPost == post.ID && post.ID > 0 && post.Post && !post.Deleted &&
		post.ChannelID == source.ID && post.Discussion != nil &&
		post.Discussion.ChannelID == group.ID && post.Discussion.MessageID == root.ID
}

// ProjectChannelHistoryClearMessage returns the owner-local service-message
// projection for one channel history boundary. Identity fields from the shared
// source are retained when available, while all user payload, media, reply,
// reaction, pin, TTL and edit state is removed. A zero source is valid for a
// retained anchor whose shared row has already been physically pruned.
func ProjectChannelHistoryClearMessage(source ChannelMessage, channelID int64, messageID, messageDate int) ChannelMessage {
	if source.ChannelID == 0 {
		source.ChannelID = channelID
	}
	if source.ID == 0 {
		source.ID = messageID
	}
	if source.Date == 0 {
		source.Date = messageDate
	}
	return ChannelMessage{
		ChannelID:    source.ChannelID,
		ID:           source.ID,
		SenderUserID: source.SenderUserID,
		From:         source.From,
		Date:         source.Date,
		Post:         source.Post,
		Silent:       source.Silent,
		Action:       &ChannelMessageAction{Type: ChannelActionHistoryClear},
	}
}

// IsChannelHistoryClearMessage reports whether msg is an owner-local history
// clear projection rather than a shared channel service message.
func IsChannelHistoryClearMessage(msg ChannelMessage) bool {
	return msg.Action != nil && msg.Action.Type == ChannelActionHistoryClear
}

// MessageReactionType identifies one stored reaction constructor without depending on TL types.
type MessageReactionType string

const (
	MessageReactionEmoji       MessageReactionType = "emoji"
	MessageReactionCustomEmoji MessageReactionType = "custom_emoji"
)

// MessageReaction describes one supported message reaction value.
type MessageReaction struct {
	Type       MessageReactionType
	Emoticon   string
	DocumentID int64
}

// Value returns the canonical persisted value for a reaction constructor.
func (r MessageReaction) Value() string {
	switch r.Type {
	case MessageReactionEmoji:
		return strings.TrimSpace(r.Emoticon)
	case MessageReactionCustomEmoji:
		if r.DocumentID <= 0 {
			return ""
		}
		return strconv.FormatInt(r.DocumentID, 10)
	default:
		return ""
	}
}

// Key returns a stable identity key for de-duplication and aggregation.
func (r MessageReaction) Key() string {
	return string(r.Type) + "\x00" + r.Value()
}

// Valid reports whether a reaction can be persisted as a message reaction.
func (r MessageReaction) Valid() bool {
	switch r.Type {
	case MessageReactionEmoji:
		value := r.Value()
		return value != "" && utf8.RuneCountInString(value) <= MaxChannelReactionEmoticonLength
	case MessageReactionCustomEmoji:
		return r.DocumentID > 0
	default:
		return false
	}
}

// MessageReactionFromValue rebuilds a domain reaction from the persisted
// reaction_type/reaction_value pair used by stores.
func MessageReactionFromValue(reactionType MessageReactionType, value string) (MessageReaction, bool) {
	switch reactionType {
	case MessageReactionEmoji:
		value = strings.TrimSpace(value)
		reaction := MessageReaction{Type: MessageReactionEmoji, Emoticon: value}
		return reaction, reaction.Valid()
	case MessageReactionCustomEmoji:
		documentID, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return MessageReaction{}, false
		}
		reaction := MessageReaction{Type: MessageReactionCustomEmoji, DocumentID: documentID}
		return reaction, reaction.Valid()
	default:
		return MessageReaction{}, false
	}
}

// ChannelMessageReactionCount is an aggregated reaction counter for one message.
type ChannelMessageReactionCount struct {
	Reaction    MessageReaction
	Count       int
	ChosenOrder int
}

// ChannelMessagePeerReaction describes one peer's reaction entry.
type ChannelMessagePeerReaction struct {
	ChannelID    int64
	MessageID    int
	SenderUserID int64
	UserID       int64
	Reaction     MessageReaction
	Big          bool
	Unread       bool
	My           bool
	ChosenOrder  int
	Date         int
}

// ChannelMessageReactions is the read model carried by channel messages and reaction updates.
type ChannelMessageReactions struct {
	CanSeeList bool
	// AsTags marks reactions on Saved Messages as private message tags.
	// It is never set for ordinary private/channel reactions.
	AsTags  bool
	Results []ChannelMessageReactionCount
	Recent  []ChannelMessagePeerReaction
	// Paid is the paid reaction (Stars) aggregate (nil = none); the read path fills it from channel_message_paid_reactions
	// and the tg conversion injects the ReactionPaid count + top reactors. Stored in a separate table from ordinary reactions.
	Paid *ChannelMessagePaidReactions
}

// SetChannelMessageReactionsRequest replaces the current user's reactions for one message.
type SetChannelMessageReactionsRequest struct {
	UserID      int64
	ChannelID   int64
	MessageID   int
	Reactions   []MessageReaction
	Big         bool
	AddToRecent bool
	Date        int
	// ReactionsPerUserMax is the viewer's per-user limit tier (Premium dual tier);
	// 0 means unfilled, and the store trims to the default tier.
	ReactionsPerUserMax int
}

// ChannelMessageReactionsResult describes one reaction update.
type ChannelMessageReactionsResult struct {
	Channel    Channel
	Message    ChannelMessage
	Messages   []ChannelMessage
	Reactions  ChannelMessageReactions
	Recipients []int64
}

// DeleteChannelParticipantReactionRequest removes one participant reaction on one message.
type DeleteChannelParticipantReactionRequest struct {
	UserID            int64
	ChannelID         int64
	MessageID         int
	ParticipantUserID int64
	Date              int
}

// DeleteChannelParticipantReactionsRequest removes a bounded page of one participant's reactions.
type DeleteChannelParticipantReactionsRequest struct {
	UserID            int64
	ChannelID         int64
	ParticipantUserID int64
	Limit             int
	Date              int
}

// DeleteChannelParticipantReactionsResult describes moderation reaction clears.
type DeleteChannelParticipantReactionsResult struct {
	Channel    Channel
	Messages   []ChannelMessage
	Recipients []int64
	Deleted    int
}

// ChannelMessageReactionsRequest fetches reaction summaries for exact message ids.
type ChannelMessageReactionsRequest struct {
	UserID    int64
	ChannelID int64
	IDs       []int
}

// ChannelMessageReactionsListRequest pages per-peer reactions for one message.
type ChannelMessageReactionsListRequest struct {
	UserID    int64
	ChannelID int64
	MessageID int
	Reaction  *MessageReaction
	Offset    string
	Limit     int
}

// ChannelMessageReactionsList is a bounded page for messages.getMessageReactionsList.
type ChannelMessageReactionsList struct {
	Channel    Channel
	Message    ChannelMessage
	Count      int
	Reactions  []ChannelMessagePeerReaction
	NextOffset string
}

// ChannelMessageReactionLookupRequest is the bounded exact lookup used by
// moderation evidence capture. It avoids paging an arbitrarily large reactor
// list merely to prove that one named participant reacted.
type ChannelMessageReactionLookupRequest struct {
	ViewerUserID  int64
	ChannelID     int64
	MessageID     int
	ReactorUserID int64
}

type ChannelMessageReactionLookup struct {
	Channel   Channel
	Message   ChannelMessage
	Reactions []ChannelMessagePeerReaction
}

// RecentMessageReaction is one account-level recently used message reaction.
type RecentMessageReaction struct {
	UserID   int64
	Reaction MessageReaction
	Date     int
}

// TopMessageReaction is one account-level frequently used message reaction.
type TopMessageReaction struct {
	UserID   int64
	Reaction MessageReaction
	Count    int
	Date     int
}

// SavedReactionTag is one account-level custom title for a saved-message reaction tag.
type SavedReactionTag struct {
	UserID   int64
	Reaction MessageReaction
	Title    string
	Count    int
}

// SavedReactionTagsRequest lists account-level Saved Messages tags. SavedPeer
// is zero for the global list and non-zero for one Saved Messages sub-dialog.
type SavedReactionTagsRequest struct {
	UserID    int64
	SavedPeer Peer
	Limit     int
}

// ChannelDiscussionRef links a broadcast post to its discussion megagroup root message.
type ChannelDiscussionRef struct {
	ChannelID int64
	MessageID int
}

// ChannelDiscussionReadTarget is the minimal authoritative projection needed
// by messages.readDiscussion. It intentionally excludes message/reply/reaction
// payloads used only by messages.getDiscussionMessage.
type ChannelDiscussionReadTarget struct {
	ChannelID   int64
	RootID      int
	AlreadyRead bool
	// Guest means the viewer is authorized through the linked broadcast and has
	// no discussion-group member row whose read boundary can be advanced.
	Guest bool
}

// ChannelMessageReplies describes thread/comment counters without depending on TL types.
type ChannelMessageReplies struct {
	Comments       bool
	Replies        int
	RepliesPts     int
	RecentRepliers []Peer
	ChannelID      int64
	MaxID          int
	ReadMaxID      int
}

// ChannelUpdateEventType identifies channel pts events.
type ChannelUpdateEventType string

const (
	ChannelUpdateNewMessage     ChannelUpdateEventType = "new_channel_message"
	ChannelUpdateEditMessage    ChannelUpdateEventType = "edit_channel_message"
	ChannelUpdateDeleteMessages ChannelUpdateEventType = "delete_channel_messages"
	ChannelUpdateParticipant    ChannelUpdateEventType = "channel_participant"
	ChannelUpdatePinnedMessages ChannelUpdateEventType = "pinned_channel_messages"
	// ChannelUpdateWebPage maps updateChannelWebPage: after a channel message's pending link preview is asynchronously resolved it is
	// replaced in place with the resolved card (correlated by webPage id, not marked "edited"). It is isomorphic to edit_channel_message
	// (carrying channel pts + the message snapshot) and reuses difference/fan-out, differing only in the tg projection constructor.
	ChannelUpdateWebPage ChannelUpdateEventType = "channel_web_page"
	ChannelUpdateNoop    ChannelUpdateEventType = "noop"
)

// ChannelUpdateEvent is the channel-scoped durable update log entry.
type ChannelUpdateEvent struct {
	ChannelID    int64
	Type         ChannelUpdateEventType
	Pts          int
	PtsCount     int
	Date         int
	Message      ChannelMessage
	MessageIDs   []int
	SenderUserID int64
	UserIDs      []int64
	Pinned       bool
	Previous     ChannelMember
	Participant  ChannelMember
}

// FilterChannelUpdateEventForAvailableMinID hides message-scoped updates that
// belong to a member's pre-history-hidden range while preserving pts progress.
func FilterChannelUpdateEventForAvailableMinID(event ChannelUpdateEvent, availableMinID int) (ChannelUpdateEvent, bool) {
	if availableMinID <= 0 {
		return event, true
	}
	switch event.Type {
	case ChannelUpdateNewMessage, ChannelUpdateEditMessage:
		if event.Message.ID != 0 && event.Message.ID <= availableMinID {
			return event, false
		}
	case ChannelUpdateDeleteMessages, ChannelUpdatePinnedMessages:
		if len(event.MessageIDs) == 0 {
			return event, true
		}
		ids := make([]int, 0, len(event.MessageIDs))
		for _, id := range event.MessageIDs {
			if id > availableMinID {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return event, false
		}
		event.MessageIDs = ids
	}
	return event, true
}

// ChannelAdminLogEventType identifies durable admin log actions.
type ChannelAdminLogEventType string

const (
	ChannelAdminLogChangeTitle             ChannelAdminLogEventType = "change_title"
	ChannelAdminLogChangeUsername          ChannelAdminLogEventType = "change_username"
	ChannelAdminLogChangeLinkedChat        ChannelAdminLogEventType = "change_linked_chat"
	ChannelAdminLogToggleSignatures        ChannelAdminLogEventType = "toggle_signatures"
	ChannelAdminLogToggleSignatureProfiles ChannelAdminLogEventType = "toggle_signature_profiles"
	ChannelAdminLogTogglePreHistoryHidden  ChannelAdminLogEventType = "toggle_pre_history_hidden"
	ChannelAdminLogToggleForum             ChannelAdminLogEventType = "toggle_forum"
	ChannelAdminLogToggleAutotranslation   ChannelAdminLogEventType = "toggle_autotranslation"
	ChannelAdminLogToggleAntiSpam          ChannelAdminLogEventType = "toggle_anti_spam"
	ChannelAdminLogToggleSlowMode          ChannelAdminLogEventType = "toggle_slow_mode"
	ChannelAdminLogParticipantInvite       ChannelAdminLogEventType = "participant_invite"
	ChannelAdminLogParticipantJoin         ChannelAdminLogEventType = "participant_join"
	ChannelAdminLogParticipantLeave        ChannelAdminLogEventType = "participant_leave"
	ChannelAdminLogParticipantPromote      ChannelAdminLogEventType = "participant_promote"
	ChannelAdminLogParticipantDemote       ChannelAdminLogEventType = "participant_demote"
	ChannelAdminLogParticipantEditRank     ChannelAdminLogEventType = "participant_edit_rank"
	ChannelAdminLogParticipantBan          ChannelAdminLogEventType = "participant_ban"
	ChannelAdminLogParticipantUnban        ChannelAdminLogEventType = "participant_unban"
	ChannelAdminLogParticipantKick         ChannelAdminLogEventType = "participant_kick"
	ChannelAdminLogParticipantUnkick       ChannelAdminLogEventType = "participant_unkick"
	ChannelAdminLogUpdatePinned            ChannelAdminLogEventType = "update_pinned"
	ChannelAdminLogSendMessage             ChannelAdminLogEventType = "send_message"
	ChannelAdminLogEditMessage             ChannelAdminLogEventType = "edit_message"
	ChannelAdminLogDeleteMessage           ChannelAdminLogEventType = "delete_message"
)

// ChannelAdminLogEvent is a channel-scoped audit entry.
type ChannelAdminLogEvent struct {
	ID              int64
	ChannelID       int64
	UserID          int64
	Date            int
	Type            ChannelAdminLogEventType
	PrevString      string
	NewString       string
	PrevBool        bool
	NewBool         bool
	PrevInt         int
	NewInt          int
	PrevParticipant *ChannelMember
	NewParticipant  *ChannelMember
	Participant     *ChannelMember
	Message         *ChannelMessage
	PrevMessage     *ChannelMessage
	NewMessage      *ChannelMessage
	Query           string
}

// ChannelAdminLogFilter mirrors the user-visible admin log filter categories.
type ChannelAdminLogFilter struct {
	Join      bool
	Leave     bool
	Invite    bool
	Ban       bool
	Unban     bool
	Kick      bool
	Unkick    bool
	Promote   bool
	Demote    bool
	Info      bool
	Settings  bool
	Pinned    bool
	Edit      bool
	Delete    bool
	Send      bool
	Invites   bool
	Forums    bool
	SubExtend bool
	EditRank  bool
}

// Empty reports whether no admin log category filter was supplied.
func (f ChannelAdminLogFilter) Empty() bool {
	return !f.Join && !f.Leave && !f.Invite && !f.Ban && !f.Unban && !f.Kick && !f.Unkick &&
		!f.Promote && !f.Demote && !f.Info && !f.Settings && !f.Pinned && !f.Edit && !f.Delete &&
		!f.Send && !f.Invites && !f.Forums && !f.SubExtend && !f.EditRank
}

// ChannelAdminLogRequest describes one bounded admin log page.
type ChannelAdminLogRequest struct {
	UserID       int64
	ChannelID    int64
	Query        string
	AdminUserIDs []int64
	MaxID        int64
	MinID        int64
	Limit        int
	Filter       ChannelAdminLogFilter
}

// ChannelAdminLogResult is returned by channels.getAdminLog.
type ChannelAdminLogResult struct {
	Channel Channel
	Events  []ChannelAdminLogEvent
}

// ChannelView contains channel data personalized for a viewer.
type ChannelView struct {
	Channel           Channel
	Self              ChannelMember
	Dialog            ChannelDialog
	SelfBoostsApplied int
	ExportedInvite    *ChannelInvite
	// Forbidden means the current viewer is kicked/banned from viewing: the query response must present the
	// channelForbidden form rather than omit it, since the client relies on it to sense that it has left the chat.
	Forbidden bool
	// LinkedChatUsable means Channel.LinkedChatID points to a discussion channel that really exists, is not deleted and is a
	// megagroup. When false the conversion layer must not send linked_chat_id: the client receiving a
	// dangling id would render a "Discussion" entry that leads to an empty channel when tapped.
	LinkedChatUsable bool
}

// ChannelParticipantList is a paged participant response.
type ChannelParticipantList struct {
	Channel      Channel
	Participants []ChannelMember
	Users        []User
	Count        int
	Hash         int64
}

// ChannelParticipantsFilterKind identifies channels.getParticipants filters.
type ChannelParticipantsFilterKind string

const (
	ChannelParticipantsRecent   ChannelParticipantsFilterKind = "recent"
	ChannelParticipantsAdmins   ChannelParticipantsFilterKind = "admins"
	ChannelParticipantsKicked   ChannelParticipantsFilterKind = "kicked"
	ChannelParticipantsBanned   ChannelParticipantsFilterKind = "banned"
	ChannelParticipantsSearch   ChannelParticipantsFilterKind = "search"
	ChannelParticipantsBots     ChannelParticipantsFilterKind = "bots"
	ChannelParticipantsContacts ChannelParticipantsFilterKind = "contacts"
	ChannelParticipantsMentions ChannelParticipantsFilterKind = "mentions"
)

// ChannelParticipantsFilter is a domain-only participant list filter.
type ChannelParticipantsFilter struct {
	Kind  ChannelParticipantsFilterKind
	Query string
}

// ChannelDialogList is a paged dialog response for channel/supergroup peers.
type ChannelDialogList struct {
	Dialogs  []Dialog
	Messages []ChannelMessage
	Channels []Channel
	Users    []User
	Count    int
	Hash     int64
}

// CommonChannelsRequest describes a bounded common-supergroup page.
type CommonChannelsRequest struct {
	UserID       int64
	TargetUserID int64
	MaxID        int64
	Limit        int
	CountOnly    bool
}

// CommonChannelsResult contains shared megagroups and the full shared count.
type CommonChannelsResult struct {
	Count    int
	Channels []Channel
}

// ChannelRecommendationsRequest describes one public broadcast channel recommendation page.
type ChannelRecommendationsRequest struct {
	UserID          int64
	SourceChannelID int64
	Limit           int
}

// ChannelRecommendationsResult contains public broadcast recommendations and a bounded count hint.
type ChannelRecommendationsResult struct {
	Count    int
	Channels []Channel
}

// PublicChannelSearchResult contains contacts.search public channel/supergroup matches.
// Results are public peers the viewer is not an active member of; joined peers
// are intentionally left to dialogs/resolve paths so clients do not render
// invisible discovery rows for already joined channels.
type PublicChannelSearchResult struct {
	MyResults []Channel
	Results   []Channel
}

// LeftChannel is one channel/supergroup the user has left.
type LeftChannel struct {
	Channel Channel
	Self    ChannelMember
}

// LeftChannelsResult contains a bounded page plus the full left count.
type LeftChannelsResult struct {
	Count    int
	Channels []LeftChannel
}

// DiscussionGroupUpdateResult contains channels whose linked_chat_id changed.
type DiscussionGroupUpdateResult struct {
	Channels []Channel
}

// ChannelHistory is a paged channel history response.
type ChannelHistory struct {
	Channel  Channel
	Self     ChannelMember
	Channels []Channel
	Topics   []ChannelForumTopic
	Messages []ChannelMessage
	Users    []User
	Count    int
	Hash     int64
}

// ChannelForumTopic is a forum topic materialized from the topic root service message.
type ChannelForumTopic struct {
	ChannelID            int64
	TopicID              int
	CreatorUserID        int64
	Title                string
	IconColor            int
	IconEmojiID          int64
	TitleMissing         bool
	Closed               bool
	Hidden               bool
	Pinned               bool
	PinnedOrder          int
	Date                 int
	TopMessageID         int
	ReadInboxMaxID       int
	ReadOutboxMaxID      int
	UnreadCount          int
	UnreadMentionsCount  int
	UnreadReactionsCount int
	UnreadPollVotesCount int
}

// ChannelForumTopicFilter pages a forum topic list without OFFSET scans.
type ChannelForumTopicFilter struct {
	ChannelID   int64
	Query       string
	OffsetDate  int
	OffsetID    int
	OffsetTopic int
	Limit       int
}

// ChannelForumTopicList contains a bounded topic page and channel context.
type ChannelForumTopicList struct {
	Channel  Channel
	Dialog   ChannelDialog
	Topics   []ChannelForumTopic
	Messages []ChannelMessage
	Users    []User
	Count    int
}

// ChannelDifference is updates.getChannelDifference domain output.
type ChannelDifference struct {
	Channel      Channel
	Self         ChannelMember
	Events       []ChannelUpdateEvent
	NewMessages  []ChannelMessage
	OtherUpdates []ChannelUpdateEvent
	Users        []User
	Channels     []Channel
	Pts          int
	Final        bool
	TooLong      bool
	Dialog       ChannelDialog
	Timeout      int
}

// DirtyChannel identifies an active channel with shared channel updates and/or
// an owner-local no-PTS history-clear boundary after an account difference date.
type DirtyChannel struct {
	ChannelID           int64
	Pts                 int
	ChannelUpdatesDirty bool
	AvailableMinID      int
	HistoryClearDate    int
}

// ChannelUpdateRetentionCheckpoint is the durable recovery boundary for one channel.
// Events with pts <= RetainedThroughPts may be absent; callers below that floor must receive
// channelDifferenceTooLong. LatestEventDate/LatestPts survive event pruning and keep account-level
// dirty-channel nudges reconstructable after the hot event rows have been removed.
type ChannelUpdateRetentionCheckpoint struct {
	ChannelID          int64
	RetainedThroughPts int
	LatestEventDate    int
	LatestPts          int
}

// ChannelUpdateRetentionResult describes one bounded, atomic prune operation.
type ChannelUpdateRetentionResult struct {
	Checkpoint ChannelUpdateRetentionCheckpoint
	Deleted    int
}

// CreateChannelRequest creates a broadcast channel or megagroup.
type CreateChannelRequest struct {
	CreatorUserID int64
	Title         string
	About         string
	RandomID      int64
	Broadcast     bool
	Megagroup     bool
	Forum         bool
	ForumTabs     bool
	TTLPeriod     int
	MemberUserIDs []int64
	Date          int
}

// CreateChannelResult is returned by channel creation paths.
type CreateChannelResult struct {
	Channel    Channel
	Members    []ChannelMember
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
}

// EditChannelTitleRequest edits a channel/supergroup title and emits a service message.
type EditChannelTitleRequest struct {
	UserID    int64
	ChannelID int64
	Title     string
	Date      int
}

// EditChannelTitleResult describes one title edit.
type EditChannelTitleResult struct {
	Channel    Channel
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
}

// SetChannelWallpaperRequest sets or clears a channel wallpaper and may emit a service message.
type SetChannelWallpaperRequest struct {
	UserID    int64
	ChannelID int64
	Wallpaper *Wallpaper
	Date      int
}

// SetChannelWallpaperResult describes a channel wallpaper change.
type SetChannelWallpaperResult struct {
	Channel    Channel
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
	Changed    bool
}

// EditChannelAboutRequest modifies a channel/supergroup description.
type EditChannelAboutRequest struct {
	UserID    int64
	ChannelID int64
	About     string
	Date      int
}

// EditChannelAdminRequest modifies a member's admin rights.
type EditChannelAdminRequest struct {
	UserID      int64
	ChannelID   int64
	MemberID    int64
	AdminRights ChannelAdminRights
	RankSet     bool
	Rank        string
	Date        int
}

// HasRank reports whether channels.editAdmin explicitly supplied a rank.
func (r EditChannelAdminRequest) HasRank() bool {
	return r.RankSet || r.Rank != ""
}

// EditChannelAdminResult describes the participant transition.
type EditChannelAdminResult struct {
	Channel     Channel
	Previous    ChannelMember
	Participant ChannelMember
	Event       ChannelUpdateEvent
	Recipients  []int64
	Date        int
}

// TransferChannelOwnershipRequest transfers a channel/supergroup to another active member.
type TransferChannelOwnershipRequest struct {
	UserID     int64
	ChannelID  int64
	NewOwnerID int64
	Date       int
}

// TransferChannelOwnershipResult describes both participant transitions produced by an owner transfer.
type TransferChannelOwnershipResult struct {
	Channel          Channel
	PreviousOwner    ChannelMember
	OldOwner         ChannelMember
	PreviousNewOwner ChannelMember
	NewOwner         ChannelMember
	Events           []ChannelUpdateEvent
	Recipients       []int64
	Date             int
}

// EditChannelMemberRankRequest sets or clears a participant's member tag (rank)
// without touching their role or admin rights.
type EditChannelMemberRankRequest struct {
	UserID    int64
	ChannelID int64
	MemberID  int64
	Rank      string
	Date      int
}

// EditChannelBannedRequest modifies a participant's banned rights.
type EditChannelBannedRequest struct {
	UserID       int64
	ChannelID    int64
	Participant  Peer
	BannedRights ChannelBannedRights
	Date         int
}

// EditChannelDefaultBannedRightsRequest modifies global default restrictions.
type EditChannelDefaultBannedRightsRequest struct {
	UserID       int64
	ChannelID    int64
	BannedRights ChannelBannedRights
	Date         int
}

// EditChannelBannedResult describes the participant transition.
type EditChannelBannedResult struct {
	Channel     Channel
	Previous    ChannelMember
	Participant ChannelMember
	Event       ChannelUpdateEvent
	Recipients  []int64
	Date        int
	// Message/ServiceEvent is the "X removed Y" service message produced by a megagroup kick
	// and its channel pts event; a pure mute/unmute generates neither.
	Message      ChannelMessage
	ServiceEvent ChannelUpdateEvent
}

// DeleteChannelRequest deletes a channel/supergroup. Only the creator may do this.
type DeleteChannelRequest struct {
	UserID    int64
	ChannelID int64
	Date      int
}

// UpdateChannelUsernameRequest updates or clears a channel public username.
type UpdateChannelUsernameRequest struct {
	UserID    int64
	ChannelID int64
	Username  string
}

// ChannelAdminSettings is an admin-direct patch of channel moderation settings.
// nil fields are left unchanged; set fields are applied verbatim (no membership
// or permission checks — this is the operator/admin path).
type ChannelAdminSettings struct {
	Gigagroup          *bool
	AntiSpam           *bool
	ParticipantsHidden *bool
	NoForwards         *bool
	JoinToSend         *bool
	JoinRequest        *bool
	SlowmodeSeconds    *int
}

// Empty reports whether the patch changes nothing.
func (p ChannelAdminSettings) Empty() bool {
	return p.Gigagroup == nil && p.AntiSpam == nil && p.ParticipantsHidden == nil &&
		p.NoForwards == nil && p.JoinToSend == nil && p.JoinRequest == nil && p.SlowmodeSeconds == nil
}

// SetChannelPhotoResult describes a channel avatar mutation and its durable
// service message.
type SetChannelPhotoResult struct {
	Channel    Channel
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
	Changed    bool
}

// DeleteChannelResult describes a deleted channel.
type DeleteChannelResult struct {
	Channel    Channel
	Recipients []int64
	// LinkedMonoforum is the associated monoforum (channel Direct Messages container) soft-deleted along with the parent broadcast channel.
	// Deleting the parent channel must delete it too, or it leaves an orphan with monoforum=true whose linked_monoforum_id points to the deleted parent (client rendering would crash).
	// Non-nil only when deleting a parent channel with Direct Messages; nil for an ordinary channel.
	LinkedMonoforum *Channel
}

// SendChannelMessageRequest sends one channel/supergroup message.
type SendChannelMessageRequest struct {
	UserID    int64
	ChannelID int64
	RandomID  int64
	// IdempotencyFingerprint is the SHA-256 of the immutable client send intent. RPC callers
	// provide a raw-TL per-item value; internal callers leave it empty for the store fallback.
	IdempotencyFingerprint []byte
	// IdempotencyPreflighted is trusted internal execution metadata; see the private-send
	// equivalent. It is deliberately excluded from the durable fingerprint.
	IdempotencyPreflighted bool
	Message                string
	Entities               []MessageEntity
	Media                  *MessageMedia
	MentionUserIDs         []int64
	SkipDeliveryUserIDs    []int64
	// SkipRecipientLookup lets high-level realtime fan-out use the online member
	// read model instead of forcing store.SendChannelMessage to synchronously
	// return an active-member recipient list after commit.
	SkipRecipientLookup bool
	// PostAuthor is the author display name snapshotted on a broadcast post when signatures are enabled.
	PostAuthor string
	Silent     bool
	NoForwards bool
	ReplyTo    *MessageReply
	Forward    *MessageForward
	ViaBotID   int64
	// GroupedID is the album grouping id (the same sendMultiMedia group shares a non-zero value; non-album is always 0).
	GroupedID   int64
	ReplyMarkup *MessageReplyMarkup
	RichMessage *MessageRichMessage
	SendAs      *Peer
	Action      *ChannelMessageAction
	Date        int
	TTLPeriod   int
}

// SendMonoforumMessageRequest sends one message to channel Direct Messages (monoforum). MonoforumID is the monoforum
// virtual channel id; SavedPeer is the subscriber sub-chat grouping key (a subscriber sending = self, an admin reply = the target subscriber);
// SenderUserID is the actual sender. Send permission (subscriber identity/admin) is checked in the RPC layer; the store only checks that the monoforum exists.
type SendMonoforumMessageRequest struct {
	MonoforumID            int64
	SenderUserID           int64
	SavedPeer              Peer
	RandomID               int64
	IdempotencyFingerprint []byte
	IdempotencyPreflighted bool
	Message                string
	Entities               []MessageEntity
	Media                  *MessageMedia
	ReplyTo                *MessageReply
	Forward                *MessageForward
	Silent                 bool
	NoForwards             bool
	SuggestedPost          *SuggestedPost
	// AllowPaidStars is the maximum amount the client authorizes; the actual debit is the channel's current price and never the authorized ceiling.
	AllowPaidStars int64
	ClearDraft     bool
	Date           int
}

// ToggleSuggestedPostApprovalRequest is the domain command behind
// messages.toggleSuggestedPostApproval.  MessageID addresses the immutable
// suggestion in one monoforum subscriber sub-dialog.
type ToggleSuggestedPostApprovalRequest struct {
	UserID        int64
	MonoforumID   int64
	MessageID     int
	Reject        bool
	RejectComment string
	ScheduleDate  int
	Date          int
}

// SuggestedPostLifecycleState is persisted so approval, scheduled publication,
// settlement and refund remain idempotent across restarts.
type SuggestedPostLifecycleState string

const (
	SuggestedPostStateBalanceLow SuggestedPostLifecycleState = "balance_low"
	SuggestedPostStateRejected   SuggestedPostLifecycleState = "rejected"
	SuggestedPostStateScheduled  SuggestedPostLifecycleState = "scheduled"
	SuggestedPostStatePublished  SuggestedPostLifecycleState = "published"
	SuggestedPostStateCompleted  SuggestedPostLifecycleState = "completed"
	SuggestedPostStateRefunded   SuggestedPostLifecycleState = "refunded"
)

const MaxSuggestedPostScheduleDelay = 31 * 24 * 60 * 60

// EffectiveSuggestedPostPublishDate validates an approval schedule against the
// server's bounded future window and converts a missing or already-due absolute
// date into an immediate publication at now. Client date pickers can legally
// submit after their selected time has crossed a relative minimum; that delay
// must not turn a valid approval into an error or persist a past accepted date.
func EffectiveSuggestedPostPublishDate(scheduleDate, now int) (int, error) {
	if now <= 0 || scheduleDate > now+MaxSuggestedPostScheduleDelay {
		return 0, ErrSuggestedPostInvalid
	}
	if scheduleDate <= now {
		return now, nil
	}
	return scheduleDate, nil
}

// ToggleSuggestedPostApprovalResult contains every durable update produced by
// one command or lifecycle transition.  OriginalEvent is an edit in the
// monoforum; ServiceEvent is the approval/success/refund service message; an
// optional Published result is the broadcast post.
type ToggleSuggestedPostApprovalResult struct {
	Monoforum         Channel
	Parent            Channel
	SavedPeer         Peer
	State             SuggestedPostLifecycleState
	OriginalMessage   ChannelMessage
	OriginalEvent     ChannelUpdateEvent
	ServiceMessage    ChannelMessage
	ServiceEvent      ChannelUpdateEvent
	Published         *SendChannelMessageResult
	Recipients        []int64
	PayerStarsBalance *StarsBalance
	PayerTONBalance   *int64
	Duplicate         bool
}

// SuggestedPostLifecycleRequest bounds one worker pass; stores must use an
// indexed seek and row locks rather than scanning every approval.
type SuggestedPostLifecycleRequest struct {
	Now   int
	Limit int
}

// ChannelSendReplayRequest addresses either a regular channel send (SavedPeer is zero) or one
// monoforum sub-dialog send (SavedPeer is the subscriber scope).  Lookup is read-only and must
// never re-run membership/permission checks or allocate pts/message ids.
type ChannelSendReplayRequest struct {
	ChannelID              int64
	SenderUserID           int64
	SavedPeer              Peer
	RandomID               int64
	IdempotencyFingerprint []byte
}

// MonoforumHistoryFilter fetches monoforum direct-messages history by subscriber sub-chat.
type MonoforumHistoryFilter struct {
	MonoforumID int64
	SavedPeer   Peer
	Limit       int
	OffsetID    int
}

// MonoforumDialog is the summary of one subscriber sub-chat in channel Direct Messages (monoforum). The read watermark/unread
// is filled by a later P0 slice and is currently 0.
type MonoforumDialog struct {
	SavedPeer       Peer
	TopMessageID    int
	TopMessageDate  int
	UnreadCount     int
	ReadInboxMaxID  int
	ReadOutboxMaxID int
}

// MonoforumDialogList is the monoforum subscriber sub-chat list (ordered by each sub-chat's top message id descending).
type MonoforumDialogList struct {
	MonoforumID int64
	Channel     Channel
	Dialogs     []MonoforumDialog
	Messages    []ChannelMessage
	Count       int
}

// MonoforumDialogsFilter pages the monoforum subscriber sub-chat list (seeking by top message id descending).
type MonoforumDialogsFilter struct {
	MonoforumID int64
	Limit       int
	OffsetID    int
}

// SaveChannelDefaultSendAsRequest stores the current user's default send-as peer for a channel dialog.
type SaveChannelDefaultSendAsRequest struct {
	UserID    int64
	ChannelID int64
	SendAs    *Peer
}

// ChannelMessageViewsRequest reads and optionally increments channel-scoped message view counters.
type ChannelMessageViewsRequest struct {
	UserID    int64
	ChannelID int64
	IDs       []int
	Increment bool
	Date      int
}

// ChannelMessageViewsResult returns current view counters and lightweight reply
// counters by visible message id.
type ChannelMessageViewsResult struct {
	Channel Channel
	Views   map[int]int
	Replies map[int]*ChannelMessageReplies
	Peers   []Peer
}

// ReadChannelMessageContentsRequest marks channel/supergroup content-read hints for one viewer.
type ReadChannelMessageContentsRequest struct {
	UserID    int64
	ChannelID int64
	IDs       []int
}

// ReadChannelMessageContentsResult contains visible messages whose content-read hint can be synced.
type ReadChannelMessageContentsResult struct {
	Channel                         Channel
	Messages                        []ChannelMessage
	ClearedUnreadReactionMessageIDs []int
	// ClearedUnreadMentionMessageIDs are the mention messages flipped from unread to read by this content-read;
	// the client sends readMessageContents as soon as an @ message enters the viewport, and the server must
	// clear them in step or the badge resurrects on the next getDialogs.
	ClearedUnreadMentionMessageIDs []int
}

// GetChannelMessageAuthorRequest resolves the original user author of one channel message.
type GetChannelMessageAuthorRequest struct {
	UserID    int64
	ChannelID int64
	ID        int
}

// GetChannelMessageAuthorResult contains the resolved message author user id.
type GetChannelMessageAuthorResult struct {
	Channel      Channel
	MessageID    int
	SenderUserID int64
}

// ChannelPaidMessagesPriceResult describes a paid/direct-message price setting change.
type ChannelPaidMessagesPriceResult struct {
	Channel         Channel
	ServiceMessage  *SendChannelMessageResult
	ServiceMessages []SendChannelMessageResult
}

// SendChannelMessageResult describes a channel message send.
type SendChannelMessageResult struct {
	Channel    Channel
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
	Duplicate  bool
	// SenderStarsBalance is returned only when a paid-message debit actually occurs; the RPC projects the balance update only to the sender.
	SenderStarsBalance *StarsBalance
	// ReplayDeleteEvent is the existing durable channel delete event paired
	// with a deleted exact-random_id replay. It must be returned only to the
	// caller echo and must never be fanned out as a fresh event.
	ReplayDeleteEvent *ChannelUpdateEvent
	Discussion        *SendChannelDiscussionResult
	// MentionUserIDs are the @-mentioned members parsed from this message; online fan-out uses it to project
	// message.mentioned/media_unread for each recipient.
	MentionUserIDs []int64
	// SkipDeliveryUserIDs are the members excluded from delivery of this message by bot privacy rules (a privacy bot cannot see
	// messages other than commands/@/replies). Recipients has already subtracted them; online fan-out must also skip these
	// members' direct pushes, or an online bot would still receive every group message in real time (persistent history/difference is already hidden).
	SkipDeliveryUserIDs []int64
}

// SendChannelDiscussionResult describes the discussion megagroup root created for a broadcast post.
type SendChannelDiscussionResult struct {
	Channel    Channel
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
	// MentionUserIDs passes through the post's @ targets: the real-time push of a discussion-group linked message must also
	// project mentioned/media_unread per recipient.
	MentionUserIDs []int64
}

// CreateChannelForumTopicRequest creates one forum topic root service message.
type CreateChannelForumTopicRequest struct {
	UserID       int64
	ChannelID    int64
	Title        string
	TitleMissing bool
	IconColor    int
	IconEmojiID  int64
	RandomID     int64
	SendAs       *Peer
	Date         int
}

// CreateChannelForumTopicResult describes the topic root message and topic state.
type CreateChannelForumTopicResult struct {
	Channel    Channel
	Topic      ChannelForumTopic
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
	Duplicate  bool
}

// EditChannelForumTopicRequest edits topic metadata and emits a service message.
type EditChannelForumTopicRequest struct {
	UserID      int64
	ChannelID   int64
	TopicID     int
	Title       *string
	IconEmojiID *int64
	Closed      *bool
	Hidden      *bool
	Date        int
}

// EditChannelForumTopicResult describes one topic edit service message.
type EditChannelForumTopicResult struct {
	Channel    Channel
	Topic      ChannelForumTopic
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
}

// UpdateChannelForumTopicPinnedRequest pins or unpins one topic.
type UpdateChannelForumTopicPinnedRequest struct {
	UserID    int64
	ChannelID int64
	TopicID   int
	Pinned    bool
	Date      int
}

// UpdateChannelForumTopicPinnedResult describes one pinned-topic update.
type UpdateChannelForumTopicPinnedResult struct {
	Channel    Channel
	Topic      ChannelForumTopic
	Recipients []int64
}

// ReorderChannelPinnedForumTopicsRequest stores a bounded pinned topic order.
type ReorderChannelPinnedForumTopicsRequest struct {
	UserID    int64
	ChannelID int64
	Order     []int
	Force     bool
	Date      int
}

// ReorderChannelPinnedForumTopicsResult describes a pinned topic order update.
type ReorderChannelPinnedForumTopicsResult struct {
	Channel    Channel
	Order      []int
	Recipients []int64
}

// DeleteChannelForumTopicHistoryRequest deletes one forum topic page.
type DeleteChannelForumTopicHistoryRequest struct {
	UserID    int64
	ChannelID int64
	TopicID   int
	Date      int
}

// EditChannelMessageRequest edits one text message in a channel/supergroup.
// A non-nil Media replaces the message media snapshot wholesale (the only current caller is live location update/stop).
type EditChannelMessageRequest struct {
	UserID    int64
	ChannelID int64
	ID        int
	Message   string
	Entities  []MessageEntity
	Media     *MessageMedia
	// When SetReplyMarkup is set, replace reply_markup (a nil/empty ReplyMarkup = clear the keyboard).
	SetReplyMarkup bool
	ReplyMarkup    *MessageReplyMarkup
	// When SetRichMessage is set, replace rich_message (a nil/empty RichMessage = clear the rich text).
	SetRichMessage bool
	RichMessage    *MessageRichMessage
	// A non-zero ViaBotEditBotID requires the target message's via_bot_id to match the corresponding bot.
	ViaBotEditBotID int64
	// AllowTodoParticipantMutation lets a non-author ordinary member replace only the todo media snapshot
	// under the checklist's others_can_* grant; it does not open up ordinary text/media editing.
	AllowTodoParticipantMutation bool
	// When TodoServiceAction is non-nil, the edit appends in the same transaction a todo service message
	// as a reply to the original checklist and generates an independent channel pts.
	TodoServiceAction *ChannelMessageAction
	// MentionUserIDs are the @ targets parsed from the edited text: newly added ones get an unread mention and
	// removed ones are cleared (an implicit reply mention is unaffected).
	MentionUserIDs []int64
	EditDate       int
	// When WebPageResolve is set it is a server-internal "replace channel link preview in place": only the media is replaced (body/
	// entities/edit_date untouched) and ChannelUpdateWebPage is generated rather than edit_channel_message. Idempotency guard:
	// replace only when the target's current media is still a pending link preview with ID==ExpectedWebPageID.
	WebPageResolve    bool
	ExpectedWebPageID int64
}

// EditChannelMessageResult describes one channel edit update.
type EditChannelMessageResult struct {
	Channel        Channel
	Message        ChannelMessage
	Event          ChannelUpdateEvent
	ServiceMessage ChannelMessage
	ServiceEvent   ChannelUpdateEvent
	Recipients     []int64
}

// DeleteChannelMessagesRequest deletes a bounded set of channel/supergroup messages.
type DeleteChannelMessagesRequest struct {
	UserID    int64
	ChannelID int64
	IDs       []int
	Date      int
}

// DeleteChannelMessagesResult describes one channel delete update.
type DeleteChannelMessagesResult struct {
	Channel    Channel
	Event      ChannelUpdateEvent
	DeletedIDs []int
	Recipients []int64
	// DiscussionDeletes is the cascade deletion of the forward root of a deleted broadcast post in the linked discussion group
	// (officially deleting a post also deletes the discussion root).
	DiscussionDeletes []ChannelCascadeDelete
}

// ChannelCascadeDelete describes one cross-channel cascade deletion (e.g. a discussion root deleted along with its post).
type ChannelCascadeDelete struct {
	Channel    Channel
	Event      ChannelUpdateEvent
	Recipients []int64
}

// DeleteChannelHistoryRequest clears or deletes a bounded channel/supergroup history page.
type DeleteChannelHistoryRequest struct {
	UserID      int64
	ChannelID   int64
	MaxID       int
	ForEveryone bool
	Date        int
}

// DeleteChannelParticipantHistoryRequest deletes one bounded page of messages sent by a participant.
type DeleteChannelParticipantHistoryRequest struct {
	UserID            int64
	ChannelID         int64
	ParticipantUserID int64
	Date              int
}

// DeleteChannelHistoryResult describes a channel history delete/clear page.
type DeleteChannelHistoryResult struct {
	Channel        Channel
	Event          ChannelUpdateEvent
	DeletedIDs     []int
	Recipients     []int64
	Offset         int
	AvailableMinID int
	// AvailableMinChanged is true only when this owner-local request advanced
	// the member boundary and installed a new history-clear anchor.
	AvailableMinChanged bool
}

// UpdateChannelPinnedMessageRequest pins or unpins one channel/supergroup message.
type UpdateChannelPinnedMessageRequest struct {
	UserID    int64
	ChannelID int64
	MessageID int
	Pinned    bool
	Silent    bool
	Date      int
}

// UpdateChannelPinnedMessageResult describes one pinned-message update.
// Multi-pin model: pin/unpin does not affect other pins, and Event.MessageIDs contains only this operation's
// messages (for unpinAll, the ids cleared in this batch).
type UpdateChannelPinnedMessageResult struct {
	Channel Channel
	Event   ChannelUpdateEvent
	// UnpinEvent carries the old pin's unpin event when a single pin is replaced (Pts=0 if none).
	UnpinEvent ChannelUpdateEvent
	Recipients []int64
}

// UnpinAllChannelMessagesRequest clears every pinned message in one channel.
type UnpinAllChannelMessagesRequest struct {
	UserID    int64
	ChannelID int64
	Date      int
}

// ChannelInvite is an exported invite link without TL dependencies.
type ChannelInvite struct {
	ChannelID      int64
	InviteID       int64
	Hash           string
	AdminUserID    int64
	Title          string
	Permanent      bool
	Revoked        bool
	RequestNeeded  bool
	ExpireDate     int
	UsageLimit     int
	UsageCount     int
	RequestedCount int
	Date           int
}

// ExportChannelInviteRequest creates one invite link.
type ExportChannelInviteRequest struct {
	UserID                int64
	ChannelID             int64
	Title                 string
	RequestNeeded         bool
	ExpireDate            int
	UsageLimit            int
	LegacyRevokePermanent bool
	Date                  int
}

// ExportChannelInviteResult returns the exported invite.
type ExportChannelInviteResult struct {
	Channel Channel
	Invite  ChannelInvite
}

// CheckChannelInviteResult returns public invite preview info.
type CheckChannelInviteResult struct {
	Channel Channel
	Invite  ChannelInvite
	Already bool
	Self    ChannelMember
}

// ImportChannelInviteRequest imports an invite and joins the channel when no approval is needed.
type ImportChannelInviteRequest struct {
	UserID int64
	Hash   string
	Date   int
}

// ChannelInviteListRequest describes an exported invite management page.
type ChannelInviteListRequest struct {
	UserID      int64
	ChannelID   int64
	AdminUserID int64
	Revoked     bool
	OffsetDate  int
	OffsetHash  string
	Limit       int
}

// ChannelInviteList is a bounded exported invite page.
type ChannelInviteList struct {
	Count   int
	Invites []ChannelInvite
}

// GetChannelInviteRequest fetches one exported invite by hash.
type GetChannelInviteRequest struct {
	UserID    int64
	ChannelID int64
	Hash      string
}

// EditChannelInviteRequest edits or revokes an exported invite.
type EditChannelInviteRequest struct {
	UserID           int64
	ChannelID        int64
	Hash             string
	Revoked          bool
	HasExpireDate    bool
	ExpireDate       int
	HasUsageLimit    bool
	UsageLimit       int
	HasRequestNeeded bool
	RequestNeeded    bool
	HasTitle         bool
	Title            string
	Date             int
}

// EditChannelInviteResult returns the edited invite and optional replacement.
type EditChannelInviteResult struct {
	Invite    ChannelInvite
	NewInvite *ChannelInvite
}

// DeleteChannelInviteRequest removes one exported invite.
type DeleteChannelInviteRequest struct {
	UserID    int64
	ChannelID int64
	Hash      string
}

// DeleteRevokedChannelInvitesRequest removes revoked invites for one admin.
type DeleteRevokedChannelInvitesRequest struct {
	UserID      int64
	ChannelID   int64
	AdminUserID int64
	Limit       int
}

// ChannelAdminInviteCount aggregates invite counters for one admin.
type ChannelAdminInviteCount struct {
	AdminUserID         int64
	InvitesCount        int
	RevokedInvitesCount int
}

// ChannelInviteImporter describes a user that joined or requested via an invite.
type ChannelInviteImporter struct {
	ChannelID   int64
	InviteID    int64
	UserID      int64
	Date        int
	Requested   bool
	ApprovedBy  int64
	ViaChatlist bool
	About       string
}

// ChannelInviteImportersRequest describes importer/join-request pagination.
type ChannelInviteImportersRequest struct {
	UserID       int64
	ChannelID    int64
	Hash         string
	Requested    bool
	Query        string
	OffsetDate   int
	OffsetUserID int64
	Limit        int
}

// ChannelInviteImporterList is a bounded importer page.
type ChannelInviteImporterList struct {
	Count     int
	Importers []ChannelInviteImporter
}

// ChannelPendingJoinRequests is the admin-visible pending join request summary.
type ChannelPendingJoinRequests struct {
	ChannelID        int64
	Count            int
	RecentRequesters []int64
}

// HideChannelJoinRequestRequest approves or dismisses one pending join request.
type HideChannelJoinRequestRequest struct {
	UserID       int64
	ChannelID    int64
	TargetUserID int64
	Approved     bool
	Date         int
}

// HideChannelJoinRequestsRequest approves or dismisses pending join requests in a bounded batch.
type HideChannelJoinRequestsRequest struct {
	UserID    int64
	ChannelID int64
	Hash      string
	Approved  bool
	Limit     int
	Date      int
}

// ChannelHistoryFilter describes channel history query conditions.
type ChannelHistoryFilter struct {
	ChannelID    int64
	Query        string
	SenderUserID int64
	PinnedOnly   bool
	MusicOnly    bool
	// IncludeHistoryClearAnchor is set only by messages.getHistory. Search,
	// media and topic projections must not count the owner-local service marker
	// as shared channel content.
	IncludeHistoryClearAnchor bool
	// NeedTotalCount requests the exact number of messages matching the static
	// filters before offset/add_offset pagination. Ordinary history pages leave
	// this false and keep the bounded len(page)+has-more hint.
	NeedTotalCount bool
	// CountOnly skips message hydration and returns only the exact Count plus
	// the viewer-scoped channel metadata needed for access validation.
	CountOnly  bool
	OffsetID   int
	OffsetDate int
	AddOffset  int
	Limit      int
	MinDate    int
	MaxDate    int
	MaxID      int
	MinID      int
	Hash       int64
}

// ChannelSearchPostsRequest describes a bounded global public post search.
type ChannelSearchPostsRequest struct {
	Hashtag         string
	Query           string
	OffsetRate      int
	OffsetChannelID int64
	OffsetID        int
	Limit           int
}

// ChannelGlobalSearchRequest describes a bounded messages.searchGlobal page
// over channel/supergroup messages visible to the current account.
type ChannelGlobalSearchRequest struct {
	Query              string
	ChannelIDs         []int64
	RestrictChannelIDs bool
	// AllowPublicPreview includes linked public channels that the account can
	// preview without joining. It is enabled only by Layer 228 Community-scoped
	// search; ordinary global search remains joined-dialog-only.
	AllowPublicPreview bool
	BroadcastsOnly     bool
	GroupsOnly         bool
	MusicOnly          bool
	HasFolderID        bool
	FolderID           int
	OffsetRate         int
	OffsetChannelID    int64
	OffsetID           int
	MinDate            int
	MaxDate            int
	Limit              int
}

// ChannelRepliesFilter describes messages.getReplies query conditions.
type ChannelRepliesFilter struct {
	ChannelID     int64
	RootMessageID int
	OffsetID      int
	OffsetDate    int
	AddOffset     int
	Limit         int
	MaxID         int
	MinID         int
	Hash          int64
}

// ChannelUnreadMentionsFilter describes messages.getUnreadMentions query conditions.
type ChannelUnreadMentionsFilter struct {
	ChannelID  int64
	TopMsgID   int
	OffsetID   int
	OffsetDate int
	AddOffset  int
	Limit      int
	MaxID      int
	MinID      int
}

// ChannelUnreadReactionsFilter describes messages.getUnreadReactions query conditions.
type ChannelUnreadReactionsFilter struct {
	ChannelID int64
	TopMsgID  int
	OffsetID  int
	AddOffset int
	Limit     int
	MaxID     int
	MinID     int
}

// ReadChannelMentionsRequest clears unread mention state for a channel/supergroup.
type ReadChannelMentionsRequest struct {
	UserID    int64
	ChannelID int64
	TopMsgID  int
	Limit     int
}

// ReadChannelMentionsResult describes a bounded mentions clear operation.
type ReadChannelMentionsResult struct {
	Channel    Channel
	Cleared    int
	Remaining  int
	Offset     int
	ChannelPts int
}

// ReadChannelReactionsRequest clears unread reaction state for a channel/supergroup.
type ReadChannelReactionsRequest struct {
	UserID    int64
	ChannelID int64
	TopMsgID  int
	Limit     int
}

// ReadChannelReactionsResult describes a bounded reactions clear operation.
type ReadChannelReactionsResult struct {
	Channel    Channel
	Cleared    int
	Remaining  int
	Offset     int
	ChannelPts int
}

// ChannelDiscussionMessage describes messages.getDiscussionMessage output.
type ChannelDiscussionMessage struct {
	PostChannel       Channel
	DiscussionChannel Channel
	Messages          []ChannelMessage
	Channels          []Channel
	Users             []User
	MaxID             int
	ReadInboxMaxID    int
	ReadOutboxMaxID   int
	UnreadCount       int
}

// ChannelDifferenceRequest describes a channel difference query.
type ChannelDifferenceRequest struct {
	UserID    int64
	ChannelID int64
	Pts       int
	Limit     int
	Force     bool
}

// ReadChannelHistoryRequest advances the current user's channel read watermark.
type ReadChannelHistoryRequest struct {
	UserID    int64
	ChannelID int64
	MaxID     int
	Date      int
}

// ReadChannelHistoryResult describes a readHistory channel result.
type ReadChannelHistoryResult struct {
	ChannelID        int64
	MaxID            int
	StillUnreadCount int
	Changed          bool
	// ReadOnly marks a synthetic viewer (public preview, linked guest or
	// monoforum shell). The read is acknowledged without creating member,
	// dialog, watermark, receipt or update state.
	ReadOnly bool
	Pts      int
	// Forum marks whether the channel is a forum. The RPC layer uses it to additionally advance
	// the topic-level read watermark of General (topic 1) after a channel-level readHistory (General messages are the channel root history, covered by the channel-level read).
	Forum         bool
	Dialog        ChannelDialog
	OutboxUpdates []ChannelReadOutboxUpdate
}

// ReadChannelTopicHistoryRequest advances the per-viewer read watermark of a single forum topic (messages.readDiscussion).
// TopicID = the topic root message id (General=1). It does not touch the channel-level channel_members.read_inbox_max_id.
type ReadChannelTopicHistoryRequest struct {
	UserID    int64
	ChannelID int64
	TopicID   int
	MaxID     int
	Date      int
}

// ReadChannelTopicHistoryResult describes one per-topic read advance result. Each OutboxUpdates UserID is
// a sender awaiting a read receipt within that topic, and MaxID is the watermark its message was read up to; the topic id is Result.TopicID.
type ReadChannelTopicHistoryResult struct {
	Channel       Channel
	TopicID       int
	MaxID         int
	Changed       bool
	Pts           int
	OutboxUpdates []ChannelReadOutboxUpdate
}

// ChannelReadOutboxUpdate advances one sender's channel outbox read watermark.
type ChannelReadOutboxUpdate struct {
	UserID int64
	MaxID  int
}

// ChannelReadParticipant describes one member's read receipt for a channel message.
type ChannelReadParticipant struct {
	UserID int64
	Date   int
}

// ChannelReadParticipantsRequest queries read receipts for one small megagroup message.
type ChannelReadParticipantsRequest struct {
	UserID    int64
	ChannelID int64
	MessageID int
	Limit     int
	Date      int
}

// ChannelReadParticipantsResult is a bounded set of read receipt participants.
type ChannelReadParticipantsResult struct {
	Channel      Channel
	Message      ChannelMessage
	Participants []ChannelReadParticipant
}
