package tlprofile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

func TestLimitsRemainComparable(t *testing.T) {
	left := Limits{1, 2, 3, 4}
	right := Limits{1, 2, 3, 4}
	if left != right {
		t.Fatal("equal limits compare unequal")
	}
	lookup := map[Limits]string{left: "ok"}
	if lookup[right] != "ok" {
		t.Fatal("limits cannot be used as a stable map key")
	}
}

func TestSparseWrapperAdmissionAndDefaultProfileOverride(t *testing.T) {
	var body bin.Buffer
	putInvokeWithLayer(&body, 225)
	body.PutID(tg.InitConnectionRequestTypeID)
	body.PutUint32(0)
	body.PutInt(77)
	body.PutString("desktop")
	body.PutString("test-os")
	body.PutString("1.2.3")
	body.PutString("en")
	body.PutString("tdesktop")
	body.PutString("en")
	body.PutID(tg.HelpGetConfigRequestTypeID)
	fullSize := body.Len()

	d := NewDispatcher()
	admitted, err := d.AdmitDefault(Profile228, &body, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if body.Len() != 0 {
		t.Fatalf("remaining body = %d", body.Len())
	}
	if admitted.Call().Profile() != Profile225 || admitted.Call().Method() != SemanticMethodHelpGetConfig {
		t.Fatalf("terminal = profile %d method %#x", admitted.Call().Profile(), admitted.Call().Method())
	}
	if effective, ok := admitted.EffectiveProfile(); !ok || effective != Profile225 {
		t.Fatalf("effective profile = %d/%v", effective, ok)
	}
	if evidence, ok := admitted.ProfileEvidence(); !ok || evidence != Profile225 {
		t.Fatalf("profile evidence = %d/%v", evidence, ok)
	}
	if admitted.Prepared().WireSize() != fullSize {
		t.Fatalf("prepared wire size = %d, want %d", admitted.Prepared().WireSize(), fullSize)
	}
	if admitted.WrapperCount() != 2 {
		t.Fatalf("wrapper count = %d", admitted.WrapperCount())
	}

	outer, ok := admitted.Wrapper(0)
	if !ok || outer.Semantic() != SemanticMethodInvokeWithLayer || outer.Profile() != Profile228 {
		t.Fatalf("outer wrapper = %#v/%v", outer, ok)
	}
	layer, present, found, err := outer.Value("layer")
	if err != nil || !present || !found || layer != 225 {
		t.Fatalf("layer metadata = %#v/%v/%v/%v", layer, present, found, err)
	}
	inner, ok := admitted.Wrapper(1)
	if !ok || inner.Semantic() != SemanticMethodInitConnection || inner.Profile() != Profile225 {
		t.Fatalf("inner wrapper = %#v/%v", inner, ok)
	}
	device, present, found, err := inner.Value("device_model")
	if err != nil || !present || !found || device != "desktop" {
		t.Fatalf("device metadata = %#v/%v/%v/%v", device, present, found, err)
	}
	if _, present, found, err := inner.Value("proxy"); err != nil || present || !found {
		t.Fatalf("proxy metadata = present:%v found:%v err:%v", present, found, err)
	}
}

func TestSparseStrictAdmissionRejectsConflictingInvokeWithLayerTransactionally(t *testing.T) {
	var body bin.Buffer
	putInvokeWithLayer(&body, 225)
	body.PutID(tg.HelpGetConfigRequestTypeID)
	original := append([]byte(nil), body.Raw()...)
	_, err := NewDispatcher().Admit(Profile228, &body, Limits{})
	if !errors.Is(err, ErrProfileConflict) {
		t.Fatalf("admit error = %v", err)
	}
	if !bytes.Equal(original, body.Raw()) {
		t.Fatal("conflicting wrapper admission consumed input")
	}
}

func TestSparseWrapperVectorMetadataIsImmutable(t *testing.T) {
	// Literal exact wrapper oracle keeps this metadata test independent of
	// canonical generation and historical generic-envelope encoding support.
	var body bin.Buffer
	body.PutID(0x3dc4b4f0)
	body.PutVectorHeader(2)
	body.PutLong(11)
	body.PutLong(22)
	body.PutID(0xc4f9186b)
	admitted, err := NewDispatcher().Admit(Profile229, &body, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	wrapper, ok := admitted.Wrapper(0)
	if !ok {
		t.Fatal("missing invokeAfterMsgs wrapper")
	}
	first, present, found, err := wrapper.Value("msg_ids")
	if err != nil || !present || !found {
		t.Fatalf("first metadata read = %v/%v/%v", present, found, err)
	}
	firstIDs := first.([]int64)
	firstIDs[0] = 99
	second, _, _, err := wrapper.Value("msg_ids")
	if err != nil {
		t.Fatal(err)
	}
	if got := second.([]int64); !bytes.Equal(int64SliceBytes(got), int64SliceBytes([]int64{11, 22})) {
		t.Fatalf("mutable metadata leaked: %v", got)
	}
}

func TestSparseWrapperVectorRejectsUnboundedLengthBeforeAllocation(t *testing.T) {
	var body bin.Buffer
	body.PutID(tg.InvokeAfterMsgsRequestTypeID)
	body.PutVectorHeader(1 << 20)
	original := body.Copy()

	_, err := NewDispatcher().Admit(Profile228, &body, Limits{MaxVectorElements: 4})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("configured limit 4")) {
		t.Fatalf("admit error = %v", err)
	}
	if !bytes.Equal(body.Raw(), original) {
		t.Fatal("wrapper vector rejection consumed input")
	}

	var truncated bin.Buffer
	truncated.PutID(tg.InvokeAfterMsgsRequestTypeID)
	truncated.PutVectorHeader(2)
	truncated.PutLong(1)
	truncatedOriginal := truncated.Copy()
	_, err = NewDispatcher().Admit(Profile228, &truncated, Limits{})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("remaining wire capacity 1")) {
		t.Fatalf("truncated vector error = %v", err)
	}
	if !bytes.Equal(truncated.Raw(), truncatedOriginal) {
		t.Fatal("truncated wrapper vector rejection consumed input")
	}
}

