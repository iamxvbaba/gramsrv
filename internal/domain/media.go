package domain

import (
	"path/filepath"
	"strings"
	"time"
)

// 本文件定义媒体相关的业务值对象（文档、照片、贴纸集、可用 reaction、消息媒体）。
// 这些类型完全不依赖 tg.*；rpc 层负责 domain↔tg 转换。
//
// 字段带 json tag 是为了 store 层可直接 json.Marshal 落 JSONB（消息 media 快照、
// 文档/照片元数据）。它们是协议无关的纯数据，不是 tg 生成类型。

// MediaBackend 标识 blob 字节实际存放后端。第一阶段只有本地磁盘。
type MediaBackend string

const (
	// MediaBackendLocalFS 表示 blob 字节存在本地磁盘（object_key 为相对路径）。
	MediaBackendLocalFS MediaBackend = "localfs"
)

// FileBlob 是一个可下载的二进制对象的索引项：location_key → 后端/对象键/大小/mime。
// 真正的字节由 blob backend 按 ObjectKey 读写；本结构只描述定位与元数据。
type FileBlob struct {
	// LocationKey 是稳定的逻辑定位键，由 getFile 的 InputFileLocation 推导：
	//   doc:<id>            文档主体
	//   doc:<id>:<type>     文档缩略图（PhotoSize type）
	//   photo:<id>:<type>   照片某尺寸
	LocationKey string       `json:"location_key"`
	Backend     MediaBackend `json:"backend"`
	ObjectKey   string       `json:"object_key"`
	Size        int64        `json:"size"`
	SHA256      []byte       `json:"sha256,omitempty"`
	MimeType    string       `json:"mime_type,omitempty"`
}

// UploadPart 是 upload.saveFilePart/saveBigFilePart 累积的一个分片元数据。
// 分片字节不进 PG；PG 只记录 object_key/size/hash，组装后清理 metadata 与临时对象。
type UploadPart struct {
	OwnerUserID int64
	FileID      int64
	Part        int
	TotalParts  int // big file 已知总数；small file 为 0
	Big         bool
	Backend     MediaBackend
	ObjectKey   string
	Size        int64
	SHA256      []byte
}

// UploadPartUsage 描述某用户当前尚未组装的上传分片占用。
type UploadPartUsage struct {
	Bytes int64
	Parts int
	Files int
}

// UploadPartSlot 描述某个 (owner,file_id,part) 槽位的当前状态，用于判断重试覆盖的配额增量。
type UploadPartSlot struct {
	ExistingBytes int64
	ObjectKey     string
	FileParts     int
	Found         bool
}

// UploadPartQuota 限制某用户 in-flight 上传分片占用；字段 <=0 表示该维度不限制。
type UploadPartQuota struct {
	MaxBytes int64
	MaxParts int
	MaxFiles int
}

// UploadedFileRef 引用一个客户端已通过 upload.saveFilePart(Big) 上传完毕的文件。
// rpc 层从 tg.InputFile/InputFileBig 转换得到；files 服务据此组装 blob。
type UploadedFileRef struct {
	OwnerUserID int64
	FileID      int64
	Parts       int
	Name        string
	Big         bool
	MD5         string // small file 客户端 md5_checksum（hex），可校验；big file 为空
}

// UploadedMediaKind identifies the durable object materialized from a one-shot upload file id.
type UploadedMediaKind string

const (
	UploadedMediaPhoto    UploadedMediaKind = "photo"
	UploadedMediaDocument UploadedMediaKind = "document"
)

// UploadedMediaReceipt makes InputMediaUploaded* replayable after transient upload parts have
// been consumed. IntentHash binds the file id to the complete materialization intent (kind, part
// metadata and document spec); MediaID points at the immutable Photo/Document returned on every
// exact replay.
type UploadedMediaReceipt struct {
	OwnerUserID int64
	FileID      int64
	IntentHash  []byte
	Kind        UploadedMediaKind
	MediaID     int64
	CreatedAt   time.Time
}

// DocumentSpec 描述从上传文件创建 Document 的元数据（来自 InputMediaUploadedDocument）。
type DocumentSpec struct {
	MimeType   string
	Attributes []DocumentAttribute
	Thumb      *UploadedFileRef // 可选缩略图上传，生成 doc:<id>:m
	ForceFile  bool
	// NosoundVideo 对应 inputMediaUploadedDocument.nosound_video。真实 GIF 仍会
	// 转成 MP4，但该标志存在时按普通无声视频落库，不附 animated 属性（album 路径）。
	NosoundVideo bool
}

// FileDownloadRequest 是 upload.getFile 解析后的下载请求；
// LocationKey 由 rpc 层从 tg.InputFileLocation 推导（doc:<id> / photo:<id>:<type> 等）。
type FileDownloadRequest struct {
	LocationKey string
	Offset      int64
	Limit       int
}

// FileChunk 是 upload.getFile 返回的一段内容。
type FileChunk struct {
	Bytes    []byte
	MimeType string
	Total    int64
}

// PhotoSizeKind 标识 PhotoSize 的 TL 变体。
type PhotoSizeKind string

