package dialogs

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"reflect"
	"sort"
	"unicode/utf8"

	"telesrv/internal/app/userprojection"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PremiumChecker 报告用户当前是否有效会员（pin 上限双档判断用）。
type PremiumChecker func(ctx context.Context, userID int64) bool

// Service 提供会话列表查询。
type Service struct {
	dialogs       store.DialogStore
	channels      store.ChannelStore
	contacts      store.ContactStore
	photos        userprojection.ProfilePhotoProvider
	privacy       userprojection.PrivacyEvaluator
	freezes       userprojection.AccountFreezeProvider
	premium       PremiumChecker
	projector     *userprojection.Projector
	versions      store.ReadModelVersionStore
	peerCache     *dialogPeerReadModelCache
	listHashCache *dialogListHashCache
}

// Option adjusts optional dialogs service dependencies.
type Option func(*Service)

// WithContactStore enables viewer-specific user projection for dialog users.
func WithContactStore(c store.ContactStore) Option {
	return func(s *Service) { s.contacts = c }
}

// WithPremiumChecker 启用 pin 上限的 premium 双档（缺省一律按默认档）。
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

// WithReadModelVersions enables durable version-token backed peer dialog caching.
func WithReadModelVersions(v store.ReadModelVersionStore) Option {
	return func(s *Service) { s.versions = v }
}

// NewService 创建 dialogs 服务。
func NewService(dialogs store.DialogStore, channels ...store.ChannelStore) *Service {
	s := &Service{
		dialogs:       dialogs,
		peerCache:     newDialogPeerReadModelCache(defaultDialogPeerReadModelTTL),
		listHashCache: newDialogListHashCache(defaultDialogListHashCacheTTL),
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
	)
}

// GetDialogs 返回当前登录账号的会话摘要。未登录或无持久化实现时按空账号处理。
func (s *Service) GetDialogs(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error) {
	return s.getDialogs(ctx, userID, filter, false)
}

