package rpc

import (
	"strings"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

const mimeApplicationXTGSticker = "application/x-tgsticker"

// 本文件集中 domain media 值对象 → tg.* 的转换；tg.* 只在 rpc 层出现。
// 供 reaction / sticker 资源 RPC 与消息 media 共用。

// tgMessageMedia 把消息 media 快照转成 tg.MessageMediaClass；空载荷回退 MessageMediaEmpty。
func tgMessageMedia(m *domain.MessageMedia) tg.MessageMediaClass {
	if m.IsZero() {
		return &tg.MessageMediaEmpty{}
	}
	switch m.Kind {
	case domain.MessageMediaKindPhoto:
		out := &tg.MessageMediaPhoto{Spoiler: m.Spoiler}
		if m.Photo != nil {
			out.Photo = tgPhoto(*m.Photo)
		}
		if m.TTLSeconds > 0 {
			out.TTLSeconds = m.TTLSeconds
		}
		return out
	case domain.MessageMediaKindDocument:
		nopremium := m.Nopremium
		if m.Document != nil && m.Document.IsSticker() {
			nopremium = true
		}
		out := &tg.MessageMediaDocument{
			Spoiler:   m.Spoiler,
			Nopremium: nopremium,
			Voice:     m.Voice,
			Round:     m.Round,
			Video:     m.Video,
		}
		if m.Document != nil {
			out.Document = tgDocument(*m.Document)
		}
		if m.TTLSeconds > 0 {
			out.TTLSeconds = m.TTLSeconds
		}
		return out
	case domain.MessageMediaKindContact:
		if m.Contact == nil {
			return &tg.MessageMediaEmpty{}
		}
		return &tg.MessageMediaContact{
			PhoneNumber: m.Contact.PhoneNumber,
			FirstName:   m.Contact.FirstName,
			LastName:    m.Contact.LastName,
			Vcard:       m.Contact.Vcard,
			UserID:      m.Contact.UserID,
		}
	case domain.MessageMediaKindGeo:
		if m.Geo == nil {
			return &tg.MessageMediaEmpty{}
		}
		return &tg.MessageMediaGeo{Geo: tgGeoPoint(*m.Geo)}
	case domain.MessageMediaKindVenue:
		if m.Venue == nil {
			return &tg.MessageMediaEmpty{}
		}
		return &tg.MessageMediaVenue{
			Geo:       tgGeoPoint(m.Venue.Geo),
			Title:     m.Venue.Title,
			Address:   m.Venue.Address,
			Provider:  m.Venue.Provider,
			VenueID:   m.Venue.VenueID,
			VenueType: m.Venue.VenueType,
		}
	case domain.MessageMediaKindDice:
		if m.Dice == nil {
			return &tg.MessageMediaEmpty{}
		}
		return &tg.MessageMediaDice{Value: m.Dice.Value, Emoticon: m.Dice.Emoticon}
	case domain.MessageMediaKindPoll:
		if m.Poll == nil {
			return &tg.MessageMediaEmpty{}
		}
		out := &tg.MessageMediaPoll{Poll: tgPoll(*m.Poll), Results: tgPollResults(*m.Poll)}
		if m.Poll.AttachedMedia != nil {
			out.SetAttachedMedia(tgMessageMedia(m.Poll.AttachedMedia))
		}
		return out
	case domain.MessageMediaKindTodo:
		if m.Todo == nil {
			return &tg.MessageMediaEmpty{}
		}
		return tgTodoMedia(*m.Todo)
	case domain.MessageMediaKindGeoLive:
		if m.GeoLive == nil {
			return &tg.MessageMediaEmpty{}
		}
		out := &tg.MessageMediaGeoLive{
			Geo:    tgGeoPoint(m.GeoLive.Geo),
			Period: m.GeoLive.Period,
		}
		if m.GeoLive.Heading > 0 {
			out.SetHeading(m.GeoLive.Heading)
		}
		if m.GeoLive.ProximityNotificationRadius > 0 {
			out.SetProximityNotificationRadius(m.GeoLive.ProximityNotificationRadius)
		}
		return out
	case domain.MessageMediaKindStory:
		if m.Story == nil {
			return &tg.MessageMediaEmpty{}
		}
		peer := tgPeer(m.Story.Peer)
		if peer == nil {
			return &tg.MessageMediaEmpty{}
		}
		out := &tg.MessageMediaStory{
			ViaMention: m.Story.ViaMention,
			Peer:       peer,
			ID:         m.Story.ID,
		}
		if m.Story.Story != nil {
			out.SetStory(tgStoryItem(*m.Story.Story))
		}
		return out
	case domain.MessageMediaKindWebPage:
		if m.WebPage == nil {
			return &tg.MessageMediaEmpty{}
		}
		return tgWebPageMedia(*m.WebPage)
	default:
		return &tg.MessageMediaEmpty{}
	}
}

// tgWebPageMedia 把链接预览快照转成 messageMediaWebPage：外层 wrapper 携带
// force_large/force_small/manual/safe 标志，内层按 state 投影为
// webPagePending / webPage / webPageEmpty。
func tgWebPageMedia(w domain.MessageWebPage) tg.MessageMediaClass {
	return &tg.MessageMediaWebPage{
		ForceLargeMedia: w.ForceLargeMedia,
		ForceSmallMedia: w.ForceSmallMedia,
		Manual:          w.Manual,
		Safe:            w.Safe,
		Webpage:         tgWebPage(w),
	}
}

// tgWebPage 按链接预览快照的 state 投影出对应的 tg.WebPageClass。done 形态必须
// 始终填齐非可选的 id/url/display_url/hash，否则客户端不渲染卡片。
func tgWebPage(w domain.MessageWebPage) tg.WebPageClass {
	switch w.State {
	case domain.MessageWebPageStateDone:
		page := &tg.WebPage{
			ID:            w.ID,
			URL:           w.URL,
			DisplayURL:    w.DisplayURL,
			Hash:          w.Hash,
			HasLargeMedia: w.HasLargeMedia,
		}
		if w.Type != "" {
			page.SetType(w.Type)
		}
		if w.SiteName != "" {
			page.SetSiteName(w.SiteName)
		}
		if w.Title != "" {
			page.SetTitle(w.Title)
		}
		if w.Description != "" {
			page.SetDescription(w.Description)
		}
		if w.Author != "" {
			page.SetAuthor(w.Author)
		}
		if w.Photo != nil {
			if photo := tgPhoto(*w.Photo); photo != nil {
				if _, empty := photo.(*tg.PhotoEmpty); !empty {
					page.SetPhoto(photo)
				}
			}
		}
		if w.ComposeToneEmojiID != 0 {
			page.SetAttributes([]tg.WebPageAttributeClass{
				&tg.WebPageAttributeAiComposeTone{EmojiID: w.ComposeToneEmojiID},
			})
		}
		return page
	case domain.MessageWebPageStateEmpty:
		page := &tg.WebPageEmpty{ID: w.ID}
		if w.URL != "" {
			page.SetURL(w.URL)
		}
		return page
	default: // pending：webPagePending{id,date}，url 可选但客户端需它展示待解析链接。
		page := &tg.WebPagePending{ID: w.ID, Date: w.Date}
		if w.URL != "" {
			page.SetURL(w.URL)
		}
		return page
	}
}

// tgGeoPoint 把 domain 坐标点转成 tg.GeoPoint。
func tgGeoPoint(g domain.MessageGeoPoint) tg.GeoPointClass {
	out := &tg.GeoPoint{Lat: g.Lat, Long: g.Long, AccessHash: g.AccessHash}
	if g.AccuracyRadius > 0 {
		out.SetAccuracyRadius(g.AccuracyRadius)
	}
	return out
}

// tgChatPhoto 由 domain.Channel 反范式头像字段构造 ChatPhoto（频道/群头像缩略）。
func tgChatPhoto(ch domain.Channel) tg.ChatPhotoClass {
	if ch.PhotoID == 0 {
		return &tg.ChatPhotoEmpty{}
	}
	p := &tg.ChatPhoto{PhotoID: ch.PhotoID, DCID: ch.PhotoDCID}
	if len(ch.PhotoStripped) > 0 {
		p.SetStrippedThumb(ch.PhotoStripped)
	}
	return p
}

// tgChannelChatPhotoFull 为 channelFull.chat_photo 构造完整 Photo（合成 a/c 尺寸；
// getFile 按 photo:<id>:<type> 解析，忽略 access_hash，故合成尺寸也可下载）。
func tgChannelChatPhotoFull(ch domain.Channel) tg.PhotoClass {
	if ch.PhotoID == 0 {
		return &tg.PhotoEmpty{}
	}
	photo := &tg.Photo{ID: ch.PhotoID, DCID: ch.PhotoDCID, Sizes: syntheticAvatarSizes()}
	if len(ch.PhotoStripped) > 0 {
		photo.Sizes = append([]tg.PhotoSizeClass{&tg.PhotoStrippedSize{Type: "i", Bytes: ch.PhotoStripped}}, photo.Sizes...)
	}
	return photo
}

func syntheticAvatarSizes() []tg.PhotoSizeClass {
	return []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "a", W: 160, H: 160, Size: 0},
		&tg.PhotoSize{Type: "c", W: 640, H: 640, Size: 0},
	}
}

