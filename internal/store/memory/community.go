package memory

import (
	"context"
	"encoding/base64"
	"errors"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"sync"

	"telesrv/internal/domain"
)

type CommunityStore struct {
	mu          sync.RWMutex
	users       *UserStore
	channels    *ChannelStore
	bots        *BotStore
	dialogs     *DialogStore
	nextID      int64
	nextHash    int64
	communities map[int64]domain.Community
	members     map[int64]map[int64]domain.CommunityMember
	links       map[int64]map[domain.Peer]domain.CommunityPeerLink
	requests    map[int64]map[domain.Peer]domain.CommunityPeerLinkRequest
	states      map[int64]map[int64]domain.CommunityUserState
}

func NewCommunityStore(users *UserStore, channels *ChannelStore, bots *BotStore, dialogs *DialogStore) *CommunityStore {
	return &CommunityStore{
		users: users, channels: channels, bots: bots, dialogs: dialogs,
		nextID: 3000000000, nextHash: 990000000000,
		communities: map[int64]domain.Community{}, members: map[int64]map[int64]domain.CommunityMember{},
		links: map[int64]map[domain.Peer]domain.CommunityPeerLink{}, requests: map[int64]map[domain.Peer]domain.CommunityPeerLinkRequest{},
		states: map[int64]map[int64]domain.CommunityUserState{},
	}
}

func cloneCommunity(c domain.Community) domain.Community {
	c.PhotoStripped = append([]byte(nil), c.PhotoStripped...)
	return c
}
func cloneCommunityView(v domain.CommunityView) domain.CommunityView {
	v.Community = cloneCommunity(v.Community)
	v.Links = append([]domain.CommunityPeerLink(nil), v.Links...)
	v.ServiceMessages = append([]domain.SendChannelMessageResult(nil), v.ServiceMessages...)
	return v
}

func (s *CommunityStore) communityLocked(id int64) (domain.Community, error) {
	c, ok := s.communities[id]
	if !ok || c.Deleted {
		return domain.Community{}, domain.ErrCommunityInvalid
	}
	return c, nil
}

func (s *CommunityStore) derivedMemberLocked(c domain.Community, userID int64) (domain.CommunityMember, bool) {
	if m, ok := s.members[c.ID][userID]; ok {
		return m, true
	}
	if s.channels != nil {
		s.channels.mu.RLock()
		for p := range s.links[c.ID] {
			if p.Type != domain.PeerTypeChannel {
				continue
			}
			if m, ok := s.channels.members[p.ID][userID]; ok && m.Status == domain.ChannelMemberActive {
				s.channels.mu.RUnlock()
				return domain.CommunityMember{CommunityID: c.ID, UserID: userID, Role: domain.CommunityRoleMember, Status: domain.CommunityMemberActive, Date: c.Date}, true
			}
		}
		s.channels.mu.RUnlock()
	}
	if s.dialogs != nil {
		s.dialogs.mu.RLock()
		for p := range s.links[c.ID] {
			if p.Type != domain.PeerTypeUser {
				continue
			}
			for _, d := range s.dialogs.m[userID].Dialogs {
				if d.Peer == p && d.TopMessage > 0 {
					s.dialogs.mu.RUnlock()
					return domain.CommunityMember{CommunityID: c.ID, UserID: userID, Role: domain.CommunityRoleMember, Status: domain.CommunityMemberActive, Date: c.Date}, true
				}
			}
		}
		s.dialogs.mu.RUnlock()
	}
	return domain.CommunityMember{}, false
}

func (s *CommunityStore) viewLocked(userID, id int64) (domain.CommunityView, error) {
	c, e := s.communityLocked(id)
	if e != nil {
		return domain.CommunityView{}, e
	}
	m, ok := s.derivedMemberLocked(c, userID)
	if !ok || !m.Active() {
		return domain.CommunityView{Community: cloneCommunity(c), Self: m, Forbidden: true}, domain.ErrCommunityPrivate
	}
	v := domain.CommunityView{Community: cloneCommunity(c), Self: m, State: s.states[id][userID]}
	v.State.CommunityID = id
	v.State.UserID = userID
	for _, l := range s.links[id] {
		joined := false
		inherentlyViewable := l.Peer.Type == domain.PeerTypeUser
		if l.Peer.Type == domain.PeerTypeChannel && s.channels != nil {
			s.channels.mu.RLock()
			cm, ok := s.channels.members[l.Peer.ID][userID]
			joined = ok && cm.Status == domain.ChannelMemberActive
			if channel, ok := s.channels.channels[l.Peer.ID]; ok {
				inherentlyViewable = publicPreviewableChannel(channel)
			}
			s.channels.mu.RUnlock()
		} else if l.Peer.Type == domain.PeerTypeUser && s.dialogs != nil {
			s.dialogs.mu.RLock()
			for _, d := range s.dialogs.m[userID].Dialogs {
				if d.Peer == l.Peer && d.TopMessage > 0 {
					joined = true
					break
				}
			}
			s.dialogs.mu.RUnlock()
		}
		if l.Visibility == domain.CommunityPeerHidden && !joined && !m.CanManageLinkedPeers() {
			continue
		}
		// Community administration reveals hidden links but never grants access
		// to a linked private channel's history. TDesktop uses this bit to decide
		// between opening History directly and showing the join prompt.
		l.CanViewHistory = joined || inherentlyViewable
		v.Links = append(v.Links, l)
	}
	if s.channels != nil {
		s.channels.mu.RLock()
		for _, l := range v.Links {
			if l.Peer.Type == domain.PeerTypeChannel {
				if ch, ok := s.channels.channels[l.Peer.ID]; ok {
					v.Channels = append(v.Channels, cloneChannel(ch))
				}
			}
		}
		s.channels.mu.RUnlock()
	}
	if s.users != nil {
		s.users.mu.RLock()
		for _, l := range v.Links {
			if l.Peer.Type == domain.PeerTypeUser {
				if u, ok := s.users.byID[l.Peer.ID]; ok {
					v.Users = append(v.Users, u)
				}
			}
		}
		s.users.mu.RUnlock()
	}
	for _, cm := range s.members[id] {
		if cm.Status == domain.CommunityMemberKicked {
			v.KickedCount++
		} else if cm.Role == domain.CommunityRoleCreator || cm.Role == domain.CommunityRoleAdmin {
			v.AdminsCount++
		}
	}
	v.PendingRequests = len(s.requests[id])
	sort.Slice(v.Links, func(i, j int) bool {
		if v.Links[i].Date != v.Links[j].Date {
			return v.Links[i].Date < v.Links[j].Date
		}
		if v.Links[i].Peer.Type != v.Links[j].Peer.Type {
			return v.Links[i].Peer.Type < v.Links[j].Peer.Type
		}
		return v.Links[i].Peer.ID < v.Links[j].Peer.ID
	})
	return v, nil
}

