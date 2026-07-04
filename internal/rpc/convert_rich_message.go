package rpc

import (
	"context"
	"strconv"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// 本文件集中 Layer 227 富文本消息（richMessage）的 tg.* ↔ domain 转换。
// Phase 1：仅支持 inputRichMessage（blocks 形态）；HTML/Markdown 变体（需服务端解析为
// PageBlock）尚未实现，直接拒绝。blocks 以 TL 向量序列化为不透明字节存 domain（详见
// domain.MessageRichMessage）。

// encodeRichBlocks 把 []tg.PageBlockClass 序列化为 TL 向量字节（含 vector 头）。
func encodeRichBlocks(blocks []tg.PageBlockClass) ([]byte, error) {
	var b bin.Buffer
	b.PutVectorHeader(len(blocks))
	for _, blk := range blocks {
		if blk == nil {
			return nil, mediaInvalidErr()
		}
		if err := blk.Encode(&b); err != nil {
			return nil, err
		}
	}
	return b.Buf, nil
}

// decodeRichBlocks 把 encodeRichBlocks 产生的字节还原为 []tg.PageBlockClass。
func decodeRichBlocks(data []byte) ([]tg.PageBlockClass, error) {
	if len(data) == 0 {
		return nil, nil
	}
	b := &bin.Buffer{Buf: append([]byte(nil), data...)}
	n, err := b.VectorHeader()
	if err != nil {
		return nil, err
	}
	out := make([]tg.PageBlockClass, 0, n)
	for i := 0; i < n; i++ {
		blk, err := tg.DecodePageBlock(b)
		if err != nil {
			return nil, err
		}
		out = append(out, blk)
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

// domainRichMessageFromInput 把入站 tg.InputRichMessageClass 解析为 domain 快照：
// 序列化 blocks + 按 id 解析内嵌 photos/documents（复用 sendMedia 同款媒体解析）。
// 返回 nil 表示无富文本载荷。Phase 1 仅认 *tg.InputRichMessage。
func (r *Router) domainRichMessageFromInput(ctx context.Context, input tg.InputRichMessageClass) (*domain.MessageRichMessage, error) {
	if input == nil {
		return nil, nil
	}
	in, ok := input.(*tg.InputRichMessage)
	if !ok {
		// Phase 1：HTML/Markdown 变体需服务端解析为 PageBlock，尚未支持。
		return nil, mediaInvalidErr()
	}
	if len(in.Blocks) == 0 {
		if len(in.Photos) == 0 && len(in.Documents) == 0 {
			return nil, nil
		}
		return nil, mediaInvalidErr()
	}
	if (len(in.Photos) > 0 || len(in.Documents) > 0) && r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	normalizeRichBlocksForClients(in.Blocks)
	blocks, err := encodeRichBlocks(in.Blocks)
	if err != nil {
		return nil, err
	}
	rich := &domain.MessageRichMessage{
		Rtl:    in.Rtl,
		Blocks: blocks,
	}
	for _, p := range in.Photos {
		id, ok := inputPhotoID(p)
		if !ok {
			return nil, photoInvalidErr()
		}
		photo, found, err := r.deps.Files.GetPhoto(ctx, id)
		if err != nil {
			return nil, internalErr()
		}
		if !found {
			return nil, photoInvalidErr()
		}
		rich.Photos = append(rich.Photos, photo)
	}
	for _, d := range in.Documents {
		id, ok := inputDocumentID(d)
		if !ok {
			return nil, mediaInvalidErr()
		}
		doc, found, err := r.deps.Files.GetDocument(ctx, id)
		if err != nil {
			return nil, internalErr()
		}
		if !found {
			return nil, mediaInvalidErr()
		}
		rich.Documents = append(rich.Documents, doc)
	}
	if rich.IsZero() {
		return nil, nil
	}
	return rich, nil
}

// tgRichMessage 把 domain 富文本快照投影为 tg.RichMessage（反序列化 blocks + 复用
// tgPhoto/tgDocument 投影内嵌媒体）。空载荷或 blocks 解码失败返回 (nil, err)。
func tgRichMessage(m *domain.MessageRichMessage) (*tg.RichMessage, error) {
	if m.IsZero() {
		return nil, nil
	}
	blocks, err := decodeRichBlocks(m.Blocks)
	if err != nil {
		return nil, err
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

func mustTGRichMessage(m *domain.MessageRichMessage) *tg.RichMessage {
	out, err := tgRichMessage(m)
	if err != nil {
		panic("invalid stored rich_message: " + err.Error())
	}
	return out
}
