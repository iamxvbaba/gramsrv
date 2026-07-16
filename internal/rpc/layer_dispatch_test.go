package rpc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/tlprofile"
	appfiles "telesrv/internal/app/files"
	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
)

func TestLayerAdmissionAndroidPrivateOverlayUsesExactProfile(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)

	// DrKLO's private messages.forwardMessages constructor has the canonical
	// body layout for this empty flags/vectors fixture, but is not an official
	// Layer 227 method id.
	var body bin.Buffer
	body.PutID(0x41d41ade)
	body.PutInt(0)
	body.PutID(0x7f3b18ea) // inputPeerEmpty
	body.PutVectorHeader(0)
	body.PutVectorHeader(0)
	body.PutID(0x7f3b18ea) // inputPeerEmpty

	admitted, err := r.AdmitLayer(tlprofile.Profile227, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if body.Len() != 0 {
		t.Fatalf("private request left %d bytes", body.Len())
	}
	call := admitted.Call()
	if call.Profile() != tlprofile.Profile227 || call.Method() != tlprofile.SemanticMethodMessagesForwardMessages {
		t.Fatalf("private admission = profile:%d method:%#x", call.Profile(), call.Method())
	}
	if want, ok := tlprofile.WireID(tlprofile.Profile227, call.Method()); !ok || call.WireID() != want {
		t.Fatalf("private admission wire id = %#x, want %#x (ok=%v)", call.WireID(), want, ok)
	}
}

func TestLayerAdmissionOfficialProfileOwnsOverlappingAndroidID(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	request := &tg.ContactsSearchRequest{Q: "exact", Limit: 20}
	body := encodeExactLayerRPC(t, tlprofile.Profile225, request)
	admitted, err := r.AdmitLayer(tlprofile.Profile225, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if body.Len() != 0 || admitted.Call().Method() != tlprofile.SemanticMethodContactsSearch {
		t.Fatalf("official overlap = remaining:%d method:%#x", body.Len(), admitted.Call().Method())
	}
}

func TestLayerAdmissionAndroidOverlayFailureDoesNotConsumeInput(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	bounded := bin.Buffer{Buf: androidPrivateForwardMessagesWire()}
	boundedOriginal := bounded.Copy()
	if _, err := r.AdmitLayer(tlprofile.Profile227, &bounded, tlprofile.Limits{MaxWireBytes: len(boundedOriginal) - 1}); err == nil {
		t.Fatal("oversized Android-private request was admitted")
	}
	if !bytes.Equal(bounded.Raw(), boundedOriginal) {
		t.Fatal("wire-limited Android-private request consumed or mutated caller input")
	}

	malformed := bin.Buffer{}
	malformed.PutID(0x41d41ade)
	original := malformed.Copy()
	if _, err := r.AdmitLayer(tlprofile.Profile227, &malformed, tlprofile.Limits{}); err == nil {
		t.Fatal("malformed Android-private request was admitted")
	}
	if string(malformed.Raw()) != string(original) {
		t.Fatal("failed Android-private admission consumed or mutated caller input")
	}

	unknown := bin.Buffer{}
	unknown.PutID(0xdeadbeef)
	original = unknown.Copy()
	if _, err := r.AdmitLayer(tlprofile.Profile227, &unknown, tlprofile.Limits{}); !errors.Is(err, tlprofile.ErrUnknownRPCMethod) {
		t.Fatalf("official unknown error = %v", err)
	}
	if string(unknown.Raw()) != string(original) {
		t.Fatal("unknown official request consumed or mutated caller input")
	}
}

func androidPrivateForwardMessagesWire() []byte {
	var body bin.Buffer
	body.PutID(0x41d41ade)
	body.PutInt(0)
	body.PutID(0x7f3b18ea) // inputPeerEmpty
	body.PutVectorHeader(0)
	body.PutVectorHeader(0)
	body.PutID(0x7f3b18ea) // inputPeerEmpty
	return body.Copy()
}

func replaceTerminalRPC(t *testing.T, encoded *bin.Buffer, terminal []byte) {
	t.Helper()
	var placeholder bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&placeholder); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(encoded.Raw(), placeholder.Raw()) {
		t.Fatalf("wrapper does not end in placeholder RPC: %x", encoded.Raw())
	}
	encoded.Buf = append(encoded.Buf[:encoded.Len()-placeholder.Len()], terminal...)
}

