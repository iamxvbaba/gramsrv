package domain

// MessageEntityType identifies a message entity type.
type MessageEntityType string

const (
	MessageEntityBold          MessageEntityType = "bold"
	MessageEntityItalic        MessageEntityType = "italic"
	MessageEntityUnderline     MessageEntityType = "underline"
	MessageEntityStrike        MessageEntityType = "strike"
	MessageEntityCode          MessageEntityType = "code"
	MessageEntityPre           MessageEntityType = "pre"
	MessageEntityTextURL       MessageEntityType = "text_url"
	MessageEntityMentionName   MessageEntityType = "mention_name"
	MessageEntitySpoiler       MessageEntityType = "spoiler"
	MessageEntityBlockquote    MessageEntityType = "blockquote"
	MessageEntityCustomEmoji   MessageEntityType = "custom_emoji"
	MessageEntityMention       MessageEntityType = "mention"
	MessageEntityHashtag       MessageEntityType = "hashtag"
	MessageEntityCashtag       MessageEntityType = "cashtag"
	MessageEntityBotCommand    MessageEntityType = "bot_command"
	MessageEntityURL           MessageEntityType = "url"
	MessageEntityEmail         MessageEntityType = "email"
	MessageEntityPhone         MessageEntityType = "phone"
	MessageEntityBankCard      MessageEntityType = "bank_card"
	MessageEntityFormattedDate MessageEntityType = "formatted_date"
	MessageEntityDiffInsert    MessageEntityType = "diff_insert"
	MessageEntityDiffReplace   MessageEntityType = "diff_replace"
	MessageEntityDiffDelete    MessageEntityType = "diff_delete"
)

const (
	// MaxMessageTextLength matches the first-stage text message limit exposed to Telegram clients.
	MaxMessageTextLength = 4096
	// MaxMessageReplyQuoteLength matches TDesktop's quote_length_max app config default.
	MaxMessageReplyQuoteLength = 1024
	// MaxMessageReplyQuoteOffset bounds quote_offset, which is an offset inside message text, not a message id.
	MaxMessageReplyQuoteOffset = 2 * MaxMessageTextLength // UTF-16 units, including surrogate pairs.
	// MaxMessageEntityCount limits styled text entity vectors in message text and quotes.
	MaxMessageEntityCount = 256
	// MaxMessageBoxID is the largest message id safely expressible by a TL int / PostgreSQL int4.
	MaxMessageBoxID = 1<<31 - 1
	// MaxDeleteMessageIDs limits the owner-perspective id count of a single deleteMessages/updateDeleteMessages.
	// Large history cleanup goes through deleteHistory in batches, so a single RPC cannot build a huge array or durable payload.
	MaxDeleteMessageIDs = 1000
	// MaxGetMessageIDs limits the exact-ID batch of getMessages / channels.getMessages.
	MaxGetMessageIDs = 100
	// MaxDeleteHistoryBatch limits the number of message boxes a single deleteHistory actually cleans.
	// affectedHistory.Offset > 0 lets the client keep calling, and the server does not RETURN the whole history at once.
	MaxDeleteHistoryBatch = 1000
	// MaxForwardMessageIDs limits the owner-perspective id count of a single forwardMessages.
	MaxForwardMessageIDs = 100
	// MaxMessageHistoryAddOffset limits the absolute value of history/search add_offset.
	// TDesktop normally uses only small window offsets; the server must refuse to turn an oversized client value into a SQL OFFSET or slice capacity.
	MaxMessageHistoryAddOffset = 100
)

// ClampMessageHistoryAddOffset bounds Telegram history/search add_offset to a small local window.
func ClampMessageHistoryAddOffset(v int) int {
	if v > MaxMessageHistoryAddOffset {
		return MaxMessageHistoryAddOffset
	}
	if v < -MaxMessageHistoryAddOffset {
		return -MaxMessageHistoryAddOffset
	}
	return v
}

