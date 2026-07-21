package domain

import (
	"errors"
	"strings"
	"testing"
)

func cb(text string, data []byte) MarkupButton {
	return MarkupButton{Type: MarkupButtonCallback, Text: text, Data: data}
}

func TestValidateReplyMarkup(t *testing.T) {
	tests := []struct {
		name string
		m    *MessageReplyMarkup
		want error
	}{
		{"nil ok", nil, nil},
		{"empty ok", &MessageReplyMarkup{}, nil},
		{"callback ok", &MessageReplyMarkup{Inline: [][]MarkupButton{{cb("ok", []byte("d"))}}}, nil},
		{"callback 64B ok", &MessageReplyMarkup{Inline: [][]MarkupButton{{cb("ok", make([]byte, 64))}}}, nil},
		{"callback 65B bad", &MessageReplyMarkup{Inline: [][]MarkupButton{{cb("ok", make([]byte, 65))}}}, ErrButtonDataInvalid},
		{"empty text bad", &MessageReplyMarkup{Inline: [][]MarkupButton{{cb("", []byte("d"))}}}, ErrButtonInvalid},
		{"url https ok", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonURL, Text: "go", URL: "https://example.com/x"}}}}, nil},
		{"url http bad", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonURL, Text: "go", URL: "http://example.com"}}}}, ErrButtonURLInvalid},
		{"url javascript bad", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonURL, Text: "go", URL: "javascript:alert(1)"}}}}, ErrButtonURLInvalid},
		{"url empty bad", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonURL, Text: "go", URL: ""}}}}, ErrButtonURLInvalid},
		{"login url loopback http ok", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonLoginURL, Text: "login", URL: "http://127.0.0.1:8080/login"}}}}, nil},
		{"login url localhost http ok", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonLoginURL, Text: "login", URL: "http://localhost:8080/login"}}}}, nil},
		{"login url public http host ok", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonLoginURL, Text: "login", URL: "http://example.com:3000/login"}}}}, nil},
		{"login url public http ip ok", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonLoginURL, Text: "login", URL: "http://192.0.2.25:18080/login"}}}}, nil},
		{"login url credentials bad", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: MarkupButtonLoginURL, Text: "login", URL: "https://user@example.com/login"}}}}, ErrButtonURLInvalid},
		{"unknown type bad", &MessageReplyMarkup{Inline: [][]MarkupButton{{{Type: "rainbow", Text: "x"}}}}, ErrButtonTypeInvalid},
		{"reply keyboard ok", &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: "Help"}}}, Resize: true, Persistent: true, Placeholder: "Choose"}, nil},
		{"reply keyboard semantic style ok", &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: "Delete", Style: MarkupButtonStyleDanger, IconCustomEmojiID: 123}}}}, nil},
		{"inline semantic style ok", &MessageReplyMarkup{Type: MessageReplyMarkupInline, Inline: [][]MarkupButton{{{Type: MarkupButtonCallback, Text: "Confirm", Data: []byte("yes"), Style: MarkupButtonStyleSuccess}}}}, nil},
		{"unknown semantic style bad", &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: "Odd", Style: "rainbow"}}}}, ErrButtonInvalid},
		{"negative custom emoji bad", &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: "Odd", IconCustomEmojiID: -1}}}}, ErrButtonInvalid},
		{"reply keyboard callback bad", &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{cb("wrong", []byte("d"))}}}, ErrButtonTypeInvalid},
		{"reply keyboard empty bad", &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard}, ErrButtonInvalid},
		{"reply keyboard placeholder too long", &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: "Help"}}}, Placeholder: strings.Repeat("p", MaxReplyKeyboardPlaceholderLen+1)}, ErrButtonInvalid},
		{"reply keyboard text too long", &MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: strings.Repeat("x", MaxReplyKeyboardButtonTextLen+1)}}}}, ErrButtonInvalid},
		{"hide keyboard ok", &MessageReplyMarkup{Type: MessageReplyMarkupHide, Selective: true}, nil},
		{"force reply ok", &MessageReplyMarkup{Type: MessageReplyMarkupForceReply, SingleUse: true, Placeholder: "Answer"}, nil},
		{"missing keyboard constructor", &MessageReplyMarkup{Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: "Help"}}}}, ErrButtonInvalid},
		{"inline constructor with keyboard payload", &MessageReplyMarkup{Type: MessageReplyMarkupInline, Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: "Help"}}}}, ErrButtonInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateReplyMarkup(tt.m); !errors.Is(err, tt.want) {
				t.Fatalf("ValidateReplyMarkup = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidateReplyMarkupLimits(t *testing.T) {
	// 行数上限。
	tooManyRows := &MessageReplyMarkup{Inline: make([][]MarkupButton, MaxMarkupRows+1)}
	for i := range tooManyRows.Inline {
		tooManyRows.Inline[i] = []MarkupButton{cb("x", []byte("d"))}
	}
	if err := ValidateReplyMarkup(tooManyRows); !errors.Is(err, ErrButtonInvalid) {
		t.Fatalf("rows over limit = %v, want ErrButtonInvalid", err)
	}
	// 单行按钮数上限。
	wideRow := make([]MarkupButton, MaxMarkupButtonsPerRow+1)
	for i := range wideRow {
		wideRow[i] = cb("x", []byte("d"))
	}
	if err := ValidateReplyMarkup(&MessageReplyMarkup{Inline: [][]MarkupButton{wideRow}}); !errors.Is(err, ErrButtonInvalid) {
		t.Fatalf("row width over limit = %v, want ErrButtonInvalid", err)
	}
	// 文本长度上限。
	longText := strings.Repeat("a", MaxMarkupButtonTextLen+1)
	if err := ValidateReplyMarkup(&MessageReplyMarkup{Inline: [][]MarkupButton{{cb(longText, []byte("d"))}}}); !errors.Is(err, ErrButtonInvalid) {
		t.Fatalf("text over limit = %v, want ErrButtonInvalid", err)
	}
}

func TestMessageReplyMarkupIsZero(t *testing.T) {
	if !(*MessageReplyMarkup)(nil).IsZero() {
		t.Fatal("nil markup must be zero")
	}
	if !(&MessageReplyMarkup{}).IsZero() {
		t.Fatal("empty markup must be zero")
	}
	if !(&MessageReplyMarkup{Inline: [][]MarkupButton{{}}}).IsZero() {
		t.Fatal("markup with only empty rows must be zero")
	}
	if (&MessageReplyMarkup{Inline: [][]MarkupButton{{cb("x", nil)}}}).IsZero() {
		t.Fatal("markup with a button must not be zero")
	}
	if (&MessageReplyMarkup{Type: MessageReplyMarkupKeyboard, Keyboard: [][]MarkupButton{{{Type: MarkupButtonText, Text: "x"}}}}).IsZero() {
		t.Fatal("reply keyboard with a button must not be zero")
	}
	if (&MessageReplyMarkup{Type: MessageReplyMarkupHide}).IsZero() {
		t.Fatal("hide keyboard constructor must not be zero")
	}
}
