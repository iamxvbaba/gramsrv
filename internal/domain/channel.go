package domain

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
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
	// MaxDialogUnreadCount 钳制对话未读消息 COUNT 的上界（设计 fan-out epic Phase 2 / P1-v）。
	// 广播/大群走动态 COUNT(*)，积压巨大时既下发天文数字角标、又让 nudge 风暴下每个被 nudge
	// 成员的 getChannelDifference/getPeerDialogs 触发 O(积压) 扫描打爆 PG。store 层一律把未读
	// COUNT 用 LIMIT 子查询(PG)/提前 break(memory) 钳到本上界——既限定扫描工作量也限定下发数值。
	// 只影响角标显示数，不影响 read 水位/pts 真值与 hasUnread(EXISTS) 判定。
	// 取 99（角标显示上限“99+”惯例）：上界即扫描上界，越低单次未读 COUNT 扫描越浅——
	// 相比旧值 1000，积压 >99 的成员每次未读 COUNT（大群读路径 + fan-out 写 + forum 话题）
	// 扫描行数约降一个数量级。值可按客户端角标显示习惯调整。
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
	// MaxMessageReactionsPerUserPremium 是会员档每用户上限，对齐 appConfig
	// reactions_user_max_premium（官方值 3）。客户端按 self premium flag 选档，
	// 服务端档位必须 ≥ 客户端宣告档位，否则 premium 用户的多 reaction 会被
	// 静默裁剪、双端视图错乱。
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
	// ForumGeneralTopicID 是 General 话题的固定 id（Telegram 协议；客户端 readDiscussion General 用 msg_id=1）。
	// General 内消息 reply_to_top_id=1，开 forum 前的历史消息 reply_to_top_id=0，二者都归 General。
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
	ChangeInfo     bool
	PostMessages   bool
	EditMessages   bool
	DeleteMessages bool
	PostStories    bool
	EditStories    bool
	DeleteStories  bool
	BanUsers       bool
	InviteUsers    bool
	PinMessages    bool
	AddAdmins      bool
	ManageCall     bool
	Anonymous      bool
	ManageRanks    bool
	// ManageDirectMessages 对应 TL ChatAdminRights.manage_direct_messages(flags.17)。母广播频道的
	// 管理员据此被客户端授予 monoforum(频道私信)容器的 MonoforumAdmin 身份;creator 走 amCreator 旁路。
	ManageDirectMessages bool
}

