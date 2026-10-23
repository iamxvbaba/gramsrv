package tlprofile

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

var admissionAllocationSink Admission

func TestSparseAdmissionPreflightAndFieldMetrics(t *testing.T) {
	profiles := []Profile{Profile225, Profile226, Profile227, Profile228}
	for _, profile := range profiles {
		t.Run(fmt.Sprintf("%d", profile), func(t *testing.T) {
			d := NewDispatcher()
			var order []string
			d.OnAdmissionPreflight(func(view AdmissionView) error {
				order = append(order, "raw")
				if view.Profile() != profile || view.Semantic() != SemanticMethodUploadSaveBigFilePart || view.WireID() == 0 || view.WireSize() == 0 {
					t.Fatalf("unexpected admission view: profile=%d semantic=%#x wire=%#x size=%d", view.Profile(), view.Semantic(), view.WireID(), view.WireSize())
				}
				wireID, err := view.Uint32At(0)
				if err != nil || wireID != view.WireID() {
					t.Fatalf("wire ID = %#x, %v", wireID, err)
				}
				prefix, err := view.ReadAt(0, 4)
				if err != nil || len(prefix) != 4 {
					t.Fatalf("prefix = %x, %v", prefix, err)
				}
				prefix[0] ^= 0xff
				actual, err := view.ByteAt(0)
				if err != nil || actual != byte(wireID) {
					t.Fatalf("immutable prefix byte = %#x, %v", actual, err)
				}
				return nil
			})
			if err := d.OnFieldPreflight(FieldUploadSaveBigFilePartFileTotalParts, func(view FieldView) error {
				order = append(order, "parts")
				value, ok := view.Int32()
				if !ok || value != 3 || !view.Present() {
					t.Fatalf("parts = %d, %v, present=%v", value, ok, view.Present())
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := d.OnFieldPreflight(FieldUploadSaveBigFilePartBytes, func(view FieldView) error {
				order = append(order, "bytes")
				length, ok := view.BytesLength()
				if !ok || length != 5 || !view.Present() {
					t.Fatalf("bytes = %d, %v, present=%v", length, ok, view.Present())
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			var body bin.Buffer
			if err := EncodeObject(profile, &tg.UploadSaveBigFilePartRequest{FileID: 9, FilePart: 1, FileTotalParts: 3, Bytes: []byte("hello")}, &body); err != nil {
				t.Fatal(err)
			}
			if _, err := d.Admit(profile, &body, Limits{}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal([]byte("raw,parts,bytes"), []byte(joinTestOrder(order))) {
				t.Fatalf("callback order = %v", order)
			}
		})
	}
}

func TestSparseAdmissionReportsAbsentConditionalField(t *testing.T) {
	d := NewDispatcher()
	calls := 0
	if err := d.OnFieldPreflight(FieldContactsGetLocatedSelfExpires, func(view FieldView) error {
		calls++
		if view.Present() {
			t.Fatal("self_expires unexpectedly present")
		}
		if _, ok := view.Int32(); ok {
			t.Fatal("absent int32 exposed a value")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var body bin.Buffer
	if err := EncodeObject(Profile225, &tg.ContactsGetLocatedRequest{GeoPoint: &tg.InputGeoPointEmpty{}}, &body); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admit(Profile225, &body, Limits{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("absent callback calls = %d", calls)
	}
}

func TestSparseFieldPreflightRejectsBeforeConsumption(t *testing.T) {
	sentinel := errors.New("field rejected")
	d := NewDispatcher()
	if err := d.OnFieldPreflight(FieldUsersGetUsersID, func(view FieldView) error {
		length, ok := view.VectorLength()
		if !ok || length != 1 {
			t.Fatalf("vector length = %d, %v", length, ok)
		}
		return sentinel
	}); err != nil {
		t.Fatal(err)
	}
	var body bin.Buffer
	if err := EncodeObject(Profile228, &tg.UsersGetUsersRequest{ID: []tg.InputUserClass{&tg.InputUserSelf{}}}, &body); err != nil {
		t.Fatal(err)
	}
	original := append([]byte(nil), body.Raw()...)
	if _, err := d.Admit(Profile228, &body, Limits{}); !errors.Is(err, sentinel) {
		t.Fatalf("admit error = %v", err)
	}
	if !bytes.Equal(original, body.Raw()) {
		t.Fatal("rejected admission consumed or mutated input")
	}
}

func TestSparseAdmissionAllocationBudget(t *testing.T) {
	var encoded bin.Buffer
	if err := EncodeObject(Profile225, &tg.HelpGetConfigRequest{}, &encoded); err != nil {
		t.Fatal(err)
	}
	wire := encoded.Copy()
	dispatcher := NewDispatcher()
	var admissionErr error
	allocations := testing.AllocsPerRun(1000, func() {
		input := bin.Buffer{Buf: wire}
		admissionAllocationSink, admissionErr = dispatcher.Admit(Profile225, &input, Limits{})
	})
	if admissionErr != nil {
		t.Fatal(admissionErr)
	}
	if allocations > 2 {
		t.Fatalf("direct sparse admission allocations = %.2f, want <= 2", allocations)
	}
}

func TestSparseDirectAdmissionReusesWireCanonicalIdentity(t *testing.T) {
	var encoded bin.Buffer
	request := &tg.AccountUpdateProfileRequest{FirstName: "Alice", About: "direct route with flags"}
	if err := EncodeObject(Profile225, request, &encoded); err != nil {
		t.Fatal(err)
	}
	wire := encoded.Copy()
	admission, err := NewDispatcher().Admit(Profile225, &encoded, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	identity := admission.Prepared().SemanticIdentity()
	if identity.CanonicalSize() != len(wire) {
		t.Fatalf("canonical size = %d, want %d", identity.CanonicalSize(), len(wire))
	}
	if digest := sha256.Sum256(wire); identity.CanonicalDigest() != digest {
		t.Fatalf("canonical digest = %x, want wire digest %x", identity.CanonicalDigest(), digest)
	}
}

func joinTestOrder(values []string) string {
	var out []byte
	for index, value := range values {
		if index != 0 {
			out = append(out, ',')
		}
		out = append(out, value...)
	}
	return string(out)
}
