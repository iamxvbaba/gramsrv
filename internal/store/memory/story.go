package memory

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"telesrv/internal/domain"
)

// StoryStore is the in-memory implementation of store.StoryStore.
type StoryStore struct {
	mu        sync.RWMutex
	stories   map[storyKey]domain.Story
	read      map[storyReadKey]domain.StoryReadState
	views     map[storyViewKey]domain.StoryView
	exposures map[storyViewKey]int
	hidden    map[storyHiddenKey]bool
	profiles  map[int64]domain.User
	contacts  map[int64]map[int64]domain.Contact
	blocked   map[int64]map[int64]bool
	channels  *ChannelStore
	members   map[int64]map[int64]bool
}

type storyKey struct {
	peerType domain.PeerType
	peerID   int64
	storyID  int
}

type storyReadKey struct {
	viewerID int64
	peerType domain.PeerType
	peerID   int64
}

type storyViewKey struct {
	peerType domain.PeerType
	peerID   int64
	storyID  int
	viewerID int64
}

type storyHiddenKey struct {
	viewerID int64
	peerType domain.PeerType
	peerID   int64
}

// NewStoryStore creates an empty StoryStore.
func NewStoryStore(channels ...*ChannelStore) *StoryStore {
	var channelStore *ChannelStore
	if len(channels) > 0 {
		channelStore = channels[0]
	}
	return &StoryStore{
		stories:   make(map[storyKey]domain.Story),
		read:      make(map[storyReadKey]domain.StoryReadState),
		views:     make(map[storyViewKey]domain.StoryView),
		exposures: make(map[storyViewKey]int),
		hidden:    make(map[storyHiddenKey]bool),
		profiles:  make(map[int64]domain.User),
		contacts:  make(map[int64]map[int64]domain.Contact),
		blocked:   make(map[int64]map[int64]bool),
		channels:  channelStore,
		members:   make(map[int64]map[int64]bool),
	}
}

// SetStoryViewerProfiles seeds owner-visible user metadata used by
// ListStoryViews q filtering in the memory store.
func (s *StoryStore) SetStoryViewerProfiles(users ...domain.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.profiles == nil {
		s.profiles = make(map[int64]domain.User)
	}
	for _, user := range users {
		if user.ID == 0 {
			continue
		}
		s.profiles[user.ID] = user
	}
}

// SetStoryViewerContacts seeds owner-scoped contact metadata used by
// ListStoryViews just_contacts and q filtering in the memory store.
func (s *StoryStore) SetStoryViewerContacts(ownerUserID int64, contacts ...domain.Contact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contacts == nil {
		s.contacts = make(map[int64]map[int64]domain.Contact)
	}
	if _, ok := s.contacts[ownerUserID]; !ok {
		s.contacts[ownerUserID] = make(map[int64]domain.Contact)
	}
	for _, contact := range contacts {
		if contact.User.ID == 0 {
			continue
		}
		s.contacts[ownerUserID][contact.User.ID] = contact
	}
}

// SetStoryBlockedUsers seeds owner-scoped story blocklist membership used by
// memory-store story visibility tests.
func (s *StoryStore) SetStoryBlockedUsers(ownerUserID int64, viewerUserIDs ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked == nil {
		s.blocked = make(map[int64]map[int64]bool)
	}
	if len(viewerUserIDs) == 0 {
		delete(s.blocked, ownerUserID)
		return
	}
	blocked := make(map[int64]bool, len(viewerUserIDs))
	for _, viewerUserID := range viewerUserIDs {
		if viewerUserID == 0 || viewerUserID == ownerUserID {
			continue
		}
		blocked[viewerUserID] = true
	}
	if len(blocked) == 0 {
		delete(s.blocked, ownerUserID)
		return
	}
	s.blocked[ownerUserID] = blocked
}

// SetStoryChannelMembers seeds active channel story viewers for memory-store
// story visibility tests when no ChannelStore is attached.
func (s *StoryStore) SetStoryChannelMembers(channelID int64, viewerUserIDs ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.members == nil {
		s.members = make(map[int64]map[int64]bool)
	}
	members := make(map[int64]bool, len(viewerUserIDs))
	for _, viewerUserID := range viewerUserIDs {
		if viewerUserID == 0 {
			continue
		}
		members[viewerUserID] = true
	}
	s.members[channelID] = members
}

