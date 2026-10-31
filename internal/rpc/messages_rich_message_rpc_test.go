package rpc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestStoredRichMessageMissingLayerDecodesAsExact228(t *testing.T) {
	legacyBlocks := []tg.PageBlockClass{&tg.PageBlockBlockquote{
		Text:    &tg.TextPlain{Text: "legacy quote"},
		Caption: &tg.TextEmpty{},
	}}
	var wire bin.Buffer
	if err := tlprofile.EncodePageBlockVector(tlprofile.Profile228, legacyBlocks, &wire); err != nil {
		t.Fatal(err)
	}
	raw := wire.Copy()
	rich := &domain.MessageRichMessage{Blocks: raw}
	got, err := tgRichMessage(rich)
	if err != nil {
		t.Fatal(err)
	}
	if rich.BlocksLayer != 0 || string(rich.Blocks) != string(raw) {
		t.Fatal("legacy read mutated the persisted snapshot")
	}
	quote, ok := got.Blocks[0].(*tg.PageBlockBlockquote)
	if !ok {
		t.Fatalf("decoded block = %T", got.Blocks[0])
	}
	if quote.Collapsed {
		t.Fatal("Layer 228 blockquote acquired Layer 229 collapsed state")
	}
	text, ok := quote.Text.(*tg.TextPlain)
	if !ok || text.Text != "legacy quote" {
		t.Fatalf("decoded quote text = %#v", quote.Text)
	}
}

func TestNewRichMessageStoresExact230Profile(t *testing.T) {
	r := &Router{}
	rich, err := r.domainRichMessageFromInput(context.Background(), &tg.InputRichMessage{
		Blocks: []tg.PageBlockClass{&tg.PageBlockBlockquote{
			Collapsed: true,
			Text:      &tg.TextPlain{Text: "current quote"},
			Caption:   &tg.TextEmpty{},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rich.BlocksLayer != int(tlprofile.ProfileCanonical) {
		t.Fatalf("stored blocks layer = %d, want canonical %d", rich.BlocksLayer, tlprofile.ProfileCanonical)
	}
	if got := binary.LittleEndian.Uint32(rich.Blocks[8:12]); got != 0x66d1670b {
		t.Fatalf("stored blockquote constructor = %#08x, want Layer 230", got)
	}
}

func TestLayer228SenderRichMessageProjectsToLayer229Receiver(t *testing.T) {
	canonical := []tg.PageBlockClass{&tg.PageBlockBlockquote{
		Text:    &tg.TextPlain{Text: "cross-layer quote"},
		Caption: &tg.TextEmpty{},
	}}
	var senderWire bin.Buffer
	if err := tlprofile.EncodePageBlockVector(tlprofile.Profile228, canonical, &senderWire); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(senderWire.Raw()[8:12]); got != 0x263d7c26 {
		t.Fatalf("Layer 228 sender constructor = %#08x", got)
	}
	decodedSender, err := tlprofile.DecodePageBlockVector(
		tlprofile.Profile228,
		&bin.Buffer{Buf: senderWire.Copy()},
		tlprofile.Limits{},
	)
	if err != nil {
		t.Fatal(err)
	}

	r := &Router{}
	stored, err := r.domainRichMessageFromInput(context.Background(), &tg.InputRichMessage{Blocks: decodedSender})
	if err != nil {
		t.Fatal(err)
	}
	if stored.BlocksLayer != int(tlprofile.ProfileCanonical) {
		t.Fatalf("storage layer = %d, want canonical %d", stored.BlocksLayer, tlprofile.ProfileCanonical)
	}
	if got := binary.LittleEndian.Uint32(stored.Blocks[8:12]); got != 0x66d1670b {
		t.Fatalf("storage constructor = %#08x, want Layer 230", got)
	}

	projected, err := tgRichMessage(stored)
	if err != nil {
		t.Fatal(err)
	}
	quote := projected.Blocks[0].(*tg.PageBlockBlockquote)
	if quote.Collapsed {
		t.Fatal("Layer 228 sender acquired collapsed=true")
	}
	var receiverWire bin.Buffer
	if err := tlprofile.EncodePageBlockVector(tlprofile.Profile229, projected.Blocks, &receiverWire); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint32(receiverWire.Raw()[8:12]); got != 0x66d1670b {
		t.Fatalf("Layer 229 receiver constructor = %#08x", got)
	}
}

func TestLayer228ReceiverSkipsLayer229OnlyRichBlocks(t *testing.T) {
	message := &tg.Message{
		ID:      42,
		PeerID:  &tg.PeerUser{UserID: 1001},
		Date:    1700000000,
		Message: "base message",
	}
	message.SetRichMessage(tg.RichMessage{Blocks: []tg.PageBlockClass{
		&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "compatible"}},
		&tg.PageBlockButtonRow{},
	}})

	var wire bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile228, message, &wire); err != nil {
		t.Fatal(err)
	}
	decoded, err := tlprofile.DecodeObject(tlprofile.Profile228, &bin.Buffer{Buf: wire.Copy()}, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	projected, ok := decoded.(*tg.Message)
	if !ok {
		t.Fatalf("decoded message = %T", decoded)
	}
	if projected.Message != "base message" {
		t.Fatalf("base message = %q", projected.Message)
	}
	rich, present := projected.GetRichMessage()
	if !present {
		t.Fatal("compatible rich_message was dropped")
	}
	if len(rich.Blocks) != 1 {
		t.Fatalf("Layer 228 rich blocks = %d, want one compatible block", len(rich.Blocks))
	}
	if _, ok := rich.Blocks[0].(*tg.PageBlockParagraph); !ok {
		t.Fatalf("remaining block = %T", rich.Blocks[0])
	}
}

func TestInvalidStoredRichMessageDoesNotPanicBaseProjection(t *testing.T) {
	projected, ok := tgMessage(domain.Message{
		ID:   42,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
		RichMessage: &domain.MessageRichMessage{
			BlocksLayer: 999,
			Blocks:      []byte{1, 2, 3, 4},
		},
	}).(*tg.Message)
	if !ok {
		t.Fatal("base message projection was lost")
	}
	if _, present := projected.GetRichMessage(); present {
		t.Fatal("invalid optional rich_message was projected")
	}
}

// richTextBlocks builds a set of plain-text IV page blocks for rich-text round-trip assertions.
func richTextBlocks() []tg.PageBlockClass {
	return richTextBlocksWith("Rich Title", "First paragraph.")
}

func richTextBlocksWith(title, paragraph string) []tg.PageBlockClass {
	return []tg.PageBlockClass{
		&tg.PageBlockTitle{Text: &tg.TextPlain{Text: title}},
		&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: paragraph}},
	}
}