func TestLayerAdmissionAndroidPrivateInnermostAcrossWrappers(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	private := androidPrivateForwardMessagesWire()

	var withoutUpdates bin.Buffer
	withoutUpdates.PutID(tg.InvokeWithoutUpdatesRequestTypeID)
	withoutUpdates.Put(private)

	var afterMsg bin.Buffer
	afterMsg.PutID(tg.InvokeAfterMsgRequestTypeID)
	afterMsg.PutLong(11)
	afterMsg.Put(private)

	var afterMsgs bin.Buffer
	afterMsgs.PutID(tg.InvokeAfterMsgsRequestTypeID)
	afterMsgs.PutVectorHeader(2)
	afterMsgs.PutLong(11)
	afterMsgs.PutLong(12)
	afterMsgs.Put(private)

	init := &tg.InitConnectionRequest{
		APIID:          6,
		DeviceModel:    "Android",
		SystemVersion:  "test",
		AppVersion:     "private-layer",
		SystemLangCode: "en",
		LangPack:       "android",
		LangCode:       "en",
		Query:          &tg.HelpGetConfigRequest{},
	}
	var initWire bin.Buffer
	if err := init.Encode(&initWire); err != nil {
		t.Fatal(err)
	}
	replaceTerminalRPC(t, &initWire, private)
	var unprofiledInit bin.Buffer
	unprofiledInit.PutID(tg.InvokeWithLayerRequestTypeID)
	unprofiledInit.PutInt(int(tlprofile.Profile227))
	unprofiledInit.Put(initWire.Raw())

	var unprofiledBare bin.Buffer
	unprofiledBare.PutID(tg.InvokeWithLayerRequestTypeID)
	unprofiledBare.PutInt(int(tlprofile.Profile227))
	unprofiledBare.Put(private)

	tests := []struct {
		name       string
		body       []byte
		unprofiled bool
		wrappers   int
	}{
		{"naked", private, false, 0},
		{"invokeWithoutUpdates", withoutUpdates.Copy(), false, 1},
		{"invokeAfterMsg", afterMsg.Copy(), false, 1},
		{"invokeAfterMsgs", afterMsgs.Copy(), false, 1},
		{"invokeWithLayer", unprofiledBare.Copy(), true, 1},
		{"invokeWithLayer_initConnection", unprofiledInit.Copy(), true, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := bin.Buffer{Buf: append([]byte(nil), tc.body...)}
			var (
				admitted tlprofile.Admission
				err      error
			)
			if tc.unprofiled {
				admitted, err = r.AdmitUnprofiled(&body, tlprofile.Limits{})
			} else {
				admitted, err = r.AdmitLayer(tlprofile.Profile227, &body, tlprofile.Limits{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if body.Len() != 0 || admitted.WrapperCount() != tc.wrappers || admitted.Call().Method() != tlprofile.SemanticMethodMessagesForwardMessages {
				t.Fatalf("admission = remaining:%d wrappers:%d method:%#x", body.Len(), admitted.WrapperCount(), admitted.Call().Method())
			}
		})
	}
}

func TestLayerAdmissionAndroidPrivateFieldPolicyBeforeTypedMaterialization(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	request := &tg.MessagesCreateChatRequest{
		Users: repeatLayerPreflightValue[tg.InputUserClass](201, &tg.InputUserEmpty{}),
		Title: "private-field-policy",
	}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	// DrKLO's private createChat constructor is body-identical to canonical.
	binary.LittleEndian.PutUint32(body.Buf[:4], 0x0034a818)
	original := body.Copy()
	if _, err := r.AdmitLayer(tlprofile.Profile227, &body, tlprofile.Limits{MaxVectorElements: 8 << 10}); !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("private createChat admission err = %v, want LIMIT_INVALID", err)
	}
	if !bytes.Equal(body.Raw(), original) {
		t.Fatal("field-rejected private createChat consumed or mutated input")
	}
}

func TestLayerDispatchExactProfilesShareOneHandler(t *testing.T) {
	for _, profile := range []tlprofile.Profile{tlprofile.Profile225, tlprofile.Profile227} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
			request := &tg.InvokeWithLayerRequest{
				Layer: int(profile),
				Query: &tg.InitConnectionRequest{
					APIID:          123,
					DeviceModel:    "Desktop",
					SystemVersion:  "Windows",
					AppVersion:     "test",
					SystemLangCode: "en",
					LangPack:       "tdesktop",
					LangCode:       "en",
					Query:          &tg.HelpGetConfigRequest{},
				},
			}
			var body bin.Buffer
			if err := request.Encode(&body); err != nil {
				t.Fatal(err)
			}
			admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if body.Len() != 0 || admitted.Call().Profile() != profile {
				t.Fatalf("admission = profile:%d remaining:%d", admitted.Call().Profile(), body.Len())
			}
			result, method, err := r.DispatchAdmitted(context.Background(), [8]byte{1}, 10, 0, 0, admitted)
			if err != nil {
				t.Fatal(err)
			}
			if method != "help.getConfig" || result.Prepared().Call().Identity() != admitted.Call().Identity() {
				t.Fatalf("result = method:%q call:%#v", method, result.Prepared().Call())
			}
			var exact bin.Buffer
			if err := result.Encode(&exact); err != nil {
				t.Fatal(err)
			}
			decodedObject, err := tlprofile.DecodeObject(profile, &exact, tlprofile.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			decoded, ok := decodedObject.(*tg.Config)
			if !ok {
				t.Fatalf("decoded config type = %T", decodedObject)
			}
			if exact.Len() != 0 || decoded.ThisDC != 2 {
				t.Fatalf("decoded config = dc:%d remaining:%d", decoded.ThisDC, exact.Len())
			}
			var replay bin.Buffer
			if err := result.Encode(&replay); err != nil {
				t.Fatal(err)
			}
			if string(replay.Raw()) != string(tgBufferBytes(t, result)) {
				t.Fatalf("prepared replay differs from direct result")
			}
		})
	}
}

