package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// A Layer 233 bot web widget has no wire identity before Layer 233. The server
// must project it to messageMediaUnsupported for 225-232 so a widget-only message
// reaches an older client as an unsupported-media placeholder, not empty text.
func TestLayer233WidgetMediaProjectsToUnsupportedForHistoricalProfiles(t *testing.T) {
	widget := &tg.MessageMediaWidget{Width: 320, Height: 200, Payload: []byte("<b>x</b>"), CanQuery: true}
	widget.SetWidgetID(7)
	message := func() *tg.Message {
		return &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 2}, Date: 3, Message: "widget", Media: widget}
	}

	for profile := tlprofile.Profile225; profile <= tlprofile.Profile232; profile++ {
		var buf bin.Buffer
		if err := tlprofile.EncodeObject(profile, message(), &buf); err != nil {
			t.Fatalf("profile %d encode: %v", profile, err)
		}
		decoded, err := tlprofile.DecodeObject(profile, &buf, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("profile %d decode: %v", profile, err)
		}
		msg, ok := decoded.(*tg.Message)
		if !ok || msg == nil {
			t.Fatalf("profile %d decoded %T, want *tg.Message", profile, decoded)
		}
		if _, ok := msg.Media.(*tg.MessageMediaUnsupported); !ok {
			t.Fatalf("profile %d widget media = %T, want *tg.MessageMediaUnsupported", profile, msg.Media)
		}
	}

	var buf bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile233, message(), &buf); err != nil {
		t.Fatalf("Layer 233 encode: %v", err)
	}
	decoded, err := tlprofile.DecodeObject(tlprofile.Profile233, &buf, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("Layer 233 decode: %v", err)
	}
	msg, ok := decoded.(*tg.Message)
	if !ok || msg == nil {
		t.Fatalf("Layer 233 decoded %T, want *tg.Message", decoded)
	}
	if _, ok := msg.Media.(*tg.MessageMediaWidget); !ok {
		t.Fatalf("Layer 233 widget media = %T, want *tg.MessageMediaWidget", msg.Media)
	}
}
