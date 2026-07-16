package domain

// MessageEntityType 标识消息实体类型。
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
	MaxMessageReplyQuoteOffset = MaxMessageTextLength
	// MaxMessageEntityCount limits styled text entity vectors in message text and quotes.
	MaxMessageEntityCount = 256
	// MaxMessageBoxID 是 TL int / PostgreSQL int4 可安全表达的最大 message id。
	MaxMessageBoxID = 1<<31 - 1
	// MaxDeleteMessageIDs 限制单次 deleteMessages/updateDeleteMessages 的 owner 视角 id 数量。
	// 大批量历史清理走 deleteHistory 分批推进，避免单个 RPC 构造超大数组或 durable payload。
	MaxDeleteMessageIDs = 1000
	// MaxGetMessageIDs 限制 getMessages / channels.getMessages 精确 ID 批量。
	MaxGetMessageIDs = 100
	// MaxDeleteHistoryBatch 限制单次 deleteHistory 实际清理的 message box 数量。
	// affectedHistory.Offset > 0 时客户端可继续调用，服务端不一次性 RETURNING 全历史。
	MaxDeleteHistoryBatch = 1000
	// MaxForwardMessageIDs 限制单次 forwardMessages 的 owner 视角 id 数量。
	MaxForwardMessageIDs = 100
	// MaxMessageHistoryAddOffset 限制 history/search 的 add_offset 绝对值。
	// TDesktop 正常只使用小窗口偏移；服务端必须拒绝把客户端传入的超大值变成 SQL OFFSET 或 slice capacity。
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
	// story 回复（StoryID>0）不携带 MessageID/TopMessageID；普通回复至少有其一。
	if reply.MessageID == 0 && reply.TopMessageID == 0 && reply.StoryID == 0 {
		return ErrReplyMessageIDInvalid
	}
	if reply.QuoteOffset < 0 || reply.QuoteOffset > MaxMessageReplyQuoteOffset {
		return ErrReplyMessageIDInvalid
	}
	return nil
}

// MessageEntity 是业务层消息实体，不依赖 TL 类型。
type MessageEntity struct {
	Type   MessageEntityType
	Offset int
	Length int
	// URL 仅 text_url 使用。
	URL string
	// UserID 仅 mention_name 使用。
	UserID int64
	// Language 仅 pre 使用。
	Language string
	// DocumentID 仅 custom_emoji 使用。
	DocumentID int64
	// Collapsed 仅 blockquote 使用。
	Collapsed bool
	// Date / Relative / ShortTime / LongTime / ShortDate / LongDate / DayOfWeek
	// 仅 formatted_date 使用。
	Date      int
	Relative  bool
	ShortTime bool
	LongTime  bool
	ShortDate bool
	LongDate  bool
	DayOfWeek bool
	// OldText 仅 AI compose diff_replace 使用；普通消息输入不会接受该实体。
	OldText string
}

// Message 是账号视角下的一条私聊消息。
type Message struct {
	ID             int   // 当前 owner 视角下的 message box id，暴露给 Telegram 客户端。
	UID            int64 // 共享私聊消息主体 id，不暴露给客户端。
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
	// GroupedID 是相册分组 id：同一次 sendMultiMedia 的各条消息共享同一非零值，
	// 客户端据此把它们渲染成一个相册组。非相册消息恒 0。双盒持同一值。
	GroupedID int64
	// Effect 是消息特效 id（message.effect，flags2.2?long）：私聊 1-1 专属动画特效
	// （🎉/👍 等），发送方与接收方双盒持同一非零值并各自播放一次；非特效消息恒 0。
	// 转发不携带特效（新消息恒 0）。仅私聊；群/频道不渲染。
	Effect int64
	// ReplyMarkup 是 bot 消息携带的 inline keyboard 快照（P3）。仅 bot 出站消息可
	// 非空；普通用户消息恒 nil（发送侧 is_bot 闸门）。双盒持同一快照（无 per-viewer 差异）。
	ReplyMarkup *MessageReplyMarkup
	// RichMessage 是 Layer 227 富文本消息（richMessage）快照，可选；普通消息恒 nil。
	RichMessage *MessageRichMessage
	// Pinned 是 owner 视角的置顶标志（官方私聊多置顶语义：双方各自
	// 的 box 行独立持有，非 pm_oneside 操作两侧同步翻转）。
	Pinned bool
	// SavedPeer 是 Saved Messages 分会话分组键（message.saved_peer_id）。
	// 仅 self-chat box 行非零：直发笔记 = self；转发进收藏夹 = 源会话 peer；
	// 存量回填兜底 hidden author 占位 user 2666000。非 self-chat 行恒零值。
	SavedPeer Peer
}

