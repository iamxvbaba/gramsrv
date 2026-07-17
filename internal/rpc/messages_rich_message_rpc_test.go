package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// richTextBlocks 构造一组纯文本 IV 页面块，用于富文本往返断言。
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

// assertRichTextBlocks 校验投影出的 RichMessage 携带 richTextBlocks 的两个块（标题+段落）。
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
	r := &Router{}
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

// TestSendMessageRichMessageTextBlocksRoundTrip 验证 Layer 227 富文本（inputRichMessage 的
// blocks 形态）经 send → 发送方 echo / getMessages / getRichMessage 全链路原样往返。
func TestSendMessageRichMessageTextBlocksRoundTrip(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:  "rich",
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

	// getMessages（发送方按 box id 拉取）也应带富文本。
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

	// getRichMessage（按 peer+id 拉取完整富文本）应带富文本。
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

// TestSendMessageRichOnlyHeadingTableRoundTrip 覆盖 TDesktop rich editor 的真实发送形态：
// messages.sendMessage 带 f_rich_message（标题与表格 blocks），但 message:string 为空。
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

// TestGetRichMessageWrongPeerReturnsEmpty 验证 getRichMessage 的 peer 校验：用不匹配的 peer
// 拉取应返回 messageEmpty（不跨会话泄漏）。
func TestGetRichMessageWrongPeerReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)

	updates, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:        &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash},
		Message:     "rich",
		RandomID:    7002,
		RichMessage: &tg.InputRichMessage{Rtl: true, Blocks: richTextBlocks()},
	})
	if err != nil {
		t.Fatalf("send rich message: %v", err)
	}
	echo := newMessageFromUpdates(t, updates)

	// 用 self peer（≠ 该消息盒的 peer=friend）拉取 → messageEmpty。
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

// TestSendMessageRichMessageEmbeddedPhoto 验证富文本内嵌图片：按 id 解析为媒体快照存储，
// 投影时复用 tgPhoto 还原。
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
		Message:  "rich+photo",
		RandomID: 7003,
		RichMessage: &tg.InputRichMessage{
			Blocks: []tg.PageBlockClass{&tg.PageBlockParagraph{Text: &tg.TextPlain{Text: "see photo"}}},
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
}

// singleStoredMessage 从 messages.messages 取出唯一一条非空 *tg.Message。
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
