package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"telesrv/internal/domain"
)

// PasswordStore 是 store.PasswordStore 的内存实现。
type PasswordStore struct {
	mu                             sync.RWMutex
	m                              map[int64]domain.PasswordSettings
	reactions                      map[int64]domain.AccountReactionSettings
	accountSettings                map[int64]domain.AccountSettings
	notifySettings                 map[notifySettingsKey]domain.PeerNotifySettings
	stickerCollections             map[stickerCollectionKey][]domain.StickerCollectionItem
	userStickerSets                map[int64]map[int64]domain.UserStickerSet
	savedMusic                     map[int64][]domain.Document
	businessProfiles               map[int64]domain.BusinessProfile
	businessChatLinks              map[string]domain.BusinessChatLink
	businessChatLinkSlugs          map[int64][]string
	quickReplies                   map[int64]map[int]domain.QuickReply
	quickReplyByShortcut           map[int64]map[string]int
	quickReplyMessages             map[int64]map[int]map[int]domain.QuickReplyMessage
	businessDeliveries             map[businessAutomationDeliveryKey]domain.BusinessAutomationDelivery
	connectedBusinessBots          map[int64]domain.ConnectedBusinessBot
	connectedBusinessBotPeerStates map[connectedBusinessBotPeerKey]domain.ConnectedBusinessBotPeerState
	nextQuickReplyID               map[int64]int
	nextQuickReplyMessageID        map[int64]int
}

type businessAutomationDeliveryKey struct {
	ownerUserID      int64
	peerUserID       int64
	kind             domain.BusinessAutomationKind
	triggerMessageID int
}

type connectedBusinessBotPeerKey struct {
	ownerUserID int64
	peerUserID  int64
}

// NewPasswordStore 创建内存 PasswordStore。
func NewPasswordStore() *PasswordStore {
	return &PasswordStore{
		m:                              make(map[int64]domain.PasswordSettings),
		reactions:                      make(map[int64]domain.AccountReactionSettings),
		accountSettings:                make(map[int64]domain.AccountSettings),
		notifySettings:                 make(map[notifySettingsKey]domain.PeerNotifySettings),
		stickerCollections:             make(map[stickerCollectionKey][]domain.StickerCollectionItem),
		userStickerSets:                make(map[int64]map[int64]domain.UserStickerSet),
		savedMusic:                     make(map[int64][]domain.Document),
		businessProfiles:               make(map[int64]domain.BusinessProfile),
		businessChatLinks:              make(map[string]domain.BusinessChatLink),
		businessChatLinkSlugs:          make(map[int64][]string),
		quickReplies:                   make(map[int64]map[int]domain.QuickReply),
		quickReplyByShortcut:           make(map[int64]map[string]int),
		quickReplyMessages:             make(map[int64]map[int]map[int]domain.QuickReplyMessage),
		businessDeliveries:             make(map[businessAutomationDeliveryKey]domain.BusinessAutomationDelivery),
		connectedBusinessBots:          make(map[int64]domain.ConnectedBusinessBot),
		connectedBusinessBotPeerStates: make(map[connectedBusinessBotPeerKey]domain.ConnectedBusinessBotPeerState),
		nextQuickReplyID:               make(map[int64]int),
		nextQuickReplyMessageID:        make(map[int64]int),
	}
}

func (s *PasswordStore) GetByUser(_ context.Context, userID int64) (domain.PasswordSettings, bool, error) {
	s.mu.RLock()
	settings, ok := s.m[userID]
	s.mu.RUnlock()
	return clonePasswordSettings(settings), ok, nil
}

