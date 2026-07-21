package rpc

import (
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
)

const (
	richMessageLengthLimit  = 32768
	richMessageMaxBlocks    = 500
	richMessageMaxDepth     = 16
	richMessageMaxMedia     = 50
	richMessageMaxTableCols = 20
)

type richMessageMetrics struct {
	textLength int
	blocks     int
	depth      int
	media      int
	tableCols  int
}

func validateRichMessageBlocks(blocks []tg.PageBlockClass) error {
	metrics := richMessageMetrics{}
	collectRichMessageBlockMetrics(blocks, 1, &metrics)
	if metrics.textLength > richMessageLengthLimit || metrics.blocks > richMessageMaxBlocks ||
		metrics.depth > richMessageMaxDepth || metrics.media > richMessageMaxMedia || metrics.tableCols > richMessageMaxTableCols {
		return richMessageTooLongErr()
	}
	if metrics.blocks == 0 || metrics.textLength == 0 && metrics.media == 0 {
		return richMessageInvalidErr()
	}
	return nil
}

func collectRichMessageBlockMetrics(blocks []tg.PageBlockClass, depth int, metrics *richMessageMetrics) {
	if depth > metrics.depth {
		metrics.depth = depth
	}
	for _, block := range blocks {
		metrics.blocks++
		switch value := block.(type) {
		case *tg.PageBlockTitle:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockSubtitle:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockHeader:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockSubheader:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockKicker:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockParagraph:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockPreformatted:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockFooter:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockHeading1:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockHeading2:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockHeading3:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockHeading4:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockHeading5:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockHeading6:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockMath:
			metrics.textLength += utf16StringLength(value.Source)
		case *tg.PageBlockThinking:
			metrics.textLength += richTextUTF16Length(value.Text)
		case *tg.PageBlockAuthorDate:
			metrics.textLength += richTextUTF16Length(value.Author)
		case *tg.PageBlockBlockquote:
			metrics.textLength += richTextUTF16Length(value.Text) + richTextUTF16Length(value.Caption)
		case *tg.PageBlockPullquote:
			metrics.textLength += richTextUTF16Length(value.Text) + richTextUTF16Length(value.Caption)
		case *tg.PageBlockBlockquoteBlocks:
			metrics.textLength += richTextUTF16Length(value.Caption)
			collectRichMessageBlockMetrics(value.Blocks, depth+1, metrics)
		case *tg.PageBlockDetails:
			metrics.textLength += richTextUTF16Length(value.Title)
			collectRichMessageBlockMetrics(value.Blocks, depth+1, metrics)
		case *tg.PageBlockList:
			for _, item := range value.Items {
				switch item := item.(type) {
				case *tg.PageListItemText:
					metrics.textLength += richTextUTF16Length(item.Text)
				case *tg.PageListItemBlocks:
					collectRichMessageBlockMetrics(item.Blocks, depth+1, metrics)
				}
			}
		case *tg.PageBlockOrderedList:
			for _, item := range value.Items {
				switch item := item.(type) {
				case *tg.PageListOrderedItemText:
					metrics.textLength += richTextUTF16Length(item.Text)
				case *tg.PageListOrderedItemBlocks:
					collectRichMessageBlockMetrics(item.Blocks, depth+1, metrics)
				}
			}
		case *tg.PageBlockTable:
			metrics.textLength += richTextUTF16Length(value.Title)
			for _, row := range value.Rows {
				columns := 0
				for _, cell := range row.Cells {
					metrics.textLength += richTextUTF16Length(cell.Text)
					if cell.Colspan > 1 {
						columns += cell.Colspan
					} else {
						columns++
					}
				}
				if columns > metrics.tableCols {
					metrics.tableCols = columns
				}
			}
		case *tg.PageBlockCollage:
			metrics.textLength += richTextUTF16Length(value.Caption.Text) + richTextUTF16Length(value.Caption.Credit)
			collectRichMessageBlockMetrics(value.Items, depth+1, metrics)
		case *tg.PageBlockSlideshow:
			metrics.textLength += richTextUTF16Length(value.Caption.Text) + richTextUTF16Length(value.Caption.Credit)
			collectRichMessageBlockMetrics(value.Items, depth+1, metrics)
		case *tg.PageBlockCover:
			collectRichMessageBlockMetrics([]tg.PageBlockClass{value.Cover}, depth+1, metrics)
		case *tg.PageBlockEmbedPost:
			collectRichMessageBlockMetrics(value.Blocks, depth+1, metrics)
		case *tg.PageBlockPhoto, *tg.PageBlockVideo, *tg.PageBlockAudio:
			metrics.media++
		}
	}
}

func richTextUTF16Length(text tg.RichTextClass) int {
	switch value := text.(type) {
	case nil, *tg.TextEmpty:
		return 0
	case *tg.TextPlain:
		return utf16StringLength(value.Text)
	case *tg.TextConcat:
		total := 0
		for _, child := range value.Texts {
			total += richTextUTF16Length(child)
		}
		return total
	case *tg.TextBold:
		return richTextUTF16Length(value.Text)
	case *tg.TextItalic:
		return richTextUTF16Length(value.Text)
	case *tg.TextUnderline:
		return richTextUTF16Length(value.Text)
	case *tg.TextStrike:
		return richTextUTF16Length(value.Text)
	case *tg.TextFixed:
		return richTextUTF16Length(value.Text)
	case *tg.TextSubscript:
		return richTextUTF16Length(value.Text)
	case *tg.TextSuperscript:
		return richTextUTF16Length(value.Text)
	case *tg.TextMarked:
		return richTextUTF16Length(value.Text)
	case *tg.TextSpoiler:
		return richTextUTF16Length(value.Text)
	case *tg.TextURL:
		return richTextUTF16Length(value.Text)
	case *tg.TextMention:
		return richTextUTF16Length(value.Text)
	case *tg.TextHashtag:
		return richTextUTF16Length(value.Text)
	case *tg.TextBotCommand:
		return richTextUTF16Length(value.Text)
	case *tg.TextCashtag:
		return richTextUTF16Length(value.Text)
	case *tg.TextAutoURL:
		return richTextUTF16Length(value.Text)
	case *tg.TextAutoEmail:
		return richTextUTF16Length(value.Text)
	case *tg.TextAutoPhone:
		return richTextUTF16Length(value.Text)
	case *tg.TextBankCard:
		return richTextUTF16Length(value.Text)
	case *tg.TextEmail:
		return richTextUTF16Length(value.Text)
	case *tg.TextPhone:
		return richTextUTF16Length(value.Text)
	case *tg.TextAnchor:
		return richTextUTF16Length(value.Text)
	case *tg.TextMentionName:
		return richTextUTF16Length(value.Text)
	case *tg.TextDate:
		return richTextUTF16Length(value.Text)
	case *tg.TextCustomEmoji:
		return utf16StringLength(value.Alt)
	case *tg.TextMath:
		return utf16StringLength(value.Source)
	default:
		return 0
	}
}

func utf16StringLength(value string) int {
	length := 0
	for _, r := range value {
		length++
		if r > utf8.RuneSelf && r > 0xffff {
			length++
		}
	}
	return length
}
