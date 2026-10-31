package domain

import (
	"path/filepath"
	"strings"
	"time"
)

// This file defines media-related business value objects (documents, photos, sticker sets, available reactions, message media).
// These types do not depend on tg.* at all; the rpc layer handles domain↔tg conversion.
//
// The json tags let the store layer json.Marshal directly into JSONB (message media snapshots,
// document/photo metadata). They are protocol-neutral pure data, not tg-generated types.

// MediaBackend identifies the single durable backend where blob bytes actually live.
type MediaBackend string

const (
	// MediaBackendLocalFS means blob bytes live on local disk (object_key is a relative path).
	MediaBackendLocalFS MediaBackend = "localfs"
	// MediaBackendS3 means blob bytes live in the configured S3/MinIO bucket.
	MediaBackendS3 MediaBackend = "s3"
)

// FileBlob is an index entry for a downloadable binary object: location_key → backend/object key/size/mime.
// The actual bytes are read/written by the blob backend by ObjectKey; this struct only describes location and metadata.
type FileBlob struct {
	// LocationKey is the stable logical location key, derived from getFile's InputFileLocation:
	//   doc:<id>            document body
	//   doc:<id>:<type>     document thumbnail (PhotoSize type)
	//   photo:<id>:<type>   one photo size
	LocationKey string       `json:"location_key"`
	Backend     MediaBackend `json:"backend"`
	ObjectKey   string       `json:"object_key"`
	Size        int64        `json:"size"`
	SHA256      []byte       `json:"sha256,omitempty"`
	MimeType    string       `json:"mime_type,omitempty"`
}

// UploadPart is the metadata of one part accumulated by upload.saveFilePart/saveBigFilePart.
// Part bytes do not enter PG; PG records only object_key/size/hash, and metadata and temp objects are cleaned up after assembly.
type UploadPart struct {
	OwnerUserID int64
	FileID      int64
	Part        int
	TotalParts  int // Big: known positive total or -1 while generating; small: 0.
	Big         bool
	Backend     MediaBackend
	ObjectKey   string
	Size        int64
	SHA256      []byte
}

// UploadPartUsage describes a user's current not-yet-assembled upload part usage.
type UploadPartUsage struct {
	Bytes int64
	Parts int
	Files int
}

// UploadPartSlot describes the current state of one (owner,file_id,part) slot, used to determine the quota delta of a retry overwrite.
type UploadPartSlot struct {
	ExistingBytes int64
	ObjectKey     string
	FileParts     int
	Found         bool
}

// UploadPartQuota limits a user's in-flight upload part usage; a field <=0 means that dimension is unlimited.
type UploadPartQuota struct {
	MaxBytes int64
	MaxParts int
	MaxFiles int
}