func (s *CommunityStore) GetCommunity(_ context.Context, userID, id int64) (domain.CommunityView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.viewLocked(userID, id)
}
func (s *CommunityStore) GetCommunities(ctx context.Context, userID int64, ids []int64) ([]domain.CommunityView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []domain.CommunityView{}
	seen := map[int64]struct{}{}
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		v, e := s.viewLocked(userID, id)
		if errors.Is(e, domain.ErrCommunityInvalid) || errors.Is(e, domain.ErrCommunityPrivate) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *CommunityStore) ListJoinedCommunities(ctx context.Context, userID int64) ([]domain.CommunityView, error) {
	s.mu.RLock()
	ids := make([]int64, 0, len(s.communities))
	for id := range s.communities {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return s.GetCommunities(ctx, userID, ids)
}

func (s *CommunityStore) validatePeerLocked(actor int64, p domain.Peer) error {
	for _, byPeer := range s.links {
		if _, ok := byPeer[p]; ok {
			return domain.ErrCommunityPeerLinked
		}
	}
	switch p.Type {
	case domain.PeerTypeChannel:
		if s.channels == nil {
			return domain.ErrCommunityPeerInvalid
		}
		s.channels.mu.RLock()
		defer s.channels.mu.RUnlock()
		ch, ok := s.channels.channels[p.ID]
		if !ok || ch.Deleted || ch.Monoforum || ch.LinkedCommunityID != 0 {
			return domain.ErrCommunityPeerInvalid
		}
		m, ok := s.channels.members[p.ID][actor]
		if !ok || m.Status != domain.ChannelMemberActive || (m.Role != domain.ChannelRoleCreator && m.Role != domain.ChannelRoleAdmin) {
			return domain.ErrCommunityAdminRequired
		}
	case domain.PeerTypeUser:
		if s.users == nil || s.bots == nil {
			return domain.ErrCommunityPeerInvalid
		}
		s.users.mu.RLock()
		u, ok := s.users.byID[p.ID]
		s.users.mu.RUnlock()
		if !ok || !u.Bot || u.Deleted || u.LinkedCommunityID != 0 {
			return domain.ErrCommunityPeerInvalid
		}
		s.bots.mu.RLock()
		profile, ok := s.bots.byID[p.ID]
		s.bots.mu.RUnlock()
		if !ok || profile.OwnerUserID != actor {
			return domain.ErrCommunityAdminRequired
		}
	default:
		return domain.ErrCommunityPeerInvalid
	}
	return nil
}

func (s *CommunityStore) setPeerLinkLocked(p domain.Peer, id int64) error {
	switch p.Type {
	case domain.PeerTypeChannel:
		s.channels.mu.Lock()
		ch, ok := s.channels.channels[p.ID]
		if ok {
			ch.LinkedCommunityID = id
			s.channels.channels[p.ID] = ch
		}
		s.channels.mu.Unlock()
		if !ok {
			return domain.ErrCommunityPeerInvalid
		}
	case domain.PeerTypeUser:
		s.users.mu.Lock()
		u, ok := s.users.byID[p.ID]
		if ok {
			u.LinkedCommunityID = id
			s.users.byID[p.ID] = u
		}
		s.users.mu.Unlock()
		if !ok {
			return domain.ErrCommunityPeerInvalid
		}
	default:
		return domain.ErrCommunityPeerInvalid
	}
	return nil
}

func (s *CommunityStore) insertLinkLocked(id, actor int64, p domain.Peer, v domain.CommunityPeerVisibility, date int) (domain.CommunityPeerLink, error) {
	if e := s.validatePeerLocked(actor, p); e != nil {
		return domain.CommunityPeerLink{}, e
	}
	channels, bots := 0, 0
	for peer := range s.links[id] {
		if peer.Type == domain.PeerTypeChannel {
			channels++
		} else {
			bots++
		}
	}
	if (p.Type == domain.PeerTypeChannel && channels >= domain.MaxCommunityPeers) || (p.Type == domain.PeerTypeUser && bots >= domain.MaxCommunityBotPeers) {
		return domain.CommunityPeerLink{}, domain.ErrCommunityPeersTooMuch
	}
	l := domain.CommunityPeerLink{CommunityID: id, Peer: p, Visibility: v, CanViewHistory: true, CreatedBy: actor, Date: date}
	if s.links[id] == nil {
		s.links[id] = map[domain.Peer]domain.CommunityPeerLink{}
	}
	s.links[id][p] = l
	if e := s.setPeerLinkLocked(p, id); e != nil {
		delete(s.links[id], p)
		return domain.CommunityPeerLink{}, e
	}
	return l, nil
}

func (s *CommunityStore) unlinkLocked(id int64, peer domain.Peer) {
	delete(s.links[id], peer)
	_ = s.setPeerLinkLocked(peer, 0)
}

func (s *CommunityStore) CreateCommunity(_ context.Context, req domain.CreateCommunityRequest) (domain.CommunityView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.validatePeerLocked(req.CreatorUserID, req.InitialPeer); e != nil {
		return domain.CommunityView{}, e
	}
	id := s.nextID
	s.nextID++
	if s.channels != nil {
		s.channels.mu.Lock()
		if id < s.channels.nextID {
			id = s.channels.nextID
		}
		s.channels.nextID = id + 1
		s.channels.mu.Unlock()
	}
	hash := s.nextHash
	s.nextHash++
	c := domain.Community{ID: id, AccessHash: hash, CreatorUserID: req.CreatorUserID, Title: req.Title, About: req.About, Date: req.Date}
	s.communities[id] = c
	s.members[id] = map[int64]domain.CommunityMember{req.CreatorUserID: {CommunityID: id, UserID: req.CreatorUserID, Role: domain.CommunityRoleCreator, Status: domain.CommunityMemberActive, AdminRights: domain.CreatorChannelAdminRights(), Date: req.Date}}
	s.links[id] = map[domain.Peer]domain.CommunityPeerLink{}
	s.requests[id] = map[domain.Peer]domain.CommunityPeerLinkRequest{}
	s.states[id] = map[int64]domain.CommunityUserState{}
	l, e := s.insertLinkLocked(id, req.CreatorUserID, req.InitialPeer, req.Visibility, req.Date)
	if e != nil {
		delete(s.communities, id)
		return domain.CommunityView{}, e
	}
	serviceMessage, e := s.appendCommunityServiceMessageLocked(req.InitialPeer, req.CreatorUserID, req.Date, c.ID)
	if e != nil {
		s.unlinkLocked(id, req.InitialPeer)
		delete(s.communities, id)
		return domain.CommunityView{}, e
	}
	view := domain.CommunityView{Community: c, Self: s.members[id][req.CreatorUserID], Links: []domain.CommunityPeerLink{l}, AdminsCount: 1}
	if serviceMessage != nil {
		view.ServiceMessages = append(view.ServiceMessages, *serviceMessage)
	}
	return view, nil
}

func (s *CommunityStore) actorLocked(id, user int64) (domain.Community, domain.CommunityMember, error) {
	c, e := s.communityLocked(id)
	if e != nil {
		return domain.Community{}, domain.CommunityMember{}, e
	}
	m, ok := s.derivedMemberLocked(c, user)
	if !ok || !m.Active() {
		return domain.Community{}, domain.CommunityMember{}, domain.ErrCommunityPrivate
	}
	return c, m, nil
}

func (s *CommunityStore) ToggleCommunityPeerLink(_ context.Context, req domain.CommunityTogglePeerLinkRequest) (domain.CommunityTogglePeerLinkResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, m, e := s.actorLocked(req.CommunityID, req.ActorUserID)
	if e != nil {
		return domain.CommunityTogglePeerLinkResult{}, e
	}
	if req.Deleted {
		if !m.CanManageLinkedPeers() {
			return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityAdminRequired
		}
		if _, ok := s.links[c.ID][req.Peer]; !ok {
			return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityPeerInvalid
		}
		if e := s.setPeerLinkLocked(req.Peer, 0); e != nil {
			return domain.CommunityTogglePeerLinkResult{}, e
		}
		serviceMessage, e := s.appendCommunityServiceMessageLocked(req.Peer, req.ActorUserID, req.Date, 0)
		if e != nil {
			_ = s.setPeerLinkLocked(req.Peer, c.ID)
			return domain.CommunityTogglePeerLinkResult{}, e
		}
		delete(s.links[c.ID], req.Peer)
		return domain.CommunityTogglePeerLinkResult{Community: c, Peer: req.Peer, ServiceMessage: serviceMessage, Removed: true}, nil
	}
	if m.CanManageLinkedPeers() {
		l, e := s.insertLinkLocked(c.ID, req.ActorUserID, req.Peer, req.Visibility, req.Date)
		if e != nil {
			return domain.CommunityTogglePeerLinkResult{}, e
		}
		serviceMessage, e := s.appendCommunityServiceMessageLocked(req.Peer, req.ActorUserID, req.Date, c.ID)
		if e != nil {
			s.unlinkLocked(c.ID, req.Peer)
			return domain.CommunityTogglePeerLinkResult{}, e
		}
		return domain.CommunityTogglePeerLinkResult{Community: c, Peer: req.Peer, Link: &l, ServiceMessage: serviceMessage}, nil
	}
	if c.DefaultBannedRights.ManageLinkedPeers {
		return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityAdminRequired
	}
	if e := s.validatePeerLocked(req.ActorUserID, req.Peer); e != nil {
		return domain.CommunityTogglePeerLinkResult{}, e
	}
	s.requests[c.ID][req.Peer] = domain.CommunityPeerLinkRequest{CommunityID: c.ID, Peer: req.Peer, RequestedBy: req.ActorUserID, Visibility: req.Visibility, Date: req.Date}
	return domain.CommunityTogglePeerLinkResult{Community: c, Peer: req.Peer, RequestCreated: true}, nil
}

func (s *CommunityStore) appendCommunityServiceMessageLocked(peer domain.Peer, actorUserID int64, date int, communityID int64) (*domain.SendChannelMessageResult, error) {
	if peer.Type != domain.PeerTypeChannel || s.channels == nil {
		return nil, nil
	}
	s.channels.mu.Lock()
	defer s.channels.mu.Unlock()
	channel, ok := s.channels.channels[peer.ID]
	if !ok || channel.Deleted {
		return nil, domain.ErrChannelInvalid
	}
	message, event := s.channels.appendChannelServiceMessageLocked(peer.ID, actorUserID, date, domain.ChannelMessageAction{
		Type:        domain.ChannelActionChangeCommunity,
		CommunityID: communityID,
	})
	channel = s.channels.channels[peer.ID]
	channel.TopMessageID = message.ID
	channel.Pts = event.Pts
	s.channels.channels[peer.ID] = channel
	return &domain.SendChannelMessageResult{
		Channel: channel, Message: message, Event: event,
		Recipients: s.channels.activeMemberIDsLocked(peer.ID, 0, 0),
	}, nil
}

func (s *CommunityStore) SetCommunityCollapsed(_ context.Context, user, id int64, collapsed bool) (domain.CommunityView, bool, error) {
	s.mu.Lock()
	c, _, e := s.actorLocked(id, user)
	if e != nil {
		s.mu.Unlock()
		return domain.CommunityView{}, false, e
	}
	state := s.states[id][user]
	changed := state.Collapsed != collapsed
	state.CommunityID, state.UserID, state.Collapsed = id, user, collapsed
	if !collapsed {
		state.Pinned = false
		state.PinnedOrder = 0
	}
	s.states[id][user] = state
	v, e := s.viewLocked(user, c.ID)
	s.mu.Unlock()
	return v, changed, e
}

func encodeMemoryCommunityOffset(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(n)))
}
func decodeMemoryCommunityOffset(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return 0, domain.ErrCommunityInvalid
	}
	n, e := strconv.Atoi(string(b))
	if e != nil || n < 0 {
		return 0, domain.ErrCommunityInvalid
	}
	return n, nil
}

