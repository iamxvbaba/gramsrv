package tlprofile

import (
	"fmt"

	"github.com/iamxvbaba/td/tg"
)

// Historical verification descriptions have no entity field. Accept only a
// lossless plain-text projection instead of silently discarding entity meaning.
func layerVerificationPlainText(value tg.TextWithEntities) (string, error) {
	if len(value.Entities) != 0 {
		return "", fmt.Errorf("historical bot verification cannot represent text entities")
	}
	return value.Text, nil
}

func layerBotVerificationTextEncode(_ LayerProfile, _ *tg.BotVerification, value tg.TextWithEntities) (string, error) {
	return layerVerificationPlainText(value)
}
func layerBotVerificationTextDecode(_ LayerProfile, _ *tg.BotVerification, _ bool, value string) (tg.TextWithEntities, error) {
	return tg.TextWithEntities{Text: value}, nil
}
func layerBotVerifierSettingsTextEncode(_ LayerProfile, _ *tg.BotVerifierSettings, present bool, value tg.TextWithEntities) (string, bool, error) {
	if !present {
		return "", false, nil
	}
	text, err := layerVerificationPlainText(value)
	return text, true, err
}
func layerBotVerifierSettingsTextDecode(_ LayerProfile, _ *tg.BotVerifierSettings, present bool, value string) (tg.TextWithEntities, bool, error) {
	if !present {
		return tg.TextWithEntities{}, false, nil
	}
	return tg.TextWithEntities{Text: value}, true, nil
}
func layerBotsSetCustomVerificationTextEncode(_ LayerProfile, _ *tg.BotsSetCustomVerificationRequest, present bool, value tg.TextWithEntities) (string, bool, error) {
	if !present {
		return "", false, nil
	}
	text, err := layerVerificationPlainText(value)
	return text, true, err
}
func layerBotsSetCustomVerificationTextDecode(_ LayerProfile, _ *tg.BotsSetCustomVerificationRequest, present bool, value string) (tg.TextWithEntities, bool, error) {
	if !present {
		return tg.TextWithEntities{}, false, nil
	}
	return tg.TextWithEntities{Text: value}, true, nil
}
