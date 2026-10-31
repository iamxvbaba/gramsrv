package domain

// MediaCategory is the base category of a shared-media tab. A message can belong to several categories at once
// (e.g. an image with a link = Photo + URL). It is stored in the media index table's category column (SMALLINT).
//
// Only "base categories" are stored here; the client's composite filters (PhotoVideo / RoundVoice) are mapped at query time to
// the union of several base categories (see the callers of MediaCategoriesForFilter).
type MediaCategory int16

const (
	MediaCategoryNone       MediaCategory = 0
	MediaCategoryPhoto      MediaCategory = 1 // media.kind=photo
	MediaCategoryVideo      MediaCategory = 2 // document has the video attribute and is not round
	MediaCategoryGif        MediaCategory = 3 // document has the animated attribute
	MediaCategoryFile       MediaCategory = 4 // generic document (no video/audio/animated/sticker attributes)
	MediaCategoryMusic      MediaCategory = 5 // document has the audio attribute and voice=false
	MediaCategoryVoice      MediaCategory = 6 // document has the audio attribute and voice=true
	MediaCategoryRoundVideo MediaCategory = 7 // document has the video attribute and round_message=true (video message)
	MediaCategoryURL        MediaCategory = 8 // text has a url/text_url/email entity or messageMediaWebPage
	MediaCategoryPoll       MediaCategory = 9 // media.kind=poll
	// MediaCategoryGeoLive is the internal index category for messages.getRecentLocations.
	// It does not map to an ordinary shared-media filter; it is only used to read live-location messages in a bounded way per peer.
	MediaCategoryGeoLive MediaCategory = 10 // media.kind=geo_live
)

// MediaCategoryCounts is the exact counts aggregated by base category from the shared-media index.
type MediaCategoryCounts map[MediaCategory]int

// CountAny returns the count for the union of the given base categories. Current composite filters consist only of disjoint categories
// (PhotoVideo=Photo+Video, RoundVoice=Voice+RoundVideo), so they can be summed directly.
func (c MediaCategoryCounts) CountAny(categories []MediaCategory) int {
	if len(categories) == 0 {
		return 0
	}
	seen := make(map[MediaCategory]struct{}, len(categories))
	total := 0
	for _, category := range categories {
		if category == MediaCategoryNone {
			continue
		}
		if _, ok := seen[category]; ok {
			continue
		}
		seen[category] = struct{}{}
		total += c[category]
	}
	return total
}

// MediaSearchRequest is the input of a shared-media tab paginated query (the media-filter branch of messages.search).
// Categories is the union of base categories the tab maps to (PhotoVideo→[Photo,Video], RoundVoice→[Voice,RoundVideo]).
// OffsetID locates the first strictly older message; a negative AddOffset fetches toward newer items (possibly including the cursor itself).
// MaxID/MinID and MaxDate/MinDate are all open intervals; counts ignore the pagination offset.
type MediaSearchRequest struct {
	Categories     []MediaCategory
	Query          string
	SenderUserID   int64
	MinDate        int
	MaxDate        int
	TopMsgID       int
	SavedPeer      Peer
	SavedReactions []MessageReaction
	OffsetID       int
	AddOffset      int
	Limit          int
	MaxID          int
	MinID          int
	KnownCount     int
	HasKnownCount  bool
}

// ClassifyMediaCategories returns every shared-media category a message belongs to (possibly empty: no media and no link,
// or payloads like stickers/geo/contacts that belong to no media tab). It is the single source of truth for media classification: the write path uses it
// to maintain the index, and migration backfill must match its semantics.
func ClassifyMediaCategories(media *MessageMedia, entities []MessageEntity) []MediaCategory {
	cats := make([]MediaCategory, 0, 2)
	add := func(category MediaCategory) {
		if category == MediaCategoryNone {
			return
		}
		for _, existing := range cats {
			if existing == category {
				return
			}
		}
		cats = append(cats, category)
	}
	if media != nil {
		switch media.Kind {
		case MessageMediaKindPhoto:
			add(MediaCategoryPhoto)
		case MessageMediaKindDocument:
			if c, ok := classifyDocumentCategory(media.Document); ok {
				add(c)
			}
		case MessageMediaKindPoll:
			add(MediaCategoryPoll)
		case MessageMediaKindGeoLive:
			add(MediaCategoryGeoLive)
		case MessageMediaKindWebPage:
			add(MediaCategoryURL)
		}
	}
	if hasURLEntity(entities) {
		add(MediaCategoryURL)
	}
	return cats
}

// classifyDocumentCategory decides which media category a document falls into based on TL DocumentAttribute.
// The client's tab UI decides using TL attributes together with the GIFv MIME. Priority: sticker (belongs to no tab) >
// canonical animated MP4 (GIF) > audio (music/voice) > video (video/round) > generic file.
func classifyDocumentCategory(doc *Document) (MediaCategory, bool) {
	if doc == nil {
		return MediaCategoryNone, false
	}
	var (
		hasSticker  bool
		hasAnimated bool
		hasAudio    bool
		audioVoice  bool
		hasVideo    bool
		videoRound  bool
	)
	for _, a := range doc.Attributes {
		switch a.Kind {
		case DocAttrSticker, DocAttrCustomEmoji:
			hasSticker = true
		case DocAttrAnimated:
			hasAnimated = true
		case DocAttrAudio:
			hasAudio = true
			if a.Voice {
				audioVoice = true
			}
		case DocAttrVideo:
			hasVideo = true
			if a.RoundMessage {
				videoRound = true
			}
		}
	}
	switch {
	case hasSticker:
		return MediaCategoryNone, false // stickers/custom emoji do not appear in shared media
	case hasAnimated && doc.IsGif():
		return MediaCategoryGif, true
	case hasAudio:
		if audioVoice {
			return MediaCategoryVoice, true
		}
		return MediaCategoryMusic, true
	case hasVideo:
		if videoRound {
			return MediaCategoryRoundVideo, true
		}
		return MediaCategoryVideo, true
	default:
		return MediaCategoryFile, true
	}
}

func hasURLEntity(entities []MessageEntity) bool {
	for _, e := range entities {
		if e.Type == MessageEntityURL || e.Type == MessageEntityTextURL || e.Type == MessageEntityEmail {
			return true
		}
	}
	return false
}
