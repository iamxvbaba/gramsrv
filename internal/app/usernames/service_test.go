package usernames

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

var (
	testUser    = domain.Peer{Type: domain.PeerTypeUser, ID: 42}
	testChannel = domain.Peer{Type: domain.PeerTypeChannel, ID: 77}
	testClock   = time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
)

type toggleCall struct {
	peer     domain.Peer
	username string
	active   bool
}

// fakeRegistry is an in-memory domain.Username registry recording exactly what
// the service asked it to do, so the tests can assert normalisation reached the
// store and validation did not.
type fakeRegistry struct {
	lists    map[domain.Peer][]domain.Username
	toggles  []toggleCall
	orders   [][]string
	clears   []domain.Peer
	changed  bool
	batchErr error
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{lists: map[domain.Peer][]domain.Username{}, changed: true}
}

func (f *fakeRegistry) PeerUsernames(_ context.Context, peer domain.Peer) ([]domain.Username, error) {
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	return append([]domain.Username(nil), f.lists[peer]...), nil
}

func (f *fakeRegistry) PeerUsernamesBatch(_ context.Context, peers []domain.Peer) (map[domain.Peer][]domain.Username, error) {
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	out := make(map[domain.Peer][]domain.Username, len(peers))
	for _, peer := range peers {
		if list, ok := f.lists[peer]; ok {
			out[peer] = append([]domain.Username(nil), list...)
		}
	}
	return out, nil
}

func (f *fakeRegistry) SetUsernameActive(_ context.Context, peer domain.Peer, username string, active bool) (bool, error) {
	f.toggles = append(f.toggles, toggleCall{peer: peer, username: username, active: active})
	return f.changed, nil
}

func (f *fakeRegistry) ReorderUsernames(_ context.Context, _ domain.Peer, order []string) (bool, error) {
	f.orders = append(f.orders, append([]string(nil), order...))
	return f.changed, nil
}

func (f *fakeRegistry) DeactivateAllUsernames(_ context.Context, peer domain.Peer) (bool, error) {
	f.clears = append(f.clears, peer)
	return f.changed, nil
}

// fakeCollectibles records the lifecycle commands and serves stored assets.
type fakeCollectibles struct {
	assets    map[string]domain.CollectibleUsername
	mints     []domain.MintCollectibleUsernameRequest
	transfers []domain.TransferCollectibleUsernameRequest
	revokes   []domain.RevokeCollectibleUsernameRequest
	deletes   []domain.DeleteCollectibleUsernameRequest
	filters   []domain.CollectibleUsernameFilter
	logLimits []int
	created   bool
	changed   bool
}

func newFakeCollectibles() *fakeCollectibles {
	return &fakeCollectibles{assets: map[string]domain.CollectibleUsername{}, created: true, changed: true}
}

func (f *fakeCollectibles) MintCollectibleUsername(_ context.Context, req domain.MintCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	f.mints = append(f.mints, req)
	asset := domain.CollectibleUsername{
		ID: int64(len(f.mints)), Username: req.Username, Status: domain.CollectibleUsernameStatusVault,
		PurchaseDate: req.PurchaseDate, Currency: req.Currency, Amount: req.Amount, URL: req.URL,
	}
	if req.Owner.Type != "" {
		asset.Status = domain.CollectibleUsernameStatusOwned
		asset.Owner = req.Owner
		asset.OriginalOwner = req.Owner
	}
	f.assets[strings.ToLower(req.Username)] = asset
	return asset, f.created, nil
}

func (f *fakeCollectibles) TransferCollectibleUsername(_ context.Context, req domain.TransferCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	f.transfers = append(f.transfers, req)
	asset := f.assets[strings.ToLower(req.Username)]
	asset.Username = req.Username
	asset.Status = domain.CollectibleUsernameStatusOwned
	asset.Owner = req.To
	asset.TransferCount++
	f.assets[strings.ToLower(req.Username)] = asset
	return asset, f.changed, nil
}

func (f *fakeCollectibles) RevokeCollectibleUsername(_ context.Context, req domain.RevokeCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error) {
	f.revokes = append(f.revokes, req)
	asset := f.assets[strings.ToLower(req.Username)]
	asset.Username = req.Username
	asset.Owner = domain.Peer{}
	asset.Status = domain.CollectibleUsernameStatusVault
	if req.Burn {
		asset.Status = domain.CollectibleUsernameStatusBurned
	}
	f.assets[strings.ToLower(req.Username)] = asset
	return asset, f.changed, nil
}

