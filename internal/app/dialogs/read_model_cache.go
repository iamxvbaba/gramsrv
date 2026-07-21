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
	dialogLightReadModel          = readmodel.ModelDialogLight
	channelBaseReadModel          = readmodel.ModelChannelBase
	channelMemberReadModel        = readmodel.ModelChannelMember
	defaultDialogPeerReadModelTTL = 24 * time.Hour
	dialogPeerReadModelMaxEntries = 8192
)

type dialogPeerCacheKey struct {
	userID int64
	peer   domain.Peer
}

// dialogPeerReadModelCache 由统一缓存原语承载(epoch 守卫 / LRU / clone)。它走 per-peer
// 外部构建:Service 按 peer 查缓存、把 miss 合批打一次后端、再 per-peer 写回。版本闸门用
// 值自带的 DialogList.Hash 比对(原语存 hash=0,版本由值携带)。返回的是整批原始 list(非
// per-peer 切片重组),故不用 GetOrLoadBatch——它返回 per-key 值会丢非 peer 归属的全局元素。
type dialogPeerReadModelCache struct {
	cache *readmodelcache.Cache[dialogPeerCacheKey, domain.DialogList]
}

func newDialogPeerReadModelCache(ttl time.Duration) *dialogPeerReadModelCache {
	if ttl <= 0 {
		ttl = defaultDialogPeerReadModelTTL
	}
	return &dialogPeerReadModelCache{
		cache: readmodelcache.New[dialogPeerCacheKey, domain.DialogList](readmodelcache.Config[dialogPeerCacheKey, domain.DialogList]{
			MaxEntries: dialogPeerReadModelMaxEntries,
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
	return s.cachedPeerDialogsReadModel(ctx, userID, unique, s.userDialogHashes, s.loadUserPeerDialogs)
}

func (s *Service) channelPeerDialogsReadModel(ctx context.Context, userID int64, channelIDs []int64) (domain.DialogList, error) {
	if s == nil {
		return domain.DialogList{}, nil
	}
	unique := uniqueChannelPeers(channelIDs)
	if len(unique) == 0 {
		return domain.DialogList{}, nil
	}
	return s.cachedPeerDialogsReadModel(ctx, userID, unique, s.channelDialogHashes, s.loadChannelPeerDialogsByPeers)
}

func (s *Service) cachedPeerDialogsReadModel(
	ctx context.Context,
	userID int64,
	peers []domain.Peer,
	hashesFor func(context.Context, int64, []domain.Peer) (map[domain.Peer]int64, error),
	load func(context.Context, int64, []domain.Peer) (domain.DialogList, error),
) (domain.DialogList, error) {
	if s.peerCache == nil || s.versions == nil {
		return load(ctx, userID, peers)
	}
	hashes, err := hashesFor(ctx, userID, peers)
	if err != nil {
		return domain.DialogList{}, err
	}
	loadEpoch := s.peerCache.cacheEpoch()
	var out domain.DialogList
	misses := make([]domain.Peer, 0, len(peers))
	for _, peer := range peers {
		hash := hashes[peer]
		if hash != 0 {
			if cached, ok := s.peerCache.lookup(dialogPeerCacheKey{userID: userID, peer: peer}, hash); ok {
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
		s.peerCache.putIfEpoch(dialogPeerCacheKey{userID: userID, peer: peer}, peerList, hash, loadEpoch)
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
	if err := s.attachDrafts(ctx, userID, &list); err != nil {
		return domain.DialogList{}, err
	}
	if err := s.projectDialogUsers(ctx, userID, &list); err != nil {
		return domain.DialogList{}, err
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
	if err := s.attachDrafts(ctx, userID, &out); err != nil {
		return domain.DialogList{}, err
	}
	if err := s.projectDialogUsers(ctx, userID, &out); err != nil {
		return domain.DialogList{}, err
	}
	return out, nil
}

func (s *Service) userDialogHashes(ctx context.Context, userID int64, peers []domain.Peer) (map[domain.Peer]int64, error) {
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

func (s *Service) channelDialogHashes(ctx context.Context, userID int64, peers []domain.Peer) (map[domain.Peer]int64, error) {
	keys := make([]store.ReadModelKey, 0, len(peers)*3)
	for _, peer := range peers {
		keys = append(keys,
			store.ReadModelKey{Model: channelBaseReadModel, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID},
			store.ReadModelKey{Model: channelMemberReadModel, OwnerUserID: userID, PeerType: peer.Type, PeerID: peer.ID},
			store.ReadModelKey{Model: dialogLightReadModel, OwnerUserID: userID, PeerType: peer.Type, PeerID: peer.ID},
		)
	}
	rows, err := s.versions.ReadModelHashes(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.Peer]int64, len(peers))
	for _, peer := range peers {
		base := rows[store.ReadModelKey{Model: channelBaseReadModel, OwnerUserID: 0, PeerType: peer.Type, PeerID: peer.ID}]
		if base == 0 {
			continue
		}
		member := rows[store.ReadModelKey{Model: channelMemberReadModel, OwnerUserID: userID, PeerType: peer.Type, PeerID: peer.ID}]
		dialog := rows[store.ReadModelKey{Model: dialogLightReadModel, OwnerUserID: userID, PeerType: peer.Type, PeerID: peer.ID}]
		out[peer] = readmodel.MixHashes(base, member, dialog)
	}
	return out, nil
}

// lookup 命中且版本(值自带 DialogList.Hash)匹配才返回;原语已在返回边界 clone。
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
	s.invalidateDialogListHashes(userID)
	if s.peerCache == nil || peer.Type == "" || peer.ID == 0 {
		return
	}
	s.peerCache.invalidate(dialogPeerCacheKey{userID: userID, peer: peer})
}

func (s *Service) FlushReadModelCache() {
	if s == nil {
		return
	}
	if s.peerCache != nil {
		s.peerCache.flush()
	}
	if s.listHashCache != nil {
		s.listHashCache.flush()
	}
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
				// monoforum 与母广播频道互为 linked_monoforum_id。per-peer 缓存必须把同批下发的关联频道
				// 一并保留,否则缓存命中时 getPeerDialogs 只回该 peer 自身、丢掉关联频道,客户端无法
				// resolve linked_monoforum_id(GetChannelDialogs 的同批下发在缓存层被抹掉)。
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
	return in
}

func cloneDialogSlice(in []domain.Dialog) []domain.Dialog {
	out := make([]domain.Dialog, len(in))
	for i := range in {
		out[i] = cloneDialog(in[i])
	}
	return out
}

func cloneDialog(in domain.Dialog) domain.Dialog {
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