func (s *StoryStore) CreateStory(_ context.Context, req domain.StoryCreateRequest) (domain.StoryCreateResult, error) {
	if err := validateStoryPeer(req.Owner); err != nil {
		return domain.StoryCreateResult{}, err
	}
	if req.RandomID == 0 {
		return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.stories {
		if existing.Owner == req.Owner && existing.RandomID == req.RandomID {
			return domain.StoryCreateResult{Story: cloneStory(existing), Duplicate: true}, nil
		}
	}
	nextID := 1
	for _, existing := range s.stories {
		if existing.Owner == req.Owner && existing.ID >= nextID {
			nextID = existing.ID + 1
		}
	}
	if nextID <= 0 || nextID > domain.MaxStoryID {
		return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
	}
	story := domain.Story{
		Owner:            req.Owner,
		ID:               nextID,
		RandomID:         req.RandomID,
		Date:             req.Date,
		ExpireDate:       req.Date + req.Period,
		Pinned:           req.Pinned,
		Public:           req.Public,
		CloseFriends:     req.CloseFriends,
		Contacts:         req.Contacts,
		SelectedContacts: req.SelectedContacts,
		NoForwards:       req.NoForwards,
		Out:              true,
		PrivacyRules:     cloneStoryPrivacyRules(req.PrivacyRules),
		AllowUserIDs:     append([]int64(nil), req.AllowUserIDs...),
		DisallowUserIDs:  append([]int64(nil), req.DisallowUserIDs...),
		Caption:          req.Caption,
		Entities:         append([]domain.MessageEntity(nil), req.Entities...),
		Media:            req.Media,
		MediaAreas:       cloneStoryMediaAreas(req.MediaAreas),
		Forward:          cloneStoryForward(req.Forward),
	}
	s.stories[keyForStory(story.Owner, story.ID)] = story
	if story.Owner.Type == domain.PeerTypeUser {
		s.read[storyReadKey{viewerID: story.Owner.ID, peerType: story.Owner.Type, peerID: story.Owner.ID}] = domain.StoryReadState{
			ViewerID:  story.Owner.ID,
			Peer:      story.Owner,
			MaxReadID: story.ID,
			Date:      story.Date,
		}
	}
	return domain.StoryCreateResult{Story: cloneStory(story)}, nil
}

func (s *StoryStore) UpsertStory(_ context.Context, req domain.UpsertStoryRequest) (domain.Story, error) {
	story := cloneStory(req.Story)
	if err := validateStoryIdentity(story.Owner, story.ID); err != nil {
		return domain.Story{}, err
	}
	s.mu.Lock()
	s.stories[keyForStory(story.Owner, story.ID)] = story
	s.mu.Unlock()
	return cloneStory(story), nil
}

func (s *StoryStore) ListActiveStories(ctx context.Context, viewerUserID int64, hidden bool, now, limit int) (domain.StoryList, error) {
	return s.ListActiveStoriesPage(ctx, viewerUserID, hidden, now, domain.StoryListCursor{}, limit)
}

func (s *StoryStore) ListActiveStoriesPage(_ context.Context, viewerUserID int64, hidden bool, now int, cursor domain.StoryListCursor, limit int) (domain.StoryList, error) {
	if limit <= 0 || limit > domain.MaxStoryListLimit {
		limit = domain.MaxStoryListLimit
	}
	pageItems, reads := s.activeStoryPeerPagesForViewer(viewerUserID, hidden, now)
	total := len(pageItems)
	if cursor.Set {
		pageItems = activeStoryPeerPagesAfter(pageItems, cursor)
	}
	hasMore := len(pageItems) > limit
	if hasMore {
		pageItems = pageItems[:limit]
	}
	peers := activeStoryPeerStories(pageItems, reads)
	stories := make([]domain.Story, 0)
	for _, item := range pageItems {
		stories = append(stories, item.stories...)
	}
	s.recordStoryExposures(viewerUserID, stories)
	return domain.StoryList{
		Count:   total,
		HasMore: hasMore,
		Stories: stories,
		Peers:   peers,
	}, nil
}

func (s *StoryStore) ActiveStoriesDigest(_ context.Context, viewerUserID int64, hidden bool, now int) (domain.StoryListDigest, error) {
	pageItems, reads := s.activeStoryPeerPagesForViewer(viewerUserID, hidden, now)
	return domain.DigestStoryPeerList(activeStoryPeerStories(pageItems, reads)), nil
}

func (s *StoryStore) activeStoryPeerPagesForViewer(viewerUserID int64, hidden bool, now int) ([]activeStoryPeerPage, []domain.StoryReadState) {
	s.mu.RLock()
	byPeer := make(map[domain.Peer][]domain.Story)
	for _, story := range s.stories {
		if !story.Active(now) || !s.storyVisibleLocked(story, viewerUserID) {
			continue
		}
		isHidden := s.hidden[storyHiddenKey{viewerID: viewerUserID, peerType: story.Owner.Type, peerID: story.Owner.ID}]
		if hidden != isHidden {
			continue
		}
		byPeer[story.Owner] = append(byPeer[story.Owner], cloneStory(story))
	}
	for peer := range byPeer {
		s.populateStoryForwardCountsLocked(byPeer[peer], viewerUserID)
	}
	reads := cloneReadStatesLocked(s.read, viewerUserID)
	s.mu.RUnlock()
	pageItems := make([]activeStoryPeerPage, 0, len(byPeer))
	for peer, stories := range byPeer {
		sortStoriesOldestFirst(stories)
		pageItems = append(pageItems, activeStoryPeerPage{
			peer:    peer,
			maxDate: latestStoryDate(stories),
			stories: stories,
		})
	}
	sortActiveStoryPeerPages(pageItems)
	return pageItems, reads
}

func (s *StoryStore) ListOwnerActiveStories(_ context.Context, owner domain.Peer, now, limit int) (domain.StoryList, error) {
	if err := validateStoryPeer(owner); err != nil {
		return domain.StoryList{}, err
	}
	if limit <= 0 || limit > domain.MaxStoryListLimit {
		limit = domain.MaxStoryListLimit
	}
	s.mu.RLock()
	stories := make([]domain.Story, 0, len(s.stories))
	for _, story := range s.stories {
		if story.Owner != owner || !story.Active(now) {
			continue
		}
		stories = append(stories, fanoutStorySnapshot(story))
	}
	s.mu.RUnlock()
	sortStoriesOldestFirst(stories)
	if len(stories) > limit {
		stories = stories[:limit]
	}
	return domain.StoryList{Count: len(stories), Stories: stories}, nil
}

func (s *StoryStore) GetPeerStories(_ context.Context, viewerUserID int64, peer domain.Peer, now int) (domain.PeerStories, error) {
	if err := validateStoryPeer(peer); err != nil {
		return domain.PeerStories{}, err
	}
	s.mu.RLock()
	var stories []domain.Story
	for _, story := range s.stories {
		if story.Owner != peer || !story.Active(now) || !s.storyVisibleLocked(story, viewerUserID) {
			continue
		}
		stories = append(stories, cloneStory(story))
	}
	s.populateStoryForwardCountsLocked(stories, viewerUserID)
	read := s.read[storyReadKey{viewerID: viewerUserID, peerType: peer.Type, peerID: peer.ID}]
	s.mu.RUnlock()
	sortStoriesOldestFirst(stories)
	s.recordStoryExposures(viewerUserID, stories)
	return domain.PeerStories{Peer: peer, MaxReadID: read.MaxReadID, Stories: stories}, nil
}

func (s *StoryStore) GetStoriesByID(_ context.Context, viewerUserID int64, peer domain.Peer, ids []int, now int) (domain.StoryList, error) {
	if err := validateStoryPeer(peer); err != nil {
		return domain.StoryList{}, err
	}
	ids, err := normalizeStoryIDListNonEmpty(ids)
	if err != nil {
		return domain.StoryList{}, err
	}
	out := make([]domain.Story, 0, len(ids))
	s.mu.RLock()
	for _, id := range ids {
		story, ok := s.stories[keyForStory(peer, id)]
		if !ok || story.Deleted || !s.storyVisibleLocked(story, viewerUserID) {
			continue
		}
		// Exact lookups may return expired pinned/profile stories; non-pinned expired
		// stories are still returned as snapshots for share/reply resolution.
		_ = now
		item := cloneStory(story)
		item.Out = item.Owner.Type == domain.PeerTypeUser && item.Owner.ID == viewerUserID
		out = append(out, item)
	}
	s.populateStoryForwardCountsLocked(out, viewerUserID)
	s.mu.RUnlock()
	s.recordStoryExposures(viewerUserID, out)
	return domain.StoryList{Count: len(out), Stories: out}, nil
}

func (s *StoryStore) ListPinnedStories(_ context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error) {
	_ = now
	if err := validateStoryPeer(peer); err != nil {
		return domain.StoryList{}, err
	}
	if limit <= 0 || limit > domain.MaxStoryListLimit {
		limit = domain.MaxStoryListLimit
	}
	s.mu.RLock()
	out := make([]domain.Story, 0)
	pinnedToTop := make([]domain.Story, 0)
	total := 0
	for _, story := range s.stories {
		if story.Owner != peer || story.Deleted || !story.Pinned || !s.storyVisibleLocked(story, viewerUserID) {
			continue
		}
		total++
		if story.PinnedToTopOrder > 0 {
			pinnedToTop = append(pinnedToTop, cloneStory(story))
		}
		if offsetID > 0 && story.ID >= offsetID {
			continue
		}
		out = append(out, cloneStory(story))
	}
	s.populateStoryForwardCountsLocked(out, viewerUserID)
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID > out[j].ID
		}
		return out[i].Date > out[j].Date
	})
	sort.Slice(pinnedToTop, func(i, j int) bool {
		if pinnedToTop[i].PinnedToTopOrder != pinnedToTop[j].PinnedToTopOrder {
			return pinnedToTop[i].PinnedToTopOrder < pinnedToTop[j].PinnedToTopOrder
		}
		return pinnedToTop[i].ID > pinnedToTop[j].ID
	})
	topIDs := make([]int, 0, len(pinnedToTop))
	for _, story := range pinnedToTop {
		topIDs = append(topIDs, story.ID)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	s.recordStoryExposures(viewerUserID, out)
	return domain.StoryList{Count: total, Stories: out, PinnedToTop: topIDs}, nil
}

