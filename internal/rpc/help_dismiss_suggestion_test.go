package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"
)

func TestHelpDismissSuggestionAndroidChangePhone(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 42)
	req := &tg.HelpDismissSuggestionRequest{
		Peer:       &tg.InputPeerEmpty{},
		Suggestion: "VALIDATE_PHONE_NUMBER",
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	enc, err := r.Dispatch(ctx, [8]byte{1, 2, 3}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	box, ok := enc.(*tg.BoolBox)
	if !ok {
		t.Fatalf("response = %T, want *tg.BoolBox", enc)
	}
	if _, ok := box.Bool.(*tg.BoolTrue); !ok {
		t.Fatalf("bool response = %T, want BoolTrue", box.Bool)
	}
}

func TestHelpDismissSuggestionRequiresAuthorization(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.HelpDismissSuggestionRequest{Peer: &tg.InputPeerEmpty{}, Suggestion: "VALIDATE_PHONE_NUMBER"}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := r.Dispatch(context.Background(), [8]byte{1}, 77, &in); !tgerr.Is(err, "AUTH_KEY_UNREGISTERED") {
		t.Fatalf("unauthorized err = %v", err)
	}
}

func TestHelpDismissSuggestionEmptyIsFalse(t *testing.T) {
	r := &Router{}
	ok, err := r.onHelpDismissSuggestion(WithUserID(context.Background(), 42), &tg.HelpDismissSuggestionRequest{Peer: &tg.InputPeerEmpty{}})
	if err != nil || ok {
		t.Fatalf("empty suggestion result=%v err=%v", ok, err)
	}
}