const (
	// PhotoSizeKindDefault → photoSize（可下载，type/w/h/size）。
	PhotoSizeKindDefault PhotoSizeKind = "size"
	// PhotoSizeKindStripped → photoStrippedSize（内联字节，秒开模糊图）。
	PhotoSizeKindStripped PhotoSizeKind = "stripped"
	// PhotoSizeKindCached → photoCachedSize（内联字节 + w/h）。
	PhotoSizeKindCached PhotoSizeKind = "cached"
	// PhotoSizeKindPath → photoPathSize（内联 svg path 字节，矢量占位）。
	PhotoSizeKindPath PhotoSizeKind = "path"
	// PhotoSizeKindProgressive → photoSizeProgressive（渐进式 jpeg 多段大小）。
	PhotoSizeKindProgressive PhotoSizeKind = "progressive"
	// PhotoSizeKindVideo → photo.video_sizes 里的 videoSize（animated profile video）。
	PhotoSizeKindVideo PhotoSizeKind = "video"
	// PhotoSizeKindVideoEmojiMarkup → photo.video_sizes 里的 videoSizeEmojiMarkup。
	PhotoSizeKindVideoEmojiMarkup PhotoSizeKind = "video_emoji_markup"
	// PhotoSizeKindVideoStickerMarkup → photo.video_sizes 里的 videoSizeStickerMarkup。
	PhotoSizeKindVideoStickerMarkup PhotoSizeKind = "video_sticker_markup"
)

// PhotoSize 描述照片/缩略图的一种渲染尺寸。
type PhotoSize struct {
	Kind                 PhotoSizeKind `json:"kind"`
	Type                 string        `json:"type"`
	W                    int           `json:"w,omitempty"`
	H                    int           `json:"h,omitempty"`
	Size                 int           `json:"size,omitempty"`
	Bytes                []byte        `json:"bytes,omitempty"` // stripped/cached/path 内联内容
	Sizes                []int         `json:"sizes,omitempty"` // progressive
	VideoStartTs         float64       `json:"video_start_ts,omitempty"`
	EmojiID              int64         `json:"emoji_id,omitempty"`
	BackgroundColors     []int         `json:"background_colors,omitempty"`
	StickerSetID         int64         `json:"sticker_set_id,omitempty"`
	StickerSetAccessHash int64         `json:"sticker_set_access_hash,omitempty"`
	StickerSetShortName  string        `json:"sticker_set_short_name,omitempty"`
	StickerSetSystemKey  string        `json:"sticker_set_system_key,omitempty"`
	StickerID            int64         `json:"sticker_id,omitempty"`
}

// Downloadable 表示该尺寸需要客户端通过 upload.getFile 拉取（而非内联字节）。
func (s PhotoSize) Downloadable() bool {
	return s.Kind == PhotoSizeKindDefault || s.Kind == PhotoSizeKindProgressive || s.Kind == PhotoSizeKindVideo
}

// DocumentAttributeKind 标识 TL DocumentAttribute 变体。
type DocumentAttributeKind string

const (
	DocAttrImageSize   DocumentAttributeKind = "image_size"
	DocAttrAnimated    DocumentAttributeKind = "animated"
	DocAttrSticker     DocumentAttributeKind = "sticker"
	DocAttrVideo       DocumentAttributeKind = "video"
	DocAttrAudio       DocumentAttributeKind = "audio"
	DocAttrFilename    DocumentAttributeKind = "filename"
	DocAttrCustomEmoji DocumentAttributeKind = "custom_emoji"
)

// DocumentAttribute 是主路径用到的 TL DocumentAttribute 变体的并集。
type DocumentAttribute struct {
	Kind DocumentAttributeKind `json:"kind"`

	// image_size / video / sticker box
	W int `json:"w,omitempty"`
	H int `json:"h,omitempty"`

	// sticker / custom_emoji
	Alt                  string `json:"alt,omitempty"`
	Mask                 bool   `json:"mask,omitempty"`
	StickerSetID         int64  `json:"sticker_set_id,omitempty"`
	StickerSetAccessHash int64  `json:"sticker_set_access_hash,omitempty"`
	Free                 bool   `json:"free,omitempty"`       // custom_emoji
	TextColor            bool   `json:"text_color,omitempty"` // custom_emoji

	// video
	Duration          float64 `json:"duration,omitempty"`
	RoundMessage      bool    `json:"round_message,omitempty"`
	SupportsStreaming bool    `json:"supports_streaming,omitempty"`
	NoSound           bool    `json:"no_sound,omitempty"`
	VideoCodec        string  `json:"video_codec,omitempty"`

	// audio
	AudioDuration int    `json:"audio_duration,omitempty"`
	Voice         bool   `json:"voice,omitempty"`
	Title         string `json:"title,omitempty"`
	Performer     string `json:"performer,omitempty"`
	Waveform      []byte `json:"waveform,omitempty"`

	// filename
	FileName string `json:"file_name,omitempty"`
}

// Document 是已存储的 Telegram 文档（贴纸、gif、文件、视频、音频、自定义 emoji……）。
type Document struct {
	ID            int64               `json:"id"`
	AccessHash    int64               `json:"access_hash"`
	FileReference []byte              `json:"file_reference,omitempty"`
	Date          int                 `json:"date,omitempty"`
	MimeType      string              `json:"mime_type,omitempty"`
	Size          int64               `json:"size,omitempty"`
	DCID          int                 `json:"dc_id,omitempty"`
	Attributes    []DocumentAttribute `json:"attributes,omitempty"`
	Thumbs        []PhotoSize         `json:"thumbs,omitempty"`
}