func (f *fakeCollectibles) DeleteCollectibleUsername(_ context.Context, req domain.DeleteCollectibleUsernameRequest) (bool, error) {
	f.deletes = append(f.deletes, req)
	key := strings.ToLower(req.Username)
	if _, ok := f.assets[key]; !ok {
		return false, nil
	}
	delete(f.assets, key)
	return true, nil
}

func (f *fakeCollectibles) CollectibleUsername(_ context.Context, username string) (domain.CollectibleUsername, error) {
	asset, ok := f.assets[strings.ToLower(username)]
	if !ok {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	return asset, nil
}

func (f *fakeCollectibles) CollectibleUsernameByID(_ context.Context, id int64) (domain.CollectibleUsername, error) {
	for _, asset := range f.assets {
		if asset.ID == id {
			return asset, nil
		}
	}
	return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
}

func (f *fakeCollectibles) ListCollectibleUsernames(_ context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error) {
	f.filters = append(f.filters, filter)
	return nil, nil
}

func (f *fakeCollectibles) CollectibleUsernameTransfers(_ context.Context, _ int64, limit int) ([]domain.CollectibleUsernameTransfer, error) {
	f.logLimits = append(f.logLimits, limit)
	return nil, nil
}

// recordingNotifier captures the peers whose projections were invalidated.
type recordingNotifier struct {
	peers []domain.Peer
	err   error
}

func (n *recordingNotifier) NotifyPeerUsernamesChanged(_ context.Context, peer domain.Peer) error {
	n.peers = append(n.peers, peer)
	return n.err
}

func newTestService(t *testing.T, registry *fakeRegistry, collectibles *fakeCollectibles, opts ...Option) (*Service, *recordingNotifier) {
	t.Helper()
	notifier := &recordingNotifier{}
	base := []Option{
		WithRegistryStore(registry),
		WithCollectibleStore(collectibles),
		WithNotifier(notifier),
		WithClock(func() time.Time { return testClock }),
	}
	return NewService(append(base, opts...)...), notifier
}

func TestPeerUsernamesProjectsStoredOrder(t *testing.T) {
	// Legacy numbering: the editable slot and the first collectible both carry
	// sort_order 0, and the editable slot wins that tie, so a peer that never
	// reordered anything projects its own username first.
	registry := newFakeRegistry()
	registry.lists[testUser] = []domain.Username{
		{Username: "zeta", Active: true, SortOrder: 1, CollectibleID: 2},
		{Username: "alpha", Active: true, SortOrder: 0, CollectibleID: 1},
		{Username: "editable", Active: true, Editable: true, SortOrder: 0},
	}
	service, _ := newTestService(t, registry, newFakeCollectibles())

	if got := projectedNames(t, service); got != "editable,alpha,zeta" {
		t.Fatalf("projection order = %v, want editable,alpha,zeta", got)
	}

	// After a reorder that made a collectible primary, stored order decides and
	// the editable slot is no longer first: clients show usernames[0] as primary.
	registry.lists[testUser] = []domain.Username{
		{Username: "zeta", Active: true, SortOrder: 2, CollectibleID: 2},
		{Username: "alpha", Active: true, SortOrder: 0, CollectibleID: 1},
		{Username: "editable", Active: true, Editable: true, SortOrder: 1},
	}
	if got := projectedNames(t, service); got != "alpha,editable,zeta" {
		t.Fatalf("reordered projection = %v, want alpha,editable,zeta", got)
	}
}

func projectedNames(t *testing.T, service *Service) string {
	t.Helper()
	list, err := service.PeerUsernames(context.Background(), testUser)
	if err != nil {
		t.Fatalf("PeerUsernames: %v", err)
	}
	got := make([]string, 0, len(list))
	for _, item := range list {
		got = append(got, item.Username)
	}
	return strings.Join(got, ",")
}

func TestUsernamesBatchSkipsInvalidAndEmptyPeers(t *testing.T) {
	registry := newFakeRegistry()
	registry.lists[testUser] = []domain.Username{{Username: "alpha", Active: true, CollectibleID: 1}}
	registry.lists[testChannel] = nil
	service, _ := newTestService(t, registry, newFakeCollectibles())

	batch, err := service.UsernamesBatch(context.Background(), []domain.Peer{testUser, testUser, testChannel, {}, {Type: domain.PeerTypeUser}})
	if err != nil {
		t.Fatalf("UsernamesBatch: %v", err)
	}
	if len(batch) != 1 || len(batch[testUser]) != 1 {
		t.Fatalf("batch = %#v, want only the peer holding usernames", batch)
	}
}

func TestToggleUsernameNormalizesBeforeStore(t *testing.T) {
	registry := newFakeRegistry()
	registry.lists[testUser] = []domain.Username{
		{Username: "editable", Active: true, Editable: true},
		{Username: "Nft_One", Active: false, CollectibleID: 1},
	}
	service, notifier := newTestService(t, registry, newFakeCollectibles())

	changed, err := service.ToggleUsername(context.Background(), testUser, "  @Nft_One ", true)
	if err != nil || !changed {
		t.Fatalf("ToggleUsername = %v, %v", changed, err)
	}
	if len(registry.toggles) != 1 || registry.toggles[0].username != "Nft_One" || !registry.toggles[0].active {
		t.Fatalf("store toggles = %#v, want normalized Nft_One", registry.toggles)
	}
	if len(notifier.peers) != 1 || notifier.peers[0] != testUser {
		t.Fatalf("notified peers = %#v, want the toggled peer", notifier.peers)
	}
}

func TestToggleUsernameValidatesBeforeStore(t *testing.T) {
	tests := []struct {
		name     string
		list     []domain.Username
		username string
		active   bool
		wantErr  error
	}{
		{
			name:     "editable slot is not collectible",
			list:     []domain.Username{{Username: "editable", Active: true, Editable: true}},
			username: "editable",
			wantErr:  domain.ErrUsernameNotCollectible,
		},
		{
			name:     "unknown username",
			list:     []domain.Username{{Username: "alpha", Active: true, CollectibleID: 1}},
			username: "missing",
			wantErr:  domain.ErrUsernameNotOccupied,
		},
		{
			name:     "empty username",
			list:     []domain.Username{{Username: "alpha", Active: true, CollectibleID: 1}},
			username: "@",
			wantErr:  domain.ErrUsernameInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := newFakeRegistry()
			registry.lists[testUser] = test.list
			service, notifier := newTestService(t, registry, newFakeCollectibles())

			_, err := service.ToggleUsername(context.Background(), testUser, test.username, test.active)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ToggleUsername error = %v, want %v", err, test.wantErr)
			}
			if len(registry.toggles) != 0 {
				t.Fatalf("store was called with invalid input: %#v", registry.toggles)
			}
			if len(notifier.peers) != 0 {
				t.Fatalf("notifier ran for a rejected toggle: %#v", notifier.peers)
			}
		})
	}
}

