package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// fakeUsernameRegistry is an in-memory UsernameRegistryService. It enforces the
// same domain rules the real registry does, so the RPC tests exercise the actual
// error mapping rather than hand-rolled sentinels.
type fakeUsernameRegistry struct {
	byPeer       map[domain.Peer][]domain.Username
	collectibles map[string]domain.CollectibleUsername
	// err, when set, fails every read. Used for the degradation tests.
	err error
	// batchCalls / peerCalls count read fan-out so the tests can assert no N+1.
	batchCalls int
	peerCalls  int
}

func newFakeUsernameRegistry() *fakeUsernameRegistry {
	return &fakeUsernameRegistry{
		byPeer:       make(map[domain.Peer][]domain.Username),
		collectibles: make(map[string]domain.CollectibleUsername),
	}
}

func (f *fakeUsernameRegistry) PeerUsernames(_ context.Context, peer domain.Peer) ([]domain.Username, error) {
	f.peerCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.byPeer[peer], nil
}

func (f *fakeUsernameRegistry) UsernamesBatch(_ context.Context, peers []domain.Peer) (map[domain.Peer][]domain.Username, error) {
	f.batchCalls++
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[domain.Peer][]domain.Username, len(peers))
	for _, peer := range peers {
		if list, ok := f.byPeer[peer]; ok {
			out[peer] = list
		}
	}
	return out, nil
}

func (f *fakeUsernameRegistry) ToggleUsername(_ context.Context, peer domain.Peer, username string, active bool) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	current := f.byPeer[peer]
	if err := domain.ValidateUsernameToggle(current, username, active); err != nil {
		return false, err
	}
	changed := false
	next := append([]domain.Username(nil), current...)
	for i := range next {
		if next[i].Username != domain.NormalizeUsername(username) {
			continue
		}
		if next[i].Active != active {
			next[i].Active = active
			changed = true
		}
	}
	f.byPeer[peer] = next
	return changed, nil
}

func (f *fakeUsernameRegistry) ReorderUsernames(_ context.Context, peer domain.Peer, order []string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	current := f.byPeer[peer]
	next, err := domain.ApplyUsernameReorder(current, order)
	if err != nil {
		return false, err
	}
	// Same contract as both real backends: the renumbering is always persisted,
	// and "changed" is only about what a client can see.
	changed := !domain.SameUsernameOrder(current, next)
	f.byPeer[peer] = next
	return changed, nil
}

func (f *fakeUsernameRegistry) DeactivateAllUsernames(_ context.Context, peer domain.Peer) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	next := append([]domain.Username(nil), f.byPeer[peer]...)
	changed := false
	for i := range next {
		if next[i].Collectible() && next[i].Active {
			next[i].Active = false
			changed = true
		}
	}
	f.byPeer[peer] = next
	return changed, nil
}

