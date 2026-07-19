package memory

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"sort"
	"sync"
	"telesrv/internal/domain"
)

// DialogStore 是 store.DialogStore 的内存实现。
type DialogStore struct {
	mu          sync.RWMutex
	m           map[int64]domain.DialogList
	drafts      map[int64]map[dialogDraftKey]domain.DialogDraft
	folders     map[int64]map[int]domain.DialogFolder
	folderOrder map[int64][]int
	folderTags  map[int64]bool
	// archivePinned 记录 archive folder 行置顶状态；无记录时官方默认 true。
	archivePinned map[int64]bool
	translations  map[translationPreferenceKey]bool
}

type translationPreferenceKey struct {
	userID int64
	peer   domain.Peer
}

type dialogDraftKey struct {
	peerType     domain.PeerType
	peerID       int64
	topMessageID int
}

// NewDialogStore 创建内存 DialogStore。
func NewDialogStore() *DialogStore {
	return &DialogStore{
		m:             make(map[int64]domain.DialogList),
		drafts:        make(map[int64]map[dialogDraftKey]domain.DialogDraft),
		folders:       make(map[int64]map[int]domain.DialogFolder),
		folderOrder:   make(map[int64][]int),
		folderTags:    make(map[int64]bool),
		archivePinned: make(map[int64]bool),
		translations:  make(map[translationPreferenceKey]bool),
	}
}

func (s *DialogStore) ListByUser(_ context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error) {
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	list.Dialogs = cloneDialogs(list.Dialogs)
	list.Messages = cloneMessages(list.Messages)
	list.Users = append([]domain.User(nil), list.Users...)
	return filterDialogList(list, filter), nil
}

func (s *DialogStore) ListByPeers(_ context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error) {
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	list.Dialogs = cloneDialogs(list.Dialogs)
	list.Messages = cloneMessages(list.Messages)
	list.Users = append([]domain.User(nil), list.Users...)

	byPeer := make(map[domain.Peer]domain.Dialog, len(list.Dialogs))
	for _, dialog := range list.Dialogs {
		byPeer[dialog.Peer] = dialog
	}
	out := domain.DialogList{
		Dialogs: make([]domain.Dialog, 0, len(peers)),
		Users:   make([]domain.User, 0, len(peers)),
	}
	seenPeers := make(map[domain.Peer]struct{}, len(peers))
	seenUsers := map[int64]struct{}{}
	for _, peer := range peers {
		if _, ok := seenPeers[peer]; ok {
			continue
		}
		seenPeers[peer] = struct{}{}
		dialog := byPeer[peer]
		if dialog.Peer.ID == 0 {
			dialog.Peer = peer
		}
		out.Dialogs = append(out.Dialogs, dialog)
		if peer.Type == domain.PeerTypeUser {
			if user, ok := findDialogUser(list.Users, peer.ID); ok {
				appendDialogUser(&out, seenUsers, user)
			} else if u, ok := domain.SystemUserByID(peer.ID); ok {
				appendDialogUser(&out, seenUsers, u)
			}
		}
	}
	out.Messages = keepDialogMessages(list.Messages, out.Dialogs)
	out.Count = len(out.Dialogs)
	out.Hash = dialogListHash(out.Dialogs)
	return out, nil
}

// SaveList 保存一份用户会话列表，供测试和本地替身使用。
func (s *DialogStore) SaveList(_ context.Context, userID int64, list domain.DialogList) error {
	list.Dialogs = cloneDialogs(list.Dialogs)
	list.Messages = cloneMessages(list.Messages)
	list.Users = append([]domain.User(nil), list.Users...)
	s.mu.Lock()
	s.m[userID] = list
	s.mu.Unlock()
	return nil
}

func (s *DialogStore) Upsert(_ context.Context, userID int64, dialog domain.Dialog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	for i, existing := range list.Dialogs {
		if existing.Peer == dialog.Peer {
			if dialog.FolderID == domain.DialogMainFolderID && existing.FolderID != domain.DialogMainFolderID {
				dialog.FolderID = existing.FolderID
			}
			list.Dialogs[i] = dialog
			s.m[userID] = list
			return nil
		}
	}
	list.Dialogs = append(list.Dialogs, dialog)
	s.m[userID] = list
	return nil
}

