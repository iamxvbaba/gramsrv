package rpc

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/app/contacts"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

const (
	maxContactImportBatch = 500
	maxContactDeleteBatch = 500
	maxContactNameLength  = 128
	maxContactPhoneLength = 64
	maxContactNoteLength  = 4096
	maxContactSearchQLen  = 256
	maxContactSearchLimit = 50
	maxCloseFriendsCount  = 5000
	maxContactSetBlocked  = 5000
)

// registerContacts 注册 contacts.* RPC handler。
func (r *Router) registerContacts(d *tlprofile.Dispatcher) {
	registerRPC[*tg.ContactsGetContactsRequest](d, tlprofile.SemanticMethodContactsGetContacts, func(ctx context.Context, layerRequest *tg.ContactsGetContactsRequest) (any, error) {
		return r.onContactsGetContacts(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.ContactsGetContactIDsRequest](d, tlprofile.SemanticMethodContactsGetContactIDs, func(ctx context.Context, layerRequest *tg.ContactsGetContactIDsRequest) (any, error) {
		return r.onContactsGetContactIDs(ctx, layerRequest.
			Hash)
	})
	registerRPC[*tg.ContactsGetStatusesRequest](d, tlprofile.SemanticMethodContactsGetStatuses, func(ctx context.Context, layerRequest *tg.ContactsGetStatusesRequest) (any, error) {
		return r.onContactsGetStatuses(ctx)
	})
	registerRPC[*tg.ContactsImportContactsRequest](d, tlprofile.SemanticMethodContactsImportContacts, func(ctx context.Context, layerRequest *tg.ContactsImportContactsRequest) (any, error) {
		return r.onContactsImportContacts(ctx, layerRequest.
			Contacts)
	})
	registerRPC[*tg.ContactsAddContactRequest](d, tlprofile.SemanticMethodContactsAddContact, func(ctx context.Context, layerRequest *tg.ContactsAddContactRequest) (any, error) {
		return r.onContactsAddContact(ctx, layerRequest)
	})
	registerRPC[*tg.ContactsAcceptContactRequest](d, tlprofile.SemanticMethodContactsAcceptContact, func(ctx context.Context, layerRequest *tg.ContactsAcceptContactRequest) (any, error) {
		return r.onContactsAcceptContact(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.ContactsDeleteContactsRequest](d, tlprofile.SemanticMethodContactsDeleteContacts, func(ctx context.Context, layerRequest *tg.ContactsDeleteContactsRequest) (any, error) {
		return r.onContactsDeleteContacts(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.ContactsEditCloseFriendsRequest](d, tlprofile.SemanticMethodContactsEditCloseFriends, func(ctx context.Context, layerRequest *tg.ContactsEditCloseFriendsRequest) (any, error) {
		return r.onContactsEditCloseFriends(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.ContactsBlockRequest](d, tlprofile.SemanticMethodContactsBlock, func(ctx context.Context, layerRequest *tg.ContactsBlockRequest) (any, error) {
		return r.onContactsBlock(ctx, layerRequest)
	})
	registerRPC[*tg.ContactsUnblockRequest](d, tlprofile.SemanticMethodContactsUnblock, func(ctx context.Context, layerRequest *tg.ContactsUnblockRequest) (any, error) {
		return r.onContactsUnblock(ctx, layerRequest)
	})
	registerRPC[*tg.ContactsSetBlockedRequest](d, tlprofile.SemanticMethodContactsSetBlocked, func(ctx context.Context, layerRequest *tg.ContactsSetBlockedRequest) (any, error) {
		return r.onContactsSetBlocked(ctx, layerRequest)
	})
	registerRPC[*tg.ContactsUpdateContactNoteRequest](d, tlprofile.SemanticMethodContactsUpdateContactNote, func(ctx context.Context, layerRequest *tg.ContactsUpdateContactNoteRequest) (any, error) {
		return r.onContactsUpdateContactNote(ctx, layerRequest)
	})
	registerRPC[*tg.ContactsSearchRequest](d, tlprofile.SemanticMethodContactsSearch, func(ctx context.Context, layerRequest *tg.ContactsSearchRequest) (any, error) {
		return r.onContactsSearch(ctx, layerRequest)
	})
	registerRPC[*tg.ContactsResolveUsernameRequest](d, tlprofile.SemanticMethodContactsResolveUsername, func(ctx context.Context, layerRequest *tg.ContactsResolveUsernameRequest) (any, error) {
		return r.onContactsResolveUsername(ctx, layerRequest)
	})
	registerRPC[*tg.ContactsResolvePhoneRequest](d, tlprofile.SemanticMethodContactsResolvePhone, func(ctx context.Context, layerRequest *tg.ContactsResolvePhoneRequest) (any, error) {
		return r.onContactsResolvePhone(ctx, layerRequest.
			Phone)
	})
	registerRPC[*tg.ContactsGetTopPeersRequest](d, tlprofile.SemanticMethodContactsGetTopPeers, func(ctx context.Context, req *tg.ContactsGetTopPeersRequest) (any, error) {
		return tdesktop.TopPeers(), nil
	})
	registerRPC[*tg.ContactsGetBlockedRequest](d, tlprofile.SemanticMethodContactsGetBlocked, func(ctx context.Context, layerRequest *tg.ContactsGetBlockedRequest) (any, error) {
		return r.onContactsGetBlocked(ctx, layerRequest)
	})
	registerRPC[*tg.ContactsGetBirthdaysRequest](d, tlprofile.SemanticMethodContactsGetBirthdays, func(ctx context.Context, layerRequest *tg.ContactsGetBirthdaysRequest) (any, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return &tg.ContactsContactBirthdays{Contacts: []tg.ContactBirthday{}, Users: []tg.UserClass{}}, nil
	})
	registerRPC[*tg.ContactsGetSponsoredPeersRequest](d, tlprofile.SemanticMethodContactsGetSponsoredPeers, func(ctx context.Context, layerRequest *tg.ContactsGetSponsoredPeersRequest) (any, error) {
		q := layerRequest.
			Q
		_ = q

		if utf8.RuneCountInString(q) > maxContactSearchQLen {
			return nil, limitInvalidErr()
		}
		return &tg.ContactsSponsoredPeersEmpty{}, nil
	})

}

func (r *Router) onContactsEditCloseFriends(ctx context.Context, id []int64) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if len(id) > maxCloseFriendsCount {
		return false, limitInvalidErr()
	}
	if r.deps.Contacts == nil {
		return true, nil
	}
	result, err := r.deps.Contacts.EditCloseFriends(ctx, userID, id)
	if err != nil {
		return false, contactErr(err)
	}
	if err := r.fanoutCloseFriendStoryChanges(ctx, userID, result); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) fanoutCloseFriendStoryChanges(ctx context.Context, ownerID int64, result domain.CloseFriendsEditResult) error {
	if ownerID == 0 || r.deps.Stories == nil || r.deps.Updates == nil {
		return nil
	}
	if len(result.AddedUserIDs) == 0 && len(result.RemovedUserIDs) == 0 {
		return nil
	}
	candidateIDs := make([]int64, 0, len(result.AddedUserIDs)+len(result.RemovedUserIDs))
	candidateIDs = append(candidateIDs, result.AddedUserIDs...)
	candidateIDs = append(candidateIDs, result.RemovedUserIDs...)
	blockedFacts, err := r.storyBlockedFactsForUsers(ctx, ownerID, candidateIDs)
	if err != nil {
		return err
	}
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}
	list, err := r.deps.Stories.ListOwnerActiveStories(ctx, ownerID, owner, int(r.clock.Now().Unix()), domain.MaxStoryListLimit)
	if err != nil {
		return storyErr(err)
	}
	for _, story := range list.Stories {
		if !story.CloseFriends {
			continue
		}
		story = storyFanoutSnapshot(story)
		for _, userID := range result.AddedUserIDs {
			if blockedFacts[userID] {
				continue
			}
			if story.VisibleToWithFacts(userID, true, true) {
				if err := r.recordStoryFanout(ctx, userID, story); err != nil {
					return err
				}
			}
		}
		for _, userID := range result.RemovedUserIDs {
			if blockedFacts[userID] || story.VisibleToWithFacts(userID, true, false) {
				continue
			}
			deleted := story
			deleted.Deleted = true
			if err := r.recordStoryFanout(ctx, userID, deleted); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Router) storyBlockedFactsForUsers(ctx context.Context, ownerID int64, userIDs []int64) (map[int64]bool, error) {
	out := make(map[int64]bool)
	if ownerID == 0 || r.deps.Contacts == nil {
		return out, nil
	}
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 || userID == ownerID {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		blocked, err := r.deps.Contacts.IsBlocked(ctx, ownerID, userID)
		if err != nil {
			return nil, internalErr()
		}
		if blocked {
			out[userID] = true
		}
	}
	return out, nil
}

func (r *Router) fanoutStoryBlocklistChange(ctx context.Context, ownerID, viewerID int64, blocked bool) error {
	if ownerID == 0 || viewerID == 0 || ownerID == viewerID || r.deps.Stories == nil || r.deps.Updates == nil {
		return nil
	}
	facts, err := r.storyViewerFactsForOwner(ctx, ownerID, viewerID)
	if err != nil {
		return err
	}
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}
	now := int(r.clock.Now().Unix())
	list, err := r.deps.Stories.ListOwnerActiveStories(ctx, ownerID, owner, now, domain.MaxStoryListLimit)
	if err != nil {
		return storyErr(err)
	}
	for _, story := range list.Stories {
		if !story.Active(now) {
			continue
		}
		beforeVisible := story.VisibleToWithStoryFacts(viewerID, facts.isContact, facts.closeFriend, !blocked)
		afterVisible := story.VisibleToWithStoryFacts(viewerID, facts.isContact, facts.closeFriend, blocked)
		switch {
		case afterVisible && !beforeVisible:
			if err := r.recordStoryFanout(ctx, viewerID, storyFanoutSnapshot(story)); err != nil {
				return err
			}
		case beforeVisible && !afterVisible:
			deleted := storyFanoutSnapshot(story)
			deleted.Deleted = true
			if err := r.recordStoryFanout(ctx, viewerID, deleted); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Router) storyViewerFactsForOwner(ctx context.Context, ownerID, viewerID int64) (storyPrivacyFanoutFacts, error) {
	if ownerID == 0 || viewerID == 0 || r.deps.Contacts == nil {
		return storyPrivacyFanoutFacts{}, nil
	}
	list, _, err := r.deps.Contacts.GetContacts(ctx, ownerID, 0)
	if err != nil {
		return storyPrivacyFanoutFacts{}, internalErr()
	}
	for _, contact := range list.Contacts {
		if contact.User.ID != viewerID {
			continue
		}
		return storyPrivacyFanoutFacts{
			isContact:   true,
			closeFriend: contact.CloseFriend || contact.User.CloseFriend,
		}, nil
	}
	return storyPrivacyFanoutFacts{}, nil
}

func (r *Router) recordStoryFanout(ctx context.Context, userID int64, story domain.Story) error {
	if r.deps.Updates == nil || userID == 0 {
		return nil
	}
	if _, _, err := r.deps.Updates.RecordStoryFanout(ctx, userID, story); err != nil {
		return internalErr()
	}
	return nil
}

func storyFanoutSnapshot(story domain.Story) domain.Story {
	story.Out = false
	story.Views = domain.StoryViews{}
	story.SentReaction = nil
	return story
}

func (r *Router) onContactsBlock(ctx context.Context, req *tg.ContactsBlockRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.ID)
	if !ok || peer.Type != domain.PeerTypeUser || peer.ID == 0 || peer.ID == userID {
		return false, userIDInvalidErr()
	}
	if r.deps.Contacts == nil {
		return true, nil
	}
	wasBlocked, err := r.deps.Contacts.IsBlocked(ctx, userID, peer.ID)
	if err != nil {
		return false, contactErr(err)
	}
	if _, err := r.deps.Contacts.BlockContact(ctx, userID, peer.ID, int(r.clock.Now().Unix())); err != nil {
		return false, contactErr(err)
	}
	if !wasBlocked {
		if err := r.recordPeerStoryBlocked(ctx, userID, peer, true); err != nil {
			return false, internalErr()
		}
		if err := r.fanoutStoryBlocklistChange(ctx, userID, peer.ID, true); err != nil {
			return false, err
		}
	}
	if settings, err := r.deps.Contacts.GetPeerSettings(ctx, userID, peer); err == nil {
		_ = r.recordPeerSettings(ctx, userID, peer, settings)
	}
	r.invalidateRPCProjectionForViewer(userID)
	return true, nil
}

func (r *Router) onContactsUnblock(ctx context.Context, req *tg.ContactsUnblockRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.ID)
	if !ok || peer.Type != domain.PeerTypeUser || peer.ID == 0 || peer.ID == userID {
		return false, userIDInvalidErr()
	}
	if r.deps.Contacts == nil {
		return true, nil
	}
	wasBlocked, err := r.deps.Contacts.IsBlocked(ctx, userID, peer.ID)
	if err != nil {
		return false, contactErr(err)
	}
	if _, err := r.deps.Contacts.UnblockContact(ctx, userID, peer.ID); err != nil {
		return false, contactErr(err)
	}
	if wasBlocked {
		if err := r.recordPeerStoryBlocked(ctx, userID, peer, false); err != nil {
			return false, internalErr()
		}
		if err := r.fanoutStoryBlocklistChange(ctx, userID, peer.ID, false); err != nil {
			return false, err
		}
	}
	if settings, err := r.deps.Contacts.GetPeerSettings(ctx, userID, peer); err == nil {
		_ = r.recordPeerSettings(ctx, userID, peer, settings)
	}
	r.invalidateRPCProjectionForViewer(userID)
	return true, nil
}

func (r *Router) onContactsSetBlocked(ctx context.Context, req *tg.ContactsSetBlockedRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if len(req.ID) > maxContactSetBlocked || req.Limit < 0 || req.Limit > maxContactSetBlocked {
		return false, limitInvalidErr()
	}
	if r.deps.Contacts == nil {
		return true, nil
	}
	desired := make(map[int64]domain.Peer, len(req.ID))
	desiredOrder := make([]domain.Peer, 0, len(req.ID))
	for _, input := range req.ID {
		peer, ok := r.domainPeerFromInputPeer(userID, input)
		if !ok || peer.Type != domain.PeerTypeUser || peer.ID == 0 || peer.ID == userID {
			return false, userIDInvalidErr()
		}
		if _, ok := desired[peer.ID]; ok {
			continue
		}
		desired[peer.ID] = peer
		desiredOrder = append(desiredOrder, peer)
	}
	current, err := r.currentBlockedUserIDs(ctx, userID)
	if err != nil {
		return false, err
	}
	now := int(r.clock.Now().Unix())
	for _, peer := range desiredOrder {
		if current[peer.ID] {
			continue
		}
		changed, err := r.deps.Contacts.BlockContact(ctx, userID, peer.ID, now)
		if err != nil {
			return false, contactErr(err)
		}
		if changed {
			if err := r.applyContactBlocklistChange(ctx, userID, peer, true); err != nil {
				return false, err
			}
		}
	}
	removeIDs := make([]int64, 0, len(current))
	for id := range current {
		if _, ok := desired[id]; ok {
			continue
		}
		removeIDs = append(removeIDs, id)
	}
	sort.Slice(removeIDs, func(i, j int) bool { return removeIDs[i] < removeIDs[j] })
	for _, id := range removeIDs {
		peer := domain.Peer{Type: domain.PeerTypeUser, ID: id}
		changed, err := r.deps.Contacts.UnblockContact(ctx, userID, id)
		if err != nil {
			return false, contactErr(err)
		}
		if changed {
			if err := r.applyContactBlocklistChange(ctx, userID, peer, false); err != nil {
				return false, err
			}
		}
	}
	r.invalidateRPCProjectionForViewer(userID)
	return true, nil
}

func (r *Router) currentBlockedUserIDs(ctx context.Context, userID int64) (map[int64]bool, error) {
	out := make(map[int64]bool)
	if r.deps.Contacts == nil || userID == 0 {
		return out, nil
	}
	offset := 0
	for {
		list, err := r.deps.Contacts.GetBlocked(ctx, userID, offset, 100)
		if err != nil {
			return nil, internalErr()
		}
		if list.Count > maxContactSetBlocked {
			return nil, limitInvalidErr()
		}
		for _, item := range list.Blocked {
			if item.User.ID != 0 {
				out[item.User.ID] = true
			}
		}
		offset += len(list.Blocked)
		if len(list.Blocked) == 0 || offset >= list.Count {
			break
		}
	}
	return out, nil
}

func (r *Router) applyContactBlocklistChange(ctx context.Context, userID int64, peer domain.Peer, blocked bool) error {
	if err := r.recordPeerStoryBlocked(ctx, userID, peer, blocked); err != nil {
		return internalErr()
	}
	if err := r.fanoutStoryBlocklistChange(ctx, userID, peer.ID, blocked); err != nil {
		return err
	}
	if r.deps.Contacts != nil {
		if settings, err := r.deps.Contacts.GetPeerSettings(ctx, userID, peer); err == nil {
			_ = r.recordPeerSettings(ctx, userID, peer, settings)
		}
	}
	return nil
}

func (r *Router) onContactsGetBlocked(ctx context.Context, req *tg.ContactsGetBlockedRequest) (tg.ContactsBlockedClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.Limit > 100 || req.Offset < 0 {
		return nil, limitInvalidErr()
	}
	if r.deps.Contacts == nil {
		return tdesktop.BlockedContacts(), nil
	}
	list, err := r.deps.Contacts.GetBlocked(ctx, userID, req.Offset, req.Limit)
	if err != nil {
		return nil, internalErr()
	}
	blocked := make([]tg.PeerBlocked, 0, len(list.Blocked))
	users := make([]tg.UserClass, 0, len(list.Blocked))
	for _, item := range list.Blocked {
		if item.User.ID == 0 {
			continue
		}
		blocked = append(blocked, tg.PeerBlocked{
			PeerID: &tg.PeerUser{UserID: item.User.ID},
			Date:   item.Date,
		})
		users = append(users, r.tgUser(item.User))
	}
	if list.Count > len(blocked)+req.Offset {
		return &tg.ContactsBlockedSlice{Count: list.Count, Blocked: blocked, Chats: []tg.ChatClass{}, Users: users}, nil
	}
	return &tg.ContactsBlocked{Blocked: blocked, Chats: []tg.ChatClass{}, Users: users}, nil
}

func (r *Router) onContactsGetContacts(ctx context.Context, hash int64) (tg.ContactsContactsClass, error) {
	if r.deps.Contacts == nil {
		return &tg.ContactsContacts{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	list, notModified, err := r.deps.Contacts.GetContacts(ctx, userID, hash)
	if err != nil {
		return nil, internalErr()
	}
	if notModified {
		return &tg.ContactsContactsNotModified{}, nil
	}
	return r.tgContacts(ctx, userID, list), nil
}

func (r *Router) onContactsGetStatuses(ctx context.Context) ([]tg.ContactStatus, error) {
	if r.deps.Contacts == nil {
		return []tg.ContactStatus{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	list, _, err := r.deps.Contacts.GetContacts(ctx, userID, 0)
	if err != nil {
		return nil, internalErr()
	}
	contactUserIDs := make([]int64, 0, len(list.Contacts))
	out := make([]tg.ContactStatus, 0, len(list.Contacts))
	seen := make(map[int64]struct{}, len(list.Contacts))
	for _, contact := range list.Contacts {
		id := contact.User.ID
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		contactUserIDs = append(contactUserIDs, id)
	}
	usersByID := make(map[int64]domain.User, len(contactUserIDs))
	if len(contactUserIDs) > 0 && r.deps.Users != nil {
		users, err := r.deps.Users.ByIDs(ctx, userID, contactUserIDs)
		if err != nil {
			return nil, internalErr()
		}
		for _, u := range users {
			if u.ID != 0 {
				usersByID[u.ID] = u
			}
		}
	}
	seen = make(map[int64]struct{}, len(list.Contacts))
	for _, contact := range list.Contacts {
		id := contact.User.ID
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		u := contact.User
		if current, ok := usersByID[id]; ok {
			u.LastSeenAt = current.LastSeenAt
			u.Status = current.Status
		}
		out = append(out, tg.ContactStatus{
			UserID: id,
			Status: tgUserStatus(r.userPresenceStatusForUser(u)),
		})
	}
	return out, nil
}

func (r *Router) onContactsGetContactIDs(ctx context.Context, hash int64) ([]int, error) {
	if r.deps.Contacts == nil {
		return nil, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	ids, notModified, err := r.deps.Contacts.ContactIDs(ctx, userID, hash)
	if err != nil {
		return nil, internalErr()
	}
	if notModified {
		return nil, nil
	}
	return ids, nil
}

func (r *Router) onContactsImportContacts(ctx context.Context, input []tg.InputPhoneContact) (*tg.ContactsImportedContacts, error) {
	if r.deps.Contacts == nil {
		return &tg.ContactsImportedContacts{}, nil
	}
	if len(input) > maxContactImportBatch {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	items := make([]domain.ContactInput, 0, len(input))
	for _, item := range input {
		note, entities := contactNote(item.GetNote())
		if !validContactInput(item.Phone, item.FirstName, item.LastName, note, len(entities)) {
			return nil, limitInvalidErr()
		}
		items = append(items, domain.ContactInput{
			ClientID:     item.ClientID,
			Phone:        item.Phone,
			FirstName:    item.FirstName,
			LastName:     item.LastName,
			Note:         note,
			NoteEntities: entities,
		})
	}
	res, err := r.deps.Contacts.ImportContacts(ctx, userID, items)
	if err != nil {
		r.log.Warn("contacts.importContacts service failed", append(r.contextLogFields(ctx), zap.Error(err), zap.Int("contacts", len(items)))...)
		return nil, internalErr()
	}
	out := &tg.ContactsImportedContacts{
		Imported: make([]tg.ImportedContact, 0, len(res.Imported)),
		Users:    make([]tg.UserClass, 0, len(res.Contacts)),
	}
	for _, imported := range res.Imported {
		out.Imported = append(out.Imported, tg.ImportedContact{UserID: imported.UserID, ClientID: imported.ClientID})
	}
	for _, contact := range res.Contacts {
		out.Users = append(out.Users, r.tgUser(contact.User))
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, userID, out.Users, nil)
	out.RetryContacts = append(out.RetryContacts, res.RetryContacts...)
	for _, contact := range res.Contacts {
		peer := domain.Peer{Type: domain.PeerTypeUser, ID: contact.User.ID}
		settings, err := r.deps.Contacts.GetPeerSettings(ctx, userID, peer)
		if err != nil {
			r.log.Warn("contacts.importContacts peer settings failed", append(r.contextLogFields(ctx), zap.Error(err), zap.Int64("peer_user_id", contact.User.ID))...)
			return nil, internalErr()
		}
		if err := r.recordPeerSettings(ctx, userID, peer, settings); err != nil {
			r.log.Warn("contacts.importContacts record peer settings failed", append(r.contextLogFields(ctx), zap.Error(err), zap.Int64("peer_user_id", contact.User.ID))...)
			return nil, internalErr()
		}
		if contact.Mutual {
			if err := r.recordAcceptedContactTargetUpdates(ctx, userID, contact.User.ID); err != nil {
				r.log.Warn("contacts.importContacts record accepted target failed", append(r.contextLogFields(ctx), zap.Error(err), zap.Int64("peer_user_id", contact.User.ID))...)
				return nil, err
			}
		}
	}
	if err := r.recordContactsReset(ctx, userID); err != nil {
		r.log.Warn("contacts.importContacts record contacts reset failed", append(r.contextLogFields(ctx), zap.Error(err), zap.Int("contacts", len(items)))...)
		return nil, internalErr()
	}
	r.invalidateRPCProjectionForViewer(userID)
	r.pushContactsReset(ctx, userID)
	return out, nil
}

func (r *Router) onContactsAddContact(ctx context.Context, req *tg.ContactsAddContactRequest) (tg.UpdatesClass, error) {
	if r.deps.Contacts == nil {
		return &tg.Updates{Date: int(r.clock.Now().Unix())}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	target, found, err := r.userFromInput(ctx, userID, req.ID)
	if err != nil {
		return nil, contactErr(err)
	}
	if !found {
		return nil, contactIDInvalidErr()
	}
	note, entities := contactNote(req.GetNote())
	if !validContactInput(req.Phone, req.FirstName, req.LastName, note, len(entities)) {
		return nil, limitInvalidErr()
	}
	contact, err := r.deps.Contacts.AddContact(ctx, userID, domain.ContactInput{
		ContactUserID:            target.ID,
		Phone:                    req.Phone,
		FirstName:                req.FirstName,
		LastName:                 req.LastName,
		Note:                     note,
		NoteEntities:             entities,
		AddPhonePrivacyException: req.AddPhonePrivacyException,
	})
	if err != nil {
		return nil, contactErr(err)
	}
	peerUser := contact.User
	peerUser.Contact = true
	peerUser.Mutual = contact.Mutual || contact.User.Mutual
	if contact.Phone != "" {
		peerUser.Phone = contact.Phone
	}
	if contact.FirstName != "" || contact.LastName != "" {
		peerUser.FirstName = contact.FirstName
		peerUser.LastName = contact.LastName
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: contact.User.ID}
	settings, err := r.deps.Contacts.GetPeerSettings(ctx, userID, peer)
	if err != nil {
		return nil, internalErr()
	}
	updates := r.contactPeerSettingsUpdates(ctx, userID, peerUser, settings, true)
	updates.Updates = append(updates.Updates, &tg.UpdateContactsReset{})
	if err := r.recordPeerSettings(ctx, userID, peer, settings); err != nil {
		return nil, internalErr()
	}
	if err := r.recordContactsReset(ctx, userID); err != nil {
		return nil, internalErr()
	}
	if contact.Mutual {
		if err := r.recordAcceptedContactTargetUpdates(ctx, userID, contact.User.ID); err != nil {
			return nil, err
		}
	}
	r.invalidateRPCProjectionForViewer(userID)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, updates)
	return updates, nil
}

func (r *Router) onContactsAcceptContact(ctx context.Context, id tg.InputUserClass) (tg.UpdatesClass, error) {
	if r.deps.Contacts == nil {
		return &tg.Updates{Date: int(r.clock.Now().Unix())}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	target, found, err := r.userFromInput(ctx, userID, id)
	if err != nil {
		return nil, contactErr(err)
	}
	if !found || target.ID == userID {
		return nil, contactIDInvalidErr()
	}
	contact, err := r.deps.Contacts.AcceptContact(ctx, userID, target.ID)
	if err != nil {
		return nil, contactErr(err)
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: target.ID}
	settings, err := r.deps.Contacts.GetPeerSettings(ctx, userID, peer)
	if err != nil {
		return nil, internalErr()
	}
	peerUser := contact.User
	peerUser.Contact = true
	peerUser.Mutual = contact.Mutual || contact.User.Mutual
	if contact.Phone != "" {
		peerUser.Phone = contact.Phone
	}
	if contact.FirstName != "" || contact.LastName != "" {
		peerUser.FirstName = contact.FirstName
		peerUser.LastName = contact.LastName
	}
	updates := r.contactPeerSettingsUpdates(ctx, userID, peerUser, settings, true)
	updates.Updates = append(updates.Updates, &tg.UpdateContactsReset{})
	if err := r.recordPeerSettings(ctx, userID, peer, settings); err != nil {
		return nil, internalErr()
	}
	if err := r.recordContactsReset(ctx, userID); err != nil {
		return nil, internalErr()
	}

	if err := r.recordAcceptedContactTargetUpdates(ctx, userID, target.ID); err != nil {
		return nil, err
	}

	r.invalidateRPCProjectionForViewer(userID)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, updates)
	return updates, nil
}

func (r *Router) onContactsDeleteContacts(ctx context.Context, ids []tg.InputUserClass) (tg.UpdatesClass, error) {
	if r.deps.Contacts == nil {
		return &tg.Updates{Date: int(r.clock.Now().Unix())}, nil
	}
	if len(ids) > maxContactDeleteBatch {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	contactIDs := make([]int64, 0, len(ids))
	users := make([]tg.UserClass, 0, len(ids)+1)
	if r.deps.Users != nil {
		if u, err := r.deps.Users.Self(ctx, userID); err == nil {
			users = append(users, r.tgSelfUser(u))
		}
	}
	seen := map[int64]struct{}{userID: {}}
	for _, id := range ids {
		u, found, err := r.userFromInput(ctx, userID, id)
		if err != nil {
			return nil, contactErr(err)
		}
		if !found || u.ID == userID {
			continue
		}
		contactIDs = append(contactIDs, u.ID)
		u.Contact = false
		u.Mutual = false
		if _, ok := seen[u.ID]; !ok {
			users = append(users, r.tgUser(u))
			seen[u.ID] = struct{}{}
		}
	}
	if _, err := r.deps.Contacts.DeleteContacts(ctx, userID, contactIDs); err != nil {
		return nil, internalErr()
	}
	updates := make([]tg.UpdateClass, 0, len(contactIDs))
	for _, id := range contactIDs {
		updates = append(updates, &tg.UpdatePeerSettings{
			Peer:     &tg.PeerUser{UserID: id},
			Settings: tgPeerSettings(domain.PeerSettings{AddContact: true, BlockContact: true}),
		})
	}
	if len(contactIDs) > 0 {
		for _, id := range contactIDs {
			if err := r.recordPeerSettings(ctx, userID, domain.Peer{Type: domain.PeerTypeUser, ID: id}, domain.PeerSettings{AddContact: true, BlockContact: true}); err != nil {
				return nil, internalErr()
			}
		}
		updates = append(updates, &tg.UpdateContactsReset{})
		if err := r.recordContactsReset(ctx, userID); err != nil {
			return nil, internalErr()
		}
	}
	r.invalidateRPCProjectionForViewer(userID)
	out := &tg.Updates{Updates: updates, Users: users, Date: int(r.clock.Now().Unix())}
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, out)
	return out, nil
}

func (r *Router) onContactsUpdateContactNote(ctx context.Context, req *tg.ContactsUpdateContactNoteRequest) (bool, error) {
	if r.deps.Contacts == nil {
		return true, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	target, found, err := r.userFromInput(ctx, userID, req.ID)
	if err != nil {
		return false, contactErr(err)
	}
	if !found {
		return false, contactIDInvalidErr()
	}
	if utf8.RuneCountInString(req.Note.Text) > maxContactNoteLength || len(req.Note.Entities) > maxMessageEntityCount {
		return false, limitInvalidErr()
	}
	if _, err := r.deps.Contacts.UpdateContactNote(ctx, userID, target.ID, req.Note.Text, domainMessageEntities(req.Note.Entities)); err != nil {
		return false, contactErr(err)
	}
	if err := r.recordContactsReset(ctx, userID); err != nil {
		return false, internalErr()
	}
	r.invalidateRPCProjectionForViewer(userID)
	r.pushContactsReset(ctx, userID)
	return true, nil
}

func (r *Router) onContactsSearch(ctx context.Context, req *tg.ContactsSearchRequest) (*tg.ContactsFound, error) {
	if r.deps.Contacts == nil && r.deps.Channels == nil {
		return &tg.ContactsFound{}, nil
	}
	query := normalizeSearchQuery(req.Q)
	if query == "" {
		return nil, searchQueryEmptyErr()
	}
	if utf8.RuneCountInString(query) < 3 {
		return nil, queryTooShortErr()
	}
	if utf8.RuneCountInString(query) > maxContactSearchQLen {
		return nil, limitInvalidErr()
	}
	limit := req.Limit
	if limit <= 0 || limit > maxContactSearchLimit {
		limit = maxContactSearchLimit
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	res := domain.UserSearchResult{}
	if r.deps.Contacts != nil {
		userRes, err := r.deps.Contacts.Search(ctx, userID, query, limit)
		if err != nil {
			return nil, internalErr()
		}
		res = userRes
	}
	if r.deps.Channels != nil {
		channelRes, err := r.deps.Channels.SearchPublicChannels(ctx, userID, query, limit)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		res.ChannelResults = channelRes.Results
	}
	return r.tgContactsFound(ctx, userID, r.withUserSearchPresence(res)), nil
}

func (r *Router) onContactsResolveUsername(ctx context.Context, req *tg.ContactsResolveUsernameRequest) (*tg.ContactsResolvedPeer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if svc, ok := r.deps.Users.(UserIdentityService); ok {
		u, found, err := svc.ResolveUsername(ctx, userID, req.Username)
		if err != nil {
			return nil, usernameErr(err)
		}
		if found {
			return r.tgResolvedUserPeerWithStories(ctx, userID, u), nil
		}
	}
	if r.deps.Channels != nil {
		ch, found, err := r.deps.Channels.ResolvePublicUsername(ctx, userID, req.Username)
		if err != nil {
			return nil, usernameErr(err)
		}
		if found {
			view, err := r.deps.Channels.ResolveChannel(ctx, userID, ch.ID)
			if err != nil {
				return nil, channelInvalidErr(err)
			}
			return r.tgResolvedChannelPeerWithStories(ctx, userID, view), nil
		}
	}
	return nil, usernameNotOccupiedErr()
}

func (r *Router) onContactsResolvePhone(ctx context.Context, phone string) (*tg.ContactsResolvedPeer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	svc, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return nil, phoneNotOccupiedErr()
	}
	u, found, err := svc.ResolvePhone(ctx, userID, phone)
	if err != nil {
		if errors.Is(err, domain.ErrPhoneNotOccupied) {
			return nil, phoneNotOccupiedErr()
		}
		return nil, internalErr()
	}
	if !found {
		return nil, phoneNotOccupiedErr()
	}
	return r.tgResolvedUserPeerWithStories(ctx, userID, u), nil
}

func (r *Router) tgResolvedUserPeer(currentUserID int64, u domain.User) *tg.ContactsResolvedPeer {
	var user tg.UserClass
	if u.ID == currentUserID {
		user = r.tgSelfUser(u)
	} else {
		user = r.tgUser(u)
	}
	return &tg.ContactsResolvedPeer{
		Peer:  &tg.PeerUser{UserID: u.ID},
		Users: []tg.UserClass{user},
	}
}

func tgResolvedChannelPeer(currentUserID int64, view domain.ChannelView) *tg.ContactsResolvedPeer {
	return &tg.ContactsResolvedPeer{
		Peer:  &tg.PeerChannel{ChannelID: view.Channel.ID},
		Chats: []tg.ChatClass{tgChannelChatForView(currentUserID, view)},
	}
}

func normalizeSearchQuery(query string) string {
	query = strings.TrimSpace(query)
	query = strings.TrimPrefix(query, "@")
	return strings.TrimSpace(query)
}

func validContactInput(phone, firstName, lastName, note string, entities int) bool {
	if utf8.RuneCountInString(phone) > maxContactPhoneLength {
		return false
	}
	if utf8.RuneCountInString(firstName) > maxContactNameLength || utf8.RuneCountInString(lastName) > maxContactNameLength {
		return false
	}
	if utf8.RuneCountInString(note) > maxContactNoteLength || entities > maxMessageEntityCount {
		return false
	}
	return true
}

func contactNote(note tg.TextWithEntities, ok bool) (string, []domain.MessageEntity) {
	if !ok {
		return "", nil
	}
	return note.Text, domainMessageEntities(note.Entities)
}

func (r *Router) contactPeerSettingsUpdates(ctx context.Context, userID int64, peerUser domain.User, settings domain.PeerSettings, includeSelf bool) *tg.Updates {
	users := make([]tg.UserClass, 0, 2)
	if includeSelf && r.deps.Users != nil {
		if self, err := r.deps.Users.Self(ctx, userID); err == nil && self.ID != 0 {
			users = append(users, r.tgSelfUser(self))
		}
	}
	users = append(users, r.tgUser(peerUser))
	out := &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdatePeerSettings{
				Peer:     &tg.PeerUser{UserID: peerUser.ID},
				Settings: tgPeerSettings(settings),
			},
		},
		Users: users,
		Date:  int(r.clock.Now().Unix()),
		Seq:   0,
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, userID, out.Users, nil)
	return out
}

func (r *Router) recordAcceptedContactTargetUpdates(ctx context.Context, userID, targetUserID int64) error {
	if targetUserID == 0 || targetUserID == userID {
		return nil
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
	settings, err := r.deps.Contacts.GetPeerSettings(ctx, targetUserID, peer)
	if err != nil {
		return internalErr()
	}
	var zeroAuthKeyID [8]byte
	if err := r.recordPeerSettingsForUser(ctx, zeroAuthKeyID, targetUserID, peer, settings, zeroAuthKeyID, 0); err != nil {
		return internalErr()
	}
	if err := r.recordContactsResetForUser(ctx, zeroAuthKeyID, targetUserID, zeroAuthKeyID, 0); err != nil {
		return internalErr()
	}
	peerUser := domain.User{ID: userID}
	if r.deps.Users != nil {
		u, found, err := r.deps.Users.ByID(ctx, targetUserID, userID)
		if err != nil {
			return internalErr()
		}
		if found {
			peerUser = u
		}
	}
	updates := r.contactPeerSettingsUpdates(ctx, targetUserID, peerUser, settings, true)
	updates.Updates = append(updates.Updates, &tg.UpdateContactsReset{})
	r.pushUserUpdatesIfNoReliableDispatch(ctx, targetUserID, updates)
	return nil
}

func (r *Router) pushContactsReset(ctx context.Context, userID int64) {
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateContactsReset{}},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	})
}

func (r *Router) recordContactsReset(ctx context.Context, userID int64) error {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	return r.recordContactsResetForUser(ctx, authKeyID, userID, rawAuthKeyIDForOrigin(ctx), sessionID)
}

func (r *Router) recordContactsResetForUser(ctx context.Context, stateAuthKeyID [8]byte, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) error {
	if r.deps.Updates == nil || userID == 0 {
		return nil
	}
	event, _, err := r.deps.Updates.RecordContactsReset(ctx, stateAuthKeyID, userID, excludeAuthKeyID, excludeSessionID)
	if err == nil && excludeSessionID != 0 {
		r.bookkeepAuxPtsForCurrentSession(ctx, event)
	}
	return err
}

func (r *Router) recordPeerSettings(ctx context.Context, userID int64, peer domain.Peer, settings domain.PeerSettings) error {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	return r.recordPeerSettingsForUser(ctx, authKeyID, userID, peer, settings, rawAuthKeyIDForOrigin(ctx), sessionID)
}

func (r *Router) recordPeerStoryBlocked(ctx context.Context, userID int64, peer domain.Peer, blocked bool) error {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	if r.deps.Updates == nil || userID == 0 {
		return nil
	}
	event, _, err := r.deps.Updates.RecordPeerStoryBlocked(ctx, authKeyID, userID, peer, blocked, rawAuthKeyIDForOrigin(ctx), sessionID)
	if err == nil && sessionID != 0 {
		r.bookkeepAuxPtsForCurrentSession(ctx, event)
	}
	return err
}

func (r *Router) recordPeerSettingsForUser(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, settings domain.PeerSettings, excludeAuthKeyID [8]byte, excludeSessionID int64) error {
	if r.deps.Updates == nil || userID == 0 {
		return nil
	}
	event, _, err := r.deps.Updates.RecordPeerSettings(ctx, stateAuthKeyID, userID, peer, settings, excludeAuthKeyID, excludeSessionID)
	if err == nil && excludeSessionID != 0 {
		r.bookkeepAuxPtsForCurrentSession(ctx, event)
	}
	return err
}

type reliableUpdateDispatchReporter interface {
	UsesReliableDispatch() bool
}

func (r *Router) hasReliableUpdateDispatch() bool {
	reporter, ok := r.deps.Updates.(reliableUpdateDispatchReporter)
	return ok && reporter.UsesReliableDispatch()
}

func (r *Router) pushUserUpdatesIfNoReliableDispatch(ctx context.Context, userID int64, updates *tg.Updates) {
	if r.hasReliableUpdateDispatch() {
		return
	}
	r.pushUserUpdates(ctx, userID, updates)
}

func (r *Router) pushUserUpdates(ctx context.Context, userID int64, updates *tg.Updates) int {
	return r.pushUserMessage(ctx, userID, "push user updates", updates)
}

func tgPeerSettings(settings domain.PeerSettings) tg.PeerSettings {
	if settings.HiddenPeerSettingsBar {
		return tg.PeerSettings{}
	}
	out := tg.PeerSettings{
		AddContact:            settings.AddContact,
		BlockContact:          settings.BlockContact,
		ShareContact:          settings.ShareContact,
		NeedContactsException: settings.NeedContactsException,
	}
	if settings.BusinessBotID != 0 {
		out.SetBusinessBotID(settings.BusinessBotID)
		out.SetBusinessBotManageURL(settings.BusinessBotManageURL)
		if settings.BusinessBotPaused {
			out.SetBusinessBotPaused(true)
		}
		if settings.BusinessBotCanReply {
			out.SetBusinessBotCanReply(true)
		}
	}
	return out
}

func contactErr(err error) error {
	switch {
	case errors.Is(err, contacts.ErrContactNameEmpty):
		return contactNameEmptyErr()
	case errors.Is(err, contacts.ErrContactIDInvalid):
		return contactIDInvalidErr()
	case errors.Is(err, contacts.ErrContactReqMissing):
		return contactReqMissingErr()
	default:
		return internalErr()
	}
}
