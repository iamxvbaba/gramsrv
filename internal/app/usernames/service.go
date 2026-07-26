// Package usernames implements the collectible (Fragment-style) username
// registry use cases: reading a peer's username vector, toggling and reordering
// the collectible rows a client owns, and the operator lifecycle that mints,
// transfers, revokes and burns the assets behind those rows.
//
// The service owns normalisation and validation. Every entry point normalises
// the name through domain.NormalizeUsername and runs the domain Validate()
// checks before the store is touched, so an RPC handler, the admin API and a
// unit test all reject the same shapes with the same errors.
package usernames

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/links"
	"telesrv/internal/store"
)

const (
	// defaultListLimit is the admin listing page size used when the caller does
	// not bound the query itself.
	defaultListLimit = 50
	// maxListLimit bounds one listing page regardless of the requested limit.
	maxListLimit = 200
	// defaultTransferLimit / maxTransferLimit bound the provenance log page.
	defaultTransferLimit = 50
	maxTransferLimit     = 200
	// usernamePlaceholder is the substitution supported by the operator URL
	// template, e.g. https://example.org/nft/{username}.
	usernamePlaceholder = "{username}"
	// defaultCollectibleURLPath is the public-link route used when no operator
	// template is configured.
	defaultCollectibleURLPath = "nft/username"
)

// ErrPeerInvalid rejects a registry mutation for a peer that cannot hold
// usernames. Only users and channels have a username registry; anything else is
// a caller bug rather than a client-visible protocol state.
var ErrPeerInvalid = errors.New("username peer invalid")

// PeerUsernameNotifier is the domain-only edge hook invoked after a username
// registry mutation. The RPC router implements it: it invalidates the cached
// peer projections and pushes the username change to online clients, exactly
// like the account.updateUsername path does for the editable slot. Keeping it an
// injected port means this package never depends on the protocol edge.
type PeerUsernameNotifier interface {
	NotifyPeerUsernamesChanged(ctx context.Context, peer domain.Peer) error
}

// Service is the collectible username use-case layer.
type Service struct {
	registry     store.UsernameRegistryStore
	collectibles store.CollectibleUsernameStore
	notifier     PeerUsernameNotifier

	// urlTemplate is the operator-provided collectible landing URL template;
	// publicBaseURL is the fallback root the default route is built from.
	urlTemplate   string
	publicBaseURL string

	now func() time.Time
	log *zap.Logger
}

// Option adjusts optional service dependencies.
type Option func(*Service)

// WithRegistryStore injects the peer username registry reader/writer.
func WithRegistryStore(registry store.UsernameRegistryStore) Option {
	return func(s *Service) { s.registry = registry }
}

// WithCollectibleStore injects the collectible asset lifecycle store.
func WithCollectibleStore(collectibles store.CollectibleUsernameStore) Option {
	return func(s *Service) { s.collectibles = collectibles }
}

// WithNotifier injects the edge invalidation/update hook.
func WithNotifier(notifier PeerUsernameNotifier) Option {
	return func(s *Service) { s.notifier = notifier }
}

// WithURLTemplate configures the collectible asset landing URL template. An
// empty template keeps the public-link default route.
func WithURLTemplate(template string) Option {
	return func(s *Service) { s.urlTemplate = strings.TrimSpace(template) }
}

// WithPublicBaseURL configures the public-link root the default collectible URL
// route is derived from.
func WithPublicBaseURL(baseURL string) Option {
	return func(s *Service) { s.publicBaseURL = strings.TrimSpace(baseURL) }
}

