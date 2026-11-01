package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"telesrv/internal/domain"
)

const MaxContactMutationBatch = 500

var (
	ErrContactMutationInvalid  = errors.New("contact mutation invalid")
	ErrContactMutationRequired = errors.New("contact mutation delivery effects are required")
)

type ContactMutationKind uint8

const (
	ContactMutationInvalid ContactMutationKind = iota
	ContactMutationAdd
	ContactMutationAccept
	ContactMutationImport
	ContactMutationDelete
	ContactMutationNote
)

// ContactMutationPeerSettings is the post-mutation projection for one owner
// and peer. The app computes it from privacy/block read models before the
// aggregate starts; the store only selects entries whose relationship really
// changed while holding the owning transaction locks.
type ContactMutationPeerSettings struct {
	TargetUserID int64
	PeerUserID   int64
	Settings     domain.PeerSettings
}

// ContactMutation is the complete command accepted by the contact aggregate.
// For Accept, OwnerUserID is the actor, ContactUserIDs contains the requested
// peer, and Reciprocal is the peer-owned card that must be inserted atomically.
type ContactMutation struct {
	Kind           ContactMutationKind
	OwnerUserID    int64
	Inputs         []domain.ContactInput
	ContactUserIDs []int64
	Reciprocal     domain.ContactInput
	Note           string
	NoteEntities   []domain.MessageEntity
	Date           int

	PeerSettings      []ContactMutationPeerSettings
	PhonePrivacyRules *domain.PrivacyRules
}

type ContactMutationRequiredEvent struct {
	TargetUserID int64
	Event        domain.UpdateEvent
}

// ContactMutationSnapshot is immutable aggregate output passed to the TL
// delivery builder before commit. Private contact note data is intentionally
// absent from RequiredEvents and therefore cannot enter shared durable update
// payloads.
type ContactMutationSnapshot struct {
	Kind        ContactMutationKind
	OwnerUserID int64
	Contacts    []domain.Contact
	Deleted     int
	Found       bool
	Changed     bool

	RequiredEvents      []ContactMutationRequiredEvent
	PhonePrivacyRules   *domain.PrivacyRules
	PhonePrivacyChanged bool
}

