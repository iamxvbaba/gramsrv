package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// registerChannels 注册超级群/频道相关 RPC。messages.createChat 在这里注册，
// 因为 telesrv 将普通群创建直接实现为 megagroup。
func (r *Router) registerChannels(d *tlprofile.Dispatcher) {
	registerRPC[*tg.MessagesCreateChatRequest](d, tlprofile.SemanticMethodMessagesCreateChat, func(ctx context.Context, layerRequest *tg.MessagesCreateChatRequest) (any, error) {
		return r.onMessagesCreateChat(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesMigrateChatRequest](d, tlprofile.SemanticMethodMessagesMigrateChat, func(ctx context.Context, layerRequest *tg.MessagesMigrateChatRequest) (any, error) {
		return r.onMessagesMigrateChat(ctx, layerRequest.
			ChatID)
	})
	registerRPC[*tg.MessagesGetChatsRequest](d, tlprofile.SemanticMethodMessagesGetChats, func(ctx context.Context, layerRequest *tg.MessagesGetChatsRequest) (any, error) {
		return r.onMessagesGetChats(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.MessagesGetFullChatRequest](d, tlprofile.SemanticMethodMessagesGetFullChat, func(ctx context.Context, layerRequest *tg.MessagesGetFullChatRequest) (any, error) {
		return r.onMessagesGetFullChat(ctx, layerRequest.
			ChatID)
	})
	registerRPC[*tg.MessagesAddChatUserRequest](d, tlprofile.SemanticMethodMessagesAddChatUser, func(ctx context.Context, layerRequest *tg.MessagesAddChatUserRequest) (any, error) {
		return r.onMessagesAddChatUser(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteChatUserRequest](d, tlprofile.SemanticMethodMessagesDeleteChatUser, func(ctx context.Context, layerRequest *tg.MessagesDeleteChatUserRequest) (any, error) {
		return r.onMessagesDeleteChatUser(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditChatTitleRequest](d, tlprofile.SemanticMethodMessagesEditChatTitle, func(ctx context.Context, layerRequest *tg.MessagesEditChatTitleRequest) (any, error) {
		return r.onMessagesEditChatTitle(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditChatPhotoRequest](d, tlprofile.SemanticMethodMessagesEditChatPhoto, func(ctx context.Context, layerRequest *tg.MessagesEditChatPhotoRequest) (any, error) {
		return r.onMessagesEditChatPhoto(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditChatAdminRequest](d, tlprofile.SemanticMethodMessagesEditChatAdmin, func(ctx context.Context, layerRequest *tg.MessagesEditChatAdminRequest) (any, error) {
		return r.onMessagesEditChatAdmin(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditChatAboutRequest](d, tlprofile.SemanticMethodMessagesEditChatAbout, func(ctx context.Context, layerRequest *tg.MessagesEditChatAboutRequest) (any, error) {
		return r.onMessagesEditChatAbout(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditChatDefaultBannedRightsRequest](d, tlprofile.SemanticMethodMessagesEditChatDefaultBannedRights, func(ctx context.Context, layerRequest *tg.MessagesEditChatDefaultBannedRightsRequest) (any, error) {
		return r.onMessagesEditChatDefaultBannedRights(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditChatCreatorRequest](d, tlprofile.SemanticMethodMessagesEditChatCreator, func(ctx context.Context, layerRequest *tg.MessagesEditChatCreatorRequest) (any, error) {
		return r.onMessagesEditChatCreator(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetFutureChatCreatorAfterLeaveRequest](d, tlprofile.SemanticMethodMessagesGetFutureChatCreatorAfterLeave, func(ctx context.Context, layerRequest *tg.MessagesGetFutureChatCreatorAfterLeaveRequest) (any, error) {
		return r.onMessagesGetFutureChatCreatorAfterLeave(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.MessagesEditChatParticipantRankRequest](d, tlprofile.SemanticMethodMessagesEditChatParticipantRank, func(ctx context.Context, layerRequest *tg.MessagesEditChatParticipantRankRequest) (any, error) {
		return r.onMessagesEditChatParticipantRank(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetChatThemeRequest](d, tlprofile.SemanticMethodMessagesSetChatTheme, func(ctx context.Context, layerRequest *tg.MessagesSetChatThemeRequest) (any, error) {
		return r.onMessagesSetChatTheme(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetChatWallPaperRequest](d, tlprofile.SemanticMethodMessagesSetChatWallPaper, func(ctx context.Context, layerRequest *tg.MessagesSetChatWallPaperRequest) (any, error) {
		return r.onMessagesSetChatWallPaper(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesToggleNoForwardsRequest](d, tlprofile.SemanticMethodMessagesToggleNoForwards, func(ctx context.Context, layerRequest *tg.MessagesToggleNoForwardsRequest) (any, error) {
		return r.onMessagesToggleNoForwards(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetChatAvailableReactionsRequest](d, tlprofile.SemanticMethodMessagesSetChatAvailableReactions, func(ctx context.Context, layerRequest *tg.MessagesSetChatAvailableReactionsRequest) (any, error) {
		return r.onMessagesSetChatAvailableReactions(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsCreateChannelRequest](d, tlprofile.SemanticMethodChannelsCreateChannel, func(ctx context.Context, layerRequest *tg.ChannelsCreateChannelRequest) (any, error) {
		return r.onChannelsCreateChannel(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsGetChannelsRequest](d, tlprofile.SemanticMethodChannelsGetChannels, func(ctx context.Context, layerRequest *tg.ChannelsGetChannelsRequest) (any, error) {
		return r.onChannelsGetChannels(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.ChannelsGetFullChannelRequest](d, tlprofile.SemanticMethodChannelsGetFullChannel, func(ctx context.Context, layerRequest *tg.ChannelsGetFullChannelRequest) (any, error) {
		return r.onChannelsGetFullChannel(ctx, layerRequest.
			Channel)
	})
	registerRPC[*tg.ChannelsGetParticipantsRequest](d, tlprofile.SemanticMethodChannelsGetParticipants, func(ctx context.Context, layerRequest *tg.ChannelsGetParticipantsRequest) (any, error) {
		return r.onChannelsGetParticipants(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsGetParticipantRequest](d, tlprofile.SemanticMethodChannelsGetParticipant, func(ctx context.Context, layerRequest *tg.ChannelsGetParticipantRequest) (any, error) {
		return r.onChannelsGetParticipant(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsGetSendAsRequest](d, tlprofile.SemanticMethodChannelsGetSendAs, func(ctx context.Context, layerRequest *tg.ChannelsGetSendAsRequest) (any, error) {
		return r.onChannelsGetSendAs(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsCheckUsernameRequest](d, tlprofile.SemanticMethodChannelsCheckUsername, func(ctx context.Context, layerRequest *tg.ChannelsCheckUsernameRequest) (any, error) {
		return r.onChannelsCheckUsername(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsUpdateUsernameRequest](d, tlprofile.SemanticMethodChannelsUpdateUsername, func(ctx context.Context, layerRequest *tg.ChannelsUpdateUsernameRequest) (any, error) {
		return r.onChannelsUpdateUsername(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsGetAdminedPublicChannelsRequest](d, tlprofile.SemanticMethodChannelsGetAdminedPublicChannels, func(ctx context.Context, layerRequest *tg.ChannelsGetAdminedPublicChannelsRequest) (any, error) {
		return r.onChannelsGetAdminedPublicChannels(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsExportMessageLinkRequest](d, tlprofile.SemanticMethodChannelsExportMessageLink, func(ctx context.Context, layerRequest *tg.ChannelsExportMessageLinkRequest) (any, error) {
		return r.onChannelsExportMessageLink(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleSignaturesRequest](d, tlprofile.SemanticMethodChannelsToggleSignatures, func(ctx context.Context, layerRequest *tg.ChannelsToggleSignaturesRequest) (any, error) {
		return r.onChannelsToggleSignatures(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsTogglePreHistoryHiddenRequest](d, tlprofile.SemanticMethodChannelsTogglePreHistoryHidden, func(ctx context.Context, layerRequest *tg.ChannelsTogglePreHistoryHiddenRequest) (any, error) {
		return r.onChannelsTogglePreHistoryHidden(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleSlowModeRequest](d, tlprofile.SemanticMethodChannelsToggleSlowMode, func(ctx context.Context, layerRequest *tg.ChannelsToggleSlowModeRequest) (any, error) {
		return r.onChannelsToggleSlowMode(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsSetStickersRequest](d, tlprofile.SemanticMethodChannelsSetStickers, func(ctx context.Context, layerRequest *tg.ChannelsSetStickersRequest) (any, error) {
		return r.onChannelsSetStickers(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsSetEmojiStickersRequest](d, tlprofile.SemanticMethodChannelsSetEmojiStickers, func(ctx context.Context, layerRequest *tg.ChannelsSetEmojiStickersRequest) (any, error) {
		return r.onChannelsSetEmojiStickers(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsReorderUsernamesRequest](d, tlprofile.SemanticMethodChannelsReorderUsernames, func(ctx context.Context, layerRequest *tg.ChannelsReorderUsernamesRequest) (any, error) {
		return r.onChannelsReorderUsernames(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleUsernameRequest](d, tlprofile.SemanticMethodChannelsToggleUsername, func(ctx context.Context, layerRequest *tg.ChannelsToggleUsernameRequest) (any, error) {
		return r.onChannelsToggleUsername(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsDeactivateAllUsernamesRequest](d, tlprofile.SemanticMethodChannelsDeactivateAllUsernames, func(ctx context.Context, layerRequest *tg.ChannelsDeactivateAllUsernamesRequest) (any, error) {
		return r.onChannelsDeactivateAllUsernames(ctx, layerRequest.
			Channel)
	})
	registerRPC[*tg.ChannelsUpdateColorRequest](d, tlprofile.SemanticMethodChannelsUpdateColor, func(ctx context.Context, layerRequest *tg.ChannelsUpdateColorRequest) (any, error) {
		return r.onChannelsUpdateColor(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsUpdateEmojiStatusRequest](d, tlprofile.SemanticMethodChannelsUpdateEmojiStatus, func(ctx context.Context, layerRequest *tg.ChannelsUpdateEmojiStatusRequest) (any, error) {
		return r.onChannelsUpdateEmojiStatus(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsReadMessageContentsRequest](d, tlprofile.SemanticMethodChannelsReadMessageContents, func(ctx context.Context, layerRequest *tg.ChannelsReadMessageContentsRequest) (any, error) {
		return r.onChannelsReadMessageContents(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsReportSpamRequest](d, tlprofile.SemanticMethodChannelsReportSpam, func(ctx context.Context, layerRequest *tg.ChannelsReportSpamRequest) (any, error) {
		return r.onChannelsReportSpam(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsGetLeftChannelsRequest](d, tlprofile.SemanticMethodChannelsGetLeftChannels, func(ctx context.Context, layerRequest *tg.ChannelsGetLeftChannelsRequest) (any, error) {
		return r.onChannelsGetLeftChannels(ctx, layerRequest.
			Offset)
	})
	registerRPC[*tg.ChannelsGetInactiveChannelsRequest](d, tlprofile.SemanticMethodChannelsGetInactiveChannels, func(ctx context.Context, layerRequest *tg.ChannelsGetInactiveChannelsRequest) (any, error) {
		return r.onChannelsGetInactiveChannels(ctx)
	})
	registerRPC[*tg.ChannelsGetGroupsForDiscussionRequest](d, tlprofile.SemanticMethodChannelsGetGroupsForDiscussion, func(ctx context.Context, layerRequest *tg.ChannelsGetGroupsForDiscussionRequest) (any, error) {
		return r.onChannelsGetGroupsForDiscussion(ctx)
	})
	registerRPC[*tg.ChannelsSetDiscussionGroupRequest](d, tlprofile.SemanticMethodChannelsSetDiscussionGroup, func(ctx context.Context, layerRequest *tg.ChannelsSetDiscussionGroupRequest) (any, error) {
		return r.onChannelsSetDiscussionGroup(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsEditLocationRequest](d, tlprofile.SemanticMethodChannelsEditLocation, func(ctx context.Context, layerRequest *tg.ChannelsEditLocationRequest) (any, error) {
		return r.onChannelsEditLocation(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsConvertToGigagroupRequest](d, tlprofile.SemanticMethodChannelsConvertToGigagroup, func(ctx context.Context, layerRequest *tg.ChannelsConvertToGigagroupRequest) (any, error) {
		return r.onChannelsConvertToGigagroup(ctx, layerRequest.
			Channel)
	})
	registerRPC[*tg.ChannelsDeleteParticipantHistoryRequest](d, tlprofile.SemanticMethodChannelsDeleteParticipantHistory, func(ctx context.Context, layerRequest *tg.ChannelsDeleteParticipantHistoryRequest) (any, error) {
		return r.onChannelsDeleteParticipantHistory(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleJoinToSendRequest](d, tlprofile.SemanticMethodChannelsToggleJoinToSend, func(ctx context.Context, layerRequest *tg.ChannelsToggleJoinToSendRequest) (any, error) {
		return r.onChannelsToggleJoinToSend(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleJoinRequestRequest](d, tlprofile.SemanticMethodChannelsToggleJoinRequest, func(ctx context.Context, layerRequest *tg.ChannelsToggleJoinRequestRequest) (any, error) {
		return r.onChannelsToggleJoinRequest(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleForumRequest](d, tlprofile.SemanticMethodChannelsToggleForum, func(ctx context.Context, layerRequest *tg.ChannelsToggleForumRequest) (any, error) {
		return r.onChannelsToggleForum(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleAntiSpamRequest](d, tlprofile.SemanticMethodChannelsToggleAntiSpam, func(ctx context.Context, layerRequest *tg.ChannelsToggleAntiSpamRequest) (any, error) {
		return r.onChannelsToggleAntiSpam(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsReportAntiSpamFalsePositiveRequest](d, tlprofile.SemanticMethodChannelsReportAntiSpamFalsePositive, func(ctx context.Context, layerRequest *tg.ChannelsReportAntiSpamFalsePositiveRequest) (any, error) {
		return r.onChannelsReportAntiSpamFalsePositive(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleParticipantsHiddenRequest](d, tlprofile.SemanticMethodChannelsToggleParticipantsHidden, func(ctx context.Context, layerRequest *tg.ChannelsToggleParticipantsHiddenRequest) (any, error) {
		return r.onChannelsToggleParticipantsHidden(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleViewForumAsMessagesRequest](d, tlprofile.SemanticMethodChannelsToggleViewForumAsMessages, func(ctx context.Context, layerRequest *tg.ChannelsToggleViewForumAsMessagesRequest) (any, error) {
		return r.onChannelsToggleViewForumAsMessages(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsGetChannelRecommendationsRequest](d, tlprofile.SemanticMethodChannelsGetChannelRecommendations, func(ctx context.Context, layerRequest *tg.ChannelsGetChannelRecommendationsRequest) (any, error) {
		return r.onChannelsGetChannelRecommendations(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsSetBoostsToUnblockRestrictionsRequest](d, tlprofile.SemanticMethodChannelsSetBoostsToUnblockRestrictions, func(ctx context.Context, layerRequest *tg.ChannelsSetBoostsToUnblockRestrictionsRequest) (any, error) {
		return r.onChannelsSetBoostsToUnblockRestrictions(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsRestrictSponsoredMessagesRequest](d, tlprofile.SemanticMethodChannelsRestrictSponsoredMessages, func(ctx context.Context, layerRequest *tg.ChannelsRestrictSponsoredMessagesRequest) (any, error) {
		return r.onChannelsRestrictSponsoredMessages(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsSearchPostsRequest](d, tlprofile.SemanticMethodChannelsSearchPosts, func(ctx context.Context, layerRequest *tg.ChannelsSearchPostsRequest) (any, error) {
		return r.onChannelsSearchPosts(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsUpdatePaidMessagesPriceRequest](d, tlprofile.SemanticMethodChannelsUpdatePaidMessagesPrice, func(ctx context.Context, layerRequest *tg.ChannelsUpdatePaidMessagesPriceRequest) (any, error) {
		return r.onChannelsUpdatePaidMessagesPrice(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsToggleAutotranslationRequest](d, tlprofile.SemanticMethodChannelsToggleAutotranslation, func(ctx context.Context, layerRequest *tg.ChannelsToggleAutotranslationRequest) (any, error) {
		return r.onChannelsToggleAutotranslation(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsGetMessageAuthorRequest](d, tlprofile.SemanticMethodChannelsGetMessageAuthor, func(ctx context.Context, layerRequest *tg.ChannelsGetMessageAuthorRequest) (any, error) {
		return r.onChannelsGetMessageAuthor(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsCheckSearchPostsFloodRequest](d, tlprofile.SemanticMethodChannelsCheckSearchPostsFlood, func(ctx context.Context, layerRequest *tg.ChannelsCheckSearchPostsFloodRequest) (any, error) {
		return r.onChannelsCheckSearchPostsFlood(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsSetMainProfileTabRequest](d, tlprofile.SemanticMethodChannelsSetMainProfileTab, func(ctx context.Context, layerRequest *tg.ChannelsSetMainProfileTabRequest) (any, error) {
		return r.onChannelsSetMainProfileTab(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsInviteToChannelRequest](d, tlprofile.SemanticMethodChannelsInviteToChannel, func(ctx context.Context, layerRequest *tg.ChannelsInviteToChannelRequest) (any, error) {
		return r.onChannelsInviteToChannel(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsJoinChannelRequest](d, tlprofile.SemanticMethodChannelsJoinChannel, func(ctx context.Context, layerRequest *tg.ChannelsJoinChannelRequest) (any, error) {
		return r.onChannelsJoinChannel(ctx, layerRequest.
			Channel)
	})
	registerRPC[*tg.ChannelsLeaveChannelRequest](d, tlprofile.SemanticMethodChannelsLeaveChannel, func(ctx context.Context, layerRequest *tg.ChannelsLeaveChannelRequest) (any, error) {
		return r.onChannelsLeaveChannel(ctx, layerRequest.
			Channel)
	})
	registerRPC[*tg.ChannelsEditAdminRequest](d, tlprofile.SemanticMethodChannelsEditAdmin, func(ctx context.Context, layerRequest *tg.ChannelsEditAdminRequest) (any, error) {
		return r.onChannelsEditAdmin(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsEditBannedRequest](d, tlprofile.SemanticMethodChannelsEditBanned, func(ctx context.Context, layerRequest *tg.ChannelsEditBannedRequest) (any, error) {
		return r.onChannelsEditBanned(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsEditTitleRequest](d, tlprofile.SemanticMethodChannelsEditTitle, func(ctx context.Context, layerRequest *tg.ChannelsEditTitleRequest) (any, error) {
		return r.onChannelsEditTitle(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsEditPhotoRequest](d, tlprofile.SemanticMethodChannelsEditPhoto, func(ctx context.Context, layerRequest *tg.ChannelsEditPhotoRequest) (any, error) {
		return r.onChannelsEditPhoto(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsDeleteChannelRequest](d, tlprofile.SemanticMethodChannelsDeleteChannel, func(ctx context.Context, layerRequest *tg.ChannelsDeleteChannelRequest) (any, error) {
		return r.onChannelsDeleteChannel(ctx, layerRequest.
			Channel)
	})
	registerRPC[*tg.ChannelsGetAdminLogRequest](d, tlprofile.SemanticMethodChannelsGetAdminLog, func(ctx context.Context, layerRequest *tg.ChannelsGetAdminLogRequest) (any, error) {
		return r.onChannelsGetAdminLog(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsReadHistoryRequest](d, tlprofile.SemanticMethodChannelsReadHistory, func(ctx context.Context, layerRequest *tg.ChannelsReadHistoryRequest) (any, error) {
		return r.onChannelsReadHistory(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsGetMessagesRequest](d, tlprofile.SemanticMethodChannelsGetMessages, func(ctx context.Context, layerRequest *tg.ChannelsGetMessagesRequest) (any, error) {
		return r.onChannelsGetMessages(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsDeleteMessagesRequest](d, tlprofile.SemanticMethodChannelsDeleteMessages, func(ctx context.Context, layerRequest *tg.ChannelsDeleteMessagesRequest) (any, error) {
		return r.onChannelsDeleteMessages(ctx, layerRequest)
	})
	registerRPC[*tg.ChannelsDeleteHistoryRequest](d, tlprofile.SemanticMethodChannelsDeleteHistory, func(ctx context.Context, layerRequest *tg.ChannelsDeleteHistoryRequest) (any, error) {
		return r.onChannelsDeleteHistory(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesUpdatePinnedMessageRequest](d, tlprofile.SemanticMethodMessagesUpdatePinnedMessage, func(ctx context.Context, layerRequest *tg.MessagesUpdatePinnedMessageRequest) (any, error) {
		return r.onMessagesUpdatePinnedMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesUnpinAllMessagesRequest](d, tlprofile.SemanticMethodMessagesUnpinAllMessages, func(ctx context.Context, layerRequest *tg.MessagesUnpinAllMessagesRequest) (any, error) {
		return r.onMessagesUnpinAllMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesExportChatInviteRequest](d, tlprofile.SemanticMethodMessagesExportChatInvite, func(ctx context.Context, layerRequest *tg.MessagesExportChatInviteRequest) (any, error) {
		return r.onMessagesExportChatInvite(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesCheckChatInviteRequest](d, tlprofile.SemanticMethodMessagesCheckChatInvite, func(ctx context.Context, layerRequest *tg.MessagesCheckChatInviteRequest) (any, error) {
		return r.onMessagesCheckChatInvite(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesImportChatInviteRequest](d, tlprofile.SemanticMethodMessagesImportChatInvite, func(ctx context.Context, layerRequest *tg.MessagesImportChatInviteRequest) (any, error) {
		return r.onMessagesImportChatInvite(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetExportedChatInvitesRequest](d, tlprofile.SemanticMethodMessagesGetExportedChatInvites, func(ctx context.Context, layerRequest *tg.MessagesGetExportedChatInvitesRequest) (any, error) {
		return r.onMessagesGetExportedChatInvites(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetExportedChatInviteRequest](d, tlprofile.SemanticMethodMessagesGetExportedChatInvite, func(ctx context.Context, layerRequest *tg.MessagesGetExportedChatInviteRequest) (any, error) {
		return r.onMessagesGetExportedChatInvite(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditExportedChatInviteRequest](d, tlprofile.SemanticMethodMessagesEditExportedChatInvite, func(ctx context.Context, layerRequest *tg.MessagesEditExportedChatInviteRequest) (any, error) {
		return r.onMessagesEditExportedChatInvite(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteRevokedExportedChatInvitesRequest](d, tlprofile.SemanticMethodMessagesDeleteRevokedExportedChatInvites, func(ctx context.Context, layerRequest *tg.MessagesDeleteRevokedExportedChatInvitesRequest) (any, error) {
		return r.onMessagesDeleteRevokedExportedChatInvites(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteExportedChatInviteRequest](d, tlprofile.SemanticMethodMessagesDeleteExportedChatInvite, func(ctx context.Context, layerRequest *tg.MessagesDeleteExportedChatInviteRequest) (any, error) {
		return r.onMessagesDeleteExportedChatInvite(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetAdminsWithInvitesRequest](d, tlprofile.SemanticMethodMessagesGetAdminsWithInvites, func(ctx context.Context, layerRequest *tg.MessagesGetAdminsWithInvitesRequest) (any, error) {
		return r.onMessagesGetAdminsWithInvites(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.MessagesGetChatInviteImportersRequest](d, tlprofile.SemanticMethodMessagesGetChatInviteImporters, func(ctx context.Context, layerRequest *tg.MessagesGetChatInviteImportersRequest) (any, error) {
		return r.onMessagesGetChatInviteImporters(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesHideChatJoinRequestRequest](d, tlprofile.SemanticMethodMessagesHideChatJoinRequest, func(ctx context.Context, layerRequest *tg.MessagesHideChatJoinRequestRequest) (any, error) {
		return r.onMessagesHideChatJoinRequest(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesHideAllChatJoinRequestsRequest](d, tlprofile.SemanticMethodMessagesHideAllChatJoinRequests, func(ctx context.Context, layerRequest *tg.MessagesHideAllChatJoinRequestsRequest) (any, error) {
		return r.onMessagesHideAllChatJoinRequests(ctx, layerRequest)
	})
	registerRPC[*tg.UpdatesGetChannelDifferenceRequest](d, tlprofile.SemanticMethodUpdatesGetChannelDifference, func(ctx context.Context, layerRequest *tg.UpdatesGetChannelDifferenceRequest) (any, error) {
		return r.onUpdatesGetChannelDifference(ctx, layerRequest)
	})
}