func TestReorderUsernamesNormalizesPermutation(t *testing.T) {
	registry := newFakeRegistry()
	registry.lists[testUser] = []domain.Username{
		{Username: "editable", Active: true, Editable: true},
		{Username: "alpha", Active: true, SortOrder: 0, CollectibleID: 1},
		{Username: "zeta", Active: true, SortOrder: 1, CollectibleID: 2},
	}
	service, notifier := newTestService(t, registry, newFakeCollectibles())

	changed, err := service.ReorderUsernames(context.Background(), testUser, []string{"@zeta", " alpha ", "editable"})
	if err != nil || !changed {
		t.Fatalf("ReorderUsernames = %v, %v", changed, err)
	}
	if len(registry.orders) != 1 || strings.Join(registry.orders[0], ",") != "zeta,alpha,editable" {
		t.Fatalf("store order = %#v, want normalized zeta,alpha,editable", registry.orders)
	}
	if len(notifier.peers) != 1 {
		t.Fatalf("notified peers = %#v, want one", notifier.peers)
	}
}

func TestReorderUsernamesRejectsIncompletePermutation(t *testing.T) {
	registry := newFakeRegistry()
	registry.lists[testUser] = []domain.Username{
		{Username: "alpha", Active: true, CollectibleID: 1},
		{Username: "zeta", Active: true, CollectibleID: 2},
	}
	service, _ := newTestService(t, registry, newFakeCollectibles())

	if _, err := service.ReorderUsernames(context.Background(), testUser, []string{"alpha"}); !errors.Is(err, domain.ErrUsernameOrderInvalid) {
		t.Fatalf("ReorderUsernames error = %v, want ErrUsernameOrderInvalid", err)
	}
	if len(registry.orders) != 0 {
		t.Fatalf("store was called with a non-permutation: %#v", registry.orders)
	}
}

