package layerwire

import (
	"errors"
	"math"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestValidateCanonicalRequestFlaggedHotPathAllocatesNothing(t *testing.T) {
	var body bin.Buffer
	req := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerSelf{},
		Message:  "hello",
		RandomID: 7,
	}
	if err := req.Encode(&body); err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if err := ValidateCanonicalRequest(body.Buf); err != nil {
		t.Fatalf("validate request: %v", err)
	}
	if allocs := testing.AllocsPerRun(1000, func() {
		if err := ValidateCanonicalRequest(body.Buf); err != nil {
			panic(err)
		}
	}); allocs != 0 {
		t.Fatalf("canonical request preflight allocations = %.2f, want 0", allocs)
	}
}

func TestValidateCanonicalRequestVectorLimits(t *testing.T) {
	editCloseFriends := canonical.byName["contacts.editCloseFriends"]
	if editCloseFriends == nil {
		t.Fatal("contacts.editCloseFriends missing from canonical schema")
	}

	t.Run("explicit_5000_override", func(t *testing.T) {
		var body bin.Buffer
		body.PutID(editCloseFriends.crc)
		body.PutVectorHeader(5000)
		for i := 0; i < 5000; i++ {
			body.PutLong(int64(i))
		}
		if err := ValidateCanonicalRequest(body.Buf); err != nil {
			t.Fatalf("validate legal 5000-element close-friends request: %v", err)
		}
	})

	t.Run("override_stops_at_5000", func(t *testing.T) {
		var body bin.Buffer
		body.PutID(editCloseFriends.crc)
		body.PutVectorHeader(5001)
		err := ValidateCanonicalRequest(body.Buf)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error = %v, want ErrResourceLimit", err)
		}
	})

	t.Run("default_4096", func(t *testing.T) {
		getMessages := canonical.byName["messages.getMessages"]
		var body bin.Buffer
		body.PutID(getMessages.crc)
		body.PutVectorHeader(defaultMaxVectorElements + 1)
		err := ValidateCanonicalRequest(body.Buf)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error = %v, want ErrResourceLimit", err)
		}
	})

	t.Run("max_int32_count_rejected_before_iteration", func(t *testing.T) {
		var body bin.Buffer
		body.PutID(editCloseFriends.crc)
		body.PutID(vectorTypeID)
		body.PutInt32(math.MaxInt32)
		err := ValidateCanonicalRequest(body.Buf)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error = %v, want ErrResourceLimit", err)
		}
	})
}

func TestValidateCanonicalRequestDepthLimit(t *testing.T) {
	invoke := canonical.byName["invokeWithoutUpdates"]
	leaf := canonical.byName["help.getConfig"]
	if invoke == nil || leaf == nil {
		t.Fatal("generic wrapper methods missing from canonical schema")
	}
	request := func(wrappers int) []byte {
		var body bin.Buffer
		for i := 0; i < wrappers; i++ {
			body.PutID(invoke.crc)
		}
		body.PutID(leaf.crc)
		return body.Buf
	}
	if err := ValidateCanonicalRequest(request(defaultMaxWalkDepth - 1)); err != nil {
		t.Fatalf("depth exactly %d rejected: %v", defaultMaxWalkDepth, err)
	}
	err := ValidateCanonicalRequest(request(defaultMaxWalkDepth))
	if !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("depth %d error = %v, want ErrResourceLimit", defaultMaxWalkDepth+1, err)
	}
}

func TestTLBytesSkipIsZeroCopyAndBounded(t *testing.T) {
	var encoded bin.Buffer
	encoded.PutBytes([]byte("payload"))
	fieldLen := len(encoded.Buf)
	raw := append(encoded.Copy(), 0xaa, 0xbb, 0xcc, 0xdd)
	b := &bin.Buffer{Buf: raw}
	walk := newWalkState()
	if err := walk.skipTLBytes(b, "bytes"); err != nil {
		t.Fatalf("skip bytes: %v", err)
	}
	if len(b.Buf) != 4 || &b.Buf[0] != &raw[fieldLen] {
		t.Fatalf("walker did not retain the original backing buffer")
	}

	t.Run("per_field_budget", func(t *testing.T) {
		limited := newWalkState()
		limited.limits.maxFieldBytes = 3
		probe := &bin.Buffer{Buf: encoded.Copy()}
		err := limited.skipTLBytes(probe, "bytes")
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error = %v, want ErrResourceLimit", err)
		}
	})

	t.Run("aggregate_budget", func(t *testing.T) {
		limited := newWalkState()
		limited.limits.maxTotalBytes = 10
		first := &bin.Buffer{Buf: encoded.Copy()}
		if err := limited.skipTLBytes(first, "bytes"); err != nil {
			t.Fatalf("first field: %v", err)
		}
		second := &bin.Buffer{Buf: encoded.Copy()}
		err := limited.skipTLBytes(second, "bytes")
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("second error = %v, want ErrResourceLimit", err)
		}
	})

	t.Run("truncated_payload_is_malformed", func(t *testing.T) {
		importAuth := canonical.byName["auth.importAuthorization"]
		var body bin.Buffer
		body.PutID(importAuth.crc)
		body.PutLong(1)
		body.Put([]byte{5, 'a', 'b'}) // declares five bytes, lacks payload/padding
		err := ValidateCanonicalRequest(body.Buf)
		if !errors.Is(err, ErrMalformed) || errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error = %v, want only ErrMalformed", err)
		}
	})
}

