package store

import (
	"context"

	"telesrv/internal/domain"
)

// ChannelStore persists Telegram channels/supergroups and their single-copy messages.
type ChannelStore interface {
	CreateChannel(ctx context.Context, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	GetChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error)
	// ResolveChannel 是 GetChannel 的轻量版：只做访问校验并返回 Channel(含 access_hash)+Self，
	// 跳过 dialog top/读态/boost 这 3 条额外查询。供只需 access_hash/频道标志的纯解析路径用。
	ResolveChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error)
	GetChannels(ctx context.Context, viewerUserID int64, channelIDs []int64) ([]domain.ChannelView, error)
	GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error)
	SaveChannelDefaultSendAs(ctx context.Context, req domain.SaveChannelDefaultSendAsRequest) (domain.ChannelView, error)
	GetParticipants(ctx context.Context, viewerUserID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error)
	GetParticipant(ctx context.Context, viewerUserID, channelID, participantUserID int64) (domain.ChannelMember, error)
	FutureCreatorAfterLeave(ctx context.Context, channelID, userID int64) (domain.ChannelMember, error)
	InviteToChannel(ctx context.Context, channelID, inviterUserID int64, userIDs []int64, date int) (domain.CreateChannelResult, error)
	JoinChannel(ctx context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error)
	LeaveChannel(ctx context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error)
	EditChannelTitle(ctx context.Context, req domain.EditChannelTitleRequest) (domain.EditChannelTitleResult, error)
	SetChannelWallpaper(ctx context.Context, req domain.SetChannelWallpaperRequest) (domain.SetChannelWallpaperResult, error)
	EditChannelAbout(ctx context.Context, req domain.EditChannelAboutRequest) (domain.Channel, error)
	EditChannelAdmin(ctx context.Context, req domain.EditChannelAdminRequest) (domain.EditChannelAdminResult, error)
	TransferChannelOwnership(ctx context.Context, req domain.TransferChannelOwnershipRequest) (domain.TransferChannelOwnershipResult, error)
	EditChannelMemberRank(ctx context.Context, req domain.EditChannelMemberRankRequest) (domain.EditChannelAdminResult, error)
	EditChannelBanned(ctx context.Context, req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error)
	EditChannelDefaultBannedRights(ctx context.Context, req domain.EditChannelDefaultBannedRightsRequest) (domain.Channel, error)
	DeleteChannel(ctx context.Context, req domain.DeleteChannelRequest) (domain.DeleteChannelResult, error)
	CheckUsername(ctx context.Context, userID, channelID int64, username string) (bool, error)
	UpdateUsername(ctx context.Context, req domain.UpdateChannelUsernameRequest) (domain.Channel, error)
	SetChannelVerified(ctx context.Context, channelID int64, verified bool) (domain.Channel, error)
	ListAdminedPublicChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListStoryPostableChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListSendAsChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ResolvePublicChannelUsername(ctx context.Context, viewerUserID int64, username string) (domain.Channel, bool, error)
	SearchPublicChannels(ctx context.Context, viewerUserID int64, query string, limit int) (domain.PublicChannelSearchResult, error)
	SetSignatures(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetChannelPhoto(ctx context.Context, userID, channelID int64, photo *domain.Photo) (domain.Channel, error)
	SetPreHistoryHidden(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetParticipantsHidden(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetForum(ctx context.Context, userID, channelID int64, enabled, tabs bool) (domain.Channel, error)
	SetAutotranslation(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetRestrictedSponsored(ctx context.Context, userID, channelID int64, restricted bool) (domain.Channel, error)
	SetPaidMessagesPrice(ctx context.Context, userID, channelID int64, stars int64, broadcastMessagesAllowed bool) (domain.ChannelPaidMessagesPriceResult, error)
	SetAntiSpam(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetSlowMode(ctx context.Context, userID, channelID int64, seconds int) (domain.Channel, error)
	SetBoostsToUnblockRestrictions(ctx context.Context, userID, channelID int64, boosts int) (domain.Channel, error)
	SetNoForwards(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetJoinToSend(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetJoinRequest(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetAvailableReactions(ctx context.Context, userID, channelID int64, policy domain.ChannelReactionPolicy) (domain.Channel, error)
	SetColor(ctx context.Context, userID, channelID int64, forProfile bool, color domain.ChannelPeerColor) (domain.Channel, error)
	SetEmojiStatus(ctx context.Context, userID, channelID int64, status domain.ChannelEmojiStatus) (domain.Channel, error)
	ListAdminLog(ctx context.Context, req domain.ChannelAdminLogRequest) (domain.ChannelAdminLogResult, error)
	GetChannelMessageViews(ctx context.Context, req domain.ChannelMessageViewsRequest) (domain.ChannelMessageViewsResult, error)
	SetChannelMessageReactions(ctx context.Context, req domain.SetChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error)
	AddChannelMessagePaidReaction(ctx context.Context, req domain.SendChannelPaidReactionRequest) (domain.ChannelMessagePaidReactionResult, error)
	GetChannelMessageReactions(ctx context.Context, req domain.ChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error)
	VoteChannelMessagePoll(ctx context.Context, req domain.VoteChannelMessagePollRequest) (domain.ChannelMessagePollResult, error)
	CloseChannelMessagePoll(ctx context.Context, req domain.CloseChannelMessagePollRequest) (domain.ChannelMessagePollResult, error)
	ListChannelMessageReactions(ctx context.Context, req domain.ChannelMessageReactionsListRequest) (domain.ChannelMessageReactionsList, error)
	ListTopMessageReactions(ctx context.Context, userID int64, limit int) ([]domain.MessageReaction, error)
	ListRecentMessageReactions(ctx context.Context, userID int64, limit int) ([]domain.MessageReaction, error)
	ClearRecentMessageReactions(ctx context.Context, userID int64) error
	ListSavedReactionTags(ctx context.Context, userID int64, limit int) ([]domain.SavedReactionTag, error)
	UpsertSavedReactionTag(ctx context.Context, tag domain.SavedReactionTag) error
	GetPremiumBoostStatus(ctx context.Context, viewerUserID, channelID int64, now int) (domain.PremiumBoostStatus, error)
	ListPremiumBoosts(ctx context.Context, viewerUserID, channelID int64, gifts bool, offset string, limit, now int) (domain.PremiumBoostList, error)
	GetPremiumMyBoosts(ctx context.Context, userID int64, now, premiumUntil int) (domain.PremiumMyBoosts, error)
	ApplyPremiumBoost(ctx context.Context, userID, channelID int64, slots []int, now, premiumUntil int) (domain.PremiumMyBoosts, error)
	GetPremiumUserBoosts(ctx context.Context, viewerUserID, channelID, targetUserID int64, now int) (domain.PremiumBoostList, error)
	CreateForumTopic(ctx context.Context, req domain.CreateChannelForumTopicRequest) (domain.CreateChannelForumTopicResult, error)
	EditForumTopic(ctx context.Context, req domain.EditChannelForumTopicRequest) (domain.EditChannelForumTopicResult, error)
	UpdatePinnedForumTopic(ctx context.Context, req domain.UpdateChannelForumTopicPinnedRequest) (domain.UpdateChannelForumTopicPinnedResult, error)
	ReorderPinnedForumTopics(ctx context.Context, req domain.ReorderChannelPinnedForumTopicsRequest) (domain.ReorderChannelPinnedForumTopicsResult, error)
	DeleteForumTopicHistory(ctx context.Context, req domain.DeleteChannelForumTopicHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	ListForumTopics(ctx context.Context, viewerUserID int64, filter domain.ChannelForumTopicFilter) (domain.ChannelForumTopicList, error)
	GetForumTopicsByID(ctx context.Context, viewerUserID, channelID int64, ids []int) (domain.ChannelForumTopicList, error)
	SendChannelMessage(ctx context.Context, req domain.SendChannelMessageRequest) (domain.SendChannelMessageResult, error)
	// SendMonoforumMessage 向频道私信(monoforum)发消息,按 saved_peer 分订阅者子会话;不要求成员身份。
	SendMonoforumMessage(ctx context.Context, req domain.SendMonoforumMessageRequest) (domain.SendChannelMessageResult, error)
	EditChannelMessage(ctx context.Context, req domain.EditChannelMessageRequest) (domain.EditChannelMessageResult, error)
	DeleteChannelMessages(ctx context.Context, req domain.DeleteChannelMessagesRequest) (domain.DeleteChannelMessagesResult, error)
	DeleteChannelHistory(ctx context.Context, req domain.DeleteChannelHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	DeleteChannelParticipantHistory(ctx context.Context, req domain.DeleteChannelParticipantHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	UpdatePinnedMessage(ctx context.Context, req domain.UpdateChannelPinnedMessageRequest) (domain.UpdateChannelPinnedMessageResult, error)
	UnpinAllChannelMessages(ctx context.Context, req domain.UnpinAllChannelMessagesRequest) (domain.UpdateChannelPinnedMessageResult, error)
	ClearDanglingPinnedMessage(ctx context.Context, channelID int64, messageID int) error
	ExportInvite(ctx context.Context, req domain.ExportChannelInviteRequest) (domain.ExportChannelInviteResult, error)
	// EnsurePermanentInvite 幂等返回 (channel, admin) 当前未撤销的永久邀请，缺失则创建。
	// 官方语义：每个有邀请权限的管理员都持有自己的主链接（DrKLO 建频道后
	// getExportedChatInvites(limit=1) 直接取 invites[0]，空列表即客户端闪退）。
	// date==0 时由实现取当前时间。
	EnsurePermanentInvite(ctx context.Context, channelID, adminUserID int64, date int) (domain.ChannelInvite, error)
	CheckInvite(ctx context.Context, userID int64, hash string, date int) (domain.CheckChannelInviteResult, error)
	ImportInvite(ctx context.Context, req domain.ImportChannelInviteRequest) (domain.CreateChannelResult, error)
	ListExportedInvites(ctx context.Context, req domain.ChannelInviteListRequest) (domain.ChannelInviteList, error)
	GetExportedInvite(ctx context.Context, req domain.GetChannelInviteRequest) (domain.ChannelInvite, error)
	EditExportedInvite(ctx context.Context, req domain.EditChannelInviteRequest) (domain.EditChannelInviteResult, error)
	DeleteExportedInvite(ctx context.Context, req domain.DeleteChannelInviteRequest) error
	DeleteRevokedExportedInvites(ctx context.Context, req domain.DeleteRevokedChannelInvitesRequest) error
	ListAdminsWithInvites(ctx context.Context, userID, channelID int64) ([]domain.ChannelAdminInviteCount, error)
	ListInviteImporters(ctx context.Context, req domain.ChannelInviteImportersRequest) (domain.ChannelInviteImporterList, error)
	PendingJoinRequests(ctx context.Context, channelID int64, limit int) (domain.ChannelPendingJoinRequests, error)
	HideChatJoinRequest(ctx context.Context, req domain.HideChannelJoinRequestRequest) (domain.CreateChannelResult, error)
	HideAllChatJoinRequests(ctx context.Context, req domain.HideChannelJoinRequestsRequest) (domain.CreateChannelResult, error)
	ListChannelDialogs(ctx context.Context, viewerUserID int64, filter domain.DialogFilter) (domain.ChannelDialogList, error)
	GetChannelDialogs(ctx context.Context, viewerUserID int64, channelIDs []int64) (domain.ChannelDialogList, error)
	ListCommonChannels(ctx context.Context, req domain.CommonChannelsRequest) (domain.CommonChannelsResult, error)
	ListLeftChannels(ctx context.Context, userID int64, offset, limit int) (domain.LeftChannelsResult, error)
	ListInactiveChannels(ctx context.Context, userID int64, limit int) (domain.ChannelDialogList, error)
	ListChannelRecommendations(ctx context.Context, req domain.ChannelRecommendationsRequest) (domain.ChannelRecommendationsResult, error)
	ListDiscussionGroups(ctx context.Context, userID int64, limit int) ([]domain.Channel, error)
	SetDiscussionGroup(ctx context.Context, userID, broadcastID, groupID int64) (domain.DiscussionGroupUpdateResult, error)
	// SetChannelDialogPinned 置顶/取消置顶频道会话；order 在会话当前 folder
	// 内分配，返回 (changed, 该会话所在 folder_id)。
	SetChannelDialogPinned(ctx context.Context, userID, channelID int64, pinned bool) (bool, int, error)
	// ReorderChannelPinnedDialogs 重排指定 folder 内的频道置顶；force 只清除
	// 该 folder 内不在 order 中的置顶。
	ReorderChannelPinnedDialogs(ctx context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error)
	SetChannelDialogUnreadMark(ctx context.Context, userID, channelID int64, unread bool) (bool, error)
	SetChannelViewForumAsMessages(ctx context.Context, userID, channelID int64, enabled bool) (bool, error)
	ListChannelUnreadMarked(ctx context.Context, userID int64) ([]domain.Peer, error)
	EditChannelPeerFolders(ctx context.Context, userID int64, peers []domain.FolderPeerUpdate) error
	// CountChannelArchiveUnread 统计归档中有未读（含手动标记）的频道会话数
	// 与未读消息总数，供主列表 dialogFolder 条目聚合。
	CountChannelArchiveUnread(ctx context.Context, userID int64) (peers int, messages int, err error)
	ListChannelHistory(ctx context.Context, viewerUserID int64, filter domain.ChannelHistoryFilter) (domain.ChannelHistory, error)
	// ListMonoforumHistory 拉取某订阅者在频道私信(monoforum)内的历史。
	ListMonoforumHistory(ctx context.Context, filter domain.MonoforumHistoryFilter) (domain.ChannelHistory, error)
	// ListMonoforumDialogs 列出 monoforum 的订阅者子会话(管理员视角的私信列表)。
	ListMonoforumDialogs(ctx context.Context, filter domain.MonoforumDialogsFilter) (domain.MonoforumDialogList, error)
	// ResolveMonoforumSend 按 id 取 monoforum 频道(不要求成员身份)并返回调用者是否为其母频道管理员。
	ResolveMonoforumSend(ctx context.Context, viewerUserID, monoforumID int64) (domain.Channel, bool, error)
	SearchPublicPosts(ctx context.Context, viewerUserID int64, req domain.ChannelSearchPostsRequest) (domain.ChannelHistory, error)
	SearchJoinedMessages(ctx context.Context, viewerUserID int64, req domain.ChannelGlobalSearchRequest) (domain.ChannelHistory, error)
	GetChannelMessages(ctx context.Context, viewerUserID, channelID int64, ids []int) (domain.ChannelHistory, error)
	// SearchChannelMedia 返回某频道中属于给定媒体类别的消息(共享媒体标签页),newest-first 分页。
	SearchChannelMedia(ctx context.Context, viewerUserID, channelID int64, req domain.MediaSearchRequest) (domain.ChannelHistory, error)
	// CountChannelMediaCategories 返回某频道对当前 viewer 可见消息按基础媒体类别聚合的精确计数。
	CountChannelMediaCategories(ctx context.Context, viewerUserID, channelID int64) (domain.MediaCategoryCounts, error)
	ChannelPollFanoutViews(ctx context.Context, channelID int64, msgID int, viewers []int64, now int) (domain.ChannelPollFanoutViews, error)
	ListStoryMessageForwards(ctx context.Context, req domain.StoryMessageForwardListRequest) (domain.StoryMessageForwardList, error)
	GetChannelMessageForInlineBot(ctx context.Context, botID, channelID int64, id int) (domain.Channel, domain.ChannelMessage, bool, error)
	ReadChannelMessageContents(ctx context.Context, req domain.ReadChannelMessageContentsRequest) (domain.ReadChannelMessageContentsResult, error)
	ListChannelReplies(ctx context.Context, viewerUserID int64, filter domain.ChannelRepliesFilter) (domain.ChannelHistory, error)
	ListChannelUnreadMentions(ctx context.Context, viewerUserID int64, filter domain.ChannelUnreadMentionsFilter) (domain.ChannelHistory, error)
	ReadChannelMentions(ctx context.Context, req domain.ReadChannelMentionsRequest) (domain.ReadChannelMentionsResult, error)
	ListChannelUnreadReactions(ctx context.Context, viewerUserID int64, filter domain.ChannelUnreadReactionsFilter) (domain.ChannelHistory, error)
	ReadChannelReactions(ctx context.Context, req domain.ReadChannelReactionsRequest) (domain.ReadChannelReactionsResult, error)
	GetDiscussionMessage(ctx context.Context, viewerUserID, channelID int64, msgID int) (domain.ChannelDiscussionMessage, error)
	ReadChannelHistory(ctx context.Context, req domain.ReadChannelHistoryRequest) (domain.ReadChannelHistoryResult, error)
	// ReadChannelTopicHistory 推进 forum 单话题的 per-viewer 已读水位（不碰频道级），返回需推进 outbox 的发送者。
	ReadChannelTopicHistory(ctx context.Context, req domain.ReadChannelTopicHistoryRequest) (domain.ReadChannelTopicHistoryResult, error)
	// GeneralForumTopic 现算 forum General 话题（id=1）对 viewer 的状态（per-topic 水位，不被普通话题串扰）。
	GeneralForumTopic(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelForumTopic, error)
	ListMessageReadParticipants(ctx context.Context, req domain.ChannelReadParticipantsRequest) (domain.ChannelReadParticipantsResult, error)
	ListChannelDifference(ctx context.Context, req domain.ChannelDifferenceRequest) (domain.ChannelDifference, error)
	ListActiveChannelIDsForUser(ctx context.Context, userID, afterChannelID int64, limit int) ([]int64, error)
	ListDirtyActiveChannelsForUser(ctx context.Context, userID int64, sinceDate int, afterChannelID int64, limit int) ([]domain.DirtyChannel, error)
	ListActiveChannelMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error)
	ListActiveChannelMembers(ctx context.Context, viewerUserID, channelID int64, limit int) (domain.Channel, domain.ChannelMember, []domain.ChannelMember, error)
	ListChannelInviteAdminMemberIDs(ctx context.Context, channelID int64, limit int) ([]int64, error)
	FilterActiveChannelMemberIDs(ctx context.Context, channelID int64, userIDs []int64) ([]int64, error)
	MaxChannelPts(ctx context.Context, channelID int64) (int, error)
	// SetActiveCall 写入/清除（callID=0）channel 行上的活跃群通话关联
	//（channel.call_active/call_not_empty flag 与 channelFull.call 的数据源）。
	SetActiveCall(ctx context.Context, channelID, callID, callAccessHash int64, notEmpty bool) (domain.Channel, error)
	// AppendCallServiceMessage 生成群通话服务消息（started/ended/invite，带频道
	// pts），Recipients 为活跃成员（rpc 据此扇出 updateNewChannelMessage）。
	AppendCallServiceMessage(ctx context.Context, channelID, senderUserID int64, date int, action domain.ChannelMessageAction) (domain.SendChannelMessageResult, error)
	// AppendStarGiftAdminLog 记录频道 Star gift 的 Recent Actions 快照，不插入频道历史、不推进 pts。
	AppendStarGiftAdminLog(ctx context.Context, channelID, senderUserID int64, savedID int64, date int, action domain.ChannelMessageAction) error
}

// ChannelIDAllocator allocates channel IDs.
type ChannelIDAllocator interface {
	NextChannelID(ctx context.Context) (int64, error)
	CurrentChannelID(ctx context.Context) (int64, error)
}

// ChannelMessageIDAllocator allocates channel-scoped message IDs.
type ChannelMessageIDAllocator interface {
	NextChannelMessageID(ctx context.Context, channelID int64) (int, error)
	CurrentChannelMessageID(ctx context.Context, channelID int64) (int, error)
}
