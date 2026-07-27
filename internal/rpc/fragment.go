package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

// Collectible (Fragment-style) usernames on the protocol edge.
//
// This file owns three things:
//
//   - fragment.getCollectibleInfo, the purchase-record lookup a client opens from
//     the "this username was bought on Fragment" badge;
//   - the projection overlay that turns the legacy single-username vector into
//     the full username#b4073647 list (editable slot + collectibles);
//   - the shared toggle/reorder/deactivate plumbing behind
//     account.*, channels.* and bots.* username management.
//
// Every entry point degrades: with Deps.Usernames nil (or on any registry error)
// the wire shape is exactly what it was before collectibles existed.

// registerFragment 注册 fragment.* RPC handler。
func (r *Router) registerFragment(d *tlprofile.Dispatcher) {
	registerRPC[*tg.FragmentGetCollectibleInfoRequest](d, tlprofile.SemanticMethodFragmentGetCollectibleInfo, func(ctx context.Context, layerRequest *tg.FragmentGetCollectibleInfoRequest) (any, error) {
		return r.onFragmentGetCollectibleInfo(ctx, layerRequest)
	})
}

// onFragmentGetCollectibleInfo answers fragment.getCollectibleInfo.
//
// Only inputCollectibleUsername is answerable here: this server has no
// collectible-phone registry. The method exposes only assets visible to the
// caller: their own inactive collectible, a collectible attached to a channel
// they own, or anybody's active collectible.
func (r *Router) onFragmentGetCollectibleInfo(ctx context.Context, req *tg.FragmentGetCollectibleInfoRequest) (*tg.FragmentCollectibleInfo, error) {
	if req == nil {
		return nil, collectibleInvalidErr()
	}
	// An authenticated caller is required, matching every other profile lookup.
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	switch collectible := req.Collectible.(type) {
	case *tg.InputCollectibleUsername:
		if collectible == nil {
			return nil, collectibleInvalidErr()
		}
		return r.collectibleUsernameInfo(ctx, userID, collectible.Username)
	case *tg.InputCollectiblePhone:
		if collectible == nil || strings.TrimSpace(collectible.Phone) == "" {
			return nil, collectibleInvalidErr()
		}
		return nil, collectibleNotFoundErr()
	default:
		return nil, collectibleInvalidErr()
	}
}

func (r *Router) collectibleUsernameInfo(ctx context.Context, userID int64, username string) (*tg.FragmentCollectibleInfo, error) {
	name := domain.NormalizeUsername(username)
	// Syntax first: a name that cannot be a collectible is COLLECTIBLE_INVALID, and
	// rejecting it here keeps malformed input off the registry.
	if !domain.ValidCollectibleUsername(name) {
		return nil, collectibleInvalidErr()
	}
	if r.deps.Usernames == nil {
		return nil, collectibleNotFoundErr()
	}
	asset, err := r.deps.Usernames.Collectible(ctx, name)
	if err != nil {
		return nil, collectibleInfoErr(err)
	}
	if !asset.Owned() {
		return nil, collectibleNotFoundErr()
	}
	visible, err := r.collectibleVisibleTo(ctx, userID, asset)
	if err != nil {
		return nil, internalErr()
	}
	if !visible {
		// Do not reveal that an inactive collectible is associated with another
		// peer. The official surface intentionally collapses that state to not found.
		return nil, collectibleNotFoundErr()
	}
	info := asset.Info()
	out := &tg.FragmentCollectibleInfo{
		PurchaseDate:   info.PurchaseDate,
		Currency:       info.Currency,
		Amount:         info.Amount,
		CryptoCurrency: info.CryptoCurrency,
		CryptoAmount:   info.CryptoAmount,
		URL:            info.URL,
	}
	return out, nil
}

func (r *Router) collectibleVisibleTo(ctx context.Context, userID int64, asset domain.CollectibleUsername) (bool, error) {
	if asset.Owner.Type == domain.PeerTypeUser && asset.Owner.ID == userID {
		return true, nil
	}
	list, err := r.deps.Usernames.PeerUsernames(ctx, asset.Owner)
	if err != nil {
		return false, err
	}
	for _, item := range list {
		if item.CollectibleID == asset.ID && item.Active {
			return true, nil
		}
	}
	if asset.Owner.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return false, nil
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, asset.Owner.ID)
	if err != nil {
		// Inaccessible channels are indistinguishable from an inactive asset owned
		// by somebody else.
		return false, nil
	}
	return view.Self.Role == domain.ChannelRoleCreator, nil
}

