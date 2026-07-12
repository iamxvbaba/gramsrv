package store

import (
	"context"

	"telesrv/internal/domain"
)

// DialogStore 持久化用户会话摘要。
type DialogStore interface {
	ListByUser(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error)
	ListByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error)
	Upsert(ctx context.Context, userID int64, dialog domain.Dialog) error
	// UpsertInbox records a newly received private message in a dialog without
	// overwriting existing read watermarks or pinned/folder metadata.
	UpsertInbox(ctx context.Context, userID int64, dialog domain.Dialog) error
	SaveDraft(ctx context.Context, userID int64, draft domain.DialogDraft) error
	GetDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (domain.DialogDraft, bool, error)
	DeleteDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (bool, error)
	ListDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error)
	ClearDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error)
	MarkRead(ctx context.Context, userID int64, peer domain.Peer, maxID int) (domain.ReadHistoryResult, error)
	// SetPinned 置顶/取消置顶一条会话；order 在会话当前 folder 内分配，
	// 返回 (changed, 该会话所在 folder_id)。
	SetPinned(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, int, error)
	// ReorderPinned 重排指定 folder（0 主列表/1 归档）内的置顶顺序；
	// force 只清除该 folder 内不在 order 中的置顶。
	ReorderPinned(ctx context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error)
	SetUnreadMark(ctx context.Context, userID int64, peer domain.Peer, unread bool) (bool, error)
	ListUnreadMarked(ctx context.Context, userID int64) ([]domain.Peer, error)
	SetChatTheme(ctx context.Context, userID int64, peer domain.Peer, emoticon string) (bool, error)
	SetPeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	PeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	// SetTranslationDisabled persists the per-account peer preference used by
	// messages.togglePeerTranslations without creating a synthetic dialog.
	SetTranslationDisabled(ctx context.Context, userID int64, peer domain.Peer, disabled bool) (bool, error)
	TranslationDisabled(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	ListFolders(ctx context.Context, userID int64) (domain.DialogFolderList, error)
	GetFolder(ctx context.Context, userID int64, folderID int) (domain.DialogFolder, bool, error)
	UpsertFolder(ctx context.Context, userID int64, folder domain.DialogFolder) error
	DeleteFolder(ctx context.Context, userID int64, folderID int) error
	ReorderFolders(ctx context.Context, userID int64, order []int) error
	SetFolderTagsEnabled(ctx context.Context, userID int64, enabled bool) error
	EditPeerFolders(ctx context.Context, userID int64, peers []domain.FolderPeerUpdate) error
	// CountArchiveUnread 统计归档（folder_id=1）中有未读（含手动标记）的
	// 会话数与未读消息总数，供主列表 dialogFolder 条目聚合。
	CountArchiveUnread(ctx context.Context, userID int64) (peers int, messages int, err error)
	// SetArchivePinned 设置 archive folder 行本身的置顶状态
	// （toggleDialogPin(inputDialogPeerFolder)），返回是否发生变化。
	SetArchivePinned(ctx context.Context, userID int64, pinned bool) (bool, error)
	// ArchivePinned 返回 archive folder 行置顶状态；从未设置过时官方默认 true。
	ArchivePinned(ctx context.Context, userID int64) (bool, error)
}