func (f *fakeUsernameRegistry) Collectible(_ context.Context, username string) (domain.CollectibleUsername, error) {
	if f.err != nil {
		return domain.CollectibleUsername{}, f.err
	}
	// Usernames are case-insensitive in the registry, as they are in the store.
	asset, ok := f.collectibles[strings.ToLower(domain.NormalizeUsername(username))]
	if !ok {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	return asset, nil
}

var _ UsernameRegistryService = (*fakeUsernameRegistry)(nil)

func TestTgUsernamesFromRegistryFollowsStoredOrder(t *testing.T) {
	// Legacy numbering, which is what every peer that never reordered carries:
	// the editable slot and the first collectible share sort_order 0 and the
	// editable slot wins the tie, so it still projects first.
	assertUsernameVector(t,
		[]domain.Username{
			{Username: "second", Active: false, SortOrder: 1, CollectibleID: 22},
			{Username: "editable_slot", Active: true, Editable: true, SortOrder: 0},
			{Username: "first", Active: true, SortOrder: 0, CollectibleID: 11},
		},
		[]tg.Username{
			{Editable: true, Active: true, Username: "editable_slot"},
			{Editable: false, Active: true, Username: "first"},
			{Editable: false, Active: false, Username: "second"},
		})

	// After a reorder made a collectible primary, stored order wins: clients read
	// usernames[0] as the peer's primary username
	// (core.telegram.org/api/fragment), so the editable slot must be able to
	// leave that position.
	assertUsernameVector(t,
		[]domain.Username{
			{Username: "second", Active: false, SortOrder: 2, CollectibleID: 22},
			{Username: "editable_slot", Active: true, Editable: true, SortOrder: 1},
			{Username: "first", Active: true, SortOrder: 0, CollectibleID: 11},
		},
		[]tg.Username{
			{Editable: false, Active: true, Username: "first"},
			{Editable: true, Active: true, Username: "editable_slot"},
			{Editable: false, Active: false, Username: "second"},
		})
}

func assertUsernameVector(t *testing.T, list []domain.Username, want []tg.Username) {
	t.Helper()
	got := tgUsernamesFromRegistry(list, "editable_slot")
	if len(got) != len(want) {
		t.Fatalf("usernames = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Username != want[i].Username || got[i].Editable != want[i].Editable || got[i].Active != want[i].Active {
			t.Fatalf("usernames[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestTgUsernamesFromRegistryDegradesToScalar(t *testing.T) {
	// Empty registry contribution must be byte-identical to the legacy vector.
	if got, want := tgUsernamesFromRegistry(nil, "legacy"), tgUsernames("legacy"); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("empty registry = %+v, want %+v", got, want)
	}
	if got := tgUsernamesFromRegistry(nil, ""); got != nil {
		t.Fatalf("empty registry with empty scalar = %+v, want nil", got)
	}
	// A registry list that normalizes away entirely also degrades rather than
	// emitting an empty vector.
	if got, want := tgUsernamesFromRegistry([]domain.Username{{Username: "  "}}, "legacy"), tgUsernames("legacy"); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("blank registry rows = %+v, want %+v", got, want)
	}
}

type usernameProjectionFixture struct {
	router   *Router
	registry *fakeUsernameRegistry
	owner    domain.User
	friend   domain.User
}

func newUsernameProjectionFixture(t *testing.T, registry UsernameRegistryService) usernameProjectionFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002001", FirstName: "Owner", Username: "owner_slot"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002002", FirstName: "Friend", Username: "friend_slot"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	fake, _ := registry.(*fakeUsernameRegistry)
	return usernameProjectionFixture{
		router: New(Config{}, Deps{
			Users:     appusers.NewService(userStore),
			Usernames: registry,
		}, zaptest.NewLogger(t), clock.System),
		registry: fake,
		owner:    owner,
		friend:   friend,
	}
}

func usernameStrings(list []tg.Username) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.Username)
	}
	return out
}

func TestUsersGetUsersProjectsCollectibleUsernamesInOneBatch(t *testing.T) {
	registry := newFakeUsernameRegistry()
	f := newUsernameProjectionFixture(t, registry)
	registry.byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}] = []domain.Username{
		{Username: "owner_slot", Editable: true, Active: true},
		{Username: "nft", Active: true, SortOrder: 0, CollectibleID: 7},
	}
	registry.byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: f.friend.ID}] = []domain.Username{
		{Username: "friend_slot", Editable: true, Active: true},
		{Username: "gem", Active: false, SortOrder: 0, CollectibleID: 8},
	}
	ctx := WithUserID(context.Background(), f.owner.ID)

	out, err := f.router.onUsersGetUsers(ctx, []tg.InputUserClass{
		&tg.InputUserSelf{},
		&tg.InputUser{UserID: f.friend.ID, AccessHash: f.friend.AccessHash},
	})
	if err != nil {
		t.Fatalf("get users: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("users = %d, want 2", len(out))
	}
	self := out[0].(*tg.User)
	if _, ok := self.GetUsername(); ok {
		t.Fatalf("self scalar username is set together with collectible vector")
	}
	vector, ok := self.GetUsernames()
	if !ok {
		t.Fatalf("self usernames unset, want registry vector")
	}
	if got := usernameStrings(vector); len(got) != 2 || got[0] != "owner_slot" || got[1] != "nft" {
		t.Fatalf("self usernames = %v, want [owner_slot nft]", got)
	}
	if !vector[0].Editable || !vector[0].Active {
		t.Fatalf("editable slot flags = %+v, want editable+active", vector[0])
	}
	if vector[1].Editable || !vector[1].Active {
		t.Fatalf("collectible flags = %+v, want non-editable+active", vector[1])
	}
	friend := out[1].(*tg.User)
	friendVector, _ := friend.GetUsernames()
	if got := usernameStrings(friendVector); len(got) != 2 || got[1] != "gem" {
		t.Fatalf("friend usernames = %v, want [friend_slot gem]", got)
	}
	if friendVector[1].Active {
		t.Fatalf("inactive collectible projected active: %+v", friendVector[1])
	}
	// Two users, one batch read: no N+1.
	if registry.batchCalls != 1 || registry.peerCalls != 0 {
		t.Fatalf("registry reads = batch %d / peer %d, want batch 1 / peer 0", registry.batchCalls, registry.peerCalls)
	}
}