// ValidateMessageReplyBounds validates reply fields that are independent of peer visibility.
func ValidateMessageReplyBounds(reply *MessageReply) error {
	if reply == nil {
		return nil
	}
	if reply.MessageID < 0 || reply.MessageID > MaxMessageBoxID {
		return ErrReplyMessageIDInvalid
	}
	if reply.TopMessageID < 0 || reply.TopMessageID > MaxMessageBoxID {
		return ErrReplyMessageIDInvalid
	}
	if reply.StoryID < 0 || reply.StoryID > MaxStoryID {
		return ErrReplyMessageIDInvalid
	}
	// A story reply (StoryID>0) carries no MessageID/TopMessageID; a normal reply has at least one.
	if reply.MessageID == 0 && reply.TopMessageID == 0 && reply.StoryID == 0 && reply.External == nil {
		return ErrReplyMessageIDInvalid
	}
	if reply.QuoteOffset < 0 || reply.QuoteOffset > MaxMessageReplyQuoteOffset {
		return ErrReplyMessageIDInvalid
	}
	return nil
}

// MessageEntity is a business-layer message entity, independent of TL types.
type MessageEntity struct {
	Type   MessageEntityType
	Offset int
	Length int
	// URL is used only by text_url.
	URL string
	// UserID is used only by mention_name.
	UserID int64
	// Language is used only by pre.
	Language string
	// DocumentID is used only by custom_emoji.
	DocumentID int64
	// Collapsed is used only by blockquote.
	Collapsed bool
	// Date / Relative / ShortTime / LongTime / ShortDate / LongDate / DayOfWeek
	// are used only by formatted_date.
	Date      int
	Relative  bool
	ShortTime bool
	LongTime  bool
	ShortDate bool
	LongDate  bool
	DayOfWeek bool
	// OldText is used only by AI compose diff_replace; ordinary message input does not accept this entity.
	OldText string
}

// Message is one private chat message from an account's perspective.
type Message struct {
	ID             int   // message box id from the current owner's perspective, exposed to Telegram clients
	UID            int64 // shared private-message body id, not exposed to clients
	RandomID       int64
	OwnerUserID    int64
	Peer           Peer
	From           Peer
	Date           int
	EditDate       int
	HideEdited     bool
	Out            bool
	Silent         bool
	NoForwards     bool
	Body           string
	Entities       []MessageEntity
	ReplyTo        *MessageReply
	Forward        *MessageForward
	Reactions      *ChannelMessageReactions
	Pts            int
	TTLPeriod      int
	ExpiresAt      int
	Media          *MessageMedia
	MediaUnread    bool
	ReactionUnread bool
	ViaBotID       int64
	// GroupedID is the album grouping id: the messages of one sendMultiMedia share the same non-zero value,
	// and the client renders them as a single album group. Non-album messages are always 0. Both boxes hold the same value.
	GroupedID int64
	// Effect is the message effect id (message.effect, flags2.2?long): the animation effect exclusive to a 1-1 private chat
	// (🎉/👍 etc.); both the sender and recipient boxes hold the same non-zero value and each plays it once; non-effect messages are always 0.
	// A forward carries no effect (the new message is always 0). Private chats only; groups/channels do not render it.
	Effect int64
	// ReplyMarkup is the reply/inline keyboard snapshot carried by a bot message. Only a bot outbound message may be
	// non-nil; an ordinary user message is always nil (the send-side is_bot gate). Both boxes hold the same snapshot (no per-viewer difference).
	ReplyMarkup *MessageReplyMarkup
	// RichMessage is the Layer 228 rich text message (richMessage) snapshot, optional; an ordinary message is always nil.
	RichMessage *MessageRichMessage
	// Pinned is the owner-perspective pin flag (official private-chat multi-pin semantics: each side's
	// box row holds it independently, and a non-pm_oneside operation flips both sides together).
	Pinned bool
	// Deleted is the owner-perspective soft-delete visibility marker. It is used only by update replay/difference recovery
	// to decide whether a message snapshot can still be sent; an ordinary history query should filter deleted boxes directly at the store layer.
	Deleted bool
	// SavedPeer is the Saved Messages sub-chat grouping key (message.saved_peer_id).
	// Only a self-chat box row is non-zero: a directly written note = self; a forward into saved messages = the source chat peer;
	// a backfill fallback uses the hidden-author placeholder user 2666000. A non-self-chat row is always the zero value.
	SavedPeer Peer
	PaidStars int64
}

