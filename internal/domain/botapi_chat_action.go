package domain

// BotAPIChatAction is a Bot API sendChatAction action, spelled as the official
// wire string.
type BotAPIChatAction string

const (
	BotAPIChatActionTyping          BotAPIChatAction = "typing"
	BotAPIChatActionUploadPhoto     BotAPIChatAction = "upload_photo"
	BotAPIChatActionRecordVideo     BotAPIChatAction = "record_video"
	BotAPIChatActionUploadVideo     BotAPIChatAction = "upload_video"
	BotAPIChatActionRecordVoice     BotAPIChatAction = "record_voice"
	BotAPIChatActionUploadVoice     BotAPIChatAction = "upload_voice"
	BotAPIChatActionUploadDocument  BotAPIChatAction = "upload_document"
	BotAPIChatActionChooseSticker   BotAPIChatAction = "choose_sticker"
	BotAPIChatActionFindLocation    BotAPIChatAction = "find_location"
	BotAPIChatActionRecordVideoNote BotAPIChatAction = "record_video_note"
	BotAPIChatActionUploadVideoNote BotAPIChatAction = "upload_video_note"
)

// Valid reports whether a is one of the official sendChatAction actions.
func (a BotAPIChatAction) Valid() bool {
	switch a {
	case BotAPIChatActionTyping, BotAPIChatActionUploadPhoto, BotAPIChatActionRecordVideo,
		BotAPIChatActionUploadVideo, BotAPIChatActionRecordVoice, BotAPIChatActionUploadVoice,
		BotAPIChatActionUploadDocument, BotAPIChatActionChooseSticker, BotAPIChatActionFindLocation,
		BotAPIChatActionRecordVideoNote, BotAPIChatActionUploadVideoNote:
		return true
	default:
		return false
	}
}
