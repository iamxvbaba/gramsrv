package rpc

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/iamxvbaba/td/tg"
)

func botAPIRichMessageProjection(blocks []tg.PageBlockClass, rtl bool) ([]byte, error) {
	projected, err := botAPIRichBlocks(blocks)
	if err != nil {
		return nil, err
	}
	if len(projected) == 0 {
		return nil, richMessageInvalidErr()
	}
	out := map[string]any{"blocks": projected}
	if rtl {
		out["is_rtl"] = true
	}
	return json.Marshal(out)
}

func botAPIRichBlocks(blocks []tg.PageBlockClass) ([]any, error) {
	out := make([]any, 0, len(blocks))
	for _, block := range blocks {
		projected, err := botAPIRichBlock(block)
		if err != nil {
			return nil, err
		}
		if projected != nil {
			out = append(out, projected)
		}
	}
	return out, nil
}

func botAPIRichBlock(block tg.PageBlockClass) (map[string]any, error) {
	textBlock := func(kind string, text tg.RichTextClass) (map[string]any, error) {
		value, err := botAPIRichText(text)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": kind, "text": value}, nil
	}
	heading := func(size int, text tg.RichTextClass) (map[string]any, error) {
		value, err := botAPIRichText(text)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "heading", "text": value, "size": size}, nil
	}
	switch value := block.(type) {
	case *tg.PageBlockParagraph:
		return textBlock("paragraph", value.Text)
	case *tg.PageBlockTitle:
		return heading(1, value.Text)
	case *tg.PageBlockSubtitle:
		return heading(2, value.Text)
	case *tg.PageBlockHeader:
		return heading(2, value.Text)
	case *tg.PageBlockSubheader:
		return heading(3, value.Text)
	case *tg.PageBlockKicker:
		return heading(6, value.Text)
	case *tg.PageBlockHeading1:
		return heading(1, value.Text)
	case *tg.PageBlockHeading2:
		return heading(2, value.Text)
	case *tg.PageBlockHeading3:
		return heading(3, value.Text)
	case *tg.PageBlockHeading4:
		return heading(4, value.Text)
	case *tg.PageBlockHeading5:
		return heading(5, value.Text)
	case *tg.PageBlockHeading6:
		return heading(6, value.Text)
	case *tg.PageBlockPreformatted:
		out, err := textBlock("pre", value.Text)
		if err == nil && value.Language != "" {
			out["language"] = value.Language
		}
		return out, err
	case *tg.PageBlockFooter:
		return textBlock("footer", value.Text)
	case *tg.PageBlockDivider:
		return map[string]any{"type": "divider"}, nil
	case *tg.PageBlockMath:
		return map[string]any{"type": "mathematical_expression", "expression": value.Source}, nil
	case *tg.PageBlockAnchor:
		return map[string]any{"type": "anchor", "name": value.Name}, nil
	case *tg.PageBlockDetails:
		blocks, err := botAPIRichBlocks(value.Blocks)
		if err != nil {
			return nil, err
		}
		summary, err := botAPIRichText(value.Title)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"type": "details", "summary": summary, "blocks": blocks}
		if value.Open {
			out["is_open"] = true
		}
		return out, nil
	case *tg.PageBlockBlockquote:
		text, err := botAPIRichText(value.Text)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"type": "blockquote", "blocks": []any{map[string]any{"type": "paragraph", "text": text}}}
		if !botAPIRichTextEmpty(value.Caption) {
			credit, err := botAPIRichText(value.Caption)
			if err != nil {
				return nil, err
			}
			out["credit"] = credit
		}
		return out, nil
	case *tg.PageBlockBlockquoteBlocks:
		blocks, err := botAPIRichBlocks(value.Blocks)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"type": "blockquote", "blocks": blocks}
		if !botAPIRichTextEmpty(value.Caption) {
			credit, err := botAPIRichText(value.Caption)
			if err != nil {
				return nil, err
			}
			out["credit"] = credit
		}
		return out, nil
	case *tg.PageBlockPullquote:
		out, err := textBlock("pullquote", value.Text)
		if err != nil {
			return nil, err
		}
		if !botAPIRichTextEmpty(value.Caption) {
			credit, err := botAPIRichText(value.Caption)
			if err != nil {
				return nil, err
			}
			out["credit"] = credit
		}
		return out, nil
	case *tg.PageBlockList:
		return botAPIUnorderedRichList(value)
	case *tg.PageBlockOrderedList:
		return botAPIOrderedRichList(value)
	case *tg.PageBlockTable:
		return botAPIRichTable(value)
	case *tg.PageBlockCollage:
		return botAPIRichBlockCollection("collage", value.Items, value.Caption)
	case *tg.PageBlockSlideshow:
		return botAPIRichBlockCollection("slideshow", value.Items, value.Caption)
	case *tg.PageBlockCover:
		return botAPIRichBlock(value.Cover)
	case *tg.PageBlockThinking:
		return textBlock("thinking", value.Text)
	default:
		return nil, errors.New("RICH_MESSAGE_PROJECTION_UNSUPPORTED")
	}
}