func TestInboundTransformsShareWalkerBudgets(t *testing.T) {
	t.Run("canonical_alias", func(t *testing.T) {
		var body bin.Buffer
		body.PutID(0x41d41ade) // DrKLO messages.forwardMessages alias
		body.PutUint32(0)
		body.PutID(canonical.byName["inputPeerEmpty"].crc)
		body.PutID(vectorTypeID)
		body.PutInt32(math.MaxInt32)
		_, ok, err := UpgradeInbound(0x41d41ade, &body)
		if !ok || !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("ok=%v error=%v, want matched ErrResourceLimit", ok, err)
		}
	})

	t.Run("drift_body_transform", func(t *testing.T) {
		var body bin.Buffer
		body.PutID(0x2e1ee318) // DrKLO langpack.getStrings body transform
		body.PutString("en")
		body.PutID(vectorTypeID)
		body.PutInt32(math.MaxInt32)
		_, ok, err := UpgradeInbound(0x2e1ee318, &body)
		if !ok || !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("ok=%v error=%v, want matched ErrResourceLimit", ok, err)
		}
	})

	t.Run("outbound_structural_transform", func(t *testing.T) {
		poll := canonical.byName["pollAnswerVoters"]
		var body bin.Buffer
		body.PutID(poll.crc)
		body.PutUint32(1 << 2)
		body.PutBytes(nil)
		body.PutInt(1)
		body.PutID(vectorTypeID)
		body.PutInt32(math.MaxInt32)
		_, err := Transcode(body.Buf, CanonicalLayer-1)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error = %v, want ErrResourceLimit", err)
		}
	})
}

func TestWalkerArithmeticAndMalformedClassification(t *testing.T) {
	if defaultMaxWalkUnits != 131072 {
		t.Fatalf("default constructor/vector budget = %d, want 131072", defaultMaxWalkUnits)
	}
	if _, ok := checkedMulInt(math.MaxInt, 2); ok {
		t.Fatal("checkedMulInt accepted overflow")
	}
	if _, ok := checkedAddUint64(math.MaxUint64, 1); ok {
		t.Fatal("checkedAddUint64 accepted overflow")
	}

	t.Run("aggregate_constructor_and_vector_units", func(t *testing.T) {
		editCloseFriends := canonical.byName["contacts.editCloseFriends"]
		var body bin.Buffer
		body.PutID(editCloseFriends.crc)
		body.PutVectorHeader(4)
		for i := 0; i < 4; i++ {
			body.PutLong(int64(i))
		}
		walk := newWalkState()
		walk.limits.maxUnits = 4 // top constructor + four elements needs five
		probe := &bin.Buffer{Buf: body.Buf}
		err := walk.skipObject(canonical, probe, 1)
		if !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("error = %v, want ErrResourceLimit", err)
		}
	})

	tests := []struct {
		name string
		body []byte
	}{
		{name: "empty"},
		{name: "unknown_constructor", body: []byte{1, 2, 3, 4}},
		{name: "trailing_bytes", body: append(methodIDBytes(canonical.byName["help.getConfig"].crc), 0, 0, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCanonicalRequest(tt.body)
			if !errors.Is(err, ErrMalformed) || errors.Is(err, ErrResourceLimit) {
				t.Fatalf("error = %v, want only ErrMalformed", err)
			}
		})
	}
}

func methodIDBytes(id uint32) []byte {
	var b bin.Buffer
	b.PutID(id)
	return b.Buf
}

func FuzzValidateCanonicalRequest(f *testing.F) {
	f.Add(methodIDBytes(canonical.byName["help.getConfig"].crc))
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4})
	f.Fuzz(func(t *testing.T, body []byte) {
		err := ValidateCanonicalRequest(body)
		if err != nil && !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("unclassified walker error: %v", err)
		}
	})
}