// NewHistoryClearMessage projects an existing owner-perspective box in place into a
// messageActionHistoryClear service message. The caller passes only the identity fields that must be kept;
// the remaining body, media, reply, reaction, TTL, pin and other payloads are all cleared per the invariant.
func NewHistoryClearMessage(ownerUserID int64, peer Peer, boxID int, uid int64, date, pts int) Message {
	self := Peer{Type: PeerTypeUser, ID: ownerUserID}
	return Message{
		ID:          boxID,
		UID:         uid,
		OwnerUserID: ownerUserID,
		Peer:        peer,
		From:        self,
		Date:        date,
		Out:         true,
		Pts:         pts,
		Media: &MessageMedia{
			Kind: MessageMediaKindService,
			ServiceAction: &MessageServiceAction{
				Kind: MessageServiceActionHistoryClear,
			},
		},
	}
}

// IsHistoryClearServiceMessage reports whether the box is already a history-clear anchor.
func IsHistoryClearServiceMessage(msg Message) bool {
	return msg.Media != nil &&
		msg.Media.Kind == MessageMediaKindService &&
		msg.Media.ServiceAction != nil &&
		msg.Media.ServiceAction.Kind == MessageServiceActionHistoryClear
}

const MessageRichBlocksLegacyLayer = 228

// MessageRichMessage is the protocol-neutral snapshot of a rich text message (richMessage): a set of IV
// PageBlocks (Blocks) + embedded resolved Photos/Documents.
//
// Blocks stores the opaque bytes of the gotd TL-serialized []tg.PageBlockClass — the PageBlock system is huge and
// input (inputRichMessage.blocks) and output (richMessage.blocks) are isomorphic and passed through verbatim, so it is not modeled per type
// in domain; the rpc layer serializes the tg.PageBlock vector ↔ bytes (domain does not depend on tg).
// As with message media, Photos/Documents store resolved snapshots (including the viewer-independent access_hash),
// and projection reuses tgPhoto/tgDocument. HTML/Markdown input is also normalized to the same set of Blocks at the RPC boundary.
//
// BlocksLayer is the exact TL profile of these persisted bytes, not the sending client's Layer. Historical records
// lack this field; 0 is the formal storage-v1 marker, always interpreted as Layer 228. New writes must explicitly store
// the profile used at encoding time, and reads must not rely on trial-decoding constructors or falling back on failure.
type MessageRichMessage struct {
	Rtl         bool       `json:"rtl,omitempty"`
	Part        bool       `json:"part,omitempty"`
	BlocksLayer int        `json:"blocks_layer,omitempty"`
	Blocks      []byte     `json:"blocks,omitempty"`
	Photos      []Photo    `json:"photos,omitempty"`
	Documents   []Document `json:"documents,omitempty"`
	// BotAPIProjection is the Bot API RichMessage JSON derived by the RPC boundary from the same
	// set of validated PageBlocks. It is not a second source of truth: the write boundary may only
	// generate it from Blocks, and the HTTP Bot API projection is read-only, avoiding a reverse dependency of the botapi package on tg types.
	BotAPIProjection []byte `json:"bot_api_projection,omitempty"`
}

// EffectiveBlocksLayer returns the deterministic storage grammar for Blocks.
// A missing JSON field is the original storage-v1 format and therefore Layer
// 228; it is not an adaptive decode fallback.
func (m *MessageRichMessage) EffectiveBlocksLayer() int {
	if m == nil || m.BlocksLayer == 0 {
		return MessageRichBlocksLegacyLayer
	}
	return m.BlocksLayer
}

// IsZero means no rich text payload (skip an empty snapshot on persist, do not send rich_message on projection).
func (m *MessageRichMessage) IsZero() bool {
	return m == nil || (len(m.Blocks) == 0 && len(m.Photos) == 0 && len(m.Documents) == 0)
}

// MessageReply describes a message reply/thread header without depending on TL types.
type MessageReply struct {
	MessageID     int
	Peer          Peer
	TopMessageID  int
	ForumTopic    bool
	QuoteText     string
	QuoteEntities []MessageEntity
	QuoteOffset   int
	// External is an immutable source snapshot resolved by the owning store.
	// It is not client input and does not authorize a source message lookup.
	External *MessageReplyExternal `json:",omitempty"`
	// StoryID > 0 means this is a reply (comment) to a story: MessageID is 0, Peer is the story author,
	// and it projects as messageReplyStoryHeader rather than an ordinary messageReplyHeader.
	StoryID int
}