func richHeadingTableBlocks() []tg.PageBlockClass {
	return []tg.PageBlockClass{
		&tg.PageBlockHeading2{Text: &tg.TextPlain{Text: "Quarterly results"}},
		&tg.PageBlockTable{
			Bordered: true,
			Striped:  true,
			Title:    &tg.TextPlain{Text: "Revenue"},
			Rows: []tg.PageTableRow{
				{Cells: []tg.PageTableCell{
					{Header: true, Text: &tg.TextPlain{Text: "Quarter"}},
					{Header: true, Text: &tg.TextPlain{Text: "Amount"}},
				}},
				{Cells: []tg.PageTableCell{
					{Text: &tg.TextPlain{Text: "Q1"}},
					{AlignRight: true, Text: &tg.TextPlain{Text: "100"}},
				}},
			},
		},
	}
}

func assertRichHeadingTableBlocks(t *testing.T, label string, rich tg.RichMessage) {
	t.Helper()
	if len(rich.Blocks) != 2 {
		t.Fatalf("%s: blocks = %d, want heading and table", label, len(rich.Blocks))
	}
	heading, ok := rich.Blocks[0].(*tg.PageBlockHeading2)
	if !ok {
		t.Fatalf("%s: block[0] = %T, want *tg.PageBlockHeading2", label, rich.Blocks[0])
	}
	if text, ok := heading.Text.(*tg.TextPlain); !ok || text.Text != "Quarterly results" {
		t.Fatalf("%s: heading text = %+v", label, heading.Text)
	}
	table, ok := rich.Blocks[1].(*tg.PageBlockTable)
	if !ok {
		t.Fatalf("%s: block[1] = %T, want *tg.PageBlockTable", label, rich.Blocks[1])
	}
	if !table.Bordered || !table.Striped || len(table.Rows) != 2 || len(table.Rows[0].Cells) != 2 {
		t.Fatalf("%s: table shape = %+v", label, table)
	}
	if !table.Rows[0].Cells[0].Header {
		t.Fatalf("%s: first table cell lost header flag", label)
	}
}

func richEmptyCaption() tg.PageCaption {
	return tg.PageCaption{
		Text:   &tg.TextEmpty{},
		Credit: &tg.TextEmpty{},
	}
}

func richOrderedListWithoutNums() []tg.PageBlockClass {
	return []tg.PageBlockClass{
		&tg.PageBlockOrderedList{
			Items: []tg.PageListOrderedItemClass{
				&tg.PageListOrderedItemText{Text: &tg.TextPlain{Text: "one"}},
				&tg.PageListOrderedItemBlocks{
					Blocks: []tg.PageBlockClass{
						&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "two"}},
					},
				},
			},
		},
	}
}

func richNestedOrderedListBlock() tg.PageBlockClass {
	return richOrderedListWithoutNums()[0]
}

func assertOrderedListNums(t *testing.T, label string, blocks []tg.PageBlockClass, want ...string) {
	t.Helper()
	if len(blocks) != 1 {
		t.Fatalf("%s: blocks = %d, want 1", label, len(blocks))
	}
	list, ok := blocks[0].(*tg.PageBlockOrderedList)
	if !ok {
		t.Fatalf("%s: block[0] = %T, want *tg.PageBlockOrderedList", label, blocks[0])
	}
	if len(list.Items) != len(want) {
		t.Fatalf("%s: items = %d, want %d", label, len(list.Items), len(want))
	}
	for idx, item := range list.Items {
		var (
			num string
			ok  bool
		)
		switch i := item.(type) {
		case *tg.PageListOrderedItemText:
			num, ok = i.GetNum()
		case *tg.PageListOrderedItemBlocks:
			num, ok = i.GetNum()
		default:
			t.Fatalf("%s: item[%d] = %T, want ordered text/blocks", label, idx, item)
		}
		if !ok || num != want[idx] {
			t.Fatalf("%s: item[%d].num = %q, ok=%v, want %q", label, idx, num, ok, want[idx])
		}
	}
}

func collectOrderedListNums(blocks []tg.PageBlockClass) []string {
	var nums []string
	var walk func(tg.PageBlockClass)
	walk = func(block tg.PageBlockClass) {
		switch b := block.(type) {
		case *tg.PageBlockList:
			for _, item := range b.Items {
				if item, ok := item.(*tg.PageListItemBlocks); ok {
					for _, child := range item.Blocks {
						walk(child)
					}
				}
			}
		case *tg.PageBlockCover:
			walk(b.Cover)
		case *tg.PageBlockEmbedPost:
			for _, child := range b.Blocks {
				walk(child)
			}
		case *tg.PageBlockCollage:
			for _, child := range b.Items {
				walk(child)
			}
		case *tg.PageBlockSlideshow:
			for _, child := range b.Items {
				walk(child)
			}
		case *tg.PageBlockDetails:
			for _, child := range b.Blocks {
				walk(child)
			}
		case *tg.PageBlockBlockquoteBlocks:
			for _, child := range b.Blocks {
				walk(child)
			}
		case *tg.PageBlockOrderedList:
			for _, item := range b.Items {
				switch i := item.(type) {
				case *tg.PageListOrderedItemText:
					if num, ok := i.GetNum(); ok {
						nums = append(nums, num)
					} else {
						nums = append(nums, "")
					}
				case *tg.PageListOrderedItemBlocks:
					if num, ok := i.GetNum(); ok {
						nums = append(nums, num)
					} else {
						nums = append(nums, "")
					}
					for _, child := range i.Blocks {
						walk(child)
					}
				}
			}
		}
	}
	for _, block := range blocks {
		walk(block)
	}
	return nums
}

