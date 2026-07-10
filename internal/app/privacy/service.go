package privacy

import (
	"context"
	"slices"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const maxPrivacyRules = 100

// Service owns account privacy rules and viewer-specific evaluation.
type Service struct {
	rules    store.PrivacyStore
	contacts store.ContactStore
}

func NewService(rules store.PrivacyStore, contacts store.ContactStore) *Service {
	return &Service{rules: rules, contacts: contacts}
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
	if !ValidKey(key) {
		return domain.PrivacyRules{}, domain.ErrPrivacyKeyInvalid
	}
	if len(rules) == 0 {
		rules = domain.DefaultPrivacyRules(key)
	}
	if err := validateRules(rules); err != nil {
		return domain.PrivacyRules{}, err
	}
	out := domain.PrivacyRules{OwnerUserID: ownerUserID, Key: key, Rules: cloneRuleSlice(rules)}
	if s != nil && s.rules != nil {
		if err := s.rules.SetPrivacyRules(ctx, out); err != nil {
			return domain.PrivacyRules{}, err
		}
	}
	return out, nil
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
	evalCtx := domain.PrivacyContext{
		OwnerUserID:  ownerUserID,
		ViewerUserID: viewerUserID,
	}
	if s != nil && s.contacts != nil {
		if _, found, err := s.contacts.Get(ctx, ownerUserID, viewerUserID); err != nil {
			return false, err
		} else if found {
			evalCtx.ViewerIsContact = true
		}
	}
	return Evaluate(rules, evalCtx), nil
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

// CanSeeBatch 批量评估多个 owner 对同一 viewer 在多个 key 上的可见性，结果等价于对每个
// (owner,key) 调一次 CanSee，但只用一次 ListPrivacyRules + 一次 GetReverseContacts + 内存
// Evaluate（消除 projectBatch / fan-out 投影里 per-user 3×CanSee×2行 的 N+1）。返回
// map[ownerUserID]map[key]bool；owner==viewer 恒 true（与 CanSee 一致）。
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
			// 自己恒可见全部 key（与 CanSee 的 ownerUserID==viewerUserID 分支一致）。
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
	// 批量取 rules：存在的行进 map，缺失的 (owner,key) 用 defaultRules（复刻 GetRules 兜底）。
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
	// 批量取「viewer 是否在 owner 的联系人里」（owner→viewer 方向，对应 CanSee 的
	// contacts.Get(owner, viewer)）。
	var reverse map[int64]domain.Contact
	if s != nil && s.contacts != nil {
		var err error
		reverse, err = s.contacts.GetReverseContacts(ctx, viewerUserID, owners)
		if err != nil {
			return nil, err
		}
	}
	for _, owner := range owners {
		_, isContact := reverse[owner]
		m := make(map[domain.PrivacyKey]bool, len(keys))
		for _, k := range keys {
			rules, ok := rulesByOwner[owner][k]
			if !ok {
				rules = defaultRules(owner, k)
			}
			m[k] = Evaluate(rules, domain.PrivacyContext{
				OwnerUserID:     owner,
				ViewerUserID:    viewerUserID,
				ViewerIsContact: isContact,
			})
		}
		out[owner] = m
	}
	return out, nil
}

// CanSeeMatrix 批量评估 owners × viewers × keys 的可见性矩阵，结果等价于逐 (owner,viewer,key)
// 调 CanSee，但只用一次 ListPrivacyRules + 每 owner 一次 GetMany(owner,viewers) + 内存 Evaluate
// （把 fan-out 投影从 O(viewer) 次 privacy 查询降到 O(owner)）。返回 map[owner]map[viewer]map[key]bool。
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
	for _, owner := range owners {
		// owner 的联系人中哪些是本批 viewer（= privacy 的 ViewerIsContact，对应 contacts.Get(owner,viewer)）。
		var ownerContacts map[int64]domain.Contact
		if s != nil && s.contacts != nil {
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
			_, isContact := ownerContacts[viewer]
			for _, k := range keys {
				rules, ok := rulesByOwner[owner][k]
				if !ok {
					rules = defaultRules(owner, k)
				}
				m[k] = Evaluate(rules, domain.PrivacyContext{
					OwnerUserID:     owner,
					ViewerUserID:    viewer,
					ViewerIsContact: isContact,
				})
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
	if ctx.OwnerUserID != 0 && ctx.OwnerUserID == ctx.ViewerUserID {
		return true
	}
	if len(rules.Rules) == 0 {
		rules.Rules = domain.DefaultPrivacyRules(rules.Key)
	}
	for _, rule := range rules.Rules {
		if explicitDisallowMatches(rule, ctx) {
			return false
		}
	}
	for _, rule := range rules.Rules {
		if explicitAllowMatches(rule, ctx) {
			return true
		}
	}
	for _, rule := range rules.Rules {
		switch rule.Kind {
		case domain.PrivacyRuleDisallowContacts:
			if ctx.ViewerIsContact {
				return false
			}
		case domain.PrivacyRuleAllowContacts:
			if ctx.ViewerIsContact {
				return true
			}
		}
	}
	for _, rule := range rules.Rules {
		switch rule.Kind {
		case domain.PrivacyRuleDisallowAll:
			return false
		case domain.PrivacyRuleAllowAll:
			return true
		}
	}
	return false
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
	}
	return nil
}

func explicitDisallowMatches(rule domain.PrivacyRule, ctx domain.PrivacyContext) bool {
	switch rule.Kind {
	case domain.PrivacyRuleDisallowUsers:
		return slices.Contains(rule.UserIDs, ctx.ViewerUserID)
	case domain.PrivacyRuleDisallowChatParticipants:
		return intersects(rule.ChatIDs, ctx.SharedChatIDs)
	case domain.PrivacyRuleDisallowBots:
		return ctx.ViewerIsBot
	default:
		return false
	}
}

func explicitAllowMatches(rule domain.PrivacyRule, ctx domain.PrivacyContext) bool {
	switch rule.Kind {
	case domain.PrivacyRuleAllowUsers:
		return slices.Contains(rule.UserIDs, ctx.ViewerUserID)
	case domain.PrivacyRuleAllowChatParticipants:
		return intersects(rule.ChatIDs, ctx.SharedChatIDs)
	case domain.PrivacyRuleAllowCloseFriends:
		return ctx.ViewerCloseFriend
	case domain.PrivacyRuleAllowPremium:
		return ctx.ViewerIsPremium
	case domain.PrivacyRuleAllowBots:
		return ctx.ViewerIsBot
	default:
		return false
	}
}

func intersects(a, b []int64) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[int64]struct{}, len(a))
	for _, id := range a {
		set[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := set[id]; ok {
			return true
		}
	}
	return false
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
