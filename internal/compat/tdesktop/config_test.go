package tdesktop

import (
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
)

func TestBuildConfigIncludesDefaultReaction(t *testing.T) {
	config := BuildConfig(2, "127.0.0.1", 2398, time.Unix(1, 0), "https://sgq.me", "https://updates.example.test/root/")
	reaction, ok := config.GetReactionsDefault()
	if !ok {
		t.Fatal("reactions_default is absent")
	}
	emoji, ok := reaction.(*tg.ReactionEmoji)
	if !ok || emoji.Emoticon != DefaultReactionEmoticon {
		t.Fatalf("reactions_default = %#v, want %q emoji", reaction, DefaultReactionEmoticon)
	}
	if got, ok := config.GetAutoupdateURLPrefix(); !ok || got != "https://updates.example.test/root" {
		t.Fatalf("autoupdate_url_prefix = %q, %v", got, ok)
	}
}

func TestBuildConfigAdvertisesCanonicalPrimaryDC(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want string
		ipv6 bool
	}{
		{name: "ipv4", ip: "192.0.2.10", want: "192.0.2.10"},
		{name: "ipv6", ip: "2001:0db8::1", want: "2001:db8::1", ipv6: true},
		{name: "mapped ipv4", ip: "::ffff:192.0.2.10", want: "192.0.2.10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := BuildConfig(2, tt.ip, 2398, time.Unix(1, 0), "https://sgq.me", "")
			if len(config.DCOptions) != 1 {
				t.Fatalf("len(DCOptions) = %d, want 1", len(config.DCOptions))
			}
			option := config.DCOptions[0]
			if option.ID != 2 || option.IPAddress != tt.want || option.Port != 2398 || option.Ipv6 != tt.ipv6 {
				t.Fatalf("DCOptions[0] = %+v, want dc=2 ip=%q port=2398 ipv6=%v", option, tt.want, tt.ipv6)
			}
			if option.MediaOnly || option.CDN || option.TCPObfuscatedOnly || option.Static || option.ThisPortOnly {
				t.Fatalf("DCOptions[0] has unexpected restrictive flags: %+v", option)
			}
		})
	}
}
