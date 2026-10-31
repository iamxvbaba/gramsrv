package privacy

import (
	"context"
	"slices"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	maxPrivacyRules   = 100
	maxPrivacyRuleIDs = 5000
)

// Service owns account privacy rules and viewer-specific evaluation.
type Service struct {
	rules           store.PrivacyStore
	contacts        store.ContactStore
	baseUsers       baseUserProvider
	memberships     channelMembershipProvider
	viewerFacts     *readmodelcache.Cache[int64, viewerFacts]
	membershipFacts *readmodelcache.Cache[membershipKey, bool]
	now             func() time.Time
}

func NewService(rules store.PrivacyStore, contacts store.ContactStore) *Service {
	return &Service{
		rules:           rules,
		contacts:        contacts,
		viewerFacts:     newViewerFactsCache(),
		membershipFacts: newMembershipCache(),
		now:             time.Now,
	}
}

// ConfigureReadModels wires the cold loaders behind the bounded in-memory
// privacy fact caches. It is called after users/channels services are built to
// avoid a package dependency cycle.
func (s *Service) ConfigureReadModels(users baseUserProvider, memberships channelMembershipProvider) *Service {
	if s == nil {
		return s
	}
	s.baseUsers = users
	s.memberships = memberships
	return s
}

func (s *Service) GetRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (domain.PrivacyRules, error) {
	if !ValidKey(key) {
		return domain.PrivacyRules{}, domain.ErrPrivacyKeyInvalid
	}
	if s == nil || s.rules == nil {
		return defaultRules(ownerUserID, key), nil
	}
	rules, ok, err := s.rules.GetPrivacyRules(ctx, ownerUserID, key)
	if err != nil {
		return domain.PrivacyRules{}, err
	}
	if !ok {
		return defaultRules(ownerUserID, key), nil
	}
	rules.OwnerUserID = ownerUserID
	rules.Key = key
	if len(rules.Rules) == 0 {
		rules.Rules = domain.DefaultPrivacyRules(key)
	}
	return cloneRules(rules), nil
}

func (s *Service) SetRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey, rules []domain.PrivacyRule) (domain.PrivacyRules, error) {
	out, err := normalizedRules(ownerUserID, key, rules)
	if err != nil {
		return domain.PrivacyRules{}, err
	}
	if s != nil && s.rules != nil {
		if err := s.rules.SetPrivacyRules(ctx, out); err != nil {
			return domain.PrivacyRules{}, err
		}
	}
	return out, nil
}

func normalizedRules(ownerUserID int64, key domain.PrivacyKey, rules []domain.PrivacyRule) (domain.PrivacyRules, error) {
	if !ValidKey(key) {
		return domain.PrivacyRules{}, domain.ErrPrivacyKeyInvalid
	}
	if len(rules) == 0 {
		rules = domain.DefaultPrivacyRules(key)
	}
	if err := validateRules(rules); err != nil {
		return domain.PrivacyRules{}, err
	}
	return domain.PrivacyRules{OwnerUserID: ownerUserID, Key: key, Rules: cloneRuleSlice(rules)}, nil
}

func (s *Service) AddAllowUser(ctx context.Context, ownerUserID int64, key domain.PrivacyKey, targetUserID int64) (domain.PrivacyRules, bool, error) {
	if targetUserID == 0 {
		return domain.PrivacyRules{}, false, domain.ErrPrivacyRuleInvalid
	}
	rules, err := s.GetRules(ctx, ownerUserID, key)
	if err != nil {
		return domain.PrivacyRules{}, false, err
	}
	for i := range rules.Rules {
		if rules.Rules[i].Kind != domain.PrivacyRuleAllowUsers {
			continue
		}
		if slices.Contains(rules.Rules[i].UserIDs, targetUserID) {
			return rules, false, nil
		}
		rules.Rules[i].UserIDs = append(rules.Rules[i].UserIDs, targetUserID)
		next, err := s.SetRules(ctx, ownerUserID, key, rules.Rules)
		return next, true, err
	}
	rules.Rules = append([]domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowUsers, UserIDs: []int64{targetUserID}}}, rules.Rules...)
	next, err := s.SetRules(ctx, ownerUserID, key, rules.Rules)
	return next, true, err
}

