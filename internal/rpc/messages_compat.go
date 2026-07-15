package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"

	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/seed/catalog"
	"unicode/utf8"
)

// webpageRequestResolveBudget 是交互式读 RPC（getWebPagePreview/getWebPage）未命中缓存时
// 同步抓取的短预算上界——远小于异步解析的 20s，避免慢/挂上游把 RPC worker 钉死。
const webpageRequestResolveBudget = 6 * time.Second

func (r *Router) onMessagesGetSavedHistory(ctx context.Context, req *tg.MessagesGetSavedHistoryRequest) (tg.MessagesMessagesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := validateSavedHistoryBounds(req.OffsetID, req.OffsetDate, req.AddOffset, req.Limit, req.MaxID, req.MinID); err != nil {
		return nil, err
	}
	parentPeer, hasParent, err := r.validateSavedHistoryParentPeer(ctx, userID, req.GetParentPeer)
	if err != nil {
		return nil, err
	}
	savedPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if hasParent {
		mono, isMono, err := r.resolveMonoforumForAdmin(ctx, userID, parentPeer)
		if err != nil {
			return nil, err
		}
		if !isMono {
			// 普通频道(非 monoforum)传 parent_peer:保持旧的良性空响应。
			if req.Hash != 0 {
				return &tg.MessagesMessagesNotModified{Count: 0}, nil
			}
			return &tg.MessagesMessages{
				Messages: []tg.MessageClass{},
				Chats:    r.savedHistoryChats(ctx, userID, hasParent, parentPeer, req.Peer),
				Users:    []tg.UserClass{},
			}, nil
		}
		// parent_peer = monoforum:返回该订阅者(req.Peer)在频道私信内的历史。
		return r.monoforumSavedHistory(ctx, userID, mono, savedPeer, req.Limit, req.OffsetID)
	}
	if r.deps.Messages == nil {
		return messagesNotModifiedOrEmpty(req.Hash), nil
	}
	list, err := r.deps.Messages.GetHistory(ctx, userID, domain.MessageFilter{
		HasPeer:        true,
		Peer:           domain.Peer{Type: domain.PeerTypeUser, ID: userID},
		SavedPeer:      savedPeer,
		OffsetID:       req.OffsetID,
		OffsetDate:     req.OffsetDate,
		AddOffset:      req.AddOffset,
		Limit:          req.Limit,
		MaxID:          req.MaxID,
		MinID:          req.MinID,
		Hash:           req.Hash,
		NeedTotalCount: true,
	})
	if err != nil {
		return nil, internalErr()
	}
	if req.Hash != 0 && list.Hash == req.Hash {
		return &tg.MessagesMessagesNotModified{Count: list.Count}, nil
	}
	out := tgMessagesMessages(userID, r.enrichMessageList(ctx, userID, list))
	// saved peer 是频道（频道转发子会话）时补 chat 上下文。
	if chats := r.savedHistoryChats(ctx, userID, false, domain.Peer{}, req.Peer); len(chats) > 0 {
		switch m := out.(type) {
		case *tg.MessagesMessages:
			m.Chats = mergeTGChats(m.Chats, chats)
		case *tg.MessagesMessagesSlice:
			m.Chats = mergeTGChats(m.Chats, chats)
		}
	}
	return out, nil
}