func (s *CommunityStore) ListCommunityPeerLinkRequests(_ context.Context, user, id int64, offset string, limit int) (domain.CommunityPeerLinkRequestPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, m, e := s.actorLocked(id, user)
	if e != nil {
		return domain.CommunityPeerLinkRequestPage{}, e
	}
	if !m.CanManageLinkedPeers() {
		return domain.CommunityPeerLinkRequestPage{}, domain.ErrCommunityAdminRequired
	}
	start, e := decodeMemoryCommunityOffset(offset)
	if e != nil {
		return domain.CommunityPeerLinkRequestPage{}, e
	}
	items := make([]domain.CommunityPeerLinkRequest, 0, len(s.requests[id]))
	for _, r := range s.requests[id] {
		items = append(items, r)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Date != items[j].Date {
			return items[i].Date > items[j].Date
		}
		if items[i].Peer.Type != items[j].Peer.Type {
			return items[i].Peer.Type > items[j].Peer.Type
		}
		return items[i].Peer.ID > items[j].Peer.ID
	})
	page := domain.CommunityPeerLinkRequestPage{TotalCount: len(items)}
	if start > len(items) {
		start = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	page.Requests = append(page.Requests, items[start:end]...)
	if end < len(items) {
		page.NextOffset = encodeMemoryCommunityOffset(end)
	}
	if s.channels != nil {
		s.channels.mu.RLock()
		for _, r := range page.Requests {
			if r.Peer.Type == domain.PeerTypeChannel {
				if ch, ok := s.channels.channels[r.Peer.ID]; ok {
					page.Channels = append(page.Channels, cloneChannel(ch))
				}
			}
		}
		s.channels.mu.RUnlock()
	}
	if s.users != nil {
		s.users.mu.RLock()
		seen := map[int64]struct{}{}
		for _, r := range page.Requests {
			ids := []int64{r.RequestedBy}
			if r.Peer.Type == domain.PeerTypeUser {
				ids = append(ids, r.Peer.ID)
			}
			for _, uid := range ids {
				if _, ok := seen[uid]; ok {
					continue
				}
				seen[uid] = struct{}{}
				if u, ok := s.users.byID[uid]; ok {
					page.Users = append(page.Users, u)
				}
			}
		}
		s.users.mu.RUnlock()
	}
	return page, nil
}