func botAPIUnorderedRichList(list *tg.PageBlockList) (map[string]any, error) {
	items := make([]any, 0, len(list.Items))
	for _, raw := range list.Items {
		item := map[string]any{"label": "•"}
		switch value := raw.(type) {
		case *tg.PageListItemText:
			text, err := botAPIRichText(value.Text)
			if err != nil {
				return nil, err
			}
			item["blocks"] = []any{map[string]any{"type": "paragraph", "text": text}}
			if value.Checkbox {
				item["has_checkbox"] = true
				if value.Checked {
					item["is_checked"] = true
				}
			}
		case *tg.PageListItemBlocks:
			blocks, err := botAPIRichBlocks(value.Blocks)
			if err != nil {
				return nil, err
			}
			item["blocks"] = blocks
		default:
			return nil, errors.New("RICH_MESSAGE_PROJECTION_UNSUPPORTED")
		}
		items = append(items, item)
	}
	return map[string]any{"type": "list", "items": items}, nil
}

func botAPIOrderedRichList(list *tg.PageBlockOrderedList) (map[string]any, error) {
	items := make([]any, 0, len(list.Items))
	for index, raw := range list.Items {
		item := map[string]any{"label": strconv.Itoa(index + 1)}
		switch value := raw.(type) {
		case *tg.PageListOrderedItemText:
			text, err := botAPIRichText(value.Text)
			if err != nil {
				return nil, err
			}
			item["blocks"] = []any{map[string]any{"type": "paragraph", "text": text}}
			botAPIFillOrderedListItem(item, value.Num, value.Value, value.Type, value.Checkbox, value.Checked)
		case *tg.PageListOrderedItemBlocks:
			blocks, err := botAPIRichBlocks(value.Blocks)
			if err != nil {
				return nil, err
			}
			item["blocks"] = blocks
			botAPIFillOrderedListItem(item, value.Num, value.Value, value.Type, value.Checkbox, value.Checked)
		default:
			return nil, errors.New("RICH_MESSAGE_PROJECTION_UNSUPPORTED")
		}
		items = append(items, item)
	}
	return map[string]any{"type": "list", "items": items}, nil
}

func botAPIFillOrderedListItem(item map[string]any, label string, value int, kind string, checkbox, checked bool) {
	if label != "" {
		item["label"] = label
	}
	if value != 0 {
		item["value"] = value
	}
	if kind != "" {
		item["type"] = kind
	}
	if checkbox {
		item["has_checkbox"] = true
		if checked {
			item["is_checked"] = true
		}
	}
}

