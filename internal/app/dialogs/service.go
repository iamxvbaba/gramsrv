package dialogs

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"reflect"
	"sort"
	"time"
	"unicode/utf8"

	"telesrv/internal/app/userprojection"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PremiumChecker reports whether the user currently has an active Premium membership (used for the two-tier pin limit decision).
type PremiumChecker func(ctx context.Context, userID int64) bool

// Service provides dialog list queries.
type Service struct {
	dialogs          store.DialogStore
	channels         store.ChannelStore
	contacts         store.ContactStore
	photos           userprojection.ProfilePhotoProvider
	privacy          userprojection.PrivacyEvaluator
	freezes          userprojection.AccountFreezeProvider
	phones           userprojection.CollectiblePhoneProvider
	premium          PremiumChecker
	projector        *userprojection.Projector
	versions         store.ReadModelVersionStore
	privatePeerCache *dialogPeerReadModelCache
	draftCache       *dialogDraftReadModelCache
	listHashCache    *dialogListHashCache
	listCache        *dialogListSnapshotCache
	sharedListCache  store.DialogListSnapshotCache
}

// Option adjusts optional dialogs service dependencies.
type Option func(*Service)

// WithContactStore enables viewer-specific user projection for dialog users.
func WithContactStore(c store.ContactStore) Option {
	return func(s *Service) { s.contacts = c }
}

// WithPremiumChecker enables the Premium two-tier pin limit (defaults to the default tier otherwise).
func WithPremiumChecker(p PremiumChecker) Option {
	return func(s *Service) { s.premium = p }
}

// WithPhotoProvider enables current profile photo enrichment for dialog users.
func WithPhotoProvider(p userprojection.ProfilePhotoProvider) Option {
	return func(s *Service) { s.photos = p }
}

// WithPrivacyEvaluator enables viewer-specific privacy projection for dialog users.
func WithPrivacyEvaluator(p userprojection.PrivacyEvaluator) Option {
	return func(s *Service) { s.privacy = p }
}

func WithAccountFreezeProvider(p userprojection.AccountFreezeProvider) Option {
	return func(s *Service) { s.freezes = p }
}

func WithCollectiblePhoneProvider(p userprojection.CollectiblePhoneProvider) Option {
	return func(s *Service) { s.phones = p }
}

// WithReadModelVersions enables durable version-token backed peer dialog caching.
func WithReadModelVersions(v store.ReadModelVersionStore) Option {
	return func(s *Service) { s.versions = v }
}

// WithDialogHydrationCaches configures the bounded structural-private-peer and
// cloud-draft working sets. Channel structure has its own store-level cache and
// is deliberately not duplicated here.
func WithDialogHydrationCaches(privateMaxEntries int, privateMaxBytes int64, draftMaxEntries int, draftMaxBytes int64) Option {
	return func(s *Service) {
		s.privatePeerCache = newDialogPeerReadModelCacheWithLimits(privateMaxEntries, privateMaxBytes, defaultDialogPeerReadModelTTL)
		s.draftCache = newDialogDraftReadModelCache(draftMaxEntries, draftMaxBytes, defaultDialogDraftReadModelTTL)
	}
}

// WithDialogListSnapshotCache configures the bounded materialized owner working
// set. maxHeaders is retained as the configuration/API name and measures
// header-equivalent weighted units, so high-membership owners cannot turn
// maxEntries into an unbounded heap commitment.
func WithDialogListSnapshotCache(maxEntries int, maxHeaders int64, ttl time.Duration) Option {
	return func(s *Service) {
		s.listCache = newDialogListSnapshotCache(maxEntries, maxHeaders, ttl)
	}
}

// WithSharedDialogListSnapshotCache installs the production Redis L2 for
// process-cold materialized owner restoration. Errors are propagated; production
// never silently replaces Redis failure with a full PostgreSQL scan.
func WithSharedDialogListSnapshotCache(cache store.DialogListSnapshotCache) Option {
	return func(s *Service) { s.sharedListCache = cache }
}

// NewService creates the dialogs service.
func NewService(dialogs store.DialogStore, channels ...store.ChannelStore) *Service {
	s := &Service{
		dialogs:          dialogs,
		privatePeerCache: newDialogPeerReadModelCache(defaultDialogPeerReadModelTTL),
		draftCache:       newDialogDraftReadModelCache(defaultDialogDraftReadModelMaxEntries, defaultDialogDraftReadModelMaxBytes, defaultDialogDraftReadModelTTL),
		listHashCache:    newDialogListHashCache(defaultDialogListHashCacheTTL),
		listCache:        newDialogListSnapshotCache(0, 0, 0),
	}
	if len(channels) > 0 {
		s.channels = channels[0]
	}
	s.rebuildProjector()
	return s
}

// Configure applies optional dependencies after construction.
func (s *Service) Configure(opts ...Option) *Service {
	if s == nil {
		return s
	}
	for _, opt := range opts {
		opt(s)
	}
	s.rebuildProjector()
	return s
}

func (s *Service) rebuildProjector() {
	if s == nil {
		return
	}
	s.projector = userprojection.New(
		userprojection.WithContactStore(s.contacts),
		userprojection.WithPhotoProvider(s.photos),
		userprojection.WithPrivacyEvaluator(s.privacy),
		userprojection.WithAccountFreezeProvider(s.freezes),
		userprojection.WithCollectiblePhoneProvider(s.phones),
	)
}

// GetDialogs returns the dialog summary of the currently logged-in account. When not logged in or with no durable implementation it is treated as an empty account.
func (s *Service) GetDialogs(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error) {
	return s.getDialogs(ctx, userID, filter, false)
}

// getDialogs is the implementation of GetDialogs. lightweight=true skips draft attachment,
// viewer projection and list-hash write-back, for attachArchiveSummary to take the top archived
// dialog: the archive summary needs only the top peer/message, so drafts are meaningless; and
// the appended archive users are uniformly projected by the outer GetDialogs via
// projectDialogUsers, so projecting again inside is pure duplication (the original archive
// recursion running the full GetDialogs would run ListDrafts + a projection an extra time).
func (s *Service) getDialogs(ctx context.Context, userID int64, filter domain.DialogFilter, lightweight bool) (domain.DialogList, error) {
	if s == nil || userID == 0 {
		return domain.DialogList{}, nil
	}
	if filter.HasFolderID && filter.FolderID >= domain.DialogCustomFolderMinID && filter.Folder == nil {
		if s.dialogs == nil {
			return domain.DialogList{}, nil
		}
		folder, found, err := s.dialogs.GetFolder(ctx, userID, filter.FolderID)
		if err != nil {
			return domain.DialogList{}, err
		}
		if !found {
			return domain.DialogList{}, nil
		}
		filter.Folder = &folder
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 100
	}
	if !lightweight {
		if key, ok := dialogSnapshotKey(userID, filter); ok && s.supportsDialogListSnapshot() {
			listHashEpoch := s.listHashCache.cacheEpoch()
			if s.sharedListCache != nil {
				page, err := s.stableDialogSnapshotPage(ctx, key, filter)
				if err != nil {
					return domain.DialogList{}, err
				}
				s.rememberDialogListHash(userID, filter, page, listHashEpoch)
				return page, nil
			}
			snap, err := s.listCache.getOrLoad(ctx, key, func() (*dialogListSnapshot, error) {
				return s.loadDialogListSnapshot(ctx, key)
			})
			if err != nil {
				return domain.DialogList{}, err
			}
			page, err := s.hydrateDialogSnapshotPage(ctx, userID, dialogListSnapshotPageHeaders(snap, filter))
			if err != nil {
				return domain.DialogList{}, err
			}
			s.rememberDialogListHash(userID, filter, page, listHashEpoch)
			return page, nil
		}
	}
	return s.loadDialogs(ctx, userID, filter, lightweight)
}