func TestLayerDispatchUnprofiledInvariantDoesNotPublishRepresentativeLayer(t *testing.T) {
	rawAuthKeyID := [8]byte{1}
	const sessionID = int64(10)
	auth := &captureAuthService{
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			rawAuthKeyID: {Layer: 225, DeviceModel: "stale-device"},
		},
	}
	sessions := &layerCaptureSessions{}
	r := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{Auth: auth, Sessions: sessions},
		zaptest.NewLogger(t),
		clock.System,
	)
	request := &tg.AuthBindTempAuthKeyRequest{
		PermAuthKeyID:    1,
		Nonce:            2,
		ExpiresAt:        3,
		EncryptedMessage: []byte("bind"),
	}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := admitted.ProfileEvidence(); ok {
		t.Fatal("unprofiled invariant request exposed its canonical codec representative as client evidence")
	}
	if !admitted.Call().WireInvariant() {
		t.Fatal("auth.bindTempAuthKey did not carry generated wire-invariant proof")
	}
	result, method, err := r.DispatchAdmitted(WithLayer(context.Background(), 226), rawAuthKeyID, sessionID, 0, 0, admitted)
	if err != nil {
		t.Fatal(err)
	}
	if method != "auth.bindTempAuthKey" || result == nil || !result.WireInvariant() {
		t.Fatalf("invariant dispatch = method:%q result:%T invariant:%v", method, result, result != nil && result.WireInvariant())
	}
	if auth.bindTempCalls != 1 || auth.bindTempLayer != 0 {
		t.Fatalf("unprofiled bind handler context = calls:%d layer:%d, want one call with unknown layer", auth.bindTempCalls, auth.bindTempLayer)
	}
	if calls := sessions.layerCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("stale auth metadata reached exact layer binder: %+v", calls)
	}
	if layer, ok := r.NegotiatedSessionLayer(rawAuthKeyID, sessionID); ok || layer != 0 {
		t.Fatalf("unprofiled invariant published session layer = (%d,%v)", layer, ok)
	}

	var profiledBody bin.Buffer
	if err := (&tg.InvokeWithLayerRequest{Layer: 227, Query: request}).Encode(&profiledBody); err != nil {
		t.Fatal(err)
	}
	profiled, err := r.AdmitUnprofiled(&profiledBody, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if profile, ok := profiled.ProfileEvidence(); !ok || profile != tlprofile.Profile227 {
		t.Fatalf("profiled bind evidence = (%d,%v), want layer 227", profile, ok)
	}
	freezeAndPublishLayer(t, r, rawAuthKeyID, sessionID, 100, 1, 227)
	if _, _, err := r.DispatchAdmitted(context.Background(), rawAuthKeyID, sessionID, 100, 1, profiled); err != nil {
		t.Fatal(err)
	}
	if auth.bindTempCalls != 2 || auth.bindTempLayer != 227 {
		t.Fatalf("profiled bind handler context = calls:%d layer:%d, want layer 227", auth.bindTempCalls, auth.bindTempLayer)
	}
	if calls := sessions.layerCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("Router duplicated edge exact-profile binder calls = %+v", calls)
	}
	if layer, ok := r.NegotiatedSessionLayer(rawAuthKeyID, sessionID); !ok || layer != 227 {
		t.Fatalf("authoritative wrapper session layer = (%d,%v), want (227,true)", layer, ok)
	}
}

func TestLayerDispatchInheritedDefaultIsEffectiveWithoutBecomingExplicitEvidence(t *testing.T) {
	rawAuthKeyID := [8]byte{0x73}
	const sessionID = int64(73)
	auth := &captureAuthService{}
	r := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{Auth: auth},
		zaptest.NewLogger(t),
		clock.System,
	)
	request := &tg.AuthBindTempAuthKeyRequest{PermAuthKeyID: businessAuthKeyInt64(rawAuthKeyID)}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	admitted, err := r.AdmitDefaultLayer(tlprofile.Profile225, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if profile, ok := admitted.EffectiveProfile(); !ok || profile != tlprofile.Profile225 {
		t.Fatalf("effective profile = (%d,%v), want (225,true)", profile, ok)
	}
	if profile, ok := admitted.ProfileEvidence(); ok || profile != tlprofile.Profile(0) {
		t.Fatalf("explicit evidence = (%d,%v), want (0,false)", profile, ok)
	}
	result, _, err := r.DispatchAdmitted(WithLayer(context.Background(), 227), rawAuthKeyID, sessionID, 0, 0, admitted)
	if err != nil || result == nil {
		t.Fatalf("dispatch inherited default = (%T,%v)", result, err)
	}
	if auth.bindTempLayer != 225 {
		t.Fatalf("handler layer = %d, want immutable effective 225", auth.bindTempLayer)
	}
	if layer, ok := r.NegotiatedSessionLayer(rawAuthKeyID, sessionID); ok || layer != 0 {
		t.Fatalf("inherited default became explicit registry state = (%d,%v)", layer, ok)
	}
}

func TestLayerDispatchProfiledBareIgnoresStaleMetadataLayer(t *testing.T) {
	rawAuthKeyID := [8]byte{2, 2, 0}
	const sessionID = int64(225)
	auth := &captureAuthService{
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			rawAuthKeyID: {Layer: 227, DeviceModel: "newer-device-metadata"},
		},
	}
	sessions := &layerCaptureSessions{}
	r := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{Auth: auth, Sessions: sessions},
		zaptest.NewLogger(t),
		clock.System,
	)
	request := &tg.AuthBindTempAuthKeyRequest{
		PermAuthKeyID:    1,
		Nonce:            2,
		ExpiresAt:        3,
		EncryptedMessage: []byte("bind"),
	}
	body := encodeExactLayerRPC(t, tlprofile.Profile225, request)
	admitted, err := r.AdmitLayer(tlprofile.Profile225, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchAdmitted(WithLayer(context.Background(), 226), rawAuthKeyID, sessionID, 0, 0, admitted); err != nil {
		t.Fatal(err)
	}
	if auth.bindTempCalls != 1 || auth.bindTempLayer != 225 {
		t.Fatalf("profiled bare handler context = calls:%d layer:%d, want one call at 225", auth.bindTempCalls, auth.bindTempLayer)
	}
	if calls := sessions.layerCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("profiled bare request rebound stale metadata before exact evidence: %+v", calls)
	}
}