// StickerSetRef 返回该文档归属的贴纸集引用（若有 sticker/custom_emoji 属性）。
func (d Document) StickerSetRef() (id, accessHash int64, ok bool) {
	for _, attr := range d.Attributes {
		if attr.Kind == DocAttrSticker || attr.Kind == DocAttrCustomEmoji {
			if attr.StickerSetID != 0 {
				return attr.StickerSetID, attr.StickerSetAccessHash, true
			}
		}
	}
	return 0, 0, false
}

// IsMusic reports whether the document is a Telegram profile/shared-music song:
// documentAttributeAudio with voice=false. MIME type is intentionally not used
// as the source of truth because clients key their UI off the TL attribute.
func (d Document) IsMusic() bool {
	for _, attr := range d.Attributes {
		if attr.Kind == DocAttrAudio && !attr.Voice {
			return true
		}
	}
	return false
}

// IsSticker reports whether the document carries a sticker attribute
// (static / animated / video stickers all use documentAttributeSticker).
func (d Document) IsSticker() bool {
	for _, attr := range d.Attributes {
		if attr.Kind == DocAttrSticker {
			return true
		}
	}
	return false
}

func (d Document) IsCustomEmoji() bool {
	for _, attr := range d.Attributes {
		if attr.Kind == DocAttrCustomEmoji {
			return true
		}
	}
	return false
}

func (d Document) IsStickerLike() bool {
	return d.IsSticker() || d.IsCustomEmoji()
}

// MaxStickerMaterialDocumentSize bounds uploaded documents that can be promoted
// into user-created sticker/custom emoji sets without server-side transcoding.
const MaxStickerMaterialDocumentSize int64 = 20 << 20

// IsStickerSetMaterial reports whether an existing Document can be used as an
// input item for stickers.createStickerSet/stickers.addStickerToSet. Already
// tagged sticker/custom emoji documents always pass. Untagged TGS/WebP/Lottie
// JSON uploads can be shaped later from their MIME/body; video uploads must
// already carry a video attribute because this layer does not transcode or
// probe containers.
func (d Document) IsStickerSetMaterial() bool {
	if d.IsStickerLike() {
		return true
	}
	if d.ID == 0 || d.AccessHash == 0 || d.Size > MaxStickerMaterialDocumentSize {
		return false
	}
	switch d.stickerMaterialMimeOrExt() {
	case "application/x-tgsticker", "image/webp", "application/json":
		return true
	case "video/webm", "video/mp4":
		return d.hasDocumentAttribute(DocAttrVideo)
	default:
		return false
	}
}

func (d Document) hasDocumentAttribute(kind DocumentAttributeKind) bool {
	for _, attr := range d.Attributes {
		if attr.Kind == kind {
			return true
		}
	}
	return false
}

// StickerSetMaterialMime returns the normalized sticker material MIME inferred
// from the document MIME type or a filename attribute.
func (d Document) StickerSetMaterialMime() string {
	return d.stickerMaterialMimeOrExt()
}

func (d Document) stickerMaterialMimeOrExt() string {
	mimeType := strings.ToLower(strings.TrimSpace(d.MimeType))
	switch mimeType {
	case "application/x-tgsticker", "image/webp", "video/webm", "video/mp4",
		"application/json", "application/lottie+json", "text/json":
		if mimeType == "application/lottie+json" || mimeType == "text/json" {
			return "application/json"
		}
		return mimeType
	}
	for _, attr := range d.Attributes {
		if attr.Kind != DocAttrFilename {
			continue
		}
		switch strings.ToLower(filepath.Ext(strings.TrimSpace(attr.FileName))) {
		case ".tgs":
			return "application/x-tgsticker"
		case ".webp":
			return "image/webp"
		case ".json":
			return "application/json"
		case ".webm":
			return "video/webm"
		case ".mp4":
			return "video/mp4"
		}
	}
	return mimeType
}

// IsGif reports whether the document is a canonical, savable Telegram GIFv.
// Telegram 的 GIF 是无声 MP4；仅有 animated 属性的 raw image/gif 不是合法 saved GIF。
func (d Document) IsGif() bool {
	if !strings.EqualFold(strings.TrimSpace(d.MimeType), "video/mp4") {
		return false
	}
	var animated, video bool
	for _, attr := range d.Attributes {
		switch attr.Kind {
		case DocAttrAnimated:
			animated = true
		case DocAttrVideo:
			if !attr.RoundMessage && attr.W > 0 && attr.H > 0 && attr.Duration > 0 {
				video = true
			}
		}
	}
	return animated && video
}

// Photo 是已存储的 Telegram 照片（头像或图片消息）。
type Photo struct {
	ID            int64       `json:"id"`
	AccessHash    int64       `json:"access_hash"`
	FileReference []byte      `json:"file_reference,omitempty"`
	Date          int         `json:"date,omitempty"`
	DCID          int         `json:"dc_id,omitempty"`
	HasStickers   bool        `json:"has_stickers,omitempty"`
	Sizes         []PhotoSize `json:"sizes,omitempty"`
}