func (m ContactMutation) Validate() error {
	if m.OwnerUserID <= 0 || m.Date <= 0 {
		return fmt.Errorf("%w: owner and date are required", ErrContactMutationInvalid)
	}
	switch m.Kind {
	case ContactMutationAdd:
		if len(m.Inputs) != 1 || len(m.ContactUserIDs) != 0 {
			return fmt.Errorf("%w: add requires one input", ErrContactMutationInvalid)
		}
	case ContactMutationImport:
		if len(m.Inputs) == 0 || len(m.Inputs) > MaxContactMutationBatch || len(m.ContactUserIDs) != 0 {
			return fmt.Errorf("%w: import requires 1..%d inputs", ErrContactMutationInvalid, MaxContactMutationBatch)
		}
	case ContactMutationAccept:
		if len(m.ContactUserIDs) != 1 || len(m.Inputs) != 0 || m.Reciprocal.ContactUserID != m.OwnerUserID {
			return fmt.Errorf("%w: accept requires one peer and reciprocal owner card", ErrContactMutationInvalid)
		}
	case ContactMutationDelete:
		if len(m.ContactUserIDs) == 0 || len(m.ContactUserIDs) > MaxContactMutationBatch || len(m.Inputs) != 0 {
			return fmt.Errorf("%w: delete requires 1..%d peers", ErrContactMutationInvalid, MaxContactMutationBatch)
		}
	case ContactMutationNote:
		if len(m.ContactUserIDs) != 1 || len(m.Inputs) != 0 {
			return fmt.Errorf("%w: note requires one peer", ErrContactMutationInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown kind %d", ErrContactMutationInvalid, m.Kind)
	}

	seen := make(map[int64]struct{}, len(m.Inputs)+len(m.ContactUserIDs))
	for _, input := range m.Inputs {
		if input.ContactUserID <= 0 || input.ContactUserID == m.OwnerUserID {
			return fmt.Errorf("%w: invalid contact user id", ErrContactMutationInvalid)
		}
		if _, ok := seen[input.ContactUserID]; ok {
			return fmt.Errorf("%w: duplicate contact user id %d", ErrContactMutationInvalid, input.ContactUserID)
		}
		seen[input.ContactUserID] = struct{}{}
	}
	for _, id := range m.ContactUserIDs {
		if id <= 0 || id == m.OwnerUserID {
			return fmt.Errorf("%w: invalid contact user id", ErrContactMutationInvalid)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate contact user id %d", ErrContactMutationInvalid, id)
		}
		seen[id] = struct{}{}
	}
	if m.PhonePrivacyRules != nil {
		if m.PhonePrivacyRules.OwnerUserID != m.OwnerUserID || m.PhonePrivacyRules.Key != domain.PrivacyKeyPhoneNumber {
			return fmt.Errorf("%w: phone privacy owner/key mismatch", ErrContactMutationInvalid)
		}
		for _, input := range m.Inputs {
			if input.AddPhonePrivacyException && !privacyRulesAllowUser(*m.PhonePrivacyRules, input.ContactUserID) {
				return fmt.Errorf("%w: phone privacy exception missing user %d", ErrContactMutationInvalid, input.ContactUserID)
			}
		}
	} else {
		for _, input := range m.Inputs {
			if input.AddPhonePrivacyException {
				return fmt.Errorf("%w: phone privacy rules are required", ErrContactMutationInvalid)
			}
		}
	}
	return nil
}

func (m ContactMutation) SettingsFor(targetUserID, peerUserID int64) (domain.PeerSettings, bool) {
	for _, item := range m.PeerSettings {
		if item.TargetUserID == targetUserID && item.PeerUserID == peerUserID {
			return item.Settings, true
		}
	}
	return domain.PeerSettings{}, false
}

func ValidateContactMutationDeliveryEffects(snapshot ContactMutationSnapshot, effects []DeliveryEffect) error {
	expected := len(snapshot.RequiredEvents)
	if snapshot.PhonePrivacyChanged {
		expected++
	}
	if len(effects) != expected {
		return fmt.Errorf("contact mutation delivery requires %d effects, got %d", expected, len(effects))
	}
	for i, required := range snapshot.RequiredEvents {
		effect := effects[i]
		if err := effect.Validate(); err != nil {
			return fmt.Errorf("contact mutation effect %d: %w", i, err)
		}
		if effect.Kind != DeliveryEffectAccountPTS || effect.TargetUserID != required.TargetUserID ||
			effect.ExcludeAuthKeyID != ([8]byte{}) || effect.ExcludeSessionID != 0 ||
			!reflect.DeepEqual(effect.Event, required.Event) {
			return fmt.Errorf("contact mutation effect %d does not match required account event", i)
		}
	}
	if snapshot.PhonePrivacyChanged {
		effect := effects[len(effects)-1]
		if err := effect.Validate(); err != nil {
			return fmt.Errorf("contact privacy delivery effect: %w", err)
		}
		if effect.Kind != DeliveryEffectAbsolute || effect.TargetUserID != snapshot.OwnerUserID ||
			effect.ExcludeAuthKeyID != ([8]byte{}) || effect.ExcludeSessionID != 0 ||
			effect.RecoveryPolicy != OutboxRecoveryAbsoluteReload {
			return fmt.Errorf("contact privacy mutation requires one zero-exclusion absolute owner reload")
		}
	}
	return nil
}

func SortContactMutationRequiredEvents(events []ContactMutationRequiredEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].TargetUserID != events[j].TargetUserID {
			return events[i].TargetUserID < events[j].TargetUserID
		}
		if contactEventRank(events[i].Event.Type) != contactEventRank(events[j].Event.Type) {
			return contactEventRank(events[i].Event.Type) < contactEventRank(events[j].Event.Type)
		}
		return events[i].Event.Peer.ID < events[j].Event.Peer.ID
	})
}