func (s *CommunityStore) decideLocked(actor, id int64, p domain.Peer, reject bool, date int) (domain.CommunityTogglePeerLinkResult, error) {
	c, m, e := s.actorLocked(id, actor)
	if e != nil {
		return domain.CommunityTogglePeerLinkResult{}, e
	}
	if !m.CanManageLinkedPeers() {
		return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityAdminRequired
	}
	r, ok := s.requests[id][p]
	if !ok {
		return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityRequestMissing
	}
	delete(s.requests[id], p)
	if reject {
		return domain.CommunityTogglePeerLinkResult{Community: c, Peer: p, RequestedBy: r.RequestedBy}, nil
	}
	l, e := s.insertLinkLocked(id, r.RequestedBy, p, r.Visibility, date)
	if e != nil {
		s.requests[id][p] = r
		return domain.CommunityTogglePeerLinkResult{}, e
	}
	serviceMessage, e := s.appendCommunityServiceMessageLocked(p, actor, date, c.ID)
	if e != nil {
		s.unlinkLocked(id, p)
		return domain.CommunityTogglePeerLinkResult{}, e
	}
	return domain.CommunityTogglePeerLinkResult{Community: c, Peer: p, RequestedBy: r.RequestedBy, Link: &l, ServiceMessage: serviceMessage}, nil
}
func (s *CommunityStore) DecideCommunityPeerLinkRequest(_ context.Context, actor, id int64, p domain.Peer, reject bool, date int) (domain.CommunityTogglePeerLinkResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decideLocked(actor, id, p, reject, date)
}
func (s *CommunityStore) DecideAllCommunityPeerLinkRequests(_ context.Context, actor, id int64, reject bool, date int) ([]domain.CommunityTogglePeerLinkResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, m, e := s.actorLocked(id, actor)
	if e != nil {
		return nil, e
	}
	if !m.CanManageLinkedPeers() {
		return nil, domain.ErrCommunityAdminRequired
	}
	peers := make([]domain.Peer, 0, len(s.requests[id]))
	for p := range s.requests[id] {
		peers = append(peers, p)
	}
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Type != peers[j].Type {
			return peers[i].Type < peers[j].Type
		}
		return peers[i].ID < peers[j].ID
	})
	if reject {
		s.requests[id] = map[domain.Peer]domain.CommunityPeerLinkRequest{}
		return make([]domain.CommunityTogglePeerLinkResult, len(peers)), nil
	}
	channels, bots := 0, 0
	for p := range s.links[id] {
		if p.Type == domain.PeerTypeChannel {
			channels++
		} else {
			bots++
		}
	}
	for _, p := range peers {
		if p.Type == domain.PeerTypeChannel {
			channels++
		} else {
			bots++
		}
		if channels > domain.MaxCommunityPeers || bots > domain.MaxCommunityBotPeers {
			return nil, domain.ErrCommunityPeersTooMuch
		}
		r := s.requests[id][p]
		if e := s.validatePeerLocked(r.RequestedBy, p); e != nil {
			return nil, e
		}
	}
	out := []domain.CommunityTogglePeerLinkResult{}
	for _, p := range peers {
		r := s.requests[id][p]
		l, e := s.insertLinkLocked(id, r.RequestedBy, p, r.Visibility, date)
		if e != nil {
			return nil, e
		}
		delete(s.requests[id], p)
		serviceMessage, e := s.appendCommunityServiceMessageLocked(p, actor, date, s.communities[id].ID)
		if e != nil {
			s.unlinkLocked(id, p)
			return nil, e
		}
		out = append(out, domain.CommunityTogglePeerLinkResult{Community: s.communities[id], Peer: p, RequestedBy: r.RequestedBy, Link: &l, ServiceMessage: serviceMessage})
	}
	return out, nil
}

