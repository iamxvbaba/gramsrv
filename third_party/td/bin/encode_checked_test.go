package bin

import (
	"errors"
	"strings"
	"testing"
)

func TestBufferCheckedTLLengthRollback(t *testing.T) {
	oversizedString := strings.Repeat("x", maxLongStringLength+1)
	buffer := &Buffer{Buf: []byte{0xaa}}
	err := buffer.PutStringChecked(oversizedString)
	var invalid *InvalidLengthError
	if !errors.As(err, &invalid) || invalid.Where != "string" || invalid.Length != len(oversizedString) {
		t.Fatalf("PutStringChecked error = %#v", err)
	}
	if got := buffer.Buf; len(got) != 1 || got[0] != 0xaa {
		t.Fatalf("PutStringChecked changed buffer: %x", got)
	}

	oversizedBytes := make([]byte, maxLongStringLength+1)
	err = buffer.PutBytesChecked(oversizedBytes)
	invalid = nil
	if !errors.As(err, &invalid) || invalid.Where != "bytes" || invalid.Length != len(oversizedBytes) {
		t.Fatalf("PutBytesChecked error = %#v", err)
	}
	if got := buffer.Buf; len(got) != 1 || got[0] != 0xaa {
		t.Fatalf("PutBytesChecked changed buffer: %x", got)
	}
}
