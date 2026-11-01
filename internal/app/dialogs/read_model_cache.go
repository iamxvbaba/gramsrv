package dialogs

import (
	"context"
	"time"

	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	dialogLightReadModel                       = readmodel.ModelDialogLight
	defaultDialogPeerReadModelTTL              = 24 * time.Hour
	defaultDialogPeerReadModelMaxEntries       = 500000
	defaultDialogPeerReadModelMaxBytes   int64 = 256 << 20
)

type dialogPeerCacheKey struct {
	userID int64
	peer   domain.Peer
}

// dialogPeerReadModelCache is backed by the shared cache primitive (epoch guard / LRU / clone).
// It uses per-peer external building: the Service looks up the cache per peer, batches the misses
// into one backend call, then writes back per peer. The version gate compares against the value's
// own DialogList.Hash (the primitive stores hash=0, the version is carried by the value). It
// returns the whole raw list (rather than reassembling per-peer slices), so it does not use
// GetOrLoadBatch — that returns per-key values and would drop global elements not owned by a peer.
type dialogPeerReadModelCache struct {
	cache *readmodelcache.Cache[dialogPeerCacheKey, domain.DialogList]
}

func newDialogPeerReadModelCache(ttl time.Duration) *dialogPeerReadModelCache {
	return newDialogPeerReadModelCacheWithLimits(defaultDialogPeerReadModelMaxEntries, defaultDialogPeerReadModelMaxBytes, ttl)
}

func newDialogPeerReadModelCacheWithLimits(maxEntries int, maxBytes int64, ttl time.Duration) *dialogPeerReadModelCache {
	if ttl <= 0 {
		ttl = defaultDialogPeerReadModelTTL
	}
	return &dialogPeerReadModelCache{
		cache: readmodelcache.New[dialogPeerCacheKey, domain.DialogList](readmodelcache.Config[dialogPeerCacheKey, domain.DialogList]{
			MaxEntries: maxEntries,
			MaxWeight:  maxBytes,
			Weight:     dialogPeerListApproxBytes,
			TTL:        ttl,
			Clone:      cloneDialogList,
		}),
	}
}

func (s *Service) userPeerDialogsReadModel(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error) {
	if s == nil {
		return domain.DialogList{}, nil
	}
	unique := uniqueUserPeers(peers)
	if len(unique) == 0 {
		return domain.DialogList{}, nil
	}
	return s.cachedPeerDialogsReadModel(ctx, userID, unique, s.dialogHashes, s.loadUserPeerDialogs)
}

func (s *Service) channelPeerDialogsReadModel(ctx context.Context, userID int64, channelIDs []int64) (domain.DialogList, error) {
	if s == nil {
		return domain.DialogList{}, nil
	}
	unique := uniqueChannelPeers(channelIDs)
	if len(unique) == 0 {
		return domain.DialogList{}, nil
	}
	return s.loadChannelPeerDialogsByPeers(ctx, userID, unique)
}

func (s *Service) cachedPeerDialogsReadModel(
	ctx context.Context,
	userID int64,
	peers []domain.Peer,
	hashesFor func(context.Context, int64, []domain.Peer) (map[domain.Peer]int64, error),
	load func(context.Context, int64, []domain.Peer) (domain.DialogList, error),
) (domain.DialogList, error) {
	if s.privatePeerCache == nil || s.versions == nil {
		return load(ctx, userID, peers)
	}
	hashes, err := hashesFor(ctx, userID, peers)
	if err != nil {
		return domain.DialogList{}, err
	}
	loadEpoch := s.privatePeerCache.cacheEpoch()
	var out domain.DialogList
	misses := make([]domain.Peer, 0, len(peers))
	for _, peer := range peers {
		hash := hashes[peer]
		if hash != 0 {
			if cached, ok := s.privatePeerCache.lookup(dialogPeerCacheKey{userID: userID, peer: peer}, hash); ok {
				out = mergeDialogLists(out, cached)
				continue
			}
		}
		misses = append(misses, peer)
	}
	if len(misses) == 0 {
		out.Count = len(out.Dialogs)
		return out, nil
	}
	list, err := load(ctx, userID, misses)
	if err != nil {
		return domain.DialogList{}, err
	}
	for _, peer := range misses {
		hash := hashes[peer]
		if hash == 0 {
			continue
		}
		peerList := dialogListForPeer(list, peer)
		peerList.Hash = hash
		s.privatePeerCache.putIfEpoch(dialogPeerCacheKey{userID: userID, peer: peer}, peerList, hash, loadEpoch)
	}
	if len(out.Dialogs) > 0 || len(out.Messages) > 0 || len(out.ChannelMessages) > 0 || len(out.Users) > 0 || len(out.Channels) > 0 {
		return mergeDialogLists(out, list), nil
	}
	return list, nil
}