// MessageForward describes the original author information of a forwarded message.
type MessageForward struct {
	From           Peer
	FromName       string
	Date           int
	ChannelPost    int
	SavedFrom      Peer
	SavedFromMsgID int
}

// MessageList is a message query result from an account's perspective.
type MessageList struct {
	Messages []Message
	Users    []User
	Count    int
	Hash     int64
}

// MessageFilter describes history/search query conditions.
type MessageFilter struct {
	HasPeer    bool
	Peer       Peer
	Query      string
	OffsetID   int
	OffsetDate int
	MinDate    int
	MaxDate    int
	AddOffset  int
	Limit      int
	MaxID      int
	MinID      int
	Hash       int64
	// SenderUserID intersects all other predicates; zero means no sender filter.
	SenderUserID int64
	// CountOnly ignores pagination and returns the exact filtered total without
	// loading message payloads or users. History's default limit is separate.
	CountOnly bool
	// PinnedOnly returns only pinned messages (the query path of messages.search filterPinned and
	// userFull.pinned_msg_id).
	PinnedOnly     bool
	MusicOnly      bool
	PhoneCallsOnly bool
	// MissedPhoneCallsOnly narrows PhoneCallsOnly to incoming calls that ended
	// with the protocol-level "missed" reason.
	MissedPhoneCallsOnly bool
	NeedTotalCount       bool
	// When SavedPeer is non-zero, return only the messages of that saved sub-chat in the self-chat
	// (messages.getSavedHistory); Peer must also be self.
	SavedPeer Peer
	// When SavedReactions is non-empty, return only Saved Messages carrying at least one of the tags.
	// Used only by messages.search(peer=self); an ordinary private-chat reaction does not participate in matching.
	SavedReactions []MessageReaction
	// PeerIDs restricts a global private search to these user peers. Empty is a
	// valid restricted set, so RestrictPeerIDs carries presence separately.
	PeerIDs         []int64
	RestrictPeerIDs bool
}

// SendPrivateTextRequest is the private text/media send command.
type SendPrivateTextRequest struct {
	SenderUserID    int64
	RecipientUserID int64
	RandomID        int64
	Message         string
	Entities        []MessageEntity
	Media           *MessageMedia
	Silent          bool
	NoForwards      bool
	ReplyTo         *MessageReply
	Forward         *MessageForward
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
	// OriginClientSession carries the exact request's initConnection language
	// into an in-process bot responder. Stores and idempotency fingerprints
	// intentionally ignore this ephemeral metadata.
	OriginClientSession ClientSessionMetadata
	// OriginUserID identifies the authenticated initiator when a server-generated
	// service message is authored by another user. Zero preserves the ordinary
	// send path where the sender is the initiator.
	OriginUserID     int64
	RecipientBlocked bool
	// IdempotencyFingerprint is the SHA-256 computed by the call boundary over the original, immutable send request.
	// The RPC layer should prefer the original TL request fingerprint, so server-derived fields such as link previews, dice results and
	// uploaded media do not make a legitimate replay look different; when an internal call leaves it empty, the store generates an equivalent
	// fingerprint from the domain command's immutable fields.
	IdempotencyFingerprint []byte
	// IdempotencyPreflighted is internal execution metadata.  A trusted caller sets it only
	// after a read-only replay lookup returned absent, allowing the app/store layers to avoid
	// repeating the same indexed lookup.  The transactional unique-key path still fences a
	// concurrent first writer; this flag is never part of the durable request fingerprint.
	IdempotencyPreflighted bool
	TTLPeriod              int
	ViaBotID               int64
	// GroupedID is the album grouping id (the same sendMultiMedia group shares a non-zero value; non-album is always 0).
	GroupedID int64
	// Effect is the message effect id (private-chat only, 0 means no effect; the caller has validated legality against the catalog).
	Effect int64
	// BusinessAutomationKind is internal app-layer metadata used to suppress
	// recursive greeting/away automation for server-generated replies.
	BusinessAutomationKind BusinessAutomationKind
	// ReplyMarkup is the reply/inline keyboard snapshot of a bot outbound message; an ordinary user send is always nil.
	ReplyMarkup *MessageReplyMarkup
	// RichMessage is the Layer 228 rich text message (richMessage) snapshot, optional; an ordinary message is always nil.
	RichMessage *MessageRichMessage
	PaidStars   int64
}