// tgPhoto 把 domain.Photo 转成 tg.PhotoClass。
func tgPhoto(p domain.Photo) tg.PhotoClass {
	if p.ID == 0 {
		return &tg.PhotoEmpty{}
	}
	sizes := tgPhotoSizes(p.Sizes)
	if len(sizes) == 0 {
		return &tg.PhotoEmpty{}
	}
	return &tg.Photo{
		ID:            p.ID,
		AccessHash:    p.AccessHash,
		FileReference: p.FileReference,
		Date:          p.Date,
		Sizes:         sizes,
		VideoSizes:    tgPhotoVideoSizes(p.Sizes),
		DCID:          p.DCID,
		HasStickers:   p.HasStickers,
	}
}

// tgDocument 把 domain.Document 转成 tg.DocumentClass。
func tgDocument(d domain.Document) tg.DocumentClass {
	if d.ID == 0 {
		return &tg.DocumentEmpty{}
	}
	return &tg.Document{
		ID:            d.ID,
		AccessHash:    d.AccessHash,
		FileReference: d.FileReference,
		Date:          d.Date,
		MimeType:      d.MimeType,
		Size:          d.Size,
		Thumbs:        tgDocumentThumbs(d.MimeType, d.Thumbs),
		DCID:          d.DCID,
		Attributes:    tgDocumentAttributes(d.MimeType, d.Attributes),
	}
}