func ClonePhotoPtr(photo *Photo) *Photo {
	if photo == nil {
		return nil
	}
	clone := *photo
	clone.FileReference = append([]byte(nil), photo.FileReference...)
	clone.Sizes = append([]PhotoSize(nil), photo.Sizes...)
	for i := range clone.Sizes {
		clone.Sizes[i].Bytes = append([]byte(nil), photo.Sizes[i].Bytes...)
		clone.Sizes[i].Sizes = append([]int(nil), photo.Sizes[i].Sizes...)
		clone.Sizes[i].BackgroundColors = append([]int(nil), photo.Sizes[i].BackgroundColors...)
	}
	return &clone
}

// MessageMediaKind 枚举消息可挂载的媒体载荷。
type MessageMediaKind string

const (
	MessageMediaKindNone     MessageMediaKind = ""
	MessageMediaKindPhoto    MessageMediaKind = "photo"
	MessageMediaKindDocument MessageMediaKind = "document"
	MessageMediaKindContact  MessageMediaKind = "contact"
	MessageMediaKindService  MessageMediaKind = "service"
	MessageMediaKindGeo      MessageMediaKind = "geo"
	MessageMediaKindVenue    MessageMediaKind = "venue"
	MessageMediaKindDice     MessageMediaKind = "dice"
	MessageMediaKindPoll     MessageMediaKind = "poll"
	MessageMediaKindGeoLive  MessageMediaKind = "geo_live"
	MessageMediaKindTodo     MessageMediaKind = "todo"
	MessageMediaKindStory    MessageMediaKind = "story"
	MessageMediaKindWebPage  MessageMediaKind = "web_page"
)

// MessageTodoItem 是清单中的一项（id 为列表内唯一正整数，客户端分配）。
type MessageTodoItem struct {
	ID       int             `json:"id"`
	Title    string          `json:"title"`
	Entities []MessageEntity `json:"entities,omitempty"`
}

// MessageTodoCompletion 记录某项被谁在何时勾选完成。
type MessageTodoCompletion struct {
	ID          int   `json:"id"`
	CompletedBy int64 `json:"completed_by"`
	Date        int   `json:"date"`
}

// MessageTodo 是待办清单（messageMediaToDo）。append/toggle 经 editMessage 媒体替换链路
// 整体更新快照（与 live location 同模式）。
type MessageTodo struct {
	OthersCanAppend   bool                    `json:"others_can_append,omitempty"`
	OthersCanComplete bool                    `json:"others_can_complete,omitempty"`
	Title             string                  `json:"title"`
	TitleEntities     []MessageEntity         `json:"title_entities,omitempty"`
	Items             []MessageTodoItem       `json:"items"`
	Completions       []MessageTodoCompletion `json:"completions,omitempty"`
}

// MessageGeoPoint 是消息中携带的地理坐标点（messageMediaGeo/Venue 共用）。
// AccessHash 在发送时随机生成，客户端会原样带回 upload.getWebFile 地图缩略请求。
type MessageGeoPoint struct {
	Lat            float64 `json:"lat"`
	Long           float64 `json:"long"`
	AccessHash     int64   `json:"access_hash,omitempty"`
	AccuracyRadius int     `json:"accuracy_radius,omitempty"`
}

// MessageVenue 是带场所信息的位置（messageMediaVenue）。
type MessageVenue struct {
	Geo       MessageGeoPoint `json:"geo"`
	Title     string          `json:"title"`
	Address   string          `json:"address,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	VenueID   string          `json:"venue_id,omitempty"`
	VenueType string          `json:"venue_type,omitempty"`
}

// MessageDice 是互动 emoji（messageMediaDice）。Value 在发送落库时由服务端
// 一次定值，此后对所有 viewer 与转发快照保持不变。
type MessageDice struct {
	Emoticon string `json:"emoticon"`
	Value    int    `json:"value"`
}

// MessageGeoLive 是实时位置（messageMediaGeoLive）。客户端按 message.date + Period
// 自行计算过期；停止共享 = editMessage 把 Period 改为已逝时长（立即过期），服务端无后台任务。
type MessageGeoLive struct {
	Geo                         MessageGeoPoint `json:"geo"`
	Heading                     int             `json:"heading,omitempty"`
	Period                      int             `json:"period"`
	ProximityNotificationRadius int             `json:"proximity_notification_radius,omitempty"`
}

// MessageStory is a story shared as messageMediaStory. Peer+ID are the stable
// reference; Story is the viewer-visible snapshot captured when the message was
// sent so history/difference can replay without re-resolving the source story.
type MessageStory struct {
	Peer       Peer   `json:"peer"`
	ID         int    `json:"id"`
	ViaMention bool   `json:"via_mention,omitempty"`
	Story      *Story `json:"story,omitempty"`
}

// MessageWebPageState 区分链接预览的三种 TL 形态：pending（webPagePending，
// 已知链接但预览尚未异步抓取完成）、done（webPage，已解析的预览卡片）、
// empty（webPageEmpty，已解析但该链接无可展示内容）。
type MessageWebPageState string

const (
	MessageWebPageStatePending MessageWebPageState = "pending"
	MessageWebPageStateDone    MessageWebPageState = "done"
	MessageWebPageStateEmpty   MessageWebPageState = "empty"
)

// MessageWebPage 是以 messageMediaWebPage 形式挂载的链接预览。URL 是稳定引用，
// 其余字段是服务端在解析时捕获的快照，使 history/difference 无需重新抓取即可回放
// （与 MessageStory 同模式）。State 决定投影为 webPagePending / webPage / webPageEmpty。
//
// ForceLargeMedia/ForceSmallMedia/Manual/Safe 是 messageMediaWebPage 外层 wrapper
// 的标志位；HasLargeMedia 是 webPage 本体的标志位。ID 必须在 pending→done 切换前后
// 保持稳定，客户端按 webPage id 关联占位与解析结果。
type MessageWebPage struct {
	State       MessageWebPageState `json:"state"`
	ID          int64               `json:"id"`
	URL         string              `json:"url"`
	DisplayURL  string              `json:"display_url,omitempty"`
	Hash        int                 `json:"hash,omitempty"`
	Date        int                 `json:"date,omitempty"`
	Type        string              `json:"type,omitempty"`
	SiteName    string              `json:"site_name,omitempty"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Author      string              `json:"author,omitempty"`
	Photo       *Photo              `json:"photo,omitempty"`
	// ComposeToneEmojiID 映射 webPageAttributeAiComposeTone，用于 TDesktop 渲染
	// addstyle tone 分享卡片；0 表示普通网页。
	ComposeToneEmojiID int64 `json:"compose_tone_emoji_id,omitempty"`

	ForceLargeMedia bool `json:"force_large_media,omitempty"`
	ForceSmallMedia bool `json:"force_small_media,omitempty"`
	Manual          bool `json:"manual,omitempty"`
	Safe            bool `json:"safe,omitempty"`
	HasLargeMedia   bool `json:"has_large_media,omitempty"`
}