// MessageRichMessage 是 Layer 227 富文本消息（richMessage）的协议中立快照：一组 IV
// PageBlock（Blocks）+ 内嵌已解析的 Photos/Documents。
//
// Blocks 存 gotd TL 序列化后的 []tg.PageBlockClass 不透明字节——PageBlock 体系庞大且
// input(inputRichMessage.blocks) 与 output(richMessage.blocks) 同构、原样透传，故不在
// domain 逐类型建模；rpc 层负责 tg.PageBlock 向量 ↔ bytes 的序列化（domain 不依赖 tg）。
// 与 message media 同理，Photos/Documents 存已解析快照（含 viewer 无关的 access_hash），
// 投影复用 tgPhoto/tgDocument。Phase 1 仅支持 inputRichMessage（blocks 形态），不解析
// HTML/Markdown 变体。
//
// 已知局限：Blocks 是 gotd 线格式不透明字节，跨 gotd 版本（PageBlock 构造器变更）可能
// 失效——富文本消息为全新实验特性、无存量数据，Phase 1 接受该耦合。
type MessageRichMessage struct {
	Rtl       bool       `json:"rtl,omitempty"`
	Part      bool       `json:"part,omitempty"`
	Blocks    []byte     `json:"blocks,omitempty"`
	Photos    []Photo    `json:"photos,omitempty"`
	Documents []Document `json:"documents,omitempty"`
}

// IsZero 表示无富文本载荷（落库时跳过空快照、投影时不下发 rich_message）。
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
	// StoryID > 0 表示这是一条对 story 的回复（评论）：MessageID 为 0，Peer 为 story 作者，
	// 投影为 messageReplyStoryHeader 而非普通 messageReplyHeader。
	StoryID int
}

// MessageForward 描述一条转发消息的原始作者信息。
type MessageForward struct {
	From           Peer
	FromName       string
	Date           int
	ChannelPost    int
	SavedFrom      Peer
	SavedFromMsgID int
}

// MessageList 是账号视角下的消息查询结果。
type MessageList struct {
	Messages []Message
	Users    []User
	Count    int
	Hash     int64
}

// MessageFilter 描述历史/搜索查询条件。
type MessageFilter struct {
	HasPeer    bool
	Peer       Peer
	Query      string
	OffsetID   int
	OffsetDate int
	AddOffset  int
	Limit      int
	MaxID      int
	MinID      int
	Hash       int64
	// PinnedOnly 仅返回置顶消息（messages.search filterPinned 与
	// userFull.pinned_msg_id 的查询路径）。
	PinnedOnly     bool
	MusicOnly      bool
	NeedTotalCount bool
	// SavedPeer 非零时仅返回 self-chat 中该 saved 子会话的消息
	// （messages.getSavedHistory）；Peer 必须同时是 self。
	SavedPeer Peer
}

// SendPrivateTextRequest 是私聊文本/媒体发送命令。
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
	// OriginUserID identifies the authenticated initiator when a server-generated
	// service message is authored by another user. Zero preserves the ordinary
	// send path where the sender is the initiator.
	OriginUserID     int64
	RecipientBlocked bool
	// IdempotencyFingerprint 是调用边界对原始、不可变发送请求计算的 SHA-256。
	// RPC 层应优先填入原始 TL 请求指纹，避免链接预览、骰子结果、上传媒体
	// 等服务端派生字段让合法重放看起来不同；内部调用留空时 store 会基于
	// domain command 的不可变字段生成等价指纹。
	IdempotencyFingerprint []byte
	// IdempotencyPreflighted is internal execution metadata.  A trusted caller sets it only
	// after a read-only replay lookup returned absent, allowing the app/store layers to avoid
	// repeating the same indexed lookup.  The transactional unique-key path still fences a
	// concurrent first writer; this flag is never part of the durable request fingerprint.
	IdempotencyPreflighted bool
	TTLPeriod              int
	ViaBotID               int64
	// GroupedID 相册分组 id（sendMultiMedia 同组共享非零值，非相册恒 0）。
	GroupedID int64
	// Effect 消息特效 id（私聊专属，0 表无特效；调用方已对 catalog 校验过合法性）。
	Effect int64
	// BusinessAutomationKind is internal app-layer metadata used to suppress
	// recursive greeting/away automation for server-generated replies.
	BusinessAutomationKind BusinessAutomationKind
	// ReplyMarkup 是 bot 出站消息的 inline keyboard 快照（P3）；普通用户发送恒 nil。
	ReplyMarkup *MessageReplyMarkup
	// RichMessage 是 Layer 227 富文本消息（richMessage）快照，可选；普通消息恒 nil。
	RichMessage *MessageRichMessage
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