func TestUsersGetUsersDegradesWithoutRegistry(t *testing.T) {
	f := newUsernameProjectionFixture(t, nil)
	ctx := WithUserID(context.Background(), f.owner.ID)

	out, err := f.router.onUsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil {
		t.Fatalf("get users: %v", err)
	}
	self := out[0].(*tg.User)
	// Without a collectible registry the official legacy shape is scalar-only.
	self.SetFlags()
	vector, ok := self.GetUsernames()
	if ok || len(vector) != 0 {
		t.Fatalf("usernames = %+v (set %v), want vector absent", vector, ok)
	}
	if scalar, ok := self.GetUsername(); !ok || scalar != "owner_slot" {
		t.Fatalf("scalar username = %q (set %v), want owner_slot", scalar, ok)
	}
}

func TestUsersGetUsersDegradesWhenRegistryFails(t *testing.T) {
	registry := newFakeUsernameRegistry()
	registry.err = errors.New("registry unavailable")
	f := newUsernameProjectionFixture(t, registry)
	ctx := WithUserID(context.Background(), f.owner.ID)

	out, err := f.router.onUsersGetUsers(ctx, []tg.InputUserClass{
		&tg.InputUserSelf{},
		&tg.InputUser{UserID: f.friend.ID, AccessHash: f.friend.AccessHash},
	})
	if err != nil {
		t.Fatalf("get users must not fail when the registry does: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("users = %d, want 2", len(out))
	}
	for i, want := range []string{"owner_slot", "friend_slot"} {
		user := out[i].(*tg.User)
		user.SetFlags()
		vector, ok := user.GetUsernames()
		if ok || len(vector) != 0 {
			t.Fatalf("users[%d] usernames = %+v, want vector absent", i, vector)
		}
		if scalar, ok := user.GetUsername(); !ok || scalar != want {
			t.Fatalf("users[%d] scalar username = %q (set %v), want %q", i, scalar, ok, want)
		}
	}
}

func TestFragmentGetCollectibleInfoRPC(t *testing.T) {
	registry := newFakeUsernameRegistry()
	f := newUsernameProjectionFixture(t, registry)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}
	registry.collectibles["nfts"] = domain.CollectibleUsername{
		ID:             7,
		Username:       "nfts",
		Status:         domain.CollectibleUsernameStatusOwned,
		Owner:          peer,
		PurchaseDate:   time.Unix(1700000000, 0),
		Currency:       domain.CollectibleCurrencyUSD,
		Amount:         550000,
		CryptoCurrency: domain.CollectibleCryptoCurrencyTON,
		CryptoAmount:   1200000000,
		URL:            "https://fragment.example/username/nfts",
	}
	registry.byPeer[peer] = []domain.Username{{Username: "nfts", Active: false, CollectibleID: 7}}
	ctx := WithUserID(context.Background(), f.owner.ID)

	info, err := f.router.onFragmentGetCollectibleInfo(ctx, &tg.FragmentGetCollectibleInfoRequest{
		Collectible: &tg.InputCollectibleUsername{Username: "@NFTS"},
	})
	if err != nil {
		t.Fatalf("get collectible info: %v", err)
	}
	if info.PurchaseDate != 1700000000 || info.Currency != domain.CollectibleCurrencyUSD || info.Amount != 550000 {
		t.Fatalf("collectible info = %+v, want the stored purchase record", info)
	}
	if info.CryptoCurrency != domain.CollectibleCryptoCurrencyTON || info.CryptoAmount != 1200000000 {
		t.Fatalf("collectible crypto = %s/%d, want TON/1200000000", info.CryptoCurrency, info.CryptoAmount)
	}
	if info.URL != "https://fragment.example/username/nfts" {
		t.Fatalf("collectible url = %q", info.URL)
	}

	// Inactive collectibles are private to their current owner.
	friendCtx := WithUserID(context.Background(), f.friend.ID)
	if _, err := f.router.onFragmentGetCollectibleInfo(friendCtx, &tg.FragmentGetCollectibleInfoRequest{
		Collectible: &tg.InputCollectibleUsername{Username: "nfts"},
	}); !tgerr.Is(err, "COLLECTIBLE_NOT_FOUND") {
		t.Fatalf("inactive collectible seen by another user: %v, want COLLECTIBLE_NOT_FOUND", err)
	}
	registry.byPeer[peer][0].Active = true
	if _, err := f.router.onFragmentGetCollectibleInfo(friendCtx, &tg.FragmentGetCollectibleInfoRequest{
		Collectible: &tg.InputCollectibleUsername{Username: "nfts"},
	}); err != nil {
		t.Fatalf("active collectible hidden from another user: %v", err)
	}

	// A name with no collectible asset behind it is not occupied as a collectible.
	if _, err := f.router.onFragmentGetCollectibleInfo(ctx, &tg.FragmentGetCollectibleInfoRequest{
		Collectible: &tg.InputCollectibleUsername{Username: "owner_slot"},
	}); !tgerr.Is(err, "COLLECTIBLE_NOT_FOUND") {
		t.Fatalf("non-collectible username err = %v, want COLLECTIBLE_NOT_FOUND", err)
	}
	// Syntactically impossible name is rejected before the registry is consulted.
	if _, err := f.router.onFragmentGetCollectibleInfo(ctx, &tg.FragmentGetCollectibleInfoRequest{
		Collectible: &tg.InputCollectibleUsername{Username: "a"},
	}); !tgerr.Is(err, "COLLECTIBLE_INVALID") {
		t.Fatalf("malformed username err = %v, want COLLECTIBLE_INVALID", err)
	}
	// Collectible phones do not exist in this server.
	if _, err := f.router.onFragmentGetCollectibleInfo(ctx, &tg.FragmentGetCollectibleInfoRequest{
		Collectible: &tg.InputCollectiblePhone{Phone: "15550002001"},
	}); !tgerr.Is(err, "COLLECTIBLE_NOT_FOUND") {
		t.Fatalf("collectible phone err = %v, want COLLECTIBLE_NOT_FOUND", err)
	}
}

