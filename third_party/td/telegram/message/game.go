package message

import "github.com/iamxvbaba/td/tg"

// Game adds a game attachment.
func Game(id tg.InputGameClass, caption ...StyledTextOption) MediaOption {
	return Media(&tg.InputMediaGame{
		ID: id,
	}, caption...)
}