// SendPrivateTextResult 描述一次私聊文本发送的双端结果。
type SendPrivateTextResult struct {
	SenderMessage    Message
	RecipientMessage Message
	SenderEvent      UpdateEvent
	RecipientEvent   UpdateEvent
	Duplicate        bool
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
	// ReactionsPerUserMax 是 viewer 的每用户上限档位（premium 双档）；
	// 0 表示未填，store 侧按默认档裁剪。
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

// ForwardPrivateMessagesRequest 是私聊文本消息转发命令。
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

// ForwardPrivateMessagesResult 描述一次私聊转发的 owner 维度结果。
type ForwardPrivateMessagesResult struct {
	OwnerUserID        int64
	SenderMessages     []Message
	RecipientMessages  []Message
	SenderEvents       []UpdateEvent
	RecipientEvents    []UpdateEvent
	Duplicates         []bool
	ReplayDeleteEvents []*UpdateEvent
}

// ReadHistoryRequest 是账号视角的 messages.readHistory 命令。
type ReadHistoryRequest struct {
	OwnerUserID     int64
	Peer            Peer
	MaxID           int
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// ReadHistoryResult 描述一次会话已读操作的业务结果。
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
	// SenderEvents 是对端发送者的内容已读回执：reader 听完 voice/round 后，
	// 每个原 sender 收到一条 updateReadMessagesContents，messages 用 sender
	// 自己视角的 box id。
	SenderEvents []UpdateEvent
}

// OutboxReadDateRequest 是 messages.getOutboxReadDate 查询。
type OutboxReadDateRequest struct {
	OwnerUserID int64
	Peer        Peer
	ID          int
}

// EditMessageRequest 是账号视角下编辑一条已发送私聊消息的命令。
// Media 非 nil 时整体替换消息媒体快照（当前唯一调用方是 live location 续报/停止，
// rpc 层负责限定媒体种类）；nil 表示纯文本编辑。
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
	// SetReplyMarkup 置位时替换 reply_markup（ReplyMarkup 为 nil/空 = 清空键盘）；
	// 未置位则保留原 markup。仅 bot 编辑自己消息时由 RPC 层置位（P3）。
	SetReplyMarkup bool
	ReplyMarkup    *MessageReplyMarkup
	// SetRichMessage 置位时替换 rich_message（RichMessage nil/空 = 清空富文本）。
	// TDesktop EditRichMessage 只带 f_rich_message，不带 f_message；store 不能把它误判为空编辑。
	SetRichMessage bool
	RichMessage    *MessageRichMessage
	// ViaBotEditBotID 非零时允许对应 bot 编辑经由它发送的 inline 私聊消息。
	ViaBotEditBotID int64
	// AllowTodoParticipantMutation 允许 checklist 参与者在 others_can_* 授权下通过
	// edit 事件替换 todo 媒体快照。仅 RPC todo handler 设置；store 仍会限制为
	// todo->todo 且正文/entities/markup 不变，避免普通消息越权编辑。
	AllowTodoParticipantMutation bool
	// WebPageResolve 置位时为服务端内部「链接预览就地替换」：仅替换 media（不碰 body/
	// entities/edit_date，故不标记「已编辑」），生成 UpdateEventWebPage 而非 edit_message。
	// 幂等守卫：仅当目标当前 media 仍是 ID==ExpectedWebPageID 的 pending 链接预览才替换，
	// 否则返回 ErrMessageNotModified（消息已删/已改/已解析）。
	WebPageResolve    bool
	ExpectedWebPageID int64
}

// EditedMessageForUser 描述一次编辑对某个 owner 视角造成的影响。
type EditedMessageForUser struct {
	UserID  int64
	Message Message
	Event   UpdateEvent
}

// EditMessageResult 描述消息编辑后的 owner 维度结果。
type EditMessageResult struct {
	OwnerUserID int64
	Edited      []EditedMessageForUser
}