func TestFragmentGetCollectibleInfoWithoutRegistry(t *testing.T) {
	f := newUsernameProjectionFixture(t, nil)
	ctx := WithUserID(context.Background(), f.owner.ID)

	if _, err := f.router.onFragmentGetCollectibleInfo(ctx, &tg.FragmentGetCollectibleInfoRequest{
		Collectible: &tg.InputCollectibleUsername{Username: "owner_slot"},
	}); !tgerr.Is(err, "COLLECTIBLE_NOT_FOUND") {
		t.Fatalf("no registry err = %v, want COLLECTIBLE_NOT_FOUND", err)
	}
}

func TestAccountToggleAndReorderUsernamesRPC(t *testing.T) {
	registry := newFakeUsernameRegistry()
	f := newUsernameProjectionFixture(t, registry)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}
	registry.byPeer[peer] = []domain.Username{
		{Username: "owner_slot", Editable: true, Active: true},
		{Username: "alpha", Active: true, SortOrder: 0, CollectibleID: 1},
		{Username: "bravo", Active: true, SortOrder: 1, CollectibleID: 2},
	}
	ctx := WithUserID(context.Background(), f.owner.ID)

	if ok, err := f.router.onAccountToggleUsername(ctx, &tg.AccountToggleUsernameRequest{Username: "alpha", Active: false}); !ok || err != nil {
		t.Fatalf("toggle collectible = %v,%v, want true,nil", ok, err)
	}
	if list := registry.byPeer[peer]; list[1].Active {
		t.Fatalf("alpha still active after toggle: %+v", list)
	}
	// Toggling the same value again changes nothing.
	if ok, err := f.router.onAccountToggleUsername(ctx, &tg.AccountToggleUsernameRequest{Username: "alpha", Active: false}); ok || !tgerr.Is(err, "USERNAME_NOT_MODIFIED") {
		t.Fatalf("idempotent toggle = %v,%v, want false,USERNAME_NOT_MODIFIED", ok, err)
	}
	// The editable slot is not a collectible: account.updateUsername owns it.
	if ok, err := f.router.onAccountToggleUsername(ctx, &tg.AccountToggleUsernameRequest{Username: "owner_slot", Active: false}); ok || !tgerr.Is(err, "USERNAME_INVALID") {
		t.Fatalf("toggle editable slot = %v,%v, want false,USERNAME_INVALID", ok, err)
	}
	// An unknown name is not occupied.
	if ok, err := f.router.onAccountToggleUsername(ctx, &tg.AccountToggleUsernameRequest{Username: "ghost", Active: true}); ok || !tgerr.Is(err, "USERNAME_NOT_OCCUPIED") {
		t.Fatalf("toggle unknown = %v,%v, want false,USERNAME_NOT_OCCUPIED", ok, err)
	}

	// alpha is inactive at this point, so the order does not have to mention it.
	if ok, err := f.router.onAccountReorderUsernames(ctx, &tg.AccountReorderUsernamesRequest{Order: []string{"owner_slot", "bravo"}}); !ok || err != nil {
		t.Fatalf("reorder = %v,%v, want true,nil", ok, err)
	}
	list := domain.SortUsernames(registry.byPeer[peer])
	if len(list) != 3 || !list[0].Editable || list[1].Username != "bravo" || list[2].Username != "alpha" {
		t.Fatalf("order after reorder = %+v, want editable, bravo, alpha", list)
	}
	// The editable slot may lead the order or follow a collectible.
	if ok, err := f.router.onAccountReorderUsernames(ctx, &tg.AccountReorderUsernamesRequest{Order: []string{"bravo", "owner_slot"}}); !ok || err != nil {
		t.Fatalf("reorder with a collectible first = %v,%v, want true,nil", ok, err)
	}
	if list := domain.SortUsernames(registry.byPeer[peer]); list[0].Username != "bravo" || !list[1].Editable {
		t.Fatalf("order after promoting a collectible = %+v, want bravo, editable, alpha", list)
	}
	// An order that omits an active username is still rejected.
	if ok, err := f.router.onAccountReorderUsernames(ctx, &tg.AccountReorderUsernamesRequest{Order: []string{"bravo"}}); ok || !tgerr.Is(err, "ORDER_INVALID") {
		t.Fatalf("partial reorder = %v,%v, want false,ORDER_INVALID", ok, err)
	}
	// So is one naming something the peer does not own.
	if ok, err := f.router.onAccountReorderUsernames(ctx, &tg.AccountReorderUsernamesRequest{Order: []string{"owner_slot", "bravo", "ghost"}}); ok || !tgerr.Is(err, "ORDER_INVALID") {
		t.Fatalf("reorder with an unknown name = %v,%v, want false,ORDER_INVALID", ok, err)
	}
	if ok, err := f.router.onAccountReorderUsernames(ctx, &tg.AccountReorderUsernamesRequest{
		Order: make([]string, domain.MaxPeerCollectibleUsernames+2),
	}); ok || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("oversized reorder = %v,%v, want false,LIMIT_INVALID", ok, err)
	}
}