func (s *CommunityStore) GetCommunityParticipantJoinedChats(_ context.Context, user, id, participant int64) (domain.CommunityParticipantJoinedChats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, m, e := s.actorLocked(id, user)
	if e != nil {
		return domain.CommunityParticipantJoinedChats{}, e
	}
	if !m.CanBanUsers() && user != participant {
		return domain.CommunityParticipantJoinedChats{}, domain.ErrCommunityAdminRequired
	}
	out := domain.CommunityParticipantJoinedChats{}
	if s.channels != nil {
		s.channels.mu.RLock()
		for p := range s.links[id] {
			if p.Type != domain.PeerTypeChannel {
				continue
			}
			cm, ok := s.channels.members[p.ID][participant]
			if !ok || cm.Status != domain.ChannelMemberActive {
				continue
			}
			out.JoinedChatIDs = append(out.JoinedChatIDs, p.ID)
			if cm.Role == domain.ChannelRoleCreator {
				out.CreatorChatIDs = append(out.CreatorChatIDs, p.ID)
			}
			if ch, ok := s.channels.channels[p.ID]; ok {
				out.Channels = append(out.Channels, cloneChannel(ch))
			}
		}
		s.channels.mu.RUnlock()
	}
	if s.users != nil {
		s.users.mu.RLock()
		if participantUser, ok := s.users.byID[participant]; ok {
			out.Users = append(out.Users, participantUser)
		}
		s.users.mu.RUnlock()
	}
	sort.Slice(out.JoinedChatIDs, func(i, j int) bool { return out.JoinedChatIDs[i] < out.JoinedChatIDs[j] })
	sort.Slice(out.CreatorChatIDs, func(i, j int) bool { return out.CreatorChatIDs[i] < out.CreatorChatIDs[j] })
	return out, nil
}