// WithClock injects the clock (tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithLogger injects the service logger.
func WithLogger(log *zap.Logger) Option {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

// NewService creates the collectible username service. Every dependency is
// optional: a service without stores answers with a configuration error instead
// of panicking, which keeps partial deployments diagnosable.
func NewService(opts ...Option) *Service {
	s := &Service{now: time.Now, log: zap.NewNop()}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	return s
}

// SetPeerUsernameNotifier injects the edge hook after construction. The RPC
// router is built after the app services, so the notification port is bound
// here rather than through NewService.
func (s *Service) SetPeerUsernameNotifier(notifier PeerUsernameNotifier) {
	if s == nil {
		return
	}
	s.notifier = notifier
}

// Configured reports whether both registries are installed.
func (s *Service) Configured() bool {
	return s != nil && s.registry != nil && s.collectibles != nil
}

func (s *Service) registryStore() (store.UsernameRegistryStore, error) {
	if s == nil || s.registry == nil {
		return nil, fmt.Errorf("username registry store is not configured")
	}
	return s.registry, nil
}

func (s *Service) collectibleStore() (store.CollectibleUsernameStore, error) {
	if s == nil || s.collectibles == nil {
		return nil, fmt.Errorf("collectible username store is not configured")
	}
	return s.collectibles, nil
}

// PeerUsernames returns the peer's username vector in projection order.
func (s *Service) PeerUsernames(ctx context.Context, peer domain.Peer) ([]domain.Username, error) {
	registry, err := s.registryStore()
	if err != nil {
		return nil, err
	}
	if !validPeer(peer) {
		return nil, nil
	}
	list, err := registry.PeerUsernames(ctx, peer)
	if err != nil {
		return nil, err
	}
	return domain.SortUsernames(list), nil
}

// UsernamesBatch resolves several peers in one round trip. Peers holding no
// usernames are absent from the result.
func (s *Service) UsernamesBatch(ctx context.Context, peers []domain.Peer) (map[domain.Peer][]domain.Username, error) {
	registry, err := s.registryStore()
	if err != nil {
		return nil, err
	}
	unique := make([]domain.Peer, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		if !validPeer(peer) {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		unique = append(unique, peer)
	}
	if len(unique) == 0 {
		return map[domain.Peer][]domain.Username{}, nil
	}
	batch, err := registry.PeerUsernamesBatch(ctx, unique)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.Peer][]domain.Username, len(batch))
	for peer, list := range batch {
		if len(list) == 0 {
			continue
		}
		out[peer] = domain.SortUsernames(list)
	}
	return out, nil
}

// ToggleUsername activates or deactivates one collectible row. The editable
// slot is never touched: it is owned by account/channels.updateUsername.
func (s *Service) ToggleUsername(ctx context.Context, peer domain.Peer, username string, active bool) (bool, error) {
	registry, err := s.registryStore()
	if err != nil {
		return false, err
	}
	if !validPeer(peer) {
		return false, ErrPeerInvalid
	}
	username = domain.NormalizeUsername(username)
	if username == "" {
		return false, domain.ErrUsernameInvalid
	}
	current, err := registry.PeerUsernames(ctx, peer)
	if err != nil {
		return false, err
	}
	if err := domain.ValidateUsernameToggle(current, username, active); err != nil {
		return false, err
	}
	changed, err := registry.SetUsernameActive(ctx, peer, username, active)
	if err != nil {
		return false, err
	}
	if changed {
		s.notifyPeers(ctx, peer)
	}
	return changed, nil
}

// ReorderUsernames rewrites the collectible order. order must be a permutation
// of the peer's collectible usernames; the editable slot always projects first.
func (s *Service) ReorderUsernames(ctx context.Context, peer domain.Peer, order []string) (bool, error) {
	registry, err := s.registryStore()
	if err != nil {
		return false, err
	}
	if !validPeer(peer) {
		return false, ErrPeerInvalid
	}
	normalized := make([]string, 0, len(order))
	for _, name := range order {
		normalized = append(normalized, domain.NormalizeUsername(name))
	}
	current, err := registry.PeerUsernames(ctx, peer)
	if err != nil {
		return false, err
	}
	if err := domain.ValidateUsernameReorder(current, normalized); err != nil {
		return false, err
	}
	changed, err := registry.ReorderUsernames(ctx, peer, normalized)
	if err != nil {
		return false, err
	}
	if changed {
		s.notifyPeers(ctx, peer)
	}
	return changed, nil
}