// TestReorderUsernamesAcceptsTheEditableSlot is the report "channels.reorderUsernames
// answers USERNAME_INVALID": Telegram Desktop sends the whole visible list, and
// core.telegram.org/api/fragment requires exactly that ("all currently active
// usernames must be specified"), so the editable slot is a legitimate member of
// the order -- including as its first entry, and including when it is the only
// username the peer has.
func TestReorderUsernamesAcceptsTheEditableSlot(t *testing.T) {
	registry := newFakeRegistry()
	registry.lists[testChannel] = []domain.Username{
		{Username: "chan_slot", Active: true, Editable: true},
	}
	service, _ := newTestService(t, registry, newFakeCollectibles())

	if _, err := service.ReorderUsernames(context.Background(), testChannel, []string{"chan_slot"}); err != nil {
		t.Fatalf("editable-only reorder: %v", err)
	}
	if len(registry.orders) != 1 || strings.Join(registry.orders[0], ",") != "chan_slot" {
		t.Fatalf("store order = %#v, want chan_slot", registry.orders)
	}

	// An inactive collectible does not have to be listed, and listing an unknown
	// name is still rejected.
	registry.lists[testChannel] = []domain.Username{
		{Username: "chan_slot", Active: true, Editable: true},
		{Username: "hidden", Active: false, CollectibleID: 7},
	}
	if _, err := service.ReorderUsernames(context.Background(), testChannel, []string{"chan_slot"}); err != nil {
		t.Fatalf("reorder omitting an inactive collectible: %v", err)
	}
	if _, err := service.ReorderUsernames(context.Background(), testChannel, []string{"chan_slot", "nothere"}); !errors.Is(err, domain.ErrUsernameOrderInvalid) {
		t.Fatalf("reorder with an unknown name = %v, want ErrUsernameOrderInvalid", err)
	}
}

func TestDeactivateAllUsernamesNotifiesPeer(t *testing.T) {
	registry := newFakeRegistry()
	service, notifier := newTestService(t, registry, newFakeCollectibles())

	changed, err := service.DeactivateAllUsernames(context.Background(), testChannel)
	if err != nil || !changed {
		t.Fatalf("DeactivateAllUsernames = %v, %v", changed, err)
	}
	if len(registry.clears) != 1 || registry.clears[0] != testChannel {
		t.Fatalf("store clears = %#v", registry.clears)
	}
	if len(notifier.peers) != 1 || notifier.peers[0] != testChannel {
		t.Fatalf("notified peers = %#v", notifier.peers)
	}
}

func TestMintRendersCollectibleURL(t *testing.T) {
	tests := []struct {
		name     string
		opts     []Option
		url      string
		wantURL  string
		username string
	}{
		{
			name:     "public-link default route",
			opts:     []Option{WithPublicBaseURL("https://example.test")},
			username: "alpha",
			wantURL:  "https://example.test/nft/username/alpha",
		},
		{
			name:     "template placeholder",
			opts:     []Option{WithURLTemplate("https://frag.example/u/{username}?ref=1"), WithPublicBaseURL("https://example.test")},
			username: "alpha",
			wantURL:  "https://frag.example/u/alpha?ref=1",
		},
		{
			name:     "template without placeholder appends the name",
			opts:     []Option{WithURLTemplate("https://frag.example/u/")},
			username: "alpha",
			wantURL:  "https://frag.example/u/alpha",
		},
		{
			name:     "explicit request URL wins",
			opts:     []Option{WithURLTemplate("https://frag.example/u/{username}")},
			username: "alpha",
			url:      "https://operator.example/custom",
			wantURL:  "https://operator.example/custom",
		},
		{
			name:     "no template and no base URL keeps the URL empty",
			username: "alpha",
			wantURL:  "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collectibles := newFakeCollectibles()
			service, _ := newTestService(t, newFakeRegistry(), collectibles, test.opts...)

			asset, created, err := service.Mint(context.Background(), domain.MintCollectibleUsernameRequest{
				Username: "@" + test.username, Currency: domain.CollectibleCurrencyStars, Amount: 1000, URL: test.url,
			})
			if err != nil || !created {
				t.Fatalf("Mint = %v, %v", created, err)
			}
			if asset.URL != test.wantURL {
				t.Fatalf("asset URL = %q, want %q", asset.URL, test.wantURL)
			}
			if len(collectibles.mints) != 1 {
				t.Fatalf("mints = %d, want 1", len(collectibles.mints))
			}
			if got := collectibles.mints[0].Username; got != test.username {
				t.Fatalf("stored username = %q, want normalized %q", got, test.username)
			}
			if !collectibles.mints[0].PurchaseDate.Equal(testClock) {
				t.Fatalf("purchase date = %v, want the service clock %v", collectibles.mints[0].PurchaseDate, testClock)
			}
		})
	}
}

