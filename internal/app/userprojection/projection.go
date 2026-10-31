package userprojection

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ProfilePhotoProvider returns current profile photos for a batch of owners.
type ProfilePhotoProvider interface {
	CurrentProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64) (map[int64]domain.ProfilePhotoRef, error)
}

// ProfilePhotoKindProvider returns current profile/fallback photos for a batch of owners.
type ProfilePhotoKindProvider interface {
	CurrentProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerIDs []int64, kind domain.ProfilePhotoKind) (map[int64]domain.ProfilePhotoRef, error)
}

// PrivacyEvaluator answers viewer-specific visibility for one user privacy key.
type PrivacyEvaluator interface {
	CanSee(ctx context.Context, ownerUserID, viewerUserID int64, key domain.PrivacyKey) (bool, error)
}

// AccountFreezeProvider returns durable account freeze facts for a bounded
// batch. The projector only exposes them to viewers other than the frozen user.
type AccountFreezeProvider interface {
	AccountFreezes(ctx context.Context, userIDs []int64) (map[int64]domain.AccountFreeze, error)
}

// CollectiblePhoneProvider returns the live collectible number attached to each
// user. It is batched because user vectors are projected on every hot RPC path.
type CollectiblePhoneProvider interface {
	OwnedCollectiblePhones(ctx context.Context, userIDs []int64) (map[int64]domain.CollectiblePhone, error)
}

// BatchPrivacyEvaluator evaluates visibility of multiple owners to a single viewer in a
// batch, eliminating the per-user 3×CanSee N+1 in projectBatch / fan-out projection.
// Optional: an evaluator that implements it (privacy.Service) makes projectBatch prefer
// batch prefetch, otherwise it falls back to per-CanSee. Results must be byte-equivalent to
// per-CanSee.
type BatchPrivacyEvaluator interface {
	CanSeeBatch(ctx context.Context, ownerUserIDs []int64, viewerUserID int64, keys []domain.PrivacyKey) (map[int64]map[domain.PrivacyKey]bool, error)
}

// MatrixPrivacyEvaluator evaluates the owners×viewers×keys visibility matrix in a batch
// (one ListPrivacyRules + one GetMany per owner + in-memory Evaluate), letting ForViewers
// reduce the privacy queries of cross-viewer fan-out projection from O(viewer) to O(owner).
// Optional: privacy.Service implements it, otherwise ForViewers falls back to per-CanSee.
// Results must be byte-equivalent to per-CanSee.
type MatrixPrivacyEvaluator interface {
	CanSeeMatrix(ctx context.Context, ownerUserIDs, viewerUserIDs []int64, keys []domain.PrivacyKey) (map[int64]map[int64]map[domain.PrivacyKey]bool, error)
}

// privacyProjectionKeys are the privacy keys projectBatch projection uses (phone/status/photo).
var privacyProjectionKeys = []domain.PrivacyKey{
	domain.PrivacyKeyPhoneNumber,
	domain.PrivacyKeyStatusTimestamp,
	domain.PrivacyKeyProfilePhoto,
}

// Projector builds the current viewer's user view for RPC response payloads.
// It intentionally stays in app/domain types; tg.* conversion remains in rpc.
type Projector struct {
	contacts store.ContactStore
	photos   ProfilePhotoProvider
	privacy  PrivacyEvaluator
	freezes  AccountFreezeProvider
	phones   CollectiblePhoneProvider
}

// Option configures a Projector.
type Option func(*Projector)

// WithContactStore enables viewer-specific contact name/phone projection.
func WithContactStore(c store.ContactStore) Option {
	return func(p *Projector) { p.contacts = c }
}

// WithPhotoProvider enables current profile photo enrichment.
func WithPhotoProvider(photos ProfilePhotoProvider) Option {
	return func(p *Projector) { p.photos = photos }
}

// WithPrivacyEvaluator enables profile/photo/status privacy projection.
func WithPrivacyEvaluator(privacy PrivacyEvaluator) Option {
	return func(p *Projector) { p.privacy = privacy }
}

// WithAccountFreezeProvider enables viewer-scoped frozen-account visibility.
func WithAccountFreezeProvider(provider AccountFreezeProvider) Option {
	return func(p *Projector) { p.freezes = provider }
}

func WithCollectiblePhoneProvider(provider CollectiblePhoneProvider) Option {
	return func(p *Projector) { p.phones = provider }
}