const dialogListSnapshotStableReadAttempts = 4

func (s *Service) stableDialogSnapshotPage(
	ctx context.Context,
	key dialogListSnapshotKey,
	filter domain.DialogFilter,
) (domain.DialogList, error) {
	for attempt := 0; attempt < dialogListSnapshotStableReadAttempts; attempt++ {
		ownerHash, err := s.dialogOwnerHash(ctx, key.userID)
		if err != nil {
			return domain.DialogList{}, err
		}
		snap, err := s.listCache.getOrLoadVersioned(ctx, key, ownerHash, func() (*dialogListSnapshot, error) {
			return s.loadDialogListSnapshotAtOwnerHash(ctx, key, ownerHash)
		})
		if errors.Is(err, errDialogListSnapshotGenerationChanged) {
			continue
		}
		if err != nil {
			return domain.DialogList{}, err
		}

		page, hydrateErr := s.hydrateDialogSnapshotPage(ctx, key.userID, dialogListSnapshotPageHeaders(snap, filter))
		currentOwnerHash, hashErr := s.dialogOwnerHash(ctx, key.userID)
		if hashErr != nil {
			return domain.DialogList{}, hashErr
		}
		if currentOwnerHash != ownerHash {
			continue
		}
		if hydrateErr != nil {
			return domain.DialogList{}, hydrateErr
		}
		return page, nil
	}
	return domain.DialogList{}, errDialogListSnapshotGenerationChanged
}

type dialogListSnapshotStore interface {
	ListAllBuiltinDialogSnapshotHeaders(context.Context, int64) (domain.DialogList, error)
}

type channelDialogListSnapshotStore interface {
	ListAllBuiltinChannelDialogSnapshot(context.Context, int64) (domain.ChannelDialogList, error)
}

type channelDialogSnapshotHydrator interface {
	HydrateChannelDialogSnapshot(context.Context, int64, []domain.Dialog) (domain.ChannelDialogList, error)
}

type privateDialogPeerIDStore interface {
	ListPrivateDialogPeerIDs(context.Context, int64, int) ([]int64, error)
}

// PrivateDialogPeerIDs returns the bounded private-peer candidate set used by
// transient presence fan-out without entering the full dialogs projection.
// A process-cold server first tries the version-addressed shared owner snapshot:
// its private dialog headers are fully covered by dialog_owner and can be
// sorted into the same narrow result without another PostgreSQL acquisition.
func (s *Service) PrivateDialogPeerIDs(ctx context.Context, userID int64, limit int) ([]int64, error) {
	if s == nil || s.dialogs == nil || userID == 0 {
		return nil, nil
	}
	privateStore, ok := s.dialogs.(privateDialogPeerIDStore)
	if !ok {
		return nil, errors.New("dialog store does not provide private peer candidates")
	}
	if limit <= 0 || limit > 4096 {
		limit = 4096
	}
	if s.sharedListCache == nil || s.versions == nil {
		return privateStore.ListPrivateDialogPeerIDs(ctx, userID, limit)
	}
	for attempt := 0; attempt < dialogListSnapshotStableReadAttempts; attempt++ {
		ownerHash, err := s.dialogOwnerHash(ctx, userID)
		if err != nil {
			return nil, err
		}
		value, found, err := s.sharedListCache.GetDialogListSnapshot(
			ctx,
			store.DialogListSnapshotCacheKey{UserID: userID, OwnerHash: ownerHash},
		)
		if err != nil {
			return nil, err
		}
		var ids []int64
		if found {
			ids = privateDialogPeerIDsFromDialogs(value.Dialogs, userID, limit)
		} else {
			ids, err = privateStore.ListPrivateDialogPeerIDs(ctx, userID, limit)
			if err != nil {
				return nil, err
			}
		}
		currentOwnerHash, err := s.dialogOwnerHash(ctx, userID)
		if err != nil {
			return nil, err
		}
		if currentOwnerHash == ownerHash {
			return ids, nil
		}
	}
	return nil, errDialogListSnapshotGenerationChanged
}

func privateDialogPeerIDsFromDialogs(dialogs []domain.Dialog, userID int64, limit int) []int64 {
	candidates := make([]domain.Dialog, 0, min(limit, len(dialogs)))
	seen := make(map[int64]struct{}, min(limit, len(dialogs)))
	for _, dialog := range dialogs {
		if dialog.Peer.Type != domain.PeerTypeUser || dialog.Peer.ID == 0 || dialog.Peer.ID == userID {
			continue
		}
		if _, duplicate := seen[dialog.Peer.ID]; duplicate {
			continue
		}
		seen[dialog.Peer.ID] = struct{}{}
		candidates = append(candidates, dialog)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].TopMessageDate != candidates[j].TopMessageDate {
			return candidates[i].TopMessageDate > candidates[j].TopMessageDate
		}
		if candidates[i].TopMessage != candidates[j].TopMessage {
			return candidates[i].TopMessage > candidates[j].TopMessage
		}
		return candidates[i].Peer.ID > candidates[j].Peer.ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	ids := make([]int64, len(candidates))
	for index := range candidates {
		ids[index] = candidates[index].Peer.ID
	}
	return ids
}

func (s *Service) supportsDialogListSnapshot() bool {
	if s == nil || s.listCache == nil {
		return false
	}
	if s.dialogs != nil {
		if _, ok := s.dialogs.(dialogListSnapshotStore); !ok {
			return false
		}
	}
	if s.channels != nil {
		if _, ok := s.channels.(channelDialogListSnapshotStore); !ok {
			return false
		}
		if _, ok := s.channels.(channelDialogSnapshotHydrator); !ok {
			return false
		}
	}
	return true
}