func (s *DialogStore) UpsertInbox(_ context.Context, userID int64, dialog domain.Dialog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	for i, existing := range list.Dialogs {
		if existing.Peer != dialog.Peer {
			continue
		}
		if dialog.TopMessage >= existing.TopMessage {
			existing.TopMessage = dialog.TopMessage
			existing.TopMessageDate = dialog.TopMessageDate
		}
		existing.UnreadCount = countInboxUnread(list, existing.Peer, existing.ReadInboxMaxID, existing.TopMessage)
		list.Dialogs[i] = existing
		s.m[userID] = list
		return nil
	}
	dialog.UnreadCount = countInboxUnread(list, dialog.Peer, dialog.ReadInboxMaxID, dialog.TopMessage)
	if dialog.UnreadCount == 0 {
		dialog.UnreadCount = 1
	}
	list.Dialogs = append(list.Dialogs, dialog)
	s.m[userID] = list
	return nil
}

func countInboxUnread(list domain.DialogList, peer domain.Peer, readMax, topMessage int) int {
	unread := 0
	for _, msg := range list.Messages {
		if msg.Peer == peer && !msg.Out && msg.ID > readMax && (topMessage <= 0 || msg.ID <= topMessage) {
			unread++
		}
	}
	return unread
}

func (s *DialogStore) SaveDraft(_ context.Context, userID int64, draft domain.DialogDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts[userID] == nil {
		s.drafts[userID] = make(map[dialogDraftKey]domain.DialogDraft)
	}
	s.drafts[userID][draftKey(draft.Peer, draft.TopMessageID)] = cloneDialogDraft(draft)
	return nil
}

func (s *DialogStore) GetDraft(_ context.Context, userID int64, peer domain.Peer, topMessageID int) (domain.DialogDraft, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	draft, ok := s.drafts[userID][draftKey(peer, topMessageID)]
	if !ok {
		return domain.DialogDraft{}, false, nil
	}
	return cloneDialogDraft(draft), true, nil
}

func (s *DialogStore) DeleteDraft(_ context.Context, userID int64, peer domain.Peer, topMessageID int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.drafts[userID]
	if len(items) == 0 {
		return false, nil
	}
	key := draftKey(peer, topMessageID)
	if _, ok := items[key]; !ok {
		return false, nil
	}
	delete(items, key)
	return true, nil
}

func (s *DialogStore) ListDrafts(_ context.Context, userID int64, limit int) ([]domain.DialogDraft, error) {
	s.mu.RLock()
	items := s.drafts[userID]
	out := make([]domain.DialogDraft, 0, len(items))
	for _, draft := range items {
		out = append(out, cloneDialogDraft(draft))
	}
	s.mu.RUnlock()
	sortDialogDrafts(out)
	if limit <= 0 || limit > domain.MaxDialogDraftsPerUser {
		limit = domain.MaxDialogDraftsPerUser
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *DialogStore) ClearDrafts(_ context.Context, userID int64, limit int) ([]domain.DialogDraft, error) {
	if limit <= 0 || limit > domain.MaxDialogDraftsPerUser {
		limit = domain.MaxDialogDraftsPerUser
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.drafts[userID]
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]domain.DialogDraft, 0, len(items))
	for _, draft := range items {
		out = append(out, cloneDialogDraft(draft))
	}
	sortDialogDrafts(out)
	if len(out) > limit {
		out = out[:limit]
	}
	for _, draft := range out {
		delete(items, draftKey(draft.Peer, draft.TopMessageID))
	}
	return out, nil
}

func (s *DialogStore) MarkRead(_ context.Context, userID int64, peer domain.Peer, maxID int) (domain.ReadHistoryResult, error) {
	result := domain.ReadHistoryResult{OwnerUserID: userID, Peer: peer, MaxID: maxID}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	for i, dialog := range list.Dialogs {
		if dialog.Peer != peer {
			continue
		}
		readMax := maxID
		if readMax <= 0 {
			readMax = dialog.TopMessage
		}
		if readMax > dialog.TopMessage {
			readMax = dialog.TopMessage
		}
		result.MaxID = readMax
		result.Changed = dialog.UnreadCount > 0 || readMax > dialog.ReadInboxMaxID
		if readMax > dialog.ReadInboxMaxID {
			dialog.ReadInboxMaxID = readMax
		}
		if readMax >= dialog.TopMessage {
			dialog.UnreadCount = 0
			dialog.UnreadMentions = 0
			dialog.UnreadReactions = 0
		}
		dialog.UnreadMark = false
		result.StillUnreadCount = dialog.UnreadCount
		list.Dialogs[i] = dialog
		s.m[userID] = list
		return result, nil
	}
	return result, nil
}

