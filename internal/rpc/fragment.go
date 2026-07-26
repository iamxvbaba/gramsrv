package rpc

import (
	"context"
	"errors"

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
// the wire shape is exactly what it was before collectibles existed. That is a
// hard requirement, not a nicety -- the scalar users.username /
// channels.username column mirrors domain.ActiveUsername, so the legacy
// projection is always correct, just shorter.

// registerFragment 注册 fragment.* RPC handler。
func (r *Router) registerFragment(d *tlprofile.Dispatcher) {
	registerRPC[*tg.FragmentGetCollectibleInfoRequest](d, tlprofile.SemanticMethodFragmentGetCollectibleInfo, func(ctx context.Context, layerRequest *tg.FragmentGetCollectibleInfoRequest) (any, error) {
		return r.onFragmentGetCollectibleInfo(ctx, layerRequest)
	})
}

// onFragmentGetCollectibleInfo answers fragment.getCollectibleInfo.
//
// Only inputCollectibleUsername is answerable here: this server mints
// collectible usernames but has no collectible phone registry at all, so
// inputCollectiblePhone is rejected with 400 PHONE_NOT_OCCUPIED -- the official
// error for "the queried number is not a collectible", and the honest answer for
// a deployment where no number ever can be. Returning an empty
// fragment.collectibleInfo instead would make clients render a fake purchase.
func (r *Router) onFragmentGetCollectibleInfo(ctx context.Context, req *tg.FragmentGetCollectibleInfoRequest) (*tg.FragmentCollectibleInfo, error) {
	if req == nil {
		return nil, usernameInvalidErr()
	}
	// An authenticated caller is required, matching every other profile lookup.
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	switch collectible := req.Collectible.(type) {
	case *tg.InputCollectibleUsername:
		if collectible == nil {
			return nil, usernameInvalidErr()
		}
		return r.collectibleUsernameInfo(ctx, collectible.Username)
	case *tg.InputCollectiblePhone:
		return nil, phoneNotOccupiedErr()
	default:
		return nil, usernameInvalidErr()
	}
}

func (r *Router) collectibleUsernameInfo(ctx context.Context, username string) (*tg.FragmentCollectibleInfo, error) {
	name := domain.NormalizeUsername(username)
	// Syntax first: a name that cannot be a collectible is USERNAME_INVALID, and
	// rejecting it here keeps malformed input off the registry.
	if !domain.ValidCollectibleUsername(name) {
		return nil, usernameInvalidErr()
	}
	if r.deps.Usernames == nil {
		// No registry wired: no name in this deployment is collectible, so the
		// name is simply not occupied as one.
		return nil, usernameNotOccupiedErr()
	}
	info, err := r.deps.Usernames.CollectibleInfo(ctx, name)
	if err != nil {
		return nil, collectibleInfoErr(err)
	}
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

// collectibleInfoErr maps registry lookup failures onto TL.
//
//   - "no collectible asset backs this name" is USERNAME_NOT_OCCUPIED: the name
//     may well exist as an ordinary editable username, it just is not occupied by
//     a collectible, which is precisely what the official error expresses.
//   - "this name is the editable slot, not a collectible" is USERNAME_INVALID:
//     the *input* is wrong for this method, not the occupancy state.
func collectibleInfoErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrCollectibleUsernameNotFound),
		errors.Is(err, domain.ErrUsernameNotOccupied),
		errors.Is(err, domain.ErrCollectibleUsernameBurned),
		errors.Is(err, domain.ErrCollectibleUsernameNotOwned):
		return usernameNotOccupiedErr()
	case errors.Is(err, domain.ErrUsernameNotCollectible),
		errors.Is(err, domain.ErrUsernameInvalid):
		return usernameInvalidErr()
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
		return collectibleUsernameErr(err)
	}
	if !changed {
		// Re-sending the order a peer already has is idempotent, not an error: a
		// client that reconciles its local list against the server would otherwise
		// see a 400 for doing nothing wrong.
		return nil
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
// and channel objects.
//
// It mirrors applyStoryMaxIDsToPeerObjects: one batched read-model call per
// response instead of a per-peer query, and a silent no-op whenever the read
// model is unavailable. Overlaying after projection is what keeps the ~90
// pure tgUser/tgChannel call sites untouched -- they keep emitting the legacy
// vector, and this pass upgrades it wherever a Router-level entry point runs.
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
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil {
			continue
		}
		list, ok := byPeer[domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}]
		if !ok {
			continue
		}
		if vector := tgUsernamesFromRegistry(list, u.Username); len(vector) > 0 {
			u.SetUsernames(vector)
		}
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok || ch == nil {
			continue
		}
		list, ok := byPeer[domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}]
		if !ok {
			continue
		}
		// ch.Username is the flagged scalar; GetUsername reports the empty string
		// when unset, which is exactly the fallback tgUsernamesFromRegistry wants.
		scalar, _ := ch.GetUsername()
		if vector := tgUsernamesFromRegistry(list, scalar); len(vector) > 0 {
			ch.SetUsernames(vector)
		}
	}
}

// usernameRegistryMap loads the registry for the given peers. A single peer goes
// through PeerUsernames so a one-object projection does not pay for a batch
// round trip; anything larger goes through UsernamesBatch (no N+1). Any error
// yields an empty map, which the caller treats as "keep the legacy vector".
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