func tgDocuments(docs []domain.Document) []tg.DocumentClass {
	out := make([]tg.DocumentClass, 0, len(docs))
	for _, d := range docs {
		out = append(out, tgDocument(d))
	}
	return out
}

func tgDocumentThumbs(mimeType string, sizes []domain.PhotoSize) []tg.PhotoSizeClass {
	if len(sizes) == 0 {
		return nil
	}
	out := make([]tg.PhotoSizeClass, 0, len(sizes))
	for _, s := range sizes {
		if isSeedSyntheticTGStickerPreviewThumb(mimeType, s) {
			continue
		}
		if s.Kind == domain.PhotoSizeKindCached && len(s.Bytes) > 0 {
			size := s.Size
			if size == 0 {
				size = len(s.Bytes)
			}
			if s.Type != "" && s.W > 0 && s.H > 0 && size > 0 {
				out = append(out, &tg.PhotoSize{Type: s.Type, W: s.W, H: s.H, Size: size})
			}
			continue
		}
		out = append(out, tgPhotoSize(s))
	}
	return compactPhotoSizeClasses(out)
}

func isSeedSyntheticTGStickerPreviewThumb(mimeType string, s domain.PhotoSize) bool {
	// Older seed imports gave TGS documents without thumbnails a 1x1 transparent
	// "m" PNG. Clients can prefer that unusable preview and render blank stickers.
	return mimeType == mimeApplicationXTGSticker &&
		s.Kind == domain.PhotoSizeKindCached &&
		s.Type == "m" &&
		s.W <= 1 &&
		s.H <= 1 &&
		len(s.Bytes) > 0
}