func (s *Service) loadUserPeerDialogs(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error) {
	if s == nil || s.dialogs == nil || len(peers) == 0 {
		return domain.DialogList{}, nil
	}
	list, err := s.dialogs.ListByPeers(ctx, userID, peers)
	if err != nil {
		return domain.DialogList{}, err
	}
	for i := range list.Dialogs {
		list.Dialogs[i].Draft = nil
	}
	return list, nil
}

func (s *Service) loadChannelPeerDialogsByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error) {
	if s == nil || s.channels == nil || len(peers) == 0 {
		return domain.DialogList{}, nil
	}
	channelIDs := channelPeerIDs(peers)
	if len(channelIDs) == 0 {
		return domain.DialogList{}, nil
	}
	list, err := s.channels.GetChannelDialogs(ctx, userID, channelIDs)
	if err != nil {
		return domain.DialogList{}, err
	}
	out := mergeChannelDialogs(domain.DialogList{}, list)
	out, err = s.appendMissingChannelPeerPreviews(ctx, userID, channelIDs, out)
	if err != nil {
		return domain.DialogList{}, err
	}
	return out, nil
}

func (s *Service) dialogHashes(ctx context.Context, userID int64, peers []domain.Peer) (map[domain.Peer]int64, error) {
	keys := make([]store.ReadModelKey, 0, len(peers))
	for _, peer := range peers {
		keys = append(keys, store.ReadModelKey{Model: dialogLightReadModel, OwnerUserID: userID, PeerType: peer.Type, PeerID: peer.ID})
	}
	rows, err := s.versions.ReadModelHashes(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.Peer]int64, len(peers))
	for _, peer := range peers {
		out[peer] = rows[store.ReadModelKey{Model: dialogLightReadModel, OwnerUserID: userID, PeerType: peer.Type, PeerID: peer.ID}]
	}
	return out, nil
}

// lookup returns only on a hit whose version (the value's own DialogList.Hash) matches; the
// primitive already clones at the return boundary.
func (c *dialogPeerReadModelCache) lookup(key dialogPeerCacheKey, currentHash int64) (domain.DialogList, bool) {
	if c == nil {
		return domain.DialogList{}, false
	}
	list, ok := c.cache.Peek(key)
	if !ok || (currentHash != 0 && list.Hash != currentHash) {
		return domain.DialogList{}, false
	}
	return list, true
}

func (c *dialogPeerReadModelCache) putIfEpoch(key dialogPeerCacheKey, list domain.DialogList, hash int64, expectedEpoch uint64) {
	if c == nil || key.userID == 0 || key.peer.Type == "" || key.peer.ID == 0 || hash == 0 {
		return
	}
	list.Hash = hash
	c.cache.StoreIfEpoch(key, list, expectedEpoch)
}

func (c *dialogPeerReadModelCache) invalidate(key dialogPeerCacheKey) {
	if c == nil {
		return
	}
	c.cache.Invalidate(key)
}

func (c *dialogPeerReadModelCache) flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

func (c *dialogPeerReadModelCache) cacheEpoch() uint64 {
	if c == nil {
		return 0
	}
	return c.cache.LoadEpoch()
}

