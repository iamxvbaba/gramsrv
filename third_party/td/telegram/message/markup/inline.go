package markup

import "github.com/iamxvbaba/td/tg"

// InlineRow creates inline keyboard with single row using given buttons.
func InlineRow(buttons ...tg.KeyboardInlineButton) tg.ReplyMarkupClass {
	return InlineKeyboard(tg.KeyboardInlineButtonRow{Buttons: buttons})
}

// InlineKeyboard creates inline keyboard using given rows.
func InlineKeyboard(rows ...tg.KeyboardInlineButtonRow) tg.ReplyMarkupClass {
	return &tg.ReplyInlineMarkup{
		Rows: rows,
	}
}