// CreatorChannelAdminRights returns the full rights set clients expect on creator projections.
func CreatorChannelAdminRights() ChannelAdminRights {
	return ChannelAdminRights{
		ChangeInfo:     true,
		PostMessages:   true,
		EditMessages:   true,
		DeleteMessages: true,
		PostStories:    true,
		EditStories:    true,
		DeleteStories:  true,
		BanUsers:       true,
		InviteUsers:    true,
		PinMessages:    true,
		AddAdmins:      true,
		ManageCall:     true,
		ManageRanks:    true,
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
	UntilDate       int
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

// MessageReactionsUserMax 返回 viewer 的每用户 reaction 上限档位。
func MessageReactionsUserMax(premium bool) int {
	if premium {
		return MaxMessageReactionsPerUserPremium
	}
	return MaxMessageReactionsPerUser
}

// NormalizeReactionsPerUserMax 把请求携带的档位约束到合法区间：
// <=0（旧调用方未填）回退默认档，上限封顶 premium 档。
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
// perUserMax 经 NormalizeReactionsPerUserMax 约束；premium viewer 传 premium 档。
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
	PreHistoryHidden         bool
	ParticipantsHidden       bool
	AntiSpam                 bool
	// HasLink 表示频道/群存在未撤销的导出邀请链接。Android send-as 入口会用
	// megagroup && (public || has_geo || has_link) 判定是否拉取候选列表。
	HasLink      bool
	LinkedChatID int64
	// Monoforum 标记本频道是「频道私信(Direct Messages)」的 monoforum 虚拟频道。
	// LinkedMonoforumID:母频道指向其 monoforum;monoforum 反向指向母频道(双向)。
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
	// 活跃群通话关联（channel.call_active/call_not_empty flag 与 channelFull.call
	// 的数据源；客户端拉 dialogs/getFullChannel 时凭它重建 banner）。
	ActiveCallID         int64
	ActiveCallAccessHash int64
	ActiveCallNotEmpty   bool
	// 当前头像（反范式存于 channels 表）。PhotoID==0 表示无头像。
	PhotoID       int64
	PhotoDCID     int
	PhotoStripped []byte
}

// MembersListAdminOnly 表示该频道的成员/订阅者列表仅管理员可查看。广播频道（非
// megagroup）的订阅者列表在官方语义里恒为管理员专属——普通订阅者看不到 Subscribers/
// Administrators/Channel Settings 等行，也不能枚举订阅者；此外成员被隐藏
// (ParticipantsHidden) 时对非管理员同样不可见。超级群（megagroup）默认成员可见。
// 该判定只看频道本身，是否放行还需叠加 viewer 是否管理员。
func (c Channel) MembersListAdminOnly() bool {
	return c.ParticipantsHidden || (c.Broadcast && !c.Megagroup)
}

// ChannelMember is one user's channel membership and read state.
type ChannelMember struct {
	ChannelID            int64
	UserID               int64
	InviterUserID        int64
	Role                 ChannelMemberRole
	Status               ChannelMemberStatus
	JoinedAt             int
	LeftAt               int
	AdminRights          ChannelAdminRights
	BannedRights         ChannelBannedRights
	Rank                 string
	AvailableMinID       int
	AvailableMinPts      int
	ReadInboxMaxID       int
	ReadInboxDate        int
	ReadOutboxMaxID      int
	UnreadMark           bool
	SlowmodeLastSendDate int
	// Guest is a computed, non-persisted view used for subscribers accessing a
	// private linked discussion group without joining it. Guest must never be
	// written to channel_members and is still projected to clients as left.
	Guest bool
}

// ChannelDialog is the current user's owner-view dialog state for a channel.
type ChannelDialog struct {
	UserID              int64
	ChannelID           int64
	FolderID            int
	TopMessageID        int
	TopMessageDate      int
	ReadInboxMaxID      int
	ReadOutboxMaxID     int
	UnreadCount         int
	UnreadMentions      int
	UnreadReactions     int
	Pinned              bool
	PinnedOrder         int
	UnreadMark          bool
	ViewForumAsMessages bool
	HasScheduled        bool
	DefaultSendAs       *Peer
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
	ChannelActionChatJoined      ChannelMessageActionType = "chat_joined"
	// ChannelActionChatJoinedByLink 是经邀请链接加入的服务消息，
	// 渲染为 "X joined the group via invite link"。
	ChannelActionChatJoinedByLink ChannelMessageActionType = "chat_joined_by_link"
	ChannelActionEditTitle        ChannelMessageActionType = "chat_edit_title"
	ChannelActionTopicCreate      ChannelMessageActionType = "topic_create"
	ChannelActionTopicEdit        ChannelMessageActionType = "topic_edit"
	// ChannelActionTodoCompletions / ChannelActionTodoAppendTasks 映射
	// messageActionTodoCompletions / messageActionTodoAppendTasks：超级群
	// checklist 协作生成的服务消息，经 reply_to 指向原 checklist 消息。
	ChannelActionTodoCompletions ChannelMessageActionType = "todo_completions"
	ChannelActionTodoAppendTasks ChannelMessageActionType = "todo_append_tasks"
	// ChannelActionGroupCall 映射 messageActionGroupCall：started（CallDuration=0）
	// 与 ended（CallDuration>0）共用同一构造器，官方语义即如此。
	ChannelActionGroupCall ChannelMessageActionType = "group_call"
	// ChannelActionGroupCallScheduled 映射 messageActionGroupCallScheduled：
	// 定时通话创建的服务消息（"scheduled a video chat for ..."），ScheduleDate 必填。
	ChannelActionGroupCallScheduled ChannelMessageActionType = "group_call_scheduled"
	// ChannelActionInviteToGroupCall 映射 messageActionInviteToGroupCall（被邀请
	// 者通过频道消息收到可点击的入会卡片，UserIDs 为受邀人）。
	ChannelActionInviteToGroupCall ChannelMessageActionType = "invite_to_group_call"
	// ChannelActionBoostApply 映射 messageActionBoostApply。
	ChannelActionBoostApply ChannelMessageActionType = "boost_apply"
	// ChannelActionPaidMessagesPrice 映射 messageActionPaidMessagesPrice：
	// 广播频道 Direct Messages 开关/价格变更的服务消息。
	ChannelActionPaidMessagesPrice ChannelMessageActionType = "paid_messages_price"
	// ChannelActionStarGift 映射 messageActionStarGift：频道礼物的 admin-log 快照。
	ChannelActionStarGift ChannelMessageActionType = "star_gift"
	// ChannelActionSetChatWallpaper 映射 messageActionSetChatWallPaper：频道外观页设置 wallpaper。
	ChannelActionSetChatWallpaper ChannelMessageActionType = "set_chat_wallpaper"
)

// ChannelMessageAction describes a service action without depending on tg.*.
type ChannelMessageAction struct {
	Type           ChannelMessageActionType
	Title          string
	IconColor      int
	IconEmojiID    int64
	IconEmojiIDSet bool
	TitleMissing   bool
	Closed         *bool
	Hidden         *bool
	UserIDs        []int64
	// InviterUserID 仅 chat_joined_by_link 使用（messageActionChatJoinedByLink.inviter_id）。
	InviterUserID int64
	// CallID/CallAccessHash/CallDuration 仅 group_call / invite_to_group_call 使用。
	CallID         int64
	CallAccessHash int64
	CallDuration   int
	// CallScheduleDate 仅 group_call_scheduled 使用（开播时间）。
	CallScheduleDate int
	// Boosts 仅 boost_apply 服务消息使用。
	Boosts int
	// BroadcastMessagesAllowed/Stars 仅 paid_messages_price 服务消息使用。
	BroadcastMessagesAllowed bool
	Stars                    int64
	// Completed/Incompleted/TodoItems 仅 todo_* 服务消息使用。
	Completed   []int
	Incompleted []int
	TodoItems   []MessageTodoItem
	// StarGift 仅 star_gift 服务消息使用。
	StarGift *MessageStarGiftAction
	// Wallpaper 仅 set_chat_wallpaper 服务消息使用。
	Wallpaper *Wallpaper
	// Photo 仅 chat_edit_photo 服务消息使用。
	Photo *Photo
}

// ChannelMessage is a single stored message in a channel/supergroup.
type ChannelMessage struct {
	ChannelID    int64
	ID           int
	RandomID     int64
	SenderUserID int64
	From         Peer
	SendAs       *Peer
	// SavedPeer 是 monoforum 私信子会话分组键(按订阅者分组);普通频道消息为零值。
	SavedPeer  Peer
	Date       int
	EditDate   int
	Post       bool
	Silent     bool
	NoForwards bool
	Body       string
	Entities   []MessageEntity
	ReplyTo    *MessageReply
	Forward    *MessageForward
	ViaBotID   int64
	// GroupedID 相册分组 id（sendMultiMedia 同组共享非零值，非相册恒 0）。
	GroupedID   int64
	ReplyMarkup *MessageReplyMarkup
	Discussion  *ChannelDiscussionRef
	Replies     *ChannelMessageReplies
	Reactions   *ChannelMessageReactions
	Action      *ChannelMessageAction
	Media       *MessageMedia
	// RichMessage 是 Layer 227 富文本消息（richMessage）快照，可选；普通消息恒 nil。
	RichMessage *MessageRichMessage
	// FromBoostsApplied 是发送时的 sender boost 数快照（message.from_boosts_applied）。
	FromBoostsApplied int
	TTLPeriod         int
	ExpiresAt         int
	Pinned            bool
	Mentioned         bool
	MediaUnread       bool
	// Views 是 broadcast post 的浏览数聚合；PostAuthor 是 signatures
	// 开启时的作者展示名快照。
	ViewsCount int
	PostAuthor string
	Pts        int
	Deleted    bool
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
	Results    []ChannelMessageReactionCount
	Recent     []ChannelMessagePeerReaction
	// Paid 是付费 reaction（Stars）聚合（nil = 无）；读路径从 channel_message_paid_reactions
	// 填充、tg 转换注入 ReactionPaid 计数 + top reactors。与普通 reaction 分表存储。
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
	// ReactionsPerUserMax 是 viewer 的每用户上限档位（premium 双档）；
	// 0 表示未填，store 侧按默认档裁剪。
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
	// ChannelUpdateWebPage 映射 updateChannelWebPage：频道消息的 pending 链接预览异步解析后
	// 就地替换为已解析卡片（按 webPage id 关联，不标记「已编辑」）。与 edit_channel_message
	// 同构（携频道 pts + 消息快照），difference/fan-out 复用，仅 tg 投影构造器不同。
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
	ChannelAdminLogChangeTitle            ChannelAdminLogEventType = "change_title"
	ChannelAdminLogChangeUsername         ChannelAdminLogEventType = "change_username"
	ChannelAdminLogChangeLinkedChat       ChannelAdminLogEventType = "change_linked_chat"
	ChannelAdminLogToggleSignatures       ChannelAdminLogEventType = "toggle_signatures"
	ChannelAdminLogTogglePreHistoryHidden ChannelAdminLogEventType = "toggle_pre_history_hidden"
	ChannelAdminLogToggleForum            ChannelAdminLogEventType = "toggle_forum"
	ChannelAdminLogToggleAutotranslation  ChannelAdminLogEventType = "toggle_autotranslation"
	ChannelAdminLogToggleAntiSpam         ChannelAdminLogEventType = "toggle_anti_spam"
	ChannelAdminLogToggleSlowMode         ChannelAdminLogEventType = "toggle_slow_mode"
	ChannelAdminLogParticipantInvite      ChannelAdminLogEventType = "participant_invite"
	ChannelAdminLogParticipantJoin        ChannelAdminLogEventType = "participant_join"
	ChannelAdminLogParticipantLeave       ChannelAdminLogEventType = "participant_leave"
	ChannelAdminLogParticipantPromote     ChannelAdminLogEventType = "participant_promote"
	ChannelAdminLogParticipantDemote      ChannelAdminLogEventType = "participant_demote"
	ChannelAdminLogParticipantEditRank    ChannelAdminLogEventType = "participant_edit_rank"
	ChannelAdminLogParticipantBan         ChannelAdminLogEventType = "participant_ban"
	ChannelAdminLogParticipantUnban       ChannelAdminLogEventType = "participant_unban"
	ChannelAdminLogParticipantKick        ChannelAdminLogEventType = "participant_kick"
	ChannelAdminLogParticipantUnkick      ChannelAdminLogEventType = "participant_unkick"
	ChannelAdminLogUpdatePinned           ChannelAdminLogEventType = "update_pinned"
	ChannelAdminLogSendMessage            ChannelAdminLogEventType = "send_message"
	ChannelAdminLogEditMessage            ChannelAdminLogEventType = "edit_message"
	ChannelAdminLogDeleteMessage          ChannelAdminLogEventType = "delete_message"
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
	// Forbidden 表示当前 viewer 被踢/被禁止查看：查询响应必须呈现
	// channelForbidden 形态而不是省略，客户端靠它感知自己已离开会话。
	Forbidden bool
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

// DirtyChannel identifies an active channel with channel-scoped updates after an account difference date.
type DirtyChannel struct {
	ChannelID int64
	Pts       int
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
	// Message/ServiceEvent 是 megagroup 踢人产生的 "X removed Y" 服务
	// 消息及其 channel pts 事件；纯禁言/解禁不生成。
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
	// LinkedMonoforum 是随母广播频道一并软删的关联 monoforum(频道私信容器)。删父频道必须连带删它,
	// 否则会留下 monoforum=true 但 linked_monoforum_id 指向已删父频道的孤儿(客户端渲染会崩)。
	// 仅删带 Direct Messages 的母频道时非 nil;普通频道为 nil。
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
	// PostAuthor 是 signatures 开启的 broadcast post 上快照的作者展示名。
	PostAuthor string
	Silent     bool
	NoForwards bool
	ReplyTo    *MessageReply
	Forward    *MessageForward
	ViaBotID   int64
	// GroupedID 相册分组 id（sendMultiMedia 同组共享非零值，非相册恒 0）。
	GroupedID   int64
	ReplyMarkup *MessageReplyMarkup
	RichMessage *MessageRichMessage
	SendAs      *Peer
	Action      *ChannelMessageAction
	Date        int
	TTLPeriod   int
}

// SendMonoforumMessageRequest 向频道私信(monoforum)发送一条消息。MonoforumID 是 monoforum
// 虚拟频道 id;SavedPeer 是订阅者子会话分组键(订阅者发=自己,管理员回复=目标订阅者);
// SenderUserID 是实际发件人。发件权限(订阅者身份/管理员)在 RPC 层校验,store 只校验 monoforum 存在。
type SendMonoforumMessageRequest struct {
	MonoforumID            int64
	SenderUserID           int64
	SavedPeer              Peer
	RandomID               int64
	IdempotencyFingerprint []byte
	IdempotencyPreflighted bool
	Message                string
	Entities               []MessageEntity
	Date                   int
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

// MonoforumHistoryFilter 按订阅者子会话拉取 monoforum 私信历史。
type MonoforumHistoryFilter struct {
	MonoforumID int64
	SavedPeer   Peer
	Limit       int
	OffsetID    int
}

// MonoforumDialog 是频道私信(monoforum)中一个订阅者子会话的摘要。读水位/未读由后续 P0
// 切片补齐,当前置 0。
type MonoforumDialog struct {
	SavedPeer       Peer
	TopMessageID    int
	TopMessageDate  int
	UnreadCount     int
	ReadInboxMaxID  int
	ReadOutboxMaxID int
}

// MonoforumDialogList 是 monoforum 订阅者子会话列表(按各子会话 top 消息 id 倒序)。
type MonoforumDialogList struct {
	MonoforumID int64
	Channel     Channel
	Dialogs     []MonoforumDialog
	Messages    []ChannelMessage
	Count       int
}

// MonoforumDialogsFilter 分页拉取 monoforum 订阅者子会话列表(按 top 消息 id 倒序 seek)。
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
	// ClearedUnreadMentionMessageIDs 是本次内容已读翻转为已读的 mention
	// 消息；客户端视口看到 @ 消息即发 readMessageContents，服务端必须
	// 同步清除否则角标在下一次 getDialogs 复活。
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
	// ReplayDeleteEvent is the existing durable channel delete event paired
	// with a deleted exact-random_id replay. It must be returned only to the
	// caller echo and must never be fanned out as a fresh event.
	ReplayDeleteEvent *ChannelUpdateEvent
	Discussion        *SendChannelDiscussionResult
	// MentionUserIDs 是本条消息解析出的被 @ 成员；在线 fanout 按它为
	// 每个接收者投影 message.mentioned/media_unread。
	MentionUserIDs []int64
	// SkipDeliveryUserIDs 是本条消息按 bot privacy 规则被排除投递的成员（隐私 bot 对
	// 命令/@/回复以外的消息不可见）。Recipients 已扣除它们；在线 fanout 还须据此跳过这些
	// 成员的直接推送，否则在线 bot 仍能实时收到群里全部消息（持久 history/difference 已隐藏）。
	SkipDeliveryUserIDs []int64
}

// SendChannelDiscussionResult describes the discussion megagroup root created for a broadcast post.
type SendChannelDiscussionResult struct {
	Channel    Channel
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	Recipients []int64
	// MentionUserIDs 透传 post 的 @ 目标：讨论组联动消息的实时推送也要
	// 按接收者投影 mentioned/media_unread。
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
// Media 非 nil 时整体替换消息媒体快照（当前唯一调用方是 live location 续报/停止）。
type EditChannelMessageRequest struct {
	UserID    int64
	ChannelID int64
	ID        int
	Message   string
	Entities  []MessageEntity
	Media     *MessageMedia
	// SetReplyMarkup 置位时替换 reply_markup（ReplyMarkup 为 nil/空 = 清空键盘）。
	SetReplyMarkup bool
	ReplyMarkup    *MessageReplyMarkup
	// SetRichMessage 置位时替换 rich_message（RichMessage nil/空 = 清空富文本）。
	SetRichMessage bool
	RichMessage    *MessageRichMessage
	// ViaBotEditBotID 非零时要求目标消息 via_bot_id 匹配对应 bot。
	ViaBotEditBotID int64
	// AllowTodoParticipantMutation 允许非作者普通成员在 checklist 的
	// others_can_* 授权下仅替换 todo media 快照；不放开普通文本/媒体编辑。
	AllowTodoParticipantMutation bool
	// TodoServiceAction 非 nil 时，编辑同事务追加一条 reply 到原 checklist
	// 的 todo 服务消息，并生成独立 channel pts。
	TodoServiceAction *ChannelMessageAction
	// MentionUserIDs 是编辑后文本解析出的 @ 目标：新增者补未读提及、
	// 被移除者清除（reply 隐式提及不受影响）。
	MentionUserIDs []int64
	EditDate       int
	// WebPageResolve 置位时为服务端内部「频道链接预览就地替换」：仅换 media（不碰 body/
	// entities/edit_date），生成 ChannelUpdateWebPage 而非 edit_channel_message。幂等守卫：
	// 仅当目标当前 media 仍是 ID==ExpectedWebPageID 的 pending 链接预览才替换。
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
	// DiscussionDeletes 是被删 broadcast post 在 linked 讨论组里的转发根
	// 级联删除（官方删 post 同时删除讨论根）。
	DiscussionDeletes []ChannelCascadeDelete
}

// ChannelCascadeDelete 描述一次跨频道的级联删除（如讨论根随 post 删除）。
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
// 多置顶模型：pin/unpin 互不影响其它置顶，Event.MessageIDs 仅含本次操作的
// 消息（unpinAll 时为本批全部清除的 id）。
type UpdateChannelPinnedMessageResult struct {
	Channel Channel
	Event   ChannelUpdateEvent
	// UnpinEvent 携带单置顶替换时旧置顶的 unpin 事件（无则 Pts=0）。
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
	OffsetID     int
	OffsetDate   int
	AddOffset    int
	Limit        int
	MinDate      int
	MaxDate      int
	MaxID        int
	MinID        int
	Hash         int64
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
	Query           string
	BroadcastsOnly  bool
	GroupsOnly      bool
	MusicOnly       bool
	HasFolderID     bool
	FolderID        int
	OffsetRate      int
	OffsetChannelID int64
	OffsetID        int
	MinDate         int
	MaxDate         int
	Limit           int
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
	Pts              int
	// Forum 标记该频道是否为话题群。RPC 层据此在频道级 readHistory 后顺带推进
	// General(topic 1) 的话题级已读水位（General 消息即频道根历史，被频道级已读覆盖）。
	Forum         bool
	Dialog        ChannelDialog
	OutboxUpdates []ChannelReadOutboxUpdate
}

// ReadChannelTopicHistoryRequest 推进 forum 单个话题的 per-viewer 已读水位（messages.readDiscussion）。
// TopicID = 话题根消息 id（General=1）。不碰频道级 channel_members.read_inbox_max_id。
type ReadChannelTopicHistoryRequest struct {
	UserID    int64
	ChannelID int64
	TopicID   int
	MaxID     int
	Date      int
}

// ReadChannelTopicHistoryResult 描述一次 per-topic 已读推进结果。OutboxUpdates 每条 UserID 是
// 该话题内待收已读回执的发送者，MaxID 为其消息被读到的水位；话题 id 即 Result.TopicID。
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
