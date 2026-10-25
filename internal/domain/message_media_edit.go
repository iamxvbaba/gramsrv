package domain

import "errors"

var ErrMessageMediaEditInvalid = errors.New("message media replacement invalid")

// ValidateMessageMediaReplacement protects both ordinary and scheduled edits.
// Albums keep their content family; photo/video albums allow either visual kind.
func ValidateMessageMediaReplacement(previous, next *MessageMedia, groupedID int64, forwarded bool) error {
	oldKind, newKind := editableMediaFamily(previous), editableMediaFamily(next)
	if forwarded || oldKind == "" || newKind == "" ||
		(groupedID != 0 && oldKind != newKind) {
		return ErrMessageMediaEditInvalid
	}
	return nil
}

func editableMediaFamily(media *MessageMedia) string {
	if media == nil || media.TTLSeconds != 0 {
		return ""
	}
	if media.Kind == MessageMediaKindPhoto && media.Photo != nil {
		return "visual"
	}
	if media.Kind != MessageMediaKindDocument || media.Document == nil {
		return ""
	}
	kind := "document"
	for _, attr := range media.Document.Attributes {
		switch attr.Kind {
		case DocAttrSticker, DocAttrCustomEmoji:
			return ""
		case DocAttrVideo:
			if attr.RoundMessage {
				return ""
			}
			if kind != "animation" {
				kind = "visual"
			}
		case DocAttrAudio:
			if attr.Voice {
				return ""
			}
			kind = "audio"
		case DocAttrAnimated:
			kind = "animation"
		}
	}
	return kind
}