func (s *StoryStore) HasPinnedStories(_ context.Context, viewerUserID int64, peer domain.Peer, now int) (bool, error) {
	_ = now
	if err := validateStoryPeer(peer); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, story := range s.stories {
		if story.Owner == peer && !story.Deleted && story.Pinned && s.storyVisibleLocked(story, viewerUserID) {
			return true, nil
		}
	}
	return false, nil
}

func (s *StoryStore) ListStoriesArchive(_ context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error) {
	if err := validateStoryPeer(peer); err != nil {
		return domain.StoryList{}, err
	}
	if limit < 0 || limit > domain.MaxStoryListLimit {
		limit = domain.MaxStoryListLimit
	}
	if offsetID < 0 {
		offsetID = 0
	}
	s.mu.RLock()
	total := 0
	out := make([]domain.Story, 0)
	for _, story := range s.stories {
		if story.Owner != peer || story.Deleted || story.ExpireDate > now {
			continue
		}
		total++
		if limit == 0 || (offsetID > 0 && story.ID >= offsetID) {
			continue
		}
		item := cloneStory(story)
		item.Out = item.Owner.Type == domain.PeerTypeUser && item.Owner.ID == viewerUserID
		out = append(out, item)
	}
	s.populateStoryForwardCountsLocked(out, viewerUserID)
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID > out[j].ID
		}
		return out[i].Date > out[j].Date
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	s.recordStoryExposures(viewerUserID, out)
	return domain.StoryList{Count: total, Stories: out}, nil
}

func (s *StoryStore) ListReadStates(_ context.Context, viewerUserID int64) ([]domain.StoryReadState, error) {
	s.mu.RLock()
	out := cloneReadStatesLocked(s.read, viewerUserID)
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Peer.Type != out[j].Peer.Type {
			return out[i].Peer.Type < out[j].Peer.Type
		}
		return out[i].Peer.ID < out[j].Peer.ID
	})
	return out, nil
}

func (s *StoryStore) GetPeerMaxIDs(_ context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.RecentStory, error) {
	if len(peers) > domain.MaxStoryIDs {
		return nil, domain.ErrStoryIDInvalid
	}
	out := make([]domain.RecentStory, len(peers))
	s.mu.RLock()
	for i, peer := range peers {
		if err := validateStoryPeer(peer); err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		maxID := 0
		for _, story := range s.stories {
			if story.Owner == peer && story.Active(now) && s.storyVisibleLocked(story, viewerUserID) && story.ID > maxID {
				maxID = story.ID
			}
		}
		out[i] = domain.RecentStory{Peer: peer, MaxID: maxID}
	}
	s.mu.RUnlock()
	return out, nil
}

func (s *StoryStore) GetPeerHiddenStates(_ context.Context, viewerUserID int64, peers []domain.Peer) (map[domain.Peer]bool, error) {
	if len(peers) > domain.MaxStoryIDs {
		return nil, domain.ErrStoryIDInvalid
	}
	if viewerUserID == 0 {
		return nil, domain.ErrStoryPeerInvalid
	}
	out := make(map[domain.Peer]bool, len(peers))
	s.mu.RLock()
	for _, peer := range peers {
		if err := validateStoryPeer(peer); err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		key := storyHiddenKey{viewerID: viewerUserID, peerType: peer.Type, peerID: peer.ID}
		out[peer] = s.hidden[key]
	}
	s.mu.RUnlock()
	return out, nil
}

func (s *StoryStore) GetPeerStoryProjections(_ context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.PeerStoryProjection, error) {
	if len(peers) > domain.MaxStoryIDs {
		return nil, domain.ErrStoryIDInvalid
	}
	if viewerUserID == 0 {
		return nil, domain.ErrStoryPeerInvalid
	}
	out := make([]domain.PeerStoryProjection, len(peers))
	s.mu.RLock()
	for i, peer := range peers {
		if err := validateStoryPeer(peer); err != nil {
			s.mu.RUnlock()
			return nil, err
		}
		maxID := 0
		for _, story := range s.stories {
			if story.Owner == peer && story.Active(now) && s.storyVisibleLocked(story, viewerUserID) && story.ID > maxID {
				maxID = story.ID
			}
		}
		key := storyHiddenKey{viewerID: viewerUserID, peerType: peer.Type, peerID: peer.ID}
		out[i] = domain.PeerStoryProjection{
			Peer:   peer,
			Recent: domain.RecentStory{Peer: peer, MaxID: maxID},
			Hidden: s.hidden[key],
		}
	}
	s.mu.RUnlock()
	return out, nil
}

func (s *StoryStore) MarkRead(_ context.Context, viewerUserID int64, peer domain.Peer, maxID, date int) (domain.StoryReadResult, error) {
	if viewerUserID == 0 {
		return domain.StoryReadResult{}, domain.ErrStoryPeerInvalid
	}
	if err := validateStoryIdentity(peer, maxID); err != nil {
		return domain.StoryReadResult{}, err
	}
	key := storyReadKey{viewerID: viewerUserID, peerType: peer.Type, peerID: peer.ID}
	s.mu.Lock()
	state := s.read[key]
	advanced := maxID > state.MaxReadID
	if advanced {
		state = domain.StoryReadState{ViewerID: viewerUserID, Peer: peer, MaxReadID: maxID, Date: date}
		s.read[key] = state
	}
	s.mu.Unlock()
	return domain.StoryReadResult{ViewerID: viewerUserID, Peer: peer, MaxReadID: state.MaxReadID, Advanced: advanced, Date: date}, nil
}

func (s *StoryStore) IncrementViews(_ context.Context, viewerUserID int64, peer domain.Peer, ids []int, date int) (int, error) {
	if viewerUserID == 0 {
		return 0, domain.ErrStoryPeerInvalid
	}
	if err := validateStoryPeer(peer); err != nil {
		return 0, err
	}
	ids, err := normalizeStoryIDListNonEmpty(ids)
	if err != nil {
		return 0, err
	}
	if peer.IsSelfUser(viewerUserID) {
		return 0, nil
	}
	created := 0
	s.mu.Lock()
	for _, id := range ids {
		story, ok := s.stories[keyForStory(peer, id)]
		if !ok || !story.Interactable(date) || !s.storyVisibleLocked(story, viewerUserID) {
			continue
		}
		key := storyViewKey{peerType: peer.Type, peerID: peer.ID, storyID: id, viewerID: viewerUserID}
		if _, ok := s.views[key]; ok {
			continue
		}
		s.views[key] = domain.StoryView{Owner: peer, StoryID: id, ViewerID: viewerUserID, Date: date}
		story.Views.ViewsCount++
		story.Views.HasViewers = true
		story.Views.RecentViewers = prependRecentViewer(story.Views.RecentViewers, viewerUserID)
		s.stories[keyForStory(peer, id)] = story
		created++
	}
	s.mu.Unlock()
	return created, nil
}