// MessageServiceActionKind 标识私聊服务消息动作。
type MessageServiceActionKind string

const (
	MessageServiceActionSuggestProfilePhoto MessageServiceActionKind = "suggest_profile_photo"
	// MessageServiceActionPinMessage 映射 messageActionPinMessage：非
	// pm_oneside 私聊置顶生成的服务消息，被置顶消息经 reply_to 指向。
	MessageServiceActionPinMessage MessageServiceActionKind = "pin_message"
	// MessageServiceActionPhoneCall 映射 messageActionPhoneCall：私聊通话
	// 结束（含 missed 超时）后落历史的通话条目，sender 恒为主叫。
	MessageServiceActionPhoneCall MessageServiceActionKind = "phone_call"
	// MessageServiceActionConferenceCall 映射 messageActionConferenceCall：
	// ad-hoc conference call 的私聊邀请/状态服务消息。
	MessageServiceActionConferenceCall MessageServiceActionKind = "conference_call"
	// MessageServiceActionBotAllowed 映射 messageActionBotAllowed：用户授权
	// bot 后在 bot 私聊中留下的服务消息。
	MessageServiceActionBotAllowed MessageServiceActionKind = "bot_allowed"
	// MessageServiceActionWebViewDataSent 映射 messageActionWebViewDataSent*
	//：simple webview 把 data 回传给 bot 后在私聊中留下的服务消息。
	MessageServiceActionWebViewDataSent MessageServiceActionKind = "web_view_data_sent"
	// MessageServiceActionRequestedPeer 映射 messageActionRequestedPeer*：用户
	// 响应 bot 的 request-peer 按钮后，把所选 peer 作为可恢复服务消息发给 bot。
	MessageServiceActionRequestedPeer MessageServiceActionKind = "requested_peer"
	// MessageServiceActionSetChatTheme 映射 messageActionSetChatTheme：
	// 私聊双方共享的 chat theme token 变更。
	MessageServiceActionSetChatTheme MessageServiceActionKind = "set_chat_theme"
	// MessageServiceActionStarGift 映射 messageActionStarGift：收到一份 Star 礼物。
	// 礼物快照（贴纸/星价）内嵌在 action 里，收礼人无需额外拉取即可渲染气泡。
	MessageServiceActionStarGift MessageServiceActionKind = "star_gift"
	// MessageServiceActionStarGiftUnique maps messageActionStarGiftUnique. The
	// immutable collectible snapshot is carried by the service message so an
	// exact replay/difference never depends on mutable catalog state.
	MessageServiceActionStarGiftUnique MessageServiceActionKind = "star_gift_unique"
)

// MessagePhoneCallAction 是 messageActionPhoneCall 的协议中立载荷。
// Reason 取值同 PhoneCallDiscardReason；Duration 仅通话真正建立后非零。
type MessagePhoneCallAction struct {
	CallID   int64  `json:"call_id"`
	Reason   string `json:"reason,omitempty"`
	Duration int    `json:"duration,omitempty"`
	Video    bool   `json:"video,omitempty"`
}

// MessageConferenceCallAction 是 messageActionConferenceCall 的协议中立载荷。
type MessageConferenceCallAction struct {
	CallID            int64  `json:"call_id"`
	Missed            bool   `json:"missed,omitempty"`
	Active            bool   `json:"active,omitempty"`
	Video             bool   `json:"video,omitempty"`
	Duration          int    `json:"duration,omitempty"`
	OtherParticipants []Peer `json:"other_participants,omitempty"`
}

