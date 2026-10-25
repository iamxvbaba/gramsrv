package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"time"
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
	deliveryOutbox                 *DeliveryOutboxStore
	updateEvents                   *UpdateEventStore
}

// AttachDeliveryOutbox wires the production-shaped durable boundary used by
// account mutation tests. PostgreSQL owns the real single-transaction commit.
func (s *PasswordStore) AttachDeliveryOutbox(outbox *DeliveryOutboxStore) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.deliveryOutbox = outbox
	s.mu.Unlock()
}

// AttachUpdateEventStore binds quick-reply account mutations to the same
// event/dispatch stream consumed by updates.getDifference.
func (s *PasswordStore) AttachUpdateEventStore(events *UpdateEventStore) {
	if s == nil || events == nil {
		return
	}
	s.mu.Lock()
	s.updateEvents = events
	s.mu.Unlock()
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
		updateEvents:                   NewUpdateEventStore(),
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
	defer s.mu.Unlock()
	return s.savePasswordLocked(userID, settings)
}

func (s *PasswordStore) savePasswordLocked(userID int64, settings domain.PasswordSettings) error {
	settings.LoginEmail = normalizeLoginEmail(settings.LoginEmail)
	settings.LoginEmailPattern = domain.MaskEmail(settings.LoginEmail)
	if settings.LoginEmail != "" {
		for ownerUserID, existing := range s.m {
			if ownerUserID != userID && strings.EqualFold(existing.LoginEmail, settings.LoginEmail) {
				return domain.ErrEmailOccupied
			}
		}
	}
	s.m[userID] = clonePasswordSettings(settings)
	return nil
}

func (s *PasswordStore) Mutate(ctx context.Context, userID int64, apply func(*domain.PasswordSettings) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	settings := clonePasswordSettings(s.m[userID])
	if err := apply(&settings); err != nil {
		return err
	}
	return s.savePasswordLocked(userID, settings)
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
	if in.PendingChange != nil {
		change := *in.PendingChange
		change.CodeHash = append([]byte(nil), change.CodeHash...)
		change.Input.NewPasswordHash = append([]byte(nil), change.Input.NewPasswordHash...)
		if change.Input.NewAlgo != nil {
			algo := *change.Input.NewAlgo
			algo.Salt1 = append([]byte(nil), algo.Salt1...)
			algo.Salt2 = append([]byte(nil), algo.Salt2...)
			algo.P = append([]byte(nil), algo.P...)
			change.Input.NewAlgo = &algo
		}
		out.PendingChange = &change
	}
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

// UpdateReactionSettings mirrors the PostgreSQL patch boundary: the patch,
// its delivery effects and the stored settings change together or not at all.
func (s *PasswordStore) UpdateReactionSettings(ctx context.Context, userID int64, patch domain.AccountReactionSettingsPatch, effects store.DeliveryEffectsBuilder[domain.AccountReactionSettings]) (domain.AccountReactionSettings, error) {
	if userID <= 0 {
		return domain.AccountReactionSettings{}, fmt.Errorf("update reaction settings: invalid user")
	}
	if effects == nil && !patch.NotifyOnly() {
		return domain.AccountReactionSettings{}, store.ErrDeliveryOutboxRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.reactions[userID]
	if !found {
		current = domain.DefaultAccountReactionSettings()
	}
	updated, err := patch.Apply(current)
	if err != nil {
		return domain.AccountReactionSettings{}, err
	}
	if effects != nil {
		intents, err := effects(cloneAccountReactionSettings(updated))
		if err != nil {
			return domain.AccountReactionSettings{}, err
		}
		if _, err := applyDeliveryEffects(ctx, intents, s.deliveryOutbox, nil); err != nil {
			return domain.AccountReactionSettings{}, err
		}
	}
	s.reactions[userID] = cloneAccountReactionSettings(updated)
	return cloneAccountReactionSettings(updated), nil
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

func (s *PasswordStore) GetForumTopicNotifySettings(_ context.Context, ownerUserID int64, peer domain.Peer, topicIDs []int) (map[int]domain.PeerNotifySettings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int]domain.PeerNotifySettings, len(topicIDs))
	for _, id := range topicIDs {
		key := notifySettingsKeyOf(ownerUserID, domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer, TopicID: id})
		if settings, ok := s.notifySettings[key]; ok {
			out[id] = settings.Clone()
		}
	}
	return out, nil
}