func TestMintValidatesBeforeStore(t *testing.T) {
	tests := []struct {
		name    string
		req     domain.MintCollectibleUsernameRequest
		wantErr error
	}{
		{
			name:    "username too short",
			req:     domain.MintCollectibleUsernameRequest{Username: "ab", Currency: domain.CollectibleCurrencyStars},
			wantErr: domain.ErrUsernameInvalid,
		},
		{
			name:    "unsupported currency",
			req:     domain.MintCollectibleUsernameRequest{Username: "alpha", Currency: "EUR"},
			wantErr: domain.ErrCollectibleCurrencyInvalid,
		},
		{
			name:    "crypto amount without currency",
			req:     domain.MintCollectibleUsernameRequest{Username: "alpha", Currency: domain.CollectibleCurrencyStars, CryptoAmount: 5},
			wantErr: domain.ErrCollectibleCurrencyInvalid,
		},
		{
			name: "owner peer is not a username holder",
			req: domain.MintCollectibleUsernameRequest{
				Username: "alpha", Currency: domain.CollectibleCurrencyStars,
				Owner: domain.Peer{Type: domain.PeerTypeCommunity, ID: 5},
			},
			wantErr: domain.ErrCollectibleUsernameStateInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collectibles := newFakeCollectibles()
			service, notifier := newTestService(t, newFakeRegistry(), collectibles)

			if _, _, err := service.Mint(context.Background(), test.req); !errors.Is(err, test.wantErr) {
				t.Fatalf("Mint error = %v, want %v", err, test.wantErr)
			}
			if len(collectibles.mints) != 0 {
				t.Fatalf("store was called with invalid input: %#v", collectibles.mints)
			}
			if len(notifier.peers) != 0 {
				t.Fatalf("notifier ran for a rejected mint: %#v", notifier.peers)
			}
		})
	}
}

func TestMintNotifiesOwnerOnly(t *testing.T) {
	collectibles := newFakeCollectibles()
	service, notifier := newTestService(t, newFakeRegistry(), collectibles, WithPublicBaseURL("https://example.test"))

	if _, _, err := service.Mint(context.Background(), domain.MintCollectibleUsernameRequest{
		Username: "vaulted", Currency: domain.CollectibleCurrencyStars,
	}); err != nil {
		t.Fatalf("Mint vault: %v", err)
	}
	if len(notifier.peers) != 0 {
		t.Fatalf("vault mint notified %#v, want nothing", notifier.peers)
	}

	if _, _, err := service.Mint(context.Background(), domain.MintCollectibleUsernameRequest{
		Username: "assigned", Currency: domain.CollectibleCurrencyStars, Owner: testUser,
	}); err != nil {
		t.Fatalf("Mint assigned: %v", err)
	}
	if len(notifier.peers) != 1 || notifier.peers[0] != testUser {
		t.Fatalf("notified peers = %#v, want the assigned owner", notifier.peers)
	}
}

func TestTransferNotifiesPreviousAndNewOwner(t *testing.T) {
	collectibles := newFakeCollectibles()
	collectibles.assets["alpha"] = domain.CollectibleUsername{
		ID: 1, Username: "alpha", Status: domain.CollectibleUsernameStatusOwned, Owner: testUser,
	}
	service, notifier := newTestService(t, newFakeRegistry(), collectibles)

	_, changed, err := service.Transfer(context.Background(), domain.TransferCollectibleUsernameRequest{
		Username: "@Alpha", To: testChannel, Actor: "admin", CommandKey: "cmd-1",
	})
	if err != nil || !changed {
		t.Fatalf("Transfer = %v, %v", changed, err)
	}
	if len(collectibles.transfers) != 1 || collectibles.transfers[0].Username != "Alpha" {
		t.Fatalf("stored transfer = %#v, want normalized username", collectibles.transfers)
	}
	if len(notifier.peers) != 2 {
		t.Fatalf("notified peers = %#v, want previous and new owner", notifier.peers)
	}
	seen := map[domain.Peer]bool{notifier.peers[0]: true, notifier.peers[1]: true}
	if !seen[testUser] || !seen[testChannel] {
		t.Fatalf("notified peers = %#v, want %v and %v", notifier.peers, testUser, testChannel)
	}
}

