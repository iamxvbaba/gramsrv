package tdesktop

import (
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

func TestBuildConfigIncludesDefaultReaction(t *testing.T) {
	config := BuildConfig(2, "127.0.0.1", 2398, time.Unix(1, 0), "https://telesrv.net")
	reaction, ok := config.GetReactionsDefault()
	if !ok {
		t.Fatal("reactions_default is absent")
	}
	emoji, ok := reaction.(*tg.ReactionEmoji)
	if !ok || emoji.Emoticon != DefaultReactionEmoticon {
		t.Fatalf("reactions_default = %#v, want %q emoji", reaction, DefaultReactionEmoticon)
	}
}
