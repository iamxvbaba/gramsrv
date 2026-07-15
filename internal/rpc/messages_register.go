package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
	"unicode/utf8"
)

// registerMessages 注册 messages.* RPC handler。
func (r *Router) registerMessages(d *tg.ServerDispatcher) {
	d.OnMessagesReceivedMessages(r.onMessagesReceivedMessages)
	d.OnMessagesSetTyping(r.onMessagesSetTyping)
	d.OnMessagesSaveDraft(r.onMessagesSaveDraft)
	d.OnMessagesSaveDefaultSendAs(r.onMessagesSaveDefaultSendAs)
	d.OnMessagesGetAllDrafts(r.onMessagesGetAllDrafts)
	d.OnMessagesClearAllDrafts(r.onMessagesClearAllDrafts)
	d.OnMessagesGetAllStickers(r.onMessagesGetAllStickers)
	d.OnMessagesGetEmojiStickers(r.onMessagesGetEmojiStickers)
	d.OnMessagesGetMaskStickers(r.onMessagesGetMaskStickers)
	d.OnMessagesGetFeaturedStickers(r.onMessagesGetFeaturedStickers)
	d.OnMessagesGetFeaturedEmojiStickers(r.onMessagesGetFeaturedEmojiStickers)
	d.OnMessagesGetOldFeaturedStickers(r.onMessagesGetOldFeaturedStickers)
	d.OnMessagesGetRecentStickers(r.onMessagesGetRecentStickers)
	d.OnMessagesGetFavedStickers(r.onMessagesGetFavedStickers)
	d.OnMessagesGetSavedGifs(r.onMessagesGetSavedGifs)
	d.OnMessagesFaveSticker(r.onMessagesFaveSticker)
	d.OnMessagesSaveRecentSticker(r.onMessagesSaveRecentSticker)
	d.OnMessagesSaveGif(r.onMessagesSaveGif)
	d.OnMessagesClearRecentStickers(r.onMessagesClearRecentStickers)
	d.OnMessagesSendMessage(r.onMessagesSendMessage)
	d.OnMessagesForwardMessages(r.onMessagesForwardMessages)
	d.OnMessagesGetDialogFilters(r.onMessagesGetDialogFilters)
	d.OnMessagesGetSuggestedDialogFilters(func(ctx context.Context) ([]tg.DialogFilterSuggested, error) {
		return tdesktop.SuggestedDialogFilters(), nil
	})
	d.OnMessagesUpdateDialogFilter(r.onMessagesUpdateDialogFilter)
	d.OnMessagesUpdateDialogFiltersOrder(r.onMessagesUpdateDialogFiltersOrder)
	d.OnMessagesToggleDialogFilterTags(r.onMessagesToggleDialogFilterTags)
	d.OnMessagesGetSavedDialogs(r.onMessagesGetSavedDialogs)
	d.OnMessagesGetPinnedSavedDialogs(func(ctx context.Context) (tg.MessagesSavedDialogsClass, error) {
		return r.onMessagesGetPinnedSavedDialogs(ctx)
	})
	d.OnMessagesToggleSavedDialogPin(r.onMessagesToggleSavedDialogPin)
	d.OnMessagesReorderPinnedSavedDialogs(r.onMessagesReorderPinnedSavedDialogs)
	d.OnMessagesGetSavedDialogsByID(r.onMessagesGetSavedDialogsByID)
	d.OnMessagesGetSavedHistory(r.onMessagesGetSavedHistory)
	d.OnMessagesReadSavedHistory(r.onMessagesReadSavedHistory)
	d.OnMessagesDeleteSavedHistory(r.onMessagesDeleteSavedHistory)
	d.OnMessagesGetCommonChats(r.onMessagesGetCommonChats)
	d.OnMessagesGetDefaultHistoryTTL(r.onMessagesGetDefaultHistoryTTL)
	d.OnMessagesSetHistoryTTL(r.onMessagesSetHistoryTTL)
	d.OnMessagesSetDefaultHistoryTTL(r.onMessagesSetDefaultHistoryTTL)
	d.OnMessagesGetSponsoredMessages(r.onMessagesGetSponsoredMessages)
	d.OnMessagesGetWebPagePreview(r.onMessagesGetWebPagePreview)
	d.OnMessagesRequestWebView(r.onMessagesRequestWebView)
	d.OnMessagesProlongWebView(r.onMessagesProlongWebView)
	d.OnMessagesSendWebViewResultMessage(r.onMessagesSendWebViewResultMessage)
	d.OnMessagesRequestSimpleWebView(r.onMessagesRequestSimpleWebView)
	d.OnMessagesGetBotApp(r.onMessagesGetBotApp)
	d.OnMessagesRequestAppWebView(r.onMessagesRequestAppWebView)
	d.OnMessagesRequestMainWebView(r.onMessagesRequestMainWebView)
	d.OnMessagesSendWebViewData(r.onMessagesSendWebViewData)
	d.OnMessagesSendBotRequestedPeer(r.onMessagesSendBotRequestedPeer)
	d.OnMessagesGetPreparedInlineMessage(r.onMessagesGetPreparedInlineMessage)
	d.OnMessagesGetEmojiGameInfo(r.onMessagesGetEmojiGameInfo)
	d.OnMessagesGetGameHighScores(r.onMessagesGetGameHighScores)
	d.OnMessagesGetInlineGameHighScores(r.onMessagesGetInlineGameHighScores)
	d.OnMessagesSetGameScore(r.onMessagesSetGameScore)
	d.OnMessagesSetInlineGameScore(r.onMessagesSetInlineGameScore)
	d.OnMessagesUploadMedia(r.onMessagesUploadMedia)
	d.OnMessagesSendMedia(r.onMessagesSendMedia)
	d.OnMessagesSendMultiMedia(r.onMessagesSendMultiMedia)
	d.OnMessagesReportSpam(r.onMessagesReportSpam)
	d.OnMessagesReport(r.onMessagesReport)
	d.OnMessagesReportReaction(r.onMessagesReportReaction)
	d.OnMessagesReportMessagesDelivery(r.onMessagesReportMessagesDelivery)
	d.OnMessagesReportReadMetrics(r.onMessagesReportReadMetrics)
	d.OnMessagesReportMusicListen(r.onMessagesReportMusicListen)
	d.OnMessagesReportSponsoredMessage(r.onMessagesReportSponsoredMessage)
	d.OnMessagesReadMessageContents(r.onMessagesReadMessageContents)
	d.OnMessagesTranslateText(r.onMessagesTranslateText)
	d.OnMessagesTogglePeerTranslations(r.onMessagesTogglePeerTranslations)
	d.OnMessagesGetMessagesViews(r.onMessagesGetMessagesViews)
	d.OnMessagesGetUnreadMentions(r.onMessagesGetUnreadMentions)
	d.OnMessagesReadMentions(r.onMessagesReadMentions)
	d.OnMessagesGetSearchCounters(r.onMessagesGetSearchCounters)
	d.OnMessagesGetReplies(r.onMessagesGetReplies)
	d.OnMessagesGetDiscussionMessage(r.onMessagesGetDiscussionMessage)
	d.OnMessagesReadDiscussion(r.onMessagesReadDiscussion)
	d.OnMessagesGetForumTopics(r.onMessagesGetForumTopics)
	d.OnMessagesGetForumTopicsByID(r.onMessagesGetForumTopicsByID)
	d.OnMessagesGetOnlines(r.onMessagesGetOnlines)
	d.OnMessagesGetAvailableReactions(r.onMessagesGetAvailableReactions)
	d.OnMessagesGetAvailableEffects(r.onMessagesGetAvailableEffects)
	d.OnMessagesGetStickers(r.onMessagesGetStickers)
	d.OnMessagesInstallStickerSet(r.onMessagesInstallStickerSet)
	d.OnMessagesUninstallStickerSet(r.onMessagesUninstallStickerSet)
	d.OnMessagesReorderStickerSets(r.onMessagesReorderStickerSets)
	d.OnMessagesToggleStickerSets(r.onMessagesToggleStickerSets)
	d.OnMessagesGetMyStickers(r.onMessagesGetMyStickers)
	d.OnMessagesGetArchivedStickers(func(ctx context.Context, req *tg.MessagesGetArchivedStickersRequest) (*tg.MessagesArchivedStickers, error) {
		return &tg.MessagesArchivedStickers{
			Count: 0,
			Sets:  []tg.StickerSetCoveredClass{},
		}, nil
	})
	d.OnMessagesGetStickerSet(r.onMessagesGetStickerSet)
	d.OnMessagesGetEmojiGroups(func(ctx context.Context, hash int) (tg.MessagesEmojiGroupsClass, error) {
		return tdesktop.EmojiGroups(hash), nil
	})
	d.OnMessagesGetEmojiStatusGroups(func(ctx context.Context, hash int) (tg.MessagesEmojiGroupsClass, error) {
		return tdesktop.EmojiStatusGroups(), nil
	})
	d.OnMessagesGetEmojiStickerGroups(r.onMessagesGetEmojiStickerGroups)
	d.OnMessagesGetEmojiProfilePhotoGroups(func(ctx context.Context, hash int) (tg.MessagesEmojiGroupsClass, error) {
		return tdesktop.EmojiProfilePhotoGroups(), nil
	})
	d.OnMessagesGetEmojiKeywords(r.onMessagesGetEmojiKeywords)
	d.OnMessagesGetEmojiKeywordsDifference(r.onMessagesGetEmojiKeywordsDifference)
	d.OnMessagesGetEmojiKeywordsLanguages(func(ctx context.Context, langcodes []string) ([]tg.EmojiLanguage, error) {
		return []tg.EmojiLanguage{}, nil
	})
	d.OnMessagesGetCustomEmojiDocuments(r.onMessagesGetCustomEmojiDocuments)
	d.OnMessagesGetAttachedStickers(r.onMessagesGetAttachedStickers)
	d.OnMessagesSearchStickerSets(r.onMessagesSearchStickerSets)
	d.OnMessagesSearchStickers(r.onMessagesSearchStickers)
	d.OnMessagesGetAttachMenuBots(r.onMessagesGetAttachMenuBots)
	d.OnMessagesGetAttachMenuBot(r.onMessagesGetAttachMenuBot)
	d.OnMessagesToggleBotInAttachMenu(r.onMessagesToggleBotInAttachMenu)
	d.OnMessagesGetQuickReplies(r.onMessagesGetQuickReplies)
	d.OnMessagesCheckQuickReplyShortcut(r.onMessagesCheckQuickReplyShortcut)
	d.OnMessagesReorderQuickReplies(r.onMessagesReorderQuickReplies)
	d.OnMessagesEditQuickReplyShortcut(r.onMessagesEditQuickReplyShortcut)
	d.OnMessagesDeleteQuickReplyShortcut(r.onMessagesDeleteQuickReplyShortcut)
	d.OnMessagesGetQuickReplyMessages(r.onMessagesGetQuickReplyMessages)
	d.OnMessagesSendQuickReplyMessages(r.onMessagesSendQuickReplyMessages)
	d.OnMessagesDeleteQuickReplyMessages(r.onMessagesDeleteQuickReplyMessages)
	d.OnMessagesGetWebPage(r.onMessagesGetWebPage)
	d.OnMessagesGetDialogs(func(ctx context.Context, req *tg.MessagesGetDialogsRequest) (tg.MessagesDialogsClass, error) {
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
		if filter.Hash != 0 {
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
		if ClientTypeFrom(ctx) == ClientTypeTDesktop && tdesktop.ShouldMergePinnedIntoInitialDialogs(filter) {
			pinned, err := r.pinnedDialogsList(ctx, userID, domain.DialogMainFolderID)
			if err != nil {
				return nil, internalErr()
			}
			list = tdesktop.MergeInitialDialogsWithPinned(list, pinned)
		}
		if filter.Hash != 0 && list.Hash == filter.Hash {
			return &tg.MessagesDialogsNotModified{Count: list.Count}, nil
		}
		return r.tgMessagesDialogs(ctx, userID, r.withDialogListPresence(ctx, userID, list)), nil
	})
	d.OnMessagesGetPinnedDialogs(func(ctx context.Context, folderID int) (*tg.MessagesPeerDialogs, error) {
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
	d.OnMessagesGetPeerDialogs(func(ctx context.Context, peers []tg.InputDialogPeerClass) (*tg.MessagesPeerDialogs, error) {
		id, _ := AuthKeyIDFrom(ctx)
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		// difference 类 catch-up FLOOD_WAIT（设计 Phase 2 / §10.3）：DrKLO 收 nudge 对未加载频道
		// 走 loadUnknownChannel→getPeerDialogs，限速须同时覆盖它（不止 getChannelDifference）。
		if err := r.checkCatchupRateLimit(ctx, userID, peerDialogsRateLimitKeyPrefix); err != nil {
			return nil, err
		}
		domainPeers, err := r.dialogPeersFromInput(ctx, userID, peers)
		if err != nil {
			return nil, err
		}
		var list domain.DialogList
		if len(domainPeers) > 0 && r.deps.Dialogs != nil {
			var err error
			list, err = r.deps.Dialogs.GetPeerDialogs(ctx, userID, domainPeers)
			if err != nil {
				return nil, internalErr()
			}
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
	d.OnMessagesGetPeerSettings(r.onMessagesGetPeerSettings)
	d.OnMessagesToggleDialogPin(r.onMessagesToggleDialogPin)
	d.OnMessagesReorderPinnedDialogs(r.onMessagesReorderPinnedDialogs)
	d.OnMessagesMarkDialogUnread(r.onMessagesMarkDialogUnread)
	d.OnMessagesGetDialogUnreadMarks(r.onMessagesGetDialogUnreadMarks)
	d.OnMessagesHidePeerSettingsBar(r.onMessagesHidePeerSettingsBar)
	d.OnMessagesGetMessageEditData(r.onMessagesGetMessageEditData)
	d.OnMessagesEditMessage(r.onMessagesEditMessage)
	d.OnMessagesGetOutboxReadDate(r.onMessagesGetOutboxReadDate)
	d.OnMessagesGetMessageReadParticipants(r.onMessagesGetMessageReadParticipants)
	d.OnMessagesDeleteMessages(r.onMessagesDeleteMessages)
	d.OnMessagesDeleteHistory(r.onMessagesDeleteHistory)
	d.OnMessagesGetMessages(r.onMessagesGetMessages)
	d.OnMessagesGetRichMessage(r.onMessagesGetRichMessage)
	d.OnMessagesGetHistory(func(ctx context.Context, req *tg.MessagesGetHistoryRequest) (tg.MessagesMessagesClass, error) {
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
	d.OnMessagesGetRecentLocations(r.onMessagesGetRecentLocations)
	d.OnMessagesReadHistory(func(ctx context.Context, req *tg.MessagesReadHistoryRequest) (*tg.MessagesAffectedMessages, error) {
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
	d.OnMessagesSearch(func(ctx context.Context, req *tg.MessagesSearchRequest) (tg.MessagesMessagesClass, error) {
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
	d.OnMessagesSearchGlobal(r.onMessagesSearchGlobal)
	d.OnMessagesGetSearchResultsCalendar(r.onMessagesGetSearchResultsCalendar)
	d.OnMessagesGetSearchResultsPositions(r.onMessagesGetSearchResultsPositions)
	d.OnMessagesSendReaction(r.onMessagesSendReaction)
	d.OnMessagesComposeMessageWithAI(r.onMessagesComposeMessageWithAI)
	// 语音转文字无识别后端：注册为显式失败（TRANSCRIPTION_FAILED），premium
	// 客户端点击转录按钮得到优雅失败提示，而不是 NOT_IMPLEMENTED trace。
	d.OnMessagesTranscribeAudio(func(ctx context.Context, req *tg.MessagesTranscribeAudioRequest) (*tg.MessagesTranscribedAudio, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return nil, tgerr400("TRANSCRIPTION_FAILED")
	})
	d.OnMessagesGetMessagesReactions(r.onMessagesGetMessagesReactions)
	d.OnMessagesGetMessageReactionsList(r.onMessagesGetMessageReactionsList)
	d.OnMessagesSetDefaultReaction(r.onMessagesSetDefaultReaction)
	d.OnMessagesGetPaidReactionPrivacy(r.onMessagesGetPaidReactionPrivacy)
	d.OnMessagesTogglePaidReactionPrivacy(r.onMessagesTogglePaidReactionPrivacy)
	d.OnMessagesSendPaidReaction(r.onMessagesSendPaidReaction)
	d.OnMessagesDeleteParticipantReactions(r.onMessagesDeleteParticipantReactions)
	d.OnMessagesDeleteParticipantReaction(r.onMessagesDeleteParticipantReaction)
	d.OnMessagesGetUnreadReactions(r.onMessagesGetUnreadReactions)
	d.OnMessagesReadReactions(r.onMessagesReadReactions)
	d.OnMessagesGetTopReactions(r.onMessagesGetTopReactions)
	d.OnMessagesGetRecentReactions(r.onMessagesGetRecentReactions)
	d.OnMessagesClearRecentReactions(r.onMessagesClearRecentReactions)
	d.OnMessagesGetSavedReactionTags(r.onMessagesGetSavedReactionTags)
	d.OnMessagesUpdateSavedReactionTag(r.onMessagesUpdateSavedReactionTag)
	d.OnMessagesGetDefaultTagReactions(r.onMessagesGetDefaultTagReactions)
	d.OnMessagesSendVote(r.onMessagesSendVote)
	d.OnMessagesGetPollResults(r.onMessagesGetPollResults)
	d.OnMessagesGetPollVotes(r.onMessagesGetPollVotes)
	d.OnMessagesAddPollAnswer(r.onMessagesAddPollAnswer)
	d.OnMessagesDeletePollAnswer(r.onMessagesDeletePollAnswer)
	d.OnMessagesGetUnreadPollVotes(r.onMessagesGetUnreadPollVotes)
	d.OnMessagesReadPollVotes(r.onMessagesReadPollVotes)
	d.OnMessagesAppendTodoList(r.onMessagesAppendTodoList)
	d.OnMessagesToggleTodoCompleted(r.onMessagesToggleTodoCompleted)
	d.OnMessagesGetScheduledHistory(r.onMessagesGetScheduledHistory)
	d.OnMessagesGetScheduledMessages(r.onMessagesGetScheduledMessages)
	d.OnMessagesSendScheduledMessages(r.onMessagesSendScheduledMessages)
	d.OnMessagesDeleteScheduledMessages(r.onMessagesDeleteScheduledMessages)
	d.OnMessagesCreateForumTopic(r.onMessagesCreateForumTopic)
	d.OnMessagesEditForumTopic(r.onMessagesEditForumTopic)
	d.OnMessagesUpdatePinnedForumTopic(r.onMessagesUpdatePinnedForumTopic)
	d.OnMessagesReorderPinnedForumTopics(r.onMessagesReorderPinnedForumTopics)
	d.OnMessagesDeleteTopicHistory(r.onMessagesDeleteTopicHistory)
}