// MessageBotAllowedAction 是 messageActionBotAllowed 的协议中立载荷。
type MessageBotAllowedAction struct {
	AttachMenu  bool   `json:"attach_menu,omitempty"`
	FromRequest bool   `json:"from_request,omitempty"`
	Domain      string `json:"domain,omitempty"`
}

const (
	MaxWebViewDataButtonTextLen = MaxBotMenuButtonTextLen
	MaxWebViewDataPayloadLen    = MaxMessageTextLength
)

// MessageWebViewDataAction 是 messageActionWebViewDataSent* 的协议中立载荷。
type MessageWebViewDataAction struct {
	ButtonText string `json:"button_text"`
	Data       string `json:"data,omitempty"`
}

// MessageRequestedPeerAction 是 messageActionRequestedPeer* 的协议中立载荷。
type MessageRequestedPeerAction struct {
	ButtonID int    `json:"button_id"`
	Peers    []Peer `json:"peers"`
}

// MessageServiceAction 是私聊服务消息动作的协议中立表示。
type MessageServiceAction struct {
	Kind              MessageServiceActionKind     `json:"kind"`
	Photo             *Photo                       `json:"photo,omitempty"`
	Call              *MessagePhoneCallAction      `json:"call,omitempty"`
	ConferenceCall    *MessageConferenceCallAction `json:"conference_call,omitempty"`
	BotAllowed        *MessageBotAllowedAction     `json:"bot_allowed,omitempty"`
	WebViewData       *MessageWebViewDataAction    `json:"web_view_data,omitempty"`
	RequestedPeer     *MessageRequestedPeerAction  `json:"requested_peer,omitempty"`
	ChatThemeEmoticon string                       `json:"chat_theme_emoticon,omitempty"`
	StarGift          *MessageStarGiftAction       `json:"star_gift,omitempty"`
	StarGiftUnique    *MessageStarGiftUniqueAction `json:"star_gift_unique,omitempty"`
}

// MessageStarGiftAction 是 messageActionStarGift 的协议中立载荷：内嵌礼物快照（贴纸/星价）
// 使收礼人无需额外拉取即可渲染。PeerUserID/PeerChannelID 为收礼方；NameHidden 时下发不暴露 from。
type MessageStarGiftAction struct {
	GiftID         int64     `json:"gift_id"`
	Stars          int64     `json:"stars"`
	ConvertStars   int64     `json:"convert_stars,omitempty"`
	Title          string    `json:"title,omitempty"`
	Sticker        *Document `json:"sticker,omitempty"`
	Message        string    `json:"message,omitempty"`
	FromUserID     int64     `json:"from_user_id,omitempty"`
	PeerUserID     int64     `json:"peer_user_id,omitempty"`
	PeerChannelID  int64     `json:"peer_channel_id,omitempty"`
	SavedID        int64     `json:"saved_id,omitempty"`
	NameHidden     bool      `json:"name_hidden,omitempty"`
	Saved          bool      `json:"saved,omitempty"`
	Converted      bool      `json:"converted,omitempty"`
	CanUpgrade     bool      `json:"can_upgrade,omitempty"`
	PrepaidUpgrade bool      `json:"prepaid_upgrade,omitempty"`
	UpgradeStars   int64     `json:"upgrade_stars,omitempty"`
	UpgradeMsgID   int       `json:"upgrade_msg_id,omitempty"`
}

// MessageStarGiftUniqueAction is the protocol-neutral payload of an upgrade
// service message. Commercial transfer/resale/export fields are intentionally
// absent from the collectibles mainline.
type MessageStarGiftUniqueAction struct {
	Gift           UniqueStarGift `json:"gift"`
	FromUserID     int64          `json:"from_user_id,omitempty"`
	Peer           Peer           `json:"peer"`
	SavedID        int64          `json:"saved_id,omitempty"`
	Upgrade        bool           `json:"upgrade,omitempty"`
	Saved          bool           `json:"saved,omitempty"`
	PrepaidUpgrade bool           `json:"prepaid_upgrade,omitempty"`
}

// MessageMedia 是一条消息媒体载荷的业务表示（落库为消息行上的 JSONB 快照）。
type MessageMedia struct {
	Kind          MessageMediaKind      `json:"kind"`
	Photo         *Photo                `json:"photo,omitempty"`
	Document      *Document             `json:"document,omitempty"`
	Contact       *MessageContact       `json:"contact,omitempty"`
	ServiceAction *MessageServiceAction `json:"service_action,omitempty"`
	Geo           *MessageGeoPoint      `json:"geo,omitempty"`
	Venue         *MessageVenue         `json:"venue,omitempty"`
	Dice          *MessageDice          `json:"dice,omitempty"`
	Poll          *MessagePoll          `json:"poll,omitempty"`
	GeoLive       *MessageGeoLive       `json:"geo_live,omitempty"`
	Todo          *MessageTodo          `json:"todo,omitempty"`
	Story         *MessageStory         `json:"story,omitempty"`
	WebPage       *MessageWebPage       `json:"web_page,omitempty"`
	Spoiler       bool                  `json:"spoiler,omitempty"`
	TTLSeconds    int                   `json:"ttl_seconds,omitempty"`
	Nopremium     bool                  `json:"nopremium,omitempty"`
	Voice         bool                  `json:"voice,omitempty"`
	Round         bool                  `json:"round,omitempty"`
	Video         bool                  `json:"video,omitempty"`
	// InvertMedia 映射 message.invert_media：媒体（典型为链接预览）渲染在文本上方。
	// 存于媒体快照而非消息行，避免新增消息表列；读时投影为 tg.Message.invert_media。
	InvertMedia bool `json:"invert_media,omitempty"`
}

