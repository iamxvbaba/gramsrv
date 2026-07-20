package botapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"telesrv/internal/domain"
)

func TestParseBotAPIHTMLNestedUTF16LinksAndDate(t *testing.T) {
	plain, entities, err := parseBotAPIHTML(`<b>A <i>😀</i></b> <a href="tg://user?id=42">Alice</a> <tg-time unix="1700000000" format="wdT">now</tg-time>`)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "A 😀 Alice now" {
		t.Fatalf("plain = %q", plain)
	}
	want := []domain.MessageEntity{
		{Type: domain.MessageEntityBold, Offset: 0, Length: 4},
		{Type: domain.MessageEntityItalic, Offset: 2, Length: 2},
		{Type: domain.MessageEntityMentionName, Offset: 5, Length: 5, UserID: 42},
		{Type: domain.MessageEntityFormattedDate, Offset: 11, Length: 3, Date: 1700000000, DayOfWeek: true, ShortDate: true, LongTime: true},
	}
	if !reflect.DeepEqual(entities, want) {
		t.Fatalf("entities = %#v, want %#v", entities, want)
	}
}

func TestParseBotAPIHTMLPreAndEscapes(t *testing.T) {
	plain, entities, err := parseBotAPIHTML(`<pre><code class="language-go">if a &lt; b &amp;&amp; b &gt; c</code></pre>`)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "if a < b && b > c" {
		t.Fatalf("plain = %q", plain)
	}
	want := []domain.MessageEntity{{Type: domain.MessageEntityPre, Offset: 0, Length: 17, Language: "go"}}
	if !reflect.DeepEqual(entities, want) {
		t.Fatalf("entities = %#v, want %#v", entities, want)
	}
}

func TestParseBotAPILegacyMarkdown(t *testing.T) {
	plain, entities, err := parseBotAPIMarkdown(`*bold* _😀_ [site](https://example.com) \*raw\*`)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "bold 😀 site *raw*" {
		t.Fatalf("plain = %q", plain)
	}
	want := []domain.MessageEntity{
		{Type: domain.MessageEntityBold, Offset: 0, Length: 4},
		{Type: domain.MessageEntityItalic, Offset: 5, Length: 2},
		{Type: domain.MessageEntityTextURL, Offset: 8, Length: 4, URL: "https://example.com"},
	}
	if !reflect.DeepEqual(entities, want) {
		t.Fatalf("entities = %#v, want %#v", entities, want)
	}
}

func TestParseBotAPIMarkdownV2NestedLinksAndExpandableQuote(t *testing.T) {
	plain, entities, err := parseBotAPIMarkdownV2(`*bold _😀_* [site](https://example.com/a\)b) ||secret||`)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "bold 😀 site secret" {
		t.Fatalf("plain = %q", plain)
	}
	want := []domain.MessageEntity{
		{Type: domain.MessageEntityBold, Offset: 0, Length: 7},
		{Type: domain.MessageEntityItalic, Offset: 5, Length: 2},
		{Type: domain.MessageEntityTextURL, Offset: 8, Length: 4, URL: "https://example.com/a)b"},
		{Type: domain.MessageEntitySpoiler, Offset: 13, Length: 6},
	}
	if !reflect.DeepEqual(entities, want) {
		t.Fatalf("entities = %#v, want %#v", entities, want)
	}

	plain, entities, err = parseBotAPIMarkdownV2(">visible\n>hidden||")
	if err != nil {
		t.Fatal(err)
	}
	if plain != "visible\nhidden" || !reflect.DeepEqual(entities, []domain.MessageEntity{{Type: domain.MessageEntityBlockquote, Offset: 0, Length: 14, Collapsed: true}}) {
		t.Fatalf("expandable quote plain=%q entities=%#v", plain, entities)
	}
}

func TestParseBotAPIMarkdownV2FormattedDate(t *testing.T) {
	plain, entities, err := parseBotAPIMarkdownV2(`![when](tg://time?unix=1700000000&format=wdT)`)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.MessageEntity{{
		Type: domain.MessageEntityFormattedDate, Offset: 0, Length: 4, Date: 1700000000,
		DayOfWeek: true, ShortDate: true, LongTime: true,
	}}
	if plain != "when" || !reflect.DeepEqual(entities, want) {
		t.Fatalf("plain=%q entities=%#v", plain, entities)
	}
}