func TestAccountUsernameManagementWithoutRegistry(t *testing.T) {
	f := newUsernameProjectionFixture(t, nil)
	ctx := WithUserID(context.Background(), f.owner.ID)

	if ok, err := f.router.onAccountToggleUsername(ctx, &tg.AccountToggleUsernameRequest{Username: "owner_slot", Active: true}); ok || !tgerr.Is(err, "USERNAME_NOT_MODIFIED") {
		t.Fatalf("toggle without registry = %v,%v, want false,USERNAME_NOT_MODIFIED", ok, err)
	}
	if ok, err := f.router.onAccountReorderUsernames(ctx, &tg.AccountReorderUsernamesRequest{Order: []string{"owner_slot"}}); ok || !tgerr.Is(err, "USERNAME_NOT_MODIFIED") {
		t.Fatalf("reorder without registry = %v,%v, want false,USERNAME_NOT_MODIFIED", ok, err)
	}
}

// TestCollectibleUsernameRPCsAreRegistered proves the three previously missing
// methods reach a handler through the real dispatcher rather than the unregistered
// fallback: an ordinary client calls them by wire constructor, not by Go method.
func TestCollectibleUsernameRPCsAreRegistered(t *testing.T) {
	f := newUsernameProjectionFixture(t, nil)
	ctx := WithUserID(context.Background(), f.owner.ID)
	cases := []struct {
		name string
		req  bin.Encoder
		want string
	}{
		{name: "account.reorderUsernames", req: &tg.AccountReorderUsernamesRequest{Order: []string{"owner_slot"}}, want: "USERNAME_NOT_MODIFIED"},
		{name: "account.toggleUsername", req: &tg.AccountToggleUsernameRequest{Username: "owner_slot", Active: true}, want: "USERNAME_NOT_MODIFIED"},
		{name: "fragment.getCollectibleInfo", req: &tg.FragmentGetCollectibleInfoRequest{Collectible: &tg.InputCollectibleUsername{Username: "owner_slot"}}, want: "COLLECTIBLE_NOT_FOUND"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var in bin.Buffer
			if err := tt.req.Encode(&in); err != nil {
				t.Fatalf("encode request: %v", err)
			}
			if _, err := f.router.Dispatch(ctx, [8]byte{}, 0, &in); !tgerr.Is(err, tt.want) {
				t.Fatalf("dispatch err = %v, want %s", err, tt.want)
			}
		})
	}
}