func (s *CommunityStore) ToggleCommunityParticipantBanned(_ context.Context, actor, id, participant int64, unban bool, date int) (domain.CommunityParticipantBanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, m, e := s.actorLocked(id, actor)
	if e != nil {
		return domain.CommunityParticipantBanResult{}, e
	}
	if !m.CanBanUsers() || participant == c.CreatorUserID {
		return domain.CommunityParticipantBanResult{}, domain.ErrCommunityAdminRequired
	}
	if unban {
		old, ok := s.members[id][participant]
		if ok && old.Role == domain.CommunityRoleMember && old.Status == domain.CommunityMemberKicked {
			delete(s.members[id], participant)
			return domain.CommunityParticipantBanResult{Changed: true}, nil
		}
		return domain.CommunityParticipantBanResult{}, nil
	}
	if participantMember, ok := s.derivedMemberLocked(c, participant); !ok ||
		(participantMember.Status != domain.CommunityMemberActive && participantMember.Status != domain.CommunityMemberKicked) {
		return domain.CommunityParticipantBanResult{}, domain.ErrCommunityParticipantInvalid
	}
	old, alreadyKicked := s.members[id][participant]
	alreadyKicked = alreadyKicked && old.Role == domain.CommunityRoleMember && old.Status == domain.CommunityMemberKicked
	result := domain.CommunityParticipantBanResult{}
	for p := range s.links[id] {
		owned := false
		if p.Type == domain.PeerTypeChannel && s.channels != nil {
			s.channels.mu.Lock()
			ch := s.channels.channels[p.ID]
			owned = ch.CreatorUserID == participant
			if !owned {
				if cm, ok := s.channels.members[p.ID][participant]; ok && cm.Status == domain.ChannelMemberActive {
					previous := cm
					cm.Status = domain.ChannelMemberKicked
					cm.Role = domain.ChannelRoleMember
					cm.InviterUserID = actor
					cm.LeftAt = date
					cm.BannedRights = domain.ChannelBannedRights{ViewMessages: true}
					s.channels.members[p.ID][participant] = cm
					ch.ParticipantsCount--
					ch.KickedCount++
					s.channels.channels[p.ID] = ch
					event := transientChannelParticipantEvent(ch.ID, actor, previous, cm, date)
					var serviceMessage domain.ChannelMessage
					var serviceEvent domain.ChannelUpdateEvent
					if ch.Megagroup {
						serviceMessage, serviceEvent = s.channels.appendChannelServiceMessageLocked(ch.ID, actor, date, domain.ChannelMessageAction{Type: domain.ChannelActionChatDelete, UserIDs: []int64{participant}})
						ch = s.channels.channels[p.ID]
						ch.TopMessageID, ch.Pts = serviceMessage.ID, serviceEvent.Pts
						s.channels.channels[p.ID] = ch
					}
					recipients := s.channels.activeMemberIDsLocked(p.ID, 0, 0)
					recipients = append(recipients, participant)
					result.ChannelBans = append(result.ChannelBans, domain.EditChannelBannedResult{
						Channel: ch, Previous: previous, Participant: cm, Event: event, Recipients: recipients,
						Date: date, Message: serviceMessage, ServiceEvent: serviceEvent,
					})
				}
			}
			s.channels.mu.Unlock()
		} else if p.Type == domain.PeerTypeUser && s.bots != nil {
			s.bots.mu.RLock()
			owned = s.bots.byID[p.ID].OwnerUserID == participant
			s.bots.mu.RUnlock()
		}
		if owned {
			if e := s.setPeerLinkLocked(p, 0); e != nil {
				return domain.CommunityParticipantBanResult{}, e
			}
			serviceMessage, e := s.appendCommunityServiceMessageLocked(p, actor, date, 0)
			if e != nil {
				_ = s.setPeerLinkLocked(p, c.ID)
				return domain.CommunityParticipantBanResult{}, e
			}
			delete(s.links[id], p)
			result.RemovedLinks = append(result.RemovedLinks, domain.CommunityTogglePeerLinkResult{Community: c, Peer: p, Removed: true, ServiceMessage: serviceMessage})
		}
	}
	if !alreadyKicked {
		s.members[id][participant] = domain.CommunityMember{CommunityID: id, UserID: participant, Role: domain.CommunityRoleMember, Status: domain.CommunityMemberKicked, Date: date}
	}
	result.Changed = !alreadyKicked || len(result.ChannelBans) > 0 || len(result.RemovedLinks) > 0
	return result, nil
}