func (s *DialogStore) SetPinned(_ context.Context, userID int64, peer domain.Peer, pinned bool) (bool, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	targetFolderID := domain.DialogMainFolderID
	for _, dialog := range list.Dialogs {
		if dialog.Peer == peer {
			targetFolderID = dialog.FolderID
			break
		}
	}
	// order 仅在目标会话所在 folder 内分配；memory 双 store 互不可见，
	// 与 postgres 跨表统一 order 空间相比此处只看私聊表（reorder 会统一重排）。
	nextOrder := 1
	for _, dialog := range list.Dialogs {
		if dialog.Pinned && dialog.FolderID == targetFolderID && dialog.PinnedOrder >= nextOrder {
			nextOrder = dialog.PinnedOrder + 1
		}
	}
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer != peer {
			continue
		}
		list.Dialogs[i].Pinned = pinned
		if pinned {
			if list.Dialogs[i].PinnedOrder == 0 {
				list.Dialogs[i].PinnedOrder = nextOrder
			}
		} else {
			list.Dialogs[i].PinnedOrder = 0
		}
		s.m[userID] = list
		return true, targetFolderID, nil
	}
	return false, 0, nil
}

func (s *DialogStore) ReorderPinned(_ context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	changed := false
	positions := make(map[domain.Peer]int, len(order))
	for i, peer := range order {
		if peer.Type == "" || peer.ID == 0 {
			continue
		}
		if _, ok := positions[peer]; ok {
			continue
		}
		positions[peer] = len(order) - i
	}
	for i := range list.Dialogs {
		if list.Dialogs[i].FolderID != folderID {
			continue
		}
		pos, ok := positions[list.Dialogs[i].Peer]
		if ok {
			if !list.Dialogs[i].Pinned || list.Dialogs[i].PinnedOrder != pos {
				changed = true
			}
			list.Dialogs[i].Pinned = true
			list.Dialogs[i].PinnedOrder = pos
			continue
		}
		if force && list.Dialogs[i].Pinned {
			changed = true
			list.Dialogs[i].Pinned = false
			list.Dialogs[i].PinnedOrder = 0
		}
	}
	s.m[userID] = list
	return changed, nil
}

func (s *DialogStore) SetUnreadMark(_ context.Context, userID int64, peer domain.Peer, unread bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer != peer {
			continue
		}
		// 值守卫对齐 postgres：重复标记同值返回 changed=false，避免幽灵事件。
		if list.Dialogs[i].UnreadMark == unread {
			return false, nil
		}
		list.Dialogs[i].UnreadMark = unread
		s.m[userID] = list
		return true, nil
	}
	return false, nil
}

func (s *DialogStore) ListUnreadMarked(_ context.Context, userID int64) ([]domain.Peer, error) {
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	out := make([]domain.Peer, 0, len(list.Dialogs))
	for _, dialog := range list.Dialogs {
		if dialog.UnreadMark {
			out = append(out, dialog.Peer)
		}
	}
	return out, nil
}

func (s *DialogStore) SetChatTheme(_ context.Context, userID int64, peer domain.Peer, emoticon string) (bool, error) {
	if userID == 0 || peer.Type == "" || peer.ID == 0 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer != peer {
			continue
		}
		if list.Dialogs[i].ThemeEmoticon == emoticon {
			return false, nil
		}
		list.Dialogs[i].ThemeEmoticon = emoticon
		s.m[userID] = list
		return true, nil
	}
	if emoticon == "" {
		return false, nil
	}
	list.Dialogs = append(list.Dialogs, domain.Dialog{Peer: peer, ThemeEmoticon: emoticon})
	s.m[userID] = list
	return true, nil
}

func (s *DialogStore) SetPeerSettingsBarHidden(_ context.Context, userID int64, peer domain.Peer) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer != peer {
			continue
		}
		list.Dialogs[i].PeerSettingsBarHidden = true
		s.m[userID] = list
		return true, nil
	}
	return false, nil
}