// New creates a user projector.
func New(opts ...Option) *Projector {
	p := &Projector{}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ForViewer applies both current profile photos and owner-specific contact view.
func (p *Projector) ForViewer(ctx context.Context, viewerUserID int64, users []domain.User) ([]domain.User, error) {
	users = sanitizeDeletedUsers(users)
	if p == nil {
		return users, nil
	}
	return projectBatch(ctx, p.contacts, p.photos, p.privacy, p.freezes, p.phones, viewerUserID, users)
}

// One applies ForViewer to a single user.
func (p *Projector) One(ctx context.Context, viewerUserID int64, user domain.User) (domain.User, error) {
	if p == nil {
		return user, nil
	}
	projected, err := p.ForViewer(ctx, viewerUserID, []domain.User{user})
	if err != nil || len(projected) == 0 {
		return domain.User{}, err
	}
	return projected[0], nil
}

// ForViewers projects the same set of owner users in a batch across multiple viewers
// (fan-out templating). It compresses the cost of running ForViewer (=projectBatch) once per
// viewer (O(viewer)×(photos+contacts+privacy) queries) into:
//   - one profile/fallback photo batch (reused across viewers)
//   - one viewer-owned contact projection (contact rename/phone override + personal photo overlay)
//   - O(owner) GetMany calls + one ListPrivacyRules (done inside CanSeeMatrix)
//
// It returns map[viewerID][]domain.User, each slice byte-equivalent to that viewer's
// ForViewer(viewer, users). The caller's users are never modified (copied internally).
func (p *Projector) ForViewers(ctx context.Context, viewerUserIDs []int64, users []domain.User) (map[int64][]domain.User, error) {
	users = sanitizeDeletedUsers(users)
	out := make(map[int64][]domain.User, len(viewerUserIDs))
	if p == nil || len(users) == 0 {
		for _, v := range viewerUserIDs {
			out[v] = cloneUsers(users)
		}
		return out, nil
	}
	viewers := dedupNonZeroInt64(viewerUserIDs)
	if len(viewers) == 0 {
		for _, v := range viewerUserIDs {
			out[v] = cloneUsers(users)
		}
		return out, nil
	}
	ids := uniqueUserIDs(users)

	// The three prefetch groups are independent (shared photos, viewer-owned contact projection, privacy matrix), so they run concurrently in a single wave.
	var (
		profileRefs          map[int64]domain.ProfilePhotoRef
		fallbackRefs         map[int64]domain.ProfilePhotoRef
		contactsByViewer     map[int64]map[int64]domain.Contact
		personalRefsByViewer map[int64]map[int64]domain.ProfilePhotoRef
		matrix               map[int64]map[int64]map[domain.PrivacyKey]bool
		freezes              map[int64]domain.AccountFreeze
		collectiblePhones    map[int64]domain.CollectiblePhone
	)
	g, gctx := errgroup.WithContext(ctx)
	// 1) Shared photos: one profile/fallback batch, reused across all viewers.
	g.Go(func() error {
		var err error
		profileRefs, fallbackRefs, err = p.batchProfileFallbackPhotos(gctx, ids)
		return err
	})
	// 2) Rename/phone override + personal photo: read the contact rows the viewer owns in a
	//    batch, matching the same semantics as projectBatch's GetMany/PersonalPhotos(viewer, owners).
	g.Go(func() error {
		if p.contacts == nil || len(ids) == 0 || len(viewers) == 0 {
			return nil
		}
		batch, err := p.contacts.ContactProjectionForViewers(gctx, viewers, ids)
		if err != nil {
			return err
		}
		contactsByViewer = batch.Contacts
		personalRefsByViewer = batch.PersonalPhotos
		return err
	})
	// 3) privacy visibility matrix: O(owner) queries; when nil (no MatrixPrivacyEvaluator), applyPrivacy falls back to per-CanSee.
	if me, ok := p.privacy.(MatrixPrivacyEvaluator); ok && p.privacy != nil {
		g.Go(func() error {
			var err error
			matrix, err = me.CanSeeMatrix(gctx, ids, viewers, privacyProjectionKeys)
			return err
		})
	}
	if p.freezes != nil && len(ids) > 0 {
		g.Go(func() error {
			var err error
			freezes, err = p.freezes.AccountFreezes(gctx, ids)
			return err
		})
	}
	if p.phones != nil && len(ids) > 0 {
		g.Go(func() error {
			var err error
			collectiblePhones, err = p.phones.OwnedCollectiblePhones(gctx, ids)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	// 4) Assemble per viewer, reusing the exact same apply* chain as projectBatch.
	for _, viewer := range viewers {
		personalRefs := personalRefsByViewer[viewer]
		projected := cloneUsers(users)
		cache := make(map[int64]domain.User, len(projected))
		for i := range projected {
			u := projected[i]
			if u.ID == 0 {
				continue
			}
			if u.Deleted {
				projected[i] = u.DeletedTombstone()
				continue
			}
			if pj, ok := cache[u.ID]; ok {
				projected[i] = pj
				continue
			}
			pj := applyBasePhotos(u, profileRefs, fallbackRefs, personalRefs, viewer)
			if viewer != 0 && u.ID != viewer && u.ID != domain.OfficialSystemUserID && !u.Bot {
				contact, found := contactsByViewer[viewer][u.ID]
				pj = applyContactProjection(pj, contact, found)
				pj = applyCollectiblePhone(pj, collectiblePhones[u.ID])
				var vis map[domain.PrivacyKey]bool
				if matrix != nil {
					vis = matrix[u.ID][viewer]
				}
				var perr error
				hasKnownContactPhone := found && contact.Phone != ""
				pj, perr = applyPrivacy(ctx, p.privacy, viewer, pj, hasKnownContactPhone, vis, profileRefs, fallbackRefs, personalRefs)
				if perr != nil {
					return nil, perr
				}
			}
			if viewer == 0 || u.ID == viewer || u.ID == domain.OfficialSystemUserID || u.Bot {
				pj = applyCollectiblePhone(pj, collectiblePhones[u.ID])
			}
			pj = reapplyExclusiveCollectiblePhone(pj, collectiblePhones[u.ID])
			pj = applyAccountFreezeProjection(pj, viewer, freezes[u.ID])
			cache[u.ID] = pj
			projected[i] = pj
		}
		out[viewer] = projected
	}
	return out, nil
}

// batchProfileFallbackPhotos fetches owners' profile/fallback photos (same logic as
// projectBatch). When photos is nil it returns empty maps (applyBasePhotos treats it as no
// photo lookups).
func (p *Projector) batchProfileFallbackPhotos(ctx context.Context, ids []int64) (profileRefs, fallbackRefs map[int64]domain.ProfilePhotoRef, err error) {
	profileRefs = map[int64]domain.ProfilePhotoRef{}
	fallbackRefs = map[int64]domain.ProfilePhotoRef{}
	if p.photos == nil || len(ids) == 0 {
		return profileRefs, fallbackRefs, nil
	}
	if kindPhotos, ok := p.photos.(ProfilePhotoKindProvider); ok {
		refs, err := kindPhotos.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, ids, domain.ProfilePhotoKindProfile)
		if err != nil {
			return nil, nil, err
		}
		profileRefs = refs
		refs, err = kindPhotos.CurrentProfilePhotosKind(ctx, domain.PeerTypeUser, ids, domain.ProfilePhotoKindFallback)
		if err != nil {
			return nil, nil, err
		}
		fallbackRefs = refs
		return profileRefs, fallbackRefs, nil
	}
	refs, err := p.photos.CurrentProfilePhotos(ctx, domain.PeerTypeUser, ids)
	if err != nil {
		return nil, nil, err
	}
	return refs, fallbackRefs, nil
}

func cloneUsers(users []domain.User) []domain.User {
	if len(users) == 0 {
		return nil
	}
	out := make([]domain.User, len(users))
	copy(out, users)
	for i := range out {
		out[i].PhotoStripped = append([]byte(nil), out[i].PhotoStripped...)
		out[i].ContactNoteEntities = append([]domain.MessageEntity(nil), out[i].ContactNoteEntities...)
		out[i].RestrictionReasons = append([]domain.UserRestrictionReason(nil), out[i].RestrictionReasons...)
	}
	return out
}

func dedupNonZeroInt64(ids []int64) []int64 {
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

// WithProfilePhotos enriches users with their current avatar from profile photo storage.
// The lookup is best-effort: a storage error keeps the original user list.
func WithProfilePhotos(ctx context.Context, photos ProfilePhotoProvider, users []domain.User) []domain.User {
	users = sanitizeDeletedUsers(users)
	if photos == nil || len(users) == 0 {
		return users
	}
	ids := make([]int64, 0, len(users))
	seen := make(map[int64]struct{}, len(users))
	for _, u := range users {
		if u.ID == 0 || u.Deleted {
			continue
		}
		if _, ok := seen[u.ID]; ok {
			continue
		}
		seen[u.ID] = struct{}{}
		ids = append(ids, u.ID)
	}
	if len(ids) == 0 {
		return users
	}
	refs, err := photos.CurrentProfilePhotos(ctx, domain.PeerTypeUser, ids)
	if err != nil || len(refs) == 0 {
		return users
	}
	out := cloneUsers(users)
	for i := range out {
		if ref, ok := refs[out[i].ID]; ok {
			applyPhotoRef(&out[i], ref)
		}
	}
	return out
}

// ForViewer applies the owner-specific user view that Telegram clients expect.
// A contact relationship alone never grants phone visibility. A viewer may retain
// an owner-scoped phone it explicitly supplied, while the target account phone is
// governed by PhoneNumber privacy.
func ForViewer(ctx context.Context, contacts store.ContactStore, viewerUserID int64, users []domain.User) ([]domain.User, error) {
	users = sanitizeDeletedUsers(users)
	if contacts == nil || viewerUserID == 0 || len(users) == 0 {
		return users, nil
	}
	out := cloneUsers(users)
	cache := make(map[int64]domain.User, len(users))
	for i := range out {
		u := out[i]
		if u.ID == 0 || u.Deleted || u.ID == viewerUserID || u.ID == domain.OfficialSystemUserID || u.Bot {
			continue
		}
		if projected, ok := cache[u.ID]; ok {
			out[i] = projected
			continue
		}
		projected, err := projectOne(ctx, contacts, viewerUserID, u)
		if err != nil {
			return nil, err
		}
		cache[u.ID] = projected
		out[i] = projected
	}
	return out, nil
}

// One applies ForViewer to a single user.
func One(ctx context.Context, contacts store.ContactStore, viewerUserID int64, user domain.User) (domain.User, error) {
	projected, err := ForViewer(ctx, contacts, viewerUserID, []domain.User{user})
	if err != nil || len(projected) == 0 {
		return domain.User{}, err
	}
	return projected[0], nil
}

func projectBatch(ctx context.Context, contacts store.ContactStore, photos ProfilePhotoProvider, privacy PrivacyEvaluator, freezesProvider AccountFreezeProvider, phoneProvider CollectiblePhoneProvider, viewerUserID int64, users []domain.User) ([]domain.User, error) {
	if len(users) == 0 {
		return users, nil
	}
	out := cloneUsers(users)
	out = sanitizeDeletedUsers(out)
	ids := uniqueUserIDs(out)
	var (
		profileRefs       = map[int64]domain.ProfilePhotoRef{}
		fallbackRefs      = map[int64]domain.ProfilePhotoRef{}
		personalRefs      = map[int64]domain.ProfilePhotoRef{}
		contactsByID      map[int64]domain.Contact
		visibility        map[int64]map[domain.PrivacyKey]bool
		freezes           map[int64]domain.AccountFreeze
		collectiblePhones map[int64]domain.CollectiblePhone
	)
	// These prefetch queries are independent (photo profile/fallback, contact
	// GetMany/PersonalPhotos, privacy visibility); running them concurrently collapses ~6
	// serial round-trips into one wave. Each goroutine writes only its own variable and the
	// assembly loop runs serially after Wait (pure memory, no queries), so there is no data race.
	g, gctx := errgroup.WithContext(ctx)
	if photos != nil && len(ids) > 0 {
		if kindPhotos, ok := photos.(ProfilePhotoKindProvider); ok {
			g.Go(func() error {
				refs, err := kindPhotos.CurrentProfilePhotosKind(gctx, domain.PeerTypeUser, ids, domain.ProfilePhotoKindProfile)
				if err != nil {
					return err
				}
				profileRefs = refs
				return nil
			})
			g.Go(func() error {
				refs, err := kindPhotos.CurrentProfilePhotosKind(gctx, domain.PeerTypeUser, ids, domain.ProfilePhotoKindFallback)
				if err != nil {
					return err
				}
				fallbackRefs = refs
				return nil
			})
		} else {
			g.Go(func() error {
				refs, err := photos.CurrentProfilePhotos(gctx, domain.PeerTypeUser, ids)
				if err != nil {
					return err
				}
				profileRefs = refs
				return nil
			})
		}
	}
	if contacts != nil && viewerUserID != 0 && len(ids) > 0 {
		g.Go(func() error {
			m, err := contacts.GetMany(gctx, viewerUserID, ids)
			if err != nil {
				return err
			}
			contactsByID = m
			return nil
		})
		g.Go(func() error {
			refs, err := contacts.PersonalPhotos(gctx, viewerUserID, ids)
			if err != nil {
				return err
			}
			personalRefs = refs
			return nil
		})
	}
	// Batch-prefetch privacy visibility (if the evaluator supports it): reduces the per-user
	// 3×CanSee×2-row N+1 to one ListPrivacyRules + one GetReverseContacts + in-memory
	// Evaluate; when nil, applyPrivacy falls back to per-CanSee.
	g.Go(func() error {
		v, err := prefetchPrivacyVisibility(gctx, privacy, viewerUserID, out)
		if err != nil {
			return err
		}
		visibility = v
		return nil
	})
	if freezesProvider != nil && len(ids) > 0 {
		g.Go(func() error {
			m, err := freezesProvider.AccountFreezes(gctx, ids)
			if err != nil {
				return err
			}
			freezes = m
			return nil
		})
	}
	if phoneProvider != nil && len(ids) > 0 {
		g.Go(func() error {
			var err error
			collectiblePhones, err = phoneProvider.OwnedCollectiblePhones(gctx, ids)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	cache := make(map[int64]domain.User, len(out))
	for i := range out {
		u := out[i]
		if u.ID == 0 {
			continue
		}
		if u.Deleted {
			out[i] = u.DeletedTombstone()
			continue
		}
		if projected, ok := cache[u.ID]; ok {
			out[i] = projected
			continue
		}
		projected := applyBasePhotos(u, profileRefs, fallbackRefs, personalRefs, viewerUserID)
		// Bots and the system account are exempt from contact/privacy projection: official
		// bots have no phone/last seen, and do not take part in contact rename or privacy trimming.
		if viewerUserID != 0 && u.ID != viewerUserID && u.ID != domain.OfficialSystemUserID && !u.Bot {
			contact, found := contactsByID[u.ID]
			projected = applyContactProjection(projected, contact, found)
			projected = applyCollectiblePhone(projected, collectiblePhones[u.ID])
			hasKnownContactPhone := found && contact.Phone != ""
			var err error
			projected, err = applyPrivacy(ctx, privacy, viewerUserID, projected, hasKnownContactPhone, visibility[u.ID], profileRefs, fallbackRefs, personalRefs)
			if err != nil {
				return nil, err
			}
		}
		if viewerUserID == 0 || u.ID == viewerUserID || u.ID == domain.OfficialSystemUserID || u.Bot {
			projected = applyCollectiblePhone(projected, collectiblePhones[u.ID])
		}
		projected = reapplyExclusiveCollectiblePhone(projected, collectiblePhones[u.ID])
		projected = applyAccountFreezeProjection(projected, viewerUserID, freezes[u.ID])
		cache[u.ID] = projected
		out[i] = projected
	}
	return out, nil
}

func applyCollectiblePhone(user domain.User, phone domain.CollectiblePhone) domain.User {
	if !user.Deleted && !user.Bot && phone.Owned() && phone.OwnerUserID == user.ID {
		user.Phone = phone.Phone
	}
	return user
}

func reapplyExclusiveCollectiblePhone(user domain.User, phone domain.CollectiblePhone) domain.User {
	if phone.AlwaysVisible() && !user.Deleted && !user.Bot && phone.OwnerUserID == user.ID {
		user.Phone = phone.Phone
	}
	return user
}

func applyAccountFreezeProjection(user domain.User, viewerUserID int64, freeze domain.AccountFreeze) domain.User {
	// Frozen accounts are presented to peers as deleted accounts: the client
	// renders "Deleted account" instead of the frozen persona. The owner and
	// unauthenticated/system views keep the live account. The tombstone carries
	// FrozenForViewer so the RPC boundary can attach the synthetic "frozen"
	// third-party mark alongside the deleted presentation.
	if freeze.Frozen && viewerUserID != 0 && user.ID != 0 && user.ID != viewerUserID && user.ID != domain.OfficialSystemUserID {
		user.Deleted = true
		tombstone := user.DeletedTombstone()
		tombstone.FrozenForViewer = true
		return tombstone
	}
	if freeze.Frozen {
		user.Status = domain.UserStatus{Kind: domain.UserStatusEmpty}
		user.LastSeenAt = 0
	}
	user.RestrictionReasons = nil
	return user
}

func prefetchPrivacyVisibility(ctx context.Context, privacy PrivacyEvaluator, viewerUserID int64, users []domain.User) (map[int64]map[domain.PrivacyKey]bool, error) {
	if privacy == nil || viewerUserID == 0 {
		return nil, nil
	}
	batch, ok := privacy.(BatchPrivacyEvaluator)
	if !ok {
		return nil, nil
	}
	ids := make([]int64, 0, len(users))
	seen := make(map[int64]struct{}, len(users))
	for _, u := range users {
		if u.ID == 0 || u.Deleted || u.ID == viewerUserID || u.ID == domain.OfficialSystemUserID || u.Bot {
			continue
		}
		if _, ok := seen[u.ID]; ok {
			continue
		}
		seen[u.ID] = struct{}{}
		ids = append(ids, u.ID)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return batch.CanSeeBatch(ctx, ids, viewerUserID, privacyProjectionKeys)
}

func projectOne(ctx context.Context, contacts store.ContactStore, viewerUserID int64, user domain.User) (domain.User, error) {
	if user.Deleted {
		return user.DeletedTombstone(), nil
	}
	contact, found, err := contacts.Get(ctx, viewerUserID, user.ID)
	if err != nil {
		return domain.User{}, err
	}
	return applyContactProjection(user, contact, found), nil
}

func uniqueUserIDs(users []domain.User) []int64 {
	seen := make(map[int64]struct{}, len(users))
	ids := make([]int64, 0, len(users))
	for _, user := range users {
		if user.ID == 0 || user.Deleted {
			continue
		}
		if _, ok := seen[user.ID]; ok {
			continue
		}
		seen[user.ID] = struct{}{}
		ids = append(ids, user.ID)
	}
	return ids
}

func applyBasePhotos(user domain.User, profileRefs, fallbackRefs, personalRefs map[int64]domain.ProfilePhotoRef, viewerUserID int64) domain.User {
	if user.Deleted {
		return user.DeletedTombstone()
	}
	if !hasPhotoLookups(profileRefs, fallbackRefs, personalRefs) {
		return user
	}
	clearPhoto(&user)
	if viewerUserID != 0 && user.ID != viewerUserID {
		if ref, ok := personalRefs[user.ID]; ok && ref.PhotoID != 0 {
			ref.Personal = true
			applyPhotoRef(&user, ref)
			return user
		}
	}
	if ref, ok := profileRefs[user.ID]; ok && ref.PhotoID != 0 {
		applyPhotoRef(&user, ref)
		return user
	}
	if ref, ok := fallbackRefs[user.ID]; ok && ref.PhotoID != 0 {
		applyPhotoRef(&user, ref)
	}
	return user
}

func applyContactProjection(user domain.User, contact domain.Contact, found bool) domain.User {
	if user.Deleted {
		return user.DeletedTombstone()
	}
	if !found {
		// A contact relationship alone never grants phone visibility; the target
		// account phone is governed by PhoneNumber privacy (applyPrivacy decides
		// whether to strip it — never wipe here so AllowAll still surfaces it).
		user.Contact = false
		user.Mutual = false
		user.CloseFriend = false
		user.ContactNote = ""
		user.ContactNoteEntities = nil
		return user
	}
	user.Contact = true
	user.Mutual = contact.Mutual || contact.User.Mutual
	user.CloseFriend = contact.CloseFriend || contact.User.CloseFriend
	user.ContactNote = contact.Note
	user.ContactNoteEntities = append([]domain.MessageEntity(nil), contact.NoteEntities...)
	// contact.Phone is an owner-local fact supplied by this viewer. It may differ
	// from the target's current account phone and is safe to preserve because the
	// viewer already knew it. An empty contact.Phone must not replace or authorize
	// the target account phone carried by user.Phone.
	if contact.Phone != "" {
		user.Phone = contact.Phone
	}
	if contact.FirstName != "" || contact.LastName != "" {
		// FirstName uses NULLIF at the durable read boundary while LastName is an
		// explicit owner-local value. Preserve the base first name when only a
		// local last name exists; setting a local first name with an empty last
		// name intentionally clears the base last name.
		if contact.FirstName != "" {
			user.FirstName = contact.FirstName
			user.LastName = contact.LastName
		} else {
			user.LastName = contact.LastName
		}
	} else if contact.User.FirstName != "" || contact.User.LastName != "" {
		user.FirstName = contact.User.FirstName
		user.LastName = contact.User.LastName
	}
	return user
}

func applyPrivacy(ctx context.Context, privacy PrivacyEvaluator, viewerUserID int64, user domain.User, hasKnownContactPhone bool, vis map[domain.PrivacyKey]bool, profileRefs, fallbackRefs, personalRefs map[int64]domain.ProfilePhotoRef) (domain.User, error) {
	if user.Deleted {
		return user.DeletedTombstone(), nil
	}
	if privacy == nil {
		// Missing privacy wiring must fail closed for an account phone. The only
		// safe exception is an owner-scoped phone the viewer explicitly supplied.
		if !hasKnownContactPhone {
			user.Phone = ""
		}
		return user, nil
	}
	// vis is the batch-prefetch result (computed by projectBatch via one ListPrivacyRules +
	// GetReverseContacts); when nil it falls back to per-CanSee, and the two are equivalent.
	canSee := func(key domain.PrivacyKey) (bool, error) {
		if vis != nil {
			return vis[key], nil
		}
		return privacy.CanSee(ctx, user.ID, viewerUserID, key)
	}
	phoneAllowed, err := canSee(domain.PrivacyKeyPhoneNumber)
	if err != nil {
		return domain.User{}, err
	}
	if !phoneAllowed && !hasKnownContactPhone {
		user.Phone = ""
	}
	statusAllowed, err := canSee(domain.PrivacyKeyStatusTimestamp)
	if err != nil {
		return domain.User{}, err
	}
	if !statusAllowed {
		user.Status = domain.ApproximateUserStatus(user.LastSeenAt, int(time.Now().Unix()))
		user.LastSeenAt = 0
	}
	if ref, ok := personalRefs[user.ID]; ok && ref.PhotoID != 0 {
		ref.Personal = true
		applyPhotoRef(&user, ref)
		return user, nil
	}
	if !hasPhotoLookups(profileRefs, fallbackRefs, personalRefs) && user.PhotoID == 0 {
		return user, nil
	}
	profileAllowed, err := canSee(domain.PrivacyKeyProfilePhoto)
	if err != nil {
		return domain.User{}, err
	}
	if profileAllowed {
		if ref, ok := profileRefs[user.ID]; ok && ref.PhotoID != 0 {
			applyPhotoRef(&user, ref)
			return user, nil
		}
	}
	if ref, ok := fallbackRefs[user.ID]; ok && ref.PhotoID != 0 {
		applyPhotoRef(&user, ref)
		return user, nil
	}
	clearPhoto(&user)
	return user, nil
}

func hasPhotoLookups(profileRefs, fallbackRefs, personalRefs map[int64]domain.ProfilePhotoRef) bool {
	return len(profileRefs) != 0 || len(fallbackRefs) != 0 || len(personalRefs) != 0
}

func applyPhotoRef(user *domain.User, ref domain.ProfilePhotoRef) {
	user.PhotoID = ref.PhotoID
	user.PhotoDCID = ref.DCID
	user.PhotoStripped = append([]byte(nil), ref.Stripped...)
	user.PhotoPersonal = ref.Personal
	user.PhotoHasVideo = ref.HasVideo
}

func clearPhoto(user *domain.User) {
	user.PhotoID = 0
	user.PhotoDCID = 0
	user.PhotoStripped = nil
	user.PhotoPersonal = false
	user.PhotoHasVideo = false
}

func sanitizeDeletedUsers(users []domain.User) []domain.User {
	var out []domain.User
	for i, user := range users {
		if !user.Deleted {
			continue
		}
		if out == nil {
			out = make([]domain.User, len(users))
			copy(out, users)
		}
		out[i] = user.DeletedTombstone()
	}
	if out != nil {
		return out
	}
	return users
}
