package domain

// SavedHiddenAuthorUserID is the placeholder user id of the official hidden-author saved-messages sub-chat
// (TDesktop kSavedHiddenAuthorId / TDLib HIDDEN_AUTHOR_DIALOG_ID).
// New runtime writes can always determine the source chat and never produce this value; only in backfill
// are messages whose "fwd header has only from_name and the source chat is unknowable" grouped into this sub-chat.
const SavedHiddenAuthorUserID int64 = 2666000

// MaxPinnedSavedDialogs is the saved-messages sub-chat pin limit (same tier as the official Premium limit).
const MaxPinnedSavedDialogs = 100

// MaxSavedDialogsLimit is the per-page limit of messages.getSavedDialogs.
const MaxSavedDialogsLimit = 100

// SavedPeerForSelfChat computes the saved sub-chat grouping key for a new self-chat message, aligned with
// TDLib SavedMessagesTopicId semantics: a forward with a source chat (saved_from_peer) →
// the source chat; a fwd header with only from_name and an unknowable source → the hidden-author placeholder (a defensive
// branch; runtime forwards always carry a source chat); everything else (direct send, drop_author) → self.
func SavedPeerForSelfChat(selfUserID int64, forward *MessageForward) Peer {
	if forward != nil {
		if forward.SavedFrom.ID != 0 {
			return forward.SavedFrom
		}
		if forward.From.ID == 0 && forward.FromName != "" {
			return Peer{Type: PeerTypeUser, ID: SavedHiddenAuthorUserID}
		}
	}
	return Peer{Type: PeerTypeUser, ID: selfUserID}
}

// SavedDialog is one saved-messages sub-chat (the business form of the savedDialog TL constructor).
type SavedDialog struct {
	Peer Peer
	// TopMessage is the owner-perspective box id of the sub-chat's current latest visible message.
	TopMessage int
	Pinned     bool
}

// SavedDialogList is the business-layer result of getSavedDialogs/getPinnedSavedDialogs/getSavedDialogsByID.
// Messages and Dialogs correspond one-to-one (a full row per top message).
type SavedDialogList struct {
	Dialogs  []SavedDialog
	Messages []Message
	Users    []User
	Channels []Channel
	// Count is the total number of sub-chats under the filter scope (with/without pinned).
	Count int
	// Full being true means this page already contains all remaining sub-chats under the filter scope
	// (maps to messages.savedDialogs; false maps to savedDialogsSlice).
	Full bool
}

// SavedDialogsFilter describes the getSavedDialogs pagination condition.
type SavedDialogsFilter struct {
	ExcludePinned bool
	// OffsetID is the top message box id of the previous page's last sub-chat; 0 or >= MaxMessageBoxID
	// is treated as starting from the newest (TDesktop sends 0 on the first page, DrKLO Android sends int32 max).
	OffsetID int
	// OffsetDate/OffsetPeer are only for validity checking; box id is monotonic in time, so paging always advances
	// by strictly descending top box id.
	OffsetDate int
	OffsetPeer Peer
	Limit      int
}

// DeleteSavedHistoryRequest is the business command of messages.deleteSavedHistory:
// delete the messages of one saved sub-chat in the self-chat. Self-chat is one-sided, with no revoke semantics.
type DeleteSavedHistoryRequest struct {
	OwnerUserID int64
	// SavedPeer is the sub-chat grouping key to clear.
	SavedPeer       Peer
	MaxID           int
	MinDate         int
	MaxDate         int
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// DeleteSavedHistoryResult describes one saved sub-chat deletion batch.
type DeleteSavedHistoryResult struct {
	// MessageIDs are the deleted owner-perspective box ids in this batch.
	MessageIDs []int
	Event      UpdateEvent
	// More being true means there are remaining batches; the client continues by affectedHistory.offset.
	More bool
}