func (s *Service) loadDialogOwnerSnapshotHeaders(ctx context.Context, userID int64) (domain.DialogList, error) {
	var out domain.DialogList
	if s.dialogs != nil {
		headers, err := s.dialogs.(dialogListSnapshotStore).ListAllBuiltinDialogSnapshotHeaders(ctx, userID)
		if err != nil {
			return domain.DialogList{}, err
		}
		peers := make([]domain.Peer, 0, len(headers.Dialogs))
		for _, dialog := range headers.Dialogs {
			if dialog.Peer.Type == domain.PeerTypeUser && dialog.Peer.ID != 0 {
				peers = append(peers, dialog.Peer)
			}
		}
		materialized := headers
		if len(peers) > 0 {
			materialized, err = s.dialogs.ListByPeers(ctx, userID, peers)
			if err != nil {
				return domain.DialogList{}, err
			}
			materialized = orderMaterializedDialogList(headers, materialized)
		}
		out = mergeDialogLists(out, materialized)
	}
	if s.channels != nil {
		materialized, err := s.channels.(channelDialogListSnapshotStore).ListAllBuiltinChannelDialogSnapshot(ctx, userID)
		if err != nil {
			return domain.DialogList{}, err
		}
		out = mergeChannelDialogs(out, materialized)
	}
	sortDialogList(out.Dialogs)
	out.Count = len(out.Dialogs)
	if err := s.attachArchiveSummaryFromOwnerHeaders(ctx, userID, &out); err != nil {
		return domain.DialogList{}, err
	}
	if err := s.attachOwnerSnapshotDrafts(ctx, userID, &out); err != nil {
		return domain.DialogList{}, err
	}
	return out, nil
}

// attachOwnerSnapshotDrafts materializes the complete bounded cloud-draft set
// once under the dialog_owner stable-read fence. Draft writes bump
// dialog_light and its aggregate dialog_owner generation, so storing these
// overlays in the version-addressed owner snapshot is exact: page reads no
// longer need one ListDraftsByPeers query apiece, and a concurrent draft write
// forces the snapshot materialization retry before publication.
func (s *Service) attachOwnerSnapshotDrafts(ctx context.Context, userID int64, out *domain.DialogList) error {
	if s == nil || s.dialogs == nil || userID == 0 || out == nil || len(out.Dialogs) == 0 {
		return nil
	}
	drafts, err := s.dialogs.ListDrafts(ctx, userID, domain.MaxDialogDraftsPerUser)
	if err != nil {
		return err
	}
	byPeer := make(map[domain.Peer]domain.DialogDraft, len(drafts))
	for _, draft := range drafts {
		if draft.TopMessageID == 0 && draft.Peer.Type != "" && draft.Peer.ID != 0 {
			byPeer[draft.Peer] = cloneDraft(draft)
		}
	}
	for index := range out.Dialogs {
		out.Dialogs[index].Draft = nil
		if draft, found := byPeer[out.Dialogs[index].Peer]; found {
			draft := cloneDraft(draft)
			out.Dialogs[index].Draft = &draft
		}
	}
	return nil
}

func (s *Service) attachArchiveSummaryFromOwnerHeaders(ctx context.Context, userID int64, out *domain.DialogList) error {
	if out == nil {
		return nil
	}
	var top domain.Dialog
	for _, dialog := range out.Dialogs {
		if dialog.FolderID == domain.DialogArchiveFolderID {
			top = dialog
			break
		}
	}
	if top.Peer.ID == 0 {
		return nil
	}
	unreadPeers, unreadMessages := 0, 0
	if s.dialogs != nil {
		peers, messages, err := s.dialogs.CountArchiveUnread(ctx, userID)
		if err != nil {
			return err
		}
		unreadPeers += peers
		unreadMessages += messages
	}
	if s.channels != nil {
		peers, messages, err := s.channels.CountChannelArchiveUnread(ctx, userID)
		if err != nil {
			return err
		}
		unreadPeers += peers
		unreadMessages += messages
	}
	archivePinned := true
	if s.dialogs != nil {
		pinned, err := s.dialogs.ArchivePinned(ctx, userID)
		if err != nil {
			return err
		}
		archivePinned = pinned
	}
	out.ArchiveSummary = &domain.DialogArchiveSummary{
		TopPeer: top.Peer, TopMessage: top.TopMessage,
		TopDialog:        cloneDialogPtr(top),
		UnreadPeersCount: unreadPeers, UnreadMessagesCount: unreadMessages,
		Pinned: archivePinned,
	}
	return nil
}

func (s *Service) hydrateDialogSnapshotPage(ctx context.Context, userID int64, headers domain.DialogList) (domain.DialogList, error) {
	hydrated := cloneDialogList(headers)
	if s.channels != nil {
		channelDialogs := make([]domain.Dialog, 0, len(hydrated.Dialogs)+1)
		present := make(map[domain.Peer]struct{}, len(hydrated.Dialogs))
		for _, dialog := range hydrated.Dialogs {
			present[dialog.Peer] = struct{}{}
			if dialog.Peer.Type == domain.PeerTypeChannel && dialog.Peer.ID != 0 {
				channelDialogs = append(channelDialogs, dialog)
			}
		}
		if hydrated.ArchiveSummary != nil && hydrated.ArchiveSummary.TopDialog != nil {
			top := *hydrated.ArchiveSummary.TopDialog
			if top.Peer.Type == domain.PeerTypeChannel && top.Peer.ID != 0 {
				if _, ok := present[top.Peer]; !ok {
					channelDialogs = append(channelDialogs, top)
				}
			}
		}
		if len(channelDialogs) > 0 {
			projection, err := s.channels.(channelDialogSnapshotHydrator).HydrateChannelDialogSnapshot(ctx, userID, channelDialogs)
			if err != nil {
				return domain.DialogList{}, err
			}
			byPeer := make(map[domain.Peer]domain.Dialog, len(projection.Dialogs))
			for _, dialog := range projection.Dialogs {
				byPeer[dialog.Peer] = dialog
			}
			for index := range hydrated.Dialogs {
				if dialog, ok := byPeer[hydrated.Dialogs[index].Peer]; ok {
					hydrated.Dialogs[index] = dialog
				}
			}
			hydrated.ChannelMessages = append(hydrated.ChannelMessages, projection.Messages...)
			hydrated.Channels = append(hydrated.Channels, projection.Channels...)
			hydrated.Users = append(hydrated.Users, projection.Users...)
		}
	}
	// Drafts are part of the version-addressed owner snapshot. Re-reading them
	// per page would discard that materialization and recreate a PostgreSQL
	// acquisition for every messages.getDialogs cursor.
	if err := s.projectDialogUsers(ctx, userID, &hydrated); err != nil {
		return domain.DialogList{}, err
	}
	return hydrated, nil
}

func orderMaterializedDialogList(headers, materialized domain.DialogList) domain.DialogList {
	materialized.Dialogs = orderMaterializedDialogs(headers.Dialogs, materialized.Dialogs)
	materialized.Count = len(materialized.Dialogs)
	return materialized
}

func orderMaterializedDialogs(headers, materialized []domain.Dialog) []domain.Dialog {
	byPeer := make(map[domain.Peer]domain.Dialog, len(materialized))
	for _, dialog := range materialized {
		byPeer[dialog.Peer] = dialog
	}
	ordered := make([]domain.Dialog, 0, len(headers))
	for _, header := range headers {
		if dialog, ok := byPeer[header.Peer]; ok {
			ordered = append(ordered, dialog)
			continue
		}
		// Keep the authoritative header so a concurrent disappearance remains
		// visible to the generation/dependency guard instead of silently
		// shrinking a page while the read model is being materialized.
		ordered = append(ordered, header)
	}
	return ordered
}