func (s *StoryStore) SetReaction(_ context.Context, viewerUserID int64, peer domain.Peer, storyID int, reaction *domain.MessageReaction, date int) (domain.StoryReactionResult, error) {
	if viewerUserID == 0 {
		return domain.StoryReactionResult{}, domain.ErrStoryPeerInvalid
	}
	if err := validateStoryIdentity(peer, storyID); err != nil {
		return domain.StoryReactionResult{}, err
	}
	if peer.IsSelfUser(viewerUserID) {
		return domain.StoryReactionResult{}, domain.ErrStoryPeerInvalid
	}
	s.mu.Lock()
	story, ok := s.stories[keyForStory(peer, storyID)]
	if !ok || !story.Interactable(date) || !s.storyVisibleLocked(story, viewerUserID) {
		s.mu.Unlock()
		return domain.StoryReactionResult{}, domain.ErrStoryNotFound
	}
	key := storyViewKey{peerType: peer.Type, peerID: peer.ID, storyID: storyID, viewerID: viewerUserID}
	view := s.views[key]
	existingView := view.ViewerID != 0
	if view.ViewerID == 0 {
		view = domain.StoryView{Owner: peer, StoryID: storyID, ViewerID: viewerUserID, Date: date}
		story.Views.ViewsCount++
		story.Views.HasViewers = true
		story.Views.RecentViewers = prependRecentViewer(story.Views.RecentViewers, viewerUserID)
	}
	changed := !sameReaction(view.Reaction, reaction)
	if existingView && !changed {
		story.SentReaction = cloneReactionPtr(reaction)
		s.mu.Unlock()
		return domain.StoryReactionResult{
			ViewerID: viewerUserID,
			Peer:     peer,
			StoryID:  storyID,
			Reaction: cloneReactionPtr(reaction),
			Story:    cloneStory(story),
			Changed:  false,
			Date:     view.Date,
		}, nil
	}
	if changed {
		story.Views = adjustStoryReactionCounts(story.Views, view.Reaction, reaction)
	}
	view.Reaction = cloneReactionPtr(reaction)
	view.Date = date
	s.views[key] = view
	story.SentReaction = cloneReactionPtr(reaction)
	s.stories[keyForStory(peer, storyID)] = story
	s.mu.Unlock()
	return domain.StoryReactionResult{
		ViewerID: viewerUserID,
		Peer:     peer,
		StoryID:  storyID,
		Reaction: cloneReactionPtr(reaction),
		Story:    cloneStory(story),
		Changed:  changed,
		Date:     date,
	}, nil
}

func (s *StoryStore) ListStoryViews(_ context.Context, req domain.StoryViewListRequest) (domain.StoryViewList, error) {
	if req.ViewerUserID == 0 {
		return domain.StoryViewList{}, domain.ErrStoryPeerInvalid
	}
	if err := validateStoryIdentity(req.Owner, req.StoryID); err != nil {
		return domain.StoryViewList{}, err
	}
	if err := domain.ValidateStoryInteractionOffset(req.Offset, false); err != nil {
		return domain.StoryViewList{}, err
	}
	limit := clampStoryInteractionLimit(req.Limit)
	cursor := parseStoryInteractionCursor(req.Offset)
	s.mu.RLock()
	story, ok := s.stories[keyForStory(req.Owner, req.StoryID)]
	if !ok || story.Deleted {
		s.mu.RUnlock()
		return domain.StoryViewList{}, domain.ErrStoryNotFound
	}
	views := s.storyViewsLocked(req.Owner, req.StoryID)
	for i := range views {
		views[i].BlockedMyStoriesFrom = s.storyViewerBlockedLocked(req.Owner, views[i].ViewerID)
	}
	reposts := s.storyRepostViewsLocked(req.Owner, req.StoryID, req.ViewerUserID)
	profiles := cloneStoryViewerProfiles(s.profiles)
	contacts := cloneStoryViewerContacts(s.contacts[req.ViewerUserID])
	s.mu.RUnlock()
	filteredViews := filterStoryViewsForList(views, req, profiles, contacts)
	display := filteredViews
	if req.Query == "" && !req.JustContacts {
		display = append(display, reposts...)
	}
	sortStoryViewsForList(display, req.ReactionsFirst, req.ForwardsFirst)
	_, viewsCount, reactionsCount := storyViewCounts(views)
	count := len(filteredViews)
	if req.Query == "" && !req.JustContacts {
		count += len(reposts)
	}
	if req.Query != "" || req.JustContacts {
		allViews := s.storyViewsSnapshot(req.Owner, req.StoryID)
		_, viewsCount, reactionsCount = storyViewCounts(allViews)
	}
	page, nextOffset := pageStoryViews(display, limit, cursor, req.ReactionsFirst, req.ForwardsFirst)
	return domain.StoryViewList{
		Count:          count,
		ViewsCount:     viewsCount,
		ForwardsCount:  len(reposts),
		ReactionsCount: reactionsCount,
		Views:          page,
		NextOffset:     nextOffset,
	}, nil
}

func (s *StoryStore) storyViewsSnapshot(owner domain.Peer, storyID int) []domain.StoryView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.storyViewsLocked(owner, storyID)
}

func (s *StoryStore) ListStoryReactions(_ context.Context, req domain.StoryReactionListRequest) (domain.StoryReactionList, error) {
	if req.ViewerUserID == 0 {
		return domain.StoryReactionList{}, domain.ErrStoryPeerInvalid
	}
	if err := validateStoryIdentity(req.Owner, req.StoryID); err != nil {
		return domain.StoryReactionList{}, err
	}
	if err := domain.ValidateStoryReactionInteractionOffset(req.Offset, req.ForwardsFirst); err != nil {
		return domain.StoryReactionList{}, err
	}
	limit := clampStoryInteractionLimit(req.Limit)
	cursor := parseStoryInteractionCursor(req.Offset)
	s.mu.RLock()
	story, ok := s.stories[keyForStory(req.Owner, req.StoryID)]
	if !ok || story.Deleted {
		s.mu.RUnlock()
		return domain.StoryReactionList{}, domain.ErrStoryNotFound
	}
	views := s.storyViewsLocked(req.Owner, req.StoryID)
	reposts := []domain.StoryView(nil)
	if req.Reaction == nil {
		reposts = s.storyRepostViewsLocked(req.Owner, req.StoryID, req.ViewerUserID)
	}
	s.mu.RUnlock()
	reactions := make([]domain.StoryView, 0, len(views)+len(reposts))
	for _, view := range views {
		if view.Reaction == nil {
			continue
		}
		if req.Reaction != nil && !sameReaction(view.Reaction, req.Reaction) {
			continue
		}
		reactions = append(reactions, view)
	}
	reactions = append(reactions, reposts...)
	sortStoryViewsForList(reactions, false, req.ForwardsFirst)
	page, nextOffset := pageStoryViews(reactions, limit, cursor, false, req.ForwardsFirst)
	return domain.StoryReactionList{
		Count:      len(reactions),
		Reactions:  page,
		NextOffset: nextOffset,
	}, nil
}

