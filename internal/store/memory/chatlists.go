package memory

import (
	"context"
	"sort"
	"sync"

	"telesrv/internal/domain"
)

type ChatlistStore struct {
	mu          sync.RWMutex
	nextInvite  int64
	invites     map[string]domain.ChatlistInvite
	memberships map[chatlistMembershipKey]domain.ChatlistMembership
}

type chatlistMembershipKey struct {
	userID        int64
	localFilterID int
}

// NewChatlistStore creates an in-memory ChatlistStore.
func NewChatlistStore() *ChatlistStore {
	return &ChatlistStore{
		nextInvite:  1,
		invites:     make(map[string]domain.ChatlistInvite),
		memberships: make(map[chatlistMembershipKey]domain.ChatlistMembership),
	}
}

func (s *ChatlistStore) CountInvites(_ context.Context, ownerUserID int64, filterID int) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, invite := range s.invites {
		if invite.OwnerUserID == ownerUserID && invite.FilterID == filterID && !invite.Deleted {
			count++
		}
	}
	return count, nil
}

func (s *ChatlistStore) CountActiveInvites(_ context.Context, ownerUserID int64, filterID int) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, invite := range s.invites {
		if invite.OwnerUserID == ownerUserID && invite.FilterID == filterID && !invite.Deleted && !invite.Revoked {
			count++
		}
	}
	return count, nil
}

func (s *ChatlistStore) SaveInvite(_ context.Context, invite domain.ChatlistInvite) (domain.ChatlistInvite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if invite.Slug == "" {
		return domain.ChatlistInvite{}, domain.ErrChatlistInviteInvalid
	}
	if existing, ok := s.invites[invite.Slug]; ok && (existing.OwnerUserID != invite.OwnerUserID || existing.FilterID != invite.FilterID) {
		return domain.ChatlistInvite{}, domain.ErrChatlistSlugOccupied
	}
	if invite.ID == 0 {
		if existing, ok := s.invites[invite.Slug]; ok {
			invite.ID = existing.ID
			if invite.Date == 0 {
				invite.Date = existing.Date
			}
		} else {
			invite.ID = s.nextInvite
			s.nextInvite++
		}
	}
	invite.Deleted = false
	s.invites[invite.Slug] = cloneChatlistInvite(invite)
	return cloneChatlistInvite(invite), nil
}

func (s *ChatlistStore) GetInvite(_ context.Context, ownerUserID int64, filterID int, slug string) (domain.ChatlistInvite, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invite, ok := s.invites[slug]
	if !ok || invite.Deleted || invite.OwnerUserID != ownerUserID || invite.FilterID != filterID {
		return domain.ChatlistInvite{}, false, nil
	}
	return cloneChatlistInvite(invite), true, nil
}

func (s *ChatlistStore) GetInviteBySlug(_ context.Context, slug string) (domain.ChatlistInvite, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	invite, ok := s.invites[slug]
	if !ok || invite.Deleted || invite.Revoked {
		return domain.ChatlistInvite{}, false, nil
	}
	return cloneChatlistInvite(invite), true, nil
}

func (s *ChatlistStore) ListInvites(_ context.Context, ownerUserID int64, filterID int) ([]domain.ChatlistInvite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ChatlistInvite, 0)
	for _, invite := range s.invites {
		if invite.OwnerUserID == ownerUserID && invite.FilterID == filterID && !invite.Deleted {
			out = append(out, cloneChatlistInvite(invite))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

func (s *ChatlistStore) DeleteInvite(_ context.Context, ownerUserID int64, filterID int, slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	invite, ok := s.invites[slug]
	if !ok || invite.Deleted || invite.OwnerUserID != ownerUserID || invite.FilterID != filterID {
		return false, nil
	}
	invite.Deleted = true
	s.invites[slug] = invite
	return true, nil
}

func (s *ChatlistStore) CountMemberships(_ context.Context, userID int64) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for key := range s.memberships {
		if key.userID == userID {
			count++
		}
	}
	return count, nil
}

func (s *ChatlistStore) SaveMembership(_ context.Context, membership domain.ChatlistMembership) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if membership.UserID == 0 || membership.LocalFilterID == 0 || membership.Slug == "" {
		return domain.ErrChatlistInvalid
	}
	key := chatlistMembershipKey{userID: membership.UserID, localFilterID: membership.LocalFilterID}
	for existingKey, existing := range s.memberships {
		if existingKey != key && existing.UserID == membership.UserID && existing.Slug == membership.Slug {
			delete(s.memberships, existingKey)
		}
	}
	s.memberships[key] = cloneChatlistMembership(membership)
	return nil
}

func (s *ChatlistStore) GetMembershipBySlug(_ context.Context, userID int64, slug string) (domain.ChatlistMembership, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, membership := range s.memberships {
		if membership.UserID == userID && membership.Slug == slug {
			return cloneChatlistMembership(membership), true, nil
		}
	}
	return domain.ChatlistMembership{}, false, nil
}

func (s *ChatlistStore) GetMembershipByLocalFilter(_ context.Context, userID int64, localFilterID int) (domain.ChatlistMembership, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	membership, ok := s.memberships[chatlistMembershipKey{userID: userID, localFilterID: localFilterID}]
	if !ok {
		return domain.ChatlistMembership{}, false, nil
	}
	return cloneChatlistMembership(membership), true, nil
}

func (s *ChatlistStore) DeleteMembershipByLocalFilter(_ context.Context, userID int64, localFilterID int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := chatlistMembershipKey{userID: userID, localFilterID: localFilterID}
	if _, ok := s.memberships[key]; !ok {
		return false, nil
	}
	delete(s.memberships, key)
	return true, nil
}

func (s *ChatlistStore) SetMembershipHidden(_ context.Context, userID int64, localFilterID int, hidden bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := chatlistMembershipKey{userID: userID, localFilterID: localFilterID}
	membership, ok := s.memberships[key]
	if !ok {
		return false, nil
	}
	changed := membership.HiddenUpdates != hidden
	membership.HiddenUpdates = hidden
	s.memberships[key] = membership
	return changed, nil
}

func cloneChatlistInvite(invite domain.ChatlistInvite) domain.ChatlistInvite {
	invite.Peers = append([]domain.DialogFolderPeer(nil), invite.Peers...)
	return invite
}

func cloneChatlistMembership(membership domain.ChatlistMembership) domain.ChatlistMembership {
	return membership
}