func (s *Service) loadDialogs(ctx context.Context, userID int64, filter domain.DialogFilter, lightweight bool) (domain.DialogList, error) {
	// Snapshot the list-hash epoch before loading any dialog state: if a dialog_light write
	// invalidation occurs during loading/projection, rememberDialogListHash will refuse to write
	// back the stale hash accordingly, preventing a later getDialogs from wrongly returning NotModified.
	listHashEpoch := s.listHashCache.cacheEpoch()
	var out domain.DialogList
	if s.dialogs != nil {
		var list domain.DialogList
		var err error
		list, err = s.dialogs.ListByUser(ctx, userID, filter)
		if err != nil {
			return domain.DialogList{}, err
		}
		out = mergeDialogLists(out, list)
	}
	if s.channels != nil {
		var list domain.ChannelDialogList
		var err error
		list, err = s.channels.ListChannelDialogs(ctx, userID, filter)
		if err != nil {
			return domain.DialogList{}, err
		}
		out = mergeChannelDialogs(out, list)
	}
	sortDialogList(out.Dialogs)
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if len(out.Dialogs) > limit {
		keep := make(map[domain.Peer]struct{}, limit)
		for _, d := range out.Dialogs[:limit] {
			keep[d.Peer] = struct{}{}
		}
		out.Dialogs = out.Dialogs[:limit]
		out.Messages = filterPrivateMessagesByPeer(out.Messages, keep)
		out.ChannelMessages = filterChannelMessagesByPeer(out.ChannelMessages, keep)
		out.Channels = filterChannelsByPeer(out.Channels, keep)
	}
	if out.Count == 0 {
		out.Count = len(out.Dialogs)
	}
	if err := s.attachArchiveSummary(ctx, userID, filter, &out); err != nil {
		return domain.DialogList{}, err
	}
	if lightweight {
		// Archive summary top dialog: no draft attachment, no projection (projected uniformly by
		// the outer layer), no list-hash write.
		return out, nil
	}
	if err := s.attachDrafts(ctx, userID, &out); err != nil {
		return domain.DialogList{}, err
	}
	if err := s.projectDialogUsers(ctx, userID, &out); err != nil {
		return domain.DialogList{}, err
	}
	s.rememberDialogListHash(userID, filter, out, listHashEpoch)
	return out, nil
}

// attachArchiveSummary aggregates the archive summary onto the main list's first-page response:
// TDesktop can only discover the archive from the dialogFolder entry in the response header (a
// newly logged-in device has no updates to replay), and without it archived dialogs are entirely
// invisible. Not attached for archive/custom filter/pinned/pagination requests.
func (s *Service) attachArchiveSummary(ctx context.Context, userID int64, filter domain.DialogFilter, out *domain.DialogList) error {
	if s == nil || out == nil {
		return nil
	}
	if filter.HasFolderID && filter.FolderID != domain.DialogMainFolderID {
		return nil
	}
	// exclude_pinned requests exclude the folder entry per official semantics (the archive row
	// belongs to the pinned set). PinnedOnly (getPinnedDialogs) must not be skipped: DrKLO's main
	// list getDialogs always carries exclude_pinned, so discovery of the archive row depends
	// entirely on the dialogFolder entry in the getPinnedDialogs response
	// (fetchFolderInLoadedPinnedDialogs).
	if filter.ExcludePinned {
		return nil
	}
	if filter.OffsetID != 0 || filter.OffsetDate != 0 || filter.HasOffsetPeer {
		return nil
	}
	top, err := s.getDialogs(ctx, userID, domain.DialogFilter{
		HasFolderID: true,
		FolderID:    domain.DialogArchiveFolderID,
		Limit:       1,
	}, true)
	if err != nil {
		return err
	}
	if len(top.Dialogs) == 0 {
		return nil
	}
	unreadPeers, unreadMessages := 0, 0
	if s.dialogs != nil {
		peers, messages, err := s.dialogs.CountArchiveUnread(ctx, userID)
		if err != nil {
			return err
		}
		unreadPeers += peers
		unreadMessages += messages
	}
	if s.channels != nil {
		peers, messages, err := s.channels.CountChannelArchiveUnread(ctx, userID)
		if err != nil {
			return err
		}
		unreadPeers += peers
		unreadMessages += messages
	}
	archivePinned := true
	if s.dialogs != nil {
		pinned, err := s.dialogs.ArchivePinned(ctx, userID)
		if err != nil {
			return err
		}
		archivePinned = pinned
	}
	// getPinnedDialogs returns only the pinned set: once the archive row is unpinned it no longer belongs to it.
	if filter.PinnedOnly && !archivePinned {
		return nil
	}
	topDialog := top.Dialogs[0]
	out.ArchiveSummary = &domain.DialogArchiveSummary{
		TopPeer:             topDialog.Peer,
		TopMessage:          topDialog.TopMessage,
		TopDialog:           cloneDialogPtr(topDialog),
		UnreadPeersCount:    unreadPeers,
		UnreadMessagesCount: unreadMessages,
		Pinned:              archivePinned,
	}
	// The dialog object that dialogFolder.peer points to must be delivered with the response:
	// TDesktop's Folder::applyDialog immediately dereferences that peer (owner().history(peerId)).
	out.Messages = append(out.Messages, top.Messages...)
	out.ChannelMessages = append(out.ChannelMessages, top.ChannelMessages...)
	out.Users = append(out.Users, top.Users...)
	out.Channels = append(out.Channels, top.Channels...)
	return nil
}

func cloneDialogPtr(dialog domain.Dialog) *domain.Dialog {
	clone := cloneDialog(dialog)
	return &clone
}

// GetPeerDialogs returns the dialog summary for the given peers. A missing peer is returned by the store as an empty dialog placeholder.
func (s *Service) GetPeerDialogs(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error) {
	if s == nil || userID == 0 || len(peers) == 0 {
		return domain.DialogList{}, nil
	}
	if len(peers) > domain.MaxDialogFolderPeers {
		return domain.DialogList{}, domain.ErrChannelInvalid
	}
	userPeers := make([]domain.Peer, 0, len(peers))
	channelIDs := make([]int64, 0, len(peers))
	for _, peer := range peers {
		switch peer.Type {
		case domain.PeerTypeUser:
			userPeers = append(userPeers, peer)
		case domain.PeerTypeChannel:
			channelIDs = append(channelIDs, peer.ID)
		}
	}
	var out domain.DialogList
	if len(userPeers) > 0 && s.dialogs != nil {
		list, err := s.userPeerDialogsReadModel(ctx, userID, userPeers)
		if err != nil {
			return domain.DialogList{}, err
		}
		out = mergeDialogLists(out, list)
	}
	if len(channelIDs) > 0 && s.channels != nil {
		channelOut, err := s.channelPeerDialogsReadModel(ctx, userID, channelIDs)
		if err != nil {
			return domain.DialogList{}, err
		}
		out = mergeDialogLists(out, channelOut)
	}
	if err := s.attachDrafts(ctx, userID, &out); err != nil {
		return domain.DialogList{}, err
	}
	if err := s.projectDialogUsers(ctx, userID, &out); err != nil {
		return domain.DialogList{}, err
	}
	return out, nil
}