func newCollectibleChannelFixture(t *testing.T, registry UsernameRegistryService) (*Router, domain.User, *tg.Channel) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550003001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	r := New(Config{}, Deps{
		Users:     appusers.NewService(userStore),
		Channels:  appchannels.NewService(channelStore),
		Usernames: registry,
	}, zaptest.NewLogger(t), clock.System)
	ownerCtx := WithUserID(ctx, owner.ID)
	created, err := r.onChannelsCreateChannel(ownerCtx, &tg.ChannelsCreateChannelRequest{
		Broadcast: true,
		Title:     "Collectible Channel",
		About:     "about",
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	if _, err := r.onChannelsUpdateUsername(ownerCtx, &tg.ChannelsUpdateUsernameRequest{
		Channel:  &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Username: "chan_slot",
	}); err != nil {
		t.Fatalf("update channel username: %v", err)
	}
	return r, owner, channel
}

func TestChannelsUsernameManagementUsesRegistry(t *testing.T) {
	registry := newFakeUsernameRegistry()
	r, owner, channel := newCollectibleChannelFixture(t, registry)
	ctx := WithUserID(context.Background(), owner.ID)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}
	registry.byPeer[peer] = []domain.Username{
		{Username: "chan_slot", Editable: true, Active: true},
		{Username: "alpha", Active: true, SortOrder: 0, CollectibleID: 31},
		{Username: "bravo", Active: true, SortOrder: 1, CollectibleID: 32},
	}

	if ok, err := r.onChannelsToggleUsername(ctx, &tg.ChannelsToggleUsernameRequest{Channel: input, Username: "alpha", Active: false}); !ok || err != nil {
		t.Fatalf("toggle channel collectible = %v,%v, want true,nil", ok, err)
	}
	if ok, err := r.onChannelsToggleUsername(ctx, &tg.ChannelsToggleUsernameRequest{Channel: input, Username: "alpha", Active: false}); ok || !tgerr.Is(err, "USERNAME_NOT_MODIFIED") {
		t.Fatalf("idempotent channel toggle = %v,%v, want false,USERNAME_NOT_MODIFIED", ok, err)
	}
	if ok, err := r.onChannelsToggleUsername(ctx, &tg.ChannelsToggleUsernameRequest{Channel: input, Username: "chan_slot", Active: false}); ok || !tgerr.Is(err, "USERNAME_INVALID") {
		t.Fatalf("toggle channel editable slot = %v,%v, want false,USERNAME_INVALID", ok, err)
	}
	// alpha was just deactivated, so the order carries only the active names.
	if ok, err := r.onChannelsReorderUsernames(ctx, &tg.ChannelsReorderUsernamesRequest{Channel: input, Order: []string{"chan_slot", "bravo"}}); !ok || err != nil {
		t.Fatalf("reorder channel usernames = %v,%v, want true,nil", ok, err)
	}
	if list := domain.SortUsernames(registry.byPeer[peer]); list[1].Username != "bravo" {
		t.Fatalf("channel order = %+v, want bravo first collectible", list)
	}
	if ok, err := r.onChannelsDeactivateAllUsernames(ctx, input); !ok || err != nil {
		t.Fatalf("deactivate all = %v,%v, want true,nil", ok, err)
	}
	for _, item := range registry.byPeer[peer] {
		if item.Collectible() && item.Active {
			t.Fatalf("collectible still active after deactivateAll: %+v", item)
		}
	}
	// Deactivate-all is idempotent, while reorder follows the documented
	// USERNAME_NOT_MODIFIED result for an unchanged order.
	if ok, err := r.onChannelsDeactivateAllUsernames(ctx, input); !ok || err != nil {
		t.Fatalf("repeat deactivate all = %v,%v, want true,nil", ok, err)
	}
	if ok, err := r.onChannelsReorderUsernames(ctx, &tg.ChannelsReorderUsernamesRequest{Channel: input, Order: []string{"chan_slot"}}); ok || !tgerr.Is(err, "USERNAME_NOT_MODIFIED") {
		t.Fatalf("repeat reorder after deactivateAll = %v,%v, want false,USERNAME_NOT_MODIFIED", ok, err)
	}

	// A non-admin must not reach the registry at all.
	other := WithUserID(context.Background(), owner.ID+9999)
	if _, err := r.onChannelsToggleUsername(other, &tg.ChannelsToggleUsernameRequest{Channel: input, Username: "alpha", Active: true}); err == nil {
		t.Fatalf("toggle as stranger err = nil, want permission failure")
	}
}

