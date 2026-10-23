package markup

import "github.com/iamxvbaba/td/tg"

// ForceReply creates markup to force the user to send a reply.
func ForceReply(singleUse, selective bool) tg.ReplyMarkupClass {
	return &tg.ReplyKeyboardForceReply{
		SingleUse: singleUse,
		Selective: selective,
	}
}