func TestBotAPIFormattedTextPrecedenceAndEntityBounds(t *testing.T) {
	plain, entities, err := botAPIFormattedTextRaw(`<b>ok</b>`, " HTML ", `{not json`, domain.MaxMessageTextLength, true)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "ok" || !reflect.DeepEqual(entities, []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 2}}) {
		t.Fatalf("plain=%q entities=%#v", plain, entities)
	}

	for name, raw := range map[string]string{
		"unterminated HTML":   `<b>broken`,
		"reserved MarkdownV2": `plain-text`,
	} {
		t.Run(name, func(t *testing.T) {
			mode := "HTML"
			if strings.Contains(name, "MarkdownV2") {
				mode = "MarkdownV2"
			}
			if _, _, err := botAPIFormattedTextRaw(raw, mode, "", domain.MaxMessageTextLength, true); err == nil || !strings.Contains(err.Error(), "Can't parse entities") {
				t.Fatalf("error = %v", err)
			}
		})
	}

	_, _, err = botAPIFormattedText("😀x", "", []apiMessageEntity{{Type: "bold", Offset: 1, Length: 1}}, domain.MaxMessageTextLength, true)
	if err == nil || err.Error() != "ENTITY_BOUNDS_INVALID" {
		t.Fatalf("surrogate-split error = %v", err)
	}
	_, _, err = botAPIFormattedText("abcdef", "", []apiMessageEntity{
		{Type: "bold", Offset: 0, Length: 4},
		{Type: "italic", Offset: 2, Length: 4},
	}, domain.MaxMessageTextLength, true)
	if err == nil || err.Error() != "ENTITY_BOUNDS_INVALID" {
		t.Fatalf("crossing error = %v", err)
	}
}

func TestBotAPIExplicitExtendedEntitiesRoundTrip(t *testing.T) {
	input := []apiMessageEntity{
		{Type: "expandable_blockquote", Offset: 0, Length: 4},
		{Type: "date_time", Offset: 5, Length: 4, UnixTime: 1700000000, DateTimeFormat: "wdT"},
		{Type: "bank_card_number", Offset: 10, Length: 4},
	}
	_, entities, err := botAPIFormattedText("text when 1234", "", input, domain.MaxMessageTextLength, true)
	if err != nil {
		t.Fatal(err)
	}
	projected := apiMessageEntities(entities, nil)
	if projected[0]["type"] != "expandable_blockquote" || projected[1]["type"] != "date_time" || projected[1]["unix_time"] != 1700000000 || projected[1]["date_time_format"] != "wdT" || projected[2]["type"] != "bank_card_number" {
		t.Fatalf("projected = %#v", projected)
	}
}

func TestBotAPIInlineAndNestedMediaUseFormattedTextParser(t *testing.T) {
	payload := apiInlineResult{InputMessageContent: json.RawMessage(`{
		"message_text":"<b>inline</b>",
		"parse_mode":"HTML",
		"entities":[{"type":"bold","offset":999,"length":1}]
	}`)}
	message, entities, _, err := inputTextMessageContentFromAPI(payload)
	if err != nil {
		t.Fatal(err)
	}
	if message != "inline" || !reflect.DeepEqual(entities, []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 6}}) {
		t.Fatalf("inline message=%q entities=%#v", message, entities)
	}

	fileID := encodeBotAPIFileID("photo:7002:m")
	raw, _ := json.Marshal(map[string]any{
		"type": "photo", "media": fileID, "caption": "_media_", "parse_mode": "MarkdownV2",
	})
	var input domain.BotAPIEphemeralEditInput
	if err := parseEphemeralEditMedia(string(raw), &input); err != nil {
		t.Fatal(err)
	}
	if input.Fields.Message != "media" || !reflect.DeepEqual(input.Fields.Entities, []domain.MessageEntity{{Type: domain.MessageEntityItalic, Offset: 0, Length: 5}}) {
		t.Fatalf("media fields=%#v", input.Fields)
	}
}