func (s *DialogStore) PeerSettingsBarHidden(_ context.Context, userID int64, peer domain.Peer) (bool, error) {
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	for _, dialog := range list.Dialogs {
		if dialog.Peer == peer {
			return dialog.PeerSettingsBarHidden, nil
		}
	}
	return false, nil
}

func (s *DialogStore) SetTranslationDisabled(_ context.Context, userID int64, peer domain.Peer, disabled bool) (bool, error) {
	key := translationPreferenceKey{userID: userID, peer: peer}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.translations[key]
	if disabled {
		s.translations[key] = true
	} else {
		delete(s.translations, key)
	}
	return previous != disabled, nil
}

func (s *DialogStore) TranslationDisabled(_ context.Context, userID int64, peer domain.Peer) (bool, error) {
	s.mu.RLock()
	disabled := s.translations[translationPreferenceKey{userID: userID, peer: peer}]
	s.mu.RUnlock()
	return disabled, nil
}

func (s *DialogStore) ListFolders(_ context.Context, userID int64) (domain.DialogFolderList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID := s.folders[userID]
	order := append([]int(nil), s.folderOrder[userID]...)
	seen := make(map[int]struct{}, len(byID))
	out := domain.DialogFolderList{
		TagsEnabled: s.folderTags[userID],
		Folders:     make([]domain.DialogFolder, 0, len(byID)),
	}
	for _, id := range order {
		folder, ok := byID[id]
		if !ok {
			continue
		}
		seen[id] = struct{}{}
		out.Folders = append(out.Folders, cloneDialogFolder(folder))
	}
	remaining := make([]int, 0, len(byID))
	for id := range byID {
		if _, ok := seen[id]; !ok {
			remaining = append(remaining, id)
		}
	}
	sort.Ints(remaining)
	for _, id := range remaining {
		out.Folders = append(out.Folders, cloneDialogFolder(byID[id]))
	}
	return out, nil
}

func (s *DialogStore) GetFolder(_ context.Context, userID int64, folderID int) (domain.DialogFolder, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	folder, ok := s.folders[userID][folderID]
	if !ok {
		return domain.DialogFolder{}, false, nil
	}
	return cloneDialogFolder(folder), true, nil
}

func (s *DialogStore) UpsertFolder(_ context.Context, userID int64, folder domain.DialogFolder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.folders[userID] == nil {
		s.folders[userID] = make(map[int]domain.DialogFolder)
	}
	s.folders[userID][folder.ID] = cloneDialogFolder(folder)
	if !containsInt(s.folderOrder[userID], folder.ID) {
		s.folderOrder[userID] = append(s.folderOrder[userID], folder.ID)
	}
	return nil
}

func (s *DialogStore) DeleteFolder(_ context.Context, userID int64, folderID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.folders[userID], folderID)
	s.folderOrder[userID] = removeInt(s.folderOrder[userID], folderID)
	return nil
}

func (s *DialogStore) ReorderFolders(_ context.Context, userID int64, order []int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	byID := s.folders[userID]
	seen := make(map[int]struct{}, len(order))
	next := make([]int, 0, len(byID))
	for _, id := range order {
		if id < domain.DialogCustomFolderMinID {
			continue
		}
		if _, ok := byID[id]; !ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		next = append(next, id)
	}
	remaining := make([]int, 0, len(byID))
	for id := range byID {
		if _, ok := seen[id]; !ok {
			remaining = append(remaining, id)
		}
	}
	sort.Ints(remaining)
	next = append(next, remaining...)
	s.folderOrder[userID] = next
	return nil
}

func (s *DialogStore) SetFolderTagsEnabled(_ context.Context, userID int64, enabled bool) error {
	s.mu.Lock()
	s.folderTags[userID] = enabled
	s.mu.Unlock()
	return nil
}