func TestRevokeNotifiesPreviousOwner(t *testing.T) {
	collectibles := newFakeCollectibles()
	collectibles.assets["alpha"] = domain.CollectibleUsername{
		ID: 1, Username: "alpha", Status: domain.CollectibleUsernameStatusOwned, Owner: testUser,
	}
	service, notifier := newTestService(t, newFakeRegistry(), collectibles)

	asset, changed, err := service.Revoke(context.Background(), domain.RevokeCollectibleUsernameRequest{
		Username: "alpha", Burn: true, Actor: "admin",
	})
	if err != nil || !changed {
		t.Fatalf("Revoke = %v, %v", changed, err)
	}
	if asset.Status != domain.CollectibleUsernameStatusBurned {
		t.Fatalf("asset status = %q, want burned", asset.Status)
	}
	if len(notifier.peers) != 1 || notifier.peers[0] != testUser {
		t.Fatalf("notified peers = %#v, want the previous owner", notifier.peers)
	}
}

func TestCollectibleInfoProjectsPurchaseRecord(t *testing.T) {
	collectibles := newFakeCollectibles()
	collectibles.assets["alpha"] = domain.CollectibleUsername{
		ID: 1, Username: "alpha", Status: domain.CollectibleUsernameStatusOwned, Owner: testUser,
		PurchaseDate: testClock, Currency: domain.CollectibleCurrencyStars, Amount: 2500,
		URL: "https://example.test/nft/username/alpha",
	}
	service, _ := newTestService(t, newFakeRegistry(), collectibles)

	info, err := service.CollectibleInfo(context.Background(), "@ALPHA")
	if err != nil {
		t.Fatalf("CollectibleInfo: %v", err)
	}
	if info.PurchaseDate != int(testClock.Unix()) || info.Amount != 2500 || info.Currency != domain.CollectibleCurrencyStars {
		t.Fatalf("collectible info = %#v", info)
	}
	if _, err := service.CollectibleInfo(context.Background(), "ab"); !errors.Is(err, domain.ErrUsernameInvalid) {
		t.Fatalf("CollectibleInfo short name error = %v, want ErrUsernameInvalid", err)
	}
}