func TestChannelsUsernameManagementWithoutRegistryKeepsLegacyAnswers(t *testing.T) {
	r, owner, channel := newCollectibleChannelFixture(t, nil)
	ctx := WithUserID(context.Background(), owner.ID)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	if ok, err := r.onChannelsToggleUsername(ctx, &tg.ChannelsToggleUsernameRequest{Channel: input, Username: "chan_slot", Active: true}); !ok || err != nil {
		t.Fatalf("toggle without registry = %v,%v, want true,nil", ok, err)
	}
	if ok, err := r.onChannelsReorderUsernames(ctx, &tg.ChannelsReorderUsernamesRequest{Channel: input, Order: []string{"chan_slot"}}); !ok || err != nil {
		t.Fatalf("reorder without registry = %v,%v, want true,nil", ok, err)
	}
	if ok, err := r.onChannelsDeactivateAllUsernames(ctx, input); !ok || err != nil {
		t.Fatalf("deactivate without registry = %v,%v, want true,nil", ok, err)
	}
}

// TestChannelsDeactivateAllUsernamesIsIdempotent covers the report "I created a
// channel without a username and later could not set one": Telegram Desktop
// drives its channel-username editor by calling channels.deactivateAllUsernames
// first and only continuing when it answers true. A channel that owns no
// collectible username at all -- every freshly created channel -- has nothing to
// deactivate, and answering USERNAME_NOT_MODIFIED there aborted the whole flow
// client-side. Deactivating an empty set is success.
func TestChannelsDeactivateAllUsernamesIsIdempotent(t *testing.T) {
	registry := newFakeUsernameRegistry()
	r, owner, channel := newCollectibleChannelFixture(t, registry)
	ctx := WithUserID(context.Background(), owner.ID)
	input := &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}

	// A channel with only its editable slot: no collectible username exists.
	registry.byPeer[peer] = []domain.Username{{Username: "chan_slot", Editable: true, Active: true}}
	for attempt := 0; attempt < 2; attempt++ {
		if ok, err := r.onChannelsDeactivateAllUsernames(ctx, input); !ok || err != nil {
			t.Fatalf("deactivate all with nothing collectible (attempt %d) = %v,%v, want true,nil", attempt, ok, err)
		}
	}
	// The whole visible list is just the editable slot, which is exactly what
	// Telegram Desktop sends here.
	if ok, err := r.onChannelsReorderUsernames(ctx, &tg.ChannelsReorderUsernamesRequest{Channel: input, Order: []string{"chan_slot"}}); ok || !tgerr.Is(err, "USERNAME_NOT_MODIFIED") {
		t.Fatalf("reorder with nothing collectible = %v,%v, want false,USERNAME_NOT_MODIFIED", ok, err)
	}
	// The editable slot is untouched by either call: only collectibles are hidden.
	if len(registry.byPeer[peer]) != 1 || !registry.byPeer[peer][0].Active {
		t.Fatalf("editable slot after bulk calls = %+v, want it left active", registry.byPeer[peer])
	}

	// A channel with no rows at all behaves the same way.
	delete(registry.byPeer, peer)
	if ok, err := r.onChannelsDeactivateAllUsernames(ctx, input); !ok || err != nil {
		t.Fatalf("deactivate all on an empty registry = %v,%v, want true,nil", ok, err)
	}

	// Permission is still checked before the registry is consulted.
	stranger := WithUserID(context.Background(), owner.ID+4242)
	if ok, err := r.onChannelsDeactivateAllUsernames(stranger, input); ok || err == nil {
		t.Fatalf("deactivate all as stranger = %v,%v, want false and a permission failure", ok, err)
	}
}