func (s *StoryStore) ListStoryPublicForwards(_ context.Context, req domain.StoryPublicForwardListRequest) (domain.StoryPublicForwardList, error) {
	if req.ViewerUserID == 0 {
		return domain.StoryPublicForwardList{}, domain.ErrStoryPeerInvalid
	}
	if err := validateStoryIdentity(req.Owner, req.StoryID); err != nil {
		return domain.StoryPublicForwardList{}, err
	}
	if err := domain.ValidateStoryInteractionOffset(req.Offset, false); err != nil {
		return domain.StoryPublicForwardList{}, err
	}
	limit := clampStoryInteractionLimit(req.Limit)
	cursor := parseStoryInteractionCursor(req.Offset)
	s.mu.RLock()
	story, ok := s.stories[keyForStory(req.Owner, req.StoryID)]
	if !ok || story.Deleted {
		s.mu.RUnlock()
		return domain.StoryPublicForwardList{}, domain.ErrStoryNotFound
	}
	reposts := s.storyRepostViewsLocked(req.Owner, req.StoryID, req.ViewerUserID)
	s.mu.RUnlock()
	sortStoryViewsForList(reposts, false, true)
	page, nextOffset := pageStoryViews(reposts, limit, cursor, false, true)
	return domain.StoryPublicForwardList{Count: len(reposts), Forwards: page, NextOffset: nextOffset}, nil
}