func (s *Service) appendMissingChannelPeerPreviews(ctx context.Context, userID int64, channelIDs []int64, out domain.DialogList) (domain.DialogList, error) {
	if s == nil || s.channels == nil || userID == 0 || len(channelIDs) == 0 {
		return out, nil
	}
	present := make(map[int64]struct{}, len(out.Dialogs))
	for _, dialog := range out.Dialogs {
		if dialog.Peer.Type == domain.PeerTypeChannel && dialog.Peer.ID != 0 {
			present[dialog.Peer.ID] = struct{}{}
		}
	}
	seen := make(map[int64]struct{}, len(channelIDs))
	missingIDs := make([]int64, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		if _, ok := seen[channelID]; ok {
			continue
		}
		seen[channelID] = struct{}{}
		if _, ok := present[channelID]; ok {
			continue
		}

		missingIDs = append(missingIDs, channelID)
	}
	if len(missingIDs) == 0 {
		return out, nil
	}
	views, err := s.channels.GetChannels(ctx, userID, missingIDs)
	if err != nil {
		if isChannelPreviewAccessError(err) {
			return out, nil
		}
		return domain.DialogList{}, err
	}
	viewsByID := make(map[int64]domain.ChannelView, len(views))
	for _, view := range views {
		if view.Channel.ID != 0 {
			viewsByID[view.Channel.ID] = view
		}
	}
	for _, channelID := range missingIDs {
		view, ok := viewsByID[channelID]
		if !ok || view.Forbidden {
			continue
		}
		if view.Self.Status == domain.ChannelMemberLeft && !view.Self.Guest {
			// A visible public preview still needs one transient dialog so a
			// client can finish bootstrapping the requested peer. Keep the top
			// message and read state at zero: the response is an admission
			// token, not a persisted/chat-list dialog snapshot. In particular,
			// clients that persist non-zero top dialogs will instead continue
			// with messages.getHistory, which is the authoritative preview
			// history path.
			out.Dialogs = append(out.Dialogs, publicChannelPreviewBootstrapDialog(view))
			out.Channels = append(out.Channels, view.Channel)
			out.Count++
			present[channelID] = struct{}{}
			continue
		}
		// Linked discussion guests need a transient peer-dialog snapshot so
		// TDesktop can finish materializing the comments History after
		// requestSelf. ChannelLeft keeps the snapshot out of the main chat list,
		// while Guest authorizes the target's real top-message snapshot.
		if view.Self.Status != domain.ChannelMemberActive && !view.Self.Guest {
			continue
		}
		history, err := s.channels.ListChannelHistory(ctx, userID, domain.ChannelHistoryFilter{
			ChannelID: channelID,
			Limit:     1,
		})
		if err != nil {
			if isChannelPreviewAccessError(err) {
				continue
			}
			return domain.DialogList{}, err
		}

		dialog := dialogFromChannelView(view)
		if len(history.Messages) > 0 {
			top := history.Messages[0]
			dialog.TopMessage = top.ID
			dialog.TopMessageDate = top.Date
			out.ChannelMessages = append(out.ChannelMessages, top)
		}
		out.Dialogs = append(out.Dialogs, dialog)
		out.Channels = append(out.Channels, view.Channel)
		out.Channels = append(out.Channels, history.Channels...)
		out.Users = append(out.Users, history.Users...)
		out.Count++
		present[channelID] = struct{}{}
	}
	return out, nil
}

func publicChannelPreviewBootstrapDialog(view domain.ChannelView) domain.Dialog {
	return domain.Dialog{
		Peer:        domain.Peer{Type: domain.PeerTypeChannel, ID: view.Channel.ID},
		ChannelLeft: true,
		Pts:         view.Channel.Pts,
	}
}

func isChannelPreviewAccessError(err error) bool {
	return errors.Is(err, domain.ErrChannelPrivate) ||
		errors.Is(err, domain.ErrChannelUserBanned) ||
		errors.Is(err, domain.ErrChannelInvalid)
}

func dialogFromChannelView(view domain.ChannelView) domain.Dialog {
	dialog := view.Dialog
	return domain.Dialog{
		Peer:                   domain.Peer{Type: domain.PeerTypeChannel, ID: dialog.ChannelID},
		ChannelLeft:            view.Self.Status == domain.ChannelMemberLeft,
		FolderID:               dialog.FolderID,
		TopMessage:             dialog.TopMessageID,
		TopMessageDate:         dialog.TopMessageDate,
		HistoryClearAnchorID:   dialog.HistoryClearAnchorID,
		HistoryClearAnchorDate: dialog.HistoryClearAnchorDate,
		ReadInboxMaxID:         dialog.ReadInboxMaxID,
		ReadOutboxMaxID:        dialog.ReadOutboxMaxID,
		UnreadCount:            dialog.UnreadCount,
		UnreadMentions:         dialog.UnreadMentions,
		UnreadReactions:        dialog.UnreadReactions,
		Pinned:                 dialog.Pinned,
		PinnedOrder:            dialog.PinnedOrder,
		UnreadMark:             dialog.UnreadMark,
		ViewForumAsMessages:    dialog.ViewForumAsMessages,
		HasScheduled:           dialog.HasScheduled,
		Pts:                    view.Channel.Pts,
	}
}

// SaveDraft stores or clears a cloud draft for one peer/topic.
// It returns whether the authoritative draft content changed. A repeated save
// with identical content does not refresh Date, invalidate read models, or
// force a durable update.
func (s *Service) SaveDraft(ctx context.Context, userID int64, draft domain.DialogDraft) (bool, error) {
	if s == nil || s.dialogs == nil || userID == 0 {
		return false, nil
	}
	if err := validateDraft(draft); err != nil {
		return false, err
	}
	if draft.Empty() {
		changed, err := s.dialogs.DeleteDraft(ctx, userID, draft.Peer, draft.TopMessageID)
		if err == nil && changed {
			s.InvalidateDialog(userID, draft.Peer)
		}
		return changed, err
	}
	existing, found, err := s.dialogs.GetDraft(ctx, userID, draft.Peer, draft.TopMessageID)
	if err != nil {
		return false, err
	}
	if found && sameDialogDraftContent(existing, draft) {
		return false, nil
	}
	if err := s.dialogs.SaveDraft(ctx, userID, draft); err != nil {
		return false, err
	}
	s.InvalidateDialog(userID, draft.Peer)
	return true, nil
}

func sameDialogDraftContent(a, b domain.DialogDraft) bool {
	a.Date = 0
	b.Date = 0
	return reflect.DeepEqual(a, b)
}

// GetDraft reads a dialog's current cloud draft (used to reload per peer when replaying a draft_message event).
func (s *Service) GetDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (domain.DialogDraft, bool, error) {
	if s == nil || s.dialogs == nil || userID == 0 {
		return domain.DialogDraft{}, false, nil
	}
	if err := validateDraftKey(peer, topMessageID); err != nil {
		return domain.DialogDraft{}, false, err
	}
	return s.dialogs.GetDraft(ctx, userID, peer, topMessageID)
}