func TestSparseWrapperVectorsShareAggregateLimit(t *testing.T) {
	var body bin.Buffer
	putTestInvokeAfterMsgs(&body, []int64{1, 2, 3})
	putTestInvokeAfterMsgs(&body, []int64{4, 5, 6})
	body.PutID(tg.HelpGetConfigRequestTypeID)
	original := body.Copy()

	_, err := NewDispatcher().Admit(Profile228, &body, Limits{
		MaxVectorElements:    4,
		MaxAggregateElements: 4,
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("remaining aggregate budget 1")) {
		t.Fatalf("admit error = %v", err)
	}
	if !bytes.Equal(body.Raw(), original) {
		t.Fatal("aggregate wrapper vector rejection consumed input")
	}
}

func TestSparseWrapperAndTerminalShareStructuralLimits(t *testing.T) {
	var body bin.Buffer
	putInvokeWithLayer(&body, 228)
	putTestInvokeAfterMsgs(&body, []int64{1, 2, 3, 4})
	if err := (&tg.MessagesDeleteMessagesRequest{ID: []int{5, 6, 7, 8}}).Encode(&body); err != nil {
		t.Fatal(err)
	}
	original := body.Copy()

	t.Run("aggregate", func(t *testing.T) {
		input := &bin.Buffer{Buf: append([]byte(nil), original...)}
		_, err := NewDispatcher().AdmitUnprofiled(input, Limits{
			MaxVectorElements:    8,
			MaxAggregateElements: 7,
		})
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("remaining aggregate budget 3")) {
			t.Fatalf("admit error = %v", err)
		}
		if !bytes.Equal(input.Raw(), original) {
			t.Fatal("cross-wrapper aggregate rejection consumed input")
		}
	})

	t.Run("depth", func(t *testing.T) {
		input := &bin.Buffer{Buf: append([]byte(nil), original...)}
		_, err := NewDispatcher().AdmitUnprofiled(input, Limits{
			MaxVectorElements:    8,
			MaxAggregateElements: 8,
			MaxDepth:             3,
		})
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("maximum scan depth 3 exceeded")) {
			t.Fatalf("admit error = %v", err)
		}
		if !bytes.Equal(input.Raw(), original) {
			t.Fatal("cross-wrapper depth rejection consumed input")
		}
	})

	t.Run("exact budget succeeds", func(t *testing.T) {
		input := &bin.Buffer{Buf: append([]byte(nil), original...)}
		if _, err := NewDispatcher().AdmitUnprofiled(input, Limits{
			MaxVectorElements:    8,
			MaxAggregateElements: 8,
			MaxDepth:             4,
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSparseWrapperObjectUsesSharedStructuralLimits(t *testing.T) {
	t.Run("aggregate", func(t *testing.T) {
		params := &tg.JSONArray{Value: []tg.JSONValueClass{
			&tg.JSONNull{}, &tg.JSONNull{}, &tg.JSONNull{}, &tg.JSONNull{}, &tg.JSONNull{},
		}}
		var body bin.Buffer
		putInvokeWithLayer(&body, 228)
		putTestInitConnectionWithParams(t, &body, params)
		body.PutID(tg.HelpGetConfigRequestTypeID)
		original := body.Copy()

		_, err := NewDispatcher().AdmitUnprofiled(&body, Limits{
			MaxVectorElements:    8,
			MaxAggregateElements: 4,
		})
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("remaining aggregate budget 4")) {
			t.Fatalf("admit error = %v", err)
		}
		if !bytes.Equal(body.Raw(), original) {
			t.Fatal("wrapper Object aggregate rejection consumed input")
		}
	})

	t.Run("depth", func(t *testing.T) {
		params := &tg.JSONArray{Value: []tg.JSONValueClass{
			&tg.JSONArray{Value: []tg.JSONValueClass{&tg.JSONNull{}}},
		}}
		var body bin.Buffer
		putInvokeWithLayer(&body, 228)
		putTestInitConnectionWithParams(t, &body, params)
		body.PutID(tg.HelpGetConfigRequestTypeID)
		original := body.Copy()

		_, err := NewDispatcher().AdmitUnprofiled(&body, Limits{
			MaxVectorElements:    4,
			MaxAggregateElements: 4,
			MaxDepth:             4,
		})
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("maximum scan depth 4 exceeded")) {
			t.Fatalf("admit error = %v", err)
		}
		if !bytes.Equal(body.Raw(), original) {
			t.Fatal("wrapper Object depth rejection consumed input")
		}
	})
}

func TestSparseWrapperObjectEnforcesDeclaredTypeRef(t *testing.T) {
	var body bin.Buffer
	putInvokeWithLayer(&body, 228)
	body.PutID(tg.InitConnectionRequestTypeID)
	body.PutUint32(1 << 1)
	body.PutInt(1)
	for _, value := range []string{"device", "system", "app", "en", "", "en"} {
		body.PutString(value)
	}
	// params is declared JSONValue. A known function constructor must not pass
	// merely because it is present in the exact-profile route table.
	body.PutID(tg.HelpGetConfigRequestTypeID)
	body.PutID(tg.HelpGetConfigRequestTypeID)
	original := body.Copy()

	_, err := NewDispatcher().AdmitUnprofiled(&body, Limits{})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("not a member of class JSONValue")) {
		t.Fatalf("admit error = %v", err)
	}
	if !bytes.Equal(body.Raw(), original) {
		t.Fatal("wrong wrapper Object type consumed input")
	}
}

func TestSparseWrapperRejectsOversizedFullWireBeforeParsing(t *testing.T) {
	var body bin.Buffer
	putInvokeWithLayer(&body, 228)
	body.PutID(tg.HelpGetConfigRequestTypeID)
	original := body.Copy()

	_, err := NewDispatcher().Admit(Profile228, &body, Limits{MaxWireBytes: len(original) - bin.Word})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("wire byte length")) {
		t.Fatalf("admit error = %v", err)
	}
	if !bytes.Equal(body.Raw(), original) {
		t.Fatal("oversized wrapper rejection consumed input")
	}
}