func (s *Service) InvalidateDialog(userID int64, peer domain.Peer) {
	if s == nil || userID == 0 {
		return
	}
	s.InvalidateDialogOwner(userID)
	if s.draftCache != nil && peer.Type != "" && peer.ID != 0 {
		s.draftCache.invalidate(dialogPeerCacheKey{userID: userID, peer: peer})
	}
	if s.privatePeerCache != nil && peer.Type == domain.PeerTypeUser && peer.ID != 0 {
		s.privatePeerCache.invalidate(dialogPeerCacheKey{userID: userID, peer: peer})
	}
}

// InvalidateDialogOwner invalidates owner-list L1 state without inventing an
// exact peer. Redis L2 values are version-addressed and validated, so old keys
// expire naturally rather than requiring a global/key-pattern delete.
func (s *Service) InvalidateDialogOwner(userID int64) {
	if s == nil || userID == 0 {
		return
	}
	s.invalidateDialogListHashes(userID)
	if s.listCache != nil {
		s.listCache.invalidateOwner(userID)
	}
}

// InvalidateDialogListsForChannel invalidates only local bounded owner
// snapshots that actually contain the changed shared channel. The listener
// invokes it for channel_base; reconnect flush remains the missed-NOTIFY guard.
func (s *Service) InvalidateDialogListsForChannel(channelID int64) {
	if s != nil && s.listCache != nil {
		s.listCache.invalidateChannel(channelID)
	}
}

func (s *Service) FlushReadModelCache() {
	if s == nil {
		return
	}
	if s.privatePeerCache != nil {
		s.privatePeerCache.flush()
	}
	if s.draftCache != nil {
		s.draftCache.flush()
	}
	if s.listHashCache != nil {
		s.listHashCache.flush()
	}
	if s.listCache != nil {
		s.listCache.flush()
	}
}

func dialogPeerListApproxBytes(list domain.DialogList) int64 {
	weight := int64(256 + len(list.Dialogs)*256 + len(list.Messages)*512 + len(list.Users)*512)
	for _, dialog := range list.Dialogs {
		weight += int64(len(dialog.ThemeEmoticon))
	}
	for _, msg := range list.Messages {
		weight += int64(len(msg.Body) + len(msg.Entities)*64)
		if msg.ReplyTo != nil {
			weight += int64(128 + len(msg.ReplyTo.QuoteText) + len(msg.ReplyTo.QuoteEntities)*64)
		}
		if msg.Forward != nil {
			weight += int64(96 + len(msg.Forward.FromName))
		}
		if msg.RichMessage != nil {
			weight += int64(len(msg.RichMessage.Blocks) + len(msg.RichMessage.BotAPIProjection) + len(msg.RichMessage.Photos)*256 + len(msg.RichMessage.Documents)*256)
		}
	}
	for _, user := range list.Users {
		weight += int64(len(user.Phone) + len(user.FirstName) + len(user.LastName) + len(user.About) + len(user.Username) + len(user.PhotoStripped))
	}
	if weight < 1 {
		return 1
	}
	return weight
}

func (s *Service) invalidateDialogListHashes(userID int64) {
	if s == nil || s.listHashCache == nil || userID == 0 {
		return
	}
	s.listHashCache.invalidateOwner(userID)
}

func uniqueUserPeers(peers []domain.Peer) []domain.Peer {
	return uniquePeersOfType(peers, domain.PeerTypeUser)
}

func uniqueChannelPeers(channelIDs []int64) []domain.Peer {
	out := make([]domain.Peer, 0, len(channelIDs))
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, domain.Peer{Type: domain.PeerTypeChannel, ID: id})
	}
	return out
}