func (s *DialogStore) EditPeerFolders(_ context.Context, userID int64, peers []domain.FolderPeerUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	updates := make(map[domain.Peer]int, len(peers))
	for _, item := range peers {
		if item.Peer.Type == "" || item.Peer.ID == 0 {
			continue
		}
		if item.FolderID != domain.DialogMainFolderID && item.FolderID != domain.DialogArchiveFolderID {
			continue
		}
		updates[item.Peer] = item.FolderID
	}
	for i := range list.Dialogs {
		folderID, ok := updates[list.Dialogs[i].Peer]
		if !ok {
			continue
		}
		// 换 folder 时清 pinned：TDesktop 在归档/还原时本地无条件 unpin，
		// 服务端保留旧 pin 会在下次 getDialogs 时把状态漂移回来。
		if list.Dialogs[i].FolderID != folderID {
			list.Dialogs[i].Pinned = false
			list.Dialogs[i].PinnedOrder = 0
		}
		list.Dialogs[i].FolderID = folderID
	}
	s.m[userID] = list
	return nil
}

func (s *DialogStore) SetArchivePinned(_ context.Context, userID int64, pinned bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.archivePinned[userID]
	if !ok {
		current = true
	}
	s.archivePinned[userID] = pinned
	return current != pinned, nil
}

func (s *DialogStore) ArchivePinned(_ context.Context, userID int64) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if pinned, ok := s.archivePinned[userID]; ok {
		return pinned, nil
	}
	return true, nil
}

func (s *DialogStore) CountArchiveUnread(_ context.Context, userID int64) (int, int, error) {
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	peers, messages := 0, 0
	for _, dialog := range list.Dialogs {
		if dialog.FolderID != domain.DialogArchiveFolderID {
			continue
		}
		if dialog.UnreadCount > 0 || dialog.UnreadMark {
			peers++
		}
		messages += dialog.UnreadCount
	}
	return peers, messages, nil
}

func upsertMemoryDialog(list domain.DialogList, dialog domain.Dialog) domain.DialogList {
	for i := range list.Dialogs {
		if list.Dialogs[i].Peer == dialog.Peer {
			if dialog.ReadInboxMaxID == 0 {
				dialog.ReadInboxMaxID = list.Dialogs[i].ReadInboxMaxID
			}
			if dialog.ReadOutboxMaxID == 0 {
				dialog.ReadOutboxMaxID = list.Dialogs[i].ReadOutboxMaxID
			}
			if dialog.FolderID == domain.DialogMainFolderID && list.Dialogs[i].FolderID != domain.DialogMainFolderID {
				dialog.FolderID = list.Dialogs[i].FolderID
			}
			if dialog.UnreadCount == 0 {
				dialog.UnreadCount = list.Dialogs[i].UnreadCount
			}
			if dialog.UnreadMentions == 0 {
				dialog.UnreadMentions = list.Dialogs[i].UnreadMentions
			}
			if dialog.UnreadReactions == 0 {
				dialog.UnreadReactions = list.Dialogs[i].UnreadReactions
			}
			if !dialog.UnreadMark {
				dialog.UnreadMark = list.Dialogs[i].UnreadMark
			}
			// 与 postgres 的 ON CONFLICT 局部更新对齐：消息路径的 upsert
			// 不得抹掉既有置顶/设置类状态。
			if !dialog.Pinned && list.Dialogs[i].Pinned {
				dialog.Pinned = list.Dialogs[i].Pinned
				dialog.PinnedOrder = list.Dialogs[i].PinnedOrder
			}
			if dialog.TTLPeriod == 0 {
				dialog.TTLPeriod = list.Dialogs[i].TTLPeriod
			}
			if dialog.ThemeEmoticon == "" {
				dialog.ThemeEmoticon = list.Dialogs[i].ThemeEmoticon
			}
			if !dialog.HasScheduled {
				dialog.HasScheduled = list.Dialogs[i].HasScheduled
			}
			if !dialog.PeerSettingsBarHidden {
				dialog.PeerSettingsBarHidden = list.Dialogs[i].PeerSettingsBarHidden
			}
			if !dialog.ViewForumAsMessages {
				dialog.ViewForumAsMessages = list.Dialogs[i].ViewForumAsMessages
			}
			if dialog.Draft == nil {
				dialog.Draft = list.Dialogs[i].Draft
			}
			list.Dialogs[i] = dialog
			return list
		}
	}
	list.Dialogs = append(list.Dialogs, dialog)
	return list
}