// UploadedFileRef references a file a client has fully uploaded via upload.saveFilePart(Big).
// The rpc layer converts it from tg.InputFile/InputFileBig; the files service assembles the blob from it.
type UploadedFileRef struct {
	OwnerUserID int64
	FileID      int64
	Parts       int
	Name        string
	Big         bool
	MD5         string // small-file client md5_checksum (hex), verifiable; empty for a big file
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

// DocumentSpec describes the metadata for creating a Document from an uploaded file (from InputMediaUploadedDocument).
type DocumentSpec struct {
	MimeType   string
	Attributes []DocumentAttribute
	Thumb      *UploadedFileRef // optional thumbnail upload, produces doc:<id>:m
	ForceFile  bool
	// NosoundVideo corresponds to inputMediaUploadedDocument.nosound_video. A real GIF is still
	// converted to MP4, but when this flag is present it is persisted as an ordinary soundless video without the animated attribute (album path).
	NosoundVideo bool
}

// FileDownloadRequest is the parsed download request of upload.getFile;
// LocationKey is derived by the rpc layer from tg.InputFileLocation (doc:<id> / photo:<id>:<type> etc.).
type FileDownloadRequest struct {
	LocationKey string
	Offset      int64
	Limit       int
}

// FileChunk is one piece of content returned by upload.getFile. Bytes is a read-only borrow: the caller may read or
// encode within the lifetime of the FileChunk, but must not modify the content or write an append result back to the original
// backing. The file service may let concurrent cache/singleflight callers share an immutable backing,
// so a hot range is not copied into a separate payload per downloader.
type FileChunk struct {
	Bytes    []byte
	MimeType string
	Total    int64
	// ImmutableRange is present only when Bytes came from the exact range of a
	// content-addressed immutable blob. MTProto may retain this descriptor instead
	// of a second long-lived copy of Bytes until the answer is acknowledged.
	ImmutableRange *ImmutableFileRange
}

// ImmutableFileRange is a capability for exact byte replay. ObjectKey names
// immutable content in Backend; RangeSHA256 protects the requested slice from
// backend corruption or an invalid implementation. It deliberately contains no
// location key, access hash, or mutable database identifier.
type ImmutableFileRange struct {
	Backend     MediaBackend
	ObjectKey   string
	Offset      int64
	Length      int
	Total       int64
	MimeType    string
	RangeSHA256 [32]byte
}

// PhotoSizeKind identifies the TL variant of a PhotoSize.
type PhotoSizeKind string

const (
	// PhotoSizeKindDefault → photoSize (downloadable, type/w/h/size).
	PhotoSizeKindDefault PhotoSizeKind = "size"
	// PhotoSizeKindStripped → photoStrippedSize (inline bytes, an instantly-shown blurred image).
	PhotoSizeKindStripped PhotoSizeKind = "stripped"
	// PhotoSizeKindCached → photoCachedSize (inline bytes + w/h).
	PhotoSizeKindCached PhotoSizeKind = "cached"
	// PhotoSizeKindPath → photoPathSize (inline svg path bytes, a vector placeholder).
	PhotoSizeKindPath PhotoSizeKind = "path"
	// PhotoSizeKindProgressive → photoSizeProgressive (progressive jpeg multi-segment sizes).
	PhotoSizeKindProgressive PhotoSizeKind = "progressive"
	// PhotoSizeKindVideo → videoSize in photo.video_sizes (animated profile video).
	PhotoSizeKindVideo PhotoSizeKind = "video"
	// PhotoSizeKindVideoEmojiMarkup → videoSizeEmojiMarkup in photo.video_sizes.
	PhotoSizeKindVideoEmojiMarkup PhotoSizeKind = "video_emoji_markup"
	// PhotoSizeKindVideoStickerMarkup → videoSizeStickerMarkup in photo.video_sizes.
	PhotoSizeKindVideoStickerMarkup PhotoSizeKind = "video_sticker_markup"
)

// PhotoSize describes one rendering size of a photo/thumbnail.
type PhotoSize struct {
	Kind                 PhotoSizeKind `json:"kind"`
	Type                 string        `json:"type"`
	W                    int           `json:"w,omitempty"`
	H                    int           `json:"h,omitempty"`
	Size                 int           `json:"size,omitempty"`
	Bytes                []byte        `json:"bytes,omitempty"` // stripped/cached/path inline content
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

// Downloadable means the size must be pulled by the client via upload.getFile (rather than inline bytes).
func (s PhotoSize) Downloadable() bool {
	return s.Kind == PhotoSizeKindDefault || s.Kind == PhotoSizeKindProgressive || s.Kind == PhotoSizeKindVideo
}

// DocumentAttributeKind identifies the TL DocumentAttribute variant.
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

// DocumentAttribute is the union of the TL DocumentAttribute variants used on the main path.
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

// Document is a stored Telegram document (sticker, gif, file, video, audio, custom emoji, ...).
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

// StickerSetRef returns the sticker set reference this document belongs to (if it has a sticker/custom_emoji attribute).
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
// A Telegram GIF is a soundless MP4; a raw image/gif with only the animated attribute is not a valid saved GIF.
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

// Photo is a stored Telegram photo (avatar or image message).
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

// MessageMediaKind enumerates the media payloads a message can carry.
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
	MessageMediaKindGiveaway MessageMediaKind = "giveaway"
	MessageMediaKindInvoice  MessageMediaKind = "invoice"
)

// MessageGiveaway is the immutable launch-card snapshot shown in the boost
// peer. Channels contains the boost peer first followed by any additional
// channels users must join. A results card is a separate lifecycle message.
type MessageGiveaway struct {
	OnlyNewSubscribers bool     `json:"only_new_subscribers,omitempty"`
	WinnersAreVisible  bool     `json:"winners_are_visible,omitempty"`
	Channels           []int64  `json:"channels"`
	CountriesISO2      []string `json:"countries_iso2,omitempty"`
	PrizeDescription   string   `json:"prize_description,omitempty"`
	Quantity           int      `json:"quantity"`
	Stars              int64    `json:"stars"`
	UntilDate          int      `json:"until_date"`
}

// MessageTodoItem is one item in the checklist (id is a unique positive integer within the list, assigned by the client).
type MessageTodoItem struct {
	ID       int             `json:"id"`
	Title    string          `json:"title"`
	Entities []MessageEntity `json:"entities,omitempty"`
}

// MessageTodoCompletion records who completed an item and when.
type MessageTodoCompletion struct {
	ID          int   `json:"id"`
	CompletedBy int64 `json:"completed_by"`
	Date        int   `json:"date"`
}

// MessageTodo is a to-do checklist (messageMediaToDo). append/toggle update the snapshot wholesale via the
// editMessage media-replacement path (same pattern as live location).
type MessageTodo struct {
	OthersCanAppend   bool                    `json:"others_can_append,omitempty"`
	OthersCanComplete bool                    `json:"others_can_complete,omitempty"`
	Title             string                  `json:"title"`
	TitleEntities     []MessageEntity         `json:"title_entities,omitempty"`
	Items             []MessageTodoItem       `json:"items"`
	Completions       []MessageTodoCompletion `json:"completions,omitempty"`
}

// MessageGeoPoint is the geographic coordinate point carried in a message (shared by messageMediaGeo/Venue).
// AccessHash is generated randomly when sending, and the client echoes it back verbatim in the upload.getWebFile map thumbnail request.
type MessageGeoPoint struct {
	Lat            float64 `json:"lat"`
	Long           float64 `json:"long"`
	AccessHash     int64   `json:"access_hash,omitempty"`
	AccuracyRadius int     `json:"accuracy_radius,omitempty"`
}

// MessageVenue is a location with venue information (messageMediaVenue).
type MessageVenue struct {
	Geo       MessageGeoPoint `json:"geo"`
	Title     string          `json:"title"`
	Address   string          `json:"address,omitempty"`
	Provider  string          `json:"provider,omitempty"`
	VenueID   string          `json:"venue_id,omitempty"`
	VenueType string          `json:"venue_type,omitempty"`
}

// MessageDice is an interactive emoji (messageMediaDice). Value is fixed once by the server when the send
// is persisted, and stays unchanged for all viewers and forward snapshots thereafter.
type MessageDice struct {
	Emoticon string `json:"emoticon"`
	Value    int    `json:"value"`
}

// MessageGeoLive is a live location (messageMediaGeoLive). The client computes expiry itself from message.date + Period;
// stopping sharing = editMessage setting Period to the elapsed duration (expiring immediately), with no server background task.
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

// MessageWebPageState distinguishes the three TL forms of a link preview: pending (webPagePending,
// link known but preview not yet asynchronously fetched), done (webPage, a resolved preview card),
// empty (webPageEmpty, resolved but the link has no displayable content).
type MessageWebPageState string

const (
	MessageWebPageStatePending MessageWebPageState = "pending"
	MessageWebPageStateDone    MessageWebPageState = "done"
	MessageWebPageStateEmpty   MessageWebPageState = "empty"
)

// MessageWebPage is a link preview attached as messageMediaWebPage. URL is the stable reference and
// the remaining fields are the snapshot captured by the server at resolution time, so history/difference can replay
// without re-fetching (same pattern as MessageStory). State decides the projection to webPagePending / webPage / webPageEmpty.
//
// ForceLargeMedia/ForceSmallMedia/Manual/Safe are the flag bits of the outer messageMediaWebPage wrapper;
// HasLargeMedia is a flag on the webPage body itself. ID must stay stable across a pending→done
// switch, since the client correlates the placeholder and the resolved result by webPage id.
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
	// ComposeToneEmojiID maps webPageAttributeAiComposeTone, used by TDesktop to render
	// the addstyle tone share card; 0 means an ordinary web page.
	ComposeToneEmojiID int64 `json:"compose_tone_emoji_id,omitempty"`
	// UniqueGift maps webPageAttributeUniqueStarGift (type telegram_nft).
	UniqueGift *UniqueStarGift `json:"unique_gift,omitempty"`
	// CollectionIcons maps webPageAttributeStarGiftCollection (telegram_collection).
	CollectionIcons []Document `json:"collection_icons,omitempty"`
	// AuctionGift and AuctionEndDate map webPageAttributeStarGiftAuction
	// (telegram_auction).
	AuctionGift    *StarGift `json:"auction_gift,omitempty"`
	AuctionEndDate int       `json:"auction_end_date,omitempty"`

	ForceLargeMedia bool `json:"force_large_media,omitempty"`
	ForceSmallMedia bool `json:"force_small_media,omitempty"`
	Manual          bool `json:"manual,omitempty"`
	Safe            bool `json:"safe,omitempty"`
	HasLargeMedia   bool `json:"has_large_media,omitempty"`
}

