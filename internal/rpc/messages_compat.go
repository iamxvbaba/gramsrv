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

// webpageRequestResolveBudget is the short-budget upper bound for a synchronous
// fetch when an interactive read RPC (getWebPagePreview/getWebPage) misses the
// cache -- far below the async resolver's 20s, so a slow/hanging upstream cannot
// pin an RPC worker.
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
			// An ordinary channel (non-monoforum) passed as parent_peer: keep the old benign empty response.
			if req.Hash != 0 {
				return &tg.MessagesMessagesNotModified{Count: 0}, nil
			}
			result := &tg.MessagesMessages{
				Messages: []tg.MessageClass{},
				Chats:    r.savedHistoryChats(ctx, userID, hasParent, parentPeer, req.Peer),
				Users:    []tg.UserClass{},
			}
			r.applyPeerReadModelsToMessages(ctx, userID, result)
			return result, nil
		}
		// parent_peer = monoforum: return this subscriber's (req.Peer) history within the channel private messages.
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
	// When the saved peer is a channel (a channel-forward sub-dialog), add chat context.
	if chats := r.savedHistoryChats(ctx, userID, false, domain.Peer{}, req.Peer); len(chats) > 0 {
		switch m := out.(type) {
		case *tg.MessagesMessages:
			m.Chats = mergeTGChats(m.Chats, chats)
		case *tg.MessagesMessagesSlice:
			m.Chats = mergeTGChats(m.Chats, chats)
		}
	}
	r.applyPeerReadModelsToMessages(ctx, userID, out)
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
		// Deleting a monoforum sub-dialog is out of scope: after parent validation, keep no-op semantics.
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
	r.applyUsernamesToPeerObjects(ctx, nil, chats)
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
	// We only freeze a single-version dictionary: if the client version is behind, send the full set (0..version), otherwise echo an empty delta.
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

// catalogEmojiKeywords converts catalog dictionary entries into a TL emojiKeyword vector.
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

// onMessagesGetWebPage returns the resolved link preview for a URL (instant view
// entry; cached_page is not filled). On a hash match it returns
// webPageNotModified so the client reuses its local cache; on no preview/disabled/
// failure it returns webPageEmpty.
func (r *Router) onMessagesGetWebPage(ctx context.Context, req *tg.MessagesGetWebPageRequest) (*tg.MessagesWebPage, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return r.webPageForURL(ctx, req.URL, req.Hash), nil
}

// webPageForURL resolves a URL's link preview for getWebPage to return. On a hash
// match it returns webPageNotModified; on no preview/disabled/failure it returns
// webPageEmpty (with URL). cached_page (instant view) is not filled.
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

// webPagePreviewMedia resolves the first link in a message and returns the media
// for the input-box preview. getWebPagePreview is a synchronous probe: it returns
// messageMediaWebPage only when a done card has already been resolved, and
// messageMediaEmpty otherwise (it does not return pending -- clients do not poll
// pending for a preview). A fetch failure/disabled resolver always degrades to
// empty and never errors.
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

// resolveWebPageForRequest resolves a link preview for an interactive read RPC:
// first check the cache (LookupWebPage; a hit returns immediately without
// fetching or blocking); only on a miss does it fetch synchronously, using a
// limited short budget (webpageRequestResolveBudget) rather than the async
// resolver's 30s, so a slow/hanging upstream cannot pin an RPC worker. A hit
// (including a negatively cached empty) returns ok=true and the caller decides
// based on state; a fetch failure returns false. Disabled returns false.
func (r *Router) resolveWebPageForRequest(ctx context.Context, url string) (domain.MessageWebPage, bool) {
	if page, ok := r.resolveInternalWebPage(ctx, url); ok {
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
	// On-time delivery of a scheduled message must be equivalent to an immediate
	// send: replay all entity types (mentionName is already resolved to user_id in
	// domain and goes straight through in output form).
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