// DeactivateAllUsernames clears the active flag on every collectible row.
func (s *Service) DeactivateAllUsernames(ctx context.Context, peer domain.Peer) (bool, error) {
	registry, err := s.registryStore()
	if err != nil {
		return false, err
	}
	if !validPeer(peer) {
		return false, ErrPeerInvalid
	}
	changed, err := registry.DeactivateAllUsernames(ctx, peer)
	if err != nil {
		return false, err
	}
	if changed {
		s.notifyPeers(ctx, peer)
	}
	return changed, nil
}

// CollectibleInfo returns the fragment.collectibleInfo projection for a name.
func (s *Service) CollectibleInfo(ctx context.Context, username string) (domain.CollectibleInfo, error) {
	asset, err := s.Collectible(ctx, username)
	if err != nil {
		return domain.CollectibleInfo{}, err
	}
	return asset.Info(), nil
}

// Collectible looks up the asset behind a collectible username.
func (s *Service) Collectible(ctx context.Context, username string) (domain.CollectibleUsername, error) {
	collectibles, err := s.collectibleStore()
	if err != nil {
		return domain.CollectibleUsername{}, err
	}
	username = domain.NormalizeUsername(username)
	if !domain.ValidCollectibleUsername(username) {
		return domain.CollectibleUsername{}, domain.ErrUsernameInvalid
	}
	return collectibles.CollectibleUsername(ctx, username)
}

// Mint creates a collectible asset, optionally assigning it in the same
// command. An empty URL is rendered from the configured template and an unset
// purchase date is stamped with the service clock, so the stored provenance is
// always complete and reproducible.
func (s *Service) Mint(ctx context.Context, req domain.MintCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	collectibles, err := s.collectibleStore()
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	req.Username = domain.NormalizeUsername(req.Username)
	req.Actor = strings.TrimSpace(req.Actor)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if strings.TrimSpace(req.URL) == "" {
		req.URL = s.CollectibleURL(req.Username)
	}
	if req.PurchaseDate.IsZero() {
		req.PurchaseDate = s.now().UTC()
	}
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	asset, created, err := collectibles.MintCollectibleUsername(ctx, req)
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	if created {
		s.notifyPeers(ctx, req.Owner, asset.Owner)
	}
	return asset, created, nil
}

// Transfer moves the asset to req.To, either out of the vault or from the
// current holder. Both the previous and the new holder are invalidated.
func (s *Service) Transfer(ctx context.Context, req domain.TransferCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	collectibles, err := s.collectibleStore()
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	req.Username = domain.NormalizeUsername(req.Username)
	req.Actor = strings.TrimSpace(req.Actor)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	previousOwner := s.currentOwner(ctx, collectibles, req.Username)
	asset, changed, err := collectibles.TransferCollectibleUsername(ctx, req)
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	if changed {
		s.notifyPeers(ctx, previousOwner, req.To, asset.Owner)
	}
	return asset, changed, nil
}

// Revoke returns the asset to the vault, or burns it when req.Burn is set.
func (s *Service) Revoke(ctx context.Context, req domain.RevokeCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	collectibles, err := s.collectibleStore()
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	req.Username = domain.NormalizeUsername(req.Username)
	req.Actor = strings.TrimSpace(req.Actor)
	req.Reason = strings.TrimSpace(req.Reason)
	req.CommandKey = strings.TrimSpace(req.CommandKey)
	if err := req.Validate(); err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	previousOwner := s.currentOwner(ctx, collectibles, req.Username)
	asset, changed, err := collectibles.RevokeCollectibleUsername(ctx, req)
	if err != nil {
		return domain.CollectibleUsername{}, false, err
	}
	if changed {
		s.notifyPeers(ctx, previousOwner, asset.Owner)
	}
	return asset, changed, nil
}