func TestSparseUnprofiledAdmission(t *testing.T) {
	t.Run("invariant terminal", func(t *testing.T) {
		var body bin.Buffer
		body.PutID(tg.HelpGetConfigRequestTypeID)
		admitted, err := NewDispatcher().AdmitUnprofiled(&body, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if admitted.Call().Profile() != ProfileCanonical || !admitted.Call().WireInvariant() {
			t.Fatalf("call profile/invariant = %d/%v", admitted.Call().Profile(), admitted.Call().WireInvariant())
		}
		if effective, ok := admitted.EffectiveProfile(); ok || effective != 0 {
			t.Fatalf("effective profile = %d/%v", effective, ok)
		}
		if evidence, ok := admitted.ProfileEvidence(); ok || evidence != 0 {
			t.Fatalf("profile evidence = %d/%v", evidence, ok)
		}

		var exact bin.Buffer
		exact.PutID(tg.HelpGetConfigRequestTypeID)
		profiled, err := NewDispatcher().Admit(Profile225, &exact, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if profiled.Prepared().Identity() != admitted.Prepared().Identity() {
			t.Fatal("wire-invariant identity changed after exact profile admission")
		}
	})

	t.Run("explicit selector", func(t *testing.T) {
		var body bin.Buffer
		putInvokeWithLayer(&body, 226)
		body.PutID(tg.HelpGetConfigRequestTypeID)
		admitted, err := NewDispatcher().AdmitUnprofiled(&body, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if admitted.Call().Profile() != Profile226 || admitted.WrapperCount() != 1 {
			t.Fatalf("call profile/wrappers = %d/%d", admitted.Call().Profile(), admitted.WrapperCount())
		}
		if evidence, ok := admitted.ProfileEvidence(); !ok || evidence != Profile226 {
			t.Fatalf("profile evidence = %d/%v", evidence, ok)
		}
	})

	t.Run("known method requires profile", func(t *testing.T) {
		if tlUnprofiledInvariant(tg.MessagesSendMessageRequestTypeID) {
			t.Fatal("messages.sendMessage unexpectedly became an unprofiled invariant")
		}
		var body bin.Buffer
		body.PutID(tg.MessagesSendMessageRequestTypeID)
		before := body.Copy()
		_, err := NewDispatcher().AdmitUnprofiled(&body, Limits{})
		if !errors.Is(err, ErrProfileRequired) {
			t.Fatalf("admit error = %v", err)
		}
		if !bytes.Equal(before, body.Raw()) {
			t.Fatal("profile-required rejection consumed input")
		}
	})

	t.Run("unknown method", func(t *testing.T) {
		var body bin.Buffer
		body.PutID(0x7f00aa55)
		_, err := NewDispatcher().AdmitUnprofiled(&body, Limits{})
		if !errors.Is(err, ErrUnknownRPCMethod) {
			t.Fatalf("admit error = %v", err)
		}
	})
}

func TestSparseUnknownWrappedTerminalEvidence(t *testing.T) {
	const unknown = uint32(0xd1435160)
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		var body bin.Buffer
		putInvokeWithLayer(&body, int(profile))
		body.PutID(tg.InitConnectionRequestTypeID)
		body.PutUint32(0)
		body.PutInt(1)
		for _, value := range []string{"device", "system", "app", "en", "", "en"} {
			body.PutString(value)
		}
		body.PutID(unknown)
		before := body.Copy()
		_, err := NewDispatcher().Admit(profile, &body, Limits{})
		if !errors.Is(err, ErrUnknownRPCMethod) {
			t.Fatalf("profile %d error = %v", profile, err)
		}
		var terminal *UnknownTerminalError
		if !errors.As(err, &terminal) {
			t.Fatalf("profile %d error type = %T", profile, err)
		}
		if terminal.Profile != profile || terminal.WireID != unknown || terminal.WireSize != 4 || terminal.WrapperCount() != 2 {
			t.Fatalf("profile %d terminal = %+v wrappers=%d", profile, terminal, terminal.WrapperCount())
		}
		outer, outerOK := terminal.Wrapper(0)
		inner, innerOK := terminal.Wrapper(1)
		if !outerOK || !innerOK || outer.Semantic() != SemanticMethodInvokeWithLayer || inner.Semantic() != SemanticMethodInitConnection {
			t.Fatalf("profile %d wrapper chain = %#v/%v %#v/%v", profile, outer, outerOK, inner, innerOK)
		}
		if !bytes.Equal(body.Raw(), before) {
			t.Fatalf("profile %d failed admission consumed input", profile)
		}
	}
}

func TestSparseTopLevelGZIPCanProveUnprofiledSelector(t *testing.T) {
	t.Run("selector", func(t *testing.T) {
		var expanded bin.Buffer
		putInvokeWithLayer(&expanded, 226)
		expanded.PutID(tg.HelpGetConfigRequestTypeID)
		var body bin.Buffer
		putTestGZIP(t, &body, expanded.Raw())
		original := body.Copy()

		releases := 0
		admitted, err := NewDispatcher().AdmitUnprofiledWithOptions(&body, AdmissionOptions{
			ExpandGZIP: func(wire []byte, maxExpandedBytes int) ([]byte, func(), error) {
				decoded, err := decodeTestGZIP(wire, maxExpandedBytes)
				return decoded, func() { releases++ }, err
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if releases != 1 {
			t.Fatalf("release count = %d", releases)
		}
		if admitted.Call().Profile() != Profile226 || admitted.WrapperCount() != 1 {
			t.Fatalf("call profile/wrappers = %d/%d", admitted.Call().Profile(), admitted.WrapperCount())
		}
		if evidence, ok := admitted.ProfileEvidence(); !ok || evidence != Profile226 {
			t.Fatalf("profile evidence = %d/%v", evidence, ok)
		}
		if got, want := admitted.Prepared().WireSize(), len(original); got != want {
			t.Fatalf("wire size = %d, want %d", got, want)
		}
		if got, want := admitted.Prepared().WireDigest(), sha256.Sum256(original); got != want {
			t.Fatalf("wire digest = %x, want %x", got, want)
		}
	})

	t.Run("conflicting nested selector", func(t *testing.T) {
		var expanded bin.Buffer
		putInvokeWithLayer(&expanded, 228)
		putInvokeWithLayer(&expanded, 227)
		expanded.PutID(tg.HelpGetConfigRequestTypeID)
		var body bin.Buffer
		putTestGZIP(t, &body, expanded.Raw())
		original := body.Copy()
		releases := 0

		_, err := NewDispatcher().AdmitUnprofiledWithOptions(&body, AdmissionOptions{
			ExpandGZIP: func(wire []byte, maxExpandedBytes int) ([]byte, func(), error) {
				decoded, err := decodeTestGZIP(wire, maxExpandedBytes)
				return decoded, func() { releases++ }, err
			},
		})
		if !errors.Is(err, ErrProfileConflict) {
			t.Fatalf("admit error = %v", err)
		}
		if releases != 1 {
			t.Fatalf("release count = %d", releases)
		}
		if !bytes.Equal(body.Raw(), original) {
			t.Fatal("conflicting compressed selector consumed input")
		}
	})
}

func TestSparseNestedGZIPSharesExpansionBudgetAndReleasesLIFO(t *testing.T) {
	var terminal bin.Buffer
	putInvokeWithLayer(&terminal, 228)
	terminal.PutID(tg.HelpGetConfigRequestTypeID)
	var inner bin.Buffer
	putTestGZIP(t, &inner, terminal.Raw())
	var body bin.Buffer
	putTestGZIP(t, &body, inner.Raw())

	maxWireBytes := len(inner.Raw()) + len(terminal.Raw()) + 64
	var callbackLimits []int
	var releaseOrder []int
	admitted, err := NewDispatcher().AdmitUnprofiledWithOptions(&body, AdmissionOptions{
		Limits: Limits{MaxWireBytes: maxWireBytes},
		ExpandGZIP: func(wire []byte, maxExpandedBytes int) ([]byte, func(), error) {
			callbackLimits = append(callbackLimits, maxExpandedBytes)
			index := len(callbackLimits)
			decoded, err := decodeTestGZIP(wire, maxExpandedBytes)
			return decoded, func() { releaseOrder = append(releaseOrder, index) }, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Call().Profile() != Profile228 {
		t.Fatalf("call profile = %d", admitted.Call().Profile())
	}
	wantLimits := []int{maxWireBytes, maxWireBytes - len(inner.Raw())}
	if len(callbackLimits) != len(wantLimits) || callbackLimits[0] != wantLimits[0] || callbackLimits[1] != wantLimits[1] {
		t.Fatalf("callback limits = %v, want %v", callbackLimits, wantLimits)
	}
	if len(releaseOrder) != 2 || releaseOrder[0] != 2 || releaseOrder[1] != 1 {
		t.Fatalf("release order = %v, want [2 1]", releaseOrder)
	}
}

func TestSparseGZIPTerminalAdmission(t *testing.T) {
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			var terminal bin.Buffer
			terminal.PutID(tg.HelpGetConfigRequestTypeID)

			var body bin.Buffer
			putInvokeWithLayer(&body, int(profile))
			putTestInitConnection(&body)
			putTestGZIP(t, &body, terminal.Raw())
			original := body.Copy()

			var expansions, releases int
			options := AdmissionOptions{ExpandGZIP: func(wire []byte, maxExpandedBytes int) ([]byte, func(), error) {
				expansions++
				expanded, err := decodeTestGZIP(wire, maxExpandedBytes)
				return expanded, func() { releases++ }, err
			}}
			admitted, err := NewDispatcher().AdmitUnprofiledWithOptions(&body, options)
			if err != nil {
				t.Fatal(err)
			}
			if expansions != 1 || releases != 1 {
				t.Fatalf("gzip expansion/release = %d/%d", expansions, releases)
			}
			if body.Len() != 0 {
				t.Fatalf("remaining body = %d", body.Len())
			}
			if admitted.Call().Profile() != profile || admitted.Call().Method() != SemanticMethodHelpGetConfig {
				t.Fatalf("terminal = profile %d method %#x", admitted.Call().Profile(), admitted.Call().Method())
			}
			if evidence, ok := admitted.ProfileEvidence(); !ok || evidence != profile {
				t.Fatalf("profile evidence = %d/%v", evidence, ok)
			}
			if admitted.WrapperCount() != 2 {
				t.Fatalf("wrapper count = %d", admitted.WrapperCount())
			}
			if got, want := admitted.Prepared().WireSize(), len(original); got != want {
				t.Fatalf("wire size = %d, want %d", got, want)
			}
			if got, want := admitted.Prepared().WireDigest(), sha256.Sum256(original); got != want {
				t.Fatalf("wire digest = %x, want %x", got, want)
			}

			var plain bin.Buffer
			putInvokeWithLayer(&plain, int(profile))
			putTestInitConnection(&plain)
			plain.PutID(tg.HelpGetConfigRequestTypeID)
			plainAdmission, err := NewDispatcher().AdmitUnprofiled(&plain, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if admitted.Prepared().Identity() == plainAdmission.Prepared().Identity() {
				t.Fatal("compressed and plain requests share exact wire identity")
			}
			if admitted.Prepared().SemanticIdentity() != plainAdmission.Prepared().SemanticIdentity() {
				t.Fatal("compressed and plain requests differ in semantic identity")
			}
		})
	}
}

func TestSparseWrappedGZIPPreservesFieldPreflightDepth(t *testing.T) {
	sentinel := errors.New("wrapped field rejected")
	dispatcher := NewDispatcher()
	fieldCalls := 0
	if err := dispatcher.OnFieldPreflight(FieldUploadSaveBigFilePartBytes, func(view FieldView) error {
		fieldCalls++
		length, ok := view.BytesLength()
		if !ok || length != 5 || !view.Present() || view.Profile() != Profile228 {
			t.Fatalf("wrapped field = length:%d ok:%v present:%v profile:%d", length, ok, view.Present(), view.Profile())
		}
		return sentinel
	}); err != nil {
		t.Fatal(err)
	}

	var terminal bin.Buffer
	if err := EncodeObject(Profile228, &tg.UploadSaveBigFilePartRequest{
		FileID: 9, FilePart: 1, FileTotalParts: 3, Bytes: []byte("hello"),
	}, &terminal); err != nil {
		t.Fatal(err)
	}
	var body bin.Buffer
	putInvokeWithLayer(&body, 228)
	putTestInitConnection(&body)
	putTestGZIP(t, &body, terminal.Raw())
	original := body.Copy()
	releases := 0

	_, err := dispatcher.AdmitUnprofiledWithOptions(&body, AdmissionOptions{
		ExpandGZIP: func(wire []byte, maxExpandedBytes int) ([]byte, func(), error) {
			decoded, err := decodeTestGZIP(wire, maxExpandedBytes)
			return decoded, func() { releases++ }, err
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("admit error = %v", err)
	}
	if fieldCalls != 1 {
		t.Fatalf("field callback calls = %d", fieldCalls)
	}
	if releases != 1 {
		t.Fatalf("release count = %d", releases)
	}
	if !bytes.Equal(body.Raw(), original) {
		t.Fatal("field rejection consumed compressed input")
	}
}

func TestSparseGZIPTerminalFailureIsTransactional(t *testing.T) {
	t.Run("missing expander", func(t *testing.T) {
		body := testWrappedGZIPBody(t, Profile228, []byte{0x55, 0xaa, 0x00, 0x7f})
		original := body.Copy()
		_, err := NewDispatcher().AdmitUnprofiled(&body, Limits{})
		if !errors.Is(err, ErrGZIPExpanderMissing) {
			t.Fatalf("admit error = %v", err)
		}
		if !bytes.Equal(body.Raw(), original) {
			t.Fatal("missing-expander rejection consumed input")
		}
	})

	t.Run("expander error releases", func(t *testing.T) {
		sentinel := errors.New("test gzip failure")
		body := testWrappedGZIPBody(t, Profile228, []byte{0x55, 0xaa, 0x00, 0x7f})
		original := body.Copy()
		releases := 0
		_, err := NewDispatcher().AdmitUnprofiledWithOptions(&body, AdmissionOptions{
			ExpandGZIP: func([]byte, int) ([]byte, func(), error) {
				return nil, func() { releases++ }, sentinel
			},
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("admit error = %v", err)
		}
		if releases != 1 {
			t.Fatalf("release count = %d", releases)
		}
		if !bytes.Equal(body.Raw(), original) {
			t.Fatal("failed expansion consumed input")
		}
	})

	t.Run("oversized expansion releases", func(t *testing.T) {
		body := testWrappedGZIPBody(t, Profile228, []byte{0x55, 0xaa, 0x00, 0x7f})
		original := body.Copy()
		maxWireBytes := len(original) + 32
		releases := 0
		_, err := NewDispatcher().AdmitUnprofiledWithOptions(&body, AdmissionOptions{
			Limits: Limits{MaxWireBytes: maxWireBytes},
			ExpandGZIP: func(_ []byte, maxExpandedBytes int) ([]byte, func(), error) {
				expanded := make([]byte, maxExpandedBytes+bin.Word)
				return expanded, func() { releases++ }, nil
			},
		})
		if err == nil || !bytes.Contains([]byte(err.Error()), []byte("exceeds current limit")) {
			t.Fatalf("admit error = %v", err)
		}
		if releases != 1 {
			t.Fatalf("release count = %d", releases)
		}
		if !bytes.Equal(body.Raw(), original) {
			t.Fatal("oversized expansion consumed input")
		}
	})
}

func TestSparseNestedGZIPCountsTowardDepthAndPreservesUnknownTerminal(t *testing.T) {
	const unknown = uint32(0xd1435160)
	var terminal bin.Buffer
	terminal.PutID(unknown)
	var inner bin.Buffer
	putTestGZIP(t, &inner, terminal.Raw())

	body := testWrappedGZIPBody(t, Profile228, inner.Raw())
	original := body.Copy()
	var releases int
	options := AdmissionOptions{
		Limits: Limits{MaxDepth: 5},
		ExpandGZIP: func(wire []byte, maxExpandedBytes int) ([]byte, func(), error) {
			expanded, err := decodeTestGZIP(wire, maxExpandedBytes)
			return expanded, func() { releases++ }, err
		},
	}
	_, err := NewDispatcher().AdmitUnprofiledWithOptions(&body, options)
	if !errors.Is(err, ErrUnknownRPCMethod) {
		t.Fatalf("admit error = %v", err)
	}
	var terminalErr *UnknownTerminalError
	if !errors.As(err, &terminalErr) {
		t.Fatalf("admit error type = %T", err)
	}
	if terminalErr.WireID != unknown || terminalErr.WireSize != bin.Word || terminalErr.WrapperCount() != 2 {
		t.Fatalf("unknown terminal = %+v wrappers=%d", terminalErr, terminalErr.WrapperCount())
	}
	if releases != 2 {
		t.Fatalf("release count = %d", releases)
	}
	if !bytes.Equal(body.Raw(), original) {
		t.Fatal("unknown terminal rejection consumed input")
	}

	var knownTerminal bin.Buffer
	knownTerminal.PutID(tg.HelpGetConfigRequestTypeID)
	var knownInner bin.Buffer
	putTestGZIP(t, &knownInner, knownTerminal.Raw())
	depthBody := testWrappedGZIPBody(t, Profile228, knownInner.Raw())
	depthOriginal := depthBody.Copy()
	releases = 0
	_, err = NewDispatcher().AdmitUnprofiledWithOptions(&depthBody, AdmissionOptions{
		Limits: Limits{MaxDepth: 4},
		ExpandGZIP: func(wire []byte, maxExpandedBytes int) ([]byte, func(), error) {
			expanded, err := decodeTestGZIP(wire, maxExpandedBytes)
			return expanded, func() { releases++ }, err
		},
	})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("maximum scan depth 4 exceeded")) {
		t.Fatalf("depth error = %v", err)
	}
	if releases != 2 {
		t.Fatalf("depth release count = %d", releases)
	}
	if !bytes.Equal(depthBody.Raw(), depthOriginal) {
		t.Fatal("depth rejection consumed input")
	}
}

func TestSparseGZIPReleaseDoesNotInvalidateMaterializedRequest(t *testing.T) {
	var expanded bin.Buffer
	putTestInitConnection(&expanded)
	expanded.PutID(tg.AccountCheckUsernameRequestTypeID)
	expanded.PutString("alice")

	var body bin.Buffer
	putInvokeWithLayer(&body, 228)
	putTestGZIP(t, &body, expanded.Raw())

	releases := 0
	dispatcher := NewDispatcher()
	if err := dispatcher.Register(SemanticMethodAccountCheckUsername, func(_ context.Context, request bin.Object) (any, error) {
		typed, ok := request.(*tg.AccountCheckUsernameRequest)
		if !ok || typed.Username != "alice" {
			t.Fatalf("materialized request after release = %#v", request)
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	dispatcher.OnWrappers(func(ctx context.Context, _ Admission, next Next) error {
		return next(ctx)
	})
	admitted, err := dispatcher.AdmitUnprofiledWithOptions(&body, AdmissionOptions{
		ExpandGZIP: func(wire []byte, maxExpandedBytes int) ([]byte, func(), error) {
			data, err := decodeTestGZIP(wire, maxExpandedBytes)
			return data, func() {
				for i := range data {
					data[i] = 0xff
				}
				releases++
			}, err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("release count = %d", releases)
	}
	initWrapper, ok := admitted.Wrapper(1)
	if !ok {
		t.Fatal("missing initConnection wrapper")
	}
	device, present, found, err := initWrapper.Value("device_model")
	if err != nil || !present || !found || device != "device" {
		t.Fatalf("materialized wrapper after release = %#v/%v/%v/%v", device, present, found, err)
	}
	if _, err := dispatcher.Dispatch(context.Background(), admitted); err != nil {
		t.Fatal(err)
	}
}

func int64SliceBytes(values []int64) []byte {
	var b bin.Buffer
	for _, value := range values {
		b.PutLong(value)
	}
	return b.Copy()
}

func putInvokeWithLayer(body *bin.Buffer, layer int) {
	body.PutID(tg.InvokeWithLayerRequestTypeID)
	body.PutInt(layer)
}

func putTestInvokeAfterMsgs(body *bin.Buffer, ids []int64) {
	body.PutID(tg.InvokeAfterMsgsRequestTypeID)
	body.PutVectorHeader(len(ids))
	for _, id := range ids {
		body.PutLong(id)
	}
}

func putTestInitConnection(body *bin.Buffer) {
	body.PutID(tg.InitConnectionRequestTypeID)
	body.PutUint32(0)
	body.PutInt(1)
	for _, value := range []string{"device", "system", "app", "en", "", "en"} {
		body.PutString(value)
	}
}

func putTestInitConnectionWithParams(t *testing.T, body *bin.Buffer, params tg.JSONValueClass) {
	t.Helper()
	body.PutID(tg.InitConnectionRequestTypeID)
	body.PutUint32(1 << 1)
	body.PutInt(1)
	for _, value := range []string{"device", "system", "app", "en", "", "en"} {
		body.PutString(value)
	}
	if err := params.Encode(body); err != nil {
		t.Fatal(err)
	}
}

func putTestGZIP(t *testing.T, body *bin.Buffer, data []byte) {
	t.Helper()
	if err := (proto.GZIP{Data: data}).Encode(body); err != nil {
		t.Fatal(err)
	}
}

func testWrappedGZIPBody(t *testing.T, profile Profile, terminal []byte) bin.Buffer {
	t.Helper()
	var body bin.Buffer
	putInvokeWithLayer(&body, int(profile))
	putTestInitConnection(&body)
	putTestGZIP(t, &body, terminal)
	return body
}

func decodeTestGZIP(wire []byte, maxExpandedBytes int) ([]byte, error) {
	cursor := &bin.Buffer{Buf: append([]byte(nil), wire...)}
	var packed proto.GZIP
	if err := packed.Decode(cursor); err != nil {
		return nil, err
	}
	if cursor.Len() != 0 {
		return nil, fmt.Errorf("test gzip left %d bytes", cursor.Len())
	}
	if len(packed.Data) > maxExpandedBytes {
		return nil, fmt.Errorf("test gzip expansion %d exceeds %d", len(packed.Data), maxExpandedBytes)
	}
	return packed.Data, nil
}
