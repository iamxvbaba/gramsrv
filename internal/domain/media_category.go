package domain

// MediaCategory 是共享媒体标签页的基础类别。一条消息可同时属于多个类别
// （例如带链接的图片 = Photo + URL）。落库为媒体索引表的 category 列（SMALLINT）。
//
// 这里只存"基础类别";客户端的复合过滤器（PhotoVideo / RoundVoice）在查询期映射为
// 多个基础类别的并集（见 MediaCategoriesForFilter 的调用方）。
type MediaCategory int16

const (
	MediaCategoryNone       MediaCategory = 0
	MediaCategoryPhoto      MediaCategory = 1 // media.kind=photo
	MediaCategoryVideo      MediaCategory = 2 // document 含 video 属性且非 round
	MediaCategoryGif        MediaCategory = 3 // document 含 animated 属性
	MediaCategoryFile       MediaCategory = 4 // 通用文档（无 video/audio/animated/sticker 属性）
	MediaCategoryMusic      MediaCategory = 5 // document 含 audio 属性且 voice=false
	MediaCategoryVoice      MediaCategory = 6 // document 含 audio 属性且 voice=true
	MediaCategoryRoundVideo MediaCategory = 7 // document 含 video 属性且 round_message=true（视频消息）
	MediaCategoryURL        MediaCategory = 8 // 文本含 url/text_url/email 实体或 messageMediaWebPage
	MediaCategoryPoll       MediaCategory = 9 // media.kind=poll
)

// MediaCategoryCounts 是共享媒体索引按基础类别聚合出的精确计数。
type MediaCategoryCounts map[MediaCategory]int

// CountAny 返回给定基础类别并集的计数。当前复合 filter 只由互斥类别组成
// (PhotoVideo=Photo+Video, RoundVoice=Voice+RoundVideo)，因此可直接相加。
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

// MediaSearchRequest 是共享媒体标签页分页查询的入参（messages.search 媒体过滤分支）。
// Categories 是该标签页映射到的基础类别并集（PhotoVideo→[Photo,Video]、RoundVoice→[Voice,RoundVideo]）。
// 分页对齐历史语义：OffsetID 为游标（返回 id 严格小于它）、AddOffset 为额外偏移、MaxID/MinID 为闭区间。
type MediaSearchRequest struct {
	Categories    []MediaCategory
	OffsetID      int
	AddOffset     int
	Limit         int
	MaxID         int
	MinID         int
	KnownCount    int
	HasKnownCount bool
}

// ClassifyMediaCategories 返回一条消息所属的全部共享媒体类别（可为空：无媒体且无链接，
// 或贴纸/地理/联系人等不进任何媒体标签页的载荷）。是媒体索引的唯一分类真值，写路径据此
// 维护索引、迁移回填须与其语义一致。
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
		case MessageMediaKindWebPage:
			add(MediaCategoryURL)
		}
	}
	if hasURLEntity(entities) {
		add(MediaCategoryURL)
	}
	return cats
}

// classifyDocumentCategory 按 TL DocumentAttribute 判定一个文档落入哪个媒体类别。
// 客户端的标签页 UI 以 TL 属性和 GIFv MIME 共同判定。优先级：sticker（不入任何标签页）>
// canonical animated MP4 (GIF) > audio(music/voice) > video(video/round) > 通用文件。
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
		return MediaCategoryNone, false // 贴纸/自定义 emoji 不出现在共享媒体
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