// HasContent reports whether the command contains a client-visible message payload.
// TDesktop rich-message posting intentionally leaves Message empty, so every send
// boundary must treat text, media, and rich_message as equivalent content sources.
func (r SendPrivateTextRequest) HasContent() bool {
	return r.Message != "" || !r.Media.IsZero() || !r.RichMessage.IsZero()
}

// PrivateSendReplayRequest identifies one already-committed private send without carrying any
// mutable or resolver-derived message fields.  The fingerprint is computed at the original
// request boundary and must be a complete SHA-256 value.
type PrivateSendReplayRequest struct {
	SenderUserID           int64
	RecipientUserID        int64
	RandomID               int64
	IdempotencyFingerprint []byte
}

// SendPrivateTextResult describes the two-sided result of one private text send.
type PaidStarsCharge struct {
	Amount           int64
	SenderBalance    int64
	RecipientBalance int64
}

type SendPrivateTextResult struct {
	SenderMessage    Message
	RecipientMessage Message
	SenderEvent      UpdateEvent
	RecipientEvent   UpdateEvent
	Duplicate        bool
	PaidStars        *PaidStarsCharge
	// ReplayDeleteEvent is the already-durable sender-side deletion that must
	// follow the first-send snapshot in an exact random_id replay. It never
	// represents a newly allocated event.
	ReplayDeleteEvent *UpdateEvent
}

// SetPrivateChatThemeRequest changes the shared theme token for a private dialog.
type SetPrivateChatThemeRequest struct {
	OwnerUserID      int64
	Peer             Peer
	Emoticon         string
	Date             int
	OriginAuthKeyID  [8]byte
	OriginSessionID  int64
	RecipientBlocked bool
}

// SetPrivateChatThemeResult describes the state change and optional service message.
type SetPrivateChatThemeResult struct {
	OwnerUserID int64
	Peer        Peer
	Emoticon    string
	Changed     bool
	Send        SendPrivateTextResult
}

// SetPrivateMessageReactionsRequest replaces the current user's reactions for one private message.
type SetPrivateMessageReactionsRequest struct {
	UserID      int64
	Peer        Peer
	MessageID   int
	Reactions   []MessageReaction
	Big         bool
	AddToRecent bool
	Date        int
	// ReactionsPerUserMax is the viewer's per-user limit tier (Premium dual tier);
	// 0 means unfilled, and the store trims to the default tier.
	ReactionsPerUserMax int
}

// PrivateMessageReactionsRequest fetches reaction summaries for exact private message ids.
type PrivateMessageReactionsRequest struct {
	OwnerUserID int64
	Peer        Peer
	IDs         []int
}

// PrivateMessageReactionsResult describes private reaction updates in owner-visible boxes.
type PrivateMessageReactionsResult struct {
	Messages  []Message
	Reactions ChannelMessageReactions
}

// ForwardPrivateMessagesRequest is the private text message forward command.
type ForwardPrivateMessagesRequest struct {
	OwnerUserID      int64
	FromPeer         Peer
	ToUserID         int64
	MessageIDs       []int
	RandomIDs        []int64
	Silent           bool
	NoForwards       bool
	DropAuthor       bool
	ReplyTo          *MessageReply
	Date             int
	OriginAuthKeyID  [8]byte
	OriginSessionID  int64
	RecipientBlocked bool
	TTLPeriod        int
}

// ForwardPrivateMessagesResult describes the owner-dimension result of one private forward.
type ForwardPrivateMessagesResult struct {
	OwnerUserID        int64
	SenderMessages     []Message
	RecipientMessages  []Message
	SenderEvents       []UpdateEvent
	RecipientEvents    []UpdateEvent
	Duplicates         []bool
	ReplayDeleteEvents []*UpdateEvent
}

const PrivateNoForwardsRequestExpirePeriod = 24 * 60 * 60