func uniquePeersOfType(peers []domain.Peer, peerType domain.PeerType) []domain.Peer {
	out := make([]domain.Peer, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		if peer.Type != peerType || peer.ID == 0 {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		out = append(out, peer)
	}
	return out
}

func channelPeerIDs(peers []domain.Peer) []int64 {
	out := make([]int64, 0, len(peers))
	for _, peer := range peers {
		if peer.Type == domain.PeerTypeChannel && peer.ID != 0 {
			out = append(out, peer.ID)
		}
	}
	return out
}

func dialogListForPeer(list domain.DialogList, peer domain.Peer) domain.DialogList {
	out := domain.DialogList{Hash: list.Hash}
	for _, dialog := range list.Dialogs {
		if dialog.Peer == peer {
			out.Dialogs = append(out.Dialogs, cloneDialog(dialog))
		}
	}
	for _, msg := range list.Messages {
		if msg.Peer == peer {
			out.Messages = append(out.Messages, cloneMessageForDialogCache(msg))
		}
	}
	for _, msg := range list.ChannelMessages {
		if msg.ChannelID == peer.ID && peer.Type == domain.PeerTypeChannel {
			out.ChannelMessages = append(out.ChannelMessages, cloneChannelMessageForDialogCache(msg))
		}
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		for _, user := range list.Users {
			if user.ID == peer.ID {
				out.Users = append(out.Users, cloneDialogUser(user))
			}
		}
	case domain.PeerTypeChannel:
		var linkedID int64
		for _, channel := range list.Channels {
			if channel.ID == peer.ID {
				out.Channels = append(out.Channels, cloneDialogChannel(channel))
				// A monoforum and its parent broadcast channel reference each other via
				// linked_monoforum_id. The per-peer cache must preserve the associated channel
				// delivered in the same batch, otherwise on a cache hit getPeerDialogs returns only
				// the peer itself and drops the associated channel, so the client cannot resolve
				// linked_monoforum_id (the same-batch delivery of GetChannelDialogs is erased at the
				// cache layer).
				if channel.LinkedMonoforumID != 0 && (channel.Monoforum || channel.BroadcastMessagesAllowed) {
					linkedID = channel.LinkedMonoforumID
				}
			}
		}
		if linkedID != 0 {
			for _, channel := range list.Channels {
				if channel.ID == linkedID {
					out.Channels = append(out.Channels, cloneDialogChannel(channel))
				}
			}
		}
	}
	out.Count = len(out.Dialogs)
	return out
}

func cloneDialogList(in domain.DialogList) domain.DialogList {
	in.Dialogs = cloneDialogSlice(in.Dialogs)
	in.Messages = cloneDialogMessages(in.Messages)
	in.ChannelMessages = cloneDialogChannelMessages(in.ChannelMessages)
	in.Users = cloneDialogUsers(in.Users)
	in.Channels = cloneDialogChannels(in.Channels)
	in.ArchiveSummary = cloneDialogArchiveSummary(in.ArchiveSummary)
	return in
}

func cloneDialogArchiveSummary(in *domain.DialogArchiveSummary) *domain.DialogArchiveSummary {
	if in == nil {
		return nil
	}
	out := *in
	if in.TopDialog != nil {
		dialog := cloneDialog(*in.TopDialog)
		out.TopDialog = &dialog
	}
	return &out
}

func cloneDialogSlice(in []domain.Dialog) []domain.Dialog {
	out := make([]domain.Dialog, len(in))
	for i := range in {
		out[i] = cloneDialog(in[i])
	}
	return out
}

func cloneDialog(in domain.Dialog) domain.Dialog {
	if in.DefaultSendAs != nil {
		peer := *in.DefaultSendAs
		in.DefaultSendAs = &peer
	}
	if in.ChannelMember != nil {
		member := *in.ChannelMember
		in.ChannelMember = &member
	}
	if in.Draft != nil {
		draft := cloneDraft(*in.Draft)
		in.Draft = &draft
	}
	return in
}

func cloneDialogMessages(in []domain.Message) []domain.Message {
	out := make([]domain.Message, len(in))
	for i := range in {
		out[i] = cloneMessageForDialogCache(in[i])
	}
	return out
}

func cloneMessageForDialogCache(msg domain.Message) domain.Message {
	msg.Entities = append([]domain.MessageEntity(nil), msg.Entities...)
	msg.RichMessage = cloneRichMessage(msg.RichMessage)
	if msg.ReplyTo != nil {
		reply := *msg.ReplyTo
		reply.QuoteEntities = append([]domain.MessageEntity(nil), msg.ReplyTo.QuoteEntities...)
		msg.ReplyTo = &reply
	}
	if msg.Forward != nil {
		forward := *msg.Forward
		msg.Forward = &forward
	}
	return msg
}

func cloneDialogChannelMessages(in []domain.ChannelMessage) []domain.ChannelMessage {
	out := make([]domain.ChannelMessage, len(in))
	for i := range in {
		out[i] = cloneChannelMessageForDialogCache(in[i])
	}
	return out
}

func cloneChannelMessageForDialogCache(msg domain.ChannelMessage) domain.ChannelMessage {
	msg.Entities = append([]domain.MessageEntity(nil), msg.Entities...)
	msg.RichMessage = cloneRichMessage(msg.RichMessage)
	if msg.ReplyTo != nil {
		reply := *msg.ReplyTo
		reply.QuoteEntities = append([]domain.MessageEntity(nil), msg.ReplyTo.QuoteEntities...)
		msg.ReplyTo = &reply
	}
	if msg.Forward != nil {
		forward := *msg.Forward
		msg.Forward = &forward
	}
	if msg.SendAs != nil {
		sendAs := *msg.SendAs
		msg.SendAs = &sendAs
	}
	if msg.Reactions != nil {
		reactions := *msg.Reactions
		reactions.Results = append([]domain.ChannelMessageReactionCount(nil), msg.Reactions.Results...)
		reactions.Recent = append([]domain.ChannelMessagePeerReaction(nil), msg.Reactions.Recent...)
		msg.Reactions = &reactions
	}
	if msg.ReplyMarkup != nil {
		msg.ReplyMarkup = cloneReplyMarkupForDialogCache(msg.ReplyMarkup)
	}
	if msg.Action != nil {
		action := *msg.Action
		action.UserIDs = append([]int64(nil), msg.Action.UserIDs...)
		action.Completed = append([]int(nil), msg.Action.Completed...)
		action.Incompleted = append([]int(nil), msg.Action.Incompleted...)
		action.TodoItems = append([]domain.MessageTodoItem(nil), msg.Action.TodoItems...)
		msg.Action = &action
	}
	return msg
}

func cloneReplyMarkupForDialogCache(in *domain.MessageReplyMarkup) *domain.MessageReplyMarkup {
	if in == nil {
		return nil
	}
	out := &domain.MessageReplyMarkup{
		Type:        in.Type,
		Resize:      in.Resize,
		SingleUse:   in.SingleUse,
		Selective:   in.Selective,
		Persistent:  in.Persistent,
		Placeholder: in.Placeholder,
	}
	if len(in.Inline) > 0 {
		out.Inline = make([][]domain.MarkupButton, len(in.Inline))
		for i, row := range in.Inline {
			out.Inline[i] = make([]domain.MarkupButton, len(row))
			for j, button := range row {
				out.Inline[i][j] = button
				out.Inline[i][j].Data = append([]byte(nil), button.Data...)
			}
		}
	}
	if len(in.Keyboard) > 0 {
		out.Keyboard = make([][]domain.MarkupButton, len(in.Keyboard))
		for i, row := range in.Keyboard {
			out.Keyboard[i] = append([]domain.MarkupButton(nil), row...)
		}
	}
	return out
}

func cloneDialogUsers(in []domain.User) []domain.User {
	out := make([]domain.User, len(in))
	for i := range in {
		out[i] = cloneDialogUser(in[i])
	}
	return out
}

func cloneDialogUser(in domain.User) domain.User {
	if in.PhotoStripped != nil {
		in.PhotoStripped = append([]byte(nil), in.PhotoStripped...)
	}
	if in.RestrictionReasons != nil {
		in.RestrictionReasons = append([]domain.UserRestrictionReason(nil), in.RestrictionReasons...)
	}
	return in
}

func cloneDialogChannels(in []domain.Channel) []domain.Channel {
	out := make([]domain.Channel, len(in))
	for i := range in {
		out[i] = cloneDialogChannel(in[i])
	}
	return out
}

func cloneDialogChannel(in domain.Channel) domain.Channel {
	in.PhotoStripped = append([]byte(nil), in.PhotoStripped...)
	in.ReactionPolicy.Emoticons = append([]string(nil), in.ReactionPolicy.Emoticons...)
	in.ReactionPolicy.CustomEmojiIDs = append([]int64(nil), in.ReactionPolicy.CustomEmojiIDs...)
	return in
}
