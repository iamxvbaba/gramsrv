package rpc

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
)

// This file centralizes the tg.* <-> domain conversion for rich text messages
// (richMessage). All three input forms of inputRichMessage -- blocks, HTML and
// Markdown -- are normalized into PageBlock at the RPC boundary; blocks are
// serialized as a TL vector into opaque bytes and stored in domain (see
// domain.MessageRichMessage).

// encodeRichBlocks serializes []tg.PageBlockClass into TL vector bytes using the explicit storage profile.
func encodeRichBlocks(profile tlprofile.Profile, blocks []tg.PageBlockClass) ([]byte, error) {
	var b bin.Buffer
	if err := tlprofile.EncodePageBlockVector(profile, blocks, &b); err != nil {
		return nil, err
	}
	return b.Buf, nil
}

// decodeRichBlocks restores the full PageBlock vector using the exact profile it
// was written with. The caller must provide the profile determined by the persisted
// metadata; retrying with a different profile on failure is not allowed.
func decodeRichBlocks(profile tlprofile.Profile, data []byte) ([]tg.PageBlockClass, error) {
	if len(data) == 0 {
		return nil, nil
	}
	b := &bin.Buffer{Buf: append([]byte(nil), data...)}
	return tlprofile.DecodePageBlockVector(profile, b, tlprofile.Limits{})
}

// richMessageMediaRefs is the media closure referenced by one PageBlock graph.
// IDs retain first-reference order so every projection is deterministic.
type richMessageMediaRefs struct {
	photoIDs    []int64
	documentIDs []int64
	photos      map[int64]struct{}
	documents   map[int64]struct{}
}

func collectRichMessageMediaRefs(blocks []tg.PageBlockClass) (richMessageMediaRefs, error) {
	refs := richMessageMediaRefs{
		photos:    make(map[int64]struct{}),
		documents: make(map[int64]struct{}),
	}
	if err := refs.collectBlocks(blocks); err != nil {
		return richMessageMediaRefs{}, err
	}
	return refs, nil
}

func (r *richMessageMediaRefs) addPhoto(id int64, required bool) error {
	if id == 0 {
		if required {
			return photoInvalidErr()
		}
		return nil
	}
	if _, ok := r.photos[id]; ok {
		return nil
	}
	r.photos[id] = struct{}{}
	r.photoIDs = append(r.photoIDs, id)
	return nil
}

func (r *richMessageMediaRefs) addDocument(id int64) error {
	if id == 0 {
		return mediaInvalidErr()
	}
	if _, ok := r.documents[id]; ok {
		return nil
	}
	r.documents[id] = struct{}{}
	r.documentIDs = append(r.documentIDs, id)
	return nil
}

func (r *richMessageMediaRefs) collectBlocks(blocks []tg.PageBlockClass) error {
	for _, block := range blocks {
		switch value := block.(type) {
		case *tg.PageBlockPhoto:
			if err := r.addPhoto(value.PhotoID, true); err != nil {
				return err
			}
		case *tg.PageBlockVideo:
			if err := r.addDocument(value.VideoID); err != nil {
				return err
			}
		case *tg.PageBlockAudio:
			if err := r.addDocument(value.AudioID); err != nil {
				return err
			}
		case *tg.PageBlockEmbed:
			if id, ok := value.GetPosterPhotoID(); ok {
				if err := r.addPhoto(id, false); err != nil {
					return err
				}
			}
		case *tg.PageBlockEmbedPost:
			if err := r.addPhoto(value.AuthorPhotoID, false); err != nil {
				return err
			}
			if err := r.collectBlocks(value.Blocks); err != nil {
				return err
			}
		case *tg.PageBlockRelatedArticles:
			for i := range value.Articles {
				if id, ok := value.Articles[i].GetPhotoID(); ok {
					if err := r.addPhoto(id, false); err != nil {
						return err
					}
				}
			}
		case *tg.PageBlockList:
			for _, item := range value.Items {
				if item, ok := item.(*tg.PageListItemBlocks); ok {
					if err := r.collectBlocks(item.Blocks); err != nil {
						return err
					}
				}
			}
		case *tg.PageBlockOrderedList:
			for _, item := range value.Items {
				if item, ok := item.(*tg.PageListOrderedItemBlocks); ok {
					if err := r.collectBlocks(item.Blocks); err != nil {
						return err
					}
				}
			}
		case *tg.PageBlockCover:
			if err := r.collectBlocks([]tg.PageBlockClass{value.Cover}); err != nil {
				return err
			}
		case *tg.PageBlockCollage:
			if err := r.collectBlocks(value.Items); err != nil {
				return err
			}
		case *tg.PageBlockSlideshow:
			if err := r.collectBlocks(value.Items); err != nil {
				return err
			}
		case *tg.PageBlockDetails:
			if err := r.collectBlocks(value.Blocks); err != nil {
				return err
			}
		case *tg.PageBlockBlockquoteBlocks:
			if err := r.collectBlocks(value.Blocks); err != nil {
				return err
			}
		}
	}
	return nil
}