// assertRichTextBlocks verifies the projected RichMessage carries the two blocks from richTextBlocks (heading + paragraph).
func assertRichTextBlocks(t *testing.T, label string, rich tg.RichMessage) {
	t.Helper()
	if !rich.Rtl {
		t.Errorf("%s: rtl = false, want true", label)
	}
	if len(rich.Blocks) != 2 {
		t.Fatalf("%s: blocks = %d, want 2", label, len(rich.Blocks))
	}
	title, ok := rich.Blocks[0].(*tg.PageBlockTitle)
	if !ok {
		t.Fatalf("%s: block[0] = %T, want *tg.PageBlockTitle", label, rich.Blocks[0])
	}
	if tp, ok := title.Text.(*tg.TextPlain); !ok || tp.Text != "Rich Title" {
		t.Errorf("%s: title text = %+v, want plain %q", label, title.Text, "Rich Title")
	}
	para, ok := rich.Blocks[1].(*tg.PageBlockParagraph)
	if !ok {
		t.Fatalf("%s: block[1] = %T, want *tg.PageBlockParagraph", label, rich.Blocks[1])
	}
	if tp, ok := para.Text.(*tg.TextPlain); !ok || tp.Text != "First paragraph." {
		t.Errorf("%s: paragraph text = %+v, want plain %q", label, para.Text, "First paragraph.")
	}
}

func assertRichTitle(t *testing.T, label string, rich tg.RichMessage, want string) {
	t.Helper()
	if len(rich.Blocks) == 0 {
		t.Fatalf("%s: missing rich blocks", label)
	}
	title, ok := rich.Blocks[0].(*tg.PageBlockTitle)
	if !ok {
		t.Fatalf("%s: block[0] = %T, want *tg.PageBlockTitle", label, rich.Blocks[0])
	}
	if tp, ok := title.Text.(*tg.TextPlain); !ok || tp.Text != want {
		t.Fatalf("%s: title text = %+v, want plain %q", label, title.Text, want)
	}
}

func TestRichMessageOrderedListNumsNormalized(t *testing.T) {
	ctx := context.Background()
	r := &Router{}

	rich, err := r.domainRichMessageFromInput(ctx, &tg.InputRichMessage{
		Blocks: richOrderedListWithoutNums(),
	})
	if err != nil {
		t.Fatalf("domain rich message: %v", err)
	}
	got, err := tgRichMessage(rich)
	if err != nil {
		t.Fatalf("tg rich message: %v", err)
	}
	assertOrderedListNums(t, "new input", got.Blocks, "1", "2")
}