func tgPhotoSizes(sizes []domain.PhotoSize) []tg.PhotoSizeClass {
	if len(sizes) == 0 {
		return nil
	}
	out := make([]tg.PhotoSizeClass, 0, len(sizes))
	for _, s := range sizes {
		if isPhotoVideoSize(s.Kind) {
			continue
		}
		out = append(out, tgPhotoSize(s))
	}
	return compactPhotoSizeClasses(out)
}

func tgPhotoSize(s domain.PhotoSize) tg.PhotoSizeClass {
	switch s.Kind {
	case domain.PhotoSizeKindDefault:
		return &tg.PhotoSize{Type: s.Type, W: s.W, H: s.H, Size: s.Size}
	case domain.PhotoSizeKindStripped:
		return &tg.PhotoStrippedSize{Type: s.Type, Bytes: s.Bytes}
	case domain.PhotoSizeKindCached:
		return &tg.PhotoCachedSize{Type: s.Type, W: s.W, H: s.H, Bytes: s.Bytes}
	case domain.PhotoSizeKindPath:
		return &tg.PhotoPathSize{Type: s.Type, Bytes: s.Bytes}
	case domain.PhotoSizeKindProgressive:
		return &tg.PhotoSizeProgressive{Type: s.Type, W: s.W, H: s.H, Sizes: s.Sizes}
	default:
		return nil
	}
}