type richMessagePhotoBatchProvider interface {
	GetPhotos(context.Context, []int64) ([]domain.Photo, error)
}

func (r *Router) resolveRichMessagePhotos(ctx context.Context, ids []int64) ([]domain.Photo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	resolved := make(map[int64]domain.Photo, len(ids))
	if batch, ok := r.deps.Files.(richMessagePhotoBatchProvider); ok {
		photos, err := batch.GetPhotos(ctx, ids)
		if err != nil {
			return nil, internalErr()
		}
		for _, photo := range photos {
			resolved[photo.ID] = photo
		}
	} else {
		for _, id := range ids {
			photo, found, err := r.deps.Files.GetPhoto(ctx, id)
			if err != nil {
				return nil, internalErr()
			}
			if found {
				resolved[id] = photo
			}
		}
	}
	out := make([]domain.Photo, 0, len(ids))
	for _, id := range ids {
		photo, ok := resolved[id]
		if !ok || photo.ID != id || len(photo.Sizes) == 0 {
			return nil, photoInvalidErr()
		}
		out = append(out, photo)
	}
	return out, nil
}

func (r *Router) resolveRichMessageDocuments(ctx context.Context, ids []int64) ([]domain.Document, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	documents, err := r.deps.Files.GetDocuments(ctx, ids)
	if err != nil {
		return nil, internalErr()
	}
	resolved := make(map[int64]domain.Document, len(documents))
	for _, document := range documents {
		resolved[document.ID] = document
	}
	out := make([]domain.Document, 0, len(ids))
	for _, id := range ids {
		document, ok := resolved[id]
		if !ok || document.ID != id {
			return nil, mediaInvalidErr()
		}
		out = append(out, document)
	}
	return out, nil
}

func normalizeRichBlocksForClients(blocks []tg.PageBlockClass) {
	for _, block := range blocks {
		normalizeRichBlockForClients(block)
	}
}

func normalizeRichBlockForClients(block tg.PageBlockClass) {
	switch b := block.(type) {
	case *tg.PageBlockList:
		for _, item := range b.Items {
			if item, ok := item.(*tg.PageListItemBlocks); ok {
				normalizeRichBlocksForClients(item.Blocks)
			}
		}
	case *tg.PageBlockCover:
		normalizeRichBlockForClients(b.Cover)
	case *tg.PageBlockEmbedPost:
		normalizeRichBlocksForClients(b.Blocks)
	case *tg.PageBlockCollage:
		normalizeRichBlocksForClients(b.Items)
	case *tg.PageBlockSlideshow:
		normalizeRichBlocksForClients(b.Items)
	case *tg.PageBlockOrderedList:
		normalizeOrderedListForClients(b)
	case *tg.PageBlockDetails:
		normalizeRichBlocksForClients(b.Blocks)
	case *tg.PageBlockBlockquoteBlocks:
		normalizeRichBlocksForClients(b.Blocks)
	}
}

func normalizeOrderedListForClients(list *tg.PageBlockOrderedList) {
	if list == nil {
		return
	}
	reversed := list.Reversed || list.Flags.Has(2)
	current := 1
	if list.Flags.Has(0) || list.Start != 0 {
		current = list.Start
	} else if reversed {
		current = len(list.Items)
	}
	step := 1
	if reversed {
		step = -1
	}
	for _, item := range list.Items {
		value := current
		switch i := item.(type) {
		case *tg.PageListOrderedItemText:
			if v, ok := i.GetValue(); ok || i.Value != 0 {
				value = v
				if !ok {
					value = i.Value
				}
			}
			if num, ok := i.GetNum(); !ok || num == "" {
				i.SetNum(strconv.Itoa(value))
			}
		case *tg.PageListOrderedItemBlocks:
			if v, ok := i.GetValue(); ok || i.Value != 0 {
				value = v
				if !ok {
					value = i.Value
				}
			}
			if num, ok := i.GetNum(); !ok || num == "" {
				i.SetNum(strconv.Itoa(value))
			}
			normalizeRichBlocksForClients(i.Blocks)
		}
		current = value + step
	}
}