func (s *CommunityStore) allParticipantsLocked(id int64) []domain.CommunityMember {
	byID := map[int64]domain.CommunityMember{}
	for uid, m := range s.members[id] {
		byID[uid] = m
	}
	if s.channels != nil {
		s.channels.mu.RLock()
		for p := range s.links[id] {
			if p.Type != domain.PeerTypeChannel {
				continue
			}
			for uid, cm := range s.channels.members[p.ID] {
				if cm.Status == domain.ChannelMemberActive {
					if _, ok := byID[uid]; !ok {
						byID[uid] = domain.CommunityMember{CommunityID: id, UserID: uid, Role: domain.CommunityRoleMember, Status: domain.CommunityMemberActive}
					}
				}
			}
		}
		s.channels.mu.RUnlock()
	}
	out := make([]domain.CommunityMember, 0, len(byID))
	for _, m := range byID {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		rank := func(r domain.CommunityMemberRole) int {
			if r == domain.CommunityRoleCreator {
				return 0
			}
			if r == domain.CommunityRoleAdmin {
				return 1
			}
			return 2
		}
		if rank(out[i].Role) != rank(out[j].Role) {
			return rank(out[i].Role) < rank(out[j].Role)
		}
		return out[i].UserID < out[j].UserID
	})
	return out
}
func (s *CommunityStore) ListCommunityParticipants(_ context.Context, user, id int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.CommunityParticipantList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, m, e := s.actorLocked(id, user)
	if e != nil {
		return domain.CommunityParticipantList{}, e
	}
	restricted := filter.Kind == domain.ChannelParticipantsKicked || filter.Kind == domain.ChannelParticipantsBanned
	if restricted && !m.CanManageLinkedPeers() {
		return domain.CommunityParticipantList{}, domain.ErrCommunityAdminRequired
	}
	all := s.allParticipantsLocked(id)
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	usersByID := make(map[int64]domain.User, len(all))
	if s.users != nil {
		s.users.mu.RLock()
		for _, participant := range all {
			if user, ok := s.users.byID[participant.UserID]; ok {
				usersByID[participant.UserID] = user
			}
		}
		s.users.mu.RUnlock()
	}
	items := all[:0]
	for _, p := range all {
		ok := p.Status == domain.CommunityMemberActive
		if filter.Kind == domain.ChannelParticipantsAdmins {
			ok = ok && (p.Role == domain.CommunityRoleCreator || p.Role == domain.CommunityRoleAdmin)
		} else if filter.Kind == domain.ChannelParticipantsKicked || filter.Kind == domain.ChannelParticipantsBanned {
			ok = p.Status == domain.CommunityMemberKicked
		}
		if ok && query != "" {
			user := usersByID[p.UserID]
			haystack := strings.ToLower(strings.Join([]string{
				strconv.FormatInt(p.UserID, 10), user.FirstName, user.LastName, user.Username, user.Phone,
			}, " "))
			ok = strings.Contains(haystack, query)
		}
		if ok {
			items = append(items, p)
		}
	}
	out := domain.CommunityParticipantList{Community: c, Count: len(items)}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	out.Participants = append([]domain.CommunityMember(nil), items[offset:end]...)
	h := fnv.New64a()
	for _, p := range out.Participants {
		_, _ = h.Write([]byte(strconv.FormatInt(p.UserID, 10) + string(p.Role) + string(p.Status)))
	}
	out.Hash = int64(h.Sum64() & 0x7fffffffffffffff)
	for _, p := range out.Participants {
		if user, ok := usersByID[p.UserID]; ok {
			out.Users = append(out.Users, user)
		}
	}
	return out, nil
}

func (s *CommunityStore) editViewLocked(user, id int64, can func(domain.CommunityMember) bool, edit func(*domain.Community) bool) (domain.CommunityView, bool, error) {
	c, m, e := s.actorLocked(id, user)
	if e != nil {
		return domain.CommunityView{}, false, e
	}
	if !can(m) {
		return domain.CommunityView{}, false, domain.ErrCommunityAdminRequired
	}
	changed := edit(&c)
	s.communities[id] = c
	v, e := s.viewLocked(user, id)
	return v, changed, e
}
func (s *CommunityStore) EditCommunityTitle(_ context.Context, user, id int64, title string) (domain.CommunityView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editViewLocked(user, id, domain.CommunityMember.CanChangeInfo, func(c *domain.Community) bool {
		if c.Title == title {
			return false
		}
		c.Title = title
		return true
	})
}
func (s *CommunityStore) EditCommunityAbout(_ context.Context, user, id int64, about string) (domain.CommunityView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editViewLocked(user, id, domain.CommunityMember.CanChangeInfo, func(c *domain.Community) bool {
		if c.About == about {
			return false
		}
		c.About = about
		return true
	})
}
func (s *CommunityStore) EditCommunityDefaultBannedRights(_ context.Context, user, id int64, rights domain.ChannelBannedRights) (domain.CommunityView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editViewLocked(user, id, domain.CommunityMember.CanChangeInfo, func(c *domain.Community) bool {
		if c.DefaultBannedRights == rights {
			return false
		}
		c.DefaultBannedRights = rights
		return true
	})
}
func (s *CommunityStore) SetCommunityPhoto(_ context.Context, user, id int64, photo *domain.Photo, date int) (domain.CommunityView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.editViewLocked(user, id, domain.CommunityMember.CanChangeInfo, func(c *domain.Community) bool {
		pid, dc := int64(0), 0
		var stripped []byte
		if photo != nil {
			pid, dc = photo.ID, photo.DCID
			stripped = domain.StrippedFromSizes(photo.Sizes)
		}
		if c.PhotoID == pid && c.PhotoDCID == dc && string(c.PhotoStripped) == string(stripped) {
			return false
		}
		c.PhotoID, c.PhotoDCID, c.PhotoStripped = pid, dc, append([]byte(nil), stripped...)
		return true
	})
}
func (s *CommunityStore) EditCommunityAdmin(_ context.Context, req domain.CommunityEditAdminRequest) (domain.CommunityView, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, m, e := s.actorLocked(req.CommunityID, req.ActorUserID)
	if e != nil {
		return domain.CommunityView{}, false, e
	}
	zero := req.Rights == (domain.ChannelAdminRights{})
	if req.UserID == req.ActorUserID && m.Role == domain.CommunityRoleAdmin && zero {
		delete(s.members[c.ID], req.UserID)
		v, e := s.viewLocked(req.ActorUserID, c.ID)
		if errors.Is(e, domain.ErrCommunityPrivate) {
			return domain.CommunityView{Community: c, Self: m, Forbidden: true}, true, nil
		}
		return v, true, e
	}
	if !m.CanAddAdmins() {
		return domain.CommunityView{}, false, domain.ErrCommunityAdminRequired
	}
	if req.UserID == c.CreatorUserID {
		return domain.CommunityView{}, false, domain.ErrCommunityCreatorRequired
	}
	old, ok := s.members[c.ID][req.UserID]
	if zero {
		if ok && old.Role == domain.CommunityRoleAdmin {
			delete(s.members[c.ID], req.UserID)
			v, e := s.viewLocked(req.ActorUserID, c.ID)
			return v, true, e
		}
		v, e := s.viewLocked(req.ActorUserID, c.ID)
		return v, false, e
	}
	s.members[c.ID][req.UserID] = domain.CommunityMember{CommunityID: c.ID, UserID: req.UserID, Role: domain.CommunityRoleAdmin, Status: domain.CommunityMemberActive, AdminRights: req.Rights, Rank: req.Rank, Date: req.Date}
	v, e := s.viewLocked(req.ActorUserID, c.ID)
	return v, true, e
}