// IsMusic reports whether the message media is a music document rather than a
// voice note, video, or generic file.
func (m *MessageMedia) IsMusic() bool {
	return m != nil &&
		m.Kind == MessageMediaKindDocument &&
		m.Document != nil &&
		!m.Voice &&
		m.Document.IsMusic()
}

// MessageContact is a shared contact card attached to a message.
type MessageContact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name,omitempty"`
	Vcard       string `json:"vcard,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
}

// IsZero 表示无媒体（用于落库时跳过空快照、转换时回退 MessageMediaEmpty）。
func (m *MessageMedia) IsZero() bool {
	return m == nil || m.Kind == MessageMediaKindNone
}

// HasUnreadPayload 表示该媒体是否参与 media_unread（"未听"）状态。
// 只有 voice/round 才有此语义：客户端只为它们渲染未听标记并上报
// readMessageContents；photo/document 置位只会留下永不清除的脏状态。
func (m *MessageMedia) HasUnreadPayload() bool {
	if m.IsZero() || m.Kind != MessageMediaKindDocument {
		return false
	}
	return m.Voice || m.Round
}

// StickerPack 是 emoji→文档 id 的映射条目（messages.stickerSet.packs）。
type StickerPack struct {
	Emoticon    string  `json:"emoticon"`
	DocumentIDs []int64 `json:"document_ids"`
}

type StickerKeyword struct {
	DocumentID int64    `json:"document_id"`
	Keywords   []string `json:"keywords"`
}

const (
	// StickerSetSystemKeyEmojiDefaultStatuses 是 inputStickerSetEmojiDefaultStatuses
	// 对应的系统集标识：premium 用户 emoji status 选择器的"默认状态"主体集
	// （messages.getStickerSet 与 account.getDefaultEmojiStatuses 共用）。
	StickerSetSystemKeyEmojiDefaultStatuses = "emoji_default_statuses"
	// StickerSetSystemKeyEmojiDefaultTopicIcons 对应论坛 topic 默认图标系统集。
	StickerSetSystemKeyEmojiDefaultTopicIcons = "emoji_default_topic_icons"
	// StickerSetSystemKeyPremiumGifts 对应 premium gifts 系统集。
	StickerSetSystemKeyPremiumGifts = "premium_gifts"
	// StickerSetSystemKeyTonGifts 对应 TON gifts 系统集。
	StickerSetSystemKeyTonGifts = "ton_gifts"
)

// StickerSetKind 区分贴纸集用途（影响 getAllStickers / getEmojiStickers 归类）。
type StickerSetKind string

const (
	StickerSetKindStickers StickerSetKind = "stickers"
	StickerSetKindEmoji    StickerSetKind = "emoji"
	StickerSetKindMasks    StickerSetKind = "masks"
	// StickerSetKindSystem 是 TDesktop 通过 InputStickerSetDice/AnimatedEmoji 等系统集请求的内置集。
	StickerSetKindSystem StickerSetKind = "system"
)

// StickerSet 是贴纸/自定义 emoji 集的元数据 + 有序文档 id。
type StickerSet struct {
	ID              int64            `json:"id"`
	AccessHash      int64            `json:"access_hash"`
	ShortName       string           `json:"short_name"`
	Title           string           `json:"title"`
	Count           int              `json:"count"`
	Hash            int              `json:"hash"`
	Kind            StickerSetKind   `json:"set_kind"`
	Official        bool             `json:"official,omitempty"`
	Animated        bool             `json:"animated,omitempty"`
	Videos          bool             `json:"videos,omitempty"`
	Emojis          bool             `json:"emojis,omitempty"`
	Masks           bool             `json:"masks,omitempty"`
	TextColor       bool             `json:"text_color,omitempty"`
	Creator         bool             `json:"creator,omitempty"`
	CreatorUserID   int64            `json:"creator_user_id,omitempty"`
	Installed       bool             `json:"installed,omitempty"`
	Archived        bool             `json:"archived,omitempty"`
	Deleted         bool             `json:"deleted,omitempty"`
	InstalledDate   int              `json:"installed_date,omitempty"`
	ThumbDocumentID int64            `json:"thumb_document_id,omitempty"`
	Thumbs          []PhotoSize      `json:"thumbs,omitempty"`
	ThumbDCID       int              `json:"thumb_dc_id,omitempty"`
	ThumbVersion    int              `json:"thumb_version,omitempty"`
	DocumentIDs     []int64          `json:"document_ids,omitempty"`
	Packs           []StickerPack    `json:"packs,omitempty"`
	Keywords        []StickerKeyword `json:"keywords,omitempty"`
	SortOrder       int              `json:"sort_order,omitempty"`
	Software        string           `json:"software,omitempty"`
	// SystemKey 是 TDesktop 系统集的稳定标识（如 "animated_emoji"、"dice:🎲"），用于 InputStickerSet* 路由。
	SystemKey string `json:"system_key,omitempty"`
}

const (
	MinStickerSetShortNameLen = 5
	MaxStickerSetShortNameLen = 32
	MaxStickerSetTitleLen     = 64
	MaxStickerSetItems        = 120
	MaxStickerSetKeywords     = 20
	MaxStickerSetKeywordLen   = 32
)

type StickerSetItemInput struct {
	DocumentID         int64
	DocumentAccessHash int64
	Emoji              string
	Keywords           string
}

type CreateStickerSetRequest struct {
	CreatorUserID   int64
	Title           string
	ShortName       string
	Kind            StickerSetKind
	TextColor       bool
	ThumbDocumentID int64
	ThumbAccessHash int64
	Items           []StickerSetItemInput
	Software        string
	Date            int
}

// ProfilePhotoKind distinguishes a user's real profile photo from the fallback
// public photo shown when privacy hides the real one.
type ProfilePhotoKind string

const (
	ProfilePhotoKindProfile  ProfilePhotoKind = "profile"
	ProfilePhotoKindFallback ProfilePhotoKind = "fallback"
)

// ProfilePhotoRef 是渲染头像所需的最小信息（当前 profile/fallback/personal photo）。
type ProfilePhotoRef struct {
	PhotoID  int64
	DCID     int
	Stripped []byte // photoStrippedSize 内联缩略图，可空
	Personal bool   // true 表示 viewer 私有联系人头像
	HasVideo bool   // true 表示 photo.video_sizes 非空，需输出 userProfilePhoto.has_video
}

// StrippedFromSizes 从照片尺寸列表里取出 stripped 缩略图字节（用于 UserProfilePhoto/ChatPhoto 占位）。
func StrippedFromSizes(sizes []PhotoSize) []byte {
	for _, s := range sizes {
		if s.Kind == PhotoSizeKindStripped {
			return s.Bytes
		}
	}
	return nil
}

// PhotoHasVideo 从照片尺寸快照推导 UserProfilePhoto.has_video。
func PhotoHasVideo(sizes []PhotoSize) bool {
	for _, s := range sizes {
		switch s.Kind {
		case PhotoSizeKindVideo, PhotoSizeKindVideoEmojiMarkup, PhotoSizeKindVideoStickerMarkup:
			return true
		}
	}
	return false
}

// StickerSetRefKind 标识 InputStickerSet 的解析方式。
type StickerSetRefKind string

const (
	StickerSetRefByID        StickerSetRefKind = "id"
	StickerSetRefByShortName StickerSetRefKind = "short_name"
	StickerSetRefBySystem    StickerSetRefKind = "system"
)

// StickerSetRef 是 rpc 层从 tg.InputStickerSet 转换得到的贴纸集引用。
type StickerSetRef struct {
	Kind       StickerSetRefKind
	ID         int64
	AccessHash int64
	ShortName  string
	SystemKey  string
}

// AvailableReaction 描述 messages.getAvailableReactions 的一项（真实资源由文档 id 引用）。
type AvailableReaction struct {
	Reaction            string `json:"reaction"`
	Title               string `json:"title"`
	Inactive            bool   `json:"inactive,omitempty"`
	Premium             bool   `json:"premium,omitempty"`
	StaticIconID        int64  `json:"static_icon_id,omitempty"`
	AppearAnimationID   int64  `json:"appear_animation_id,omitempty"`
	SelectAnimationID   int64  `json:"select_animation_id,omitempty"`
	ActivateAnimationID int64  `json:"activate_animation_id,omitempty"`
	EffectAnimationID   int64  `json:"effect_animation_id,omitempty"`
	AroundAnimationID   int64  `json:"around_animation_id,omitempty"`
	CenterIconID        int64  `json:"center_icon_id,omitempty"`
	Order               int    `json:"order,omitempty"`
}

// AvailableEffect 是一条消息发送特效（messages.getAvailableEffects）。引用三个文档:
// 选择器静态图标 / 气泡上的特效贴纸 / 全屏特效动画(后两者可为 0)。属全局静态目录,
// 服务端 seed 后常驻内存。
type AvailableEffect struct {
	ID                int64  `json:"id"`
	Emoticon          string `json:"emoticon"`
	StaticIconID      int64  `json:"static_icon_id,omitempty"`
	EffectStickerID   int64  `json:"effect_sticker_id"`
	EffectAnimationID int64  `json:"effect_animation_id,omitempty"`
	PremiumRequired   bool   `json:"premium_required,omitempty"`
	Order             int    `json:"order,omitempty"`
}

// DocumentIDs 收集该 effect 引用的全部文档 id（去零去重）。
func (e AvailableEffect) DocumentIDs() []int64 {
	return dedupNonZeroIDs(e.StaticIconID, e.EffectStickerID, e.EffectAnimationID)
}

// DocumentIDs 收集该 reaction 引用的全部文档 id（去零去重，便于批量加载）。
func (r AvailableReaction) DocumentIDs() []int64 {
	raw := []int64{
		r.StaticIconID, r.AppearAnimationID, r.SelectAnimationID,
		r.ActivateAnimationID, r.EffectAnimationID, r.AroundAnimationID, r.CenterIconID,
	}
	out := make([]int64, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for _, id := range raw {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// dedupNonZeroIDs 返回去零去重后的 id 列表（保序）。
func dedupNonZeroIDs(ids ...int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