func botAPIRichTable(table *tg.PageBlockTable) (map[string]any, error) {
	rows := make([]any, 0, len(table.Rows))
	for _, row := range table.Rows {
		cells := make([]any, 0, len(row.Cells))
		for _, cell := range row.Cells {
			item := map[string]any{"align": "left", "valign": "top"}
			if !botAPIRichTextEmpty(cell.Text) {
				text, err := botAPIRichText(cell.Text)
				if err != nil {
					return nil, err
				}
				item["text"] = text
			}
			if cell.Header {
				item["is_header"] = true
			}
			if cell.Colspan > 1 {
				item["colspan"] = cell.Colspan
			}
			if cell.Rowspan > 1 {
				item["rowspan"] = cell.Rowspan
			}
			if cell.AlignCenter {
				item["align"] = "center"
			} else if cell.AlignRight {
				item["align"] = "right"
			}
			if cell.ValignMiddle {
				item["valign"] = "middle"
			} else if cell.ValignBottom {
				item["valign"] = "bottom"
			}
			cells = append(cells, item)
		}
		rows = append(rows, cells)
	}
	out := map[string]any{"type": "table", "cells": rows}
	if table.Bordered {
		out["is_bordered"] = true
	}
	if table.Striped {
		out["is_striped"] = true
	}
	if !botAPIRichTextEmpty(table.Title) {
		caption, err := botAPIRichText(table.Title)
		if err != nil {
			return nil, err
		}
		out["caption"] = caption
	}
	return out, nil
}

func botAPIRichBlockCollection(kind string, blocks []tg.PageBlockClass, caption tg.PageCaption) (map[string]any, error) {
	items, err := botAPIRichBlocks(blocks)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"type": kind, "blocks": items}
	if !botAPIRichTextEmpty(caption.Text) || !botAPIRichTextEmpty(caption.Credit) {
		projected := map[string]any{}
		if !botAPIRichTextEmpty(caption.Text) {
			projected["text"], err = botAPIRichText(caption.Text)
		}
		if err == nil && !botAPIRichTextEmpty(caption.Credit) {
			projected["credit"], err = botAPIRichText(caption.Credit)
		}
		if err != nil {
			return nil, err
		}
		out["caption"] = projected
	}
	return out, nil
}