func TestLayerDispatchUnprofiledInvariantWithholdsUpdatesReadinessUntilEvidence(t *testing.T) {
	const (
		userID    = int64(1000000227)
		sessionID = int64(220227)
	)
	rawAuthKeyID := [8]byte{0x22, 0x02, 0x27}
	auth := &captureAuthService{
		userID: userID,
		authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
			rawAuthKeyID: {Layer: 225, DeviceModel: "stale-device"},
		},
	}
	sessions := &layerCaptureSessions{}
	r := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{Auth: auth, Sessions: sessions},
		zaptest.NewLogger(t),
		clock.System,
	)
	// updates.getState stages readiness inside its handler in addition to the
	// common bare-RPC path, so this locks both no-evidence gates.
	request := &tg.UpdatesGetStateRequest{}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, evidence := admitted.ProfileEvidence(); evidence || !admitted.Call().WireInvariant() {
		t.Fatalf("unprofiled getState evidence/invariant = %v/%v", evidence, admitted.Call().WireInvariant())
	}

	dispatchCtx := postresponse.WithCallbacks(context.Background())
	result, method, err := r.DispatchAdmitted(dispatchCtx, rawAuthKeyID, sessionID, 10, 1, admitted)
	if err != nil || result == nil || method != "updates.getState" {
		t.Fatalf("unprofiled getState dispatch = method:%q result:%T err:%v", method, result, err)
	}
	postresponse.Run(dispatchCtx)
	if got := sessions.snapshot(); got.receives || got.receivesCalls != 0 {
		t.Fatalf("unprofiled invariant activated updates after delivery: %+v", got)
	}
	if calls := sessions.layerCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("unprofiled invariant bound stale layer: %+v", calls)
	}

	restore, err := r.PrepareAdmittedReplay(context.Background(), rawAuthKeyID, sessionID, 10, 1, admitted)
	if err != nil || restore == nil {
		t.Fatalf("prepare unprofiled invariant replay = callback:%v err:%v", restore != nil, err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if got := sessions.snapshot(); got.receives || got.receivesCalls != 0 {
		t.Fatalf("unprofiled invariant replay activated updates: %+v", got)
	}

	var wrapped bin.Buffer
	if err := (&tg.InvokeWithLayerRequest{Layer: 227, Query: request}).Encode(&wrapped); err != nil {
		t.Fatal(err)
	}
	profiled, err := r.AdmitUnprofiled(&wrapped, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	freezeAndPublishLayer(t, r, rawAuthKeyID, sessionID, 20, 2, 227)
	profiledCtx := postresponse.WithCallbacks(context.Background())
	if _, _, err := r.DispatchAdmitted(profiledCtx, rawAuthKeyID, sessionID, 20, 2, profiled); err != nil {
		t.Fatal(err)
	}
	postresponse.Run(profiledCtx)
	if got := sessions.snapshot(); !got.receives || got.receivesCalls != 1 {
		t.Fatalf("authoritative profile did not activate updates: %+v", got)
	}
	if calls := sessions.layerCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("Router duplicated edge authoritative-profile bind: %+v", calls)
	}
}

func TestLayerAdmissionPreflightRunsBeforeLargeVectorDecode(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	users := make([]tg.InputUserClass, 101)
	for index := range users {
		users[index] = &tg.InputUserEmpty{}
	}
	request := &tg.InvokeWithLayerRequest{
		Layer: int(tlprofile.Profile225),
		Query: &tg.UsersGetUsersRequest{ID: users},
	}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitUnprofiled(&body, tlprofile.Limits{}); !tgerr.Is(err, "INPUT_REQUEST_TOO_LONG") {
		t.Fatalf("admission err = %v, want INPUT_REQUEST_TOO_LONG", err)
	}
}

func TestLayerAdmissionFieldPoliciesCoverEveryRoutableProfile(t *testing.T) {
	type vectorCase struct {
		name      string
		method    tlprofile.SemanticID
		max       int
		errorCode string
		request   func(int) bin.Object
	}
	cases := []vectorCase{
		{"users.getUsers", tlprofile.SemanticMethodUsersGetUsers, 100, "INPUT_REQUEST_TOO_LONG", func(n int) bin.Object {
			return &tg.UsersGetUsersRequest{ID: repeatLayerPreflightValue[tg.InputUserClass](n, &tg.InputUserEmpty{})}
		}},
		{"users.getRequirementsToContact", tlprofile.SemanticMethodUsersGetRequirementsToContact, maxRequirementsToContactUsers, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.UsersGetRequirementsToContactRequest{ID: repeatLayerPreflightValue[tg.InputUserClass](n, &tg.InputUserEmpty{})}
		}},
		{"contacts.importContacts", tlprofile.SemanticMethodContactsImportContacts, maxContactImportBatch, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.ContactsImportContactsRequest{Contacts: make([]tg.InputPhoneContact, n)}
		}},
		{"contacts.deleteContacts", tlprofile.SemanticMethodContactsDeleteContacts, maxContactDeleteBatch, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.ContactsDeleteContactsRequest{ID: repeatLayerPreflightValue[tg.InputUserClass](n, &tg.InputUserEmpty{})}
		}},
		{"contacts.editCloseFriends", tlprofile.SemanticMethodContactsEditCloseFriends, maxCloseFriendsCount, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.ContactsEditCloseFriendsRequest{ID: make([]int64, n)}
		}},
		{"contacts.setBlocked", tlprofile.SemanticMethodContactsSetBlocked, maxContactSetBlocked, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.ContactsSetBlockedRequest{ID: repeatLayerPreflightValue[tg.InputPeerClass](n, &tg.InputPeerEmpty{})}
		}},
		{"messages.getMessages", tlprofile.SemanticMethodMessagesGetMessages, maxGetMessagesIDs, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.MessagesGetMessagesRequest{ID: repeatLayerPreflightValue[tg.InputMessageClass](n, &tg.InputMessageID{})}
		}},
		{"messages.getChats", tlprofile.SemanticMethodMessagesGetChats, maxGetMessagesIDs, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.MessagesGetChatsRequest{ID: make([]int64, n)}
		}},
		{"messages.getPeerDialogs", tlprofile.SemanticMethodMessagesGetPeerDialogs, maxDialogInputPeers, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.MessagesGetPeerDialogsRequest{Peers: repeatLayerPreflightValue[tg.InputDialogPeerClass](n, &tg.InputDialogPeer{Peer: &tg.InputPeerEmpty{}})}
		}},
		{"messages.readMessageContents", tlprofile.SemanticMethodMessagesReadMessageContents, maxGetMessagesIDs, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.MessagesReadMessageContentsRequest{ID: make([]int, n)}
		}},
		{"messages.getCustomEmojiDocuments", tlprofile.SemanticMethodMessagesGetCustomEmojiDocuments, maxEmojiDocuments, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.MessagesGetCustomEmojiDocumentsRequest{DocumentID: make([]int64, n)}
		}},
		{"messages.deleteMessages", tlprofile.SemanticMethodMessagesDeleteMessages, domain.MaxDeleteMessageIDs, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.MessagesDeleteMessagesRequest{ID: make([]int, n)}
		}},
		{"messages.createChat", tlprofile.SemanticMethodMessagesCreateChat, 200, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.MessagesCreateChatRequest{Users: repeatLayerPreflightValue[tg.InputUserClass](n, &tg.InputUserEmpty{}), Title: "layer-policy"}
		}},
		{"channels.getChannels", tlprofile.SemanticMethodChannelsGetChannels, maxGetMessagesIDs, "LIMIT_INVALID", func(n int) bin.Object {
			return &tg.ChannelsGetChannelsRequest{ID: repeatLayerPreflightValue[tg.InputChannelClass](n, &tg.InputChannelEmpty{})}
		}},
	}

	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	limits := tlprofile.Limits{MaxVectorElements: 8 << 10}
	for profile := tlprofile.Profile225; profile <= tlprofile.Profile227; profile++ {
		for _, tc := range cases {
			tc := tc
			if _, available := tlprofile.WireID(profile, tc.method); !available {
				continue
			}
			t.Run(fmt.Sprintf("layer_%d/%s/at_cap", profile, tc.name), func(t *testing.T) {
				body := encodeExactLayerRPC(t, profile, tc.request(tc.max))
				if _, err := r.AdmitLayer(profile, &body, limits); err != nil {
					t.Fatal(err)
				}
				if body.Len() != 0 {
					t.Fatalf("admitted request left %d bytes", body.Len())
				}
			})
			t.Run(fmt.Sprintf("layer_%d/%s/over_cap", profile, tc.name), func(t *testing.T) {
				body := encodeExactLayerRPC(t, profile, tc.request(tc.max+1))
				original := body.Copy()
				if _, err := r.AdmitLayer(profile, &body, limits); !tgerr.Is(err, tc.errorCode) {
					t.Fatalf("admission err = %v, want %s", err, tc.errorCode)
				}
				if string(body.Raw()) != string(original) {
					t.Fatal("rejected request consumed or mutated caller input")
				}
			})
		}
	}
}