func collectibleInvalidErr() error  { return tgerr400("COLLECTIBLE_INVALID") }
func collectibleNotFoundErr() error { return tgerr400("COLLECTIBLE_NOT_FOUND") }

// collectibleInfoErr maps registry lookup failures onto TL.
func collectibleInfoErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrCollectibleUsernameNotFound),
		errors.Is(err, domain.ErrUsernameNotOccupied),
		errors.Is(err, domain.ErrCollectibleUsernameBurned),
		errors.Is(err, domain.ErrCollectibleUsernameNotOwned):
		return collectibleNotFoundErr()
	case errors.Is(err, domain.ErrUsernameNotCollectible),
		errors.Is(err, domain.ErrUsernameInvalid):
		return collectibleInvalidErr()
	default:
		return internalErr()
	}
}

// collectibleUsernameErr maps registry mutation failures onto TL.
//
// domain.ErrUsernameNotCollectible and domain.ErrUsernameOrderInvalid both mean
// "the client asked for something the collectible slots cannot express" -- moving
// or deactivating the editable slot, or an order that is not a permutation of the
// peer's collectibles -- so both are USERNAME_INVALID.
func collectibleUsernameErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrUsernameNotCollectible),
		errors.Is(err, domain.ErrUsernameOrderInvalid),
		errors.Is(err, domain.ErrUsernameNotEditable),
		errors.Is(err, domain.ErrUsernameInvalid):
		return usernameInvalidErr()
	case errors.Is(err, domain.ErrUsernameNotOccupied),
		errors.Is(err, domain.ErrCollectibleUsernameNotFound),
		errors.Is(err, domain.ErrCollectibleUsernameNotOwned),
		errors.Is(err, domain.ErrCollectibleUsernameBurned):
		return usernameNotOccupiedErr()
	case errors.Is(err, domain.ErrCollectibleUsernameLimit):
		return limitInvalidErr()
	default:
		return internalErr()
	}
}

// toggleRegistryUsername is the shared body of account/channels/bots
// .toggleUsername. Callers do the permission check first.
func (r *Router) toggleRegistryUsername(ctx context.Context, peer domain.Peer, username string, active bool) error {
	name := domain.NormalizeUsername(username)
	if !domain.ValidCollectibleUsername(name) {
		return usernameInvalidErr()
	}
	changed, err := r.deps.Usernames.ToggleUsername(ctx, peer, name, active)
	if err != nil {
		return collectibleUsernameErr(err)
	}
	if !changed {
		return usernameNotModifiedErr()
	}
	r.invalidateRegistryProjection(peer)
	return nil
}

// reorderRegistryUsernames is the shared body of account/channels/bots
// .reorderUsernames.
func (r *Router) reorderRegistryUsernames(ctx context.Context, peer domain.Peer, order []string) error {
	normalized := make([]string, 0, len(order))
	for _, name := range order {
		normalized = append(normalized, domain.NormalizeUsername(name))
	}
	changed, err := r.deps.Usernames.ReorderUsernames(ctx, peer, normalized)
	if err != nil {
		if errors.Is(err, domain.ErrUsernameOrderInvalid) {
			return tgerr400("ORDER_INVALID")
		}
		return collectibleUsernameErr(err)
	}
	if !changed {
		return usernameNotModifiedErr()
	}
	r.invalidateRegistryProjection(peer)
	return nil
}

// deactivateAllRegistryUsernames is the shared body of
// channels.deactivateAllUsernames: it hides every collectible username of a peer.
//
// Deactivating an empty set is success, not USERNAME_NOT_MODIFIED: Telegram
// Desktop calls channels.deactivateAllUsernames as a step of its "set the
// username" flow, so a peer that has no collectible usernames yet -- the common
// case for a freshly created channel -- would abort that flow on a 400. The
// no-op stub this replaced also answered true, and clients depend on it.
func (r *Router) deactivateAllRegistryUsernames(ctx context.Context, peer domain.Peer) error {
	changed, err := r.deps.Usernames.DeactivateAllUsernames(ctx, peer)
	if err != nil {
		return collectibleUsernameErr(err)
	}
	if !changed {
		return nil
	}
	r.invalidateRegistryProjection(peer)
	return nil
}