// MessageServiceActionKind identifies a private-chat service message action.
type MessageServiceActionKind string

const MessageServiceActionGramTransfer MessageServiceActionKind = "gram_transfer"

const (
	MessageServiceActionSuggestProfilePhoto MessageServiceActionKind = "suggest_profile_photo"
	// MessageServiceActionHistoryClear maps messageActionHistoryClear. A private-chat
	// messages.deleteHistory(just_clear) reuses the top box id at the start of the clear and
	// converts it in place into an owner-local service message, so getDialogs/getHistory can
	// still rebuild the chat from the real top message on a cold start.
	MessageServiceActionHistoryClear MessageServiceActionKind = "history_clear"
	// MessageServiceActionPinMessage maps messageActionPinMessage: the service message
	// generated by a non-pm_oneside private-chat pin, with the pinned message referenced via reply_to.
	MessageServiceActionPinMessage MessageServiceActionKind = "pin_message"
	// MessageServiceActionPhoneCall maps messageActionPhoneCall: the call entry
	// persisted after a private-chat call ends (including a missed timeout), with the sender always being the caller.
	MessageServiceActionPhoneCall MessageServiceActionKind = "phone_call"
	// MessageServiceActionConferenceCall maps messageActionConferenceCall:
	// the private-chat invite/status service message of an ad-hoc conference call.
	MessageServiceActionConferenceCall MessageServiceActionKind = "conference_call"
	// MessageServiceActionBotAllowed maps messageActionBotAllowed: the service message
	// left in the bot's private chat after a user authorizes the bot.
	MessageServiceActionBotAllowed MessageServiceActionKind = "bot_allowed"
	// MessageServiceActionWebViewDataSent maps messageActionWebViewDataSent*:
	// the service message left in the private chat after a simple webview passes data back to the bot.
	MessageServiceActionWebViewDataSent MessageServiceActionKind = "web_view_data_sent"
	// MessageServiceActionRequestedPeer maps messageActionRequestedPeer*: after a user
	// responds to the bot's request-peer button, it sends the selected peer to the bot as a recoverable service message.
	MessageServiceActionRequestedPeer MessageServiceActionKind = "requested_peer"
	// MessageServiceActionSetChatTheme maps messageActionSetChatTheme:
	// a change to the chat theme token shared by both sides of a private chat.
	MessageServiceActionSetChatTheme MessageServiceActionKind = "set_chat_theme"
	// MessageServiceActionNoForwardsToggle / Request map the private-chat content-protection
	// state toggle and close request. Chat-level protection cannot be written to an ordinary message's NoForwards field.
	MessageServiceActionNoForwardsToggle  MessageServiceActionKind = "no_forwards_toggle"
	MessageServiceActionNoForwardsRequest MessageServiceActionKind = "no_forwards_request"
	// MessageServiceActionStarGift maps messageActionStarGift: a Star gift was received.
	// The gift snapshot (sticker/star price) is embedded in the action, so the recipient can render the bubble without an extra fetch.
	MessageServiceActionStarGift MessageServiceActionKind = "star_gift"
	// MessageServiceActionGiftStars maps messageActionGiftStars: fiat-purchased
	// Stars credited directly to a friend, distinct from collectible Star Gifts.
	MessageServiceActionGiftStars MessageServiceActionKind = "gift_stars"
	// MessageServiceActionStarGiftUnique maps messageActionStarGiftUnique. The
	// immutable collectible snapshot is carried by the service message so an
	// exact replay/difference never depends on mutable catalog state.
	MessageServiceActionStarGiftUnique        MessageServiceActionKind = "star_gift_unique"
	MessageServiceActionStarGiftOffer         MessageServiceActionKind = "star_gift_offer"
	MessageServiceActionStarGiftOfferDeclined MessageServiceActionKind = "star_gift_offer_declined"
	MessageServiceActionGiftPremium           MessageServiceActionKind = "gift_premium"
	// MessageServiceActionPayment is messageActionPaymentSentMe: a payer settled
	// an invoice of ours.
	MessageServiceActionPayment MessageServiceActionKind = "payment_sent_me"
)

