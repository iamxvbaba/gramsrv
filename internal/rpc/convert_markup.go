package rpc

import (
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// P3 reply_markup 错误码（对齐官方）。
func buttonDataInvalidErr() error { return tgerr.New(400, "BUTTON_DATA_INVALID") }
func buttonInvalidErr() error     { return tgerr.New(400, "BUTTON_INVALID") }
func buttonURLInvalidErr() error  { return tgerr.New(400, "BUTTON_URL_INVALID") }

// replyMarkupErr 把 domain 校验错误映射为客户端错误码。
func replyMarkupErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrButtonDataInvalid):
		return buttonDataInvalidErr()
	case errors.Is(err, domain.ErrButtonURLInvalid):
		return buttonURLInvalidErr()
	case errors.Is(err, domain.ErrButtonInvalid), errors.Is(err, domain.ErrButtonTypeInvalid):
		return buttonInvalidErr()
	default:
		return replyMarkupInvalidErr()
	}
}

// domainReplyMarkupForSender 解析入站 reply_markup。P3 语义：
//   - 仅 bot 账号下发的 markup 被接受；非 bot 一律丢弃（返回 nil，不报错——对齐
//     官方「普通用户 markup 无效」，I1）。
//   - 仅 ReplyInlineMarkup 被处理；reply keyboard 家族（自定义键盘/隐藏/强制回复）
//     P3 不支持，静默丢弃（不报错，避免破坏 bot 发送；记 P4）。
//   - inline 行内按钮仅 callback / url；其它按钮类型（webview/game/url_auth/
//     request_* 等）→ ErrButtonTypeInvalid（拒绝整条发送，绝不半实现下发）。
//   - data≤64B、行/按钮上限、url https 由 domain.ValidateReplyMarkup 校验。
func domainReplyMarkupForSender(markup tg.ReplyMarkupClass, senderIsBot bool) (*domain.MessageReplyMarkup, error) {
	if markup == nil || !senderIsBot {
		return nil, nil
	}
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok {
		// reply keyboard / hide / force-reply：P3 不支持，丢弃。
		return nil, nil
	}
	parsed, err := domainInlineMarkup(inline)
	if err != nil {
		return nil, err
	}
	if parsed.IsZero() {
		return nil, nil
	}
	if err := domain.ValidateReplyMarkup(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func domainInlineMarkup(inline *tg.ReplyInlineMarkup) (*domain.MessageReplyMarkup, error) {
	out := &domain.MessageReplyMarkup{Inline: make([][]domain.MarkupButton, 0, len(inline.Rows))}
	for _, row := range inline.Rows {
		domainRow := make([]domain.MarkupButton, 0, len(row.Buttons))
		for _, btn := range row.Buttons {
			db, err := domainMarkupButton(btn)
			if err != nil {
				return nil, err
			}
			domainRow = append(domainRow, db)
		}
		out.Inline = append(out.Inline, domainRow)
	}
	return out, nil
}

func domainMarkupButton(btn tg.KeyboardButtonClass) (domain.MarkupButton, error) {
	switch b := btn.(type) {
	case *tg.KeyboardButtonCallback:
		return domain.MarkupButton{
			Type:             domain.MarkupButtonCallback,
			Text:             b.Text,
			Data:             append([]byte(nil), b.Data...),
			RequiresPassword: b.RequiresPassword,
		}, nil
	case *tg.KeyboardButtonURL:
		return domain.MarkupButton{
			Type: domain.MarkupButtonURL,
			Text: b.Text,
			URL:  b.URL,
		}, nil
	default:
		// webview/game/url_auth/request_*/switch_inline/buy 等 P3 未实现按钮类型。
		return domain.MarkupButton{}, domain.ErrButtonTypeInvalid
	}
}

// tgReplyMarkup 把存储的 inline keyboard 快照还原为 tg.ReplyInlineMarkup。
func tgReplyMarkup(m *domain.MessageReplyMarkup) tg.ReplyMarkupClass {
	if m.IsZero() {
		return nil
	}
	rows := make([]tg.KeyboardButtonRow, 0, len(m.Inline))
	for _, row := range m.Inline {
		buttons := make([]tg.KeyboardButtonClass, 0, len(row))
		for _, btn := range row {
			buttons = append(buttons, tgMarkupButton(btn))
		}
		rows = append(rows, tg.KeyboardButtonRow{Buttons: buttons})
	}
	return &tg.ReplyInlineMarkup{Rows: rows}
}

func tgMarkupButton(btn domain.MarkupButton) tg.KeyboardButtonClass {
	switch btn.Type {
	case domain.MarkupButtonURL:
		return &tg.KeyboardButtonURL{Text: btn.Text, URL: btn.URL}
	default: // callback
		out := &tg.KeyboardButtonCallback{Text: btn.Text, Data: btn.Data}
		if btn.RequiresPassword {
			out.SetRequiresPassword(true)
		}
		return out
	}
}