func filterDialogList(list domain.DialogList, filter domain.DialogFilter) domain.DialogList {
	sort.SliceStable(list.Dialogs, func(i, j int) bool {
		return dialogLess(list.Dialogs[i], list.Dialogs[j])
	})

	base := make([]domain.Dialog, 0, len(list.Dialogs))
	for _, d := range list.Dialogs {
		if !dialogMatchesFolder(d, list.Users, filter) {
			continue
		}
		if filter.PinnedOnly && !d.Pinned {
			continue
		}
		if filter.ExcludePinned && d.Pinned {
			continue
		}
		base = append(base, d)
	}

	list.Count = len(base)
	list.Hash = dialogListHash(base)
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	page := make([]domain.Dialog, 0, len(base))
	for _, d := range base {
		if !afterDialogOffset(d, filter) {
			continue
		}
		page = append(page, d)
		if len(page) >= limit {
			break
		}
	}
	list.Dialogs = page
	list.Messages = keepDialogMessages(list.Messages, page)
	return list
}

func dialogMatchesFolder(d domain.Dialog, users []domain.User, filter domain.DialogFilter) bool {
	if !filter.HasFolderID {
		// 不带 folder_id 视为主列表（folder 0）：归档对话只以 dialogFolder
		// 聚合条目出现，DrKLO Android 主列表请求不设 flag。
		return d.FolderID == domain.DialogMainFolderID
	}
	if filter.FolderID < domain.DialogCustomFolderMinID {
		return d.FolderID == filter.FolderID
	}
	if filter.Folder == nil {
		return false
	}
	folder := filter.Folder
	if folder.ExcludeArchived && d.FolderID == domain.DialogArchiveFolderID {
		return false
	}
	if folder.ExcludeRead && d.UnreadCount == 0 && !d.UnreadMark {
		return false
	}
	if hasFolderPeer(folder.ExcludePeers, d.Peer) {
		return false
	}
	if hasFolderPeer(folder.IncludePeers, d.Peer) || hasFolderPeer(folder.PinnedPeers, d.Peer) {
		return true
	}
	if d.Peer.Type == domain.PeerTypeUser {
		user, ok := findDialogUser(users, d.Peer.ID)
		if ok && user.Contact && folder.Contacts {
			return true
		}
		if (!ok || !user.Contact) && folder.NonContacts {
			return true
		}
	}
	return false
}

func hasFolderPeer(peers []domain.DialogFolderPeer, peer domain.Peer) bool {
	for _, item := range peers {
		if item.Peer == peer {
			return true
		}
	}
	return false
}

func dialogLess(a, b domain.Dialog) bool {
	if a.Pinned != b.Pinned {
		return a.Pinned && !b.Pinned
	}
	if a.Pinned && b.Pinned && a.PinnedOrder != b.PinnedOrder {
		if a.PinnedOrder == 0 {
			return false
		}
		if b.PinnedOrder == 0 {
			return true
		}
		return a.PinnedOrder > b.PinnedOrder
	}
	if a.TopMessageDate != b.TopMessageDate {
		return a.TopMessageDate > b.TopMessageDate
	}
	if a.TopMessage != b.TopMessage {
		return a.TopMessage > b.TopMessage
	}
	return a.Peer.ID > b.Peer.ID
}

func afterDialogOffset(d domain.Dialog, filter domain.DialogFilter) bool {
	if filter.OffsetDate <= 0 && filter.OffsetID <= 0 {
		return true
	}
	if filter.OffsetDate > 0 {
		if d.TopMessageDate != filter.OffsetDate {
			return d.TopMessageDate < filter.OffsetDate
		}
		if filter.OffsetID <= 0 {
			return false
		}
		if d.TopMessage != filter.OffsetID {
			return d.TopMessage < filter.OffsetID
		}
		if filter.HasOffsetPeer {
			return d.Peer.ID < filter.OffsetPeer.ID
		}
		return false
	}
	return d.TopMessage < filter.OffsetID
}

func keepDialogMessages(messages []domain.Message, dialogs []domain.Dialog) []domain.Message {
	want := make(map[int]struct{}, len(dialogs))
	for _, d := range dialogs {
		if d.TopMessage != 0 {
			want[d.TopMessage] = struct{}{}
		}
	}
	out := make([]domain.Message, 0, len(want))
	for _, msg := range messages {
		if _, ok := want[msg.ID]; ok {
			out = append(out, msg)
		}
	}
	return out
}

func findDialogUser(users []domain.User, id int64) (domain.User, bool) {
	for _, user := range users {
		if user.ID == id {
			return user, true
		}
	}
	return domain.User{}, false
}