func TestBotAPIFormattedTextIsUsedByAllMessageEntryPoints(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{
		self:             domain.User{ID: 1001, FirstName: "Bot", Bot: true},
		sendMessage:      domain.Message{ID: 1, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001}, From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001}, Body: "hello"},
		sendMediaMessage: domain.Message{ID: 2, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001}, From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001}, Body: "caption"},
		editMessage:      domain.Message{ID: 3, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001}, From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001}, Body: "edited"},
		ephemeralMessage: domain.EphemeralMessage{ID: 4, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001}, SenderUserID: 1001, ReceiverUserID: 2001, Date: 1_900_000_000, Content: domain.EphemeralContent{Message: "ephemeral"}},
	}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := performBotAPIRequest(t, h, bots.profile, "sendMessage", `{"chat_id":2001,"text":"<b>hello</b>","parse_mode":"HTML"}`)
	if rec.Code != http.StatusOK || gateway.sendText != "hello" || len(gateway.sendEntities) != 1 || gateway.sendEntities[0].Type != domain.MessageEntityBold {
		t.Fatalf("sendMessage status=%d body=%s text=%q entities=%#v", rec.Code, rec.Body.String(), gateway.sendText, gateway.sendEntities)
	}

	fileID := encodeBotAPIFileID("doc:7001")
	body, _ := json.Marshal(map[string]any{"chat_id": 2001, "document": fileID, "caption": "*caption*", "parse_mode": "MarkdownV2"})
	rec = performBotAPIRequest(t, h, bots.profile, "sendDocument", string(body))
	if rec.Code != http.StatusOK || gateway.sendMediaCaption != "caption" || len(gateway.sendMediaEntities) != 1 || gateway.sendMediaEntities[0].Type != domain.MessageEntityBold {
		t.Fatalf("sendDocument status=%d body=%s caption=%q entities=%#v", rec.Code, rec.Body.String(), gateway.sendMediaCaption, gateway.sendMediaEntities)
	}

	rec = performBotAPIRequest(t, h, bots.profile, "editMessageText", `{"chat_id":2001,"message_id":3,"text":"_edited_","parse_mode":"Markdown"}`)
	if rec.Code != http.StatusOK || gateway.editText != "edited" || len(gateway.editEntities) != 1 || gateway.editEntities[0].Type != domain.MessageEntityItalic {
		t.Fatalf("edit status=%d body=%s text=%q entities=%#v", rec.Code, rec.Body.String(), gateway.editText, gateway.editEntities)
	}

	rec = performBotAPIRequest(t, h, bots.profile, "sendMessage", `{"chat_id":-1000000003001,"receiver_user_id":2001,"text":"<u>ephemeral</u>","parse_mode":"HTML"}`)
	if rec.Code != http.StatusOK || len(gateway.ephemeralSends) == 0 {
		t.Fatalf("ephemeral status=%d body=%s", rec.Code, rec.Body.String())
	}
	lastSend := gateway.ephemeralSends[len(gateway.ephemeralSends)-1]
	if lastSend.Text != "ephemeral" || len(lastSend.Entities) != 1 || lastSend.Entities[0].Type != domain.MessageEntityUnderline {
		t.Fatalf("ephemeral status=%d body=%s input=%#v", rec.Code, rec.Body.String(), lastSend)
	}

	rec = performBotAPIRequest(t, h, bots.profile, "editEphemeralMessageCaption", `{"chat_id":-1000000003001,"receiver_user_id":2001,"ephemeral_message_id":4,"caption":"<s>caption</s>","parse_mode":"HTML"}`)
	if rec.Code != http.StatusOK || len(gateway.ephemeralEdits) == 0 {
		t.Fatalf("ephemeral edit status=%d body=%s", rec.Code, rec.Body.String())
	}
	lastEdit := gateway.ephemeralEdits[len(gateway.ephemeralEdits)-1]
	if lastEdit.Fields.Message != "caption" || len(lastEdit.Fields.Entities) != 1 || lastEdit.Fields.Entities[0].Type != domain.MessageEntityStrike {
		t.Fatalf("ephemeral edit status=%d body=%s input=%#v", rec.Code, rec.Body.String(), lastEdit)
	}
}

func FuzzBotAPIFormattedTextParsersNeverPanic(f *testing.F) {
	for _, seed := range []string{"", "plain", "<b>x</b>", "<", "&broken", "*x*", "_", ">quote\n>hidden||", "![x](tg://emoji?id=1)", "😀"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 || !utf8.ValidString(input) {
			return
		}
		_, _, _ = parseBotAPIHTML(input)
		_, _, _ = parseBotAPIMarkdown(input)
		_, _, _ = parseBotAPIMarkdownV2(input)
	})
}

func TestBotAPIHTMLParseFailureIsAtomic(t *testing.T) {
	bots := &fakeBotAPIBots{profile: domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}}
	gateway := &fakeBotAPIGateway{}
	h := (&handler{bots: bots, gateway: gateway}).routes()
	rec := performBotAPIRequest(t, h, bots.profile, "sendMessage", `{"chat_id":2001,"text":"<b>broken","parse_mode":"HTML"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Can't parse entities") || gateway.sendCalled {
		t.Fatalf("status=%d body=%s gatewayCalled=%v", rec.Code, rec.Body.String(), gateway.sendCalled)
	}
}