func tgPhotoVideoSizes(sizes []domain.PhotoSize) []tg.VideoSizeClass {
	if len(sizes) == 0 {
		return nil
	}
	out := make([]tg.VideoSizeClass, 0, len(sizes))
	for _, s := range sizes {
		switch s.Kind {
		case domain.PhotoSizeKindVideo:
			v := &tg.VideoSize{Type: s.Type, W: s.W, H: s.H, Size: s.Size}
			if s.VideoStartTs != 0 {
				v.SetVideoStartTs(s.VideoStartTs)
			}
			out = append(out, v)
		case domain.PhotoSizeKindVideoEmojiMarkup:
			out = append(out, &tg.VideoSizeEmojiMarkup{
				EmojiID:          s.EmojiID,
				BackgroundColors: append([]int(nil), s.BackgroundColors...),
			})
		case domain.PhotoSizeKindVideoStickerMarkup:
			out = append(out, &tg.VideoSizeStickerMarkup{
				Stickerset:       tgInputStickerSetFromPhotoSize(s),
				StickerID:        s.StickerID,
				BackgroundColors: append([]int(nil), s.BackgroundColors...),
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func tgInputStickerSetFromPhotoSize(s domain.PhotoSize) tg.InputStickerSetClass {
	if s.StickerSetID != 0 {
		return &tg.InputStickerSetID{ID: s.StickerSetID, AccessHash: s.StickerSetAccessHash}
	}
	if s.StickerSetShortName != "" {
		return &tg.InputStickerSetShortName{ShortName: s.StickerSetShortName}
	}
	if input, ok := tgInputStickerSetFromSystemKey(s.StickerSetSystemKey); ok {
		return input
	}
	return &tg.InputStickerSetEmpty{}
}

func tgInputStickerSetFromSystemKey(systemKey string) (tg.InputStickerSetClass, bool) {
	switch systemKey {
	case "animated_emoji":
		return &tg.InputStickerSetAnimatedEmoji{}, true
	case "animated_emoji_animations":
		return &tg.InputStickerSetAnimatedEmojiAnimations{}, true
	case "emoji_generic_animations":
		return &tg.InputStickerSetEmojiGenericAnimations{}, true
	case domain.StickerSetSystemKeyEmojiDefaultStatuses:
		return &tg.InputStickerSetEmojiDefaultStatuses{}, true
	case domain.StickerSetSystemKeyEmojiDefaultTopicIcons:
		return &tg.InputStickerSetEmojiDefaultTopicIcons{}, true
	case domain.StickerSetSystemKeyPremiumGifts:
		return &tg.InputStickerSetPremiumGifts{}, true
	case domain.StickerSetSystemKeyTonGifts:
		return &tg.InputStickerSetTonGifts{}, true
	default:
		if strings.HasPrefix(systemKey, "dice:") {
			return &tg.InputStickerSetDice{Emoticon: strings.TrimPrefix(systemKey, "dice:")}, true
		}
		return nil, false
	}
}

func isPhotoVideoSize(kind domain.PhotoSizeKind) bool {
	return kind == domain.PhotoSizeKindVideo || kind == domain.PhotoSizeKindVideoEmojiMarkup || kind == domain.PhotoSizeKindVideoStickerMarkup
}

func compactPhotoSizeClasses(in []tg.PhotoSizeClass) []tg.PhotoSizeClass {
	out := in[:0]
	for _, s := range in {
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}

func tgDocumentAttributes(mimeType string, attrs []domain.DocumentAttribute) []tg.DocumentAttributeClass {
	out := make([]tg.DocumentAttributeClass, 0, len(attrs))
	for _, a := range attrs {
		switch a.Kind {
		case domain.DocAttrImageSize:
			out = append(out, &tg.DocumentAttributeImageSize{W: a.W, H: a.H})
		case domain.DocAttrAnimated:
			if mimeType == mimeApplicationXTGSticker {
				continue
			}
			out = append(out, &tg.DocumentAttributeAnimated{})
		case domain.DocAttrSticker:
			out = append(out, &tg.DocumentAttributeSticker{
				Mask:       a.Mask,
				Alt:        a.Alt,
				Stickerset: tgInputStickerSetFromIDs(a.StickerSetID, a.StickerSetAccessHash),
			})
		case domain.DocAttrVideo:
			video := &tg.DocumentAttributeVideo{
				RoundMessage:      a.RoundMessage,
				SupportsStreaming: a.SupportsStreaming,
				Nosound:           a.NoSound,
				Duration:          a.Duration,
				W:                 a.W,
				H:                 a.H,
			}
			if a.VideoCodec != "" {
				video.SetVideoCodec(a.VideoCodec)
			}
			out = append(out, video)
		case domain.DocAttrAudio:
			attr := &tg.DocumentAttributeAudio{
				Voice:     a.Voice,
				Duration:  a.AudioDuration,
				Title:     a.Title,
				Performer: a.Performer,
			}
			if len(a.Waveform) > 0 {
				attr.SetWaveform(a.Waveform)
			}
			out = append(out, attr)
		case domain.DocAttrFilename:
			out = append(out, &tg.DocumentAttributeFilename{FileName: a.FileName})
		case domain.DocAttrCustomEmoji:
			out = append(out, &tg.DocumentAttributeCustomEmoji{
				Free:       a.Free,
				TextColor:  a.TextColor,
				Alt:        a.Alt,
				Stickerset: tgInputStickerSetFromIDs(a.StickerSetID, a.StickerSetAccessHash),
			})
		}
	}
	return out
}

func tgInputStickerSetFromIDs(id, accessHash int64) tg.InputStickerSetClass {
	if id == 0 {
		return &tg.InputStickerSetEmpty{}
	}
	return &tg.InputStickerSetID{ID: id, AccessHash: accessHash}
}

// ---- available reactions ----

// tgAvailableReactions 用真实文档构造 messages.availableReactions；docByID 由 handler 预加载。
func tgAvailableReactions(reactions []domain.AvailableReaction, docByID map[int64]domain.Document, hash int) *tg.MessagesAvailableReactions {
	out := &tg.MessagesAvailableReactions{Hash: hash, Reactions: make([]tg.AvailableReaction, 0, len(reactions))}
	doc := func(id int64) tg.DocumentClass {
		if d, ok := docByID[id]; ok {
			return tgDocument(d)
		}
		return &tg.DocumentEmpty{ID: id}
	}
	for _, r := range reactions {
		ar := tg.AvailableReaction{
			Inactive:          r.Inactive,
			Premium:           r.Premium,
			Reaction:          r.Reaction,
			Title:             r.Title,
			StaticIcon:        doc(r.StaticIconID),
			AppearAnimation:   doc(r.AppearAnimationID),
			SelectAnimation:   doc(r.SelectAnimationID),
			ActivateAnimation: doc(r.ActivateAnimationID),
			EffectAnimation:   doc(r.EffectAnimationID),
		}
		if r.AroundAnimationID != 0 {
			ar.SetAroundAnimation(doc(r.AroundAnimationID))
		}
		if r.CenterIconID != 0 {
			ar.SetCenterIcon(doc(r.CenterIconID))
		}
		out.Reactions = append(out.Reactions, ar)
	}
	return out
}

// reactionDocumentIDs 收集一组 reaction 引用的全部文档 id（用于批量预加载）。
func reactionDocumentIDs(reactions []domain.AvailableReaction) []int64 {
	seen := make(map[int64]struct{})
	out := make([]int64, 0, len(reactions)*7)
	for _, r := range reactions {
		for _, id := range r.DocumentIDs() {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// tgAvailableEffects 构造 messages.availableEffects:effects 引用文档 id,文档对象在
// 独立 Documents 数组(去重),docByID 由 handler 预加载。
func tgAvailableEffects(effects []domain.AvailableEffect, docByID map[int64]domain.Document, hash int) *tg.MessagesAvailableEffects {
	out := &tg.MessagesAvailableEffects{
		Hash:      hash,
		Effects:   make([]tg.AvailableEffect, 0, len(effects)),
		Documents: []tg.DocumentClass{},
	}
	seenDoc := make(map[int64]struct{})
	addDoc := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seenDoc[id]; ok {
			return
		}
		if d, ok := docByID[id]; ok {
			out.Documents = append(out.Documents, tgDocument(d))
			seenDoc[id] = struct{}{}
		}
	}
	for _, e := range effects {
		eff := tg.AvailableEffect{
			ID:              e.ID,
			Emoticon:        e.Emoticon,
			EffectStickerID: e.EffectStickerID,
			PremiumRequired: e.PremiumRequired,
		}
		if e.StaticIconID != 0 {
			eff.SetStaticIconID(e.StaticIconID)
		}
		if e.EffectAnimationID != 0 {
			eff.SetEffectAnimationID(e.EffectAnimationID)
		}
		out.Effects = append(out.Effects, eff)
		addDoc(e.StaticIconID)
		addDoc(e.EffectStickerID)
		addDoc(e.EffectAnimationID)
	}
	return out
}

// effectDocumentIDs 收集一组 effect 引用的全部文档 id（用于批量预加载）。
func effectDocumentIDs(effects []domain.AvailableEffect) []int64 {
	seen := make(map[int64]struct{})
	out := make([]int64, 0, len(effects)*3)
	for _, e := range effects {
		for _, id := range e.DocumentIDs() {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// ---- sticker sets ----

func tgStickerSet(set domain.StickerSet) tg.StickerSet {
	out := tg.StickerSet{
		Archived:   set.Archived,
		Official:   set.Official,
		Masks:      set.Masks,
		Emojis:     set.Emojis,
		TextColor:  set.TextColor,
		Creator:    set.Creator,
		ID:         set.ID,
		AccessHash: set.AccessHash,
		Title:      set.Title,
		ShortName:  set.ShortName,
		Count:      set.Count,
		Hash:       set.Hash,
	}
	if set.Installed {
		date := set.InstalledDate
		if date == 0 {
			date = 1
		}
		out.SetInstalledDate(date)
	}
	if thumbs := tgStickerSetThumbs(set.Thumbs); len(thumbs) > 0 {
		out.SetThumbs(thumbs)
		out.SetThumbDCID(set.ThumbDCID)
		out.SetThumbVersion(set.ThumbVersion)
	}
	if set.ThumbDocumentID != 0 {
		out.SetThumbDocumentID(set.ThumbDocumentID)
	}
	return out
}

func tgStickerSetThumbs(sizes []domain.PhotoSize) []tg.PhotoSizeClass {
	if len(sizes) == 0 {
		return nil
	}
	filtered := make([]domain.PhotoSize, 0, len(sizes))
	for _, s := range sizes {
		if s.Downloadable() {
			continue
		}
		filtered = append(filtered, s)
	}
	return tgPhotoSizes(filtered)
}

func tgStickerSets(sets []domain.StickerSet) []tg.StickerSet {
	out := make([]tg.StickerSet, 0, len(sets))
	for _, s := range sets {
		out = append(out, tgStickerSet(s))
	}
	return out
}

func tgStickerPacks(packs []domain.StickerPack) []tg.StickerPack {
	out := make([]tg.StickerPack, 0, len(packs))
	for _, p := range packs {
		out = append(out, tg.StickerPack{Emoticon: p.Emoticon, Documents: append([]int64(nil), p.DocumentIDs...)})
	}
	return out
}

func tgStickerKeywords(keywords []domain.StickerKeyword) []tg.StickerKeyword {
	out := make([]tg.StickerKeyword, 0, len(keywords))
	for _, kw := range keywords {
		if kw.DocumentID == 0 || len(kw.Keywords) == 0 {
			continue
		}
		out = append(out, tg.StickerKeyword{DocumentID: kw.DocumentID, Keyword: append([]string(nil), kw.Keywords...)})
	}
	return out
}

// tgMessagesStickerSet 构造完整 messages.stickerSet（set + packs + documents）。
func tgMessagesStickerSet(set domain.StickerSet, docs []domain.Document) *tg.MessagesStickerSet {
	return &tg.MessagesStickerSet{
		Set:       tgStickerSet(set),
		Packs:     tgStickerPacks(set.Packs),
		Keywords:  tgStickerKeywords(set.Keywords),
		Documents: tgDocuments(docs),
	}
}

// stickerSetRefFromInput 把 tg.InputStickerSet 转成 domain.StickerSetRef。
func stickerSetRefFromInput(input tg.InputStickerSetClass) (domain.StickerSetRef, bool) {
	switch in := input.(type) {
	case *tg.InputStickerSetID:
		return domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: in.ID, AccessHash: in.AccessHash}, true
	case *tg.InputStickerSetShortName:
		return domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: in.ShortName}, true
	case *tg.InputStickerSetAnimatedEmoji:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: "animated_emoji"}, true
	case *tg.InputStickerSetAnimatedEmojiAnimations:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: "animated_emoji_animations"}, true
	case *tg.InputStickerSetEmojiGenericAnimations:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: "emoji_generic_animations"}, true
	case *tg.InputStickerSetEmojiDefaultStatuses:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: domain.StickerSetSystemKeyEmojiDefaultStatuses}, true
	case *tg.InputStickerSetEmojiChannelDefaultStatuses:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: domain.StickerSetSystemKeyEmojiDefaultStatuses}, true
	case *tg.InputStickerSetEmojiDefaultTopicIcons:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: domain.StickerSetSystemKeyEmojiDefaultTopicIcons}, true
	case *tg.InputStickerSetPremiumGifts:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: domain.StickerSetSystemKeyPremiumGifts}, true
	case *tg.InputStickerSetTonGifts:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: domain.StickerSetSystemKeyTonGifts}, true
	case *tg.InputStickerSetDice:
		return domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: "dice:" + in.Emoticon}, true
	default:
		return domain.StickerSetRef{}, false
	}
}

// mediaCatalogHash 用一组 int64（文档/集合 id）算稳定 hash，供 *NotModified 缓存判定。
func mediaCatalogHash(values []int64) int64 {
	var hash uint64
	for _, v := range values {
		hash ^= uint64(v)
		hash = hash*0x4f25 + uint64(v)
	}
	return int64(hash & 0x7fffffffffffffff)
}
