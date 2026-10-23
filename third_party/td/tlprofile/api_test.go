package tlprofile_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

func TestDispatcherBindsResultToExactAdmission(t *testing.T) {
	dispatcher := tlprofile.NewDispatcher()
	if err := dispatcher.Register(tlprofile.SemanticMethodHelpGetConfig, func(_ context.Context, request bin.Object) (any, error) {
		if _, ok := request.(*tg.HelpGetConfigRequest); !ok {
			t.Fatalf("request type = %T", request)
		}
		return &tg.Config{Date: 1, Expires: 2, ThisDC: 1}, nil
	}); err != nil {
		t.Fatal(err)
	}

	var wire bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&wire); err != nil {
		t.Fatal(err)
	}
	admission, err := dispatcher.Admit(tlprofile.Profile225, &wire, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), admission)
	if err != nil {
		t.Fatal(err)
	}
	var got bin.Buffer
	if err := result.Encode(&got); err != nil {
		t.Fatal(err)
	}
	var want bin.Buffer
	if err := admission.Call().EncodeResult(&tg.Config{Date: 1, Expires: 2, ThisDC: 1}, &want); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Raw(), want.Raw()) {
		t.Fatalf("result bytes differ: got=%x want=%x", got.Raw(), want.Raw())
	}
	if result.Prepared().Identity() != admission.Prepared().Identity() {
		t.Fatal("result lost exact admission identity")
	}
	if _, err := dispatcher.Dispatch(context.Background(), admission); err == nil {
		t.Fatal("copied admission dispatched more than once")
	}
}

func TestObjectCodecExactCoreRoundTrip(t *testing.T) {
	value := &tg.UpdateUserName{UserID: 42, FirstName: "A", LastName: "B", Usernames: []tg.Username{}}
	var got bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile225, value, &got); err != nil {
		t.Fatal(err)
	}
	decoded, err := tlprofile.DecodeObject(tlprofile.Profile225, &bin.Buffer{Buf: got.Copy()}, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.(*tg.UpdateUserName); !ok {
		t.Fatalf("decoded type = %T", decoded)
	}
	var roundTrip bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile225, decoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Raw(), roundTrip.Raw()) {
		t.Fatalf("object round-trip differs: got=%x want=%x", roundTrip.Raw(), got.Raw())
	}
}