func AppendContactMutationPeerEvent(snapshot *ContactMutationSnapshot, mutation ContactMutation, targetUserID, peerUserID int64) error {
	for _, existing := range snapshot.RequiredEvents {
		if existing.TargetUserID == targetUserID && existing.Event.Type == domain.UpdateEventPeerSettings && existing.Event.Peer.ID == peerUserID {
			return nil
		}
	}
	settings, ok := mutation.SettingsFor(targetUserID, peerUserID)
	if !ok {
		return fmt.Errorf("contact mutation peer settings missing target=%d peer=%d", targetUserID, peerUserID)
	}
	snapshot.RequiredEvents = append(snapshot.RequiredEvents, ContactMutationRequiredEvent{
		TargetUserID: targetUserID,
		Event: domain.UpdateEvent{
			Type: domain.UpdateEventPeerSettings, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peerUserID},
			Settings: settings, Date: mutation.Date, PtsCount: 1,
		},
	})
	return nil
}

func EnsureContactMutationResetEvents(snapshot *ContactMutationSnapshot, date int) {
	owners := make(map[int64]struct{})
	for _, event := range snapshot.RequiredEvents {
		owners[event.TargetUserID] = struct{}{}
	}
	if snapshot.Changed && len(owners) == 0 {
		owners[snapshot.OwnerUserID] = struct{}{}
	}
	for ownerID := range owners {
		snapshot.RequiredEvents = append(snapshot.RequiredEvents, ContactMutationRequiredEvent{
			TargetUserID: ownerID,
			Event:        domain.UpdateEvent{Type: domain.UpdateEventContactsReset, Date: date, PtsCount: 1},
		})
	}
}

func CloneContactMutationSnapshot(in ContactMutationSnapshot) ContactMutationSnapshot {
	out := in
	out.Contacts = append([]domain.Contact(nil), in.Contacts...)
	for i := range out.Contacts {
		out.Contacts[i].NoteEntities = append([]domain.MessageEntity(nil), in.Contacts[i].NoteEntities...)
		out.Contacts[i].User.PhotoStripped = append([]byte(nil), in.Contacts[i].User.PhotoStripped...)
	}
	out.RequiredEvents = append([]ContactMutationRequiredEvent(nil), in.RequiredEvents...)
	if in.PhonePrivacyRules != nil {
		rules := ClonePrivacyRules(*in.PhonePrivacyRules)
		out.PhonePrivacyRules = &rules
	}
	return out
}

// ContactMutationSnapshotForDelivery removes every viewer-owned contact card
// field before invoking the RPC/TL builder. RequiredEvents and the optional
// privacy rule snapshot are sufficient to encode delivery; aliases, phones
// and private notes must never be available to a shared durable payload.
func ContactMutationSnapshotForDelivery(in ContactMutationSnapshot) ContactMutationSnapshot {
	out := CloneContactMutationSnapshot(in)
	out.Contacts = nil
	return out
}

func ClonePrivacyRules(in domain.PrivacyRules) domain.PrivacyRules {
	out := in
	out.Rules = append([]domain.PrivacyRule(nil), in.Rules...)
	for i := range out.Rules {
		out.Rules[i].UserIDs = append([]int64(nil), in.Rules[i].UserIDs...)
		out.Rules[i].ChatIDs = append([]int64(nil), in.Rules[i].ChatIDs...)
	}
	return out
}

func contactEventRank(kind domain.UpdateEventType) int {
	if kind == domain.UpdateEventPeerSettings {
		return 0
	}
	return 1
}

func privacyRulesAllowUser(rules domain.PrivacyRules, userID int64) bool {
	for _, rule := range rules.Rules {
		if rule.Kind != domain.PrivacyRuleAllowUsers {
			continue
		}
		for _, id := range rule.UserIDs {
			if id == userID {
				return true
			}
		}
	}
	return false
}