func TestListAndTransfersBoundThePage(t *testing.T) {
	collectibles := newFakeCollectibles()
	service, _ := newTestService(t, newFakeRegistry(), collectibles)

	if _, err := service.List(context.Background(), domain.CollectibleUsernameFilter{Query: " @Alpha ", Limit: 0}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := service.List(context.Background(), domain.CollectibleUsernameFilter{Limit: 100000}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(collectibles.filters) != 2 ||
		collectibles.filters[0].Limit != defaultListLimit || collectibles.filters[0].Query != "Alpha" ||
		collectibles.filters[1].Limit != maxListLimit {
		t.Fatalf("filters = %#v", collectibles.filters)
	}
	if _, err := service.List(context.Background(), domain.CollectibleUsernameFilter{Status: "sold"}); !errors.Is(err, domain.ErrCollectibleUsernameStateInvalid) {
		t.Fatalf("List accepted an unmodelled status")
	}

	if _, err := service.Transfers(context.Background(), 7, 0); err != nil {
		t.Fatalf("Transfers: %v", err)
	}
	if len(collectibles.logLimits) != 1 || collectibles.logLimits[0] != defaultTransferLimit {
		t.Fatalf("transfer log limits = %#v", collectibles.logLimits)
	}
	if _, err := service.Transfers(context.Background(), 0, 10); !errors.Is(err, domain.ErrCollectibleUsernameNotFound) {
		t.Fatalf("Transfers accepted a zero collectible id")
	}
}

func TestServiceWithoutStoresReportsConfiguration(t *testing.T) {
	service := NewService()
	if service.Configured() {
		t.Fatal("Configured = true without stores")
	}
	if _, err := service.PeerUsernames(context.Background(), testUser); err == nil {
		t.Fatal("PeerUsernames accepted a missing registry store")
	}
	if _, err := service.ToggleUsername(context.Background(), testUser, "alpha", true); err == nil {
		t.Fatal("ToggleUsername accepted a missing registry store")
	}
	if _, _, err := service.Mint(context.Background(), domain.MintCollectibleUsernameRequest{Username: "alpha"}); err == nil {
		t.Fatal("Mint accepted a missing collectible store")
	}
}

func TestNilServiceIsSafe(t *testing.T) {
	var service *Service
	service.SetPeerUsernameNotifier(&recordingNotifier{})
	if service.Configured() {
		t.Fatal("nil service reported configured")
	}
	if url := service.CollectibleURL("alpha"); url != "" {
		t.Fatalf("nil service URL = %q", url)
	}
	if _, err := service.PeerUsernames(context.Background(), testUser); err == nil {
		t.Fatal("nil service PeerUsernames returned no error")
	}
	if _, _, err := service.Transfer(context.Background(), domain.TransferCollectibleUsernameRequest{Username: "alpha", To: testUser}); err == nil {
		t.Fatal("nil service Transfer returned no error")
	}
}

func TestNotifierFailureDoesNotFailTheMutation(t *testing.T) {
	registry := newFakeRegistry()
	registry.lists[testUser] = []domain.Username{
		{Username: "editable", Active: true, Editable: true},
		{Username: "alpha", Active: true, CollectibleID: 1},
	}
	notifier := &recordingNotifier{err: errors.New("push failed")}
	service := NewService(
		WithRegistryStore(registry),
		WithCollectibleStore(newFakeCollectibles()),
		WithNotifier(notifier),
	)

	changed, err := service.ToggleUsername(context.Background(), testUser, "alpha", false)
	if err != nil || !changed {
		t.Fatalf("ToggleUsername = %v, %v; committed mutation must survive a failed push", changed, err)
	}
}

// TestServiceDeleteNotifiesPreviousOwner covers the hard delete: the request is
// normalised and validated before the store is touched, and the peer that held
// the asset is invalidated so its projection stops advertising the username.
func TestServiceDeleteNotifiesPreviousOwner(t *testing.T) {
	ctx := context.Background()
	registry := newFakeRegistry()
	collectibles := newFakeCollectibles()
	holder := domain.Peer{Type: domain.PeerTypeUser, ID: 501}
	collectibles.assets["gone"] = domain.CollectibleUsername{
		ID: 9, Username: "Gone", Status: domain.CollectibleUsernameStatusOwned, Owner: holder,
	}
	svc, notifier := newTestService(t, registry, collectibles)

	deleted, err := svc.Delete(ctx, domain.DeleteCollectibleUsernameRequest{
		Username: " @Gone ", Actor: "admin", Reason: "issued by mistake",
	})
	if err != nil || !deleted {
		t.Fatalf("delete: deleted=%v err=%v", deleted, err)
	}
	if len(collectibles.deletes) != 1 || collectibles.deletes[0].Username != "Gone" {
		t.Fatalf("store received %+v, want the normalised name", collectibles.deletes)
	}
	if len(notifier.peers) != 1 || notifier.peers[0] != holder {
		t.Fatalf("notified peers = %#v, want the previous owner %+v", notifier.peers, holder)
	}

	// An invalid name never reaches the store.
	before := len(collectibles.deletes)
	if _, err := svc.Delete(ctx, domain.DeleteCollectibleUsernameRequest{Username: "no"}); err == nil {
		t.Fatalf("delete of a too-short name = nil error, want rejection")
	}
	if len(collectibles.deletes) != before {
		t.Fatalf("store was called with an invalid request: %+v", collectibles.deletes)
	}

	// Nothing live left is not an error, and nothing is notified.
	notifier.peers = nil
	deleted, err = svc.Delete(ctx, domain.DeleteCollectibleUsernameRequest{
		Username: "absentname", Actor: "admin", Reason: "again",
	})
	if err != nil || deleted {
		t.Fatalf("delete of unknown name = %v err=%v, want (false, nil)", deleted, err)
	}
	if len(notifier.peers) != 0 {
		t.Fatalf("no-op delete notified %+v", notifier.peers)
	}
}
