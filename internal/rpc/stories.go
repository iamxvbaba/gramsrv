package rpc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

const (
	maxStoryAlbumTitleLength        = 12
	maxStoryAllStoriesStateLength   = 128
	maxStorySearchPostsOffsetLength = 64
	minStoryWeatherTemperatureC     = -274
	maxStoryWeatherTemperatureC     = 1000000
	storyStealthFuturePeriodSeconds = 25 * 60
	storyStealthCooldownSeconds     = 3 * 60 * 60
)

// registerStories 注册 TDesktop/Android 已发现的 stories.* RPC。
func (r *Router) registerStories(d *tlprofile.Dispatcher) {
	registerRPC[*tg.StoriesGetAllStoriesRequest](d, tlprofile.SemanticMethodStoriesGetAllStories, func(ctx context.Context, layerRequest *tg.StoriesGetAllStoriesRequest) (any, error) {
		return r.onStoriesGetAllStories(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetPeerStoriesRequest](d, tlprofile.SemanticMethodStoriesGetPeerStories, func(ctx context.Context, layerRequest *tg.StoriesGetPeerStoriesRequest) (any, error) {
		return r.onStoriesGetPeerStories(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.StoriesGetStoriesByIDRequest](d, tlprofile.SemanticMethodStoriesGetStoriesByID, func(ctx context.Context, layerRequest *tg.StoriesGetStoriesByIDRequest) (any, error) {
		return r.onStoriesGetStoriesByID(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetStoriesArchiveRequest](d, tlprofile.SemanticMethodStoriesGetStoriesArchive, func(ctx context.Context, layerRequest *tg.StoriesGetStoriesArchiveRequest) (any, error) {
		return r.onStoriesGetStoriesArchive(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetPinnedStoriesRequest](d, tlprofile.SemanticMethodStoriesGetPinnedStories, func(ctx context.Context, layerRequest *tg.StoriesGetPinnedStoriesRequest) (any, error) {
		return r.onStoriesGetPinnedStories(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesExportStoryLinkRequest](d, tlprofile.SemanticMethodStoriesExportStoryLink, func(ctx context.Context, layerRequest *tg.StoriesExportStoryLinkRequest) (any, error) {
		return r.onStoriesExportStoryLink(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesReportRequest](d, tlprofile.SemanticMethodStoriesReport, func(ctx context.Context, layerRequest *tg.StoriesReportRequest) (any, error) {
		return r.onStoriesReport(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesActivateStealthModeRequest](d, tlprofile.SemanticMethodStoriesActivateStealthMode, func(ctx context.Context, layerRequest *tg.StoriesActivateStealthModeRequest) (any, error) {
		return r.onStoriesActivateStealthMode(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesSearchPostsRequest](d, tlprofile.SemanticMethodStoriesSearchPosts, func(ctx context.Context, layerRequest *tg.StoriesSearchPostsRequest) (any, error) {
		return r.onStoriesSearchPosts(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesSendStoryRequest](d, tlprofile.SemanticMethodStoriesSendStory, func(ctx context.Context, layerRequest *tg.StoriesSendStoryRequest) (any, error) {
		return r.onStoriesSendStory(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesEditStoryRequest](d, tlprofile.SemanticMethodStoriesEditStory, func(ctx context.Context, layerRequest *tg.StoriesEditStoryRequest) (any, error) {
		return r.onStoriesEditStory(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesDeleteStoriesRequest](d, tlprofile.SemanticMethodStoriesDeleteStories, func(ctx context.Context, layerRequest *tg.StoriesDeleteStoriesRequest) (any, error) {
		return r.onStoriesDeleteStories(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesTogglePinnedRequest](d, tlprofile.SemanticMethodStoriesTogglePinned, func(ctx context.Context, layerRequest *tg.StoriesTogglePinnedRequest) (any, error) {
		return r.onStoriesTogglePinned(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesTogglePinnedToTopRequest](d, tlprofile.SemanticMethodStoriesTogglePinnedToTop, func(ctx context.Context, layerRequest *tg.StoriesTogglePinnedToTopRequest) (any, error) {
		return r.onStoriesTogglePinnedToTop(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesToggleAllStoriesHiddenRequest](d, tlprofile.SemanticMethodStoriesToggleAllStoriesHidden, func(ctx context.Context, layerRequest *tg.StoriesToggleAllStoriesHiddenRequest) (any, error) {
		return r.onStoriesToggleAllStoriesHidden(ctx, layerRequest.
			Hidden)
	})
	registerRPC[*tg.StoriesCreateAlbumRequest](d, tlprofile.SemanticMethodStoriesCreateAlbum, func(ctx context.Context, layerRequest *tg.StoriesCreateAlbumRequest) (any, error) {
		return r.onStoriesCreateAlbum(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesUpdateAlbumRequest](d, tlprofile.SemanticMethodStoriesUpdateAlbum, func(ctx context.Context, layerRequest *tg.StoriesUpdateAlbumRequest) (any, error) {
		return r.onStoriesUpdateAlbum(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesReorderAlbumsRequest](d, tlprofile.SemanticMethodStoriesReorderAlbums, func(ctx context.Context, layerRequest *tg.StoriesReorderAlbumsRequest) (any, error) {
		return r.onStoriesReorderAlbums(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesDeleteAlbumRequest](d, tlprofile.SemanticMethodStoriesDeleteAlbum, func(ctx context.Context, layerRequest *tg.StoriesDeleteAlbumRequest) (any, error) {
		return r.onStoriesDeleteAlbum(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetAlbumsRequest](d, tlprofile.SemanticMethodStoriesGetAlbums, func(ctx context.Context, layerRequest *tg.StoriesGetAlbumsRequest) (any, error) {
		return r.onStoriesGetAlbums(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetAlbumStoriesRequest](d, tlprofile.SemanticMethodStoriesGetAlbumStories, func(ctx context.Context, layerRequest *tg.StoriesGetAlbumStoriesRequest) (any, error) {
		return r.onStoriesGetAlbumStories(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetAllReadPeerStoriesRequest](d, tlprofile.SemanticMethodStoriesGetAllReadPeerStories, func(ctx context.Context, layerRequest *tg.StoriesGetAllReadPeerStoriesRequest) (any, error) {
		return r.onStoriesGetAllReadPeerStories(ctx)
	})
	registerRPC[*tg.StoriesGetPeerMaxIDsRequest](d, tlprofile.SemanticMethodStoriesGetPeerMaxIDs, func(ctx context.Context, layerRequest *tg.StoriesGetPeerMaxIDsRequest) (any, error) {
		return r.onStoriesGetPeerMaxIDs(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.StoriesReadStoriesRequest](d, tlprofile.SemanticMethodStoriesReadStories, func(ctx context.Context, layerRequest *tg.StoriesReadStoriesRequest) (any, error) {
		return r.onStoriesReadStories(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesIncrementStoryViewsRequest](d, tlprofile.SemanticMethodStoriesIncrementStoryViews, func(ctx context.Context, layerRequest *tg.StoriesIncrementStoryViewsRequest) (any, error) {
		return r.onStoriesIncrementStoryViews(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetStoriesViewsRequest](d, tlprofile.SemanticMethodStoriesGetStoriesViews, func(ctx context.Context, layerRequest *tg.StoriesGetStoriesViewsRequest) (any, error) {
		return r.onStoriesGetStoriesViews(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetStoryViewsListRequest](d, tlprofile.SemanticMethodStoriesGetStoryViewsList, func(ctx context.Context, layerRequest *tg.StoriesGetStoryViewsListRequest) (any, error) {
		return r.onStoriesGetStoryViewsList(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesGetStoryReactionsListRequest](d, tlprofile.SemanticMethodStoriesGetStoryReactionsList, func(ctx context.Context, layerRequest *tg.StoriesGetStoryReactionsListRequest) (any, error) {
		return r.onStoriesGetStoryReactionsList(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesTogglePeerStoriesHiddenRequest](d, tlprofile.SemanticMethodStoriesTogglePeerStoriesHidden, func(ctx context.Context, layerRequest *tg.StoriesTogglePeerStoriesHiddenRequest) (any, error) {
		return r.onStoriesTogglePeerStoriesHidden(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesCanSendStoryRequest](d, tlprofile.SemanticMethodStoriesCanSendStory, func(ctx context.Context, layerRequest *tg.StoriesCanSendStoryRequest) (any, error) {
		return r.onStoriesCanSendStory(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.StoriesGetChatsToSendRequest](d, tlprofile.SemanticMethodStoriesGetChatsToSend, func(ctx context.Context, layerRequest *tg.StoriesGetChatsToSendRequest) (any, error) {
		return r.onStoriesGetChatsToSend(ctx)
	})
	registerRPC[*tg.StoriesSendReactionRequest](d, tlprofile.SemanticMethodStoriesSendReaction, func(ctx context.Context, layerRequest *tg.StoriesSendReactionRequest) (any, error) {
		return r.onStoriesSendReaction(ctx, layerRequest)
	})
	registerRPC[*tg.StoriesStartLiveRequest](d, tlprofile.SemanticMethodStoriesStartLive, func(ctx context.Context, layerRequest *tg.StoriesStartLiveRequest) (any, error) {
		return r.onStoriesStartLive(ctx, layerRequest)
	})
}

func (r *Router) onStoriesGetAllStories(ctx context.Context, req *tg.StoriesGetAllStoriesRequest) (tg.StoriesAllStoriesClass, error) {
	if err := validateStoriesGetAllStoriesRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	hidden := storyAllStoriesHidden(req)
	requestState, hasRequestState := storyAllStoriesRequestState(req)
	next := storyAllStoriesNext(req)
	now := int(r.clock.Now().Unix())
	var cursor domain.StoryListCursor
	if next {
		if digest, ok := storyAllStoriesDigestFromCompleteState(requestState, hidden); ok {
			list := domain.StoryList{Hidden: hidden, Count: digest.Count, State: requestState}
			if r.deps.Stories == nil || userID == 0 {
				return tgStoriesAllStories(userID, list), nil
			}
			return r.tgStoriesAllStories(ctx, userID, list), nil
		}
		cursor, err = storyAllStoriesCursorFromState(requestState, hidden)
		if err != nil {
			return nil, offsetInvalidErr()
		}
	}
	if r.deps.Stories == nil || userID == 0 {
		list := domain.StoryList{Hidden: hidden, State: storyAllStoriesDigestState(hidden, domain.StoryListDigest{})}
		if hasRequestState && !next && requestState == list.State {
			return tgStoriesAllStoriesNotModified(list.State), nil
		}
		return tgStoriesAllStories(userID, list), nil
	}
	var (
		completeState string
		haveDigest    bool
	)
	if hasRequestState && !next && storyAllStoriesCompleteState(requestState) {
		digest, err := r.deps.Stories.GetAllStoriesDigest(ctx, userID, hidden, now)
		if err != nil {
			return nil, storyErr(err)
		}
		completeState = storyAllStoriesDigestState(hidden, digest)
		haveDigest = true
		if requestState == completeState {
			return tgStoriesAllStoriesNotModified(completeState), nil
		}
	}
	list, err := r.deps.Stories.GetAllStoriesPage(ctx, userID, hidden, now, cursor, domain.MaxStoryListLimit)
	if err != nil {
		return nil, storyErr(err)
	}
	if list.HasMore {
		list.State = storyAllStoriesCursorStateFromList(hidden, list)
	} else {
		if !haveDigest {
			digest, err := r.deps.Stories.GetAllStoriesDigest(ctx, userID, hidden, now)
			if err != nil {
				return nil, storyErr(err)
			}
			completeState = storyAllStoriesDigestState(hidden, digest)
		}
		list.State = completeState
	}
	list.Hidden = hidden
	return r.tgStoriesAllStories(ctx, userID, list), nil
}

func validateStoriesGetAllStoriesRequest(req *tg.StoriesGetAllStoriesRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	state, hasState := storyAllStoriesRequestState(req)
	if len(state) > maxStoryAllStoriesStateLength {
		return offsetInvalidErr()
	}
	next := storyAllStoriesNext(req)
	hidden := storyAllStoriesHidden(req)
	if next {
		if !hasState || state == "" {
			return offsetInvalidErr()
		}
		if storyAllStoriesCompleteState(state) {
			if !storyAllStoriesCompleteStateForHidden(state, hidden) {
				return offsetInvalidErr()
			}
			return nil
		}
		if !storyAllStoriesCursorStateToken(state) {
			return offsetInvalidErr()
		}
		if _, err := storyAllStoriesCursorFromState(state, hidden); err != nil {
			return offsetInvalidErr()
		}
		return nil
	}
	if !hasState {
		return nil
	}
	if state == "" || !storyAllStoriesCompleteStateForHidden(state, hidden) {
		return offsetInvalidErr()
	}
	return nil
}

func storyAllStoriesRequestState(req *tg.StoriesGetAllStoriesRequest) (string, bool) {
	state, hasState := req.GetState()
	if !hasState && req.State != "" {
		state, hasState = req.State, true
	}
	return state, hasState
}

func storyAllStoriesNext(req *tg.StoriesGetAllStoriesRequest) bool {
	return req != nil && (req.Next || req.GetNext())
}

func storyAllStoriesHidden(req *tg.StoriesGetAllStoriesRequest) bool {
	return req != nil && (req.Hidden || req.GetHidden())
}

func storyAllStoriesCompleteState(state string) bool {
	return strings.HasPrefix(state, "ts1:")
}

func storyAllStoriesDigestFromCompleteState(state string, hidden bool) (domain.StoryListDigest, bool) {
	if !storyAllStoriesCompleteStateForHidden(state, hidden) {
		return domain.StoryListDigest{}, false
	}
	parts := strings.Split(state, ":")
	count, err := strconv.Atoi(parts[2])
	if err != nil || count < 0 {
		return domain.StoryListDigest{}, false
	}
	hash, err := strconv.ParseUint(parts[3], 16, 64)
	if err != nil {
		return domain.StoryListDigest{}, false
	}
	return domain.StoryListDigest{Count: count, Hash: hash}, true
}

func storyAllStoriesCompleteStateForHidden(state string, hidden bool) bool {
	parts := strings.Split(state, ":")
	if len(parts) != 4 || parts[0] != "ts1" {
		return false
	}
	switch parts[1] {
	case "0":
		if hidden {
			return false
		}
	case "1":
		if !hidden {
			return false
		}
	default:
		return false
	}
	count, err := strconv.Atoi(parts[2])
	if err != nil || count < 0 {
		return false
	}
	if len(parts[3]) != 16 {
		return false
	}
	_, err = strconv.ParseUint(parts[3], 16, 64)
	return err == nil
}

func storyAllStoriesDigestState(hidden bool, digest domain.StoryListDigest) string {
	hiddenBit := "0"
	if hidden {
		hiddenBit = "1"
	}
	return fmt.Sprintf("ts1:%s:%d:%016x", hiddenBit, digest.Count, digest.Hash)
}

func storyAllStoriesCursorStateFromList(hidden bool, list domain.StoryList) string {
	if cursor, ok := storyAllStoriesCursorFromList(list); ok {
		return storyAllStoriesCursorState(hidden, cursor)
	}
	return storyAllStoriesDigestState(hidden, domain.DigestStoryPeerList(list.Peers))
}

func storyAllStoriesCursorFromList(list domain.StoryList) (domain.StoryListCursor, bool) {
	if len(list.Peers) == 0 {
		return domain.StoryListCursor{}, false
	}
	peer := list.Peers[len(list.Peers)-1]
	maxDate := 0
	for _, story := range peer.Stories {
		if story.Date > maxDate {
			maxDate = story.Date
		}
	}
	if maxDate <= 0 {
		return domain.StoryListCursor{}, false
	}
	return domain.StoryListCursor{Set: true, Date: maxDate, Peer: peer.Peer}, true
}

func storyAllStoriesCursorState(hidden bool, cursor domain.StoryListCursor) string {
	hiddenBit := "0"
	if hidden {
		hiddenBit = "1"
	}
	return fmt.Sprintf("tsc1:%s:%d:%s:%d", hiddenBit, cursor.Date, cursor.Peer.Type, cursor.Peer.ID)
}

func storyAllStoriesCursorStateToken(state string) bool {
	return strings.HasPrefix(state, "tsc1:")
}

func storyAllStoriesCursorFromState(state string, hidden bool) (domain.StoryListCursor, error) {
	parts := strings.Split(state, ":")
	if len(parts) != 5 || parts[0] != "tsc1" {
		return domain.StoryListCursor{}, domain.ErrStoryOffsetInvalid
	}
	switch parts[1] {
	case "0":
		if hidden {
			return domain.StoryListCursor{}, domain.ErrStoryOffsetInvalid
		}
	case "1":
		if !hidden {
			return domain.StoryListCursor{}, domain.ErrStoryOffsetInvalid
		}
	default:
		return domain.StoryListCursor{}, domain.ErrStoryOffsetInvalid
	}
	date, err := strconv.Atoi(parts[2])
	if err != nil || date <= 0 {
		return domain.StoryListCursor{}, domain.ErrStoryOffsetInvalid
	}
	peer := domain.Peer{Type: domain.PeerType(parts[3])}
	switch peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
	default:
		return domain.StoryListCursor{}, domain.ErrStoryOffsetInvalid
	}
	peerID, err := strconv.ParseInt(parts[4], 10, 64)
	if err != nil || peerID <= 0 {
		return domain.StoryListCursor{}, domain.ErrStoryOffsetInvalid
	}
	peer.ID = peerID
	return domain.StoryListCursor{Set: true, Date: date, Peer: peer}, nil
}

func (r *Router) onStoriesGetPeerStories(ctx context.Context, peer tg.InputPeerClass) (*tg.StoriesPeerStories, error) {
	if err := validateStoriesDirectInputPeer(peer); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	domainPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return r.tgStoriesPeerStories(ctx, userID, domain.PeerStories{Peer: domainPeer}), nil
	}
	stories, err := r.deps.Stories.GetPeerStories(ctx, userID, domainPeer, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, storyErr(err)
	}
	return r.tgStoriesPeerStories(ctx, userID, stories), nil
}

func (r *Router) onStoriesGetStoriesByID(ctx context.Context, req *tg.StoriesGetStoriesByIDRequest) (*tg.StoriesStories, error) {
	if err := validateStoriesGetStoriesByIDRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return &tg.StoriesStories{}, nil
	}
	list, err := r.deps.Stories.GetStoriesByID(ctx, userID, peer, uniqueStoryIDs(req.ID), int(r.clock.Now().Unix()))
	if err != nil {
		return nil, storyErr(err)
	}
	return r.tgStoriesStories(ctx, userID, list), nil
}

func validateStoriesGetStoriesByIDRequest(req *tg.StoriesGetStoriesByIDRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if len(req.ID) == 0 {
		return storyIDEmptyErr()
	}
	if err := validateStoryIDSlice(req.ID); err != nil {
		return err
	}
	return nil
}

func (r *Router) onStoriesGetChatsToSend(ctx context.Context) (tg.MessagesChatsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Channels == nil || userID == 0 {
		return r.applyStoryMaxIDsToChats(ctx, userID, &tg.MessagesChats{Chats: []tg.ChatClass{}}), nil
	}
	channels, err := r.deps.Channels.ListStoryPostableChannels(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	ids := make([]int64, 0, len(channels))
	for _, channel := range channels {
		if channel.ID != 0 {
			ids = append(ids, channel.ID)
		}
	}
	views, err := r.deps.Channels.GetChannels(ctx, userID, ids)
	if err != nil {
		return nil, internalErr()
	}
	chats := make([]tg.ChatClass, 0, len(views))
	for _, view := range views {
		if view.Forbidden {
			continue
		}
		chats = append(chats, tgChannelChatForView(userID, view))
	}
	return r.applyStoryMaxIDsToChats(ctx, userID, &tg.MessagesChats{Chats: chats}), nil
}

func (r *Router) onStoriesGetStoriesArchive(ctx context.Context, req *tg.StoriesGetStoriesArchiveRequest) (*tg.StoriesStories, error) {
	if err := validateStoriesGetStoriesArchiveRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Stories == nil || userID == 0 {
		return tdesktop.StoriesArchive(), nil
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	list, err := r.deps.Stories.GetStoriesArchive(ctx, userID, peer, req.OffsetID, req.Limit, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, storyErr(err)
	}
	return r.tgStoriesStories(ctx, userID, list), nil
}

func validateStoriesGetStoriesArchiveRequest(req *tg.StoriesGetStoriesArchiveRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if err := validateStoryPageBounds(req.OffsetID, req.Limit); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesGetPinnedStories(ctx context.Context, req *tg.StoriesGetPinnedStoriesRequest) (*tg.StoriesStories, error) {
	if err := validateStoriesGetPinnedStoriesRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Stories == nil || userID == 0 {
		return tdesktop.PinnedStories(), nil
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	list, err := r.cachedPinnedStories(ctx, userID, peer, req.OffsetID, req.Limit)
	if err != nil {
		return nil, storyErr(err)
	}
	return r.tgStoriesStories(ctx, userID, list), nil
}

func (r *Router) cachedPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit int) (domain.StoryList, error) {
	if r.storyPinnedListCache == nil {
		return r.deps.Stories.GetPinnedStories(ctx, viewerUserID, peer, offsetID, limit, int(r.clock.Now().Unix()))
	}
	key := storyPinnedStoriesKey(viewerUserID, peer, offsetID, limit)
	return r.storyPinnedListCache.getOrLoad(ctx, key, func() (domain.StoryList, error) {
		return r.deps.Stories.GetPinnedStories(ctx, viewerUserID, peer, key.offsetID, key.limit, int(r.clock.Now().Unix()))
	})
}

func validateStoriesGetPinnedStoriesRequest(req *tg.StoriesGetPinnedStoriesRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if err := validateStoryPageBounds(req.OffsetID, req.Limit); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesExportStoryLink(ctx context.Context, req *tg.StoriesExportStoryLinkRequest) (*tg.ExportedStoryLink, error) {
	if err := validateStoriesExportStoryLinkRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories != nil && userID != 0 {
		list, err := r.deps.Stories.GetStoriesByID(ctx, userID, peer, []int{req.ID}, int(r.clock.Now().Unix()))
		if err != nil {
			return nil, storyErr(err)
		}
		if len(list.Stories) == 0 {
			return nil, storyIDInvalidErr()
		}
	}
	return &tg.ExportedStoryLink{Link: r.storyExportLink(peer, req.ID)}, nil
}

func validateStoriesExportStoryLinkRequest(req *tg.StoriesExportStoryLinkRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.ID <= 0 || req.ID > domain.MaxStoryID {
		return storyIDInvalidErr()
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesReport(ctx context.Context, req *tg.StoriesReportRequest) (tg.ReportResultClass, error) {
	if err := validateStoriesReportRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories != nil && userID != 0 {
		ids := uniqueStoryIDs(req.ID)
		list, err := r.deps.Stories.GetStoriesByID(ctx, userID, peer, ids, int(r.clock.Now().Unix()))
		if err != nil {
			return nil, storyErr(err)
		}
		if len(list.Stories) != len(ids) {
			return nil, storyIDInvalidErr()
		}
	}
	return reportResultForOption(string(req.Option))
}

func validateStoriesReportRequest(req *tg.StoriesReportRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if len(req.ID) == 0 {
		return storyIDEmptyErr()
	}
	if len(req.ID) > domain.MaxStoryIDs || len(req.Option) > maxReportOptionLength || utf8.RuneCountInString(req.Message) > maxReportCommentLength {
		return limitInvalidErr()
	}
	if err := validateStoryIDSlice(req.ID); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesActivateStealthMode(ctx context.Context, req *tg.StoriesActivateStealthModeRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	past, future := storyStealthPastRequested(req), storyStealthFutureRequested(req)
	if !past && !future {
		return nil, inputRequestInvalidErr()
	}
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return tgStoryStealthModeUpdates(int(r.clock.Now().Unix()), past, future), nil
}

func storyStealthPastRequested(req *tg.StoriesActivateStealthModeRequest) bool {
	return req != nil && (req.Past || req.GetPast())
}

func storyStealthFutureRequested(req *tg.StoriesActivateStealthModeRequest) bool {
	return req != nil && (req.Future || req.GetFuture())
}

func (r *Router) onStoriesSearchPosts(ctx context.Context, req *tg.StoriesSearchPostsRequest) (*tg.StoriesFoundStories, error) {
	if err := validateStoriesSearchPostsRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if peer, ok := req.GetPeer(); ok {
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer); err != nil {
			return nil, err
		}
	}
	return &tg.StoriesFoundStories{
		Stories: []tg.FoundStory{},
		Chats:   []tg.ChatClass{},
		Users:   []tg.UserClass{},
	}, nil
}

func validateStoriesSearchPostsRequest(req *tg.StoriesSearchPostsRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.Limit < 0 || req.Limit > domain.MaxStoryListLimit {
		return limitInvalidErr()
	}
	if len(req.Offset) > maxStorySearchPostsOffsetLength {
		return offsetInvalidErr()
	}
	hashtag, hasHashtag := storySearchPostsHashtag(req)
	area, hasArea := storySearchPostsArea(req)
	if hasHashtag == hasArea {
		return searchQueryEmptyErr()
	}
	if hasHashtag {
		hashtag, err := normalizeStorySearchHashtag(hashtag)
		if err != nil {
			return err
		}
		if hashtag == "" {
			return searchQueryEmptyErr()
		}
		if utf8.RuneCountInString(hashtag) > maxChannelSearchPostsQuery {
			return limitInvalidErr()
		}
		return validateStoriesSearchPostsPeer(req)
	}
	if err := validateStorySearchArea(area); err != nil {
		return err
	}
	return validateStoriesSearchPostsPeer(req)
}

func validateStoriesSearchPostsPeer(req *tg.StoriesSearchPostsRequest) error {
	if peer, ok := req.GetPeer(); ok {
		return validateStoriesDirectInputPeer(peer)
	}
	return nil
}

func storySearchPostsHashtag(req *tg.StoriesSearchPostsRequest) (string, bool) {
	hashtag, hasHashtag := req.GetHashtag()
	if !hasHashtag && req.Hashtag != "" {
		hashtag, hasHashtag = req.Hashtag, true
	}
	return hashtag, hasHashtag
}

func storySearchPostsArea(req *tg.StoriesSearchPostsRequest) (tg.MediaAreaClass, bool) {
	area, hasArea := req.GetArea()
	if !hasArea && req.Area != nil {
		area, hasArea = req.Area, true
	}
	return area, hasArea
}

func normalizeStorySearchHashtag(hashtag string) (string, error) {
	hashtag = strings.TrimSpace(hashtag)
	if hashtag == "" {
		return "", nil
	}
	if strings.HasPrefix(hashtag, "#") || strings.HasPrefix(hashtag, "$") {
		hashtag = strings.TrimSpace(hashtag[1:])
	}
	if hashtag == "" {
		return "", nil
	}
	if strings.ContainsAny(hashtag, "#$") {
		return "", limitInvalidErr()
	}
	return hashtag, nil
}

func validateStorySearchArea(area tg.MediaAreaClass) error {
	if storyMediaAreaClassNil(area) {
		return mediaInvalidErr()
	}
	switch typed := area.(type) {
	case *tg.MediaAreaGeoPoint:
		if _, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates); err != nil {
			return err
		}
		if _, err := domainStoryGeoPointFromTL(typed.Geo); err != nil {
			return err
		}
		if _, err := domainStoryGeoPointAddressFromTL(typed); err != nil {
			return err
		}
		return nil
	case *tg.MediaAreaVenue:
		if _, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates); err != nil {
			return err
		}
		_, err := domainStoryVenueFromTL(typed)
		return err
	default:
		return mediaInvalidErr()
	}
}

func (r *Router) onStoriesGetAlbums(ctx context.Context, req *tg.StoriesGetAlbumsRequest) (tg.StoriesAlbumsClass, error) {
	if err := validateStoriesGetAlbumsRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	return tdesktop.StoryAlbums(), nil
}

func validateStoriesGetAlbumsRequest(req *tg.StoriesGetAlbumsRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesGetAlbumStories(ctx context.Context, req *tg.StoriesGetAlbumStoriesRequest) (*tg.StoriesStories, error) {
	if err := validateStoriesGetAlbumStoriesRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	return r.tgStoriesStories(ctx, userID, domain.StoryList{}), nil
}

func validateStoriesGetAlbumStoriesRequest(req *tg.StoriesGetAlbumStoriesRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if err := validateStoryAlbumID(req.AlbumID); err != nil {
		return err
	}
	if req.Offset < 0 || req.Offset > domain.MaxStoryAlbumOffset {
		return offsetInvalidErr()
	}
	if req.Limit < 0 || req.Limit > domain.MaxStoryListLimit {
		return limitInvalidErr()
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesToggleAllStoriesHidden(ctx context.Context, hidden bool) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	r.invalidateStoryProjectionCacheForViewer(userID)
	return true, nil
}

func (r *Router) onStoriesStartLive(ctx context.Context, req *tg.StoriesStartLiveRequest) (tg.UpdatesClass, error) {
	if err := validateStoriesStartLiveRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if _, err := r.storyVisibilityFromInputPrivacyRules(ctx, userID, req.PrivacyRules); err != nil {
		return nil, err
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	return nil, methodInvalidErr()
}

func validateStoriesStartLiveRequest(req *tg.StoriesStartLiveRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.RandomID == 0 {
		return randomIDEmptyErr()
	}
	if err := validateStoryCaptionEntities(req.Caption, req.Entities); err != nil {
		return err
	}
	if stars, ok := req.GetSendPaidMessagesStars(); ok {
		if stars < 0 || stars > maxChannelPaidMessageStars {
			return starsAmountInvalidErr()
		}
	}
	if err := validateStoryInputPrivacyRulesShape(req.PrivacyRules); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func validateStoryInputPrivacyRulesShape(rules []tg.InputPrivacyRuleClass) error {
	for _, rule := range rules {
		if inputPrivacyRuleClassNil(rule) {
			return privacyValueInvalidErr()
		}
		switch typed := rule.(type) {
		case *tg.InputPrivacyValueAllowUsers:
			for _, user := range typed.Users {
				if inputUserClassNil(user) {
					return userIDInvalidErr()
				}
			}
		case *tg.InputPrivacyValueDisallowUsers:
			for _, user := range typed.Users {
				if inputUserClassNil(user) {
					return userIDInvalidErr()
				}
			}
		}
	}
	return nil
}

func (r *Router) onStoriesCreateAlbum(ctx context.Context, req *tg.StoriesCreateAlbumRequest) (*tg.StoryAlbum, error) {
	if err := validateStoriesCreateAlbumRequest(req); err != nil {
		return nil, err
	}
	if _, _, err := r.storyAlbumWriteScope(ctx, req.Peer); err != nil {
		return nil, err
	}
	return nil, methodInvalidErr()
}

func (r *Router) onStoriesUpdateAlbum(ctx context.Context, req *tg.StoriesUpdateAlbumRequest) (*tg.StoryAlbum, error) {
	if err := validateStoriesUpdateAlbumRequest(req); err != nil {
		return nil, err
	}
	if _, _, err := r.storyAlbumWriteScope(ctx, req.Peer); err != nil {
		return nil, err
	}
	return nil, methodInvalidErr()
}

func validateStoriesCreateAlbumRequest(req *tg.StoriesCreateAlbumRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if err := validateStoryAlbumTitle(req.Title); err != nil {
		return err
	}
	if err := validateStoryAlbumStoryIDSlice(req.Stories); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func validateStoriesUpdateAlbumRequest(req *tg.StoriesUpdateAlbumRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if err := validateStoryAlbumID(req.AlbumID); err != nil {
		return err
	}
	if !storyUpdateAlbumHasMutation(req) {
		return inputRequestInvalidErr()
	}
	if title, ok := req.GetTitle(); ok {
		if err := validateStoryAlbumTitle(title); err != nil {
			return err
		}
	}
	if ids, ok := req.GetDeleteStories(); ok {
		if err := validateStoryAlbumUpdateStoryIDSlice(ids); err != nil {
			return err
		}
	}
	if ids, ok := req.GetAddStories(); ok {
		if err := validateStoryAlbumUpdateStoryIDSlice(ids); err != nil {
			return err
		}
	}
	if ids, ok := req.GetOrder(); ok {
		if err := validateStoryAlbumUpdateStoryIDSlice(ids); err != nil {
			return err
		}
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func storyUpdateAlbumHasMutation(req *tg.StoriesUpdateAlbumRequest) bool {
	if req == nil {
		return false
	}
	if _, ok := req.GetTitle(); ok {
		return true
	}
	if _, ok := req.GetDeleteStories(); ok {
		return true
	}
	if _, ok := req.GetAddStories(); ok {
		return true
	}
	if _, ok := req.GetOrder(); ok {
		return true
	}
	return false
}

func (r *Router) onStoriesReorderAlbums(ctx context.Context, req *tg.StoriesReorderAlbumsRequest) (bool, error) {
	if err := validateStoriesReorderAlbumsRequest(req); err != nil {
		return false, err
	}
	if _, _, err := r.storyAlbumWriteScope(ctx, req.Peer); err != nil {
		return false, err
	}
	return true, nil
}

func validateStoriesReorderAlbumsRequest(req *tg.StoriesReorderAlbumsRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if err := validateStoryAlbumIDSlice(req.Order); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesDeleteAlbum(ctx context.Context, req *tg.StoriesDeleteAlbumRequest) (bool, error) {
	if err := validateStoriesDeleteAlbumRequest(req); err != nil {
		return false, err
	}
	if _, _, err := r.storyAlbumWriteScope(ctx, req.Peer); err != nil {
		return false, err
	}
	return true, nil
}

func validateStoriesDeleteAlbumRequest(req *tg.StoriesDeleteAlbumRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if err := validateStoryAlbumID(req.AlbumID); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesGetAllReadPeerStories(ctx context.Context) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	now := int(r.clock.Now().Unix())
	if r.deps.Stories == nil || userID == 0 {
		return tgEmptyUpdates(now), nil
	}
	states, err := r.deps.Stories.ListReadStates(ctx, userID)
	if err != nil {
		return nil, storyErr(err)
	}
	states = normalizeStoryReadStates(userID, states)
	return tgReadStoryUpdates(states, now), nil
}

func normalizeStoryReadStates(viewerUserID int64, states []domain.StoryReadState) []domain.StoryReadState {
	byPeer := make(map[domain.Peer]domain.StoryReadState, len(states))
	for _, state := range states {
		if state.ViewerID != viewerUserID || state.MaxReadID <= 0 || state.MaxReadID > domain.MaxStoryID {
			continue
		}
		if state.Peer.ID == 0 || state.Peer.Type == "" {
			continue
		}
		existing, ok := byPeer[state.Peer]
		if !ok || state.MaxReadID > existing.MaxReadID || (state.MaxReadID == existing.MaxReadID && state.Date > existing.Date) {
			byPeer[state.Peer] = state
		}
	}
	out := make([]domain.StoryReadState, 0, len(byPeer))
	for _, state := range byPeer {
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Peer.Type != out[j].Peer.Type {
			return out[i].Peer.Type < out[j].Peer.Type
		}
		return out[i].Peer.ID < out[j].Peer.ID
	})
	return out
}

func (r *Router) onStoriesGetPeerMaxIDs(ctx context.Context, id []tg.InputPeerClass) ([]tg.RecentStory, error) {
	if len(id) > domain.MaxStoryIDs {
		return nil, storyIDInvalidErr()
	}
	for _, input := range id {
		if err := validateStoriesDirectInputPeer(input); err != nil {
			return nil, err
		}
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peers := make([]domain.Peer, 0, len(id))
	for _, input := range id {
		peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
		if err != nil {
			return nil, err
		}
		peers = append(peers, peer)
	}
	if r.deps.Stories == nil || userID == 0 {
		return make([]tg.RecentStory, len(id)), nil
	}
	recent, err := r.deps.Stories.GetPeerMaxIDs(ctx, userID, peers, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, storyErr(err)
	}
	return tgRecentStories(alignStoryRecentByPeer(peers, recent)), nil
}

func alignStoryRecentByPeer(peers []domain.Peer, recent []domain.RecentStory) []domain.RecentStory {
	byPeer := make(map[domain.Peer]domain.RecentStory, len(recent))
	for _, item := range recent {
		if item.Peer.ID == 0 || item.Peer.Type == "" {
			continue
		}
		existing, ok := byPeer[item.Peer]
		if !ok || item.MaxID > existing.MaxID {
			byPeer[item.Peer] = item
			continue
		}
		if item.Live && !existing.Live {
			existing.Live = true
			byPeer[item.Peer] = existing
		}
	}
	out := make([]domain.RecentStory, len(peers))
	for i, peer := range peers {
		out[i] = domain.RecentStory{Peer: peer}
		if item, ok := byPeer[peer]; ok {
			item.Peer = peer
			out[i] = item
		}
	}
	return out
}

func (r *Router) onStoriesCanSendStory(ctx context.Context, peer tg.InputPeerClass) (*tg.StoriesCanSendStoryCount, error) {
	if err := validateStoriesDirectInputPeer(peer); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	domainPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil {
		if !canSendStoryFallbackPeerAllowed(userID, domainPeer) {
			return nil, peerIDInvalidErr()
		}
		return &tg.StoriesCanSendStoryCount{CountRemains: domain.DefaultStoryCanSendRemaining}, nil
	}
	count, err := r.deps.Stories.CanSendStory(ctx, userID, domainPeer)
	if err != nil {
		return nil, storyErr(err)
	}
	return &tg.StoriesCanSendStoryCount{CountRemains: count}, nil
}

func canSendStoryFallbackPeerAllowed(userID int64, peer domain.Peer) bool {
	return userID != 0 && peer.Type == domain.PeerTypeUser && peer.ID == userID
}

func validateStoriesDirectInputPeer(peer tg.InputPeerClass) error {
	if inputPeerClassNil(peer) {
		return peerIDInvalidErr()
	}
	switch typed := peer.(type) {
	case *tg.InputPeerEmpty:
		return peerIDInvalidErr()
	case *tg.InputPeerChat:
		if typed.ChatID <= 0 {
			return peerIDInvalidErr()
		}
	case *tg.InputPeerUser:
		if typed.UserID <= 0 {
			return peerIDInvalidErr()
		}
	case *tg.InputPeerChannel:
		if typed.ChannelID <= 0 {
			return peerIDInvalidErr()
		}
	case *tg.InputPeerUserFromMessage:
		if typed.UserID <= 0 || typed.MsgID <= 0 {
			return peerIDInvalidErr()
		}
	case *tg.InputPeerChannelFromMessage:
		if typed.ChannelID <= 0 || typed.MsgID <= 0 {
			return peerIDInvalidErr()
		}
	}
	return nil
}

func (r *Router) onStoriesSendStory(ctx context.Context, req *tg.StoriesSendStoryRequest) (tg.UpdatesClass, error) {
	period, err := validateStoriesSendStoryRequest(req)
	if err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return nil, peerIDInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	forward, err := r.domainStoryForwardFromSend(ctx, userID, req, now)
	if err != nil {
		return nil, err
	}
	media, err := r.resolveStoryInputMedia(ctx, userID, req.Media)
	if err != nil {
		return nil, err
	}
	visibility, err := r.storyVisibilityFromInputPrivacyRules(ctx, userID, req.PrivacyRules)
	if err != nil {
		return nil, err
	}
	mediaAreas, err := r.domainStoryMediaAreasFromSend(ctx, userID, peer, req)
	if err != nil {
		return nil, err
	}
	res, err := r.deps.Stories.CreateStory(ctx, userID, domain.StoryCreateRequest{
		Owner:            peer,
		RandomID:         req.RandomID,
		Date:             now,
		Period:           period,
		Pinned:           req.Pinned,
		Public:           visibility.Public,
		CloseFriends:     visibility.CloseFriends,
		Contacts:         visibility.Contacts,
		SelectedContacts: visibility.SelectedContacts,
		NoForwards:       req.Noforwards,
		PrivacyRules:     visibility.Rules,
		AllowUserIDs:     visibility.AllowUserIDs,
		DisallowUserIDs:  visibility.DisallowUserIDs,
		Caption:          req.Caption,
		Entities:         domainMessageEntitiesForViewer(userID, req.Entities),
		Media:            media,
		MediaAreas:       mediaAreas,
		Forward:          forward,
	})
	if err != nil {
		return nil, storyErr(err)
	}
	r.invalidateStoryProjectionCacheForPeer(peer)
	if !res.Duplicate {
		if err := r.recordStoryChange(ctx, userID, res.Story); err != nil {
			return nil, err
		}
		if err := r.fanoutChannelStoryChange(ctx, userID, res.Story); err != nil {
			return nil, err
		}
	}
	return r.tgStoryChangeUpdates(ctx, userID, peer, res.Story, req.RandomID, true, now), nil
}

func validateStoriesSendStoryRequest(req *tg.StoriesSendStoryRequest) (int, error) {
	if req == nil {
		return 0, inputRequestInvalidErr()
	}
	if req.RandomID == 0 {
		return 0, randomIDEmptyErr()
	}
	if err := validateStoryCaptionEntities(req.Caption, req.Entities); err != nil {
		return 0, err
	}
	if err := validateStoryInputMediaClass(req.Media); err != nil {
		return 0, err
	}
	if err := validateStorySendUnsupportedOptions(req); err != nil {
		return 0, err
	}
	return storySendPeriodFromRequest(req)
}

func storySendPeriodFromRequest(req *tg.StoriesSendStoryRequest) (int, error) {
	period, ok := req.GetPeriod()
	if !ok {
		return domain.DefaultStoryPeriod, nil
	}
	switch period {
	case 6 * 3600, 12 * 3600, domain.DefaultStoryPeriod, 2 * domain.DefaultStoryPeriod:
		return period, nil
	default:
		return 0, storyPeriodInvalidErr()
	}
}

func (r *Router) domainStoryForwardFromSend(ctx context.Context, userID int64, req *tg.StoriesSendStoryRequest, now int) (*domain.StoryForward, error) {
	sourceInput, hasSourcePeer := req.GetFwdFromID()
	sourceStoryID, hasSourceStory := req.GetFwdFromStory()
	if !hasSourcePeer && !hasSourceStory {
		if req.FwdModified {
			return nil, storyIDInvalidErr()
		}
		return nil, nil
	}
	if !hasSourcePeer || !hasSourceStory {
		return nil, storyIDInvalidErr()
	}
	if err := validateStorySendForwardPayload(sourceInput, sourceStoryID); err != nil {
		return nil, err
	}
	sourcePeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, sourceInput)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil {
		return nil, storyIDInvalidErr()
	}
	list, err := r.deps.Stories.GetStoriesByID(ctx, userID, sourcePeer, []int{sourceStoryID}, now)
	if err != nil {
		return nil, storyErr(err)
	}
	if len(list.Stories) != 1 || list.Stories[0].ID != sourceStoryID || list.Stories[0].Owner != sourcePeer {
		return nil, storyIDInvalidErr()
	}
	source := list.Stories[0]
	if source.NoForwards {
		return nil, chatForwardsRestrictedErr()
	}
	forward := &domain.StoryForward{
		Source:   source.Owner,
		From:     source.Owner,
		StoryID:  source.ID,
		Modified: req.FwdModified,
	}
	r.applyStoryForwardAuthorPrivacy(ctx, userID, forward)
	return forward, nil
}

func (r *Router) applyStoryForwardAuthorPrivacy(ctx context.Context, forwarderUserID int64, forward *domain.StoryForward) {
	if forward == nil || forward.From.Type != domain.PeerTypeUser || forward.From.ID == 0 || forward.FromName != "" {
		return
	}
	if forward.From.ID == forwarderUserID || r.deps.Privacy == nil {
		return
	}
	allowed, err := r.deps.Privacy.CanSee(ctx, forward.From.ID, forwarderUserID, domain.PrivacyKeyForwards)
	if err != nil || allowed {
		return
	}
	name := r.forwardAuthorDisplayName(ctx, forwarderUserID, forward.From.ID)
	forward.From = domain.Peer{}
	forward.FromName = name
}

func (r *Router) onStoriesEditStory(ctx context.Context, req *tg.StoriesEditStoryRequest) (tg.UpdatesClass, error) {
	if err := validateStoriesEditStoryRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	var media *domain.MessageMedia
	updateMedia := false
	if input, ok := req.GetMedia(); ok {
		updateMedia = true
		media, err = r.resolveStoryInputMedia(ctx, userID, input)
		if err != nil {
			return nil, err
		}
	}
	caption, updateCaption := req.GetCaption()
	entities := req.Entities
	if _, ok := req.GetEntities(); !ok {
		entities = nil
	}
	rules, updatePrivacy := req.GetPrivacyRules()
	visibility, err := r.storyVisibilityFromInputPrivacyRules(ctx, userID, rules)
	if err != nil {
		return nil, err
	}
	mediaAreas, updateMediaAreas, err := r.domainStoryMediaAreasFromEdit(ctx, userID, peer, req)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return nil, peerIDInvalidErr()
	}
	musicOnlyNoop := storyEditHasOnlyEmptyMusic(req, updateMedia, updateCaption, updatePrivacy, updateMediaAreas)
	res, err := r.deps.Stories.EditStory(ctx, userID, domain.StoryEditRequest{
		Owner:            peer,
		ID:               req.ID,
		Media:            media,
		UpdateMedia:      updateMedia,
		Caption:          caption,
		Entities:         domainMessageEntitiesForViewer(userID, entities),
		UpdateCaption:    updateCaption,
		Public:           visibility.Public,
		CloseFriends:     visibility.CloseFriends,
		Contacts:         visibility.Contacts,
		SelectedContacts: visibility.SelectedContacts,
		PrivacyRules:     visibility.Rules,
		AllowUserIDs:     visibility.AllowUserIDs,
		DisallowUserIDs:  visibility.DisallowUserIDs,
		UpdatePrivacy:    updatePrivacy,
		MediaAreas:       mediaAreas,
		UpdateMediaAreas: updateMediaAreas,
	})
	if err != nil {
		if musicOnlyNoop && errors.Is(err, domain.ErrStoryNotModified) {
			return r.tgUnchangedStoryUpdates(ctx, userID, peer, req.ID, int(r.clock.Now().Unix()))
		}
		return nil, storyErr(err)
	}
	r.invalidateStoryProjectionCacheForPeer(peer)
	if err := r.recordStoryChange(ctx, userID, res.Story); err != nil {
		return nil, err
	}
	if err := r.fanoutChannelStoryChange(ctx, userID, res.Story); err != nil {
		return nil, err
	}
	if updatePrivacy {
		if err := r.fanoutStoryPrivacyChange(ctx, userID, res.Previous, res.Story); err != nil {
			return nil, err
		}
	}
	return r.tgStoryChangeUpdates(ctx, userID, peer, res.Story, 0, false, int(r.clock.Now().Unix())), nil
}

func validateStoriesEditStoryRequest(req *tg.StoriesEditStoryRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.ID <= 0 || req.ID > domain.MaxStoryID {
		return storyIDInvalidErr()
	}
	if err := validateStoryCaptionEntities(req.Caption, req.Entities); err != nil {
		return err
	}
	if media, ok := req.GetMedia(); ok {
		if err := validateStoryInputMediaClass(media); err != nil {
			return err
		}
	}
	return validateStoryEditUnsupportedOptions(req)
}

func validateStoryCaptionEntities(caption string, entities []tg.MessageEntityClass) error {
	if utf8.RuneCountInString(caption) > maxSendMessageTextLength {
		return mediaCaptionTooLongErr()
	}
	if len(entities) > maxMessageEntityCount {
		return entitiesTooLongErr()
	}
	if len(entities) == 0 {
		return nil
	}
	limit := utf16CodeUnitLen(caption)
	for _, entity := range entities {
		if messageEntityClassNil(entity) || !storyCaptionEntitySupported(entity) {
			return entityBoundsInvalidErr()
		}
		offset, length := entity.GetOffset(), entity.GetLength()
		if offset < 0 || length <= 0 || offset > limit || length > limit-offset {
			return entityBoundsInvalidErr()
		}
		switch typed := entity.(type) {
		case *tg.MessageEntityCustomEmoji:
			if typed.DocumentID <= 0 {
				return entityBoundsInvalidErr()
			}
		case *tg.InputMessageEntityMentionName:
			if inputUserClassNil(typed.UserID) {
				return userIDInvalidErr()
			}
		}
	}
	return nil
}

func utf16CodeUnitLen(s string) int {
	n := 0
	for _, r := range s {
		if r <= 0xFFFF {
			n++
		} else {
			n += 2
		}
	}
	return n
}

func messageEntityClassNil(entity tg.MessageEntityClass) bool {
	switch typed := entity.(type) {
	case nil:
		return true
	case *tg.MessageEntityUnknown:
		return typed == nil
	case *tg.MessageEntityMention:
		return typed == nil
	case *tg.MessageEntityHashtag:
		return typed == nil
	case *tg.MessageEntityBotCommand:
		return typed == nil
	case *tg.MessageEntityURL:
		return typed == nil
	case *tg.MessageEntityEmail:
		return typed == nil
	case *tg.MessageEntityBold:
		return typed == nil
	case *tg.MessageEntityItalic:
		return typed == nil
	case *tg.MessageEntityCode:
		return typed == nil
	case *tg.MessageEntityPre:
		return typed == nil
	case *tg.MessageEntityTextURL:
		return typed == nil
	case *tg.MessageEntityMentionName:
		return typed == nil
	case *tg.InputMessageEntityMentionName:
		return typed == nil
	case *tg.MessageEntityPhone:
		return typed == nil
	case *tg.MessageEntityCashtag:
		return typed == nil
	case *tg.MessageEntityUnderline:
		return typed == nil
	case *tg.MessageEntityStrike:
		return typed == nil
	case *tg.MessageEntityBankCard:
		return typed == nil
	case *tg.MessageEntitySpoiler:
		return typed == nil
	case *tg.MessageEntityCustomEmoji:
		return typed == nil
	case *tg.MessageEntityBlockquote:
		return typed == nil
	case *tg.MessageEntityFormattedDate:
		return typed == nil
	case *tg.MessageEntityDiffInsert:
		return typed == nil
	case *tg.MessageEntityDiffReplace:
		return typed == nil
	case *tg.MessageEntityDiffDelete:
		return typed == nil
	default:
		return false
	}
}

func storyCaptionEntitySupported(entity tg.MessageEntityClass) bool {
	switch entity.(type) {
	case *tg.MessageEntityMention,
		*tg.MessageEntityHashtag,
		*tg.MessageEntityBotCommand,
		*tg.MessageEntityURL,
		*tg.MessageEntityEmail,
		*tg.MessageEntityBold,
		*tg.MessageEntityItalic,
		*tg.MessageEntityCode,
		*tg.MessageEntityPre,
		*tg.MessageEntityTextURL,
		*tg.MessageEntityMentionName,
		*tg.InputMessageEntityMentionName,
		*tg.MessageEntityPhone,
		*tg.MessageEntityCashtag,
		*tg.MessageEntityUnderline,
		*tg.MessageEntityStrike,
		*tg.MessageEntityBankCard,
		*tg.MessageEntitySpoiler,
		*tg.MessageEntityCustomEmoji,
		*tg.MessageEntityBlockquote,
		*tg.MessageEntityFormattedDate:
		return true
	default:
		return false
	}
}

func validateStorySendUnsupportedOptions(req *tg.StoriesSendStoryRequest) error {
	if albums, ok := req.GetAlbums(); ok && len(albums) > 0 {
		return mediaInvalidErr()
	}
	if music, ok := req.GetMusic(); ok && !storyInputDocumentIsEmpty(music) {
		return documentInvalidErr()
	}
	return validateStorySendForwardFlags(req)
}

func validateStorySendForwardFlags(req *tg.StoriesSendStoryRequest) error {
	sourceInput, hasSourcePeer := req.GetFwdFromID()
	sourceStoryID, hasSourceStory := req.GetFwdFromStory()
	if !hasSourcePeer && !hasSourceStory {
		if req.FwdModified {
			return storyIDInvalidErr()
		}
		return nil
	}
	if !hasSourcePeer || !hasSourceStory {
		return storyIDInvalidErr()
	}
	return validateStorySendForwardPayload(sourceInput, sourceStoryID)
}

func validateStorySendForwardPayload(sourceInput tg.InputPeerClass, sourceStoryID int) error {
	if sourceStoryID <= 0 || sourceStoryID > domain.MaxStoryID {
		return storyIDInvalidErr()
	}
	if err := validateStoriesDirectInputPeer(sourceInput); err != nil {
		return storyIDInvalidErr()
	}
	return nil
}

func validateStoryEditUnsupportedOptions(req *tg.StoriesEditStoryRequest) error {
	if music, ok := req.GetMusic(); ok && !storyInputDocumentIsEmpty(music) {
		return documentInvalidErr()
	}
	return nil
}

func storyInputDocumentIsEmpty(input tg.InputDocumentClass) bool {
	if input == nil {
		return false
	}
	empty, ok := input.(*tg.InputDocumentEmpty)
	return ok && empty != nil
}

func storyEditHasOnlyEmptyMusic(req *tg.StoriesEditStoryRequest, updateMedia, updateCaption, updatePrivacy, updateMediaAreas bool) bool {
	if updateMedia || updateCaption || updatePrivacy || updateMediaAreas {
		return false
	}
	music, ok := req.GetMusic()
	return ok && storyInputDocumentIsEmpty(music)
}

func (r *Router) domainStoryMediaAreasFromSend(ctx context.Context, userID int64, peer domain.Peer, req *tg.StoriesSendStoryRequest) ([]domain.StoryMediaArea, error) {
	areas, ok := req.GetMediaAreas()
	if !ok {
		return nil, nil
	}
	return r.domainStoryMediaAreasFromTL(ctx, userID, peer, areas)
}

func (r *Router) domainStoryMediaAreasFromEdit(ctx context.Context, userID int64, peer domain.Peer, req *tg.StoriesEditStoryRequest) ([]domain.StoryMediaArea, bool, error) {
	areas, ok := req.GetMediaAreas()
	if !ok {
		return nil, false, nil
	}
	out, err := r.domainStoryMediaAreasFromTL(ctx, userID, peer, areas)
	return out, true, err
}

func (r *Router) domainStoryMediaAreasFromTL(ctx context.Context, userID int64, peer domain.Peer, areas []tg.MediaAreaClass) ([]domain.StoryMediaArea, error) {
	if len(areas) == 0 {
		return nil, nil
	}
	if len(areas) > domain.MaxStoryMediaAreas {
		return nil, limitInvalidErr()
	}
	out := make([]domain.StoryMediaArea, 0, len(areas))
	for _, area := range areas {
		if storyMediaAreaClassNil(area) {
			return nil, mediaInvalidErr()
		}
		switch typed := area.(type) {
		case *tg.MediaAreaSuggestedReaction:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			reaction, err := domainStoryReactionValueFromTL(typed.Reaction)
			if err != nil {
				return nil, err
			}
			out = append(out, domain.StoryMediaArea{
				Kind:        domain.StoryMediaAreaSuggestedReaction,
				Coordinates: coords,
				Dark:        typed.Dark,
				Flipped:     typed.Flipped,
				Reaction:    &reaction,
			})
		case *tg.MediaAreaURL:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			if !validStoryMediaAreaURL(typed.URL) {
				return nil, mediaInvalidErr()
			}
			out = append(out, domain.StoryMediaArea{
				Kind:        domain.StoryMediaAreaURL,
				Coordinates: coords,
				URL:         typed.URL,
			})
		case *tg.MediaAreaGeoPoint:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			geo, err := domainStoryGeoPointFromTL(typed.Geo)
			if err != nil {
				return nil, err
			}
			address, err := domainStoryGeoPointAddressFromTL(typed)
			if err != nil {
				return nil, err
			}
			out = append(out, domain.StoryMediaArea{
				Kind:        domain.StoryMediaAreaGeoPoint,
				Coordinates: coords,
				Geo:         geo,
				GeoAddress:  address,
			})
		case *tg.MediaAreaVenue:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			venue, err := domainStoryVenueFromTL(typed)
			if err != nil {
				return nil, err
			}
			out = append(out, domain.StoryMediaArea{
				Kind:        domain.StoryMediaAreaVenue,
				Coordinates: coords,
				Geo:         &venue.Geo,
				Venue:       venue,
			})
		case *tg.InputMediaAreaVenue:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			area, err := r.domainStoryVenueMediaAreaFromInput(ctx, userID, peer, coords, typed)
			if err != nil {
				return nil, err
			}
			out = append(out, area)
		case *tg.MediaAreaWeather:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			if !validStoryWeatherEmoji(typed.Emoji) || !validStoryWeatherTemperature(typed.TemperatureC) {
				return nil, mediaInvalidErr()
			}
			out = append(out, domain.StoryMediaArea{
				Kind:         domain.StoryMediaAreaWeather,
				Coordinates:  coords,
				WeatherEmoji: typed.Emoji,
				TemperatureC: typed.TemperatureC,
				Color:        typed.Color,
			})
		case *tg.MediaAreaChannelPost:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			if !validStoryChannelPost(typed.ChannelID, typed.MsgID) {
				return nil, mediaInvalidErr()
			}
			out = append(out, domain.StoryMediaArea{
				Kind:        domain.StoryMediaAreaChannelPost,
				Coordinates: coords,
				ChannelID:   typed.ChannelID,
				MsgID:       typed.MsgID,
			})
		case *tg.MediaAreaStarGift:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			if !validStoryStarGiftSlug(typed.Slug) {
				return nil, mediaInvalidErr()
			}
			out = append(out, domain.StoryMediaArea{
				Kind:         domain.StoryMediaAreaStarGift,
				Coordinates:  coords,
				StarGiftSlug: typed.Slug,
			})
		case *tg.InputMediaAreaChannelPost:
			coords, err := domainStoryMediaAreaCoordinatesFromTL(typed.Coordinates)
			if err != nil {
				return nil, err
			}
			area, err := r.domainStoryChannelPostMediaAreaFromInput(ctx, userID, coords, typed)
			if err != nil {
				return nil, err
			}
			out = append(out, area)
		default:
			return nil, mediaInvalidErr()
		}
	}
	return out, nil
}

func storyMediaAreaClassNil(area tg.MediaAreaClass) bool {
	switch typed := area.(type) {
	case nil:
		return true
	case *tg.MediaAreaSuggestedReaction:
		return typed == nil
	case *tg.MediaAreaURL:
		return typed == nil
	case *tg.MediaAreaGeoPoint:
		return typed == nil
	case *tg.MediaAreaVenue:
		return typed == nil
	case *tg.InputMediaAreaVenue:
		return typed == nil
	case *tg.MediaAreaWeather:
		return typed == nil
	case *tg.MediaAreaChannelPost:
		return typed == nil
	case *tg.MediaAreaStarGift:
		return typed == nil
	case *tg.InputMediaAreaChannelPost:
		return typed == nil
	default:
		return false
	}
}

func domainStoryMediaAreaCoordinatesFromTL(in tg.MediaAreaCoordinates) (domain.StoryMediaAreaCoordinates, error) {
	radius, hasRadius := in.GetRadius()
	out := domain.StoryMediaAreaCoordinates{
		X:         in.X,
		Y:         in.Y,
		W:         in.W,
		H:         in.H,
		Rotation:  in.Rotation,
		Radius:    radius,
		HasRadius: hasRadius,
	}
	if !storyAreaPercent(out.X, true) ||
		!storyAreaPercent(out.Y, true) ||
		!storyAreaPercent(out.W, false) ||
		!storyAreaPercent(out.H, false) ||
		!storyAreaRotation(out.Rotation) ||
		(hasRadius && !storyAreaPercent(out.Radius, true)) {
		return domain.StoryMediaAreaCoordinates{}, mediaInvalidErr()
	}
	return out, nil
}

func storyAreaPercent(v float64, allowZero bool) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return false
	}
	return allowZero || v > 0
}

func storyAreaRotation(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 360
}

func validStoryMediaAreaURL(raw string) bool {
	return raw != "" &&
		len(raw) <= domain.MaxStoryMediaAreaURLLength &&
		strings.TrimSpace(raw) == raw
}

func validStoryWeatherEmoji(raw string) bool {
	return raw != "" &&
		strings.TrimSpace(raw) == raw &&
		utf8.RuneCountInString(raw) <= domain.MaxStoryWeatherEmojiLength
}

func validStoryWeatherTemperature(v float64) bool {
	return !math.IsNaN(v) &&
		!math.IsInf(v, 0) &&
		v >= minStoryWeatherTemperatureC &&
		v <= maxStoryWeatherTemperatureC
}

func validStoryChannelPost(channelID int64, msgID int) bool {
	return channelID > 0 && msgID > 0 && msgID <= domain.MaxMessageBoxID
}

func validStoryStarGiftSlug(raw string) bool {
	if raw == "" ||
		len(raw) > domain.MaxStoryStarGiftSlugLength ||
		strings.TrimSpace(raw) != raw {
		return false
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' ||
			c == '_' ||
			c == '-' {
			continue
		}
		return false
	}
	return true
}

func (r *Router) domainStoryChannelPostMediaAreaFromInput(ctx context.Context, userID int64, coords domain.StoryMediaAreaCoordinates, area *tg.InputMediaAreaChannelPost) (domain.StoryMediaArea, error) {
	if area == nil || area.Channel == nil || area.MsgID <= 0 || area.MsgID > domain.MaxMessageBoxID {
		return domain.StoryMediaArea{}, mediaInvalidErr()
	}
	if r.deps.Channels == nil {
		return domain.StoryMediaArea{}, channelInvalidErr(domain.ErrChannelInvalid)
	}
	channelID, err := r.channelIDFromInput(ctx, userID, area.Channel)
	if err != nil {
		return domain.StoryMediaArea{}, err
	}
	if !validStoryChannelPost(channelID, area.MsgID) {
		return domain.StoryMediaArea{}, mediaInvalidErr()
	}
	history, err := r.deps.Channels.GetMessages(ctx, userID, channelID, []int{area.MsgID})
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return domain.StoryMediaArea{}, mediaInvalidErr()
		}
		return domain.StoryMediaArea{}, channelInvalidErr(err)
	}
	if len(history.Messages) != 1 || history.Messages[0].ID != area.MsgID {
		return domain.StoryMediaArea{}, mediaInvalidErr()
	}
	return domain.StoryMediaArea{
		Kind:        domain.StoryMediaAreaChannelPost,
		Coordinates: coords,
		ChannelID:   channelID,
		MsgID:       area.MsgID,
	}, nil
}

func domainStoryVenueFromTL(area *tg.MediaAreaVenue) (*domain.MessageVenue, error) {
	if area == nil {
		return nil, mediaInvalidErr()
	}
	geo, err := domainStoryGeoPointFromTL(area.Geo)
	if err != nil {
		return nil, err
	}
	return domainStoryVenueFromValues(*geo, area.Title, area.Address, area.Provider, area.VenueID, area.VenueType)
}

func (r *Router) domainStoryVenueMediaAreaFromInput(ctx context.Context, userID int64, peer domain.Peer, coords domain.StoryMediaAreaCoordinates, area *tg.InputMediaAreaVenue) (domain.StoryMediaArea, error) {
	if area == nil ||
		area.QueryID == 0 ||
		area.ResultID == "" ||
		len(area.ResultID) > domain.MaxBotInlineResultIDLen {
		return domain.StoryMediaArea{}, mediaInvalidErr()
	}
	results, result, ok := r.inlines.resultForSendContext(ctx, r.clock.Now(), userID, area.QueryID, area.ResultID)
	if !ok || !r.inlineResultsAllowPeer(ctx, userID, results, peer) ||
		result.Media == nil ||
		result.Media.Kind != domain.MessageMediaKindVenue ||
		result.Media.Venue == nil {
		return domain.StoryMediaArea{}, mediaInvalidErr()
	}
	venue, err := domainStoryVenueFromValues(result.Media.Venue.Geo, result.Media.Venue.Title, result.Media.Venue.Address, result.Media.Venue.Provider, result.Media.Venue.VenueID, result.Media.Venue.VenueType)
	if err != nil {
		return domain.StoryMediaArea{}, err
	}
	return domain.StoryMediaArea{
		Kind:        domain.StoryMediaAreaVenue,
		Coordinates: coords,
		Geo:         &venue.Geo,
		Venue:       venue,
	}, nil
}

func domainStoryVenueFromValues(geo domain.MessageGeoPoint, title, address, provider, venueID, venueType string) (*domain.MessageVenue, error) {
	if !validStoryDomainGeoPoint(geo) ||
		strings.TrimSpace(title) == "" ||
		utf8.RuneCountInString(title) > maxVenueTitleLength ||
		utf8.RuneCountInString(address) > maxVenueAddressLength ||
		utf8.RuneCountInString(provider) > maxVenueProviderLength ||
		utf8.RuneCountInString(venueID) > maxVenueIDLength ||
		utf8.RuneCountInString(venueType) > maxVenueIDLength {
		return nil, mediaInvalidErr()
	}
	return &domain.MessageVenue{
		Geo:       geo,
		Title:     title,
		Address:   address,
		Provider:  provider,
		VenueID:   venueID,
		VenueType: venueType,
	}, nil
}

func validStoryDomainGeoPoint(geo domain.MessageGeoPoint) bool {
	return !math.IsNaN(geo.Lat) &&
		!math.IsInf(geo.Lat, 0) &&
		!math.IsNaN(geo.Long) &&
		!math.IsInf(geo.Long, 0) &&
		geo.Lat >= -90 &&
		geo.Lat <= 90 &&
		geo.Long >= -180 &&
		geo.Long <= 180
}

func domainStoryGeoPointFromTL(geo tg.GeoPointClass) (*domain.MessageGeoPoint, error) {
	point, ok := geo.(*tg.GeoPoint)
	if !ok || point == nil ||
		math.IsNaN(point.Lat) || math.IsInf(point.Lat, 0) ||
		math.IsNaN(point.Long) || math.IsInf(point.Long, 0) ||
		point.Lat < -90 || point.Lat > 90 ||
		point.Long < -180 || point.Long > 180 {
		return nil, mediaInvalidErr()
	}
	accuracy, _ := point.GetAccuracyRadius()
	if accuracy < 0 || accuracy > maxGeoAccuracyRadiusMeters {
		accuracy = 0
	}
	return &domain.MessageGeoPoint{
		Lat:            point.Lat,
		Long:           point.Long,
		AccessHash:     point.AccessHash,
		AccuracyRadius: accuracy,
	}, nil
}

func domainStoryGeoPointAddressFromTL(area *tg.MediaAreaGeoPoint) (*domain.StoryGeoPointAddress, error) {
	if area == nil {
		return nil, nil
	}
	address, ok := area.GetAddress()
	if !ok {
		return nil, nil
	}
	if !validStoryGeoCountry(address.CountryISO2) ||
		!validStoryGeoAddressPart(address.State) ||
		!validStoryGeoAddressPart(address.City) ||
		!validStoryGeoAddressPart(address.Street) {
		return nil, mediaInvalidErr()
	}
	return &domain.StoryGeoPointAddress{
		CountryISO2: address.CountryISO2,
		State:       address.State,
		City:        address.City,
		Street:      address.Street,
	}, nil
}

func validStoryGeoCountry(raw string) bool {
	return utf8.RuneCountInString(raw) == 2 && strings.TrimSpace(raw) == raw
}

func validStoryGeoAddressPart(raw string) bool {
	return utf8.RuneCountInString(raw) <= domain.MaxStoryGeoAddressPartLength &&
		strings.TrimSpace(raw) == raw
}

func (r *Router) tgUnchangedStoryUpdates(ctx context.Context, viewerUserID int64, peer domain.Peer, storyID, date int) (tg.UpdatesClass, error) {
	list, err := r.deps.Stories.GetStoriesByID(ctx, viewerUserID, peer, []int{storyID}, date)
	if err != nil {
		return nil, storyErr(err)
	}
	if len(list.Stories) == 0 {
		return nil, storyIDInvalidErr()
	}
	return r.tgStoryChangeUpdates(ctx, viewerUserID, peer, list.Stories[0], 0, false, date), nil
}

type storyPrivacyFanoutFacts struct {
	isContact    bool
	closeFriend  bool
	storyBlocked bool
}

func (r *Router) fanoutStoryPrivacyChange(ctx context.Context, ownerID int64, before, after domain.Story) error {
	if ownerID == 0 || r.deps.Stories == nil || r.deps.Updates == nil {
		return nil
	}
	if before.Owner != after.Owner || before.ID != after.ID || after.Owner.Type != domain.PeerTypeUser || after.Owner.ID != ownerID {
		return nil
	}
	now := int(r.clock.Now().Unix())
	beforeActive := before.Active(now)
	afterActive := after.Active(now)
	if !beforeActive && !afterActive {
		return nil
	}
	candidates := make(map[int64]storyPrivacyFanoutFacts)
	addCandidate := func(userID int64, facts storyPrivacyFanoutFacts) {
		if userID == 0 || userID == ownerID {
			return
		}
		existing, ok := candidates[userID]
		if !ok && len(candidates) >= domain.MaxStoryPrivacyFanoutTargets {
			return
		}
		facts.isContact = facts.isContact || existing.isContact
		facts.closeFriend = facts.closeFriend || existing.closeFriend
		facts.storyBlocked = facts.storyBlocked || existing.storyBlocked
		candidates[userID] = facts
	}
	addCandidateIDs := func(ids []int64) {
		for _, userID := range ids {
			addCandidate(userID, storyPrivacyFanoutFacts{})
		}
	}
	addCandidateIDs(before.AllowUserIDs)
	addCandidateIDs(after.AllowUserIDs)
	addCandidateIDs(before.DisallowUserIDs)
	addCandidateIDs(after.DisallowUserIDs)
	if r.deps.Contacts != nil {
		list, _, err := r.deps.Contacts.GetContacts(ctx, ownerID, 0)
		if err != nil {
			return internalErr()
		}
		for _, contact := range list.Contacts {
			contactUserID := contact.User.ID
			addCandidate(contactUserID, storyPrivacyFanoutFacts{
				isContact:   true,
				closeFriend: contact.CloseFriend || contact.User.CloseFriend,
			})
		}
	}
	viewerIDs, err := r.deps.Stories.ListStoryViewerIDs(ctx, ownerID, after.Owner, after.ID, domain.MaxStoryPrivacyFanoutTargets)
	if err != nil {
		return storyErr(err)
	}
	addCandidateIDs(viewerIDs)
	candidateIDs := make([]int64, 0, len(candidates))
	for userID := range candidates {
		candidateIDs = append(candidateIDs, userID)
	}
	blockedFacts, err := r.storyBlockedFactsForUsers(ctx, ownerID, candidateIDs)
	if err != nil {
		return err
	}
	for userID, blocked := range blockedFacts {
		facts := candidates[userID]
		facts.storyBlocked = blocked
		candidates[userID] = facts
	}
	visible := func(story domain.Story, active bool, userID int64, facts storyPrivacyFanoutFacts) bool {
		return active && story.VisibleToWithStoryFacts(userID, facts.isContact, facts.closeFriend, facts.storyBlocked)
	}
	for userID, facts := range candidates {
		beforeVisible := visible(before, beforeActive, userID, facts)
		afterVisible := visible(after, afterActive, userID, facts)
		switch {
		case afterVisible:
			if err := r.recordStoryFanout(ctx, userID, storyFanoutSnapshot(after)); err != nil {
				return err
			}
		case beforeVisible:
			deleted := storyFanoutSnapshot(after)
			deleted.Deleted = true
			if err := r.recordStoryFanout(ctx, userID, deleted); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Router) fanoutDeletedUserStory(ctx context.Context, ownerID int64, before, deleted domain.Story, now int) error {
	if ownerID == 0 || r.deps.Stories == nil || r.deps.Updates == nil {
		return nil
	}
	if before.ID == 0 {
		before = deleted
		before.Deleted = false
	}
	if deleted.ID == 0 {
		deleted = before
		deleted.Deleted = true
	}
	if before.Owner.Type != domain.PeerTypeUser || before.Owner.ID != ownerID || before.ID <= 0 {
		return nil
	}
	if deleted.Owner != before.Owner || deleted.ID != before.ID {
		return nil
	}
	if !before.Active(now) && !before.Pinned {
		return nil
	}
	candidates := make(map[int64]storyPrivacyFanoutFacts)
	addCandidate := func(userID int64, facts storyPrivacyFanoutFacts) {
		if userID == 0 || userID == ownerID {
			return
		}
		existing, ok := candidates[userID]
		if !ok && len(candidates) >= domain.MaxStoryPrivacyFanoutTargets {
			return
		}
		facts.isContact = facts.isContact || existing.isContact
		facts.closeFriend = facts.closeFriend || existing.closeFriend
		facts.storyBlocked = facts.storyBlocked || existing.storyBlocked
		candidates[userID] = facts
	}
	addCandidateIDs := func(ids []int64) {
		for _, userID := range ids {
			addCandidate(userID, storyPrivacyFanoutFacts{})
		}
	}
	addCandidateIDs(before.AllowUserIDs)
	addCandidateIDs(before.DisallowUserIDs)
	if r.deps.Contacts != nil {
		list, _, err := r.deps.Contacts.GetContacts(ctx, ownerID, 0)
		if err != nil {
			return internalErr()
		}
		for _, contact := range list.Contacts {
			contactUserID := contact.User.ID
			addCandidate(contactUserID, storyPrivacyFanoutFacts{
				isContact:   true,
				closeFriend: contact.CloseFriend || contact.User.CloseFriend,
			})
		}
	}
	viewerIDs, err := r.deps.Stories.ListStoryViewerIDs(ctx, ownerID, before.Owner, before.ID, domain.MaxStoryPrivacyFanoutTargets)
	if err != nil {
		return storyErr(err)
	}
	addCandidateIDs(viewerIDs)
	candidateIDs := make([]int64, 0, len(candidates))
	for userID := range candidates {
		candidateIDs = append(candidateIDs, userID)
	}
	blockedFacts, err := r.storyBlockedFactsForUsers(ctx, ownerID, candidateIDs)
	if err != nil {
		return err
	}
	for userID, blocked := range blockedFacts {
		facts := candidates[userID]
		facts.storyBlocked = blocked
		candidates[userID] = facts
	}
	deleted = storyFanoutSnapshot(deleted)
	deleted.Deleted = true
	for userID, facts := range candidates {
		if before.VisibleToWithStoryFacts(userID, facts.isContact, facts.closeFriend, facts.storyBlocked) {
			if err := r.recordStoryFanout(ctx, userID, deleted); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Router) fanoutUnpinnedExpiredUserStory(ctx context.Context, ownerID int64, before, after domain.Story, now int) error {
	if before.ID == 0 || after.ID == 0 {
		return nil
	}
	if before.Owner != after.Owner || before.ID != after.ID {
		return nil
	}
	if !before.Pinned || before.Active(now) || after.Active(now) || after.Pinned || after.Deleted {
		return nil
	}
	return r.fanoutDeletedUserStory(ctx, ownerID, before, after, now)
}

func (r *Router) fanoutChannelStoryChange(ctx context.Context, originUserID int64, story domain.Story) error {
	if originUserID == 0 ||
		story.Owner.Type != domain.PeerTypeChannel ||
		story.Owner.ID == 0 ||
		r.deps.Channels == nil ||
		r.deps.Updates == nil {
		return nil
	}
	memberIDs, err := r.deps.Channels.ActiveMemberIDs(ctx, originUserID, story.Owner.ID, domain.MaxChannelRealtimeFanout)
	if err != nil {
		return internalErr()
	}
	snapshot := storyFanoutSnapshot(story)
	for _, userID := range memberIDs {
		if userID == 0 || userID == originUserID {
			continue
		}
		if err := r.recordStoryFanout(ctx, userID, snapshot); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) onStoriesDeleteStories(ctx context.Context, req *tg.StoriesDeleteStoriesRequest) ([]int, error) {
	ids, err := validateStoriesDeleteStoriesRequest(req)
	if err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	if r.deps.Stories == nil || userID == 0 {
		return append([]int(nil), ids...), nil
	}
	res, err := r.deps.Stories.DeleteStories(ctx, userID, peer, ids, now)
	if err != nil {
		return nil, storyErr(err)
	}
	r.invalidateStoryProjectionCacheForPeer(peer)
	previousByID := make(map[int]domain.Story, len(res.Previous))
	for _, story := range res.Previous {
		previousByID[story.ID] = story
	}
	for _, story := range res.Stories {
		if err := r.recordStoryChange(ctx, userID, story); err != nil {
			return nil, err
		}
		if err := r.fanoutDeletedUserStory(ctx, userID, previousByID[story.ID], story, now); err != nil {
			return nil, err
		}
		if err := r.fanoutChannelStoryChange(ctx, userID, story); err != nil {
			return nil, err
		}
	}
	return res.IDs, nil
}

func validateStoriesDeleteStoriesRequest(req *tg.StoriesDeleteStoriesRequest) ([]int, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if len(req.ID) == 0 {
		return nil, storyIDEmptyErr()
	}
	if err := validateStoryIDSlice(req.ID); err != nil {
		return nil, err
	}
	if err := validateStoriesDirectInputPeer(req.Peer); err != nil {
		return nil, err
	}
	return uniqueStoryIDs(req.ID), nil
}

func (r *Router) onStoriesTogglePinned(ctx context.Context, req *tg.StoriesTogglePinnedRequest) ([]int, error) {
	ids, err := validateStoriesTogglePinnedRequest(req)
	if err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	if r.deps.Stories == nil || userID == 0 {
		return append([]int(nil), ids...), nil
	}
	res, err := r.deps.Stories.TogglePinned(ctx, userID, peer, ids, req.Pinned, now)
	if err != nil {
		return nil, storyErr(err)
	}
	r.invalidateStoryProjectionCacheForPeer(peer)
	previousByID := make(map[int]domain.Story, len(res.Previous))
	for _, story := range res.Previous {
		previousByID[story.ID] = story
	}
	for _, story := range res.Stories {
		if err := r.recordStoryChange(ctx, userID, story); err != nil {
			return nil, err
		}
		if err := r.fanoutUnpinnedExpiredUserStory(ctx, userID, previousByID[story.ID], story, now); err != nil {
			return nil, err
		}
		if err := r.fanoutChannelStoryChange(ctx, userID, story); err != nil {
			return nil, err
		}
	}
	return res.IDs, nil
}

func validateStoriesTogglePinnedRequest(req *tg.StoriesTogglePinnedRequest) ([]int, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if len(req.ID) > 0 {
		if err := validateStoryIDSlice(req.ID); err != nil {
			return nil, err
		}
	}
	if err := validateStoriesDirectInputPeer(req.Peer); err != nil {
		return nil, err
	}
	if len(req.ID) == 0 {
		return []int{}, nil
	}
	return uniqueStoryIDs(req.ID), nil
}

func (r *Router) onStoriesTogglePinnedToTop(ctx context.Context, req *tg.StoriesTogglePinnedToTopRequest) (bool, error) {
	ids, err := validateStoriesTogglePinnedToTopRequest(req)
	if err != nil {
		return false, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return true, nil
	}
	if err := r.deps.Stories.TogglePinnedToTop(ctx, userID, peer, ids); err != nil {
		return false, storyErr(err)
	}
	r.invalidateStoryProjectionCacheForPeer(peer)
	return true, nil
}

func validateStoriesTogglePinnedToTopRequest(req *tg.StoriesTogglePinnedToTopRequest) ([]int, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if len(req.ID) > 0 {
		if err := validateStoryIDSlice(req.ID); err != nil {
			return nil, err
		}
	}
	ids := uniqueStoryIDs(req.ID)
	if len(ids) > domain.MaxStoryPinnedToTop {
		return nil, storyIDInvalidErr()
	}
	if err := validateStoriesDirectInputPeer(req.Peer); err != nil {
		return nil, err
	}
	return ids, nil
}

func (r *Router) onStoriesReadStories(ctx context.Context, req *tg.StoriesReadStoriesRequest) ([]int, error) {
	if err := validateStoriesReadStoriesRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return []int{}, nil
	}
	now := int(r.clock.Now().Unix())
	read, err := r.deps.Stories.ReadStories(ctx, userID, peer, req.MaxID, now)
	if err != nil {
		return nil, storyErr(err)
	}
	if read.Advanced && r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		if _, _, err := r.deps.Updates.RecordReadStories(ctx, authKeyID, userID, read, rawAuthKeyIDForOrigin(ctx), sessionID); err != nil {
			return nil, internalErr()
		}
	}
	if read.MaxReadID <= 0 {
		return []int{}, nil
	}
	return []int{read.MaxReadID}, nil
}

func validateStoriesReadStoriesRequest(req *tg.StoriesReadStoriesRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.MaxID <= 0 || req.MaxID > domain.MaxStoryID {
		return storyIDInvalidErr()
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesIncrementStoryViews(ctx context.Context, req *tg.StoriesIncrementStoryViewsRequest) (bool, error) {
	if err := validateStoriesIncrementStoryViewsRequest(req); err != nil {
		return false, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return true, nil
	}
	if _, err := r.deps.Stories.IncrementViews(ctx, userID, peer, uniqueStoryIDs(req.ID), int(r.clock.Now().Unix())); err != nil {
		return false, storyErr(err)
	}
	// 浏览计数变化主要影响作者自己查看自己故事时的 ViewsCount。view 增量高频，
	// 只失效作者自视角这一条缓存，避免按 peer 全量失效把热门故事的缓存打穿。
	if peer.Type == domain.PeerTypeUser {
		r.invalidateStoryProjectionCache(peer.ID, peer)
	}
	return true, nil
}

func validateStoriesIncrementStoryViewsRequest(req *tg.StoriesIncrementStoryViewsRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if len(req.ID) == 0 {
		return storyIDEmptyErr()
	}
	if err := validateStoryIDSlice(req.ID); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesGetStoriesViews(ctx context.Context, req *tg.StoriesGetStoriesViewsRequest) (*tg.StoriesStoryViews, error) {
	if err := validateStoriesGetStoriesViewsRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return tgEmptyStoriesStoryViews(len(req.ID)), nil
	}
	list, err := r.deps.Stories.GetStoriesByID(ctx, userID, peer, req.ID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, storyErr(err)
	}
	r.addStoryMessageForwardCounts(ctx, userID, list.Stories)
	return r.tgStoriesStoryViewsForIDs(ctx, userID, peer, req.ID, list.Stories), nil
}

func validateStoriesGetStoriesViewsRequest(req *tg.StoriesGetStoriesViewsRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if len(req.ID) == 0 {
		return storyIDEmptyErr()
	}
	if err := validateStoryIDSlice(req.ID); err != nil {
		return err
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesGetStoryViewsList(ctx context.Context, req *tg.StoriesGetStoryViewsListRequest) (*tg.StoriesStoryViewsList, error) {
	if err := validateStoriesGetStoryViewsListRequest(req); err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return r.tgStoryViewsList(ctx, userID, domain.StoryViewList{}), nil
	}
	q, _ := storyViewsListQuery(req)
	list, err := r.deps.Stories.GetStoryViewsList(ctx, userID, domain.StoryViewListRequest{
		Owner:          peer,
		StoryID:        req.ID,
		Offset:         req.Offset,
		Limit:          req.Limit,
		Query:          q,
		JustContacts:   req.GetJustContacts(),
		ReactionsFirst: req.GetReactionsFirst(),
		ForwardsFirst:  req.GetForwardsFirst(),
	})
	if err != nil {
		return nil, storyErr(err)
	}
	if q == "" && !req.GetJustContacts() {
		list = r.withStoryMessageForwardViews(ctx, userID, list, domain.StoryMessageForwardListRequest{
			Owner:          peer,
			StoryID:        req.ID,
			Offset:         req.Offset,
			Limit:          req.Limit,
			ReactionsFirst: req.GetReactionsFirst(),
			ForwardsFirst:  req.GetForwardsFirst(),
		})
	}
	return r.tgStoryViewsList(ctx, userID, list), nil
}

func validateStoriesGetStoryViewsListRequest(req *tg.StoriesGetStoryViewsListRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.ID <= 0 || req.ID > domain.MaxStoryID {
		return storyIDInvalidErr()
	}
	if err := validateStoryInteractionListLimit(req.Limit); err != nil {
		return err
	}
	if err := domain.ValidateStoryInteractionOffset(req.Offset, false); err != nil {
		return storyErr(err)
	}
	if q, ok := storyViewsListQuery(req); ok && utf8.RuneCountInString(q) > domain.MaxStoryViewQueryLength {
		return limitInvalidErr()
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func storyViewsListQuery(req *tg.StoriesGetStoryViewsListRequest) (string, bool) {
	if req == nil {
		return "", false
	}
	q, ok := req.GetQ()
	if !ok && req.Q != "" {
		q, ok = req.Q, true
	}
	return q, ok
}

func (r *Router) onStoriesGetStoryReactionsList(ctx context.Context, req *tg.StoriesGetStoryReactionsListRequest) (*tg.StoriesStoryReactionsList, error) {
	reaction, offset, err := validateStoriesGetStoryReactionsListRequest(req)
	if err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	if err := r.requireChannelStoryInteractionsAdmin(ctx, userID, peer.ID); err != nil {
		return nil, err
	}
	if r.deps.Stories == nil || userID == 0 {
		return r.tgStoryReactionsList(ctx, userID, domain.StoryReactionList{}), nil
	}
	list, err := r.deps.Stories.GetStoryReactionsList(ctx, userID, domain.StoryReactionListRequest{
		Owner:                    peer,
		StoryID:                  req.ID,
		Reaction:                 reaction,
		Offset:                   offset,
		Limit:                    req.Limit,
		ForwardsFirst:            req.GetForwardsFirst(),
		CanViewOwnerInteractions: true,
	})
	if err != nil {
		return nil, storyErr(err)
	}
	if reaction == nil {
		list = r.withStoryMessageForwardReactions(ctx, userID, list, domain.StoryMessageForwardListRequest{
			Owner:         peer,
			StoryID:       req.ID,
			Offset:        offset,
			Limit:         req.Limit,
			ForwardsFirst: req.GetForwardsFirst(),
		})
	}
	return r.tgStoryReactionsList(ctx, userID, list), nil
}

func validateStoriesGetStoryReactionsListRequest(req *tg.StoriesGetStoryReactionsListRequest) (*domain.MessageReaction, string, error) {
	if req == nil {
		return nil, "", inputRequestInvalidErr()
	}
	if req.ID <= 0 || req.ID > domain.MaxStoryID {
		return nil, "", storyIDInvalidErr()
	}
	if err := validateStoryInteractionListLimit(req.Limit); err != nil {
		return nil, "", err
	}
	var reaction *domain.MessageReaction
	if inputReaction, ok := req.GetReaction(); ok {
		parsed, err := domainStoryReactionFromTL(inputReaction)
		if err != nil {
			return nil, "", err
		}
		reaction = parsed
	}
	offset, _ := req.GetOffset()
	if err := domain.ValidateStoryReactionInteractionOffset(offset, req.GetForwardsFirst()); err != nil {
		return nil, "", storyErr(err)
	}
	if err := validateStoriesDirectInputPeer(req.Peer); err != nil {
		return nil, "", err
	}
	return reaction, offset, nil
}

func (r *Router) requireChannelStoryInteractionsAdmin(ctx context.Context, userID, channelID int64) error {
	if userID == 0 || channelID == 0 || r.deps.Channels == nil {
		return channelInvalidErr(domain.ErrChannelInvalid)
	}
	member, err := r.deps.Channels.GetParticipant(ctx, userID, channelID, userID)
	if err != nil {
		return channelInvalidErr(err)
	}
	if member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin {
		return nil
	}
	return channelInvalidErr(domain.ErrChannelAdminRequired)
}

func (r *Router) onStoriesTogglePeerStoriesHidden(ctx context.Context, req *tg.StoriesTogglePeerStoriesHiddenRequest) (bool, error) {
	if err := validateStoriesTogglePeerStoriesHiddenRequest(req); err != nil {
		return false, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if r.deps.Stories != nil && userID != 0 {
		if err := r.deps.Stories.TogglePeerStoriesHidden(ctx, userID, peer, req.Hidden); err != nil {
			return false, storyErr(err)
		}
	}
	r.invalidateStoryProjectionCache(userID, peer)
	return true, nil
}

func validateStoriesTogglePeerStoriesHiddenRequest(req *tg.StoriesTogglePeerStoriesHiddenRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	return validateStoriesDirectInputPeer(req.Peer)
}

func (r *Router) onStoriesSendReaction(ctx context.Context, req *tg.StoriesSendReactionRequest) (tg.UpdatesClass, error) {
	reaction, err := validateStoriesSendReactionRequest(req)
	if err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	if r.deps.Stories == nil || userID == 0 {
		return r.tgStoryReactionUpdates(ctx, userID, peer, req.StoryID, req.Reaction, now), nil
	}
	res, err := r.deps.Stories.SendReaction(ctx, userID, peer, req.StoryID, reaction, now)
	if err != nil {
		return nil, storyErr(err)
	}
	if res.Changed {
		// 反应改变了该故事的 reaction 计数(所有 viewer 可见)与本 viewer 的 SentReaction，
		// 必须失效该 peer 的故事投影缓存，否则 getPinnedStories 会命中陈旧页(陈旧 SentReaction/计数)。
		r.invalidateStoryProjectionCacheForPeer(peer)
	}
	if reaction != nil && res.Changed {
		if err := r.recordMessageReactionUse(ctx, userID, []domain.MessageReaction{*reaction}, req.GetAddToRecent(), now); err != nil {
			return nil, internalErr()
		}
	}
	if res.Changed && r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		if _, _, err := r.deps.Updates.RecordSentStoryReaction(ctx, authKeyID, userID, res, rawAuthKeyIDForOrigin(ctx), sessionID); err != nil {
			return nil, internalErr()
		}
		if ownerUserID, ok := ownerStoryReactionNotificationUserID(res, userID); ok && res.Reaction != nil {
			event, _, err := r.deps.Updates.RecordNewStoryReaction(ctx, [8]byte{}, ownerUserID, res, [8]byte{}, 0)
			if err != nil {
				return nil, internalErr()
			}
			if updates := r.BuildOutboxUpdates(ctx, []OutboxUpdateRequest{{
				TargetUserID: ownerUserID,
				Event:        event,
			}}); len(updates) == 1 && updates[0] != nil {
				r.pushUserUpdatesIfNoReliableDispatch(ctx, ownerUserID, updates[0])
			}
		}
	}
	return r.tgStoryReactionUpdates(ctx, userID, peer, req.StoryID, req.Reaction, now), nil
}

func validateStoriesSendReactionRequest(req *tg.StoriesSendReactionRequest) (*domain.MessageReaction, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if req.StoryID <= 0 || req.StoryID > domain.MaxStoryID {
		return nil, storyIDInvalidErr()
	}
	reaction, err := domainStoryReactionFromTL(req.Reaction)
	if err != nil {
		return nil, err
	}
	if err := validateStoriesDirectInputPeer(req.Peer); err != nil {
		return nil, err
	}
	return reaction, nil
}

func ownerStoryReactionNotificationUserID(res domain.StoryReactionResult, viewerUserID int64) (int64, bool) {
	owner := res.Story.Owner
	if owner.ID == 0 {
		owner = res.Peer
	}
	if owner.Type != domain.PeerTypeUser || owner.ID == 0 || owner.ID == viewerUserID {
		return 0, false
	}
	return owner.ID, true
}

type storyVisibility struct {
	Public           bool
	CloseFriends     bool
	Contacts         bool
	SelectedContacts bool
	Rules            []domain.PrivacyRule
	AllowUserIDs     []int64
	DisallowUserIDs  []int64
}

func (r *Router) storyVisibilityFromInputPrivacyRules(ctx context.Context, userID int64, rules []tg.InputPrivacyRuleClass) (storyVisibility, error) {
	domainRules, err := r.domainPrivacyRulesFromInput(ctx, userID, rules)
	if err != nil {
		return storyVisibility{}, err
	}
	return storyVisibilityFromDomainPrivacyRules(domainRules), nil
}

func storyVisibilityFromDomainPrivacyRules(rules []domain.PrivacyRule) storyVisibility {
	out := storyVisibility{Rules: cloneDomainPrivacyRules(rules)}
	for i, rule := range rules {
		if i == 0 {
			switch rule.Kind {
			case domain.PrivacyRuleAllowAll:
				out.Public = true
			case domain.PrivacyRuleAllowContacts:
				out.Contacts = true
			case domain.PrivacyRuleAllowUsers:
				out.SelectedContacts = true
			case domain.PrivacyRuleAllowCloseFriends:
				out.CloseFriends = true
			}
		}
		switch rule.Kind {
		case domain.PrivacyRuleAllowUsers:
			out.AllowUserIDs = appendUniquePositiveInt64(out.AllowUserIDs, rule.UserIDs...)
		case domain.PrivacyRuleDisallowUsers:
			out.DisallowUserIDs = appendUniquePositiveInt64(out.DisallowUserIDs, rule.UserIDs...)
		case domain.PrivacyRuleDisallowAll:
			out.Public = false
		}
	}
	return out
}

func cloneDomainPrivacyRules(in []domain.PrivacyRule) []domain.PrivacyRule {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.PrivacyRule, len(in))
	for i, rule := range in {
		out[i] = rule
		out[i].UserIDs = append([]int64(nil), rule.UserIDs...)
		out[i].ChatIDs = append([]int64(nil), rule.ChatIDs...)
	}
	return out
}

func appendUniquePositiveInt64(base []int64, ids ...int64) []int64 {
	seen := make(map[int64]struct{}, len(base)+len(ids))
	out := make([]int64, 0, len(base)+len(ids))
	for _, id := range base {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (r *Router) resolveStoryInputMedia(ctx context.Context, userID int64, input tg.InputMediaClass) (*domain.MessageMedia, error) {
	if err := validateStoryInputMediaClass(input); err != nil {
		return nil, err
	}
	media, err := r.resolveInputMedia(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	if media == nil {
		return nil, mediaEmptyErr()
	}
	switch media.Kind {
	case domain.MessageMediaKindPhoto, domain.MessageMediaKindDocument:
		return media, nil
	default:
		return nil, mediaTypeInvalidErr()
	}
}

func validateStoryInputMediaClass(input tg.InputMediaClass) error {
	switch typed := input.(type) {
	case nil:
		return mediaEmptyErr()
	case *tg.InputMediaEmpty:
		return mediaEmptyErr()
	case *tg.InputMediaUploadedPhoto:
		if typed == nil {
			return mediaEmptyErr()
		}
		if typed.File == nil {
			return mediaInvalidErr()
		}
	case *tg.InputMediaUploadedDocument:
		if typed == nil {
			return mediaEmptyErr()
		}
		if typed.File == nil {
			return mediaInvalidErr()
		}
	case *tg.InputMediaPhoto:
		if typed == nil {
			return mediaEmptyErr()
		}
		if _, ok := inputPhotoID(typed.ID); !ok {
			return photoInvalidErr()
		}
	case *tg.InputMediaDocument:
		if typed == nil {
			return mediaEmptyErr()
		}
		if _, ok := inputDocumentCandidateIDs(typed.ID); !ok {
			return mediaInvalidErr()
		}
	default:
		return mediaTypeInvalidErr()
	}
	return nil
}

func validateStoryIDSlice(ids []int) error {
	if len(ids) > domain.MaxStoryIDs {
		return storyIDInvalidErr()
	}
	for _, id := range ids {
		if id <= 0 || id > domain.MaxStoryID {
			return storyIDInvalidErr()
		}
	}
	return nil
}

func validateStoryPageBounds(offsetID, limit int) error {
	if offsetID < -1 || offsetID > domain.MaxStoryID {
		return storyIDInvalidErr()
	}
	if limit < 0 || limit > domain.MaxStoryListLimit {
		return limitInvalidErr()
	}
	return nil
}

func validateStoryInteractionListLimit(limit int) error {
	if limit < 0 || limit > domain.MaxStoryListLimit {
		return limitInvalidErr()
	}
	return nil
}

func validateStoryAlbumID(id int) error {
	if id <= 0 || id > domain.MaxStoryID {
		return inputRequestInvalidErr()
	}
	return nil
}

func validateStoryAlbumIDSlice(ids []int) error {
	if len(ids) > domain.MaxStoryIDs {
		return limitInvalidErr()
	}
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if err := validateStoryAlbumID(id); err != nil {
			return err
		}
		if _, ok := seen[id]; ok {
			return inputRequestInvalidErr()
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateStoryAlbumStoryIDSlice(ids []int) error {
	if err := validateStoryIDSlice(ids); err != nil {
		return err
	}
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			return inputRequestInvalidErr()
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateStoryAlbumUpdateStoryIDSlice(ids []int) error {
	if len(ids) == 0 {
		return inputRequestInvalidErr()
	}
	return validateStoryAlbumStoryIDSlice(ids)
}

func validateStoryAlbumTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return inputRequestInvalidErr()
	}
	if utf8.RuneCountInString(title) > maxStoryAlbumTitleLength {
		return limitInvalidErr()
	}
	return nil
}

func (r *Router) storyAlbumWriteScope(ctx context.Context, input tg.InputPeerClass) (int64, domain.Peer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, domain.Peer{}, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return 0, domain.Peer{}, err
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		if userID != 0 && peer.ID == userID {
			return userID, peer, nil
		}
		return 0, domain.Peer{}, peerIDInvalidErr()
	case domain.PeerTypeChannel:
		if err := r.requireChannelStoryAlbumAdmin(ctx, userID, peer.ID); err != nil {
			return 0, domain.Peer{}, err
		}
		return userID, peer, nil
	default:
		return 0, domain.Peer{}, peerIDInvalidErr()
	}
}

func (r *Router) requireChannelStoryAlbumAdmin(ctx context.Context, userID, channelID int64) error {
	if userID == 0 || channelID == 0 || r.deps.Channels == nil {
		return channelInvalidErr(domain.ErrChannelInvalid)
	}
	member, err := r.deps.Channels.GetParticipant(ctx, userID, channelID, userID)
	if err != nil {
		return channelInvalidErr(err)
	}
	if member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.EditStories) {
		return nil
	}
	return channelInvalidErr(domain.ErrChannelAdminRequired)
}

func uniqueStoryIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (r *Router) storyExportLink(peer domain.Peer, storyID int) string {
	return r.publicLink(fmt.Sprintf("story/%s/%d/%d", peer.Type, peer.ID, storyID))
}

func (r *Router) recordStoryChange(ctx context.Context, userID int64, story domain.Story) error {
	if r.deps.Updates == nil {
		return nil
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	if _, _, err := r.deps.Updates.RecordStory(ctx, authKeyID, userID, story, rawAuthKeyIDForOrigin(ctx), sessionID); err != nil {
		return internalErr()
	}
	return nil
}

func tgStoryChangeUpdates(peer domain.Peer, story domain.Story, randomID int64, includeStoryID bool, date int) tg.UpdatesClass {
	updates := make([]tg.UpdateClass, 0, 2)
	if includeStoryID {
		updates = append(updates, &tg.UpdateStoryID{ID: story.ID, RandomID: randomID})
	}
	updates = append(updates, &tg.UpdateStory{Peer: tgPeer(peer), Story: tgStoryItem(story)})
	return &tg.Updates{
		Updates: updates,
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
}

func tgStoryStealthModeUpdates(date int, past, future bool) tg.UpdatesClass {
	mode := tg.StoriesStealthMode{}
	if future {
		mode.SetActiveUntilDate(date + storyStealthFuturePeriodSeconds)
	}
	if past || future {
		mode.SetCooldownUntilDate(date + storyStealthCooldownSeconds)
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateStoriesStealthMode{StealthMode: mode}},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
}

func domainStoryReactionFromTL(reaction tg.ReactionClass) (*domain.MessageReaction, error) {
	if reactionClassNil(reaction) {
		return nil, reactionInvalidErr()
	}
	if empty, ok := reaction.(*tg.ReactionEmpty); ok && empty != nil {
		return nil, nil
	}
	out, err := domainStoryReactionValueFromTL(reaction)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func tgStoryReactionUpdates(peer domain.Peer, storyID int, reaction tg.ReactionClass, date int) tg.UpdatesClass {
	if reaction == nil {
		reaction = &tg.ReactionEmpty{}
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateSentStoryReaction{Peer: tgPeer(peer), StoryID: storyID, Reaction: reaction}},
		Date:    date,
		Seq:     0,
	}
}

func tgEmptyStoriesStoryViews(count int) *tg.StoriesStoryViews {
	if count < 0 {
		count = 0
	}
	views := make([]tg.StoryViews, count)
	return &tg.StoriesStoryViews{Views: views, Users: []tg.UserClass{}}
}

func (r *Router) tgStoriesStoryViewsForIDs(ctx context.Context, viewerUserID int64, peer domain.Peer, ids []int, stories []domain.Story) *tg.StoriesStoryViews {
	byID := make(map[int]domain.Story, len(stories))
	recentViewerIDs := make([]int64, 0)
	for _, story := range stories {
		if story.Owner != peer || story.ID <= 0 || story.ID > domain.MaxStoryID {
			continue
		}
		byID[story.ID] = story
		recentViewerIDs = append(recentViewerIDs, storyViewsForCounterResponse(viewerUserID, story).RecentViewers...)
	}
	views := make([]tg.StoryViews, 0, len(ids))
	for _, id := range ids {
		story, ok := byID[id]
		if !ok {
			views = append(views, tg.StoryViews{})
			continue
		}
		views = append(views, tgStoryViews(storyViewsForCounterResponse(viewerUserID, story)))
	}
	return &tg.StoriesStoryViews{
		Views: views,
		Users: tgUsersForViewer(viewerUserID, r.domainUsersForIDs(ctx, viewerUserID, uniquePeerIDs(recentViewerIDs))),
	}
}

func storyViewsForCounterResponse(viewerUserID int64, story domain.Story) domain.StoryViews {
	views := story.Views
	if !story.Owner.IsSelfUser(viewerUserID) {
		views.HasViewers = false
		views.RecentViewers = nil
	}
	return views
}

func (r *Router) addStoryMessageForwardCounts(ctx context.Context, viewerUserID int64, stories []domain.Story) {
	if r.deps.Channels == nil || viewerUserID == 0 {
		return
	}
	for i := range stories {
		count, err := r.storyMessageForwardCount(ctx, viewerUserID, stories[i].Owner, stories[i].ID)
		if err != nil {
			continue
		}
		stories[i].Views.ForwardsCount += count
	}
}

func (r *Router) withStoryMessageForwardViews(ctx context.Context, viewerUserID int64, list domain.StoryViewList, req domain.StoryMessageForwardListRequest) domain.StoryViewList {
	forwards := r.storyMessageForwardPage(ctx, viewerUserID, req)
	if forwards.Count == 0 && len(forwards.Forwards) == 0 {
		return list
	}
	hasMore := storyInteractionSourcesHaveMore(len(list.Views), len(forwards.Forwards), req.Limit, list.NextOffset != "" || forwards.NextOffset != "")
	list.Count += forwards.Count
	list.ForwardsCount += forwards.Count
	list.Views = mergeStoryInteractionViews(list.Views, forwards.Forwards, req.Limit, req.ReactionsFirst, req.ForwardsFirst)
	list.NextOffset = nextStoryInteractionOffset(list.Views, req.Limit, req.ReactionsFirst, req.ForwardsFirst, hasMore)
	return list
}

func (r *Router) withStoryMessageForwardReactions(ctx context.Context, viewerUserID int64, list domain.StoryReactionList, req domain.StoryMessageForwardListRequest) domain.StoryReactionList {
	forwards := r.storyMessageForwardPage(ctx, viewerUserID, req)
	if forwards.Count == 0 && len(forwards.Forwards) == 0 {
		return list
	}
	hasMore := storyInteractionSourcesHaveMore(len(list.Reactions), len(forwards.Forwards), req.Limit, list.NextOffset != "" || forwards.NextOffset != "")
	list.Count += forwards.Count
	list.Reactions = mergeStoryInteractionViews(list.Reactions, forwards.Forwards, req.Limit, false, req.ForwardsFirst)
	list.NextOffset = nextStoryInteractionOffset(list.Reactions, req.Limit, false, req.ForwardsFirst, hasMore)
	return list
}

func (r *Router) storyMessageForwardPage(ctx context.Context, viewerUserID int64, req domain.StoryMessageForwardListRequest) domain.StoryMessageForwardList {
	if r.deps.Channels == nil || viewerUserID == 0 {
		return domain.StoryMessageForwardList{}
	}
	req.ViewerUserID = viewerUserID
	if req.Limit <= 0 || req.Limit > domain.MaxStoryInteractionListLimit {
		req.Limit = domain.MaxStoryInteractionListLimit
	}
	list, err := r.deps.Channels.ListStoryMessageForwards(ctx, viewerUserID, req)
	if err != nil {
		return domain.StoryMessageForwardList{}
	}
	return list
}

func (r *Router) storyMessageForwardCount(ctx context.Context, viewerUserID int64, owner domain.Peer, storyID int) (int, error) {
	list, err := r.deps.Channels.ListStoryMessageForwards(ctx, viewerUserID, domain.StoryMessageForwardListRequest{
		ViewerUserID: viewerUserID,
		Owner:        owner,
		StoryID:      storyID,
		Limit:        1,
	})
	if err != nil {
		return 0, err
	}
	return list.Count, nil
}

func storyInteractionSourcesHaveMore(leftLen, rightLen, limit int, sourceHasMore bool) bool {
	if limit <= 0 || limit > domain.MaxStoryInteractionListLimit {
		limit = domain.MaxStoryInteractionListLimit
	}
	return sourceHasMore || leftLen+rightLen > limit
}

func mergeStoryInteractionViews(left, right []domain.StoryView, limit int, reactionsFirst, forwardsFirst bool) []domain.StoryView {
	if limit <= 0 || limit > domain.MaxStoryInteractionListLimit {
		limit = domain.MaxStoryInteractionListLimit
	}
	merged := make([]domain.StoryView, 0, len(left)+len(right))
	merged = append(merged, left...)
	merged = append(merged, right...)
	sort.Slice(merged, func(i, j int) bool {
		return storyInteractionLess(merged[i], merged[j], reactionsFirst, forwardsFirst)
	})
	if len(merged) > limit {
		return merged[:limit]
	}
	return merged
}

func nextStoryInteractionOffset(views []domain.StoryView, limit int, reactionsFirst, forwardsFirst bool, hasMore bool) string {
	if limit <= 0 || limit > domain.MaxStoryInteractionListLimit {
		limit = domain.MaxStoryInteractionListLimit
	}
	if !hasMore || len(views) == 0 || len(views) < limit {
		return ""
	}
	return formatStoryInteractionOffset(views[len(views)-1], reactionsFirst, forwardsFirst)
}

func storyInteractionLess(a, b domain.StoryView, reactionsFirst, forwardsFirst bool) bool {
	ga := storyInteractionGroup(a, reactionsFirst, forwardsFirst)
	gb := storyInteractionGroup(b, reactionsFirst, forwardsFirst)
	if ga != gb {
		return ga < gb
	}
	if a.Date != b.Date {
		return a.Date > b.Date
	}
	ak, aid := storyInteractionCursorKey(a)
	bk, bid := storyInteractionCursorKey(b)
	if ak != bk {
		return ak > bk
	}
	return aid > bid
}

func storyInteractionGroup(view domain.StoryView, reactionsFirst, forwardsFirst bool) int {
	if forwardsFirst {
		if view.Repost != nil || view.PublicForward != nil {
			return 0
		}
		return 1
	}
	if reactionsFirst && view.Reaction == nil && view.Repost == nil && view.PublicForward == nil {
		return 1
	}
	return 0
}

func storyInteractionCursorKey(view domain.StoryView) (int64, int) {
	if view.PublicForward != nil {
		return -view.PublicForward.Message.ChannelID, view.PublicForward.Message.ID
	}
	if view.Repost != nil {
		if view.Repost.Owner.Type == domain.PeerTypeChannel {
			return -view.Repost.Owner.ID, 0
		}
		return view.Repost.Owner.ID, 0
	}
	return view.ViewerID, 0
}

func formatStoryInteractionOffset(view domain.StoryView, reactionsFirst, forwardsFirst bool) string {
	group := storyInteractionGroup(view, reactionsFirst, forwardsFirst)
	key, messageID := storyInteractionCursorKey(view)
	out := strconv.Itoa(group) + ":" + strconv.Itoa(view.Date) + ":" + strconv.FormatInt(key, 10)
	if messageID > 0 {
		out += ":" + strconv.Itoa(messageID)
	}
	return out
}

func storyErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrStoryIDInvalid):
		return storyIDInvalidErr()
	case errors.Is(err, domain.ErrStoryPeerInvalid):
		return peerIDInvalidErr()
	case errors.Is(err, domain.ErrStoryNotFound):
		return storyIDInvalidErr()
	case errors.Is(err, domain.ErrStoryNotModified):
		return storyNotModifiedErr()
	case errors.Is(err, domain.ErrStoryOffsetInvalid):
		return offsetInvalidErr()
	case errors.Is(err, domain.ErrStoryPeriodInvalid):
		return storyPeriodInvalidErr()
	case errors.Is(err, domain.ErrChannelInvalid),
		errors.Is(err, domain.ErrChannelPrivate),
		errors.Is(err, domain.ErrChannelUserBanned),
		errors.Is(err, domain.ErrChannelWriteForbidden),
		errors.Is(err, domain.ErrChannelAdminRequired):
		return channelInvalidErr(err)
	default:
		return internalErr()
	}
}
