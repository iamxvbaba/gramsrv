package tlprofile

import (
	"errors"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestSparseClientRPCOverlayDirectAndAdmission(t *testing.T) {
	if got, want := ClientRPCOverlayMethodCount(ClientRPCOverlayDrkloAndroid), 17; got != want {
		t.Fatalf("DrKLO overlay methods = %d, want %d", got, want)
	}
	if got, want := ClientRPCOverlayMethodCount(ClientRPCOverlayDrkloAndroidTheme), 4; got != want {
		t.Fatalf("DrKLO theme overlay methods = %d, want %d", got, want)
	}

	private := drkloForwardMessagesBody()
	in := &bin.Buffer{Buf: private.Copy()}
	canonical, handled, err := AdaptClientRPCOverlayWithLimits(Profile227, ClientRPCOverlayDrkloAndroid, in, Limits{})
	if err != nil || !handled {
		t.Fatalf("direct overlay = handled:%v err:%v", handled, err)
	}
	if in.Len() != 0 {
		t.Fatalf("direct overlay left %d bytes", in.Len())
	}
	if id, err := canonical.PeekID(); err != nil || id != 0x13704a7c {
		t.Fatalf("canonical ID = %#x, %v", id, err)
	}

	d := NewDispatcher()
	fieldCalls := 0
	if err := d.OnFieldPreflight(FieldMessagesForwardMessagesID, func(view FieldView) error {
		fieldCalls++
		length, ok := view.VectorLength()
		if !ok || length != 0 {
			t.Fatalf("adapted ID vector = %d/%v", length, ok)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var captured UnknownMethodView
	d.OnUnknownMethod(func(view UnknownMethodView) (OutboundCall, bool, error) {
		captured = view
		return view.AdaptClientRPCOverlay(ClientRPCOverlayDrkloAndroid)
	})
	body := &bin.Buffer{Buf: private.Copy()}
	admitted, err := d.Admit(Profile227, body, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if body.Len() != 0 {
		t.Fatalf("overlay admission left %d bytes", body.Len())
	}
	if admitted.Call().Method() != SemanticMethodMessagesForwardMessages || admitted.Call().Profile() != Profile227 {
		t.Fatalf("adapted call = method %#x profile %d", admitted.Call().Method(), admitted.Call().Profile())
	}
	if fieldCalls != 1 {
		t.Fatalf("adapted canonical field callbacks = %d", fieldCalls)
	}
	if _, err := captured.Buffer(); err == nil {
		t.Fatal("unknown-method view remained active after callback")
	}
}

func TestSparseClientRPCOverlayInsideInvokeWithLayer(t *testing.T) {
	private := drkloForwardMessagesBody()
	var body bin.Buffer
	putInvokeWithLayer(&body, 227)
	body.Buf = append(body.Buf, private.Raw()...)
	d := NewDispatcher()
	d.OnUnknownMethod(func(view UnknownMethodView) (OutboundCall, bool, error) {
		return view.AdaptClientRPCOverlay(ClientRPCOverlayDrkloAndroid)
	})
	admitted, err := d.AdmitDefault(Profile228, &body, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Call().Profile() != Profile227 || admitted.WrapperCount() != 1 {
		t.Fatalf("wrapped overlay profile/wrappers = %d/%d", admitted.Call().Profile(), admitted.WrapperCount())
	}
	if evidence, ok := admitted.ProfileEvidence(); !ok || evidence != Profile227 {
		t.Fatalf("wrapped overlay evidence = %d/%v", evidence, ok)
	}
}

func TestSparseClientRPCOverlayCanReuseHistoricalOfficialWireID(t *testing.T) {
	// contacts.search#11f812d8 is official in Layers 225/226, while DrKLO keeps
	// using that older shape as a private overlay on Layers 227/228. Admission
	// must classify it against the effective profile before treating the global
	// schema-set constructor as an ordinary method.
	for _, profile := range []Profile{Profile227, Profile228} {
		profile := profile
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			var private bin.Buffer
			private.PutID(0x11f812d8)
			private.PutString("query")
			private.PutInt(20)

			d := NewDispatcher()
			d.OnUnknownMethod(func(view UnknownMethodView) (OutboundCall, bool, error) {
				return view.AdaptClientRPCOverlay(ClientRPCOverlayDrkloAndroid)
			})
			admitted, err := d.Admit(profile, &private, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if private.Len() != 0 {
				t.Fatalf("overlay admission left %d bytes", private.Len())
			}
			if got, want := admitted.Call().Method(), SemanticMethodContactsSearch; got != want {
				t.Fatalf("adapted method = %#x, want %#x", got, want)
			}
			if got, want := admitted.Call().WireID(), uint32(0x05f58d0f); got != want {
				t.Fatalf("adapted exact wire = %#08x, want %#08x", got, want)
			}
		})
	}
}

func TestSparseClientRPCOverlayRetainedJoinMethodsUseCurrentResult(t *testing.T) {
	tests := []struct {
		name          string
		privateWireID uint32
		method        SemanticID
		exactWireID   uint32
		body          func(*testing.T) bin.Buffer
	}{
		{
			name:          "messages.importChatInvite",
			privateWireID: 0x6c50051c,
			method:        SemanticMethodMessagesImportChatInvite,
			exactWireID:   0xde91436e,
			body: func(*testing.T) bin.Buffer {
				var body bin.Buffer
				body.PutID(0x6c50051c)
				body.PutString("private-invite")
				return body
			},
		},
		{
			name:          "channels.joinChannel",
			privateWireID: 0x24b524c5,
			method:        SemanticMethodChannelsJoinChannel,
			exactWireID:   0x7f6a1e22,
			body: func(t *testing.T) bin.Buffer {
				var body bin.Buffer
				body.PutID(0x24b524c5)
				if err := (&tg.InputChannel{ChannelID: 41, AccessHash: 42}).Encode(&body); err != nil {
					t.Fatal(err)
				}
				return body
			},
		},
	}
	for _, test := range tests {
		test := test
		for _, profile := range []Profile{Profile227, Profile228} {
			profile := profile
			t.Run(fmt.Sprintf("%s/layer_%d", test.name, profile), func(t *testing.T) {
				body := test.body(t)
				if wireID, err := body.PeekID(); err != nil || wireID != test.privateWireID {
					t.Fatalf("private wire = %#08x err=%v, want %#08x", wireID, err, test.privateWireID)
				}
				d := NewDispatcher()
				d.OnUnknownMethod(func(view UnknownMethodView) (OutboundCall, bool, error) {
					return view.AdaptClientRPCOverlay(ClientRPCOverlayDrkloAndroid)
				})
				admitted, err := d.Admit(profile, &body, Limits{})
				if err != nil {
					t.Fatal(err)
				}
				if body.Len() != 0 {
					t.Fatalf("overlay admission left %d bytes", body.Len())
				}
				if got := admitted.Call().Method(); got != test.method {
					t.Fatalf("adapted method = %#x, want %#x", got, test.method)
				}
				if got := admitted.Call().WireID(); got != test.exactWireID {
					t.Fatalf("adapted exact wire = %#08x, want %#08x", got, test.exactWireID)
				}
				var result bin.Buffer
				if err := admitted.Call().EncodeResult(&tg.MessagesChatInviteJoinResultOk{
					Updates: &tg.UpdatesTooLong{},
				}, &result); err != nil {
					t.Fatalf("encode current join result: %v", err)
				}
				if wireID, err := result.PeekID(); err != nil || wireID != 0x445663a7 {
					t.Fatalf("result wire = %#08x err=%v, want chatInviteJoinResultOk#445663a7", wireID, err)
				}
			})
		}
	}
}

func TestSparseClientRPCOverlayRejectsMalformedTransactionally(t *testing.T) {
	var malformed bin.Buffer
	malformed.PutID(0x41d41ade)
	before := malformed.Copy()
	d := NewDispatcher()
	d.OnUnknownMethod(func(view UnknownMethodView) (OutboundCall, bool, error) {
		return view.AdaptClientRPCOverlay(ClientRPCOverlayDrkloAndroid)
	})
	_, err := d.Admit(Profile228, &malformed, Limits{})
	if err == nil {
		t.Fatal("malformed private overlay was admitted")
	}
	if !errors.Is(err, ErrUnknownRPCMethod) && malformed.Len() != len(before) {
		t.Fatalf("malformed overlay error/input = %v/%d", err, malformed.Len())
	}
	if malformed.Len() != len(before) {
		t.Fatal("malformed private overlay consumed input")
	}
}

func drkloForwardMessagesBody() bin.Buffer {
	var private bin.Buffer
	private.PutID(0x41d41ade)
	private.PutInt(0)
	private.PutID(0x7f3b18ea)
	private.PutVectorHeader(0)
	private.PutVectorHeader(0)
	private.PutID(0x7f3b18ea)
	return private
}