func (s *Service) CanSee(ctx context.Context, ownerUserID, viewerUserID int64, key domain.PrivacyKey) (bool, error) {
	if ownerUserID == 0 || viewerUserID == 0 {
		return false, nil
	}
	if ownerUserID == viewerUserID {
		return true, nil
	}
	rules, err := s.GetRules(ctx, ownerUserID, key)
	if err != nil {
		return false, err
	}
	needs := needsForRules(rules)
	evalCtx := domain.PrivacyContext{
		OwnerUserID:  ownerUserID,
		ViewerUserID: viewerUserID,
	}
	if s != nil && s.contacts != nil {
		if contact, found, err := s.contacts.Get(ctx, ownerUserID, viewerUserID); err != nil {
			return false, err
		} else if found {
			evalCtx.ViewerIsContact = true
			evalCtx.ViewerCloseFriend = contact.CloseFriend
		}
	}
	if needs.viewerBase {
		facts, err := s.loadViewerFacts(ctx, []int64{viewerUserID})
		if err != nil {
			return false, err
		}
		applyViewerFacts(&evalCtx, facts[viewerUserID], s.now().Unix())
	}
	if len(needs.chatIDs) > 0 {
		facts, err := s.loadMembershipFacts(ctx, needs.chatIDs, []int64{viewerUserID})
		if err != nil {
			return false, err
		}
		applyMembershipFacts(&evalCtx, needs.chatIDs, facts)
	}
	return Evaluate(rules, evalCtx), nil
}

// P2PAllowedBetween decides whether a private call may use direct P2P. Safe default:
// besides the bidirectional AND of both sides' phone_p2p privacy rules (equivalent to
// per-side CanSee), it also hard-requires the caller and callee to be each other's
// contacts — even if one side's privacy rule explicitly allows all, a non-contact can
// never obtain the peer's real IP and the call always falls back to the TURN relay.
// When the contact relationship or contact store cannot be confirmed, it denies (fail
// closed); this is the server-side basis for the rpc layer's P2P gate.
func (s *Service) P2PAllowedBetween(ctx context.Context, callerID, calleeID int64) (bool, error) {
	if callerID == 0 || calleeID == 0 || callerID == calleeID {
		return false, nil
	}
	if s == nil || s.contacts == nil {
		return false, nil
	}
	callerHasCallee, err := s.reciprocalContact(ctx, callerID, calleeID)
	if err != nil {
		return false, err
	}
	if !callerHasCallee {
		return false, nil
	}
	calleeHasCaller, err := s.reciprocalContact(ctx, calleeID, callerID)
	if err != nil {
		return false, err
	}
	if !calleeHasCaller {
		return false, nil
	}
	calleeAllows, err := s.CanSee(ctx, calleeID, callerID, domain.PrivacyKeyPhoneP2P)
	if err != nil {
		return false, err
	}
	callerAllows, err := s.CanSee(ctx, callerID, calleeID, domain.PrivacyKeyPhoneP2P)
	if err != nil {
		return false, err
	}
	return calleeAllows && callerAllows, nil
}

func (s *Service) reciprocalContact(ctx context.Context, userID, contactUserID int64) (bool, error) {
	_, found, err := s.contacts.Get(ctx, userID, contactUserID)
	return found, err
}