// DeleteDraft clears one cloud draft.
func (s *Service) DeleteDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (bool, error) {
	if s == nil || s.dialogs == nil || userID == 0 {
		return false, nil
	}
	if err := validateDraftKey(peer, topMessageID); err != nil {
		return false, err
	}
	changed, err := s.dialogs.DeleteDraft(ctx, userID, peer, topMessageID)
	if err == nil && changed {
		s.InvalidateDialog(userID, peer)
	}
	return changed, err
}

// ListDrafts returns bounded cloud drafts for messages.getAllDrafts.
func (s *Service) ListDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error) {
	if s == nil || s.dialogs == nil || userID == 0 {
		return nil, nil
	}
	return s.dialogs.ListDrafts(ctx, userID, clampDraftLimit(limit))
}

// ClearDrafts deletes bounded cloud drafts for messages.clearAllDrafts.
func (s *Service) ClearDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error) {
	if s == nil || s.dialogs == nil || userID == 0 {
		return nil, nil
	}
	drafts, err := s.dialogs.ClearDrafts(ctx, userID, clampDraftLimit(limit))
	if err == nil {
		for _, draft := range drafts {
			s.InvalidateDialog(userID, draft.Peer)
		}
	}
	return drafts, err
}

// TogglePinned pins/unpins a dialog; the pin order is allocated within the dialog's current
// folder, returning (changed, the dialog's folder_id) for updateDialogPinned.folder_id to use.
// On pin it checks the per-folder limit (a repeat pin is idempotently allowed), and returns
// ErrPinnedDialogsTooMuch when over the limit.
func (s *Service) TogglePinned(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, int, error) {
	if s == nil || userID == 0 || peer.Type == "" || peer.ID == 0 {
		return false, 0, nil
	}
	if pinned {
		if err := s.checkPinnedLimit(ctx, userID, peer); err != nil {
			return false, 0, err
		}
	}
	switch peer.Type {
	case domain.PeerTypeChannel:
		if s.channels == nil {
			return false, 0, nil
		}
		changed, folderID, err := s.channels.SetChannelDialogPinned(ctx, userID, peer.ID, pinned)
		if err == nil && changed {
			if pinned {
				if err := s.promotePinnedDialog(ctx, userID, folderID, peer); err != nil {
					return changed, folderID, err
				}
			}
			s.InvalidateDialog(userID, peer)
		}
		return changed, folderID, err
	default:
		if s.dialogs == nil {
			return false, 0, nil
		}
		changed, folderID, err := s.dialogs.SetPinned(ctx, userID, peer, pinned)
		if err == nil && changed {
			if pinned {
				if err := s.promotePinnedDialog(ctx, userID, folderID, peer); err != nil {
					return changed, folderID, err
				}
			}
			s.InvalidateDialog(userID, peer)
		}
		return changed, folderID, err
	}
}

func (s *Service) promotePinnedDialog(ctx context.Context, userID int64, folderID int, peer domain.Peer) error {
	if s == nil || userID == 0 || peer.Type == "" || peer.ID == 0 {
		return nil
	}
	list, err := s.GetDialogs(ctx, userID, domain.DialogFilter{
		PinnedOnly:  true,
		HasFolderID: true,
		FolderID:    folderID,
		Limit:       domain.PinnedDialogsLimit(folderID, true),
	})
	if err != nil {
		return err
	}
	order := make([]domain.Peer, 0, len(list.Dialogs))
	order = append(order, peer)
	seen := map[domain.Peer]struct{}{peer: {}}
	found := false
	for _, dialog := range list.Dialogs {
		if dialog.Peer == peer {
			found = true
			continue
		}
		if !dialog.Pinned {
			continue
		}
		if _, ok := seen[dialog.Peer]; ok {
			continue
		}
		seen[dialog.Peer] = struct{}{}
		order = append(order, dialog.Peer)
	}
	if !found {
		return nil
	}
	_, err = s.ReorderPinned(ctx, userID, folderID, order, false)
	return err
}

func (s *Service) checkPinnedLimit(ctx context.Context, userID int64, peer domain.Peer) error {
	current, err := s.GetPeerDialogs(ctx, userID, []domain.Peer{peer})
	if err != nil {
		return err
	}
	folderID := domain.DialogMainFolderID
	for _, dialog := range current.Dialogs {
		if dialog.Peer != peer {
			continue
		}
		if dialog.Pinned {
			// A repeat pin is idempotent and does not consume a new slot.
			return nil
		}
		folderID = dialog.FolderID
		break
	}
	premium := s.premium != nil && s.premium(ctx, userID)
	limit := domain.PinnedDialogsLimit(folderID, premium)
	pinnedList, err := s.GetDialogs(ctx, userID, domain.DialogFilter{
		PinnedOnly:  true,
		HasFolderID: true,
		FolderID:    folderID,
		Limit:       limit,
	})
	if err != nil {
		return err
	}
	if pinnedList.Count >= limit {
		return domain.ErrPinnedDialogsTooMuch
	}
	return nil
}

// ToggleArchivePinned pins/unpins the archive folder row itself
// (toggleDialogPin(inputDialogPeerFolder)), returning whether anything changed.
func (s *Service) ToggleArchivePinned(ctx context.Context, userID int64, pinned bool) (bool, error) {
	if s == nil || s.dialogs == nil || userID == 0 {
		return false, nil
	}
	changed, err := s.dialogs.SetArchivePinned(ctx, userID, pinned)
	if err == nil && changed {
		s.invalidateDialogListHashes(userID)
	}
	return changed, err
}

// ReorderPinned reorders the pin order within the given folder (0 main list / 1 archive);
// force clears only the pins within that folder that are not in order, never touching other folders.
func (s *Service) ReorderPinned(ctx context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error) {
	if s == nil || userID == 0 {
		return false, nil
	}
	changed := false
	if s.dialogs != nil {
		privateChanged, err := s.dialogs.ReorderPinned(ctx, userID, folderID, order, force)
		if err != nil {
			return false, err
		}
		if privateChanged {
			changed = true
			for _, peer := range order {
				if peer.Type != domain.PeerTypeChannel {
					s.InvalidateDialog(userID, peer)
				}
			}
		}
	}
	if s.channels != nil {
		channelChanged, err := s.channels.ReorderChannelPinnedDialogs(ctx, userID, folderID, order, force)
		if err != nil {
			return false, err
		}
		if channelChanged {
			changed = true
			for _, peer := range order {
				if peer.Type == domain.PeerTypeChannel {
					s.InvalidateDialog(userID, peer)
				}
			}
		}
	}
	if changed && force {
		// force may unpin peers omitted from order; the store returns only a boolean,
		// so flush the small peer-dialog snapshot cache to avoid stale omitted peers.
		s.FlushReadModelCache()
	}
	return changed, nil
}