// List is the admin listing query. The limit is always bounded, so an
// unfiltered operator request can never ask the store for an unbounded scan.
func (s *Service) List(ctx context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error) {
	collectibles, err := s.collectibleStore()
	if err != nil {
		return nil, err
	}
	if filter.Status != "" && !filter.Status.Valid() {
		return nil, domain.ErrCollectibleUsernameStateInvalid
	}
	if filter.Owner.Type != "" && !validPeer(filter.Owner) {
		return nil, domain.ErrCollectibleUsernameStateInvalid
	}
	filter.Query = domain.NormalizeUsername(filter.Query)
	filter.Limit = clampLimit(filter.Limit, defaultListLimit, maxListLimit)
	return collectibles.ListCollectibleUsernames(ctx, filter)
}

// Transfers returns the provenance log of one asset, newest first.
func (s *Service) Transfers(ctx context.Context, collectibleID int64, limit int) ([]domain.CollectibleUsernameTransfer, error) {
	collectibles, err := s.collectibleStore()
	if err != nil {
		return nil, err
	}
	if collectibleID <= 0 {
		return nil, domain.ErrCollectibleUsernameNotFound
	}
	return collectibles.CollectibleUsernameTransfers(ctx, collectibleID, clampLimit(limit, defaultTransferLimit, maxTransferLimit))
}

// CollectibleURL renders the asset landing URL for a name. The operator
// template wins; {username} is substituted when present and appended as a path
// segment when it is not. Without a template the public-link default route is
// used, and without any configured root the URL stays empty rather than
// pointing at an unrelated host.
func (s *Service) CollectibleURL(username string) string {
	if s == nil {
		return ""
	}
	username = domain.NormalizeUsername(username)
	if username == "" {
		return ""
	}
	template := strings.TrimSpace(s.urlTemplate)
	if template != "" {
		if strings.Contains(template, usernamePlaceholder) {
			return strings.ReplaceAll(template, usernamePlaceholder, username)
		}
		return strings.TrimRight(template, "/") + "/" + username
	}
	if strings.TrimSpace(s.publicBaseURL) == "" {
		return ""
	}
	return links.Build(s.publicBaseURL, defaultCollectibleURLPath+"/"+username, nil)
}

// currentOwner reads the holder before a lifecycle mutation so the previous
// peer's projection is invalidated too. It is best effort: a missing or
// unreadable asset only means there is no extra peer to notify, and the
// mutation itself remains the authority.
func (s *Service) currentOwner(ctx context.Context, collectibles store.CollectibleUsernameStore, username string) domain.Peer {
	asset, err := collectibles.CollectibleUsername(ctx, username)
	if err != nil {
		if !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
			s.log.Debug("read collectible username owner before mutation",
				zap.String("username", username),
				zap.Error(err))
		}
		return domain.Peer{}
	}
	if !asset.Owned() {
		return domain.Peer{}
	}
	return asset.Owner
}

// notifyPeers invalidates projections and pushes updates for every distinct
// affected peer. Notification is best effort: the registry mutation already
// committed, and a failed push converges through the client's next
// authoritative peer read.
func (s *Service) notifyPeers(ctx context.Context, peers ...domain.Peer) {
	if s == nil || s.notifier == nil {
		return
	}
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		if !validPeer(peer) {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		if err := s.notifier.NotifyPeerUsernamesChanged(ctx, peer); err != nil {
			s.log.Warn("notify collectible username change failed",
				zap.String("peer_type", string(peer.Type)),
				zap.Int64("peer_id", peer.ID),
				zap.Error(err))
		}
	}
}

func validPeer(peer domain.Peer) bool {
	switch peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		return peer.ID > 0
	default:
		return false
	}
}

func clampLimit(limit, fallback, maximum int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > maximum {
		return maximum
	}
	return limit
}