// getDialogs 是 GetDialogs 的实现。lightweight=true 跳过草稿附加、viewer 投影与
// list-hash 写回,供 attachArchiveSummary 取归档顶部会话用:归档摘要只需 top
// peer/message,草稿无意义;且追加进来的归档 users 会被外层 GetDialogs 的
// projectDialogUsers 统一投影,内层再投影纯属重复(原归档递归走完整 GetDialogs
// 会多跑一次 ListDrafts + 一次投影)。
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
	// 在加载任何会话状态前快照 list-hash epoch：若加载/投影期间发生 dialog_light 写失效，
	// rememberDialogListHash 会据此拒绝写回 stale hash，避免后续 getDialogs 误返 NotModified。
	listHashEpoch := s.listHashCache.cacheEpoch()
	var out domain.DialogList
	if s.dialogs != nil {
		list, err := s.dialogs.ListByUser(ctx, userID, filter)
		if err != nil {
			return domain.DialogList{}, err
		}
		out = mergeDialogLists(out, list)
	}
	if s.channels != nil {
		list, err := s.channels.ListChannelDialogs(ctx, userID, filter)
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
		// 归档摘要顶部会话:不附草稿、不投影(由外层统一投影)、不写 list-hash。
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

// attachArchiveSummary 在主列表第一页响应上聚合归档摘要：TDesktop 只能靠
// 响应头部的 dialogFolder 条目发现 archive（新登录设备没有任何 update 可
// 重放），缺少它归档会话将彻底不可见。归档/自定义 filter/置顶/翻页请求不附加。
func (s *Service) attachArchiveSummary(ctx context.Context, userID int64, filter domain.DialogFilter, out *domain.DialogList) error {
	if s == nil || out == nil {
		return nil
	}
	if filter.HasFolderID && filter.FolderID != domain.DialogMainFolderID {
		return nil
	}
	// exclude_pinned 请求按官方语义排除 folder 条目（archive 行属 pinned 集合）。
	// PinnedOnly（getPinnedDialogs）不能跳过：DrKLO 主列表 getDialogs 一律带
	// exclude_pinned，archive 行的发现完全依赖 getPinnedDialogs 响应里的
	// dialogFolder 条目（fetchFolderInLoadedPinnedDialogs）。
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
	// getPinnedDialogs 只返回置顶集合：archive 行被 unpin 后不再属于它。
	if filter.PinnedOnly && !archivePinned {
		return nil
	}
	topDialog := top.Dialogs[0]
	out.ArchiveSummary = &domain.DialogArchiveSummary{
		TopPeer:             topDialog.Peer,
		TopMessage:          topDialog.TopMessage,
		UnreadPeersCount:    unreadPeers,
		UnreadMessagesCount: unreadMessages,
		Pinned:              archivePinned,
	}
	// dialogFolder.peer 指向的会话对象必须随响应下发：TDesktop
	// Folder::applyDialog 会立即解引用该 peer（owner().history(peerId)）。
	out.Messages = append(out.Messages, top.Messages...)
	out.ChannelMessages = append(out.ChannelMessages, top.ChannelMessages...)
	out.Users = append(out.Users, top.Users...)
	out.Channels = append(out.Channels, top.Channels...)
	return nil
}

// GetPeerDialogs 返回指定 peer 的会话摘要。缺失的 peer 由 store 按空会话占位返回。
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
		// Linked discussion guests need a transient peer-dialog snapshot so
		// TDesktop can finish materializing the comments History after
		// requestSelf. ChannelLeft keeps the snapshot out of the main chat list,
		// and Guest guarantees this path never turns an ordinary public preview
		// into a dialog.
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

func isChannelPreviewAccessError(err error) bool {
	return errors.Is(err, domain.ErrChannelPrivate) ||
		errors.Is(err, domain.ErrChannelUserBanned) ||
		errors.Is(err, domain.ErrChannelInvalid)
}

func dialogFromChannelView(view domain.ChannelView) domain.Dialog {
	dialog := view.Dialog
	return domain.Dialog{
		Peer:                domain.Peer{Type: domain.PeerTypeChannel, ID: dialog.ChannelID},
		ChannelLeft:         view.Self.Status == domain.ChannelMemberLeft,
		FolderID:            dialog.FolderID,
		TopMessage:          dialog.TopMessageID,
		TopMessageDate:      dialog.TopMessageDate,
		ReadInboxMaxID:      dialog.ReadInboxMaxID,
		ReadOutboxMaxID:     dialog.ReadOutboxMaxID,
		UnreadCount:         dialog.UnreadCount,
		UnreadMentions:      dialog.UnreadMentions,
		UnreadReactions:     dialog.UnreadReactions,
		Pinned:              dialog.Pinned,
		PinnedOrder:         dialog.PinnedOrder,
		UnreadMark:          dialog.UnreadMark,
		ViewForumAsMessages: dialog.ViewForumAsMessages,
		HasScheduled:        dialog.HasScheduled,
		Pts:                 view.Channel.Pts,
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

// GetDraft 读取某会话当前云草稿（draft_message 事件重放时按 peer 重载用）。
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

// TogglePinned 置顶/取消置顶一条会话；置顶顺序在会话当前 folder 内分配，
// 返回 (changed, 该会话所在 folder_id) 供 updateDialogPinned.folder_id 使用。
// pin 时按 folder 校验上限（重复 pin 幂等放行），超限返回 ErrPinnedDialogsTooMuch。
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
			// 重复 pin 幂等，不占新名额。
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

// ToggleArchivePinned 置顶/取消置顶 archive folder 行本身
// （toggleDialogPin(inputDialogPeerFolder)），返回是否变化。
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

// ReorderPinned 重排指定 folder（0 主列表/1 归档）内的置顶顺序；
// force 只清除该 folder 内不在 order 中的置顶，绝不跨 folder 误伤。
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
	drafts, err := s.dialogs.ListDrafts(ctx, userID, domain.MaxDialogDraftsPerUser)
	if err != nil {
		return err
	}
	if len(drafts) == 0 {
		return nil
	}
	byPeer := make(map[domain.Peer]domain.DialogDraft, len(drafts))
	for _, draft := range drafts {
		if draft.TopMessageID != 0 {
			continue
		}
		byPeer[draft.Peer] = cloneDraft(draft)
	}
	if len(byPeer) == 0 {
		return nil
	}
	attached := false
	for i := range list.Dialogs {
		draft, ok := byPeer[list.Dialogs[i].Peer]
		if !ok {
			continue
		}
		d := cloneDraft(draft)
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
	_, _ = h.Write(buf[:26])
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
