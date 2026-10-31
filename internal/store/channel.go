package store

import (
	"context"
	"errors"
	"time"

	"telesrv/internal/domain"
)

// MaxActiveChannelMemberPairs bounds every exact channel-membership store
// request independently of caller-side privacy projection admission.
const MaxActiveChannelMemberPairs = 65536

var ErrActiveChannelMemberPairsLimit = errors.New("active channel membership pair limit exceeded")

// ChannelStore persists Telegram channels/supergroups and their single-copy messages.
type ChannelStore interface {
	CreateChannel(ctx context.Context, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	GetChannel(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelView, error)
	// ResolveChannel is a lightweight version of GetChannel: it only performs access
	// validation and returns Channel (with access_hash) + Self, skipping the 3 extra
	// queries for dialog top/read state/boost. Used by pure resolution paths that only
	// need access_hash/channel flags.
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
	SetChannelScamFake(ctx context.Context, channelID int64, scam, fake bool) (domain.Channel, error)
	SetChannelAdminSettings(ctx context.Context, channelID int64, patch domain.ChannelAdminSettings) (domain.Channel, error)
	SetChannelUsernameAdmin(ctx context.Context, channelID int64, username string) (domain.Channel, error)
	SetChannelColorAdmin(ctx context.Context, channelID int64, forProfile bool, color domain.ChannelPeerColor) (domain.Channel, error)
	SetChannelEmojiStatusAdmin(ctx context.Context, channelID int64, status domain.ChannelEmojiStatus) (domain.Channel, error)
	SetChannelPhotoAdmin(ctx context.Context, channelID int64, photo domain.Photo) (domain.Channel, error)
	ListAdminedPublicChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListCommunityLinkableChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListStoryPostableChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListSendAsChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	// ResolvePublicChannelUsername resolves an active public channel/supergroup.
	// viewerUserID may be zero for anonymous public-link projection; this lookup
	// never returns viewer-specific membership or dialog state.
	ResolvePublicChannelUsername(ctx context.Context, viewerUserID int64, username string) (domain.Channel, bool, error)
	SearchPublicChannels(ctx context.Context, viewerUserID int64, query string, limit int) (domain.PublicChannelSearchResult, error)
	SetSignatures(ctx context.Context, userID, channelID int64, enabled, profiles bool) (domain.Channel, error)
	SetChannelPhoto(ctx context.Context, userID, channelID int64, photo *domain.Photo, date int) (domain.SetChannelPhotoResult, error)
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
	// SendMonoforumMessage sends a message to a channel's direct messages (monoforum), splitting subscribers into sub-dialogs by saved_peer; it does not require membership.
	SendMonoforumMessage(ctx context.Context, req domain.SendMonoforumMessageRequest) (domain.SendChannelMessageResult, error)
	EditChannelMessage(ctx context.Context, req domain.EditChannelMessageRequest) (domain.EditChannelMessageResult, error)
	// EditFactCheck writes or deletes the Layer 231 fact-check tag on a message; empty text means delete.
	EditFactCheck(ctx context.Context, req domain.EditFactCheckRequest) (domain.EditFactCheckResult, error)
	DeleteChannelMessages(ctx context.Context, req domain.DeleteChannelMessagesRequest) (domain.DeleteChannelMessagesResult, error)
	DeleteChannelHistory(ctx context.Context, req domain.DeleteChannelHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	DeleteChannelParticipantHistory(ctx context.Context, req domain.DeleteChannelParticipantHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	UpdatePinnedMessage(ctx context.Context, req domain.UpdateChannelPinnedMessageRequest) (domain.UpdateChannelPinnedMessageResult, error)
	UnpinAllChannelMessages(ctx context.Context, req domain.UnpinAllChannelMessagesRequest) (domain.UpdateChannelPinnedMessageResult, error)
	ClearDanglingPinnedMessage(ctx context.Context, channelID int64, messageID int) error
	ExportInvite(ctx context.Context, req domain.ExportChannelInviteRequest) (domain.ExportChannelInviteResult, error)
	// EnsurePermanentInvite idempotently returns the current unrevoked permanent invite
	// for (channel, admin), creating it when missing. Official semantics: every admin with
	// invite permission holds their own primary link (after DrKLO creates a channel,
	// getExportedChatInvites(limit=1) takes invites[0] directly, and an empty list crashes
	// the client). When date==0 the implementation uses the current time.
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
	// SetChannelDialogPinned pins/unpins a channel dialog; order is assigned within the
	// dialog's current folder, returning (changed, the folder_id the dialog belongs to).
	SetChannelDialogPinned(ctx context.Context, userID, channelID int64, pinned bool) (bool, int, error)
	// ReorderChannelPinnedDialogs reorders channel pins within the given folder; force
	// only clears pins in that folder that are not in order.
	ReorderChannelPinnedDialogs(ctx context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error)
	SetChannelDialogUnreadMark(ctx context.Context, userID, channelID int64, unread bool) (bool, error)
	SetChannelViewForumAsMessages(ctx context.Context, userID, channelID int64, enabled bool) (bool, error)
	ListChannelUnreadMarked(ctx context.Context, userID int64) ([]domain.Peer, error)
	EditChannelPeerFolders(ctx context.Context, userID int64, peers []domain.FolderPeerUpdate) error
	// CountChannelArchiveUnread counts channel dialogs with unread (including manually
	// marked) and the total unread message count in the archive, for main-list dialogFolder
	// entry aggregation.
	CountChannelArchiveUnread(ctx context.Context, userID int64) (peers int, messages int, err error)
	ListChannelHistory(ctx context.Context, viewerUserID int64, filter domain.ChannelHistoryFilter) (domain.ChannelHistory, error)
	// ListMonoforumHistory fetches one subscriber's history within channel direct messages (monoforum).
	ListMonoforumHistory(ctx context.Context, filter domain.MonoforumHistoryFilter) (domain.ChannelHistory, error)
	// ListMonoforumDialogs lists the monoforum's subscriber sub-dialogs (the direct-messages list from the admin's view).
	ListMonoforumDialogs(ctx context.Context, filter domain.MonoforumDialogsFilter) (domain.MonoforumDialogList, error)
	// ResolveMonoforumSend fetches a monoforum channel by id (does not require membership) and returns whether the caller is an admin of its parent channel.
	ResolveMonoforumSend(ctx context.Context, viewerUserID, monoforumID int64) (domain.Channel, bool, error)
	SearchPublicPosts(ctx context.Context, viewerUserID int64, req domain.ChannelSearchPostsRequest) (domain.ChannelHistory, error)
	SearchJoinedMessages(ctx context.Context, viewerUserID int64, req domain.ChannelGlobalSearchRequest) (domain.ChannelHistory, error)
	GetChannelMessages(ctx context.Context, viewerUserID, channelID int64, ids []int) (domain.ChannelHistory, error)
	// SearchChannelMedia returns messages in a channel that belong to the given media category (shared media tab), paginated newest-first.
	SearchChannelMedia(ctx context.Context, viewerUserID, channelID int64, req domain.MediaSearchRequest) (domain.ChannelHistory, error)
	// CountChannelMediaCategories returns exact counts of a channel's messages visible to the current viewer, aggregated by base media category.
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
	// ReadChannelTopicHistory advances a forum single topic's per-viewer read watermark (without touching the channel level), returning the senders whose outbox must advance.
	ReadChannelTopicHistory(ctx context.Context, req domain.ReadChannelTopicHistoryRequest) (domain.ReadChannelTopicHistoryResult, error)
	// GeneralForumTopic computes the forum General topic's (id=1) state for the viewer on the fly (per-topic watermark, not disturbed by regular topics).
	GeneralForumTopic(ctx context.Context, viewerUserID, channelID int64) (domain.ChannelForumTopic, error)
	ListMessageReadParticipants(ctx context.Context, req domain.ChannelReadParticipantsRequest) (domain.ChannelReadParticipantsResult, error)
	ListChannelDifference(ctx context.Context, req domain.ChannelDifferenceRequest) (domain.ChannelDifference, error)
	// PruneChannelUpdateEvents atomically removes at most limit complete event rows through throughPts
	// and advances the channel retained floor only to the last contiguous row actually removed.
	PruneChannelUpdateEvents(ctx context.Context, channelID int64, throughPts, limit int) (domain.ChannelUpdateRetentionResult, error)
	// DeleteExpiredChannelUpdateEvents selects expired channel-log heads using an indexed, bounded seek
	// and delegates each channel to the same atomic floor+delete primitive. It never uses SQL OFFSET.
	DeleteExpiredChannelUpdateEvents(ctx context.Context, olderThan time.Duration, limit int) (int, error)
	ListActiveChannelIDsForUser(ctx context.Context, userID, afterChannelID int64, limit int) ([]int64, error)
	ListDirtyActiveChannelsForUser(ctx context.Context, userID int64, sinceDate int, afterChannelID int64, limit int) ([]domain.DirtyChannel, error)
	ListActiveChannelMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error)
	ListActiveChannelMembers(ctx context.Context, viewerUserID, channelID int64, limit int) (domain.Channel, domain.ChannelMember, []domain.ChannelMember, error)
	ListChannelInviteAdminMemberIDs(ctx context.Context, channelID int64, limit int) ([]int64, error)
	FilterActiveChannelMemberIDs(ctx context.Context, channelID int64, userIDs []int64) ([]int64, error)
	// FilterActiveChannelMemberPairs intersects only the supplied channel->user
	// edges. Implementations must keep this as one bounded batch rather than
	// widening it into channels x users or issuing one query per channel.
	FilterActiveChannelMemberPairs(ctx context.Context, userIDsByChannel map[int64][]int64) (map[int64][]int64, error)
	// FilterChannelMessageAudienceIDs authoritatively intersects a bounded online
	// candidate set with users allowed to receive channel message-box updates:
	// active members plus non-banned public-channel preview subscribers.
	FilterChannelMessageAudienceIDs(ctx context.Context, channelID int64, userIDs []int64) ([]int64, error)
	MaxChannelPts(ctx context.Context, channelID int64) (int, error)
	// MaxChannelPtsBatch returns existing channel watermarks with one bounded store round trip.
	// Missing/deleted ids are omitted so a stale process-local membership key cannot poison the
	// entire fan-out recovery sweep.
	MaxChannelPtsBatch(ctx context.Context, channelIDs []int64) (map[int64]int, error)
	// SetActiveCall writes/clears (callID=0) the active group call association on the
	// channel row (the data source for the channel.call_active/call_not_empty flags and
	// channelFull.call).
	SetActiveCall(ctx context.Context, channelID, callID, callAccessHash int64, notEmpty bool) (domain.Channel, error)
	// AppendCallServiceMessage generates a group call service message (started/ended/invite,
	// with channel pts), and Recipients are the active members (rpc fans out
	// updateNewChannelMessage from this).
	AppendCallServiceMessage(ctx context.Context, channelID, senderUserID int64, date int, action domain.ChannelMessageAction) (domain.SendChannelMessageResult, error)
	// AppendStarGiftAdminLog records a Recent Actions snapshot for a channel Star gift without inserting into channel history or advancing pts.
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