func (s *StoryStore) ListStoryViewerIDs(_ context.Context, owner domain.Peer, storyID, limit int) ([]int64, error) {
	if err := validateStoryIdentity(owner, storyID); err != nil {
		return nil, err
	}
	limit = clampStoryPrivacyFanoutLimit(limit)
	s.mu.RLock()
	if _, ok := s.stories[keyForStory(owner, storyID)]; !ok {
		s.mu.RUnlock()
		return nil, domain.ErrStoryNotFound
	}
	ids := make([]int64, 0)
	seen := make(map[int64]struct{})
	for key := range s.views {
		if key.peerType != owner.Type || key.peerID != owner.ID || key.storyID != storyID || key.viewerID == 0 {
			continue
		}
		if _, ok := seen[key.viewerID]; ok {
			continue
		}
		seen[key.viewerID] = struct{}{}
		ids = append(ids, key.viewerID)
	}
	for key := range s.exposures {
		if key.peerType != owner.Type || key.peerID != owner.ID || key.storyID != storyID || key.viewerID == 0 {
			continue
		}
		if _, ok := seen[key.viewerID]; ok {
			continue
		}
		seen[key.viewerID] = struct{}{}
		ids = append(ids, key.viewerID)
	}
	s.mu.RUnlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (s *StoryStore) EditStory(_ context.Context, req domain.StoryEditRequest) (domain.StoryEditResult, error) {
	if err := validateStoryIdentity(req.Owner, req.ID); err != nil {
		return domain.StoryEditResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := keyForStory(req.Owner, req.ID)
	story, ok := s.stories[key]
	if !ok || story.Deleted {
		return domain.StoryEditResult{}, domain.ErrStoryNotFound
	}
	updated := cloneStory(story)
	if req.UpdateMedia {
		updated.Media = req.Media
	}
	if req.UpdateCaption {
		updated.Caption = req.Caption
		updated.Entities = append([]domain.MessageEntity(nil), req.Entities...)
	}
	if req.UpdatePrivacy {
		updated.Public = req.Public
		updated.CloseFriends = req.CloseFriends
		updated.Contacts = req.Contacts
		updated.SelectedContacts = req.SelectedContacts
		updated.PrivacyRules = cloneStoryPrivacyRules(req.PrivacyRules)
		updated.AllowUserIDs = append([]int64(nil), req.AllowUserIDs...)
		updated.DisallowUserIDs = append([]int64(nil), req.DisallowUserIDs...)
	}
	if req.UpdateMediaAreas {
		updated.MediaAreas = cloneStoryMediaAreas(req.MediaAreas)
	}
	if reflect.DeepEqual(story, updated) {
		return domain.StoryEditResult{}, domain.ErrStoryNotModified
	}
	updated.Edited = true
	s.stories[key] = updated
	return domain.StoryEditResult{Story: cloneStory(updated), Previous: cloneStory(story)}, nil
}

func (s *StoryStore) DeleteStories(_ context.Context, peer domain.Peer, ids []int, date int) (domain.StoryMutationResult, error) {
	_ = date
	if err := validateStoryPeer(peer); err != nil {
		return domain.StoryMutationResult{}, err
	}
	ids, err := normalizeStoryIDListNonEmpty(ids)
	if err != nil {
		return domain.StoryMutationResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := domain.StoryMutationResult{Peer: peer, IDs: append([]int(nil), ids...)}
	for _, id := range ids {
		key := keyForStory(peer, id)
		story, ok := s.stories[key]
		if !ok {
			continue
		}
		if story.Deleted {
			continue
		}
		out.Previous = append(out.Previous, cloneStory(story))
		story.Deleted = true
		story.Pinned = false
		story.PinnedToTopOrder = 0
		s.stories[key] = story
		out.Stories = append(out.Stories, cloneStory(story))
	}
	return out, nil
}

func (s *StoryStore) TogglePinned(_ context.Context, peer domain.Peer, ids []int, pinned bool, date int) (domain.StoryMutationResult, error) {
	_ = date
	if err := validateStoryPeer(peer); err != nil {
		return domain.StoryMutationResult{}, err
	}
	ids, err := normalizeStoryIDList(ids)
	if err != nil {
		return domain.StoryMutationResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := domain.StoryMutationResult{Peer: peer, IDs: append([]int(nil), ids...)}
	for _, id := range ids {
		key := keyForStory(peer, id)
		story, ok := s.stories[key]
		if !ok || story.Deleted {
			continue
		}
		if story.Pinned == pinned {
			continue
		}
		out.Previous = append(out.Previous, cloneStory(story))
		story.Pinned = pinned
		if !pinned {
			story.PinnedToTopOrder = 0
		}
		s.stories[key] = story
		out.Stories = append(out.Stories, cloneStory(story))
	}
	return out, nil
}

func (s *StoryStore) TogglePinnedToTop(_ context.Context, peer domain.Peer, ids []int) error {
	if err := validateStoryPeer(peer); err != nil {
		return err
	}
	ids, err := normalizeStoryPinnedToTopIDs(ids)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		story, ok := s.stories[keyForStory(peer, id)]
		if !ok || story.Deleted || !story.Pinned {
			return domain.ErrStoryIDInvalid
		}
	}
	for key, story := range s.stories {
		if story.Owner == peer && story.PinnedToTopOrder != 0 {
			story.PinnedToTopOrder = 0
			s.stories[key] = story
		}
	}
	for i, id := range ids {
		key := keyForStory(peer, id)
		story := s.stories[key]
		story.PinnedToTopOrder = i + 1
		s.stories[key] = story
	}
	return nil
}

func (s *StoryStore) SetPeerHidden(_ context.Context, viewerUserID int64, peer domain.Peer, hidden bool) error {
	if viewerUserID == 0 {
		return domain.ErrStoryPeerInvalid
	}
	if err := validateStoryPeer(peer); err != nil {
		return err
	}
	key := storyHiddenKey{viewerID: viewerUserID, peerType: peer.Type, peerID: peer.ID}
	s.mu.Lock()
	if hidden {
		s.hidden[key] = true
	} else {
		delete(s.hidden, key)
	}
	s.mu.Unlock()
	return nil
}

func validateStoryIdentity(peer domain.Peer, id int) error {
	if err := validateStoryPeer(peer); err != nil {
		return err
	}
	if id <= 0 || id > domain.MaxStoryID {
		return domain.ErrStoryIDInvalid
	}
	return nil
}

func validateStoryPeer(peer domain.Peer) error {
	switch peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		if peer.ID > 0 {
			return nil
		}
	}
	return domain.ErrStoryPeerInvalid
}

func validateStoryIDList(ids []int) error {
	if len(ids) > domain.MaxStoryIDs {
		return domain.ErrStoryIDInvalid
	}
	for _, id := range ids {
		if id <= 0 || id > domain.MaxStoryID {
			return domain.ErrStoryIDInvalid
		}
	}
	return nil
}

func validateStoryIDListNonEmpty(ids []int) error {
	if len(ids) == 0 {
		return domain.ErrStoryIDInvalid
	}
	return validateStoryIDList(ids)
}

func normalizeStoryIDList(ids []int) ([]int, error) {
	if err := validateStoryIDList(ids); err != nil {
		return nil, err
	}
	return normalizeStoryIDListUnchecked(ids), nil
}

func normalizeStoryPinnedToTopIDs(ids []int) ([]int, error) {
	ids, err := normalizeStoryIDList(ids)
	if err != nil {
		return nil, err
	}
	if len(ids) > domain.MaxStoryPinnedToTop {
		return nil, domain.ErrStoryIDInvalid
	}
	return ids, nil
}

func normalizeStoryIDListNonEmpty(ids []int) ([]int, error) {
	if err := validateStoryIDListNonEmpty(ids); err != nil {
		return nil, err
	}
	return normalizeStoryIDListUnchecked(ids), nil
}

func normalizeStoryIDListUnchecked(ids []int) []int {
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

func (s *StoryStore) storyViewsLocked(peer domain.Peer, storyID int) []domain.StoryView {
	out := make([]domain.StoryView, 0)
	for key, view := range s.views {
		if key.peerType != peer.Type || key.peerID != peer.ID || key.storyID != storyID {
			continue
		}
		out = append(out, cloneStoryView(view))
	}
	return out
}

func (s *StoryStore) storyRepostViewsLocked(peer domain.Peer, storyID int, viewerUserID int64) []domain.StoryView {
	out := make([]domain.StoryView, 0)
	for _, repost := range s.stories {
		if !s.storyCountsAsPublicRepostLocked(repost, peer, storyID, viewerUserID) {
			continue
		}
		item := cloneStory(repost)
		out = append(out, domain.StoryView{
			Owner:    peer,
			StoryID:  storyID,
			ViewerID: storyPeerCursorKey(item.Owner),
			Date:     item.Date,
			Repost:   &item,
		})
	}
	return out
}

func (s *StoryStore) populateStoryForwardCountsLocked(stories []domain.Story, viewerUserID int64) {
	for i := range stories {
		stories[i].Views.ForwardsCount = s.storyForwardCountLocked(stories[i].Owner, stories[i].ID, viewerUserID)
	}
}

func (s *StoryStore) storyForwardCountLocked(peer domain.Peer, storyID int, viewerUserID int64) int {
	count := 0
	for _, repost := range s.stories {
		if s.storyCountsAsPublicRepostLocked(repost, peer, storyID, viewerUserID) {
			count++
		}
	}
	return count
}

func (s *StoryStore) storyCountsAsPublicRepostLocked(repost domain.Story, peer domain.Peer, storyID int, viewerUserID int64) bool {
	if repost.Forward == nil || repost.Deleted || !repost.Public {
		return false
	}
	if storyForwardSourcePeer(repost.Forward) != peer || repost.Forward.StoryID != storyID {
		return false
	}
	return s.storyBaseVisibleLocked(repost, viewerUserID)
}

func storyForwardSourcePeer(forward *domain.StoryForward) domain.Peer {
	if forward == nil {
		return domain.Peer{}
	}
	if forward.Source.ID != 0 {
		return forward.Source
	}
	return forward.From
}

func (s *StoryStore) recordStoryExposures(viewerUserID int64, stories []domain.Story) {
	if viewerUserID == 0 || len(stories) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exposures == nil {
		s.exposures = make(map[storyViewKey]int)
	}
	for _, story := range stories {
		if story.ID <= 0 || story.Owner.ID == 0 {
			continue
		}
		if story.Owner.Type == domain.PeerTypeUser && story.Owner.ID == viewerUserID {
			continue
		}
		key := storyViewKey{
			peerType: story.Owner.Type,
			peerID:   story.Owner.ID,
			storyID:  story.ID,
			viewerID: viewerUserID,
		}
		if previous, ok := s.exposures[key]; !ok || story.Date > previous {
			s.exposures[key] = story.Date
		}
	}
}

func (s *StoryStore) storyVisibleLocked(story domain.Story, viewerUserID int64) bool {
	if story.Owner.Type == domain.PeerTypeChannel && !s.storyViewerChannelMemberLocked(story.Owner.ID, viewerUserID) {
		return false
	}
	return s.storyBaseVisibleLocked(story, viewerUserID)
}

func (s *StoryStore) storyBaseVisibleLocked(story domain.Story, viewerUserID int64) bool {
	contact, viewerIsContact := s.storyViewerContactLocked(story.Owner, viewerUserID)
	return story.VisibleToWithStoryFacts(
		viewerUserID,
		viewerIsContact,
		contact.CloseFriend || contact.User.CloseFriend,
		s.storyViewerBlockedLocked(story.Owner, viewerUserID),
	)
}

func (s *StoryStore) storyViewerChannelMemberLocked(channelID, viewerUserID int64) bool {
	if channelID == 0 || viewerUserID == 0 {
		return false
	}
	if s.channels != nil {
		s.channels.mu.RLock()
		defer s.channels.mu.RUnlock()
		channel, ok := s.channels.channels[channelID]
		if !ok || channel.Deleted {
			return false
		}
		member, ok := s.channels.members[channelID][viewerUserID]
		return ok && member.Status == domain.ChannelMemberActive && !member.BannedRights.ViewMessages
	}
	if members, ok := s.members[channelID]; ok {
		return members[viewerUserID]
	}
	return true
}

func (s *StoryStore) storyViewerIsContactLocked(owner domain.Peer, viewerUserID int64) bool {
	_, ok := s.storyViewerContactLocked(owner, viewerUserID)
	return ok
}

func (s *StoryStore) storyViewerContactLocked(owner domain.Peer, viewerUserID int64) (domain.Contact, bool) {
	if owner.Type != domain.PeerTypeUser || owner.ID == 0 || viewerUserID == 0 {
		return domain.Contact{}, false
	}
	contacts := s.contacts[owner.ID]
	if contacts == nil {
		return domain.Contact{}, false
	}
	contact, ok := contacts[viewerUserID]
	return contact, ok
}

func (s *StoryStore) storyViewerBlockedLocked(owner domain.Peer, viewerUserID int64) bool {
	if owner.Type != domain.PeerTypeUser || owner.ID == 0 || viewerUserID == 0 || owner.ID == viewerUserID {
		return false
	}
	blocked := s.blocked[owner.ID]
	return blocked != nil && blocked[viewerUserID]
}

func keyForStory(peer domain.Peer, id int) storyKey {
	return storyKey{peerType: peer.Type, peerID: peer.ID, storyID: id}
}

func cloneReadStatesLocked(read map[storyReadKey]domain.StoryReadState, viewerUserID int64) []domain.StoryReadState {
	out := make([]domain.StoryReadState, 0, len(read))
	for _, state := range read {
		if state.ViewerID == viewerUserID {
			out = append(out, state)
		}
	}
	return out
}

func groupPeerStories(stories []domain.Story, reads []domain.StoryReadState) []domain.PeerStories {
	readByPeer := make(map[domain.Peer]int, len(reads))
	for _, read := range reads {
		readByPeer[read.Peer] = read.MaxReadID
	}
	index := make(map[domain.Peer]int)
	out := make([]domain.PeerStories, 0)
	for _, story := range stories {
		i, ok := index[story.Owner]
		if !ok {
			i = len(out)
			index[story.Owner] = i
			out = append(out, domain.PeerStories{Peer: story.Owner, MaxReadID: readByPeer[story.Owner]})
		}
		out[i].Stories = append(out[i].Stories, cloneStory(story))
	}
	for i := range out {
		sortStoriesOldestFirst(out[i].Stories)
	}
	return out
}

type activeStoryPeerPage struct {
	peer    domain.Peer
	maxDate int
	stories []domain.Story
}

func activeStoryPeerStories(items []activeStoryPeerPage, reads []domain.StoryReadState) []domain.PeerStories {
	readByPeer := make(map[domain.Peer]int, len(reads))
	for _, read := range reads {
		readByPeer[read.Peer] = read.MaxReadID
	}
	out := make([]domain.PeerStories, 0, len(items))
	for _, item := range items {
		stories := make([]domain.Story, 0, len(item.stories))
		for _, story := range item.stories {
			stories = append(stories, cloneStory(story))
		}
		sortStoriesOldestFirst(stories)
		out = append(out, domain.PeerStories{
			Peer:      item.peer,
			MaxReadID: readByPeer[item.peer],
			Stories:   stories,
		})
	}
	return out
}

func sortActiveStoryPeerPages(items []activeStoryPeerPage) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].maxDate != items[j].maxDate {
			return items[i].maxDate > items[j].maxDate
		}
		if items[i].peer.Type != items[j].peer.Type {
			return items[i].peer.Type < items[j].peer.Type
		}
		return items[i].peer.ID < items[j].peer.ID
	})
}