// ContactStore persists a user's contact list.
type ContactStore interface {
	ListByUser(ctx context.Context, userID int64) (domain.ContactList, error)
	Get(ctx context.Context, userID, contactUserID int64) (domain.Contact, bool, error)
	GetMany(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.Contact, error)
	GetReverseContacts(ctx context.Context, userID int64, ownerUserIDs []int64) (map[int64]domain.Contact, error)
	ContactProjectionForViewers(ctx context.Context, viewerUserIDs, contactUserIDs []int64) (domain.ContactProjectionBatch, error)
	MutateContacts(ctx context.Context, mutation ContactMutation, effects DeliveryEffectsBuilder[ContactMutationSnapshot]) (ContactMutationSnapshot, error)
	SetCloseFriends(ctx context.Context, userID int64, contactUserIDs []int64) (domain.CloseFriendsEditResult, error)
	SetPersonalPhotoWithDelivery(ctx context.Context, viewerUserID, contactUserID, photoID int64, date int, effects DeliveryEffectsBuilder[ContactPersonalPhotoDeliverySnapshot]) (domain.Contact, bool, error)
	PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error)
	ReadBlocklistIDs(ctx context.Context, ownerUserID int64) ([]int64, error)
	MutateBlocklist(ctx context.Context, mutation BlocklistMutation, effects DeliveryEffectsBuilder[BlocklistMutationSnapshot]) (BlocklistMutationSnapshot, error)
	IsBlocked(ctx context.Context, userID, blockedUserID int64) (bool, error)
	ListBlocked(ctx context.Context, userID int64, offset, limit int) (domain.BlockedContactList, error)
}

// SparseContactProjectionStore reads only the explicitly requested
// viewer->contact pairs. Unlike ContactProjectionForViewers, the two dimensions
// are not crossed: a target listed for one viewer is never read for another
// viewer unless that pair is also present in contactUserIDsByViewer.
type SparseContactProjectionStore interface {
	ContactProjectionForViewerUserIDs(ctx context.Context, contactUserIDsByViewer map[int64][]int64) (domain.ContactProjectionBatch, error)
}

// SparseReverseContactStore reads only explicitly requested owner->viewer
// relationship pairs, allowing the bounded batcher to combine requests
// without widening them into a cross product.
type SparseReverseContactStore interface {
	GetReverseContactsForViewerUserIDs(ctx context.Context, viewerUserIDsByOwner map[int64][]int64) (map[int64]map[int64]domain.Contact, error)
}

type ContactPersonalPhotoDeliverySnapshot struct {
	ViewerUserID  int64
	ContactUserID int64
	Contact       domain.Contact
	PhotoID       int64
}

func ValidateContactPersonalPhotoDeliveryEffects(snapshot ContactPersonalPhotoDeliverySnapshot, effects []DeliveryEffect) error {
	if snapshot.ViewerUserID <= 0 || snapshot.ContactUserID <= 0 ||
		snapshot.Contact.User.ID != snapshot.ContactUserID || len(effects) != 1 {
		return fmt.Errorf("contact personal photo delivery requires one viewer-owned effect")
	}
	effect := effects[0]
	if err := effect.Validate(); err != nil {
		return fmt.Errorf("contact personal photo delivery effect: %w", err)
	}
	if effect.Kind != DeliveryEffectAbsolute || effect.TargetUserID != snapshot.ViewerUserID ||
		effect.RecoveryPolicy != OutboxRecoveryAbsoluteReload {
		return fmt.Errorf("contact personal photo delivery requires an absolute viewer reload")
	}
	if (effect.ExcludeAuthKeyID == ([8]byte{})) != (effect.ExcludeSessionID == 0) {
		return fmt.Errorf("contact personal photo delivery exclusion pair is incomplete")
	}
	return nil
}
