package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
	"unicode/utf8"
)

// registerMessages 注册 messages.* RPC handler。
func (r *Router) registerMessages(d *tlprofile.Dispatcher) {
	registerRPC[*tg.MessagesRequestURLAuthRequest](d, tlprofile.SemanticMethodMessagesRequestURLAuth, func(ctx context.Context, req *tg.MessagesRequestURLAuthRequest) (any, error) {
		return r.onMessagesRequestURLAuth(ctx, req)
	})
	registerRPC[*tg.MessagesAcceptURLAuthRequest](d, tlprofile.SemanticMethodMessagesAcceptURLAuth, func(ctx context.Context, req *tg.MessagesAcceptURLAuthRequest) (any, error) {
		return r.onMessagesAcceptURLAuth(ctx, req)
	})
	registerRPC[*tg.MessagesDeclineURLAuthRequest](d, tlprofile.SemanticMethodMessagesDeclineURLAuth, func(ctx context.Context, req *tg.MessagesDeclineURLAuthRequest) (any, error) {
		return r.onMessagesDeclineURLAuth(ctx, req.URL)
	})
	registerRPC[*tg.MessagesCheckURLAuthMatchCodeRequest](d, tlprofile.SemanticMethodMessagesCheckURLAuthMatchCode, func(ctx context.Context, req *tg.MessagesCheckURLAuthMatchCodeRequest) (any, error) {
		return r.onMessagesCheckURLAuthMatchCode(ctx, req.URL, req.MatchCode)
	})
	registerRPC[*tg.MessagesReceivedMessagesRequest](d, tlprofile.SemanticMethodMessagesReceivedMessages, func(ctx context.Context, layerRequest *tg.MessagesReceivedMessagesRequest) (any, error) {
		return r.onMessagesReceivedMessages(ctx, layerRequest.
			MaxID)
	})
	registerRPC[*tg.MessagesSetTypingRequest](d, tlprofile.SemanticMethodMessagesSetTyping, func(ctx context.Context, layerRequest *tg.MessagesSetTypingRequest) (any, error) {
		return r.onMessagesSetTyping(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSaveDraftRequest](d, tlprofile.SemanticMethodMessagesSaveDraft, func(ctx context.Context, layerRequest *tg.MessagesSaveDraftRequest) (any, error) {
		return r.onMessagesSaveDraft(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSaveDefaultSendAsRequest](d, tlprofile.SemanticMethodMessagesSaveDefaultSendAs, func(ctx context.Context, layerRequest *tg.MessagesSaveDefaultSendAsRequest) (any, error) {
		return r.onMessagesSaveDefaultSendAs(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetAllDraftsRequest](d, tlprofile.SemanticMethodMessagesGetAllDrafts, func(ctx context.Context, layerRequest *tg.MessagesGetAllDraftsRequest) (any, error) {
		return r.onMessagesGetAllDrafts(ctx)
	})
	registerRPC[*tg.MessagesClearAllDraftsRequest](d, tlprofile.SemanticMethodMessagesClearAllDrafts, func(ctx context.Context, layerRequest *tg.MessagesClearAllDraftsRequest) (any, error) {
		return r.onMessagesClearAllDrafts(ctx)
	})
	registerRPC[*tg.MessagesGetAllStickersRequest](d, tlprofile.SemanticMethodMessagesGetAllStickers, func(ctx context.Context, layerRequest *tg.MessagesGetAllStickersRequest) (any, error) {
		return r.onMessagesGetAllStickers(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetEmojiStickersRequest](d, tlprofile.SemanticMethodMessagesGetEmojiStickers, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiStickersRequest) (any, error) {
		return r.onMessagesGetEmojiStickers(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetMaskStickersRequest](d, tlprofile.SemanticMethodMessagesGetMaskStickers, func(ctx context.Context, layerRequest *tg.MessagesGetMaskStickersRequest) (any, error) {
		return r.onMessagesGetMaskStickers(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetFeaturedStickersRequest](d, tlprofile.SemanticMethodMessagesGetFeaturedStickers, func(ctx context.Context, layerRequest *tg.MessagesGetFeaturedStickersRequest) (any, error) {
		return r.onMessagesGetFeaturedStickers(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetFeaturedEmojiStickersRequest](d, tlprofile.SemanticMethodMessagesGetFeaturedEmojiStickers, func(ctx context.Context, layerRequest *tg.MessagesGetFeaturedEmojiStickersRequest) (any, error) {
		return r.onMessagesGetFeaturedEmojiStickers(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetOldFeaturedStickersRequest](d, tlprofile.SemanticMethodMessagesGetOldFeaturedStickers, func(ctx context.Context, layerRequest *tg.MessagesGetOldFeaturedStickersRequest) (any, error) {
		return r.onMessagesGetOldFeaturedStickers(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetRecentStickersRequest](d, tlprofile.SemanticMethodMessagesGetRecentStickers, func(ctx context.Context, layerRequest *tg.MessagesGetRecentStickersRequest) (any, error) {
		return r.onMessagesGetRecentStickers(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetFavedStickersRequest](d, tlprofile.SemanticMethodMessagesGetFavedStickers, func(ctx context.Context, layerRequest *tg.MessagesGetFavedStickersRequest) (any, error) {
		return r.onMessagesGetFavedStickers(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetSavedGifsRequest](d, tlprofile.SemanticMethodMessagesGetSavedGifs, func(ctx context.Context, layerRequest *tg.MessagesGetSavedGifsRequest) (any, error) {
		return r.onMessagesGetSavedGifs(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesFaveStickerRequest](d, tlprofile.SemanticMethodMessagesFaveSticker, func(ctx context.Context, layerRequest *tg.MessagesFaveStickerRequest) (any, error) {
		return r.onMessagesFaveSticker(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSaveRecentStickerRequest](d, tlprofile.SemanticMethodMessagesSaveRecentSticker, func(ctx context.Context, layerRequest *tg.MessagesSaveRecentStickerRequest) (any, error) {
		return r.onMessagesSaveRecentSticker(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSaveGifRequest](d, tlprofile.SemanticMethodMessagesSaveGif, func(ctx context.Context, layerRequest *tg.MessagesSaveGifRequest) (any, error) {
		return r.onMessagesSaveGif(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesClearRecentStickersRequest](d, tlprofile.SemanticMethodMessagesClearRecentStickers, func(ctx context.Context, layerRequest *tg.MessagesClearRecentStickersRequest) (any, error) {
		return r.onMessagesClearRecentStickers(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendMessageRequest](d, tlprofile.SemanticMethodMessagesSendMessage, func(ctx context.Context, layerRequest *tg.MessagesSendMessageRequest) (any, error) {
		return r.onMessagesSendMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesToggleSuggestedPostApprovalRequest](d, tlprofile.SemanticMethodMessagesToggleSuggestedPostApproval, func(ctx context.Context, layerRequest *tg.MessagesToggleSuggestedPostApprovalRequest) (any, error) {
		return r.onMessagesToggleSuggestedPostApproval(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesForwardMessagesRequest](d, tlprofile.SemanticMethodMessagesForwardMessages, func(ctx context.Context, layerRequest *tg.MessagesForwardMessagesRequest) (any, error) {
		return r.onMessagesForwardMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetDialogFiltersRequest](d, tlprofile.SemanticMethodMessagesGetDialogFilters, func(ctx context.Context, layerRequest *tg.MessagesGetDialogFiltersRequest) (any, error) {
		return r.onMessagesGetDialogFilters(ctx)
	})
	registerRPC[*tg.MessagesGetSuggestedDialogFiltersRequest](d, tlprofile.SemanticMethodMessagesGetSuggestedDialogFilters, func(ctx context.Context, layerRequest *tg.MessagesGetSuggestedDialogFiltersRequest) (any, error) {
		return tdesktop.SuggestedDialogFilters(), nil
	})
	registerRPC[*tg.MessagesUpdateDialogFilterRequest](d, tlprofile.SemanticMethodMessagesUpdateDialogFilter, func(ctx context.Context, layerRequest *tg.MessagesUpdateDialogFilterRequest) (any, error) {
		return r.onMessagesUpdateDialogFilter(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesUpdateDialogFiltersOrderRequest](d, tlprofile.SemanticMethodMessagesUpdateDialogFiltersOrder, func(ctx context.Context, layerRequest *tg.MessagesUpdateDialogFiltersOrderRequest) (any, error) {
		return r.onMessagesUpdateDialogFiltersOrder(ctx, layerRequest.
			Order)
	})
	registerRPC[*tg.MessagesToggleDialogFilterTagsRequest](d, tlprofile.SemanticMethodMessagesToggleDialogFilterTags, func(ctx context.Context, layerRequest *tg.MessagesToggleDialogFilterTagsRequest) (any, error) {
		return r.onMessagesToggleDialogFilterTags(ctx, layerRequest.
			Enabled)
	})
	registerRPC[*tg.MessagesGetSavedDialogsRequest](d, tlprofile.SemanticMethodMessagesGetSavedDialogs, func(ctx context.Context, layerRequest *tg.MessagesGetSavedDialogsRequest) (any, error) {
		return r.onMessagesGetSavedDialogs(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetPinnedSavedDialogsRequest](d, tlprofile.SemanticMethodMessagesGetPinnedSavedDialogs, func(ctx context.Context, layerRequest *tg.MessagesGetPinnedSavedDialogsRequest) (any, error) {
		return r.onMessagesGetPinnedSavedDialogs(ctx)
	})
	registerRPC[*tg.MessagesToggleSavedDialogPinRequest](d, tlprofile.SemanticMethodMessagesToggleSavedDialogPin, func(ctx context.Context, layerRequest *tg.MessagesToggleSavedDialogPinRequest) (any, error) {
		return r.onMessagesToggleSavedDialogPin(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReorderPinnedSavedDialogsRequest](d, tlprofile.SemanticMethodMessagesReorderPinnedSavedDialogs, func(ctx context.Context, layerRequest *tg.MessagesReorderPinnedSavedDialogsRequest) (any, error) {
		return r.onMessagesReorderPinnedSavedDialogs(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetSavedDialogsByIDRequest](d, tlprofile.SemanticMethodMessagesGetSavedDialogsByID, func(ctx context.Context, layerRequest *tg.MessagesGetSavedDialogsByIDRequest) (any, error) {
		return r.onMessagesGetSavedDialogsByID(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetSavedHistoryRequest](d, tlprofile.SemanticMethodMessagesGetSavedHistory, func(ctx context.Context, layerRequest *tg.MessagesGetSavedHistoryRequest) (any, error) {
		return r.onMessagesGetSavedHistory(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReadSavedHistoryRequest](d, tlprofile.SemanticMethodMessagesReadSavedHistory, func(ctx context.Context, layerRequest *tg.MessagesReadSavedHistoryRequest) (any, error) {
		return r.onMessagesReadSavedHistory(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteSavedHistoryRequest](d, tlprofile.SemanticMethodMessagesDeleteSavedHistory, func(ctx context.Context, layerRequest *tg.MessagesDeleteSavedHistoryRequest) (any, error) {
		return r.onMessagesDeleteSavedHistory(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetCommonChatsRequest](d, tlprofile.SemanticMethodMessagesGetCommonChats, func(ctx context.Context, layerRequest *tg.MessagesGetCommonChatsRequest) (any, error) {
		return r.onMessagesGetCommonChats(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetDefaultHistoryTTLRequest](d, tlprofile.SemanticMethodMessagesGetDefaultHistoryTTL, func(ctx context.Context, layerRequest *tg.MessagesGetDefaultHistoryTTLRequest) (any, error) {
		return r.onMessagesGetDefaultHistoryTTL(ctx)
	})
	registerRPC[*tg.MessagesSetHistoryTTLRequest](d, tlprofile.SemanticMethodMessagesSetHistoryTTL, func(ctx context.Context, layerRequest *tg.MessagesSetHistoryTTLRequest) (any, error) {
		return r.onMessagesSetHistoryTTL(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetDefaultHistoryTTLRequest](d, tlprofile.SemanticMethodMessagesSetDefaultHistoryTTL, func(ctx context.Context, layerRequest *tg.MessagesSetDefaultHistoryTTLRequest) (any, error) {
		return r.onMessagesSetDefaultHistoryTTL(ctx, layerRequest.
			Period)
	})
	registerRPC[*tg.MessagesGetSponsoredMessagesRequest](d, tlprofile.SemanticMethodMessagesGetSponsoredMessages, func(ctx context.Context, layerRequest *tg.MessagesGetSponsoredMessagesRequest) (any, error) {
		return r.onMessagesGetSponsoredMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetWebPagePreviewRequest](d, tlprofile.SemanticMethodMessagesGetWebPagePreview, func(ctx context.Context, layerRequest *tg.MessagesGetWebPagePreviewRequest) (any, error) {
		return r.onMessagesGetWebPagePreview(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesRequestWebViewRequest](d, tlprofile.SemanticMethodMessagesRequestWebView, func(ctx context.Context, layerRequest *tg.MessagesRequestWebViewRequest) (any, error) {
		return r.onMessagesRequestWebView(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesProlongWebViewRequest](d, tlprofile.SemanticMethodMessagesProlongWebView, func(ctx context.Context, layerRequest *tg.MessagesProlongWebViewRequest) (any, error) {
		return r.onMessagesProlongWebView(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendWebViewResultMessageRequest](d, tlprofile.SemanticMethodMessagesSendWebViewResultMessage, func(ctx context.Context, layerRequest *tg.MessagesSendWebViewResultMessageRequest) (any, error) {
		return r.onMessagesSendWebViewResultMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesRequestSimpleWebViewRequest](d, tlprofile.SemanticMethodMessagesRequestSimpleWebView, func(ctx context.Context, layerRequest *tg.MessagesRequestSimpleWebViewRequest) (any, error) {
		return r.onMessagesRequestSimpleWebView(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetBotAppRequest](d, tlprofile.SemanticMethodMessagesGetBotApp, func(ctx context.Context, layerRequest *tg.MessagesGetBotAppRequest) (any, error) {
		return r.onMessagesGetBotApp(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesRequestAppWebViewRequest](d, tlprofile.SemanticMethodMessagesRequestAppWebView, func(ctx context.Context, layerRequest *tg.MessagesRequestAppWebViewRequest) (any, error) {
		return r.onMessagesRequestAppWebView(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesRequestMainWebViewRequest](d, tlprofile.SemanticMethodMessagesRequestMainWebView, func(ctx context.Context, layerRequest *tg.MessagesRequestMainWebViewRequest) (any, error) {
		return r.onMessagesRequestMainWebView(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendWebViewDataRequest](d, tlprofile.SemanticMethodMessagesSendWebViewData, func(ctx context.Context, layerRequest *tg.MessagesSendWebViewDataRequest) (any, error) {
		return r.onMessagesSendWebViewData(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendBotRequestedPeerRequest](d, tlprofile.SemanticMethodMessagesSendBotRequestedPeer, func(ctx context.Context, layerRequest *tg.MessagesSendBotRequestedPeerRequest) (any, error) {
		return r.onMessagesSendBotRequestedPeer(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetPreparedInlineMessageRequest](d, tlprofile.SemanticMethodMessagesGetPreparedInlineMessage, func(ctx context.Context, layerRequest *tg.MessagesGetPreparedInlineMessageRequest) (any, error) {
		return r.onMessagesGetPreparedInlineMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetEmojiGameInfoRequest](d, tlprofile.SemanticMethodMessagesGetEmojiGameInfo, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiGameInfoRequest) (any, error) {
		return r.onMessagesGetEmojiGameInfo(ctx)
	})
	registerRPC[*tg.MessagesGetGameHighScoresRequest](d, tlprofile.SemanticMethodMessagesGetGameHighScores, func(ctx context.Context, layerRequest *tg.MessagesGetGameHighScoresRequest) (any, error) {
		return r.onMessagesGetGameHighScores(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetInlineGameHighScoresRequest](d, tlprofile.SemanticMethodMessagesGetInlineGameHighScores, func(ctx context.Context, layerRequest *tg.MessagesGetInlineGameHighScoresRequest) (any, error) {
		return r.onMessagesGetInlineGameHighScores(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetGameScoreRequest](d, tlprofile.SemanticMethodMessagesSetGameScore, func(ctx context.Context, layerRequest *tg.MessagesSetGameScoreRequest) (any, error) {
		return r.onMessagesSetGameScore(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetInlineGameScoreRequest](d, tlprofile.SemanticMethodMessagesSetInlineGameScore, func(ctx context.Context, layerRequest *tg.MessagesSetInlineGameScoreRequest) (any, error) {
		return r.onMessagesSetInlineGameScore(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesUploadMediaRequest](d, tlprofile.SemanticMethodMessagesUploadMedia, func(ctx context.Context, layerRequest *tg.MessagesUploadMediaRequest) (any, error) {
		return r.onMessagesUploadMedia(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendMediaRequest](d, tlprofile.SemanticMethodMessagesSendMedia, func(ctx context.Context, layerRequest *tg.MessagesSendMediaRequest) (any, error) {
		return r.onMessagesSendMedia(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendMultiMediaRequest](d, tlprofile.SemanticMethodMessagesSendMultiMedia, func(ctx context.Context, layerRequest *tg.MessagesSendMultiMediaRequest) (any, error) {
		return r.onMessagesSendMultiMedia(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReportSpamRequest](d, tlprofile.SemanticMethodMessagesReportSpam, func(ctx context.Context, layerRequest *tg.MessagesReportSpamRequest) (any, error) {
		return r.onMessagesReportSpam(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.MessagesReportRequest](d, tlprofile.SemanticMethodMessagesReport, func(ctx context.Context, layerRequest *tg.MessagesReportRequest) (any, error) {
		return r.onMessagesReport(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReportReactionRequest](d, tlprofile.SemanticMethodMessagesReportReaction, func(ctx context.Context, layerRequest *tg.MessagesReportReactionRequest) (any, error) {
		return r.onMessagesReportReaction(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReportMessagesDeliveryRequest](d, tlprofile.SemanticMethodMessagesReportMessagesDelivery, func(ctx context.Context, layerRequest *tg.MessagesReportMessagesDeliveryRequest) (any, error) {
		return r.onMessagesReportMessagesDelivery(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReportReadMetricsRequest](d, tlprofile.SemanticMethodMessagesReportReadMetrics, func(ctx context.Context, layerRequest *tg.MessagesReportReadMetricsRequest) (any, error) {
		return r.onMessagesReportReadMetrics(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReportMusicListenRequest](d, tlprofile.SemanticMethodMessagesReportMusicListen, func(ctx context.Context, layerRequest *tg.MessagesReportMusicListenRequest) (any, error) {
		return r.onMessagesReportMusicListen(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReportSponsoredMessageRequest](d, tlprofile.SemanticMethodMessagesReportSponsoredMessage, func(ctx context.Context, layerRequest *tg.MessagesReportSponsoredMessageRequest) (any, error) {
		return r.onMessagesReportSponsoredMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReadMessageContentsRequest](d, tlprofile.SemanticMethodMessagesReadMessageContents, func(ctx context.Context, layerRequest *tg.MessagesReadMessageContentsRequest) (any, error) {
		return r.onMessagesReadMessageContents(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.MessagesTranslateTextRequest](d, tlprofile.SemanticMethodMessagesTranslateText, func(ctx context.Context, layerRequest *tg.MessagesTranslateTextRequest) (any, error) {
		return r.onMessagesTranslateText(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesTogglePeerTranslationsRequest](d, tlprofile.SemanticMethodMessagesTogglePeerTranslations, func(ctx context.Context, layerRequest *tg.MessagesTogglePeerTranslationsRequest) (any, error) {
		return r.onMessagesTogglePeerTranslations(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetMessagesViewsRequest](d, tlprofile.SemanticMethodMessagesGetMessagesViews, func(ctx context.Context, layerRequest *tg.MessagesGetMessagesViewsRequest) (any, error) {
		return r.onMessagesGetMessagesViews(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetUnreadMentionsRequest](d, tlprofile.SemanticMethodMessagesGetUnreadMentions, func(ctx context.Context, layerRequest *tg.MessagesGetUnreadMentionsRequest) (any, error) {
		return r.onMessagesGetUnreadMentions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReadMentionsRequest](d, tlprofile.SemanticMethodMessagesReadMentions, func(ctx context.Context, layerRequest *tg.MessagesReadMentionsRequest) (any, error) {
		return r.onMessagesReadMentions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetSearchCountersRequest](d, tlprofile.SemanticMethodMessagesGetSearchCounters, func(ctx context.Context, layerRequest *tg.MessagesGetSearchCountersRequest) (any, error) {
		return r.onMessagesGetSearchCounters(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetRepliesRequest](d, tlprofile.SemanticMethodMessagesGetReplies, func(ctx context.Context, layerRequest *tg.MessagesGetRepliesRequest) (any, error) {
		return r.onMessagesGetReplies(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetDiscussionMessageRequest](d, tlprofile.SemanticMethodMessagesGetDiscussionMessage, func(ctx context.Context, layerRequest *tg.MessagesGetDiscussionMessageRequest) (any, error) {
		return r.onMessagesGetDiscussionMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReadDiscussionRequest](d, tlprofile.SemanticMethodMessagesReadDiscussion, func(ctx context.Context, layerRequest *tg.MessagesReadDiscussionRequest) (any, error) {
		return r.onMessagesReadDiscussion(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetForumTopicsRequest](d, tlprofile.SemanticMethodMessagesGetForumTopics, func(ctx context.Context, layerRequest *tg.MessagesGetForumTopicsRequest) (any, error) {
		return r.onMessagesGetForumTopics(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetForumTopicsByIDRequest](d, tlprofile.SemanticMethodMessagesGetForumTopicsByID, func(ctx context.Context, layerRequest *tg.MessagesGetForumTopicsByIDRequest) (any, error) {
		return r.onMessagesGetForumTopicsByID(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetOnlinesRequest](d, tlprofile.SemanticMethodMessagesGetOnlines, func(ctx context.Context, layerRequest *tg.MessagesGetOnlinesRequest) (any, error) {
		return r.onMessagesGetOnlines(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.MessagesGetAvailableReactionsRequest](d, tlprofile.SemanticMethodMessagesGetAvailableReactions, func(ctx context.Context, layerRequest *tg.MessagesGetAvailableReactionsRequest) (any, error) {
		return r.onMessagesGetAvailableReactions(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetAvailableEffectsRequest](d, tlprofile.SemanticMethodMessagesGetAvailableEffects, func(ctx context.Context, layerRequest *tg.MessagesGetAvailableEffectsRequest) (any, error) {
		return r.onMessagesGetAvailableEffects(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetStickersRequest](d, tlprofile.SemanticMethodMessagesGetStickers, func(ctx context.Context, layerRequest *tg.MessagesGetStickersRequest) (any, error) {
		return r.onMessagesGetStickers(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesInstallStickerSetRequest](d, tlprofile.SemanticMethodMessagesInstallStickerSet, func(ctx context.Context, layerRequest *tg.MessagesInstallStickerSetRequest) (any, error) {
		return r.onMessagesInstallStickerSet(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesUninstallStickerSetRequest](d, tlprofile.SemanticMethodMessagesUninstallStickerSet, func(ctx context.Context, layerRequest *tg.MessagesUninstallStickerSetRequest) (any, error) {
		return r.onMessagesUninstallStickerSet(ctx, layerRequest.
			Stickerset)
	})
	registerRPC[*tg.MessagesReorderStickerSetsRequest](d, tlprofile.SemanticMethodMessagesReorderStickerSets, func(ctx context.Context, layerRequest *tg.MessagesReorderStickerSetsRequest) (any, error) {
		return r.onMessagesReorderStickerSets(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesToggleStickerSetsRequest](d, tlprofile.SemanticMethodMessagesToggleStickerSets, func(ctx context.Context, layerRequest *tg.MessagesToggleStickerSetsRequest) (any, error) {
		return r.onMessagesToggleStickerSets(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetMyStickersRequest](d, tlprofile.SemanticMethodMessagesGetMyStickers, func(ctx context.Context, layerRequest *tg.MessagesGetMyStickersRequest) (any, error) {
		return r.onMessagesGetMyStickers(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetArchivedStickersRequest](d, tlprofile.SemanticMethodMessagesGetArchivedStickers, func(ctx context.Context, req *tg.MessagesGetArchivedStickersRequest) (any, error) {
		return &tg.MessagesArchivedStickers{
			Count: 0,
			Sets:  []tg.StickerSetCoveredClass{},
		}, nil
	})
	registerRPC[*tg.MessagesGetStickerSetRequest](d, tlprofile.SemanticMethodMessagesGetStickerSet, func(ctx context.Context, layerRequest *tg.MessagesGetStickerSetRequest) (any, error) {
		return r.onMessagesGetStickerSet(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetEmojiGroupsRequest](d, tlprofile.SemanticMethodMessagesGetEmojiGroups, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiGroupsRequest) (any, error) {
		hash := layerRequest.
			Hash
		_ = hash

		return tdesktop.EmojiGroups(hash), nil
	})
	registerRPC[*tg.MessagesGetEmojiStatusGroupsRequest](d, tlprofile.SemanticMethodMessagesGetEmojiStatusGroups, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiStatusGroupsRequest) (any, error) {
		hash := layerRequest.
			Hash
		_ = hash

		return tdesktop.EmojiStatusGroups(), nil
	})
	registerRPC[*tg.MessagesGetEmojiStickerGroupsRequest](d, tlprofile.SemanticMethodMessagesGetEmojiStickerGroups, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiStickerGroupsRequest) (any, error) {
		return r.onMessagesGetEmojiStickerGroups(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetEmojiProfilePhotoGroupsRequest](d, tlprofile.SemanticMethodMessagesGetEmojiProfilePhotoGroups, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiProfilePhotoGroupsRequest) (any, error) {
		hash := layerRequest.
			Hash
		_ = hash

		return tdesktop.EmojiProfilePhotoGroups(), nil
	})
	registerRPC[*tg.MessagesGetEmojiKeywordsRequest](d, tlprofile.SemanticMethodMessagesGetEmojiKeywords, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiKeywordsRequest) (any, error) {
		return r.onMessagesGetEmojiKeywords(ctx, layerRequest.
			LangCode)
	})
	registerRPC[*tg.MessagesGetEmojiKeywordsDifferenceRequest](d, tlprofile.SemanticMethodMessagesGetEmojiKeywordsDifference, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiKeywordsDifferenceRequest) (any, error) {
		return r.onMessagesGetEmojiKeywordsDifference(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetEmojiKeywordsLanguagesRequest](d, tlprofile.SemanticMethodMessagesGetEmojiKeywordsLanguages, func(ctx context.Context, layerRequest *tg.MessagesGetEmojiKeywordsLanguagesRequest) (any, error) {
		langcodes := layerRequest.
			LangCodes
		_ = langcodes

		return []tg.EmojiLanguage{}, nil
	})
	registerRPC[*tg.MessagesGetCustomEmojiDocumentsRequest](d, tlprofile.SemanticMethodMessagesGetCustomEmojiDocuments, func(ctx context.Context, layerRequest *tg.MessagesGetCustomEmojiDocumentsRequest) (any, error) {
		return r.onMessagesGetCustomEmojiDocuments(ctx, layerRequest.
			DocumentID)
	})
	registerRPC[*tg.MessagesGetAttachedStickersRequest](d, tlprofile.SemanticMethodMessagesGetAttachedStickers, func(ctx context.Context, layerRequest *tg.MessagesGetAttachedStickersRequest) (any, error) {
		return r.onMessagesGetAttachedStickers(ctx, layerRequest.
			Media)
	})
	registerRPC[*tg.MessagesSearchStickerSetsRequest](d, tlprofile.SemanticMethodMessagesSearchStickerSets, func(ctx context.Context, layerRequest *tg.MessagesSearchStickerSetsRequest) (any, error) {
		return r.onMessagesSearchStickerSets(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSearchStickersRequest](d, tlprofile.SemanticMethodMessagesSearchStickers, func(ctx context.Context, layerRequest *tg.MessagesSearchStickersRequest) (any, error) {
		return r.onMessagesSearchStickers(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetAttachMenuBotsRequest](d, tlprofile.SemanticMethodMessagesGetAttachMenuBots, func(ctx context.Context, layerRequest *tg.MessagesGetAttachMenuBotsRequest) (any, error) {
		return r.onMessagesGetAttachMenuBots(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesGetAttachMenuBotRequest](d, tlprofile.SemanticMethodMessagesGetAttachMenuBot, func(ctx context.Context, layerRequest *tg.MessagesGetAttachMenuBotRequest) (any, error) {
		return r.onMessagesGetAttachMenuBot(ctx, layerRequest.
			Bot)
	})
	registerRPC[*tg.MessagesToggleBotInAttachMenuRequest](d, tlprofile.SemanticMethodMessagesToggleBotInAttachMenu, func(ctx context.Context, layerRequest *tg.MessagesToggleBotInAttachMenuRequest) (any, error) {
		return r.onMessagesToggleBotInAttachMenu(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetQuickRepliesRequest](d, tlprofile.SemanticMethodMessagesGetQuickReplies, func(ctx context.Context, layerRequest *tg.MessagesGetQuickRepliesRequest) (any, error) {
		return r.onMessagesGetQuickReplies(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesCheckQuickReplyShortcutRequest](d, tlprofile.SemanticMethodMessagesCheckQuickReplyShortcut, func(ctx context.Context, layerRequest *tg.MessagesCheckQuickReplyShortcutRequest) (any, error) {
		return r.onMessagesCheckQuickReplyShortcut(ctx, layerRequest.
			Shortcut)
	})
	registerRPC[*tg.MessagesReorderQuickRepliesRequest](d, tlprofile.SemanticMethodMessagesReorderQuickReplies, func(ctx context.Context, layerRequest *tg.MessagesReorderQuickRepliesRequest) (any, error) {
		return r.onMessagesReorderQuickReplies(ctx, layerRequest.
			Order)
	})
	registerRPC[*tg.MessagesEditQuickReplyShortcutRequest](d, tlprofile.SemanticMethodMessagesEditQuickReplyShortcut, func(ctx context.Context, layerRequest *tg.MessagesEditQuickReplyShortcutRequest) (any, error) {
		return r.onMessagesEditQuickReplyShortcut(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteQuickReplyShortcutRequest](d, tlprofile.SemanticMethodMessagesDeleteQuickReplyShortcut, func(ctx context.Context, layerRequest *tg.MessagesDeleteQuickReplyShortcutRequest) (any, error) {
		return r.onMessagesDeleteQuickReplyShortcut(ctx, layerRequest.
			ShortcutID)
	})
	registerRPC[*tg.MessagesGetQuickReplyMessagesRequest](d, tlprofile.SemanticMethodMessagesGetQuickReplyMessages, func(ctx context.Context, layerRequest *tg.MessagesGetQuickReplyMessagesRequest) (any, error) {
		return r.onMessagesGetQuickReplyMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendQuickReplyMessagesRequest](d, tlprofile.SemanticMethodMessagesSendQuickReplyMessages, func(ctx context.Context, layerRequest *tg.MessagesSendQuickReplyMessagesRequest) (any, error) {
		return r.onMessagesSendQuickReplyMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteQuickReplyMessagesRequest](d, tlprofile.SemanticMethodMessagesDeleteQuickReplyMessages, func(ctx context.Context, layerRequest *tg.MessagesDeleteQuickReplyMessagesRequest) (any, error) {
		return r.onMessagesDeleteQuickReplyMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetWebPageRequest](d, tlprofile.SemanticMethodMessagesGetWebPage, func(ctx context.Context, layerRequest *tg.MessagesGetWebPageRequest) (any, error) {
		return r.onMessagesGetWebPage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetDialogsRequest](d, tlprofile.SemanticMethodMessagesGetDialogs, func(ctx context.Context, req *tg.MessagesGetDialogsRequest) (any, error) {
		if r.deps.Dialogs == nil {
			return &tg.MessagesDialogs{}, nil
		}
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		filter, err := r.dialogFilterFromRequest(ctx, userID, req)
		if err != nil {
			return nil, err
		}
		if filter.Hash != 0 && r.deps.Communities == nil {
			hashCheck, err := r.deps.Dialogs.GetDialogsHash(ctx, userID, filter)
			if err != nil {
				return nil, internalErr()
			}
			if hashCheck.Known && hashCheck.Matched {
				return &tg.MessagesDialogsNotModified{Count: hashCheck.Count}, nil
			}
		}
		list, err := r.deps.Dialogs.GetDialogs(ctx, userID, filter)
		if err != nil {
			return nil, internalErr()
		}
		list, err = r.withCommunityDialogList(ctx, userID, filter, list)
		if err != nil {
			return nil, communityErr(err)
		}
		if ClientTypeFrom(ctx) == ClientTypeTDesktop && tdesktop.ShouldMergePinnedIntoInitialDialogs(filter) {
			pinned, err := r.pinnedDialogsList(ctx, userID, domain.DialogMainFolderID)
			if err != nil {
				return nil, internalErr()
			}
			list = tdesktop.MergeInitialDialogsWithPinned(list, pinned)
		}
		if filter.Hash != 0 && r.deps.Communities == nil && list.Hash == filter.Hash {
			return &tg.MessagesDialogsNotModified{Count: list.Count}, nil
		}
		return r.tgMessagesDialogs(ctx, userID, r.withDialogListPresence(ctx, userID, list)), nil
	})
	registerRPC[*tg.MessagesGetPinnedDialogsRequest](d, tlprofile.SemanticMethodMessagesGetPinnedDialogs, func(ctx context.Context, layerRequest *tg.MessagesGetPinnedDialogsRequest) (any, error) {
		folderID := layerRequest.
			FolderID
		_ = folderID

		id, _ := AuthKeyIDFrom(ctx)
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		list, err := r.pinnedDialogsList(ctx, userID, folderID)
		if err != nil {
			return nil, internalErr()
		}
		st := domain.UpdateState{Date: int(r.clock.Now().Unix())}
		if r.deps.Updates != nil {
			var err error
			st, err = r.deps.Updates.GetState(ctx, id, userID)
			if err != nil {
				return nil, internalErr()
			}
		}
		return r.tgPeerDialogs(ctx, userID, r.withDialogListPresence(ctx, userID, list), st), nil
	})
	registerRPC[*tg.MessagesGetPeerDialogsRequest](d, tlprofile.SemanticMethodMessagesGetPeerDialogs, func(ctx context.Context, layerRequest *tg.MessagesGetPeerDialogsRequest) (any, error) {
		peers := layerRequest.
			Peers
		_ = peers

		id, _ := AuthKeyIDFrom(ctx)
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		domainPeers, err := r.dialogPeersFromInput(ctx, userID, peers)
		if err != nil {
			return nil, err
		}
		channelIDs := make([]int64, 0, len(domainPeers))
		for _, peer := range domainPeers {
			if peer.Type == domain.PeerTypeChannel {
				channelIDs = append(channelIDs, peer.ID)
			}
		}
		if err := r.checkFrozenChannelParticipants(ctx, userID, channelIDs...); err != nil {
			return nil, err
		}
		// difference 类 catch-up FLOOD_WAIT（设计 Phase 2 / §10.3）：DrKLO 收 nudge 对未加载频道
		// 走 loadUnknownChannel→getPeerDialogs，限速须同时覆盖它（不止 getChannelDifference）。
		// 冻结账号的 guest/non-member 必须先返回 FROZEN_PARTICIPANT_MISSING，拒绝路径不能
		// 消耗限流额度或产生其它可变状态。
		if err := r.checkCatchupRateLimit(ctx, userID, peerDialogsRateLimitKeyPrefix); err != nil {
			return nil, err
		}
		regularPeers := make([]domain.Peer, 0, len(domainPeers))
		communityIDs := make([]int64, 0)
		for _, peer := range domainPeers {
			if peer.Type == domain.PeerTypeCommunity {
				communityIDs = append(communityIDs, peer.ID)
			} else {
				regularPeers = append(regularPeers, peer)
			}
		}
		var list domain.DialogList
		if len(regularPeers) > 0 && r.deps.Dialogs != nil {
			var err error
			list, err = r.deps.Dialogs.GetPeerDialogs(ctx, userID, regularPeers)
			if err != nil {
				return nil, internalErr()
			}
		}
		if len(communityIDs) > 0 && r.deps.Communities != nil {
			views, err := r.deps.Communities.GetMany(ctx, userID, communityIDs)
			if err != nil {
				return nil, communityErr(err)
			}
			list.Communities = append(list.Communities, views...)
			list.Count += len(views)
		}
		st := domain.UpdateState{Date: int(r.clock.Now().Unix())}
		if r.deps.Updates != nil {
			var err error
			st, err = r.deps.Updates.GetState(ctx, id, userID)
			if err != nil {
				return nil, internalErr()
			}
		}
		r.trackChannelInterest(ctx, userID, channelIDsFromDialogs(list)...)
		return r.tgPeerDialogs(ctx, userID, r.withDialogListPresence(ctx, userID, list), st), nil
	})
	registerRPC[*tg.MessagesGetPeerSettingsRequest](d, tlprofile.SemanticMethodMessagesGetPeerSettings, func(ctx context.Context, layerRequest *tg.MessagesGetPeerSettingsRequest) (any, error) {
		return r.onMessagesGetPeerSettings(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.MessagesToggleDialogPinRequest](d, tlprofile.SemanticMethodMessagesToggleDialogPin, func(ctx context.Context, layerRequest *tg.MessagesToggleDialogPinRequest) (any, error) {
		return r.onMessagesToggleDialogPin(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReorderPinnedDialogsRequest](d, tlprofile.SemanticMethodMessagesReorderPinnedDialogs, func(ctx context.Context, layerRequest *tg.MessagesReorderPinnedDialogsRequest) (any, error) {
		return r.onMessagesReorderPinnedDialogs(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesMarkDialogUnreadRequest](d, tlprofile.SemanticMethodMessagesMarkDialogUnread, func(ctx context.Context, layerRequest *tg.MessagesMarkDialogUnreadRequest) (any, error) {
		return r.onMessagesMarkDialogUnread(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetDialogUnreadMarksRequest](d, tlprofile.SemanticMethodMessagesGetDialogUnreadMarks, func(ctx context.Context, layerRequest *tg.MessagesGetDialogUnreadMarksRequest) (any, error) {
		return r.onMessagesGetDialogUnreadMarks(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesHidePeerSettingsBarRequest](d, tlprofile.SemanticMethodMessagesHidePeerSettingsBar, func(ctx context.Context, layerRequest *tg.MessagesHidePeerSettingsBarRequest) (any, error) {
		return r.onMessagesHidePeerSettingsBar(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.MessagesGetMessageEditDataRequest](d, tlprofile.SemanticMethodMessagesGetMessageEditData, func(ctx context.Context, layerRequest *tg.MessagesGetMessageEditDataRequest) (any, error) {
		return r.onMessagesGetMessageEditData(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditMessageRequest](d, tlprofile.SemanticMethodMessagesEditMessage, func(ctx context.Context, layerRequest *tg.MessagesEditMessageRequest) (any, error) {
		return r.onMessagesEditMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetOutboxReadDateRequest](d, tlprofile.SemanticMethodMessagesGetOutboxReadDate, func(ctx context.Context, layerRequest *tg.MessagesGetOutboxReadDateRequest) (any, error) {
		return r.onMessagesGetOutboxReadDate(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetMessageReadParticipantsRequest](d, tlprofile.SemanticMethodMessagesGetMessageReadParticipants, func(ctx context.Context, layerRequest *tg.MessagesGetMessageReadParticipantsRequest) (any, error) {
		return r.onMessagesGetMessageReadParticipants(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteMessagesRequest](d, tlprofile.SemanticMethodMessagesDeleteMessages, func(ctx context.Context, layerRequest *tg.MessagesDeleteMessagesRequest) (any, error) {
		return r.onMessagesDeleteMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteHistoryRequest](d, tlprofile.SemanticMethodMessagesDeleteHistory, func(ctx context.Context, layerRequest *tg.MessagesDeleteHistoryRequest) (any, error) {
		return r.onMessagesDeleteHistory(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetMessagesRequest](d, tlprofile.SemanticMethodMessagesGetMessages, func(ctx context.Context, layerRequest *tg.MessagesGetMessagesRequest) (any, error) {
		return r.onMessagesGetMessages(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.MessagesGetRichMessageRequest](d, tlprofile.SemanticMethodMessagesGetRichMessage, func(ctx context.Context, layerRequest *tg.MessagesGetRichMessageRequest) (any, error) {
		return r.onMessagesGetRichMessage(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetHistoryRequest](d, tlprofile.SemanticMethodMessagesGetHistory, func(ctx context.Context, req *tg.MessagesGetHistoryRequest) (any, error) {
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		filter, ok := r.messageFilterFromHistoryRequest(userID, req)
		if !ok {
			return messagesNotModifiedOrEmpty(req.Hash), nil
		}
		if filter.Peer.Type == domain.PeerTypeChannel {
			if r.deps.Channels == nil {
				return messagesNotModifiedOrEmpty(req.Hash), nil
			}
			if err := r.validateInputPeerChannelAccess(ctx, userID, req.Peer, filter.Peer.ID); err != nil {
				return nil, err
			}
			if err := r.checkFrozenChannelParticipants(ctx, userID, filter.Peer.ID); err != nil {
				return nil, err
			}
			if isLegacyInputPeerChat(req.Peer) {
				return &tg.MessagesMessages{}, nil
			}
			history, err := r.deps.Channels.GetHistory(ctx, userID, domain.ChannelHistoryFilter{
				ChannelID:  filter.Peer.ID,
				OffsetID:   filter.OffsetID,
				OffsetDate: filter.OffsetDate,
				AddOffset:  filter.AddOffset,
				Limit:      filter.Limit,
				MaxID:      filter.MaxID,
				MinID:      filter.MinID,
				Hash:       filter.Hash,
			})
			if err != nil {
				return nil, channelInvalidErr(err)
			}
			history = r.enrichChannelHistory(ctx, userID, history)
			r.trackChannelInterest(ctx, userID, filter.Peer.ID)
			if filter.Hash != 0 && history.Hash == filter.Hash {
				return &tg.MessagesMessagesNotModified{Count: history.Count}, nil
			}
			return r.tgChannelHistoryMessages(ctx, userID, history), nil
		}
		r.clearChannelInterest(ctx, userID)
		if r.deps.Messages == nil {
			return messagesNotModifiedOrEmpty(req.Hash), nil
		}
		list, err := r.deps.Messages.GetHistory(ctx, userID, filter)
		if err != nil {
			return nil, internalErr()
		}
		if filter.Hash != 0 && list.Hash == filter.Hash {
			return &tg.MessagesMessagesNotModified{Count: list.Count}, nil
		}
		return r.tgMessagesMessages(ctx, userID, r.enrichMessageList(ctx, userID, list)), nil
	})
	registerRPC[*tg.MessagesGetRecentLocationsRequest](d, tlprofile.SemanticMethodMessagesGetRecentLocations, func(ctx context.Context, layerRequest *tg.MessagesGetRecentLocationsRequest) (any, error) {
		return r.onMessagesGetRecentLocations(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReadHistoryRequest](d, tlprofile.SemanticMethodMessagesReadHistory, func(ctx context.Context, req *tg.MessagesReadHistoryRequest) (any, error) {
		id, _ := AuthKeyIDFrom(ctx)
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		peer, peerErr := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
		if peerErr == nil && peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
			read, err := r.deps.Channels.ReadHistory(ctx, userID, domain.ReadChannelHistoryRequest{
				UserID:    userID,
				ChannelID: peer.ID,
				MaxID:     req.MaxID,
				Date:      int(r.clock.Now().Unix()),
			})
			if err != nil {
				return nil, channelInvalidErr(err)
			}
			event, err := r.recordChannelReadInbox(ctx, userID, read)
			if err != nil {
				return nil, err
			}
			r.pushChannelReadOutboxUpdates(ctx, read.ChannelID, read.OutboxUpdates)
			r.advanceForumGeneralReadAfterChannelRead(ctx, userID, read)
			if event.Pts != 0 {
				return &tg.MessagesAffectedMessages{Pts: event.Pts, PtsCount: event.PtsCount}, nil
			}
			return r.affectedMessages(ctx, id, userID)
		}
		if peerErr != nil {
			return nil, peerErr
		}
		if r.deps.Messages != nil {
			sessionID, _ := SessionIDFrom(ctx)
			read, err := r.deps.Messages.ReadHistory(ctx, userID, domain.ReadHistoryRequest{
				OwnerUserID:     userID,
				Peer:            peer,
				MaxID:           req.MaxID,
				Date:            int(r.clock.Now().Unix()),
				OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
				OriginSessionID: sessionID,
			})
			if err != nil {
				return nil, internalErr()
			}
			if read.Changed && read.InboxEvent.Pts != 0 {
				r.pushCurrentReadHistoryEvent(ctx, read.InboxEvent)
				r.pushReadHistoryEvent(ctx, read.OwnerUserID, read.InboxEvent)
				if read.OutboxChanged && read.OutboxEvent.Pts != 0 {
					r.pushReadHistoryEvent(ctx, read.OutboxUserID, read.OutboxEvent)
				}
				return &tg.MessagesAffectedMessages{Pts: read.InboxEvent.Pts, PtsCount: read.InboxEvent.PtsCount}, nil
			}
		}
		return r.affectedMessages(ctx, id, userID)
	})
	registerRPC[*tg.MessagesSearchRequest](d, tlprofile.SemanticMethodMessagesSearch, func(ctx context.Context, req *tg.MessagesSearchRequest) (any, error) {
		if utf8.RuneCountInString(req.Q) > maxMessageSearchQLength {
			return nil, limitInvalidErr()
		}
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		filter := r.messageFilterFromSearchRequest(userID, req)
		if filter.HasPeer && filter.Peer.Type == domain.PeerTypeChannel {
			if r.deps.Channels == nil {
				return messagesNotModifiedOrEmpty(req.Hash), nil
			}
			if isLegacyInputPeerChat(req.Peer) {
				return &tg.MessagesMessages{}, nil
			}
			if messagesSearchFilterChatPhotos(req.Filter) {
				view, err := r.resolveInputPeerChannelView(ctx, userID, req.Peer, filter.Peer.ID)
				if err != nil {
					return nil, channelInvalidErr(err)
				}
				out := &tg.MessagesChannelMessages{
					Pts:      view.Channel.Pts,
					Count:    0,
					Messages: []tg.MessageClass{},
					Chats:    []tg.ChatClass{tgChannelChatForView(userID, view)},
					Users:    []tg.UserClass{},
				}
				r.applyStoryMaxIDsToMessages(ctx, userID, out)
				return out, nil
			}
			if searchFilterNeedsMediaStore(req.Filter) {
				if mediaSearchCountOnlyRequest(req) {
					view, err := r.resolveInputPeerChannelView(ctx, userID, req.Peer, filter.Peer.ID)
					if err != nil {
						return nil, channelInvalidErr(err)
					}
					counts, err := r.mediaCountsForPeer(ctx, userID, filter.Peer)
					if err != nil {
						return nil, channelInvalidErr(err)
					}
					out := &tg.MessagesChannelMessages{
						Pts:      view.Channel.Pts,
						Count:    counts.CountAny(mediaCategoriesForFilter(req.Filter)),
						Messages: []tg.MessageClass{},
						Chats:    []tg.ChatClass{tgChannelChatForView(userID, view)},
						Users:    []tg.UserClass{},
					}
					r.applyStoryMaxIDsToMessages(ctx, userID, out)
					return out, nil
				}
				if err := r.validateInputPeerChannelAccess(ctx, userID, req.Peer, filter.Peer.ID); err != nil {
					return nil, err
				}
				categories := mediaCategoriesForFilter(req.Filter)
				mediaReq := domain.MediaSearchRequest{
					Categories: categories,
					OffsetID:   req.OffsetID,
					AddOffset:  domain.ClampMessageHistoryAddOffset(req.AddOffset),
					Limit:      req.Limit,
					MaxID:      req.MaxID,
					MinID:      req.MinID,
				}
				if mediaSearchCanReusePeerWideCount(req) {
					counts, err := r.mediaCountsForPeer(ctx, userID, filter.Peer)
					if err != nil {
						return nil, channelInvalidErr(err)
					}
					mediaReq.KnownCount = counts.CountAny(categories)
					mediaReq.HasKnownCount = true
				}
				history, err := r.deps.Channels.SearchChannelMedia(ctx, userID, filter.Peer.ID, mediaReq)
				if err != nil {
					return nil, channelInvalidErr(err)
				}
				history = r.enrichChannelHistory(ctx, userID, history)
				return r.tgChannelHistoryMessages(ctx, userID, history), nil
			}
			if err := r.validateInputPeerChannelAccess(ctx, userID, req.Peer, filter.Peer.ID); err != nil {
				return nil, err
			}
			chFilter, ok := r.channelHistoryFilterFromSearchRequest(userID, req, filter.Peer.ID)
			if !ok {
				return nil, peerIDInvalidErr()
			}
			history, err := r.deps.Channels.GetHistory(ctx, userID, chFilter)
			if err != nil {
				return nil, channelInvalidErr(err)
			}
			history = r.enrichChannelHistory(ctx, userID, history)
			if chFilter.Hash != 0 && history.Hash == chFilter.Hash {
				return &tg.MessagesMessagesNotModified{Count: history.Count}, nil
			}
			return r.tgChannelHistoryMessages(ctx, userID, history), nil
		}
		if _, ok := req.Filter.(*tg.InputMessagesFilterPinned); ok {
			if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
				return nil, err
			}
			if r.deps.Messages == nil || !filter.HasPeer || filter.Peer.Type != domain.PeerTypeUser {
				return &tg.MessagesMessages{}, nil
			}
			// 私聊置顶列表：客户端置顶栏经 filterPinned 分页拉取，必须
			// 带总数（NotModified 哈希不参与该过滤器）。
			filter.PinnedOnly = true
			filter.NeedTotalCount = true
			filter.Hash = 0
			list, err := r.deps.Messages.Search(ctx, userID, filter)
			if err != nil {
				return nil, internalErr()
			}
			return r.tgMessagesMessages(ctx, userID, r.enrichMessageList(ctx, userID, list)), nil
		}
		if messagesSearchFilterChatPhotos(req.Filter) {
			if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
				return nil, err
			}
			return r.tgMessagesMessages(ctx, userID, domain.MessageList{}), nil
		}
		if r.deps.Messages == nil {
			return messagesNotModifiedOrEmpty(req.Hash), nil
		}
		if searchFilterNeedsMediaStore(req.Filter) {
			peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
			if err != nil {
				return nil, err
			}
			if peer.Type != domain.PeerTypeUser {
				return &tg.MessagesMessages{}, nil
			}
			if mediaSearchCountOnlyRequest(req) {
				counts, err := r.mediaCountsForPeer(ctx, userID, peer)
				if err != nil {
					return nil, internalErr()
				}
				return r.tgMessagesMessages(ctx, userID, domain.MessageList{
					Count: counts.CountAny(mediaCategoriesForFilter(req.Filter)),
				}), nil
			}
			categories := mediaCategoriesForFilter(req.Filter)
			mediaReq := domain.MediaSearchRequest{
				Categories: categories,
				OffsetID:   req.OffsetID,
				AddOffset:  domain.ClampMessageHistoryAddOffset(req.AddOffset),
				Limit:      req.Limit,
				MaxID:      req.MaxID,
				MinID:      req.MinID,
			}
			if mediaSearchCanReusePeerWideCount(req) {
				counts, err := r.mediaCountsForPeer(ctx, userID, peer)
				if err != nil {
					return nil, internalErr()
				}
				mediaReq.KnownCount = counts.CountAny(categories)
				mediaReq.HasKnownCount = true
			}
			list, err := r.deps.Messages.SearchPrivateMedia(ctx, userID, peer.ID, mediaReq)
			if err != nil {
				return nil, internalErr()
			}
			return r.tgMessagesMessages(ctx, userID, r.enrichMessageList(ctx, userID, list)), nil
		}
		list, err := r.deps.Messages.Search(ctx, userID, filter)
		if err != nil {
			return nil, internalErr()
		}
		if filter.Hash != 0 && list.Hash == filter.Hash {
			return &tg.MessagesMessagesNotModified{Count: list.Count}, nil
		}
		return r.tgMessagesMessages(ctx, userID, r.enrichMessageList(ctx, userID, list)), nil
	})
	registerRPC[*tg.MessagesSearchGlobalRequest](d, tlprofile.SemanticMethodMessagesSearchGlobal, func(ctx context.Context, layerRequest *tg.MessagesSearchGlobalRequest) (any, error) {
		return r.onMessagesSearchGlobal(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetSearchResultsCalendarRequest](d, tlprofile.SemanticMethodMessagesGetSearchResultsCalendar, func(ctx context.Context, layerRequest *tg.MessagesGetSearchResultsCalendarRequest) (any, error) {
		return r.onMessagesGetSearchResultsCalendar(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetSearchResultsPositionsRequest](d, tlprofile.SemanticMethodMessagesGetSearchResultsPositions, func(ctx context.Context, layerRequest *tg.MessagesGetSearchResultsPositionsRequest) (any, error) {
		return r.onMessagesGetSearchResultsPositions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendReactionRequest](d, tlprofile.SemanticMethodMessagesSendReaction, func(ctx context.Context, layerRequest *tg.MessagesSendReactionRequest) (any, error) {

		// 语音转文字无识别后端：注册为显式失败（TRANSCRIPTION_FAILED），premium
		// 客户端点击转录按钮得到优雅失败提示，而不是 NOT_IMPLEMENTED trace。
		return r.onMessagesSendReaction(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesComposeMessageWithAIRequest](d, tlprofile.SemanticMethodMessagesComposeMessageWithAI, func(ctx context.Context, layerRequest *tg.MessagesComposeMessageWithAIRequest) (any, error) {
		return r.onMessagesComposeMessageWithAI(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesTranscribeAudioRequest](d, tlprofile.SemanticMethodMessagesTranscribeAudio, func(ctx context.Context, req *tg.MessagesTranscribeAudioRequest) (any, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	})
	registerRPC[*tg.MessagesGetMessagesReactionsRequest](d, tlprofile.SemanticMethodMessagesGetMessagesReactions, func(ctx context.Context, layerRequest *tg.MessagesGetMessagesReactionsRequest) (any, error) {
		return r.onMessagesGetMessagesReactions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetMessageReactionsListRequest](d, tlprofile.SemanticMethodMessagesGetMessageReactionsList, func(ctx context.Context, layerRequest *tg.MessagesGetMessageReactionsListRequest) (any, error) {
		return r.onMessagesGetMessageReactionsList(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSetDefaultReactionRequest](d, tlprofile.SemanticMethodMessagesSetDefaultReaction, func(ctx context.Context, layerRequest *tg.MessagesSetDefaultReactionRequest) (any, error) {
		return r.onMessagesSetDefaultReaction(ctx, layerRequest.
			Reaction)
	})
	registerRPC[*tg.MessagesGetPaidReactionPrivacyRequest](d, tlprofile.SemanticMethodMessagesGetPaidReactionPrivacy, func(ctx context.Context, layerRequest *tg.MessagesGetPaidReactionPrivacyRequest) (any, error) {
		return r.onMessagesGetPaidReactionPrivacy(ctx)
	})
	registerRPC[*tg.MessagesTogglePaidReactionPrivacyRequest](d, tlprofile.SemanticMethodMessagesTogglePaidReactionPrivacy, func(ctx context.Context, layerRequest *tg.MessagesTogglePaidReactionPrivacyRequest) (any, error) {
		return r.onMessagesTogglePaidReactionPrivacy(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendPaidReactionRequest](d, tlprofile.SemanticMethodMessagesSendPaidReaction, func(ctx context.Context, layerRequest *tg.MessagesSendPaidReactionRequest) (any, error) {
		return r.onMessagesSendPaidReaction(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteParticipantReactionsRequest](d, tlprofile.SemanticMethodMessagesDeleteParticipantReactions, func(ctx context.Context, layerRequest *tg.MessagesDeleteParticipantReactionsRequest) (any, error) {
		return r.onMessagesDeleteParticipantReactions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteParticipantReactionRequest](d, tlprofile.SemanticMethodMessagesDeleteParticipantReaction, func(ctx context.Context, layerRequest *tg.MessagesDeleteParticipantReactionRequest) (any, error) {
		return r.onMessagesDeleteParticipantReaction(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetUnreadReactionsRequest](d, tlprofile.SemanticMethodMessagesGetUnreadReactions, func(ctx context.Context, layerRequest *tg.MessagesGetUnreadReactionsRequest) (any, error) {
		return r.onMessagesGetUnreadReactions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReadReactionsRequest](d, tlprofile.SemanticMethodMessagesReadReactions, func(ctx context.Context, layerRequest *tg.MessagesReadReactionsRequest) (any, error) {
		return r.onMessagesReadReactions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetTopReactionsRequest](d, tlprofile.SemanticMethodMessagesGetTopReactions, func(ctx context.Context, layerRequest *tg.MessagesGetTopReactionsRequest) (any, error) {
		return r.onMessagesGetTopReactions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetRecentReactionsRequest](d, tlprofile.SemanticMethodMessagesGetRecentReactions, func(ctx context.Context, layerRequest *tg.MessagesGetRecentReactionsRequest) (any, error) {
		return r.onMessagesGetRecentReactions(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesClearRecentReactionsRequest](d, tlprofile.SemanticMethodMessagesClearRecentReactions, func(ctx context.Context, layerRequest *tg.MessagesClearRecentReactionsRequest) (any, error) {
		return r.onMessagesClearRecentReactions(ctx)
	})
	registerRPC[*tg.MessagesGetSavedReactionTagsRequest](d, tlprofile.SemanticMethodMessagesGetSavedReactionTags, func(ctx context.Context, layerRequest *tg.MessagesGetSavedReactionTagsRequest) (any, error) {
		return r.onMessagesGetSavedReactionTags(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesUpdateSavedReactionTagRequest](d, tlprofile.SemanticMethodMessagesUpdateSavedReactionTag, func(ctx context.Context, layerRequest *tg.MessagesUpdateSavedReactionTagRequest) (any, error) {
		return r.onMessagesUpdateSavedReactionTag(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetDefaultTagReactionsRequest](d, tlprofile.SemanticMethodMessagesGetDefaultTagReactions, func(ctx context.Context, layerRequest *tg.MessagesGetDefaultTagReactionsRequest) (any, error) {
		return r.onMessagesGetDefaultTagReactions(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.MessagesSendVoteRequest](d, tlprofile.SemanticMethodMessagesSendVote, func(ctx context.Context, layerRequest *tg.MessagesSendVoteRequest) (any, error) {
		return r.onMessagesSendVote(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetPollResultsRequest](d, tlprofile.SemanticMethodMessagesGetPollResults, func(ctx context.Context, layerRequest *tg.MessagesGetPollResultsRequest) (any, error) {
		return r.onMessagesGetPollResults(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetPollVotesRequest](d, tlprofile.SemanticMethodMessagesGetPollVotes, func(ctx context.Context, layerRequest *tg.MessagesGetPollVotesRequest) (any, error) {
		return r.onMessagesGetPollVotes(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesAddPollAnswerRequest](d, tlprofile.SemanticMethodMessagesAddPollAnswer, func(ctx context.Context, layerRequest *tg.MessagesAddPollAnswerRequest) (any, error) {
		return r.onMessagesAddPollAnswer(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeletePollAnswerRequest](d, tlprofile.SemanticMethodMessagesDeletePollAnswer, func(ctx context.Context, layerRequest *tg.MessagesDeletePollAnswerRequest) (any, error) {
		return r.onMessagesDeletePollAnswer(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetUnreadPollVotesRequest](d, tlprofile.SemanticMethodMessagesGetUnreadPollVotes, func(ctx context.Context, layerRequest *tg.MessagesGetUnreadPollVotesRequest) (any, error) {
		return r.onMessagesGetUnreadPollVotes(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReadPollVotesRequest](d, tlprofile.SemanticMethodMessagesReadPollVotes, func(ctx context.Context, layerRequest *tg.MessagesReadPollVotesRequest) (any, error) {
		return r.onMessagesReadPollVotes(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesAppendTodoListRequest](d, tlprofile.SemanticMethodMessagesAppendTodoList, func(ctx context.Context, layerRequest *tg.MessagesAppendTodoListRequest) (any, error) {
		return r.onMessagesAppendTodoList(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesToggleTodoCompletedRequest](d, tlprofile.SemanticMethodMessagesToggleTodoCompleted, func(ctx context.Context, layerRequest *tg.MessagesToggleTodoCompletedRequest) (any, error) {
		return r.onMessagesToggleTodoCompleted(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetScheduledHistoryRequest](d, tlprofile.SemanticMethodMessagesGetScheduledHistory, func(ctx context.Context, layerRequest *tg.MessagesGetScheduledHistoryRequest) (any, error) {
		return r.onMessagesGetScheduledHistory(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesGetScheduledMessagesRequest](d, tlprofile.SemanticMethodMessagesGetScheduledMessages, func(ctx context.Context, layerRequest *tg.MessagesGetScheduledMessagesRequest) (any, error) {
		return r.onMessagesGetScheduledMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesSendScheduledMessagesRequest](d, tlprofile.SemanticMethodMessagesSendScheduledMessages, func(ctx context.Context, layerRequest *tg.MessagesSendScheduledMessagesRequest) (any, error) {
		return r.onMessagesSendScheduledMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteScheduledMessagesRequest](d, tlprofile.SemanticMethodMessagesDeleteScheduledMessages, func(ctx context.Context, layerRequest *tg.MessagesDeleteScheduledMessagesRequest) (any, error) {
		return r.onMessagesDeleteScheduledMessages(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesCreateForumTopicRequest](d, tlprofile.SemanticMethodMessagesCreateForumTopic, func(ctx context.Context, layerRequest *tg.MessagesCreateForumTopicRequest) (any, error) {
		return r.onMessagesCreateForumTopic(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesEditForumTopicRequest](d, tlprofile.SemanticMethodMessagesEditForumTopic, func(ctx context.Context, layerRequest *tg.MessagesEditForumTopicRequest) (any, error) {
		return r.onMessagesEditForumTopic(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesUpdatePinnedForumTopicRequest](d, tlprofile.SemanticMethodMessagesUpdatePinnedForumTopic, func(ctx context.Context, layerRequest *tg.MessagesUpdatePinnedForumTopicRequest) (any, error) {
		return r.onMessagesUpdatePinnedForumTopic(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesReorderPinnedForumTopicsRequest](d, tlprofile.SemanticMethodMessagesReorderPinnedForumTopics, func(ctx context.Context, layerRequest *tg.MessagesReorderPinnedForumTopicsRequest) (any, error) {
		return r.onMessagesReorderPinnedForumTopics(ctx, layerRequest)
	})
	registerRPC[*tg.MessagesDeleteTopicHistoryRequest](d, tlprofile.SemanticMethodMessagesDeleteTopicHistory, func(ctx context.Context, layerRequest *tg.MessagesDeleteTopicHistoryRequest) (any, error) {
		return r.onMessagesDeleteTopicHistory(ctx, layerRequest)
	})
}