func TestLayerAdmissionUploadFieldsCoverEveryProfile(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	for profile := tlprofile.Profile225; profile <= tlprofile.Profile227; profile++ {
		t.Run(fmt.Sprintf("layer_%d/saveFilePart", profile), func(t *testing.T) {
			atCap := encodeExactLayerRPC(t, profile, &tg.UploadSaveFilePartRequest{Bytes: make([]byte, appfiles.MaxUploadPartBytes)})
			if _, err := r.AdmitLayer(profile, &atCap, tlprofile.Limits{}); err != nil {
				t.Fatal(err)
			}
			over := encodeExactLayerRPC(t, profile, &tg.UploadSaveFilePartRequest{Bytes: make([]byte, appfiles.MaxUploadPartBytes+1)})
			original := over.Copy()
			if _, err := r.AdmitLayer(profile, &over, tlprofile.Limits{}); !tgerr.Is(err, "FILE_PART_TOO_BIG") {
				t.Fatalf("oversized part err = %v", err)
			}
			if string(over.Raw()) != string(original) {
				t.Fatal("oversized part consumed input")
			}
		})
		t.Run(fmt.Sprintf("layer_%d/saveBigFilePart", profile), func(t *testing.T) {
			atCap := encodeExactLayerRPC(t, profile, &tg.UploadSaveBigFilePartRequest{FileTotalParts: appfiles.MaxUploadParts, Bytes: make([]byte, appfiles.MaxUploadPartBytes)})
			if _, err := r.AdmitLayer(profile, &atCap, tlprofile.Limits{}); err != nil {
				t.Fatal(err)
			}
			for _, totalParts := range []int{0, -1, appfiles.MaxUploadParts + 1} {
				body := encodeExactLayerRPC(t, profile, &tg.UploadSaveBigFilePartRequest{FileTotalParts: totalParts})
				original := body.Copy()
				if _, err := r.AdmitLayer(profile, &body, tlprofile.Limits{}); !tgerr.Is(err, "FILE_PART_INVALID") {
					t.Fatalf("total parts %d err = %v", totalParts, err)
				}
				if string(body.Raw()) != string(original) {
					t.Fatalf("invalid total parts %d consumed input", totalParts)
				}
			}
			over := encodeExactLayerRPC(t, profile, &tg.UploadSaveBigFilePartRequest{FileTotalParts: 1, Bytes: make([]byte, appfiles.MaxUploadPartBytes+1)})
			if _, err := r.AdmitLayer(profile, &over, tlprofile.Limits{}); !tgerr.Is(err, "FILE_PART_TOO_BIG") {
				t.Fatalf("oversized big part err = %v", err)
			}
		})
	}
}

