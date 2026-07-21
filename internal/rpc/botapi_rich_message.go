package rpc

import (
	"bytes"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	richbuilder "github.com/iamxvbaba/td/telegram/message/rich"
	"github.com/iamxvbaba/td/tg"
	"golang.org/x/net/html"

	"telesrv/internal/domain"
)

const (
	botAPIRichSentinelScheme = "telesrv-rich"
	botAPIRichDateMaxUnix    = int64(1<<31 - 1)
)

type botAPIHTMLTableSpec struct {
	bordered bool
	striped  bool
	cells    []botAPIHTMLTableCellSpec
}

type botAPIHTMLTableCellSpec struct {
	align  string
	valign string
}

func tgInputRichMessageFromBotAPI(input domain.BotAPIRichMessageInput) (tg.InputRichMessageClass, error) {
	if input.SourceCount() != 1 || len(input.BlocksJSON) != 0 {
		return nil, richMessageInvalidErr()
	}
	if len(input.MediaJSON) != 0 {
		return nil, richMessageMediaUnsupportedErr()
	}
	if input.HTML != "" {
		return &tg.InputRichMessageHTML{
			Rtl: input.RTL, Noautolink: input.SkipEntityDetection, HTML: input.HTML,
		}, nil
	}
	if input.Markdown != "" {
		return &tg.InputRichMessageMarkdown{
			Rtl: input.RTL, Noautolink: input.SkipEntityDetection, Markdown: input.Markdown,
		}, nil
	}
	return nil, richMessageInvalidErr()
}

func parseBotAPIRichHTML(source string) ([]tg.PageBlockClass, error) {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return nil, richMessageInvalidErr()
	}
	tables := make([]botAPIHTMLTableSpec, 0)
	var transform func(*html.Node) error
	transform = func(node *html.Node) error {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "img", "video", "audio", "tg-map", "tg-collage", "tg-slideshow":
				// The current local blob backend cannot materialize an arbitrary
				// rich HTML media URL atomically. Fail explicitly so Bedolaga's
				// documented one-shot no-logo retry is used instead of losing media.
				return webpageMediaEmptyErr()
			case "tg-time":
				unixTime, err := strconv.ParseInt(htmlNodeAttr(node, "unix"), 10, 64)
				if err != nil || unixTime <= 0 || unixTime > botAPIRichDateMaxUnix {
					return richMessageDateInvalidErr()
				}
				format := htmlNodeAttr(node, "format")
				if _, ok := botAPIRichDateFlags(format); !ok {
					return richMessageDateInvalidErr()
				}
				node.Data = "a"
				node.Attr = []html.Attribute{{Key: "href", Val: fmt.Sprintf("%s://time?unix=%d&format=%s", botAPIRichSentinelScheme, unixTime, url.QueryEscape(format))}}
			case "footer":
				node.Data = "p"
				node.Attr = nil
				anchor := &html.Node{Type: html.ElementNode, Data: "a", Attr: []html.Attribute{{Key: "href", Val: botAPIRichSentinelScheme + "://footer"}}}
				for child := node.FirstChild; child != nil; {
					next := child.NextSibling
					node.RemoveChild(child)
					anchor.AppendChild(child)
					child = next
				}
				node.AppendChild(anchor)
			case "table":
				tables = append(tables, botAPIHTMLTableSpecFromNode(node))
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := transform(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := transform(doc); err != nil {
		return nil, err
	}
	var normalized bytes.Buffer
	if err := html.Render(&normalized, doc); err != nil {
		return nil, richMessageInvalidErr()
	}
	blocks, err := richbuilder.ParseHTML(strings.NewReader(normalized.String()))
	if err != nil {
		return nil, richMessageInvalidErr()
	}
	postProcessBotAPIRichHTML(blocks, tables)
	return blocks, nil
}

func parseBotAPIRichMarkdown(source string) ([]tg.PageBlockClass, error) {
	blocks, err := richbuilder.ParseMarkdown(strings.NewReader(source))
	if err != nil {
		return nil, richMessageInvalidErr()
	}
	return blocks, nil
}

func botAPIHTMLTableSpecFromNode(table *html.Node) botAPIHTMLTableSpec {
	spec := botAPIHTMLTableSpec{bordered: htmlNodeHasAttr(table, "bordered"), striped: htmlNodeHasAttr(table, "striped")}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if child.Type == html.ElementNode && (child.Data == "td" || child.Data == "th") {
				spec.cells = append(spec.cells, botAPIHTMLTableCellSpec{
					align: strings.ToLower(htmlNodeAttr(child, "align")), valign: strings.ToLower(htmlNodeAttr(child, "valign")),
				})
			}
			walk(child)
		}
	}
	walk(table)
	return spec
}