// CanSeeAnonymous evaluates one owner's privacy rules for an unauthenticated
// public-web viewer. Anonymous viewers are never contacts, premium users,
// close friends, bots, or shared-chat participants; explicit allow-all and
// disallow rules still retain their normal precedence through Evaluate.
func (s *Service) CanSeeAnonymous(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (bool, error) {
	if ownerUserID == 0 {
		return false, nil
	}
	rules, err := s.GetRules(ctx, ownerUserID, key)
	if err != nil {
		return false, err
	}
	return Evaluate(rules, domain.PrivacyContext{OwnerUserID: ownerUserID}), nil
}

// CanSeeBatch batch-evaluates multiple owners' visibility for the same viewer across
// multiple keys; the result is equivalent to calling CanSee once per (owner,key), but uses
// only one ListPrivacyRules + one GetReverseContacts + in-memory Evaluate (removing the
// per-user 3×CanSee×2-row N+1 in projectBatch / fan-out projection). Returns
// map[ownerUserID]map[key]bool; owner==viewer is always true (consistent with CanSee).
func (s *Service) CanSeeBatch(ctx context.Context, ownerUserIDs []int64, viewerUserID int64, keys []domain.PrivacyKey) (map[int64]map[domain.PrivacyKey]bool, error) {
	out := make(map[int64]map[domain.PrivacyKey]bool, len(ownerUserIDs))
	if viewerUserID == 0 || len(ownerUserIDs) == 0 || len(keys) == 0 {
		return out, nil
	}
	for _, k := range keys {
		if !ValidKey(k) {
			return nil, domain.ErrPrivacyKeyInvalid
		}
	}
	owners := make([]int64, 0, len(ownerUserIDs))
	seen := make(map[int64]struct{}, len(ownerUserIDs))
	for _, id := range ownerUserIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if id == viewerUserID {
			// Self is always visible for all keys (consistent with CanSee's ownerUserID==viewerUserID branch).
			m := make(map[domain.PrivacyKey]bool, len(keys))
			for _, k := range keys {
				m[k] = true
			}
			out[id] = m
			continue
		}
		owners = append(owners, id)
	}
	if len(owners) == 0 {
		return out, nil
	}
	// Batch-fetch rules: existing rows go into the map; missing (owner,key) use defaultRules (replicating GetRules' fallback).
	rulesByOwner := make(map[int64]map[domain.PrivacyKey]domain.PrivacyRules, len(owners))
	if s != nil && s.rules != nil {
		list, err := s.rules.ListPrivacyRules(ctx, owners, keys)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			if !ValidKey(r.Key) {
				continue
			}
			if len(r.Rules) == 0 {
				r.Rules = domain.DefaultPrivacyRules(r.Key)
			}
			if rulesByOwner[r.OwnerUserID] == nil {
				rulesByOwner[r.OwnerUserID] = make(map[domain.PrivacyKey]domain.PrivacyRules, len(keys))
			}
			rulesByOwner[r.OwnerUserID][r.Key] = cloneRules(r)
		}
	}
	var needs evaluationNeeds
	for _, owner := range owners {
		for _, key := range keys {
			rules, ok := rulesByOwner[owner][key]
			if !ok {
				rules = defaultRules(owner, key)
			}
			mergeNeeds(&needs, needsForRules(rules))
		}
	}
	// Batch-fetch "is the viewer among the owner's contacts" (owner→viewer direction,
	// corresponding to CanSee's contacts.Get(owner, viewer)).
	var reverse map[int64]domain.Contact
	if s != nil && s.contacts != nil {
		var err error
		reverse, err = s.contacts.GetReverseContacts(ctx, viewerUserID, owners)
		if err != nil {
			return nil, err
		}
	}
	var baseFacts map[int64]viewerFacts
	if needs.viewerBase {
		var err error
		baseFacts, err = s.loadViewerFacts(ctx, []int64{viewerUserID})
		if err != nil {
			return nil, err
		}
	}
	var membershipFacts map[membershipKey]bool
	if len(needs.chatIDs) > 0 {
		var err error
		membershipFacts, err = s.loadMembershipFacts(ctx, needs.chatIDs, []int64{viewerUserID})
		if err != nil {
			return nil, err
		}
	}
	now := s.now().Unix()
	for _, owner := range owners {
		contact, isContact := reverse[owner]
		m := make(map[domain.PrivacyKey]bool, len(keys))
		for _, k := range keys {
			rules, ok := rulesByOwner[owner][k]
			if !ok {
				rules = defaultRules(owner, k)
			}
			evalCtx := domain.PrivacyContext{
				OwnerUserID:       owner,
				ViewerUserID:      viewerUserID,
				ViewerIsContact:   isContact,
				ViewerCloseFriend: isContact && contact.CloseFriend,
			}
			applyViewerFacts(&evalCtx, baseFacts[viewerUserID], now)
			applyMembershipFacts(&evalCtx, needs.chatIDs, membershipFacts)
			m[k] = Evaluate(rules, evalCtx)
		}
		out[owner] = m
	}
	return out, nil
}

// CanContactForFreeBatch evaluates the complete exception predicate for
// per-user contact requirements. Contacts are always free because the global
// setting is explicitly "noncontact peers"; privacyKeyNoPaidMessages adds
// exceptions beyond that relationship. Both facts come from the in-memory
// privacy/contact read models after their bounded cold loads.
func (s *Service) CanContactForFreeBatch(ctx context.Context, ownerUserIDs []int64, viewerUserID int64) (map[int64]bool, error) {
	owners := dedupNonZero(ownerUserIDs)
	out := make(map[int64]bool, len(owners))
	if viewerUserID == 0 || len(owners) == 0 {
		return out, nil
	}
	visibility, err := s.CanSeeBatch(
		ctx,
		owners,
		viewerUserID,
		[]domain.PrivacyKey{domain.PrivacyKeyNoPaidMessages},
	)
	if err != nil {
		return nil, err
	}
	var contacts map[int64]domain.Contact
	if s != nil && s.contacts != nil {
		contacts, err = s.contacts.GetReverseContacts(ctx, viewerUserID, owners)
		if err != nil {
			return nil, err
		}
	}
	for _, ownerUserID := range owners {
		_, isContact := contacts[ownerUserID]
		out[ownerUserID] = ownerUserID == viewerUserID ||
			isContact ||
			visibility[ownerUserID][domain.PrivacyKeyNoPaidMessages]
	}
	return out, nil
}