// invalidateRegistryProjection drops the cached user/channel projections that
// embed the username vector, so the next getFullUser / getFullChannel rebuilds it.
func (r *Router) invalidateRegistryProjection(peer domain.Peer) {
	switch peer.Type {
	case domain.PeerTypeUser:
		r.invalidateRPCProjectionForUser(peer.ID)
	case domain.PeerTypeChannel:
		r.invalidateRPCProjectionForChannel(peer.ID)
	}
}

// applyUsernamesToPeerObjects overlays the registry onto already-projected user
// and channel objects. Per the Fragment contract, the scalar username is cleared
// and the vector is set only when a collectible is associated with the peer.
//
// It mirrors applyStoryMaxIDsToPeerObjects: one batched read-model call per
// response instead of a per-peer query, and a silent no-op whenever the read
// model is unavailable. Overlaying after projection is what keeps the ~90
// pure tgUser/tgChannel call sites untouched -- they keep emitting the legacy
// scalar, and this pass upgrades it wherever a Router-level entry point runs.
func (r *Router) applyUsernamesToPeerObjects(ctx context.Context, users []tg.UserClass, chats []tg.ChatClass) {
	if r.deps.Usernames == nil || len(users)+len(chats) == 0 {
		return
	}
	peers := make([]domain.Peer, 0, len(users)+len(chats))
	seen := make(map[domain.Peer]struct{}, len(users)+len(chats))
	addPeer := func(peer domain.Peer) {
		if peer.ID == 0 {
			return
		}
		if _, ok := seen[peer]; ok {
			return
		}
		seen[peer] = struct{}{}
		peers = append(peers, peer)
	}
	for _, item := range users {
		if u, ok := item.(*tg.User); ok && u != nil {
			addPeer(domain.Peer{Type: domain.PeerTypeUser, ID: u.ID})
		}
	}
	for _, item := range chats {
		if ch, ok := item.(*tg.Channel); ok && ch != nil {
			addPeer(domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID})
		}
	}
	if len(peers) == 0 {
		return
	}
	byPeer := r.usernameRegistryMap(ctx, peers)
	if len(byPeer) == 0 {
		return
	}
	applyUsernamesFromRegistry(users, chats, byPeer)
}

// applyUsernamesFromRegistry applies a previously loaded registry snapshot.
// Notification fan-out uses this form so one peer-wide read does not become one
// database query per online viewer.
func applyUsernamesFromRegistry(users []tg.UserClass, chats []tg.ChatClass, byPeer map[domain.Peer][]domain.Username) {
	if len(byPeer) == 0 {
		return
	}
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil {
			continue
		}
		list, ok := byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}]
		if !ok || !hasCollectibleUsername(list) {
			continue
		}
		if vector := tgUsernamesFromRegistry(list, u.Username); len(vector) > 0 {
			u.Flags.Unset(3)
			u.Username = ""
			u.SetUsernames(vector)
		}
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok || ch == nil {
			continue
		}
		list, ok := byPeer[domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}]
		if !ok || !hasCollectibleUsername(list) {
			continue
		}
		// ch.Username is the flagged scalar; GetUsername reports the empty string
		// when unset, which is exactly the fallback tgUsernamesFromRegistry wants.
		scalar, _ := ch.GetUsername()
		if vector := tgUsernamesFromRegistry(list, scalar); len(vector) > 0 {
			ch.Flags.Unset(6)
			ch.Username = ""
			ch.SetUsernames(vector)
		}
	}
}

// usernameRegistryMap loads the registry for the given peers. A single peer goes
// through PeerUsernames so a one-object projection does not pay for a batch
// round trip; anything larger goes through UsernamesBatch (no N+1). Any error
// yields an empty map, which the caller treats as "keep the legacy scalar".
func (r *Router) usernameRegistryMap(ctx context.Context, peers []domain.Peer) map[domain.Peer][]domain.Username {
	if r.deps.Usernames == nil || len(peers) == 0 {
		return nil
	}
	if len(peers) == 1 {
		list, err := r.deps.Usernames.PeerUsernames(ctx, peers[0])
		if err != nil || len(list) == 0 {
			return nil
		}
		return map[domain.Peer][]domain.Username{peers[0]: list}
	}
	byPeer, err := r.deps.Usernames.UsernamesBatch(ctx, peers)
	if err != nil {
		return nil
	}
	return byPeer
}