func activeStoryPeerPagesAfter(items []activeStoryPeerPage, cursor domain.StoryListCursor) []activeStoryPeerPage {
	for i, item := range items {
		if activeStoryPeerPageAfter(item, cursor) {
			return items[i:]
		}
	}
	return nil
}

func activeStoryPeerPageAfter(item activeStoryPeerPage, cursor domain.StoryListCursor) bool {
	if !cursor.Set {
		return true
	}
	if item.maxDate != cursor.Date {
		return item.maxDate < cursor.Date
	}
	if item.peer.Type != cursor.Peer.Type {
		return item.peer.Type > cursor.Peer.Type
	}
	return item.peer.ID > cursor.Peer.ID
}

func latestStoryDate(stories []domain.Story) int {
	maxDate := 0
	for _, story := range stories {
		if story.Date > maxDate {
			maxDate = story.Date
		}
	}
	return maxDate
}

func sortStoriesNewestFirst(stories []domain.Story) {
	sort.Slice(stories, func(i, j int) bool {
		if stories[i].Date != stories[j].Date {
			return stories[i].Date > stories[j].Date
		}
		if stories[i].Owner != stories[j].Owner {
			if stories[i].Owner.Type != stories[j].Owner.Type {
				return stories[i].Owner.Type < stories[j].Owner.Type
			}
			return stories[i].Owner.ID < stories[j].Owner.ID
		}
		return stories[i].ID > stories[j].ID
	})
}

func sortStoriesOldestFirst(stories []domain.Story) {
	sort.Slice(stories, func(i, j int) bool {
		if stories[i].ID != stories[j].ID {
			return stories[i].ID < stories[j].ID
		}
		return stories[i].Date < stories[j].Date
	})
}

func cloneStory(story domain.Story) domain.Story {
	story.PrivacyRules = cloneStoryPrivacyRules(story.PrivacyRules)
	story.AllowUserIDs = append([]int64(nil), story.AllowUserIDs...)
	story.DisallowUserIDs = append([]int64(nil), story.DisallowUserIDs...)
	story.Entities = append([]domain.MessageEntity(nil), story.Entities...)
	story.MediaAreas = cloneStoryMediaAreas(story.MediaAreas)
	story.Forward = cloneStoryForward(story.Forward)
	story.Views.Reactions = append([]domain.ChannelMessageReactionCount(nil), story.Views.Reactions...)
	story.Views.RecentViewers = append([]int64(nil), story.Views.RecentViewers...)
	story.SentReaction = cloneReactionPtr(story.SentReaction)
	return story
}

func cloneStoryForward(in *domain.StoryForward) *domain.StoryForward {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneStoryMediaAreas(in []domain.StoryMediaArea) []domain.StoryMediaArea {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.StoryMediaArea, len(in))
	for i, area := range in {
		out[i] = area
		out[i].Reaction = cloneReactionPtr(area.Reaction)
		if area.Geo != nil {
			geo := *area.Geo
			out[i].Geo = &geo
		}
		if area.GeoAddress != nil {
			address := *area.GeoAddress
			out[i].GeoAddress = &address
		}
		if area.Venue != nil {
			venue := *area.Venue
			out[i].Venue = &venue
		}
	}
	return out
}

func fanoutStorySnapshot(story domain.Story) domain.Story {
	story = cloneStory(story)
	story.Out = false
	story.Views = domain.StoryViews{}
	story.SentReaction = nil
	return story
}