// PrivateNoForwardsState is the single content-protection authority for a pair of ordinary users.
// EnabledByUserID is 0 or one of the participants; when non-zero, chats shared by both sides are protected.
type PrivateNoForwardsState struct {
	UserLowID       int64
	UserHighID      int64
	EnabledByUserID int64
}

func (s PrivateNoForwardsState) Enabled() bool {
	return s.EnabledByUserID != 0
}

func (s PrivateNoForwardsState) ForViewer(viewerUserID int64) (myEnabled, peerEnabled bool) {
	if s.EnabledByUserID == 0 {
		return false, false
	}
	return s.EnabledByUserID == viewerUserID, s.EnabledByUserID != viewerUserID
}

// TogglePrivateNoForwardsRequest is the atomic state + service message command for private content protection.
type TogglePrivateNoForwardsRequest struct {
	ActorUserID     int64
	PeerUserID      int64
	Enabled         bool
	RequestMsgID    int
	RandomID        int64
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// TogglePrivateNoForwardsResult returns both the committed authoritative state and this call's real service message.
// Changed=false with an empty Send means the officially defined no-op.
type TogglePrivateNoForwardsResult struct {
	State   PrivateNoForwardsState
	Changed bool
	Send    SendPrivateTextResult
}

// ReadHistoryRequest is the messages.readHistory command from an account's perspective.
type ReadHistoryRequest struct {
	OwnerUserID     int64
	Peer            Peer
	MaxID           int
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// ReadHistoryResult describes the business result of one chat read operation.
type ReadHistoryResult struct {
	OwnerUserID      int64
	Peer             Peer
	MaxID            int
	StillUnreadCount int
	ChannelPts       int
	Changed          bool
	InboxEvent       UpdateEvent
	OutboxChanged    bool
	OutboxUserID     int64
	OutboxEvent      UpdateEvent
}

// ReadMessageContentsRequest marks media/mention contents as read for exact owner-visible messages.
type ReadMessageContentsRequest struct {
	OwnerUserID     int64
	IDs             []int
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// ReadMessageContentsResult contains owner-visible message IDs whose content unread state changed.
type ReadMessageContentsResult struct {
	OwnerUserID int64
	MessageIDs  []int
	Event       UpdateEvent
	// SenderEvents are the content read receipts for the peer senders: after the reader finishes a voice/round,
	// each original sender receives an updateReadMessagesContents, where messages uses the sender's
	// own-perspective box id.
	SenderEvents []UpdateEvent
}

// OutboxReadDateRequest is the messages.getOutboxReadDate query.
type OutboxReadDateRequest struct {
	OwnerUserID int64
	Peer        Peer
	ID          int
}

// EditMessageRequest is the command to edit a sent private message from an account's perspective.
// A non-nil Media replaces the message media snapshot wholesale (the only current caller is live location update/stop,
// and the rpc layer limits the media kind); nil means a text-only edit.
type EditMessageRequest struct {
	OwnerUserID     int64
	Peer            Peer
	ID              int
	Message         string
	Entities        []MessageEntity
	Media           *MessageMedia
	EditDate        int
	HideEdited      bool
	OriginAuthKeyID [8]byte
	OriginSessionID int64
	// When SetReplyMarkup is set, replace reply_markup (a nil/empty ReplyMarkup = clear the keyboard);
	// when unset, the original markup is preserved. The RPC layer sets it only when a bot edits its own message (P3).
	SetReplyMarkup bool
	ReplyMarkup    *MessageReplyMarkup
	// When SetRichMessage is set, replace rich_message (a nil/empty RichMessage = clear the rich text).
	// TDesktop EditRichMessage carries only f_rich_message, not f_message; the store must not mistake it for an empty edit.
	SetRichMessage bool
	RichMessage    *MessageRichMessage
	// A non-zero ViaBotEditBotID allows the corresponding bot to edit inline private messages sent through it.
	ViaBotEditBotID int64
	// AllowTodoParticipantMutation lets a checklist participant replace the todo media snapshot through an
	// edit event under the others_can_* grant. Only the RPC todo handler sets it; the store still restricts it to
	// todo->todo with an unchanged body/entities/markup, preventing an ordinary message from being edited without permission.
	AllowTodoParticipantMutation bool
	// When WebPageResolve is set it is a server-internal "replace link preview in place": only the media is replaced (body/
	// entities/edit_date untouched, so it is not marked "edited") and UpdateEventWebPage is generated rather than edit_message.
	// Idempotency guard: replace only when the target's current media is still a pending link preview with ID==ExpectedWebPageID,
	// otherwise return ErrMessageNotModified (the message was deleted/changed/resolved).
	WebPageResolve    bool
	ExpectedWebPageID int64
}

// EditedMessageForUser describes the effect of one edit on a given owner's perspective.
type EditedMessageForUser struct {
	UserID  int64
	Message Message
	Event   UpdateEvent
}

// EditMessageResult describes the owner-dimension result after a message edit.
type EditMessageResult struct {
	OwnerUserID int64
	Edited      []EditedMessageForUser
}

// Self returns the edit result for the current requesting account.
func (r EditMessageResult) Self() EditedMessageForUser {
	for _, item := range r.Edited {
		if item.UserID == r.OwnerUserID {
			return item
		}
	}
	return EditedMessageForUser{UserID: r.OwnerUserID}
}

// Changed reports whether this edit actually affected any owner's perspective.
func (r EditMessageResult) Changed() bool {
	return len(r.Edited) > 0
}

// DeleteMessagesRequest is the command to delete messages by id from an account's perspective.
type DeleteMessagesRequest struct {
	OwnerUserID     int64
	IDs             []int
	Revoke          bool
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// DeleteHistoryRequest is the command to clear a peer's history from an account's perspective.
type DeleteHistoryRequest struct {
	OwnerUserID int64
	Peer        Peer
	MaxID       int
	// MinDate/MaxDate are the closed interval of a "delete by date" (unix seconds, 0 means unbounded);
	// the client destroys locally before sending the request, so a server that silently ignores it would resurrect the deletions.
	MinDate         int
	MaxDate         int
	JustClear       bool
	Revoke          bool
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// DeletedMessagesForUser describes the effect of one deletion on a given owner's perspective.
type DeletedMessagesForUser struct {
	UserID     int64
	MessageIDs []int
	// Event keeps a single delete_messages event for the ordinary deleteMessages path and
	// the replay receipt; just_clear additionally carries read/edit events in Events.
	Event  UpdateEvent
	Events []UpdateEvent
	// Pts/PtsCount is the affected watermark summary of this method for that owner.
	// For an ordinary delete it equals Event; for just_clear it equals the pts of the last real update and
	// the sum of the delete/read/edit three-segment pts_count.
	Pts      int
	PtsCount int
}

// AffectedPts returns the final PTS and this call's total increment that messages.Affected* should use.
func (d DeletedMessagesForUser) AffectedPts() (int, int) {
	if d.Pts != 0 {
		return d.Pts, d.PtsCount
	}
	return d.Event.Pts, d.Event.PtsCount
}

// DeleteMessagesResult describes the owner-dimension result after message deletion.
type DeleteMessagesResult struct {
	OwnerUserID int64
	Deleted     []DeletedMessagesForUser
	Offset      int
}

// Self returns the deletion result for the current requesting account.
func (r DeleteMessagesResult) Self() DeletedMessagesForUser {
	for _, item := range r.Deleted {
		if item.UserID == r.OwnerUserID {
			return item
		}
	}
	return DeletedMessagesForUser{UserID: r.OwnerUserID}
}

// Changed reports whether this deletion actually affected any owner's perspective.
func (r DeleteMessagesResult) Changed() bool {
	for _, item := range r.Deleted {
		pts, _ := item.AffectedPts()
		if len(item.MessageIDs) > 0 || pts > 0 {
			return true
		}
	}
	return false
}

// PinPrivateMessageRequest is the private-chat command of messages.updatePinnedMessage
// (including Saved Messages = self peer).
type PinPrivateMessageRequest struct {
	OwnerUserID int64
	Peer        Peer
	MessageID   int
	Pinned      bool
	// PmOneside pins only on this side (when the official private-chat pin box's "pin for the other side too" is unchecked);
	// it does not flip the peer and generates no service message. unpin has no such semantics and always clears both sides.
	PmOneside bool
	Silent    bool
	// RecipientBlocked applies the normal private-service delivery policy.
	RecipientBlocked bool
	Date             int
	OriginAuthKeyID  [8]byte
	OriginSessionID  int64
}

// PinnedMessagesForUser describes the effect of a pin state change on a given owner's perspective.
type PinnedMessagesForUser struct {
	UserID     int64
	Peer       Peer
	MessageIDs []int
	Pinned     bool
	Event      UpdateEvent
}

// MaxUnpinAllBatch limits the pin count a single unpinAllMessages actually clears;
// the rest is cleared by the client continuing per affectedHistory.Offset>0, keeping a single
// updatePinnedMessages messages vector bounded.
const MaxUnpinAllBatch = 1000

// PinPrivateMessageResult describes the owner-dimension result of a private pin/unpin.
// An empty Updated means the state did not change (an idempotent no-op that burns no pts).
type PinPrivateMessageResult struct {
	OwnerUserID int64
	Updated     []PinnedMessagesForUser
	// ServiceMessage is committed atomically with a changed shared private pin.
	ServiceMessage SendPrivateTextResult
	// A non-zero Offset means unpinAll still has remaining batches to clear.
	Offset int
}

// Self returns the pin result for the current requesting account.
func (r PinPrivateMessageResult) Self() PinnedMessagesForUser {
	for _, item := range r.Updated {
		if item.UserID == r.OwnerUserID {
			return item
		}
	}
	return PinnedMessagesForUser{UserID: r.OwnerUserID}
}

// Changed reports whether this operation actually flipped the pin state for any owner's perspective.
func (r PinPrivateMessageResult) Changed() bool {
	for _, item := range r.Updated {
		if len(item.MessageIDs) > 0 {
			return true
		}
	}
	return false
}

// UnpinAllPrivateMessagesRequest is the private-chat command of messages.unpinAllMessages.
type UnpinAllPrivateMessagesRequest struct {
	OwnerUserID     int64
	Peer            Peer
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// ScheduledMessage is a pending account-owned message that has not entered
// normal peer history yet.
type ScheduledMessage struct {
	OwnerUserID          int64
	ID                   int
	Peer                 Peer
	RandomID             int64
	Message              string
	Entities             []MessageEntity
	Media                *MessageMedia
	RichMessage          *MessageRichMessage
	Silent               bool
	NoForwards           bool
	ReplyTo              *MessageReply
	Forward              *MessageForward
	SendAs               *Peer
	ScheduleDate         int
	ScheduleRepeatPeriod int
	PaidStars            int64
	CreatedAt            int
	UpdatedAt            int
	State                string
	SentMessageID        int
}

// ScheduledMessageList is a bounded scheduled queue page.
type ScheduledMessageList struct {
	Messages []ScheduledMessage
	Count    int
	Hash     int64
}

// ScheduleMessageRequest creates one scheduled message.
type ScheduleMessageRequest struct {
	OwnerUserID          int64
	Peer                 Peer
	RandomID             int64
	Message              string
	Entities             []MessageEntity
	Media                *MessageMedia
	RichMessage          *MessageRichMessage
	Silent               bool
	NoForwards           bool
	ReplyTo              *MessageReply
	Forward              *MessageForward
	SendAs               *Peer
	ScheduleDate         int
	ScheduleRepeatPeriod int
	PaidStars            int64
	Date                 int
}

// EditScheduledMessageRequest updates one pending scheduled message before it
// enters normal history.
type EditScheduledMessageRequest struct {
	OwnerUserID    int64
	Peer           Peer
	ID             int
	SetMessage     bool
	Message        string
	Entities       []MessageEntity
	SetRichMessage bool
	RichMessage    *MessageRichMessage
	ScheduleDate   int
	Date           int
}

// ScheduledMessageFilter selects scheduled messages for one owner/peer.
type ScheduledMessageFilter struct {
	OwnerUserID int64
	Peer        Peer
	IDs         []int
	Limit       int
	Hash        int64
}

// ScheduledMessageClaim describes a due/manual scheduled dispatch claim.
type ScheduledMessageClaim struct {
	OwnerUserID int64
	Peer        Peer
	IDs         []int
	Now         int
	Limit       int
	LeaseUntil  int
}