func (s *PasswordStore) SaveNotifySettings(_ context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.notifySettings[notifySettingsKeyOf(ownerUserID, scope)] = settings.Clone()
	s.mu.Unlock()
	return nil
}

func (s *PasswordStore) SaveNotifySettingsWithDelivery(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings, delivery store.DeliveryOutboxEnqueue) error {
	s.mu.RLock()
	outbox := s.deliveryOutbox
	s.mu.RUnlock()
	if outbox == nil {
		return store.ErrDeliveryOutboxRequired
	}
	// Validate and append before the infallible in-memory map assignment. This
	// fake has no cross-resource rollback; production correctness is provided by
	// PasswordStore's PostgreSQL transaction above.
	if _, err := applyDeliveryEffects(ctx, []store.DeliveryEffect{store.AbsoluteDeliveryEffect(delivery)}, outbox, nil); err != nil {
		return err
	}
	return s.SaveNotifySettings(ctx, ownerUserID, scope, settings)
}

func (s *PasswordStore) ResetNotifySettingsWithDelivery(ctx context.Context, ownerUserID int64, effects store.DeliveryEffectsBuilder[store.NotifySettingsResetSnapshot]) error {
	if ownerUserID <= 0 || effects == nil {
		return store.ErrDeliveryOutboxRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	outbox := s.deliveryOutbox
	if outbox == nil {
		return store.ErrDeliveryOutboxRequired
	}
	snapshot := store.NotifySettingsResetSnapshot{OwnerUserID: ownerUserID}
	for key := range s.notifySettings {
		if key.owner != ownerUserID {
			continue
		}
		scope := domain.NotifyScope{Kind: key.kind, TopicID: key.topicID}
		if key.kind == domain.NotifyScopePeer {
			scope.Peer = domain.Peer{Type: key.peerType, ID: key.peerID}
		}
		snapshot.Scopes = append(snapshot.Scopes, scope)
	}
	sort.Slice(snapshot.Scopes, func(i, j int) bool {
		a, b := snapshot.Scopes[i], snapshot.Scopes[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Peer.Type != b.Peer.Type {
			return a.Peer.Type < b.Peer.Type
		}
		if a.Peer.ID != b.Peer.ID {
			return a.Peer.ID < b.Peer.ID
		}
		return a.TopicID < b.TopicID
	})
	intents, err := effects(snapshot)
	if err != nil {
		return err
	}
	if err := store.ValidateNotifySettingsResetDeliveryEffects(snapshot, intents); err != nil {
		return err
	}
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if err := appendAbsoluteDeliveryEffectsLocked(outbox, intents, time.Now()); err != nil {
		return err
	}
	for key := range s.notifySettings {
		if key.owner == ownerUserID {
			delete(s.notifySettings, key)
		}
	}
	return nil
}

type stickerCollectionKey struct {
	owner int64
	kind  domain.StickerCollectionKind
}

func (s *PasswordStore) MutateStickerCollection(ctx context.Context, mutation domain.StickerCollectionMutation, effects store.DeliveryEffectsBuilder[domain.StickerCollectionMutation]) error {
	if err := validateMemoryStickerCollectionMutation(mutation); err != nil {
		return err
	}
	if effects == nil {
		return store.ErrDeliveryOutboxRequired
	}
	intents, err := effects(mutation)
	if err != nil {
		return fmt.Errorf("build sticker collection delivery effects: %w", err)
	}
	return s.withAbsoluteDeliveryEffects(ctx, intents, func() {
		key := stickerCollectionKey{owner: mutation.OwnerUserID, kind: mutation.Kind}
		switch mutation.Mutation {
		case domain.StickerCollectionMutationClear:
			delete(s.stickerCollections, key)
		case domain.StickerCollectionMutationSave, domain.StickerCollectionMutationUnsave:
			cur := s.stickerCollections[key]
			next := make([]domain.StickerCollectionItem, 0, len(cur)+1)
			for _, it := range cur {
				if it.DocumentID != mutation.DocumentID {
					next = append(next, it)
				}
			}
			if mutation.Mutation == domain.StickerCollectionMutationSave {
				next = append([]domain.StickerCollectionItem{{DocumentID: mutation.DocumentID, Date: mutation.Date}}, next...)
				if max := domain.MaxStickerCollectionItems(mutation.Kind); len(next) > max {
					next = next[:max]
				}
			}
			s.stickerCollections[key] = next
		}
	})
}

func (s *PasswordStore) ListStickerCollection(_ context.Context, userID int64, kind domain.StickerCollectionKind, limit int) ([]domain.StickerCollectionItem, error) {
	if userID <= 0 || !validMemoryStickerCollectionKind(kind) {
		return nil, domain.ErrStickerInvalid
	}
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

func (s *PasswordStore) MutateUserStickerSets(ctx context.Context, mutation domain.UserStickerSetMutation, effects store.DeliveryEffectsBuilder[domain.UserStickerSetMutation]) error {
	if err := validateMemoryUserStickerSetMutation(mutation); err != nil {
		return err
	}
	if effects == nil {
		return store.ErrDeliveryOutboxRequired
	}
	mutation.Items = append([]domain.UserStickerSetMutationItem(nil), mutation.Items...)
	mutation.Order = append([]int64(nil), mutation.Order...)
	intents, err := effects(mutation)
	if err != nil {
		return fmt.Errorf("build user sticker set delivery effects: %w", err)
	}
	return s.withAbsoluteDeliveryEffects(ctx, intents, func() {
		sets := s.userStickerSets[mutation.OwnerUserID]
		if sets == nil {
			sets = make(map[int64]domain.UserStickerSet)
			s.userStickerSets[mutation.OwnerUserID] = sets
		}
		switch mutation.Mutation {
		case domain.UserStickerSetMutationInstall:
			base := int64(mutation.Date) << 32
			for i, input := range mutation.Items {
				installedDate := mutation.Date
				if current, ok := sets[input.StickerSetID]; ok && current.InstalledDate != 0 {
					installedDate = current.InstalledDate
				}
				sets[input.StickerSetID] = domain.UserStickerSet{
					OwnerUserID: mutation.OwnerUserID, StickerSetID: input.StickerSetID,
					Kind: input.Kind, Archived: mutation.Archived, InstalledDate: installedDate,
					OrderValue: base - int64(i),
				}
			}
		case domain.UserStickerSetMutationUninstall:
			for _, input := range mutation.Items {
				delete(sets, input.StickerSetID)
			}
		case domain.UserStickerSetMutationArchive, domain.UserStickerSetMutationUnarchive:
			archived := mutation.Mutation == domain.UserStickerSetMutationArchive
			for _, input := range mutation.Items {
				if item, ok := sets[input.StickerSetID]; ok {
					item.Archived = archived
					if !archived && mutation.Date > 0 {
						item.OrderValue = int64(mutation.Date) << 32
					}
					sets[input.StickerSetID] = item
				}
			}
		case domain.UserStickerSetMutationReorder:
			orderValue := int64(mutation.Date) << 32
			for _, id := range mutation.Order {
				if item, ok := sets[id]; ok && item.Kind == mutation.Kind {
					item.OrderValue = orderValue
					sets[id] = item
				}
				orderValue--
			}
		}
	})
}

func (s *PasswordStore) ListUserStickerSets(_ context.Context, userID int64, kind domain.StickerSetKind, archived *bool, offsetID int64, limit int) ([]domain.UserStickerSet, int, error) {
	if userID <= 0 || !validMemoryInstalledStickerSetKind(kind) {
		return nil, 0, domain.ErrStickerInvalid
	}
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

// withAbsoluteDeliveryEffects is the memory store's transaction boundary for
// account-local mutations. Both resource mutexes stay held until the account
// snapshot and every outbox row have been installed, so concurrent readers
// cannot observe either half early. All fallible validation happens before the
// first write; the callback is deliberately infallible map mutation only.
func (s *PasswordStore) withAbsoluteDeliveryEffects(ctx context.Context, effects []store.DeliveryEffect, mutate func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(effects) == 0 {
		return store.ErrDeliveryOutboxRequired
	}
	for i := range effects {
		if err := effects[i].Validate(); err != nil {
			return fmt.Errorf("delivery effect %d: %w", i, err)
		}
		if effects[i].Kind != store.DeliveryEffectAbsolute {
			return fmt.Errorf("delivery effect %d: sticker account mutations require absolute delivery", i)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	outbox := s.deliveryOutbox
	if outbox == nil {
		return store.ErrDeliveryOutboxRequired
	}
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if err := appendAbsoluteDeliveryEffectsLocked(outbox, effects, time.Now()); err != nil {
		return err
	}
	mutate()
	return nil
}

func validateMemoryStickerCollectionMutation(mutation domain.StickerCollectionMutation) error {
	if mutation.OwnerUserID <= 0 || !validMemoryStickerCollectionKind(mutation.Kind) {
		return domain.ErrStickerInvalid
	}
	switch mutation.Mutation {
	case domain.StickerCollectionMutationSave, domain.StickerCollectionMutationUnsave:
		if mutation.DocumentID <= 0 {
			return domain.ErrStickerInvalid
		}
	case domain.StickerCollectionMutationClear:
		if mutation.DocumentID != 0 {
			return domain.ErrStickerInvalid
		}
	default:
		return domain.ErrStickerInvalid
	}
	return nil
}

func validateMemoryUserStickerSetMutation(mutation domain.UserStickerSetMutation) error {
	if mutation.OwnerUserID <= 0 {
		return domain.ErrStickerInvalid
	}
	if mutation.Mutation == domain.UserStickerSetMutationReorder {
		if !validMemoryInstalledStickerSetKind(mutation.Kind) || len(mutation.Order) == 0 || len(mutation.Order) > domain.MaxInstalledStickerSets {
			return domain.ErrStickerInvalid
		}
		return validateMemoryUniqueStickerSetIDs(mutation.Order)
	}
	switch mutation.Mutation {
	case domain.UserStickerSetMutationInstall, domain.UserStickerSetMutationUninstall,
		domain.UserStickerSetMutationArchive, domain.UserStickerSetMutationUnarchive:
	default:
		return domain.ErrStickerInvalid
	}
	if len(mutation.Items) == 0 || len(mutation.Items) > domain.MaxInstalledStickerSets {
		return domain.ErrStickerInvalid
	}
	ids := make([]int64, 0, len(mutation.Items))
	for _, item := range mutation.Items {
		if !validMemoryInstalledStickerSetKind(item.Kind) {
			return domain.ErrStickerInvalid
		}
		ids = append(ids, item.StickerSetID)
	}
	return validateMemoryUniqueStickerSetIDs(ids)
}

func validMemoryStickerCollectionKind(kind domain.StickerCollectionKind) bool {
	switch kind {
	case domain.StickerCollectionFaved, domain.StickerCollectionRecent,
		domain.StickerCollectionRecentAttached, domain.StickerCollectionGif:
		return true
	default:
		return false
	}
}

func validMemoryInstalledStickerSetKind(kind domain.StickerSetKind) bool {
	switch kind {
	case domain.StickerSetKindStickers, domain.StickerSetKindMasks, domain.StickerSetKindEmoji:
		return true
	default:
		return false
	}
}

func validateMemoryUniqueStickerSetIDs(ids []int64) error {
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return domain.ErrStickerInvalid
		}
		if _, exists := seen[id]; exists {
			return domain.ErrStickerInvalid
		}
		seen[id] = struct{}{}
	}
	return nil
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