func TestChannelsGetChannelsProjectsCollectibleUsernames(t *testing.T) {
	registry := newFakeUsernameRegistry()
	r, owner, channel := newCollectibleChannelFixture(t, registry)
	ctx := WithUserID(context.Background(), owner.ID)
	registry.byPeer[domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID}] = []domain.Username{
		{Username: "chan_slot", Editable: true, Active: true},
		{Username: "chan_nft", Active: true, SortOrder: 0, CollectibleID: 44},
	}

	chats, err := r.onChannelsGetChannels(ctx, []tg.InputChannelClass{
		&tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("get channels: %v", err)
	}
	out := chats.(*tg.MessagesChats).Chats
	if len(out) != 1 {
		t.Fatalf("chats = %d, want 1", len(out))
	}
	vector, ok := out[0].(*tg.Channel).GetUsernames()
	if !ok {
		t.Fatalf("channel usernames unset, want registry vector")
	}
	if got := usernameStrings(vector); len(got) != 2 || got[0] != "chan_slot" || got[1] != "chan_nft" {
		t.Fatalf("channel usernames = %v, want [chan_slot chan_nft]", got)
	}
	if scalar, ok := out[0].(*tg.Channel).GetUsername(); ok || scalar != "" {
		t.Fatalf("scalar channel username = %q (set %v), want absent with collectible vector", scalar, ok)
	}
}

func TestChannelsGetChannelsDegradesWithoutRegistry(t *testing.T) {
	r, owner, channel := newCollectibleChannelFixture(t, nil)
	ctx := WithUserID(context.Background(), owner.ID)

	chats, err := r.onChannelsGetChannels(ctx, []tg.InputChannelClass{
		&tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("get channels: %v", err)
	}
	vector, ok := chats.(*tg.MessagesChats).Chats[0].(*tg.Channel).GetUsernames()
	if ok || len(vector) != 0 {
		t.Fatalf("legacy channel usernames = %+v (set %v), want vector absent", vector, ok)
	}
	if scalar, ok := chats.(*tg.MessagesChats).Chats[0].(*tg.Channel).GetUsername(); !ok || scalar != "chan_slot" {
		t.Fatalf("legacy scalar username = %q (set %v), want chan_slot", scalar, ok)
	}
}