// domainRichMessageFromInput parses the inbound tg.InputRichMessageClass into a
// domain snapshot: HTML/Markdown is first parsed server-side into PageBlock, then
// shares limit validation, serialization and Bot API output projection with the
// blocks form; embedded photos/documents reuse the same media resolution as
// sendMedia. A nil return means there is no rich text payload.
func (r *Router) domainRichMessageFromInput(ctx context.Context, input tg.InputRichMessageClass) (*domain.MessageRichMessage, error) {
	if input == nil {
		return nil, nil
	}
	var (
		in           *tg.InputRichMessage
		sourceParsed bool
	)
	switch value := input.(type) {
	case *tg.InputRichMessage:
		in = value
	case *tg.InputRichMessageHTML:
		if value == nil || value.HTML == "" || len(value.Files) != 0 {
			return nil, richMessageInvalidErr()
		}
		blocks, err := parseBotAPIRichHTML(value.HTML)
		if err != nil {
			return nil, err
		}
		in = &tg.InputRichMessage{Rtl: value.Rtl, Noautolink: value.Noautolink, Blocks: blocks}
		sourceParsed = true
	case *tg.InputRichMessageMarkdown:
		if value == nil || value.Markdown == "" || len(value.Files) != 0 {
			return nil, richMessageInvalidErr()
		}
		blocks, err := parseBotAPIRichMarkdown(value.Markdown)
		if err != nil {
			return nil, err
		}
		in = &tg.InputRichMessage{Rtl: value.Rtl, Noautolink: value.Noautolink, Blocks: blocks}
		sourceParsed = true
	default:
		return nil, richMessageInvalidErr()
	}
	if len(in.Blocks) == 0 {
		if len(in.Photos) == 0 && len(in.Documents) == 0 {
			return nil, nil
		}
		return nil, richMessageInvalidErr()
	}
	if err := validateRichMessageBlocks(in.Blocks); err != nil {
		return nil, err
	}
	for _, photo := range in.Photos {
		if _, ok := inputPhotoID(photo); !ok {
			return nil, photoInvalidErr()
		}
	}
	for _, document := range in.Documents {
		if _, ok := inputDocumentID(document); !ok {
			return nil, mediaInvalidErr()
		}
	}
	refs, err := collectRichMessageMediaRefs(in.Blocks)
	if err != nil {
		return nil, err
	}
	if (len(refs.photoIDs) > 0 || len(refs.documentIDs) > 0) && r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	normalizeRichBlocksForClients(in.Blocks)
	blocks, err := encodeRichBlocks(tlprofile.ProfileCanonical, in.Blocks)
	if err != nil {
		return nil, err
	}
	rich := &domain.MessageRichMessage{
		Rtl:         in.Rtl,
		BlocksLayer: int(tlprofile.ProfileCanonical),
		Blocks:      blocks,
	}
	projection, projectionErr := botAPIRichMessageProjection(in.Blocks, in.Rtl)
	if projectionErr != nil && sourceParsed {
		return nil, richMessageInvalidErr()
	}
	if projectionErr == nil {
		rich.BotAPIProjection = projection
	}
	rich.Photos, err = r.resolveRichMessagePhotos(ctx, refs.photoIDs)
	if err != nil {
		return nil, err
	}
	rich.Documents, err = r.resolveRichMessageDocuments(ctx, refs.documentIDs)
	if err != nil {
		return nil, err
	}
	if rich.IsZero() {
		return nil, nil
	}
	return rich, nil
}

// tgRichMessage projects the domain rich text snapshot into tg.RichMessage
// (deserialize blocks + project embedded media via tgPhoto/tgDocument). Returns
// (nil, err) for an empty payload or a blocks decode failure.
func tgRichMessage(m *domain.MessageRichMessage) (*tg.RichMessage, error) {
	if m.IsZero() {
		return nil, nil
	}
	layer := m.EffectiveBlocksLayer()
	profile, ok := tlprofile.ResolveProfile(layer)
	if !ok {
		return nil, fmt.Errorf("stored rich_message blocks layer %d is unavailable", layer)
	}
	blocks, err := decodeRichBlocks(profile, m.Blocks)
	if err != nil {
		return nil, fmt.Errorf("decode stored rich_message blocks at layer %d: %w", layer, err)
	}
	out := &tg.RichMessage{
		Rtl:       m.Rtl,
		Part:      m.Part,
		Blocks:    blocks,
		Photos:    make([]tg.PhotoClass, 0, len(m.Photos)),
		Documents: make([]tg.DocumentClass, 0, len(m.Documents)),
	}
	for _, p := range m.Photos {
		out.Photos = append(out.Photos, tgPhoto(p))
	}
	for _, d := range m.Documents {
		out.Documents = append(out.Documents, tgDocument(d))
	}
	return out, nil
}

var richMessageProjectionFailureCount atomic.Uint64

// optionalTGRichMessage projects an optional extension without allowing one
// malformed persisted snapshot to terminate the RPC worker or server process.
// Known historical formats are decoded exactly above. Truly invalid data is
// omitted from the base message and compatibility-traced with logarithmic
// sampling so a repeatedly requested row cannot create an unbounded log storm.
func optionalTGRichMessage(scope string, id int, m *domain.MessageRichMessage) *tg.RichMessage {
	out, err := tgRichMessage(m)
	if err != nil {
		count := richMessageProjectionFailureCount.Add(1)
		if count <= 10 || count&(count-1) == 0 {
			log.Printf("rich_message compatibility trace: scope=%s id=%d blocks_layer=%d failures=%d error=%q",
				scope, id, m.EffectiveBlocksLayer(), count, err)
		}
		return nil
	}
	return out
}