func (s *Service) MarkUnread(ctx context.Context, userID int64, peer domain.Peer, unread bool) (bool, error) {
	if s == nil || userID == 0 || peer.Type == "" || peer.ID == 0 {
		return false, nil
	}
	switch peer.Type {
	case domain.PeerTypeChannel:
		if s.channels == nil {
			return false, nil
		}
		changed, err := s.channels.SetChannelDialogUnreadMark(ctx, userID, peer.ID, unread)
		if err == nil && changed {
			s.InvalidateDialog(userID, peer)
		}
		return changed, err
	default:
		if s.dialogs == nil {
			return false, nil
		}
		changed, err := s.dialogs.SetUnreadMark(ctx, userID, peer, unread)
		if err == nil && changed {
			s.InvalidateDialog(userID, peer)
		}
		return changed, err
	}
}

func (s *Service) UnreadMarks(ctx context.Context, userID int64) ([]domain.Peer, error) {
	if s == nil || userID == 0 {
		return nil, nil
	}
	var out []domain.Peer
	if s.dialogs != nil {
		peers, err := s.dialogs.ListUnreadMarked(ctx, userID)
		if err != nil {
			return nil, err
		}
		out = append(out, peers...)
	}
	if s.channels != nil {
		peers, err := s.channels.ListChannelUnreadMarked(ctx, userID)
		if err != nil {
			return nil, err
		}
		out = append(out, peers...)
	}
	return out, nil
}

func (s *Service) HidePeerSettingsBar(ctx context.Context, userID int64, peer domain.Peer) (bool, error) {
	if s == nil || s.dialogs == nil || userID == 0 || peer.Type == "" || peer.ID == 0 {
		return false, nil
	}
	changed, err := s.dialogs.SetPeerSettingsBarHidden(ctx, userID, peer)
	if err == nil && changed {
		s.InvalidateDialog(userID, peer)
	}
	return changed, err
}

func (s *Service) PeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error) {
	if s == nil || s.dialogs == nil || userID == 0 || peer.Type == "" || peer.ID == 0 {
		return false, nil
	}
	return s.dialogs.PeerSettingsBarHidden(ctx, userID, peer)
}

func (s *Service) GetDialogFolders(ctx context.Context, userID int64) (domain.DialogFolderList, error) {
	if s == nil || s.dialogs == nil || userID == 0 {
		return domain.DialogFolderList{}, nil
	}
	return s.dialogs.ListFolders(ctx, userID)
}

func (s *Service) SaveDialogFolder(ctx context.Context, userID int64, folder domain.DialogFolder) error {
	if s == nil || s.dialogs == nil || userID == 0 {
		return nil
	}
	if err := s.dialogs.UpsertFolder(ctx, userID, folder); err != nil {
		return err
	}
	s.invalidateDialogListHashes(userID)
	return nil
}

func (s *Service) DeleteDialogFolder(ctx context.Context, userID int64, folderID int) error {
	if s == nil || s.dialogs == nil || userID == 0 {
		return nil
	}
	if err := s.dialogs.DeleteFolder(ctx, userID, folderID); err != nil {
		return err
	}
	s.invalidateDialogListHashes(userID)
	return nil
}

func (s *Service) ReorderDialogFolders(ctx context.Context, userID int64, order []int) error {
	if s == nil || s.dialogs == nil || userID == 0 {
		return nil
	}
	if err := s.dialogs.ReorderFolders(ctx, userID, order); err != nil {
		return err
	}
	s.invalidateDialogListHashes(userID)
	return nil
}

func (s *Service) ToggleDialogFolderTags(ctx context.Context, userID int64, enabled bool) error {
	if s == nil || s.dialogs == nil || userID == 0 {
		return nil
	}
	return s.dialogs.SetFolderTagsEnabled(ctx, userID, enabled)
}

func (s *Service) EditPeerFolders(ctx context.Context, userID int64, peers []domain.FolderPeerUpdate) error {
	if s == nil || userID == 0 || len(peers) == 0 {
		return nil
	}
	privatePeers := make([]domain.FolderPeerUpdate, 0, len(peers))
	channelPeers := make([]domain.FolderPeerUpdate, 0, len(peers))
	for _, peer := range peers {
		if peer.Peer.Type == domain.PeerTypeChannel {
			channelPeers = append(channelPeers, peer)
		} else {
			privatePeers = append(privatePeers, peer)
		}
	}
	if len(privatePeers) > 0 && s.dialogs != nil {
		if err := s.dialogs.EditPeerFolders(ctx, userID, privatePeers); err != nil {
			return err
		}
		for _, update := range privatePeers {
			s.InvalidateDialog(userID, update.Peer)
		}
	}
	if len(channelPeers) > 0 && s.channels != nil {
		if err := s.channels.EditChannelPeerFolders(ctx, userID, channelPeers); err != nil {
			return err
		}
		for _, update := range channelPeers {
			s.InvalidateDialog(userID, update.Peer)
		}
	}
	return nil
}

func (s *Service) attachDrafts(ctx context.Context, userID int64, list *domain.DialogList) error {
	if s == nil || s.dialogs == nil || userID == 0 || list == nil || len(list.Dialogs) == 0 {
		return nil
	}
	peers := make([]domain.Peer, 0, len(list.Dialogs))
	for _, dialog := range list.Dialogs {
		if dialog.Peer.ID != 0 {
			peers = append(peers, dialog.Peer)
		}
	}
	drafts, err := s.dialogDraftsReadModel(ctx, userID, peers)
	if err != nil {
		return err
	}
	attached := false
	for i := range list.Dialogs {
		list.Dialogs[i].Draft = nil
		draft, ok := drafts[list.Dialogs[i].Peer]
		if !ok || !draft.found {
			continue
		}
		d := cloneDraft(draft.draft)
		list.Dialogs[i].Draft = &d
		attached = true
	}
	if attached {
		list.Hash = dialogHashWithDrafts(list.Hash, list.Dialogs)
	}
	return nil
}

func (s *Service) projectDialogUsers(ctx context.Context, userID int64, list *domain.DialogList) error {
	if s == nil || s.projector == nil || list == nil || len(list.Users) == 0 {
		return nil
	}
	users, err := s.projector.ForViewer(ctx, userID, list.Users)
	if err != nil {
		return err
	}
	list.Users = users
	return nil
}

func validateDraft(draft domain.DialogDraft) error {
	if err := validateDraftKey(draft.Peer, draft.TopMessageID); err != nil {
		return err
	}
	if len(draft.Entities) > domain.MaxMessageEntityCount {
		return domain.ErrChannelInvalid
	}
	if utf8.RuneCountInString(draft.Message) > domain.MaxMessageTextLength {
		return domain.ErrChannelInvalid
	}
	if domain.ValidateMessageReplyBounds(draft.ReplyTo) != nil {
		return domain.ErrReplyMessageIDInvalid
	}
	return nil
}

func validateDraftKey(peer domain.Peer, topMessageID int) error {
	if peer.ID == 0 || (peer.Type != domain.PeerTypeUser && peer.Type != domain.PeerTypeChannel) {
		return domain.ErrChannelInvalid
	}
	if topMessageID < 0 || topMessageID > domain.MaxMessageBoxID {
		return domain.ErrReplyMessageIDInvalid
	}
	return nil
}

func clampDraftLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxDialogDraftsPerUser {
		return domain.MaxDialogDraftsPerUser
	}
	return limit
}

func cloneDraft(draft domain.DialogDraft) domain.DialogDraft {
	draft.Entities = append([]domain.MessageEntity(nil), draft.Entities...)
	if draft.ReplyTo != nil {
		reply := *draft.ReplyTo
		reply.QuoteEntities = append([]domain.MessageEntity(nil), draft.ReplyTo.QuoteEntities...)
		draft.ReplyTo = &reply
	}
	if draft.WebPage != nil {
		webpage := *draft.WebPage
		draft.WebPage = &webpage
	}
	draft.RichMessage = cloneRichMessage(draft.RichMessage)
	return draft
}

func dialogHashWithDrafts(base int64, dialogs []domain.Dialog) int64 {
	if len(dialogs) == 0 {
		return base
	}
	h := fnv.New64a()
	var buf [48]byte
	binary.LittleEndian.PutUint64(buf[:8], uint64(base))
	_, _ = h.Write(buf[:8])
	for _, d := range dialogs {
		if d.Draft == nil {
			continue
		}
		binary.LittleEndian.PutUint64(buf[:8], uint64(d.Peer.ID))
		binary.LittleEndian.PutUint32(buf[8:12], uint32(d.Draft.TopMessageID))
		binary.LittleEndian.PutUint32(buf[12:16], uint32(d.Draft.Date))
		binary.LittleEndian.PutUint64(buf[16:24], uint64(len(d.Draft.Message)))
		if d.Draft.NoWebpage {
			buf[24] = 1
		} else {
			buf[24] = 0
		}
		if d.Draft.InvertMedia {
			buf[25] = 1
		} else {
			buf[25] = 0
		}
		binary.LittleEndian.PutUint64(buf[26:34], uint64(len(d.Draft.Entities)))
		binary.LittleEndian.PutUint64(buf[34:42], uint64(d.Draft.Effect))
		_, _ = h.Write(buf[:])
		_, _ = h.Write([]byte(d.Draft.Message))
		if d.Draft.WebPage != nil {
			_, _ = h.Write([]byte(d.Draft.WebPage.URL))
		}
		writeDraftRichHash(h, buf[:], d.Draft.RichMessage)
	}
	return int64(h.Sum64())
}

func cloneRichMessage(m *domain.MessageRichMessage) *domain.MessageRichMessage {
	if m == nil {
		return nil
	}
	clone := *m
	clone.Blocks = append([]byte(nil), m.Blocks...)
	clone.Photos = append([]domain.Photo(nil), m.Photos...)
	clone.Documents = append([]domain.Document(nil), m.Documents...)
	clone.BotAPIProjection = append([]byte(nil), m.BotAPIProjection...)
	return &clone
}

func writeDraftRichHash(h interface{ Write([]byte) (int, error) }, buf []byte, rich *domain.MessageRichMessage) {
	if rich.IsZero() {
		return
	}
	if rich.Rtl {
		buf[0] = 1
	} else {
		buf[0] = 0
	}
	if rich.Part {
		buf[1] = 1
	} else {
		buf[1] = 0
	}
	binary.LittleEndian.PutUint64(buf[2:10], uint64(len(rich.Blocks)))
	binary.LittleEndian.PutUint64(buf[10:18], uint64(len(rich.Photos)))
	binary.LittleEndian.PutUint64(buf[18:26], uint64(len(rich.Documents)))
	binary.LittleEndian.PutUint64(buf[26:34], uint64(rich.EffectiveBlocksLayer()))
	_, _ = h.Write(buf[:34])
	_, _ = h.Write(rich.Blocks)
	for _, photo := range rich.Photos {
		binary.LittleEndian.PutUint64(buf[:8], uint64(photo.ID))
		binary.LittleEndian.PutUint64(buf[8:16], uint64(photo.AccessHash))
		_, _ = h.Write(buf[:16])
	}
	for _, document := range rich.Documents {
		binary.LittleEndian.PutUint64(buf[:8], uint64(document.ID))
		binary.LittleEndian.PutUint64(buf[8:16], uint64(document.AccessHash))
		_, _ = h.Write(buf[:16])
	}
}

func mergeDialogLists(out, in domain.DialogList) domain.DialogList {
	out.Dialogs = append(out.Dialogs, in.Dialogs...)
	out.Messages = append(out.Messages, in.Messages...)
	out.ChannelMessages = append(out.ChannelMessages, in.ChannelMessages...)
	out.Users = append(out.Users, in.Users...)
	out.Channels = append(out.Channels, in.Channels...)
	out.Count += in.Count
	out.Hash ^= in.Hash
	return out
}

func mergeChannelDialogs(out domain.DialogList, in domain.ChannelDialogList) domain.DialogList {
	out.Dialogs = append(out.Dialogs, in.Dialogs...)
	out.ChannelMessages = append(out.ChannelMessages, in.Messages...)
	out.Channels = append(out.Channels, in.Channels...)
	out.Users = append(out.Users, in.Users...)
	out.Count += in.Count
	out.Hash ^= in.Hash
	return out
}

func sortDialogList(dialogs []domain.Dialog) {
	sort.SliceStable(dialogs, func(i, j int) bool {
		if dialogs[i].Pinned != dialogs[j].Pinned {
			return dialogs[i].Pinned
		}
		if dialogs[i].PinnedOrder != dialogs[j].PinnedOrder {
			return dialogs[i].PinnedOrder > dialogs[j].PinnedOrder
		}
		if dialogs[i].TopMessageDate != dialogs[j].TopMessageDate {
			return dialogs[i].TopMessageDate > dialogs[j].TopMessageDate
		}
		if dialogs[i].TopMessage != dialogs[j].TopMessage {
			return dialogs[i].TopMessage > dialogs[j].TopMessage
		}
		return dialogs[i].Peer.ID > dialogs[j].Peer.ID
	})
}

func filterPrivateMessagesByPeer(messages []domain.Message, keep map[domain.Peer]struct{}) []domain.Message {
	out := messages[:0]
	for _, msg := range messages {
		if _, ok := keep[msg.Peer]; ok {
			out = append(out, msg)
		}
	}
	return out
}

func filterChannelMessagesByPeer(messages []domain.ChannelMessage, keep map[domain.Peer]struct{}) []domain.ChannelMessage {
	out := messages[:0]
	for _, msg := range messages {
		peer := domain.Peer{Type: domain.PeerTypeChannel, ID: msg.ChannelID}
		if _, ok := keep[peer]; ok {
			out = append(out, msg)
		}
	}
	return out
}

func filterChannelsByPeer(channels []domain.Channel, keep map[domain.Peer]struct{}) []domain.Channel {
	out := channels[:0]
	for _, ch := range channels {
		peer := domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}
		if _, ok := keep[peer]; ok {
			out = append(out, ch)
		}
	}
	return out
}