// MessagePhoneCallAction is the protocol-neutral payload of messageActionPhoneCall.
// Reason takes the same values as PhoneCallDiscardReason; Duration is non-zero only after the call is actually established.
type MessagePhoneCallAction struct {
	CallID   int64  `json:"call_id"`
	Reason   string `json:"reason,omitempty"`
	Duration int    `json:"duration,omitempty"`
	Video    bool   `json:"video,omitempty"`
}

// MessageConferenceCallAction is the protocol-neutral payload of messageActionConferenceCall.
type MessageConferenceCallAction struct {
	CallID            int64  `json:"call_id"`
	Missed            bool   `json:"missed,omitempty"`
	Active            bool   `json:"active,omitempty"`
	Video             bool   `json:"video,omitempty"`
	Duration          int    `json:"duration,omitempty"`
	OtherParticipants []Peer `json:"other_participants,omitempty"`
}

// MessageBotAllowedAction is the protocol-neutral payload of messageActionBotAllowed.
type MessageBotAllowedAction struct {
	AttachMenu  bool   `json:"attach_menu,omitempty"`
	FromRequest bool   `json:"from_request,omitempty"`
	Domain      string `json:"domain,omitempty"`
}

const (
	MaxWebViewDataButtonTextLen = MaxBotMenuButtonTextLen
	MaxWebViewDataPayloadLen    = MaxMessageTextLength
)