func (s *CommunityStore) DeleteCommunity(_ context.Context, user, id int64, date int) (domain.CommunityView, []domain.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, m, e := s.actorLocked(id, user)
	if e != nil {
		return domain.CommunityView{}, nil, e
	}
	if m.Role != domain.CommunityRoleCreator {
		return domain.CommunityView{}, nil, domain.ErrCommunityCreatorRequired
	}
	peers := make([]domain.Peer, 0, len(s.links[id]))
	serviceMessages := make([]domain.SendChannelMessageResult, 0, len(s.links[id]))
	for p := range s.links[id] {
		peers = append(peers, p)
		if e := s.setPeerLinkLocked(p, 0); e != nil {
			return domain.CommunityView{}, nil, e
		}
		serviceMessage, e := s.appendCommunityServiceMessageLocked(p, user, date, 0)
		if e != nil {
			_ = s.setPeerLinkLocked(p, c.ID)
			return domain.CommunityView{}, nil, e
		}
		if serviceMessage != nil {
			serviceMessages = append(serviceMessages, *serviceMessage)
		}
	}
	c.Deleted = true
	c.Title = ""
	c.About = ""
	s.communities[id] = c
	delete(s.links, id)
	delete(s.requests, id)
	delete(s.states, id)
	return domain.CommunityView{Community: c, Self: m, Forbidden: true, ServiceMessages: serviceMessages}, peers, nil
}
func (s *CommunityStore) SetCommunityPinned(_ context.Context, user, id int64, pinned bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, e := s.actorLocked(id, user); e != nil {
		return false, e
	}
	st, ok := s.states[id][user]
	if !ok || !st.Collapsed {
		return false, domain.ErrCommunityInvalid
	}
	if st.Pinned == pinned {
		return false, nil
	}
	st.Pinned = pinned
	if pinned {
		max := 1000000000
		for _, byUser := range s.states {
			if x, ok := byUser[user]; ok && x.Pinned && x.PinnedOrder > max {
				max = x.PinnedOrder
			}
		}
		st.PinnedOrder = max + 1
	} else {
		st.PinnedOrder = 0
	}
	s.states[id][user] = st
	return true, nil
}
func (s *CommunityStore) ReorderCommunityPinned(_ context.Context, user int64, order []domain.Peer, force bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[int64]struct{}{}
	for _, peer := range order {
		if peer.Type != domain.PeerTypeCommunity {
			continue
		}
		if _, ok := seen[peer.ID]; ok {
			return false, domain.ErrCommunityInvalid
		}
		seen[peer.ID] = struct{}{}
		st, ok := s.states[peer.ID][user]
		if !ok || !st.Collapsed {
			return false, domain.ErrCommunityInvalid
		}
	}
	changed := false
	for id, byUser := range s.states {
		if st, ok := byUser[user]; ok && st.Pinned {
			if force {
				st.Pinned = false
				st.PinnedOrder = 0
				s.states[id][user] = st
				changed = true
			}
		}
	}
	for i, peer := range order {
		if peer.Type != domain.PeerTypeCommunity {
			continue
		}
		st := s.states[peer.ID][user]
		pinnedOrder := len(order) - i
		if st.Pinned && st.PinnedOrder == pinnedOrder {
			continue
		}
		st.Pinned = true
		st.PinnedOrder = pinnedOrder
		s.states[peer.ID][user] = st
		changed = true
	}
	return changed, nil
}
func (s *CommunityStore) CommunitySearchScope(ctx context.Context, user, id int64) (domain.CommunitySearchScope, error) {
	v, e := s.GetCommunity(ctx, user, id)
	if e != nil {
		return domain.CommunitySearchScope{}, e
	}
	out := domain.CommunitySearchScope{CommunityID: id}
	for _, l := range v.Links {
		if l.Peer.Type == domain.PeerTypeChannel {
			if l.CanViewHistory {
				out.ChannelIDs = append(out.ChannelIDs, l.Peer.ID)
			}
		} else {
			out.BotUserIDs = append(out.BotUserIDs, l.Peer.ID)
		}
	}
	return out, nil
}
