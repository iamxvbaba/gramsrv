package tlprofile

import (
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestOlderProfilesDropEphemeralBotCommandsFromVectors(t *testing.T) {
	commands := []tg.BotCommand{
		{Command: "public", Description: "visible everywhere"},
		{Flags: 1, Ephemeral: true, Command: "private", Description: "Layer 228 only"},
	}
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		t.Run("layer"+strconv.Itoa(int(profile)), func(t *testing.T) {
			request := &tg.BotsGetBotCommandsRequest{
				Scope:    &tg.BotCommandScopeDefault{},
				LangCode: "",
			}
			var canonical bin.Buffer
			if err := request.Encode(&canonical); err != nil {
				t.Fatal(err)
			}
			outbound, err := prepareSparseOutbound(profile, &canonical, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			var wire bin.Buffer
			if err := outbound.Encode(&wire); err != nil {
				t.Fatal(err)
			}
			admission, err := NewDispatcher().Admit(profile, &wire, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			var result bin.Buffer
			if err := admission.Call().EncodeResult(commands, &result); err != nil {
				t.Fatal(err)
			}
			got, err := decodeBotCommands(profile, &result)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if profile == Profile228 {
				want = 2
			}
			if len(got) != want {
				t.Fatalf("commands = %+v, want %d entries", got, want)
			}
			if got[0].Command != "public" {
				t.Fatalf("first command = %+v", got[0])
			}
			if profile == Profile228 && (!got[1].Ephemeral || got[1].Command != "private") {
				t.Fatalf("ephemeral command = %+v", got[1])
			}
		})
	}
}

func decodeBotCommands(profile Profile, input *bin.Buffer) ([]tg.BotCommand, error) {
	count, err := input.VectorHeader()
	if err != nil {
		return nil, err
	}
	result := make([]tg.BotCommand, 0, count)
	state := layerCodecState{}
	for range count {
		var value *tg.BotCommand
		if profile == Profile228 {
			value, err = layerDecodeWire9852d6d2(LayerProfile(profile), input, &state)
		} else {
			value, err = layerDecodeWirec27ac8c7(LayerProfile(profile), input, &state)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, *value)
	}
	return result, nil
}