func repeatLayerPreflightValue[T any](n int, value T) []T {
	result := make([]T, n)
	for index := range result {
		result[index] = value
	}
	return result
}

func encodeExactLayerRPC(t *testing.T, profile tlprofile.Profile, request bin.Object) bin.Buffer {
	t.Helper()
	var body bin.Buffer
	if err := tlprofile.EncodeObject(profile, request, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestLayerDispatchRejectsUnsupportedWrapperBeforeHandler(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	request := &tg.InvokeWithLayerRequest{
		Layer: int(tlprofile.Profile227),
		Query: &tg.InvokeWithTakeoutRequest{
			TakeoutID: 1,
			Query:     &tg.HelpGetConfigRequest{},
		},
	}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, method, err := r.DispatchAdmitted(context.Background(), [8]byte{1}, 10, 0, 0, admitted); method != "help.getConfig" || !tgerr.Is(err, "NOT_IMPLEMENTED") {
		t.Fatalf("dispatch = method:%q err:%v", method, err)
	}
}

func TestPrepareAdmittedReplayRestoresReadinessOnlyAfterDelivery(t *testing.T) {
	const (
		userID    = int64(1000000901)
		sessionID = int64(901)
	)
	authKeyID := [8]byte{9, 0, 1}
	sessions := &captureSessions{userID: userID, userResolved: true}
	r := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{Sessions: sessions},
		zaptest.NewLogger(t),
		clock.System,
	)
	request := &tg.InvokeWithLayerRequest{
		Layer: int(tlprofile.Profile225),
		Query: &tg.InitConnectionRequest{
			APIID: 123, DeviceModel: "Desktop", SystemVersion: "Windows", AppVersion: "test",
			SystemLangCode: "en", LangPack: "tdesktop", LangCode: "en",
			Query: &tg.HelpGetConfigRequest{},
		},
	}
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatal(err)
	}
	admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	freezeAndPublishLayer(t, r, authKeyID, sessionID, 9010, 1, 225)
	after, err := r.PrepareAdmittedReplay(context.Background(), authKeyID, sessionID, 9010, 1, admitted)
	if err != nil {
		t.Fatal(err)
	}
	if after == nil {
		t.Fatal("successful naked replay has no delivery callback")
	}
	if got := sessions.snapshot(); got.receives || got.receivesCalls != 0 {
		t.Fatalf("replay marked ready before physical delivery: %+v", got)
	}
	if layer, ok := r.NegotiatedSessionLayer(authKeyID, sessionID); !ok || layer != 225 {
		t.Fatalf("admission-time protocol evidence = (%d,%v), want (225,true)", layer, ok)
	}
	if err := after(); err != nil {
		t.Fatalf("restore delivered replay: %v", err)
	}
	if got := sessions.snapshot(); !got.receives || got.receivesCalls != 1 {
		t.Fatalf("delivered replay readiness = %+v", got)
	}
	if layer, ok := r.NegotiatedSessionLayer(authKeyID, sessionID); !ok || layer != 225 {
		t.Fatalf("delivered replay changed protocol evidence = (%d,%v), want (225,true)", layer, ok)
	}
	if err := after(); err != nil {
		t.Fatalf("idempotent replay restore: %v", err)
	}
	if got := sessions.snapshot(); got.receivesCalls != 1 {
		t.Fatalf("replay delivery callback was not idempotent: %+v", got)
	}
}

