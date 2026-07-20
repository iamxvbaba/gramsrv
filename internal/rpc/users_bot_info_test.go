package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

func TestTGBotInfoPreservesEphemeralCommandMarker(t *testing.T) {
	got := tgBotInfoFromProfile(42, domain.BotProfile{
		Commands: []domain.BotCommand{
			{Command: "public", Description: "visible everywhere"},
			{Command: "private", Description: "Layer 228 only", Ephemeral: true},
		},
	}, true)
	if len(got.Commands) != 2 {
		t.Fatalf("commands = %+v, want two", got.Commands)
	}
	if got.Commands[0].Ephemeral {
		t.Fatalf("public command = %+v, want ephemeral=false", got.Commands[0])
	}
	if !got.Commands[1].Ephemeral {
		t.Fatalf("private command = %+v, want ephemeral=true", got.Commands[1])
	}
}