// ViewerIsPremium reads the same bounded viewer-facts read model used by
// AllowPremium privacy rules. Contact permission checks must not bypass that
// cache with a per-send users-table query.
func (s *Service) ViewerIsPremium(ctx context.Context, viewerUserID int64) (bool, error) {
	if viewerUserID == 0 {
		return false, nil
	}
	facts, err := s.loadViewerFacts(ctx, []int64{viewerUserID})
	if err != nil {
		return false, err
	}
	fact := facts[viewerUserID]
	return fact.Found && !fact.Bot && fact.PremiumUntil > s.now().Unix(), nil
}

// CanSeeMatrix batch-evaluates the owners × viewers × keys visibility matrix; the result is
// equivalent to calling CanSee per (owner,viewer,key). The production contact store reads
// contact relationships via a single exact owner->viewer pair batch; test/alternative
// implementations that do not support sparse projection fall back to GetMany per owner.
// Returns map[owner]map[viewer]map[key]bool.
func (s *Service) CanSeeMatrix(ctx context.Context, ownerUserIDs, viewerUserIDs []int64, keys []domain.PrivacyKey) (map[int64]map[int64]map[domain.PrivacyKey]bool, error) {
	out := make(map[int64]map[int64]map[domain.PrivacyKey]bool, len(ownerUserIDs))
	if len(ownerUserIDs) == 0 || len(viewerUserIDs) == 0 || len(keys) == 0 {
		return out, nil
	}
	for _, k := range keys {
		if !ValidKey(k) {
			return nil, domain.ErrPrivacyKeyInvalid
		}
	}
	owners := dedupNonZero(ownerUserIDs)
	viewers := dedupNonZero(viewerUserIDs)
	if len(owners) == 0 || len(viewers) == 0 {
		return out, nil
	}
	rulesByOwner := make(map[int64]map[domain.PrivacyKey]domain.PrivacyRules, len(owners))
	if s != nil && s.rules != nil {
		list, err := s.rules.ListPrivacyRules(ctx, owners, keys)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			if !ValidKey(r.Key) {
				continue
			}
			if len(r.Rules) == 0 {
				r.Rules = domain.DefaultPrivacyRules(r.Key)
			}
			if rulesByOwner[r.OwnerUserID] == nil {
				rulesByOwner[r.OwnerUserID] = make(map[domain.PrivacyKey]domain.PrivacyRules, len(keys))
			}
			rulesByOwner[r.OwnerUserID][r.Key] = cloneRules(r)
		}
	}
	var needs evaluationNeeds
	for _, owner := range owners {
		for _, key := range keys {
			rules, ok := rulesByOwner[owner][key]
			if !ok {
				rules = defaultRules(owner, key)
			}
			mergeNeeds(&needs, needsForRules(rules))
		}
	}
	var baseFacts map[int64]viewerFacts
	if needs.viewerBase {
		var err error
		baseFacts, err = s.loadViewerFacts(ctx, viewers)
		if err != nil {
			return nil, err
		}
	}
	var membershipFacts map[membershipKey]bool
	if len(needs.chatIDs) > 0 {
		var err error
		membershipFacts, err = s.loadMembershipFacts(ctx, needs.chatIDs, viewers)
		if err != nil {
			return nil, err
		}
	}
	var contactsByOwner map[int64]map[int64]domain.Contact
	useSparseContacts := false
	if s != nil && s.contacts != nil {
		if loader, ok := s.contacts.(store.SparseContactProjectionStore); ok {
			requested := make(map[int64][]int64, len(owners))
			for _, owner := range owners {
				requested[owner] = viewers
			}
			batch, err := loader.ContactProjectionForViewerUserIDs(ctx, requested)
			if err != nil {
				return nil, err
			}
			contactsByOwner = batch.Contacts
			useSparseContacts = true
		}
	}
	now := s.now().Unix()
	for _, owner := range owners {
		// Which of the owner's contacts are viewers of this batch (= privacy's ViewerIsContact, corresponding to contacts.Get(owner,viewer)).
		var ownerContacts map[int64]domain.Contact
		if useSparseContacts {
			ownerContacts = contactsByOwner[owner]
		} else if s != nil && s.contacts != nil {
			var err error
			ownerContacts, err = s.contacts.GetMany(ctx, owner, viewers)
			if err != nil {
				return nil, err
			}
		}
		perViewer := make(map[int64]map[domain.PrivacyKey]bool, len(viewers))
		for _, viewer := range viewers {
			m := make(map[domain.PrivacyKey]bool, len(keys))
			if owner == viewer {
				for _, k := range keys {
					m[k] = true
				}
				perViewer[viewer] = m
				continue
			}
			contact, isContact := ownerContacts[viewer]
			for _, k := range keys {
				rules, ok := rulesByOwner[owner][k]
				if !ok {
					rules = defaultRules(owner, k)
				}
				evalCtx := domain.PrivacyContext{
					OwnerUserID:       owner,
					ViewerUserID:      viewer,
					ViewerIsContact:   isContact,
					ViewerCloseFriend: isContact && contact.CloseFriend,
				}
				applyViewerFacts(&evalCtx, baseFacts[viewer], now)
				applyMembershipFacts(&evalCtx, needs.chatIDs, membershipFacts)
				m[k] = Evaluate(rules, evalCtx)
			}
			perViewer[viewer] = m
		}
		out[owner] = perViewer
	}
	return out, nil
}