func (r *Router) onMessagesReadSavedHistory(ctx context.Context, req *tg.MessagesReadSavedHistoryRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req.MaxID < 0 || req.MaxID > domain.MaxMessageBoxID {
		return false, messageIDInvalidErr()
	}
	if err := r.validateRequiredSavedHistoryParentPeer(ctx, userID, req.ParentPeer); err != nil {
		return false, err
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onMessagesDeleteSavedHistory(ctx context.Context, req *tg.MessagesDeleteSavedHistoryRequest) (*tg.MessagesAffectedHistory, error) {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.MaxID < 0 || req.MaxID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	minDate, hasMinDate := req.GetMinDate()
	if hasMinDate && minDate < 0 {
		return nil, limitInvalidErr()
	}
	maxDate, hasMaxDate := req.GetMaxDate()
	if hasMaxDate && maxDate < 0 {
		return nil, limitInvalidErr()
	}
	_, hasParent, err := r.validateSavedHistoryParentPeer(ctx, userID, req.GetParentPeer)
	if err != nil {
		return nil, err
	}
	savedPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if hasParent || r.deps.Messages == nil {
		// monoforum 子会话删除范围外：parent 校验后保持 no-op 语义。
		return r.affectedHistory(ctx, authKeyID, userID, 0)
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.DeleteSavedHistory(ctx, userID, domain.DeleteSavedHistoryRequest{
		OwnerUserID:     userID,
		SavedPeer:       savedPeer,
		MaxID:           req.MaxID,
		MinDate:         minDate,
		MaxDate:         maxDate,
		Date:            int(r.clock.Now().Unix()),
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionID,
	})
	if err != nil {
		return nil, internalErr()
	}
	if len(res.MessageIDs) == 0 || res.Event.Pts == 0 {
		return r.affectedHistory(ctx, authKeyID, userID, 0)
	}
	offset := 0
	if res.More {
		offset = 1
	}
	return &tg.MessagesAffectedHistory{
		Pts:      res.Event.Pts,
		PtsCount: res.Event.PtsCount,
		Offset:   offset,
	}, nil
}

func (r *Router) onMessagesGetCommonChats(ctx context.Context, req *tg.MessagesGetCommonChatsRequest) (tg.MessagesChatsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.Limit < 0 || req.Limit > maxCommonChatsLimit {
		return nil, limitInvalidErr()
	}
	if req.MaxID < 0 {
		return nil, messageIDInvalidErr()
	}
	target, found, err := r.userFromInput(ctx, userID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || target.ID == 0 || target.ID == userID {
		return nil, userIDInvalidErr()
	}
	if req.Limit == 0 || r.deps.Channels == nil {
		return &tg.MessagesChats{Chats: []tg.ChatClass{}}, nil
	}
	common, err := r.deps.Channels.CommonChannels(ctx, userID, domain.CommonChannelsRequest{
		UserID:       userID,
		TargetUserID: target.ID,
		MaxID:        req.MaxID,
		Limit:        req.Limit,
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	chats := make([]tg.ChatClass, 0, len(common.Channels))
	for _, ch := range common.Channels {
		chats = append(chats, tgChannelChatMin(userID, ch))
	}
	return &tg.MessagesChats{Chats: chats}, nil
}

func (r *Router) onMessagesGetAttachedStickers(ctx context.Context, media tg.InputStickeredMediaClass) ([]tg.StickerSetCoveredClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if media == nil {
		return nil, mediaEmptyErr()
	}
	return []tg.StickerSetCoveredClass{}, nil
}

func (r *Router) onMessagesGetCustomEmojiDocuments(ctx context.Context, documentIDs []int64) ([]tg.DocumentClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if len(documentIDs) > maxEmojiDocuments {
		return nil, limitInvalidErr()
	}
	for _, id := range documentIDs {
		if id <= 0 {
			return nil, messageIDInvalidErr()
		}
	}
	if r.deps.Files == nil || len(documentIDs) == 0 {
		return []tg.DocumentClass{}, nil
	}
	docs, err := r.deps.Files.GetDocuments(ctx, documentIDs)
	if err != nil {
		return nil, internalErr()
	}
	byID := documentsByID(docs)
	out := make([]tg.DocumentClass, 0, len(documentIDs))
	for _, id := range documentIDs {
		if d, ok := byID[id]; ok {
			out = append(out, tgDocument(d))
		} else {
			out = append(out, &tg.DocumentEmpty{ID: id})
		}
	}
	return out, nil
}

func (r *Router) onMessagesGetEmojiKeywords(ctx context.Context, langcode string) (*tg.EmojiKeywordsDifference, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if err := validateEmojiLangCode(langcode); err != nil {
		return nil, err
	}
	if set, ok := catalog.EmojiKeywords(langcode); ok {
		return &tg.EmojiKeywordsDifference{
			LangCode:    langcode,
			FromVersion: 0,
			Version:     set.Version,
			Keywords:    catalogEmojiKeywords(set.Keywords),
		}, nil
	}
	return &tg.EmojiKeywordsDifference{
		LangCode:    langcode,
		FromVersion: 0,
		Version:     0,
		Keywords:    []tg.EmojiKeywordClass{},
	}, nil
}

func (r *Router) onMessagesGetEmojiKeywordsDifference(ctx context.Context, req *tg.MessagesGetEmojiKeywordsDifferenceRequest) (*tg.EmojiKeywordsDifference, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if err := validateEmojiLangCode(req.LangCode); err != nil {
		return nil, err
	}
	if req.FromVersion < 0 {
		return nil, limitInvalidErr()
	}
	// 我们只固化了单一版本词典:客户端版本落后则下发全量(0..version),否则回显空增量。
	if set, ok := catalog.EmojiKeywords(req.LangCode); ok && req.FromVersion < set.Version {
		return &tg.EmojiKeywordsDifference{
			LangCode:    req.LangCode,
			FromVersion: req.FromVersion,
			Version:     set.Version,
			Keywords:    catalogEmojiKeywords(set.Keywords),
		}, nil
	}
	version := req.FromVersion
	if set, ok := catalog.EmojiKeywords(req.LangCode); ok {
		version = set.Version
	}
	return &tg.EmojiKeywordsDifference{
		LangCode:    req.LangCode,
		FromVersion: req.FromVersion,
		Version:     version,
		Keywords:    []tg.EmojiKeywordClass{},
	}, nil
}

// catalogEmojiKeywords 把 catalog 词典条目转成 TL emojiKeyword 向量。
func catalogEmojiKeywords(kws []catalog.EmojiKeyword) []tg.EmojiKeywordClass {
	out := make([]tg.EmojiKeywordClass, 0, len(kws))
	for _, k := range kws {
		out = append(out, &tg.EmojiKeyword{Keyword: k.Keyword, Emoticons: k.Emoticons})
	}
	return out
}

func (r *Router) onMessagesGetExtendedMedia(ctx context.Context, req *tg.MessagesGetExtendedMediaRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := validateMessageIDVector(req.ID); err != nil {
		return nil, err
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
}

func (r *Router) onMessagesGetWebPagePreview(ctx context.Context, req *tg.MessagesGetWebPagePreviewRequest) (*tg.MessagesWebPagePreview, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return nil, messageEmptyErr()
	}
	if utf8.RuneCountInString(message) > maxSendMessageTextLength || len(req.Entities) > maxMessageEntityCount {
		return nil, limitInvalidErr()
	}
	media := r.webPagePreviewMedia(ctx, req.Message, req.Entities)
	return &tg.MessagesWebPagePreview{
		Media: media,
		Chats: []tg.ChatClass{},
		Users: []tg.UserClass{},
	}, nil
}

// onMessagesGetWebPage 返回某 URL 的已解析链接预览（instant view 入口；cached_page 不填充）。
// hash 匹配则回 webPageNotModified 让客户端复用本地缓存；无预览/未启用/失败回 webPageEmpty。
func (r *Router) onMessagesGetWebPage(ctx context.Context, req *tg.MessagesGetWebPageRequest) (*tg.MessagesWebPage, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return r.webPageForURL(ctx, req.URL, req.Hash), nil
}

// webPageForURL 解析 URL 的链接预览供 getWebPage 返回。hash 匹配回 webPageNotModified；
// 无预览/未启用/失败回 webPageEmpty（带 URL）。cached_page（instant view）不填充。
func (r *Router) webPageForURL(ctx context.Context, url string, hash int) *tg.MessagesWebPage {
	emptyResult := func() *tg.MessagesWebPage {
		page := &tg.WebPageEmpty{}
		if url != "" {
			page.SetURL(url)
		}
		return &tg.MessagesWebPage{Webpage: page, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	}
	page, ok := r.resolveWebPageForRequest(ctx, url)
	if !ok || page.State != domain.MessageWebPageStateDone {
		return emptyResult()
	}
	if hash != 0 && hash == page.Hash {
		return &tg.MessagesWebPage{Webpage: &tg.WebPageNotModified{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
	}
	return &tg.MessagesWebPage{Webpage: tgWebPage(page), Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}
}

// webPagePreviewMedia 解析消息内首个链接，返回输入框预览用的 media。getWebPagePreview 是同步
// 探针：只有已解析出 done 卡片才返回 messageMediaWebPage，其余一律 messageMediaEmpty（不返回
// pending——客户端不会对 preview 轮询 pending）。抓取失败/未启用一律降级为空，绝不报错。
func (r *Router) webPagePreviewMedia(ctx context.Context, message string, entities []tg.MessageEntityClass) tg.MessageMediaClass {
	url, ok := firstPreviewableURL(message, entities)
	if !ok {
		return &tg.MessageMediaEmpty{}
	}
	page, ok := r.resolveWebPageForRequest(ctx, url)
	if !ok || page.State != domain.MessageWebPageStateDone {
		return &tg.MessageMediaEmpty{}
	}
	return tgWebPageMedia(page)
}

// resolveWebPageForRequest 为交互式读 RPC 解析链接预览：先查缓存（LookupWebPage，命中即返回，
// 不抓取不阻塞）；未命中才同步抓取，但用受限短预算（webpageRequestResolveBudget）而非异步解析
// 的 20s，避免慢/挂上游把 RPC worker 钉死。命中（含负缓存的 empty）返回 ok=true，调用方据 state
// 决定；抓取失败返回 false。未启用返回 false。
func (r *Router) resolveWebPageForRequest(ctx context.Context, url string) (domain.MessageWebPage, bool) {
	if page, ok := r.resolveAIComposeStyleWebPage(ctx, url); ok {
		return page, true
	}
	if r.deps.Files == nil {
		return domain.MessageWebPage{}, false
	}
	if page, ok := r.deps.Files.LookupWebPage(ctx, url); ok {
		return page, true
	}
	fctx, cancel := context.WithTimeout(ctx, webpageRequestResolveBudget)
	defer cancel()
	page, err := r.deps.Files.ResolveWebPage(fctx, url)
	if err != nil {
		return domain.MessageWebPage{}, false
	}
	return page, true
}

func tgInputMessageEntities(entities []domain.MessageEntity) []tg.MessageEntityClass {
	// 定时消息到点投递必须与即时发送等价：实体全类型回放（mentionName
	// 在 domain 已解析为 user_id，直接走输出形态）。
	return tgMessageEntities(entities)
}

func mergeTGUsers(base []tg.UserClass, extra []tg.UserClass) []tg.UserClass {
	seen := make(map[int64]struct{}, len(base)+len(extra))
	out := make([]tg.UserClass, 0, len(base)+len(extra))
	add := func(user tg.UserClass) {
		u, ok := user.(*tg.User)
		if !ok || u.ID == 0 {
			out = append(out, user)
			return
		}
		if _, ok := seen[u.ID]; ok {
			return
		}
		seen[u.ID] = struct{}{}
		out = append(out, user)
	}
	for _, user := range base {
		add(user)
	}
	for _, user := range extra {
		add(user)
	}
	return out
}

func mergeTGChats(base []tg.ChatClass, extra []tg.ChatClass) []tg.ChatClass {
	seen := make(map[int64]struct{}, len(base)+len(extra))
	out := make([]tg.ChatClass, 0, len(base)+len(extra))
	add := func(chat tg.ChatClass) {
		var id int64
		switch c := chat.(type) {
		case *tg.Channel:
			id = c.ID
		case *tg.Chat:
			id = c.ID
		}
		if id == 0 {
			out = append(out, chat)
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, chat)
	}
	for _, chat := range base {
		add(chat)
	}
	for _, chat := range extra {
		add(chat)
	}
	return out
}

func optionalString(get func() (string, bool)) string {
	if get == nil {
		return ""
	}
	value, ok := get()
	if !ok {
		return ""
	}
	return value
}

func validateEmojiLangCode(langcode string) error {
	if langcode == "" || len(langcode) > maxEmojiLangCodeLength {
		return limitInvalidErr()
	}
	for _, c := range langcode {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return limitInvalidErr()
		}
	}
	return nil
}

func validateSavedHistoryBounds(offsetID, offsetDate, addOffset, limit, maxID, minID int) error {
	if err := validateHistoryBounds(offsetID, addOffset, limit, maxID, minID); err != nil {
		return err
	}
	if offsetDate < 0 {
		return limitInvalidErr()
	}
	return nil
}

func (r *Router) validateSavedHistoryParentPeer(ctx context.Context, userID int64, getParent func() (tg.InputPeerClass, bool)) (domain.Peer, bool, error) {
	parentPeer, ok := getParent()
	if !ok {
		return domain.Peer{}, false, nil
	}
	if err := r.validateRequiredSavedHistoryParentPeer(ctx, userID, parentPeer); err != nil {
		return domain.Peer{}, false, err
	}
	parent, _ := r.domainPeerFromInputPeer(userID, parentPeer)
	return parent, true, nil
}

func (r *Router) validateRequiredSavedHistoryParentPeer(ctx context.Context, userID int64, parentPeer tg.InputPeerClass) error {
	parent, err := r.checkedDomainPeerFromInputPeer(ctx, userID, parentPeer)
	if err != nil || parent.Type != domain.PeerTypeChannel {
		return parentPeerInvalidErr()
	}
	return nil
}

func messagesAllStickersEmpty(hash int64) tg.MessagesAllStickersClass {
	return &tg.MessagesAllStickers{Hash: 0, Sets: []tg.StickerSet{}}
}

func messagesFeaturedStickersEmpty(hash int64) tg.MessagesFeaturedStickersClass {
	return &tg.MessagesFeaturedStickers{
		Hash:   0,
		Count:  0,
		Sets:   []tg.StickerSetCoveredClass{},
		Unread: []int64{},
	}
}