func TestRequestBoundOnlyLayerRPCDoesNotPublishMetadataOrReadiness(t *testing.T) {
	for _, mode := range []string{"dispatch", "replay"} {
		t.Run(mode, func(t *testing.T) {
			authKeyID := [8]byte{9, 0, 11, byte(len(mode))}
			const sessionID = int64(911)
			sessions := &captureSessions{}
			r := New(
				Config{DC: 2, IP: "127.0.0.1", Port: 2398},
				Deps{Sessions: sessions},
				zaptest.NewLogger(t),
				clock.System,
			)
			request := &tg.InvokeWithLayerRequest{
				Layer: int(tlprofile.Profile225),
				Query: &tg.InitConnectionRequest{
					APIID: 123, DeviceModel: "Stale Desktop", SystemVersion: "Windows", AppVersion: "stale",
					SystemLangCode: "en", LangPack: "tdesktop", LangCode: "en",
					Query: &tg.HelpGetConfigRequest{},
				},
			}
			var body bin.Buffer
			if err := request.Encode(&body); err != nil {
				t.Fatal(err)
			}
			admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := r.WithLayerRPCProfileEvidenceFresh(context.Background(), false)
			switch mode {
			case "dispatch":
				if result, method, err := r.DispatchAdmitted(ctx, authKeyID, sessionID, 9011, 1, admitted); err != nil || result == nil || method != "help.getConfig" {
					t.Fatalf("request-bound dispatch = (%T,%q,%v)", result, method, err)
				}
			case "replay":
				after, err := r.PrepareAdmittedReplay(ctx, authKeyID, sessionID, 9011, 1, admitted)
				if err != nil {
					t.Fatal(err)
				}
				if err := after(); err != nil {
					t.Fatal(err)
				}
			}
			if got := sessions.snapshot(); got.receives || got.receivesCalls != 0 {
				t.Fatalf("request-bound %s published readiness: %+v", mode, got)
			}
			r.clientInfoMu.RLock()
			sessionInfo := r.clientInfo[clientInfoSessionKey{rawAuthKeyID: authKeyID, sessionID: sessionID}]
			authInfo := r.authInfo[authKeyID]
			r.clientInfoMu.RUnlock()
			if sessionInfo.hasClientInfo || authInfo.hasClientInfo {
				t.Fatalf("request-bound %s published init metadata: session=%+v auth=%+v", mode, sessionInfo, authInfo)
			}
		})
	}
}

func TestPrepareAdmittedReplayDoesNotRollBackNewerExplicitLayerOrClientInfo(t *testing.T) {
	authKeyID := [8]byte{9, 0, 2}
	const sessionID = int64(902)
	auth := &captureAuthService{authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo)}
	sessions := &layerCaptureSessions{}
	r := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{Auth: auth, Sessions: sessions},
		zaptest.NewLogger(t),
		clock.System,
	)
	oldRequest := &tg.InvokeWithLayerRequest{
		Layer: int(tlprofile.Profile225),
		Query: &tg.InitConnectionRequest{
			APIID: 123, DeviceModel: "Old Desktop", SystemVersion: "Windows 10", AppVersion: "old",
			SystemLangCode: "en", LangPack: "tdesktop", LangCode: "en",
			Query: &tg.HelpGetConfigRequest{},
		},
	}
	var body bin.Buffer
	if err := oldRequest.Encode(&body); err != nil {
		t.Fatal(err)
	}
	admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	freezeAndPublishLayer(t, r, authKeyID, sessionID, 10, 1, 225)
	after, err := r.PrepareAdmittedReplay(context.Background(), authKeyID, sessionID, 10, 1, admitted)
	if err != nil {
		t.Fatal(err)
	}

	freezeAndPublishLayer(t, r, authKeyID, sessionID, 20, 2, 227)
	newerCtx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), authKeyID), sessionID), authKeyID)
	r.rememberClientInfoAt(newerCtx, ClientInfo{
		APIID: 123, DeviceModel: "New Desktop", SystemVersion: "Windows 11", AppVersion: "new", Type: ClientTypeTDesktop,
	}, 2)
	if err := after(); err != nil {
		t.Fatalf("restore old replay after correction: %v", err)
	}

	if layer, ok := r.NegotiatedSessionLayer(authKeyID, sessionID); !ok || layer != 227 {
		t.Fatalf("exact session rolled back = (%d,%v), want (227,true)", layer, ok)
	}
	if got := auth.authKeyClientInfos[authKeyID].Layer; got != 227 {
		t.Fatalf("durable default rolled back = %d, want 227", got)
	}
	if calls := sessions.layerCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("Router replay touched edge-owned exact binder: %+v", calls)
	}
	info, ok, _ := r.clientSessionInfo(newerCtx)
	if !ok || !info.hasClientInfo || info.clientInfo.DeviceModel != "New Desktop" || info.clientInfo.AppVersion != "new" {
		t.Fatalf("newer client info was overwritten by replay: %+v ok=%v", info, ok)
	}
}