func (s *PasswordStore) Save(_ context.Context, userID int64, settings domain.PasswordSettings) error {
	s.mu.Lock()
	settings.LoginEmail = normalizeLoginEmail(settings.LoginEmail)
	settings.LoginEmailPattern = domain.MaskEmail(settings.LoginEmail)
	if settings.LoginEmail != "" {
		for ownerUserID, existing := range s.m {
			if ownerUserID != userID && strings.EqualFold(existing.LoginEmail, settings.LoginEmail) {
				s.mu.Unlock()
				return domain.ErrEmailOccupied
			}
		}
	}
	s.m[userID] = clonePasswordSettings(settings)
	s.mu.Unlock()
	return nil
}

func (s *PasswordStore) LoginEmailOwner(_ context.Context, email string) (int64, bool, error) {
	email = normalizeLoginEmail(email)
	if email == "" {
		return 0, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for userID, settings := range s.m {
		if strings.EqualFold(settings.LoginEmail, email) {
			return userID, true, nil
		}
	}
	return 0, false, nil
}

func normalizeLoginEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func clonePasswordSettings(in domain.PasswordSettings) domain.PasswordSettings {
	out := in
	if in.CurrentAlgo != nil {
		algo := *in.CurrentAlgo
		algo.Salt1 = append([]byte(nil), algo.Salt1...)
		algo.Salt2 = append([]byte(nil), algo.Salt2...)
		algo.P = append([]byte(nil), algo.P...)
		out.CurrentAlgo = &algo
	}
	out.SRPB = append([]byte(nil), in.SRPB...)
	out.NewAlgo.Salt1 = append([]byte(nil), in.NewAlgo.Salt1...)
	out.NewAlgo.Salt2 = append([]byte(nil), in.NewAlgo.Salt2...)
	out.NewAlgo.P = append([]byte(nil), in.NewAlgo.P...)
	out.NewSecureAlgo.Salt = append([]byte(nil), in.NewSecureAlgo.Salt...)
	out.SecureRandom = append([]byte(nil), in.SecureRandom...)
	out.SRPVerifier = append([]byte(nil), in.SRPVerifier...)
	out.SRPBSecret = append([]byte(nil), in.SRPBSecret...)
	return out
}

func (s *PasswordStore) GetReactionSettings(_ context.Context, userID int64) (domain.AccountReactionSettings, bool, error) {
	s.mu.RLock()
	settings, ok := s.reactions[userID]
	s.mu.RUnlock()
	return cloneAccountReactionSettings(settings), ok, nil
}

func (s *PasswordStore) SaveReactionSettings(_ context.Context, userID int64, settings domain.AccountReactionSettings) error {
	s.mu.Lock()
	s.reactions[userID] = cloneAccountReactionSettings(settings)
	s.mu.Unlock()
	return nil
}

func cloneAccountReactionSettings(in domain.AccountReactionSettings) domain.AccountReactionSettings {
	out := in
	if in.PaidPrivacy.Peer != nil {
		peer := *in.PaidPrivacy.Peer
		out.PaidPrivacy.Peer = &peer
	}
	return out
}

func (s *PasswordStore) GetAccountSettings(_ context.Context, userID int64) (domain.AccountSettings, bool, error) {
	s.mu.RLock()
	settings, ok := s.accountSettings[userID]
	s.mu.RUnlock()
	return settings, ok, nil // AccountSettings 全是值类型，无需深拷贝
}

func (s *PasswordStore) GetAccountSettingsBatch(_ context.Context, userIDs []int64) (map[int64]domain.AccountSettings, error) {
	out := make(map[int64]domain.AccountSettings, len(userIDs))
	s.mu.RLock()
	for _, userID := range userIDs {
		if settings, ok := s.accountSettings[userID]; ok {
			out[userID] = settings
		}
	}
	s.mu.RUnlock()
	return out, nil
}

func (s *PasswordStore) SaveAccountSettings(_ context.Context, userID int64, settings domain.AccountSettings) error {
	s.mu.Lock()
	s.accountSettings[userID] = settings
	s.mu.Unlock()
	return nil
}

type notifySettingsKey struct {
	owner    int64
	kind     domain.NotifyScopeKind
	peerType domain.PeerType
	peerID   int64
	topicID  int
}

func notifySettingsKeyOf(owner int64, scope domain.NotifyScope) notifySettingsKey {
	key := notifySettingsKey{owner: owner, kind: scope.Kind}
	if scope.Kind == domain.NotifyScopePeer {
		key.peerType = scope.Peer.Type
		key.peerID = scope.Peer.ID
		key.topicID = scope.TopicID
	}
	return key
}

func (s *PasswordStore) GetNotifySettings(_ context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, bool, error) {
	s.mu.RLock()
	settings, ok := s.notifySettings[notifySettingsKeyOf(ownerUserID, scope)]
	s.mu.RUnlock()
	return settings.Clone(), ok, nil
}

func (s *PasswordStore) SaveNotifySettings(_ context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error {
	s.mu.Lock()
	s.notifySettings[notifySettingsKeyOf(ownerUserID, scope)] = settings.Clone()
	s.mu.Unlock()
	return nil
}

func (s *PasswordStore) ResetNotifySettings(_ context.Context, ownerUserID int64) error {
	s.mu.Lock()
	for key := range s.notifySettings {
		if key.owner == ownerUserID {
			delete(s.notifySettings, key)
		}
	}
	s.mu.Unlock()
	return nil
}

type stickerCollectionKey struct {
	owner int64
	kind  domain.StickerCollectionKind
}

func (s *PasswordStore) SaveStickerCollectionItem(_ context.Context, userID int64, kind domain.StickerCollectionKind, documentID int64, unsave bool, now, max int) error {
	if userID == 0 || documentID == 0 {
		return domain.ErrStickerInvalid
	}
	if max <= 0 {
		max = domain.MaxStickerCollectionItems(kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stickerCollectionKey{owner: userID, kind: kind}
	cur := s.stickerCollections[key]
	// 移除既有同 id 项。
	next := make([]domain.StickerCollectionItem, 0, len(cur)+1)
	for _, it := range cur {
		if it.DocumentID == documentID {
			continue
		}
		next = append(next, it)
	}
	if unsave {
		s.stickerCollections[key] = next
		return nil
	}
	// 最新置顶 + 截断。
	next = append([]domain.StickerCollectionItem{{DocumentID: documentID, Date: now}}, next...)
	if len(next) > max {
		next = next[:max]
	}
	s.stickerCollections[key] = next
	return nil
}

func (s *PasswordStore) ListStickerCollection(_ context.Context, userID int64, kind domain.StickerCollectionKind, limit int) ([]domain.StickerCollectionItem, error) {
	if limit <= 0 || limit > domain.MaxStickerCollectionItems(kind) {
		limit = domain.MaxStickerCollectionItems(kind)
	}
	s.mu.RLock()
	cur := s.stickerCollections[stickerCollectionKey{owner: userID, kind: kind}]
	s.mu.RUnlock()
	if len(cur) > limit {
		cur = cur[:limit]
	}
	return append([]domain.StickerCollectionItem(nil), cur...), nil
}

func (s *PasswordStore) ClearStickerCollection(_ context.Context, userID int64, kind domain.StickerCollectionKind) error {
	s.mu.Lock()
	delete(s.stickerCollections, stickerCollectionKey{owner: userID, kind: kind})
	s.mu.Unlock()
	return nil
}

func (s *PasswordStore) InstallUserStickerSet(_ context.Context, userID int64, setID int64, kind domain.StickerSetKind, archived bool, installedDate int) error {
	if userID == 0 || setID == 0 {
		return domain.ErrStickerInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sets := s.userStickerSets[userID]
	if sets == nil {
		sets = make(map[int64]domain.UserStickerSet)
		s.userStickerSets[userID] = sets
	}
	order := int64(installedDate) << 32
	sets[setID] = domain.UserStickerSet{
		OwnerUserID:   userID,
		StickerSetID:  setID,
		Kind:          kind,
		Archived:      archived,
		InstalledDate: installedDate,
		OrderValue:    order,
	}
	return nil
}

func (s *PasswordStore) UninstallUserStickerSet(_ context.Context, userID int64, setID int64) error {
	s.mu.Lock()
	if sets := s.userStickerSets[userID]; sets != nil {
		delete(sets, setID)
	}
	s.mu.Unlock()
	return nil
}

func (s *PasswordStore) SetUserStickerSetArchived(_ context.Context, userID int64, setID int64, archived bool, now int) error {
	s.mu.Lock()
	if sets := s.userStickerSets[userID]; sets != nil {
		if item, ok := sets[setID]; ok {
			item.Archived = archived
			if !archived && now > 0 {
				item.OrderValue = int64(now) << 32
			}
			sets[setID] = item
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *PasswordStore) ReorderUserStickerSets(_ context.Context, userID int64, kind domain.StickerSetKind, order []int64, now int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets := s.userStickerSets[userID]
	if sets == nil {
		return nil
	}
	orderValue := int64(now) << 32
	for _, id := range order {
		item, ok := sets[id]
		if !ok || item.Kind != kind {
			continue
		}
		item.OrderValue = orderValue
		sets[id] = item
		orderValue--
	}
	return nil
}

func (s *PasswordStore) ListUserStickerSets(_ context.Context, userID int64, kind domain.StickerSetKind, archived *bool, offsetID int64, limit int) ([]domain.UserStickerSet, int, error) {
	if limit <= 0 || limit > domain.MaxInstalledStickerSets {
		limit = domain.MaxInstalledStickerSets
	}
	s.mu.RLock()
	raw := s.userStickerSets[userID]
	items := make([]domain.UserStickerSet, 0, len(raw))
	for _, item := range raw {
		if item.Kind != kind {
			continue
		}
		if archived != nil && item.Archived != *archived {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].OrderValue == items[j].OrderValue {
			return items[i].StickerSetID > items[j].StickerSetID
		}
		return items[i].OrderValue > items[j].OrderValue
	})
	total := len(items)
	if offsetID != 0 {
		start := 0
		for i, item := range items {
			if item.StickerSetID == offsetID {
				start = i + 1
				break
			}
		}
		items = items[start:]
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return append([]domain.UserStickerSet(nil), items...), total, nil
}

func (s *PasswordStore) ListNotifyExceptions(_ context.Context, ownerUserID int64) ([]domain.NotifyException, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.NotifyException, 0)
	for key, settings := range s.notifySettings {
		if key.owner != ownerUserID || key.kind != domain.NotifyScopePeer || settings.IsZero() {
			continue
		}
		out = append(out, domain.NotifyException{
			Peer:     domain.Peer{Type: key.peerType, ID: key.peerID},
			TopicID:  key.topicID,
			Settings: settings.Clone(),
		})
	}
	return out, nil
}

func (s *PasswordStore) AllPeerNotifySettings(_ context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error) {
	out := make(map[domain.Peer]domain.PeerNotifySettings)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for key, settings := range s.notifySettings {
		if key.owner != ownerUserID || key.kind != domain.NotifyScopePeer || key.topicID != 0 {
			continue
		}
		out[domain.Peer{Type: key.peerType, ID: key.peerID}] = settings.Clone()
	}
	return out, nil
}

func (s *PasswordStore) GetPeerNotifySettings(_ context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error) {
	out := make(map[domain.Peer]domain.PeerNotifySettings, len(peers))
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range peers {
		key := notifySettingsKey{owner: ownerUserID, kind: domain.NotifyScopePeer, peerType: p.Type, peerID: p.ID, topicID: 0}
		if settings, ok := s.notifySettings[key]; ok {
			out[p] = settings.Clone()
		}
	}
	return out, nil
}

func (s *PasswordStore) SaveMusic(_ context.Context, req domain.SaveMusicRequest) error {
	if req.UserID == 0 || req.Document.ID == 0 || !req.Document.IsMusic() {
		return domain.ErrDocumentInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.savedMusic[req.UserID]
	if req.Unsave {
		s.savedMusic[req.UserID] = removeSavedMusicDocument(current, req.Document.ID)
		return nil
	}
	if req.AfterDocumentID == req.Document.ID {
		for _, doc := range current {
			if doc.ID == req.Document.ID {
				return nil
			}
		}
		return domain.ErrDocumentInvalid
	}
	next := make([]domain.Document, 0, len(current)+1)
	afterIndex := -1
	for _, doc := range current {
		if doc.ID == req.Document.ID {
			continue
		}
		if doc.ID == req.AfterDocumentID {
			afterIndex = len(next)
		}
		next = append(next, cloneDocument(doc))
	}
	insert := cloneDocument(req.Document)
	if req.AfterDocumentID != 0 {
		if afterIndex < 0 {
			return domain.ErrDocumentInvalid
		}
		next = append(next, domain.Document{})
		copy(next[afterIndex+2:], next[afterIndex+1:])
		next[afterIndex+1] = insert
	} else {
		next = append([]domain.Document{insert}, next...)
	}
	if len(next) > domain.MaxSavedMusicItems {
		next = next[:domain.MaxSavedMusicItems]
	}
	s.savedMusic[req.UserID] = next
	return nil
}

func (s *PasswordStore) ListSavedMusicIDs(_ context.Context, userID int64, limit int) ([]int64, error) {
	s.mu.RLock()
	current := s.savedMusic[userID]
	s.mu.RUnlock()
	if limit <= 0 || limit > domain.MaxSavedMusicItems {
		limit = domain.MaxSavedMusicItems
	}
	if len(current) > limit {
		current = current[:limit]
	}
	out := make([]int64, 0, len(current))
	for _, doc := range current {
		out = append(out, doc.ID)
	}
	return out, nil
}

func (s *PasswordStore) ListSavedMusic(_ context.Context, userID int64, offset, limit int) (domain.SavedMusicList, error) {
	s.mu.RLock()
	current := cloneDocuments(s.savedMusic[userID])
	s.mu.RUnlock()
	out := domain.SavedMusicList{UserID: userID, Count: len(current)}
	if offset < 0 || offset >= len(current) || limit <= 0 {
		return out, nil
	}
	end := offset + limit
	if end > len(current) {
		end = len(current)
	}
	out.Documents = cloneDocuments(current[offset:end])
	return out, nil
}

func (s *PasswordStore) GetSavedMusicByIDs(_ context.Context, userID int64, ids []int64) (domain.SavedMusicList, error) {
	s.mu.RLock()
	current := cloneDocuments(s.savedMusic[userID])
	s.mu.RUnlock()
	byID := make(map[int64]domain.Document, len(current))
	for _, doc := range current {
		byID[doc.ID] = doc
	}
	out := domain.SavedMusicList{UserID: userID, Count: len(current)}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if doc, ok := byID[id]; ok {
			out.Documents = append(out.Documents, cloneDocument(doc))
		}
	}
	return out, nil
}

func removeSavedMusicDocument(in []domain.Document, id int64) []domain.Document {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.Document, 0, len(in))
	for _, doc := range in {
		if doc.ID != id {
			out = append(out, cloneDocument(doc))
		}
	}
	return out
}

func cloneDocument(in domain.Document) domain.Document {
	out := in
	out.FileReference = append([]byte(nil), in.FileReference...)
	out.Attributes = append([]domain.DocumentAttribute(nil), in.Attributes...)
	out.Thumbs = append([]domain.PhotoSize(nil), in.Thumbs...)
	return out
}

func cloneDocuments(in []domain.Document) []domain.Document {
	out := make([]domain.Document, 0, len(in))
	for _, doc := range in {
		out = append(out, cloneDocument(doc))
	}
	return out
}