// Self 返回当前请求账号的编辑结果。
func (r EditMessageResult) Self() EditedMessageForUser {
	for _, item := range r.Edited {
		if item.UserID == r.OwnerUserID {
			return item
		}
	}
	return EditedMessageForUser{UserID: r.OwnerUserID}
}

// Changed 表示本次编辑是否实际影响了任何 owner 视角。
func (r EditMessageResult) Changed() bool {
	return len(r.Edited) > 0
}

// DeleteMessagesRequest 是账号视角下按消息 ID 删除消息的命令。
type DeleteMessagesRequest struct {
	OwnerUserID     int64
	IDs             []int
	Revoke          bool
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// DeleteHistoryRequest 是账号视角下清空某个 peer 历史的命令。
type DeleteHistoryRequest struct {
	OwnerUserID int64
	Peer        Peer
	MaxID       int
	// MinDate/MaxDate 是"按日期删除"的闭区间（unix 秒，0 表示不限）；
	// 客户端先本地销毁再发请求，服务端静默忽略会造成删除复活。
	MinDate         int
	MaxDate         int
	JustClear       bool
	Revoke          bool
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// DeletedMessagesForUser 描述一次删除对某个 owner 视角造成的影响。
type DeletedMessagesForUser struct {
	UserID     int64
	MessageIDs []int
	Event      UpdateEvent
}

// DeleteMessagesResult 描述消息删除后的 owner 维度结果。
type DeleteMessagesResult struct {
	OwnerUserID int64
	Deleted     []DeletedMessagesForUser
	Offset      int
}

// Self 返回当前请求账号的删除结果。
func (r DeleteMessagesResult) Self() DeletedMessagesForUser {
	for _, item := range r.Deleted {
		if item.UserID == r.OwnerUserID {
			return item
		}
	}
	return DeletedMessagesForUser{UserID: r.OwnerUserID}
}

// Changed 表示本次删除是否实际影响了任何 owner 视角。
func (r DeleteMessagesResult) Changed() bool {
	for _, item := range r.Deleted {
		if len(item.MessageIDs) > 0 {
			return true
		}
	}
	return false
}

// PinPrivateMessageRequest 是 messages.updatePinnedMessage 的私聊命令
// （含 Saved Messages = self peer）。
type PinPrivateMessageRequest struct {
	OwnerUserID int64
	Peer        Peer
	MessageID   int
	Pinned      bool
	// PmOneside 仅置顶在本侧（官方私聊置顶框"同时为对方置顶"未勾选时），
	// 不向对端翻转、不生成服务消息。unpin 无此语义，恒双侧清除。
	PmOneside       bool
	Silent          bool
	Date            int
	OriginAuthKeyID [8]byte
	OriginSessionID int64
}

// PinnedMessagesForUser 描述置顶状态变化对某个 owner 视角的影响。
type PinnedMessagesForUser struct {
	UserID     int64
	Peer       Peer
	MessageIDs []int
	Pinned     bool
	Event      UpdateEvent
}

// MaxUnpinAllBatch 限制单次 unpinAllMessages 实际清除的置顶数量；
// 超出部分由客户端按 affectedHistory.Offset>0 续发清除，单条
// updatePinnedMessages 的 messages 向量随之有界。
const MaxUnpinAllBatch = 1000

// PinPrivateMessageResult 描述私聊置顶/取消置顶的 owner 维度结果。
// Updated 为空表示状态未变化（幂等 no-op，不烧 pts）。
type PinPrivateMessageResult struct {
	OwnerUserID int64
	Updated     []PinnedMessagesForUser
	// Offset 非 0 表示 unpinAll 还有剩余批次待清。
	Offset int
}

// Self 返回当前请求账号的置顶结果。
func (r PinPrivateMessageResult) Self() PinnedMessagesForUser {
	for _, item := range r.Updated {
		if item.UserID == r.OwnerUserID {
			return item
		}
	}
	return PinnedMessagesForUser{UserID: r.OwnerUserID}
}

// Changed 表示本次操作是否实际翻转了任何 owner 视角的置顶状态。
func (r PinPrivateMessageResult) Changed() bool {
	for _, item := range r.Updated {
		if len(item.MessageIDs) > 0 {
			return true
		}
	}
	return false
}

// UnpinAllPrivateMessagesRequest 是 messages.unpinAllMessages 的私聊命令。
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