func TestDelayedExplicitDispatchCannotRollBackAdmissionTimeLayerOrInitMetadata(t *testing.T) {
	authKeyID := [8]byte{9, 0, 3}
	const sessionID = int64(903)
	auth := &captureAuthService{authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo)}
	sessions := &layerCaptureSessions{}
	r := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{Auth: auth, Sessions: sessions},
		zaptest.NewLogger(t),
		clock.System,
	)
	admit := func(layer int, device, version string) tlprofile.Admission {
		t.Helper()
		request := &tg.InvokeWithLayerRequest{
			Layer: layer,
			Query: &tg.InitConnectionRequest{
				APIID: 123, DeviceModel: device, SystemVersion: "Windows", AppVersion: version,
				SystemLangCode: "en", LangPack: "tdesktop", LangCode: "en",
				Query: &tg.HelpGetConfigRequest{},
			},
		}
		var body bin.Buffer
		if err := request.Encode(&body); err != nil {
			t.Fatal(err)
		}
		admitted, err := r.AdmitUnprofiled(&body, tlprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return admitted
	}
	old := admit(225, "Old Desktop", "old")
	newer := admit(227, "New Desktop", "new")
	freezeAndPublishLayer(t, r, authKeyID, sessionID, 10, 1, 225)
	freezeAndPublishLayer(t, r, authKeyID, sessionID, 20, 2, 227)

	if _, _, err := r.DispatchAdmitted(context.Background(), authKeyID, sessionID, 20, 2, newer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchAdmitted(context.Background(), authKeyID, sessionID, 10, 1, old); err != nil {
		t.Fatal(err)
	}
	if layer, msgID, ok := r.NegotiatedSessionLayerEvidence(authKeyID, sessionID); !ok || layer != 227 || msgID != 20 {
		t.Fatalf("exact evidence after delayed dispatch = (%d,%d,%v), want (227,20,true)", layer, msgID, ok)
	}
	if got := auth.authKeyClientInfos[authKeyID]; got.Layer != 227 || got.DeviceModel != "New Desktop" || got.AppVersion != "new" {
		t.Fatalf("durable protocol metadata rolled back: %+v", got)
	}
	if calls := sessions.layerCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("handler dispatch duplicated/rolled back edge profile binding: %+v", calls)
	}
}

func TestSameLayerNakedInitConnectionUsesMessageIDWatermark(t *testing.T) {
	authKeyID := [8]byte{9, 0, 4}
	const sessionID = int64(904)
	auth := &captureAuthService{authKeyClientInfos: map[[8]byte]domain.AuthKeyClientInfo{
		authKeyID: {Layer: 227},
	}}
	r := New(
		Config{DC: 2, IP: "127.0.0.1", Port: 2398},
		Deps{Auth: auth},
		zaptest.NewLogger(t),
		clock.System,
	)
	admit := func(device, version string) tlprofile.Admission {
		t.Helper()
		request := &tg.InitConnectionRequest{
			APIID: 123, DeviceModel: device, SystemVersion: "Windows", AppVersion: version,
			SystemLangCode: "en", LangPack: "tdesktop", LangCode: "en",
			Query: &tg.HelpGetConfigRequest{},
		}
		var body bin.Buffer
		if err := request.Encode(&body); err != nil {
			t.Fatal(err)
		}
		admitted, err := r.AdmitDefaultLayer(tlprofile.Profile227, &body, tlprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return admitted
	}
	newer := admit("New Desktop", "new")
	old := admit("Old Desktop", "old")
	if _, _, err := r.DispatchAdmitted(context.Background(), authKeyID, sessionID, 20, 2, newer); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.DispatchAdmitted(context.Background(), authKeyID, sessionID, 10, 1, old); err != nil {
		t.Fatal(err)
	}
	if got := auth.authKeyClientInfos[authKeyID]; got.DeviceModel != "New Desktop" || got.AppVersion != "new" || got.Layer != 227 {
		t.Fatalf("same-layer naked init metadata rolled back: %+v", got)
	}
	ctx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), authKeyID), sessionID), authKeyID)
	info, ok, _ := r.clientSessionInfo(ctx)
	if !ok || info.wrapperMsgID != 20 || !info.hasClientInfo || info.clientInfo.DeviceModel != "New Desktop" {
		t.Fatalf("session wrapper watermark/client info = %+v ok=%v", info, ok)
	}
}

func TestCrossSessionInitMetadataUsesAdmissionSequence(t *testing.T) {
	rawOld := [8]byte{9, 0, 5, 1}
	rawNew := [8]byte{9, 0, 5, 2}
	permAuthKeyID := [8]byte{9, 0, 5, 3}
	auth := &captureAuthService{
		resolvedAuthKeyID:  permAuthKeyID,
		hasResolved:        true,
		authKeyClientInfos: make(map[[8]byte]domain.AuthKeyClientInfo),
	}
	r := New(Config{DC: 2}, Deps{Auth: auth}, zaptest.NewLogger(t), clock.System)
	newCtx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), rawNew), 2), permAuthKeyID)
	oldCtx := WithAuthKeyID(WithSessionID(WithRawAuthKeyID(context.Background(), rawOld), 1), permAuthKeyID)
	r.rememberClientInfoAt(newCtx, ClientInfo{APIID: 123, DeviceModel: "New Desktop", AppVersion: "new", Type: ClientTypeTDesktop}, 2)
	r.rememberClientInfoAt(oldCtx, ClientInfo{APIID: 123, DeviceModel: "Old Desktop", AppVersion: "old", Type: ClientTypeTDesktop}, 1)

	if got := auth.authKeyClientInfos[permAuthKeyID]; got.DeviceModel != "New Desktop" || got.AppVersion != "new" {
		t.Fatalf("cross-session durable metadata rolled back: %+v", got)
	}
	oldInfo, ok, _ := r.clientSessionInfo(oldCtx)
	if !ok || !oldInfo.hasClientInfo || oldInfo.clientInfo.DeviceModel != "Old Desktop" {
		t.Fatalf("old session lost its request-local metadata: %+v ok=%v", oldInfo, ok)
	}
}

func tgBufferBytes(t *testing.T, encoder interface{ Encode(*bin.Buffer) error }) []byte {
	t.Helper()
	var encoded bin.Buffer
	if err := encoder.Encode(&encoded); err != nil {
		t.Fatal(err)
	}
	return encoded.Copy()
}