func cloneStoryPrivacyRules(in []domain.PrivacyRule) []domain.PrivacyRule {
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

func cloneReactionPtr(in *domain.MessageReaction) *domain.MessageReaction {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneStoryView(view domain.StoryView) domain.StoryView {
	view.Reaction = cloneReactionPtr(view.Reaction)
	if view.Repost != nil {
		repost := cloneStory(*view.Repost)
		view.Repost = &repost
	}
	if view.PublicForward != nil {
		forward := *view.PublicForward
		forward.Message = cloneChannelMessage(forward.Message)
		view.PublicForward = &forward
	}
	return view
}

func sameReaction(a, b *domain.MessageReaction) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

type storyInteractionCursor struct {
	set       bool
	group     int
	date      int
	viewerID  int64
	messageID int
}

func parseStoryInteractionCursor(offset string) storyInteractionCursor {
	if offset == "" {
		return storyInteractionCursor{}
	}
	parts := strings.Split(offset, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return storyInteractionCursor{}
	}
	group, err1 := strconv.Atoi(parts[0])
	date, err2 := strconv.Atoi(parts[1])
	viewerID, err3 := strconv.ParseInt(parts[2], 10, 64)
	var messageID int
	var err4 error
	if len(parts) == 4 {
		messageID, err4 = strconv.Atoi(parts[3])
	}
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || group < 0 || viewerID == 0 || messageID < 0 {
		return storyInteractionCursor{}
	}
	return storyInteractionCursor{set: true, group: group, date: date, viewerID: viewerID, messageID: messageID}
}

func formatStoryInteractionCursor(view domain.StoryView, reactionsFirst, forwardsFirst bool) string {
	group := storyViewSortGroup(view, reactionsFirst, forwardsFirst)
	out := strconv.Itoa(group) + ":" + strconv.Itoa(view.Date) + ":" + strconv.FormatInt(storyViewCursorKey(view), 10)
	if id := storyViewCursorMessageID(view); id > 0 {
		out += ":" + strconv.Itoa(id)
	}
	return out
}

func storyViewSortGroup(view domain.StoryView, reactionsFirst, forwardsFirst bool) int {
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

func storyViewCursorKey(view domain.StoryView) int64 {
	if view.PublicForward != nil {
		return storyPeerCursorKey(domain.Peer{Type: domain.PeerTypeChannel, ID: view.PublicForward.Message.ChannelID})
	}
	if view.Repost != nil {
		return storyPeerCursorKey(view.Repost.Owner)
	}
	return view.ViewerID
}

func storyViewCursorMessageID(view domain.StoryView) int {
	if view.PublicForward != nil {
		return view.PublicForward.Message.ID
	}
	return 0
}

func storyPeerCursorKey(peer domain.Peer) int64 {
	if peer.Type == domain.PeerTypeChannel {
		return -peer.ID
	}
	return peer.ID
}

func sortStoryViewsForList(views []domain.StoryView, reactionsFirst, forwardsFirst bool) {
	sort.Slice(views, func(i, j int) bool {
		gi := storyViewSortGroup(views[i], reactionsFirst, forwardsFirst)
		gj := storyViewSortGroup(views[j], reactionsFirst, forwardsFirst)
		if gi != gj {
			return gi < gj
		}
		if views[i].Date != views[j].Date {
			return views[i].Date > views[j].Date
		}
		if storyViewCursorKey(views[i]) != storyViewCursorKey(views[j]) {
			return storyViewCursorKey(views[i]) > storyViewCursorKey(views[j])
		}
		return storyViewCursorMessageID(views[i]) > storyViewCursorMessageID(views[j])
	})
}

func pageStoryViews(views []domain.StoryView, limit int, cursor storyInteractionCursor, reactionsFirst, forwardsFirst bool) ([]domain.StoryView, string) {
	if limit <= 0 || limit > domain.MaxStoryInteractionListLimit {
		limit = domain.MaxStoryInteractionListLimit
	}
	out := make([]domain.StoryView, 0, min(limit, len(views)))
	for _, view := range views {
		if !storyViewAfterCursor(view, cursor, reactionsFirst, forwardsFirst) {
			continue
		}
		if len(out) == limit {
			return out, formatStoryInteractionCursor(out[len(out)-1], reactionsFirst, forwardsFirst)
		}
		out = append(out, cloneStoryView(view))
	}
	return out, ""
}

func storyViewAfterCursor(view domain.StoryView, cursor storyInteractionCursor, reactionsFirst, forwardsFirst bool) bool {
	if !cursor.set {
		return true
	}
	group := storyViewSortGroup(view, reactionsFirst, forwardsFirst)
	if group != cursor.group {
		return group > cursor.group
	}
	if view.Date != cursor.date {
		return view.Date < cursor.date
	}
	key := storyViewCursorKey(view)
	if key != cursor.viewerID {
		return key < cursor.viewerID
	}
	return storyViewCursorMessageID(view) < cursor.messageID
}

func filterStoryViewsForList(views []domain.StoryView, req domain.StoryViewListRequest, profiles map[int64]domain.User, contacts map[int64]domain.Contact) []domain.StoryView {
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if query == "" && !req.JustContacts {
		return views
	}
	out := make([]domain.StoryView, 0, len(views))
	for _, view := range views {
		contact, isContact := contacts[view.ViewerID]
		if req.JustContacts && !isContact {
			continue
		}
		if query != "" && !storyViewerMatchesQuery(view.ViewerID, query, profiles[view.ViewerID], contact, isContact) {
			continue
		}
		out = append(out, view)
	}
	return out
}

func storyViewerMatchesQuery(viewerID int64, query string, profile domain.User, contact domain.Contact, isContact bool) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return true
	}
	trimmedAt := strings.TrimPrefix(query, "@")
	candidates := []string{
		profile.FirstName,
		profile.LastName,
		strings.TrimSpace(profile.FirstName + " " + profile.LastName),
		profile.Username,
		strconv.FormatInt(viewerID, 10),
	}
	if isContact {
		candidates = append(candidates,
			contact.FirstName,
			contact.LastName,
			strings.TrimSpace(contact.FirstName+" "+contact.LastName),
			contact.Phone,
			contact.User.FirstName,
			contact.User.LastName,
			strings.TrimSpace(contact.User.FirstName+" "+contact.User.LastName),
			contact.User.Username,
		)
	}
	for _, candidate := range candidates {
		value := strings.ToLower(strings.TrimSpace(candidate))
		if value == "" {
			continue
		}
		if strings.Contains(value, query) || (trimmedAt != query && strings.Contains(value, trimmedAt)) {
			return true
		}
	}
	return false
}

func cloneStoryViewerProfiles(in map[int64]domain.User) map[int64]domain.User {
	if len(in) == 0 {
		return nil
	}
	out := make(map[int64]domain.User, len(in))
	for id, user := range in {
		out[id] = user
	}
	return out
}

func cloneStoryViewerContacts(in map[int64]domain.Contact) map[int64]domain.Contact {
	if len(in) == 0 {
		return nil
	}
	out := make(map[int64]domain.Contact, len(in))
	for id, contact := range in {
		out[id] = cloneContact(contact)
	}
	return out
}

func storyViewCounts(views []domain.StoryView) (count, viewsCount, reactionsCount int) {
	for _, view := range views {
		if view.Repost != nil || view.ViewerID == 0 {
			continue
		}
		count++
		viewsCount++
		if view.Reaction != nil {
			reactionsCount++
		}
	}
	return count, viewsCount, reactionsCount
}

func clampStoryInteractionLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxStoryInteractionListLimit {
		return domain.MaxStoryInteractionListLimit
	}
	return limit
}

func clampStoryPrivacyFanoutLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxStoryPrivacyFanoutTargets {
		return domain.MaxStoryPrivacyFanoutTargets
	}
	return limit
}

func adjustStoryReactionCounts(views domain.StoryViews, old, next *domain.MessageReaction) domain.StoryViews {
	if old != nil {
		for i := range views.Reactions {
			if views.Reactions[i].Reaction == *old {
				views.Reactions[i].Count--
				if views.Reactions[i].Count <= 0 {
					views.Reactions = append(views.Reactions[:i], views.Reactions[i+1:]...)
				}
				break
			}
		}
		if views.ReactionsCount > 0 {
			views.ReactionsCount--
		}
	}
	if next != nil {
		found := false
		for i := range views.Reactions {
			if views.Reactions[i].Reaction == *next {
				views.Reactions[i].Count++
				found = true
				break
			}
		}
		if !found {
			views.Reactions = append(views.Reactions, domain.ChannelMessageReactionCount{Reaction: *next, Count: 1, ChosenOrder: len(views.Reactions)})
		}
		views.ReactionsCount++
	}
	return views
}

func prependRecentViewer(in []int64, viewerID int64) []int64 {
	out := make([]int64, 0, min(3, len(in)+1))
	out = append(out, viewerID)
	for _, id := range in {
		if id == viewerID {
			continue
		}
		out = append(out, id)
		if len(out) == 3 {
			break
		}
	}
	return out
}