func TestBotAPIRichHTMLParsesBedolagaMenuStructures(t *testing.T) {
	r := &Router{}
	rich, err := r.domainRichMessageFromInput(context.Background(), &tg.InputRichMessageHTML{
		Rtl:        true,
		Noautolink: true,
		HTML: `<h4>Admin</h4>
		<table bordered striped><tr><th>Status</th><td align="right" valign="bottom"><tg-time unix="1700000000" format="R">now</tg-time></td></tr></table>
		<details open><summary>More</summary><p><blockquote><code>healthy</code></blockquote></p></details>
		<footer>Choose an option</footer>`,
	})
	if err != nil {
		t.Fatalf("parse Bedolaga rich HTML: %v", err)
	}
	decoded, err := tgRichMessage(rich)
	if err != nil {
		t.Fatalf("decode rich HTML: %v", err)
	}
	if !decoded.Rtl {
		t.Fatal("rich HTML lost is_rtl")
	}
	var heading, table, details, footer bool
	for _, block := range decoded.Blocks {
		switch value := block.(type) {
		case *tg.PageBlockHeading4:
			heading = true
		case *tg.PageBlockTable:
			table = true
			if !value.Bordered || !value.Striped || len(value.Rows) != 1 || len(value.Rows[0].Cells) != 2 {
				t.Fatalf("table shape = %+v", value)
			}
			cell := value.Rows[0].Cells[1]
			if !cell.AlignRight || !cell.ValignBottom || !richTextContainsDate(cell.Text, 1700000000) {
				t.Fatalf("table date/alignment = %+v", cell)
			}
		case *tg.PageBlockDetails:
			details = value.Open && len(value.Blocks) != 0
		case *tg.PageBlockFooter:
			footer = true
		}
	}
	if !heading || !table || !details || !footer {
		t.Fatalf("parsed blocks heading=%v table=%v details=%v footer=%v: %#v", heading, table, details, footer, decoded.Blocks)
	}
	var projected struct {
		RTL    bool `json:"is_rtl"`
		Blocks []struct {
			Type string `json:"type"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(rich.BotAPIProjection, &projected); err != nil {
		t.Fatalf("decode Bot API projection: %v", err)
	}
	if !projected.RTL || len(projected.Blocks) != len(decoded.Blocks) {
		t.Fatalf("Bot API projection = %s", rich.BotAPIProjection)
	}

	if _, err := r.domainRichMessageFromInput(context.Background(), &tg.InputRichMessageHTML{
		HTML: `<h4>Admin</h4><img src="https://example.test/logo.png">`,
	}); err == nil || !tgerr.Is(err, "WEBPAGE_MEDIA_EMPTY") {
		t.Fatalf("HTML media err = %v, want WEBPAGE_MEDIA_EMPTY for Bedolaga no-logo retry", err)
	}
}

func TestBotAPIRichMarkdownParsesAndProjects(t *testing.T) {
	r := &Router{}
	rich, err := r.domainRichMessageFromInput(context.Background(), &tg.InputRichMessageMarkdown{
		Markdown: "# Bedolaga\n\n**Subscription:** active",
	})
	if err != nil {
		t.Fatalf("parse rich Markdown: %v", err)
	}
	decoded, err := tgRichMessage(rich)
	if err != nil {
		t.Fatalf("decode rich Markdown: %v", err)
	}
	if len(decoded.Blocks) < 2 || len(rich.BotAPIProjection) == 0 || !strings.Contains(string(rich.BotAPIProjection), "Bedolaga") {
		t.Fatalf("Markdown decoded=%#v projection=%s", decoded.Blocks, rich.BotAPIProjection)
	}
}

func richTextContainsDate(text tg.RichTextClass, want int) bool {
	switch value := text.(type) {
	case *tg.TextDate:
		return value.Date == want
	case *tg.TextConcat:
		for _, child := range value.Texts {
			if richTextContainsDate(child, want) {
				return true
			}
		}
	case *tg.TextBold:
		return richTextContainsDate(value.Text, want)
	case *tg.TextItalic:
		return richTextContainsDate(value.Text, want)
	case *tg.TextFixed:
		return richTextContainsDate(value.Text, want)
	case *tg.TextURL:
		return richTextContainsDate(value.Text, want)
	}
	return false
}

func TestRichMessageRejectsResourcesWithoutBlocks(t *testing.T) {
	ctx := context.Background()
	r := &Router{}

	rich, err := r.domainRichMessageFromInput(ctx, &tg.InputRichMessage{})
	if err != nil {
		t.Fatalf("empty input rich message: %v", err)
	}
	if rich != nil {
		t.Fatalf("empty input rich message = %+v, want nil", rich)
	}
	if _, err := r.domainRichMessageFromInput(ctx, &tg.InputRichMessage{
		Photos: []tg.InputPhotoClass{&tg.InputPhoto{ID: 1, AccessHash: 2}},
	}); err == nil {
		t.Fatalf("orphan rich photos without blocks accepted")
	}
	if _, err := r.domainRichMessageFromInput(ctx, &tg.InputRichMessage{
		Documents: []tg.InputDocumentClass{&tg.InputDocument{ID: 1, AccessHash: 2}},
	}); err == nil {
		t.Fatalf("orphan rich documents without blocks accepted")
	}
}

func TestRichMessageNormalizesNestedOrderedListContainers(t *testing.T) {
	ctx := context.Background()
	r := &Router{}
	caption := richEmptyCaption()
	blocks := []tg.PageBlockClass{
		&tg.PageBlockList{Items: []tg.PageListItemClass{
			&tg.PageListItemBlocks{Blocks: []tg.PageBlockClass{richNestedOrderedListBlock()}},
		}},
		&tg.PageBlockCover{Cover: richNestedOrderedListBlock()},
		&tg.PageBlockEmbedPost{
			URL:       "https://example.test/post",
			Author:    "author",
			Blocks:    []tg.PageBlockClass{richNestedOrderedListBlock()},
			Caption:   caption,
			WebpageID: 1,
		},
		&tg.PageBlockCollage{Items: []tg.PageBlockClass{richNestedOrderedListBlock()}, Caption: caption},
		&tg.PageBlockSlideshow{Items: []tg.PageBlockClass{richNestedOrderedListBlock()}, Caption: caption},
		&tg.PageBlockDetails{
			Title:  &tg.TextPlain{Text: "details"},
			Blocks: []tg.PageBlockClass{richNestedOrderedListBlock()},
		},
		&tg.PageBlockBlockquoteBlocks{
			Blocks:  []tg.PageBlockClass{richNestedOrderedListBlock()},
			Caption: &tg.TextEmpty{},
		},
	}
	rich, err := r.domainRichMessageFromInput(ctx, &tg.InputRichMessage{Blocks: blocks})
	if err != nil {
		t.Fatalf("domain rich message: %v", err)
	}
	got, err := tgRichMessage(rich)
	if err != nil {
		t.Fatalf("tg rich message: %v", err)
	}
	nums := collectOrderedListNums(got.Blocks)
	want := []string{"1", "2", "1", "2", "1", "2", "1", "2", "1", "2", "1", "2", "1", "2"}
	if len(nums) != len(want) {
		t.Fatalf("ordered nums = %v, want %v", nums, want)
	}
	for i := range want {
		if nums[i] != want[i] {
			t.Fatalf("ordered nums = %v, want %v", nums, want)
		}
	}
}

func TestRichMessageBlockFormatsEncodeDecode(t *testing.T) {
	caption := richEmptyCaption()
	blocks := []tg.PageBlockClass{
		&tg.PageBlockTitle{Text: &tg.TextPlain{Text: "title"}},
		&tg.PageBlockSubtitle{Text: &tg.TextPlain{Text: "subtitle"}},
		&tg.PageBlockAuthorDate{Author: &tg.TextPlain{Text: "author"}, PublishedDate: 1},
		&tg.PageBlockHeader{Text: &tg.TextPlain{Text: "header"}},
		&tg.PageBlockSubheader{Text: &tg.TextPlain{Text: "subheader"}},
		&tg.PageBlockParagraph{Text: &tg.TextConcat{Texts: []tg.RichTextClass{
			&tg.TextPlain{Text: "plain"},
			&tg.TextBold{Text: &tg.TextPlain{Text: "bold"}},
			&tg.TextItalic{Text: &tg.TextPlain{Text: "italic"}},
			&tg.TextUnderline{Text: &tg.TextPlain{Text: "underline"}},
			&tg.TextStrike{Text: &tg.TextPlain{Text: "strike"}},
			&tg.TextFixed{Text: &tg.TextPlain{Text: "fixed"}},
			&tg.TextSpoiler{Text: &tg.TextPlain{Text: "spoiler"}},
			&tg.TextURL{Text: &tg.TextPlain{Text: "url"}, URL: "https://example.test"},
			&tg.TextEmail{Text: &tg.TextPlain{Text: "email"}, Email: "a@example.test"},
			&tg.TextPhone{Text: &tg.TextPlain{Text: "phone"}, Phone: "+10000000000"},
			&tg.TextMath{Source: "x"},
		}}},
		&tg.PageBlockPreformatted{Text: &tg.TextPlain{Text: "pre"}, Language: "go"},
		&tg.PageBlockFooter{Text: &tg.TextPlain{Text: "footer"}},
		&tg.PageBlockDivider{},
		&tg.PageBlockAnchor{Name: "anchor"},
		&tg.PageBlockList{Items: []tg.PageListItemClass{
			&tg.PageListItemText{Text: &tg.TextPlain{Text: "item"}},
			&tg.PageListItemBlocks{Blocks: []tg.PageBlockClass{
				&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "nested"}},
			}},
		}},
		&tg.PageBlockBlockquote{Text: &tg.TextPlain{Text: "quote"}, Caption: &tg.TextEmpty{}},
		&tg.PageBlockPullquote{Text: &tg.TextPlain{Text: "pull"}, Caption: &tg.TextEmpty{}},
		&tg.PageBlockPhoto{PhotoID: 1, Caption: caption},
		&tg.PageBlockVideo{VideoID: 2, Caption: caption},
		&tg.PageBlockCover{Cover: &tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "cover"}}},
		&tg.PageBlockEmbedPost{
			URL:       "https://example.test/post",
			Author:    "author",
			Blocks:    []tg.PageBlockClass{&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "post"}}},
			Caption:   caption,
			WebpageID: 3,
		},
		&tg.PageBlockCollage{Items: []tg.PageBlockClass{&tg.PageBlockPhoto{PhotoID: 4, Caption: caption}}, Caption: caption},
		&tg.PageBlockSlideshow{Items: []tg.PageBlockClass{&tg.PageBlockVideo{VideoID: 5, Caption: caption}}, Caption: caption},
		&tg.PageBlockAudio{AudioID: 6, Caption: caption},
		&tg.PageBlockKicker{Text: &tg.TextPlain{Text: "kicker"}},
		&tg.PageBlockTable{Title: &tg.TextPlain{Text: "table"}},
		&tg.PageBlockOrderedList{Items: []tg.PageListOrderedItemClass{
			&tg.PageListOrderedItemText{Text: &tg.TextPlain{Text: "one"}},
		}},
		&tg.PageBlockDetails{Title: &tg.TextPlain{Text: "details"}, Blocks: []tg.PageBlockClass{
			&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "inside"}},
		}},
		&tg.PageBlockRelatedArticles{Title: &tg.TextPlain{Text: "related"}, Articles: []tg.PageRelatedArticle{
			{URL: "https://example.test/a", WebpageID: 7},
		}},
		&tg.PageBlockMap{Geo: &tg.GeoPointEmpty{}, Zoom: 13, W: 64, H: 64, Caption: caption},
		&tg.PageBlockHeading1{Text: &tg.TextPlain{Text: "h1"}},
		&tg.PageBlockHeading2{Text: &tg.TextPlain{Text: "h2"}},
		&tg.PageBlockHeading3{Text: &tg.TextPlain{Text: "h3"}},
		&tg.PageBlockHeading4{Text: &tg.TextPlain{Text: "h4"}},
		&tg.PageBlockHeading5{Text: &tg.TextPlain{Text: "h5"}},
		&tg.PageBlockHeading6{Text: &tg.TextPlain{Text: "h6"}},
		&tg.PageBlockMath{Source: "x^2"},
		&tg.PageBlockThinking{Text: &tg.TextPlain{Text: "thinking"}},
		&tg.PageBlockBlockquoteBlocks{
			Blocks:  []tg.PageBlockClass{&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "blocks"}}},
			Caption: &tg.TextEmpty{},
		},
		&tg.PageBlockUnsupported{},
	}
	ctx := context.Background()
	r, _, _ := newMediaTestRouter(t)
	files := r.deps.Files.(*fakeFiles)
	for _, id := range []int64{1, 3, 4} {
		files.photos[id] = domain.Photo{
			ID: id, AccessHash: id + 100, DCID: 2,
			Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 32, H: 32, Size: 64}},
		}
	}
	for _, id := range []int64{2, 5, 6} {
		files.docs[id] = domain.Document{ID: id, AccessHash: id + 100, DCID: 2, MimeType: "application/octet-stream", Size: 64}
	}
	rich, err := r.domainRichMessageFromInput(ctx, &tg.InputRichMessage{Blocks: blocks})
	if err != nil {
		t.Fatalf("domain rich message: %v", err)
	}
	got, err := tgRichMessage(rich)
	if err != nil {
		t.Fatalf("tg rich message: %v", err)
	}
	if len(got.Blocks) != len(blocks) {
		t.Fatalf("blocks = %d, want %d", len(got.Blocks), len(blocks))
	}
	nums := collectOrderedListNums(got.Blocks)
	if len(nums) != 1 || nums[0] != "1" {
		t.Fatalf("ordered nums = %v, want [1]", nums)
	}
}

// TestSendMessageRichMessageTextBlocksRoundTrip verifies Layer 227 rich text (the
// inputRichMessage blocks form) round-trips verbatim through the whole chain:
// send -> sender echo / getMessages / getRichMessage.
func TestSendMessageRichMessageTextBlocksRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "",
		RandomID: 7001,
		RichMessage: &tg.InputRichMessage{
			Rtl:    true,
			Blocks: richTextBlocks(),
		},
	})
	if err != nil {
		t.Fatalf("send rich message: %v", err)
	}
	echo := newMessageFromUpdates(t, updates)
	rich, ok := echo.GetRichMessage()
	if !ok {
		t.Fatalf("send echo missing rich message")
	}
	assertRichTextBlocks(t, "send echo", rich)

	// getMessages (the sender pulling by box id) should also carry the rich text.
	got, err := r.onMessagesGetMessages(WithUserID(ctx, owner.ID), []tg.InputMessageClass{&tg.InputMessageID{ID: echo.ID}})
	if err != nil {
		t.Fatalf("get messages: %v", err)
	}
	stored := singleStoredMessage(t, got)
	rich, ok = stored.GetRichMessage()
	if !ok {
		t.Fatalf("getMessages missing rich message")
	}
	assertRichTextBlocks(t, "getMessages", rich)

	// getRichMessage (pulling the full rich text by peer+id) should carry the rich text.
	gotRich, err := r.onMessagesGetRichMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetRichMessageRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		ID:   echo.ID,
	})
	if err != nil {
		t.Fatalf("get rich message: %v", err)
	}
	stored = singleStoredMessage(t, gotRich)
	rich, ok = stored.GetRichMessage()
	if !ok {
		t.Fatalf("getRichMessage missing rich message")
	}
	assertRichTextBlocks(t, "getRichMessage", rich)
}

// TestSendMessageRichOnlyHeadingTableRoundTrip covers TDesktop rich editor's real
// send shape: messages.sendMessage with f_rich_message (heading and table blocks)
// but an empty message:string.
func TestSendMessageRichOnlyHeadingTableRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		RandomID: 7101,
		RichMessage: &tg.InputRichMessage{
			Blocks: richHeadingTableBlocks(),
		},
	})
	if err != nil {
		t.Fatalf("send rich-only message: %v", err)
	}
	echo := newMessageFromUpdates(t, updates)
	if echo.Message != "" {
		t.Fatalf("rich-only echo message = %q, want empty fallback text", echo.Message)
	}
	rich, ok := echo.GetRichMessage()
	if !ok {
		t.Fatalf("rich-only echo missing rich message")
	}
	assertRichHeadingTableBlocks(t, "rich-only echo", rich)

	got, err := r.onMessagesGetMessages(WithUserID(ctx, owner.ID), []tg.InputMessageClass{&tg.InputMessageID{ID: echo.ID}})
	if err != nil {
		t.Fatalf("get rich-only message: %v", err)
	}
	stored := singleStoredMessage(t, got)
	rich, ok = stored.GetRichMessage()
	if !ok {
		t.Fatalf("getMessages missing rich-only message")
	}
	assertRichHeadingTableBlocks(t, "getMessages", rich)

	gotRich, err := r.onMessagesGetRichMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetRichMessageRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		ID:   echo.ID,
	})
	if err != nil {
		t.Fatalf("get rich-only message body: %v", err)
	}
	stored = singleStoredMessage(t, gotRich)
	rich, ok = stored.GetRichMessage()
	if !ok {
		t.Fatalf("getRichMessage missing rich-only message")
	}
	assertRichHeadingTableBlocks(t, "getRichMessage", rich)
}

func TestEditMessageRichOnlyPrivateRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:        &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		RandomID:    7102,
		RichMessage: &tg.InputRichMessage{Rtl: true, Blocks: richTextBlocks()},
	})
	if err != nil {
		t.Fatalf("send rich-only message: %v", err)
	}
	msg := newMessageFromUpdates(t, updates)

	editReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		ID:   msg.ID,
	}
	editReq.SetRichMessage(&tg.InputRichMessage{
		Rtl:    true,
		Blocks: richTextBlocksWith("Edited Title", "Edited paragraph."),
	})
	edited, err := r.onMessagesEditMessage(WithUserID(ctx, owner.ID), editReq)
	if err != nil {
		t.Fatalf("edit rich-only private message: %v", err)
	}
	editedMsg := editMessageFromUpdates(t, edited)
	rich, ok := editedMsg.GetRichMessage()
	if !ok {
		t.Fatalf("edited private message missing rich message")
	}
	assertRichTitle(t, "edited private", rich, "Edited Title")
}

func TestChannelRichMessageSendEditHistoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, owner, channel := newRichChannelTestRouter(t)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:        peer,
		RandomID:    7201,
		RichMessage: &tg.InputRichMessage{Rtl: true, Blocks: richTextBlocks()},
	})
	if err != nil {
		t.Fatalf("send channel rich-only message: %v", err)
	}
	echo := newMessageFromUpdates(t, updates)
	rich, ok := echo.GetRichMessage()
	if !ok {
		t.Fatalf("channel echo missing rich message")
	}
	assertRichTextBlocks(t, "channel echo", rich)

	historyList, err := r.deps.Channels.GetHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel get history: %v", err)
	}
	history := r.tgChannelHistoryMessages(WithUserID(ctx, owner.ID), owner.ID, historyList)
	stored := singleChannelStoredMessage(t, history)
	rich, ok = stored.GetRichMessage()
	if !ok {
		t.Fatalf("channel history missing rich message")
	}
	assertRichTextBlocks(t, "channel history", rich)

	editReq := &tg.MessagesEditMessageRequest{Peer: peer, ID: echo.ID}
	editReq.SetRichMessage(&tg.InputRichMessage{
		Rtl:    true,
		Blocks: richTextBlocksWith("Edited Channel", "Edited channel paragraph."),
	})
	edited, err := r.onMessagesEditMessage(WithUserID(ctx, owner.ID), editReq)
	if err != nil {
		t.Fatalf("edit channel rich-only message: %v", err)
	}
	editedMsg := editChannelMessageFromUpdates(t, edited)
	rich, ok = editedMsg.GetRichMessage()
	if !ok {
		t.Fatalf("edited channel message missing rich message")
	}
	assertRichTitle(t, "edited channel", rich, "Edited Channel")
}

func TestSaveDraftRichMessageRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newRichDraftTestRouter(t)

	ok, err := r.onMessagesSaveDraft(WithUserID(ctx, owner.ID), &tg.MessagesSaveDraftRequest{
		Peer:        &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		RichMessage: &tg.InputRichMessage{Rtl: true, Blocks: richTextBlocks()},
	})
	if err != nil || !ok {
		t.Fatalf("save rich draft = %v, %v", ok, err)
	}
	got, err := r.onMessagesGetAllDrafts(WithUserID(ctx, owner.ID))
	if err != nil {
		t.Fatalf("get all drafts: %v", err)
	}
	updates := got.(*tg.Updates)
	if len(updates.Updates) != 1 {
		t.Fatalf("draft updates = %+v, want one", updates.Updates)
	}
	update, ok := updates.Updates[0].(*tg.UpdateDraftMessage)
	if !ok {
		t.Fatalf("draft update = %T", updates.Updates[0])
	}
	draft, ok := update.Draft.(*tg.DraftMessage)
	if !ok {
		t.Fatalf("draft = %T, want *tg.DraftMessage", update.Draft)
	}
	rich, ok := draft.GetRichMessage()
	if !ok {
		t.Fatalf("draft missing rich message")
	}
	assertRichTextBlocks(t, "draft", rich)
}

// TestGetRichMessageWrongPeerReturnsEmpty verifies getRichMessage's peer
// validation: pulling with a mismatched peer should return messageEmpty (no
// cross-dialog leakage).
func TestGetRichMessageWrongPeerReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:        &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:     "",
		RandomID:    7002,
		RichMessage: &tg.InputRichMessage{Rtl: true, Blocks: richTextBlocks()},
	})
	if err != nil {
		t.Fatalf("send rich message: %v", err)
	}
	echo := newMessageFromUpdates(t, updates)

	// Pull with the self peer (different from the message box's peer=friend) -> messageEmpty.
	gotRich, err := r.onMessagesGetRichMessage(WithUserID(ctx, owner.ID), &tg.MessagesGetRichMessageRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   echo.ID,
	})
	if err != nil {
		t.Fatalf("get rich message wrong peer: %v", err)
	}
	box, ok := gotRich.(*tg.MessagesMessages)
	if !ok || len(box.Messages) != 1 {
		t.Fatalf("getRichMessage wrong peer = %T %+v, want one messages.messages", gotRich, gotRich)
	}
	if _, ok := box.Messages[0].(*tg.MessageEmpty); !ok {
		t.Fatalf("getRichMessage wrong peer message = %T, want *tg.MessageEmpty", box.Messages[0])
	}
}

// TestSendMessageRichMessageEmbeddedPhoto verifies an embedded image in rich
// text: it is resolved by id and stored as a media snapshot, then restored via
// tgPhoto during projection.
func TestSendMessageRichMessageEmbeddedPhoto(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	files, ok := r.deps.Files.(*fakeFiles)
	if !ok {
		t.Fatalf("deps.Files = %T, want *fakeFiles", r.deps.Files)
	}
	files.photos[889] = domain.Photo{ID: 889, AccessHash: 42, DCID: 2, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600}}}

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "",
		RandomID: 7003,
		RichMessage: &tg.InputRichMessage{
			Blocks: []tg.PageBlockClass{
				&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "see photo"}},
				&tg.PageBlockPhoto{PhotoID: 889, Caption: richEmptyCaption()},
			},
			Photos: []tg.InputPhotoClass{&tg.InputPhoto{ID: 889, AccessHash: 42}},
		},
	})
	if err != nil {
		t.Fatalf("send rich message with photo: %v", err)
	}
	echo := newMessageFromUpdates(t, updates)
	rich, ok := echo.GetRichMessage()
	if !ok {
		t.Fatalf("send echo missing rich message")
	}
	if len(rich.Photos) != 1 {
		t.Fatalf("rich photos = %d, want 1", len(rich.Photos))
	}
	photo, ok := rich.Photos[0].(*tg.Photo)
	if !ok {
		t.Fatalf("rich photo = %T, want *tg.Photo", rich.Photos[0])
	}
	if photo.ID != 889 {
		t.Errorf("rich photo id = %d, want 889", photo.ID)
	}
	block, ok := rich.Blocks[1].(*tg.PageBlockPhoto)
	if !ok || block.PhotoID != photo.ID {
		t.Fatalf("rich photo block = %#v, photo id = %d", rich.Blocks[1], photo.ID)
	}
}

// TestRichMessageMediaClosureResolvesBlockReferences verifies the server builds the
// output resource tables from the PageBlock graph. The input resource vector is
// optional on the wire; its absence must not leave a dangling block that Web cannot
// render when the referenced upload already exists.
func TestRichMessageMediaClosureResolvesBlockReferences(t *testing.T) {
	ctx := context.Background()
	r, _, _ := newMediaTestRouter(t)
	files := r.deps.Files.(*fakeFiles)
	for _, id := range []int64{889, 890, 891} {
		files.photos[id] = domain.Photo{
			ID: id, AccessHash: id + 100, DCID: 2,
			Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600, Size: 123}},
		}
	}
	files.docs[990] = domain.Document{ID: 990, AccessHash: 1090, DCID: 2, MimeType: "video/mp4", Size: 456}
	files.docs[991] = domain.Document{ID: 991, AccessHash: 1091, DCID: 2, MimeType: "audio/mpeg", Size: 789}

	embed := &tg.PageBlockEmbed{Caption: richEmptyCaption()}
	embed.SetPosterPhotoID(890)
	article := tg.PageRelatedArticle{URL: "https://example.test/article", WebpageID: 1}
	article.SetPhotoID(891)
	blocks := []tg.PageBlockClass{
		&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "media closure"}},
		&tg.PageBlockCollage{
			Items: []tg.PageBlockClass{
				&tg.PageBlockPhoto{PhotoID: 889, Caption: richEmptyCaption()},
				&tg.PageBlockVideo{VideoID: 990, Caption: richEmptyCaption()},
			},
			Caption: richEmptyCaption(),
		},
		&tg.PageBlockDetails{
			Title: &tg.TextPlain{Text: "nested"},
			Blocks: []tg.PageBlockClass{
				&tg.PageBlockAudio{AudioID: 991, Caption: richEmptyCaption()},
				embed,
				&tg.PageBlockRelatedArticles{
					Title:    &tg.TextPlain{Text: "related"},
					Articles: []tg.PageRelatedArticle{article},
				},
				&tg.PageBlockEmbedPost{
					URL:           "https://example.test/post",
					WebpageID:     2,
					AuthorPhotoID: 890,
					Author:        "author",
					Blocks: []tg.PageBlockClass{
						&tg.PageBlockPhoto{PhotoID: 889, Caption: richEmptyCaption()},
					},
					Caption: richEmptyCaption(),
				},
			},
		},
	}

	rich, err := r.domainRichMessageFromInput(ctx, &tg.InputRichMessage{Blocks: blocks})
	if err != nil {
		t.Fatalf("resolve block media closure: %v", err)
	}
	if got := []int64{rich.Photos[0].ID, rich.Photos[1].ID, rich.Photos[2].ID}; !slicesEqual(got, []int64{889, 890, 891}) {
		t.Fatalf("photo closure = %v, want [889 890 891]", got)
	}
	if got := []int64{rich.Documents[0].ID, rich.Documents[1].ID}; !slicesEqual(got, []int64{990, 991}) {
		t.Fatalf("document closure = %v, want [990 991]", got)
	}
}

func TestRichMessageMediaClosureRejectsUnknownReference(t *testing.T) {
	r, _, _ := newMediaTestRouter(t)
	_, err := r.domainRichMessageFromInput(context.Background(), &tg.InputRichMessage{
		Blocks: []tg.PageBlockClass{
			&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "missing photo"}},
			&tg.PageBlockPhoto{PhotoID: 999, Caption: richEmptyCaption()},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "PHOTO_INVALID") {
		t.Fatalf("unknown rich photo error = %v, want PHOTO_INVALID", err)
	}
}

func TestChannelRichPhotoHistoryExactLayerRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, owner, channel := newRichChannelTestRouter(t)
	files := r.deps.Files.(*fakeFiles)
	files.photos[889] = domain.Photo{
		ID: 889, AccessHash: 42, FileReference: []byte{1, 2, 3}, Date: 1700000000, DCID: 2,
		Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600, Size: 123}},
	}
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}
	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     peer,
		RandomID: 7202,
		RichMessage: &tg.InputRichMessage{
			Blocks: []tg.PageBlockClass{
				&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "Android full-screen photo"}},
				&tg.PageBlockPhoto{PhotoID: 889, Caption: richEmptyCaption()},
			},
			// Deliberately omit Photos: the PageBlock graph is the authoritative
			// selection and the uploaded server object completes the output closure.
		},
	})
	if err != nil {
		t.Fatalf("send channel rich photo: %v", err)
	}
	assertRichPhotoReference(t, "channel echo", newMessageFromUpdates(t, updates), 889)

	historyList, err := r.deps.Channels.GetHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channel.ID,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("channel history: %v", err)
	}
	history := r.tgChannelHistoryMessages(WithUserID(ctx, owner.ID), owner.ID, historyList)
	stored := singleChannelStoredMessage(t, history)
	assertRichPhotoReference(t, "channel history", stored, 889)

	for _, profile := range []tlprofile.Profile{tlprofile.Profile227, tlprofile.Profile228} {
		var encoded bin.Buffer
		if err := tlprofile.EncodeObject(profile, stored, &encoded); err != nil {
			t.Fatalf("layer %d encode: %v", profile, err)
		}
		decoded, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: encoded.Copy()}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("layer %d decode: %v", profile, err)
		}
		message, ok := decoded.(*tg.Message)
		if !ok {
			t.Fatalf("layer %d decoded %T, want *tg.Message", profile, decoded)
		}
		assertRichPhotoReference(t, "exact layer", message, 889)
	}
}

func assertRichPhotoReference(t *testing.T, label string, message *tg.Message, photoID int64) {
	t.Helper()
	rich, ok := message.GetRichMessage()
	if !ok {
		t.Fatalf("%s: missing rich_message", label)
	}
	var referenced bool
	for _, block := range rich.Blocks {
		if photo, ok := block.(*tg.PageBlockPhoto); ok && photo.PhotoID == photoID {
			referenced = true
			break
		}
	}
	if !referenced {
		t.Fatalf("%s: missing pageBlockPhoto(%d)", label, photoID)
	}
	if len(rich.Photos) != 1 {
		t.Fatalf("%s: photos = %d, want 1", label, len(rich.Photos))
	}
	photo, ok := rich.Photos[0].(*tg.Photo)
	if !ok || photo.ID != photoID {
		t.Fatalf("%s: photo = %#v, want id %d", label, rich.Photos[0], photoID)
	}
}

func slicesEqual(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// singleStoredMessage extracts the single non-empty *tg.Message from messages.messages.
func singleStoredMessage(t *testing.T, res tg.MessagesMessagesClass) *tg.Message {
	t.Helper()
	box, ok := res.(*tg.MessagesMessages)
	if !ok || len(box.Messages) != 1 {
		t.Fatalf("messages = %T %+v, want one messages.messages", res, res)
	}
	msg, ok := box.Messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("stored message = %T, want *tg.Message", box.Messages[0])
	}
	return msg
}

func singleChannelStoredMessage(t *testing.T, res tg.MessagesMessagesClass) *tg.Message {
	t.Helper()
	box, ok := res.(*tg.MessagesChannelMessages)
	if !ok {
		t.Fatalf("channel messages = %T %+v, want messages.channelMessages", res, res)
	}
	var got *tg.Message
	for _, item := range box.Messages {
		msg, ok := item.(*tg.Message)
		if !ok {
			continue
		}
		if got != nil {
			t.Fatalf("channel messages = %+v, want one regular message", box.Messages)
		}
		got = msg
	}
	if got == nil {
		t.Fatalf("channel messages = %+v, want one regular message", box.Messages)
	}
	return got
}

func editMessageFromUpdates(t *testing.T, updates tg.UpdatesClass) *tg.Message {
	t.Helper()
	upd, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, u := range upd.Updates {
		if edit, ok := u.(*tg.UpdateEditMessage); ok {
			msg, ok := edit.Message.(*tg.Message)
			if !ok {
				t.Fatalf("edit message = %T, want *tg.Message", edit.Message)
			}
			return msg
		}
	}
	t.Fatal("no updateEditMessage found")
	return nil
}

func editChannelMessageFromUpdates(t *testing.T, updates tg.UpdatesClass) *tg.Message {
	t.Helper()
	upd, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, u := range upd.Updates {
		if edit, ok := u.(*tg.UpdateEditChannelMessage); ok {
			msg, ok := edit.Message.(*tg.Message)
			if !ok {
				t.Fatalf("edit channel message = %T, want *tg.Message", edit.Message)
			}
			return msg
		}
	}
	t.Fatal("no updateEditChannelMessage found")
	return nil
}

func newRichChannelTestRouter(t *testing.T) (*Router, domain.User, domain.Channel) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550009101", FirstName: "Owner"})
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	created, err := channelSvc.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Rich Channel",
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create rich channel: %v", err)
	}
	dialogStore := memory.NewDialogStore()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Dialogs:  appdialogs.NewService(dialogStore, channelStore),
		Files:    &fakeFiles{docs: map[int64]domain.Document{}, photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), clock.System)
	return r, owner, created.Channel
}

func newRichDraftTestRouter(t *testing.T) (*Router, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 31, Phone: "15550009201", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 32, Phone: "15550009202", FirstName: "Friend"})
	dialogStore := memory.NewDialogStore()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:   appusers.NewService(userStore),
		Dialogs: appdialogs.NewService(dialogStore, memory.NewChannelStore()),
		Files:   &fakeFiles{docs: map[int64]domain.Document{}, photos: map[int64]domain.Photo{}},
	}, zaptest.NewLogger(t), clock.System)
	return r, owner, friend
}

func TestStoredRichMessageExplicit229ProfileIsRetained(t *testing.T) {
	blocks := []tg.PageBlockClass{&tg.PageBlockBlockquote{Collapsed: true, Text: &tg.TextPlain{Text: "stored 229"}, Caption: &tg.TextEmpty{}}}
	var wire bin.Buffer
	if err := tlprofile.EncodePageBlockVector(tlprofile.Profile229, blocks, &wire); err != nil {
		t.Fatal(err)
	}
	rich := &domain.MessageRichMessage{BlocksLayer: 229, Blocks: wire.Copy()}
	got, err := tgRichMessage(rich)
	if err != nil {
		t.Fatal(err)
	}
	quote := got.Blocks[0].(*tg.PageBlockBlockquote)
	if !quote.Collapsed || quote.Text.(*tg.TextPlain).Text != "stored 229" || rich.BlocksLayer != 229 {
		t.Fatalf("229 snapshot changed: %+v", got)
	}
}