func appendDialogUser(list *domain.DialogList, seen map[int64]struct{}, user domain.User) {
	if user.ID == 0 {
		return
	}
	if _, ok := seen[user.ID]; ok {
		return
	}
	seen[user.ID] = struct{}{}
	list.Users = append(list.Users, user)
}

func cloneDialogs(dialogs []domain.Dialog) []domain.Dialog {
	out := append([]domain.Dialog(nil), dialogs...)
	for i := range out {
		if out[i].Draft != nil {
			draft := cloneDialogDraft(*out[i].Draft)
			out[i].Draft = &draft
		}
	}
	return out
}

func cloneDialogDraft(draft domain.DialogDraft) domain.DialogDraft {
	draft.Entities = append([]domain.MessageEntity(nil), draft.Entities...)
	draft.ReplyTo = cloneMessageReply(draft.ReplyTo)
	if draft.WebPage != nil {
		webpage := *draft.WebPage
		draft.WebPage = &webpage
	}
	draft.RichMessage = cloneRichMessage(draft.RichMessage)
	if draft.SuggestedPost != nil {
		suggested := *draft.SuggestedPost
		if suggested.Price != nil {
			price := *suggested.Price
			suggested.Price = &price
		}
		draft.SuggestedPost = &suggested
	}
	return draft
}

func sortDialogDrafts(drafts []domain.DialogDraft) {
	sort.SliceStable(drafts, func(i, j int) bool {
		if drafts[i].Date != drafts[j].Date {
			return drafts[i].Date > drafts[j].Date
		}
		if drafts[i].Peer.Type != drafts[j].Peer.Type {
			return drafts[i].Peer.Type < drafts[j].Peer.Type
		}
		if drafts[i].Peer.ID != drafts[j].Peer.ID {
			return drafts[i].Peer.ID > drafts[j].Peer.ID
		}
		return drafts[i].TopMessageID > drafts[j].TopMessageID
	})
}

func cloneDialogFolder(folder domain.DialogFolder) domain.DialogFolder {
	folder.TitleEntities = append([]domain.MessageEntity(nil), folder.TitleEntities...)
	folder.PinnedPeers = append([]domain.DialogFolderPeer(nil), folder.PinnedPeers...)
	folder.IncludePeers = append([]domain.DialogFolderPeer(nil), folder.IncludePeers...)
	folder.ExcludePeers = append([]domain.DialogFolderPeer(nil), folder.ExcludePeers...)
	return folder
}

func containsInt(items []int, value int) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func removeInt(items []int, value int) []int {
	out := items[:0]
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

func dialogListHash(dialogs []domain.Dialog) int64 {
	if len(dialogs) == 0 {
		return 0
	}
	h := fnv.New64a()
	var buf [47]byte
	for _, d := range dialogs {
		binary.LittleEndian.PutUint64(buf[:8], uint64(d.Peer.ID))
		binary.LittleEndian.PutUint32(buf[8:12], uint32(d.FolderID))
		binary.LittleEndian.PutUint32(buf[12:16], uint32(d.TopMessage))
		binary.LittleEndian.PutUint32(buf[16:20], uint32(d.TopMessageDate))
		binary.LittleEndian.PutUint32(buf[20:24], uint32(d.ReadInboxMaxID))
		binary.LittleEndian.PutUint32(buf[24:28], uint32(d.ReadOutboxMaxID))
		binary.LittleEndian.PutUint32(buf[28:32], uint32(d.UnreadCount))
		binary.LittleEndian.PutUint32(buf[32:36], uint32(d.UnreadMentions))
		binary.LittleEndian.PutUint32(buf[36:40], uint32(d.UnreadReactions))
		if d.Pinned {
			buf[40] = 1
		} else {
			buf[40] = 0
		}
		binary.LittleEndian.PutUint32(buf[41:45], uint32(d.PinnedOrder))
		if d.UnreadMark {
			buf[45] = 1
		} else {
			buf[45] = 0
		}
		if d.PeerSettingsBarHidden {
			buf[46] = 1
		} else {
			buf[46] = 0
		}
		_, _ = h.Write(buf[:])
		_, _ = h.Write([]byte(d.ThemeEmoticon))
	}
	return int64(h.Sum64())
}