func botAPIRichText(text tg.RichTextClass) (any, error) {
	wrapped := func(kind string, child tg.RichTextClass) (any, error) {
		value, err := botAPIRichText(child)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": kind, "text": value}, nil
	}
	valued := func(kind, field, value string, child tg.RichTextClass) (any, error) {
		out, err := wrapped(kind, child)
		if err != nil {
			return nil, err
		}
		out.(map[string]any)[field] = value
		return out, nil
	}
	switch value := text.(type) {
	case nil, *tg.TextEmpty:
		return "", nil
	case *tg.TextPlain:
		return value.Text, nil
	case *tg.TextConcat:
		items := make([]any, 0, len(value.Texts))
		for _, child := range value.Texts {
			item, err := botAPIRichText(child)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	case *tg.TextBold:
		return wrapped("bold", value.Text)
	case *tg.TextItalic:
		return wrapped("italic", value.Text)
	case *tg.TextUnderline:
		return wrapped("underline", value.Text)
	case *tg.TextStrike:
		return wrapped("strikethrough", value.Text)
	case *tg.TextSpoiler:
		return wrapped("spoiler", value.Text)
	case *tg.TextFixed:
		return wrapped("code", value.Text)
	case *tg.TextSubscript:
		return wrapped("subscript", value.Text)
	case *tg.TextSuperscript:
		return wrapped("superscript", value.Text)
	case *tg.TextMarked:
		return wrapped("marked", value.Text)
	case *tg.TextDate:
		out, err := wrapped("date_time", value.Text)
		if err != nil {
			return nil, err
		}
		item := out.(map[string]any)
		item["unix_time"] = value.Date
		item["date_time_format"] = botAPIRichDateFormat(value)
		return item, nil
	case *tg.TextCustomEmoji:
		return map[string]any{"type": "custom_emoji", "custom_emoji_id": strconv.FormatInt(value.DocumentID, 10), "alternative_text": value.Alt}, nil
	case *tg.TextMath:
		return map[string]any{"type": "mathematical_expression", "expression": value.Source}, nil
	case *tg.TextURL:
		if strings.HasPrefix(value.URL, "#") {
			return valued("anchor_link", "anchor_name", strings.TrimPrefix(value.URL, "#"), value.Text)
		}
		return valued("url", "url", value.URL, value.Text)
	case *tg.TextEmail:
		return valued("email_address", "email_address", value.Email, value.Text)
	case *tg.TextPhone:
		return valued("phone_number", "phone_number", value.Phone, value.Text)
	case *tg.TextBankCard:
		return valued("bank_card_number", "bank_card_number", botAPIRichPlainText(value.Text), value.Text)
	case *tg.TextMention:
		return valued("mention", "username", strings.TrimPrefix(botAPIRichPlainText(value.Text), "@"), value.Text)
	case *tg.TextHashtag:
		return valued("hashtag", "hashtag", strings.TrimPrefix(botAPIRichPlainText(value.Text), "#"), value.Text)
	case *tg.TextCashtag:
		return valued("cashtag", "cashtag", strings.TrimPrefix(botAPIRichPlainText(value.Text), "$"), value.Text)
	case *tg.TextBotCommand:
		return valued("bot_command", "bot_command", botAPIRichPlainText(value.Text), value.Text)
	case *tg.TextAutoURL:
		return valued("url", "url", botAPIRichPlainText(value.Text), value.Text)
	case *tg.TextAutoEmail:
		return valued("email_address", "email_address", botAPIRichPlainText(value.Text), value.Text)
	case *tg.TextAutoPhone:
		return valued("phone_number", "phone_number", botAPIRichPlainText(value.Text), value.Text)
	case *tg.TextMentionName:
		out, err := wrapped("text_mention", value.Text)
		if err != nil {
			return nil, err
		}
		out.(map[string]any)["user"] = map[string]any{"id": value.UserID, "is_bot": false, "first_name": "User " + strconv.FormatInt(value.UserID, 10)}
		return out, nil
	case *tg.TextAnchor:
		anchor := map[string]any{"type": "anchor", "name": value.Name}
		if botAPIRichTextEmpty(value.Text) {
			return anchor, nil
		}
		inner, err := botAPIRichText(value.Text)
		if err != nil {
			return nil, err
		}
		return []any{anchor, inner}, nil
	default:
		return nil, errors.New("RICH_MESSAGE_PROJECTION_UNSUPPORTED")
	}
}

func botAPIRichDateFormat(date *tg.TextDate) string {
	if date.Relative {
		return "r"
	}
	var out strings.Builder
	if date.ShortTime {
		out.WriteByte('t')
	}
	if date.LongTime {
		out.WriteByte('T')
	}
	if date.ShortDate {
		out.WriteByte('d')
	}
	if date.LongDate {
		out.WriteByte('D')
	}
	if date.DayOfWeek {
		out.WriteByte('w')
	}
	return out.String()
}

func botAPIRichTextEmpty(text tg.RichTextClass) bool {
	return text == nil || botAPIRichPlainText(text) == ""
}

func botAPIRichPlainText(text tg.RichTextClass) string {
	var out strings.Builder
	var walk func(tg.RichTextClass)
	walk = func(value tg.RichTextClass) {
		switch value := value.(type) {
		case *tg.TextPlain:
			out.WriteString(value.Text)
		case *tg.TextConcat:
			for _, child := range value.Texts {
				walk(child)
			}
		case *tg.TextBold:
			walk(value.Text)
		case *tg.TextItalic:
			walk(value.Text)
		case *tg.TextUnderline:
			walk(value.Text)
		case *tg.TextStrike:
			walk(value.Text)
		case *tg.TextFixed:
			walk(value.Text)
		case *tg.TextSubscript:
			walk(value.Text)
		case *tg.TextSuperscript:
			walk(value.Text)
		case *tg.TextMarked:
			walk(value.Text)
		case *tg.TextSpoiler:
			walk(value.Text)
		case *tg.TextURL:
			walk(value.Text)
		case *tg.TextEmail:
			walk(value.Text)
		case *tg.TextPhone:
			walk(value.Text)
		case *tg.TextAnchor:
			walk(value.Text)
		case *tg.TextMentionName:
			walk(value.Text)
		case *tg.TextDate:
			walk(value.Text)
		case *tg.TextCustomEmoji:
			out.WriteString(value.Alt)
		}
	}
	walk(text)
	return out.String()
}