func dedupNonZero(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
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

func Evaluate(rules domain.PrivacyRules, ctx domain.PrivacyContext) bool {
	return domain.EvaluatePrivacy(rules, ctx)
}

func ValidKey(key domain.PrivacyKey) bool {
	switch key {
	case domain.PrivacyKeyStatusTimestamp,
		domain.PrivacyKeyChatInvite,
		domain.PrivacyKeyPhoneCall,
		domain.PrivacyKeyPhoneP2P,
		domain.PrivacyKeyForwards,
		domain.PrivacyKeyProfilePhoto,
		domain.PrivacyKeyPhoneNumber,
		domain.PrivacyKeyAddedByPhone,
		domain.PrivacyKeyVoiceMessages,
		domain.PrivacyKeyAbout,
		domain.PrivacyKeyBirthday,
		domain.PrivacyKeyStarGiftsAutoSave,
		domain.PrivacyKeyNoPaidMessages,
		domain.PrivacyKeySavedMusic:
		return true
	default:
		return false
	}
}

func validateRules(rules []domain.PrivacyRule) error {
	if len(rules) > maxPrivacyRules {
		return domain.ErrPrivacyRuleInvalid
	}
	totalIDs := 0
	for _, rule := range rules {
		switch rule.Kind {
		case domain.PrivacyRuleAllowContacts,
			domain.PrivacyRuleAllowAll,
			domain.PrivacyRuleAllowUsers,
			domain.PrivacyRuleDisallowContacts,
			domain.PrivacyRuleDisallowAll,
			domain.PrivacyRuleDisallowUsers,
			domain.PrivacyRuleAllowChatParticipants,
			domain.PrivacyRuleDisallowChatParticipants,
			domain.PrivacyRuleAllowCloseFriends,
			domain.PrivacyRuleAllowPremium,
			domain.PrivacyRuleAllowBots,
			domain.PrivacyRuleDisallowBots:
		default:
			return domain.ErrPrivacyRuleInvalid
		}
		totalIDs += len(rule.UserIDs) + len(rule.ChatIDs)
		if totalIDs > maxPrivacyRuleIDs {
			return domain.ErrPrivacyRuleInvalid
		}
		for _, id := range rule.UserIDs {
			if id <= 0 {
				return domain.ErrPrivacyRuleInvalid
			}
		}
		for _, id := range rule.ChatIDs {
			if id <= 0 {
				return domain.ErrPrivacyRuleInvalid
			}
		}
	}
	return nil
}

func defaultRules(ownerUserID int64, key domain.PrivacyKey) domain.PrivacyRules {
	return domain.PrivacyRules{
		OwnerUserID: ownerUserID,
		Key:         key,
		Rules:       domain.DefaultPrivacyRules(key),
	}
}

func cloneRules(in domain.PrivacyRules) domain.PrivacyRules {
	out := in
	out.Rules = cloneRuleSlice(in.Rules)
	return out
}

func cloneRuleSlice(in []domain.PrivacyRule) []domain.PrivacyRule {
	out := make([]domain.PrivacyRule, len(in))
	for i, rule := range in {
		out[i] = rule
		out[i].UserIDs = append([]int64(nil), rule.UserIDs...)
		out[i].ChatIDs = append([]int64(nil), rule.ChatIDs...)
	}
	return out
}
