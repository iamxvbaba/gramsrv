package store

import (
	"context"

	"telesrv/internal/domain"
)

// DialogStore persists per-user dialog summaries.
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
	ListDraftsByPeers(ctx context.Context, userID int64, peers []domain.Peer) ([]domain.DialogDraft, error)
	ClearDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error)
	MarkRead(ctx context.Context, userID int64, peer domain.Peer, maxID int) (domain.ReadHistoryResult, error)
	// SetPinned pins/unpins a dialog; order is assigned within the dialog's current
	// folder, returning (changed, the folder_id the dialog belongs to).
	SetPinned(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, int, error)
	// ReorderPinned reorders pins within the given folder (0 main list/1 archived);
	// force only clears pins in that folder that are not in order.
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
	// CountArchiveUnread counts dialogs with unread (including manually marked) and the
	// total unread message count in the archive (folder_id=1), for main-list dialogFolder
	// entry aggregation.
	CountArchiveUnread(ctx context.Context, userID int64) (peers int, messages int, err error)
	// SetArchivePinned sets the pin state of the archive folder row itself
	// (toggleDialogPin(inputDialogPeerFolder)) and returns whether it changed.
	SetArchivePinned(ctx context.Context, userID int64, pinned bool) (bool, error)
	// ArchivePinned returns the archive folder row pin state; the official default is true when never set.
	ArchivePinned(ctx context.Context, userID int64) (bool, error)
}