// MessageWebViewDataAction is the protocol-neutral payload of messageActionWebViewDataSent*.
type MessageWebViewDataAction struct {
	ButtonText string `json:"button_text"`
	Data       string `json:"data,omitempty"`
}

// MessageRequestedPeerAction is the protocol-neutral payload of messageActionRequestedPeer*.
type MessageRequestedPeerAction struct {
	ButtonID int    `json:"button_id"`
	Peers    []Peer `json:"peers"`
	// Details is the immutable, permission-gated snapshot delivered to the bot.
	// It is kept separate from Peers because the sender-side MTProto action only
	// exposes peer identities, while the bot-side/Bot API view may additionally
	// expose the requested name, username, and profile photo.
	Details           []MessageRequestedPeerDetails `json:"details,omitempty"`
	NameRequested     bool                          `json:"name_requested,omitempty"`
	UsernameRequested bool                          `json:"username_requested,omitempty"`
	PhotoRequested    bool                          `json:"photo_requested,omitempty"`
}

type MessageRequestedPeerDetails struct {
	Peer      Peer   `json:"peer"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	Photo     *Photo `json:"photo,omitempty"`
}

// MessageNoForwardsAction is the protocol-neutral payload of the private-chat content-protection service action.
// ExpiresAt is only used for the read-time absolute expiry projection of a request; toggle stays 0.
type MessageNoForwardsAction struct {
	PrevValue bool `json:"prev_value"`
	NewValue  bool `json:"new_value"`
	Expired   bool `json:"expired,omitempty"`
	ExpiresAt int  `json:"expires_at,omitempty"`
}

// MessageServiceAction is the protocol-neutral representation of a private-chat service message action.
type MessageServiceAction struct {
	GramTransfer          *MessageGramTransferAction          `json:"gram_transfer,omitempty"`
	Kind                  MessageServiceActionKind            `json:"kind"`
	Photo                 *Photo                              `json:"photo,omitempty"`
	Call                  *MessagePhoneCallAction             `json:"call,omitempty"`
	ConferenceCall        *MessageConferenceCallAction        `json:"conference_call,omitempty"`
	BotAllowed            *MessageBotAllowedAction            `json:"bot_allowed,omitempty"`
	WebViewData           *MessageWebViewDataAction           `json:"web_view_data,omitempty"`
	RequestedPeer         *MessageRequestedPeerAction         `json:"requested_peer,omitempty"`
	ChatThemeEmoticon     string                              `json:"chat_theme_emoticon,omitempty"`
	NoForwards            *MessageNoForwardsAction            `json:"no_forwards,omitempty"`
	StarGift              *MessageStarGiftAction              `json:"star_gift,omitempty"`
	GiftStars             *MessageGiftStarsAction             `json:"gift_stars,omitempty"`
	StarGiftUnique        *MessageStarGiftUniqueAction        `json:"star_gift_unique,omitempty"`
	StarGiftOffer         *MessageStarGiftOfferAction         `json:"star_gift_offer,omitempty"`
	StarGiftOfferDeclined *MessageStarGiftOfferDeclinedAction `json:"star_gift_offer_declined,omitempty"`
	GiftPremium           *MessageGiftPremiumAction           `json:"gift_premium,omitempty"`
	Payment               *MessagePaymentAction               `json:"payment,omitempty"`
}

// MessagePaymentAction is projected as messageActionPaymentSentMe, which is what
// a bot receives once a payer settles an invoice. It is the only place a bot
// learns the charge id, and therefore the only way it can later refund.
//
// Without it a payment settles but stays invisible to the bot, so refundStarPayment
// would be unreachable: nothing would ever supply telegram_payment_charge_id.
type MessagePaymentAction struct {
	Currency         string `json:"currency"`
	TotalAmount      int64  `json:"total_amount"`
	Payload          string `json:"payload,omitempty"`
	ChargeID         string `json:"charge_id"`
	ProviderChargeID string `json:"provider_charge_id"`
	Title            string `json:"title,omitempty"`
	Description      string `json:"description,omitempty"`
}

// MessageGiftPremiumAction is projected as
// messageActionGiftPremium#48e91302 at the RPC boundary.
type MessageGiftPremiumAction struct {
	Currency string             `json:"currency"`
	Amount   int64              `json:"amount"`
	Days     int                `json:"days"`
	Message  PremiumGiftMessage `json:"message,omitempty"`
}

// MessageGiftStarsAction is the immutable service-message projection. The
// recipient-only BalanceAfter field is not encoded in messageActionGiftStars;
// it lets online push and offline difference attach the matching non-PTS
// updateStarsBalance without querying mutable current state.
type MessageGiftStarsAction struct {
	Currency      string `json:"currency"`
	Amount        int64  `json:"amount"`
	Stars         int64  `json:"stars"`
	TransactionID string `json:"transaction_id,omitempty"`
	BalanceAfter  int64  `json:"balance_after"`
}

// MessageStarGiftAction is the protocol-neutral payload of messageActionStarGift: the embedded gift snapshot (sticker/star price)
// lets the recipient render without an extra fetch. PeerUserID/PeerChannelID is the recipient side; when NameHidden is set the output does not expose from.
type MessageStarGiftAction struct {
	GiftID             int64           `json:"gift_id"`
	Stars              int64           `json:"stars"`
	ConvertStars       int64           `json:"convert_stars,omitempty"`
	Title              string          `json:"title,omitempty"`
	Sticker            *Document       `json:"sticker,omitempty"`
	Message            string          `json:"message,omitempty"`
	MessageEntities    []MessageEntity `json:"message_entities,omitempty"`
	FromUserID         int64           `json:"from_user_id,omitempty"`
	PeerUserID         int64           `json:"peer_user_id,omitempty"`
	PeerChannelID      int64           `json:"peer_channel_id,omitempty"`
	SavedID            int64           `json:"saved_id,omitempty"`
	NameHidden         bool            `json:"name_hidden,omitempty"`
	Saved              bool            `json:"saved,omitempty"`
	Converted          bool            `json:"converted,omitempty"`
	CanUpgrade         bool            `json:"can_upgrade,omitempty"`
	PrepaidUpgrade     bool            `json:"prepaid_upgrade,omitempty"`
	PrepaidUpgradeHash string          `json:"prepaid_upgrade_hash,omitempty"`
	UpgradeSeparate    bool            `json:"upgrade_separate,omitempty"`
	// UpgradePriceStars belongs to the inner StarGift.upgrade_stars field and
	// is the price of a normal paid upgrade. UpgradeStars below belongs to the
	// outer messageActionStarGift and is only the amount already prepaid by the
	// sender. TDesktop uses these two fields to choose the paid vs free flow.
	UpgradePriceStars int64 `json:"upgrade_price_stars,omitempty"`
	UpgradeStars      int64 `json:"upgrade_stars,omitempty"`
	UpgradeMsgID      int   `json:"upgrade_msg_id,omitempty"`
	GiftMsgID         int   `json:"gift_msg_id,omitempty"`
	GiftNum           int   `json:"gift_num,omitempty"`
	AuctionAcquired   bool  `json:"auction_acquired,omitempty"`
	To                Peer  `json:"to,omitempty"`
}

type MessageStarGiftUniqueAction struct {
	Gift                     UniqueStarGift  `json:"gift"`
	FromUserID               int64           `json:"from_user_id,omitempty"`
	Peer                     Peer            `json:"peer"`
	SavedID                  int64           `json:"saved_id,omitempty"`
	Upgrade                  bool            `json:"upgrade,omitempty"`
	Saved                    bool            `json:"saved,omitempty"`
	PrepaidUpgrade           bool            `json:"prepaid_upgrade,omitempty"`
	Transferred              bool            `json:"transferred,omitempty"`
	Refunded                 bool            `json:"refunded,omitempty"`
	Assigned                 bool            `json:"assigned,omitempty"`
	FromOffer                bool            `json:"from_offer,omitempty"`
	Craft                    bool            `json:"craft,omitempty"`
	CanExportAt              int             `json:"can_export_at,omitempty"`
	TransferStars            int64           `json:"transfer_stars,omitempty"`
	ResaleAmount             *StarGiftAmount `json:"resale_amount,omitempty"`
	CanTransferAt            int             `json:"can_transfer_at,omitempty"`
	CanResellAt              int             `json:"can_resell_at,omitempty"`
	DropOriginalDetailsStars int64           `json:"drop_original_details_stars,omitempty"`
	CanCraftAt               int             `json:"can_craft_at,omitempty"`
}

type MessageStarGiftOfferAction struct {
	Gift      UniqueStarGift `json:"gift"`
	Price     StarGiftAmount `json:"price"`
	ExpiresAt int            `json:"expires_at"`
	Accepted  bool           `json:"accepted,omitempty"`
	Declined  bool           `json:"declined,omitempty"`
}

type MessageStarGiftOfferDeclinedAction struct {
	Gift    UniqueStarGift `json:"gift"`
	Price   StarGiftAmount `json:"price"`
	Expired bool           `json:"expired,omitempty"`
}

// MessageMedia is the business representation of a message's media payload (persisted as a JSONB snapshot on the message row).
type MessageMedia struct {
	Kind           MessageMediaKind      `json:"kind"`
	Photo          *Photo                `json:"photo,omitempty"`
	LivePhotoVideo *Document             `json:"live_photo_video,omitempty"`
	Document       *Document             `json:"document,omitempty"`
	Contact        *MessageContact       `json:"contact,omitempty"`
	ServiceAction  *MessageServiceAction `json:"service_action,omitempty"`
	Geo            *MessageGeoPoint      `json:"geo,omitempty"`
	Venue          *MessageVenue         `json:"venue,omitempty"`
	Dice           *MessageDice          `json:"dice,omitempty"`
	Poll           *MessagePoll          `json:"poll,omitempty"`
	GeoLive        *MessageGeoLive       `json:"geo_live,omitempty"`
	Todo           *MessageTodo          `json:"todo,omitempty"`
	Story          *MessageStory         `json:"story,omitempty"`
	WebPage        *MessageWebPage       `json:"web_page,omitempty"`
	Giveaway       *MessageGiveaway      `json:"giveaway,omitempty"`
	Invoice        *PremiumInvoice       `json:"invoice,omitempty"`
	// ProductInvoice is the generic bot sale shape. It is separate from Invoice
	// because a Premium invoice carries a plan, not a product, and the two
	// validate against different rules.
	ProductInvoice *Invoice `json:"product_invoice,omitempty"`
	Spoiler        bool     `json:"spoiler,omitempty"`
	TTLSeconds     int      `json:"ttl_seconds,omitempty"`
	Nopremium      bool     `json:"nopremium,omitempty"`
	Voice          bool     `json:"voice,omitempty"`
	Round          bool     `json:"round,omitempty"`
	Video          bool     `json:"video,omitempty"`
	// InvertMedia maps message.invert_media: the media (typically a link preview) renders above the text.
	// Stored in the media snapshot rather than the message row to avoid a new message-table column; projected to tg.Message.invert_media on read.
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

// IsZero means no media (used to skip an empty snapshot on persist and fall back to MessageMediaEmpty on conversion).
func (m *MessageMedia) IsZero() bool {
	return m == nil || m.Kind == MessageMediaKindNone
}

// HasUnreadPayload reports whether this media participates in the media_unread ("unheard") state.
// Only voice/round have this semantics: the client renders the unheard marker only for them and reports
// readMessageContents; setting it on a photo/document only leaves a dirty state that is never cleared.
func (m *MessageMedia) HasUnreadPayload() bool {
	if m.IsZero() || m.Kind != MessageMediaKindDocument {
		return false
	}
	return m.Voice || m.Round
}

// StickerPack is an emoji→document id mapping entry (messages.stickerSet.packs).
type StickerPack struct {
	Emoticon    string  `json:"emoticon"`
	DocumentIDs []int64 `json:"document_ids"`
}

type StickerKeyword struct {
	DocumentID int64    `json:"document_id"`
	Keywords   []string `json:"keywords"`
}

const (
	// StickerSetSystemKeyEmojiDefaultStatuses is the system-set identifier for inputStickerSetEmojiDefaultStatuses:
	// the "default statuses" main set of the Premium user emoji status picker
	// (shared by messages.getStickerSet and account.getDefaultEmojiStatuses).
	StickerSetSystemKeyEmojiDefaultStatuses = "emoji_default_statuses"
	// StickerSetSystemKeyEmojiDefaultTopicIcons is the system set for default forum topic icons.
	StickerSetSystemKeyEmojiDefaultTopicIcons = "emoji_default_topic_icons"
	// StickerSetSystemKeyPremiumGifts is the system set for premium gifts.
	StickerSetSystemKeyPremiumGifts = "premium_gifts"
	// StickerSetSystemKeyTonGifts is the system set for TON gifts.
	StickerSetSystemKeyTonGifts = "ton_gifts"
)

// StickerSetKind distinguishes sticker set purposes (affects getAllStickers / getEmojiStickers classification).
type StickerSetKind string

const (
	StickerSetKindStickers StickerSetKind = "stickers"
	StickerSetKindEmoji    StickerSetKind = "emoji"
	StickerSetKindMasks    StickerSetKind = "masks"
	// StickerSetKindSystem is a built-in set TDesktop requests via system sets such as InputStickerSetDice/AnimatedEmoji.
	StickerSetKindSystem StickerSetKind = "system"
)

// StickerSet is the metadata of a sticker/custom emoji set + ordered document ids.
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
	// SystemKey is the stable identifier of a TDesktop system set (e.g. "animated_emoji", "dice:🎲"), used for InputStickerSet* routing.
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

// ProfilePhotoRef is the minimal information needed to render an avatar (current profile/fallback/personal photo).
type ProfilePhotoRef struct {
	PhotoID  int64
	DCID     int
	Stripped []byte // photoStrippedSize inline thumbnail, may be empty
	Personal bool   // true means a viewer-private contact avatar
	HasVideo bool   // true means photo.video_sizes is non-empty, requiring output of userProfilePhoto.has_video
}

// StrippedFromSizes extracts the stripped thumbnail bytes from a photo size list (used as the UserProfilePhoto/ChatPhoto placeholder).
func StrippedFromSizes(sizes []PhotoSize) []byte {
	for _, s := range sizes {
		if s.Kind == PhotoSizeKindStripped {
			return s.Bytes
		}
	}
	return nil
}

// PhotoHasVideo derives UserProfilePhoto.has_video from a photo size snapshot.
func PhotoHasVideo(sizes []PhotoSize) bool {
	for _, s := range sizes {
		switch s.Kind {
		case PhotoSizeKindVideo, PhotoSizeKindVideoEmojiMarkup, PhotoSizeKindVideoStickerMarkup:
			return true
		}
	}
	return false
}

// StickerSetRefKind identifies how an InputStickerSet is resolved.
type StickerSetRefKind string

const (
	StickerSetRefByID        StickerSetRefKind = "id"
	StickerSetRefByShortName StickerSetRefKind = "short_name"
	StickerSetRefBySystem    StickerSetRefKind = "system"
)

// StickerSetRef is the sticker set reference the rpc layer converts from tg.InputStickerSet.
type StickerSetRef struct {
	Kind       StickerSetRefKind
	ID         int64
	AccessHash int64
	ShortName  string
	SystemKey  string
}

// AvailableReaction describes one item of messages.getAvailableReactions (real assets are referenced by document id).
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

// AvailableEffect is a message send effect (messages.getAvailableEffects). It references three documents:
// the picker static icon / the effect sticker on the bubble / the full-screen effect animation (the latter two may be 0). It is a global static catalog,
// resident in memory after the server seeds it.
type AvailableEffect struct {
	ID                int64  `json:"id"`
	Emoticon          string `json:"emoticon"`
	StaticIconID      int64  `json:"static_icon_id,omitempty"`
	EffectStickerID   int64  `json:"effect_sticker_id"`
	EffectAnimationID int64  `json:"effect_animation_id,omitempty"`
	PremiumRequired   bool   `json:"premium_required,omitempty"`
	Order             int    `json:"order,omitempty"`
}

// DocumentIDs collects all document ids this effect references (dropping zeros and duplicates).
func (e AvailableEffect) DocumentIDs() []int64 {
	return dedupNonZeroIDs(e.StaticIconID, e.EffectStickerID, e.EffectAnimationID)
}

// DocumentIDs collects all document ids this reaction references (dropping zeros and duplicates, for bulk loading).
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

// dedupNonZeroIDs returns the id list with zeros removed and duplicates dropped (order preserved).
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

type MessageGramTransferAction struct {
	Amount int64 `json:"amount"`
	// PeerAddress is the recipient's wallet address.
	PeerAddress string `json:"peer_address,omitempty"`
	// TransactionID is the ledger transaction id.
	TransactionID string `json:"transaction_id,omitempty"`
	// Comment is the note the sender wrote on the transfer.
	Comment string `json:"comment,omitempty"`
	// CommentEncrypted reports that the sender's note is encrypted: the client
	// holds the key, the server does not, and the note stays empty.
	CommentEncrypted bool `json:"comment_encrypted,omitempty"`
}