func postProcessBotAPIRichHTML(blocks []tg.PageBlockClass, tables []botAPIHTMLTableSpec) {
	tableIndex := 0
	var visit func([]tg.PageBlockClass)
	visit = func(items []tg.PageBlockClass) {
		for index, block := range items {
			switch value := block.(type) {
			case *tg.PageBlockParagraph:
				if footer, ok := botAPIRichFooterText(value.Text); ok {
					items[index] = &tg.PageBlockFooter{Text: postProcessBotAPIRichText(footer)}
				} else {
					value.Text = postProcessBotAPIRichText(value.Text)
				}
			case *tg.PageBlockHeading1:
				value.Text = postProcessBotAPIRichText(value.Text)
			case *tg.PageBlockHeading2:
				value.Text = postProcessBotAPIRichText(value.Text)
			case *tg.PageBlockHeading3:
				value.Text = postProcessBotAPIRichText(value.Text)
			case *tg.PageBlockHeading4:
				value.Text = postProcessBotAPIRichText(value.Text)
			case *tg.PageBlockHeading5:
				value.Text = postProcessBotAPIRichText(value.Text)
			case *tg.PageBlockHeading6:
				value.Text = postProcessBotAPIRichText(value.Text)
			case *tg.PageBlockFooter:
				value.Text = postProcessBotAPIRichText(value.Text)
			case *tg.PageBlockPreformatted:
				value.Text = postProcessBotAPIRichText(value.Text)
			case *tg.PageBlockBlockquote:
				value.Text = postProcessBotAPIRichText(value.Text)
				value.Caption = postProcessBotAPIRichText(value.Caption)
			case *tg.PageBlockBlockquoteBlocks:
				value.Caption = postProcessBotAPIRichText(value.Caption)
				visit(value.Blocks)
			case *tg.PageBlockDetails:
				value.Title = postProcessBotAPIRichText(value.Title)
				visit(value.Blocks)
			case *tg.PageBlockTable:
				value.Title = postProcessBotAPIRichText(value.Title)
				if tableIndex < len(tables) {
					spec := tables[tableIndex]
					tableIndex++
					value.Bordered, value.Striped = spec.bordered, spec.striped
					cellIndex := 0
					for rowIndex := range value.Rows {
						for columnIndex := range value.Rows[rowIndex].Cells {
							cell := &value.Rows[rowIndex].Cells[columnIndex]
							cell.Text = postProcessBotAPIRichText(cell.Text)
							if cellIndex < len(spec.cells) {
								cellSpec := spec.cells[cellIndex]
								cell.AlignCenter = cellSpec.align == "center"
								cell.AlignRight = cellSpec.align == "right"
								cell.ValignMiddle = cellSpec.valign == "middle"
								cell.ValignBottom = cellSpec.valign == "bottom"
							}
							cellIndex++
						}
					}
				}
			}
		}
	}
	visit(blocks)
}

func postProcessBotAPIRichText(text tg.RichTextClass) tg.RichTextClass {
	switch value := text.(type) {
	case *tg.TextConcat:
		for i := range value.Texts {
			value.Texts[i] = postProcessBotAPIRichText(value.Texts[i])
		}
	case *tg.TextBold:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextItalic:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextUnderline:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextStrike:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextFixed:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextSubscript:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextSuperscript:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextMarked:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextSpoiler:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextURL:
		parsed, err := url.Parse(value.URL)
		if err == nil && parsed.Scheme == botAPIRichSentinelScheme && parsed.Host == "time" {
			unixTime, unixErr := strconv.ParseInt(parsed.Query().Get("unix"), 10, 32)
			flags, ok := botAPIRichDateFlags(parsed.Query().Get("format"))
			if unixErr == nil && ok {
				return richbuilder.Date(postProcessBotAPIRichText(value.Text), int(unixTime), flags)
			}
		}
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextEmail:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextPhone:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextAnchor:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextMentionName:
		value.Text = postProcessBotAPIRichText(value.Text)
	case *tg.TextDate:
		value.Text = postProcessBotAPIRichText(value.Text)
	}
	return text
}

func botAPIRichFooterText(text tg.RichTextClass) (tg.RichTextClass, bool) {
	link, ok := text.(*tg.TextURL)
	if !ok || link.URL != botAPIRichSentinelScheme+"://footer" {
		return nil, false
	}
	return link.Text, true
}

func botAPIRichDateFlags(format string) (richbuilder.DateFlags, bool) {
	if format == "r" || format == "R" {
		return richbuilder.DateFlags{Relative: true}, true
	}
	var flags richbuilder.DateFlags
	if format == "" {
		return flags, false
	}
	for _, value := range format {
		switch value {
		case 't':
			flags.ShortTime = true
		case 'T':
			flags.LongTime = true
		case 'd':
			flags.ShortDate = true
		case 'D':
			flags.LongDate = true
		case 'w', 'W':
			flags.DayOfWeek = true
		default:
			return richbuilder.DateFlags{}, false
		}
	}
	return flags, true
}

func htmlNodeAttr(node *html.Node, key string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return attribute.Val
		}
	}
	return ""
}

func htmlNodeHasAttr(node *html.Node, key string) bool {
	for _, attribute := range node.Attr {
		if attribute.Key == key {
			return true
		}
	}
	return false
}
